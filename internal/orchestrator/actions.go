package orchestrator

import "git.qingteng.cn/ms/ainspection/internal/mcts"

// —————— Locate 阶段动作类型 ——————

// LocateAction 定义 locate 阶段 MCTS Rollout 中的动作类型。
type LocateAction string

const (
	LocateStaticAnalysis   LocateAction = "StaticAnalysis"   // 静态分析：代码审查、AST 遍历
	LocateDynamicProbe     LocateAction = "DynamicProbe"     // 动态探测：日志查询、metrics 拉取
	LocateHypothesisPropose LocateAction = "HypothesisPropose" // 假设提出：基于证据生成假设
	LocateUserQuery        LocateAction = "UserQuery"        // 用户介入：向用户请求确认
	LocateInfoAggregate    LocateAction = "InfoAggregate"    // 信息聚合：汇总多源信号
)

// —————— Fix 阶段动作类型 ——————

// FixAction 定义 fix 阶段 MCTS Rollout 中的动作类型。
type FixAction string

const (
	FixTemplateInstantiate FixAction = "TemplateInstantiate" // 模板实例化：基于模板生成代码
	FixMutation           FixAction = "Mutation"            // 变异：修改已有实现
	FixTestRun            FixAction = "TestRun"             // 测试执行：运行测试验证
	FixStaticCheck        FixAction = "StaticCheck"         // 静态评估：lint/build 检查
	FixUserConfirm        FixAction = "UserConfirm"         // 用户交互：请求确认方案
	FixPatchMinify        FixAction = "PatchMinify"         // 补丁简化：缩小改动范围
)

// —————— Rollout 上下文 ——————

// LocateRolloutAction locate 阶段的 Rollout 动作。
type LocateRolloutAction struct {
	Type    LocateAction      `json:"type"`
	Target  mcts.SourceContext `json:"target,omitempty"`
	Prompt  string            `json:"prompt"`  // 动作提示词
	Executed bool             `json:"executed"` // 是否已执行
}

// FixRolloutAction fix 阶段的 Rollout 动作。
type FixRolloutAction struct {
	Type    FixAction         `json:"type"`
	Target  mcts.SourceContext `json:"target,omitempty"`
	Prompt  string            `json:"prompt"`  // 动作提示词
	Test    *mcts.TestContext  `json:"test,omitempty"` // 测试上下文
	Executed bool             `json:"executed"` // 是否已执行
}

// —————— 动作生成 ——————

// DefaultLocateActions 返回 locate 阶段的默认动作池。
func DefaultLocateActions() []LocateRolloutAction {
	return []LocateRolloutAction{
		{Type: LocateStaticAnalysis, Prompt: "审查相关函数的代码逻辑，检查空指针、边界条件"},
		{Type: LocateDynamicProbe, Prompt: "查询 Loki 日志中最近的 ERROR 和 WARN"},
		{Type: LocateHypothesisPropose, Prompt: "基于已有证据提出新的根因假设"},
		{Type: LocateInfoAggregate, Prompt: "聚合静态分析和动态探测的结果"},
		{Type: LocateUserQuery, Prompt: "向用户请求确认当前证据是否指向正确方向"},
	}
}

// DefaultFixActions 返回 fix 阶段的默认动作池。
func DefaultFixActions() []FixRolloutAction {
	return []FixRolloutAction{
		{Type: FixTemplateInstantiate, Prompt: "基于修复模板生成初始 diff"},
		{Type: FixMutation, Prompt: "对已有方案进行变异，生成备选实现"},
		{Type: FixTestRun, Prompt: "运行相关单元测试验证修复"},
		{Type: FixStaticCheck, Prompt: "执行 go build 和 golangci-lint"},
		{Type: FixPatchMinify, Prompt: "检查 diff 是否可以进一步精简"},
		{Type: FixUserConfirm, Prompt: "向用户展示当前修复方案，请求确认"},
	}
}
