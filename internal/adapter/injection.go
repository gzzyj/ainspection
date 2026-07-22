package adapter

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// ============================================================
// JSON Schema 转换（从 skill/adapter_claude.go 迁移）
// ============================================================

// ParamToJSONSchema 将 SkillParam 列表转为 JSON Schema object。
func ParamToJSONSchema(params []SkillParam) map[string]any {
	properties := make(map[string]any)
	var required []string

	for _, p := range params {
		prop := map[string]any{
			"type":        MapJSONType(p.Type),
			"description": p.Description,
		}

		if len(p.Enum) > 0 {
			enumVals := make([]any, len(p.Enum))
			for i, e := range p.Enum {
				enumVals[i] = e
			}
			prop["enum"] = enumVals
		}
		if p.Default != "" {
			prop["default"] = p.Default
		}
		if p.Pattern != "" {
			prop["pattern"] = p.Pattern
		}

		properties[p.Name] = prop

		if p.Required {
			required = append(required, p.Name)
		}
	}

	schema := map[string]any{
		"type":       "object",
		"properties": properties,
	}
	if len(required) > 0 {
		schema["required"] = required
	}

	return schema
}

// MapJSONType 将 skill 参数类型转为 JSON Schema type。
func MapJSONType(t string) string {
	switch t {
	case "integer":
		return "integer"
	case "boolean":
		return "boolean"
	case "object":
		return "object"
	case "array":
		return "array"
	default:
		return "string"
	}
}

// ============================================================
// Bash 工具定义（从 skill/bash.go 迁移）
// ============================================================

// BashToolName L3 内置 bash 工具的名称。
const BashToolName = "bash"

// BashToolDescription L3 内置 bash 工具的描述。
const BashToolDescription = "在沙箱中执行 shell 命令。命令会经过白名单检查（config.security.allowed_commands），" +
	"管道/链式命令/命令替换会被 blocked_patterns 拒绝。可用的命令包括 go build/vet/test、golangci-lint、gofumpt、git、kubectl get 等。"

// BashParamSchema bash 工具的 JSON Schema 参数定义。
var BashParamSchema = map[string]any{
	"type": "object",
	"properties": map[string]any{
		"cmd": map[string]any{
			"type":        "string",
			"description": "要执行的 shell 命令",
		},
	},
	"required": []string{"cmd"},
}

// ============================================================
// Tool 格式定义（各 CLI/API 的工具 schema 格式）
// ============================================================

// ClaudeToolDef Anthropic API 的 tool 定义格式。
type ClaudeToolDef struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	InputSchema map[string]any `json:"input_schema"`
}

// OpenAIToolDef OpenAI function-calling 的 tool 定义格式（Kimi/Codex/Qwen/Gemini 共用）。
type OpenAIToolDef struct {
	Type     string           `json:"type"` // "function"
	Function OpenAIFunction   `json:"function"`
}

// OpenAIFunction OpenAI function-calling 的 function 字段。
type OpenAIFunction struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	Parameters  map[string]any `json:"parameters"`
}

// SkillToClaudeTool 将 SkillDef 转为 Claude Anthropic 格式的 tool 定义。
func SkillToClaudeTool(s *SkillDef) ClaudeToolDef {
	return ClaudeToolDef{
		Name:        s.Name,
		Description: s.Description,
		InputSchema: ParamToJSONSchema(s.Parameters),
	}
}

// SkillToOpenAITool 将 SkillDef 转为 OpenAI function-calling 格式的 tool 定义。
func SkillToOpenAITool(s *SkillDef) OpenAIToolDef {
	return OpenAIToolDef{
		Type: "function",
		Function: OpenAIFunction{
			Name:        s.Name,
			Description: s.Description,
			Parameters:  ParamToJSONSchema(s.Parameters),
		},
	}
}

// MakeClaudeBashTool 返回 L3 bash 工具的 Claude 格式定义。
func MakeClaudeBashTool() ClaudeToolDef {
	return ClaudeToolDef{
		Name:        BashToolName,
		Description: BashToolDescription,
		InputSchema: BashParamSchema,
	}
}

// MakeOpenAIBashTool 返回 L3 bash 工具的 OpenAI 格式定义。
func MakeOpenAIBashTool() OpenAIToolDef {
	return OpenAIToolDef{
		Type: "function",
		Function: OpenAIFunction{
			Name:        BashToolName,
			Description: BashToolDescription,
			Parameters:  BashParamSchema,
		},
	}
}

// ============================================================
// Skill Markdown 写入（Plan B injection 的核心）
// ============================================================

// skillMarkdownTemplate 生成 Agent Skill 的 SKILL.md 内容。
const skillMarkdownTemplate = `---
name: %s
description: %s
---

%s
`

// INSTRUMENT: injection-write-skill — Plan B 核心：将中立 SkillDef 转为 Agent Skill 文件
// LAYER: L2
// STATUS: implemented
// WriteSkillMarkdown 将单个 SkillDef 写入 agent 的 skills 目录。
// 写入路径: <skillsDir>/<skill.Name>/SKILL.md
func WriteSkillMarkdown(skillsDir string, def SkillDef) error {
	skillDir := filepath.Join(skillsDir, def.Name)
	if err := os.MkdirAll(skillDir, 0755); err != nil {
		return fmt.Errorf("create skill dir %s: %w", skillDir, err)
	}

	content := fmt.Sprintf(skillMarkdownTemplate, def.Name, def.Description, def.Body)
	skillFile := filepath.Join(skillDir, "SKILL.md")
	if err := os.WriteFile(skillFile, []byte(content), 0644); err != nil {
		return fmt.Errorf("write skill file %s: %w", skillFile, err)
	}

	return nil
}

// WriteSkillsMarkdown 批量写入 SkillDef 到 skills 目录。
func WriteSkillsMarkdown(skillsDir string, defs []SkillDef) error {
	for _, def := range defs {
		if err := WriteSkillMarkdown(skillsDir, def); err != nil {
			return err
		}
	}
	return nil
}

// WriteHookFile 将 HookDef 列表写入指定文件（格式因 CLI 而异，由各 adapter 调用）。
func WriteHookFile(path string, content []byte) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return fmt.Errorf("create hook dir %s: %w", dir, err)
	}
	if err := os.WriteFile(path, content, 0644); err != nil {
		return fmt.Errorf("write hook file %s: %w", path, err)
	}
	return nil
}

// ============================================================
// LLM 配置写入（Setup 第 2 步）
// ============================================================

// WriteJSONConfig 将 map[string]any 序列化为 JSON 写入指定路径。
func WriteJSONConfig(path string, config map[string]any) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return fmt.Errorf("create config dir: %w", err)
	}
	data, err := json.MarshalIndent(config, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal config: %w", err)
	}
	return os.WriteFile(path, data, 0644)
}

// WriteTOMLConfig 将 TOML 字符串写入指定路径。
func WriteTOMLConfig(path string, content string) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return fmt.Errorf("create config dir: %w", err)
	}
	return os.WriteFile(path, []byte(content), 0644)
}

// BuildLLMConfigJSON 构建 Claude/Gemini/Qwen settings.json 的 LLM 配置。
func BuildLLMConfigJSON(cfg AgentSetupConfig) map[string]any {
	config := map[string]any{
		"model": cfg.Model,
	}
	if cfg.Endpoint != "" {
		config["endpoint"] = cfg.Endpoint
	}
	if cfg.APIKey != "" {
		config["api_key"] = cfg.APIKey
	}
	return config
}

// BuildLLMConfigTOML 构建 Kimi/Codex config.toml 的 LLM 配置。
func BuildLLMConfigTOML(cfg AgentSetupConfig) string {
	var b strings.Builder
	fmt.Fprintf(&b, "[model]\n")
	fmt.Fprintf(&b, "name = \"%s\"\n", cfg.Model)
	if cfg.Endpoint != "" {
		fmt.Fprintf(&b, "endpoint = \"%s\"\n", cfg.Endpoint)
	}
	if cfg.APIKeyEnv != "" {
		fmt.Fprintf(&b, "api_key_env = \"%s\"\n", cfg.APIKeyEnv)
	}
	return b.String()
}

// ============================================================
// Hook 格式转换（各 CLI 的 hook 配置格式）
// ============================================================

// ToClaudeHooks 将 HookDef 列表转为 Claude settings.json 的 hooks 配置。
func ToClaudeHooks(defs []HookDef) map[string]any {
	hooks := make(map[string]any)
	for _, d := range defs {
		hooks[d.Event] = map[string]any{
			"command":  d.Command,
			"timeout":  d.TimeoutS,
		}
	}
	return hooks
}

// ToKimiHooks 将 HookDef 列表转为 Kimi config.toml 的 [[hooks]] 片段。
func ToKimiHooks(defs []HookDef) string {
	var b strings.Builder
	for _, d := range defs {
		fmt.Fprintf(&b, "[[hooks]]\n")
		fmt.Fprintf(&b, "event = \"%s\"\n", d.Event)
		fmt.Fprintf(&b, "command = \"%s\"\n", d.Command)
		if d.TimeoutS > 0 {
			fmt.Fprintf(&b, "timeout = %d\n", d.TimeoutS)
		}
		b.WriteString("\n")
	}
	return b.String()
}

// ToCodexHooks 将 HookDef 列表转为 Codex hooks.json 格式。
func ToCodexHooks(defs []HookDef) []map[string]any {
	var hooks []map[string]any
	for _, d := range defs {
		hooks = append(hooks, map[string]any{
			"event":   d.Event,
			"command": d.Command,
			"timeout": d.TimeoutS,
		})
	}
	return hooks
}

// ToQwenHooks 将 HookDef 列表转为 Qwen 单个 hook 文件内容。
func ToQwenHooks(d HookDef) string {
	return fmt.Sprintf("event: %s\ncommand: %s\ntimeout: %d\n", d.Event, d.Command, d.TimeoutS)
}

// ToGeminiHooks 将 HookDef 列表转为 Gemini settings.json 的 hooks 配置。
func ToGeminiHooks(defs []HookDef) map[string]any {
	// Gemini 与 Claude 使用相同的 JSON hook 格式
	return ToClaudeHooks(defs)
}

// ============================================================
// L2 平台原生工具注册表（从 skill/native.go 迁移）
// ============================================================

// NativeToolDef 平台原生工具的描述（L2 层）。
type NativeToolDef struct {
	Name        string
	Description string
}

// wellKnownNativeTools 已知的 L2 平台原生工具的 schema 定义。
var wellKnownNativeTools = map[string]NativeToolDef{
	"web_search": {
		Name:        "web_search",
		Description: "搜索互联网获取最新信息，用于查找技术文档、错误码含义等",
	},
	"code_interpreter": {
		Name:        "code_interpreter",
		Description: "在沙箱中执行 Python 代码进行数据分析和计算",
	},
}

// ResolveNativeTool 根据名称获取 L2 原生工具定义，未知则返回 nil。
func ResolveNativeTool(name string) *NativeToolDef {
	if def, ok := wellKnownNativeTools[name]; ok {
		return &def
	}
	return nil
}

// NativeToClaudeTool 将 L2 原生工具转为 Claude 格式。
func NativeToClaudeTool(def NativeToolDef) ClaudeToolDef {
	return ClaudeToolDef{
		Name:        def.Name,
		Description: def.Description,
		InputSchema: map[string]any{
			"type":       "object",
			"properties": map[string]any{},
		},
	}
}

// NativeToOpenAITool 将 L2 原生工具转为 OpenAI 格式。
func NativeToOpenAITool(def NativeToolDef) OpenAIToolDef {
	return OpenAIToolDef{
		Type: "function",
		Function: OpenAIFunction{
			Name:        def.Name,
			Description: def.Description,
			Parameters: map[string]any{
				"type":       "object",
				"properties": map[string]any{},
			},
		},
	}
}
