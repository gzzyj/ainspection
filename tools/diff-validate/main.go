// diff-validate 校验 unified diff 的合法性。
//
// 用法:
//
//	diff-validate <file.diff>
//	diff-validate - < file.diff           (从 stdin 读取)
//	git diff HEAD~1 | diff-validate -     (管道输入)
//
// 输出 JSON: {"valid": true|false, "issues": ["..."]}
//
// 检查项:
//  1. git apply --check 是否能通过
//  2. 是否包含调试代码 (fmt.Println, console.log 等)
//  3. diff 是否为空
package main

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"regexp"
	"strings"
)

// Result diff 校验结果。
type Result struct {
	Valid  bool     `json:"valid"`
	Issues []string `json:"issues"`
}

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, "Usage: diff-validate <file.diff|- >")
		os.Exit(2)
	}

	path := os.Args[1]

	var diffBytes []byte
	var err error

	if path == "-" {
		diffBytes, err = io.ReadAll(os.Stdin)
	} else {
		diffBytes, err = os.ReadFile(path)
	}
	if err != nil {
		output(Result{Valid: false, Issues: []string{fmt.Sprintf("read input: %v", err)}})
		os.Exit(1)
	}

	diff := string(diffBytes)
	issues := validate(diff)

	if len(issues) > 0 {
		output(Result{Valid: false, Issues: issues})
		os.Exit(1)
	}

	output(Result{Valid: true, Issues: nil})
}

// validate 执行所有校验并返回问题列表。
func validate(diff string) []string {
	var issues []string

	if strings.TrimSpace(diff) == "" {
		return []string{"diff is empty"}
	}

	// 1. git apply --check
	if issue := checkGitApply(diff); issue != "" {
		issues = append(issues, issue)
	}

	// 2. 调试代码检测
	if debugIssues := checkDebugCode(diff); len(debugIssues) > 0 {
		issues = append(issues, debugIssues...)
	}

	return issues
}

// checkGitApply 使用 git apply --check 校验 patch。
func checkGitApply(diff string) string {
	cmd := exec.Command("git", "apply", "--check")
	cmd.Stdin = strings.NewReader(diff)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Sprintf("git apply --check failed: %s", strings.TrimSpace(string(out)))
	}
	return ""
}

// debugPatterns 调试代码检测模式。
var debugPatterns = []struct {
	pattern *regexp.Regexp
	name    string
}{
	{regexp.MustCompile(`(?m)^\+.*fmt\.Println\(`), "Go: fmt.Println"},
	{regexp.MustCompile(`(?m)^\+.*fmt\.Printf\(`), "Go: fmt.Printf"},
	{regexp.MustCompile(`(?m)^\+.*console\.log\(`), "JS: console.log"},
	{regexp.MustCompile(`(?m)^\+.*console\.error\(`), "JS: console.error"},
	{regexp.MustCompile(`(?m)^\+.*print\(`), "Python: print()"},
	{regexp.MustCompile(`(?m)^\+.*System\.out\.println\(`), "Java: System.out.println"},
	{regexp.MustCompile(`(?m)^\+.*log\.Println\(`), "Go: log.Println (debug)"},
	{regexp.MustCompile(`(?m)^\+.*log\.Printf\(`), "Go: log.Printf (debug)"},
	{regexp.MustCompile(`(?m)^\+.*\bdebugger\b`), "debugger statement"},
	{regexp.MustCompile(`(?m)^\+.*dump\(`), "debug dump()"},
}

// checkDebugCode 检测 diff 中新增的调试代码行。
func checkDebugCode(diff string) []string {
	var issues []string
	for _, dp := range debugPatterns {
		if dp.pattern.MatchString(diff) {
			issues = append(issues, "debug code found: "+dp.name)
		}
	}
	return issues
}

// output 输出 JSON 结果到 stdout。
func output(r Result) {
	data, _ := json.MarshalIndent(r, "", "  ")
	fmt.Println(string(data))
}
