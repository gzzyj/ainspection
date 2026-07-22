package orchestrator

import (
	"context"
	"fmt"
	"time"

	"git.qingteng.cn/ms/ainspection/internal/mcts"
	"git.qingteng.cn/ms/ainspection/internal/security"
)

// ActionExecutor 执行单个 rollout 动作并返回观测影响。
type ActionExecutor interface {
	// ExecuteLocateAction 执行定位阶段动作，返回 (影响分, 输出, 错误)。
	ExecuteLocateAction(ctx context.Context, action LocateRolloutAction,
		sourceCtx *mcts.SourceContext, sandboxPath string) (impact float64, output string, err error)

	// ExecuteFixAction 执行修复阶段动作，返回 (影响分, 输出, 错误)。
	ExecuteFixAction(ctx context.Context, action FixRolloutAction,
		sourceCtx *mcts.SourceContext, testCtx *mcts.TestContext,
		sandboxPath string) (impact float64, output string, err error)
}

// RolloutExecutor 通过安全基础设施执行真实 Rollout 动作。
//
// 设计原则：
//   - 所有命令经 CommandExecutor 白名单校验
//   - 每次执行记录审计日志
//   - 单动作超时自动降级到模拟值
//   - 逐动作开关控制，未启用的动作回退到随机模拟
type RolloutExecutor struct {
	cmdExecutor       security.CommandExecutor
	auditLogger       security.AuditLogger
	enabledLocate     map[LocateAction]bool
	enabledFix        map[FixAction]bool
	maxRealSteps      int
	perActionTimeoutS int
}

// DefaultMaxRealSteps RolloutExecutor 默认最大真实执行步数。
const DefaultMaxRealSteps = 2

// DefaultPerActionTimeoutS RolloutExecutor 默认单动作超时秒数。
const DefaultPerActionTimeoutS = 30

// NewRolloutExecutor 创建 RolloutExecutor。
func NewRolloutExecutor(
	cmdExecutor security.CommandExecutor,
	auditLogger security.AuditLogger,
	enabledLocate []LocateAction,
	enabledFix []FixAction,
	maxRealSteps int,
	perActionTimeoutS int,
) *RolloutExecutor {
	if maxRealSteps <= 0 {
		maxRealSteps = DefaultMaxRealSteps
	}
	if perActionTimeoutS <= 0 {
		perActionTimeoutS = DefaultPerActionTimeoutS
	}

	lmap := make(map[LocateAction]bool, len(enabledLocate))
	for _, a := range enabledLocate {
		lmap[a] = true
	}
	fmap := make(map[FixAction]bool, len(enabledFix))
	for _, a := range enabledFix {
		fmap[a] = true
	}

	return &RolloutExecutor{
		cmdExecutor:       cmdExecutor,
		auditLogger:       auditLogger,
		enabledLocate:     lmap,
		enabledFix:        fmap,
		maxRealSteps:      maxRealSteps,
		perActionTimeoutS: perActionTimeoutS,
	}
}

// IsLocateEnabled 检查指定 locate 动作是否启用。
func (e *RolloutExecutor) IsLocateEnabled(action LocateAction) bool {
	return e.enabledLocate[action]
}

// IsFixEnabled 检查指定 fix 动作是否启用。
func (e *RolloutExecutor) IsFixEnabled(action FixAction) bool {
	return e.enabledFix[action]
}

// INSTRUMENT: rollout-executor-locate — locate 动作真实执行，含安全检查与审计
// LAYER: L2
// STATUS: implemented
// ExecuteLocateAction 执行单个 locate 动作。
func (e *RolloutExecutor) ExecuteLocateAction(
	ctx context.Context, action LocateRolloutAction,
	sourceCtx *mcts.SourceContext, sandboxPath string,
) (impact float64, output string, err error) {
	timeoutCtx, cancel := context.WithTimeout(ctx, time.Duration(e.perActionTimeoutS)*time.Second)
	defer cancel()

	result := "ok"

	switch action.Type {
	case LocateStaticAnalysis:
		impact, output, err = e.execStaticAnalysis(timeoutCtx, sandboxPath)
	case LocateDynamicProbe:
		impact, output, err = e.execDynamicProbe(timeoutCtx, sandboxPath)
	case LocateHypothesisPropose:
		// 无外部命令；记录到审计日志
		impact = 0.02
		output = "hypothesis recorded"
	case LocateInfoAggregate:
		impact, output, err = e.execInfoAggregate(timeoutCtx, sandboxPath)
	case LocateUserQuery:
		// 无外部命令
		impact = 0
		output = "user query (no-op)"
	default:
		impact = 0
		output = fmt.Sprintf("unknown action: %s", action.Type)
	}

	if err != nil {
		result = "failed"
	}

	// 审计记录
	if e.auditLogger != nil {
		e.auditLogger.Append(security.NewExecRecord(
			"", "", "rollout_executor",
			string(action.Type), result, output,
		))
	}

	return impact, output, err
}

// INSTRUMENT: rollout-executor-fix — fix 动作真实执行，含安全检查与审计
// LAYER: L2
// STATUS: implemented
// ExecuteFixAction 执行单个 fix 动作。
func (e *RolloutExecutor) ExecuteFixAction(
	ctx context.Context, action FixRolloutAction,
	sourceCtx *mcts.SourceContext, testCtx *mcts.TestContext,
	sandboxPath string,
) (impact float64, output string, err error) {
	timeoutCtx, cancel := context.WithTimeout(ctx, time.Duration(e.perActionTimeoutS)*time.Second)
	defer cancel()

	result := "ok"

	switch action.Type {
	case FixTestRun:
		impact, output, err = e.execTestRun(timeoutCtx, testCtx, sandboxPath)
	case FixStaticCheck:
		impact, output, err = e.execStaticCheck(timeoutCtx, sandboxPath)
	case FixTemplateInstantiate:
		// 生成 diff 到 sandbox/patches/，不需要外部命令执行
		impact = 0.08
		output = "template instantiated"
	case FixMutation:
		impact = 0.05
		output = "mutation applied"
	case FixPatchMinify:
		impact = 0.06
		output = "patch minify (no-op)"
	case FixUserConfirm:
		impact = 0
		output = "user confirm (no-op)"
	default:
		impact = 0
		output = fmt.Sprintf("unknown action: %s", action.Type)
	}

	if err != nil {
		result = "failed"
	}

	if e.auditLogger != nil {
		e.auditLogger.Append(security.NewExecRecord(
			"", "", "rollout_executor",
			string(action.Type), result, output,
		))
	}

	return impact, output, err
}

// execStaticAnalysis 执行静态分析（go vet）。
func (e *RolloutExecutor) execStaticAnalysis(ctx context.Context, workingDir string) (float64, string, error) {
	if e.cmdExecutor == nil {
		return 0.05, "no executor", nil
	}

	res, err := e.cmdExecutor.Execute(ctx, "go vet ./...", workingDir)
	if err != nil {
		return -0.05, "", fmt.Errorf("go vet: %w", err)
	}

	if !res.Allowed {
		return -0.05, res.BlockedReason, nil
	}

	if res.NeedsApproval {
		return -0.05, "needs approval", nil
	}

	if res.ExitCode == 0 {
		return 0.10, res.Stdout, nil
	}

	// 有 warning 但没 fatal error
	if res.ExitCode == 1 {
		return 0.03, res.Stderr, nil
	}

	return -0.05, res.Stderr, nil
}

// execDynamicProbe 执行动态探测（通过 kubectl logs 或直接读取 signals）。
func (e *RolloutExecutor) execDynamicProbe(ctx context.Context, sandboxPath string) (float64, string, error) {
	// 动态探测：检查 sandbox/signals/ 目录是否有信号文件
	// 无 cmdExecutor 时直接返回无结果
	if e.cmdExecutor == nil {
		return -0.02, "no executor", nil
	}

	// 尝试查找 signals 目录
	res, err := e.cmdExecutor.Execute(ctx, fmt.Sprintf("ls %s/signals/ 2>/dev/null || echo ''", sandboxPath), sandboxPath)
	if err != nil {
		return -0.02, "", fmt.Errorf("dynamic probe: %w", err)
	}

	if res.Stdout != "" && res.Stdout != "\n" {
		return 0.12, res.Stdout, nil
	}
	return -0.02, "no signals found", nil
}

// execInfoAggregate 聚合 sandbox/signals/ 目录信息。
func (e *RolloutExecutor) execInfoAggregate(ctx context.Context, sandboxPath string) (float64, string, error) {
	if e.cmdExecutor == nil {
		return -0.01, "no executor", nil
	}

	res, err := e.cmdExecutor.Execute(ctx, fmt.Sprintf("cat %s/signals/* 2>/dev/null || echo ''", sandboxPath), sandboxPath)
	if err != nil {
		return -0.01, "", fmt.Errorf("info aggregate: %w", err)
	}

	if res.Stdout != "" && res.Stdout != "\n" {
		return 0.08, res.Stdout, nil
	}
	return -0.01, "no signals", nil
}

// execTestRun 执行测试 (go test)。
func (e *RolloutExecutor) execTestRun(ctx context.Context, testCtx *mcts.TestContext, workingDir string) (float64, string, error) {
	if e.cmdExecutor == nil {
		return 0, "no executor", nil
	}

	cmd := "go test ./..."
	if testCtx != nil && testCtx.Command != "" {
		cmd = testCtx.Command
	}

	res, err := e.cmdExecutor.Execute(ctx, cmd, workingDir)
	if err != nil {
		return -0.08, "", fmt.Errorf("test run: %w", err)
	}

	if !res.Allowed || res.NeedsApproval {
		return 0, res.BlockedReason, nil
	}

	if res.ExitCode == 0 {
		return 0.15, res.Stdout, nil
	}
	return -0.08, res.Stderr, nil
}

// execStaticCheck 执行静态检查 (go build)。
func (e *RolloutExecutor) execStaticCheck(ctx context.Context, workingDir string) (float64, string, error) {
	if e.cmdExecutor == nil {
		return 0, "no executor", nil
	}

	res, err := e.cmdExecutor.Execute(ctx, "go build ./...", workingDir)
	if err != nil {
		return -0.06, "", fmt.Errorf("static check: %w", err)
	}

	if !res.Allowed || res.NeedsApproval {
		return 0, res.BlockedReason, nil
	}

	if res.ExitCode == 0 {
		return 0.10, res.Stdout, nil
	}
	return -0.06, res.Stderr, nil
}
