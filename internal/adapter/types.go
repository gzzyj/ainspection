// Package adapter 提供 Agent 适配层：统一封装不同 agent CLI/API 的 Setup、Run、ParseOutput。
//
// 6 种 agent 类型各有一个 Adapter 实现，均实现 AgentAdapter 接口。
// Skill/Hook 注入通过 SkillInjector/HookInjector 接口委托给各 adapter。
package adapter

import (
	"context"
	"fmt"
)

// AgentType 标识 agent CLI/API 类型。
type AgentType string

const (
	AgentClaudeCLI AgentType = "claude_cli"
	AgentKimiCLI   AgentType = "kimi_cli"
	AgentCodexCLI  AgentType = "codex_cli"
	AgentQwenCLI   AgentType = "qwen_cli"
	AgentGeminiCLI AgentType = "gemini_cli"
	AgentLLMNative AgentType = "llm_native"
)

// ResolveAgentType 将字符串解析为 AgentType，未知返回空。
func ResolveAgentType(s string) AgentType {
	switch s {
	case "claude_cli", "claude":
		return AgentClaudeCLI
	case "kimi_cli", "kimi":
		return AgentKimiCLI
	case "codex_cli", "codex":
		return AgentCodexCLI
	case "qwen_cli", "qwen":
		return AgentQwenCLI
	case "gemini_cli", "gemini":
		return AgentGeminiCLI
	case "llm_native":
		return AgentLLMNative
	default:
		return ""
	}
}

// ============================================================
// Skill / Hook / 配置 数据类型
// ============================================================

// SkillDef 单个 Agent Skill 定义。
type SkillDef struct {
	Name          string       // frontmatter name
	Description   string       // frontmatter description
	Parameters    []SkillParam // frontmatter parameters
	Body          string       // markdown 正文
	InjectionMode string       // "tools" | "messages" | "both"
}

// SkillParam skill 参数定义。
type SkillParam struct {
	Name        string   `yaml:"name"`
	Type        string   `yaml:"type"`
	Required    bool     `yaml:"required"`
	Description string   `yaml:"description,omitempty"`
	Enum        []string `yaml:"enum,omitempty"`
	Default     string   `yaml:"default,omitempty"`
	Pattern     string   `yaml:"pattern,omitempty"`
}

// HookDef Agent Hook 定义。
type HookDef struct {
	Event    string // hook 触发事件
	Command  string // hook 执行的命令
	TimeoutS int    // 超时秒数
}

// AgentSetupConfig 初始化 Agent 沙箱所需的完整配置。
type AgentSetupConfig struct {
	Endpoint     string
	APIKey       string
	Model        string
	Skills       []SkillDef
	Hooks        []HookDef
	SystemPrompt string
	Headers      map[string]string
	Features     map[string]bool
	Flags        []string
	APIKeyEnv    string // 环境变量名（如 MOONSHOT_API_KEY）
	CliBinary    string // CLI 二进制名
}

// AgentInput 一次 Agent 调用的输入。
type AgentInput struct {
	Prompt       string
	SessionID    string
	ContextFiles []string
	MaxTokens    int
	Temperature  float64
}

// AgentResult Agent 调用的返回结果。
type AgentResult struct {
	Text       string
	ToolCalls  []ToolCall
	TokenUsage TokenUsage
	ExitCode   int
	RawOutput  []byte
}

// TokenUsage token 用量统计。
type TokenUsage struct {
	Input  int64
	Output int64
}

// ToolCall Agent 返回的工具调用请求。
type ToolCall struct {
	ID        string
	Name      string
	Arguments map[string]any
}

// ============================================================
// 三大接口
// ============================================================

// INSTRUMENT: adapter-agent-adapter — 6 种 agent 通信方式的统一抽象接口
// LAYER: L0
// STATUS: implemented
// AgentAdapter 统一封装 agent CLI/API 的 Setup、Run、ParseOutput。
type AgentAdapter interface {
	// Name 返回适配器名称。
	Name() string

	// Type 返回 agent 类型。
	Type() AgentType

	// Setup 在 sandboxPath 中创建 agent 所需的 skill/hook/config 文件。
	Setup(ctx context.Context, sandboxPath string, cfg AgentSetupConfig) error

	// Run 在 sandboxPath 中执行一次 agent 调用。
	Run(ctx context.Context, sandboxPath string, input AgentInput) (*AgentResult, error)

	// ParseOutput 解析 agent 原始输出为结构化结果。
	ParseOutput(raw []byte) (*AgentResult, error)
}

// INSTRUMENT: adapter-skill-injector — Plan B injection 核心接口
// LAYER: L0
// STATUS: implemented
// SkillInjector 将 SkillDef 列表注入到 agent 沙箱的 skill 目录。
type SkillInjector interface {
	InjectSkills(sandboxPath string, defs []SkillDef) error
}

// HookInjector 将 HookDef 列表注入到 agent 沙箱的 hook 配置。
type HookInjector interface {
	InjectHooks(sandboxPath string, defs []HookDef) error
}

// ============================================================
// Registry
// ============================================================

// ErrNotSupported 表示 agent type 未注册。
var ErrNotSupported = fmt.Errorf("agent type not supported")

// INSTRUMENT: adapter-registry — AgentAdapter 注册与查找，按 AgentType 映射
// LAYER: L1
// STATUS: implemented
// Registry 管理所有已注册的 AgentAdapter。
type Registry struct {
	adapters map[AgentType]AgentAdapter
}

// NewRegistry 创建一个新的 Registry。
func NewRegistry() *Registry {
	return &Registry{
		adapters: make(map[AgentType]AgentAdapter),
	}
}

// Register 注册一个 adapter。
func (r *Registry) Register(a AgentAdapter) {
	r.adapters[a.Type()] = a
}

// Get 按类型获取 adapter，未注册返回 ErrNotSupported。
func (r *Registry) Get(t AgentType) (AgentAdapter, error) {
	a, ok := r.adapters[t]
	if !ok {
		return nil, fmt.Errorf("%w: %s", ErrNotSupported, t)
	}
	return a, nil
}

// GetByString 按字符串获取 adapter，兼容别名。
func (r *Registry) GetByString(typeStr string) (AgentAdapter, error) {
	t := ResolveAgentType(typeStr)
	if t == "" {
		return nil, fmt.Errorf("%w: unknown type %q", ErrNotSupported, typeStr)
	}
	return r.Get(t)
}
