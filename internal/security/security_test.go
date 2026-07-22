package security

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"git.qingteng.cn/ms/ainspection/internal/adapter"
)

// —————— CommandExecutor 测试 ——————

func defaultExecutor() CommandExecutor {
	allowed := []CommandRule{
		{Pattern: "git (status|diff|log|branch|checkout|add|commit)", AutoApprove: true},
		{Pattern: "git push", AutoApprove: false},
		{Pattern: "go (build|vet|test|mod) .*", AutoApprove: true},
		{Pattern: "golangci-lint run .*", AutoApprove: true},
		{Pattern: "skaffold (build|deploy|run)", AutoApprove: false},
		{Pattern: "kubectl (get|describe|logs|port-forward) .*", AutoApprove: true},
		{Pattern: "kubectl (apply|delete|edit|exec) .*", AutoApprove: false},
	}
	blocked := []string{
		`.*rm\s+-rf.*`,
		`.*\|.*`,
		`.*&&.*`,
		`.*\$\(.*`,
	}

	return NewCommandExecutor(allowed, blocked, 0)
}

func TestCommandAllowed(t *testing.T) {
	exec := defaultExecutor()

	dir := t.TempDir()
	result, err := exec.Execute(context.Background(), "go build ./...", dir)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !result.Allowed {
		t.Errorf("go build should be allowed, blocked_reason=%s", result.BlockedReason)
	}
	if result.NeedsApproval {
		t.Error("go build should not need approval")
	}
}

func TestCommandNeedsApproval(t *testing.T) {
	exec := defaultExecutor()

	result, err := exec.Execute(context.Background(), "git push", "/tmp")
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !result.Allowed {
		t.Error("git push should be allowed (needs approval)")
	}
	if !result.NeedsApproval {
		t.Error("git push should need approval")
	}
}

func TestCommandBlocked(t *testing.T) {
	exec := defaultExecutor()

	// echo 不在白名单中
	result, err := exec.Execute(context.Background(), "echo hello", "/tmp")
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if result.Allowed {
		t.Error("echo should not be allowed")
	}
}

func TestCommandBlockedPattern(t *testing.T) {
	exec := defaultExecutor()

	result, err := exec.Execute(context.Background(), "rm -rf /tmp/xxx", "/tmp")
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if result.Allowed {
		t.Error("rm -rf should be blocked")
	}
}

func TestCommandGoVet(t *testing.T) {
	exec := defaultExecutor()

	result, err := exec.Execute(context.Background(), "go vet ./...", "/tmp")
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !result.Allowed {
		t.Errorf("go vet should be allowed: %s", result.BlockedReason)
	}
}

func TestCommandSkaffoldDeployNeedsApproval(t *testing.T) {
	exec := defaultExecutor()

	result, err := exec.Execute(context.Background(), "skaffold deploy", "/tmp")
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !result.Allowed {
		t.Error("skaffold deploy should be allowed with approval")
	}
	if !result.NeedsApproval {
		t.Error("skaffold deploy should need approval")
	}
}

func TestCommandExecutionFailure(t *testing.T) {
	exec := defaultExecutor()

	// 执行一个会失败的命令（不存在的路径）
	result, err := exec.Execute(context.Background(), "go build ./nonexistent", t.TempDir())
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	// 命令通过了安全策略，但执行本身可能失败
	if !result.Allowed {
		t.Error("command should pass security check")
	}
	if result.NeedsApproval {
		t.Error("go build should not need approval")
	}
}

func TestCommandEmpty(t *testing.T) {
	exec := defaultExecutor()
	result, err := exec.Execute(context.Background(), "   ", "/tmp")
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if result.Allowed {
		t.Error("empty command should not be allowed")
	}
}

// —————— FSGuard 测试 ——————

func TestFSGuardAllowedRead(t *testing.T) {
	dir := t.TempDir()
	guard := NewFSGuard(
		[]string{dir}, // allowed_read
		[]string{dir}, // allowed_write
		nil,
	)

	// 在允许目录内的文件
	testFile := filepath.Join(dir, "test.txt")
	os.WriteFile(testFile, []byte("hello"), 0o644)

	resolved, err := guard.Resolve("t1", "s1", testFile, OpRead)
	if err != nil {
		t.Errorf("should allow read within bounds: %v", err)
	}
	if resolved == "" {
		t.Error("resolved path is empty")
	}
}

func TestFSGuardBlockedWrite(t *testing.T) {
	dir := t.TempDir()
	guard := NewFSGuard(
		[]string{dir},
		[]string{dir}, // only dir allowed
		nil,
	)

	// 写入 /tmp 下的其他位置
	otherDir := filepath.Join(t.TempDir(), "other")
	os.MkdirAll(otherDir, 0o755)

	_, err := guard.Resolve("t1", "s1", otherDir, OpWrite)
	if err == nil {
		t.Error("should block write outside allowed paths")
	}
}

func TestFSGuardSystemBlocked(t *testing.T) {
	guard := NewFSGuard(
		[]string{"/etc/"},
		[]string{"/etc/"},
		nil,
	)

	_, err := guard.Resolve("t1", "s1", "/etc/passwd", OpRead)
	if err == nil {
		t.Error("should block system path even if in allowed list")
	}
}

func TestFSGuardGitDirBlocked(t *testing.T) {
	td := t.TempDir()
	guard := NewFSGuard(
		[]string{td},
		[]string{td},
		nil,
	)

	_, err := guard.Resolve("t1", "s1", filepath.Join(td, ".git", "config"), OpRead)
	if err == nil {
		t.Error("should block .git directory access")
	}
}

func TestFSGuardWithinAllowed(t *testing.T) {
	dir := t.TempDir()
	subDir := filepath.Join(dir, "sub", "deep")
	os.MkdirAll(subDir, 0o755)

	guard := NewFSGuard(
		[]string{dir},
		[]string{dir},
		nil,
	)

	resolved, err := guard.Resolve("t1", "s1", subDir, OpRead)
	if err != nil {
		t.Fatalf("subdirectory access: %v", err)
	}
	if resolved == "" {
		t.Error("resolved empty")
	}
}

func TestFSGuardRepoPath(t *testing.T) {
	repo1 := filepath.Join(t.TempDir(), "repo1")
	repo2 := filepath.Join(t.TempDir(), "repo2")
	os.MkdirAll(repo1, 0o755)
	os.MkdirAll(repo2, 0o755)

	guard := NewFSGuard(
		[]string{repo1, repo2},
		[]string{repo1, repo2},
		[]string{repo1, repo2},
	)

	// repo1 内部应允许
	resolved, err := guard.Resolve("t1", "s1", filepath.Join(repo1, "src", "main.go"), OpRead)
	if err != nil {
		t.Errorf("repo1 access should be allowed: %v", err)
	}
	_ = resolved
}

func TestFSGuardSessionPath(t *testing.T) {
	guard := NewFSGuard(
		[]string{"~/.ainspection/sessions/<session-id>/"},
		[]string{"~/.ainspection/sessions/<session-id>/"},
		nil,
	)

	resolved, err := guard.Resolve("t1", "sess-abc", "~/.ainspection/sessions/sess-abc/scratch", OpRead)
	if err != nil {
		t.Errorf("session path should be allowed: %v", err)
	}
	if !strings.Contains(resolved, "sess-abc") {
		t.Errorf("resolved path should contain session id: %s", resolved)
	}
}

// —————— Sandbox 测试 ——————

func TestSandboxSetup(t *testing.T) {
	dir := t.TempDir()
	sb := NewSandboxSimple(dir, 7)

	workingDir, err := sb.SetupSession(context.Background(), "session-test-1", "", adapter.AgentSetupConfig{})
	if err != nil {
		t.Fatalf("SetupSession: %v", err)
	}

	// 验证目录结构
	for _, sub := range []string{"input", "output", "patches", "signals", "scratch"} {
		subDir := filepath.Join(workingDir, sub)
		if _, err := os.Stat(subDir); os.IsNotExist(err) {
			t.Errorf("subdirectory %s missing", sub)
		}
	}
}

func TestSandboxCleanup(t *testing.T) {
	dir := t.TempDir()
	sb := NewSandboxSimple(dir, 7)

	workingDir, _ := sb.SetupSession(context.Background(), "session-test-2", "", adapter.AgentSetupConfig{})

	if err := sb.CleanupSession("session-test-2"); err != nil {
		t.Fatalf("CleanupSession: %v", err)
	}

	// 验证 retention 文件存在
	retentionFile := filepath.Join(workingDir, ".retention")
	if _, err := os.Stat(retentionFile); os.IsNotExist(err) {
		t.Error(".retention file missing after cleanup")
	}
}

func TestSandboxCleanupIdempotent(t *testing.T) {
	dir := t.TempDir()
	sb := NewSandboxSimple(dir, 7)

	// 不存在的 session，cleanup 应不报错
	if err := sb.CleanupSession("nonexistent-session"); err != nil {
		t.Errorf("cleanup nonexistent should be no-op: %v", err)
	}
}

func TestSandboxDefaultRoot(t *testing.T) {
	sb := NewSandboxSimple("", 7)
	workingDir, err := sb.SetupSession(context.Background(), "test-default", "", adapter.AgentSetupConfig{})
	if err != nil {
		t.Fatalf("SetupSession default root: %v", err)
	}

	if !strings.Contains(workingDir, ".ainspection") {
		t.Errorf("default root should contain .ainspection: %s", workingDir)
	}

	// clean up
	sb.CleanupSession("test-default")
}

func TestSandboxSessionDir(t *testing.T) {
	sb := &sandboxImpl{root: "/tmp/sandboxes"}
	dir := sb.SessionDir("sess-1")
	expected := "/tmp/sandboxes/sess-1"
	if dir != expected {
		t.Errorf("expected %s, got %s", expected, dir)
	}
}

// —————— Audit 测试 ——————

func TestAuditAppend(t *testing.T) {
	dir := t.TempDir()
	logger := NewAuditLogger(dir, 30)

	rec := Record{
		Component: "command_executor",
		Action:    "exec",
		TaskID:    "task-1",
		SessionID: "sess-1",
		Agent:     "claude",
		Result:    "approved",
		Args:      map[string]any{"cmd": "go build ./..."},
	}

	if err := logger.Append(rec); err != nil {
		t.Fatalf("Append: %v", err)
	}

	// 验证文件存在且包含记录
	files, err := filepath.Glob(filepath.Join(dir, "*.jsonl"))
	if err != nil || len(files) == 0 {
		t.Fatal("audit file not created")
	}

	data, err := os.ReadFile(files[0])
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "command_executor") {
		t.Error("audit file should contain record")
	}
	if !strings.Contains(string(data), "go build") {
		t.Error("audit file should contain command")
	}
}

func TestAuditMultipleRecords(t *testing.T) {
	dir := t.TempDir()
	logger := NewAuditLogger(dir, 30)

	for i := 0; i < 5; i++ {
		logger.Append(NewExecRecord("t1", "s1", "claude", "go build", "approved", ""))
	}

	files, _ := filepath.Glob(filepath.Join(dir, "*.jsonl"))
	data, _ := os.ReadFile(files[0])
	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	if len(lines) != 5 {
		t.Errorf("expected 5 lines, got %d", len(lines))
	}
}

func TestAuditBlockedRecord(t *testing.T) {
	dir := t.TempDir()
	logger := NewAuditLogger(dir, 30)

	logger.Append(NewExecRecord("t1", "s1", "claude", "rm -rf /tmp/*", "blocked", "blocked_pattern"))

	files, _ := filepath.Glob(filepath.Join(dir, "*.jsonl"))
	data, _ := os.ReadFile(files[0])
	if !strings.Contains(string(data), "blocked") {
		t.Error("should contain blocked result")
	}
	if !strings.Contains(string(data), "rm -rf") {
		t.Error("should contain blocked command")
	}
}

func TestAuditFSRecord(t *testing.T) {
	dir := t.TempDir()
	logger := NewAuditLogger(dir, 30)

	logger.Append(NewFSRecord("t1", "s1", "claude", "/tmp/test", OpRead, "approved", ""))
	logger.Append(NewFSRecord("t1", "s1", "claude", "/etc/passwd", OpRead, "blocked", "system path"))

	files, _ := filepath.Glob(filepath.Join(dir, "*.jsonl"))
	data, _ := os.ReadFile(files[0])

	if !strings.Contains(string(data), "approved") || !strings.Contains(string(data), "blocked") {
		t.Error("should contain both approved and blocked fs records")
	}
}

func TestAuditInjectRecord(t *testing.T) {
	dir := t.TempDir()
	logger := NewAuditLogger(dir, 30)

	logger.Append(NewInjectRecord("t1", "s1", "claude_sonnet", 11, "approved"))

	files, _ := filepath.Glob(filepath.Join(dir, "*.jsonl"))
	data, _ := os.ReadFile(files[0])

	if !strings.Contains(string(data), "skill_injector") {
		t.Error("should contain skill_injector component")
	}
	if !strings.Contains(string(data), "11") {
		t.Error("should contain skill count")
	}
}

func TestAuditDefaultDir(t *testing.T) {
	logger := NewAuditLogger("", 30)
	// 不 panic 就是成功
	_ = logger
}

// —————— 并发安全 测试 ——————

func TestAuditConcurrent(t *testing.T) {
	dir := t.TempDir()
	logger := NewAuditLogger(dir, 30)

	done := make(chan bool, 20)
	for i := 0; i < 20; i++ {
		go func(id int) {
			logger.Append(NewExecRecord("t1", "s1", "claude", "go build", "approved", ""))
			done <- true
		}(i)
	}

	for i := 0; i < 20; i++ {
		<-done
	}

	files, _ := filepath.Glob(filepath.Join(dir, "*.jsonl"))
	data, _ := os.ReadFile(files[0])
	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	if len(lines) != 20 {
		t.Errorf("concurrent: expected 20 lines, got %d", len(lines))
	}
}

// —————— OpType 测试 ——————

func TestOpTypeString(t *testing.T) {
	if OpRead.String() != "read" {
		t.Errorf("expected read, got %s", OpRead.String())
	}
	if OpWrite.String() != "write" {
		t.Errorf("expected write, got %s", OpWrite.String())
	}
}

// —————— Audit retention 清理测试 ——————

func TestAuditRetentionCleanup(t *testing.T) {
	dir := t.TempDir()

	// 创建一些"旧"日志文件（通过手动设置文件名日期）
	oldDate := "20200101" // 很久以前的日期
	oldPath := filepath.Join(dir, oldDate+".jsonl")
	if err := os.WriteFile(oldPath, []byte(`{"component":"test"}`+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	// 创建今天的日志文件
	today := "20260101" // 模拟今天
	todayPath := filepath.Join(dir, today+".jsonl")
	if err := os.WriteFile(todayPath, []byte(`{"component":"test"}`+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	// 创建 audit logger（cleanupOldFiles 在 NewAuditLogger 中执行）
	// retention = 365 天，所以今天的文件不会被清理，但 2020 年的会被清理
	logger := NewAuditLogger(dir, 365)
	_ = logger.Append(Record{Component: "test", Action: "verify"})

	// 旧文件应被清理
	if _, err := os.Stat(oldPath); !os.IsNotExist(err) {
		t.Error("old audit file (2020) should have been cleaned up")
	}

	// 今天的文件应保留
	if _, err := os.Stat(todayPath); err != nil {
		t.Error("today's audit file should be kept")
	}
}

// —————— Sandbox Compress 测试 ——————

func TestSandboxCompressSession(t *testing.T) {
	dir := t.TempDir()
	sb := NewSandboxSimple(dir, 7)

	sessionID := "test-compress-session"

	// 创建 session 目录和文件
	workingDir, err := sb.SetupSession(context.Background(), sessionID, "", adapter.AgentSetupConfig{})
	if err != nil {
		t.Fatalf("SetupSession: %v", err)
	}

	// 创建一些测试文件
	_ = os.WriteFile(filepath.Join(workingDir, "scratch", "test.txt"), []byte("hello"), 0o600)

	// 压缩 session
	if err := sb.CompressSession(sessionID); err != nil {
		t.Fatalf("CompressSession: %v", err)
	}

	// 验证压缩文件存在
	archivePath := filepath.Join(dir, sessionID+".tar.gz")
	if _, err := os.Stat(archivePath); os.IsNotExist(err) {
		t.Error("archive file should exist after compress")
	}

	// 验证源目录已删除
	if _, err := os.Stat(workingDir); !os.IsNotExist(err) {
		t.Error("session dir should be removed after compress")
	}
}

func TestSandboxCompressNonexistent(t *testing.T) {
	dir := t.TempDir()
	sb := NewSandboxSimple(dir, 7)

	// 压缩不存在的 session 不应报错
	if err := sb.CompressSession("nonexistent"); err != nil {
		t.Errorf("CompressSession on nonexistent should not error: %v", err)
	}
}
