// Package eval 提供 ainspection eval 子命令（P2-5 L2 离线评测）。
package eval

import (
	"fmt"
	"os"
	"strings"

	"github.com/spf13/cobra"

	"git.qingteng.cn/ms/ainspection/internal/eval"
	"git.qingteng.cn/ms/ainspection/internal/prompt"
)

// New 创建 eval 子命令。
func New() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "eval",
		Short: "L2 离线评测 (Prompt Smoke Test + Case Regression)",
		Long: `对 16 个 .tmpl 模板做 smoke test 渲染验证，以及可选手工 case 回归对比。

L2a — Smoke Test (默认): 用 sample data 逐个 Parse+Execute 全部 16 个模板
L2b — Case Regression: 用真实场景数据跑 prompt 链渲染，验证输出 schema 结构`,
		RunE: run,
	}

	cmd.Flags().String("case", "", "指定 case ID 进行单个回归评测")
	cmd.Flags().String("output", "", "报告输出路径 (默认 stdout)")
	cmd.Flags().Bool("baseline", false, "保存当前渲染输出为 baseline")

	return cmd
}

func run(cmd *cobra.Command, _ []string) error {
	caseID, _ := cmd.Flags().GetString("case")
	outputPath, _ := cmd.Flags().GetString("output")

	// 寻找 prompts/ 目录
	promptDir := findPromptDir()
	renderer, err := prompt.NewRenderer(promptDir)
	if err != nil {
		return fmt.Errorf("init renderer from %s: %w", promptDir, err)
	}

	report := &eval.EvalReport{}

	if caseID != "" {
		// L2b: 单个 case 回归
		cr := eval.NewCaseRunner(renderer)
		result, err := cr.Run(caseID)
		if err != nil {
			return fmt.Errorf("run case %s: %w", caseID, err)
		}
		report.CaseResults = append(report.CaseResults, *result)
	} else {
		// L2a: 全量 smoke test
		sr := eval.NewSmokeRunner(renderer)
		report.SmokeResults = sr.Run()

		// 同时运行所有 case
		caseIDs, err := eval.ListCases()
		if err == nil && len(caseIDs) > 0 {
			cr := eval.NewCaseRunner(renderer)
			for _, id := range caseIDs {
				result, err := cr.Run(id)
				if err != nil {
					failed := eval.CaseResult{
						CaseID: id,
						Passed: false,
						Error:  err.Error(),
					}
					report.CaseResults = append(report.CaseResults, failed)
					continue
				}
				report.CaseResults = append(report.CaseResults, *result)
			}
		}
	}

	// 输出报告
	text := formatReport(report)
	if outputPath != "" {
		if err := os.WriteFile(outputPath, []byte(text), 0o644); err != nil {
			return fmt.Errorf("write report: %w", err)
		}
		fmt.Printf("Report written to %s\n", outputPath)
	} else {
		fmt.Print(text)
	}

	return nil
}

// findPromptDir 查找 prompts/ 目录。
func findPromptDir() string {
	candidates := []string{"prompts", "../prompts"}
	// 从可执行文件路径推断
	if exe, err := os.Executable(); err == nil {
		// 尝试从可执行文件同级找 prompts
		candidates = append(candidates,
			exe+"/../prompts",
			exe+"/../../prompts",
		)
	}

	for _, d := range candidates {
		if info, err := os.Stat(d); err == nil && info.IsDir() {
			return d
		}
	}
	return "prompts"
}

// formatReport 将 EvalReport 格式化为可读文本。
func formatReport(r *eval.EvalReport) string {
	var sb strings.Builder

	// Smoke Test 部分
	if len(r.SmokeResults) > 0 {
		sb.WriteString("=== Prompt Smoke Test (" + fmt.Sprintf("%d", len(r.SmokeResults)) + " templates) ===\n")

		spassed, stotal := r.SmokePassed()
		for _, s := range r.SmokeResults {
			status := "PASS"
			if !s.Passed {
				status = "FAIL"
			}

			fields := strings.Join(s.InputFields, "/")
			if s.Passed {
				sb.WriteString(fmt.Sprintf("  %-24s %s  (%d chars, fields: %s)\n",
					s.Template+".tmpl", status, s.CharCount, fields))
			} else {
				sb.WriteString(fmt.Sprintf("  %-24s %s  (error: %s)\n",
					s.Template+".tmpl", status, s.Error))
			}
		}

		sb.WriteString(fmt.Sprintf("\n  %d/%d PASSED  %d FAILED\n", spassed, stotal, stotal-spassed))
	}

	// Case Regression 部分
	if len(r.CaseResults) > 0 {
		sb.WriteString(fmt.Sprintf("\n=== Case Regression (%d cases) ===\n", len(r.CaseResults)))

		cpassed, ctotal := r.CasePassed()
		for _, c := range r.CaseResults {
			status := "PASS"
			if !c.Passed {
				status = "FAIL"
			}

			if c.Error != "" {
				sb.WriteString(fmt.Sprintf("  %-16s %s  (error: %s)\n", c.CaseID, status, c.Error))
				continue
			}

			// 输出各 stage 状态
			var details []string
			for _, stage := range []string{"get", "locate", "plan", "fix"} {
				if sc, ok := c.StageResults[stage]; ok {
					if sc.Passed {
						details = append(details, stage+" schema ok")
					} else {
						details = append(details, stage+" FAIL")
					}
				}
			}
			sb.WriteString(fmt.Sprintf("  %-16s %s  (%s)\n", c.CaseID, status, strings.Join(details, ", ")))
		}

		sb.WriteString(fmt.Sprintf("\n  %d/%d PASSED  %d FAILED\n", cpassed, ctotal, ctotal-cpassed))
	}

	// Total
	totalResults := len(r.SmokeResults) + len(r.CaseResults)
	totalPassed := 0
	sp, _ := r.SmokePassed()
	cp, _ := r.CasePassed()
	totalPassed = sp + cp

	sb.WriteString(fmt.Sprintf("\nTotal: %d/%d tests passed\n", totalPassed, totalResults))

	return sb.String()
}
