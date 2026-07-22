package adapter

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
)

// ClaudeCLIAdapter 封装 Claude CLI 的 Setup/Run/ParseOutput。
//
// Skill 注入路径: .claude/skills/<name>/SKILL.md
// Hook  注入路径: .claude/hooks/hooks.json（独立文件），Setup 时合并到 settings.json
type ClaudeCLIAdapter struct {
	name         string
	cliBinary    string
	skillsDir    string
	hooksDir     string
	hooksFile    string
	settingsFile string
}

// NewClaudeCLIAdapter 创建 Claude CLI 适配器。
func NewClaudeCLIAdapter(cliBinary, skillsDir, hooksDir, hooksFile, settingsFile string) *ClaudeCLIAdapter {
	if cliBinary == "" {
		cliBinary = "claude"
	}
	if skillsDir == "" {
		skillsDir = ".claude/skills"
	}
	if hooksDir == "" {
		hooksDir = ".claude/hooks"
	}
	if hooksFile == "" {
		hooksFile = "hooks.json"
	}
	if settingsFile == "" {
		settingsFile = ".claude/settings.json"
	}
	return &ClaudeCLIAdapter{
		name:         "claude_cli",
		cliBinary:    cliBinary,
		skillsDir:    skillsDir,
		hooksDir:     hooksDir,
		hooksFile:    hooksFile,
		settingsFile: settingsFile,
	}
}

func (a *ClaudeCLIAdapter) Name() string    { return a.name }
func (a *ClaudeCLIAdapter) Type() AgentType  { return AgentClaudeCLI }

// Setup 统一 3 步流程：
//  1. InjectSkills 写入 SKILL.md
//  2. 写入 LLM 配置 (settings.json)
//  3. InjectHooks 写入 hook 文件 → 合并到 settings.json
func (a *ClaudeCLIAdapter) Setup(ctx context.Context, sandboxPath string, cfg AgentSetupConfig) error {
	// 1. 注入 Skills
	if err := a.InjectSkills(sandboxPath, cfg.Skills); err != nil {
		return fmt.Errorf("claude setup skills: %w", err)
	}

	// 2. 写入 LLM 配置 (settings.json)
	settingsPath := filepath.Join(sandboxPath, a.settingsFile)
	settings := BuildLLMConfigJSON(cfg)
	if err := WriteJSONConfig(settingsPath, settings); err != nil {
		return fmt.Errorf("claude write llm config: %w", err)
	}

	// 3. 注入 Hooks → 合并到 settings.json
	if len(cfg.Hooks) > 0 {
		if err := a.InjectHooks(sandboxPath, cfg.Hooks); err != nil {
			return fmt.Errorf("claude setup hooks: %w", err)
		}
		// 读取 hooks 文件，合并到 settings.json
		hooksPath := filepath.Join(sandboxPath, a.hooksDir, a.hooksFile)
		hooksData, err := os.ReadFile(hooksPath)
		if err != nil {
			return fmt.Errorf("claude read hooks file: %w", err)
		}
		var hooksConfig map[string]any
		if err := json.Unmarshal(hooksData, &hooksConfig); err != nil {
			return fmt.Errorf("claude parse hooks: %w", err)
		}
		// 合并: 重新读取 settings.json，添加 hooks 字段
		settingsData, err := os.ReadFile(settingsPath)
		if err != nil {
			return fmt.Errorf("claude read settings: %w", err)
		}
		var merged map[string]any
		if err := json.Unmarshal(settingsData, &merged); err != nil {
			return fmt.Errorf("claude parse settings: %w", err)
		}
		if hooks, ok := hooksConfig["hooks"]; ok {
			merged["hooks"] = hooks
		}
		mergedData, err := json.MarshalIndent(merged, "", "  ")
		if err != nil {
			return fmt.Errorf("claude marshal merged settings: %w", err)
		}
		if err := os.WriteFile(settingsPath, mergedData, 0644); err != nil {
			return fmt.Errorf("claude write merged settings: %w", err)
		}
	}

	return nil
}

// Run 使用 os/exec 启动 claude CLI（含 session flag）。
func (a *ClaudeCLIAdapter) Run(ctx context.Context, sandboxPath string, input AgentInput) (*AgentResult, error) {
	args := []string{
		"-p", input.Prompt,
		"--output-format", "json",
	}
	if input.SessionID != "" {
		args = append(args, "--session-id", input.SessionID, "--resume", input.SessionID)
	}
	if input.MaxTokens > 0 {
		args = append(args, "--max-tokens", fmt.Sprintf("%d", input.MaxTokens))
	}

	cmd := exec.CommandContext(ctx, a.cliBinary, args...)
	cmd.Dir = sandboxPath

	// 传递 API 凭证
	if cfg := a.buildEnv(); cfg != nil {
		cmd.Env = append(os.Environ(), cfg...)
	}

	output, err := cmd.CombinedOutput()
	if err != nil {
		return &AgentResult{
			RawOutput: output,
			ExitCode:  1,
		}, fmt.Errorf("claude run: %w", err)
	}

	return a.ParseOutput(output)
}

// ParseOutput 解析 Claude CLI JSON 输出。
func (a *ClaudeCLIAdapter) ParseOutput(raw []byte) (*AgentResult, error) {
	result := &AgentResult{RawOutput: raw}

	var parsed struct {
		Content []struct {
			Text string `json:"text"`
			Type string `json:"type"`
		} `json:"content"`
		Usage struct {
			InputTokens  int `json:"input_tokens"`
			OutputTokens int `json:"output_tokens"`
		} `json:"usage"`
	}

	if err := json.Unmarshal(raw, &parsed); err != nil {
		// 非 JSON 输出，直接作为文本
		result.Text = string(raw)
		return result, nil
	}

	for _, c := range parsed.Content {
		if c.Type == "text" {
			result.Text += c.Text
		}
	}
	result.TokenUsage = TokenUsage{
		Input:  int64(parsed.Usage.InputTokens),
		Output: int64(parsed.Usage.OutputTokens),
	}

	return result, nil
}

// InjectSkills 将 SkillDef 列表写入 Claude skills 目录（临时沙箱）。
func (a *ClaudeCLIAdapter) InjectSkills(sandboxPath string, defs []SkillDef) error {
	skillsPath := filepath.Join(sandboxPath, a.skillsDir)
	return WriteSkillsMarkdown(skillsPath, defs)
}

// InjectHooks 将 HookDef 列表写入 .claude/hooks/hooks.json（独立文件）。
// Setup 阶段会读取此文件并合并到 settings.json。
func (a *ClaudeCLIAdapter) InjectHooks(sandboxPath string, defs []HookDef) error {
	hooksPath := filepath.Join(sandboxPath, a.hooksDir)
	if err := os.MkdirAll(hooksPath, 0755); err != nil {
		return fmt.Errorf("create hooks dir: %w", err)
	}

	settings := map[string]any{
		"hooks": ToClaudeHooks(defs),
	}
	data, err := json.MarshalIndent(settings, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal hooks: %w", err)
	}
	return WriteHookFile(filepath.Join(hooksPath, a.hooksFile), data)
}

// buildEnv 构建 Claude CLI 所需的环境变量。
func (a *ClaudeCLIAdapter) buildEnv() []string {
	return nil // Claude CLI 从 settings.json 或环境变量读取凭证
}
