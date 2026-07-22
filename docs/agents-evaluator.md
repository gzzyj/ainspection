# Evaluator Agent 设计

> 定义独立 Evaluator Agent 的接口、review 阶段设计、模型差异化策略和评分维度。
> **权威规范见** [`../AGENTS.md`](../AGENTS.md) 第 3 章。

---

## 1. 设计动机

### 1.1 Harness 核心原则

> "Agents grade their own work too generously. A skeptical evaluator in a fresh context provides honest feedback."
> — Anthropic Harness Design

当前 ainspection 的双阶段 MCTS（locate 假设 + fix 修复方案，详见 [`agents-mcts.md`](agents-mcts.md)）依赖 Simulation 步骤打分。如果由 Generator Agent **运行时自评**，会引入"自我评估偏差"。Evaluator 在 fresh context 中独立打分，是 MCTS Simulation 评分的唯一来源（见 §6）。

### 1.2 评估经典分离架构

```
                     ┌─────────────┐
   input.yaml ──────→│   Planner   │──────→ plan.json (结构化计划)
                     └─────────────┘
                            │
                     ┌──────┴──────┐
                     ▼             ▼
              ┌───────────┐  ┌───────────┐
              │ Generator │  │ Evaluator │  ← 独立 session + 可选不同模型
              │  (Claude) │  │ (Sonnet)  │
              └───────────┘  └───────────┘
                     │             │
                     ▼             ▼
                  patches/    review-report.yaml
```

## 2. Evaluator Agent 接口

### 2.1 触发时机

| 阶段 | Evaluator 职责 |
|------|---------------|
| locate → fix 之间 | 审查根因定位是否证据充分、假设排除是否完整 |
| fix → verify 之间 | **主 review**：审查 diff 正确性、代码风格、架构合规、安全风险 |
| verify → commit 之间 | 审查验证结果是否充分（信号是否恢复正常） |

### 2.2 接口定义

```go
// internal/orchestrator/evaluator.go
type Evaluator interface {
    // ReviewFinding 审查 locate 阶段的根因定位
    ReviewFinding(ctx context.Context, node *tree.Node) (*ReviewReport, error)

    // ReviewFix 审查 fix 阶段的 diff
    ReviewFix(ctx context.Context, node *tree.Node, patches []Patch) (*ReviewReport, error)

    // ReviewVerification 审查 verify 阶段的验证结果
    ReviewVerification(ctx context.Context, node *tree.Node, report *VerifyReport) (*ReviewReport, error)
}

type ReviewReport struct {
    Passed      bool              // 是否通过审查
    Score       int               // 1-10 综合评分
    Dimensions  []ReviewDimension // 分维度评价
    Blockers    []string          // 阻断项（必须修复）
    Warnings    []string          // 建议项（可选修复）
    Confidence  float64           // Evaluator 自身置信度
}

type ReviewDimension struct {
    Name    string  // 维度名称
    Score   int     // 1-10
    Comment string  // 评语
}
```

### 2.3 审查维度（双空间分套：locate 假设 vs fix 修复方案）

**locate 阶段**（评估 hypothesis / finding）

| 维度 | 评估问题 | 权重 |
|------|---------|------|
| **根因正确性** | 定位的根因是否有足够证据支撑？ | 0.35 |
| **证据充分度** | evidence 是否多源交叉验证（log + trace + metric）？ | 0.25 |
| **可验证性** | 假设是否能通过具体动作验证？ | 0.20 |
| **影响面** | 该根因解释了多少观测异常？ | 0.20 |

**fix 阶段**（评估 plan.step / diff）

| 维度 | 评估问题 | 权重 |
|------|---------|------|
| **修复完备性** | diff 是否解决了根因，有无副作用？ | 0.35 |
| **代码质量** | 是否符合 gofumpt 规范、命名清晰、无重复？ | 0.25 |
| **架构合规** | 是否破坏现有接口、引入循环依赖？ | 0.20 |
| **安全性** | 是否引入 SQL 注入、敏感信息泄露等风险？ | 0.20 |

两套维度同样喂给 MCTS Simulation 作为评分（见 §6）。

### 2.4 审查报告格式

```yaml
# output.yaml (review 阶段写入)
review_report:
  passed: false
  score: 6
  dimensions:
    - name: "根因正确性"
      score: 8
      comment: "payment-svc 慢查询定位准确，trace 证据充分"
    - name: "修复完备性"
      score: 7
      comment: "索引覆盖 status+created_at，但未考虑已有索引的冲突"
    - name: "代码质量"
      score: 5
      comment: "migration 文件名不符合 `<version>_<desc>.sql` 规范"
    - name: "架构合规"
      score: 8
      comment: "纯 SQL 变更，无破坏性"
    - name: "安全性"
      score: 9
      comment: "无安全风险"
  blockers:
    - "migration 文件名需改为 002_add_payment_idx.sql"
  warnings:
    - "建议确认 status 列区分度足够高后再建索引"
  confidence_self: 0.92          # Generator 自评（来自 findings/plan.step）
  confidence_evaluator: 0.78     # Evaluator 重评
  confidence_diff: 0.14          # 发散度
  confidence_final: 0.78         # 取低（双源一致原则）
```

### 2.5 confidence 双源校准

每个 finding/plan.step 同时记录三个 confidence：

- `confidence_self`：Generator 输出 finding/plan.step 时自带
- `confidence_evaluator`：Evaluator 在 review 阶段重评（同一对象、独立 context）
- `confidence_final`：取 `min(self, evaluator)` 写入 output.yaml.plan

**发散阈值**：`confidence_diff = |self - evaluator|`。

- `diff > config.evaluator.confidence_divergence_threshold`（默认 0.2）→ 在 `review_report.warnings` 记一条 "confidence diverged: self=… evaluator=…"
- `diff > 0.4` 且 `passed=true` 时 → 强制 `passed=false`，必须人工介入

---

## 3. 模型差异化策略

| Agent 角色 | 推荐模型 | session 形态 | 原因 |
|-----------|---------|---------------|------|
| **Planner** | Claude Opus | **独立 Agent + 独立 session**（locate review #1 通过后由 orchestrator 通过 `sessionMgr.Spawn` 启动） | 复杂推理，结构化计划生成；详见 [`agents-planner.md`](agents-planner.md) |
| **Generator (locate)** | Claude Opus | 主 session | 多信号源交叉验证，需要深度推理 |
| **Generator (fix)** | Claude Sonnet | 主 session | 日常修复，速度快，成本低 |
| **Evaluator** | Claude Sonnet | **独立 session**（clean context） | 新鲜视角，质疑式审查 |
| **Evaluator (安全)** | 轻量模型 + 规则引擎 | 子任务 | 模式匹配安全扫描，无需强推理 |

### 3.1 Evaluator session 隔离

```
Evaluator session 收到的输入：
  ├── input.yaml（问题域描述）
  ├── 待审查的 output.yaml（findings + next_actions）
  ├── patches/*.diff（fix 审查时）
  ├── 代码审查 rubric（固定 prompt：review-locate / review-fix / review-verify）
  │
  ≠ Generator session 的对话历史
  ≠ Generator 的 MCTS 内部树（internal/mcts 临时数据）
```

**设计目的**：Evaluator 不受 Generator 推理过程的锚定效应影响，完全基于输出产物独立判断。

---

## 4. Review 阶段工作流

### 4.1 在 pipeline 中的位置

```
locate ──→ Evaluator Review #1 ──→ fix
                 │
          passed? ──否→ 返回 locate 补充证据
                 │
                是
                 ▼
               fix ──→ Evaluator Review #2 (主 review) ──→ verify
                              │
                       passed? ──否→ 返回 fix 修正
                              │
                             是
                              ▼
                           verify ──→ Evaluator Review #3 ──→ commit
                                              │
                                       passed? ──否→ 返回 verify 补充验证
                                              │
                                             是
                                              ▼
                                           commit
```

### 4.2 门控规则

| 审查结果 | 行为 |
|---------|------|
| `passed=true, score≥7` | 进入下一阶段 |
| `passed=false` 有 blockers | 退回上一阶段，附 Evaluator 报告 |
| `passed=true, score<7` | 进入下一阶段，但 warnings 记入 investigation.log.md |
| 同一阶段 2 次 review 不通过 | 转人工，附完整审查历史 |

### 4.3 Evaluator 退回重试上限

与任务级重试上限（Max 2）一致：同一阶段 Evaluator 退回超过 2 次 → 转人工。

---

## 5. 实现计划

| 编号 | 工作项 | 归属 | 工作量 |
|------|--------|------|--------|
| EV-1 | `internal/orchestrator/evaluator.go` 接口定义 | ainspection (P0) | S |
| EV-2 | `internal/orchestrator/evaluator.go` Evaluator 完整实现（独立 session + LLM 调用） | ainspection (P1) | M |
| EV-3 | `prompts/review-locate.tmpl` / `review-fix.tmpl` / `review-verify.tmpl` 三阶段 prompt | ainspection (P0) | S |
| EV-4 | `internal/orchestrator/stage.go` 增加 review 阶段门控 | ainspection (P0) | S |
| EV-5 | Evaluator 完整实现（独立 session + 模型切换 + confidence 双源） | ainspection (P1) | M |
| EV-6 | `MCTSScore` 接口实现，桥接 internal/mcts 的 Simulation | ainspection (P1) | S |

> **P0 交付**：接口定义 + stage 门控位预留 + 三份 prompt 模板骨架。
> **P1 交付**：完整 Evaluator session 启动、模型差异化、审查报告流转、confidence 双源校准、MCTS Simulation 评分桥接。

---

## 6. MCTS Simulation 评分接口

MCTS 引擎（[`agents-mcts.md`](agents-mcts.md)）的 Simulation 步骤需要对节点（Hypothesis 或 PlanStep）打分；评分由 Evaluator 在独立 session 中给出（避免 Generator 自评偏差）。

```go
// internal/orchestrator 中 MCTSScorer 接口（由 Evaluator 实现）
type MCTSScorer interface {
    // ScoreLocate 给 HypothesisNode 打分（locate 4 维加权）
    ScoreLocate(ctx context.Context, h HypothesisForScore) (float64, error)

    // ScoreFix 给 PlanStepNode 打分（fix 4 维加权）
    ScoreFix(ctx context.Context, step PlanStepForScore, candidate DiffForScore) (float64, error)
}

// 对应输入结构（internal/orchestrator/evaluator.go）
type HypothesisForScore struct {
    Hypothesis string   `json:"hypothesis"`
    Evidence   []string `json:"evidence"`
    Depth      int      `json:"depth"`
}

type PlanStepForScore struct {
    StepID   string `json:"step_id"`
    Action   string `json:"action"`
    Target   string `json:"target"`
    Approach string `json:"approach"`
}

type DiffForScore struct {
    Content  []byte `json:"content"`
    FilePath string `json:"file_path"`
}
```

**调用规则**：

- 每次 Simulation 调用 `ScoreLocate` 或 `ScoreFix` 都启动一个**短生命周期 Evaluator 子 session**（通过 `sessionMgr.Spawn`），输入仅为节点本身 + 4 维 rubric prompt，**不携带 MCTS 内部树**
- 子 session 完成后 token 计数回写父 session 的 Usage
- 评分缓存：相同 hash 的节点（`hash(h.evidence + h.hypothesis)`）缓存 score 1 小时