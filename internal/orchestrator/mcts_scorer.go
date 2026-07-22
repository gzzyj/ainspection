package orchestrator

import (
	"context"
	"encoding/json"
	"fmt"

	"git.qingteng.cn/ms/ainspection/internal/llm"
	"git.qingteng.cn/ms/ainspection/internal/mcts"
	"git.qingteng.cn/ms/ainspection/internal/prompt"
)

// DefaultScoreMaxTokens MCTS 评分器默认 max tokens。
const DefaultScoreMaxTokens = 512

// DefaultScoreTemperature MCTS 评分器默认温度。
const DefaultScoreTemperature = 0.1

// mctsScorerImpl 基于 LLM 的 MCTS 评分器实现。
//
// 实现 evaluator.go 中声明的 MCTSScorer 接口。每个评分请求调 LLM 进行一次
// 四维评分，各维度加权求和得到 0-1 分数。
//
// 可选注入 RolloutSimulator：当不为 nil 时，单次评分升级为多步 Rollout 评分。
type mctsScorerImpl struct {
	renderer        prompt.Renderer
	llmClient       llm.Client
	modelName       string             // LLM 模型名（可注入，避免硬编码）
	scoreMaxTokens  int                // 评分 LLM max tokens
	scoreTemperature float64           // 评分 LLM 温度
	rolloutSim      *RolloutSimulator // 可选，升级为 rollout 评分
}

// NewMCTSScorer 创建 LLM 驱动的 MCTS 评分器（使用默认参数）。
func NewMCTSScorer(renderer prompt.Renderer, llmClient llm.Client, modelName string) *mctsScorerImpl {
	return &mctsScorerImpl{
		renderer:         renderer,
		llmClient:        llmClient,
		modelName:        modelName,
		scoreMaxTokens:   DefaultScoreMaxTokens,
		scoreTemperature: DefaultScoreTemperature,
	}
}

// WithScoreParams 设置 LLM 评分参数（max tokens 和 temperature）。
func (s *mctsScorerImpl) WithScoreParams(maxTokens int, temperature float64) *mctsScorerImpl {
	if maxTokens > 0 {
		s.scoreMaxTokens = maxTokens
	}
	if temperature > 0 {
		s.scoreTemperature = temperature
	}
	return s
}

// WithRollout 注入 RolloutSimulator，将单次评分升级为多步 Rollout 评分。
func (s *mctsScorerImpl) WithRollout(sim *RolloutSimulator) *mctsScorerImpl {
	s.rolloutSim = sim
	return s
}

// ScoreLocate 对 locate 阶段假设进行四维评分。
//
// 四维加权：
//   - 根因正确性 (0-0.35): 假设与症状的因果链条是否合理
//   - 证据充分度 (0-0.30): 支撑假设的证据数量和质量
//   - 可验证性 (0-0.20): 假设是否能被静态分析或动态探测验证
//   - 影响面 (0-0.15): 假设覆盖的影响范围
func (s *mctsScorerImpl) ScoreLocate(ctx context.Context, h HypothesisForScore) (float64, error) {
	score, _, err := s.scoreLocateWithDims(ctx, h)
	return score, err
}

// scoreLocateWithDims 执行评分并返回维度分（内部方法，供 ToLocateScorer 使用）。
func (s *mctsScorerImpl) scoreLocateWithDims(ctx context.Context, h HypothesisForScore) (float64, map[string]float64, error) {
	// 无 LLM 客户端时回退到规则式
	if s.llmClient == nil {
		return ruleLocateScoreFromHypothesis(h), nil, nil
	}

	promptText := buildLocateScorePrompt(h)
	resp, err := s.llmClient.Chat(ctx, llm.ChatRequest{
		Model:       s.modelName,
		System:      locateScorerSystemPrompt(),
		Temperature: s.scoreTemperature,
		MaxTokens:   s.scoreMaxTokens,
		Messages: []llm.Message{
			{Role: "user", Content: promptText},
		},
	})
	if err != nil {
		// LLM 失败回退到规则式评分
		return ruleLocateScoreFromHypothesis(h), nil, nil
	}

	score, dims := parseDetailedScoreResponse(resp)
	return computeWeightedAggregate(dims, "locate", score), dims, nil
}

// ScoreFix 对 fix 阶段修复方案进行四维评分。
//
// 四维加权：
//   - 修复完备性 (0-0.35): 是否完全解决根因，无遗漏
//   - 代码质量 (0-0.30): diff 的清晰度、最小改动原则
//   - 架构合规 (0-0.20): 是否符合项目规范和最佳实践
//   - 安全性 (0-0.15): 是否引入新的安全风险
func (s *mctsScorerImpl) ScoreFix(ctx context.Context, step PlanStepForScore, candidate DiffForScore) (float64, error) {
	score, _, err := s.scoreFixWithDims(ctx, step, candidate)
	return score, err
}

// scoreFixWithDims 执行评分并返回维度分（内部方法，供 ToFixScorer 使用）。
func (s *mctsScorerImpl) scoreFixWithDims(ctx context.Context, step PlanStepForScore, candidate DiffForScore) (float64, map[string]float64, error) {
	if s.llmClient == nil {
		return ruleFixScoreFromStep(step, candidate), nil, nil
	}

	promptText := buildFixScorePrompt(step, candidate)
	resp, err := s.llmClient.Chat(ctx, llm.ChatRequest{
		Model:       s.modelName,
		System:      fixScorerSystemPrompt(),
		Temperature: s.scoreTemperature,
		MaxTokens:   s.scoreMaxTokens,
		Messages: []llm.Message{
			{Role: "user", Content: promptText},
		},
	})
	if err != nil {
		return ruleFixScoreFromStep(step, candidate), nil, nil
	}

	score, dims := parseDetailedScoreResponse(resp)
	return computeWeightedAggregate(dims, "fix", score), dims, nil
}

// ToLocateScorer 将 ScoreLocate 适配为 mcts.Scorer 函数类型。
//
// 若 rolloutSim 已注入，单次评分升级为多步 Rollout 累积奖励。
// LLM 可用时，通过 MakeLocateScorerWithDims 提取并存储维度分到节点。
func (s *mctsScorerImpl) ToLocateScorer() mcts.Scorer {
	if s.llmClient != nil {
		// LLM 可用：存储维度分到节点
		return mcts.MakeLocateScorerWithDims(func(ctx context.Context, h mcts.HypothesisPayload) (float64, mcts.DimensionScores, error) {
			baseScore, dims, err := s.scoreLocateWithDims(ctx, HypothesisForScore{
				Hypothesis: h.Hypothesis,
				Evidence:   h.Evidence,
			})
			if err != nil {
				return 0, mcts.DimensionScores{}, err
			}

			ds := mcts.DimensionScores{
				Dimensions: dims,
				Aggregate:  baseScore,
			}

			if s.rolloutSim != nil {
				result := s.rolloutSim.SimulateLocate(ctx, baseScore, len(h.Evidence), h.SourceCtx)
				ds.Aggregate = result.FinalScore
				return result.FinalScore, ds, nil
			}

			return baseScore, ds, nil
		})
	}

	// 无 LLM：回退到无维度分的简单评分
	return mcts.MakeLocateScorer(func(ctx context.Context, h mcts.HypothesisPayload) (float64, error) {
		baseScore, err := s.ScoreLocate(ctx, HypothesisForScore{
			Hypothesis: h.Hypothesis,
			Evidence:   h.Evidence,
		})
		if err != nil {
			return 0, err
		}

		if s.rolloutSim != nil {
			result := s.rolloutSim.SimulateLocate(ctx, baseScore, len(h.Evidence), h.SourceCtx)
			return result.FinalScore, nil
		}

		return baseScore, nil
	})
}

// ToFixScorer 将 ScoreFix 适配为 mcts.Scorer 函数类型。
//
// 若 rolloutSim 已注入，单次评分升级为多步 Rollout 累积奖励。
// LLM 可用时，通过 MakeFixScorerWithDims 提取并存储维度分到节点。
func (s *mctsScorerImpl) ToFixScorer() mcts.Scorer {
	if s.llmClient != nil {
		// LLM 可用：存储维度分到节点
		return mcts.MakeFixScorerWithDims(func(ctx context.Context, p mcts.PlanStepPayload) (float64, mcts.DimensionScores, error) {
			baseScore, dims, err := s.scoreFixWithDims(ctx, PlanStepForScore{
				StepID:   p.StepID,
				Action:   p.Action,
				Target:   p.Target,
				Approach: p.Approach,
			}, DiffForScore{
				Content:  []byte(p.Diff),
				FilePath: p.Target,
			})
			if err != nil {
				return 0, mcts.DimensionScores{}, err
			}

			ds := mcts.DimensionScores{
				Dimensions: dims,
				Aggregate:  baseScore,
			}

			if s.rolloutSim != nil {
				result := s.rolloutSim.SimulateFix(ctx, baseScore, len(p.Diff), nil, nil)
				ds.Aggregate = result.FinalScore
				return result.FinalScore, ds, nil
			}

			return baseScore, ds, nil
		})
	}

	// 无 LLM：回退到无维度分的简单评分
	return mcts.MakeFixScorer(func(ctx context.Context, p mcts.PlanStepPayload) (float64, error) {
		baseScore, err := s.ScoreFix(ctx, PlanStepForScore{
			StepID:   p.StepID,
			Action:   p.Action,
			Target:   p.Target,
			Approach: p.Approach,
		}, DiffForScore{
			Content:  []byte(p.Diff),
			FilePath: p.Target,
		})
		if err != nil {
			return 0, err
		}

		if s.rolloutSim != nil {
			result := s.rolloutSim.SimulateFix(ctx, baseScore, len(p.Diff), nil, nil)
			return result.FinalScore, nil
		}

		return baseScore, nil
	})
}

// —————— LLM prompt 构建 ——————

// locateScorerSystemPrompt 返回 locate 评分的 system prompt。
func locateScorerSystemPrompt() string {
	return `你是一个代码审查专家。对给定的根因假设进行四维评分（每维 0-1），返回加权总分。

评分维度：
1. 根因正确性 (权重 0.35): 假设与症状的因果链是否合理
2. 证据充分度 (权重 0.30): 支撑假设的证据是否充足
3. 可验证性 (权重 0.20): 假设能否被静态分析或动态探测验证
4. 影响面 (权重 0.15): 假设覆盖的影响范围是否准确

返回格式: {"score": 0.75, "dimensions": {"correctness": 0.8, "evidence": 0.7, "verifiability": 0.6, "impact": 0.9}}`
}

// buildLocateScorePrompt 构建 locate 评分的用户消息。
func buildLocateScorePrompt(h HypothesisForScore) string {
	evidence := ""
	for i, e := range h.Evidence {
		if i > 0 {
			evidence += ", "
		}
		evidence += e
	}
	return fmt.Sprintf("假设: %s\n证据: %s\n深度: %d\n\n请评分。", h.Hypothesis, evidence, h.Depth)
}

// fixScorerSystemPrompt 返回 fix 评分的 system prompt。
func fixScorerSystemPrompt() string {
	return `你是一个代码审查专家。对给定的修复方案进行四维评分（每维 0-1），返回加权总分。

评分维度：
1. 修复完备性 (权重 0.35): 是否完全解决根因
2. 代码质量 (权重 0.30): diff 清晰度、最小改动原则
3. 架构合规 (权重 0.20): 项目规范和最佳实践
4. 安全性 (权重 0.15): 是否引入新的安全风险

返回格式: {"score": 0.75, "dimensions": {"completeness": 0.8, "quality": 0.7, "compliance": 0.6, "security": 0.9}}`
}

// buildFixScorePrompt 构建 fix 评分的用户消息。
func buildFixScorePrompt(step PlanStepForScore, candidate DiffForScore) string {
	diffPreview := string(candidate.Content)
	if len(diffPreview) > 500 {
		diffPreview = diffPreview[:500]
	}
	return fmt.Sprintf(
		"步骤: %s\n操作: %s\n目标: %s\n方法: %s\nDiff:\n%s\n\n请评分。",
		step.StepID, step.Action, step.Target, step.Approach, diffPreview,
	)
}

// parseDetailedScoreResponse 从 LLM 响应中完整解析 score 和 dimensions。
//
// 解析 JSON 格式: {"score": 0.75, "dimensions": {"correctness": 0.8, ...}}
// 返回 (score, dimensions)。解析失败时返回 (0.5, nil)。
func parseDetailedScoreResponse(resp *llm.ChatResponse) (float64, map[string]float64) {
	if resp == nil || len(resp.Choices) == 0 {
		return 0.5, nil
	}

	content := resp.Choices[0].Message.Content

	// 尝试完整 JSON 解析
	var parsed struct {
		Score      float64            `json:"score"`
		Dimensions map[string]float64 `json:"dimensions"`
	}
	if err := json.Unmarshal([]byte(content), &parsed); err == nil {
		if parsed.Score > 0 {
			return clampScore(parsed.Score), parsed.Dimensions
		}
	}

	// 回退：从 markdown 代码块中提取 JSON
	jsonContent := extractJSONBlock(content)
	if jsonContent != content {
		if err := json.Unmarshal([]byte(jsonContent), &parsed); err == nil {
			if parsed.Score > 0 {
				return clampScore(parsed.Score), parsed.Dimensions
			}
		}
	}

	// 末级回退：简单字符串解析 score 字段
	score := parseScoreFallback(content)
	return score, nil
}

// parseScoreFallback 简单字符串解析 score 字段（末级回退）。
func parseScoreFallback(content string) float64 {
	if i := findString(content, `"score"`); i >= 0 {
		rest := content[i+7:]
		if j := findString(rest, ":"); j >= 0 {
			rest = rest[j+1:]
			var numStr string
			for _, ch := range rest {
				if (ch >= '0' && ch <= '9') || ch == '.' {
					numStr += string(ch)
				} else if len(numStr) > 0 {
					break
				}
			}
			if numStr != "" {
				var score float64
				fmt.Sscanf(numStr, "%f", &score)
				return clampScore(score)
			}
		}
	}
	return 0.5
}

// findString 查找子串的起始位置。
func findString(s, substr string) int {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return i
		}
	}
	return -1
}

// computeWeightedAggregate 按阶段权重对维度分加权求和。
//
// locate 权重: correctness=0.35, evidence=0.30, verifiability=0.20, impact=0.15
// fix 权重: completeness=0.35, quality=0.30, compliance=0.20, security=0.15
// 若 dims 为空，返回 fallbackScore。
func computeWeightedAggregate(dims map[string]float64, stage string, fallbackScore float64) float64 {
	if len(dims) == 0 {
		return fallbackScore
	}

	weights := locateWeights
	if stage == "fix" {
		weights = fixWeights
	}

	var total float64
	var weightSum float64
	for key, weight := range weights {
		if val, ok := dims[key]; ok {
			total += val * weight
			weightSum += weight
		}
	}

	if weightSum == 0 {
		return fallbackScore
	}

	return clampScore(total / weightSum * (weightSum / 1.0)) // 归一化
}

// locateWeights locate 阶段的维度权重。
var locateWeights = map[string]float64{
	"correctness":   0.35,
	"evidence":      0.30,
	"verifiability": 0.20,
	"impact":        0.15,
}

// fixWeights fix 阶段的维度权重。
var fixWeights = map[string]float64{
	"completeness": 0.35,
	"quality":      0.30,
	"compliance":   0.20,
	"security":     0.15,
}

// —————— 规则式回退评分 ——————

// ruleLocateScoreFromHypothesis 规则式评分（LLM 不可用时回退）。
func ruleLocateScoreFromHypothesis(h HypothesisForScore) float64 {
	score := 0.0

	// 长度分
	if len(h.Hypothesis) >= 20 {
		score += 0.2
	}
	if len(h.Hypothesis) >= 50 {
		score += 0.15
	}

	// 证据分
	score += float64(len(h.Evidence)) * 0.1
	if score > 0.35 {
		score = 0.35 + (score-0.35)*0.5
	}

	// 深度加分
	if h.Depth > 0 {
		score += 0.05 * float64(h.Depth)
	}

	return clampScore(score)
}

// ruleFixScoreFromStep 规则式评分（LLM 不可用时回退）。
func ruleFixScoreFromStep(step PlanStepForScore, candidate DiffForScore) float64 {
	score := 0.0

	if len(step.Approach) >= 20 {
		score += 0.2
	}
	if len(step.Approach) >= 50 {
		score += 0.15
	}

	if len(candidate.Content) > 0 {
		score += 0.3
	}

	if step.Target != "" {
		score += 0.15
	}

	if len(candidate.Content) > 100 {
		score += 0.1
	}

	return clampScore(score)
}
