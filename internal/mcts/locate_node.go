package mcts

import (
	"context"
	"fmt"
)

// HypothesisPayload locate 阶段的节点载荷：一个根因假设。
type HypothesisPayload struct {
	Hypothesis    string        `json:"hypothesis"`                // 假设描述
	Evidence      []string      `json:"evidence"`                  // 证据路径
	SourceContext string        `json:"source_context,omitempty"`  // Deprecated: 函数/文件/行号范围（使用 SourceCtx 替代）
	SourceCtx     *SourceContext `json:"source_ctx,omitempty"`     // 结构化代码位置上下文
	UserHints     []string      `json:"user_hints,omitempty"`      // 关键词/可疑位置
	Confidence    float64       `json:"confidence,omitempty"`      // 置信度 0-1
}

// NewLocateRoot 创建一个 locate 搜索树的根节点。
func NewLocateRoot(id string, h HypothesisPayload) *node {
	return &node{
		id:      id,
		payload: h,
		depth:   0,
	}
}

// NewHypothesisNode 创建一个假设子节点。
func NewHypothesisNode(id string, parent *node, h HypothesisPayload, depth int) *node {
	return &node{
		id:      id,
		parent:  parent,
		payload: h,
		depth:   depth,
	}
}

// GetHypothesis 从节点提取 HypothesisPayload（类型安全）。
func GetHypothesis(n *node) (HypothesisPayload, error) {
	if n == nil {
		return HypothesisPayload{}, fmt.Errorf("node is nil")
	}
	h, ok := n.payload.(HypothesisPayload)
	if !ok {
		return HypothesisPayload{}, fmt.Errorf("node payload is not HypothesisPayload")
	}
	return h, nil
}

// IsHypothesisNode 检查节点是否为假设节点。
func IsHypothesisNode(n *node) bool {
	_, ok := n.payload.(HypothesisPayload)
	return ok
}

// BuildHypothesisPath 从当前节点沿父链收集所有假设，形成假设链（root → current）。
func BuildHypothesisPath(n *node) []HypothesisPayload {
	var path []HypothesisPayload
	for current := n; current != nil; current = current.parent {
		if h, err := GetHypothesis(current); err == nil {
			path = append([]HypothesisPayload{h}, path...)
		}
	}
	return path
}

// MakeLocateExpander 将简化签名（仅处理 HypothesisPayload）的函数适配为 NodeExpander。
//
// 调用方（orchestrator）无需知道 *node 类型，只需实现假设拆分逻辑。
func MakeLocateExpander(fn func(ctx context.Context, h HypothesisPayload, depth int) ([]HypothesisPayload, error)) NodeExpander {
	return func(ctx context.Context, n *node) ([]*node, error) {
		h, err := GetHypothesis(n)
		if err != nil {
			return nil, nil // 非假设节点不展开
		}
		children, err := fn(ctx, h, n.depth)
		if err != nil {
			return nil, err
		}
		nodes := make([]*node, len(children))
		for i, ch := range children {
			nodes[i] = NewHypothesisNode(n.id+"-c"+itoa(i+1), n, ch, n.depth+1)
		}
		return nodes, nil
	}
}

// MakeLocateScorer 将简化签名（仅处理 HypothesisPayload）的函数适配为 Scorer。
func MakeLocateScorer(fn func(ctx context.Context, h HypothesisPayload) (float64, error)) Scorer {
	return func(ctx context.Context, n *node) (float64, error) {
		h, err := GetHypothesis(n)
		if err != nil {
			return 0.1, nil // 非假设节点给低分
		}
		return fn(ctx, h)
	}
}

// MakeLocateScorerWithDims 将返回多维评分的函数适配为 Scorer，自动存储维度分到节点。
func MakeLocateScorerWithDims(fn func(ctx context.Context, h HypothesisPayload) (float64, DimensionScores, error)) Scorer {
	return func(ctx context.Context, n *node) (float64, error) {
		h, err := GetHypothesis(n)
		if err != nil {
			return 0.1, nil
		}
		score, dims, err := fn(ctx, h)
		if err != nil {
			return 0.1, err
		}
		n.SetDimensionScores(dims)
		return score, nil
	}
}

// itoa 简单 int→string（避免导入 strconv）。
func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var buf [20]byte
	i := len(buf)
	neg := false
	if n < 0 {
		neg = true
		n = -n
	}
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}
