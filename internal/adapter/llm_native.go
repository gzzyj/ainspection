package adapter

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"git.qingteng.cn/ms/ainspection/internal/llm"
)

// LLMNativeAdapter 封装 LLM HTTP 直连的 Setup/Run/ParseOutput。
//
// 通过 HTTP API (OpenAI 兼容) 直连 LLM，不依赖 CLI。
// 不实现 SkillInjector/HookInjector — skill/hook 通过 tools/messages 注入。
type LLMNativeAdapter struct {
	name         string
	endpoint     string
	apiKey       string
	model        string
	headers      map[string]string
	timeout      time.Duration
	maxTokens    int
	temperature  float64
	httpClient   *http.Client
	systemPrompt string
	skills       []SkillDef
}

// DefaultLLMNativeTimeout LLM Native 适配器默认超时。
const DefaultLLMNativeTimeout = 120 * time.Second

// NewLLMNativeAdapter 创建 LLM Native 适配器（使用默认 120s 超时）。
func NewLLMNativeAdapter(endpoint, apiKey, model string, headers map[string]string) *LLMNativeAdapter {
	return NewLLMNativeAdapterWithTimeout(endpoint, apiKey, model, headers, 0)
}

// NewLLMNativeAdapterWithTimeout 创建 LLM Native 适配器，支持自定义超时。
// timeout 为 0 时使用默认 120s。
func NewLLMNativeAdapterWithTimeout(endpoint, apiKey, model string, headers map[string]string, timeout time.Duration) *LLMNativeAdapter {
	if timeout <= 0 {
		timeout = DefaultLLMNativeTimeout
	}
	return &LLMNativeAdapter{
		name:        "llm_native",
		endpoint:    strings.TrimRight(endpoint, "/"),
		apiKey:      apiKey,
		model:       model,
		headers:     headers,
		timeout:     timeout,
		httpClient:  &http.Client{Timeout: timeout},
	}
}

func (a *LLMNativeAdapter) Name() string    { return a.name }
func (a *LLMNativeAdapter) Type() AgentType { return AgentLLMNative }

// Setup 保存端点/模型/APIKey + system prompt + skills（不写文件）。
func (a *LLMNativeAdapter) Setup(ctx context.Context, sandboxPath string, cfg AgentSetupConfig) error {
	a.endpoint = strings.TrimRight(cfg.Endpoint, "/")
	a.apiKey = cfg.APIKey
	a.model = cfg.Model
	if cfg.Headers != nil {
		a.headers = cfg.Headers
	}
	a.systemPrompt = cfg.SystemPrompt
	a.skills = cfg.Skills
	return nil
}

// Run 通过 HTTP POST 到 {endpoint}/v1/messages 发起 LLM 调用。
// 自动拼接 Setup 时保存的 system prompt + skill descriptions。
func (a *LLMNativeAdapter) Run(ctx context.Context, sandboxPath string, input AgentInput) (*AgentResult, error) {
	messages := a.buildMessages(input.Prompt)

	reqBody := map[string]any{
		"model":    a.model,
		"messages": messages,
		"stream":   false,
	}
	if input.MaxTokens > 0 {
		reqBody["max_tokens"] = input.MaxTokens
	}
	if input.Temperature > 0 {
		reqBody["temperature"] = input.Temperature
	}

	body, err := json.Marshal(reqBody)
	if err != nil {
		return nil, fmt.Errorf("marshal request: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost,
		a.endpoint+"/v1/messages", bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("x-api-key", a.apiKey)
	for k, v := range a.headers {
		httpReq.Header.Set(k, v)
	}

	resp, err := a.httpClient.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("http post: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		errBody, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return nil, fmt.Errorf("unexpected status %d: %s", resp.StatusCode, string(errBody))
	}

	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read response: %w", err)
	}

	return a.ParseOutput(raw)
}

// ParseOutput 解析 API 响应为 AgentResult。
func (a *LLMNativeAdapter) ParseOutput(raw []byte) (*AgentResult, error) {
	result := &AgentResult{RawOutput: raw}

	var parsed struct {
		ID      string `json:"id"`
		Model   string `json:"model"`
		Choices []struct {
			Index        int    `json:"index"`
			FinishReason string `json:"finish_reason"`
			Message      struct {
				Role    string `json:"role"`
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
		Usage struct {
			PromptTokens     int `json:"prompt_tokens"`
			CompletionTokens int `json:"completion_tokens"`
			TotalTokens      int `json:"total_tokens"`
		} `json:"usage"`
	}

	if err := json.Unmarshal(raw, &parsed); err != nil {
		result.Text = string(raw)
		return result, nil
	}

	if len(parsed.Choices) > 0 {
		result.Text = parsed.Choices[0].Message.Content
	}
	result.TokenUsage = TokenUsage{
		Input:  int64(parsed.Usage.PromptTokens),
		Output: int64(parsed.Usage.CompletionTokens),
	}

	return result, nil
}

// buildMessages 构建消息列表：system prompt + skill descriptions + user prompt。
func (a *LLMNativeAdapter) buildMessages(userPrompt string) []map[string]string {
	var msgs []map[string]string

	systemContent := a.systemPrompt
	if len(a.skills) > 0 {
		if systemContent != "" {
			systemContent += "\n\n"
		}
		systemContent += "## Available Skills\n\n"
		for _, s := range a.skills {
			systemContent += fmt.Sprintf("### %s\n%s\n\n", s.Name, s.Description)
			if s.Body != "" {
				systemContent += s.Body + "\n\n"
			}
		}
	}

	if systemContent != "" {
		msgs = append(msgs, map[string]string{"role": "system", "content": systemContent})
	}
	msgs = append(msgs, map[string]string{"role": "user", "content": userPrompt})
	return msgs
}

// —————— 流式支持（从 internal/llm/client.go 迁移） ——————

// StreamEvent 流式事件。
type StreamEvent struct {
	Delta string
	Done  bool
	Error error
}

// RunStream 发送流式对话请求，通过 channel 返回增量消息。
func (a *LLMNativeAdapter) RunStream(ctx context.Context, sandboxPath string, input AgentInput) (<-chan StreamEvent, error) {
	messages := a.buildMessages(input.Prompt)

	reqBody := map[string]any{
		"model":    a.model,
		"messages": messages,
		"stream":   true,
	}
	if input.MaxTokens > 0 {
		reqBody["max_tokens"] = input.MaxTokens
	}
	if input.Temperature > 0 {
		reqBody["temperature"] = input.Temperature
	}

	body, err := json.Marshal(reqBody)
	if err != nil {
		return nil, fmt.Errorf("marshal request: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost,
		a.endpoint+"/v1/messages", bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("x-api-key", a.apiKey)
	httpReq.Header.Set("Accept", "text/event-stream")
	for k, v := range a.headers {
		httpReq.Header.Set(k, v)
	}

	resp, err := a.httpClient.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("http post: %w", err)
	}

	ch := make(chan StreamEvent, 32)
	go func() {
		defer resp.Body.Close()
		defer close(ch)
		a.readSSE(ctx, resp.Body, ch)
	}()

	return ch, nil
}

// readSSE 解析 SSE 流式响应（从 llm/client.go 迁移）。
func (a *LLMNativeAdapter) readSSE(ctx context.Context, r io.Reader, ch chan<- StreamEvent) {
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 64*1024), 1024*1024)

	for scanner.Scan() {
		select {
		case <-ctx.Done():
			ch <- StreamEvent{Error: ctx.Err(), Done: true}
			return
		default:
		}

		line := scanner.Text()
		if line == "" || strings.HasPrefix(line, ":") {
			continue
		}

		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		data := strings.TrimPrefix(line, "data: ")

		if data == "[DONE]" {
			ch <- StreamEvent{Done: true}
			return
		}

		var event struct {
			Type  string `json:"type"`
			Delta struct {
				Content string `json:"content"`
			} `json:"delta"`
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		}
		if err := json.Unmarshal([]byte(data), &event); err != nil {
			ch <- StreamEvent{Error: fmt.Errorf("parse sse data: %w", err)}
			continue
		}

		switch event.Type {
		case "message_stop":
			ch <- StreamEvent{Done: true}
			return
		case "content_block_delta":
			ch <- StreamEvent{Delta: event.Delta.Content}
		}
	}

	if err := scanner.Err(); err != nil {
		ch <- StreamEvent{Error: err, Done: true}
	}
}

// —————— 结构化 Chat API（供 orchestrator 多轮 tool call 使用） ——————

// Chat 发送结构化对话请求（OpenAI 兼容 API），支持 system prompt、多轮消息、tools。
// 用于 orchestrator 中需要精细控制多轮 tool call 的场景。
func (a *LLMNativeAdapter) Chat(ctx context.Context, req llm.ChatRequest) (*llm.ChatResponse, error) {
	req.Stream = false
	body, err := json.Marshal(req)
	if err != nil {
		return nil, fmt.Errorf("marshal request: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost,
		a.endpoint+"/v1/messages", bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("x-api-key", a.apiKey)
	for k, v := range a.headers {
		httpReq.Header.Set(k, v)
	}

	resp, err := a.httpClient.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("http post: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		errBody, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return nil, fmt.Errorf("unexpected status %d: %s", resp.StatusCode, string(errBody))
	}

	var chatResp llm.ChatResponse
	if err := json.NewDecoder(resp.Body).Decode(&chatResp); err != nil {
		return nil, fmt.Errorf("decode response: %w", err)
	}

	return &chatResp, nil
}

// ChatStream 发送流式对话请求。
func (a *LLMNativeAdapter) ChatStream(ctx context.Context, req llm.ChatRequest) (<-chan llm.StreamEvent, error) {
	req.Stream = true
	body, err := json.Marshal(req)
	if err != nil {
		return nil, fmt.Errorf("marshal request: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost,
		a.endpoint+"/v1/messages", bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("x-api-key", a.apiKey)
	httpReq.Header.Set("Accept", "text/event-stream")
	for k, v := range a.headers {
		httpReq.Header.Set(k, v)
	}

	resp, err := a.httpClient.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("http post: %w", err)
	}

	ch := make(chan llm.StreamEvent, 32)
	go func() {
		defer resp.Body.Close()
		defer close(ch)
		a.readSSEChat(ctx, resp.Body, ch)
	}()

	return ch, nil
}

// readSSEChat 解析 SSE 流式响应为 llm.StreamEvent。
func (a *LLMNativeAdapter) readSSEChat(ctx context.Context, r io.Reader, ch chan<- llm.StreamEvent) {
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 64*1024), 1024*1024)

	for scanner.Scan() {
		select {
		case <-ctx.Done():
			ch <- llm.StreamEvent{Error: ctx.Err(), Done: true}
			return
		default:
		}

		line := scanner.Text()
		if line == "" || strings.HasPrefix(line, ":") {
			continue
		}

		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		data := strings.TrimPrefix(line, "data: ")

		if data == "[DONE]" {
			ch <- llm.StreamEvent{Done: true}
			return
		}

		var event struct {
			Type    string     `json:"type"`
			Delta   llm.Message `json:"delta"`
			Message llm.Message `json:"message"`
		}
		if err := json.Unmarshal([]byte(data), &event); err != nil {
			ch <- llm.StreamEvent{Error: fmt.Errorf("parse sse data: %w", err)}
			continue
		}

		switch event.Type {
		case "message_stop":
			ch <- llm.StreamEvent{Done: true}
			return
		case "content_block_delta":
			ch <- llm.StreamEvent{Delta: event.Delta}
		}
	}

	if err := scanner.Err(); err != nil {
		ch <- llm.StreamEvent{Error: err, Done: true}
	}
}
