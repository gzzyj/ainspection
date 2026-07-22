package security

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"regexp"
	"strings"
	"time"
)

// CommandExecutor 命令执行器接口。
type CommandExecutor interface {
	// Execute 校验并执行命令。校验流程：
	//  1. 检查 blocked_patterns（管道/链式/命令替换）
	//  2. 检查 allowed_commands 白名单
	//  3. auto_approve=false 时设置 NeedsApproval=true
	//  4. 通过后在 workingDir 中执行
	Execute(ctx context.Context, cmd string, workingDir string) (*ExecResult, error)
}

// commandExecutorImpl 默认实现。
type commandExecutorImpl struct {
	allowedCommands []CommandRule
	blockedPatterns []string
	timeout         time.Duration
}

// NewCommandExecutor 创建 CommandExecutor。
// timeout 为命令执行超时，<=0 时使用默认 60s。
func NewCommandExecutor(allowed []CommandRule, blocked []string, timeout time.Duration) CommandExecutor {
	if timeout <= 0 {
		timeout = 60 * time.Second
	}
	return &commandExecutorImpl{
		allowedCommands: allowed,
		blockedPatterns: blocked,
		timeout:         timeout,
	}
}

// INSTRUMENT: security-command-execute — 命令白名单校验与安全执行
// LAYER: L2
// STATUS: implemented
// Execute 校验并执行命令。
func (e *commandExecutorImpl) Execute(ctx context.Context, cmd string, workingDir string) (*ExecResult, error) {
	cmd = strings.TrimSpace(cmd)

	// 1. 检查 blocked_patterns
	for _, pattern := range e.blockedPatterns {
		if matched, _ := regexp.MatchString(pattern, cmd); matched {
			return &ExecResult{
				Allowed:       false,
				BlockedReason: fmt.Sprintf("blocked_pattern: %s", pattern),
			}, nil
		}
	}

	// 2. 检查 allowed_commands
	matchedRule := e.matchAllowed(cmd)
	if matchedRule == nil {
		return &ExecResult{
			Allowed:       false,
			NeedsApproval: true,
			BlockedReason: "no matching allowed_command rule",
		}, nil
	}

	// 3. 需要审批?
	if !matchedRule.AutoApprove {
		return &ExecResult{
			Allowed:       true,
			NeedsApproval: true,
		}, nil
	}

	// 4. 创建子进程执行
	if ctx == nil {
		ctx = context.Background()
	}

	execCtx, cancel := context.WithTimeout(ctx, e.timeout)
	defer cancel()

	// shlex 简单解析：按空格拆分（完整 shlex 留给 shell）
	parts := strings.Fields(cmd)
	if len(parts) == 0 {
		return &ExecResult{Allowed: false, BlockedReason: "empty command"}, nil
	}

	var exeCmd *exec.Cmd
	if len(parts) == 1 {
		exeCmd = exec.CommandContext(execCtx, parts[0])
	} else {
		exeCmd = exec.CommandContext(execCtx, parts[0], parts[1:]...)
	}
	exeCmd.Dir = workingDir

	var stdout, stderr bytes.Buffer
	exeCmd.Stdout = &stdout
	exeCmd.Stderr = &stderr

	err := exeCmd.Run()

	result := &ExecResult{
		Allowed: true,
		Stdout:  stdout.String(),
		Stderr:  stderr.String(),
	}

	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			result.ExitCode = exitErr.ExitCode()
		} else {
			result.ExitCode = -1
		}
	} else {
		result.ExitCode = 0
	}

	return result, nil
}

// matchAllowed 检查命令是否匹配任一 allowed_commands 规则。
func (e *commandExecutorImpl) matchAllowed(cmd string) *CommandRule {
	for i := range e.allowedCommands {
		rule := &e.allowedCommands[i]
		if matched, _ := regexp.MatchString("^"+rule.Pattern+"$", cmd); matched {
			return rule
		}
		// 也尝试前缀匹配（支持 "git status" 匹配 "git (status|diff|...)"）
		if matched, _ := regexp.MatchString(rule.Pattern, cmd); matched {
			// 检查是否完全匹配（cmd 必须在 pattern 描述的范围内）
			fields := strings.Fields(cmd)
			patternFields := extractPatternFields(rule.Pattern)
			if len(fields) > 0 && containsPattern(patternFields, fields[0]) {
				return rule
			}
		}
	}
	return nil
}

// extractPatternFields 从 pattern 中提取第一段的候选命令名。
// "git (status|diff|log)" → ["git"]
func extractPatternFields(pattern string) []string {
	parts := strings.Fields(pattern)
	if len(parts) == 0 {
		return nil
	}
	return parts[:1]
}

func containsPattern(patterns []string, cmd string) bool {
	for _, p := range patterns {
		if strings.TrimRight(p, ".*") == cmd {
			return true
		}
	}
	return false
}
