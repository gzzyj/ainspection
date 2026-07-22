package adapter

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
)

// CodexCLIAdapter 封装 Codex CLI (OpenAI) 的 Setup/Run/ParseOutput。
//
// Skill 注入路径: .codex/skills/<name>/SKILL.md
// Hook  注入路径: .codex/hooks.json
// 工具格式: 复用 OpenAI function-calling（与 Kimi 相同）
type CodexCLIAdapter struct {
	name       string
	cliBinary  string
	skillsDir  string
	hooksDir   string
	hooksFile  string
	configFile string
}

// NewCodexCLIAdapter 创建 Codex CLI 适配器。
func NewCodexCLIAdapter(cliBinary, skillsDir, hooksDir, hooksFile, configFile string) *CodexCLIAdapter {
	if cliBinary == "" {
		cliBinary = "codex"
	}
	if skillsDir == "" {
		skillsDir = ".codex/skills"
	}
	if hooksDir == "" {
		hooksDir = ".codex"
	}
	if hooksFile == "" {
		hooksFile = "hooks.json"
	}
	if configFile == "" {
		configFile = "config.toml"
	}
	return &CodexCLIAdapter{
		name:       "codex_cli",
		cliBinary:  cliBinary,
		skillsDir:  skillsDir,
		hooksDir:   hooksDir,
		hooksFile:  hooksFile,
		configFile: configFile,
	}
}

func (a *CodexCLIAdapter) Name() string     { return a.name }
func (a *CodexCLIAdapter) Type() AgentType   { return AgentCodexCLI }

// Setup 统一 3 步流程：
//  1. InjectSkills 写入 SKILL.md
//  2. 写入 LLM 配置 (config.toml)
//  3. InjectHooks 写入 hooks.json
func (a *CodexCLIAdapter) Setup(ctx context.Context, sandboxPath string, cfg AgentSetupConfig) error {
	// 1. 注入 Skills
	if err := a.InjectSkills(sandboxPath, cfg.Skills); err != nil {
		return fmt.Errorf("codex setup skills: %w", err)
	}

	// 2. 写入 LLM 配置 (config.toml)
	configPath := filepath.Join(sandboxPath, a.configFile)
	llmConfig := BuildLLMConfigTOML(cfg)
	if err := os.WriteFile(configPath, []byte(llmConfig), 0644); err != nil {
		return fmt.Errorf("codex write llm config: %w", err)
	}

	// 3. 注入 Hooks (hooks.json)
	if len(cfg.Hooks) > 0 {
		if err := a.InjectHooks(sandboxPath, cfg.Hooks); err != nil {
			return fmt.Errorf("codex setup hooks: %w", err)
		}
	}

	return nil
}

// Run 使用 os/exec 启动 codex CLI（positional prompt + --resume）。
func (a *CodexCLIAdapter) Run(ctx context.Context, sandboxPath string, input AgentInput) (*AgentResult, error) {
	args := []string{
		input.Prompt,
	}
	if input.SessionID != "" {
		args = append(args, "--resume", input.SessionID)
	}

	cmd := exec.CommandContext(ctx, a.cliBinary, args...)
	cmd.Dir = sandboxPath

	output, err := cmd.CombinedOutput()
	if err != nil {
		return &AgentResult{
			RawOutput: output,
			ExitCode:  1,
		}, fmt.Errorf("codex run: %w", err)
	}

	return a.ParseOutput(output)
}

// ParseOutput 解析 Codex CLI 输出。
func (a *CodexCLIAdapter) ParseOutput(raw []byte) (*AgentResult, error) {
	result := &AgentResult{
		RawOutput: raw,
		Text:      string(raw),
	}

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

// InjectSkills 将 SkillDef 列表写入 Codex skills 目录。
func (a *CodexCLIAdapter) InjectSkills(sandboxPath string, defs []SkillDef) error {
	skillsPath := filepath.Join(sandboxPath, a.skillsDir)
	return WriteSkillsMarkdown(skillsPath, defs)
}

// InjectHooks 将 HookDef 列表写入 .codex/hooks.json。
func (a *CodexCLIAdapter) InjectHooks(sandboxPath string, defs []HookDef) error {
	hooksJSON := ToCodexHooks(defs)
	data, err := json.MarshalIndent(hooksJSON, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal hooks: %w", err)
	}

	hooksPath := filepath.Join(sandboxPath, a.hooksDir)
	if err := os.MkdirAll(hooksPath, 0755); err != nil {
		return fmt.Errorf("create hooks dir: %w", err)
	}
	return WriteHookFile(filepath.Join(hooksPath, a.hooksFile), data)
}
