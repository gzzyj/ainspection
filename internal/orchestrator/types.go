// Package orchestrator 流水线中枢：阶段调度、门控、Evaluator 子 session Spawn、tool call 分发、Context Reset。
package orchestrator

import (
	"context"
	"time"

	"git.qingteng.cn/ms/ainspection/internal/adapter"
	"git.qingteng.cn/ms/ainspection/internal/config"
	"git.qingteng.cn/ms/ainspection/internal/planner"
	"git.qingteng.cn/ms/ainspection/internal/prompt"
	"git.qingteng.cn/ms/ainspection/internal/security"
	"git.qingteng.cn/ms/ainspection/internal/session"
	"git.qingteng.cn/ms/ainspection/internal/skill"
	"git.qingteng.cn/ms/ainspection/internal/tree"
)

// Note: config/adapter 包已在上方导入。

// Stage 流水线阶段枚举。
type Stage string

const (
	StageGet     Stage = "get"
	StageLocate  Stage = "locate"
	StageReview1 Stage = "review1" // locate 后
	StagePlan    Stage = "plan"
	StageFix     Stage = "fix"
	StageReview2 Stage = "review2" // fix 后（主 review）
	StageVerify  Stage = "verify"
	StageReview3 Stage = "review3" // verify 后
	StageCommit  Stage = "commit"
)

// pipelineOrder 定义阶段执行顺序。
var pipelineOrder = []Stage{
	StageGet,
	StageLocate,
	StageReview1,
	StagePlan,
	StageFix,
	StageReview2,
	StageVerify,
	StageReview3,
	StageCommit,
}

// StageResult 单个阶段的执行结果。
type StageResult struct {
	Stage      Stage
	Status     tree.TaskStatus // 阶段完成后的任务状态
	NodeID     string          // 阶段产生的节点 ID
	RetryCount int             // 本阶段重试次数
	Error      error           // 阶段执行错误（gate blocked 等）
	Duration   time.Duration
}

// PipelineStatus 流水线整体状态。
type PipelineStatus struct {
	TaskID       string
	CurrentStage Stage
	StageResults []StageResult
	StartedAt    time.Time
	UpdatedAt    time.Time
}

// GatingDecision 门控检查结果。
type GatingDecision struct {
	Allowed bool
	Reason  string
	Blocked bool // true 表示需转人工
}

// StageGate 阶段门控定义：进入下一阶段前需满足的条件。
type StageGate struct {
	From  Stage
	To    Stage
	Check func(ctx context.Context, t *tree.Task) GatingDecision
}

// RunSpec 一次流水线运行的输入规范。
type RunSpec struct {
	IssueURL string // Jira issue URL
	Desc     string // 问题文本描述
	Service  string // config.services[].name
	Profile  string // 性能分析目标服务名
	TaskID   string // 已有 task 续跑（resume 场景）
	TraceID  string // 已知的 trace ID，可选；用于跨服务调用链关联
}

// Pipeline 流水线顶层接口：负责串联所有阶段的执行。
type Pipeline interface {
	// Run 从 RunSpec 启动全新流水线。
	Run(ctx context.Context, spec RunSpec) (*PipelineStatus, error)

	// Resume 恢复已有 task 的流水线（从 current_node 对应阶段继续）。
	Resume(ctx context.Context, taskID string) (*PipelineStatus, error)
}

// pipelineImpl 是 Pipeline 的具体实现，持有所有需要的依赖。
type pipelineImpl struct {
	treeMgr    tree.Manager
	nodeOps    tree.NodeOps
	rollback   tree.Rollback
	sessionMgr session.Manager
	evaluator  Evaluator
	mctsEngine MCTSRunner
	dispatcher Dispatcher
	pcfg    *config.Config
	planner planner.Planner

	promptRenderer  prompt.Renderer
	skillLoader     skill.Loader
	skillInjector   skill.Injector
	adapterRegistry *adapter.Registry
	cmdExecutor     security.CommandExecutor
}

// MCTSRunner MCTS 引擎在 orchestrator 中的适配接口。
// orchestrator 不直接依赖 internal/mcts 的具体类型，通过此接口调用。
type MCTSRunner interface {
	// RunLocate 在假设空间搜索，返回 top-K 假设节点。
	RunLocate(ctx context.Context, input LocateInput) ([]LocateOutput, error)

	// RunFix 在修复方案空间搜索，返回 top-1 修复方案。
	RunFix(ctx context.Context, input FixInput) ([]FixOutput, error)
}

// LocateInput MCTS locate 阶段输入。
type LocateInput struct {
	InputYAML   []byte         // input.yaml 内容
	Skills      []string       // 可用 skill 名称列表
	AgentConfig AgentRunConfig // 当前 agent 配置
}

// LocateOutput MCTS locate 阶段输出（单个假设节点路径）。
type LocateOutput struct {
	Hypothesis string             // 根因假设
	Confidence float64            // confidence_final（双源校准后）
	Evidence   []string           // 证据路径
	Dimensions map[string]float64 // LLM 四维评分（correctness/evidence/verifiability/impact）
}

// FixInput MCTS fix 阶段输入。
type FixInput struct {
	PlanJSON    []byte   // plan.json 内容
	Patches     [][]byte // 已有 patch 列表
	Skills      []string
	AgentConfig AgentRunConfig
}

// FixOutput MCTS fix 阶段输出。
type FixOutput struct {
	Diff       []byte            // unified diff
	Confidence float64           // 综合置信度
	StepID     string            // 对应的 plan.steps[].id
	Dimensions map[string]float64 // LLM 四维评分（completeness/quality/compliance/security）
}

// AgentRunConfig 运行一个 agent session 所需的配置摘要（从 config.Config 裁剪）。
type AgentRunConfig struct {
	Name        string
	Endpoint    string
	APIKey      string
	Model       string
	Type        string   // agent type: claude_cli | kimi_cli | codex_cli | qwen_cli | gemini_cli | llm_native
	NativeTools []string
}

// FromConfig 从 config.AgentConfig 构建 AgentRunConfig。
func (a AgentRunConfig) FromConfig(cfg *config.AgentConfig, name string) AgentRunConfig {
	if cfg == nil {
		return AgentRunConfig{Name: name}
	}
	return AgentRunConfig{
		Name:        name,
		Endpoint:    cfg.Endpoint,
		APIKey:      cfg.APIKey,
		Model:       cfg.Model,
		Type:        cfg.ResolveType(),
		NativeTools: cfg.NativeTools,
	}
}

// ToolCall 和 ToolResult 类型定义在 internal/skill/types.go 中，
// orchestrator 通过 Dispatcher 接口引用 skill.ToolCall / skill.ToolResult。
