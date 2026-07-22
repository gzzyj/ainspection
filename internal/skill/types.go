package skill

// Skill 单个业务 skill 定义（从 .skills/*.md 解析）。
type Skill struct {
	Name           string      // frontmatter name
	Description    string      // frontmatter description
	Parameters     []Parameter // frontmatter parameters
	Body           string      // markdown 正文
	SourcePath     string      // 源文件路径
	ApprovalLevel  string      // L0 | L1 | L2 | L3
	SideEffect     string      // read | write
	Idempotent     bool
	InjectionMode  string      // tools | messages | both
}

// Parameter skill 参数定义。
type Parameter struct {
	Name        string   `yaml:"name"`
	Type        string   `yaml:"type"`
	Required    bool     `yaml:"required"`
	Description string   `yaml:"description,omitempty"`
	Enum        []string `yaml:"enum,omitempty"`
	Default     string   `yaml:"default,omitempty"`
	Pattern     string   `yaml:"pattern,omitempty"`
}

// ToolDef 注入给 LLM 的工具定义（OpenAI 兼容 function schema）。
type ToolDef struct {
	Name        string
	Description string
	Schema      map[string]any // JSON Schema
}

// ToolCall LLM 返回的 tool_use 请求（orchestrator 和 skill executor 共用）。
type ToolCall struct {
	ID        string
	Name      string
	Arguments map[string]any
}

// ToolResult tool 执行结果，回传给 LLM（orchestrator 和 skill executor 共用）。
type ToolResult struct {
	ToolCallID string
	Content    string
	IsError    bool
}

// ToolLayer 工具来源层级（orchestrator 和 skill executor 共用）。
type ToolLayer int

const (
	LayerL1Skill  ToolLayer = iota // 业务 skill（.skills/）
	LayerL2Native                  // 平台原生工具（web_search 等）
	LayerL3Bash                    // 内置 bash
	LayerUnknown                   // 未知工具
)

// String 返回层级的可读名称。
func (l ToolLayer) String() string {
	switch l {
	case LayerL1Skill:
		return "L1-skill"
	case LayerL2Native:
		return "L2-native"
	case LayerL3Bash:
		return "L3-bash"
	default:
		return "unknown"
	}
}
