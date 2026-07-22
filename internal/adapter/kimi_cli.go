package adapter

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
)

// KimiCLIAdapter 封装 Kimi/Moonshot CLI 的 Setup/Run/ParseOutput。
//
// Skill 注入路径: .kimi/skills/<name>/SKILL.md
// Hook  注入路径: config.toml 的 [[hooks]] 段（追加）
type KimiCLIAdapter struct {
	name       string
	cliBinary  string
	skillsDir  string
	hooksDir   string
	configFile string
	apiKeyEnv  string
}

// NewKimiCLIAdapter 创建 Kimi CLI 适配器。
func NewKimiCLIAdapter(cliBinary, skillsDir, hooksDir, configFile, apiKeyEnv string) *KimiCLIAdapter {
	if cliBinary == "" {
		cliBinary = "kimi"
	}
	if skillsDir == "" {
		skillsDir = ".kimi/skills"
	}
	if hooksDir == "" {
		hooksDir = ".kimi"
	}
	if configFile == "" {
		configFile = "config.toml"
	}
	if apiKeyEnv == "" {
		apiKeyEnv = "MOONSHOT_API_KEY"
	}
	return &KimiCLIAdapter{
		name:       "kimi_cli",
		cliBinary:  cliBinary,
		skillsDir:  skillsDir,
		hooksDir:   hooksDir,
		configFile: configFile,
		apiKeyEnv:  apiKeyEnv,
	}
}

func (a *KimiCLIAdapter) Name() string    { return a.name }
func (a *KimiCLIAdapter) Type() AgentType  { return AgentKimiCLI }

// Setup 统一 3 步流程：
//  1. InjectSkills 写入 SKILL.md
//  2. 写入 LLM 配置 (config.toml)
//  3. InjectHooks 追加到 config.toml
func (a *KimiCLIAdapter) Setup(ctx context.Context, sandboxPath string, cfg AgentSetupConfig) error {
	// 1. 注入 Skills
	if err := a.InjectSkills(sandboxPath, cfg.Skills); err != nil {
		return fmt.Errorf("kimi setup skills: %w", err)
	}

	// 2. 写入 LLM 配置 (config.toml)
	configPath := filepath.Join(sandboxPath, a.configFile)
	llmConfig := BuildLLMConfigTOML(cfg)
	if err := os.WriteFile(configPath, []byte(llmConfig), 0644); err != nil {
		return fmt.Errorf("kimi write llm config: %w", err)
	}

	// 3. 注入 Hooks（追加到 config.toml）
	if len(cfg.Hooks) > 0 {
		if err := a.InjectHooks(sandboxPath, cfg.Hooks); err != nil {
			return fmt.Errorf("kimi setup hooks: %w", err)
		}
	}

	return nil
}

// Run 使用 os/exec 启动 kimi CLI（含 session flag）。
func (a *KimiCLIAdapter) Run(ctx context.Context, sandboxPath string, input AgentInput) (*AgentResult, error) {
	args := []string{
		"--print", input.Prompt,
	}
	if input.SessionID != "" {
		args = append(args, "--session", input.SessionID, "--resume", input.SessionID)
	}

	cmd := exec.CommandContext(ctx, a.cliBinary, args...)
	cmd.Dir = sandboxPath
	if a.apiKeyEnv != "" {
		cmd.Env = os.Environ()
	}

	output, err := cmd.CombinedOutput()
	if err != nil {
		return &AgentResult{
			RawOutput: output,
			ExitCode:  1,
		}, fmt.Errorf("kimi run: %w", err)
	}

	return a.ParseOutput(output)
}

// ParseOutput 解析 Kimi CLI 输出。
func (a *KimiCLIAdapter) ParseOutput(raw []byte) (*AgentResult, error) {
	result := &AgentResult{
		RawOutput: raw,
		Text:      string(raw),
	}

	// 尝试解析 JSON 输出
	var parsed struct {
		Content string `json:"content"`
		Usage   struct {
			PromptTokens     int `json:"prompt_tokens"`
			CompletionTokens int `json:"completion_tokens"`
			TotalTokens      int `json:"total_tokens"`
		} `json:"usage"`
	}
	if err := json.Unmarshal(raw, &parsed); err == nil && parsed.Content != "" {
		result.Text = parsed.Content
		result.TokenUsage = TokenUsage{
			Input:  int64(parsed.Usage.PromptTokens),
			Output: int64(parsed.Usage.CompletionTokens),
		}
	}

	return result, nil
}

// InjectSkills 将 SkillDef 列表写入 Kimi skills 目录。
func (a *KimiCLIAdapter) InjectSkills(sandboxPath string, defs []SkillDef) error {
	skillsPath := filepath.Join(sandboxPath, a.skillsDir)
	return WriteSkillsMarkdown(skillsPath, defs)
}

// InjectHooks 将 HookDef 列表追加到 Kimi config.toml。
func (a *KimiCLIAdapter) InjectHooks(sandboxPath string, defs []HookDef) error {
	configPath := filepath.Join(sandboxPath, a.configFile)
	content := ToKimiHooks(defs)

	f, err := os.OpenFile(configPath, os.O_APPEND|os.O_WRONLY, 0644)
	if err != nil {
		return fmt.Errorf("open config for hooks: %w", err)
	}
	defer f.Close()
	if _, err := f.WriteString(content); err != nil {
		return fmt.Errorf("write hooks: %w", err)
	}
	return nil
}
