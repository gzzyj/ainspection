package skill

import (
	"strings"
	"testing"

	"git.qingteng.cn/ms/ainspection/internal/adapter"
)

// —————— Injector 测试 ——————

func makeTestSkills() []*Skill {
	return []*Skill{
		{
			Name: "jira-query", Description: "查询 Jira issue",
			Parameters: []Parameter{
				{Name: "jira_id", Type: "string", Required: true, Pattern: "^[A-Z]+-[0-9]+$"},
			},
			Body: "jira-query body", ApprovalLevel: "L0", SideEffect: "read", Idempotent: true,
		},
		{
			Name: "http-probe", Description: "HTTP 探测",
			Parameters: []Parameter{
				{Name: "url", Type: "string", Required: true},
				{Name: "method", Type: "string", Required: false, Enum: []string{"GET", "POST"}, Default: "GET"},
			},
			Body: "http-probe body", ApprovalLevel: "L0", SideEffect: "read", Idempotent: true,
		},
	}
}

func TestInjectClaude(t *testing.T) {
	injector := NewInjector(nil)
	skills := makeTestSkills()

	tools, body, err := injector.Inject("claude", skills, []string{"web_search"})
	if err != nil {
		t.Fatalf("Inject claude: %v", err)
	}

	// L1(2) + L2(1) + L3(1) = 4 tools
	if len(tools) != 4 {
		t.Errorf("expected 4 tools (2 L1 + 1 L2 + 1 L3), got %d", len(tools))
	}

	// 验证 body
	if !strings.Contains(body, "jira-query body") {
		t.Error("body should contain jira-query body")
	}
	if !strings.Contains(body, "http-probe body") {
		t.Error("body should contain http-probe body")
	}

	// 验证 L3 bash 存在
	foundBash := false
	for _, tool := range tools {
		if tool.Name == "bash" {
			foundBash = true
			if tool.Schema == nil {
				t.Error("bash tool should have schema")
			}
			if props, ok := tool.Schema["properties"]; ok {
				propMap := props.(map[string]any)
				if _, ok := propMap["cmd"]; !ok {
					t.Error("bash tool schema should have 'cmd' property")
				}
			}
			break
		}
	}
	if !foundBash {
		t.Error("L3 bash tool not found in claude tools")
	}

	// 验证 L1 jira-query 的 schema 格式
	var jiraTool ToolDef
	for _, tool := range tools {
		if tool.Name == "jira-query" {
			jiraTool = tool
			break
		}
	}
	if jiraTool.Schema["type"] != "object" {
		t.Error("claude tool schema should have type=object")
	}
}

func TestInjectKimi(t *testing.T) {
	injector := NewInjector(nil)
	skills := makeTestSkills()

	tools, body, err := injector.Inject("kimi", skills, nil)
	if err != nil {
		t.Fatalf("Inject kimi: %v", err)
	}

	// L1(2) + L3(1) = 3 tools (no L2 native)
	if len(tools) != 3 {
		t.Errorf("expected 3 tools, got %d", len(tools))
	}

	if !strings.Contains(body, "jira-query body") {
		t.Error("body should contain skill bodies")
	}

	for _, tool := range tools {
		if tool.Name == "" {
			t.Error("kimi tool has empty name")
		}
		if tool.Description == "" {
			t.Errorf("kimi tool %s has empty description", tool.Name)
		}
		if tool.Schema == nil {
			t.Errorf("kimi tool %s has nil schema", tool.Name)
		}
	}
}

func TestInjectQwen(t *testing.T) {
	injector := NewInjector(nil)
	skills := makeTestSkills()

	tools, _, err := injector.Inject("qwen", skills, []string{"web_search"})
	if err != nil {
		t.Fatalf("Inject qwen: %v", err)
	}

	if len(tools) != 4 {
		t.Errorf("expected 4 tools, got %d", len(tools))
	}

	for _, tool := range tools {
		if tool.Name == "" {
			t.Error("qwen tool has empty name")
		}
		if tool.Schema == nil {
			t.Errorf("qwen tool %s has nil schema", tool.Name)
		}
	}
}

func TestInjectEmptyAgentType(t *testing.T) {
	injector := NewInjector(nil)
	_, _, err := injector.Inject("", nil, nil)
	if err == nil {
		t.Error("expected error for empty agent type")
	}
}

func TestInjectUnknownAgentTypeDefault(t *testing.T) {
	injector := NewInjector(nil)
	// 未知类型不再报错，降级为生成通用 ToolDef（含 L3 bash）
	tools, _, err := injector.Inject("unknown-adapter", nil, nil)
	if err != nil {
		t.Fatalf("Inject unknown: %v", err)
	}
	if len(tools) != 1 {
		t.Errorf("expected 1 tool (bash only) for unknown type, got %d", len(tools))
	}
}

func TestInjectEmptySkills(t *testing.T) {
	injector := NewInjector(nil)

	tools, body, err := injector.Inject("claude", nil, nil)
	if err != nil {
		t.Fatalf("Inject empty: %v", err)
	}

	// 无 L1 skill，只有 L3 bash
	if len(tools) != 1 {
		t.Errorf("expected 1 tool (bash only), got %d", len(tools))
	}
	if tools[0].Name != adapter.BashToolName {
		t.Errorf("expected bash tool, got %s", tools[0].Name)
	}
	if body != "" {
		t.Errorf("expected empty body, got '%s'", body)
	}
}

// —————— Adapter 工具格式测试（使用 adapter 包） ——————

func TestClaudeToolFormat(t *testing.T) {
	s := &adapter.SkillDef{
		Name:        "test-tool",
		Description: "test desc",
		Parameters: []adapter.SkillParam{
			{Name: "x", Type: "integer", Required: true},
			{Name: "y", Type: "string", Required: false, Default: "hello", Enum: []string{"hello", "world"}},
		},
	}

	ct := adapter.SkillToClaudeTool(s)
	if ct.Name != "test-tool" {
		t.Errorf("expected 'test-tool', got %s", ct.Name)
	}

	schema := ct.InputSchema
	if schema["type"] != "object" {
		t.Error("expected type=object")
	}

	props, ok := schema["properties"].(map[string]any)
	if !ok {
		t.Fatal("properties not a map")
	}

	xProp := props["x"].(map[string]any)
	if xProp["type"] != "integer" {
		t.Errorf("expected type=integer for x, got %v", xProp["type"])
	}

	yProp := props["y"].(map[string]any)
	if yProp["type"] != "string" {
		t.Errorf("expected type=string for y, got %v", yProp["type"])
	}
	if yProp["default"] != "hello" {
		t.Errorf("expected default='hello', got %v", yProp["default"])
	}

	required := schema["required"].([]string)
	if len(required) != 1 || required[0] != "x" {
		t.Errorf("expected required=['x'], got %v", required)
	}
}

func TestOpenAIToolFormat(t *testing.T) {
	s := &adapter.SkillDef{
		Name:        "test-tool",
		Description: "test desc",
		Parameters: []adapter.SkillParam{
			{Name: "q", Type: "boolean", Required: true},
		},
	}

	kt := adapter.SkillToOpenAITool(s)
	if kt.Type != "function" {
		t.Errorf("expected type='function', got %s", kt.Type)
	}
	if kt.Function.Name != "test-tool" {
		t.Errorf("expected name='test-tool', got %s", kt.Function.Name)
	}

	params := kt.Function.Parameters
	if params["type"] != "object" {
		t.Error("expected parameters.type=object")
	}
}

func TestOpenAIToolFormatEmptyParams(t *testing.T) {
	s := &adapter.SkillDef{
		Name:        "test-tool",
		Description: "test desc",
		Parameters:  []adapter.SkillParam{},
	}

	qt := adapter.SkillToOpenAITool(s)
	if qt.Type != "function" {
		t.Errorf("expected type='function', got %s", qt.Type)
	}
	if qt.Function.Name != "test-tool" {
		t.Errorf("expected name='test-tool', got %s", qt.Function.Name)
	}

	params := qt.Function.Parameters
	if _, ok := params["properties"]; !ok {
		t.Error("expected parameters.properties to exist")
	}
}

// —————— Native 工具测试 ——————

func TestResolveNativeTool(t *testing.T) {
	def := adapter.ResolveNativeTool("web_search")
	if def == nil {
		t.Fatal("web_search native tool not found")
	}
	if def.Name != "web_search" {
		t.Errorf("expected web_search, got %s", def.Name)
	}

	def2 := adapter.ResolveNativeTool("nonexistent")
	if def2 != nil {
		t.Error("expected nil for unknown native tool")
	}
}

func TestInjectWithNativeTools(t *testing.T) {
	injector := NewInjector(nil)
	skills := makeTestSkills()

	tools, _, err := injector.Inject("claude", skills, []string{"web_search", "code_interpreter"})
	if err != nil {
		t.Fatalf("Inject with native: %v", err)
	}

	// L1(2) + L2(2) + L3(1) = 5
	if len(tools) != 5 {
		t.Errorf("expected 5 tools, got %d", len(tools))
	}

	foundSearch := false
	foundInterpreter := false
	for _, tool := range tools {
		if tool.Name == "web_search" {
			foundSearch = true
		}
		if tool.Name == "code_interpreter" {
			foundInterpreter = true
		}
	}
	if !foundSearch {
		t.Error("web_search native tool not in result")
	}
	if !foundInterpreter {
		t.Error("code_interpreter native tool not in result")
	}
}

// —————— Bash 测试 ——————

func TestBashToolDef(t *testing.T) {
	// 使用 adapter 包的 bash 工具常量
	def := ToolDef{
		Name:        adapter.BashToolName,
		Description: adapter.BashToolDescription,
		Schema:      adapter.BashParamSchema,
	}
	if def.Name != "bash" {
		t.Errorf("expected 'bash', got %s", def.Name)
	}
	if def.Description == "" {
		t.Error("bash tool has empty description")
	}

	if cmdProp, ok := def.Schema["properties"].(map[string]any)["cmd"]; !ok {
		t.Error("bash tool missing 'cmd' property")
	} else {
		cmdMap := cmdProp.(map[string]any)
		if cmdMap["type"] != "string" {
			t.Error("bash 'cmd' property should be string type")
		}
	}

	required := def.Schema["required"].([]string)
	if len(required) != 1 || required[0] != "cmd" {
		t.Errorf("bash tool required should be ['cmd'], got %v", required)
	}
}

// —————— JSON Type 映射测试 ——————

func TestMapJSONType(t *testing.T) {
	tests := []struct {
		skillType string
		jsonType  string
	}{
		{"string", "string"},
		{"integer", "integer"},
		{"boolean", "boolean"},
		{"object", "object"},
		{"array", "array"},
		{"unknown", "string"},
	}

	for _, tc := range tests {
		result := adapter.MapJSONType(tc.skillType)
		if result != tc.jsonType {
			t.Errorf("adapter.MapJSONType(%s) = %s, expected %s", tc.skillType, result, tc.jsonType)
		}
	}
}
