package session

import (
	"context"
	"fmt"
	"os/exec"

	"git.qingteng.cn/ms/ainspection/internal/security"
)

// BaselineResult 基线验证结果。
type BaselineResult struct {
	Passed  bool
	BuildOK bool
	VetOK   bool
	TestOK  bool // 仅 fix/verify 阶段要求
	Output  string
}

// VerifyBaseline 执行基线检查：go build + go vet（可选 go test）。
//
// repoPath 为业务系统源码路径（对应 config.services[].repo_path）。
// requireTest 为 true 时额外执行 go test ./...（fix/verify 阶段要求）。
// executor 为 security.CommandExecutor，为 nil 时 fallback 到 os/exec。
func VerifyBaseline(repoPath string, requireTest bool, executor security.CommandExecutor) (*BaselineResult, error) {
	result := &BaselineResult{Passed: true, BuildOK: true, VetOK: true, TestOK: true}
	ctx := context.Background()

	// 1. go build ./...
	if err := runBaselineCmd(ctx, executor, "go", []string{"build", "./..."}, repoPath, result, func(r *BaselineResult, out string) {
		r.Passed = false
		r.BuildOK = false
		r.Output = fmt.Sprintf("go build failed:\n%s", out)
	}); err != nil {
		return result, nil // 继续收集更多错误
	}

	// 2. go vet ./...
	if err := runBaselineCmd(ctx, executor, "go", []string{"vet", "./..."}, repoPath, result, func(r *BaselineResult, out string) {
		r.Passed = false
		r.VetOK = false
		if r.Output != "" {
			r.Output += "\n"
		}
		r.Output += fmt.Sprintf("go vet failed:\n%s", out)
	}); err != nil {
		// continue
	}

	// 3. go test ./...（仅在 requireTest 时）
	if requireTest {
		runBaselineCmd(ctx, executor, "go", []string{"test", "./..."}, repoPath, result, func(r *BaselineResult, out string) {
			r.Passed = false
			r.TestOK = false
			if r.Output != "" {
				r.Output += "\n"
			}
			r.Output += fmt.Sprintf("go test failed:\n%s", out)
		})
	}

	return result, nil
}

// runBaselineCmd 通过 CommandExecutor 或 exec.Command 执行命令。
func runBaselineCmd(ctx context.Context, executor security.CommandExecutor, name string, args []string, dir string, result *BaselineResult, onFail func(*BaselineResult, string)) error {
	var output string
	var cmdErr error

	if executor != nil {
		cmdStr := name + " " + joinArgs(args)
		execResult, err := executor.Execute(ctx, cmdStr, dir)
		if err != nil {
			return err
		}
		if execResult.ExitCode != 0 {
			cmdErr = fmt.Errorf("exit code %d", execResult.ExitCode)
		}
		output = execResult.Stdout + execResult.Stderr
	} else {
		cmd := exec.CommandContext(ctx, name, args...)
		cmd.Dir = dir
		out, err := cmd.CombinedOutput()
		output = string(out)
		cmdErr = err
	}

	if cmdErr != nil {
		onFail(result, output)
		return cmdErr
	}
	return nil
}

// joinArgs 拼接命令行参数。
func joinArgs(args []string) string {
	result := ""
	for i, a := range args {
		if i > 0 {
			result += " "
		}
		result += a
	}
	return result
}

// ShouldBlockSession 判断基线失败是否需要阻塞 session。
// 仅当 build 或 vet 失败时阻塞（test 失败仅 warning）。
func ShouldBlockSession(r *BaselineResult) bool {
	return !r.BuildOK || !r.VetOK
}
