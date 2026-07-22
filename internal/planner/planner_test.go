package planner

import (
	"strings"
	"testing"

	"git.qingteng.cn/ms/ainspection/internal/prompt"
)

// —————— Validate 测试 ——————

func makeValidPlan() *PlanJSON {
	return &PlanJSON{
		Version: "1.0",
		Goal:    "修复 payment-svc 慢查询导致 upstream 超时",
		Steps: []PlanStep{
			{
				ID: "step-1", Action: "添加联合索引",
				Target:          "migration/002_add_idx.sql",
				Approach:        "CREATE INDEX idx_payments_status_created ON payments(status, created_at)",
				EstimatedImpact: "P99 从 2.5s 降至 <200ms",
				Risk:            "low", Rollback: "DROP INDEX idx_payments_status_created",
			},
		},
		PreChecklist:  []string{"确认 status 列区分度 > 0.1"},
		PostChecklist: []string{"go build 通过"},
	}
}

func TestValidatePlanOK(t *testing.T) {
	plan := makeValidPlan()
	if err := ValidatePlan(plan); err != nil {
		t.Errorf("valid plan should pass: %v", err)
	}
}

func TestValidatePlanNil(t *testing.T) {
	if err := ValidatePlan(nil); err == nil {
		t.Error("nil plan should fail")
	}
}

func TestValidatePlanVersion(t *testing.T) {
	plan := makeValidPlan()
	plan.Version = "2.0"
	if err := ValidatePlan(plan); err == nil {
		t.Error("version != 1.0 should fail")
	}
}

func TestValidatePlanEmptyGoal(t *testing.T) {
	plan := makeValidPlan()
	plan.Goal = ""
	if err := ValidatePlan(plan); err == nil {
		t.Error("empty goal should fail")
	}
}

func TestValidatePlanWhitespaceGoal(t *testing.T) {
	plan := makeValidPlan()
	plan.Goal = "   "
	if err := ValidatePlan(plan); err == nil {
		t.Error("whitespace-only goal should fail")
	}
}

func TestValidatePlanEmptySteps(t *testing.T) {
	plan := makeValidPlan()
	plan.Steps = nil
	if err := ValidatePlan(plan); err == nil {
		t.Error("empty steps should fail")
	}
}

func TestValidateStepMissingID(t *testing.T) {
	plan := makeValidPlan()
	plan.Steps[0].ID = ""
	err := ValidatePlan(plan)
	if err == nil {
		t.Error("step missing id should fail")
	}
	if err != nil && !strings.Contains(err.Error(), "id is required") {
		t.Errorf("expected 'id is required', got: %v", err)
	}
}

func TestValidateStepMissingAction(t *testing.T) {
	plan := makeValidPlan()
	plan.Steps[0].Action = ""
	if err := ValidatePlan(plan); err == nil {
		t.Error("step missing action should fail")
	}
}

func TestValidateStepMissingTarget(t *testing.T) {
	plan := makeValidPlan()
	plan.Steps[0].Target = ""
	if err := ValidatePlan(plan); err == nil {
		t.Error("step missing target should fail")
	}
}

func TestValidateStepMissingApproach(t *testing.T) {
	plan := makeValidPlan()
	plan.Steps[0].Approach = ""
	if err := ValidatePlan(plan); err == nil {
		t.Error("step missing approach should fail")
	}
}

func TestValidateStepMissingRisk(t *testing.T) {
	plan := makeValidPlan()
	plan.Steps[0].Risk = ""
	if err := ValidatePlan(plan); err == nil {
		t.Error("step missing risk should fail")
	}
}

func TestValidateStepInvalidRisk(t *testing.T) {
	plan := makeValidPlan()
	plan.Steps[0].Risk = "dangerous"
	if err := ValidatePlan(plan); err == nil {
		t.Error("invalid risk should fail")
	}
}

func TestValidateStepValidRisks(t *testing.T) {
	for _, risk := range []string{"low", "medium", "high", "critical", "LOW", "Medium"} {
		plan := makeValidPlan()
		plan.Steps[0].Risk = risk
		if err := ValidatePlan(plan); err != nil {
			t.Errorf("risk %q should be valid: %v", risk, err)
		}
	}
}

func TestValidateAlternativeEmptyApproach(t *testing.T) {
	plan := makeValidPlan()
	plan.Alternatives = []Alternative{{Approach: "", Tradeoff: "bad"}}
	if err := ValidatePlan(plan); err == nil {
		t.Error("alternative with empty approach should fail")
	}
}

func TestValidateMultipleSteps(t *testing.T) {
	plan := makeValidPlan()
	plan.Steps = append(plan.Steps, PlanStep{
		ID: "step-2", Action: "验证索引", Target: "EXPLAIN query",
		Approach: "EXPLAIN SELECT", Risk: "low",
	})
	if err := ValidatePlan(plan); err != nil {
		t.Errorf("multi-step plan should pass: %v", err)
	}
}

func TestValidatePlanForFixNoPreChecklist(t *testing.T) {
	plan := makeValidPlan()
	plan.PreChecklist = nil
	if err := ValidatePlanForFix(plan); err == nil {
		t.Error("nil pre_checklist should fail ValidatePlanForFix")
	}
}

func TestValidatePlanForFixOK(t *testing.T) {
	plan := makeValidPlan()
	if err := ValidatePlanForFix(plan); err != nil {
		t.Errorf("valid plan should pass fix validation: %v", err)
	}
}

// —————— BuildPlan 测试 ——————

func TestBuildPlan(t *testing.T) {
	p := NewPlanner()
	input := PlannerInput{
		Findings: []Finding{
			{
				Hypothesis:     "payment-svc 慢查询导致 upstream 超时",
				ConfidenceSelf: 0.92,
				Evidence:       []string{"traces/slow-span.txt"},
				Status:         "confirmed",
			},
			{
				Hypothesis:     "order-svc 配置项缺失导致启动失败",
				ConfidenceSelf: 0.65,
				Evidence:       []string{"logs/config-error.txt"},
				Status:         "confirmed",
			},
		},
		DiscardedHypotheses: []Finding{
			{Hypothesis: "网络抖动", Status: "discarded"},
		},
		ParentSummary: "payment-svc 出现超时",
	}

	output, err := p.BuildPlan(input)
	if err != nil {
		t.Fatalf("BuildPlan: %v", err)
	}

	if output.Plan.Version != "1.0" {
		t.Errorf("expected version 1.0, got %s", output.Plan.Version)
	}
	if output.Plan.Goal != "payment-svc 慢查询导致 upstream 超时" {
		t.Errorf("expected goal from best finding, got %s", output.Plan.Goal)
	}
	if len(output.Plan.Steps) != 2 {
		t.Errorf("expected 2 steps, got %d", len(output.Plan.Steps))
	}

	// 步骤应有 id/action
	for i, step := range output.Plan.Steps {
		if step.ID == "" {
			t.Errorf("step %d has empty id", i)
		}
		if step.Action == "" {
			t.Errorf("step %d has empty action", i)
		}
	}

	// Alternatives 应包含 discarded hypotheses
	if len(output.Plan.Alternatives) != 1 {
		t.Errorf("expected 1 alternative (discarded), got %d", len(output.Plan.Alternatives))
	}
	if !output.Plan.Alternatives[0].Discarded {
		t.Error("discarded alternative should be marked discarded=true")
	}

	// Checklists 应非空
	if len(output.Plan.PreChecklist) == 0 {
		t.Error("pre_checklist is empty")
	}
	if len(output.Plan.PostChecklist) == 0 {
		t.Error("post_checklist is empty")
	}

	// Confidence 应为confirmed findings 的平均
	if output.Confidence < 0.7 || output.Confidence > 0.9 {
		t.Errorf("expected confidence around 0.78, got %.2f", output.Confidence)
	}
}

func TestBuildPlanNoFindings(t *testing.T) {
	p := NewPlanner()
	_, err := p.BuildPlan(PlannerInput{Findings: nil})
	if err == nil {
		t.Error("expected error for empty findings")
	}
}

func TestBuildPlanOnlyUnconfirmed(t *testing.T) {
	p := NewPlanner()
	input := PlannerInput{
		Findings: []Finding{
			{Hypothesis: "可能的原因", ConfidenceSelf: 0.3, Status: "investigating"},
		},
	}
	_, err := p.BuildPlan(input)
	if err == nil {
		t.Error("expected error: no confirmed finding to pick as goal")
	}
}

func TestBuildPlanInferredFields(t *testing.T) {
	p := NewPlanner()
	input := PlannerInput{
		Findings: []Finding{
			{Hypothesis: "慢查询导致超时", ConfidenceSelf: 0.85, Status: "confirmed", Evidence: []string{"traces/slow.txt"}},
		},
	}

	output, err := p.BuildPlan(input)
	if err != nil {
		t.Fatalf("BuildPlan: %v", err)
	}

	step := output.Plan.Steps[0]

	// Action 应被推断
	if !strings.Contains(step.Action, "性能") && !strings.Contains(step.Action, "超时") {
		t.Errorf("action should be inferred from hypothesis, got '%s'", step.Action)
	}

	// Risk 应从 confidence 推断
	if step.Risk != "low" {
		t.Errorf("risk should be 'low' for confidence 0.85, got '%s'", step.Risk)
	}

	// Confidence 应保持
	if step.ConfidenceSelf != 0.85 {
		t.Errorf("confidence should be 0.85, got %.2f", step.ConfidenceSelf)
	}
}

func TestBuildPlanRiskInference(t *testing.T) {
	p := NewPlanner()

	tests := []struct {
		confidence float64
		risk       string
	}{
		{0.95, "low"},
		{0.8, "low"},
		{0.7, "medium"},
		{0.6, "medium"},
		{0.4, "high"},
	}

	for _, tc := range tests {
		input := PlannerInput{
			Findings: []Finding{
				{Hypothesis: "test", ConfidenceSelf: tc.confidence, Status: "confirmed"},
			},
		}
		output, err := p.BuildPlan(input)
		if err != nil {
			t.Fatalf("BuildPlan for conf=%.2f: %v", tc.confidence, err)
		}
		if output.Plan.Steps[0].Risk != tc.risk {
			t.Errorf("conf=%.2f: expected risk=%s, got %s", tc.confidence, tc.risk, output.Plan.Steps[0].Risk)
		}
	}
}

// —————— BuildSystemPrompt 测试（走 Renderer） ——————

// mockRenderer 用于测试的 mock Renderer。
type mockRenderer struct {
	rendered string
}

func (m *mockRenderer) Render(name string, data any) (string, error) {
	// 返回一个包含 data 摘要的字符串
	input, ok := data.(prompt.PlanInput)
	if ok {
		m.rendered = input.ParentSummary
		return "rendered: " + input.ParentSummary, nil
	}
	return "rendered", nil
}

func (m *mockRenderer) RenderToBytes(name string, data any) ([]byte, error) {
	s, err := m.Render(name, data)
	return []byte(s), err
}

func TestBuildSystemPromptWithRenderer(t *testing.T) {
	input := PlannerInput{
		Findings: []Finding{
			{Hypothesis: "慢查询", ConfidenceSelf: 0.9, Status: "confirmed"},
		},
		ParentSummary: "payment-svc 超时",
	}

	renderer := &mockRenderer{}
	result, err := BuildSystemPrompt(renderer, input)
	if err != nil {
		t.Fatalf("BuildSystemPrompt: %v", err)
	}
	if result == "" {
		t.Error("rendered prompt is empty")
	}
	if !strings.Contains(result, "payment-svc") {
		t.Errorf("rendered content should contain parent summary, got: %s", result)
	}
	if renderer.rendered != "payment-svc 超时" {
		t.Errorf("renderer should receive parent summary, got: %s", renderer.rendered)
	}
}

func TestBuildSystemPromptWithFindings(t *testing.T) {
	input := PlannerInput{
		Findings: []Finding{
			{Hypothesis: "慢查询", ConfidenceSelf: 0.9, Status: "confirmed", Evidence: []string{"trace1"}},
		},
	}

	renderer := &mockRenderer{}
	result, err := BuildSystemPrompt(renderer, input)
	if err != nil {
		t.Fatalf("BuildSystemPrompt: %v", err)
	}
	if result == "" {
		t.Error("rendered prompt is empty")
	}
}

// —————— JSON 序列化往返测试 ——————

func TestPlanJSONRoundTrip(t *testing.T) {
	plan := makeValidPlan()

	// 验证 plan 可以用 ValidatePlan 校验
	if err := ValidatePlan(plan); err != nil {
		t.Fatalf("round-trip plan should be valid: %v", err)
	}

	// 验证 PlanOutput 包含有效 plan
	output := &PlannerOutput{Plan: *plan, Confidence: 0.85}
	if output.Plan.Version != "1.0" {
		t.Error("version mismatch")
	}
}
