package planner

import (
	"fmt"
	"strings"
)

// defaultPlanner Planner 的默认实现。
// 基于规则从 findings 构建 plan（确定性的输入→输出映射）。
// 完整 LLM 实现将在 P1 后期由 orchestrator 通过 Spawn 启动独立 Planner session 完成。
type defaultPlanner struct{}

// NewPlanner 创建 Planner 实例。
func NewPlanner() Planner {
	return &defaultPlanner{}
}

// INSTRUMENT: planner-build-plan — 基于 findings 构建修复计划，生成 PlanOutput
// LAYER: L1
// STATUS: implemented
// BuildPlan 基于规则从 findings 构建 plan。
//
// 规则：
//  1. Goal 从 findings[0].Hypothesis 提取（取 confidence 最高的 confirmed finding）
//  2. Steps 从每个 confirmed finding 生成一个 step（包含 action/target/approach）
//  3. Discarded hypotheses 转为 alternatives（标记 discarded=true）
//  4. 生成通用的 pre/post checklist
func (p *defaultPlanner) BuildPlan(input PlannerInput) (*PlannerOutput, error) {
	if len(input.Findings) == 0 {
		return nil, fmt.Errorf("no findings to build plan from")
	}

	// 取 confidence 最高的 confirmed finding 作为 goal
	best := pickBestFinding(input.Findings)
	if best == nil {
		return nil, fmt.Errorf("no confirmed finding with confidence > 0")
	}

	plan := &PlanJSON{
		Version: "1.0",
		Goal:    best.Hypothesis,
	}

	// 从每个 finding 生成一个 step
	for i, f := range input.Findings {
		step := findingToStep(i+1, f)
		plan.Steps = append(plan.Steps, step)
	}

	// discarded hypotheses → alternatives
	for _, dh := range input.DiscardedHypotheses {
		plan.Alternatives = append(plan.Alternatives, Alternative{
			Approach:  dh.Hypothesis,
			Tradeoff:  "insufficient evidence or contradicted by signals",
			Discarded: true,
		})
	}

	// 通用 checklists
	plan.PreChecklist = []string{
		"确认当前分支干净（git status clean）",
		"确认有最新的 main 分支代码",
		"阅读相关代码确认修改范围",
	}
	plan.PostChecklist = []string{
		"go build 通过",
		"go vet 通过",
		"golangci-lint 通过",
		"关联测试通过",
	}

	// 计算整体置信度（所有 confirmed findings 的平均 confidence）
	confidence := 0.0
	count := 0
	for _, f := range input.Findings {
		if f.Status == "confirmed" {
			confidence += f.ConfidenceSelf
			count++
		}
	}
	if count > 0 {
		confidence /= float64(count)
	}

	if err := ValidatePlan(plan); err != nil {
		return nil, fmt.Errorf("generated plan validation failed: %w", err)
	}

	return &PlannerOutput{
		Plan:       *plan,
		Confidence: confidence,
	}, nil
}

// BuildPromptInput 构建 plan-system.tmpl 模板的变量 map。
func (p *defaultPlanner) BuildPromptInput(input PlannerInput) map[string]any {
	return map[string]any{
		"Findings":            input.Findings,
		"DiscardedHypotheses": input.DiscardedHypotheses,
		"ParentSummary":       input.ParentSummary,
		"UserDirectives":      input.UserDirectives,
		"AvailableSkills":     input.AvailableSkills,
	}
}

// —————— 辅助函数 ——————

// pickBestFinding 选置信度最高的 confirmed finding。
func pickBestFinding(findings []Finding) *Finding {
	var best *Finding
	for i := range findings {
		f := &findings[i]
		if f.Status != "confirmed" {
			continue
		}
		if best == nil || f.ConfidenceSelf > best.ConfidenceSelf {
			best = f
		}
	}
	return best
}

// findingToStep 将一个 finding 转换为 PlanStep。
func findingToStep(index int, f Finding) PlanStep {
	id := fmt.Sprintf("step-%d", index)
	action := inferAction(f.Hypothesis)
	target := inferTarget(f.Hypothesis)
	approach := f.Hypothesis

	impact := ""
	if f.ConfidenceSelf >= 0.8 {
		impact = "high confidence fix"
	} else if f.ConfidenceSelf >= 0.5 {
		impact = "moderate confidence fix"
	} else {
		impact = "needs further investigation"
	}

	return PlanStep{
		ID:              id,
		Action:          action,
		Target:          target,
		Approach:        approach,
		EstimatedImpact: impact,
		Risk:            inferRisk(f.ConfidenceSelf),
		Rollback:        inferRollback(action),
		ConfidenceSelf:  f.ConfidenceSelf,
	}
}

// inferAction 基于 hypothesis 文本推测 action 类型。
func inferAction(hypothesis string) string {
	h := strings.ToLower(hypothesis)
	switch {
	case strings.Contains(h, "慢查询") || strings.Contains(h, "索引") || strings.Contains(h, "慢"):
		return "优化数据库查询性能"
	case strings.Contains(h, "超时"):
		return "修复超时问题"
	case strings.Contains(h, "null") || strings.Contains(h, "nil") || strings.Contains(h, "空指针"):
		return "修复空指针异常"
	case strings.Contains(h, "泄漏") || strings.Contains(h, "内存"):
		return "修复资源泄漏"
	case strings.Contains(h, "配置") || strings.Contains(h, "config"):
		return "修正配置项"
	case strings.Contains(h, "权限") || strings.Contains(h, "auth"):
		return "修复权限问题"
	default:
		return "修复代码问题"
	}
}

// inferTarget 基于 hypothesis 文本推测目标文件/模块。
func inferTarget(hypothesis string) string {
	h := strings.ToLower(hypothesis)
	parts := strings.Fields(h)
	for _, part := range parts {
		if strings.Contains(part, "-svc") || strings.Contains(part, "-service") {
			return part + "/internal/"
		}
	}
	return "待定位"
}

// inferRisk 基于 confidence 推断风险等级。
func inferRisk(confidence float64) string {
	switch {
	case confidence >= 0.8:
		return "low"
	case confidence >= 0.6:
		return "medium"
	default:
		return "high"
	}
}

// inferRollback 基于 action 推断回滚方式。
func inferRollback(action string) string {
	if strings.Contains(action, "数据库") || strings.Contains(action, "索引") {
		return "回滚 migration"
	}
	if strings.Contains(action, "配置") {
		return "恢复原配置文件"
	}
	return "git revert 对应 commit"
}
