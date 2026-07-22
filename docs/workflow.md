# 工作流定义

> 定义各阶段完整流程、门控条件、异常处理和重试上限。
> **权威规范见** [`../AGENTS.md`](../AGENTS.md) 第 3-4 章。
> **详细工作流见** [`agents-workflow.md`](agents-workflow.md)。
> **场景示例见** [`examples.md`](examples.md)。

---

## 1. Session 启动协议

每个 session 启动时执行 Orient → Setup → Verify Baseline：

```
Orient:  加载 context.yaml + input.yaml + 父节点 summary.md
Setup:   验证 CLI 工具 / 加载 agent 配置 / 注入 Skill Hook
Verify:  go build ./... && go vet ./...
         → 失败则 session blocked，通知用户"基线异常，请人工介入"
         → 通过则进入阶段工作流
```

---

## 2. 完整流水线与门控

```
get ──→ scope_defined
  │
  ▼
locate ──→ expectation_locked
  │
  ▼
[Evaluator Review #1] ──→ passed
  │
  ▼
fix ──→ diff-validate + go build + golangci-lint
  │
  ▼
[Evaluator Review #2] (主 review) ──→ passed
  │
  ▼
verify ──→ golangci-lint + skaffold deploy + 接口验证
  │
  ▼
[Evaluator Review #3] ──→ passed
  │
  ▼
commit ──→ MR created
```

| 门控 | 检查条件 | 不满足时 |
|------|---------|---------|
| `get→locate` | `scope_defined` | 提示用户提供问题描述 |
| `locate→review#1` | `expectation_locked == true` | 等待用户确认 |
| `review#1→fix` | Evaluator 审查通过 | 退回 locate（最多 2 次） |
| `fix→review#2` | diff-validate + go build + golangci-lint | 退回 fix 修正（计入重试） |
| `review#2→verify` | Evaluator 审查通过 | 退回 fix（最多 2 次） |
| `verify→review#3` | 部署成功 + 接口 2xx + linter 通过 | 按异常处理 |
| `review#3→commit` | Evaluator 验证审查通过 | 退回 verify 补充（最多 2 次） |

---

## 3. get 阶段

- **输入**：Issue URL / 手动描述
- **输出**：`context.yaml` (status=scope_defined)
- **Skill**：`jira-query`
- **Prompt**：`prompts/get-system.tmpl`

---

## 4. locate 阶段

- **输入**：context.yaml + signals/
- **输出**：树节点 (findings + plan + hypotheses)
- **Prompt**：`prompts/locate-system.tmpl` + `prompts/locate-disclose.tmpl`

交互循环：Collect → Disclose → Infer → Options → Decide（每轮提供 ≥3 个方向，标注置信度和风险）

---

## 5. fix 阶段

- **前置条件**：expectation_locked=true + Evaluator Review #1 通过
- **输入**：plan.steps + 用户确认的方向
- **输出**：patches/ diff 文件
- **Prompt**：`prompts/fix-system.tmpl`
- **工具**：diff-validate

流程：
1. 生成 unified diff
2. diff-validate 语法校验
3. go build ./... + go vet ./...
4. golangci-lint run --new-from-rev=HEAD~1
5. Linter 问题自动修复（最多 2 轮）
6. 披露 diff 摘要 → 等待用户确认应用
7. 编译/lint 失败 → 回退修正 → retry_count.fix++

---

## 6. review 阶段（Evaluator Agent）

- **Agent**：Evaluator（独立 session，可选 Sonnet 模型）
- **输入**：input.yaml + output.yaml + patches/*.diff（主 review）
- **输出**：review_report（passed/score/blockers/warnings + confidence_self/evaluator/final 三字段）
- **Prompt**：三阶段各一份
  - `prompts/review-locate.tmpl` — 审查 locate 阶段的根因定位
  - `prompts/review-fix.tmpl` — 审查 fix 阶段的 diff（主 review）
  - `prompts/review-verify.tmpl` — 审查 verify 阶段的部署/接口验证
- **详细设计**：[`agents-evaluator.md`](agents-evaluator.md)

---

## 7. verify 阶段

- **输入**：已应用 patch + review 通过的代码
- **Skill**：skaffold-deploy
- **Prompt**：`prompts/verify-system.tmpl`

流程：
1. golangci-lint run（确认无新增问题）
2. skaffold build + deploy
3. 调用目标接口验证（200 / 错误消失）
4. 采集验证期间信号（logs/metrics）
5. 失败 → 分析错误 → retry_count.verify++
6. go test ./...（确认无回归）

---

## 8. commit 阶段

- **前置条件**：Evaluator Review #3 通过
- **输入**：验证通过的修复代码
- **Skill**：glab-mr、jira-query

流程：
1. git checkout -b fix/<JIRA-ID>
2. git commit -m "[JIRA-ID] <description>"
3. glab mr create（关联 Jira Issue）
4. jira issue update（附加 MR 链接）
5. Lark 通知用户 Review

---

## 9. 异常处理与重试

| 异常 | 处理 | 重试计数 |
|------|------|---------|
| Session 基线验证失败 | session blocked，通知用户 | — |
| Agent session 超时 | 自动保存节点，用户可 resume | — |
| LLM 返回无效输出 | 重试 2 次 → 降级备选 agent | 独立计数 |
| go build 失败 | 回退 patch，Agent 修正 | retry_count.fix++ |
| golangci-lint error | Agent 自动修复，最多 2 轮 | retry_count.fix++ |
| Evaluator review 不通过 | 退回上一阶段修正 | 对应阶段 retry_count++ |
| skaffold deploy 失败 | 输出错误日志，保留现场 | retry_count.verify++ |
| 接口验证失败（非 2xx） | Agent 分析 + 修正 | retry_count.verify++ |
| go test 失败（回归） | 回退 patch，Agent 修正 | retry_count.fix++ |

**重试上限**：同一阶段 `retry_count >= 2` 时 → 标记 blocked → 通知用户 → 保留所有中间产物。

---

## 10. 关键约束（不可违反）

1. **生成-评估分离**：fix 后必须经独立 Evaluator 审查
2. **命令白名单**：Agent 只能执行 config.yaml 中预定义的命令
3. **Session 启动基线验证**：每个 session 启动时 go build + go vet
4. **重试上限**：每阶段最多 2 次，超限转人工
5. **session ≡ 节点 + 主动 Context Reset**：阶段切换时强制新 session 冷启动；CLI rollback/branch 仅动状态机不启 LLM
6. **Linter 门控**：verify 前 golangci-lint 通过
7. **用户掌舵**：expectation_locked 硬前置条件 + 敏感操作审批
8. **沙箱即会话级 working dir**：每个 session 独占 `~/.ainspection/sessions/<id>/`，FSGuard 校验路径不越界
