package skill

import (
	"context"
	"fmt"
	"slices"
	"strings"

	"git.qingteng.cn/ms/ainspection/internal/adapter"
)

// SkillPrefixAgentCmd L1 skill 调用在 agent CLI 中的前缀标识。
const SkillPrefixAgentCmd = "ainspection-skill "

// Executor tool call 执行分发器接口。
type Executor interface {
	Execute(ctx context.Context, sessionID string, call ToolCall) (ToolResult, error)
	ExecuteBatch(ctx context.Context, sessionID string, calls []ToolCall) ([]ToolResult, error)
	ResolveLayer(call ToolCall) ToolLayer
}

// SkillRunner L1 skill 的执行接口。
type SkillRunner interface {
	Run(ctx context.Context, skillName string, args map[string]any, sessionID string) (string, error)
}

// BashRunner L3 bash 的执行接口。
type BashRunner interface {
	Run(ctx context.Context, cmd string, sessionID string) (string, error)
}

// executorImpl Executor 的默认实现。
type executorImpl struct {
	skills      map[string]*Skill
	nativeTools []string
	skillRunner SkillRunner
	bashRunner  BashRunner
}

// NewExecutor 创建一个新的 Executor。
func NewExecutor(skills []*Skill, nativeTools []string, sr SkillRunner, br BashRunner) Executor {
	skillMap := make(map[string]*Skill, len(skills))
	for _, s := range skills {
		skillMap[s.Name] = s
	}
	return &executorImpl{
		skills:      skillMap,
		nativeTools: nativeTools,
		skillRunner: sr,
		bashRunner:  br,
	}
}

// INSTRUMENT: skill-executor-execute — tool call 三层分发入口 (L1 skill / L2 native / L3 bash)
// LAYER: L2
// STATUS: implemented
// Execute 执行单个 tool call。
func (e *executorImpl) Execute(ctx context.Context, sessionID string, call ToolCall) (ToolResult, error) {
	layer := e.ResolveLayer(call)
	switch layer {
	case LayerL1Skill:
		return e.executeL1(ctx, sessionID, call)
	case LayerL2Native:
		return e.executeL2(ctx, sessionID, call)
	case LayerL3Bash:
		return e.executeL3(ctx, sessionID, call)
	default:
		return ToolResult{
			ToolCallID: call.ID,
			Content:    fmt.Sprintf("unknown tool: %s (layer: %s)", call.Name, layer),
			IsError:    true,
		}, fmt.Errorf("unknown tool %q", call.Name)
	}
}

// ExecuteBatch 批量执行。
func (e *executorImpl) ExecuteBatch(ctx context.Context, sessionID string, calls []ToolCall) ([]ToolResult, error) {
	results := make([]ToolResult, len(calls))
	for i, call := range calls {
		result, err := e.Execute(ctx, sessionID, call)
		if err != nil {
			result.IsError = true
		}
		results[i] = result
	}
	return results, nil
}

// ResolveLayer 判断 tool call 的层级。
func (e *executorImpl) ResolveLayer(call ToolCall) ToolLayer {
	if call.Name == adapter.BashToolName {
		return LayerL3Bash
	}
	if strings.HasPrefix(call.Name, SkillPrefixAgentCmd) {
		return LayerL1Skill
	}
	if _, ok := e.skills[call.Name]; ok {
		return LayerL1Skill
	}
	if slices.Contains(e.nativeTools, call.Name) {
		return LayerL2Native
	}
	return LayerUnknown
}

func (e *executorImpl) executeL1(ctx context.Context, sessionID string, call ToolCall) (ToolResult, error) {
	skillName := call.Name
	if strings.HasPrefix(skillName, SkillPrefixAgentCmd) {
		parts := strings.SplitN(skillName, " ", 2)
		if len(parts) == 2 {
			skillName = parts[1]
		}
	}

	skill, ok := e.skills[skillName]
	if !ok {
		return ToolResult{
			ToolCallID: call.ID,
			Content:    fmt.Sprintf("skill %q not found", skillName),
			IsError:    true,
		}, fmt.Errorf("skill %q not found", skillName)
	}

	_ = skill.ApprovalLevel // L2/L3 审批由上层 orchestrator 处理

	output, err := e.skillRunner.Run(ctx, skillName, call.Arguments, sessionID)
	if err != nil {
		return ToolResult{
			ToolCallID: call.ID,
			Content:    fmt.Sprintf("skill %s error: %v", skillName, err),
			IsError:    true,
		}, err
	}

	return ToolResult{ToolCallID: call.ID, Content: output}, nil
}

func (e *executorImpl) executeL2(_ context.Context, _ string, call ToolCall) (ToolResult, error) {
	return ToolResult{
		ToolCallID: call.ID,
		Content:    fmt.Sprintf("native tool %s: delegated to agent platform", call.Name),
	}, nil
}

func (e *executorImpl) executeL3(ctx context.Context, sessionID string, call ToolCall) (ToolResult, error) {
	cmd, ok := call.Arguments["cmd"].(string)
	if !ok || cmd == "" {
		return ToolResult{
			ToolCallID: call.ID,
			Content:    "bash tool requires 'cmd' argument",
			IsError:    true,
		}, fmt.Errorf("bash tool missing cmd argument")
	}

	output, err := e.bashRunner.Run(ctx, cmd, sessionID)
	if err != nil {
		return ToolResult{
			ToolCallID: call.ID,
			Content:    fmt.Sprintf("bash error: %v", err),
			IsError:    true,
		}, err
	}

	return ToolResult{ToolCallID: call.ID, Content: output}, nil
}
