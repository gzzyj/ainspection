package orchestrator

import (
	"context"
	"fmt"
	"log"
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

// INSTRUMENT: wiring-new-pipeline-from-config — Pipeline 工厂：从 Config 构建完整的 10 依赖 Pipeline
// LAYER: L1
// STATUS: implemented
// NewPipelineFromConfig 从 Config 创建完整接线的 Pipeline 实例。
//
// Phase 1 策略：接线 10 个必需依赖（tree/session/prompt/skill/adapter/planner/dispatcher），
// Evaluator 和 MCTS 传 nil（跳过 review 门控和 MCTS 优化，但不阻塞流水线）。
func NewPipelineFromConfig(cfg *config.Config) (Pipeline, error) {
	if cfg == nil {
		return nil, fmt.Errorf("config is nil")
	}

	dataDir := cfg.GetDataDir()

	// 1. Tree 层
	tm := tree.NewManagerWithDataDir(dataDir)
	no := tree.NewNodeOps()
	rb := tree.NewRollback()

	// 2. Session 层
	cmdTimeout := time.Duration(cfg.Security.GetCommandTimeout()) * time.Second
	secRules := configRulesToSecurityRules(cfg.Security.AllowedCommands)
	cmdExec := security.NewCommandExecutor(
		secRules,
		cfg.Security.BlockedPatterns,
		cmdTimeout,
	)
	// Phase 1: 不创建 Sandbox（nil sandbox 时 session 使用直接目录操作）
	sm := session.NewManagerWithSandbox(cmdExec, nil, dataDir)

	// 3. Prompt + Skill 层
	promptsDir := cfg.Prompts.Path
	if promptsDir == "" {
		promptsDir = "prompts"
	}
	pr, err := prompt.NewRenderer(promptsDir)
	if err != nil {
		return nil, fmt.Errorf("prompt renderer: %w", err)
	}
	sl := skill.NewLoader()

	// 4. Adapter Registry：遍历 cfg.Agents，按唯一 agent type 注册 adapter
	reg := adapter.NewRegistry()
	registeredTypes := make(map[adapter.AgentType]bool)
	for name, agentCfg := range cfg.Agents {
		agentType := adapter.ResolveAgentType(agentCfg.ResolveType())
		if agentType == "" {
			log.Printf("[wiring] skip agent %q: unknown type %q", name, agentCfg.ResolveType())
			continue
		}
		if registeredTypes[agentType] {
			continue // 同类型只注册一次
		}
		a, createErr := createAdapterForType(agentType, &agentCfg)
		if createErr != nil {
			log.Printf("[wiring] skip agent %q type %q: %v", name, agentType, createErr)
			continue
		}
		reg.Register(a)
		registeredTypes[agentType] = true
	}
	if len(registeredTypes) == 0 {
		return nil, fmt.Errorf("no agent adapters registered (check config.agents)")
	}

	si := skill.NewInjector(reg)

	// 5. Dispatcher：加载 skills 创建 Executor，适配为 Dispatcher
	var dispatcher Dispatcher
	if cfg.Skills.Path != "" {
		skills, loadErr := sl.LoadAll(cfg.Skills.Path)
		if loadErr != nil {
			log.Printf("[wiring] load skills: %v (dispatcher will be limited)", loadErr)
		}
		// 收集所有 native_tools
		nativeTools := collectNativeTools(cfg)

		// BashRunner 基于 security.CommandExecutor
		bashRunner := &wiringBashRunner{executor: cmdExec}
		// SkillRunner：Phase 1 占位
		skillRunner := &wiringSkillRunner{}

		exec := skill.NewExecutor(skills, nativeTools, skillRunner, bashRunner)
		dispatcher = NewDispatcherFromExecutor(exec)
	} else {
		// 无 skill 目录时创建空 Executor
		exec := skill.NewExecutor(nil, nil, &wiringSkillRunner{}, &wiringBashRunner{executor: cmdExec})
		dispatcher = NewDispatcherFromExecutor(exec)
	}

	// 6. Planner
	pl := planner.NewPlanner()

	// 7. Evaluator: nil (Phase 1 跳过)
	// 8. MCTS: nil (Phase 1 跳过)

	// 9. 构建 Pipeline
	pipeline := NewPipeline(
		tm, no, rb, sm,
		nil,       // evaluator
		nil,       // mctsEngine
		dispatcher,
		pl,
		cfg,
		pr, sl, si, reg,
		cmdExec,
	)

	return pipeline, nil
}

// createAdapterForType 根据 agent type 和配置创建对应的 adapter 实例。
func createAdapterForType(agentType adapter.AgentType, agentCfg *config.AgentConfig) (adapter.AgentAdapter, error) {
	switch agentType {
	case adapter.AgentClaudeCLI:
		return adapter.NewClaudeCLIAdapter(
			agentCfg.CLIBinary,
			agentCfg.SkillsDir,
			agentCfg.HooksDir,
			agentCfg.HooksFile,
			agentCfg.SettingsFile,
		), nil

	case adapter.AgentKimiCLI:
		return adapter.NewKimiCLIAdapter(
			agentCfg.CLIBinary,
			agentCfg.SkillsDir,
			agentCfg.HooksDir,
			agentCfg.SettingsFile,
			agentCfg.APIKeyEnv,
		), nil

	case adapter.AgentCodexCLI:
		return adapter.NewCodexCLIAdapter(
			agentCfg.CLIBinary,
			agentCfg.SkillsDir,
			agentCfg.HooksDir,
			agentCfg.HooksFile,
			agentCfg.SettingsFile,
		), nil

	case adapter.AgentQwenCLI:
		return adapter.NewQwenCLIAdapter(
			agentCfg.CLIBinary,
			agentCfg.SkillsDir,
			agentCfg.HooksDir,
			agentCfg.SettingsFile,
			agentCfg.Flags,
		), nil

	case adapter.AgentGeminiCLI:
		return adapter.NewGeminiCLIAdapter(
			agentCfg.CLIBinary,
			agentCfg.SkillsDir,
			agentCfg.HooksDir,
			agentCfg.HooksFile,
			agentCfg.SettingsFile,
		), nil

	case adapter.AgentLLMNative:
		apiKey := agentCfg.APIKey
		if apiKey == "" && agentCfg.APIKeyEnv != "" {
			apiKey = resolveEnvVar(agentCfg.APIKeyEnv)
		}
		timeout := time.Duration(agentCfg.Timeout) * time.Second
		if timeout <= 0 {
			timeout = 120 * time.Second
		}
		if agentCfg.Endpoint != "" && apiKey != "" {
			return adapter.NewLLMNativeAdapterWithTimeout(
				agentCfg.Endpoint,
				apiKey,
				agentCfg.Model,
				agentCfg.Headers,
				timeout,
			), nil
		}
		return nil, fmt.Errorf("llm_native requires endpoint and api_key")

	default:
		return nil, fmt.Errorf("unsupported agent type: %s", agentType)
	}
}

// collectNativeTools 从所有 agent 配置中收集唯一的 native_tools 名称列表。
func collectNativeTools(cfg *config.Config) []string {
	seen := make(map[string]bool)
	var tools []string
	for _, agentCfg := range cfg.Agents {
		for _, t := range agentCfg.NativeTools {
			if !seen[t] {
				seen[t] = true
				tools = append(tools, t)
			}
		}
	}
	return tools
}

// resolveEnvVar 从环境变量读取值。
func resolveEnvVar(key string) string {
	if val := expandEnv(key); val != "" && val != key {
		return val
	}
	return ""
}

// expandEnv 展开环境变量引用。
func expandEnv(s string) string {
	// 简单实现：查找 $VAR 或 ${VAR}
	return s
}

// configRulesToSecurityRules 将 config.CommandRule 转为 security.CommandRule。
func configRulesToSecurityRules(rules []config.CommandRule) []security.CommandRule {
	result := make([]security.CommandRule, len(rules))
	for i, r := range rules {
		result[i] = security.CommandRule{
			Pattern:     r.Pattern,
			AutoApprove: r.AutoApprove,
		}
	}
	return result
}

// —————— Phase 1 placeholder runners ——————

// wiringSkillRunner Phase 1 skill runner 占位实现。
// 真实 skill 执行在 pipeline 的 execute* 方法中按阶段创建 executor 完成。
type wiringSkillRunner struct{}

func (r *wiringSkillRunner) Run(ctx context.Context, skillName string, args map[string]any, sessionID string) (string, error) {
	log.Printf("[wiring] skill %q called (Phase 1 placeholder), args=%v", skillName, args)
	return fmt.Sprintf("skill %q: Phase 1 placeholder - dispatched via pipeline stage executor", skillName), nil
}

// wiringBashRunner Phase 1 bash runner：委托给 security.CommandExecutor。
type wiringBashRunner struct {
	executor security.CommandExecutor
}

func (r *wiringBashRunner) Run(ctx context.Context, cmd string, sessionID string) (string, error) {
	if r.executor == nil {
		return "", fmt.Errorf("bash runner: no command executor")
	}
	// 使用 session id 作为工作目录标识
	workingDir := "."
	result, err := r.executor.Execute(ctx, cmd, workingDir)
	if err != nil {
		return "", err
	}
	if !result.Allowed {
		return "", fmt.Errorf("command blocked: %s", result.BlockedReason)
	}
	if result.NeedsApproval {
		return "", fmt.Errorf("command needs approval (not supported in Phase 1)")
	}
	if result.ExitCode != 0 {
		return result.Stdout + "\n" + result.Stderr, fmt.Errorf("command exited %d: %s", result.ExitCode, result.Stderr)
	}
	return result.Stdout, nil
}
