package planner

import (
	"git.qingteng.cn/ms/ainspection/internal/prompt"
)

// BuildSystemPrompt 通过 prompt.Renderer 渲染 plan-system.tmpl 模板。
func BuildSystemPrompt(renderer prompt.Renderer, input PlannerInput) (string, error) {
	data := toPlanInput(input)
	return renderer.Render("plan-system", data)
}

// toPlanInput 将 PlannerInput 转换为 prompt.PlanInput（模板变量结构）。
func toPlanInput(input PlannerInput) prompt.PlanInput {
	findings := make([]prompt.FindingData, len(input.Findings))
	for i, f := range input.Findings {
		findings[i] = prompt.FindingData{
			Hypothesis:     f.Hypothesis,
			Confidence:     f.ConfidenceSelf,
			ConfidenceSelf: f.ConfidenceSelf,
			Evidence:       f.Evidence,
		}
	}

	discarded := make([]prompt.DiscardedHypothesisData, len(input.DiscardedHypotheses))
	for i, dh := range input.DiscardedHypotheses {
		discarded[i] = prompt.DiscardedHypothesisData{
			Hypothesis:      dh.Hypothesis,
			EvidenceAgainst: joinEvidence(dh.Evidence),
		}
	}

	skills := make([]prompt.SkillDesc, len(input.AvailableSkills))
	for i, s := range input.AvailableSkills {
		skills[i] = prompt.SkillDesc{
			Name:        s.Name,
			Description: s.Description,
		}
	}

	return prompt.PlanInput{
		Findings:            findings,
		DiscardedHypotheses: discarded,
		ParentSummary:       input.ParentSummary,
		UserDirectives:      input.UserDirectives,
		AvailableSkills:     skills,
	}
}

// joinEvidence 将多条证据合并为一条字符串。
func joinEvidence(evidence []string) string {
	if len(evidence) == 0 {
		return ""
	}
	result := ""
	for i, e := range evidence {
		if i > 0 {
			result += "; "
		}
		result += e
	}
	return result
}
