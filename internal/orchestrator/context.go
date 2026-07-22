package orchestrator

import (
	"context"
	"fmt"

	"git.qingteng.cn/ms/ainspection/internal/session"
	"git.qingteng.cn/ms/ainspection/internal/tree"
)

// ContextResetManager 管理 Context Reset 协议。
//
// 两种触发策略：
//   - 主动 Reset（ForkOnComplete）：节点完成后，强制新 session 冷启动
//   - 被动 Reset（ForkOnThreshold）：上下文使用率 ≥ 阈值时自动 fork
//
// Reset 后子 session 仅接收：input.yaml + 父 summary.md + 当前阶段 prompt + skill/tool 描述。
type ContextResetManager struct {
	sessionMgr session.Manager
	nodeOps    tree.NodeOps
	treeMgr    tree.Manager
}

// NewContextResetManager 创建 Context Reset 管理器。
func NewContextResetManager(sm session.Manager, no tree.NodeOps, tm tree.Manager) *ContextResetManager {
	return &ContextResetManager{
		sessionMgr: sm,
		nodeOps:    no,
		treeMgr:    tm,
	}
}

// HandleNodeComplete 处理节点完成后的主动 Context Reset。
//
// 调用链：tree.NodeOps.Complete → 生成 summary.md → sessionMgr.Fork(parent, ForkOnComplete)
// 这是 Fork/Complete 联动的入口，由 orchestrator 在阶段完成后显式调用。
func (c *ContextResetManager) HandleNodeComplete(ctx context.Context, t *tree.Task, nodeID string) (*session.Session, error) {
	// 1. 完成节点（内部自动触发 summary.go 生成 summary.md）
	if err := c.nodeOps.Complete(t, nodeID); err != nil {
		return nil, fmt.Errorf("complete node %s: %w", nodeID, err)
	}

	// 2. 获取当前活跃 session
	sessions, err := c.sessionMgr.List(t.TaskID)
	if err != nil {
		return nil, fmt.Errorf("list sessions for task %s: %w", t.TaskID, err)
	}

	var currentSession *session.Session
	for _, s := range sessions {
		if s.Status == session.StatusRunning && s.NodeID == nodeID {
			currentSession = s
			break
		}
	}
	if currentSession == nil {
		return nil, fmt.Errorf("no running session found for node %s", nodeID)
	}

	// 3. 主动 Fork：结束当前 session，创建新 session 冷启动
	newSession, err := c.sessionMgr.Fork(currentSession, session.ForkOnComplete)
	if err != nil {
		return nil, fmt.Errorf("fork session for node %s: %w", nodeID, err)
	}

	return newSession, nil
}

// HandleContextThreshold 处理被动 Context Reset（上下文使用率超阈值）。
func (c *ContextResetManager) HandleContextThreshold(ctx context.Context, t *tree.Task, current *session.Session) (*session.Session, error) {
	if current.Usage < 0.4 {
		return current, nil // 未达阈值，无需 reset
	}

	newSession, err := c.sessionMgr.Fork(current, session.ForkOnThreshold)
	if err != nil {
		return nil, fmt.Errorf("fork on threshold for session %s: %w", current.ID, err)
	}

	return newSession, nil
}

// CheckAndReset 检查上下文使用率，必要时触发被动 Reset。
// 返回当前应使用的 session（可能是新 fork 的）。
func (c *ContextResetManager) CheckAndReset(ctx context.Context, t *tree.Task, current *session.Session) (*session.Session, error) {
	return c.HandleContextThreshold(ctx, t, current)
}
