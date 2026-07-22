package tree

import (
	"fmt"
	"os"
	"strings"

	"gopkg.in/yaml.v3"
)

// MaxSummaryChars 摘要最大字符数（对应 config.context.summary_max_chars）。
const MaxSummaryChars = 500

// GenerateSummary 从节点的 output.yaml 生成 human-readable summary.md。
//
// 算法（task-context.md §4）：
//  1. 问题域（1 句）
//  2. 已确认事实：confidence_final ≥ 0.7 的 findings（≤5 条），带 evidence 路径
//  3. 已排除假设：所有 discarded_hypotheses（≤5 条）
//  4. 当前最佳方向：plan.steps[0]
//  5. 用户指令：所有 user_directives
//
// 总长度 ≤ MaxSummaryChars。
func GenerateSummary(outputPath string) (string, error) {
	data, err := os.ReadFile(outputPath)
	if err != nil {
		return "", fmt.Errorf("read output: %w", err)
	}

	var out NodeOutput
	if err := yaml.Unmarshal(data, &out); err != nil {
		return "", fmt.Errorf("parse output: %w", err)
	}

	var b strings.Builder

	// 1. 问题域（1 句）
	if out.Plan != nil && out.Plan.Goal != "" {
		b.WriteString("## 问题域\n")
		b.WriteString(out.Plan.Goal)
		b.WriteString("\n\n")
	}

	// 2. 已确认事实（confidence_final ≥ 0.7，≤5 条）
	confirmed := filterByConfidence(out.Findings, 0.7, 5)
	if len(confirmed) > 0 {
		b.WriteString("## 已确认事实\n")
		for _, f := range confirmed {
			b.WriteString(fmt.Sprintf("- %s (confidence: %.0f%%)\n", f.Hypothesis, f.ConfidenceFinal*100))
			for _, e := range f.Evidence[:minInt(len(f.Evidence), 3)] {
				b.WriteString(fmt.Sprintf("  - evidence: %s\n", e))
			}
		}
		b.WriteString("\n")
	}

	// 3. 已排除假设（≤5 条）
	discarded := out.DiscardedHypotheses
	if len(discarded) > 5 {
		discarded = discarded[:5]
	}
	if len(discarded) > 0 {
		b.WriteString("## 已排除假设\n")
		for _, d := range discarded {
			b.WriteString(fmt.Sprintf("- %s: %s\n", d.Hypothesis, d.EvidenceAgainst))
		}
		b.WriteString("\n")
	}

	// 4. 当前最佳方向（plan.steps[0]）
	if out.Plan != nil && len(out.Plan.Steps) > 0 {
		s0 := out.Plan.Steps[0]
		b.WriteString("## 当前最佳方向\n")
		b.WriteString(fmt.Sprintf("- %s: %s → %s (risk: %s)\n",
			s0.ID, s0.Action, s0.Target, s0.Risk))
		if s0.EstimatedImpact != "" {
			b.WriteString(fmt.Sprintf("  - 预期影响: %s\n", s0.EstimatedImpact))
		}
		b.WriteString("\n")
	}

	// 5. 用户指令
	if len(out.UserDirectives) > 0 {
		b.WriteString("## 用户指令\n")
		for _, d := range out.UserDirectives {
			b.WriteString(fmt.Sprintf("- %s\n", d))
		}
		b.WriteString("\n")
	}

	summary := b.String()

	// 截断到 MaxSummaryChars
	if len(summary) > MaxSummaryChars {
		// 找到最后一个完整的 UTF-8 字符边界
		truncated := summary[:MaxSummaryChars]
		if idx := strings.LastIndex(truncated, "\n"); idx > 0 {
			truncated = truncated[:idx]
		}
		summary = truncated + "\n\n... (truncated)"
	}

	if summary == "" {
		summary = "(no findings yet)"
	}

	return summary, nil
}

// filterByConfidence 过滤 confidence_final ≥ minConf 的 findings，取前 limit 条。
func filterByConfidence(findings []Finding, minConf float64, limit int) []Finding {
	var result []Finding
	for _, f := range findings {
		if f.ConfidenceFinal >= minConf {
			result = append(result, f)
			if len(result) >= limit {
				break
			}
		}
	}
	return result
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}
