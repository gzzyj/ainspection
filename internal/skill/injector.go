package skill

import (
	"fmt"
	"strings"

	"git.qingteng.cn/ms/ainspection/internal/adapter"
)

// Injector Skill → ToolDef 装配接口。
//
// 通过 adapter.Registry 获取对应 AgentAdapter，若 adapter 实现
// SkillInjector 则委托注入。同时合并 L1+L2+L3 三层工具生成 ToolDef。
type Injector interface {
	// Inject 返回适配后的 tools 列表 + 拼接后的 skill body 文本。
	// agentType: "claude_cli" | "kimi_cli" | "codex_cli" | "qwen_cli" | "gemini_cli" | "llm_native"
	// nativeToolNames: config.agents.<name>.native_tools 的字符串列表
	Inject(agentType string, skills []*Skill, nativeToolNames []string) ([]ToolDef, string, error)
}

type injectorImpl struct {
	registry *adapter.Registry
}

// NewInjector 创建 Injector 实例。
func NewInjector(registry *adapter.Registry) Injector {
	return &injectorImpl{registry: registry}
}

// Inject 装配三层工具并返回适配后的 ToolDef 列表 + body 文本。
func (in *injectorImpl) Inject(agentType string, skills []*Skill, nativeToolNames []string) ([]ToolDef, string, error) {
	if agentType == "" {
		return nil, "", fmt.Errorf("agent type 为必填项")
	}

	// 转换 skill.Skill → adapter.SkillDef
	skillDefs := skillsToAdapterDefs(skills)

	// 若 registry 可用且 adapter 实现了 SkillInjector，委托注入
	if in.registry != nil {
		at := adapter.ResolveAgentType(agentType)
		if at != "" {
			if a, err := in.registry.Get(at); err == nil {
				if si, ok := a.(adapter.SkillInjector); ok {
					// 委托给 adapter 的 SkillInjector 注入 skill 文件
					// （沙箱路径由调用方在调用 InjectSkills 前确定）
					_ = si // 实际注入在 Setup 阶段完成，此处留作接口校验
				}
			}
		}
	}

	// 构建 L1 + L2 + L3 ToolDef（用于 LLM Native 等需要 tool schema 的场景）
	tools := in.buildToolDefs(skillDefs, nativeToolNames, agentType)

	// 拼接 skill body 文本
	l1Body := buildSkillBody(skills)

	return tools, l1Body, nil
}

// buildToolDefs 构建 L1+L2+L3 的 ToolDef 列表（通用）。
func (in *injectorImpl) buildToolDefs(skillDefs []adapter.SkillDef, nativeNames []string, _ string) []ToolDef {
	var tools []ToolDef

	// L1: 业务 skills
	tools = append(tools, l1SkillsToToolDefs(skillDefs)...)

	// L2: 平台原生工具
	tools = append(tools, l2NativeToToolDefs(nativeNames)...)

	// L3: 内置 bash
	tools = append(tools, l3BashToToolDef())

	return tools
}

// l1SkillsToToolDefs 将 L1 skill 转为通用 ToolDef。
func l1SkillsToToolDefs(defs []adapter.SkillDef) []ToolDef {
	var tools []ToolDef
	for _, s := range defs {
		tools = append(tools, ToolDef{
			Name:        s.Name,
			Description: s.Description,
			Schema:      adapter.ParamToJSONSchema(s.Parameters),
		})
	}
	return tools
}

// l2NativeToToolDefs 将 L2 native tool names 转为通用 ToolDef。
func l2NativeToToolDefs(names []string) []ToolDef {
	var tools []ToolDef
	for _, name := range names {
		def := adapter.ResolveNativeTool(name)
		if def == nil {
			continue
		}
		tools = append(tools, ToolDef{
			Name:        def.Name,
			Description: def.Description,
			Schema: map[string]any{
				"type":       "object",
				"properties": map[string]any{},
			},
		})
	}
	return tools
}

// l3BashToToolDef 返回 L3 bash 工具的通用 ToolDef。
func l3BashToToolDef() ToolDef {
	return ToolDef{
		Name:        adapter.BashToolName,
		Description: adapter.BashToolDescription,
		Schema:      adapter.BashParamSchema,
	}
}

// skillsToAdapterDefs 将 []*Skill 转为 []adapter.SkillDef。
func skillsToAdapterDefs(skills []*Skill) []adapter.SkillDef {
	defs := make([]adapter.SkillDef, 0, len(skills))
	for _, s := range skills {
		defs = append(defs, adapter.SkillDef{
			Name:          s.Name,
			Description:   s.Description,
			Body:          s.Body,
			Parameters:    paramsToAdapterParams(s.Parameters),
			InjectionMode: s.InjectionMode,
		})
	}
	return defs
}

// paramsToAdapterParams 将 []Parameter 转为 []adapter.SkillParam。
func paramsToAdapterParams(params []Parameter) []adapter.SkillParam {
	result := make([]adapter.SkillParam, len(params))
	for i, p := range params {
		result[i] = adapter.SkillParam{
			Name:        p.Name,
			Type:        p.Type,
			Required:    p.Required,
			Description: p.Description,
			Enum:        p.Enum,
			Default:     p.Default,
			Pattern:     p.Pattern,
		}
	}
	return result
}

// buildSkillBody 拼接所有 L1 skill 的 body 文本。
func buildSkillBody(skills []*Skill) string {
	var bodies []string
	for _, s := range skills {
		if s.Body != "" {
			bodies = append(bodies, s.Body)
		}
	}
	return strings.Join(bodies, "\n\n")
}
