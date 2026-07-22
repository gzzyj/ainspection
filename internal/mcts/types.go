// Package mcts 提供双阶段 MCTS（蒙特卡洛树搜索）引擎（P1-2）。
//
// locate 阶段在假设空间搜索根因，fix 阶段在修复方案空间搜索最优 diff。
// 两者共享通用 Engine，通过 Budget / Expander / Scorer 差异化。
package mcts

import (
	"context"
	"sort"
)

// —————— 树节点 ——————

// node MCTS 搜索树的基础节点，包含树结构和统计信息。
// payload 字段承载具体阶段类型（Hypothesis 或 PlanStep）。
type node struct {
	id              string
	parent          *node
	children        []*node
	visits          int
	totalScore      float64
	rolloutReward   float64         // Rollout 累积奖励（区分单次打分和 rollout 复合奖励）
	dimScores       DimensionScores // LLM 多维评分（维度分 + 加权总分）
	depth           int
	payload         any // HypothesisPayload 或 PlanStepPayload
	failedExpansion bool // expansion 失败后不再重试
}

// ID 返回节点标识。
func (n *node) ID() string { return n.id }

// Parent 返回父节点。
func (n *node) Parent() *node { return n.parent }

// Children 返回子节点列表。
func (n *node) Children() []*node { return n.children }

// Visits 返回访问次数。
func (n *node) Visits() int { return n.visits }

// AvgScore 返回平均得分。
func (n *node) AvgScore() float64 {
	if n.visits == 0 {
		return 0
	}
	return n.totalScore / float64(n.visits)
}

// Depth 返回节点深度。
func (n *node) Depth() int { return n.depth }

// Payload 返回节点的业务载荷。
func (n *node) Payload() any { return n.payload }

// Update 回传一次仿真得分。
func (n *node) Update(score float64) {
	n.visits++
	n.totalScore += score
}

// SetRolloutReward 设置 Rollout 累积奖励（区分单次打分）。
func (n *node) SetRolloutReward(reward float64) {
	n.rolloutReward = reward
}

// RolloutReward 返回 Rollout 累积奖励。
func (n *node) RolloutReward() float64 {
	return n.rolloutReward
}

// SetDimensionScores 存储 LLM 返回的多维评分数据。
func (n *node) SetDimensionScores(ds DimensionScores) {
	n.dimScores = ds
}

// DimensionScores 返回多维评分数据。
func (n *node) DimensionScores() DimensionScores {
	return n.dimScores
}

// AddChild 添加子节点。
func (n *node) AddChild(child *node) {
	n.children = append(n.children, child)
}

// childCount 返回直接子节点数。
func (n *node) childCount() int { return len(n.children) }

// —————— 多维评分 ——————

// DimensionScores 存储 LLM 返回的多维评分数据。
type DimensionScores struct {
	Dimensions map[string]float64 `json:"dimensions"`
	Aggregate  float64            `json:"aggregate"`
}

// —————— 结构化载荷类型 ——————

// SourceContext 描述代码位置的上下文信息。
type SourceContext struct {
	FilePath  string `json:"file_path"`
	FuncName  string `json:"func_name"`
	LineStart int    `json:"line_start"`
	LineEnd   int    `json:"line_end"`
}

// Valid 检查 SourceContext 是否包含足够信息用于定位操作目标。
func (s SourceContext) Valid() bool { return s.FilePath != "" }

// TestContext 描述测试执行上下文。
type TestContext struct {
	Command        string `json:"command"`
	ExpectedOutput string `json:"expected_output"`
	TimeoutS       int    `json:"timeout_s"`
}

// Valid 检查 TestContext 是否包含可执行的测试命令。
func (t TestContext) Valid() bool { return t.Command != "" }

// FixConstraints 描述修复方案的约束条件。
type FixConstraints struct {
	MaxLines       int    `json:"max_lines,omitempty"`
	AllowAPIChange bool   `json:"allow_api_change"`
	PerfBudgetMs   int    `json:"perf_budget_ms,omitempty"`
	Notes          string `json:"notes,omitempty"`
}

// Valid 检查 FixConstraints 是否包含有意义的约束。
func (f FixConstraints) Valid() bool { return f.MaxLines > 0 || f.AllowAPIChange }

// —————— 预算 ——————

// Budget 控制单次 MCTS 搜索的资源消耗。
type Budget struct {
	MaxIterations   int   // 最大迭代次数
	MaxDepth        int   // 最大搜索深度
	BranchingFactor int   // 每次 Expansion 生成的子节点数
	MaxTokens       int64 // 最大 token 消耗量（0 表示不限制）
}

// DefaultLocateBudget 返回 locate 阶段的默认预算。
func DefaultLocateBudget() Budget {
	return Budget{MaxIterations: 16, MaxDepth: 4, BranchingFactor: 3}
}

// DefaultFixBudget 返回 fix 阶段的默认预算。
func DefaultFixBudget() Budget {
	return Budget{MaxIterations: 8, MaxDepth: 3, BranchingFactor: 2}
}

// —————— 配置 ——————

// Config MCTS 引擎完整配置。
type Config struct {
	LocateBudget Budget
	FixBudget    Budget
	UCBC         float64
}

// DefaultConfig 返回默认配置（对齐 config.yaml.example）。
func DefaultConfig() Config {
	return Config{
		LocateBudget: DefaultLocateBudget(),
		FixBudget:    DefaultFixBudget(),
		UCBC:         1.41,
	}
}

// —————— 扩展器 & 评分器 ——————

// NodeExpander 扩展函数：给定父节点，生成子节点。
// 由外部注入（locate 用 mcts-expand 模板 + locate prompt，
// fix 用 mcts-expand 模板 + fix prompt）。
type NodeExpander func(ctx context.Context, parent *node) ([]*node, error)

// Scorer 评分函数：对节点打分（0.0 ~ 1.0）。
// 由 Evaluator 的 MCTSScorer 接口实现。
type Scorer func(ctx context.Context, n *node) (float64, error)

// —————— 引擎 ——————

// TokenUsageFunc 返回当前累计 token 消耗量（由外部计数器提供）。
type TokenUsageFunc func() int64

// Engine MCTS 搜索引擎。
type Engine struct {
	budget      Budget
	ucbC        float64
	expander    NodeExpander
	scorer      Scorer
	usedTokens  int64
	tokenUsage  TokenUsageFunc // 外部 token 计数器，返回累计消耗量
}

// NewEngine 创建 MCTS 引擎。
func NewEngine(budget Budget, ucbC float64, expander NodeExpander, scorer Scorer) *Engine {
	return &Engine{
		budget:   budget,
		ucbC:     ucbC,
		expander: expander,
		scorer:   scorer,
	}
}

// SetTokenUsage 设置外部 token 计数器回调。
func (e *Engine) SetTokenUsage(fn TokenUsageFunc) {
	e.tokenUsage = fn
}

// NewLocateEngine 创建用于 locate 阶段的引擎。
func NewLocateEngine(expander NodeExpander, scorer Scorer) *Engine {
	b := DefaultLocateBudget()
	return NewEngine(b, DefaultConfig().UCBC, expander, scorer)
}

// NewFixEngine 创建用于 fix 阶段的引擎。
func NewFixEngine(expander NodeExpander, scorer Scorer) *Engine {
	b := DefaultFixBudget()
	return NewEngine(b, DefaultConfig().UCBC, expander, scorer)
}

// —————— 结果排序 ——————

// TopK 返回按 AvgScore 降序排列的前 K 个节点。
func TopK(nodes []*node, k int) []*node {
	sort.Slice(nodes, func(i, j int) bool {
		return nodes[i].AvgScore() > nodes[j].AvgScore()
	})
	if k > len(nodes) {
		k = len(nodes)
	}
	return nodes[:k]
}

// AllLeaves 收集树中所有的叶子节点（无子节点）。
func AllLeaves(root *node) []*node {
	var leaves []*node
	collectLeaves(root, &leaves)
	return leaves
}

func collectLeaves(n *node, leaves *[]*node) {
	if len(n.children) == 0 {
		*leaves = append(*leaves, n)
		return
	}
	for _, child := range n.children {
		collectLeaves(child, leaves)
	}
}
