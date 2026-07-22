# 实现 Todo List (v2)

> 本文档是 `AGENTS.md` 的 L3 补充，按 Roadmap 梳理完整实现任务。
> **权威规范见** [`../AGENTS.md`](../AGENTS.md)；**详细设计见** [`design.md`](design.md)。

---

## 统计

| 阶段 | 任务数 | 完成 | 部分完成 | 待开始 |
|------|--------|------|----------|--------|
| P0 CLI 骨架与基础设施 | 6 | 6 | 0 | 0 |
| P1 报错自动定位闭环 | 11 | 11 | 0 | 0 |
| P2 性能分析 + 离线评测 | 5 | 5 | 0 | 0 |
| P3 综合智能 | 2 | 2 | 0 | 0 |
| V2 代码重构 | 3 | 3 | 0 | 0 |
| **合计** | **27** | **27** | **0** | **0** |

---

## P0 — CLI 骨架与基础设施 (6/6)

- **已完成** (5): P0-1 CLI骨架 / P0-2 树形上下文 / P0-3 Session管理 / P0-4 Skill系统 / P0-6 Orchestrator
- **P0-5** — qtmf DB/Redis trace 日志: `pending` (归属 qtmf MR，非 ainspection 代码)

---

## P1 — 报错自动定位闭环 (11/11)

- **已完成** (11): P1-1 Doctor / P1-2 MCTS引擎 / P1-3 Planner / P1-4 沙箱审计 / P1-5 get / P1-6 locate / P1-7 Capability Skill 集 (11个) / P1-8 上下文防污染规范 / P1-9 fix / P1-10 verify / P1-11 commit

### P1-9 fix 工作流 (含 MCTS)
- **状态**: `completed`
- **工作量**: M
- **依赖**: P1-6, P1-2
- **交付物**: `prompts/fix-system.tmpl` / `tools/diff-validate/main.go` / pipeline diff-validate 门控集成
- **说明**: diff-validate 工具创建 + executeFix 门控集成（A1）

### P1-10 verify 工作流
- **状态**: `completed`
- **工作量**: S
- **依赖**: P1-9
- **交付物**: `prompts/verify-system.tmpl` / `.skills/skaffold-deploy.md` / `.skills/http-probe.md` / `.skills/golangci-lint.md` / `.skills/go-test.md` / `.skills/go-build.md` / `.skills/go-vet.md`
- **说明**: P2-2 升级为 LLM tool call dispatch（与 get/locate/commit 一致）

### P1-11 commit 工作流
- **状态**: `completed`
- **工作量**: S
- **依赖**: P1-10
- **交付物**: `prompts/commit-system.tmpl` / `.skills/glab-mr.md` / `.skills/jira-update.md`
- **说明**: P2-3 集成测试完成 (5 个测试用例)

### P1-12 Evaluator LLM 双路径实现
- **状态**: `completed`
- **工作量**: M
- **依赖**: P1-9, P1-11
- **交付物**: `internal/orchestrator/evaluator_llm.go` / `prompts/review1-system.tmpl` / `prompts/review2-system.tmpl` / `prompts/review3-system.tmpl`
- **说明**: A2: Evaluator LLM 双路径（native + CLI）实现，补上 review 阶段门控

---

## V2 — 代码重构 (3/3)

### V2-1 文档基线重建
- **状态**: `done`
- **工作量**: M
- **交付物**: AGENTS.md / docs/design.md / docs/BASELINE.md / docs/todo.md / progress.json + 术语标准化/Plan B injection/穷举config/Source symlink/插桩标记
- **说明**: 含原 V2-1b 架构基线细化（17 个悬空概念）

### V2-2 adapter/injection 重构
- **状态**: `completed`
- **工作量**: L
- **依赖**: V2-1
- **交付物**: `internal/adapter/types.go` (AgentAdapter 接口) + `injection.go` (SkillInjector/HookInjector) + 5 CLI adapter + `llm_native.go`
- **说明**: 合并原 V2-2~V2-5；LLM native 迁移优先级低不阻塞

### V2-3 skill 重构 + 硬编码消除
- **状态**: `completed`
- **工作量**: M
- **依赖**: V2-2
- **交付物**: 重构 injector/executor / 删除 adapter_* / 19+ 处硬编码配置化
- **说明**: 合并原 V2-6 + V2-8 + V2-9

---

## P2 — 性能分析 + 离线评测 (5/5)

### P2-1 Alloy DaemonSet
- **状态**: `completed`
- **工作量**: M
- **交付物**: `deploy/alloy/` / `deploy/pyroscope/` / `deploy/install.sh` / `deploy/cleanup.sh` / `deploy/scripts/` / `deploy/README.md`
- **说明**: Alloy DaemonSet (k3s) + Pyroscope (docker) + 多用户隔离链路 + 自动安装/清理

### P2-2 Profile 读取工具
- **状态**: `completed`
- **工作量**: S
- **交付物**: `tools/pprof-summary/main.go` / `.skills/pprof-analyze.md` (输出契约更新) / `internal/prompt/renderer.go` (ProfileAnalyzeInput)
- **说明**: 薄包装 go tool pprof CLI，Pyroscope/本地文件/diff 三种模式，top/tree/traces 文本直接嵌入 JSON

### P2-3 AI Profile 分析
- **状态**: `completed`
- **工作量**: S
- **交付物**: `prompts/profile-analyze.tmpl` (v3.0)
- **说明**: 5 Parts 模板，14 种 bottleneck_type + 9 条 signal→root 映射 + 5 社区来源；集成分支决策树/火焰图信号关联/跨 profile 交叉验证

### P2-4 性能修复生成
- **状态**: `completed`
- **工作量**: S
- **依赖**: P2-3
- **交付物**: `prompts/profile-fix.tmpl` (v2.0) / `internal/prompt/renderer.go` (ProfileFixInput 新增 PlanJSON)
- **说明**: 4 Parts 模板，14 bottleneck_type→strategy 映射 + Go 改写示例，MCTS 策略空间展开 + 四维性能评分，输出与 plan-system schema 兼容

### P2-5 离线评测框架
- **状态**: `completed`
- **工作量**: S
- **依赖**: P1-* + V2-* 全部完成
- **交付物**: `internal/eval/` (smoke.go + case.go + types.go)、`cmd/eval/cmd.go`
- **说明**: L2 Prompt Smoke Test (16 模板) + Case Regression (2 cases); 降级自原 L3 端到端评测设计

---

## P3 — 综合智能 (2/2)

### P3-1 run 一键流水线
- **状态**: `completed`
- **工作量**: S
- **交付物**: `internal/orchestrator/wiring.go` / `internal/orchestrator/dispatcher_adapter.go` / `cmd/run/run.go` (重写) / `prompts/run-orchestrator.tmpl` / `internal/prompt/renderer.go` (新增 RunOrchestratorInput 类型)
- **说明**: CLI 命令与 Pipeline 引擎接线，10 必需依赖 (tree/session/prompt/skill/adapter/planner/dispatcher)，Evaluator/MCTS Phase 1 传 nil

### P3-2 跨服务调用链关联
- **状态**: `completed`
- **工作量**: M
- **依赖**: P1-7
- **交付物**: TaskSpec/RunSpec/GetInput/LocateInput + TraceID; CLI --trace-id flag → root input.yaml → get LLM output → writeGetInput → readLocateInput → locate-system prompt; 3 prompt templates 更新

---

> Prompt 模板 P0/P1 共 13 份保持不变。Skill 定义 .skills/ 共 11 个保持不变。V2 仅改变通信方式和注入方式。
