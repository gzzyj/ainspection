package orchestrator

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"git.qingteng.cn/ms/ainspection/internal/llm"
	"git.qingteng.cn/ms/ainspection/internal/mcts"
	"git.qingteng.cn/ms/ainspection/internal/prompt"
)

// LLMExpandInput 传给 mcts-expand.tmpl 模板的变量组装数据。
type LLMExpandInput struct {
	Stage           string // "locate" | "fix"
	CurrentNode     string // 当前节点的文本描述
	Depth           int
	MaxDepth        int
	BranchingFactor int
	ParentSummary   string
}

// NewLocateExpander 创建 locate 阶段的 LLM 驱动 NodeExpander。
//
// 通过 prompt.Renderer 渲染 mcts-expand.tmpl（stage="locate"），
// 调 LLM 生成子假设节点列表，返回 YAML children。
func NewLocateExpander(
	renderer prompt.Renderer,
	llmClient llm.Client,
	inputYAML []byte,
	skills []string,
	agentConfig AgentRunConfig,
) mcts.NodeExpander {
	return mcts.MakeLocateExpander(func(ctx context.Context, h mcts.HypothesisPayload, depth int) ([]mcts.HypothesisPayload, error) {
		// 构建 expand prompt
		expandInput := LLMExpandInput{
			Stage:           "locate",
			CurrentNode:     h.Hypothesis,
			Depth:           depth,
			MaxDepth:        4,
			BranchingFactor: 3,
			ParentSummary:   summarizeEvidence(h.Evidence),
		}

		systemPrompt, err := renderExpandPrompt(renderer, expandInput)
		if err != nil {
			return nil, fmt.Errorf("render expand prompt: %w", err)
		}

		// 调 LLM
		resp, err := llmClient.Chat(ctx, llm.ChatRequest{
			Model:       agentConfig.Model,
			System:      systemPrompt,
			Temperature: 0.3,
			MaxTokens:   1024,
			Messages: []llm.Message{
				{Role: "user", Content: buildExpandUserMessage(h, depth)},
			},
		})
		if err != nil {
			return nil, fmt.Errorf("llm expand: %w", err)
		}

		// 解析 LLM 返回的 children
		return parseLocateChildren(resp)
	})
}

// NewFixExpander 创建 fix 阶段的 LLM 驱动 NodeExpander。
func NewFixExpander(
	renderer prompt.Renderer,
	llmClient llm.Client,
	planJSON []byte,
	skills []string,
	agentConfig AgentRunConfig,
) mcts.NodeExpander {
	return mcts.MakeFixExpander(func(ctx context.Context, p mcts.PlanStepPayload, depth int) ([]mcts.PlanStepPayload, error) {
		expandInput := LLMExpandInput{
			Stage:           "fix",
			CurrentNode:     fmt.Sprintf("step %s: %s (%s)", p.StepID, p.Action, p.Approach),
			Depth:           depth,
			MaxDepth:        3,
			BranchingFactor: 2,
		}

		systemPrompt, err := renderExpandPrompt(renderer, expandInput)
		if err != nil {
			return nil, fmt.Errorf("render expand prompt: %w", err)
		}

		resp, err := llmClient.Chat(ctx, llm.ChatRequest{
			Model:       agentConfig.Model,
			System:      systemPrompt,
			Temperature: 0.3,
			MaxTokens:   1024,
			Messages: []llm.Message{
				{Role: "user", Content: buildFixExpandUserMessage(p, depth)},
			},
		})
		if err != nil {
			return nil, fmt.Errorf("llm expand: %w", err)
		}

		return parseFixChildren(resp, p.StepID)
	})
}

// renderExpandPrompt 渲染 mcts-expand.tmpl 生成 system prompt。
func renderExpandPrompt(renderer prompt.Renderer, input LLMExpandInput) (string, error) {
	tmplInput := prompt.MCTSExpandInput{
		Stage:           input.Stage,
		CurrentNode:     input.CurrentNode,
		Depth:           input.Depth,
		MaxDepth:        input.MaxDepth,
		BranchingFactor: input.BranchingFactor,
		ParentSummary:   input.ParentSummary,
	}
	return renderer.Render("mcts-expand", tmplInput)
}

// buildExpandUserMessage 构建 expand 的用户消息。
func buildExpandUserMessage(h mcts.HypothesisPayload, depth int) string {
	srcHint := ""
	if h.SourceCtx != nil && h.SourceCtx.Valid() {
		srcHint = fmt.Sprintf("\n父节点代码位置: %s (函数 %s, 行 %d-%d)",
			h.SourceCtx.FilePath, h.SourceCtx.FuncName, h.SourceCtx.LineStart, h.SourceCtx.LineEnd)
	}
	return fmt.Sprintf(
		"当前深度: %d\n当前假设: %s\n已有证据: %s%s\n\n请拆分为 %d 个子假设，每个子假设聚焦于一个具体方向。"+
			"若知道具体代码位置，请在 source 字段中提供 file_path, func_name, line_start, line_end。以 JSON 格式返回。",
		depth, h.Hypothesis, strings.Join(h.Evidence, ", "), srcHint, 3,
	)
}

// buildFixExpandUserMessage 构建 fix expand 的用户消息。
func buildFixExpandUserMessage(p mcts.PlanStepPayload, depth int) string {
	constraintInfo := ""
	if p.FixCon != nil && p.FixCon.Valid() {
		constraintInfo = fmt.Sprintf("\n修复约束: 最大行数%d, API变更=%v", p.FixCon.MaxLines, p.FixCon.AllowAPIChange)
	}
	testInfo := ""
	if len(p.Tests) > 0 && p.Tests[0].Valid() {
		testInfo = fmt.Sprintf("\n测试命令: %s", p.Tests[0].Command)
	}
	return fmt.Sprintf(
		"当前深度: %d\n步骤: %s\n操作: %s\n目标: %s\n方法: %s%s%s\n\n"+
			"请生成 2 个备选实现方案。若知道测试命令和修复约束，请在 tests 和 constraints 字段中提供。以 JSON 格式返回。",
		depth, p.StepID, p.Action, p.Target, p.Approach, constraintInfo, testInfo,
	)
}

// parseLocateChildren 解析 LLM 返回的 locate children。
func parseLocateChildren(resp *llm.ChatResponse) ([]mcts.HypothesisPayload, error) {
	if len(resp.Choices) == 0 {
		return nil, fmt.Errorf("llm returned empty choices")
	}

	content := resp.Choices[0].Message.Content
	children, err := parseChildrenYAML(content)
	if err != nil {
		return nil, fmt.Errorf("parse locate children: %w", err)
	}

	result := make([]mcts.HypothesisPayload, 0, len(children))
	for _, c := range children {
		payload := mcts.HypothesisPayload{
			Hypothesis: c.Hypothesis,
			Evidence:   c.Evidence,
			Confidence: c.Confidence,
		}
		// 解析结构化代码位置
		if c.Source != nil && c.Source.FilePath != "" {
			payload.SourceCtx = &mcts.SourceContext{
				FilePath:  c.Source.FilePath,
				FuncName:  c.Source.FuncName,
				LineStart: c.Source.LineStart,
				LineEnd:   c.Source.LineEnd,
			}
		}
		result = append(result, payload)
	}
	return result, nil
}

// parseFixChildren 解析 LLM 返回的 fix children。
func parseFixChildren(resp *llm.ChatResponse, parentStepID string) ([]mcts.PlanStepPayload, error) {
	if len(resp.Choices) == 0 {
		return nil, fmt.Errorf("llm returned empty choices")
	}

	content := resp.Choices[0].Message.Content
	children, err := parseChildrenYAML(content)
	if err != nil {
		return nil, fmt.Errorf("parse fix children: %w", err)
	}

	result := make([]mcts.PlanStepPayload, 0, len(children))
	for i, c := range children {
		payload := mcts.PlanStepPayload{
			StepID:   fmt.Sprintf("%s-v%d", parentStepID, i+1),
			Action:   c.Action,
			Target:   c.Target,
			Approach: c.Approach,
			Diff:     c.Diff,
		}
		// 解析结构化测试上下文
		for _, t := range c.Tests {
			if t.Command != "" {
				payload.Tests = append(payload.Tests, mcts.TestContext{
					Command:        t.Command,
					ExpectedOutput: t.ExpectedOutput,
					TimeoutS:       t.TimeoutS,
				})
			}
		}
		// 解析结构化修复约束
		if c.Constraints != nil {
			payload.FixCon = &mcts.FixConstraints{
				MaxLines:       c.Constraints.MaxLines,
				AllowAPIChange: c.Constraints.AllowAPIChange,
				PerfBudgetMs:   c.Constraints.PerfBudgetMs,
				Notes:          c.Constraints.Notes,
			}
		}
		result = append(result, payload)
	}
	return result, nil
}

// expandChild 供 YAML/JSON 解析的中间结构。
type expandChild struct {
	Hypothesis string   `json:"hypothesis" yaml:"hypothesis"`
	Evidence   []string `json:"evidence" yaml:"evidence"`
	Confidence float64  `json:"confidence" yaml:"confidence"`
	Action     string   `json:"action" yaml:"action"`
	Target     string   `json:"target" yaml:"target"`
	Approach   string   `json:"approach" yaml:"approach"`
	Diff       string   `json:"diff" yaml:"diff"`

	// 结构化上下文字段（Phase 2）
	Source      *sourceField      `json:"source,omitempty"`
	Tests       []testField       `json:"tests,omitempty"`
	Constraints *constraintField  `json:"constraints,omitempty"`
}

// sourceField LLM 返回的代码位置字段。
type sourceField struct {
	FilePath  string `json:"file_path"`
	FuncName  string `json:"func_name"`
	LineStart int    `json:"line_start"`
	LineEnd   int    `json:"line_end"`
}

// testField LLM 返回的测试描述字段。
type testField struct {
	Command        string `json:"command"`
	ExpectedOutput string `json:"expected_output"`
	TimeoutS       int    `json:"timeout_s"`
}

// constraintField LLM 返回的修复约束字段。
type constraintField struct {
	MaxLines       int    `json:"max_lines"`
	AllowAPIChange bool   `json:"allow_api_change"`
	PerfBudgetMs   int    `json:"perf_budget_ms"`
	Notes          string `json:"notes"`
}

// parseChildrenYAML 尝试从 LLM 输出中解析 children 列表。
//
// 支持两种格式：
//   - JSON: {"children": [...]}
//   - 简易 YAML/Lines: 每行一个假设（以 "- " 开头）
func parseChildrenYAML(content string) ([]expandChild, error) {
	// 尝试 JSON 解析
	if children, err := tryJSONParse(content); err == nil && len(children) > 0 {
		return children, nil
	}

	// Fallback: 按行解析（每行以 "- " 开头视为一个条目）
	return tryLineParse(content), nil
}

// tryJSONParse 尝试 JSON 解析 children。
func tryJSONParse(content string) ([]expandChild, error) {
	// 提取 JSON 块（可能在 ```json ... ``` 中）
	jsonContent := extractJSONBlock(content)

	var wrapper struct {
		Children []expandChild `json:"children"`
	}
	if err := json.Unmarshal([]byte(jsonContent), &wrapper); err != nil {
		// 尝试直接解析为数组
		var children []expandChild
		if err2 := json.Unmarshal([]byte(jsonContent), &children); err2 != nil {
			return nil, fmt.Errorf("json parse: %w (also tried array: %w)", err, err2)
		}
		return children, nil
	}
	return wrapper.Children, nil
}

// extractJSONBlock 从文本中提取 JSON 块。
func extractJSONBlock(content string) string {
	// 查找 ```json ... ``` 代码块
	start := strings.Index(content, "```json")
	if start >= 0 {
		start += 7
		if end := strings.Index(content[start:], "```"); end >= 0 {
			return strings.TrimSpace(content[start : start+end])
		}
	}
	// 查找第一个 { 或 [ 开始的位置
	if idx := strings.IndexAny(content, "{[("); idx >= 0 {
		return content[idx:]
	}
	return content
}

// tryLineParse 按行解析简单的 YAML list 格式。
func tryLineParse(content string) []expandChild {
	var children []expandChild
	lines := strings.Split(content, "\n")
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			continue
		}
		// 移除 "- " 前缀
		hypothesis := strings.TrimPrefix(trimmed, "- ")
		if hypothesis == trimmed {
			// 没有 "- " 前缀，跳过
			continue
		}
		if len(hypothesis) >= 3 {
			children = append(children, expandChild{
				Hypothesis: hypothesis,
			})
		}
	}
	return children
}

// summarizeEvidence 将证据列表合并为简短摘要。
func summarizeEvidence(evidence []string) string {
	if len(evidence) == 0 {
		return ""
	}
	if len(evidence) == 1 {
		return evidence[0]
	}
	return strings.Join(evidence[:min(3, len(evidence))], "; ")
}
