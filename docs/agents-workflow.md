# 工作流详细规范

> 整合基线验证、review 阶段、重试上限、Linter 反馈的完整工作流定义。
> **权威规范和架构见** [`../AGENTS.md`](../AGENTS.md) 第 3-4 章。

---

## 1. Session 启动协议

每个 Agent session 启动时执行三段式协议（Orient → Setup → Verify Baseline）：

### 1.1 Orient（定向）

加载当前任务上下文：
- `context.yaml` — 当前树指针和任务状态
- 目标节点的 `input.yaml` — 问题域、用户指令、证据引用
- 父节点的 `summary.md`（如非 root 节点）

### 1.2 Setup（就绪检查）

CLI 初始化：
- 验证 kubectl/skaffold/glab 工具可用
- 注入 kubeconfig
- 加载 agent 对应的 endpoint/api_key/model 配置
- 注入 Skill Hook（将 `.skills/` 转为 tool 描述）

### 1.3 Verify Baseline（基线验证）

```
每个 session 启动时强制执行：
  1. go build ./...        → 确认编译基线绿色
  2. go vet ./...          → 确认静态分析基线绿色
  3. (可选) go test ./...  → 确认测试基线绿色（fix/verify 阶段必须）

任一步骤失败：
  → session 标记为 blocked
  → 在 investigation.log.md 记录失败详情
  → 通知用户"基线异常，请人工介入"
  → 不继续执行后续阶段
```

**设计目的**：防止 Agent 在已损坏的代码上继续修改，遵循"Verify before building"原则。

---

## 2. 阶段门控与工作流

### 2.1 完整流水线

```
Session 启动 (Orient → Setup → Verify Baseline)
      │
      ▼
   get ──→ scope_defined
      │
      ▼
  locate ──→ expectation_locked
      │
      ▼
  [Evaluator Review #1] ──→ review #1 passed
      │
      ▼
   fix ──→ diff-validate + go build 通过
      │
      ▼
  [Evaluator Review #2] (主 review) ──→ review #2 passed
      │
      ▼
  verify ──→ golangci-lint + go build + skaffold deploy + 接口验证
      │
      ▼
  [Evaluator Review #3] ──→ review #3 passed
      │
      ▼
  commit ──→ MR created
```

### 2.2 门控定义

| 门控 | 检查条件 | 不满足时 |
|------|---------|---------|
| `get→locate` | `context.yaml.status == scope_defined` | 提示用户提供问题描述 |
| `locate→review#1` | `expectation_locked == true` | 拒绝进入 review，等待用户确认 |
| `review#1→fix` | Evaluator 审查通过 | 退回 locate 补充证据（最多 2 次） |
| `fix→review#2` | patch 已应用 + `go build` 通过 + diff-validate 通过 | 回退到 fix 修正 |
| `review#2→verify` | Evaluator 审查通过（主 review） | 退回 fix 修正（最多 2 次） |
| `verify→review#3` | 接口 2xx + linter 通过 + 无新错误日志 | 提示用户手动处理 |
| `review#3→commit` | Evaluator 审查验证结果通过 | 退回 verify 补充验证（最多 2 次） |
| `commit→done` | MR 创建成功 | 保留现场供用户手动完成 |

---

## 3. 任务级重试与回退

### 3.1 重试计数器

```yaml
# 在 context.yaml 中跟踪
retry_count:
  locate: 0
  fix: 0
  verify: 0
  max_per_stage: 2
```

### 3.2 重试触发条件

| 触发条件 | 计数阶段 | 行为 |
|---------|---------|------|
| `go build` 失败 | fix | 回退 patch → Agent 修正 → retry_count.fix++ |
| `go vet` 失败 | fix | 同上 |
| Evaluator review 不通过 | locate / fix / verify | 退回上一阶段 → 对应 retry_count++ |
| `skaffold deploy` 失败 | verify | 输出错误 → Agent 分析 → retry_count.verify++ |
| 接口验证失败（非 2xx） | verify | 同上 |

### 3.3 超限处理

```
retry_count[stage] >= max_per_stage (2):
  → 当前阶段标记为 blocked
  → 状态写入 context.yaml
  → 通知用户"<stage> 阶段重试 2 次仍失败，请人工介入"
  → 附完整的审查历史和错误日志
  → 保留所有中间产物（diff、build log、deploy log）供排查
```

### 3.4 LLM 无效输出重试（独立计数）

与任务级重试分离，LLM 返回无效输出（JSON 解析失败、格式不符合 schema）重试 2 次后降级到备选 agent：

```
LLM 返回无效输出 → 重试 1（同 agent，temperature+0.1）
              → 仍然无效？→ 重试 2（降级到 config.retry.fallback_agent）
              → 仍然无效？→ session 标记为 blocked，转人工
```

**降级到备选 agent 时携带的上下文（D4 冷启动 + 错误提示）**：

```
新 agent 收到的输入：
  ├── input.yaml（问题域 + 用户指令）
  ├── 父节点 summary.md
  ├── 当前阶段 prompt 模板
  ├── 当前 agent 的 tool/skill 描述
  └── ⚠️ 上一 agent 的错误输出 + invalid_reason（避免重蹈覆辙）

明确不携带：
  ✗ 上一 agent 的对话历史（避免 token 翻倍）
  ✗ 上一 agent 的 MCTS 内部树
```

错误提示格式：

```yaml
fallback_context:
  previous_agent: claude
  previous_output: <last invalid response truncated to 1KB>
  invalid_reason: "expected JSON object, got malformed yaml at line 5"
  retry_count: 2
```

---

## 4. Linter 反馈循环

### 4.1 集成位置

Linter 在两个位置接入 Agent 反馈循环：

```
fix 阶段（patch 应用后）：
  go build → go vet → golangci-lint → 输出写入 context_lint.txt
  → Agent 读取 context_lint.txt，自主修复风格问题
  → 修复后重新 go build + golangci-lint
  → 最多 2 轮 lint-fix 循环

verify 阶段（部署验证前）：
  golangci-lint run --new-from-rev=HEAD~1
  → 只检查新增代码
  → 有 warning → Agent 自动修复
  → 有 error → 阻断 verify，回退 fix
```

### 4.2 Linter 配置

复用 qtmf 仓库的 `.golangci.yaml`（路径：`/data/src_repo/qtmf/.golangci.yaml`），增加 ainspection 专项规则：

```yaml
# ainspection 专项 linter 规则（追加到 .golangci.yaml）
linters-settings:
  # 禁止使用 unsafe 包
  depguard:
    packages:
      - "unsafe"
  # 强制 error 检查
  errcheck:
    check-blank: true
```

### 4.3 Linter 输出作为 Agent 上下文

```text
## Linter 反馈

以下问题由 golangci-lint 检出，请逐项修复：

1. internal/handler/order.go:45 — errcheck: Error return value of `json.NewEncoder` is not checked
2. internal/handler/order.go:78 — gofumpt: File is not `gofumpt`-ed

修复后运行: go build ./... && golangci-lint run
```

---

## 5. Context Reset 协议

### 5.1 主动重置

与"被动阈值触发（~40%）"互补，每个节点完成后**主动重置**：

```
节点标记为 completed
      │
      ▼
生成 summary.md（≤500 字符）
      │
      ▼
结束当前 session（无论上下文使用率是否达阈值）
      │
      ▼
新 session 从以下内容冷启动：
  - input.yaml（问题域 + 用户指令）
  - 父节点 summary.md
  - 当前阶段 prompt 模板
  ≠ 之前 session 的完整对话历史
```

### 5.2 两种 Context Reset 策略对比

| 策略 | 触发条件 | 适用场景 |
|------|---------|---------|
| **被动（阈值）** | 上下文使用率达 ~40% | locate 阶段的长对话（多轮信号采集） |
| **主动（阶段完成）** | 节点 status=completed | get→locate、locate→fix、fix→verify 的阶段切换 |

### 5.3 冷启动时的上下文传递

```
子 session 收到的唯一上下文：
┌──────────────────────────────────────────┐
│ 1. 当前阶段 prompt 模板                  │
│ 2. 父节点 summary.md（格式化摘要）        │
│ 3. input.yaml（问题域 + 用户指令）         │
│ 4. 关联证据文件的路径引用（非内容）        │
│ 5. 当前 agent 的 tool/skill 描述         │
└──────────────────────────────────────────┘

明确不传递：
- 父 session 的原始对话轮次
- 父 session 的 MCTS 内部树（`internal/mcts/` 临时数据）
- 原始日志/指标全文
```

---

## 6. 结构化计划工件

### 6.1 output.yaml 的 next_actions JSON Schema

```yaml
# output.yaml (增强版)
node_id: "n1-locate-logs"
findings:
  - hypothesis: "JWT token 过期未刷新导致 401"
    confidence: 0.82
    evidence: ["logs/401-burst.txt:15-23"]
    status: confirmed
discarded_hypotheses:
  - hypothesis: "数据库连接池耗尽"
    evidence_against: "metrics/db-pool.txt 显示连接数正常"
    status: discarded
plan:                          # 新增：结构化计划（JSON Schema）
  version: "1.0"
  goal: "修复 payment-svc 慢查询导致 upstream 超时"
  steps:
    - id: "step-1"
      action: "添加联合索引"
      target: "migration/xxx_add_index.sql"
      approach: "CREATE INDEX idx_payments_status_created ON payments(status, created_at)"
      estimated_impact: "P99 延迟从 2.5s 降至 <200ms"
      risk: "low"
      rollback: "DROP INDEX idx_payments_status_created"
    - id: "step-2"
      action: "验证索引有效性"
      target: "EXPLAIN SELECT * FROM payments WHERE status=... AND created_at>..."
      approach: "确认索引被使用"
      risk: "low"
  alternatives:
    - approach: "调整 order-svc 超时时间"
      tradeoff: "治标不治本"
      discarded: true
  pre_checklist:               # 新增：执行前检查清单
    - "确认 status 列区分度 > 0.1"
    - "确认 migration 文件名格式 <version>_<desc>.sql"
  post_checklist:              # 新增：完成后验证清单
    - "go build 通过"
    - "EXPLAIN 确认索引使用"
```

### 6.2 设计目的

- **机器可解析**：JSON Schema 让后续 Agent 无需解析 Markdown
- **跨 session 可靠传递**：结构化数据在 Context Reset 中无损传输
- **可审计**：plan / pre_checklist / post_checklist 形成完整的可追溯链条

### 6.3 alternatives / discarded_hypotheses 产生流程（D3）

**LLM 初始 + 用户补充**：

1. **LLM 初始**：locate 阶段每轮 Disclose 必须输出：
   - `findings` ≥ 3 个（多方向假设）
   - `discarded_hypotheses` ≥ 1 个（带 evidence_against 排除证据）
   - 每个 finding 带 `confidence_self` ∈ [0, 1]

2. **用户补充**：用户可在交互式调查时执行：
   ```
   ainspection alternatives add --hypothesis "<text>" --confidence 0.4
   ainspection alternatives discard --id <fid> --reason "<text>"
   ```
   CLI 直接编辑 `output.yaml` 中的 `findings` / `discarded_hypotheses`，**不重新启 LLM**。

3. **MCTS 整合**：所有未 discarded 的 finding 进入 locate MCTS 的 root children；discarded 的不参与搜索但保留在 output 中作为审计证据。

---

## 7. confidence 双源校准（B5）

每个 finding/plan.step 同时记录：

| 字段 | 产生时机 | 产生者 |
|------|---------|--------|
| `confidence_self` | LLM 输出 finding/plan.step 时 | Generator |
| `confidence_evaluator` | review 阶段重评 | Evaluator（独立 session） |
| `confidence_final` | review 完成 | `min(self, evaluator)` |

**发散处理**：

- `|self - evaluator| > 0.2` → `review_report.warnings` 记一条 "confidence diverged"
- `|self - evaluator| > 0.4` 且 `passed=true` → 强制 `passed=false`，必须人工介入

**对 MCTS 的影响**：MCTS Backprop 阶段使用 `confidence_final` 而非 `confidence_self`，避免 Generator 自评偏差污染搜索树。

---

## 8. 与项目研发流程的关系 (v2)

ainspection 运行时的工作流（本文档描述）与 ainspection 自身 AI 研发流程（见 `AGENTS.md §6`）是两个层面：

| 层面 | 跟踪文件 | 用途 |
|------|---------|------|
| ainspection 运行时 | `~/.ainspection/tasks/<id>/context.yaml` | 单个修复任务的阶段状态 |
| ainspection 项目研发 | `<repo>/progress.json` | 所有开发任务的进度 + 限流 + conformance |

### 8.1 限流对抗

`progress.json` 中维护每个 agent 的 rate limit 状态。当使用 AI 开发 ainspection 自身时，如果某个 agent 的 remaining token 不足：

1. **退避** — 等待 rate limit reset 后自动重试
2. **切换** — 切换到 `fallback_agent`（config.yaml retry.fallback_agent）
3. **降级** — 仅执行核心步骤，跳过非必要的 LLM 调用

### 8.2 Conformance Check

每次 AI 开发任务完成后，自动执行（详见 `AGENTS.md §6.2`）：
- 规范偏离检测：代码实现 vs AGENTS.md / docs/ 设计
- 职责偏移检测：是否偏离核心问题解决流程
- 插桩完整性：关键决策点是否有标记（详见 §8.4）

不直接介入 ainspection 运行时工作流，但阻断不合规的代码合入。

### 8.3 Conformance Check 执行步骤

**谁来执行**: AI agent 在任务完成后，按 AGENTS.md §6.2 规范手动验证。未来的目标是将检查脚本化。

**执行步骤**:

1. **规范偏离检查** — 对照 AGENTS.md 和 docs/ 中的设计，检查本次修改的代码是否与设计一致
   - 新增模块是否在 docs/ 中有对应的设计文档？
   - 接口签名是否与 design.md 中定义的一致？
   - 模块依赖关系是否与架构图一致？

2. **职责偏移检查** — 新增代码是否在 ainspection 核心职责范围内？
   - 检查点：是否引入了不相关的业务逻辑？是否在非职责范围内做了过度抽象？

3. **插桩完整性检查** — 关键决策点是否有标记？（详见 §8.4）

**结果记录**: 更新 `progress.json` 的 `conformance` 字段，通过或记录 violation。通过则更新 `docs/BASELINE.md`。

**plan 模式交叉引用**: 如果任务涉及 plan 模式设计的方案，需确认：
1. plan 文件中的设计方案已全部实现或被显式放弃
2. 方案中涉及的文档交叉引用已更新
3. `progress.json` 中任务状态与 plan 文件一致

### 8.4 插桩标记规范

**目的**: 解决跨 agent、跨 session 开发时的信息丢失问题。不同 agent 有不同的文件查找和代码生成偏好，低层级未实现的函数可能被不同 agent 以不同方式处理（注释占位/stub/跳过），导致切换 agent 后遗漏。

**格式**: Go 注释标记，统一格式：

```go
// INSTRUMENT: <checkpoint-id> — <decision-description>
// LAYER: <layer>
// STATUS: stub | placeholder | implemented | verified
```

**层级定义**:

| LAYER | 含义 | 示例 |
|-------|------|------|
| L0 | 接口/抽象层 | `AgentAdapter` 接口定义 |
| L1 | 编排/流程层 | Pipeline 阶段调度 |
| L2 | 具体实现层 | Claude CLI adapter 的 `Setup()` |
| L3 | 工具/辅助层 | `diff-validate` 工具 |

**示例**:

```go
// INSTRUMENT: mcts-locate-expander — MCTS locate 阶段 expand 节点时调用 LLM
// LAYER: L1
// STATUS: placeholder
func (e *mctsExpander) Expand(ctx context.Context, parent *node) ([]*node, error) {
    // TODO(V2-6): 接入 adapter.Run() 替换当前的规则式实现
    return e.ruleBasedExpand(parent), nil
}
```

**跨 agent 兼容性**:
- 标记使用注释格式，不依赖特定 agent 的查找能力
- `LAYER` 字段让 agent 快速定位修改范围
- `STATUS` 字段让 agent 了解当前实现状态，避免重复造轮子或误删 stub

**与 Conformance Check 的关系**:
- Conformance Check 第 3 步「插桩完整性检查」扫描所有 `.go` 文件中 `INSTRUMENT:` 标记
- `STATUS: placeholder` 或 `STATUS: stub` 的标记被记录但不算 violation
- 缺失应有标记的关键决策点（MCTS 分支、Evaluator 评分、阶段切换）记录 violation