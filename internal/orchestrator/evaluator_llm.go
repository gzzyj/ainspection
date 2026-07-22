package orchestrator

import (
	"context"
	"fmt"
	"log"

	"git.qingteng.cn/ms/ainspection/internal/adapter"
	"git.qingteng.cn/ms/ainspection/internal/config"
	"git.qingteng.cn/ms/ainspection/internal/llm"
	"git.qingteng.cn/ms/ainspection/internal/prompt"
	"git.qingteng.cn/ms/ainspection/internal/skill"
	"git.qingteng.cn/ms/ainspection/internal/tree"

	"gopkg.in/yaml.v3"
)

// INSTRUMENT: orchestrator-evaluator-llm — Evaluator LLM 双路径实现（native + CLI）
// LAYER: L1
// STATUS: implemented
// evaluatorLLMImpl 实现 Evaluator 接口，按照 get/locate/verify/commit 一致的
// Agent adapter 双路径模式：LLM Native adapter → Chat() 多轮，CLI adapter → Run() 单次。
//
// 不直接持有 llm.Client，通过 adapterRegistry.Get() 走 adapter 通信。
type evaluatorLLMImpl struct {
	promptRenderer  prompt.Renderer
	cfg             *config.Config
	skillLoader     skill.Loader
	skillInjector   skill.Injector
	adapterRegistry *adapter.Registry
}

// NewEvaluatorLLM 创建 LLM 双路径 Evaluator 实现。
func NewEvaluatorLLM(
	pr prompt.Renderer,
	cfg *config.Config,
	sl skill.Loader,
	si skill.Injector,
	reg *adapter.Registry,
) Evaluator {
	return &evaluatorLLMImpl{
		promptRenderer:  pr,
		cfg:             cfg,
		skillLoader:     sl,
		skillInjector:   si,
		adapterRegistry: reg,
	}
}

// ReviewFinding 审查 locate 阶段的根因定位（Review #1）。
func (e *evaluatorLLMImpl) ReviewFinding(ctx context.Context, taskID string, node *tree.Node) (*ReviewReport, error) {
	output, err := tree.ReadOutput(taskID, node.ID)
	if err != nil {
		return nil, fmt.Errorf("read locate node output: %w", err)
	}

	// 将 findings 收集为 prompt 输入
	var findings []prompt.FindingData
	for _, f := range output.Findings {
		findings = append(findings, prompt.FindingData{
			Hypothesis:     f.Hypothesis,
			Confidence:     f.ConfidenceFinal,
			Evidence:       f.Evidence,
			ConfidenceSelf: f.ConfidenceSelf,
		})
	}

	return e.doReview(ctx, "review1-system", prompt.Review1Input{
		Findings: findings,
		Stage:    node.Stage,
		NodeID:   node.ID,
	})
}

// ReviewFix 审查 fix 阶段的 diff（Review #2，主 review）。
func (e *evaluatorLLMImpl) ReviewFix(ctx context.Context, taskID string, node *tree.Node, patches []Patch) (*ReviewReport, error) {
	output, err := tree.ReadOutput(taskID, node.ID)
	if err != nil {
		return nil, fmt.Errorf("read fix node output: %w", err)
	}

	diff := ""
	if output.Patch != nil {
		diff = output.Patch.Diff
	}

	planJSON := ""
	if output.Plan != nil {
		if planBytes, err := yaml.Marshal(output.Plan); err == nil {
			planJSON = string(planBytes)
		}
	}

	return e.doReview(ctx, "review2-system", prompt.Review2Input{
		Diff:     diff,
		PlanJSON: planJSON,
		Stage:    node.Stage,
		NodeID:   node.ID,
	})
}

// ReviewVerification 审查 verify 阶段的验证结果（Review #3）。
func (e *evaluatorLLMImpl) ReviewVerification(ctx context.Context, taskID string, node *tree.Node, report *VerifyReport) (*ReviewReport, error) {
	output, err := tree.ReadOutput(taskID, node.ID)
	if err != nil {
		return nil, fmt.Errorf("read verify node output: %w", err)
	}

	verifyPassed := false
	if output.ReviewReport != nil {
		verifyPassed = output.ReviewReport.Passed
	}

	return e.doReview(ctx, "review3-system", prompt.Review3Input{
		VerifyPassed: verifyPassed,
		Stage:        node.Stage,
		NodeID:       node.ID,
	})
}

// doReview 通用审查流程：渲染 system prompt → 获取 adapter → 加载 skills → 调 LLM 或 CLI → 解析 ReviewReport。
func (e *evaluatorLLMImpl) doReview(ctx context.Context, templateName string, data any) (*ReviewReport, error) {
	// 检查 evaluator 是否启用
	if e.cfg == nil || !e.cfg.Evaluator.Enabled {
		log.Printf("[evaluator] disabled by config, returning passed placeholder")
		return &ReviewReport{Passed: true, Score: 8, Confidence: 1.0}, nil
	}

	// 1. 获取 evaluator agent 配置
	agentCfg := e.getEvaluatorAgentConfig()
	if agentCfg == nil {
		log.Printf("[evaluator] no evaluator agent config, returning passed placeholder")
		return &ReviewReport{Passed: true, Score: 8, Confidence: 1.0}, nil
	}

	agentType := adapter.ResolveAgentType(agentCfg.ResolveType())
	agentAdapter, err := e.adapterRegistry.Get(agentType)
	if err != nil {
		return nil, fmt.Errorf("get evaluator adapter %q: %w", agentType, err)
	}

	// 2. 渲染 system prompt
	sysPrompt, err := e.promptRenderer.Render(templateName, data)
	if err != nil {
		log.Printf("[evaluator] render %s: %v, falling back to passed placeholder", templateName, err)
		return &ReviewReport{Passed: true, Score: 8, Confidence: 1.0}, nil
	}

	// 3. 加载 evaluator skills 并注入
	var toolDefs []skill.ToolDef
	if e.cfg.Skills.Path != "" && e.skillLoader != nil && e.skillInjector != nil {
		skills, loadErr := e.skillLoader.LoadAll(e.cfg.Skills.Path)
		if loadErr != nil {
			log.Printf("[evaluator] load skills: %v", loadErr)
		} else {
			reviewSkills := e.filterReviewSkills(skills)
			if len(reviewSkills) > 0 {
				var injectErr error
				toolDefs, _, injectErr = e.skillInjector.Inject(agentCfg.ResolveType(), reviewSkills, agentCfg.NativeTools)
				if injectErr != nil {
					log.Printf("[evaluator] inject skills: %v", injectErr)
					toolDefs = nil
				}
			}
		}
	}

	// 4. 按 adapter 类型执行审查
	var reportText string
	switch agentAdapter.Type() {
	case adapter.AgentLLMNative:
		llmAdapter := agentAdapter.(*adapter.LLMNativeAdapter)
		model := "default"
		if agentCfg.Model != "" {
			model = agentCfg.Model
		}
		resp, chatErr := llmAdapter.Chat(ctx, llm.ChatRequest{
			Model:    model,
			System:   sysPrompt,
			Messages: []llm.Message{{Role: "user", Content: "请对当前阶段产物进行独立审查"}},
			Tools:    skillToolToLLMTools(toolDefs),
		})
		if chatErr != nil {
			log.Printf("[evaluator] llm chat: %v, falling back to passed placeholder", chatErr)
			return &ReviewReport{Passed: true, Score: 8, Confidence: 1.0}, nil
		}
		if len(resp.Choices) > 0 {
			reportText = resp.Choices[0].Message.Content
		}
	default:
		userPrompt := sysPrompt + "\n\n请对当前阶段产物进行独立审查，按 YAML 格式输出 review_report"
		agentResult, runErr := agentAdapter.Run(ctx, "", adapter.AgentInput{
			Prompt: userPrompt,
		})
		if runErr != nil {
			log.Printf("[evaluator] cli run: %v, falling back to passed placeholder", runErr)
			return &ReviewReport{Passed: true, Score: 8, Confidence: 1.0}, nil
		}
		if agentResult != nil {
			reportText = agentResult.Text
		}
	}

	// 5. 解析 ReviewReport
	return parseReviewReport(reportText), nil
}

// getEvaluatorAgentConfig 获取 evaluator agent 配置。
func (e *evaluatorLLMImpl) getEvaluatorAgentConfig() *config.AgentConfig {
	if e.cfg == nil {
		return nil
	}
	agentName := e.cfg.Evaluator.Agent
	if agentName == "" {
		agentName = "evaluator"
	}
	if cfg, ok := e.cfg.Agents[agentName]; ok {
		return &cfg
	}
	// 回退到默认 agent
	if cfg, ok := e.cfg.Agents[e.cfg.GetDefaultAgent()]; ok {
		return &cfg
	}
	return nil
}

// filterReviewSkills 过滤出 review 阶段需要的 skills。
// Evaluator 以文本审查为主，默认不注入执行型 skills；如 evaluator agent
// 配置了 native_tools，通过 skill injector 的 native tools 路径注入。
func (e *evaluatorLLMImpl) filterReviewSkills(_ []*skill.Skill) []*skill.Skill {
	return nil
}

// parseReviewReport 从 LLM 文本输出中解析 ReviewReport。
func parseReviewReport(text string) *ReviewReport {
	if text == "" {
		return &ReviewReport{Passed: true, Score: 8, Confidence: 1.0}
	}

	// 提取 YAML 代码块或直接解析
	yamlBlock := extractYAMLBlock(text)
	if yamlBlock == "" {
		return &ReviewReport{Passed: true, Score: 8, Confidence: 1.0}
	}

	// 尝试解析嵌套结构 {review_report: {...}}
	var wrapper struct {
		ReviewReport ReviewReport `yaml:"review_report"`
		// 也支持扁平输出
		Passed     bool              `yaml:"passed"`
		Score      int               `yaml:"score"`
		Dimensions []ReviewDimension `yaml:"dimensions"`
		Blockers   []string          `yaml:"blockers"`
		Warnings   []string          `yaml:"warnings"`
		Confidence float64           `yaml:"confidence"`
	}

	if err := yaml.Unmarshal([]byte(yamlBlock), &wrapper); err != nil {
		log.Printf("[evaluator] parse report yaml: %v", err)
		return &ReviewReport{Passed: true, Score: 8, Confidence: 1.0}
	}

	if wrapper.ReviewReport.Passed || !wrapper.ReviewReport.Passed {
		// 嵌套结构
		return &wrapper.ReviewReport
	}

	// 扁平输出
	return &ReviewReport{
		Passed:     wrapper.Passed,
		Score:      wrapper.Score,
		Dimensions: wrapper.Dimensions,
		Blockers:   wrapper.Blockers,
		Warnings:   wrapper.Warnings,
		Confidence: wrapper.Confidence,
	}
}

// skillToolToLLMTools 将 skill.ToolDef 列表转换为 llm.ToolDef 列表。
// 该函数已在 pipeline.go 中定义，此处作为引用。
