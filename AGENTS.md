# AGENTS.md — AI Inspection Agent (v2)

> `ainspection` 是基于 Go CLI 编排多 Agent 的自动化问题检测与修复系统。
> 场景：报错/性能告警 → 多源信号采集 → AI 定位根因 → **独立 Evaluator 审查** → 修复 → 部署验证 → MR。
> **v2 架构定位**：CLI 封装 agent 生命周期 + 树形上下文（**session ≡ 节点**）+ **生成-评估分离 + 双阶段 MCTS**。Agent 通信通过 Agent Adapter（`internal/adapter/`）封装原生 CLI（claude/kimi/codex/qwen/gemini），SkillDef/HookDef 在沙箱中以原生 CLI 格式注入。

## 0. 术语标准

为避免歧义，本项目统一使用以下术语：

| 术语 | 含义 | 曾用名（废弃） |
|------|------|---------------|
| **SkillDef** | ainspection 中立的 skill 定义 (`.skills/*.md` frontmatter + body) | skill, Skill, 业务 skill |
| **Agent Skill** | 经由 injection 转换后写入沙箱的 CLI 原生 skill 文件 | skill 文件, 原生 skill |
| **SkillDef Loader** | `internal/skill/loader.go`，解析 `.skills/*.md` | skill loader |
| **Agent Adapter** | `internal/adapter/*.go`，封装单个 agent CLI 或 LLM Native | adapter, CLI adapter |
| **Skill/Hook Injection** | 将 SkillDef/HookDef 写入沙箱对应目录的过程 | inject, injection |
| **Session** | ainspection 沙箱内的一个 agent 执行周期 | (统一用 Session) |
| **Agent CLI Session** | agent CLI 自身维护的会话状态（对话历史、上下文等），通过 `--session` flag 控制 | — |
| **Sandbox** | 会话级隔离目录 (`~/.ainspection/sessions/<id>/`)，含 skill/hook/config 注入 | 沙箱, working dir |

### 「adapter」一词的用法统一

- **config.yaml**: 不需要 `agents.<name>.adapter` 字段。agent name 与 Agent Adapter 的映射是 1:1 的——`claude` 一定用 `claude_cli` adapter，`kimi` 一定用 `kimi_cli` adapter。配置中只需要 `type` 字段显式选择通信方式：
  ```yaml
  agents:
    claude:
      type: "claude_cli"    # 或 "llm_native"，显式选择通信方式
      model: "claude-opus-4-6"
  ```
- **Go 代码**: `AgentAdapter` 是 Go 接口名，`adapter.Registry` 是按 agent name 查找 Agent Adapter 的注册表
- **文档**: 说「adapter」时指代整个 `internal/adapter/` 包；说「Agent Adapter」时指代单个 agent 的封装

### Agent CLI 能力调研基线

所有 5 个 Agent CLI 均支持 markdown-based `SKILL.md` skill 格式，SkillDef 可直接写入各 CLI 对应目录：

| CLI | Skill 路径 | Hook 配置 | Session ID |
|-----|-----------|----------|-----------|
| Claude Code | `.claude/skills/<name>/SKILL.md` | `.claude/hooks/` 目录 | `--session-id`, `--resume` |
| Kimi CLI | `.kimi/skills/<name>/SKILL.md` | `~/.kimi/config.toml` 中 `[[hooks]]` 数组 | `--session`, `--resume` |
| Codex CLI | `.codex/skills/<name>/SKILL.md` | `.codex/hooks.json` | `--resume` |
| Qwen Code | `.qwen/skills/<name>/SKILL.md` | `.qwen/hooks/` (nightly) | `--session-id`, `--resume` |
| Gemini CLI | `.gemini/skills/<name>/SKILL.md` | `.gemini/hooks/` | `--session-id`, `--resume` |

> Hook 格式不统一，每个 Agent Adapter 需单独实现格式转换。

### Agent CLI 启动命令统一

| CLI | 命令 | LLM 配置文件 |
|-----|------|-------------|
| Claude Code | `claude` | `.claude/settings.json` |
| Kimi CLI | `kimi` | `config.toml` |
| Codex CLI | `codex` | `~/.codex/config.toml` |
| Qwen Code | `qwen` | `~/.qwen/settings.json` |
| Gemini CLI | `gemini` | `~/.gemini/settings.json` |

## 1. 架构全景

```
                          User
              CLI (ainspection run/config/...)
                         │
   ┌─────────────────────┼─────────────────────┐
   ▼                     ▼                     ▼
┌──────────┐     ┌──────────────┐     ┌──────────────┐
│Generator │     │  Evaluator   │     │   Planner    │
│(多Agent) │     │ (独立模型)     │     │ (独立模型)    │  ← 生成-评估分离
└────┬─────┘     └──────┬───────┘     └──────┬───────┘
     └──────────────────┼───────────────────┘
                  ┌──────┴──────┐
                  │ Tree Context│  ← session ≡ 节点 + 主动 Context Reset
                  └──────┬──────┘
                         │
   ┌─────────────────────┼─────────────────────┐
   ▼                     ▼                     ▼
sources:              Sandbox               sink:
k3s / Loki /    会话级隔离 + Skill/Hook    GitLab (MR)
Prom / Tempo /   注入 + Audit 审计          + Jira Update
Jira / Feishu

   ─ ─ ─ ─ ─  底层通信 (adapter)  ─ ─ ─ ─ ─
   claude CLI / kimi CLI / codex CLI / qwen CLI / gemini CLI / LLM Native
```

> Agent CLI 是通信手段。业务架构关注 Generator/Evaluator/Planner 在问题解决流程中的协作。

## 2. 关键设计决策

| 维度       | 传统做法        | ainspection v2                                               |
| ---------- | --------------- | ------------------------------------------------------------ |
| Agent 架构 | 单 Agent 串行   | **生成-评估分离**：Generator + Evaluator + Planner（独立 session/模型） |
| Agent 通信 | 直连 LLM HTTP API | **Agent Adapter** 封装 5 个 agent CLI + 1 个 LLM Native，复用成熟能力 |
| SkillDef 注入 | HTTP tools 参数 | SkillDef Loader 解析 markdown → Agent Adapter 注入为各 CLI 原生 Agent Skill → **写入沙箱相对路径** |
| Hook 注入 | 无              | HookDef → Agent Adapter 转为各 CLI 原生 hook 格式 → **写入沙箱** |
| 安全模型   | 无限制          | **命令白名单** + 会话级 working dir + FS 边界 + 敏感操作审批  |
| 上下文模型 | 扁平 + 全量传递 | **session ≡ 节点** + 节点摘要 + 主动 Context Reset           |
| 计划格式   | Markdown        | **JSON Schema**（机器可解析，跨 session 无损）               |
| 决策框架   | 用户临时选择    | **双阶段 MCTS**（locate 假设空间 + fix 修复方案空间）+ Evaluator 评分 |
| Agent 兼容 | 单一平台        | 多 agent CLI（Claude / Kimi / Codex / Qwen / Gemini）        |

## 3. 核心约束（不可违反）

1. **生成-评估分离** — fix 后独立 Evaluator 审查；**命令白名单** — 只能执行预定义命令
2. **基线验证** — session 启动时 `go build && go vet`；**FS 边界** — 仅读写 working dir + 源码
3. **重试上限** — 每阶段 ≤2 次，超限转人工；**Linter 门控** — verify 前 golangci-lint 通过
4. **主动 Context Reset** — 节点完成后新 session 冷启动；**JSON 计划** — `plan` 字段 JSON Schema
5. **用户掌舵** — `expectation_locked` 硬前置条件 + 敏感操作审批
6. **session ≡ 节点** — 节点是 session 的状态机面，runtime 是其运行时面；CLI rollback/branch 仅动状态机

## 4. 工作流与阶段门控

```
Session 启动 (基线验证) → get → locate (MCTS) → [Review#1] → Planner → fix (MCTS) → [Review#2] → verify → [Review#3] → commit
```

| 阶段        | Agent                | 门控                   | 不满足时           |
| ----------- | -------------------- | ---------------------- | ------------------ |
| get         | Generator            | `scope_defined`        | 提示用户           |
| locate      | Generator + MCTS     | `expectation_locked`   | 等待确认           |
| review #1-3 | **Evaluator** (独立) | 审查通过               | 退回上阶段 (≤2次)  |
| plan        | **Planner** (独立)   | plan.json 生成         | 重试               |
| fix         | Generator + MCTS     | diff + build + lint    | 回退修正           |
| verify      | Generator            | LLM dispatch (lint/build/test/vet/deploy/probe) | 分析+重试          |
| commit      | Generator            | verify 通过 + 审批     | 保留现场           |

## 5. SkillDef 与 Hook 注入

### 5.1 注入架构 (Plan B)

Injection 接口定义在 `internal/adapter/types.go`，每个 Agent Adapter 自行实现：

```go
// SkillInjector 将 SkillDef 列表转为 Agent Skill 文件并写入沙箱。
type SkillInjector interface {
    InjectSkills(sandboxPath string, defs []SkillDef) error
}

// HookInjector 将 HookDef 列表转为 Agent CLI 原生 hook 配置并写入沙箱。
type HookInjector interface {
    InjectHooks(sandboxPath string, defs []HookDef) error
}
```

各 Agent Adapter 实现行为：

| Adapter | InjectSkills 行为 | InjectHooks 行为 |
|---------|------------------|------------------|
| `claude_cli.go` | 写入 `sandbox/.claude/skills/<name>/SKILL.md` | 写入 `sandbox/.claude/hooks/` |
| `kimi_cli.go` | 写入 `sandbox/.kimi/skills/<name>/SKILL.md` | 写入 `config.toml` 的 `[[hooks]]` 数组 |
| `codex_cli.go` | 写入 `sandbox/.codex/skills/<name>/SKILL.md` | 写入 `sandbox/.codex/hooks.json` |
| `qwen_cli.go` | 写入 `sandbox/.qwen/skills/<name>/SKILL.md` | 写入 `sandbox/.qwen/hooks/` (nightly，需可用性检测) |
| `gemini_cli.go` | 写入 `sandbox/.gemini/skills/<name>/SKILL.md` | 写入 `sandbox/.gemini/hooks/` |
| `llm_native.go` | 不需要：SkillDef description 拼入 system prompt | 不需要：LLM Native 无 hook 机制 |

### 5.2 LLM Native adapter 的 SkillDef 处理

LLM Native 不支持文件系统 skill 注入，两种方式：

| 方式 | 做法 | 优劣势 |
|------|------|--------|
| **Tools 参数** | SkillDef → OpenAI function-calling tool schema → HTTP `tools` 参数 | LLM 可自行决定调用时机；token 消耗大 |
| **Messages 拼接** | SkillDef name + description + body → system message | token 消耗小；LLM 可能遗忘长上下文 |

**建议**: 同时使用两种方式。核心 skill 走 **Tools 参数**，辅助说明类 skill 走 **Messages 拼接**。由 `config.yaml` 中每个 skill 的 `injection_mode` 字段控制。

### 5.3 Agent CLI 启动（固定 Session ID）

各 Agent Adapter 的 `Run()` 方法按 CLI 实际能力使用 session flag：

| CLI | Prompt Flag | Session Flag |
|-----|------------|--------------|
| Claude | `-p, --print` | `--session-id <id> --resume <id>` |
| Kimi | `--print` | `--session <id> --resume <id>` |
| Codex | positional `[PROMPT]` | `--resume <id>` |
| Qwen | `-p, --prompt` | `--session-id <id> --resume <id>` |
| Gemini | `-p, --prompt` | `--session-id <id> --resume <id>` |

示例（Claude CLI）：

```go
func (a *ClaudeAdapter) Run(ctx context.Context, sandboxPath string, input AgentInput) (*AgentResult, error) {
    cmd := exec.CommandContext(ctx, "claude",
        "--session", input.SessionID,
        "--resume", input.SessionID,
        "-p", input.Prompt,
    )
    cmd.Dir = sandboxPath  // 工作目录 = 沙箱，skill/hook 自动加载
    // ...
}
```

### 5.4 沙箱目录结构

```
沙箱目录 (~/.ainspection/sessions/<sid>/)：
├── .claude/skills/*.md    # Claude CLI 原生 skill (Agent Skill)
├── .claude/hooks/         # Claude CLI 原生 hook 配置
├── .claude/settings.json  # Claude CLI LLM 配置
├── .kimi/   .codex/   .qwen/   .gemini/   (按 agent 类型)
├── input/  output/  patches/  signals/  scratch/
└── <service-name>/         # 源码 symlink → config.services[].repo_path (只读)
```

> STATUS: implemented

### 5.5 沙箱初始化流程

```
Sandbox.SetupSession(sessionID, agentName)
  │
  ├── 1. mkdir 标准子目录 (input/output/patches/signals/scratch/)
  ├── 2. symlink source → config.services[].repo_path (只读校验)
  ├── 3. 获取 adapter = Registry[agentName]
  ├── 4. adapter.Setup(sandboxPath)
  │      ├── 写入 LLM 配置 (endpoint/api_key/model → CLI 原生 settings 文件)
  │      ├── adapter.InjectSkills(sandboxPath, skillDefs)
  │      └── adapter.InjectHooks(sandboxPath, hookDefs)
  └── 5. 返回 sandboxPath
```

> STATUS: implemented

## 6. 项目自身 AI 研发流程

ainspection 自身使用 AI 开发，依赖两个研发流程工具：

### 6.1 progress.json（项目根目录）
跟踪每个开发任务的状态、使用的 agent、token 消耗、限流状态和 conformance 结果。
任务完成后自动更新，提供 LLM 限流感知（退避 / 切换 agent / 降级模式）。

### 6.2 Conformance Check（⛔ 阻塞性步骤，每次任务完成后强制执行）

> **⛔ 本检查不可跳过、不可延后、不可被 `go build && go test` 替代。**
> 编译通过不代表设计一致。违反此条的任何 agent 视为流程违规。

检查本项目 **规范文档 vs 代码实现** 的一致性。

**触发条件**: 任何涉及代码修改的任务完成后（包括但不限于：新建文件、删除文件、接口/类型变更、重构）。

**执行方式**: AI agent 逐项检查，对照 AGENTS.md 和 docs/ 中的设计。具体步骤：
1. **规范偏离检查** — 对照 AGENTS.md 和 docs/ 中的设计，检查本次修改的代码是否与设计一致
   - 新增模块是否在 docs/ 中有对应的设计文档？
   - 接口签名是否与 design.md 中定义的一致？**（必须逐字段核对，包括字段名、类型、tag）**
   - 模块布局是否与设计文档中的目录结构一致？
   - 模块依赖关系是否与架构图一致？
   - **实现行为是否与设计文档中的流程描述一致？**（如 Setup 的 4 步流程、Run 的命令参数等）
2. **职责偏移检查** — 新增代码是否在 ainspection 核心职责范围内？
3. **插桩完整性检查** — 关键决策点是否有 INSTRUMENT 标记？（详见 §6.3）

**结果记录**: 更新 `progress.json` 的 `conformance` 字段：
- 通过 → `conformance.last_check` 更新，`total_violations` 清零，更新 BASELINE.md
- 失败 → 记录具体 violation，**先修复再标记任务完成**

**plan 模式交叉引用**: 如果任务涉及 plan 模式设计的方案，需确认：
1. plan 文件中的设计方案已全部实现或被显式放弃
2. 方案中涉及的文档交叉引用已更新
3. `progress.json` 中任务状态与 plan 文件一致

### 6.3 插桩标记规范

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

## 7. CLI 与项目结构

```
CLI 命令                              项目源码 (Go)
ainspection {run|config|doctor}       cmd/ainspection/main.go
ainspection session                   internal/{orchestrator,tree,session, adapter,skill,mcts,planner, security,audit,config}/
   {list|resume|rollback|branch|kill} .skills/×11   prompts/×14
ainspection eval (离线评测 P2)         tools/{diff-validate,pprof-summary}/         
                                      docs/   config/config.yaml.example
                                      progress.json   ← 项目研发进度                                   
```

## 8. 文档索引

| 文档                                                              | 内容                                              |
| ----------------------------------------------------------------- | ------------------------------------------------- |
| `docs/agents-{security,evaluator,workflow,mcts,planner,eval}.md`  | 各 Agent 详细规范（含双阶段 MCTS、Planner、离线评测） |
| `docs/{design,todo,BASELINE}.md`                                  | 技术栈与逐任务设计、P0–P3 清单、基线状态          |
| `docs/{task-context,workflow}.md`                                 | 树 Schema、阶段流程与门控                         |
| `progress.json`                                                   | 项目研发进度跟踪（任务状态 + 限流 + conformance） |

## 9. 参考

Anthropic: *"agents grade their own work too generously"* | Harness: *"structured artifacts are the solution"* | OpenAI Codex: Plan→Implement→Doc 分层

> 详细规范见 `docs/`。
