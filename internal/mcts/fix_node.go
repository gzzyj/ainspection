package mcts

import (
	"context"
	"fmt"
)

// PlanStepPayload fix 阶段的节点载荷：一个修复候选方案。
type PlanStepPayload struct {
	StepID      string          `json:"step_id"`                // 对应 plan.steps[].id
	Action      string          `json:"action"`                 // 步骤描述
	Target      string          `json:"target"`                 // 目标文件
	Approach    string          `json:"approach"`               // 具体方法
	Diff        string          `json:"diff"`                   // 候选 unified diff 内容
	RootCause   string          `json:"root_cause,omitempty"`   // 根因位置/类型/上下文
	TestCases   []string        `json:"test_cases,omitempty"`   // Deprecated: 失败用例列表（使用 Tests 替代）
	Tests       []TestContext   `json:"tests,omitempty"`        // 结构化测试上下文
	Constraints string          `json:"constraints,omitempty"`  // Deprecated: 改动行数上限等约束（使用 FixCon 替代）
	FixCon      *FixConstraints `json:"fix_con,omitempty"`      // 结构化修复约束
}

// NewFixRoot 创建一个 fix 搜索树的根节点。
func NewFixRoot(id string, p PlanStepPayload) *node {
	return &node{
		id:      id,
		payload: p,
		depth:   0,
	}
}

// NewPlanStepNode 创建一个修复方案子节点。
func NewPlanStepNode(id string, parent *node, p PlanStepPayload, depth int) *node {
	return &node{
		id:      id,
		parent:  parent,
		payload: p,
		depth:   depth,
	}
}

// GetPlanStep 从节点提取 PlanStepPayload（类型安全）。
func GetPlanStep(n *node) (PlanStepPayload, error) {
	if n == nil {
		return PlanStepPayload{}, fmt.Errorf("node is nil")
	}
	p, ok := n.payload.(PlanStepPayload)
	if !ok {
		return PlanStepPayload{}, fmt.Errorf("node payload is not PlanStepPayload")
	}
	return p, nil
}

// IsPlanStepNode 检查节点是否为修复方案节点。
func IsPlanStepNode(n *node) bool {
	_, ok := n.payload.(PlanStepPayload)
	return ok
}

// BuildStepPath 从当前节点沿父链收集所有步骤。
func BuildStepPath(n *node) []PlanStepPayload {
	var path []PlanStepPayload
	for current := n; current != nil; current = current.parent {
		if p, err := GetPlanStep(current); err == nil {
			path = append([]PlanStepPayload{p}, path...)
		}
	}
	return path
}

// MakeFixExpander 将简化签名（仅处理 PlanStepPayload）的函数适配为 NodeExpander。
//
// 调用方（orchestrator）无需知道 *node 类型，只需实现修复方案拆分逻辑。
func MakeFixExpander(fn func(ctx context.Context, p PlanStepPayload, depth int) ([]PlanStepPayload, error)) NodeExpander {
	return func(ctx context.Context, n *node) ([]*node, error) {
		p, err := GetPlanStep(n)
		if err != nil {
			return nil, nil // 非 plan step 节点不展开
		}
		children, err := fn(ctx, p, n.depth)
		if err != nil {
			return nil, err
		}
		nodes := make([]*node, len(children))
		for i, cp := range children {
			nodes[i] = NewPlanStepNode(n.id+"-c"+itoa(i+1), n, cp, n.depth+1)
		}
		return nodes, nil
	}
}

// MakeFixScorer 将简化签名（仅处理 PlanStepPayload）的函数适配为 Scorer。
func MakeFixScorer(fn func(ctx context.Context, p PlanStepPayload) (float64, error)) Scorer {
	return func(ctx context.Context, n *node) (float64, error) {
		p, err := GetPlanStep(n)
		if err != nil {
			return 0.1, nil // 非 plan step 节点给低分
		}
		return fn(ctx, p)
	}
}

// MakeFixScorerWithDims 将返回多维评分的函数适配为 Scorer，自动存储维度分到节点。
func MakeFixScorerWithDims(fn func(ctx context.Context, p PlanStepPayload) (float64, DimensionScores, error)) Scorer {
	return func(ctx context.Context, n *node) (float64, error) {
		p, err := GetPlanStep(n)
		if err != nil {
			return 0.1, nil
		}
		score, dims, err := fn(ctx, p)
		if err != nil {
			return 0.1, err
		}
		n.SetDimensionScores(dims)
		return score, nil
	}
}
