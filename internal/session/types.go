// Package session 提供 Session 生命周期管理（P0-3）。
//
// session ≡ 节点：节点（tree）是状态机面，Session 是运行时面。
// 两者拼起来是同一个 session 实体的两面。
package session

import "time"

// SessionStatus session 运行时状态。
type SessionStatus string

const (
	StatusPending SessionStatus = "pending"
	StatusRunning SessionStatus = "running"
	StatusBlocked SessionStatus = "blocked"
	StatusDone    SessionStatus = "done"
	StatusKilled  SessionStatus = "killed"
)

// ForkReason 主动/被动 Context Reset 的原因枚举。
type ForkReason int

const (
	ForkOnComplete  ForkReason = iota // 主动 Reset（节点 completed）
	ForkOnThreshold                   // 被动 Reset（上下文 ≥ 阈值）
	ForkOnRollback                    // 用户 rollback
	ForkOnBranch                      // 用户 branch
)

func (r ForkReason) String() string {
	switch r {
	case ForkOnComplete:
		return "on_complete"
	case ForkOnThreshold:
		return "on_threshold"
	case ForkOnRollback:
		return "on_rollback"
	case ForkOnBranch:
		return "on_branch"
	default:
		return "unknown"
	}
}

// Session 一个 Agent 会话的运行时抽象。
type Session struct {
	ID            string        `yaml:"id"`
	TaskID        string        `yaml:"task_id"`
	NodeID        string        `yaml:"node_id"`
	AgentRole     string        `yaml:"agent_role"`
	AgentName     string        `yaml:"agent_name"`
	Status        SessionStatus `yaml:"status"`
	StartedAt     time.Time     `yaml:"started_at"`
	Seq           int           `yaml:"seq"`
	Usage         float64       `yaml:"usage"` // 0.0-1.0
	WorkingDir    string        `yaml:"working_dir"`
	ParentID      string        `yaml:"parent_id,omitempty"`   // 父 session ID（Fork/Spawn 时设）
	ForkReason    string        `yaml:"fork_reason,omitempty"` // Fork 原因（Fork 时设）
	ContextWindow int           `yaml:"context_window"`        // agent 的 context_window token 数

	// 运行时字段（不持久化到 yaml）
	tokenUsage  TokenUsage
	onThreshold ThresholdHandler // 阈值回调
	onComplete  func(s *Session) // 完成回调
}

// SessionSpec 启动 session 的输入参数。
type SessionSpec struct {
	TaskID    string
	NodeID    string
	AgentRole string
	AgentName string
	RepoPath  string // 业务系统源码路径，用于基线验证
}

// SubTaskInput Spawn 子 session 的输入参数。
type SubTaskInput struct {
	Prompt    string
	Skills    []string
	AgentName string
}

// TokenUsage 追踪 input/output token 消耗。
type TokenUsage struct {
	InputTokens   int `yaml:"input_tokens"`
	OutputTokens  int `yaml:"output_tokens"`
	ContextWindow int `yaml:"context_window"` // agent 配置的 context window 上限
}

// Total 返回总 token 消耗。
func (tu TokenUsage) Total() int {
	return tu.InputTokens + tu.OutputTokens
}

// UsageRatio 返回上下文中使用率 (0.0 ~ 1.0)。
func (tu TokenUsage) UsageRatio() float64 {
	if tu.ContextWindow <= 0 {
		return 0
	}
	return float64(tu.Total()) / float64(tu.ContextWindow)
}

// ThresholdHandler 阈值触发回调。
type ThresholdHandler func(s *Session)

// Manager Session 生命周期管理接口。
type Manager interface {
	Start(spec SessionSpec) (*Session, error)
	Resume(sessionID string) (*Session, error)
	Fork(parent *Session, reason ForkReason) (*Session, error)
	Spawn(parent *Session, sub SubTaskInput) (*Session, error)
	Kill(sessionID string) error
	List(taskID string) ([]*Session, error)
}
