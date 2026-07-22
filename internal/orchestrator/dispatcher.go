package orchestrator

import (
	"context"
	"fmt"

	"git.qingteng.cn/ms/ainspection/internal/skill"
)

// Dispatcher tool call 执行分发器接口。
//
// 当 LLM 返回 tool_use 时，Dispatcher 负责判断工具层级并路由到对应的 handler：
//   - L1: 业务 skill（.skills/*.md 定义的 11 个 skill）— 走 skill executor
//   - L2: 平台原生工具（web_search 等）— 透传给 agent 平台
//   - L3: 内置 bash — 走 security.CommandExecutor 白名单检查后执行
//
// 具体实现见 internal/skill/executor.go。
type Dispatcher interface {
	Dispatch(ctx context.Context, sessionID string, call skill.ToolCall) (skill.ToolResult, error)
	DispatchBatch(ctx context.Context, sessionID string, calls []skill.ToolCall) ([]skill.ToolResult, error)
	ResolveLayer(call skill.ToolCall) skill.ToolLayer
}

// DispatchError 分发执行错误。
type DispatchError struct {
	ToolCallID string
	Layer      skill.ToolLayer
	ToolName   string
	Err        error
}

func (e *DispatchError) Error() string {
	return fmt.Sprintf("dispatch %s tool %s (call %s): %v", e.Layer, e.ToolName, e.ToolCallID, e.Err)
}

func (e *DispatchError) Unwrap() error {
	return e.Err
}
