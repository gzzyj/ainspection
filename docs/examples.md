# 使用示例 (v2)

> 本文档是 `AGENTS.md` 的 L3 补充，提供各场景的完整交互示例。
> **架构和流程见** [`../AGENTS.md`](../AGENTS.md)。
> **v2 说明**：Agent 通信通过 adapter 封装原生 CLI，用户交互层面无变化。

---

## 1. 模糊描述 → 逐步定位 → 修复

```
用户: ainspection run --service order-svc --desc "/api/v1/order 接口最近有时返回 500"

Agent (get):
  已解析问题范围：
  - 服务: order-svc (config.services 查找成功)
  - 接口: /api/v1/order
  - 现象: HTTP 500
  - 时间: 最近 1 小时
  创建任务 20260510-093015-a3f2，开始 locate...

Agent (locate Round 1, MCTS):
  信号采集完成（loki + tempo + prom）：
  - 日志 [14:20-14:35]: 23 次 "context deadline exceeded"
  - 指标: P99 延迟从 200ms 升至 2.5s
  - Trace: 85% 请求在 payment-svc 调用上耗时 >2s

  MCTS 假设搜索（5 个 root，max_iterations=16，max_depth=4）：
  ┌────────────────────────────────────────────────────┬────────┬────────┬────────────┐
  │ Hypothesis                                          │ self   │ Eval   │ final      │
  ├────────────────────────────────────────────────────┼────────┼────────┼────────────┤
  │ payment-svc 慢查询导致 upstream 超时                │ 0.85   │ 0.82   │ 0.82  ★    │
  │ payment-svc 连接池耗尽                              │ 0.50   │ 0.45   │ 0.45       │
  │ order-svc 超时配置不合理                            │ 0.40   │ 0.35   │ 0.35       │
  │ 网络层抖动                                          │ 0.20   │ 0.18   │ 0.18       │
  │ payment-svc GC 卡顿                                 │ 0.30   │ 0.25   │ 0.25       │
  └────────────────────────────────────────────────────┴────────┴────────┴────────────┘
  MCTS 选 top-2: [payment-svc 慢查询] + [payment-svc 连接池耗尽]

  请确认探索方向（多选，留空选 top-1）:

用户: 1

Agent (locate Round 2, sub-MCTS on payment-svc 慢查询):
  Spawn 子 session 独立调查 payment-svc...
  发现 slow-query: "SELECT * FROM payments WHERE ..." 全表扫描
  → 根因确认: payment-svc 缺索引导致慢查询
  confidence_final = 0.92

  expectation_locked = true，进入 [Evaluator Review #1]

[Evaluator Review #1] passed, score=8

Planner (Spawn, 独立 Opus session):
  生成 plan.json:
  steps:
    - id: step-1
      action: 添加联合索引
      target: migration/002_add_payment_idx.sql
      approach: CREATE INDEX idx_payments_status_created ON payments(status, created_at)

Agent (fix, MCTS on PlanStep candidates):
  为 step-1 生成 2 个候选 diff (mcts.fix.branching_factor=2)，Evaluator 评分:
  - 候选 A (单列 status 索引): 修复完备性 6 / 0.65
  - 候选 B (联合索引 status+created_at): 修复完备性 9 / 0.88 ★
  应用候选 B → go build 通过 → diff-validate 通过 → [Evaluator Review #2]

[Evaluator Review #2] passed, score=9

Agent (verify):
  skaffold deploy → /api/v1/order 连续 10 次 200，P99 恢复至 180ms
  [Evaluator Review #3] passed

Agent (commit):
  分支 fix/JIRA-1234 → git commit → glab mr create
  MR: https://git.qingteng.cn/ms/order-svc/-/merge_requests/42
```

---

## 2. 高置信度输入 → 直接定位

```
用户: ainspection run --issue https://jira.qingteng.cn/browse/MS-5678

Agent (get):
  Jira Issue MS-5678: "user-svc /api/user/profile 返回 401"
  时间: 2026-05-09 09:00-10:00

Agent (locate):
  日志: 大量 "JWT token expired at ..."，集中在 token 过期后 30s 窗口
  代码: token 刷新逻辑仅在 /api/auth/refresh 触发，/api/user/profile 不触发刷新
  → 根因: profile 接口未处理 token 即将过期的场景
  置信度: 0.92

  方向:
  1. 在 middleware/auth.go 中增加 token 过期前主动刷新的逻辑 — 置信度 0.92, 风险: low
  2. 调整 token 过期时间延长至 2h — 置信度 0.60, 风险: medium (治标不治本)
  3. 全量排查所有接口的 token 处理 — 置信度 0.30, 风险: high (范围过大)

用户: 方向 1
... (后续 fix/verify/commit 同上)
```

---

## 3. 上下文回滚与分支重做（仅切状态机，不启 LLM）

```
用户: ainspection session rollback --to n1-locate-logs

CLI:
  树指针已切换: current_node_id = n1-locate-logs
  signals/ 已恢复为 n1 的证据数据
  分支 n2-locate-traces 已保留，可随时切换
  ⚠ 当前未启 LLM。下一步 ainspection run 将以 n1-locate-logs 为起点冷启动新 session。

用户: ainspection session branch --from n1-locate-logs --reason "数据库连接池方向"

CLI:
  已创建分支 n3-db-check (parent=n1-locate-logs)，input.yaml 继承自 n1
  ⚠ 节点已创建但未启 LLM。下一步 ainspection run 才会启 Agent。

用户: ainspection run

Agent (locate, n3 分支冷启动):
  检查 metrics/db-pool.txt → 连接池利用率正常 (32/50)
  检查 slow-query 日志 → 无慢查询
  → 排除数据库假设，建议切换回 trace 路径

用户: ainspection session rollback --to n2-locate-traces
用户: ainspection run
(继续 trace 路径的调查)
```

---

## 4. 性能分析流水线

```
用户: ainspection run --profile order-svc --duration 5m

CLI: kubectl port-forward order-svc-xxx :6060 → 采集 5 分钟 CPU/Heap profile

Agent (profile analyze):
  CPU Profile TopN:
  1. encoding/json.Marshal        34.2%
  2. runtime.mallocgc             18.7%
  3. database/sql.(*Rows).Next    12.1%

  Heap Profile:
  累计分配 Top1: io.ReadAll (net/http response.Body 未关闭)

  推断:
  1. json.Marshal 占用过高 → 建议改用 json.Encoder + sync.Pool 复用 buffer
  2. 疑似 goroutine 泄漏 → response.Body 未 Close，~200 goroutines 堆积

  方向:
  1. 优化 JSON 序列化 — 预期 CPU 降低 25%, 风险: low
  2. 修复 response.Body 泄漏 — 预期 goroutine 数恢复正常, 风险: low
  3. 两个都做 — 预期综合优化, 风险: medium

用户: 方向 3

Agent (fix → verify):
  优化效果对比:
  CPU: json.Marshal 34.2% → 12.1% (↓65%)
  Goroutines: 230 → 38 (↓83%)
  MR 描述含前后火焰图对比
```
