# Planner Agent 设计

> Planner 是独立 Agent + 独立 session，在 locate Evaluator review #1 通过后由 orchestrator 自动启动，把 locate 的 findings 转换为结构化 plan.json，供 fix 阶段消费。
> **权威规范见** [`../AGENTS.md`](../AGENTS.md) §1 架构图、§4 工作流。

---

## 1. 设计动机

把"定位根因"和"制定修复计划"分离到两个独立 session：

- locate Generator 专注证据收集 + 假设确认（在熟悉的对话上下文中迭代）
- Planner 在 fresh context 中只看 finalized findings，把它们结构化为可执行 plan.json
- 解耦 locate 的"发散探索"与 plan 的"收敛规划"，避免 plan 在 locate 长上下文中被冗余信息污染
- Planner 模型差异化：默认走 Opus（复杂推理）；Generator fix 走 Sonnet（速度优先），各自最优

---

## 2. 触发与生命周期

### 2.1 触发时机

```
locate Generator 输出 findings
        │
        ▼
  Evaluator Review #1
        │
   passed? ──否→ 退回 locate（重试 ≤ 2 次）
        │
       是
        ▼
  context.yaml.status = expectation_locked
        │
        ▼
  orchestrator → sessionMgr.Spawn(currentSession, PlannerInput{...})
        │
        ▼
  Planner 独立 session 在子 working dir 启动 LLM
        │
        ▼
  输出 plan.json → 写入当前 node 的 output.yaml.plan
        │
        ▼
  Planner session 结束（Spawn 子 session 完成）
        │
        ▼
  context.yaml.status = fixing
```

### 2.2 与 fix 衔接

- Planner 完成后 plan.json 已经在 output.yaml.plan
- fix Generator 启动时直接读 plan.json 的 steps，不再重新规划
- fix MCTS 在 plan.steps 上展开候选 diff（详见 [`agents-mcts.md`](agents-mcts.md) §4）

---

## 3. 接口

### 3.1 模块布局

```
internal/planner/
├── planner.go        # 入口：Plan(input PlannerInput) (PlannerOutput, error)
├── prompt.go         # 装载 prompts/plan-system.tmpl + 渲染上下文
└── validate.go       # 校验 plan.json 是否符合 JSON Schema
```

### 3.2 输入 / 输出

```go
type PlannerInput struct {
    Findings        []Finding        // locate 阶段输出（已 review 通过）
    DiscardedHyps   []DiscardedHypothesis // 已排除假设（避免 plan 重提）
    ParentSummary   string           // 顶层 input.yaml + 父 summary
    UserDirectives  []string         // 用户在 locate disclose 阶段补充的指令
    AvailableSkills []*skill.Skill   // 当前 agent 的可用 skill（影响 plan.step 写法）
}

type PlannerOutput struct {
    Plan         PlanJSON              // 详见 task-context.md §5
    Confidence   float64               // Planner 自评（双源校准源之一）
}
```

### 3.3 PlanJSON Schema

复用 [`task-context.md`](task-context.md) §5.1 的 `output.yaml.plan` schema，含字段：

- `version` / `goal` / `steps` / `alternatives` / `pre_checklist` / `post_checklist`
- 每个 step 含 `id` / `action` / `target` / `approach` / `estimated_impact` / `risk` / `rollback` / `confidence_self`

---

## 4. 模型差异化

| 字段 | 默认值 | 说明 |
|------|--------|------|
| `config.planner.agent` | `claude` | 默认 Opus 模型，结构化推理 |
| `config.planner.trigger` | `after_review_locate` | locate review 通过后自动 |
| `config.planner.enabled` | `true` | false 时 fix Generator 自己 plan（兜底） |

**模型可换**：把 `agent` 改为 `claude_sonnet` 或 `qwen` 即可降级。

---

## 5. Prompt 模板

`prompts/plan-system.tmpl`（frontmatter 含 `version: "1.0"`）模板变量：

```text
你是 ainspection Planner Agent，将已 review 通过的根因转换为可执行 plan.json。

【输入】
顶层问题: {{.ParentSummary}}
已确认 findings:
  {{range .Findings}}
  - {{.Hypothesis}} (confidence: {{.ConfidenceFinal}})
    evidence: {{.Evidence}}
  {{end}}
已排除假设（不再考虑）:
  {{range .DiscardedHyps}}
  - {{.Hypothesis}} ({{.EvidenceAgainst}})
  {{end}}
用户指令: {{.UserDirectives}}
可用 skill: {{.AvailableSkills | names}}

【输出要求】
返回 JSON Schema 严格匹配的 plan：
- goal: 一句话总目标
- steps: 至少 1 个，每个含 id/action/target/approach/estimated_impact/risk/rollback/confidence_self
- alternatives: 至少 1 个被你考虑过但 discarded 的方案，附 tradeoff
- pre_checklist / post_checklist: 各 ≥ 1 条

【约束】
- 不引入 findings 之外的根因
- 仅使用 AvailableSkills 中存在的工具
- 每个 step 的 target 路径必须在 services[].repo_path 内（FSGuard 会校验）
```

---

## 6. 与 Evaluator / MCTS 的关系

- Planner **不参与** locate 阶段的 MCTS（locate MCTS 在 Generator session 中跑完）
- Planner **的 plan 输出** 会作为 fix MCTS 的根节点（`fix_node.PlanStepNode` 的 `Step` 字段）
- Planner 自评的 `confidence_self` 进入 review_report，与 Evaluator 校准（详见 [`agents-evaluator.md`](agents-evaluator.md) §2.5）

---

## 7. 失败处理

| 失败情况 | 行为 |
|----------|------|
| LLM 返回非 JSON 或 schema 不合 | 重试 2 次（temp+0.1），仍失败 → 降级 fallback_agent |
| schema 通过但 steps 为空 | 退回 locate，要求补充 findings |
| Planner session 超 token budget | 截断输出，标记 `partial=true`，进入 fix 仍按部分 plan 执行；Evaluator review #2 必须强标 |

---

## 8. 实现里程碑

详见 [`design.md`](design.md) P1-3 与 [`todo.md`](todo.md) P1-3。

| 编号 | 任务 | 工作量 |
|------|------|-------|
| PL-1 | `internal/planner/{planner,prompt,validate}.go` 骨架 | S |
| PL-2 | `prompts/plan-system.tmpl` + JSON Schema 校验 | S |
| PL-3 | `internal/orchestrator` 接入触发点 | S |
| PL-4 | confidence 双源校准接入 | S |
