package adapter

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
)

// QwenCLIAdapter 封装 Qwen/DashScope CLI 的 Setup/Run/ParseOutput。
//
// Skill 注入路径: .qwen/skills/<name>/SKILL.md
// Hook  注入路径: .qwen/hooks/<hook-event>
// 特有 flag: --experimental-skills
type QwenCLIAdapter struct {
	name       string
	cliBinary  string
	skillsDir  string
	hooksDir   string
	configFile string
	extraFlags []string
}

// NewQwenCLIAdapter 创建 Qwen CLI 适配器。
func NewQwenCLIAdapter(cliBinary, skillsDir, hooksDir, configFile string, extraFlags []string) *QwenCLIAdapter {
	if cliBinary == "" {
		cliBinary = "qwen"
	}
	if skillsDir == "" {
		skillsDir = ".qwen/skills"
	}
	if hooksDir == "" {
		hooksDir = ".qwen/hooks"
	}
	if configFile == "" {
		configFile = "settings.json"
	}
	return &QwenCLIAdapter{
		name:       "qwen_cli",
		cliBinary:  cliBinary,
		skillsDir:  skillsDir,
		hooksDir:   hooksDir,
		configFile: configFile,
		extraFlags: extraFlags,
	}
}

func (a *QwenCLIAdapter) Name() string     { return a.name }
func (a *QwenCLIAdapter) Type() AgentType   { return AgentQwenCLI }

// hooksAvailable 检测 Qwen CLI hook 功能是否可用。
func (a *QwenCLIAdapter) hooksAvailable(sandboxPath string) bool {
	hooksPath := filepath.Join(sandboxPath, a.hooksDir)
	// 尝试创建 hook 目录并写入测试文件
	if err := os.MkdirAll(hooksPath, 0755); err != nil {
		log.Printf("[adapter] qwen hooks not available (cannot create hooks dir): %v", err)
		return false
	}
	testFile := filepath.Join(hooksPath, ".hook_test")
	if err := os.WriteFile(testFile, []byte("test"), 0644); err != nil {
		log.Printf("[adapter] qwen hooks not available (cannot write test file): %v", err)
		return false
	}
	// 读取测试文件确认可读
	if _, err := os.ReadFile(testFile); err != nil {
		log.Printf("[adapter] qwen hooks not available (cannot read test file): %v", err)
		return false
	}
	// 清理测试文件
	os.Remove(testFile)
	return true
}

// Setup 统一流程：
//  1. InjectSkills 写入 SKILL.md
//  2. 写入 LLM 配置 (settings.json)
//  3. hook 可用性检测 → 可用则 InjectHooks
func (a *QwenCLIAdapter) Setup(ctx context.Context, sandboxPath string, cfg AgentSetupConfig) error {
	// 1. 注入 Skills
	if err := a.InjectSkills(sandboxPath, cfg.Skills); err != nil {
		return fmt.Errorf("qwen setup skills: %w", err)
	}

	// 2. 写入 LLM 配置 (settings.json)
	configPath := filepath.Join(sandboxPath, a.configFile)
	if err := WriteJSONConfig(configPath, BuildLLMConfigJSON(cfg)); err != nil {
		return fmt.Errorf("qwen write llm config: %w", err)
	}

	// 3. hook 可用性检测 → 可用则注入 Hooks
	if len(cfg.Hooks) > 0 {
		if a.hooksAvailable(sandboxPath) {
			if err := a.InjectHooks(sandboxPath, cfg.Hooks); err != nil {
				return fmt.Errorf("qwen setup hooks: %w", err)
			}
		} else {
			log.Printf("[adapter] qwen hooks not available, skipping")
		}
	}

	return nil
}

// Run 使用 os/exec 启动 qwen CLI（含 --experimental-skills + session flag）。
func (a *QwenCLIAdapter) Run(ctx context.Context, sandboxPath string, input AgentInput) (*AgentResult, error) {
	args := []string{
		"-p", input.Prompt,
	}
	if input.SessionID != "" {
		args = append(args, "--session-id", input.SessionID, "--resume", input.SessionID)
	}
	args = append(args, a.extraFlags...)

	cmd := exec.CommandContext(ctx, a.cliBinary, args...)
	cmd.Dir = sandboxPath

	output, err := cmd.CombinedOutput()
	if err != nil {
		return &AgentResult{
			RawOutput: output,
			ExitCode:  1,
		}, fmt.Errorf("qwen run: %w", err)
	}

	return a.ParseOutput(output)
}

// ParseOutput 解析 Qwen CLI 输出。
func (a *QwenCLIAdapter) ParseOutput(raw []byte) (*AgentResult, error) {
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

// InjectSkills 将 SkillDef 列表写入 Qwen skills 目录。
func (a *QwenCLIAdapter) InjectSkills(sandboxPath string, defs []SkillDef) error {
	skillsPath := filepath.Join(sandboxPath, a.skillsDir)
	return WriteSkillsMarkdown(skillsPath, defs)
}

// InjectHooks 将 HookDef 列表写入 .qwen/hooks/。
func (a *QwenCLIAdapter) InjectHooks(sandboxPath string, defs []HookDef) error {
	hooksPath := filepath.Join(sandboxPath, a.hooksDir)
	if err := os.MkdirAll(hooksPath, 0755); err != nil {
		return fmt.Errorf("create hooks dir: %w", err)
	}
	for _, h := range defs {
		hookFile := filepath.Join(hooksPath, h.Event)
		content := ToQwenHooks(h)
		if err := os.WriteFile(hookFile, []byte(content), 0644); err != nil {
			return fmt.Errorf("write hook %s: %w", h.Event, err)
		}
	}
	return nil
}
