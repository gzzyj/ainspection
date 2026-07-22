package mcts

import "math"

// UCB 计算 UCB1 值。未访问过的节点 UCB = +Inf（优先探索）。
func (n *node) UCB(c float64) float64 {
	if n.visits == 0 {
		return math.Inf(1)
	}
	if n.parent == nil || n.parent.visits == 0 {
		return n.AvgScore()
	}
	return n.AvgScore() + c*math.Sqrt(math.Log(float64(n.parent.visits))/float64(n.visits))
}
