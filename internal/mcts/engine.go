package mcts

import (
	"context"
	"fmt"
	"log"
	"math"
)

// INSTRUMENT: mcts-engine-run — MCTS 搜索主循环，四步迭代核心
// LAYER: L1
// STATUS: implemented
// Run 执行 MCTS 搜索，返回搜索树中所有叶子节点（按 AvgScore 降序）。
//
// 四步循环：
//  1. Selection  — 从 root 出发，每层选 UCB 最大的子节点，直到底层或可扩展节点
//  2. Expansion  — 调用 expander 生成子节点（≤ BranchingFactor 个），挂到选中节点
//  3. Simulation — 调用 scorer 对每个新子节点打分
//  4. Backprop   — 分数沿父链回传，更新 visits 和 totalScore
//
// 终止条件: iterations ≥ MaxIterations、token 超限或无可扩展节点。
func (e *Engine) Run(ctx context.Context, root *node) ([]*node, error) {
	if root == nil {
		return nil, fmt.Errorf("mcts: root is nil")
	}

	// 先对 root 做一次评分（如果尚未访问过）
	if root.visits == 0 {
		score, err := e.scorer(ctx, root)
		if err != nil {
			return nil, fmt.Errorf("mcts: score root: %w", err)
		}
		root.Update(score)
		e.checkTokenBudget()
	}

	for i := 0; i < e.budget.MaxIterations; i++ {
		// 检查 context 是否已取消
		if ctx.Err() != nil {
			break
		}

		// 检查 token 预算
		if e.tokenExhausted() {
			log.Printf("[MCTS] token budget exhausted (%d >= %d), stopping search", e.usedTokens, e.budget.MaxTokens)
			break
		}

		// 1. Selection: 找到可扩展的节点
		selected := e.selection(root)
		if selected == nil {
			// 无可扩展节点，提前结束
			break
		}

		// 2. Expansion: 生成子节点
		children, err := e.expander(ctx, selected)
		e.checkTokenBudget()
		if err != nil || len(children) == 0 {
			// 扩展失败或无子节点，标记节点避免重试
			selected.failedExpansion = true
			continue
		}

		// 3. Simulation: 对每个子节点评分
		for _, child := range children {
			selected.AddChild(child)
			score, err := e.scorer(ctx, child)
			e.checkTokenBudget()
			if err != nil {
				// 评分失败给 0 分
				score = 0
			}

			// 4. Backprop: 沿父链回传
			e.backprop(child, score)
		}
	}

	return AllLeaves(root), nil
}

// checkTokenBudget 检查外部 token 计数器，更新 usedTokens。
func (e *Engine) checkTokenBudget() {
	if e.tokenUsage != nil {
		e.usedTokens = e.tokenUsage()
	}
}

// tokenExhausted 判断 token 预算是否已耗尽。
func (e *Engine) tokenExhausted() bool {
	if e.budget.MaxTokens <= 0 {
		return false
	}
	return e.usedTokens >= e.budget.MaxTokens
}

// INSTRUMENT: mcts-selection — UCB 选择策略，下降至可扩展节点
// LAYER: L2
// STATUS: implemented
// selection 从 root 出发，沿 UCB 最大方向下降到可扩展节点。
// 可扩展节点: depth < MaxDepth && childCount < BranchingFactor && !failedExpansion。
func (e *Engine) selection(root *node) *node {
	current := root

	for {
		// 检查当前节点是否可扩展（expansion 失败过的节点不再尝试）
		if current.depth < e.budget.MaxDepth && current.childCount() < e.budget.BranchingFactor && !current.failedExpansion {
			return current
		}

		// 已到最大深度
		if current.depth >= e.budget.MaxDepth {
			return nil // 不再扩展
		}

		// 若无子节点但已达到 branching factor 上限 → 已饱和
		if len(current.children) == 0 {
			return nil
		}

		// 选择 UCB 最大的子节点（排除 expansion 失败过的节点）
		best := e.bestChild(current)
		if best == nil {
			return nil
		}
		current = best
	}
}

// bestChild 返回 UCB 值最大的子节点（排除 expansion 失败过的节点）。
func (e *Engine) bestChild(parent *node) *node {
	if len(parent.children) == 0 {
		return nil
	}

	var best *node
	bestUCB := math.Inf(-1)

	for _, child := range parent.children {
		// 跳过 expansion 失败的节点
		if child.failedExpansion {
			continue
		}
		ucb := child.UCB(e.ucbC)
		if ucb > bestUCB {
			bestUCB = ucb
			best = child
		}
	}

	return best
}

// INSTRUMENT: mcts-backprop — 评分沿父链回传，更新 visits 和 totalScore
// LAYER: L2
// STATUS: implemented
// backprop 从给定节点出发，沿父链一路回到 root，逐层 Update(score)。
// 若节点有 rolloutReward，则优先使用 rolloutReward 替代原始 score。
func (e *Engine) backprop(n *node, score float64) {
	for current := n; current != nil; current = current.parent {
		s := score
		if current.rolloutReward != 0 {
			s = current.rolloutReward
		}
		current.Update(s)
	}
}
