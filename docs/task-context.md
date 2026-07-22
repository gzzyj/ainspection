# 树形上下文规范

> 定义树形上下文的完整 schema、操作语义、隔离机制、Context Reset 协议和结构化计划格式。
> **权威架构见** [`../AGENTS.md`](../AGENTS.md) 第 3 章。
> **工作流集成见** [`agents-workflow.md`](agents-workflow.md) 第 5-6 章。

---

## 1. 树结构 Schema

### context.yaml（树指针 + 任务状态）

```yaml
# ~/.ainspection/tasks/<task-id>/context.yaml
task_id: "20260510-093015-a3f2"   # 格式 <yyyymmdd-HHMMSS>-<random4>
current_node_id: "n1-locate-logs"
current_session_id: "20260510-093015-a3f2-1-locate"   # session ≡ 节点：与 current_node_id 1:1
root_node_id: "root"
tree_version: 1
created_at: "2026-05-09T14:30:52+08:00"
updated_at: "2026-05-09T14:45:00+08:00"
status: locating                  # 见下方 9 状态枚举
issue_ref: "JIRA-1234"
service: "order-svc"              # config.services 中 name 引用
retry_count:                      # 任务级重试计数器
  locate: 0
  fix: 0
  verify: 0
  max_per_stage: 2
baseline_verified: true           # Session 启动基线验证状态
```

**status 9 枚举**（D1）：

| 状态 | 含义 |
|------|------|
| `pending` | task 创建但尚未 get |
| `scope_defined` | get 完成（有问题域） |
| `locating` | 进入 locate（含 MCTS） |
| `expectation_locked` | locate review #1 通过，可启 Planner |
| `fixing` | Planner 已出 plan，进入 fix |
| `verifying` | fix 通过 review #2，进入部署验证 |
| `committing` | verify 通过 review #3，进入 MR |
| `done` | MR 创建成功 |
| `blocked` | 任一阶段重试 ≥2 次或 baseline 异常，需人工 |

### tree.yaml（树拓扑）

```yaml
# ~/.ainspection/tasks/<task-id>/tree.yaml
task_id: "20260509-143052"
nodes:
  root:
    parent: null
    children: ["n1-locate-logs", "n2-locate-traces"]
    status: completed
  n1-locate-logs:
    parent: "root"
    children: ["n3-review-1", "n3-fix"]
    branch_reason: "MCTS 评估 log 路径置信度 0.72"
    status: completed
  n3-review-1:
    parent: "n1-locate-logs"
    children: ["n3-fix"]
    agent_role: "evaluator"      # 新增：标识 Evaluator Agent 节点
    status: completed
  n3-fix:
    parent: "n3-review-1"
    children: []
    status: in_progress
branches:
  - name: "main"
    path: ["root", "n1-locate-logs", "n3-review-1", "n3-fix"]
```

---

## 2. 节点操作语义

> **核心原则**：所有节点操作都是**纯文件状态机变更**，不直接启动 LLM。LLM 进程由 `ainspection run` 显式触发，加载 `current_node_id` 指向的节点 input + 父 summary 冷启动。这是 `session ≡ 节点` 抽象的直接结果：节点是 session 的状态机面，LLM 进程是 session 的运行时面，两者解耦。

### 2.1 创建节点
- 前置条件：父节点 status=completed
- 操作：`mkdir nodes/<node-id>/` → 写入 input.yaml / output.yaml / meta.yaml
- 副作用：tree.yaml 更新 parent.children，context.yaml 更新 current_node_id
- **不启 LLM**

### 2.2 回滚 (Rollback)
- 树指针切换到目标节点，恢复其 signals/
- 不删除任何历史节点或分支
- **不启 LLM**（CLI `ainspection session rollback` 仅修改 context.yaml.current_node_id）

### 2.3 分支 (Branch)
- 从历史节点分叉，继承源节点的 input + user_directives
- 原分支保持不变
- **不启 LLM**（CLI `ainspection session branch` 仅创建空节点）

### 2.4 重放 (Replay)
- 用户显式触发：`ainspection run --replay <node-id>`
- 在新 session 中加载目标节点的 input.yaml，重新执行
- 输出写入新节点（非覆盖）
- **启 LLM**（这是显式 run）

### 2.5 合并 (Merge)
- 将子分支的 findings 合并到目标节点 output.yaml
- **不启 LLM**

---

## 3. Context Reset 协议

### 3.1 两种触发策略

| 策略 | 触发条件 | 行为 |
|------|---------|------|
| **被动（阈值）** | 上下文使用率达 ~40% | fork 子 session，注入父节点摘要 |
| **主动（完成）** | 节点 status=completed | 强制结束 session，新 session 冷启动 |

### 3.2 主动 Context Reset 流程

```
节点标记为 completed
      │
      ▼
生成 summary.md（≤500 字符）
      │
      ▼
结束当前 session（无论上下文使用率）
      │
      ▼
新 session 从以下内容冷启动：
  ├── input.yaml（问题域 + 用户指令）
  ├── 父节点 summary.md
  ├── 当前阶段 prompt 模板
  └── 当前 agent 的 tool/skill 描述
  ≠ 之前 session 的完整对话历史
```

### 3.3 子 session 收到的输入

```
┌────────────────────────────────────────────────┐
│ 1. 当前阶段 prompt 模板                        │
│ 2. 父节点 summary.md（格式化摘要，≤500 字符）   │
│ 3. input.yaml（问题域 + 用户指令）              │
│ 4. 关联证据文件的路径引用（非内容）             │
│ 5. 当前 agent 的 tool/skill 描述               │
│                                                │
│ ≠ 父 session 的对话历史                        │
│ ≠ 父 MCTS 内部树（internal/mcts/ 临时数据）    │
│ ≠ 原始日志/指标全文                            │
└────────────────────────────────────────────────┘
```

---

## 4. 摘要生成算法

节点标记为 `completed` 时自动生成 `summary.md`：

1. **问题域**（1 句）
2. **已确认事实**：confidence ≥ 0.7 的 findings（≤5 条），带 evidence 路径
3. **已排除假设**：所有 discarded_hypotheses（≤5 条）
4. **当前最佳方向**：plan.steps[0]
5. **用户指令**：所有 user_directives

总长度 ≤ 500 字符。

---

## 5. 结构化计划工件（JSON Schema）

### 5.1 output.yaml 中的 plan 字段

```yaml
# output.yaml（含结构化计划）
node_id: "n1-locate-logs"
session_id: "20260510-093015-a3f2-1-locate"   # session ≡ 节点：与 node_id 1:1
findings:
  - hypothesis: "payment-svc 慢查询导致 upstream 超时"
    confidence_self: 0.92            # Generator 自评（B5 双源）
    confidence_evaluator: 0.85       # Evaluator 重评
    confidence_final: 0.85           # min(self, evaluator)
    evidence: ["traces/slow-span.txt:5-12", "logs/timeout.txt:23-45"]
    status: confirmed
discarded_hypotheses:
  - hypothesis: "网络抖动"
    evidence_against: "metrics/network.txt 显示正常"
    status: discarded
plan:                                # 结构化计划，JSON Schema 约束
  version: "1.0"
  goal: "修复 payment-svc 慢查询"
  steps:
    - id: "step-1"
      action: "添加联合索引"
      target: "migration/002_add_payment_idx.sql"
      approach: "CREATE INDEX idx_payments_status_created ON payments(status, created_at)"
      estimated_impact: "P99 从 2.5s 降至 <200ms"
      risk: "low"
      rollback: "DROP INDEX idx_payments_status_created"
      confidence_self: 0.88          # plan.step 也走双源
      confidence_evaluator: 0.80
      confidence_final: 0.80
  alternatives:
    - approach: "调整 order-svc 超时时间"
      tradeoff: "治标不治本，可能掩盖其他超时问题"
      discarded: true
  pre_checklist:
    - "确认 status 列区分度 > 0.1"
    - "确认 migration 文件名格式正确"
  post_checklist:
    - "go build 通过"
    - "EXPLAIN 确认索引被使用"
    - "golangci-lint 通过"
```

### 5.2 设计目的

- **机器可解析**：Evaluator Agent 直接读取 plan 字段，无需解析 Markdown
- **跨 session 可靠传递**：Context Reset 中 JSON 结构无损
- **可审计**：plan → pre_checklist → post_checklist 形成完整追溯链

### 5.3 与 Markdown 计划的关系

- `plan` 字段（JSON）：机器消费，用于 Evaluator 检查、自动化验证
- `summary.md`（Markdown）：人类消费，用于快速理解当前状态
- 两者由 `summary.go` 自动同步：plan 更新时自动更新 summary.md 的"当前最佳方向"段

---

## 6. 并发安全

- 同一 task 同一时刻只有一个活跃 session
- `session manager` 持有任务级文件锁 `task.lock`
- 父 session 等待子 session 完成后才能继续

---

## 7. 与 progress.json 的关系

`progress.json`（项目根目录）是 **ainspection 项目自身 AI 研发** 的进度跟踪文件，与树形上下文的 `context.yaml` 职责不同：

| 文件 | 位置 | 用途 | 消费者 |
|------|------|------|--------|
| `context.yaml` | `~/.ainspection/tasks/<task-id>/` | ainspection **运行时**单个任务的树指针和状态 | Orchestrator / Session Manager |
| `progress.json` | `<repo_root>/` | ainspection **项目研发**的所有开发任务进度 | AI 开发工具 (Claude Code 等) |

- `progress.json` 跟踪的是「开发 ainspection 本身」的任务（P0-1, P1-1 等）
- `context.yaml` 跟踪的是「ainspection 运行后」的自动化修复任务
- 两者不重叠，各自服务于不同层面
