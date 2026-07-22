package adapter

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
)

// GeminiCLIAdapter 封装 Gemini CLI 的 Setup/Run/ParseOutput。
//
// Skill 注入路径: .gemini/skills/<name>/SKILL.md
// Hook  注入路径: .gemini/hooks/hooks.json（独立文件），Setup 时合并到 settings.json
// Run 使用 --resume（而非 --session）恢复会话。
type GeminiCLIAdapter struct {
	name       string
	cliBinary  string
	skillsDir  string
	hooksDir   string
	hooksFile  string
	configFile string
}

// NewGeminiCLIAdapter 创建 Gemini CLI 适配器。
func NewGeminiCLIAdapter(cliBinary, skillsDir, hooksDir, hooksFile, configFile string) *GeminiCLIAdapter {
	if cliBinary == "" {
		cliBinary = "gemini"
	}
	if skillsDir == "" {
		skillsDir = ".gemini/skills"
	}
	if hooksDir == "" {
		hooksDir = ".gemini/hooks"
	}
	if hooksFile == "" {
		hooksFile = "hooks.json"
	}
	if configFile == "" {
		configFile = "settings.json"
	}
	return &GeminiCLIAdapter{
		name:       "gemini_cli",
		cliBinary:  cliBinary,
		skillsDir:  skillsDir,
		hooksDir:   hooksDir,
		hooksFile:  hooksFile,
		configFile: configFile,
	}
}

func (a *GeminiCLIAdapter) Name() string     { return a.name }
func (a *GeminiCLIAdapter) Type() AgentType   { return AgentGeminiCLI }

// Setup 统一 3 步流程：
//  1. InjectSkills 写入 SKILL.md
//  2. 写入 LLM 配置 (settings.json)
//  3. InjectHooks 写入 hook 文件 → 合并到 settings.json
func (a *GeminiCLIAdapter) Setup(ctx context.Context, sandboxPath string, cfg AgentSetupConfig) error {
	// 1. 注入 Skills
	if err := a.InjectSkills(sandboxPath, cfg.Skills); err != nil {
		return fmt.Errorf("gemini setup skills: %w", err)
	}

	// 2. 写入 LLM 配置 (settings.json)
	configPath := filepath.Join(sandboxPath, a.configFile)
	settings := BuildLLMConfigJSON(cfg)
	if err := WriteJSONConfig(configPath, settings); err != nil {
		return fmt.Errorf("gemini write llm config: %w", err)
	}

	// 3. 注入 Hooks → 合并到 settings.json
	if len(cfg.Hooks) > 0 {
		if err := a.InjectHooks(sandboxPath, cfg.Hooks); err != nil {
			return fmt.Errorf("gemini setup hooks: %w", err)
		}
		// 读取 hooks 文件，合并到 settings.json
		hooksPath := filepath.Join(sandboxPath, a.hooksDir, a.hooksFile)
		hooksData, err := os.ReadFile(hooksPath)
		if err != nil {
			return fmt.Errorf("gemini read hooks file: %w", err)
		}
		var hooksConfig map[string]any
		if err := json.Unmarshal(hooksData, &hooksConfig); err != nil {
			return fmt.Errorf("gemini parse hooks: %w", err)
		}
		// 合并: 重新读取 settings.json，添加 hooks 字段
		settingsData, err := os.ReadFile(configPath)
		if err != nil {
			return fmt.Errorf("gemini read settings: %w", err)
		}
		var merged map[string]any
		if err := json.Unmarshal(settingsData, &merged); err != nil {
			return fmt.Errorf("gemini parse settings: %w", err)
		}
		if hooks, ok := hooksConfig["hooks"]; ok {
			merged["hooks"] = hooks
		}
		mergedData, err := json.MarshalIndent(merged, "", "  ")
		if err != nil {
			return fmt.Errorf("gemini marshal merged settings: %w", err)
		}
		if err := os.WriteFile(configPath, mergedData, 0644); err != nil {
			return fmt.Errorf("gemini write merged settings: %w", err)
		}
	}

	return nil
}

// Run 使用 os/exec 启动 gemini CLI（使用 --session-id）。
func (a *GeminiCLIAdapter) Run(ctx context.Context, sandboxPath string, input AgentInput) (*AgentResult, error) {
	args := []string{
		"-p", input.Prompt,
	}
	if input.SessionID != "" {
		args = append(args, "--session-id", input.SessionID, "--resume", input.SessionID)
	}

	cmd := exec.CommandContext(ctx, a.cliBinary, args...)
	cmd.Dir = sandboxPath

	output, err := cmd.CombinedOutput()
	if err != nil {
		return &AgentResult{
			RawOutput: output,
			ExitCode:  1,
		}, fmt.Errorf("gemini run: %w", err)
	}

	return a.ParseOutput(output)
}

// ParseOutput 解析 Gemini CLI 输出。
func (a *GeminiCLIAdapter) ParseOutput(raw []byte) (*AgentResult, error) {
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

// InjectSkills 将 SkillDef 列表写入 Gemini skills 目录。
func (a *GeminiCLIAdapter) InjectSkills(sandboxPath string, defs []SkillDef) error {
	skillsPath := filepath.Join(sandboxPath, a.skillsDir)
	return WriteSkillsMarkdown(skillsPath, defs)
}

// InjectHooks 将 HookDef 列表写入 .gemini/hooks/hooks.json（独立文件）。
// Setup 阶段会读取此文件并合并到 settings.json。
func (a *GeminiCLIAdapter) InjectHooks(sandboxPath string, defs []HookDef) error {
	hooksPath := filepath.Join(sandboxPath, a.hooksDir)
	if err := os.MkdirAll(hooksPath, 0755); err != nil {
		return fmt.Errorf("create hooks dir: %w", err)
	}

	settings := map[string]any{
		"hooks": ToGeminiHooks(defs),
	}
	data, err := json.MarshalIndent(settings, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal hooks: %w", err)
	}
	return WriteHookFile(filepath.Join(hooksPath, a.hooksFile), data)
}
