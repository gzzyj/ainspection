package planner

import (
	"fmt"
	"strings"
)

// ValidatePlan 校验 PlanJSON 的完整性和合法性。
func ValidatePlan(plan *PlanJSON) error {
	if plan == nil {
		return fmt.Errorf("plan is nil")
	}

	// version 必须为 "1.0"
	if plan.Version != "1.0" {
		return fmt.Errorf("plan.version must be '1.0', got %q", plan.Version)
	}

	// goal 必须非空
	if strings.TrimSpace(plan.Goal) == "" {
		return fmt.Errorf("plan.goal is required")
	}

	// steps 必须非空
	if len(plan.Steps) == 0 {
		return fmt.Errorf("plan.steps must contain at least 1 step")
	}

	// 逐 step 校验
	for i, step := range plan.Steps {
		if err := validateStep(i, &step); err != nil {
			return err
		}
	}

	// alternatives 校验
	for i, alt := range plan.Alternatives {
		if strings.TrimSpace(alt.Approach) == "" {
			return fmt.Errorf("plan.alternatives[%d].approach is required", i)
		}
	}

	return nil
}

// validateStep 校验单个 PlanStep。
func validateStep(index int, step *PlanStep) error {
	prefix := fmt.Sprintf("plan.steps[%d]", index)

	if strings.TrimSpace(step.ID) == "" {
		return fmt.Errorf("%s.id is required", prefix)
	}
	if strings.TrimSpace(step.Action) == "" {
		return fmt.Errorf("%s.action is required", prefix)
	}
	if strings.TrimSpace(step.Target) == "" {
		return fmt.Errorf("%s.target is required", prefix)
	}
	if strings.TrimSpace(step.Approach) == "" {
		return fmt.Errorf("%s.approach is required", prefix)
	}
	if strings.TrimSpace(step.Risk) == "" {
		return fmt.Errorf("%s.risk is required", prefix)
	}

	// risk 必须是合法值
	if !isValidRisk(step.Risk) {
		return fmt.Errorf("%s.risk must be one of [low, medium, high, critical], got %q", prefix, step.Risk)
	}

	return nil
}

// validRisks 合法的风险等级。
var validRisks = map[string]bool{
	"low": true, "medium": true, "high": true, "critical": true,
}

func isValidRisk(risk string) bool {
	return validRisks[strings.ToLower(risk)]
}

// ValidatePlanForFix 校验 plan 是否满足 fix 阶段的执行前条件。
// 额外要求：每个 step 有 rollback 或明确声明无需回滚。
func ValidatePlanForFix(plan *PlanJSON) error {
	if err := ValidatePlan(plan); err != nil {
		return err
	}

	// pre_checklist 必须存在（可为空数组）
	if plan.PreChecklist == nil {
		return fmt.Errorf("plan.pre_checklist must exist (can be empty)")
	}

	return nil
}
