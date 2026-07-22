# 基线状态文档 (v2)

> 记录当前代码基线的完成状态、已知缺口和后续开发优先级。
> **生成时间**: 2026-05-18
> **对应 commit**: P3-2 TraceID 端到端透传

---

## 1. 模块完成状态

| 模块 | 路径 | 状态 | 测试数 | 说明 |
|------|------|------|--------|------|
| 配置管理 | `internal/config` | 完整 | 0 | viper 加载/校验/路径展开/type 向后兼容 |
| **Agent Adapter** | **`internal/adapter`** | **完整** | **17** | **6 adapter + Skill/Hook injection + Registry** |
| 树管理 | `internal/tree` | 完整 | 15 | 任务/节点 CRUD/flock/rollback/branch/replay/merge |
| Session | `internal/session` | 完整 | 23 | Start/Resume/Fork/Spawn/Kill/List + 基线验证 |
| LLM 通信 | `internal/llm` | 完整 | 0 | OpenAI-compatible HTTP/SSE（LLM Native adapter 已迁移） |
| Skill 系统 | `internal/skill` | 重构完成 | 30 | injector 从 agent-type switch 升级为 adapter.Registry 委托 |
| Prompt 模板 | `internal/prompt` | 完整 | — | 13 模板定义 |
| MCTS 引擎 | `internal/mcts` | 完整 | 31 | UCB1 引擎 + locate/fix 双阶段 |
| Planner | `internal/planner` | 完整 | 25 | 规则式 plan 构建 + JSON Schema 校验 |
| 安全 | `internal/security` | 完整 | 31 | 命令白名单/FS guard/沙箱/审计日志 |
| Doctor | `internal/doctor` | 完整 | 17 | 5 类检查：工具链/API key/连通性/配置/沙箱 |
| Eval | `internal/eval` | 完整 | — | L2 Smoke Test (16 模板) + Case Regression |
| Orchestrator | `internal/orchestrator` | 完整 | 24 | Pipeline 已接入 adapter.Registry + wiring.go 工厂；Phase 1 Evaluator/MCTS nil |
| `cmd/doctor` | `cmd/doctor/cmd.go` | 完整 | — | 唯一完整接线的 CLI 命令 |
| `cmd/run` | `cmd/run/run.go` | 完整 | — | P3-1: 完整接线 Pipeline（Config → NewPipelineFromConfig → Run） |
| `cmd/config` | `cmd/config/cmd.go` | STUB | — | 3 个子命令 (get/set/validate) 均 STUB |
| `cmd/session` | `cmd/session/cmd.go` | STUB | — | 5 个子命令均 STUB |
| `cmd/eval` | `cmd/eval/cmd.go` | 完整 | — | L2 Smoke Test + Case Regression |

**构建与测试**: `go build ./...` 成功；`go test ./internal/...` 全部通过 (140+ tests)。

---

## 2. V2 重构完成清单

### 2.1 `internal/adapter/` 模块布局

| 文件 | 接口/职责 | 状态 |
|------|---------|------|
| `types.go` | AgentAdapter / SkillInjector / HookInjector 接口 + Registry + AgentType 常量 + 共享类型 | 完成 |
| `injection.go` | ParamToJSONSchema / Skill↔Tool 转换 / Skill Markdown 写入 / Hook 格式转换 / LLM 配置写入 | 完成 |
| `claude_cli.go` | Claude CLI: `.claude/skills/` + `.claude/hooks/` + `settings.json` | 完成 |
| `kimi_cli.go` | Kimi CLI: `.kimi/skills/` + `config.toml` (TOML hooks) | 完成 |
| `codex_cli.go` | Codex CLI: `.codex/skills/` + `.codex/hooks.json` | 完成 |
| `qwen_cli.go` | Qwen CLI: `.qwen/skills/` + `.qwen/hooks/` + `--experimental-skills` | 完成 |
| `gemini_cli.go` | Gemini CLI: `.gemini/skills/` + `.gemini/hooks/` + `settings.json` | 完成 |
| `llm_native.go` | HTTP 直连 (从 `internal/llm/client.go` 迁移) + SSE 流式 | 完成 |

### 2.2 `internal/skill/` 重构

| 变更 | 说明 |
|------|------|
| `injector.go` 重构 | 移除 agent-type switch，改用 `adapter.Registry` 委托；`NewInjector(registry)` |
| `executor.go` 重构 | `SkillPrefixAgentCmd` 常量化；`slices.Contains` 优化 |
| 删除文件 | `adapter_claude.go` / `adapter_kimi.go` / `adapter_qwen.go` / `native.go` / `bash.go` |
| 保留文件 | `types.go` / `loader.go` (不变) |

### 2.3 Config 增强

| 新增配置段 | 对应硬编码 | 默认值 |
|-----------|-----------|--------|
| `pipeline.stages.get.skills` | pipeline.go 硬编码 `"jira-query" \|\| "feishu-search"` | `["jira-query", "feishu-search"]` |
| `pipeline.stages.locate.skills` | pipeline.go 硬编码 5 个 skill 名称 | `["loki-query", "prom-query", "kubectl-inspect", "tempo-query", "feishu-search"]` |
| `pipeline.stages.locate.max_survey_rounds` | pipeline.go `maxRounds := 3` | 3 |
| `evaluator.min_pass_score` | evaluator.go `r.Score >= 7` | 7 |
| `mcts.{locate,fix}.expand_temperature` | mcts_expander.go `Temperature: 0.3` | 0.3 |
| `mcts.{locate,fix}.expand_max_tokens` | mcts_expander.go `MaxTokens: 1024` | 1024 |
| `mcts.{locate,fix}.score_temperature` | mcts_scorer.go `Temperature: 0.1` | 0.1 |
| `mcts.{locate,fix}.score_max_tokens` | mcts_scorer.go `MaxTokens: 512` | 512 |
| `security.command_timeout` | executor.go 硬编码 60s | 60 |
| `mcts.rollout_exec.max_real_steps` | rollout_executor.go `maxRealSteps = 2` | 2 |
| `mcts.rollout_exec.per_action_timeout_s` | rollout_executor.go `perActionTimeoutS = 30` | 30 |

### 2.4 接口与设计一致性

| 接口 | 设计文档 | 实现 | 差异 |
|------|---------|------|------|
| `AgentAdapter` | docs/design.md §3.2 | Name/Type/Setup/Run/ParseOutput | 一致 |
| `SkillInjector` | docs/design.md §3.3 | InjectSkills(sandboxPath, defs) | 一致 |
| `HookInjector` | docs/design.md §3.3 | InjectHooks(sandboxPath, defs) | 一致 |
| `AgentType` | docs/design.md §3.2 | 6 个常量 (claude_cli/kimi_cli/codex_cli/qwen_cli/gemini_cli/llm_native) | 一致 |
| `TokenUsage` | docs/design.md §3.2 | `{Input int64, Output int64}` | 一致 |
| `AgentSetupConfig` | docs/design.md §3.2 | Endpoint/APIKey/Model/Skills/Hooks/SystemPrompt + Headers/Features/Flags/APIKeyEnv/CliBinary | 合理扩展（§4 Config 段有对应字段） |
| `Registry` | AGENTS.md §0 | Register/Get/GetByString | 一致 |

---

## 3. 资产清单

### .skills/ (11 个)

| 文件名 | 文件名 |
|--------|--------|
| `jira-query.md` | `jira-update.md` |
| `feishu-search.md` | `kubectl-inspect.md` |
| `loki-query.md` | `prom-query.md` |
| `tempo-query.md` | `pprof-analyze.md` |
| `skaffold-deploy.md` | `http-probe.md` |
| `glab-mr.md` | |

### prompts/ (14 个)

| 文件名 | 文件名 |
|--------|--------|
| `plan-system.tmpl` | `get-system.tmpl` |
| `locate-system.tmpl` | `locate-disclose.tmpl` |
| `mcts-expand.tmpl` | `fix-system.tmpl` |
| `verify-system.tmpl` | `commit-system.tmpl` |
| `review-locate.tmpl` | `review-fix.tmpl` |
| `review-verify.tmpl` | `profile-analyze.tmpl` |
| `profile-fix.tmpl` | v2.0 |
| `run-orchestrator.tmpl` | v1.0 |

### 配置文件

| 资产 | 路径 | 说明 |
|------|------|------|
| 配置示例 | `config/config.yaml.example` | 6 agent 类型穷举 + pipeline stages + evaluator + MCTS LLM params + security timeout |

---

## 4. 已知缺口

### 4.1 CLI STUB 命令

| 命令 | 子命令 | 状态 |
|------|--------|------|
| `ainspection run` | — | 完整 (P3-1) |
| `ainspection config` | get / set / validate | 3 个均 STUB |
| `ainspection session` | list / resume / rollback / branch / kill | 5 个均 STUB |
| `ainspection eval` | — | 完整 (P2-5) |

### 4.2 Pipeline 阶段 (P3-1: 全部接线完成)

| 阶段 | 状态 | 说明 |
|------|------|------|
| `get` | 完整 | LLM 调用通路 (CLI + LLM Native) |
| `locate` | 完整 | MCTS expander 已接入 rollout |
| `review#1/2/3` | 完整 | Evaluator LLM 双路径，门控审查 |
| `plan` | 完整 | Planner 规则式 + LLM 双路径 |
| `fix` | 完整 | MCTS fix expander + diff-validate |
| `verify` | 完整 | LLM tool call dispatch (5 skill) |
| `commit` | 完整 | LLM tool call dispatch (glab-mr + jira-update) |
| **CLI 接线** | **完整** | `cmd/run/run.go` 通过 `NewPipelineFromConfig` 接线 |

### 4.3 缺失交付物

| 交付物 | 归属 | 影响 |
|--------|------|------|
| ~~tools/diff-validate/~~ | ~~P1-9~~ | ~~已完成~~ |
| ~~tools/pprof-summary/~~ | ~~P2-2~~ | ~~已完成~~ |
| ~~internal/eval/~~ | ~~P2-5~~ | ~~已完成~~ |
| ~~deploy/alloy/~~ | ~~P2-1~~ | ~~已完成~~ |

### 4.4 Evaluator

`internal/orchestrator/evaluator_llm.go` 已实现 Evaluator LLM 双路径 (native Chat + CLI Run)，pipeline 中 `if p.evaluator != nil` 判断有实际实现。P3-1 Phase 1 传 nil 跳过 review 门控。

### 4.5 插桩标记

代码中 5 处 INSTRUMENT 注释 (wiring.go: NewPipelineFromConfig 新增)。其余关键决策点已有标记。

### 4.6 TraceID 端到端透传 (P3-2 完成)

| 环节 | 文件 | 说明 |
|------|------|------|
| CLI 入口 | `cmd/run/run.go` | `--trace-id` flag → `RunSpec.TraceID` |
| 持久化 | `internal/tree/tree.go` | `NewTask` 写入 `trace_id` 到 root `input.yaml` |
| get 阶段入 | `internal/orchestrator/pipeline.go` | `readTraceIDFromTask` → `GetInput.TraceID` → `get-system.tmpl` |
| get 阶段出 | `internal/orchestrator/pipeline.go` | `getOutputData.TraceID` → `writeGetInput` 写回 `input.yaml` |
| locate 阶段入 | `internal/orchestrator/pipeline.go` | `readLocateInput` 从 get 节点 `input.yaml` 读取 → `LocateInput.TraceID` |
| locate prompt | `prompts/locate-system.tmpl` | 跨服务调用链关联指引 (tempo → 最慢 hop → loki) |
| locate disclose | `prompts/locate-disclose.tmpl` | `trace_id` 字段 + trace 信号格式 |

所有新增字段默认空字符串，向后兼容。`--trace-id` 可选。模板 `{{if .TraceID}}` 块在空时不渲染。

---

## 5. 后续优先级

### P3 剩余

1. ~~**P3-2 跨服务调用链关联**~~ — 已完成: TraceID CLI → root input.yaml → get → locate 端到端透传
2. **P3-3 Evaluator/MCTS 全接线** — 将 Phase 1 nil 替换为完整 Evaluator + MCTS 实例
3. **CLI STUB 接线** — `config`/`session` 子命令接入真实逻辑
4. **端到端集成测试** — 完整流水线 smoketest (需要真实 LLM backend)

---

> 本文档随代码演进同步更新。每次重大里程碑后更新进度并执行 Conformance Check。
