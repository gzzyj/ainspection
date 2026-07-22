package adapter

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestParamToJSONSchema(t *testing.T) {
	params := []SkillParam{
		{Name: "x", Type: "integer", Required: true},
		{Name: "y", Type: "string", Required: false, Default: "hello", Enum: []string{"hello", "world"}},
	}

	schema := ParamToJSONSchema(params)
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

func TestMapJSONTypeAll(t *testing.T) {
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
		result := MapJSONType(tc.skillType)
		if result != tc.jsonType {
			t.Errorf("MapJSONType(%s) = %s, expected %s", tc.skillType, result, tc.jsonType)
		}
	}
}

func TestWriteSkillMarkdown(t *testing.T) {
	dir := t.TempDir()
	def := SkillDef{
		Name:        "test-skill",
		Description: "A test skill",
		Body:        "This is the body of the skill.",
	}

	err := WriteSkillMarkdown(dir, def)
	if err != nil {
		t.Fatalf("WriteSkillMarkdown: %v", err)
	}

	// 验证文件已创建
	skillFile := filepath.Join(dir, "test-skill", "SKILL.md")
	data, err := os.ReadFile(skillFile)
	if err != nil {
		t.Fatalf("read skill file: %v", err)
	}

	content := string(data)
	if !strings.Contains(content, "A test skill") {
		t.Error("skill file should contain description")
	}
	if !strings.Contains(content, "This is the body of the skill.") {
		t.Error("skill file should contain body")
	}
}

func TestWriteSkillsMarkdown(t *testing.T) {
	dir := t.TempDir()
	defs := []SkillDef{
		{Name: "skill-1", Description: "First skill", Body: "body1"},
		{Name: "skill-2", Description: "Second skill", Body: "body2"},
	}

	err := WriteSkillsMarkdown(dir, defs)
	if err != nil {
		t.Fatalf("WriteSkillsMarkdown: %v", err)
	}

	for _, s := range defs {
		skillFile := filepath.Join(dir, s.Name, "SKILL.md")
		if _, err := os.Stat(skillFile); os.IsNotExist(err) {
			t.Errorf("skill file %s not found", skillFile)
		}
	}
}

func TestSkillToClaudeTool(t *testing.T) {
	s := &SkillDef{
		Name:        "test-tool",
		Description: "test desc",
		Parameters: []SkillParam{
			{Name: "q", Type: "boolean", Required: true},
		},
	}

	ct := SkillToClaudeTool(s)
	if ct.Name != "test-tool" {
		t.Errorf("expected 'test-tool', got %s", ct.Name)
	}
	if ct.InputSchema["type"] != "object" {
		t.Error("expected type=object")
	}
}

func TestSkillToOpenAITool(t *testing.T) {
	s := &SkillDef{
		Name:        "test-tool",
		Description: "test desc",
		Parameters:  []SkillParam{},
	}

	ot := SkillToOpenAITool(s)
	if ot.Type != "function" {
		t.Errorf("expected type='function', got %s", ot.Type)
	}
	if ot.Function.Name != "test-tool" {
		t.Errorf("expected name='test-tool', got %s", ot.Function.Name)
	}

	params := ot.Function.Parameters
	if _, ok := params["properties"]; !ok {
		t.Error("expected parameters.properties to exist")
	}
}

func TestResolveNativeTool(t *testing.T) {
	def := ResolveNativeTool("web_search")
	if def == nil {
		t.Fatal("web_search native tool not found")
	}
	if def.Name != "web_search" {
		t.Errorf("expected web_search, got %s", def.Name)
	}

	def2 := ResolveNativeTool("code_interpreter")
	if def2 == nil {
		t.Fatal("code_interpreter native tool not found")
	}

	def3 := ResolveNativeTool("nonexistent")
	if def3 != nil {
		t.Error("expected nil for unknown native tool")
	}
}

func TestMakeClaudeBashTool(t *testing.T) {
	bt := MakeClaudeBashTool()
	if bt.Name != "bash" {
		t.Errorf("expected 'bash', got %s", bt.Name)
	}
	if bt.Description == "" {
		t.Error("bash tool has empty description")
	}
}

func TestMakeOpenAIBashTool(t *testing.T) {
	bt := MakeOpenAIBashTool()
	if bt.Type != "function" {
		t.Errorf("expected type='function', got %s", bt.Type)
	}
	if bt.Function.Name != "bash" {
		t.Errorf("expected name='bash', got %s", bt.Function.Name)
	}
}

func TestToClaudeHooks(t *testing.T) {
	defs := []HookDef{
		{Event: "on_start", Command: "echo hello", TimeoutS: 10},
	}

	hooks := ToClaudeHooks(defs)
	hookMap, ok := hooks["on_start"].(map[string]any)
	if !ok {
		t.Fatal("expected on_start hook map")
	}
	if hookMap["command"] != "echo hello" {
		t.Errorf("expected command 'echo hello', got %v", hookMap["command"])
	}
}

func TestToKimiHooks(t *testing.T) {
	defs := []HookDef{
		{Event: "on_start", Command: "echo hello", TimeoutS: 10},
	}

	result := ToKimiHooks(defs)
	if !strings.Contains(result, "[[hooks]]") {
		t.Error("expected [[hooks]] section")
	}
	if !strings.Contains(result, "echo hello") {
		t.Error("expected echo hello command")
	}
}

func TestToCodexHooks(t *testing.T) {
	defs := []HookDef{
		{Event: "on_start", Command: "echo hello", TimeoutS: 10},
	}

	hooks := ToCodexHooks(defs)
	if len(hooks) != 1 {
		t.Fatalf("expected 1 hook, got %d", len(hooks))
	}
	if hooks[0]["event"] != "on_start" {
		t.Errorf("expected on_start, got %v", hooks[0]["event"])
	}

	data, err := json.Marshal(hooks)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if !strings.Contains(string(data), "echo hello") {
		t.Error("expected echo hello in JSON")
	}
}

func TestWriteHookFile(t *testing.T) {
	dir := t.TempDir()
	hookPath := filepath.Join(dir, "hooks.json")
	content := []byte(`{"hooks":[]}`)

	err := WriteHookFile(hookPath, content)
	if err != nil {
		t.Fatalf("WriteHookFile: %v", err)
	}

	data, err := os.ReadFile(hookPath)
	if err != nil {
		t.Fatalf("read hook file: %v", err)
	}
	if string(data) != string(content) {
		t.Errorf("expected %s, got %s", content, data)
	}
}

func TestBashConstants(t *testing.T) {
	if BashToolName != "bash" {
		t.Errorf("expected BashToolName='bash', got %s", BashToolName)
	}
	if BashToolDescription == "" {
		t.Error("BashToolDescription should not be empty")
	}
	if BashParamSchema == nil {
		t.Error("BashParamSchema should not be nil")
	}
}
