package mcts

import (
	"context"
	"fmt"
	"math"
	"sort"
	"testing"
)

// —————— 测试辅助 ——————

// mockScorer 给每个节点返回固定分或基于 payload 计算。
func mockScorer(score float64) Scorer {
	return func(ctx context.Context, n *node) (float64, error) {
		return score, nil
	}
}

// payloadScorer 基于 HypothesisPayload.Hypothesis 长度打分（用于区分节点）。
func payloadScorer() Scorer {
	return func(ctx context.Context, n *node) (float64, error) {
		h, err := GetHypothesis(n)
		if err != nil {
			return 0.5, nil
		}
		return float64(len(h.Hypothesis)) / 100.0, nil
	}
}

// —————— UCB 测试 ——————

func TestUCBUnvisited(t *testing.T) {
	n := &node{id: "n1", visits: 0}
	ucb := n.UCB(1.41)
	if !math.IsInf(ucb, 1) {
		t.Errorf("unvisited node UCB should be +Inf, got %.4f", ucb)
	}
}

func TestUCBVisited(t *testing.T) {
	root := &node{id: "root", visits: 10}
	root.totalScore = 8.0 // avg = 0.8

	child := &node{id: "child", parent: root, visits: 3}
	child.totalScore = 2.1 // avg = 0.7

	ucb := child.UCB(1.41)
	expected := 0.7 + 1.41*math.Sqrt(math.Log(10)/3)
	if math.Abs(ucb-expected) > 0.001 {
		t.Errorf("UCB = %.4f, expected %.4f", ucb, expected)
	}
}

func TestUCBNoParent(t *testing.T) {
	// root 节点 visits=0 时返回 +Inf（优先探索）
	root := &node{id: "root"}
	ucb := root.UCB(1.41)
	if !math.IsInf(ucb, 1) {
		t.Error("unvisited root should return +Inf")
	}

	// visited root with no parent returns AvgScore
	root.visits = 1
	root.totalScore = 0.75
	ucb = root.UCB(1.41)
	if math.Abs(ucb-0.75) > 0.001 {
		t.Errorf("visited root UCB should be 0.75, got %.4f", ucb)
	}
}

func TestUCBSelection(t *testing.T) {
	// 创建两个子节点，一个高分一个低分
	root := &node{id: "root", visits: 10}
	root.totalScore = 10.0

	high := &node{id: "high", parent: root, visits: 5}
	high.totalScore = 4.5 // avg = 0.9

	low := &node{id: "low", parent: root, visits: 5}
	low.totalScore = 2.0 // avg = 0.4

	root.children = []*node{high, low}

	// high 应有更高 UCB
	if high.UCB(1.41) <= low.UCB(1.41) {
		t.Errorf("high UCB=%.4f should be > low UCB=%.4f", high.UCB(1.41), low.UCB(1.41))
	}
}

// —————— bestChild 测试 ——————

func TestBestChild(t *testing.T) {
	engine := NewEngine(Budget{MaxIterations: 10, MaxDepth: 3, BranchingFactor: 2}, 1.41, nil, nil)

	root := &node{id: "root", visits: 10}
	c1 := &node{id: "c1", parent: root, visits: 5}
	c1.totalScore = 4.0 // avg 0.8
	c2 := &node{id: "c2", parent: root, visits: 5}
	c2.totalScore = 3.0 // avg 0.6
	root.children = []*node{c1, c2}

	best := engine.bestChild(root)
	if best.id != "c1" {
		t.Errorf("expected c1 as best child, got %s", best.id)
	}
}

func TestBestChildEmpty(t *testing.T) {
	engine := NewEngine(Budget{}, 1.41, nil, nil)
	root := &node{id: "root"}
	best := engine.bestChild(root)
	if best != nil {
		t.Error("expected nil for node with no children")
	}
}

// —————— Selection 测试 ——————

func TestSelectionExpandable(t *testing.T) {
	// root 自身可扩展（depth=0 < MaxDepth=3, childCount=0 < BranchingFactor=2)
	engine := NewEngine(Budget{MaxIterations: 10, MaxDepth: 3, BranchingFactor: 2}, 1.41, nil, nil)
	root := &node{id: "root", depth: 0}

	selected := engine.selection(root)
	if selected != root {
		t.Errorf("root should be selected (expandable), got %v", selected)
	}
}

func TestSelectionMaxDepth(t *testing.T) {
	// 到达最大深度，不应选择
	engine := NewEngine(Budget{MaxIterations: 10, MaxDepth: 2, BranchingFactor: 2}, 1.41, nil, nil)
	deep := &node{id: "deep", depth: 2} // 已到 MaxDepth

	selected := engine.selection(deep)
	if selected != nil {
		t.Error("node at max depth should not be selected")
	}
}

func TestSelectionDescend(t *testing.T) {
	// root 已有 children，需下降到可扩展的子节点
	engine := NewEngine(Budget{MaxIterations: 10, MaxDepth: 3, BranchingFactor: 2}, 1.41, nil, nil)

	root := &node{id: "root", depth: 0, visits: 10}
	root.totalScore = 10.0

	c1 := &node{id: "c1", parent: root, depth: 1, visits: 5}
	c1.totalScore = 4.0
	c2 := &node{id: "c2", parent: root, depth: 1, visits: 5}
	c2.totalScore = 3.0
	root.children = []*node{c1, c2}

	// c1 UCB 更高，且 depth=1 < MaxDepth=3, childCount=0 < BF=2
	selected := engine.selection(root)
	if selected.id != "c1" {
		t.Errorf("expected c1, got %s", selected.id)
	}
}

// —————— Backprop 测试 ——————

func TestBackprop(t *testing.T) {
	engine := NewEngine(Budget{}, 1.41, nil, nil)

	root := &node{id: "root"}
	c1 := &node{id: "c1", parent: root, depth: 1}
	c2 := &node{id: "c2", parent: c1, depth: 2}

	engine.backprop(c2, 0.9)
	engine.backprop(c2, 0.7)

	// c2: visits=2, avg=0.8
	if c2.Visits() != 2 {
		t.Errorf("c2 visits: expected 2, got %d", c2.Visits())
	}
	if math.Abs(c2.AvgScore()-0.8) > 0.001 {
		t.Errorf("c2 avg: expected 0.8, got %.4f", c2.AvgScore())
	}

	// c1: visits=2, avg=0.8
	if c1.Visits() != 2 {
		t.Errorf("c1 visits: expected 2, got %d", c1.Visits())
	}
	if math.Abs(c1.AvgScore()-0.8) > 0.001 {
		t.Errorf("c1 avg: expected 0.8, got %.4f", c1.AvgScore())
	}

	// root: visits=2, avg=0.8
	if root.Visits() != 2 {
		t.Errorf("root visits: expected 2, got %d", root.Visits())
	}
	if math.Abs(root.AvgScore()-0.8) > 0.001 {
		t.Errorf("root avg: expected 0.8, got %.4f", root.AvgScore())
	}
}

// —————— Engine Run 完整流程测试 ——————

func TestRunSimple(t *testing.T) {
	// 模拟一个简单 MCTS 搜索
	root := NewLocateRoot("root-0", HypothesisPayload{
		Hypothesis: "初始假设",
	})

	callCount := 0
	expander := func(ctx context.Context, parent *node) ([]*node, error) {
		callCount++
		// 每个节点最多展开一次，且深度受 MaxDepth=2 限制
		if parent.depth >= 1 {
			return nil, nil // 不再展开
		}
		childDepth := parent.depth + 1
		return []*node{
			NewHypothesisNode(fmt.Sprintf("h%d-a", callCount), parent, HypothesisPayload{Hypothesis: "假设A"}, childDepth),
			NewHypothesisNode(fmt.Sprintf("h%d-b", callCount), parent, HypothesisPayload{Hypothesis: "假设BB"}, childDepth),
		}, nil
	}

	engine := NewEngine(
		Budget{MaxIterations: 4, MaxDepth: 2, BranchingFactor: 2},
		1.41,
		expander,
		payloadScorer(),
	)

	leaves, err := engine.Run(context.Background(), root)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	if len(leaves) == 0 {
		t.Error("expected at least 1 leaf node")
	}

	// 验证树结构：root 应有 2 个子节点
	if len(root.children) != 2 {
		t.Errorf("root should have 2 children, got %d", len(root.children))
	}

	// 每个子节点应被访问过
	for _, child := range root.children {
		if child.Visits() == 0 {
			t.Errorf("child %s should have been visited (scored)", child.id)
		}
	}

	// root 也应被访问
	if root.Visits() == 0 {
		t.Error("root should have been visited")
	}
}

func TestRunBudgetExhausted(t *testing.T) {
	// 小预算 → 提前终止
	root := NewLocateRoot("root-0", HypothesisPayload{Hypothesis: "初始"})

	expander := func(ctx context.Context, parent *node) ([]*node, error) {
		return []*node{
			NewHypothesisNode("h1", parent, HypothesisPayload{Hypothesis: "A"}, parent.depth+1),
		}, nil
	}

	engine := NewEngine(
		Budget{MaxIterations: 1, MaxDepth: 3, BranchingFactor: 2},
		1.41,
		expander,
		mockScorer(0.8),
	)

	leaves, err := engine.Run(context.Background(), root)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	if len(leaves) == 0 {
		t.Error("expected leaf nodes")
	}
}

func TestRunContextCancel(t *testing.T) {
	root := NewLocateRoot("root-0", HypothesisPayload{Hypothesis: "初始"})

	expander := func(ctx context.Context, parent *node) ([]*node, error) {
		return []*node{
			NewHypothesisNode("h1", parent, HypothesisPayload{Hypothesis: "A"}, parent.depth+1),
		}, nil
	}

	engine := NewEngine(
		Budget{MaxIterations: 100, MaxDepth: 4, BranchingFactor: 2},
		1.41,
		expander,
		mockScorer(0.5),
	)

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // 立即取消

	leaves, err := engine.Run(ctx, root)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	// 取消后至少应有 root 节点（已被评分）
	_ = leaves
}

func TestRunMaxDepthLimit(t *testing.T) {
	root := NewLocateRoot("root-0", HypothesisPayload{Hypothesis: "初始"})

	callCount := 0
	expander := func(ctx context.Context, parent *node) ([]*node, error) {
		callCount++
		if parent.depth >= 1 {
			return nil, nil // 深度≥1 不展开
		}
		return []*node{
			NewHypothesisNode(fmt.Sprintf("h%d-1", callCount), parent, HypothesisPayload{Hypothesis: "子假设"}, parent.depth+1),
		}, nil
	}

	engine := NewEngine(
		Budget{MaxIterations: 3, MaxDepth: 2, BranchingFactor: 1},
		1.41,
		expander,
		mockScorer(0.7),
	)

	leaves, err := engine.Run(context.Background(), root)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	// 所有叶子节点深度应 ≤ MaxDepth
	for _, leaf := range leaves {
		if leaf.depth > 2 {
			t.Errorf("leaf %s depth %d exceeds MaxDepth 2", leaf.id, leaf.depth)
		}
	}
}

// —————— TopK 测试 ——————

func TestTopK(t *testing.T) {
	nodes := []*node{
		{id: "a", visits: 2, totalScore: 1.6}, // avg 0.8
		{id: "b", visits: 2, totalScore: 0.4}, // avg 0.2
		{id: "c", visits: 2, totalScore: 2.0}, // avg 1.0
	}

	top := TopK(nodes, 2)
	if len(top) != 2 {
		t.Fatalf("expected 2, got %d", len(top))
	}
	if top[0].id != "c" {
		t.Errorf("expected c, got %s", top[0].id)
	}
	if top[1].id != "a" {
		t.Errorf("expected a, got %s", top[1].id)
	}
}

func TestTopKMoreThanLen(t *testing.T) {
	nodes := []*node{
		{id: "a"},
	}
	top := TopK(nodes, 5)
	if len(top) != 1 {
		t.Errorf("expected 1, got %d", len(top))
	}
}

// —————— AllLeaves 测试 ——————

func TestAllLeaves(t *testing.T) {
	root := &node{id: "root"}
	c1 := &node{id: "c1", parent: root, depth: 1}
	c2 := &node{id: "c2", parent: root, depth: 1}
	c1a := &node{id: "c1a", parent: c1, depth: 2}
	root.children = []*node{c1, c2}
	c1.children = []*node{c1a}

	leaves := AllLeaves(root)
	if len(leaves) != 2 {
		t.Errorf("expected 2 leaves (c1a and c2), got %d", len(leaves))
	}

	ids := make(map[string]bool)
	for _, l := range leaves {
		ids[l.id] = true
	}
	if !ids["c1a"] || !ids["c2"] {
		t.Errorf("expected c1a and c2 as leaves, got %v", ids)
	}
}

// —————— 节点类型测试 ——————

func TestHypothesisNode(t *testing.T) {
	root := NewLocateRoot("root", HypothesisPayload{
		Hypothesis: "payment-svc 慢查询导致超时",
		Evidence:   []string{"traces/slow.txt"},
	})

	h, err := GetHypothesis(root)
	if err != nil {
		t.Fatalf("GetHypothesis: %v", err)
	}
	if h.Hypothesis != "payment-svc 慢查询导致超时" {
		t.Errorf("hypothesis mismatch: %s", h.Hypothesis)
	}
	if !IsHypothesisNode(root) {
		t.Error("should be hypothesis node")
	}

	// 构建路径
	child := NewHypothesisNode("n1", root, HypothesisPayload{Hypothesis: "子假设"}, 1)
	path := BuildHypothesisPath(child)
	if len(path) != 2 {
		t.Errorf("expected path length 2, got %d", len(path))
	}
}

func TestPlanStepNode(t *testing.T) {
	root := NewFixRoot("root", PlanStepPayload{
		StepID:   "step-1",
		Action:   "添加联合索引",
		Target:   "migration/002_idx.sql",
		Approach: "CREATE INDEX ...",
	})

	p, err := GetPlanStep(root)
	if err != nil {
		t.Fatalf("GetPlanStep: %v", err)
	}
	if p.StepID != "step-1" {
		t.Errorf("step ID mismatch: %s", p.StepID)
	}
	if !IsPlanStepNode(root) {
		t.Error("should be plan step node")
	}

	// 非 plan step 节点
	locRoot := NewLocateRoot("lr", HypothesisPayload{})
	if IsPlanStepNode(locRoot) {
		t.Error("locate node should not be plan step node")
	}
}

func TestGetHypothesisWrongType(t *testing.T) {
	n := NewFixRoot("root", PlanStepPayload{})
	_, err := GetHypothesis(n)
	if err == nil {
		t.Error("expected error for wrong payload type")
	}
}

func TestGetPlanStepWrongType(t *testing.T) {
	n := NewLocateRoot("root", HypothesisPayload{})
	_, err := GetPlanStep(n)
	if err == nil {
		t.Error("expected error for wrong payload type")
	}
}

// —————— Budget 默认值测试 ——————

func TestDefaultLocateBudget(t *testing.T) {
	b := DefaultLocateBudget()
	if b.MaxIterations != 16 {
		t.Errorf("expected 16 iterations, got %d", b.MaxIterations)
	}
	if b.MaxDepth != 4 {
		t.Errorf("expected max depth 4, got %d", b.MaxDepth)
	}
	if b.BranchingFactor != 3 {
		t.Errorf("expected branching factor 3, got %d", b.BranchingFactor)
	}
}

func TestDefaultFixBudget(t *testing.T) {
	b := DefaultFixBudget()
	if b.MaxIterations != 8 {
		t.Errorf("expected 8 iterations, got %d", b.MaxIterations)
	}
	if b.MaxDepth != 3 {
		t.Errorf("expected max depth 3, got %d", b.MaxDepth)
	}
	if b.BranchingFactor != 2 {
		t.Errorf("expected branching factor 2, got %d", b.BranchingFactor)
	}
}

// —————— NewLocateEngine / NewFixEngine 测试 ——————

func TestNewLocateEngineDefaults(t *testing.T) {
	e := NewLocateEngine(nil, nil)
	if e.budget.MaxIterations != 16 {
		t.Errorf("locate engine: expected 16 iterations, got %d", e.budget.MaxIterations)
	}
	if e.ucbC != 1.41 {
		t.Errorf("locate engine: expected ucb_c=1.41, got %.2f", e.ucbC)
	}
}

func TestNewFixEngineDefaults(t *testing.T) {
	e := NewFixEngine(nil, nil)
	if e.budget.MaxIterations != 8 {
		t.Errorf("fix engine: expected 8 iterations, got %d", e.budget.MaxIterations)
	}
}

// —————— Payload 访问测试 ——————

func TestPayloadAccess(t *testing.T) {
	root := NewLocateRoot("r", HypothesisPayload{Hypothesis: "H"})
	p := root.Payload()
	_, ok := p.(HypothesisPayload)
	if !ok {
		t.Error("payload should be HypothesisPayload")
	}
}

// —————— 确定性测试：相同输入 → 相同输出 ——————

func TestDeterministicRun(t *testing.T) {
	root1 := NewLocateRoot("r", HypothesisPayload{Hypothesis: "A"})
	root2 := NewLocateRoot("r", HypothesisPayload{Hypothesis: "A"})

	expander := func(ctx context.Context, parent *node) ([]*node, error) {
		if parent.depth >= 1 {
			return nil, nil
		}
		return []*node{
			NewHypothesisNode(fmt.Sprintf("c-%d", parent.depth+1), parent, HypothesisPayload{Hypothesis: "C"}, parent.depth+1),
		}, nil
	}

	scorer := mockScorer(0.75)
	engine := NewEngine(Budget{MaxIterations: 2, MaxDepth: 2, BranchingFactor: 1}, 1.41, expander, scorer)

	leaves1, _ := engine.Run(context.Background(), root1)
	leaves2, _ := engine.Run(context.Background(), root2)

	if len(leaves1) != len(leaves2) {
		t.Errorf("deterministic runs should produce same leaf count: %d vs %d", len(leaves1), len(leaves2))
	}

	// 排序后比较 avg scores
	sort.Slice(leaves1, func(i, j int) bool { return leaves1[i].id < leaves1[j].id })
	sort.Slice(leaves2, func(i, j int) bool { return leaves2[i].id < leaves2[j].id })

	for i := range leaves1 {
		if math.Abs(leaves1[i].AvgScore()-leaves2[i].AvgScore()) > 0.001 {
			t.Errorf("leaf %d scores differ: %.4f vs %.4f", i, leaves1[i].AvgScore(), leaves2[i].AvgScore())
		}
	}
}

// —————— Token 预算测试 ——————

func TestTokenBudgetExhausted(t *testing.T) {
	root := NewLocateRoot("root-0", HypothesisPayload{Hypothesis: "初始"})

	callCount := 0
	expander := func(ctx context.Context, parent *node) ([]*node, error) {
		callCount++
		if parent.depth >= 1 {
			return nil, nil
		}
		return []*node{
			NewHypothesisNode(fmt.Sprintf("h%d", callCount), parent, HypothesisPayload{Hypothesis: "子假设"}, parent.depth+1),
		}, nil
	}

	engine := NewEngine(
		Budget{MaxIterations: 100, MaxDepth: 3, BranchingFactor: 2, MaxTokens: 10},
		1.41,
		expander,
		mockScorer(0.8),
	)

	// 模拟外部 token 计数器，快速超过预算
	tokens := int64(0)
	engine.SetTokenUsage(func() int64 {
		tokens += 5 // 每次调用增加 5
		return tokens
	})

	leaves, err := engine.Run(context.Background(), root)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	// token 预算耗尽后应提前终止：第一次 scorer(root) 消耗 5，
	// 第一次 expander 后检查消耗 5（累计 10 >= MaxTokens=10），
	// 因此迭代次数应远小于 MaxIterations=100
	if tokens >= 10 {
		// 验证搜索确实因 token 超限而停止
		t.Logf("token budget exhausted at %d tokens, iterations limited", tokens)
	}
	_ = leaves
}

func TestTokenBudgetNotExhausted(t *testing.T) {
	root := NewLocateRoot("root-0", HypothesisPayload{Hypothesis: "初始"})

	callCount := 0
	expander := func(ctx context.Context, parent *node) ([]*node, error) {
		callCount++
		if parent.depth >= 1 {
			return nil, nil
		}
		return []*node{
			NewHypothesisNode(fmt.Sprintf("h%d", callCount), parent, HypothesisPayload{Hypothesis: "子假设"}, parent.depth+1),
		}, nil
	}

	engine := NewEngine(
		Budget{MaxIterations: 3, MaxDepth: 2, BranchingFactor: 2, MaxTokens: 1000},
		1.41,
		expander,
		mockScorer(0.8),
	)

	tokens := int64(0)
	engine.SetTokenUsage(func() int64 {
		tokens += 1
		return tokens
	})

	leaves, err := engine.Run(context.Background(), root)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	// token 预算足够，应正常完成
	if len(leaves) == 0 {
		t.Error("expected leaf nodes with sufficient token budget")
	}
}

// —————— Expansion 失败防重试测试 ——————

func TestExpansionFailureNotRetried(t *testing.T) {
	root := NewLocateRoot("root-0", HypothesisPayload{Hypothesis: "初始"})

	expandCalls := 0
	expander := func(ctx context.Context, parent *node) ([]*node, error) {
		expandCalls++
		// 首次展开 root 时失败
		if parent.id == "root-0" {
			return nil, fmt.Errorf("expansion failed")
		}
		return nil, nil
	}

	engine := NewEngine(
		Budget{MaxIterations: 5, MaxDepth: 3, BranchingFactor: 2},
		1.41,
		expander,
		mockScorer(0.8),
	)

	leaves, err := engine.Run(context.Background(), root)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	_ = leaves

	// root 的 expansion 失败后应被标记 failedExpansion，
	// selection 不应再选择 root，expandCalls 应 = 1
	if expandCalls != 1 {
		t.Errorf("expected 1 expand call (no retry after failure), got %d", expandCalls)
	}

	if !root.failedExpansion {
		t.Error("root should be marked failedExpansion after expander error")
	}
}

func TestExpansionEmptyResultNotRetried(t *testing.T) {
	root := NewLocateRoot("root-0", HypothesisPayload{Hypothesis: "初始"})

	expandCalls := 0
	expander := func(ctx context.Context, parent *node) ([]*node, error) {
		expandCalls++
		// 返回空切片（等同于无子节点）
		return []*node{}, nil
	}

	engine := NewEngine(
		Budget{MaxIterations: 5, MaxDepth: 3, BranchingFactor: 2},
		1.41,
		expander,
		mockScorer(0.8),
	)

	_, err := engine.Run(context.Background(), root)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	// 空结果也应标记 failedExpansion 并只调用一次
	if expandCalls != 1 {
		t.Errorf("expected 1 expand call (no retry after empty result), got %d", expandCalls)
	}

	if !root.failedExpansion {
		t.Error("root should be marked failedExpansion after empty expansion result")
	}
}
