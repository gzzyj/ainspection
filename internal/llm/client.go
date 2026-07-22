// Package llm 提供 LLM API 通信层（OpenAI 兼容 API 封装）。
//
// 与 internal/skill/ 中 adapter 的区别：
//   - skill/adapter_*.go：工具定义格式转换（Skill markdown → Anthropic/OpenAI tool schema）
//   - llm/client.go：HTTP 通信（API 调用、流式处理、错误重试）
//
// 两者同名 "adapter" 但语义不同，这里统一用 "llm" package 做 API 通信。
package llm

import (
	"context"
)

// Client LLM API 通信客户端接口。
type Client interface {
	// Chat 发送一次非流式对话请求，返回助手消息。
	Chat(ctx context.Context, req ChatRequest) (*ChatResponse, error)

	// ChatStream 发送流式对话请求，通过 channel 返回增量消息。
	ChatStream(ctx context.Context, req ChatRequest) (<-chan StreamEvent, error)
}

// ChatRequest 一次 LLM 对话请求。
type ChatRequest struct {
	Model       string    `json:"model"`
	System      string    `json:"system,omitempty"` // system prompt
	Messages    []Message `json:"messages"`
	Tools       []ToolDef `json:"tools,omitempty"`
	Temperature float64   `json:"temperature,omitempty"`
	MaxTokens   int       `json:"max_tokens,omitempty"`
	Stream      bool      `json:"stream,omitempty"`
}

// Message 一条对话消息。
type Message struct {
	Role       string     `json:"role"` // system | user | assistant | tool
	Content    string     `json:"content,omitempty"`
	ToolCalls  []ToolCall `json:"tool_calls,omitempty"`   // assistant 发起
	ToolCallID string     `json:"tool_call_id,omitempty"` // tool 响应时
	Name       string     `json:"name,omitempty"`         // tool 响应时
}

// ToolDef LLM 工具定义（OpenAI 兼容格式）。
type ToolDef struct {
	Type     string      `json:"type"` // "function"
	Function FunctionDef `json:"function"`
}

// FunctionDef 函数定义。
type FunctionDef struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	Parameters  map[string]any `json:"parameters"` // JSON Schema
}

// ToolCall LLM 返回的工具调用请求。
type ToolCall struct {
	ID       string       `json:"id"`
	Type     string       `json:"type"` // "function"
	Function FunctionCall `json:"function"`
}

// FunctionCall 函数调用的名和参数。
type FunctionCall struct {
	Name      string `json:"name"`
	Arguments string `json:"arguments"` // JSON string
}

// ChatResponse 非流式对话响应。
type ChatResponse struct {
	ID      string   `json:"id"`
	Model   string   `json:"model"`
	Choices []Choice `json:"choices"`
	Usage   Usage    `json:"usage"`
}

// Choice 单个候选回复。
type Choice struct {
	Index        int     `json:"index"`
	Message      Message `json:"message"`
	FinishReason string  `json:"finish_reason"` // stop | tool_calls | length
}

// Usage token 使用量统计。
type Usage struct {
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
	TotalTokens      int `json:"total_tokens"`
}

// StreamEvent 流式事件。
type StreamEvent struct {
	Delta Message // 增量内容
	Done  bool    // 流是否结束
	Error error
}
