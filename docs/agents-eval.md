# 离线评测框架（Agent Evaluation）— L2 Prompt Smoke Test

> **版本**: 2.0 (P2-5 降级重写)
> **定位**: L2 组件回归：验证 prompt/skill 改动后模板不崩、输出 schema 不变
> **触发**: 人工 `ainspection eval`，CI 不强制

---

## 1. 设计动机与降级说明

原设计 (L3 端到端评测: GitLab MR + Jira issue → eval case → MCTS 回放 → 三维评分) 经需求预演发现多个问题导致降级为 L2。

| 问题 | 结论 |
|------|------|
| `source_gitlab.go` + `source_jira.go` 数据源耦合太重 | 不需要，eval case 手工维护 |
| MCTS 回放缺少外部信号（loki/tempo/prom/k3s 不可用） | locate 退化为纯 grep，评分无意义 |
| Pipeline.Run() 需要 MockMode/阶段子集化改造 | 不需要，不调 Pipeline |
| 三维评分中 2.5 维无效 | 降级为结构校验 |

**降级方向**: L3 (端到端) → L2 (组件回归): 验证 "prompt/skill 改动后模板不崩、输出 schema 不变"。

---

## 2. 两层验证

```
ainspection eval                 # L2a: 全量 smoke test (默认)
ainspection eval --case <id>     # L2b: 单个 case 回归对比
```

### 2.1 L2a — Smoke Test

对 16 个 `.tmpl` 文件各用一个合理的 sample data 做 Parse+Execute，验证：
- 模板语法不崩溃
- 渲染输出非空
- 渲染输出中包含预期的关键字段名

### 2.2 L2b — Case Regression

手工维护 3-5 个 case，每个 case 用真实场景数据跑 prompt 链（get→locate→plan→fix 单轮，不调 MCTS/agent），验证：
- 输出中包含期望的结构字段（key 存在）
- 输出关键字段非空
- 与 baseline 做结构 diff（非语义评分）

---

## 3. Case 存储格式

```
~/.ainspection/evals/cases/
├── error-fix-1/
│   ├── input.yaml          # get/locate/plan/fix 的标准输入数据
│   └── expected_schema.yaml # 期望的输出 schema (key + 类型，不含值)
└── perf-fix-1/
    ├── input.yaml
    └── expected_schema.yaml
```

### 3.1 input.yaml 示例

```yaml
get:
  issue_url: "https://jira.example.com/PROJ-123"
  desc: "payment-svc 偶发 P99 飙升到 500ms"
  service: "payment-svc"
  jira_content: "..."

locate:
  skills: [{name: "grep-source", description: "..."}]
  agent_config: {name: "claude", model: "opus"}

plan:
  findings:
    - hypothesis: "JSON 序列化无 sync.Pool 导致频繁分配"
      confidence: 0.85
      evidence: ["runtime.mallocgc flat 18%"]

fix:
  plan_json: '{"steps": [...]}'
  service: "payment-svc"
  repo_path: "/tmp/mock/payment-svc"
  skills: [{name: "diff-validate", description: "..."}]
```

### 3.2 expected_schema.yaml 示例

```yaml
plan_output:
  version: string
  goal: string
  steps:
    - id: string
      action: string
      target: string
      approach: string
      estimated_impact: string
      risk: string
      confidence_self: number

fix_output:
  version: string
  goal: string
  steps:
    - id: string
      strategy_type: string
      estimated_impact: string
```

---

## 4. 模块布局

```
internal/eval/
├── smoke.go      # SmokeRunner: 16 模板 sample data → 逐个 Parse+Execute
├── case.go       # CaseRunner: 加载 cases/ → 渲染 prompt 链 → 对比 schema
└── types.go      # SmokeResult, CaseResult, EvalReport

cmd/eval/cmd.go   # CLI: ainspection eval [--case <id>] [--output <path>]
```

---

## 5. CLI

```
ainspection eval
  --case <id>      指定 case 名称 (默认跑全量 smoke + 全部 case)
  --output <path>  报告输出路径 (默认 stdout)
  --baseline       保存当前渲染输出为 baseline (人工确认后)
```

### 5.1 输出示例

```
$ ainspection eval

=== Prompt Smoke Test (16 templates) ===
  get-system.tmpl        PASS  (3287 chars, fields: IssueURL/Desc/Service/JiraContent)
  locate-system.tmpl     PASS  (4512 chars, fields: InputYAML/ParentSummary/Service)
  ...
  16/16 PASSED  0 FAILED

=== Case Regression (2 cases) ===
  error-fix-1     PASS  (get schema ok, locate schema ok, plan schema ok, fix schema ok)
  perf-fix-1      PASS  (get schema ok, locate schema ok, plan schema ok, fix schema ok)

  2/2 PASSED  0 FAILED

Total: 18/18 tests passed
```

---

## 6. 不修改的文件

- `Pipeline.Run()` — 不碰，不需要 MockMode/阶段子集化
- `internal/orchestrator/` — 不碰
- `internal/session/` — 不碰
- `internal/prompt/renderer.go` — 不碰（已有 Input 类型均可直接用于 smoke test）
- 现有 prompt/skill 文件内容逻辑 — 不碰（仅修复字段名对齐模板与 Go struct）

---

## 7. 实现状态

| 编号 | 任务 | 工作量 | 状态 |
|------|------|-------|------|
| P2-5 | L2 离线评测 (smoke + case regression) | S | completed |
