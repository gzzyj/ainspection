// Package security 提供命令白名单、文件系统边界、沙箱执行和审计日志（P1-4）。
//
// 四层纵深防御：
//  1. 命令白名单 (CommandExecutor)
//  2. 文件系统边界 (FSGuard)
//  3. 审批门控 (L0/L1/L2/L3)
//  4. 会话级沙箱 (Sandbox)
//
// 审计日志统一由 Audit Logger 记录。
package security

import "time"

// —————— 命令执行 ——————

// CommandRule 单条白名单规则。
type CommandRule struct {
	Pattern     string `yaml:"pattern" json:"pattern"`
	AutoApprove bool   `yaml:"auto_approve" json:"auto_approve"`
}

// ExecResult 命令执行结果。
type ExecResult struct {
	Allowed       bool   `json:"allowed"`
	NeedsApproval bool   `json:"needs_approval"`
	Stdout        string `json:"stdout,omitempty"`
	Stderr        string `json:"stderr,omitempty"`
	ExitCode      int    `json:"exit_code"`
	BlockedReason string `json:"blocked_reason,omitempty"`
}

// ApprovalLevel 审批等级。
type ApprovalLevel string

const (
	ApprovalL0Auto       ApprovalLevel = "L0" // 自动执行
	ApprovalL1Notify     ApprovalLevel = "L1" // 执行后通知
	ApprovalL2Confirm    ApprovalLevel = "L2" // 弹窗确认(60s 超时)
	ApprovalL3DualFactor ApprovalLevel = "L3" // 双因素(需 JIRA-ID 校验)
)

// —————— 文件系统 ——————

// OpType 文件操作类型。
type OpType int

const (
	OpRead OpType = iota
	OpWrite
)

func (o OpType) String() string {
	switch o {
	case OpRead:
		return "read"
	case OpWrite:
		return "write"
	default:
		return "unknown"
	}
}

// —————— 审计 ——————

// Record 审计日志记录。
type Record struct {
	TS        time.Time      `json:"ts"`
	TaskID    string         `json:"task_id"`
	SessionID string         `json:"session_id"`
	Agent     string         `json:"agent"`
	Component string         `json:"component"` // command_executor | fs_guard | skill_injector
	Action    string         `json:"action"`    // exec | read | write | inject
	Args      map[string]any `json:"args"`
	Result    string         `json:"result"`           // approved | blocked | failed
	Reason    string         `json:"reason,omitempty"` // 拒绝/失败原因
}
