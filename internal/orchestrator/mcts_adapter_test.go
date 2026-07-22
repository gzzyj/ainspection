package orchestrator

import (
	"context"
	"testing"

	"git.qingteng.cn/ms/ainspection/internal/config"
	"git.qingteng.cn/ms/ainspection/internal/mcts"
)

func TestNewMCTSRunner(t *testing.T) {
	// 无配置
	r := NewMCTSRunner(nil)
	if r == nil {
		t.Fatal("NewMCTSRunner returned nil")
	}

	// 有配置
	cfg := &config.Config{
		MCTS: config.MCTSConfig{
			Locate: config.MCTSStageConfig{
				MaxIterations:   16,
				MaxDepth:        4,
				BranchingFactor: 3,
			},
			Fix: config.MCTSStageConfig{
				MaxIterations:   8,
				MaxDepth:        3,
				BranchingFactor: 2,
			},
			UCBC: 1.41,
		},
	}
	r2 := NewMCTSRunner(cfg)
	if r2 == nil {
		t.Fatal("NewMCTSRunner with config returned nil")
	}
}

func TestRunLocateBasic(t *testing.T) {
	r := NewMCTSRunner(nil)

	input := LocateInput{
		InputYAML: []byte("hypothesis: payment-svc 慢查询导致超时\n"),
		Skills:    []string{"jira-query", "loki-query"},
	}

	outputs, err := r.RunLocate(context.Background(), input)
	if err != nil {
		t.Fatalf("RunLocate: %v", err)
	}

	if len(outputs) == 0 {
		t.Error("expected at least 1 locate output")
	}

	for _, o := range outputs {
		if o.Hypothesis == "" {
			t.Error("hypothesis should not be empty")
		}
		if o.Confidence < 0 || o.Confidence > 1 {
			t.Errorf("confidence should be in [0,1], got %.4f", o.Confidence)
		}
	}
}

func TestRunLocateEmptyInput(t *testing.T) {
	r := NewMCTSRunner(nil)

	outputs, err := r.RunLocate(context.Background(), LocateInput{})
	if err != nil {
		t.Fatalf("RunLocate with empty input: %v", err)
	}

	if len(outputs) == 0 {
		t.Error("expected at least 1 locate output even with empty input")
	}
}

func TestRunFixBasic(t *testing.T) {
	r := NewMCTSRunner(nil)

	input := FixInput{
		PlanJSON: []byte(`{"steps":[{"id":"step-1","target":"migration/002_idx.sql"}]}`),
	}

	outputs, err := r.RunFix(context.Background(), input)
	if err != nil {
		t.Fatalf("RunFix: %v", err)
	}

	if len(outputs) == 0 {
		t.Error("expected at least 1 fix output")
	}

	for _, o := range outputs {
		if o.Confidence < 0 || o.Confidence > 1 {
			t.Errorf("confidence should be in [0,1], got %.4f", o.Confidence)
		}
	}
}

func TestRunFixEmptyInput(t *testing.T) {
	r := NewMCTSRunner(nil)

	outputs, err := r.RunFix(context.Background(), FixInput{})
	if err != nil {
		t.Fatalf("RunFix with empty input: %v", err)
	}

	// 规则式 expander 可能返回空结果
	if outputs != nil {
		for _, o := range outputs {
			if o.Confidence < 0 || o.Confidence > 1 {
				t.Errorf("confidence should be in [0,1], got %.4f", o.Confidence)
			}
		}
	}
}

func TestMCTSConfigFromApp(t *testing.T) {
	cfg := config.MCTSConfig{
		Locate: config.MCTSStageConfig{
			MaxIterations:   20,
			MaxDepth:        5,
			BranchingFactor: 4,
		},
		Fix: config.MCTSStageConfig{
			MaxIterations:   10,
			MaxDepth:        4,
			BranchingFactor: 3,
		},
		UCBC: 2.0,
	}

	got := mctsConfigFromApp(cfg)

	if got.LocateBudget.MaxIterations != 20 {
		t.Errorf("locate iterations: expected 20, got %d", got.LocateBudget.MaxIterations)
	}
	if got.LocateBudget.MaxDepth != 5 {
		t.Errorf("locate max depth: expected 5, got %d", got.LocateBudget.MaxDepth)
	}
	if got.LocateBudget.BranchingFactor != 4 {
		t.Errorf("locate bf: expected 4, got %d", got.LocateBudget.BranchingFactor)
	}
	if got.FixBudget.MaxIterations != 10 {
		t.Errorf("fix iterations: expected 10, got %d", got.FixBudget.MaxIterations)
	}
	if got.UCBC != 2.0 {
		t.Errorf("ucb_c: expected 2.0, got %.2f", got.UCBC)
	}
}

func TestNewMCTSRunnerWithLLM(t *testing.T) {
	cfg := &config.Config{
		MCTS: config.MCTSConfig{
			Locate: config.MCTSStageConfig{MaxIterations: 16, MaxDepth: 4, BranchingFactor: 3},
			Fix:    config.MCTSStageConfig{MaxIterations: 8, MaxDepth: 3, BranchingFactor: 2},
			UCBC:   1.41,
		},
	}

	// 使用 mcts 工厂函数创建 expander/scorer
	locExpander := mcts.MakeLocateExpander(ruleLocateExpand)
	locScorer := mcts.MakeLocateScorer(ruleLocateScore)
	fixExpander := mcts.MakeFixExpander(ruleFixExpand)
	fixScorer := mcts.MakeFixScorer(ruleFixScore)

	r := NewMCTSRunnerWithLLM(cfg, locExpander, locScorer, fixExpander, fixScorer)
	if r == nil {
		t.Fatal("NewMCTSRunnerWithLLM returned nil")
	}

	// 验证 RunLocate 仍可正常工作
	outputs, err := r.RunLocate(context.Background(), LocateInput{
		InputYAML: []byte("test: deadlock in sync.Mutex\n"),
	})
	if err != nil {
		t.Fatalf("RunLocate with LLM runner: %v", err)
	}
	if len(outputs) == 0 {
		t.Error("expected locate outputs with LLM runner")
	}
}

func TestAgentRunConfigFromConfig(t *testing.T) {
	ac := AgentRunConfig{}

	// nil config
	result := ac.FromConfig(nil, "test-agent")
	if result.Name != "test-agent" {
		t.Errorf("expected name 'test-agent', got %s", result.Name)
	}

	// valid config
	agentCfg := &config.AgentConfig{
		Endpoint:    "https://api.example.com",
		APIKey:      "key-123",
		Model:       "claude-sonnet-4-6",
		Type:        "claude_cli",
		NativeTools: []string{"web_search"},
	}

	result = ac.FromConfig(agentCfg, "claude")
	if result.Endpoint != "https://api.example.com" {
		t.Errorf("endpoint mismatch: %s", result.Endpoint)
	}
	if result.Model != "claude-sonnet-4-6" {
		t.Errorf("model mismatch: %s", result.Model)
	}
	if result.Type != "claude_cli" {
		t.Errorf("type mismatch: %s", result.Type)
	}
}
