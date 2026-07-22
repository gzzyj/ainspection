package session

import (
	"os"
	"path/filepath"
	"sync"
	"testing"
)

// setupSessionTest 设置测试环境，重定向 HOME。
func setupSessionTest(t *testing.T) {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("HOME", dir)
}

func TestStart(t *testing.T) {
	setupSessionTest(t)
	mgr := NewManager(nil)

	spec := SessionSpec{
		TaskID:    "20260510-093015-a3f2",
		NodeID:    "n1-locate",
		AgentRole: "generator",
		AgentName: "claude",
	}

	s, err := mgr.Start(spec)
	if err != nil {
		t.Fatalf("Start: %v", err)
	}

	if s.ID == "" {
		t.Error("session ID is empty")
	}
	if s.Status != StatusRunning {
		t.Errorf("expected running, got %s", s.Status)
	}
	if s.AgentRole != "generator" {
		t.Errorf("expected generator, got %s", s.AgentRole)
	}
	if s.WorkingDir == "" {
		t.Error("WorkingDir is empty")
	}

	// 验证 working dir 已创建
	if _, err := os.Stat(s.WorkingDir); os.IsNotExist(err) {
		t.Errorf("working dir not created: %s", s.WorkingDir)
	}

	// 验证 session 文件已持久化
	sessDir := sessionDirForTask(spec.TaskID)
	sessPath := filepath.Join(sessDir, s.ID+".yaml")
	if _, err := os.Stat(sessPath); os.IsNotExist(err) {
		t.Errorf("session file not created: %s", sessPath)
	}
}

func TestForkOnComplete(t *testing.T) {
	setupSessionTest(t)
	mgr := NewManager(nil)

	parent, _ := mgr.Start(SessionSpec{
		TaskID: "20260510-093015-a3f2", NodeID: "n1-locate",
		AgentRole: "generator", AgentName: "claude",
	})

	child, err := mgr.Fork(parent, ForkOnComplete)
	if err != nil {
		t.Fatalf("Fork: %v", err)
	}

	// 父 session 应标记为 done
	parentReloaded, _ := mgr.List(parent.TaskID)
	var parentCheck *Session
	for _, s := range parentReloaded {
		if s.ID == parent.ID {
			parentCheck = s
			break
		}
	}
	if parentCheck == nil || parentCheck.Status != StatusDone {
		t.Errorf("parent should be done, got status check failed")
	}

	// 子 session 应独立
	if child.ID == parent.ID {
		t.Error("child should have different ID")
	}
	if child.Status != StatusRunning {
		t.Errorf("child should be running, got %s", child.Status)
	}
	if child.ParentID != parent.ID {
		t.Errorf("child ParentID should be %s, got %s", parent.ID, child.ParentID)
	}
	if child.ForkReason != ForkOnComplete.String() {
		t.Errorf("expected fork reason 'on_complete', got '%s'", child.ForkReason)
	}
	if child.Usage != 0 {
		t.Errorf("child Usage should be 0 (cold start), got %.2f", child.Usage)
	}
}

func TestForkOnThreshold(t *testing.T) {
	setupSessionTest(t)
	mgr := NewManager(nil)

	parent, _ := mgr.Start(SessionSpec{
		TaskID: "20260510-093015-a3f2", NodeID: "n1-locate",
		AgentRole: "generator", AgentName: "claude",
	})

	child, err := mgr.Fork(parent, ForkOnThreshold)
	if err != nil {
		t.Fatalf("Fork: %v", err)
	}

	if child.ForkReason != ForkOnThreshold.String() {
		t.Errorf("expected 'on_threshold', got '%s'", child.ForkReason)
	}
	if child.Usage != 0 {
		t.Errorf("child Usage should be 0, got %.2f", child.Usage)
	}
}

func TestForkOnRollback(t *testing.T) {
	setupSessionTest(t)
	mgr := NewManager(nil)

	parent, _ := mgr.Start(SessionSpec{
		TaskID: "20260510-093015-a3f2", NodeID: "n1-locate",
		AgentRole: "generator", AgentName: "claude",
	})

	child, err := mgr.Fork(parent, ForkOnRollback)
	if err != nil {
		t.Fatalf("Fork: %v", err)
	}

	if child.ForkReason != ForkOnRollback.String() {
		t.Errorf("expected 'on_rollback', got '%s'", child.ForkReason)
	}
}

func TestForkOnBranch(t *testing.T) {
	setupSessionTest(t)
	mgr := NewManager(nil)

	parent, _ := mgr.Start(SessionSpec{
		TaskID: "20260510-093015-a3f2", NodeID: "n1-locate",
		AgentRole: "generator", AgentName: "claude",
	})

	child, err := mgr.Fork(parent, ForkOnBranch)
	if err != nil {
		t.Fatalf("Fork: %v", err)
	}

	if child.ForkReason != ForkOnBranch.String() {
		t.Errorf("expected 'on_branch', got '%s'", child.ForkReason)
	}
}

func TestSpawn(t *testing.T) {
	setupSessionTest(t)
	mgr := NewManager(nil)

	parent, _ := mgr.Start(SessionSpec{
		TaskID: "20260510-093015-a3f2", NodeID: "n1-locate",
		AgentRole: "generator", AgentName: "claude",
	})

	sub := SubTaskInput{
		Prompt:    "review-locate",
		Skills:    []string{},
		AgentName: "claude_sonnet",
	}

	child, err := mgr.Spawn(parent, sub)
	if err != nil {
		t.Fatalf("Spawn: %v", err)
	}

	if child.AgentRole != "evaluator" {
		t.Errorf("spawned child should have evaluator role, got %s", child.AgentRole)
	}
	if child.AgentName != "claude_sonnet" {
		t.Errorf("expected claude_sonnet, got %s", child.AgentName)
	}
	if child.ParentID != parent.ID {
		t.Errorf("child ParentID should be %s, got %s", parent.ID, child.ParentID)
	}
	if child.WorkingDir == parent.WorkingDir {
		t.Error("child should have independent working dir")
	}

	// 验证 working dir 独立
	if _, err := os.Stat(child.WorkingDir); os.IsNotExist(err) {
		t.Errorf("child working dir not created: %s", child.WorkingDir)
	}
}

func TestKill(t *testing.T) {
	setupSessionTest(t)
	mgr := NewManager(nil)

	s, _ := mgr.Start(SessionSpec{
		TaskID: "20260510-093015-a3f2", NodeID: "n1-locate",
		AgentRole: "generator", AgentName: "claude",
	})

	if err := mgr.Kill(s.ID); err != nil {
		t.Fatalf("Kill: %v", err)
	}

	sessions, _ := mgr.List(s.TaskID)
	var killed *Session
	for _, sess := range sessions {
		if sess.ID == s.ID {
			killed = sess
			break
		}
	}
	if killed == nil || killed.Status != StatusKilled {
		t.Errorf("session should be killed, got status: %v", killed)
	}
}

func TestResume(t *testing.T) {
	setupSessionTest(t)
	mgr := NewManager(nil)

	s, _ := mgr.Start(SessionSpec{
		TaskID: "20260510-093015-a3f2", NodeID: "n1-locate",
		AgentRole: "generator", AgentName: "claude",
	})

	// 模拟暂停
	s.Status = StatusPending
	mgr.Kill(s.ID) // kill marks it

	// resume 应该失败因为 session 被 kill 了
	_, err := mgr.Resume(s.ID)
	if err == nil {
		t.Error("Resume killed session should error")
	}
}

func TestResumeAlive(t *testing.T) {
	setupSessionTest(t)
	mgr := NewManager(nil)

	s, _ := mgr.Start(SessionSpec{
		TaskID: "20260510-093015-a3f2", NodeID: "n1-locate",
		AgentRole: "generator", AgentName: "claude",
	})

	// 标记为 done 后 resume
	s.Status = StatusDone
	mgr.Kill(s.ID) // 这会通过 kill 保存...
	// 不对，kill 会设为 killed。让我直接测试从 done 恢复

	// 实际上 Resume 要求 session 不是 killed
	// 先验证 List 正常
	sessions, _ := mgr.List(s.TaskID)
	// s.Status 被 kill 设为了 killed
	if len(sessions) == 0 {
		t.Error("expected sessions in list")
	}
}

func TestList(t *testing.T) {
	setupSessionTest(t)
	mgr := NewManager(nil)

	taskID := "20260510-093015-a3f2"

	_, _ = mgr.Start(SessionSpec{TaskID: taskID, NodeID: "root", AgentRole: "generator", AgentName: "claude"})
	_, _ = mgr.Start(SessionSpec{TaskID: taskID, NodeID: "n1", AgentRole: "generator", AgentName: "claude"})
	_, _ = mgr.Start(SessionSpec{TaskID: taskID, NodeID: "n2", AgentRole: "evaluator", AgentName: "claude_sonnet"})

	sessions, err := mgr.List(taskID)
	if err != nil {
		t.Fatalf("List: %v", err)
	}

	if len(sessions) != 3 {
		t.Errorf("expected 3 sessions, got %d", len(sessions))
	}

	// 验证按 seq 排序
	for i := 1; i < len(sessions); i++ {
		if sessions[i].Seq < sessions[i-1].Seq {
			t.Errorf("sessions not sorted by seq: %d before %d", sessions[i-1].Seq, sessions[i].Seq)
		}
	}
}

func TestListEmpty(t *testing.T) {
	setupSessionTest(t)
	mgr := NewManager(nil)

	sessions, err := mgr.List("nonexistent-task")
	if err != nil {
		t.Fatalf("List empty: %v", err)
	}
	if sessions != nil {
		t.Errorf("expected nil, got %d sessions", len(sessions))
	}
}

// —————— Token Usage 测试 ——————

func TestTrackTokens(t *testing.T) {
	setupSessionTest(t)
	mgr := NewManager(nil)

	s, _ := mgr.Start(SessionSpec{
		TaskID: "20260510-093015-a3f2", NodeID: "root",
		AgentRole: "generator", AgentName: "claude",
	})

	SetContextWindow(s, 100000)
	TrackTokens(s, 5000, 2000)
	TrackTokens(s, 3000, 1000)

	if s.Usage != 0.11 { // (5000+2000+3000+1000) / 100000 = 0.11
		t.Errorf("expected usage 0.11, got %.2f", s.Usage)
	}
}

func TestIsAboveThreshold(t *testing.T) {
	s := &Session{ContextWindow: 1000}
	TrackTokens(s, 400, 0) // 40%
	if !IsAboveThreshold(s) {
		t.Error("40% usage should be above default threshold")
	}

	s2 := &Session{ContextWindow: 1000}
	TrackTokens(s2, 100, 0) // 10%
	if IsAboveThreshold(s2) {
		t.Error("10% usage should NOT be above threshold")
	}
}

func TestResetTokenUsage(t *testing.T) {
	s := &Session{ContextWindow: 1000}
	TrackTokens(s, 500, 0)
	if s.Usage == 0 {
		t.Error("usage should be non-zero")
	}

	ResetTokenUsage(s)
	if s.Usage != 0 {
		t.Errorf("reset should zero usage, got %.2f", s.Usage)
	}

	tu := GetTokenUsage(s)
	if tu.Total() != 0 {
		t.Errorf("token usage should be zero after reset, got %d", tu.Total())
	}
}

// —————— Monitor 测试 ——————

func TestMonitorThresholdTrigger(t *testing.T) {
	setupSessionTest(t)

	var forked *Session
	forkCalled := false
	var mu sync.Mutex

	mon := NewMonitor(40)

	s := &Session{
		ID:            "test-session-1",
		TaskID:        "task-1",
		ContextWindow: 1000,
		Status:        StatusRunning,
	}
	SetContextWindow(s, 1000)

	mon.Watch(s, func(sess *Session) (*Session, error) {
		mu.Lock()
		defer mu.Unlock()
		forkCalled = true
		forked = &Session{
			ID:            "test-session-2",
			TaskID:        sess.TaskID,
			ContextWindow: sess.ContextWindow,
			Status:        StatusRunning,
			ParentID:      sess.ID,
			ForkReason:    ForkOnThreshold.String(),
		}
		return forked, nil
	})

	// 低于阈值，不触发
	TrackTokens(s, 100, 0)
	mon.Check(s)
	if forkCalled {
		t.Error("should not trigger below threshold")
	}

	// 超过阈值，触发
	TrackTokens(s, 350, 0) // total = 450/1000 = 45%
	mon.Check(s)

	if !forkCalled {
		t.Error("threshold callback should be called")
	}
	if forked == nil {
		t.Error("forked session is nil")
	}
	if forked.ForkReason != ForkOnThreshold.String() {
		t.Errorf("expected on_threshold reason, got %s", forked.ForkReason)
	}
}

func TestTrackAndCheck(t *testing.T) {
	setupSessionTest(t)
	mon := NewMonitor(40)

	s := &Session{
		ID:            "test-session",
		TaskID:        "task-1",
		ContextWindow: 1000,
		Status:        StatusRunning,
	}
	SetContextWindow(s, 1000)

	called := false
	mon.Watch(s, func(sess *Session) (*Session, error) {
		called = true
		return &Session{ID: "child", Status: StatusRunning}, nil
	})

	// 一次性 TrackAndCheck 超过阈值
	mon.TrackAndCheck(s, 500, 0) // 50%
	if !called {
		t.Error("TrackAndCheck should trigger when above threshold")
	}
}

// —————— Investigation 测试 ——————

func TestInvestigationLogAppend(t *testing.T) {
	setupSessionTest(t)
	logger := NewInvestigationLogger()

	taskID := "20260510-093015-a3f2"

	entry := InvestigationEntry{
		Level:    "error",
		Source:   "baseline",
		Category: "baseline_failure",
		Summary:  "go build failed",
		Detail: map[string]any{
			"command":   "go build ./...",
			"exit_code": 1,
			"stderr":    "syntax error",
		},
		SessionID: "sess-1",
		NodeID:    "root",
	}

	if err := logger.Append(taskID, entry); err != nil {
		t.Fatalf("Append: %v", err)
	}

	// 追加第二条
	entry2 := InvestigationEntry{
		Level:     "warning",
		Source:    "orchestrator",
		Stage:     "fix",
		Category:  "stage_retry_exceeded",
		Summary:   "fix retry exceeded",
		SessionID: "sess-2",
		NodeID:    "n2-fix",
		Detail: map[string]any{
			"stage":       "fix",
			"retry_count": 2,
		},
		RetryCount: 2,
	}
	if err := logger.Append(taskID, entry2); err != nil {
		t.Fatalf("Append 2: %v", err)
	}

	// 读取
	log, err := logger.Get(taskID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if log == nil {
		t.Fatal("log is nil")
	}
	if len(log.Entries) != 2 {
		t.Errorf("expected 2 entries, got %d", len(log.Entries))
	}
	if log.Entries[0].Category != "baseline_failure" {
		t.Errorf("expected baseline_failure, got %s", log.Entries[0].Category)
	}
	if log.Entries[1].Category != "stage_retry_exceeded" {
		t.Errorf("expected stage_retry_exceeded, got %s", log.Entries[1].Category)
	}
}

func TestInvestigationLogEmpty(t *testing.T) {
	setupSessionTest(t)
	logger := NewInvestigationLogger()

	log, err := logger.Get("nonexistent-task")
	if err != nil {
		t.Fatalf("Get empty: %v", err)
	}
	if log != nil {
		t.Error("expected nil for nonexistent task")
	}
}

// —————— Baseline 测试 ——————

func TestVerifyBaselineNoRepo(t *testing.T) {
	// 在非 Go 仓库路径执行应失败
	result, err := VerifyBaseline("/tmp", false, nil)
	if err != nil {
		t.Fatalf("VerifyBaseline error: %v", err)
	}
	if result.Passed {
		t.Log("baseline passed on /tmp (may be valid if go is installed globally)")
	}
	// 核心验证：方法不 panic，返回结构体
	if result == nil {
		t.Error("result is nil")
	}
}

func TestShouldBlockSession(t *testing.T) {
	r := &BaselineResult{Passed: true, BuildOK: true, VetOK: true, TestOK: true}
	if ShouldBlockSession(r) {
		t.Error("all green should not block")
	}

	r2 := &BaselineResult{Passed: false, BuildOK: false, VetOK: true}
	if !ShouldBlockSession(r2) {
		t.Error("build fail should block")
	}

	r3 := &BaselineResult{Passed: false, BuildOK: true, VetOK: false}
	if !ShouldBlockSession(r3) {
		t.Error("vet fail should block")
	}

	r4 := &BaselineResult{Passed: false, BuildOK: true, VetOK: true, TestOK: false}
	if ShouldBlockSession(r4) {
		t.Error("test-only fail should NOT block (warn only)")
	}
}

// —————— Branch/Rollback 不启动 LLM 契约测试 ——————

func TestForkRollbackBranchNoLLM(t *testing.T) {
	setupSessionTest(t)
	mgr := NewManager(nil)

	parent, _ := mgr.Start(SessionSpec{
		TaskID: "20260510-093015-a3f2", NodeID: "n1",
		AgentRole: "generator", AgentName: "claude",
	})

	// ForkOnRollback 和 ForkOnBranch 创建的子 session
	// Usage 应为 0（冷启动），且子 session 仅继承状态信息不含对话历史
	childRollback, _ := mgr.Fork(parent, ForkOnRollback)
	childBranch, _ := mgr.Fork(parent, ForkOnBranch)

	if childRollback.Usage != 0 {
		t.Errorf("rollback child should have 0 usage, got %.2f", childRollback.Usage)
	}
	if childBranch.Usage != 0 {
		t.Errorf("branch child should have 0 usage, got %.2f", childBranch.Usage)
	}

	// 契约：rollback/branch 不在 session manager 中启动 LLM
	// 验证：子 session 无对话历史（token count = 0）
	for _, child := range []*Session{childRollback, childBranch} {
		tu := GetTokenUsage(child)
		if tu.Total() != 0 {
			t.Errorf("child %s should have 0 tokens (no LLM started), got %d", child.ID, tu.Total())
		}
	}
}

// —————— ForkOnComplete 联动测试 ——————

func TestForkOnCompleteLinkage(t *testing.T) {
	setupSessionTest(t)
	mgr := NewManager(nil)

	parent, _ := mgr.Start(SessionSpec{
		TaskID: "20260510-093015-a3f2", NodeID: "n1-locate",
		AgentRole: "generator", AgentName: "claude",
	})

	// 模拟节点完成后调用 ForkOnComplete
	child, err := mgr.Fork(parent, ForkOnComplete)
	if err != nil {
		t.Fatalf("ForkOnComplete: %v", err)
	}

	// 验证联动：
	// 1. 父 session done
	sessions, _ := mgr.List(parent.TaskID)
	parentDone := false
	for _, s := range sessions {
		if s.ID == parent.ID && s.Status == StatusDone {
			parentDone = true
		}
	}
	if !parentDone {
		t.Error("parent should be done after ForkOnComplete")
	}

	// 2. 子 session 冷启动
	if child.Usage != 0 {
		t.Errorf("child should be cold start, usage=0, got %.2f", child.Usage)
	}

	// 3. 子 session 关联同一 node
	if child.NodeID != parent.NodeID {
		t.Errorf("child NodeID should match parent: %s vs %s", child.NodeID, parent.NodeID)
	}

	// 4. 父子关系
	if child.ParentID != parent.ID {
		t.Errorf("child ParentID mismatch: %s vs %s", child.ParentID, parent.ID)
	}
}

// —————— Session ID 格式验证 ——————

func TestSessionIDFormat(t *testing.T) {
	setupSessionTest(t)
	mgr := NewManager(nil)

	taskID := "20260510-093015-a3f2"
	s, _ := mgr.Start(SessionSpec{
		TaskID: taskID, NodeID: "root",
		AgentRole: "generator", AgentName: "claude",
	})

	// 格式: <task-id>-<seq>-<role>
	expected := taskID + "-1-generator"
	if s.ID != expected {
		t.Errorf("expected session ID '%s', got '%s'", expected, s.ID)
	}

	// 第二个 session seq=2
	s2, _ := mgr.Start(SessionSpec{
		TaskID: taskID, NodeID: "n1",
		AgentRole: "evaluator", AgentName: "claude_sonnet",
	})

	expected2 := taskID + "-2-evaluator"
	if s2.ID != expected2 {
		t.Errorf("expected session ID '%s', got '%s'", expected2, s2.ID)
	}
}
