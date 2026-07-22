package orchestrator

import (
	"context"
	"fmt"
	"strings"

	"git.qingteng.cn/ms/ainspection/internal/config"
	"git.qingteng.cn/ms/ainspection/internal/mcts"
)

// mctsRunner 是 MCTSRunner 接口的具体实现，适配 mcts.Engine 到流水线。
//
// 持有两个独立的 mcts.Engine：locateEngine（假设空间搜索）和 fixEngine（修复方案空间搜索）。
// 每个 engine 通过注入规则式（Phase 1）或 LLM 驱动（Phase 2）的 expander/scorer 来完成 MCTS 四步循环。
type mctsRunner struct {
	locateEngine *mcts.Engine
	fixEngine    *mcts.Engine
}

// NewMCTSRunner 创建 MCTSRunner 实现，构造 locate 和 fix 两个引擎。
//
// Phase 1：使用规则式 expander/scorer（无需 LLM），使端到端链路可跑通。
// Phase 2：替换为 LLM 驱动的 expander/scorer。
func NewMCTSRunner(cfg *config.Config) MCTSRunner {
	mctsCfg := mcts.DefaultConfig()
	if cfg != nil {
		mctsCfg = mctsConfigFromApp(cfg.MCTS)
	}

	// locate engine：通过工厂函数创建规则式 expander/scorer
	locateEngine := mcts.NewEngine(
		mctsCfg.LocateBudget,
		mctsCfg.UCBC,
		mcts.MakeLocateExpander(ruleLocateExpand),
		mcts.MakeLocateScorer(ruleLocateScore),
	)

	// fix engine：通过工厂函数创建规则式 expander/scorer
	fixEngine := mcts.NewEngine(
		mctsCfg.FixBudget,
		mctsCfg.UCBC,
		mcts.MakeFixExpander(ruleFixExpand),
		mcts.MakeFixScorer(ruleFixScore),
	)

	return &mctsRunner{
		locateEngine: locateEngine,
		fixEngine:    fixEngine,
	}
}

// NewMCTSRunnerWithLLM 创建使用 LLM 驱动 expander/scorer 的 MCTSRunner（Phase 2）。
//
// 由 Phase 2 的 mcts_expander.go / mcts_scorer.go 提供 expander 和 scorer 实例。
func NewMCTSRunnerWithLLM(
	cfg *config.Config,
	locateExpander mcts.NodeExpander,
	locateScorer mcts.Scorer,
	fixExpander mcts.NodeExpander,
	fixScorer mcts.Scorer,
) MCTSRunner {
	mctsCfg := mcts.DefaultConfig()
	if cfg != nil {
		mctsCfg = mctsConfigFromApp(cfg.MCTS)
	}

	return &mctsRunner{
		locateEngine: mcts.NewEngine(mctsCfg.LocateBudget, mctsCfg.UCBC, locateExpander, locateScorer),
		fixEngine:    mcts.NewEngine(mctsCfg.FixBudget, mctsCfg.UCBC, fixExpander, fixScorer),
	}
}

// RunLocate 在假设空间执行 MCTS 搜索，返回 top-K 假设节点。
func (r *mctsRunner) RunLocate(ctx context.Context, input LocateInput) ([]LocateOutput, error) {
	// 从 input 构建 root 假设
	rootHypothesis := extractRootHypothesis(input)
	root := mcts.NewLocateRoot("locate-root", mcts.HypothesisPayload{
		Hypothesis: rootHypothesis,
		Evidence:   extractHints(input),
	})

	leaves, err := r.locateEngine.Run(ctx, root)
	if err != nil {
		return nil, fmt.Errorf("mcts locate run: %w", err)
	}

	// TopK 取前 3
	top := mcts.TopK(leaves, 3)
	outputs := make([]LocateOutput, 0, len(top))
	for _, leaf := range top {
		h, err := mcts.GetHypothesis(leaf)
		if err != nil {
			continue
		}
		outputs = append(outputs, LocateOutput{
			Hypothesis: h.Hypothesis,
			Confidence: leaf.AvgScore(),
			Evidence:   h.Evidence,
			Dimensions: leaf.DimensionScores().Dimensions,
		})
	}

	return outputs, nil
}

// RunFix 在修复方案空间执行 MCTS 搜索，返回 top-1 修复方案。
func (r *mctsRunner) RunFix(ctx context.Context, input FixInput) ([]FixOutput, error) {
	// 从 input 构建 root 修复步骤
	root := mcts.NewFixRoot("fix-root", mcts.PlanStepPayload{
		StepID:   "root-fix",
		Action:   "初始修复方案",
		Target:   extractTargetFromPlan(input.PlanJSON),
		Approach: "根据 plan.json 生成初始 diff",
		Diff:     "",
	})

	leaves, err := r.fixEngine.Run(ctx, root)
	if err != nil {
		return nil, fmt.Errorf("mcts fix run: %w", err)
	}

	// TopK 取前 1
	top := mcts.TopK(leaves, 1)
	outputs := make([]FixOutput, 0, len(top))
	for _, leaf := range top {
		p, err := mcts.GetPlanStep(leaf)
		if err != nil {
			continue
		}
		outputs = append(outputs, FixOutput{
			Diff:       []byte(p.Diff),
			Confidence: leaf.AvgScore(),
			StepID:     p.StepID,
			Dimensions: leaf.DimensionScores().Dimensions,
		})
	}

	return outputs, nil
}

// —————— 规则式 expander（Phase 1）——————
// 使用简化签名：context + payload + depth → children payloads

// ruleLocateExpand 基于关键词拆分的规则式假设展开。
func ruleLocateExpand(ctx context.Context, h mcts.HypothesisPayload, depth int) ([]mcts.HypothesisPayload, error) {
	keywords := splitKeywords(h.Hypothesis)
	if len(keywords) <= 1 {
		return nil, nil // 无法拆分，不展开
	}

	branchingFactor := 3
	if len(keywords) < branchingFactor {
		branchingFactor = len(keywords)
	}

	children := make([]mcts.HypothesisPayload, 0, branchingFactor)
	for i := 0; i < branchingFactor; i++ {
		children = append(children, mcts.HypothesisPayload{
			Hypothesis: fmt.Sprintf("%s → 子方向: %s", h.Hypothesis, keywords[i]),
			Evidence:   h.Evidence,
		})
	}

	return children, nil
}

// ruleFixExpand 基于步骤拆分的规则式修复方案展开。
func ruleFixExpand(ctx context.Context, p mcts.PlanStepPayload, depth int) ([]mcts.PlanStepPayload, error) {
	variants := generateApproachVariants(p.Approach)
	if len(variants) == 0 {
		return nil, nil
	}

	branchingFactor := 2
	if len(variants) < branchingFactor {
		branchingFactor = len(variants)
	}

	children := make([]mcts.PlanStepPayload, 0, branchingFactor)
	for i := 0; i < branchingFactor; i++ {
		children = append(children, mcts.PlanStepPayload{
			StepID:   fmt.Sprintf("%s-v%d", p.StepID, i+1),
			Action:   p.Action,
			Target:   p.Target,
			Approach: variants[i],
			Diff:     generateDiffForApproach(variants[i], p.Target),
		})
	}

	return children, nil
}

// —————— 规则式 scorer（Phase 1）——————
// 使用简化签名：context + payload → score

// ruleLocateScore 基于 hypothesis 质量的规则式评分。
func ruleLocateScore(ctx context.Context, h mcts.HypothesisPayload) (float64, error) {
	score := 0.0

	// 长度分：hypothesis 在 10-200 字符之间最优
	hypLen := len(h.Hypothesis)
	if hypLen >= 10 {
		score += 0.15
	}
	if hypLen >= 30 {
		score += 0.15
	}
	if hypLen > 200 {
		score -= 0.1
	}

	// 证据分：每条证据 +0.1
	score += float64(len(h.Evidence)) * 0.1
	if score > 0.4 {
		score = 0.4 + (score-0.4)*0.5
	}

	// 关键词分
	diagKeywords := []string{"导致", "超时", "空指针", "死锁", "内存泄漏", "panic", "nil", "竞态", "溢出", "重试"}
	kwCount := 0
	for _, kw := range diagKeywords {
		if strings.Contains(h.Hypothesis, kw) {
			kwCount++
		}
	}
	if kwCount >= 2 {
		score += 0.3
	} else if kwCount == 1 {
		score += 0.15
	}

	return clampScore(score), nil
}

// ruleFixScore 基于方案质量的规则式评分。
func ruleFixScore(ctx context.Context, p mcts.PlanStepPayload) (float64, error) {
	score := 0.0

	if len(p.Approach) >= 20 {
		score += 0.2
	}
	if len(p.Approach) >= 50 {
		score += 0.2
	}

	if len(p.Diff) > 0 {
		score += 0.2
		if strings.Contains(p.Diff, "+++") || strings.Contains(p.Diff, "---") {
			score += 0.2
		}
	}

	if p.Target != "" {
		score += 0.1
	}
	if len(p.Target) > 5 {
		score += 0.1
	}

	return clampScore(score), nil
}

// clampScore 将分数限制在 [0, 1] 区间。
func clampScore(score float64) float64 {
	if score > 1.0 {
		return 1.0
	}
	if score < 0.0 {
		return 0.0
	}
	return score
}

// —————— 配置转换 ——————

// mctsConfigFromApp 从应用配置构建 mcts.Config。
func mctsConfigFromApp(cfg config.MCTSConfig) mcts.Config {
	return mcts.Config{
		LocateBudget: mcts.Budget{
			MaxIterations:   cfg.Locate.MaxIterations,
			MaxDepth:        cfg.Locate.MaxDepth,
			BranchingFactor: cfg.Locate.BranchingFactor,
		},
		FixBudget: mcts.Budget{
			MaxIterations:   cfg.Fix.MaxIterations,
			MaxDepth:        cfg.Fix.MaxDepth,
			BranchingFactor: cfg.Fix.BranchingFactor,
		},
		UCBC: cfg.UCBC,
	}
}

// —————— 辅助函数 ——————

// extractRootHypothesis 从 LocateInput 提取根假设文本。
func extractRootHypothesis(input LocateInput) string {
	if len(input.InputYAML) > 0 {
		content := string(input.InputYAML)
		if len(content) > 200 {
			content = content[:200]
		}
		return "根因定位: " + content
	}
	return "根因定位: 从 input.yaml 分析"
}

// extractHints 从 LocateInput 提取用户提示。
func extractHints(input LocateInput) []string {
	if len(input.InputYAML) > 0 {
		return []string{"input.yaml"}
	}
	return input.Skills
}

// extractTargetFromPlan 从 PlanJSON 提取目标文件。
func extractTargetFromPlan(planJSON []byte) string {
	content := string(planJSON)
	if idx := strings.Index(content, `"target"`); idx >= 0 {
		rest := content[idx+8:]
		if start := strings.Index(rest, `"`); start >= 0 {
			rest = rest[start+1:]
			if end := strings.Index(rest, `"`); end >= 0 {
				return rest[:end]
			}
		}
	}
	return "unknown"
}

// splitKeywords 将 hypothesis 文本按语义分隔符拆分为关键词。
func splitKeywords(hypothesis string) []string {
	parts := strings.FieldsFunc(hypothesis, func(r rune) bool {
		return r == '→' || r == '>' || r == '-' || r == ',' || r == '，' || r == ';' || r == '；'
	})

	var keywords []string
	seen := make(map[string]bool)
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if len(p) >= 2 && !seen[p] {
			keywords = append(keywords, p)
			seen[p] = true
		}
	}

	return keywords
}

// generateApproachVariants 为给定的 approach 生成变体。
func generateApproachVariants(approach string) []string {
	return []string{
		approach,
		approach + "（优化版）",
		approach + "（保守版）",
	}
}

// generateDiffForApproach 为给定方法生成模拟 diff 内容。
func generateDiffForApproach(approach, target string) string {
	if target == "" {
		target = "target_file.go"
	}
	return fmt.Sprintf("--- a/%s\n+++ b/%s\n@@ -1,1 +1,1 @@\n-// old\n+// %s\n", target, target, approach)
}
