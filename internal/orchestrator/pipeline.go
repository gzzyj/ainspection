package orchestrator

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	"git.qingteng.cn/ms/ainspection/internal/adapter"
	"git.qingteng.cn/ms/ainspection/internal/config"
	"git.qingteng.cn/ms/ainspection/internal/llm"
	"git.qingteng.cn/ms/ainspection/internal/planner"
	"git.qingteng.cn/ms/ainspection/internal/prompt"
	"git.qingteng.cn/ms/ainspection/internal/security"
	"git.qingteng.cn/ms/ainspection/internal/session"
	"git.qingteng.cn/ms/ainspection/internal/skill"
	"git.qingteng.cn/ms/ainspection/internal/tree"

	"gopkg.in/yaml.v3"
)

// NewPipeline 创建流水线实例。
// cmdExec 可选，为 nil 时 verify/commit 阶段跳过确定性命令执行。
func NewPipeline(
	tm tree.Manager,
	no tree.NodeOps,
	rb tree.Rollback,
	sm session.Manager,
	eval Evaluator,
	mcts MCTSRunner,
	disp Dispatcher,
	pl planner.Planner,
	cfg *config.Config,
	pr prompt.Renderer,
	sl skill.Loader,
	si skill.Injector,
	reg *adapter.Registry,
	cmdExec security.CommandExecutor,
) Pipeline {
	return &pipelineImpl{
		treeMgr:    tm,
		nodeOps:    no,
		rollback:   rb,
		sessionMgr: sm,
		evaluator:  eval,
		mctsEngine: mcts,
		dispatcher: disp,
		planner:    pl,
		pcfg:       cfg,
		promptRenderer:  pr,
		skillLoader:     sl,
		skillInjector:   si,
		adapterRegistry: reg,
		cmdExecutor:     cmdExec,
	}
}

// Run 从 RunSpec 启动全新流水线。
func (p *pipelineImpl) Run(ctx context.Context, spec RunSpec) (*PipelineStatus, error) {
	status := &PipelineStatus{
		StartedAt: time.Now(),
	}

	// 1. 创建 task（树根节点）
	task, err := p.treeMgr.NewTask(tree.TaskSpec{
		IssueURL: spec.IssueURL,
		Desc:     spec.Desc,
		Service:  spec.Service,
		TraceID:  spec.TraceID,
	})
	if err != nil {
		return nil, fmt.Errorf("create task: %w", err)
	}
	status.TaskID = task.TaskID

	// 2. 创建 root 节点
	rootNode, err := p.nodeOps.Create(task, "", tree.NodeInput{
		Stage: "init",
	})
	if err != nil {
		return nil, fmt.Errorf("create root node: %w", err)
	}

	// 3. 启动首个 session
	sess, err := p.sessionMgr.Start(session.SessionSpec{
		TaskID:    task.TaskID,
		NodeID:    rootNode.ID,
		AgentRole: "generator",
		AgentName: spec.Service, // 从 service 配置推导 agent
	})
	if err != nil {
		return nil, fmt.Errorf("start session: %w", err)
	}

	// 4. 按流水线阶段执行
	ctxReset := NewContextResetManager(p.sessionMgr, p.nodeOps, p.treeMgr)

	for _, stage := range pipelineOrder {
		status.CurrentStage = stage

		result, err := p.executeStage(ctx, stage, task, sess, ctxReset)
		if err != nil {
			status.StageResults = append(status.StageResults, StageResult{
				Stage:  stage,
				Status: tree.StatusBlocked,
				Error:  err,
			})
			status.UpdatedAt = time.Now()
			return status, fmt.Errorf("stage %s: %w", stage, err)
		}

		status.StageResults = append(status.StageResults, *result)
		status.UpdatedAt = time.Now()

		// 阶段完成后主动 Context Reset
		if result.NodeID != "" {
			newSession, resetErr := ctxReset.HandleNodeComplete(ctx, task, result.NodeID)
			if resetErr != nil {
				log.Printf("[orchestrator] context reset after stage %s: %v", stage, resetErr)
			} else if newSession != nil {
				sess = newSession
			}
		}

		// 如果进入 blocked 状态，停止流水线
		if result.Status == tree.StatusBlocked {
			break
		}

		// 检查是否是最后一个阶段
		if nextStage(stage) == "" {
			break
		}
	}

	return status, nil
}

// Resume 从已有 task 恢复流水线执行。
func (p *pipelineImpl) Resume(ctx context.Context, taskID string) (*PipelineStatus, error) {
	task, err := p.treeMgr.LoadTask(taskID)
	if err != nil {
		return nil, fmt.Errorf("load task %s: %w", taskID, err)
	}

	status := &PipelineStatus{
		TaskID:    taskID,
		StartedAt: task.CreatedAt,
	}

	// 从 current_node 对应的阶段继续
	currentStage := statusFromTaskStatus(task.Status)
	if currentStage == "" {
		return nil, fmt.Errorf("task %s status %s has no corresponding pipeline stage", taskID, task.Status)
	}
	status.CurrentStage = currentStage

	// 恢复 session
	sessions, err := p.sessionMgr.List(taskID)
	if err != nil {
		return nil, fmt.Errorf("list sessions: %w", err)
	}

	var currentSession *session.Session
	for _, s := range sessions {
		if s.Status == session.StatusRunning || s.Status == session.StatusPending {
			currentSession = s
			break
		}
	}

	if currentSession == nil {
		// 没有运行中的 session，尝试 resume 最近一个
		if len(sessions) > 0 {
			currentSession, err = p.sessionMgr.Resume(sessions[len(sessions)-1].ID)
			if err != nil {
				return nil, fmt.Errorf("resume session: %w", err)
			}
		} else {
			return nil, fmt.Errorf("no sessions found for task %s", taskID)
		}
	}

	// 从当前阶段开始继续执行
	ctxReset := NewContextResetManager(p.sessionMgr, p.nodeOps, p.treeMgr)
	startIdx := stageIndex(currentStage)
	if startIdx < 0 {
		return nil, fmt.Errorf("unknown stage: %s", currentStage)
	}

	for i := startIdx; i < len(pipelineOrder); i++ {
		stage := pipelineOrder[i]
		status.CurrentStage = stage

		result, err := p.executeStage(ctx, stage, task, currentSession, ctxReset)
		if err != nil {
			status.StageResults = append(status.StageResults, StageResult{
				Stage:  stage,
				Status: tree.StatusBlocked,
				Error:  err,
			})
			status.UpdatedAt = time.Now()
			return status, fmt.Errorf("stage %s: %w", stage, err)
		}

		status.StageResults = append(status.StageResults, *result)
		status.UpdatedAt = time.Now()

		if result.NodeID != "" {
			newSession, resetErr := ctxReset.HandleNodeComplete(ctx, task, result.NodeID)
			if resetErr != nil {
				log.Printf("[orchestrator] context reset after stage %s: %v", stage, resetErr)
			} else if newSession != nil {
				currentSession = newSession
			}
		}

		if result.Status == tree.StatusBlocked {
			break
		}
	}

	return status, nil
}

// INSTRUMENT: orchestrator-executeStage — 流水线中枢，阶段路由与执行
// LAYER: L1
// STATUS: implemented
// executeStage 执行单个流水线阶段。
// 这是 orchestrator 的核心：决定每个阶段调什么、怎么调。
func (p *pipelineImpl) executeStage(
	ctx context.Context,
	stage Stage,
	task *tree.Task,
	sess *session.Session,
	ctxReset *ContextResetManager,
) (*StageResult, error) {
	switch stage {
	case StageGet:
		return p.executeGet(ctx, task, sess)
	case StageLocate:
		return p.executeLocate(ctx, task, sess)
	case StageReview1:
		return p.executeReview1(ctx, task, sess)
	case StagePlan:
		return p.executePlan(ctx, task, sess)
	case StageFix:
		return p.executeFix(ctx, task, sess)
	case StageReview2:
		return p.executeReview2(ctx, task, sess)
	case StageVerify:
		return p.executeVerify(ctx, task, sess)
	case StageReview3:
		return p.executeReview3(ctx, task, sess)
	case StageCommit:
		return p.executeCommit(ctx, task, sess)
	default:
		return nil, fmt.Errorf("unknown stage: %s", stage)
	}
}

// executeGet get 阶段：加载 system prompt → LLM 调 jira-query skill → 解析 YAML 输出 → 写入 output.yaml。
func (p *pipelineImpl) executeGet(ctx context.Context, task *tree.Task, sess *session.Session) (*StageResult, error) {
	result := &StageResult{
		Stage: StageGet,
	}
	start := time.Now()

	// 1. 确定 agent + 获取 adapter（get 阶段使用 Generator agent）
	if p.pcfg == nil {
		log.Printf("[orchestrator] config is nil, falling back to placeholder")
		return p.executeGetPlaceholder(ctx, task)
	}
	agentName := p.pcfg.GetDefaultAgent()
	if _, ok := p.pcfg.Agents["generator"]; ok {
		agentName = "generator"
	}
	agentCfg, ok := p.pcfg.Agents[agentName]
	if !ok {
		return nil, fmt.Errorf("agent %q not found in config", agentName)
	}

	// 从 adapter registry 获取通信适配器
	agentType := adapter.ResolveAgentType(agentCfg.ResolveType())
	agentAdapter, err := p.getAgentAdapter(agentType)
	if err != nil {
		return nil, fmt.Errorf("get agent adapter %q: %w", agentType, err)
	}

	// 2. 渲染 system prompt（从当前节点的 input.yaml 读取 desc + trace_id）
	desc := readDescFromTask(task)
	traceID := readTraceIDFromTask(task)
	sysPrompt, err := p.promptRenderer.Render("get-system", prompt.GetInput{
		IssueURL: task.IssueRef,
		Desc:     desc,
		Service:  task.Service,
		TraceID:  traceID,
	})
	if err != nil {
		// prompt 未加载时降级为简单占位输出
		log.Printf("[orchestrator] render get-system: %v, falling back to placeholder", err)
		return p.executeGetPlaceholder(ctx, task)
	}

	// 3. 加载 jira-query + feishu-search skills 并注入为工具
	var toolDefs []skill.ToolDef
	var skillBody string
	if p.pcfg != nil && p.pcfg.Skills.Path != "" && p.skillLoader != nil {
		skills, loadErr := p.skillLoader.LoadAll(p.pcfg.Skills.Path)
		if loadErr != nil {
			log.Printf("[orchestrator] load skills: %v", loadErr)
		} else {
			// 按 config pipeline.stages.get.skills 过滤
			allowedSkills := p.getStageSkillNames(StageGet)
			var getSkills []*skill.Skill
			for _, s := range skills {
				if slices.Contains(allowedSkills, s.Name) {
					getSkills = append(getSkills, s)
				}
			}
			if len(getSkills) > 0 {
				var injectErr error
				toolDefs, skillBody, injectErr = p.skillInjector.Inject(agentCfg.ResolveType(), getSkills, agentCfg.NativeTools)
				if injectErr != nil {
					log.Printf("[orchestrator] inject get skills: %v", injectErr)
					toolDefs = nil
					skillBody = ""
				} else {
					sysPrompt += "\n\n" + skillBody
				}
			}
		}
	}

	// 4. 按 adapter 类型分支：LLM Native → Chat() 多轮，CLI → Run() 单次
	var finalText string
	switch agentAdapter.Type() {
	case adapter.AgentLLMNative:
		finalText, err = p.executeGetWithLLM(ctx, sess, agentAdapter.(*adapter.LLMNativeAdapter),
			agentCfg, sysPrompt, toolDefs)
		if err != nil {
			log.Printf("[orchestrator] get llm native: %v, falling back to placeholder", err)
			return p.executeGetPlaceholder(ctx, task)
		}
	default:
		// CLI adapter: 单次 Run() 调用
		userPrompt := sysPrompt + "\n\n开始诊断"
		agentResult, runErr := agentAdapter.Run(ctx, sess.WorkingDir, adapter.AgentInput{
			Prompt:    userPrompt,
			SessionID: sess.ID,
		})
		if runErr != nil {
			log.Printf("[orchestrator] get cli run: %v, falling back to placeholder", runErr)
			return p.executeGetPlaceholder(ctx, task)
		}
		if agentResult != nil {
			finalText = agentResult.Text
		}
	}

	// 5. 从 finalText 解析 YAML
	if finalText == "" {
		log.Printf("[orchestrator] get: empty finalText, falling back to placeholder")
		return p.executeGetPlaceholder(ctx, task)
	}
	parsed := parseGetOutput(finalText)

	// 6. 创建节点 + 写入 output.yaml
	node, err := p.nodeOps.Create(task, task.CurrentNodeID, tree.NodeInput{
		Stage: string(StageGet),
	})
	if err != nil {
		return nil, fmt.Errorf("create get node: %w", err)
	}
	result.NodeID = node.ID

	// 组装 evidence 列表
	evidence := []string{"issue_ref: " + parsed.IssueRef}
	for _, s := range parsed.Symptoms {
		evidence = append(evidence, "symptoms: "+s)
	}
	for _, ep := range parsed.RelevantEndpoints {
		evidence = append(evidence, "endpoints: "+ep)
	}
	if parsed.TimeWindow != "" {
		evidence = append(evidence, "time_window: "+parsed.TimeWindow)
	}

	if err := tree.WriteOutput(task.TaskID, node.ID, &tree.NodeOutput{
		NodeID:    node.ID,
		SessionID: sess.ID,
		Findings: []tree.Finding{{
			Hypothesis:      parsed.ProblemDomain,
			Evidence:        evidence,
			ConfidenceSelf:  1.0,
			ConfidenceFinal: 1.0,
			Status:          "confirmed",
		}},
	}); err != nil {
		log.Printf("[orchestrator] write get output: %v", err)
	}

	// 将解析结果写入 input.yaml 供 locate 阶段读取
	if err := writeGetInput(task.TaskID, node.ID, parsed); err != nil {
		log.Printf("[orchestrator] write get input: %v", err)
	}

	// 8. 更新 task 状态
	if parsed.IssueRef != "" {
		task.IssueRef = parsed.IssueRef
	}
	task.Status = tree.StatusScopeDefined
	task.CurrentNodeID = node.ID
	if err := p.treeMgr.Save(task); err != nil {
		return nil, fmt.Errorf("save task after get: %w", err)
	}

	result.Status = tree.StatusScopeDefined
	result.Duration = time.Since(start)
	return result, nil
}

// executeGetPlaceholder LLM 不可用时的降级占位输出。
func (p *pipelineImpl) executeGetPlaceholder(ctx context.Context, task *tree.Task) (*StageResult, error) {
	result := &StageResult{
		Stage:  StageGet,
		Status: tree.StatusScopeDefined,
	}
	start := time.Now()

	node, err := p.nodeOps.Create(task, task.CurrentNodeID, tree.NodeInput{
		Stage: string(StageGet),
	})
	if err != nil {
		return nil, fmt.Errorf("create get node: %w", err)
	}
	result.NodeID = node.ID
	result.Duration = time.Since(start)

	task.Status = tree.StatusScopeDefined
	task.CurrentNodeID = node.ID
	if err := p.treeMgr.Save(task); err != nil {
		return nil, fmt.Errorf("save task after get: %w", err)
	}

	return result, nil
}

// ---- 类型转换 helpers ----

// skillToolToLLMTools 将 skill.ToolDef 切片转为 llm.ToolDef 切片。
func skillToolToLLMTools(st []skill.ToolDef) []llm.ToolDef {
	result := make([]llm.ToolDef, len(st))
	for i, t := range st {
		result[i] = llm.ToolDef{
			Type: "function",
			Function: llm.FunctionDef{
				Name:        t.Name,
				Description: t.Description,
				Parameters:  t.Schema,
			},
		}
	}
	return result
}

// llmToolCallsToSkill 将 llm.ToolCall 切片转为 skill.ToolCall 切片。
func llmToolCallsToSkill(tc []llm.ToolCall) []skill.ToolCall {
	result := make([]skill.ToolCall, len(tc))
	for i, c := range tc {
		var args map[string]any
		if c.Function.Arguments != "" {
			if err := json.Unmarshal([]byte(c.Function.Arguments), &args); err != nil {
				log.Printf("[orchestrator] parse tool call args %q: %v", c.Function.Name, err)
				args = map[string]any{}
			}
		}
		result[i] = skill.ToolCall{
			ID:        c.ID,
			Name:      c.Function.Name,
			Arguments: args,
		}
	}
	return result
}

// skillResultsToMessages 将 skill.ToolResult 切片转为 llm.Message 切片（tool 角色）。
func skillResultsToMessages(results []skill.ToolResult) []llm.Message {
	msgs := make([]llm.Message, len(results))
	for i, r := range results {
		msgs[i] = llm.Message{
			Role:       "tool",
			Content:    r.Content,
			ToolCallID: r.ToolCallID,
		}
	}
	return msgs
}

// toolResultsToText 将 tool 执行结果序列化为文本（用于 JiraContent prompt 注入）。
func toolResultsToText(results []skill.ToolResult) string {
	if len(results) == 0 {
		return ""
	}
	var parts []string
	for _, r := range results {
		parts = append(parts, r.Content)
	}
	return strings.Join(parts, "\n---\n")
}

// writeGetInput 将 get 阶段解析结果合并写入节点的 input.yaml。
func writeGetInput(taskID, nodeID string, parsed getOutputData) error {
	// 读取现有的 input.yaml
	existing, err := tree.ReadInput(taskID, nodeID)
	if err != nil {
		if os.IsNotExist(err) {
			existing = make(map[string]any)
		} else {
			return fmt.Errorf("read input.yaml: %w", err)
		}
	}

	// 合并解析字段
	if parsed.ProblemDomain != "" {
		existing["problem_domain"] = parsed.ProblemDomain
	}
	if parsed.Service != "" {
		existing["service"] = parsed.Service
	}
	if parsed.IssueRef != "" {
		existing["issue_ref"] = parsed.IssueRef
	}
	if len(parsed.Symptoms) > 0 {
		existing["symptoms"] = parsed.Symptoms
	}
	if parsed.TimeWindow != "" {
		existing["time_window"] = parsed.TimeWindow
	}
	if len(parsed.RelevantEndpoints) > 0 {
		existing["relevant_endpoints"] = parsed.RelevantEndpoints
	}
	if parsed.TraceID != "" {
		existing["trace_id"] = parsed.TraceID
	}

	// 写回 input.yaml
	path := filepath.Join(tree.TaskRoot(), taskID, "nodes", nodeID, "input.yaml")
	data, err := yaml.Marshal(existing)
	if err != nil {
		return fmt.Errorf("marshal input.yaml: %w", err)
	}
	return os.WriteFile(path, data, 0o644)
}

// ---- get 阶段 YAML 解析 ----

// getOutputData LLM 输出的 get 阶段 YAML 结构。
type getOutputData struct {
	Service           string   `yaml:"service"`
	IssueRef          string   `yaml:"issue_ref"`
	ProblemDomain     string   `yaml:"problem_domain"`
	Symptoms          []string `yaml:"symptoms"`
	TimeWindow        string   `yaml:"time_window"`
	RelevantEndpoints []string `yaml:"relevant_endpoints"`
	TraceID           string   `yaml:"trace_id"` // LLM 输出的 trace ID，可选
}

// parseGetOutput 从 LLM 文本输出中提取 YAML 块并解析为 getOutputData。
func parseGetOutput(text string) getOutputData {
	var data getOutputData

	// 尝试提取 ```yaml 代码块
	yamlBlock := extractYAMLBlock(text)
	if yamlBlock == "" {
		return data
	}

	if err := yaml.Unmarshal([]byte(yamlBlock), &data); err != nil {
		log.Printf("[orchestrator] parse get output yaml: %v", err)
	}
	return data
}

// extractYAMLBlock 从文本中提取第一个 ```yaml ... ``` 代码块。
func extractYAMLBlock(text string) string {
	start := strings.Index(text, "```yaml")
	if start < 0 {
		start = strings.Index(text, "```yml")
	}
	if start < 0 {
		// 没有 markdown 代码块标记，尝试直接作为 YAML 解析
		return text
	}

	// 跳过 ```yaml 行
	lineEnd := strings.Index(text[start:], "\n")
	if lineEnd < 0 {
		return ""
	}
	contentStart := start + lineEnd + 1

	end := strings.Index(text[contentStart:], "```")
	if end < 0 {
		return text[contentStart:]
	}

	return text[contentStart : contentStart+end]
}

// readTraceIDFromTask 从 task 的 root/current node input.yaml 读取 trace_id 字段。
func readTraceIDFromTask(task *tree.Task) string {
	input, err := tree.ReadInput(task.TaskID, "root")
	if err == nil {
		if tid, ok := input["trace_id"].(string); ok && tid != "" {
			return tid
		}
	}
	if task.CurrentNodeID != "" && task.CurrentNodeID != "root" {
		input, err := tree.ReadInput(task.TaskID, task.CurrentNodeID)
		if err == nil {
			if tid, ok := input["trace_id"].(string); ok {
				return tid
			}
		}
	}
	return ""
}

// readDescFromTask 从 task 的 root 节点 input.yaml 读取 desc 字段。
func readDescFromTask(task *tree.Task) string {
	// 优先从 root 节点读取
	input, err := tree.ReadInput(task.TaskID, "root")
	if err == nil {
		if desc, ok := input["desc"].(string); ok {
			return desc
		}
	}
	// fallback: 从当前节点读取
	if task.CurrentNodeID != "" && task.CurrentNodeID != "root" {
		input, err := tree.ReadInput(task.TaskID, task.CurrentNodeID)
		if err == nil {
			if desc, ok := input["desc"].(string); ok {
				return desc
			}
		}
	}
	return ""
}

// executeLocate locate 阶段：LLM 交互式调查 → MCTS 假设搜索 → 合并输出。
func (p *pipelineImpl) executeLocate(ctx context.Context, task *tree.Task, sess *session.Session) (*StageResult, error) {
	result := &StageResult{
		Stage: StageLocate,
	}
	start := time.Now()

	// 1. 重试检查
	if retryExceeded(task, "locate", p.maxRetryPerStage()) {
		task.Status = tree.StatusBlocked
		p.treeMgr.Save(task)
		result.Status = tree.StatusBlocked
		result.Error = fmt.Errorf("locate retry exceeded")
		return result, result.Error
	}

	// 2. 创建 locate 节点（父节点 = get 节点）
	node, err := p.nodeOps.Create(task, task.CurrentNodeID, tree.NodeInput{
		Stage: string(StageLocate),
	})
	if err != nil {
		return nil, fmt.Errorf("create locate node: %w", err)
	}
	result.NodeID = node.ID

	// 3. 从 get 节点（父节点）读取 output.yaml 构建调查输入
	locatePromptInput := p.readLocateInput(task, node.Parent)

	// 4. 多轮 LLM 交互式调查（最多 3 轮），失败时回退到纯 MCTS
	var allFindings []tree.Finding
	var allDiscarded []tree.DiscardedHypothesis

	surveyFindings, surveyDiscarded := p.runLocateSurvey(ctx, sess, locatePromptInput)
	allFindings = append(allFindings, surveyFindings...)
	allDiscarded = append(allDiscarded, surveyDiscarded...)

	// 5. 将调查 findings 传入 MCTS 搜索
	if p.mctsEngine != nil {
		locateInput := p.buildLocateInput(task, allFindings)
		locateOutputs, mctsErr := p.mctsEngine.RunLocate(ctx, locateInput)
		if mctsErr != nil {
			task.RetryCount["locate"]++
			result.RetryCount = task.RetryCount["locate"]
			log.Printf("[orchestrator] MCTS RunLocate failed: %v", mctsErr)
			if retryExceeded(task, "locate", p.maxRetryPerStage()) {
				task.Status = tree.StatusBlocked
				p.treeMgr.Save(task)
				result.Status = tree.StatusBlocked
				result.Error = fmt.Errorf("locate MCTS failed after retries: %w", mctsErr)
				return result, result.Error
			}
		}

		// 合并 MCTS 结果到 findings
		allFindings = mergeFindings(allFindings, locateOutputs)
	}

	// 6. 写入 output.yaml（findings + discarded_hypotheses）
	if err := tree.WriteOutput(task.TaskID, node.ID, &tree.NodeOutput{
		NodeID:              node.ID,
		SessionID:           sess.ID,
		Findings:            allFindings,
		DiscardedHypotheses: allDiscarded,
	}); err != nil {
		log.Printf("[orchestrator] write locate output: %v", err)
	}

	// 7. task.Status → StatusExpectationLocked
	task.Status = tree.StatusExpectationLocked
	task.CurrentNodeID = node.ID
	result.Status = tree.StatusExpectationLocked
	result.Duration = time.Since(start)

	if err := p.treeMgr.Save(task); err != nil {
		return nil, fmt.Errorf("save task after locate: %w", err)
	}

	return result, nil
}

// runLocateSurvey 运行 LLM 驱动的多轮交互式调查，返回 findings 和 discarded_hypotheses。
// 失败时返回空列表，调用方回退到纯 MCTS 模式。
func (p *pipelineImpl) runLocateSurvey(
	ctx context.Context,
	sess *session.Session,
	input prompt.LocateInput,
) ([]tree.Finding, []tree.DiscardedHypothesis) {
	// 确定 agent + 获取 adapter（locate 调查使用 Generator agent）
	if p.pcfg == nil {
		log.Printf("[orchestrator] locate: config is nil, falling back to MCTS-only")
		return nil, nil
	}
	agentName := p.pcfg.GetDefaultAgent()
	if _, ok := p.pcfg.Agents["generator"]; ok {
		agentName = "generator"
	}
	agentCfg, ok := p.pcfg.Agents[agentName]
	if !ok {
		log.Printf("[orchestrator] locate: agent %q not found, falling back to MCTS-only", agentName)
		return nil, nil
	}

	// 从 adapter registry 获取 LLM 通信适配器
	agentType := adapter.ResolveAgentType(agentCfg.ResolveType())
	llmAdapter, err := p.getLLMAdapter(agentType)
	if err != nil {
		log.Printf("[orchestrator] locate: get llm adapter %q: %v, falling back to MCTS-only", agentType, err)
		return nil, nil
	}

	// 渲染 locate-system.tmpl
	sysPrompt, err := p.promptRenderer.Render("locate-system", input)
	if err != nil {
		log.Printf("[orchestrator] render locate-system: %v, falling back to MCTS-only", err)
		return nil, nil
	}

	// 加载 locate 相关 skills
	toolDefs, skillBody := p.loadLocateSkills(agentCfg)
	sysPrompt += "\n\n" + skillBody

	// 渲染 locate-disclose.tmpl 作为输出格式要求
	disclosePrompt, err := p.promptRenderer.Render("locate-disclose", prompt.LocateDiscloseInput{
		CurrentRound: 0,
		MaxRounds:    3,
	})
	if err != nil {
		log.Printf("[orchestrator] render locate-disclose: %v", err)
	} else {
		sysPrompt += "\n\n" + disclosePrompt
	}

	// 多轮调查循环（轮数从 config pipeline.stages.locate.max_survey_rounds 读取）
	messages := []llm.Message{{Role: "user", Content: "开始根因定位调查"}}
	var allFindings []tree.Finding
	var allDiscarded []tree.DiscardedHypothesis
	maxRounds := p.pcfg.Pipeline.Stages.Locate.GetMaxSurveyRounds()

	for round := 1; round <= maxRounds; round++ {
		req := llm.ChatRequest{
			Model:    agentCfg.Model,
			System:   sysPrompt,
			Messages: messages,
			Tools:    skillToolToLLMTools(toolDefs),
		}
		resp, chatErr := llmAdapter.Chat(ctx, req)
		if chatErr != nil {
			log.Printf("[orchestrator] locate llm round %d: %v", round, chatErr)
			break
		}
		if len(resp.Choices) == 0 {
			log.Printf("[orchestrator] locate llm round %d: empty choices", round)
			break
		}

		choice := resp.Choices[0]

		// 处理 tool_use：分发执行后继续下一轮
		if choice.FinishReason == "tool_calls" && len(choice.Message.ToolCalls) > 0 {
			skillCalls := llmToolCallsToSkill(choice.Message.ToolCalls)
			messages = append(messages, llm.Message{
				Role:      "assistant",
				Content:   choice.Message.Content,
				ToolCalls: choice.Message.ToolCalls,
			})

			toolResults, dispatchErr := p.dispatcher.DispatchBatch(ctx, sess.ID, skillCalls)
			if dispatchErr != nil {
				log.Printf("[orchestrator] locate dispatch round %d: %v", round, dispatchErr)
			}
			messages = append(messages, skillResultsToMessages(toolResults)...)
			continue
		}

		// 解析本轮 disclose 输出
		text := choice.Message.Content
		roundFindings, roundDiscarded := parseDiscloseOutput(text)
		allFindings = append(allFindings, roundFindings...)
		allDiscarded = append(allDiscarded, roundDiscarded...)

		log.Printf("[orchestrator] locate round %d: %d findings, %d discarded",
			round, len(roundFindings), len(roundDiscarded))

		// 提取本轮方向列表，供下一轮 Decide 参考
		directions := extractDirections(text)

		// agent 返回了文本输出且没有 tool_calls，视为调查完成
		if len(allFindings) >= 3 {
			break
		}

		// 追加本轮输出为下一轮上下文，包含方向列表指导 LLM 自主选择
		messages = append(messages, llm.Message{Role: "assistant", Content: text})
		nextMsg := fmt.Sprintf("第%d轮调查完成。", round)
		if directions != "" {
			nextMsg += fmt.Sprintf("\n上一轮可选方向:\n%s\n请基于这些方向自主选择 confidence 最高的 top-1 继续下一轮调查。", directions)
		} else {
			nextMsg += " 继续下一轮调查，深入最有信心的方向。"
		}
		messages = append(messages, llm.Message{
			Role:    "user",
			Content: nextMsg,
		})
	}

	return allFindings, allDiscarded
}

// readLocateInput 从 get 节点的 input.yaml + output.yaml 读取数据构建 prompt.LocateInput。
func (p *pipelineImpl) readLocateInput(task *tree.Task, getNodeID string) prompt.LocateInput {
	input := prompt.LocateInput{Service: task.Service}

	// 从 get 节点（父节点）读取 output.yaml
	if getNodeID != "" {
		out, err := tree.ReadOutput(task.TaskID, getNodeID)
		if err == nil && out != nil {
			// 将 findings 格式化为 YAML 文本
			input.InputYAML = formatFindingsToYAML(out.Findings)
		}

		// 从 get 节点 input.yaml 读取 trace_id
		in, err := tree.ReadInput(task.TaskID, getNodeID)
		if err == nil {
			if tid, ok := in["trace_id"].(string); ok {
				input.TraceID = tid
			}
		}
	}

	return input
}

// loadLocateSkills 加载 locate 阶段需要的 skills 并注入为 LLM 工具。
func (p *pipelineImpl) loadLocateSkills(agentCfg config.AgentConfig) ([]skill.ToolDef, string) {
	if p.pcfg == nil || p.pcfg.Skills.Path == "" || p.skillLoader == nil || p.skillInjector == nil {
		return nil, ""
	}

	allSkills, err := p.skillLoader.LoadAll(p.pcfg.Skills.Path)
	if err != nil {
		log.Printf("[orchestrator] load locate skills: %v", err)
		return nil, ""
	}

	// 从 config 读取 locate 阶段允许的 skill 名称
	allowedSkills := p.getStageSkillNames(StageLocate)
	allowedSet := make(map[string]bool, len(allowedSkills))
	for _, name := range allowedSkills {
		allowedSet[name] = true
	}

	var filtered []*skill.Skill
	for _, s := range allSkills {
		if allowedSet[s.Name] {
			filtered = append(filtered, s)
		}
	}

	if len(filtered) == 0 {
		return nil, ""
	}

	toolDefs, skillBody, err := p.skillInjector.Inject(agentCfg.ResolveType(), filtered, agentCfg.NativeTools)
	if err != nil {
		log.Printf("[orchestrator] inject locate skills: %v", err)
		return nil, ""
	}

	return toolDefs, skillBody
}

// executeReview1 locate 后 Evaluator Review #1。
func (p *pipelineImpl) executeReview1(ctx context.Context, task *tree.Task, sess *session.Session) (*StageResult, error) {
	result := &StageResult{
		Stage: StageReview1,
	}
	start := time.Now()

	// 创建 review 节点（agent_role=evaluator）
	reviewNode, err := p.nodeOps.Create(task, task.CurrentNodeID, tree.NodeInput{
		Stage:     string(StageReview1),
		AgentRole: "evaluator",
	})
	if err != nil {
		return nil, fmt.Errorf("create review1 node: %w", err)
	}
	result.NodeID = reviewNode.ID

	// 查找 locate 节点作为审查输入
	locateNode, err := p.nodeOps.Read(task, task.CurrentNodeID)
	if err != nil {
		return nil, fmt.Errorf("read locate node: %w", err)
	}

	// P0：Evaluator 接口占位，P1 完整实现
	if p.evaluator != nil {
		report, err := p.evaluator.ReviewFinding(ctx, task.TaskID, locateNode)
		if err != nil {
			return nil, fmt.Errorf("review finding: %w", err)
		}

		gating := GatingFromReport(report, task.RetryCount["locate"], p.maxRetryPerStage())
		if !gating.Allowed {
			task.RetryCount["locate"]++
			result.RetryCount = task.RetryCount["locate"]
			if gating.Blocked {
				task.Status = tree.StatusBlocked
				p.treeMgr.Save(task)
				result.Status = tree.StatusBlocked
				result.Error = fmt.Errorf("review #1 blocked: %s", gating.Reason)
				return result, result.Error
			}
			// 未 blocked，退回 locate
			task.Status = tree.StatusLocating
			result.Status = tree.StatusLocating
			p.treeMgr.Save(task)
			return result, nil
		}
	}

	// 通过审查，状态不变（保持 expectation_locked 进入 Planner）
	result.Status = task.Status
	result.Duration = time.Since(start)
	return result, nil
}

// executePlan Planner 阶段：通过 sessionMgr.Spawn 启动独立 Planner session，
// 读取 locate 节点的 findings，调用 planner.BuildPlan() 生成 plan.json 并写入 output.yaml。
func (p *pipelineImpl) executePlan(ctx context.Context, task *tree.Task, sess *session.Session) (*StageResult, error) {
	result := &StageResult{
		Stage: StagePlan,
	}
	start := time.Now()

	// 检查 expectation_locked
	if task.Status != tree.StatusExpectationLocked {
		return nil, fmt.Errorf("plan 阶段要求 expectation_locked, 当前为 %s", task.Status)
	}

	// 从 locate 节点读取 findings
	plannerInput := p.buildPlannerInput(task)

	// 规则式生成 PlanJSON（确定性的输入→输出映射）
	planOutput, err := p.planner.BuildPlan(plannerInput)
	if err != nil {
		return nil, fmt.Errorf("build plan: %w", err)
	}

	// 选择 agent 名称：优先使用配置，fallback "claude"
	agentName := "claude"
	if p.pcfg != nil && p.pcfg.Planner.Agent != "" {
		agentName = p.pcfg.Planner.Agent
	}

	// Spawn Planner 子 session（独立 context，独立模型）
	planSession, err := p.sessionMgr.Spawn(sess, session.SubTaskInput{
		Prompt:    "plan-system", // prompts/plan-system.tmpl
		Skills:    nil,           // Planner 不需要外部 skill
		AgentName: agentName,
	})
	if err != nil {
		return nil, fmt.Errorf("spawn planner session: %w", err)
	}

	// 创建 plan 节点
	planNode, err := p.nodeOps.Create(task, task.CurrentNodeID, tree.NodeInput{
		Stage:     string(StagePlan),
		AgentRole: "planner",
	})
	if err != nil {
		return nil, fmt.Errorf("create plan node: %w", err)
	}
	result.NodeID = planNode.ID

	// 将 PlanJSON 写入节点 output.yaml
	if err := p.writePlanOutput(task.TaskID, planNode.ID, planOutput, planSession.ID); err != nil {
		log.Printf("[orchestrator] write plan output: %v", err)
	}

	task.Status = tree.StatusFixing
	task.CurrentNodeID = planNode.ID
	result.Status = tree.StatusFixing
	result.Duration = time.Since(start)

	if err := p.treeMgr.Save(task); err != nil {
		return nil, fmt.Errorf("save task after plan: %w", err)
	}

	return result, nil
}

// buildPlannerInput 从 locate 节点的 output.yaml 构建 PlannerInput。
func (p *pipelineImpl) buildPlannerInput(task *tree.Task) planner.PlannerInput {
	input := planner.PlannerInput{}

	// 尝试从当前节点的父节点（locate 节点）读取 output
	if task.CurrentNodeID != "" {
		locateOut, err := tree.ReadOutput(task.TaskID, task.CurrentNodeID)
		if err == nil && locateOut != nil {
			for _, f := range locateOut.Findings {
				input.Findings = append(input.Findings, planner.Finding{
					Hypothesis:     f.Hypothesis,
					ConfidenceSelf: f.ConfidenceFinal,
					Evidence:       f.Evidence,
					Status:         "confirmed",
				})
			}
			for _, dh := range locateOut.DiscardedHypotheses {
				input.DiscardedHypotheses = append(input.DiscardedHypotheses, planner.Finding{
					Hypothesis:     dh.Hypothesis,
					Evidence:       []string{dh.EvidenceAgainst},
					Status:         "discarded",
				})
			}
			input.UserDirectives = locateOut.UserDirectives
		}
	}

	return input
}

// writePlanOutput 将 PlannerOutput 写入节点的 output.yaml。
func (p *pipelineImpl) writePlanOutput(taskID, nodeID string, planOutput *planner.PlannerOutput, sessionID string) error {
	return tree.WriteOutput(taskID, nodeID, &tree.NodeOutput{
		NodeID:    nodeID,
		SessionID: sessionID,
		Plan: &tree.PlanOutput{
			Version:       planOutput.Plan.Version,
			Goal:          planOutput.Plan.Goal,
			Steps:         convertPlanSteps(planOutput.Plan.Steps),
			Alternatives:  convertAlternatives(planOutput.Plan.Alternatives),
			PreChecklist:  planOutput.Plan.PreChecklist,
			PostChecklist: planOutput.Plan.PostChecklist,
		},
	})
}

// convertPlanSteps 将 planner.PlanStep 切片转换为 tree.PlanStep 切片。
func convertPlanSteps(steps []planner.PlanStep) []tree.PlanStep {
	result := make([]tree.PlanStep, len(steps))
	for i, s := range steps {
		result[i] = tree.PlanStep{
			ID:              s.ID,
			Action:          s.Action,
			Target:          s.Target,
			Approach:        s.Approach,
			EstimatedImpact: s.EstimatedImpact,
			Risk:            s.Risk,
			Rollback:        s.Rollback,
			ConfidenceSelf:  s.ConfidenceSelf,
		}
	}
	return result
}

// convertAlternatives 将 planner.Alternative 切片转换为 tree.Alternative 切片。
func convertAlternatives(alt []planner.Alternative) []tree.Alternative {
	result := make([]tree.Alternative, len(alt))
	for i, a := range alt {
		result[i] = tree.Alternative{
			Approach:  a.Approach,
			Tradeoff:  a.Tradeoff,
			Discarded: a.Discarded,
		}
	}
	return result
}

// buildLocateInput 从 task 和调查 findings 构造 MCTS LocateInput。
func (p *pipelineImpl) buildLocateInput(task *tree.Task, surveyFindings []tree.Finding) LocateInput {
	input := LocateInput{}

	// 将调查 findings 序列化入 InputYAML
	var yamlLines []string
	for _, f := range surveyFindings {
		yamlLines = append(yamlLines, "- hypothesis: "+f.Hypothesis)
		yamlLines = append(yamlLines, "  confidence: "+ftoa(f.ConfidenceFinal))
		for _, e := range f.Evidence {
			yamlLines = append(yamlLines, "  evidence: "+e)
		}
	}
	if len(yamlLines) > 0 {
		input.InputYAML = []byte(strings.Join(yamlLines, "\n"))
	}

	// 同时从当前节点的 output.yaml 读取已有 findings 作为补充
	if task.CurrentNodeID != "" {
		out, err := tree.ReadOutput(task.TaskID, task.CurrentNodeID)
		if err == nil && out != nil {
			existingYAML := findingsToYAMLBytes(out.Findings)
			if len(existingYAML) > 0 {
				if len(input.InputYAML) > 0 {
					input.InputYAML = append(input.InputYAML, '\n')
					input.InputYAML = append(input.InputYAML, existingYAML...)
				} else {
					input.InputYAML = existingYAML
				}
			}
		}
	}

	return input
}

// buildFixInput 从 plan 节点读取 plan.json 构造 FixInput。
func (p *pipelineImpl) buildFixInput(task *tree.Task) FixInput {
	input := FixInput{}

	// 尝试从当前节点读取 plan output
	if task.CurrentNodeID != "" {
		planOut, err := tree.ReadOutput(task.TaskID, task.CurrentNodeID)
		if err == nil && planOut != nil && planOut.Plan != nil {
			input.PlanJSON = planJSONToBytes(planOut.Plan)
		}
	}

	return input
}

// writeFixOutput 将 fix 结果写入节点 output.yaml。
func (p *pipelineImpl) writeFixOutput(taskID, nodeID string, outputs []FixOutput) error {
	if len(outputs) == 0 {
		return nil
	}
	// 取第一个（最优）fix 输出，将步骤信息写入 plan output，patch 写入 Patch 字段
	o := outputs[0]
	return tree.WriteOutput(taskID, nodeID, &tree.NodeOutput{
		NodeID: nodeID,
		Plan: &tree.PlanOutput{
			Version: "1.0",
			Goal:    "fix: " + o.StepID,
			Steps: []tree.PlanStep{{
				ID:       o.StepID,
				Action:   "apply fix",
				Approach: "fix generated by MCTS search",
			}},
		},
		Patch: &tree.PatchOutput{
			Diff:   string(o.Diff),
			StepID: o.StepID,
		},
	})
}

// executeFix fix 阶段：调 MCTS engine + 应用 patch。
func (p *pipelineImpl) executeFix(ctx context.Context, task *tree.Task, sess *session.Session) (*StageResult, error) {
	result := &StageResult{
		Stage: StageFix,
	}
	start := time.Now()

	if retryExceeded(task, "fix", p.maxRetryPerStage()) {
		task.Status = tree.StatusBlocked
		p.treeMgr.Save(task)
		result.Status = tree.StatusBlocked
		result.Error = fmt.Errorf("fix retry exceeded")
		return result, result.Error
	}

	node, err := p.nodeOps.Create(task, task.CurrentNodeID, tree.NodeInput{
		Stage: string(StageFix),
	})
	if err != nil {
		return nil, fmt.Errorf("create fix node: %w", err)
	}
	result.NodeID = node.ID

	// 调 MCTS engine 运行 fix 搜索
	if p.mctsEngine != nil {
		// 从 plan 节点读取 plan.json 构造 FixInput
		fixInput := p.buildFixInput(task)

		fixOutputs, err := p.mctsEngine.RunFix(ctx, fixInput)
		if err != nil {
			task.RetryCount["fix"]++
			result.RetryCount = task.RetryCount["fix"]
			if retryExceeded(task, "fix", p.maxRetryPerStage()) {
				task.Status = tree.StatusBlocked
				p.treeMgr.Save(task)
				result.Status = tree.StatusBlocked
				result.Error = fmt.Errorf("fix MCTS failed: %w", err)
				return result, result.Error
			}
		}

		// 将 fix 结果回写到节点 output.yaml
		if len(fixOutputs) > 0 {
			if err := p.writeFixOutput(task.TaskID, node.ID, fixOutputs); err != nil {
				log.Printf("[orchestrator] write fix output: %v", err)
			}

			// P1: 门控 — diff-validate 校验 diff 合法性
			if len(fixOutputs[0].Diff) > 0 {
				issues := runDiffValidate(string(fixOutputs[0].Diff))
				if len(issues) > 0 {
					log.Printf("[orchestrator] diff-validate failed: %v", issues)
					tree.WriteOutput(task.TaskID, node.ID, &tree.NodeOutput{
						NodeID: node.ID,
						ReviewReport: &tree.ReviewReportYAML{
							Passed:   false,
							Blockers: issues,
						},
					})
					task.RetryCount["fix"]++
					result.RetryCount = task.RetryCount["fix"]
					if retryExceeded(task, "fix", p.maxRetryPerStage()) {
						task.Status = tree.StatusBlocked
						p.treeMgr.Save(task)
						result.Status = tree.StatusBlocked
						result.Error = fmt.Errorf("diff-validate blocked: %v", issues)
						return result, result.Error
					}
					task.Status = tree.StatusFixing
					result.Status = tree.StatusFixing
					p.treeMgr.Save(task)
					return result, nil
				}
			}
		}
	}

	// fix 完成门控：go build 在 verify 阶段执行
	task.CurrentNodeID = node.ID
	result.Status = task.Status // fix 后保持 fixing 直到 review#2 通过
	result.Duration = time.Since(start)

	if err := p.treeMgr.Save(task); err != nil {
		return nil, fmt.Errorf("save task after fix: %w", err)
	}

	return result, nil
}

// executeReview2 fix 后 Evaluator Review #2（主 review）。
func (p *pipelineImpl) executeReview2(ctx context.Context, task *tree.Task, sess *session.Session) (*StageResult, error) {
	result := &StageResult{
		Stage: StageReview2,
	}
	start := time.Now()

	reviewNode, err := p.nodeOps.Create(task, task.CurrentNodeID, tree.NodeInput{
		Stage:     string(StageReview2),
		AgentRole: "evaluator",
	})
	if err != nil {
		return nil, fmt.Errorf("create review2 node: %w", err)
	}
	result.NodeID = reviewNode.ID

	fixNode, err := p.nodeOps.Read(task, task.CurrentNodeID)
	if err != nil {
		return nil, fmt.Errorf("read fix node: %w", err)
	}

	if p.evaluator != nil {
		report, err := p.evaluator.ReviewFix(ctx, task.TaskID, fixNode, nil)
		if err != nil {
			return nil, fmt.Errorf("review fix: %w", err)
		}

		gating := GatingFromReport(report, task.RetryCount["fix"], p.maxRetryPerStage())
		if !gating.Allowed {
			task.RetryCount["fix"]++
			result.RetryCount = task.RetryCount["fix"]
			if gating.Blocked {
				task.Status = tree.StatusBlocked
				p.treeMgr.Save(task)
				result.Status = tree.StatusBlocked
				result.Error = fmt.Errorf("review #2 blocked: %s", gating.Reason)
				return result, result.Error
			}
			task.Status = tree.StatusFixing
			result.Status = tree.StatusFixing
			p.treeMgr.Save(task)
			return result, nil
		}
	}

	task.Status = tree.StatusVerifying
	task.CurrentNodeID = reviewNode.ID
	result.Status = tree.StatusVerifying
	result.Duration = time.Since(start)

	if err := p.treeMgr.Save(task); err != nil {
		return nil, fmt.Errorf("save task after review2: %w", err)
	}

	return result, nil
}

// INSTRUMENT: orchestrator-stage-verify — verify 阶段：LLM tool call dispatch 验证
// LAYER: L1
// STATUS: implemented
// executeVerify verify 阶段：LLM 根据 diff 自主 tool_call 验证命令，执行后判定 passed/failed。
// 设计: verify 升级为 LLM tool call dispatch（与 get/locate/commit 一致）：
//
//	1. LoadAll() → Inject() → LLM Chat with tools
//	2. LLM 根据 diff 自主 tool_call: go-test, golangci-lint, http-probe...
//	3. dispatcher.DispatchBatch() 执行
//	4. LLM 看执行结果，给 passed/failed
func (p *pipelineImpl) executeVerify(ctx context.Context, task *tree.Task, sess *session.Session) (*StageResult, error) {
	result := &StageResult{
		Stage: StageVerify,
	}
	start := time.Now()

	if retryExceeded(task, "verify", p.maxRetryPerStage()) {
		task.Status = tree.StatusBlocked
		p.treeMgr.Save(task)
		result.Status = tree.StatusBlocked
		result.Error = fmt.Errorf("verify retry exceeded")
		return result, result.Error
	}

	node, err := p.nodeOps.Create(task, task.CurrentNodeID, tree.NodeInput{
		Stage: string(StageVerify),
	})
	if err != nil {
		return nil, fmt.Errorf("create verify node: %w", err)
	}
	result.NodeID = node.ID

	// 1. 渲染 verify-system.tmpl
	sysPrompt, _ := p.promptRenderer.Render("verify-system", prompt.VerifyInput{
		Service: task.Service,
	})

	// 2. 获取 verify agent 配置
	var verifyAgentCfg *config.AgentConfig
	if p.pcfg != nil {
		if cfg, ok := p.pcfg.Agents["generator"]; ok {
			verifyAgentCfg = &cfg
		} else if cfg, ok := p.pcfg.Agents[p.pcfg.GetDefaultAgent()]; ok {
			verifyAgentCfg = &cfg
		}
	}
	verifyAgentType := adapter.AgentLLMNative
	if verifyAgentCfg != nil {
		verifyAgentType = adapter.ResolveAgentType(verifyAgentCfg.ResolveType())
	}

	// 3. 加载 verify skills → Inject → LLM Chat with tools
	var toolDefs []skill.ToolDef
	var skillBody string
	if p.pcfg != nil && p.pcfg.Skills.Path != "" && p.skillLoader != nil && p.skillInjector != nil {
		skills, loadErr := p.skillLoader.LoadAll(p.pcfg.Skills.Path)
		if loadErr != nil {
			log.Printf("[orchestrator] verify load skills: %v", loadErr)
		} else {
			allowedSkills := p.getStageSkillNames(StageVerify)
			var verifySkills []*skill.Skill
			for _, s := range skills {
				if slices.Contains(allowedSkills, s.Name) {
					verifySkills = append(verifySkills, s)
				}
			}
			if len(verifySkills) > 0 {
				injectType := "llm_native"
				if verifyAgentCfg != nil {
					injectType = verifyAgentCfg.ResolveType()
				}
				var injectErr error
				toolDefs, skillBody, injectErr = p.skillInjector.Inject(injectType, verifySkills, nil)
				if injectErr != nil {
					log.Printf("[orchestrator] inject verify skills: %v", injectErr)
				} else {
					sysPrompt += "\n\n" + skillBody
				}
			}
		}
	}

	// 4. 按 adapter 类型执行 verify（LLM Native → Chat + tool dispatch，CLI → Run）
	var verifyReport string
	var passed bool
	verifyAdapter, adapterErr := p.getAgentAdapter(verifyAgentType)
	if adapterErr != nil {
		log.Printf("[orchestrator] verify: get agent adapter: %v", adapterErr)
	} else {
		switch verifyAdapter.Type() {
		case adapter.AgentLLMNative:
			llmAdapter := verifyAdapter.(*adapter.LLMNativeAdapter)
			model := "default"
			if verifyAgentCfg != nil {
				model = verifyAgentCfg.Model
			}
			// 第1轮: LLM 自主 tool_call
			resp, chatErr := llmAdapter.Chat(ctx, llm.ChatRequest{
				Model:    model,
				System:   sysPrompt,
				Messages: []llm.Message{{Role: "user", Content: "请对当前改动进行验证"}},
				Tools:    skillToolToLLMTools(toolDefs),
			})
			if chatErr != nil {
				log.Printf("[orchestrator] verify llm chat: %v", chatErr)
			} else if len(resp.Choices) > 0 {
				if resp.Choices[0].FinishReason == "tool_calls" {
					// 分发 tool calls
					skillCalls := llmToolCallsToSkill(resp.Choices[0].Message.ToolCalls)
					results, dispatchErr := p.dispatcher.DispatchBatch(ctx, sess.ID, skillCalls)
					if dispatchErr != nil {
						log.Printf("[orchestrator] verify dispatch: %v", dispatchErr)
					}
					verifyReport = toolResultsToText(results)

					// 第2轮: LLM 看执行结果并判定
					messages := []llm.Message{
						{Role: "assistant", Content: resp.Choices[0].Message.Content, ToolCalls: resp.Choices[0].Message.ToolCalls},
					}
					messages = append(messages, skillResultsToMessages(results)...)
					resp2, chatErr2 := llmAdapter.Chat(ctx, llm.ChatRequest{
						Model:    model,
						System:   sysPrompt,
						Messages: messages,
					})
					if chatErr2 == nil && len(resp2.Choices) > 0 {
						judgment := resp2.Choices[0].Message.Content
						verifyReport += "\n\n=== LLM Judgment ===\n" + judgment
						passed = !strings.Contains(strings.ToLower(judgment), "fail")
					}
				} else {
					// 纯文本响应
					verifyReport = resp.Choices[0].Message.Content
					passed = !strings.Contains(strings.ToLower(verifyReport), "fail")
				}
			}
		default:
			// CLI adapter: 单次 Run() 调用
			userPrompt := sysPrompt + "\n\n请对当前改动进行验证，返回 passed/failed 判断"
			agentResult, runErr := verifyAdapter.Run(ctx, sess.WorkingDir, adapter.AgentInput{
				Prompt:    userPrompt,
				SessionID: sess.ID,
			})
			if runErr == nil && agentResult != nil {
				verifyReport = agentResult.Text
				passed = !strings.Contains(strings.ToLower(verifyReport), "fail")
			} else if runErr != nil {
				log.Printf("[orchestrator] verify cli run: %v", runErr)
			}
		}
	}

	// 5. 写入 output.yaml
	if err := tree.WriteOutput(task.TaskID, node.ID, &tree.NodeOutput{
		NodeID:       node.ID,
		SessionID:    sess.ID,
		ReviewReport: &tree.ReviewReportYAML{Passed: passed},
	}); err != nil {
		log.Printf("[orchestrator] write verify output: %v", err)
	}

	task.CurrentNodeID = node.ID
	result.Status = task.Status
	result.Duration = time.Since(start)

	if err := p.treeMgr.Save(task); err != nil {
		return nil, fmt.Errorf("save task after verify: %w", err)
	}

	return result, nil
}

// executeReview3 verify 后 Evaluator Review #3。
func (p *pipelineImpl) executeReview3(ctx context.Context, task *tree.Task, sess *session.Session) (*StageResult, error) {
	result := &StageResult{
		Stage: StageReview3,
	}
	start := time.Now()

	reviewNode, err := p.nodeOps.Create(task, task.CurrentNodeID, tree.NodeInput{
		Stage:     string(StageReview3),
		AgentRole: "evaluator",
	})
	if err != nil {
		return nil, fmt.Errorf("create review3 node: %w", err)
	}
	result.NodeID = reviewNode.ID

	verifyNode, err := p.nodeOps.Read(task, task.CurrentNodeID)
	if err != nil {
		return nil, fmt.Errorf("read verify node: %w", err)
	}

	if p.evaluator != nil {
		report, err := p.evaluator.ReviewVerification(ctx, task.TaskID, verifyNode, nil)
		if err != nil {
			return nil, fmt.Errorf("review verification: %w", err)
		}

		gating := GatingFromReport(report, task.RetryCount["verify"], p.maxRetryPerStage())
		if !gating.Allowed {
			task.RetryCount["verify"]++
			result.RetryCount = task.RetryCount["verify"]
			if gating.Blocked {
				task.Status = tree.StatusBlocked
				p.treeMgr.Save(task)
				result.Status = tree.StatusBlocked
				result.Error = fmt.Errorf("review #3 blocked: %s", gating.Reason)
				return result, result.Error
			}
			task.Status = tree.StatusVerifying
			result.Status = tree.StatusVerifying
			p.treeMgr.Save(task)
			return result, nil
		}
	}

	task.Status = tree.StatusCommitting
	task.CurrentNodeID = reviewNode.ID
	result.Status = tree.StatusCommitting
	result.Duration = time.Since(start)

	if err := p.treeMgr.Save(task); err != nil {
		return nil, fmt.Errorf("save task after review3: %w", err)
	}

	return result, nil
}

// executeCommit commit 阶段：LLM 驱动 git/glab/jira 操作。
// 设计: commit 需要 LLM 生成 MR 描述，接近 executeGet 模式。
func (p *pipelineImpl) executeCommit(ctx context.Context, task *tree.Task, sess *session.Session) (*StageResult, error) {
	result := &StageResult{
		Stage: StageCommit,
	}
	start := time.Now()

	node, err := p.nodeOps.Create(task, task.CurrentNodeID, tree.NodeInput{
		Stage: string(StageCommit),
	})
	if err != nil {
		return nil, fmt.Errorf("create commit node: %w", err)
	}
	result.NodeID = node.ID

	// 获取 commit agent 配置（前置，供 skill injection 和 adapter 共用）
	var commitAgentCfg *config.AgentConfig
	var commitAgentType adapter.AgentType
	if p.pcfg != nil {
		if cfg, ok := p.pcfg.Agents["generator"]; ok {
			commitAgentCfg = &cfg
		} else if cfg, ok := p.pcfg.Agents[p.pcfg.GetDefaultAgent()]; ok {
			commitAgentCfg = &cfg
		}
	}
	if commitAgentCfg != nil {
		commitAgentType = adapter.ResolveAgentType(commitAgentCfg.ResolveType())
	} else {
		commitAgentType = adapter.AgentLLMNative
	}

	// 1. 渲染 commit-system.tmpl
	sysPrompt, _ := p.promptRenderer.Render("commit-system", prompt.CommitInput{
		Service:      task.Service,
		JiraID:       task.IssueRef,
	})

	// 2. 加载 commit skills: glab-mr, jira-update
	var toolDefs []skill.ToolDef
	var skillBody string
	if p.pcfg != nil && p.pcfg.Skills.Path != "" && p.skillLoader != nil && p.skillInjector != nil {
		skills, loadErr := p.skillLoader.LoadAll(p.pcfg.Skills.Path)
		if loadErr != nil {
			log.Printf("[orchestrator] commit load skills: %v", loadErr)
		} else {
			allowedSkills := p.getStageSkillNames(StageCommit)
			var commitSkills []*skill.Skill
			for _, s := range skills {
				if slices.Contains(allowedSkills, s.Name) {
					commitSkills = append(commitSkills, s)
				}
			}
			if len(commitSkills) > 0 {
				injectAgentType := commitAgentCfg.ResolveType()
				if injectAgentType == "" {
					injectAgentType = "llm_native"
				}
				var injectErr error
				toolDefs, skillBody, injectErr = p.skillInjector.Inject(injectAgentType, commitSkills, nil)
				if injectErr != nil {
					log.Printf("[orchestrator] inject commit skills: %v", injectErr)
				} else {
					sysPrompt += "\n\n" + skillBody
				}
			}
		}
	}

	// 3. 按 adapter 类型执行 commit（LLM Native → Chat + tool dispatch，CLI → Run）
	var commitSummary string
	commitAdapter, adapterErr := p.getAgentAdapter(commitAgentType)
	if adapterErr != nil {
		log.Printf("[orchestrator] commit: get agent adapter: %v, marking done anyway", adapterErr)
	} else {
		switch commitAdapter.Type() {
		case adapter.AgentLLMNative:
			llmAdapter := commitAdapter.(*adapter.LLMNativeAdapter)
			model := "default"
			if commitAgentCfg != nil {
				model = commitAgentCfg.Model
			}
			resp, chatErr := llmAdapter.Chat(ctx, llm.ChatRequest{
				Model:    model,
				System:   sysPrompt,
				Messages: []llm.Message{{Role: "user", Content: "请创建 MR 并更新 JIRA"}},
				Tools:    skillToolToLLMTools(toolDefs),
			})
			if chatErr != nil {
				log.Printf("[orchestrator] commit llm chat: %v", chatErr)
			} else if len(resp.Choices) > 0 {
				if resp.Choices[0].FinishReason == "tool_calls" {
					skillCalls := llmToolCallsToSkill(resp.Choices[0].Message.ToolCalls)
					results, dispatchErr := p.dispatcher.DispatchBatch(ctx, sess.ID, skillCalls)
					if dispatchErr != nil {
						log.Printf("[orchestrator] commit dispatch: %v", dispatchErr)
					}
					commitSummary = toolResultsToText(results)
				} else {
					commitSummary = resp.Choices[0].Message.Content
				}
			}
		default:
			// CLI adapter: 单次 Run() 调用
			userPrompt := sysPrompt + "\n\n请创建 MR 并更新 JIRA"
			agentResult, runErr := commitAdapter.Run(ctx, sess.WorkingDir, adapter.AgentInput{
				Prompt:    userPrompt,
				SessionID: sess.ID,
			})
			if runErr != nil {
				log.Printf("[orchestrator] commit cli run: %v", runErr)
			} else if agentResult != nil {
				commitSummary = agentResult.Text
			}
		}
	}

	// 4. 写入 output.yaml（含 commit 摘要）
	output := &tree.NodeOutput{
		NodeID:    node.ID,
		SessionID: sess.ID,
	}
	if commitSummary != "" {
		output.ReviewReport = &tree.ReviewReportYAML{
			Passed:   true,
			Warnings: []string{commitSummary},
		}
	}
	if err := tree.WriteOutput(task.TaskID, node.ID, output); err != nil {
		log.Printf("[orchestrator] write commit output: %v", err)
	}

	task.Status = tree.StatusDone
	task.CurrentNodeID = node.ID
	result.Status = tree.StatusDone
	result.Duration = time.Since(start)

	if err := p.treeMgr.Save(task); err != nil {
		return nil, fmt.Errorf("save task after commit: %w", err)
	}

	return result, nil
}

// findingsToYAMLBytes 将 findings 序列化为近似 YAML 的字节数组（用于 InputYAML）。
func findingsToYAMLBytes(findings []tree.Finding) []byte {
	if len(findings) == 0 {
		return nil
	}
	var lines string
	for _, f := range findings {
		lines += "- hypothesis: " + f.Hypothesis + "\n"
		lines += "  confidence: " + ftoa(f.ConfidenceFinal) + "\n"
		for _, e := range f.Evidence {
			lines += "  evidence: " + e + "\n"
		}
	}
	return []byte(lines)
}

// planJSONToBytes 将 PlanOutput 序列化为近似 JSON 的字节数组。
func planJSONToBytes(plan *tree.PlanOutput) []byte {
	if plan == nil {
		return nil
	}
	steps := ""
	for i, s := range plan.Steps {
		if i > 0 {
			steps += ","
		}
		steps += `{"id":"` + s.ID + `","action":"` + s.Action + `","target":"` + s.Target + `","approach":"` + s.Approach + `"}`
	}
	return []byte(`{"version":"` + plan.Version + `","goal":"` + plan.Goal + `","steps":[` + steps + `]}`)
}

// ftoa 简单的 float64 → string 转换。
func ftoa(f float64) string {
	if f == 0 {
		return "0.00"
	}
	return fmt.Sprintf("%.2f", f)
}

// statusFromTaskStatus 从 task 状态推导当前应执行的 pipeline 阶段。
func statusFromTaskStatus(ts tree.TaskStatus) Stage {
	switch ts {
	case tree.StatusPending:
		return StageGet
	case tree.StatusScopeDefined:
		return StageLocate
	case tree.StatusLocating:
		return StageLocate
	case tree.StatusExpectationLocked:
		return StagePlan
	case tree.StatusFixing:
		return StageFix
	case tree.StatusVerifying:
		return StageVerify
	case tree.StatusCommitting:
		return StageCommit
	case tree.StatusDone:
		return "" // 已完成
	case tree.StatusBlocked:
		return "" // 需人工介入
	default:
		return ""
	}
}

// ---- locate 阶段辅助函数 ----

// parseDiscloseOutput 从 LLM 的 disclose YAML 输出中提取 findings 和 discarded_hypotheses。
// 使用 YAML 反序列化，对齐 tree.Finding / tree.DiscardedHypothesis 的 yaml tag。
func parseDiscloseOutput(text string) ([]tree.Finding, []tree.DiscardedHypothesis) {
	yamlBlock := extractYAMLBlock(text)
	if yamlBlock == "" {
		return nil, nil
	}

	// 直接反序列化到 tree.NodeOutput，取其 Findings/DiscardedHypotheses
	var output tree.NodeOutput
	if err := yaml.Unmarshal([]byte(yamlBlock), &output); err != nil {
		log.Printf("[orchestrator] parse disclose yaml: %v", err)
		return nil, nil
	}

	// 对空 status 的 finding 默认设为 "investigating"
	for i := range output.Findings {
		if output.Findings[i].Status == "" {
			output.Findings[i].Status = "investigating"
		}
	}

	return output.Findings, output.DiscardedHypotheses
}

// extractDirections 从 disclose 输出文本中提取「下一步可选方向」段。
// 用于下一轮 Decide 时将方向列表回传给 LLM，指导其自主选择。
func extractDirections(text string) string {
	// 查找方向段标题（支持中英文标记）
	idx := strings.Index(text, "下一步可选方向")
	if idx < 0 {
		idx = strings.Index(text, "可选方向")
	}
	if idx < 0 {
		return ""
	}

	// 从标题行之后开始
	lineEnd := strings.Index(text[idx:], "\n")
	if lineEnd < 0 {
		return ""
	}
	start := idx + lineEnd + 1

	// 提取以 [数字] 开头的行，直到遇到空行或非方向行
	var lines []string
	for _, line := range strings.Split(text[start:], "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			if len(lines) > 0 {
				break // 方向段结束
			}
			continue
		}
		// 检查是否为方向行（[N], 1., 1) 开头）
		if end := strings.Index(trimmed, "]"); end > 1 && trimmed[0] == '[' {
			// 匹配 [N] 格式，N 为任意位数
			if _, err := strconv.Atoi(trimmed[1:end]); err == nil {
				lines = append(lines, trimmed)
				continue
			}
		}
		if len(trimmed) >= 2 && trimmed[0] >= '0' && trimmed[0] <= '9' && (trimmed[1] == '.' || trimmed[1] == ')') {
			lines = append(lines, trimmed)
		} else {
			// 非方向行，结束
			if len(lines) > 0 {
				break
			}
		}
	}

	if len(lines) == 0 {
		return ""
	}
	return strings.Join(lines, "\n")
}

// mergeFindings 合并调查 findings 与 MCTS 搜索结果，去重并保留双源 confidence。
func mergeFindings(surveyFindings []tree.Finding, mctsOutputs []LocateOutput) []tree.Finding {
	result := make([]tree.Finding, 0, len(surveyFindings)+len(mctsOutputs))

	// 先添加调查 findings
	result = append(result, surveyFindings...)

	// 添加 MCTS 结果，简单去重（按 hypothesis 文本匹配）
	for _, o := range mctsOutputs {
		dup := false
		for i := range result {
			if strings.EqualFold(result[i].Hypothesis, o.Hypothesis) {
				// 合并证据
				result[i].Evidence = append(result[i].Evidence, o.Evidence...)
				if o.Confidence > result[i].ConfidenceFinal {
					result[i].ConfidenceFinal = o.Confidence
				}
				dup = true
				break
			}
		}
		if !dup {
			result = append(result, tree.Finding{
				Hypothesis:      o.Hypothesis,
				ConfidenceSelf:  o.Confidence,
				ConfidenceFinal: o.Confidence,
				Evidence:        o.Evidence,
				Status:          "confirmed",
			})
		}
	}

	// 去重每个 finding 的 evidence（保持插入顺序）
	for i := range result {
		result[i].Evidence = dedupStrings(result[i].Evidence)
	}

	return result
}

// dedupStrings 去重字符串切片，保持插入顺序。
func dedupStrings(ss []string) []string {
	seen := make(map[string]bool, len(ss))
	out := make([]string, 0, len(ss))
	for _, s := range ss {
		if !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	return out
}

// formatFindingsToYAML 将 findings 序列化为合法的 YAML 文本，每行预缩进 2 空格。
// 用于模板中的 block scalar 注入：input.yaml: |\n{{ .InputYAML }}
func formatFindingsToYAML(findings []tree.Finding) string {
	if len(findings) == 0 {
		return ""
	}
	data, err := yaml.Marshal(findings)
	if err != nil {
		log.Printf("[orchestrator] marshal findings to yaml: %v", err)
		return ""
	}
	// 每行预缩进 2 空格，适配模板中 input.yaml: | 的 block scalar 格式
	lines := strings.Split(strings.TrimRight(string(data), "\n"), "\n")
	for i, line := range lines {
		lines[i] = "  " + line
	}
	return strings.Join(lines, "\n")
}

// getAgentAdapter 从 adapter registry 获取 AgentAdapter（支持所有类型）。
// 若 registry 为 nil 或找不到 adapter，返回明确错误，不静默回退。
func (p *pipelineImpl) getAgentAdapter(agentType adapter.AgentType) (adapter.AgentAdapter, error) {
	if p.adapterRegistry == nil {
		return nil, fmt.Errorf("adapter registry is nil")
	}
	a, err := p.adapterRegistry.Get(agentType)
	if err != nil {
		return nil, fmt.Errorf("get adapter %q: %w", agentType, err)
	}
	return a, nil
}

// getLLMAdapter 从 adapter registry 获取 LLM Native 适配器（类型断言）。
// 仅当 agent type 为 llm_native 时成功，CLI adapter 返回错误。
func (p *pipelineImpl) getLLMAdapter(agentType adapter.AgentType) (*adapter.LLMNativeAdapter, error) {
	a, err := p.getAgentAdapter(agentType)
	if err != nil {
		return nil, err
	}
	llmAdapter, ok := a.(*adapter.LLMNativeAdapter)
	if !ok {
		return nil, fmt.Errorf("adapter %q is not LLM Native (type=%T)", agentType, a)
	}
	return llmAdapter, nil
}

// getStageSkillNames 从 config 读取指定阶段的 skill 名称列表，未配置时使用默认值。
func (p *pipelineImpl) getStageSkillNames(stage Stage) []string {
	if p.pcfg != nil {
		switch stage {
		case StageGet:
			if skills := p.pcfg.Pipeline.Stages.Get.Skills; len(skills) > 0 {
				return skills
			}
		case StageLocate:
			if skills := p.pcfg.Pipeline.Stages.Locate.Skills; len(skills) > 0 {
				return skills
			}
		case StageVerify:
			if skills := p.pcfg.Pipeline.Stages.Verify.Skills; len(skills) > 0 {
				return skills
			}
		case StageCommit:
			if skills := p.pcfg.Pipeline.Stages.Commit.Skills; len(skills) > 0 {
				return skills
			}
		}
	}

	// 默认值（向后兼容）
	switch stage {
	case StageGet:
		return []string{"jira-query", "feishu-search"}
	case StageLocate:
		return []string{"loki-query", "prom-query", "kubectl-inspect", "tempo-query", "feishu-search"}
	case StageVerify:
		return []string{"golangci-lint", "go-test", "go-build", "go-vet", "skaffold-deploy", "http-probe"}
	case StageCommit:
		return []string{"glab-mr", "jira-update"}
	default:
		return nil
	}
}

// maxRetryPerStage 获取每阶段最大重试次数，从 config 读取，默认 2。
// runDiffValidate 调用 diff-validate 工具校验 unified diff 合法性。
// 返回问题列表，空列表表示校验通过。
func runDiffValidate(diff string) []string {
	tmpFile, err := os.CreateTemp("", "ainspection-diff-*.diff")
	if err != nil {
		return []string{fmt.Sprintf("create temp file: %v", err)}
	}
	defer os.Remove(tmpFile.Name())

	if _, err := tmpFile.WriteString(diff); err != nil {
		return []string{fmt.Sprintf("write temp file: %v", err)}
	}
	tmpFile.Close()

	cmd := exec.Command("diff-validate", tmpFile.Name())
	out, err := cmd.CombinedOutput()
	if err != nil {
		// diff-validate exits 1 on validation failure — parse JSON issues
		var result struct {
			Valid  bool     `json:"valid"`
			Issues []string `json:"issues"`
		}
		if jsonErr := json.Unmarshal(out, &result); jsonErr == nil && len(result.Issues) > 0 {
			return result.Issues
		}
		// binary not found or unexpected error
		return []string{fmt.Sprintf("diff-validate: %v (output: %s)", err, strings.TrimSpace(string(out)))}
	}
	return nil
}

func (p *pipelineImpl) maxRetryPerStage() int {
	if p.pcfg != nil && p.pcfg.Retry.MaxPerStage > 0 {
		return p.pcfg.Retry.MaxPerStage
	}
	return 2
}

// executeGetWithLLM 使用 LLM Native adapter 的多轮 Chat() 流程。
// 第1轮带 tool calls → dispatch → 第2轮（带 Jira 数据）→ 返回 finalText。
func (p *pipelineImpl) executeGetWithLLM(
	ctx context.Context,
	sess *session.Session,
	llmAdapter *adapter.LLMNativeAdapter,
	agentCfg config.AgentConfig,
	sysPrompt string,
	toolDefs []skill.ToolDef,
) (string, error) {
	// 第1轮 LLM 调用（带 tools）
	req := llm.ChatRequest{
		Model:    agentCfg.Model,
		System:   sysPrompt,
		Messages: []llm.Message{{Role: "user", Content: "开始诊断"}},
		Tools:    skillToolToLLMTools(toolDefs),
	}
	resp1, err := llmAdapter.Chat(ctx, req)
	if err != nil {
		return "", fmt.Errorf("get round 1: %w", err)
	}
	if len(resp1.Choices) == 0 {
		return "", fmt.Errorf("get round 1: empty choices")
	}

	choice1 := resp1.Choices[0]
	if choice1.FinishReason != "tool_calls" || len(choice1.Message.ToolCalls) == 0 {
		return choice1.Message.Content, nil
	}

	// 分发 tool calls
	skillCalls := llmToolCallsToSkill(choice1.Message.ToolCalls)
	toolResults, dispatchErr := p.dispatcher.DispatchBatch(ctx, sess.ID, skillCalls)
	if dispatchErr != nil {
		log.Printf("[orchestrator] dispatch get skills: %v", dispatchErr)
	}

	// 第2轮 LLM 调用（带 tool results）
	messages := []llm.Message{
		{Role: "assistant", Content: choice1.Message.Content, ToolCalls: choice1.Message.ToolCalls},
	}
	messages = append(messages, skillResultsToMessages(toolResults)...)

	jiraContent := toolResultsToText(toolResults)
	sysPrompt2 := sysPrompt
	if jiraContent != "" {
		// 用 JiraContent 增强 system prompt
		sysPrompt2 += "\n\n## Jira Data\n" + jiraContent
	}

	req2 := llm.ChatRequest{
		Model:    agentCfg.Model,
		System:   sysPrompt2,
		Messages: messages,
	}
	resp2, err := llmAdapter.Chat(ctx, req2)
	if err != nil {
		log.Printf("[orchestrator] get round 2: %v, falling back to round 1", err)
		return choice1.Message.Content, nil
	}
	if len(resp2.Choices) > 0 {
		return resp2.Choices[0].Message.Content, nil
	}
	return choice1.Message.Content, nil
}

