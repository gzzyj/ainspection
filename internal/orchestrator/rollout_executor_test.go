package orchestrator

import (
	"context"
	"testing"

	"git.qingteng.cn/ms/ainspection/internal/mcts"
	"git.qingteng.cn/ms/ainspection/internal/security"
)

// —————— mock 实现 ——————

// mockCommandExecutor 实现 security.CommandExecutor，用于测试。
type mockCommandExecutor struct {
	execResult *security.ExecResult
	execErr    error
	executed   []string // 记录执行的命令
}

func (m *mockCommandExecutor) Execute(ctx context.Context, cmd string, workingDir string) (*security.ExecResult, error) {
	m.executed = append(m.executed, cmd)
	if m.execErr != nil {
		return nil, m.execErr
	}
	return m.execResult, nil
}

// mockAuditLogger 实现 security.AuditLogger，用于测试。
type mockAuditLogger struct {
	records []security.Record
}

func (m *mockAuditLogger) Append(record security.Record) error {
	m.records = append(m.records, record)
	return nil
}

// —————— 构造函数测试 ——————

func TestNewRolloutExecutor(t *testing.T) {
	cmdExec := &mockCommandExecutor{}
	audit := &mockAuditLogger{}

	exec := NewRolloutExecutor(cmdExec, audit,
		[]LocateAction{LocateStaticAnalysis},
		[]FixAction{FixTestRun},
		3, 15,
	)

	if exec.maxRealSteps != 3 {
		t.Errorf("expected maxRealSteps=3, got %d", exec.maxRealSteps)
	}
	if exec.perActionTimeoutS != 15 {
		t.Errorf("expected perActionTimeoutS=15, got %d", exec.perActionTimeoutS)
	}
	if !exec.IsLocateEnabled(LocateStaticAnalysis) {
		t.Error("expected StaticAnalysis to be enabled")
	}
	if exec.IsLocateEnabled(LocateDynamicProbe) {
		t.Error("expected DynamicProbe to be disabled")
	}
	if !exec.IsFixEnabled(FixTestRun) {
		t.Error("expected TestRun to be enabled")
	}
	if exec.IsFixEnabled(FixMutation) {
		t.Error("expected Mutation to be disabled")
	}
}

func TestNewRolloutExecutorDefaults(t *testing.T) {
	exec := NewRolloutExecutor(nil, nil, nil, nil, 0, 0)
	if exec.maxRealSteps != 2 {
		t.Errorf("default maxRealSteps: got %d, want 2", exec.maxRealSteps)
	}
	if exec.perActionTimeoutS != 30 {
		t.Errorf("default perActionTimeoutS: got %d, want 30", exec.perActionTimeoutS)
	}
}

// —————— ExecuteLocateAction 测试 ——————

func TestExecuteLocateActionStaticAnalysisPass(t *testing.T) {
	cmdExec := &mockCommandExecutor{
		execResult: &security.ExecResult{Allowed: true, ExitCode: 0, Stdout: "ok"},
	}
	audit := &mockAuditLogger{}
	exec := NewRolloutExecutor(cmdExec, audit,
		[]LocateAction{LocateStaticAnalysis}, nil, 2, 30,
	)

	action := LocateRolloutAction{Type: LocateStaticAnalysis}
	impact, output, err := exec.ExecuteLocateAction(context.Background(), action, nil, "/tmp/test")

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if impact != 0.10 {
		t.Errorf("expected impact=0.10 for pass, got %f", impact)
	}
	if output != "ok" {
		t.Errorf("expected output='ok', got '%s'", output)
	}
	if len(audit.records) != 1 {
		t.Fatalf("expected 1 audit record, got %d", len(audit.records))
	}
}

func TestExecuteLocateActionStaticAnalysisFail(t *testing.T) {
	cmdExec := &mockCommandExecutor{
		execResult: &security.ExecResult{Allowed: true, ExitCode: 2, Stderr: "error"},
	}
	audit := &mockAuditLogger{}
	exec := NewRolloutExecutor(cmdExec, audit,
		[]LocateAction{LocateStaticAnalysis}, nil, 2, 30,
	)

	action := LocateRolloutAction{Type: LocateStaticAnalysis}
	impact, _, err := exec.ExecuteLocateAction(context.Background(), action, nil, "/tmp/test")

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if impact != -0.05 {
		t.Errorf("expected impact=-0.05 for fail, got %f", impact)
	}
}

func TestExecuteLocateActionStaticAnalysisBlocked(t *testing.T) {
	cmdExec := &mockCommandExecutor{
		execResult: &security.ExecResult{Allowed: false, BlockedReason: "blocked"},
	}
	exec := NewRolloutExecutor(cmdExec, nil,
		[]LocateAction{LocateStaticAnalysis}, nil, 2, 30,
	)

	action := LocateRolloutAction{Type: LocateStaticAnalysis}
	impact, _, err := exec.ExecuteLocateAction(context.Background(), action, nil, "/tmp/test")

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if impact != -0.05 {
		t.Errorf("expected impact=-0.05 for blocked, got %f", impact)
	}
}

func TestExecuteLocateActionHypothesisPropose(t *testing.T) {
	exec := NewRolloutExecutor(nil, nil,
		[]LocateAction{LocateHypothesisPropose}, nil, 2, 30,
	)

	action := LocateRolloutAction{Type: LocateHypothesisPropose}
	impact, output, err := exec.ExecuteLocateAction(context.Background(), action, nil, "")

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if impact != 0.02 {
		t.Errorf("expected impact=0.02, got %f", impact)
	}
	if output != "hypothesis recorded" {
		t.Errorf("expected output='hypothesis recorded', got '%s'", output)
	}
}

func TestExecuteLocateActionUserQuery(t *testing.T) {
	exec := NewRolloutExecutor(nil, nil, nil, nil, 2, 30)
	action := LocateRolloutAction{Type: LocateUserQuery}
	impact, _, err := exec.ExecuteLocateAction(context.Background(), action, nil, "")

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if impact != 0 {
		t.Errorf("expected impact=0, got %f", impact)
	}
}

// —————— ExecuteFixAction 测试 ——————

func TestExecuteFixActionTestRunPass(t *testing.T) {
	cmdExec := &mockCommandExecutor{
		execResult: &security.ExecResult{Allowed: true, ExitCode: 0, Stdout: "PASS"},
	}
	exec := NewRolloutExecutor(cmdExec, nil, nil,
		[]FixAction{FixTestRun}, 2, 30,
	)

	action := FixRolloutAction{Type: FixTestRun}
	impact, output, err := exec.ExecuteFixAction(context.Background(), action, nil, nil, "/tmp/test")

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if impact != 0.15 {
		t.Errorf("expected impact=0.15 for pass, got %f", impact)
	}
	if output != "PASS" {
		t.Errorf("expected output='PASS', got '%s'", output)
	}
}

func TestExecuteFixActionTestRunFail(t *testing.T) {
	cmdExec := &mockCommandExecutor{
		execResult: &security.ExecResult{Allowed: true, ExitCode: 1, Stderr: "FAIL"},
	}
	exec := NewRolloutExecutor(cmdExec, nil, nil,
		[]FixAction{FixTestRun}, 2, 30,
	)

	action := FixRolloutAction{Type: FixTestRun}
	impact, _, err := exec.ExecuteFixAction(context.Background(), action, nil, nil, "/tmp/test")

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if impact != -0.08 {
		t.Errorf("expected impact=-0.08 for fail, got %f", impact)
	}
}

func TestExecuteFixActionTestRunWithContext(t *testing.T) {
	cmdExec := &mockCommandExecutor{
		execResult: &security.ExecResult{Allowed: true, ExitCode: 0, Stdout: "ok"},
	}
	exec := NewRolloutExecutor(cmdExec, nil, nil,
		[]FixAction{FixTestRun}, 2, 30,
	)

	testCtx := &mcts.TestContext{
		Command:        "go test -run TestFoo ./pkg/...",
		ExpectedOutput: "PASS",
		TimeoutS:       60,
	}
	action := FixRolloutAction{Type: FixTestRun}
	impact, _, err := exec.ExecuteFixAction(context.Background(), action, nil, testCtx, "/tmp/test")

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if impact != 0.15 {
		t.Errorf("expected impact=0.15, got %f", impact)
	}
	// 确认使用了自定义命令而非默认 go test ./...
	if len(cmdExec.executed) != 1 || cmdExec.executed[0] != "go test -run TestFoo ./pkg/..." {
		t.Errorf("expected custom test command, got %v", cmdExec.executed)
	}
}

func TestExecuteFixActionStaticCheckPass(t *testing.T) {
	cmdExec := &mockCommandExecutor{
		execResult: &security.ExecResult{Allowed: true, ExitCode: 0, Stdout: "ok"},
	}
	exec := NewRolloutExecutor(cmdExec, nil, nil,
		[]FixAction{FixStaticCheck}, 2, 30,
	)

	action := FixRolloutAction{Type: FixStaticCheck}
	impact, _, err := exec.ExecuteFixAction(context.Background(), action, nil, nil, "/tmp/test")

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if impact != 0.10 {
		t.Errorf("expected impact=0.10, got %f", impact)
	}
}

func TestExecuteFixActionTemplateInstantiate(t *testing.T) {
	exec := NewRolloutExecutor(nil, nil, nil, nil, 2, 30)
	action := FixRolloutAction{Type: FixTemplateInstantiate}
	impact, output, err := exec.ExecuteFixAction(context.Background(), action, nil, nil, "")

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if impact != 0.08 {
		t.Errorf("expected impact=0.08, got %f", impact)
	}
	if output != "template instantiated" {
		t.Errorf("got output=%s", output)
	}
}

func TestExecuteFixActionUserConfirm(t *testing.T) {
	exec := NewRolloutExecutor(nil, nil, nil, nil, 2, 30)
	action := FixRolloutAction{Type: FixUserConfirm}
	impact, _, err := exec.ExecuteFixAction(context.Background(), action, nil, nil, "")

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if impact != 0 {
		t.Errorf("expected impact=0, got %f", impact)
	}
}

// —————— 动作未启用时回退到模拟 ——————

func TestRolloutSimulatorFallbackWhenNotEnabled(t *testing.T) {
	cmdExec := &mockCommandExecutor{
		execResult: &security.ExecResult{Allowed: true, ExitCode: 0},
	}
	exec := NewRolloutExecutor(cmdExec, nil,
		[]LocateAction{}, // 不启用任何动作
		[]FixAction{},
		2, 30,
	)

	sim := NewRolloutSimulator().WithExecutor(exec)
	result := sim.SimulateLocate(context.Background(), 0.5, 2, nil)

	if result.StepsTaken == 0 {
		t.Error("expected at least 1 step with fallback simulation")
	}
	if result.FinalScore <= 0 {
		t.Error("expected positive final score")
	}
	// 没有命令被执行（因为没启用）
	if len(cmdExec.executed) > 0 {
		t.Errorf("expected no commands executed, got %v", cmdExec.executed)
	}
}

func TestRolloutSimulatorEnabledActionExecutes(t *testing.T) {
	cmdExec := &mockCommandExecutor{
		execResult: &security.ExecResult{Allowed: true, ExitCode: 0, Stdout: "ok"},
	}
	exec := NewRolloutExecutor(cmdExec, nil,
		[]LocateAction{LocateStaticAnalysis, LocateHypothesisPropose, LocateUserQuery,
			LocateDynamicProbe, LocateInfoAggregate}, // 启用所有
		nil,
		2, 30,
	)

	sim := NewRolloutSimulator().WithExecutor(exec)
	result := sim.SimulateLocate(context.Background(), 0.5, 2, nil)

	if result.StepsTaken == 0 {
		t.Error("expected at least 1 step")
	}
	if len(cmdExec.executed) == 0 {
		t.Error("expected at least one command executed")
	}
}

// —————— 执行失败时降级 ——————

func TestRolloutExecutorExecutionErrorFallback(t *testing.T) {
	// 创建一个会导致执行失败的 executor（无 commandExecutor）
	exec := NewRolloutExecutor(nil, nil,
		[]LocateAction{LocateStaticAnalysis},
		nil, 2, 30,
	)

	sim := NewRolloutSimulator().WithExecutor(exec)
	// 即使 executor 没有 cmdExecutor，simulator 应该回退到随机模拟
	result := sim.SimulateLocate(context.Background(), 0.5, 2, nil)

	if result.StepsTaken == 0 {
		t.Error("expected at least 1 step (fallback)")
	}
	if result.FinalScore < 0 {
		t.Error("expected non-negative final score with fallback")
	}
}
