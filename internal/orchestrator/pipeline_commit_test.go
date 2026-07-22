package orchestrator

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"git.qingteng.cn/ms/ainspection/internal/adapter"
	"git.qingteng.cn/ms/ainspection/internal/config"
	"git.qingteng.cn/ms/ainspection/internal/llm"
	"git.qingteng.cn/ms/ainspection/internal/session"
	"git.qingteng.cn/ms/ainspection/internal/skill"
	"git.qingteng.cn/ms/ainspection/internal/tree"
)

// —————— mock 实现 ——————

// mockTreeMgr 实现 tree.Manager。
type mockTreeMgr struct {
	task    *tree.Task
	saveErr error
}

func (m *mockTreeMgr) NewTask(spec tree.TaskSpec) (*tree.Task, error) {
	if m.task != nil {
		return m.task, nil
	}
	return &tree.Task{
		TaskID: "test-task-1",
		Status: tree.StatusCommitting,
	}, nil
}

func (m *mockTreeMgr) LoadTask(taskID string) (*tree.Task, error) {
	if m.task != nil {
		return m.task, nil
	}
	return &tree.Task{TaskID: taskID, Status: tree.StatusCommitting}, nil
}

func (m *mockTreeMgr) Save(t *tree.Task) error { return m.saveErr }

// mockNodeOps 实现 tree.NodeOps。
type mockNodeOps struct {
	node *tree.Node
	err  error
}

func (m *mockNodeOps) Create(t *tree.Task, parentID string, in tree.NodeInput) (*tree.Node, error) {
	if m.err != nil {
		return nil, m.err
	}
	if m.node != nil {
		return m.node, nil
	}
	return &tree.Node{
		ID:    "test-node-" + in.Stage,
		Stage: in.Stage,
	}, nil
}

func (m *mockNodeOps) Complete(t *tree.Task, nodeID string) error { return nil }
func (m *mockNodeOps) Read(t *tree.Task, nodeID string) (*tree.Node, error) {
	return &tree.Node{ID: nodeID}, nil
}

// mockSkillLoader 实现 skill.Loader。
type mockSkillLoader struct {
	skills []*skill.Skill
	err    error
}

func (m *mockSkillLoader) Load(path string) (*skill.Skill, error) {
	if len(m.skills) > 0 {
		return m.skills[0], m.err
	}
	return nil, m.err
}

func (m *mockSkillLoader) LoadAll(dir string) ([]*skill.Skill, error) {
	return m.skills, m.err
}

// mockSkillInjector 实现 skill.Injector。
type mockSkillInjector struct {
	toolDefs  []skill.ToolDef
	skillBody string
	err       error
}

func (m *mockSkillInjector) Inject(agentType string, skills []*skill.Skill, nativeToolNames []string) ([]skill.ToolDef, string, error) {
	return m.toolDefs, m.skillBody, m.err
}

// mockRenderer 实现 prompt.Renderer。
type mockRenderer struct {
	output string
	err    error
}

func (m *mockRenderer) Render(name string, data any) (string, error)       { return m.output, m.err }
func (m *mockRenderer) RenderToBytes(name string, data any) ([]byte, error) { return []byte(m.output), m.err }

// mockDispatcher 实现 Dispatcher，记录调用。
type mockDispatcher struct {
	results      []skill.ToolResult
	err          error
	dispatchCalls [][]skill.ToolCall // 记录每次 DispatchBatch 的调用
}

func (m *mockDispatcher) Dispatch(ctx context.Context, sessionID string, call skill.ToolCall) (skill.ToolResult, error) {
	return skill.ToolResult{ToolCallID: call.ID, Content: "ok"}, m.err
}

func (m *mockDispatcher) DispatchBatch(ctx context.Context, sessionID string, calls []skill.ToolCall) ([]skill.ToolResult, error) {
	m.dispatchCalls = append(m.dispatchCalls, calls)
	if m.err != nil {
		return nil, m.err
	}
	if len(m.results) > 0 {
		return m.results, nil
	}
	results := make([]skill.ToolResult, len(calls))
	for i, c := range calls {
		results[i] = skill.ToolResult{ToolCallID: c.ID, Content: c.Name + " executed"}
	}
	return results, nil
}

func (m *mockDispatcher) ResolveLayer(call skill.ToolCall) skill.ToolLayer {
	return skill.LayerL1Skill
}

// mockSessionMgr 实现 session.Manager 的最小 mock。
type mockSessionMgr struct{}

func (m *mockSessionMgr) Start(spec session.SessionSpec) (*session.Session, error) {
	return &session.Session{ID: "sess-1"}, nil
}
func (m *mockSessionMgr) List(taskID string) ([]*session.Session, error) { return nil, nil }
func (m *mockSessionMgr) Resume(sessionID string) (*session.Session, error) {
	return &session.Session{ID: sessionID}, nil
}
func (m *mockSessionMgr) Save(s *session.Session) error                       { return nil }
func (m *mockSessionMgr) Fork(parent *session.Session, reason session.ForkReason) (*session.Session, error) {
	return &session.Session{ID: parent.ID + "-fork"}, nil
}
func (m *mockSessionMgr) Spawn(parent *session.Session, sub session.SubTaskInput) (*session.Session, error) {
	return &session.Session{ID: parent.ID + "-spawn"}, nil
}
func (m *mockSessionMgr) Kill(sessionID string) error { return nil }

// —————— 辅助函数 ——————

// newTestPipeline 创建用于测试的 pipelineImpl。
func newTestPipeline(cfg *config.Config, reg *adapter.Registry, disp *mockDispatcher,
	loader *mockSkillLoader, injector *mockSkillInjector, renderer *mockRenderer,
) *pipelineImpl {
	return &pipelineImpl{
		treeMgr:         &mockTreeMgr{},
		nodeOps:         &mockNodeOps{},
		rollback:        nil,
		sessionMgr:      &mockSessionMgr{},
		evaluator:       nil,
		mctsEngine:      nil,
		dispatcher:      disp,
		planner:         nil,
		pcfg:            cfg,
		promptRenderer:  renderer,
		skillLoader:     loader,
		skillInjector:   injector,
		adapterRegistry: reg,
		cmdExecutor:     nil,
	}
}

// makeLLMMockServer 创建返回指定响应的 HTTP test server，返回 server 和 LLMNativeAdapter。
func makeLLMMockServer(t *testing.T, resp llm.ChatResponse) (*httptest.Server, *adapter.LLMNativeAdapter) {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
	}))
	adp := adapter.NewLLMNativeAdapter(server.URL, "test-key", "test-model", nil)
	return server, adp
}

// —————— 测试用例 ——————

// TestPipelineCommit_LLMNativeWithToolCalls 测试 LLM Native 路径：LLM 返回 tool_calls → DispatchBatch 执行。
func TestPipelineCommit_LLMNativeWithToolCalls(t *testing.T) {
	// 构造 mock LLM 响应（含 tool_calls）
	mockResp := llm.ChatResponse{
		ID:    "resp-1",
		Model: "test-model",
		Choices: []llm.Choice{{
			Index: 0,
			Message: llm.Message{
				Role:    "assistant",
				Content: "我需要执行以下操作",
				ToolCalls: []llm.ToolCall{
					{
						ID:   "call-1",
						Type: "function",
						Function: llm.FunctionCall{
							Name:      "glab-mr",
							Arguments: `{"title":"fix: test issue","description":"MR description"}`,
						},
					},
					{
						ID:   "call-2",
						Type: "function",
						Function: llm.FunctionCall{
							Name:      "jira-update",
							Arguments: `{"issue":"TEST-123","status":"resolved"}`,
						},
					},
				},
			},
			FinishReason: "tool_calls",
		}},
	}

	server, llmAdapter := makeLLMMockServer(t, mockResp)
	defer server.Close()

	// 注册 adapter
	reg := adapter.NewRegistry()
	reg.Register(llmAdapter)

	// 构造依赖
	disp := &mockDispatcher{}
	loader := &mockSkillLoader{
		skills: []*skill.Skill{
			{Name: "glab-mr", ApprovalLevel: "L0"},
			{Name: "jira-update", ApprovalLevel: "L0"},
		},
	}
	injector := &mockSkillInjector{
		toolDefs: []skill.ToolDef{
			{Name: "glab-mr", Description: "Create MR"},
			{Name: "jira-update", Description: "Update JIRA"},
		},
		skillBody: "## Available Skills\n- glab-mr\n- jira-update",
	}
	renderer := &mockRenderer{output: "commit system prompt"}

	cfg := &config.Config{
		Skills: config.SkillsConfig{Path: "/mock/skills"},
		Agents: map[string]config.AgentConfig{
			"generator": {Type: "llm_native", Model: "test-model"},
		},
	}

	p := newTestPipeline(cfg, reg, disp, loader, injector, renderer)

	task := &tree.Task{
		TaskID:   "test-task-1",
		Status:   tree.StatusCommitting,
		Service:  "test-svc",
		IssueRef: "TEST-123",
	}
	sess := &session.Session{ID: "sess-1", WorkingDir: "/tmp/test"}

	result, err := p.executeCommit(context.Background(), task, sess)
	if err != nil {
		t.Fatalf("executeCommit: %v", err)
	}

	// 验证 task 状态为 Done
	if task.Status != tree.StatusDone {
		t.Errorf("expected task status Done, got %s", task.Status)
	}
	if result.Status != tree.StatusDone {
		t.Errorf("expected result status Done, got %s", result.Status)
	}

	// 验证 DispatchBatch 被调用
	if len(disp.dispatchCalls) != 1 {
		t.Fatalf("expected 1 DispatchBatch call, got %d", len(disp.dispatchCalls))
	}
	if len(disp.dispatchCalls[0]) != 2 {
		t.Errorf("expected 2 tool calls dispatched, got %d", len(disp.dispatchCalls[0]))
	}
}

// TestPipelineCommit_LLMNativeNoToolCalls 测试 LLM Native 路径：LLM 返回纯文本（无 tool_calls）。
func TestPipelineCommit_LLMNativeNoToolCalls(t *testing.T) {
	mockResp := llm.ChatResponse{
		ID:    "resp-1",
		Model: "test-model",
		Choices: []llm.Choice{{
			Index: 0,
			Message: llm.Message{
				Role:    "assistant",
				Content: "Commit summary: MR created successfully",
			},
			FinishReason: "stop",
		}},
	}

	server, llmAdapter := makeLLMMockServer(t, mockResp)
	defer server.Close()

	reg := adapter.NewRegistry()
	reg.Register(llmAdapter)

	disp := &mockDispatcher{}
	loader := &mockSkillLoader{
		skills: []*skill.Skill{
			{Name: "glab-mr", ApprovalLevel: "L0"},
			{Name: "jira-update", ApprovalLevel: "L0"},
		},
	}
	injector := &mockSkillInjector{
		toolDefs: []skill.ToolDef{
			{Name: "glab-mr", Description: "Create MR"},
			{Name: "jira-update", Description: "Update JIRA"},
		},
		skillBody: "## Available Skills",
	}
	renderer := &mockRenderer{output: "commit system prompt"}

	cfg := &config.Config{
		Skills: config.SkillsConfig{Path: "/mock/skills"},
		Agents: map[string]config.AgentConfig{
			"generator": {Type: "llm_native", Model: "test-model"},
		},
	}

	p := newTestPipeline(cfg, reg, disp, loader, injector, renderer)

	task := &tree.Task{
		TaskID:   "test-task-2",
		Status:   tree.StatusCommitting,
		Service:  "test-svc",
		IssueRef: "TEST-456",
	}
	sess := &session.Session{ID: "sess-1", WorkingDir: "/tmp/test"}

	result, err := p.executeCommit(context.Background(), task, sess)
	if err != nil {
		t.Fatalf("executeCommit: %v", err)
	}

	if task.Status != tree.StatusDone {
		t.Errorf("expected task status Done, got %s", task.Status)
	}
	if result.Status != tree.StatusDone {
		t.Errorf("expected result status Done, got %s", result.Status)
	}

	// 纯文本路径不应调用 DispatchBatch
	if len(disp.dispatchCalls) > 0 {
		t.Errorf("expected 0 DispatchBatch calls for pure text, got %d", len(disp.dispatchCalls))
	}
}

// mockCLIAdapter 实现 adapter.AgentAdapter，模拟 CLI agent 的 Run()。
type mockCLIAdapter struct {
	name       string
	agentType  adapter.AgentType
	runResult  *adapter.AgentResult
	runErr     error
}

func (m *mockCLIAdapter) Name() string                                      { return m.name }
func (m *mockCLIAdapter) Type() adapter.AgentType                           { return m.agentType }
func (m *mockCLIAdapter) Setup(ctx context.Context, sandboxPath string, cfg adapter.AgentSetupConfig) error {
	return nil
}
func (m *mockCLIAdapter) Run(ctx context.Context, sandboxPath string, input adapter.AgentInput) (*adapter.AgentResult, error) {
	return m.runResult, m.runErr
}
func (m *mockCLIAdapter) ParseOutput(raw []byte) (*adapter.AgentResult, error) {
	return &adapter.AgentResult{Text: string(raw)}, nil
}

// TestPipelineCommit_CLIAdapter 测试 CLI adapter 路径：Run() 返回 commit 摘要。
func TestPipelineCommit_CLIAdapter(t *testing.T) {
	cliAdapter := &mockCLIAdapter{
		name:      "claude_cli",
		agentType: adapter.AgentClaudeCLI,
		runResult: &adapter.AgentResult{
			Text: "MR #42 created: fix test issue\nJIRA TEST-123 updated",
		},
	}

	reg := adapter.NewRegistry()
	reg.Register(cliAdapter)

	disp := &mockDispatcher{}
	loader := &mockSkillLoader{
		skills: []*skill.Skill{
			{Name: "glab-mr", ApprovalLevel: "L0"},
			{Name: "jira-update", ApprovalLevel: "L0"},
		},
	}
	injector := &mockSkillInjector{
		toolDefs: []skill.ToolDef{
			{Name: "glab-mr", Description: "Create MR"},
		},
		skillBody: "## Skills",
	}
	renderer := &mockRenderer{output: "commit system prompt"}

	cfg := &config.Config{
		Skills: config.SkillsConfig{Path: "/mock/skills"},
		Agents: map[string]config.AgentConfig{
			"generator": {Type: "claude_cli", Model: "claude-sonnet-4-6"},
		},
	}

	p := newTestPipeline(cfg, reg, disp, loader, injector, renderer)

	task := &tree.Task{
		TaskID:   "test-task-3",
		Status:   tree.StatusCommitting,
		Service:  "test-svc",
		IssueRef: "TEST-789",
	}
	sess := &session.Session{ID: "sess-1", WorkingDir: "/tmp/test"}

	result, err := p.executeCommit(context.Background(), task, sess)
	if err != nil {
		t.Fatalf("executeCommit: %v", err)
	}

	if task.Status != tree.StatusDone {
		t.Errorf("expected task status Done, got %s", task.Status)
	}
	if result.Status != tree.StatusDone {
		t.Errorf("expected result status Done, got %s", result.Status)
	}

	// CLI 路径不应调用 DispatchBatch
	if len(disp.dispatchCalls) > 0 {
		t.Errorf("expected 0 DispatchBatch calls for CLI adapter, got %d", len(disp.dispatchCalls))
	}
}

// TestPipelineCommit_AdapterNotFound 测试 adapter 查找失败时优雅降级。
func TestPipelineCommit_AdapterNotFound(t *testing.T) {
	// 空 registry，无匹配 adapter
	reg := adapter.NewRegistry()

	disp := &mockDispatcher{}
	loader := &mockSkillLoader{
		skills: []*skill.Skill{
			{Name: "glab-mr", ApprovalLevel: "L0"},
		},
	}
	injector := &mockSkillInjector{
		toolDefs:  []skill.ToolDef{{Name: "glab-mr", Description: "Create MR"}},
		skillBody: "## Skills",
	}
	renderer := &mockRenderer{output: "commit system prompt"}

	cfg := &config.Config{
		Skills: config.SkillsConfig{Path: "/mock/skills"},
		Agents: map[string]config.AgentConfig{
			"generator": {Type: "llm_native", Model: "test-model"},
		},
	}

	p := newTestPipeline(cfg, reg, disp, loader, injector, renderer)

	task := &tree.Task{
		TaskID:   "test-task-4",
		Status:   tree.StatusCommitting,
		Service:  "test-svc",
		IssueRef: "TEST-000",
	}
	sess := &session.Session{ID: "sess-1", WorkingDir: "/tmp/test"}

	result, err := p.executeCommit(context.Background(), task, sess)
	if err != nil {
		t.Fatalf("executeCommit should not error on adapter lookup failure: %v", err)
	}

	// adapter 查找失败时 task 应仍标记为 Done（优雅降级）
	if task.Status != tree.StatusDone {
		t.Errorf("expected task status Done on adapter failure, got %s", task.Status)
	}
	if result.Status != tree.StatusDone {
		t.Errorf("expected result status Done, got %s", result.Status)
	}
}

// TestPipelineCommit_SkillLoadFailure 测试 skill 加载失败时优雅降级。
func TestPipelineCommit_SkillLoadFailure(t *testing.T) {
	mockResp := llm.ChatResponse{
		ID:    "resp-1",
		Model: "test-model",
		Choices: []llm.Choice{{
			Index: 0,
			Message: llm.Message{
				Role:    "assistant",
				Content: "Commit done without skills",
			},
			FinishReason: "stop",
		}},
	}

	server, llmAdapter := makeLLMMockServer(t, mockResp)
	defer server.Close()

	reg := adapter.NewRegistry()
	reg.Register(llmAdapter)

	disp := &mockDispatcher{}
	// skill loader 返回错误
	loader := &mockSkillLoader{
		err: fmt.Errorf("skills directory not found"),
	}
	injector := &mockSkillInjector{}
	renderer := &mockRenderer{output: "commit system prompt"}

	cfg := &config.Config{
		Skills: config.SkillsConfig{Path: "/mock/skills"},
		Agents: map[string]config.AgentConfig{
			"generator": {Type: "llm_native", Model: "test-model"},
		},
	}

	p := newTestPipeline(cfg, reg, disp, loader, injector, renderer)

	task := &tree.Task{
		TaskID:   "test-task-5",
		Status:   tree.StatusCommitting,
		Service:  "test-svc",
		IssueRef: "TEST-111",
	}
	sess := &session.Session{ID: "sess-1", WorkingDir: "/tmp/test"}

	result, err := p.executeCommit(context.Background(), task, sess)
	if err != nil {
		t.Fatalf("executeCommit should not error on skill load failure: %v", err)
	}

	// skill 加载失败应优雅降级，task 仍标记 Done
	if task.Status != tree.StatusDone {
		t.Errorf("expected task status Done on skill load failure, got %s", task.Status)
	}
	if result.Status != tree.StatusDone {
		t.Errorf("expected result status Done, got %s", result.Status)
	}
}
