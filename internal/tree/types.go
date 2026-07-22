// Package tree 提供树形上下文管理（P0-2）。
//
// 存储布局: ~/.ainspection/tasks/<task-id>/
//
//	context.yaml           — 任务级状态 + 树指针
//	tree.yaml              — 节点拓扑
//	nodes/<id>/input.yaml  — 节点输入
//	nodes/<id>/output.yaml — 节点输出（findings/plan）
//	nodes/<id>/meta.yaml   — 节点元数据
//	summary.md             — 人工可读摘要（≤500 字符）
//	patches/               — diff patch 文件
//	signals/               — 证据信号文件
package tree

import "time"

// —————— 任务状态枚举 ——————

// TaskStatus 任务状态枚举（9 状态，对应 task-context.md §1）。
type TaskStatus string

const (
	StatusPending           TaskStatus = "pending"
	StatusScopeDefined      TaskStatus = "scope_defined"
	StatusLocating          TaskStatus = "locating"
	StatusExpectationLocked TaskStatus = "expectation_locked"
	StatusFixing            TaskStatus = "fixing"
	StatusVerifying         TaskStatus = "verifying"
	StatusCommitting        TaskStatus = "committing"
	StatusDone              TaskStatus = "done"
	StatusBlocked           TaskStatus = "blocked"
)

// IsTerminal 判断是否为终态。
func (s TaskStatus) IsTerminal() bool {
	return s == StatusDone || s == StatusBlocked
}

// —————— 任务 ——————

// Task 任务实体（对应 context.yaml）。
type Task struct {
	TaskID           string         `yaml:"task_id"`
	CurrentNodeID    string         `yaml:"current_node_id"`
	CurrentSessionID string         `yaml:"current_session_id"`
	RootNodeID       string         `yaml:"root_node_id"`
	TreeVersion      int            `yaml:"tree_version"`
	CreatedAt        time.Time      `yaml:"created_at"`
	UpdatedAt        time.Time      `yaml:"updated_at"`
	Status           TaskStatus     `yaml:"status"`
	IssueRef         string         `yaml:"issue_ref"`
	Service          string         `yaml:"service"`
	RetryCount       map[string]int `yaml:"retry_count"`
	BaselineVerified bool           `yaml:"baseline_verified"`
}

// TaskSpec 创建 Task 的输入参数。
type TaskSpec struct {
	IssueURL string
	Desc     string
	Service  string
	TraceID  string // 已知的 trace ID，可选；用于跨服务调用链关联
}

// —————— 树拓扑 ——————

// TreeYAML tree.yaml 的磁盘结构。
type TreeYAML struct {
	TaskID   string              `yaml:"task_id"`
	Nodes    map[string]NodeYAML `yaml:"nodes"`
	Branches []BranchYAML        `yaml:"branches,omitempty"`
	Seq      int                 `yaml:"seq"` // 全局节点计数器
}

// NodeYAML tree.yaml 中单节点的序列化形态。
type NodeYAML struct {
	Parent       string     `yaml:"parent"`
	Children     []string   `yaml:"children"`
	Status       TaskStatus `yaml:"status"`
	Stage        string     `yaml:"stage"`
	AgentRole    string     `yaml:"agent_role,omitempty"`
	BranchReason string     `yaml:"branch_reason,omitempty"`
	CreatedAt    time.Time  `yaml:"created_at"`
	CompletedAt  *time.Time `yaml:"completed_at,omitempty"`
}

// BranchYAML tree.yaml 中的分支记录。
type BranchYAML struct {
	Name string   `yaml:"name"`
	Path []string `yaml:"path"`
}

// —————— 节点 ——————

// Node 内存中的节点表示（ID 不在 struct 内，由 map key 承载）。
type Node struct {
	ID           string
	Parent       string
	Children     []string
	Status       TaskStatus
	Stage        string
	AgentRole    string
	BranchReason string
	CreatedAt    time.Time
	CompletedAt  *time.Time
}

// NodeInput 创建节点时的输入参数。
type NodeInput struct {
	Stage     string
	AgentRole string
}

// —————— 节点存储 ——————

// NodeMeta meta.yaml 的磁盘结构。
type NodeMeta struct {
	NodeID    string     `yaml:"node_id"`
	Parent    string     `yaml:"parent"`
	Stage     string     `yaml:"stage"`
	AgentRole string     `yaml:"agent_role,omitempty"`
	Status    TaskStatus `yaml:"status"`
	CreatedAt time.Time  `yaml:"created_at"`
}

// NodeOutput output.yaml 的磁盘结构（对应 task-context.md §5.1）。
type NodeOutput struct {
	NodeID              string                `yaml:"node_id"`
	SessionID           string                `yaml:"session_id"`
	Findings            []Finding             `yaml:"findings,omitempty"`
	DiscardedHypotheses []DiscardedHypothesis `yaml:"discarded_hypotheses,omitempty"`
	Plan                *PlanOutput           `yaml:"plan,omitempty"`
	Patch               *PatchOutput          `yaml:"patch,omitempty"`
	UserDirectives      []string              `yaml:"user_directives,omitempty"`
	ReviewReport        *ReviewReportYAML     `yaml:"review_report,omitempty"`
}

// Finding 单个根因发现。
type Finding struct {
	Hypothesis          string   `yaml:"hypothesis"`
	ConfidenceSelf      float64  `yaml:"confidence_self"`
	ConfidenceEvaluator float64  `yaml:"confidence_evaluator"`
	ConfidenceFinal     float64  `yaml:"confidence_final"`
	Evidence            []string `yaml:"evidence"`
	Status              string   `yaml:"status"` // confirmed | investigating
}

// DiscardedHypothesis 已排除的假设。
type DiscardedHypothesis struct {
	Hypothesis      string `yaml:"hypothesis"`
	EvidenceAgainst string `yaml:"evidence_against"`
	Status          string `yaml:"status"` // discarded
}

// PlanOutput 结构化计划。
type PlanOutput struct {
	Version       string        `yaml:"version"`
	Goal          string        `yaml:"goal"`
	Steps         []PlanStep    `yaml:"steps"`
	Alternatives  []Alternative `yaml:"alternatives,omitempty"`
	PreChecklist  []string      `yaml:"pre_checklist,omitempty"`
	PostChecklist []string      `yaml:"post_checklist,omitempty"`
}

// PlanStep 单个修复步骤。
type PlanStep struct {
	ID                  string  `yaml:"id"`
	Action              string  `yaml:"action"`
	Target              string  `yaml:"target"`
	Approach            string  `yaml:"approach"`
	EstimatedImpact     string  `yaml:"estimated_impact"`
	Risk                string  `yaml:"risk"`
	Rollback            string  `yaml:"rollback"`
	ConfidenceSelf      float64 `yaml:"confidence_self"`
	ConfidenceEvaluator float64 `yaml:"confidence_evaluator"`
	ConfidenceFinal     float64 `yaml:"confidence_final"`
}

// PatchOutput 存储 MCTS fix 阶段生成的 patch 信息。
type PatchOutput struct {
	Diff   string `yaml:"diff"`    // unified diff 内容
	StepID string `yaml:"step_id"` // 对应的 plan.steps[].id
}

// Alternative 备选方案。
type Alternative struct {
	Approach  string `yaml:"approach"`
	Tradeoff  string `yaml:"tradeoff"`
	Discarded bool   `yaml:"discarded"`
}

// ReviewReportYAML output.yaml 中的 review_report 段。
type ReviewReportYAML struct {
	Passed              bool              `yaml:"passed"`
	Score               int               `yaml:"score"`
	Dimensions          []ReviewDimension `yaml:"dimensions"`
	Blockers            []string          `yaml:"blockers"`
	Warnings            []string          `yaml:"warnings"`
	ConfidenceSelf      float64           `yaml:"confidence_self"`
	ConfidenceEvaluator float64           `yaml:"confidence_evaluator"`
	ConfidenceDiff      float64           `yaml:"confidence_diff"`
	ConfidenceFinal     float64           `yaml:"confidence_final"`
}

// ReviewDimension 审查维度评分。
type ReviewDimension struct {
	Name    string `yaml:"name"`
	Score   int    `yaml:"score"`
	Comment string `yaml:"comment"`
}

// —————— 管理器接口 ——————

// Manager 任务级树管理器接口。
type Manager interface {
	NewTask(spec TaskSpec) (*Task, error)
	LoadTask(taskID string) (*Task, error)
	Save(t *Task) error
}

// NodeOps 节点 CRUD 操作接口。
type NodeOps interface {
	Create(t *Task, parentID string, in NodeInput) (*Node, error)
	Complete(t *Task, nodeID string) error
	Read(t *Task, nodeID string) (*Node, error)
}

// Rollback 回滚/分支/重放/合并操作接口。
type Rollback interface {
	Rollback(t *Task, targetNodeID string) error
	Branch(t *Task, fromNodeID, reason string) (*Node, error)
	Replay(t *Task, nodeID string) (*Node, error)
	Merge(t *Task, branchNodeID, targetNodeID string) error
}
