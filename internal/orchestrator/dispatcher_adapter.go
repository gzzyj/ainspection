package orchestrator

import (
	"context"

	"git.qingteng.cn/ms/ainspection/internal/skill"
)

// dispatcherAdapter 将 skill.Executor 适配为 orchestrator.Dispatcher 接口。
type dispatcherAdapter struct {
	executor skill.Executor
}

// NewDispatcherFromExecutor 将 skill.Executor 包装为 Dispatcher。
func NewDispatcherFromExecutor(exec skill.Executor) Dispatcher {
	return &dispatcherAdapter{executor: exec}
}

func (d *dispatcherAdapter) Dispatch(ctx context.Context, sessionID string, call skill.ToolCall) (skill.ToolResult, error) {
	return d.executor.Execute(ctx, sessionID, call)
}

func (d *dispatcherAdapter) DispatchBatch(ctx context.Context, sessionID string, calls []skill.ToolCall) ([]skill.ToolResult, error) {
	return d.executor.ExecuteBatch(ctx, sessionID, calls)
}

func (d *dispatcherAdapter) ResolveLayer(call skill.ToolCall) skill.ToolLayer {
	return d.executor.ResolveLayer(call)
}
