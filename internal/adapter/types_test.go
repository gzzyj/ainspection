package adapter

import (
	"testing"
)

func TestResolveAgentType(t *testing.T) {
	tests := []struct {
		input    string
		expected AgentType
	}{
		{"claude_cli", AgentClaudeCLI},
		{"claude", AgentClaudeCLI},
		{"kimi_cli", AgentKimiCLI},
		{"kimi", AgentKimiCLI},
		{"codex_cli", AgentCodexCLI},
		{"codex", AgentCodexCLI},
		{"qwen_cli", AgentQwenCLI},
		{"qwen", AgentQwenCLI},
		{"gemini_cli", AgentGeminiCLI},
		{"gemini", AgentGeminiCLI},
		{"llm_native", AgentLLMNative},
		{"unknown", ""},
		{"", ""},
	}

	for _, tc := range tests {
		result := ResolveAgentType(tc.input)
		if result != tc.expected {
			t.Errorf("ResolveAgentType(%q) = %q, expected %q", tc.input, result, tc.expected)
		}
	}
}

func TestRegistry(t *testing.T) {
	reg := NewRegistry()
	if reg == nil {
		t.Fatal("NewRegistry returned nil")
	}

	// 未注册时返回 ErrNotSupported
	_, err := reg.Get(AgentClaudeCLI)
	if err == nil {
		t.Error("expected error for unregistered agent type")
	}

	// 注册后可以获取
	adapter := NewClaudeCLIAdapter("claude", "", "", "", "")
	reg.Register(adapter)

	a, err := reg.Get(AgentClaudeCLI)
	if err != nil {
		t.Fatalf("Get after Register: %v", err)
	}
	if a.Name() != "claude_cli" {
		t.Errorf("expected name 'claude_cli', got %s", a.Name())
	}

	// GetByString
	a2, err := reg.GetByString("claude")
	if err != nil {
		t.Fatalf("GetByString: %v", err)
	}
	if a2.Name() != "claude_cli" {
		t.Errorf("expected name 'claude_cli', got %s", a2.Name())
	}

	// GetByString unknown
	_, err = reg.GetByString("unknown")
	if err == nil {
		t.Error("expected error for unknown type string")
	}
}

func TestRegistryRegisterAll(t *testing.T) {
	reg := NewRegistry()
	reg.Register(NewClaudeCLIAdapter("", "", "", "", ""))
	reg.Register(NewKimiCLIAdapter("", "", "", "", ""))
	reg.Register(NewCodexCLIAdapter("", "", "", "", ""))
	reg.Register(NewQwenCLIAdapter("", "", "", "", nil))
	reg.Register(NewGeminiCLIAdapter("", "", "", "", ""))
	reg.Register(NewLLMNativeAdapter("", "", "", nil))

	for _, at := range []AgentType{
		AgentClaudeCLI, AgentKimiCLI, AgentCodexCLI,
		AgentQwenCLI, AgentGeminiCLI, AgentLLMNative,
	} {
		a, err := reg.Get(at)
		if err != nil {
			t.Errorf("Get(%s): %v", at, err)
			continue
		}
		if a.Type() != at {
			t.Errorf("Type() = %s, expected %s", a.Type(), at)
		}
	}
}
