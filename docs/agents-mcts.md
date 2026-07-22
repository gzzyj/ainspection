# 双阶段 MCTS 引擎设计

> 定义 ainspection 的 MCTS 决策搜索引擎，**locate（假设空间）** 与 **fix（修复方案空间）** 共享通用引擎、不同节点实例化。
> **权威规范见** [`../AGENTS.md`](../AGENTS.md) §2 决策表；评分接口见 [`agents-evaluator.md`](agents-evaluator.md) §6。

---

## 1. 设计动机

朴素的 LLM 决策模式是"输出 N 个方向 + 用户选 1 个"，受限于：

- LLM 自评偏差（generator 倾向高估自己的方案）
- 用户没有客观分维度依据，只能凭直觉选
- 无法对子节点（更细的子假设、更具体的实现方案）做系统探索

**双阶段 MCTS** 用 UCB1 + 独立 Evaluator 评分解决：

- locate 阶段在**假设空间**搜索：根因 → 子原因 → 子子原因
- fix 阶段在**修复方案空间**搜索：plan.step → 候选 diff
- Simulation 评分由独立 Evaluator session 给出，避免自评偏差

---

## 2. 通用引擎

### 2.1 模块布局

```
internal/mcts/
├── engine.go         # Run/Selection/Expansion/Simulation/Backprop
├── ucb.go            # UCB1 公式
├── budget.go         # max_iterations / max_depth / token_budget
├── locate_node.go    # HypothesisNode 实例（locate 阶段）
└── fix_node.go       # PlanStepNode 实例（fix 阶段）
```

### 2.2 核心接口

```go
// NodeExpander 扩展函数：给定父节点，生成子节点。
// 由外部注入（locate 用 mcts-expand 模板 + locate prompt，
// fix 用 mcts-expand 模板 + fix prompt）。
type NodeExpander func(ctx context.Context, parent *node) ([]*node, error)

// Scorer 评分函数：对节点打分（0.0 ~ 1.0）。
// 由 Evaluator 的 MCTSScorer 接口实现。
// 支持多维评分：通过 MakeLocateScorerWithDims / MakeFixScorerWithDims
// 可在评分时存储 DimensionScores 到节点。
type Scorer func(ctx context.Context, n *node) (float64, error)

// Engine 是引擎本体
type Engine struct {
    budget     Budget
    ucbC       float64
    expander   NodeExpander
    scorer     Scorer
    usedTokens int64
    tokenUsage TokenUsageFunc // 外部 token 计数器，返回累计消耗量
}

// Budget 控制资源消耗
type Budget struct {
    MaxIterations   int
    MaxDepth        int
    BranchingFactor int
    MaxTokens       int64 // 累计 LLM 输入+输出 token 上限（0 表示不限制）
}

// Run 跑一次 MCTS，返回所有叶子节点（调用方用 TopK 取前 K）
func (e *Engine) Run(ctx context.Context, root *node) ([]*node, error)
```

### 2.3 UCB1 公式

```
UCB(node) = node.AvgScore() + c * sqrt(ln(parent.Visits) / node.Visits)
```

`c` 默认 `√2 ≈ 1.41`（`config.mcts.ucb_c`），平衡 exploration vs exploitation。

### 2.4 四步循环

```
for iteration in 1..MaxIterations:
    if BudgetExceeded(): break

    # 1. Selection：从 root 一路下钻，每层选 UCB 最大的子节点，直到可扩展节点
    selected = Select(root)

    # 2. Expansion：如果未到 MaxDepth，调 expander 让 LLM 生子节点
    if depth(selected) < MaxDepth and !selected.failedExpansion:
        children = expander(ctx, selected)
        selected.AddChild(children...)

    # 3. Simulation：调 scorer 给每个子节点打分
    for child in children:
        score = scorer(ctx, child)
        # 若注入 RolloutSimulator，升级为多步 Rollout 累积奖励
        backprop(child, score)

return TopK(AllLeaves(root), by: AvgScore, k: topK)
```

### 2.5 多维评分与 Rollout

#### DimensionScores

LLM Scorer 返回四维独立分数，存储在节点上供诊断和审计：

```go
type DimensionScores struct {
    Dimensions map[string]float64 // 各维度原始分
    Aggregate  float64            // 加权总分（用于 UCB）
}
```

- locate 维度：`correctness` / `evidence` / `verifiability` / `impact`
- fix 维度：`completeness` / `quality` / `compliance` / `security`

节点通过 `SetDimensionScores` / `DimensionScores` 访问器存取。适配器 `MakeLocateScorerWithDims` / `MakeFixScorerWithDims` 在评分时自动将维度分写入节点。

#### RolloutSimulator（可选）

Engine 支持注入 `RolloutSimulator`，将单次评分升级为多步 Rollout 累积奖励：

- **纯模拟模式（默认）**：基于动作类型做快速分数扰动，零延迟
- **真实执行模式（配置开关）**：通过 `security.CommandExecutor` + `security.Sandbox` 执行真实动作（`go vet` / `go test` / skill 查询），结果回写节点

真实执行受以下约束保护：
- 逐动作类型启用开关（`EnabledLocate` / `EnabledFix`）
- 单动作超时（默认 30s），超时自动降级到模拟
- 命令白名单 + `FSGuard` 路径校验
- 执行失败或审批未通过时自动降级到模拟模式
- 每次 Rollout 真实执行步数上限（`MaxRealSteps`，默认 2）

```go
type RolloutExecConfig struct {
    Enabled           bool     // 总开关，默认 false
    EnabledLocate     []string // 启用的 locate 动作类型
    EnabledFix        []string // 启用的 fix 动作类型
    MaxRealSteps      int      // 真实执行最大步数，默认 2
    PerActionTimeoutS int      // 单动作超时，默认 30
}
```

---

## 3. locate 阶段：假设空间 MCTS

### 3.1 HypothesisNode

```go
// HypothesisPayload locate 阶段的节点载荷：一个根因假设。
type HypothesisPayload struct {
    Hypothesis    string           // 假设描述
    Evidence      []string         // 证据路径
    SourceContext string           // Deprecated: 字符串格式代码位置
    SourceCtx     *SourceContext   // 结构化代码位置（file_path / func_name / line_start / line_end）
    UserHints     []string         // 关键词 / 可疑位置
    Confidence    float64          // 置信度 0-1
}

// SourceContext 描述代码位置的上下文信息。
type SourceContext struct {
    FilePath  string
    FuncName  string
    LineStart int
    LineEnd   int
}

// NewLocateRoot / NewHypothesisNode 创建节点（通过 MakeLocateExpander 适配外部扩展逻辑）。
func NewLocateRoot(id string, h HypothesisPayload) *node
func NewHypothesisNode(id string, parent *node, h HypothesisPayload, depth int) *node
```

### 3.2 Scorer 评分（locate 4 维加权）

由 [`agents-evaluator.md`](agents-evaluator.md) §6 `MCTSScorer.ScoreLocate` 实现，4 维加权：

```
score = 0.35*根因正确性 + 0.30*证据充分度 + 0.20*可验证性 + 0.15*影响面
```

### 3.3 资源预算（默认）

```yaml
mcts:
  locate:
    max_iterations: 16        # 16 次 sim
    max_depth: 4              # 最深 4 层根因细化
    branching_factor: 3       # Expand 每次生 3 个子假设
```

### 3.4 终止条件

- `max_iterations` 达到
- `token_budget` 耗尽
- context 被取消
- （未来）真实执行验证通过时提前终止

---

## 4. fix 阶段：修复方案空间 MCTS

### 4.1 PlanStepNode

```go
// PlanStepPayload fix 阶段的节点载荷：一个修复候选方案。
type PlanStepPayload struct {
    StepID      string           // 对应 plan.steps[].id
    Action      string           // 步骤描述
    Target      string           // 目标文件
    Approach    string           // 具体方法
    Diff        string           // 候选 unified diff 内容
    RootCause   string           // 根因位置 / 类型 / 上下文
    Tests       []TestContext    // 结构化测试上下文
    FixCon      *FixConstraints  // 结构化修复约束
}

// TestContext 描述测试执行上下文。
type TestContext struct {
    Command        string
    ExpectedOutput string
    TimeoutS       int
}

// FixConstraints 描述修复方案的约束条件。
type FixConstraints struct {
    MaxLines       int
    AllowAPIChange bool
    PerfBudgetMs   int
    Notes          string
}

// NewFixRoot / NewPlanStepNode 创建节点（通过 MakeFixExpander 适配外部扩展逻辑）。
func NewFixRoot(id string, p PlanStepPayload) *node
func NewPlanStepNode(id string, parent *node, p PlanStepPayload, depth int) *node
```

### 4.2 Scorer 评分（fix 4 维加权）

由 `MCTSScorer.ScoreFix` 实现：

```
score = 0.35*修复完备性 + 0.30*代码质量 + 0.20*架构合规 + 0.15*安全性
```

### 4.3 资源预算（默认）

```yaml
mcts:
  fix:
    max_iterations: 8         # 8 次 sim（fix 比 locate 单次代价更高）
    max_depth: 3              # 最深 3 层方案细化
    branching_factor: 2       # 每次 Expand 生 2 个候选 diff
```

### 4.4 终止条件

- `max_iterations` 达到
- `token_budget` 耗尽
- context 被取消
- （未来）真实执行验证通过时提前终止

---

## 5. 与其他模块的交互

```
internal/orchestrator
        │
        │ locate 阶段：调 mcts.Engine.Run(rootHypothesis)
        │ fix 阶段：调 mcts.Engine.Run(rootPlanStep)
        ▼
internal/mcts
        │
        │ Selection/Expansion 时调 LLM
        ├─────────────────────────►  internal/adapter (Generator agent)
        │
        │ Simulation 时调 Evaluator
        │   - 基础：MCTSScorer.Score() 返回 aggregate（用于 UCB）
        │   - 扩展：RolloutSimulator 多步累积（可选真实执行）
        └─────────────────────────►  internal/orchestrator/evaluator.go (MCTSScorer)
                                              │
                                              │ Spawn 短生命周期子 session
                                              ▼
                                     internal/session.Manager
```

---

## 6. 与 user / Evaluator review 的关系

- MCTS 输出 top-K 候选节点（locate top-2 假设、fix top-1 候选 diff）
- locate 的 top-2 在 disclose 阶段呈现给用户裁决（用户最终选 1）
- fix 的 top-1 进入 Evaluator Review #2 主审查
- MCTS **不替代用户裁决**，是给用户/Evaluator 提供高质量候选 + 客观评分依据

---

## 7. 性能 / 成本约束

| 阶段   | iter × branching       | 预估 LLM 调用               | 预估 token               |
| ------ | ----------------------- | --------------------------- | ------------------------ |
| locate | 16 × 3 = 48 sim/expand | ~64 次（含 Evaluator 评分） | ~150k input + 30k output |
| fix    | 8 × 2 = 16 sim/expand  | ~24 次                      | ~80k input + 20k output  |

**TokenBudget** 字段强制截止：超额时直接返回当前 top-K，不再 Expand。
