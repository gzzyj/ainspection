// Package planner 提供 Planner Agent 的类型和计划生成逻辑（P1-3）。
//
// Planner 是独立 Agent + 独立 session，在 locate Evaluator review #1 通过后
// 由 orchestrator 通过 sessionMgr.Spawn 启动，将 locate 的 findings 转换为结构化 plan.json。
package planner

// —————— Plan JSON 结构 ——————

// PlanJSON 结构化计划（对应 output.yaml.plan 字段 + plan.json 文件）。
type PlanJSON struct {
	Version       string        `json:"version" yaml:"version"`
	Goal          string        `json:"goal" yaml:"goal"`
	Steps         []PlanStep    `json:"steps" yaml:"steps"`
	Alternatives  []Alternative `json:"alternatives,omitempty" yaml:"alternatives,omitempty"`
	PreChecklist  []string      `json:"pre_checklist,omitempty" yaml:"pre_checklist,omitempty"`
	PostChecklist []string      `json:"post_checklist,omitempty" yaml:"post_checklist,omitempty"`
}

// PlanStep 单个修复步骤。
type PlanStep struct {
	ID                  string  `json:"id" yaml:"id"`
	Action              string  `json:"action" yaml:"action"`
	Target              string  `json:"target" yaml:"target"`
	Approach            string  `json:"approach" yaml:"approach"`
	EstimatedImpact     string  `json:"estimated_impact" yaml:"estimated_impact"`
	Risk                string  `json:"risk" yaml:"risk"`
	Rollback            string  `json:"rollback,omitempty" yaml:"rollback,omitempty"`
	ConfidenceSelf      float64 `json:"confidence_self,omitempty" yaml:"confidence_self,omitempty"`
	ConfidenceEvaluator float64 `json:"confidence_evaluator,omitempty" yaml:"confidence_evaluator,omitempty"`
	ConfidenceFinal     float64 `json:"confidence_final,omitempty" yaml:"confidence_final,omitempty"`
}

// Alternative 备选方案。
type Alternative struct {
	Approach  string `json:"approach" yaml:"approach"`
	Tradeoff  string `json:"tradeoff" yaml:"tradeoff"`
	Discarded bool   `json:"discarded" yaml:"discarded"`
}

// —————— Planner 输入/输出 ——————

// Finding locate 阶段的根因发现。
type Finding struct {
	Hypothesis          string   `json:"hypothesis"`
	ConfidenceSelf      float64  `json:"confidence_self"`
	ConfidenceEvaluator float64  `json:"confidence_evaluator"`
	ConfidenceFinal     float64  `json:"confidence_final"`
	Evidence            []string `json:"evidence"`
	Status              string   `json:"status"` // confirmed | investigating
}

// PlannerInput Planner 的输入参数。
type PlannerInput struct {
	Findings            []Finding   `json:"findings"`             // locate 输出的确认 findings
	DiscardedHypotheses []Finding   `json:"discarded_hypotheses"` // 已排除假设
	ParentSummary       string      `json:"parent_summary"`       // 父节点 summary.md（≤500字）
	UserDirectives      []string    `json:"user_directives"`      // 用户指令
	AvailableSkills     []SkillDesc `json:"available_skills"`     // 当前可用 skill
}

// PlannerOutput Planner 的输出。
type PlannerOutput struct {
	Plan       PlanJSON `json:"plan"`       // 结构化计划
	Confidence float64  `json:"confidence"` // Planner 整体置信度
}

// SkillDesc skill 简要描述。
type SkillDesc struct {
	Name        string `json:"name"`
	Description string `json:"description"`
}

// —————— Planner 接口 ——————

// Planner 计划生成器接口。
//
// 实现方式：
//
//	P0/P1 默认实现：基于规则从 findings 构建 plan（确定性的输入→输出映射）。
//	完整 LLM 实现由 orchestrator 在 P1 后期通过 Spawn 启动独立 Planner session 完成。
type Planner interface {
	// BuildPlan 从 locate findings 生成结构化 plan.json。
	BuildPlan(input PlannerInput) (*PlannerOutput, error)

	// BuildPromptInput 构建 plan-system.tmpl 的模板变量。
	BuildPromptInput(input PlannerInput) map[string]any
}
