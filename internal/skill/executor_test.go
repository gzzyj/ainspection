package skill

import (
	"context"
	"strings"
	"testing"
)

// mockSkillRunner 模拟 L1 skill Runner。
type mockSkillRunner struct {
	output string
	err    error
}

func (m *mockSkillRunner) Run(ctx context.Context, skillName string, args map[string]any, sessionID string) (string, error) {
	return m.output, m.err
}

// mockBashRunner 模拟 L3 bash Runner。
type mockBashRunner struct {
	output string
	err    error
}

func (m *mockBashRunner) Run(ctx context.Context, cmd string, sessionID string) (string, error) {
	return m.output, m.err
}

func makeTestSkillsForExecutor() []*Skill {
	return []*Skill{
		{
			Name: "jira-query", Description: "查询 Jira", ApprovalLevel: "L0",
			SideEffect: "read", Idempotent: true,
			Parameters: []Parameter{{Name: "jira_id", Type: "string", Required: true}},
		},
		{
			Name: "skaffold-deploy", Description: "部署到 k3s", ApprovalLevel: "L2",
			SideEffect: "write", Idempotent: false,
			Parameters: []Parameter{{Name: "service", Type: "string", Required: true}},
		},
	}
}

func TestExecutorExecuteL1(t *testing.T) {
	skills := makeTestSkillsForExecutor()
	sr := &mockSkillRunner{output: "jira result: OK"}
	br := &mockBashRunner{}
	exec := NewExecutor(skills, nil, sr, br)

	result, err := exec.Execute(context.Background(), "session-1", ToolCall{
		ID: "call-1", Name: "jira-query", Arguments: map[string]any{"jira_id": "JIRA-1234"},
	})
	if err != nil {
		t.Fatalf("Execute L1: %v", err)
	}
	if result.ToolCallID != "call-1" {
		t.Errorf("expected call-1, got %s", result.ToolCallID)
	}
	if !strings.Contains(result.Content, "jira result") {
		t.Errorf("expected 'jira result', got '%s'", result.Content)
	}
	if result.IsError {
		t.Error("expected success")
	}
}

func TestExecutorExecuteL1NotFound(t *testing.T) {
	skills := makeTestSkillsForExecutor()
	exec := NewExecutor(skills, nil, &mockSkillRunner{}, &mockBashRunner{})

	result, err := exec.Execute(context.Background(), "session-1", ToolCall{
		ID: "call-1", Name: "nonexistent-skill", Arguments: map[string]any{},
	})
	if err == nil {
		t.Error("expected error for unknown skill")
	}
	if !result.IsError {
		t.Error("expected IsError=true")
	}
}

func TestExecutorExecuteL3(t *testing.T) {
	br := &mockBashRunner{output: "build success"}
	exec := NewExecutor(nil, nil, &mockSkillRunner{}, br)

	result, err := exec.Execute(context.Background(), "session-1", ToolCall{
		ID: "call-1", Name: "bash", Arguments: map[string]any{"cmd": "go build ./..."},
	})
	if err != nil {
		t.Fatalf("Execute L3: %v", err)
	}
	if result.Content != "build success" {
		t.Errorf("expected 'build success', got '%s'", result.Content)
	}
}

func TestExecutorExecuteL3MissingCmd(t *testing.T) {
	exec := NewExecutor(nil, nil, &mockSkillRunner{}, &mockBashRunner{})

	result, err := exec.Execute(context.Background(), "session-1", ToolCall{
		ID: "call-1", Name: "bash", Arguments: map[string]any{},
	})
	if err == nil {
		t.Error("expected error for missing cmd")
	}
	if !result.IsError {
		t.Error("expected IsError=true")
	}
	_ = result
}

func TestExecutorExecuteL2(t *testing.T) {
	exec := NewExecutor(nil, []string{"web_search"}, &mockSkillRunner{}, &mockBashRunner{})

	result, err := exec.Execute(context.Background(), "session-1", ToolCall{
		ID: "call-1", Name: "web_search", Arguments: map[string]any{"query": "test"},
	})
	if err != nil {
		t.Fatalf("Execute L2: %v", err)
	}
	if result.IsError {
		t.Error("L2 native tool should not error")
	}
	if !strings.Contains(result.Content, "delegated to agent platform") {
		t.Errorf("unexpected L2 result: %s", result.Content)
	}
}

func TestExecutorResolveLayer(t *testing.T) {
	skills := makeTestSkillsForExecutor()
	exec := NewExecutor(skills, []string{"web_search"}, &mockSkillRunner{}, &mockBashRunner{})

	tests := []struct {
		call     ToolCall
		expected ToolLayer
	}{
		{ToolCall{Name: "bash"}, LayerL3Bash},
		{ToolCall{Name: "jira-query"}, LayerL1Skill},
		{ToolCall{Name: "ainspection-skill jira-query"}, LayerL1Skill},
		{ToolCall{Name: "web_search"}, LayerL2Native},
		{ToolCall{Name: "unknown-tool"}, LayerUnknown},
	}

	for _, tc := range tests {
		layer := exec.ResolveLayer(tc.call)
		if layer != tc.expected {
			t.Errorf("ResolveLayer(%s) = %s, expected %s", tc.call.Name, layer, tc.expected)
		}
	}
}

func TestExecutorExecuteBatch(t *testing.T) {
	skills := makeTestSkillsForExecutor()
	sr := &mockSkillRunner{output: "ok"}
	br := &mockBashRunner{output: "build ok"}
	exec := NewExecutor(skills, nil, sr, br)

	calls := []ToolCall{
		{ID: "1", Name: "jira-query", Arguments: map[string]any{"jira_id": "x"}},
		{ID: "2", Name: "bash", Arguments: map[string]any{"cmd": "go vet"}},
	}

	results, err := exec.ExecuteBatch(context.Background(), "sess-1", calls)
	if err != nil {
		t.Fatalf("ExecuteBatch: %v", err)
	}
	if len(results) != 2 {
		t.Errorf("expected 2 results, got %d", len(results))
	}
	if results[0].ToolCallID != "1" {
		t.Errorf("expected call ID '1', got '%s'", results[0].ToolCallID)
	}
	if results[1].ToolCallID != "2" {
		t.Errorf("expected call ID '2', got '%s'", results[1].ToolCallID)
	}
}

func TestExecutorSkillRunnerError(t *testing.T) {
	skills := makeTestSkillsForExecutor()
	sr := &mockSkillRunner{err: context.DeadlineExceeded}
	exec := NewExecutor(skills, nil, sr, &mockBashRunner{})

	result, err := exec.Execute(context.Background(), "sess-1", ToolCall{
		ID: "call-1", Name: "jira-query", Arguments: map[string]any{"jira_id": "x"},
	})
	if err == nil {
		t.Error("expected error from skill runner")
	}
	if !result.IsError {
		t.Error("expected IsError=true")
	}
}

func TestExecutorBashRunnerError(t *testing.T) {
	br := &mockBashRunner{err: context.DeadlineExceeded}
	exec := NewExecutor(nil, nil, &mockSkillRunner{}, br)

	result, err := exec.Execute(context.Background(), "sess-1", ToolCall{
		ID: "call-1", Name: "bash", Arguments: map[string]any{"cmd": "bad cmd"},
	})
	if err == nil {
		t.Error("expected error from bash runner")
	}
	if !result.IsError {
		t.Error("expected IsError=true")
	}
}
