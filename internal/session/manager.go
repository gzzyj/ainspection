package session

import (
	"context"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"git.qingteng.cn/ms/ainspection/internal/adapter"
	"git.qingteng.cn/ms/ainspection/internal/security"
	"gopkg.in/yaml.v3"
)

// sessionRoot 返回 session 存储根目录（默认路径，向后兼容）。
func sessionRoot() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".ainspection", "sessions")
}

// sessionRootWithDataDir 基于指定的 dataDir 返回 session 存储根目录。
func sessionRootWithDataDir(dataDir string) string {
	return filepath.Join(expandDataDir(dataDir), "sessions")
}

// sessionDirForTask 返回指定 task 下的 sessions 目录（默认路径，向后兼容）。
func sessionDirForTask(taskID string) string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".ainspection", "tasks", taskID, "sessions")
}

// sessionDirForTaskWithDataDir 基于 dataDir 返回指定 task 下的 sessions 目录。
func sessionDirForTaskWithDataDir(dataDir, taskID string) string {
	return filepath.Join(expandDataDir(dataDir), "tasks", taskID, "sessions")
}

// expandDataDir 展开 dataDir 中的 ~。
func expandDataDir(dataDir string) string {
	if strings.HasPrefix(dataDir, "~/") {
		home, _ := os.UserHomeDir()
		return filepath.Join(home, dataDir[2:])
	}
	return dataDir
}

// —————— Manager 实现 ——————

type managerImpl struct {
	investLog InvestigationLogger
	executor  security.CommandExecutor
	sandbox   security.Sandbox
	dataDir   string
}

// NewManager 创建 Manager 实例（使用默认数据目录，无 sandbox）。
// executor 可选，为 nil 时跳过基线验证。
func NewManager(executor security.CommandExecutor) Manager {
	return &managerImpl{
		investLog: NewInvestigationLogger(),
		executor:  executor,
	}
}

// NewManagerWithSandbox 创建带 sandbox 和数据目录的 Manager 实例。
func NewManagerWithSandbox(executor security.CommandExecutor, sandbox security.Sandbox, dataDir string) Manager {
	return &managerImpl{
		investLog: NewInvestigationLogger(),
		executor:  executor,
		sandbox:   sandbox,
		dataDir:   expandDataDir(dataDir),
	}
}

// INSTRUMENT: session-start — session 生命周期入口，含基线验证门控
// LAYER: L1
// STATUS: implemented
// Start 启动新 session：Orient → Setup → VerifyBaseline → persist。
func (m *managerImpl) Start(spec SessionSpec) (*Session, error) {
	taskID := spec.TaskID
	nodeID := spec.NodeID

	// 分配 session seq（按 task 内已有 session 数量 +1）
	seq := m.nextSeq(taskID)

	// 生成 session ID: <task-id>-<seq>-<role>
	sessionID := fmt.Sprintf("%s-%d-%s", taskID, seq, spec.AgentRole)

	// 确定 working dir
	workingDir := m.resolveSessionDir(sessionID)

	now := time.Now()
	s := &Session{
		ID:            sessionID,
		TaskID:        taskID,
		NodeID:        nodeID,
		AgentRole:     spec.AgentRole,
		AgentName:     spec.AgentName,
		Status:        StatusRunning,
		StartedAt:     now,
		Seq:           seq,
		Usage:         0,
		WorkingDir:    workingDir,
		ContextWindow: DefaultContextWindow,
	}

	// 1. Orient：加载任务上下文（P0 仅检查目录存在）
	taskDir := filepath.Join(filepath.Dir(m.resolveSessionDirForTask(taskID)), "")
	_ = taskDir // 后续阶段使用

	// 2. Setup：创建 working dir（优先使用 sandbox，完成 mkdir + symlink + adapter.Setup）
	if m.sandbox != nil {
		// sandbox.SetupSession 完成: mkdir 标准子目录 + symlink source → repo + adapter.Setup
		sandboxPath, setupErr := m.sandbox.SetupSession(context.Background(), sessionID, spec.AgentName, adapter.AgentSetupConfig{})
		if setupErr != nil {
			return nil, fmt.Errorf("sandbox setup: %w", setupErr)
		}
		workingDir = sandboxPath
		s.WorkingDir = workingDir
	} else {
		if err := os.MkdirAll(workingDir, 0o755); err != nil {
			return nil, fmt.Errorf("setup working dir: %w", err)
		}
	}

	// 3. Verify Baseline：编译 + 静态检查（go build && go vet）
	if m.executor != nil && spec.RepoPath != "" {
		baselineResult, err := VerifyBaseline(spec.RepoPath, false, m.executor)
		if err != nil {
			log.Printf("[session] baseline verification error for %s: %v", sessionID, err)
		} else if baselineResult != nil {
			s.ContextWindow = DefaultContextWindow
			if ShouldBlockSession(baselineResult) {
				s.Status = StatusBlocked
				log.Printf("[session] baseline verification FAILED for %s: %s", sessionID, baselineResult.Output)
				// 持久化后再返回
				if saveErr := m.save(s); saveErr != nil {
					return nil, fmt.Errorf("save blocked session: %w", saveErr)
				}
				return s, fmt.Errorf("baseline verification failed: %s", baselineResult.Output)
			}
			if !baselineResult.Passed {
				log.Printf("[session] baseline test failure (non-blocking) for %s: %s", sessionID, baselineResult.Output)
			}
		}
	}

	// 持久化 session
	if err := m.save(s); err != nil {
		return nil, fmt.Errorf("save session: %w", err)
	}

	return s, nil
}

// Resume 从磁盘加载并恢复 session。
func (m *managerImpl) Resume(sessionID string) (*Session, error) {
	s, err := m.load(sessionID)
	if err != nil {
		return nil, err
	}

	if s.Status == StatusKilled {
		return nil, fmt.Errorf("session %s was killed", sessionID)
	}

	s.Status = StatusRunning
	s.Usage = 0
	s.tokenUsage = TokenUsage{ContextWindow: s.ContextWindow}

	if err := m.save(s); err != nil {
		return nil, err
	}

	return s, nil
}

// Fork 从父 session 创建子 session（Context Reset）。
//
// 主动 Reset（ForkOnComplete）:
//   - 父 session 标记为 done
//   - 子 session 冷启动（不含父对话历史）
//   - 仅传递: input.yaml + 父节点 summary.md + 当前阶段 prompt 模板
//
// 被动 Reset（ForkOnThreshold）:
//   - 父 session 标记为 done
//   - 子 session 冷启动，与主动 Reset 相同的上下文
func (m *managerImpl) Fork(parent *Session, reason ForkReason) (*Session, error) {
	if parent.Status == StatusKilled || parent.Status == StatusBlocked {
		return nil, fmt.Errorf("cannot fork session with status %s", parent.Status)
	}

	// 标记父 session 完成
	parent.Status = StatusDone

	// 生成子 session ID
	seq := m.nextSeq(parent.TaskID)
	sessionID := fmt.Sprintf("%s-%d-%s", parent.TaskID, seq, parent.AgentRole)
	workingDir := m.resolveSessionDir(sessionID)

	now := time.Now()
	child := &Session{
		ID:            sessionID,
		TaskID:        parent.TaskID,
		NodeID:        parent.NodeID, // 同一节点
		AgentRole:     parent.AgentRole,
		AgentName:     parent.AgentName,
		Status:        StatusRunning,
		StartedAt:     now,
		Seq:           seq,
		Usage:         0,
		WorkingDir:    workingDir,
		ParentID:      parent.ID,
		ForkReason:    reason.String(),
		ContextWindow: parent.ContextWindow,
	}

	// 创建子 session 的 working dir
	if m.sandbox != nil {
		sandboxPath, setupErr := m.sandbox.SetupSession(context.Background(), sessionID, parent.AgentName, adapter.AgentSetupConfig{})
		if setupErr != nil {
			return nil, fmt.Errorf("sandbox setup fork: %w", setupErr)
		}
		workingDir = sandboxPath
		child.WorkingDir = workingDir
	} else {
		if err := os.MkdirAll(workingDir, 0o755); err != nil {
			return nil, fmt.Errorf("setup fork working dir: %w", err)
		}
	}

	// 持久化父子 session
	if err := m.save(parent); err != nil {
		return nil, err
	}
	if err := m.save(child); err != nil {
		return nil, err
	}

	return child, nil
}

// Spawn 从父 session 委派子 session（Evaluator / Planner 独立 session）。
//
// 子 session 拥有:
//   - 独立 working dir
//   - 独立 context window（冷启动）
//   - 独立 agent（AgentName 可不同于父）
//
// 子 session=completed 后 output.yaml 摘要回写父对话上下文（仅摘要，不重放对话历史）。
func (m *managerImpl) Spawn(parent *Session, sub SubTaskInput) (*Session, error) {
	seq := m.nextSeq(parent.TaskID)

	// 子 session agent role 默认 "evaluator"
	agentRole := "evaluator"
	agentName := sub.AgentName
	if agentName == "" {
		agentName = parent.AgentName
	}

	sessionID := fmt.Sprintf("%s-%d-%s", parent.TaskID, seq, agentRole)
	workingDir := m.resolveSessionDir(sessionID)

	now := time.Now()
	child := &Session{
		ID:            sessionID,
		TaskID:        parent.TaskID,
		NodeID:        "", // 子 session 可能在审查时创建新节点
		AgentRole:     agentRole,
		AgentName:     agentName,
		Status:        StatusRunning,
		StartedAt:     now,
		Seq:           seq,
		Usage:         0,
		WorkingDir:    workingDir,
		ParentID:      parent.ID,
		ForkReason:    "spawn",
		ContextWindow: DefaultContextWindow,
	}

	if m.sandbox != nil {
		sandboxPath, setupErr := m.sandbox.SetupSession(context.Background(), sessionID, agentName, adapter.AgentSetupConfig{})
		if setupErr != nil {
			return nil, fmt.Errorf("sandbox setup spawn: %w", setupErr)
		}
		workingDir = sandboxPath
		child.WorkingDir = workingDir
	} else {
		if err := os.MkdirAll(workingDir, 0o755); err != nil {
			return nil, fmt.Errorf("setup spawn working dir: %w", err)
		}
	}

	if err := m.save(child); err != nil {
		return nil, err
	}

	return child, nil
}

// Kill 终止 session（标记为 killed，不删除文件）。
func (m *managerImpl) Kill(sessionID string) error {
	s, err := m.load(sessionID)
	if err != nil {
		return err
	}

	s.Status = StatusKilled
	return m.save(s)
}

// List 列出指定 task 的所有 session。
func (m *managerImpl) List(taskID string) ([]*Session, error) {
	dir := m.resolveSessionDirForTask(taskID)
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("read sessions dir: %w", err)
	}

	var sessions []*Session
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".yaml") {
			continue
		}

		s, err := m.loadFromPath(filepath.Join(dir, entry.Name()))
		if err != nil {
			continue // 跳过损坏的文件
		}
		sessions = append(sessions, s)
	}

	// 按 seq 排序
	sort.Slice(sessions, func(i, j int) bool {
		return sessions[i].Seq < sessions[j].Seq
	})

	return sessions, nil
}

// —————— 持久化 ——————

func (m *managerImpl) save(s *Session) error {
	dir := m.resolveSessionDirForTask(s.TaskID)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("mkdir sessions dir: %w", err)
	}

	data, err := yaml.Marshal(s)
	if err != nil {
		return fmt.Errorf("marshal session: %w", err)
	}

	path := filepath.Join(dir, s.ID+".yaml")
	return os.WriteFile(path, data, 0o644)
}

func (m *managerImpl) load(sessionID string) (*Session, error) {
	// 从 session ID 中提取 task ID: <task-id>-<seq>-<role>
	parts := strings.SplitN(sessionID, "-", 4)
	if len(parts) < 4 {
		return nil, fmt.Errorf("invalid session ID format: %s", sessionID)
	}
	taskID := strings.Join(parts[:3], "-") + "-" + strings.Split(parts[3], "-")[0]
	// 简化：直接用 sessionID 中的 task-id 前缀
	// session ID 格式: <yyyymmdd-HHMMSS-rand4>-<seq>-<role>
	// task ID 格式:  <yyyymmdd-HHMMSS-rand4>
	lastDash := strings.LastIndex(sessionID, "-")
	secondLastDash := strings.LastIndex(sessionID[:lastDash], "-")
	if secondLastDash < 0 {
		return nil, fmt.Errorf("invalid session ID: %s", sessionID)
	}
	taskID = sessionID[:secondLastDash]

	path := filepath.Join(m.resolveSessionDirForTask(taskID), sessionID+".yaml")
	return m.loadFromPath(path)
}

func (m *managerImpl) loadFromPath(path string) (*Session, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read session file: %w", err)
	}

	var s Session
	if err := yaml.Unmarshal(data, &s); err != nil {
		return nil, fmt.Errorf("parse session: %w", err)
	}

	return &s, nil
}

// nextSeq 返回下一个 session 序号。
func (m *managerImpl) nextSeq(taskID string) int {
	sessions, err := m.List(taskID)
	if err != nil || sessions == nil {
		return 1
	}

	maxSeq := 0
	for _, s := range sessions {
		if s.Seq > maxSeq {
			maxSeq = s.Seq
		}
	}
	return maxSeq + 1
}

// —————— dataDir 感知路径 ——————

func (m *managerImpl) resolveSessionDir(sessionID string) string {
	if m.dataDir != "" {
		return filepath.Join(m.dataDir, "sessions", sessionID)
	}
	return filepath.Join(sessionRoot(), sessionID)
}

func (m *managerImpl) resolveSessionDirForTask(taskID string) string {
	if m.dataDir != "" {
		return filepath.Join(m.dataDir, "tasks", taskID, "sessions")
	}
	return sessionDirForTask(taskID)
}

// —————— 全局 helper ——————

// SessionDir 返回指定 session 的 working dir 路径。
func SessionDir(sessionID string) string {
	return filepath.Join(sessionRoot(), sessionID)
}
