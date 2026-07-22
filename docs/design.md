# 详细架构设计 (v2)

> 本文档是 `AGENTS.md` 的 L3 补充，详细描述 v2 新架构的技术设计。
> **重点**：adapter 适配层 + skill/hook 沙箱注入 + 硬编码消除。
> 保留不变的部分（tree/session/orchestrator/MCTS/Planner/Evaluator/离线评测）参见原有 agent 设计文档。
> **权威规范见** [`../AGENTS.md`](../AGENTS.md)。

---

## 1. 设计原则

1. **CLI 编排优先**：Go CLI 负责 agent session 生命周期，agent 通信通过 adapter 层封装原生 CLI
2. **复用成熟能力**：skill/hook/沙箱 由 agent CLI 原生提供，不重复造轮子
3. **session ≡ 节点**：树拓扑/状态机 + 运行时记录拼起来是同一个 session 实体的两面
4. **跨 agent 兼容**：Skill 定义用中立 markdown，injection 模块统一做格式转换
5. **会话级隔离**：每个 session 独占 working dir，skill/hook/config 写入沙箱相对路径
6. **文件系统即状态机**：树结构、节点、证据全部落地文件系统，零外部依赖

---

## 2. 技术栈

| 组件       | 技术选型                                | 用途                     |
| ---------- | --------------------------------------- | ------------------------ |
| CLI 框架   | `github.com/spf13/cobra`              | 命令行结构               |
| 配置管理   | `github.com/spf13/viper`              | config.yaml 加载         |
| Agent 通信 | 原生 agent CLI (子进程) + LLM Native HTTP (兜底) | 统一 adapter 接口 |
| Skill 解析 | Go `text/template` + YAML frontmatter | markdown Skill 解析      |
| 树存储     | 文件系统 (YAML/JSON)                    | 树节点持久化             |
| 摘要生成   | LLM (agent 自身)                        | 节点 summary.md 自动生成 |
| 文件锁     | `github.com/gofrs/flock`              | task.lock 跨平台互斥     |
| MCTS 引擎  | 自研 (UCB1)                             | 双阶段决策（locate/fix） |
| 沙箱       | 会话级 working dir + FSGuard            | 路径隔离 + skill/hook 注入 |

---

## 3. Agent Adapter 接口 (Plan B)

### 3.1 模块布局

```
internal/adapter/
├── types.go           # AgentAdapter 通用接口 + 共享类型 + Registry
├── injection.go       # SkillInjector / HookInjector 接口定义
├── llm_native.go      # HTTP LLM 客户端 (从 internal/llm/client.go 重构)
├── claude_cli.go      # Claude CLI 封装 (实现 AgentAdapter + SkillInjector + HookInjector)
├── kimi_cli.go        # Kimi CLI 封装
├── codex_cli.go       # Codex CLI 封装
├── qwen_cli.go        # Qwen CLI 封装
└── gemini_cli.go      # Gemini CLI 封装
```

### 3.2 核心接口

```go
// AgentAdapter agent CLI 或 LLM Native 的统一抽象。
// Registry 在 types.go 中维护 name → AgentAdapter 的映射。
type AgentAdapter interface {
    Name() string
    Type() AgentType  // claude_cli | kimi_cli | codex_cli | qwen_cli | gemini_cli | llm_native

    // Setup 在沙箱中准备 agent 运行环境：
    //   1. 创建 agent 专属配置子目录 (.claude/ / .kimi/ 等)
    //   2. 写入 LLM 配置 (endpoint / api_key / model → CLI 原生 settings 文件)
    //   3. 调用自身 InjectSkills() 注入 skill 文件
    //   4. 调用自身 InjectHooks() 注入 hook 配置
    Setup(ctx context.Context, sandboxPath string, cfg AgentSetupConfig) error

    // Run 在沙箱中启动 agent 执行任务。
    // CLI adapter: 子进程调用 agent CLI，使用固定 session ID。
    // LLM Native adapter: HTTP 直连 LLM API。
    Run(ctx context.Context, sandboxPath string, input AgentInput) (*AgentResult, error)

    // ParseOutput 解析 agent 原始输出为结构化数据。
    ParseOutput(raw []byte) (*AgentResult, error)
}

type AgentType string

const (
    AgentClaudeCLI  AgentType = "claude_cli"
    AgentKimiCLI    AgentType = "kimi_cli"
    AgentCodexCLI   AgentType = "codex_cli"
    AgentQwenCLI    AgentType = "qwen_cli"
    AgentGeminiCLI  AgentType = "gemini_cli"
    AgentLLMNative  AgentType = "llm_native"
)

// AgentSetupConfig 传递给 adapter.Setup 的配置。
type AgentSetupConfig struct {
    Endpoint     string
    APIKey       string
    Model        string
    Skills       []SkillDef
    Hooks        []HookDef
    SystemPrompt string
}

// AgentInput 传递给 adapter.Run 的输入。
type AgentInput struct {
    Prompt       string
    ContextFiles []string          // 沙箱内相对路径
    MaxTokens    int
    Temperature  float64
}

// AgentResult adapter 返回的结构化结果。
type AgentResult struct {
    Text       string
    ToolCalls  []ToolCall
    TokenUsage TokenUsage
    ExitCode   int               // 子进程退出码 (CLI adapter)
    RawOutput  []byte            // 原始输出 (用于审计)
}

type TokenUsage struct {
    Input  int64
    Output int64
}

// SkillDef 中立的 skill 定义（供 injection 模块转换）。
type SkillDef struct {
    Name        string
    Description string
    Parameters  []SkillParam
    Body        string          // skill 正文 (markdown)
    InjectionMode string       // "tools" | "messages" | "both" (LLM Native 专用)
}

// HookDef 中立的 hook 定义（供 injection 模块转换）。
type HookDef struct {
    Event    string            // pre_tool / post_tool / session_start / session_end
    Command  string            // 要执行的命令
    TimeoutS int               // 超时秒数
}
```

### 3.3 Injection 接口 (Plan B)

```go
// injection.go — SkillInjector / HookInjector 接口定义。
// 每个 Agent Adapter 自行实现这两个接口，处理各自 CLI 的格式差异。

// SkillInjector 将 SkillDef 列表转为 Agent Skill 文件并写入沙箱。
// 各 adapter 实现此接口，处理各自 CLI 的格式差异。
type SkillInjector interface {
    InjectSkills(sandboxPath string, defs []SkillDef) error
}

// HookInjector 将 HookDef 列表转为 Agent CLI 原生 hook 配置并写入沙箱。
type HookInjector interface {
    InjectHooks(sandboxPath string, defs []HookDef) error
}

// ErrNotSupported 在 adapter 不支持某功能时返回。
var ErrNotSupported = errors.New("not supported by this adapter")
```

### 3.4 各 Agent Adapter 实现要点

#### llm_native.go (HTTP 直连)
- 从 `internal/llm/client.go` 迁移重构
- 实现 `AgentAdapter` 接口，内部走 HTTP (OpenAI-compatible API)
- `Setup()` 将 system prompt + skill 描述拼成 messages
- `Run()` 直接 HTTP 调用，不走子进程
- 不实现 `SkillInjector` / `HookInjector`（不需要文件系统注入）
- SkillDef 两种处理方式由 `config.yaml` 中每个 skill 的 `injection_mode` 字段控制：
  - `tools`：SkillDef → OpenAI function-calling tool schema → HTTP `tools` 参数
  - `messages`：SkillDef name + description + body → system message
  - `both`：核心 skill 走 tools，辅助 skill 走 messages
- 作为其他 CLI adapter 不可用时的兜底

#### *_cli.go (5 个 agent CLI)
- 通过 `os/exec` 子进程调用 agent CLI
- 均实现 `AgentAdapter` + `SkillInjector` 接口
- CLI adapter 均实现 `HookInjector` 接口，各自处理 hook 格式差异
- `Setup()` 流程：
  1. 创建 agent CLI 专属子目录
  2. 写入 LLM 配置（settings.json / config.toml）
  3. 调用自身 `InjectSkills()` 写入 Agent Skill 文件
  4. 调用自身 `InjectHooks()` 写入 hook 配置
- `Run()` 构建 CLI 命令参数，使用固定 session ID，以沙箱目录为工作目录执行
- `ParseOutput()` 解析 CLI 的输出格式

### 3.5 各 Agent Adapter 的 Injection 行为总览

| Agent Adapter | InjectSkills 行为 | InjectHooks 行为 | LLM 配置写入 |
|--------------|------------------|------------------|-------------|
| `claude_cli.go` | 写入 `sandbox/.claude/skills/<name>/SKILL.md` | 写入 `sandbox/.claude/hooks/` | `.claude/settings.json` |
| `kimi_cli.go` | 写入 `sandbox/.kimi/skills/<name>/SKILL.md` | 写入 `config.toml` 的 `[[hooks]]` 数组 | `config.toml` |
| `codex_cli.go` | 写入 `sandbox/.codex/skills/<name>/SKILL.md` | 写入 `sandbox/.codex/hooks.json` | `config.toml` |
| `qwen_cli.go` | 写入 `sandbox/.qwen/skills/<name>/SKILL.md` | 写入 `sandbox/.qwen/hooks/` (nightly 功能，做可用性检测) | `settings.json` |
| `gemini_cli.go` | 写入 `sandbox/.gemini/skills/<name>/SKILL.md` | 写入 `sandbox/.gemini/hooks/` | `settings.json` |
| `llm_native.go` | 不需要（拼入 system prompt / tools） | 不需要（无 hook 机制） | 不走文件系统，HTTP headers 传递 |

### 3.6 Agent CLI 启动命令与 Session ID

所有 5 个 CLI 均支持自定义 session ID：

```go
// 各 CLI adapter Run() 方法中的命令构建模式

// Claude Code
exec.CommandContext(ctx, "claude", "--session", sessionID, "--resume", sessionID, "-p", prompt)

// Kimi CLI
exec.CommandContext(ctx, "kimi", "--session", sessionID, "--resume", sessionID, "-p", prompt)

// Codex CLI
exec.CommandContext(ctx, "codex", "--session", sessionID, "-p", prompt)

// Qwen Code
exec.CommandContext(ctx, "qwen", "--session", sessionID, "-p", prompt)

// Gemini CLI
exec.CommandContext(ctx, "gemini", "--resume", sessionID, "-p", prompt)
```

所有 CLI adapter 的 `cmd.Dir` 均设为 `sandboxPath`，使 skill/hook/config 从沙箱内自动加载。

---

## 4. Config 穷举

### 4.1 agents 段完整配置

```yaml
agents:
  # ─── Claude CLI ───
  claude:
    type: "claude_cli"                     # 通信方式
    cli_binary: "claude"                   # CLI 可执行文件名
    model: "claude-opus-4-6"
    skills_dir: ".claude/skills"           # 沙箱内 skill 目录（相对路径）
    hooks_dir: ".claude/hooks"             # 沙箱内 hook 目录
    settings_file: ".claude/settings.json" # LLM 配置文件名
    timeout: 120                           # 子进程超时 (秒)
    max_tokens: 4096
    temperature: 0.7
    endpoint: "${ANTHROPIC_BASE_URL}"
    api_key: "${ANTHROPIC_API_KEY}"

  claude_sonnet:
    type: "claude_cli"
    cli_binary: "claude"
    model: "claude-sonnet-4-6"
    skills_dir: ".claude/skills"
    hooks_dir: ".claude/hooks"
    settings_file: ".claude/settings.json"

  # ─── Kimi CLI ───
  kimi:
    type: "kimi_cli"
    cli_binary: "kimi"
    model: "kimi-k2"
    skills_dir: ".kimi/skills"
    hooks_dir: ".kimi"                      # hook 写入 config.toml [[hooks]] 数组
    settings_file: "config.toml"
    api_key_env: "MOONSHOT_API_KEY"         # Kimi 用环境变量

  # ─── Codex CLI ───
  codex:
    type: "codex_cli"
    cli_binary: "codex"
    model: "gpt-5.3-codex"
    skills_dir: ".codex/skills"
    hooks_dir: ".codex"                     # hooks.json 在 .codex/ 下
    hooks_file: "hooks.json"                # hook 配置文件名
    settings_file: "config.toml"
    features:                               # Codex 需要显式启用
      skills: true
      hooks: true

  # ─── Qwen CLI ───
  qwen:
    type: "qwen_cli"
    cli_binary: "qwen"
    model: "qwen3-max"
    skills_dir: ".qwen/skills"
    hooks_dir: ".qwen/hooks"                # nightly 功能，需做可用性检测
    settings_file: "settings.json"
    flags: ["--experimental-skills"]        # 启动参数

  # ─── Gemini CLI ───
  gemini:
    type: "gemini_cli"
    cli_binary: "gemini"
    model: "gemini-2.5-pro"
    skills_dir: ".gemini/skills"
    hooks_dir: ".gemini/hooks"
    settings_file: "settings.json"

  # ─── LLM Native (HTTP 直连) ───
  llm_native:
    type: "llm_native"
    endpoint: "${ANTHROPIC_BASE_URL}"       # OpenAI-compatible API
    api_key: "${ANTHROPIC_API_KEY}"
    model: "claude-opus-4-6"
    timeout: 120
    max_tokens: 4096
    temperature: 0.7
    headers:                                # LLM Native 专用
      anthropic-version: "2023-06-01"
    skill_injection_mode: "tools"           # tools | messages | both
```

### 4.2 配置字段说明

- **`type`**: 决定使用哪个 Agent Adapter。可选值: `claude_cli | kimi_cli | codex_cli | qwen_cli | gemini_cli | llm_native`。agent name 与 Agent Adapter 的映射是 1:1 的——`claude` 只能用 `claude_cli` adapter。`type` 字段显式选择通信方式，无需单独的 `adapter` 字段。
- **`skills_dir` / `hooks_dir` / `settings_file`**: 沙箱内的相对路径，Agent Adapter 在 `Setup()` 时使用
- **`llm_native`**: 不需要 CLI 路径相关字段，但有 HTTP 相关字段
- **Kimi**: hook 通过 `config.toml` 的 `[[hooks]]` 数组定义，`InjectHooks()` 生成 TOML 格式并写入 `settings_file`
- **Qwen**: hooks 是 nightly 功能，`Setup()` 启动前检测可用性，不可用则跳过 hook 注入
- **Codex**: 需要在 `features` 中显式启用 skill/hook

### 4.3 其他 config 段

```yaml
# security 段
security:
  allowed_commands: [ ... ]                 # 命令白名单 (保持不变)
  baseline_commands:                        # 基线验证命令
    - "go build ./..."
    - "go vet ./..."
  command_timeout: 30                       # 命令执行超时 (秒)

# retry 段
retry:
  max_per_stage: 2
  llm_max_retries: 2                        # LLM 无效输出独立重试
  fallback_agent: "qwen"

# mcts 段
mcts:
  locate:
    max_iterations: 16
    max_depth: 4
    weights:
      correctness: 0.35
      evidence: 0.30
      verifiability: 0.20
      impact: 0.15
  fix:
    max_iterations: 8
    max_depth: 3
    weights:
      completeness: 0.35
      quality: 0.25
      compliance: 0.20
      security: 0.20
  ucb_c: 1.41

# services 段 — 源码仓库路径（用于 symlink 挂载）
services:
  - name: "kernel"
    repo_path: "/data/src_repo/kernel"
  - name: "qtmf"
    repo_path: "/data/src_repo/qtmf"
```

---

## 5. 沙箱增强

### 5.1 目录结构

```
~/.ainspection/sessions/<session-id>/
├── .claude/                  # Claude CLI 配置 (按需创建)
│   ├── skills/               # Agent Skill 文件 (注入)
│   │   ├── jira-query.md
│   │   ├── kubectl-inspect.md
│   │   └── ...
│   ├── hooks/                # Claude 原生 hook 配置
│   └── settings.json         # Claude CLI LLM 配置
├── .kimi/                    # Kimi CLI 配置 (按需)
├── .codex/                   # Codex CLI 配置 (按需)
├── .qwen/                    # Qwen CLI 配置 (按需)
├── .gemini/                  # Gemini CLI 配置 (按需)
├── input/                    # 节点输入文件
├── output/                   # 节点输出文件
├── patches/                  # diff 文件 (Agent 写操作唯一目录)
├── signals/                  # 采集的信号数据
├── scratch/                  # 临时工作文件
└── <service-name>/           # 源码 symlink → config.services[].repo_path (只读)
```

### 5.2 Source 挂载

- **方式**: symlink，只读
- **路径**: `sandbox/<service-name>/` → `config.yaml services[].repo_path`
- **安全**: FSGuard 对 symlink 目标路径做额外校验，确保目标在 `services[].repo_path` 白名单内
- **并发**: 多个 session 同时访问同一 repo 为只读，写操作仅在 `sandbox/patches/` 目录
- **依赖**: Go 项目使用 `go mod vendor` 的 vendor 目录，不 clone 外部仓库

### 5.3 沙箱初始化流程

```go
func (s *Sandbox) SetupSession(ctx context.Context, sessionID string, agentName string, cfg AgentSetupConfig) (string, error) {
    sandboxPath := filepath.Join(s.root, sessionID)

    // 1. 创建标准子目录
    for _, sub := range []string{"input", "output", "patches", "signals", "scratch"} {
        os.MkdirAll(filepath.Join(sandboxPath, sub), 0755)
    }

    // 2. symlink source → config.services[].repo_path (只读校验)
    for _, svc := range cfg.Services {
        target := filepath.Join(sandboxPath, svc.Name)
        if err := s.fsGuard.ValidateSymlinkTarget(svc.RepoPath); err != nil {
            return "", fmt.Errorf("symlink target rejected: %w", err)
        }
        os.Symlink(svc.RepoPath, target)
    }

    // 3. 获取 adapter = Registry[agentName]
    adapter := s.adapterRegistry[agentName]

    // 4. adapter.Setup → 写入 LLM 配置 + 注入 skill/hook
    if err := adapter.Setup(ctx, sandboxPath, cfg); err != nil {
        return "", fmt.Errorf("adapter setup: %w", err)
    }

    return sandboxPath, nil
}
```

### 5.4 安全约束不变

- CommandExecutor 强制 `cmd.Dir = session.WorkingDir`
- FSGuard 校验路径必须在 working_dir 或 `services[].repo_path` 内
- 审计日志 jsonl 按天轮转，30 天 retention
- 上述逻辑与 v1 保持一致，仅增加 agent CLI 子进程的审计

---

## 6. 硬编码消除

### 6.1 消除清单

| 位置 | 旧硬编码 | 配置化方案 |
|------|---------|-----------|
| `internal/llm/client.go` | HTTP headers (`x-api-key`, `anthropic-version`) | `config.yaml agents.<name>.headers` |
| `internal/llm/client.go` | 默认 timeout (120s) | `config.yaml agents.<name>.timeout` |
| `internal/llm/client.go` | 默认 max_tokens (4096) | `config.yaml agents.<name>.max_tokens` |
| `internal/llm/client.go` | 默认 temperature (0.7) | `config.yaml agents.<name>.temperature` |
| `internal/skill/adapter_*.go` | tool schema 结构 | Agent Adapter 注入时动态生成 |
| `internal/mcts/engine.go` | 默认 UCB c (1.41) | `config.yaml mcts.ucb_c` |
| `internal/mcts/engine.go` | 默认 budget (iter=16, depth=4) | `config.yaml mcts.locate/fix` |
| `internal/session/monitor.go` | 默认阈值 (40%) | `config.yaml context.threshold_pct` |
| `internal/session/baseline.go` | 基线命令 (`go build`, `go vet`) | `config.yaml security.baseline_commands` |
| `internal/security/executor.go` | 超时 30s | `config.yaml security.command_timeout` |
| `internal/orchestrator/mcts_scorer.go` | 评分权重 | `config.yaml mcts.{locate,fix}.weights` |
| `internal/orchestrator/pipeline.go` | LLM invalid output 重试 (2) | `config.yaml retry.llm_max_retries` |
| `internal/adapter/llm_native.go` | 默认 model / headers | 全部从 AgentSetupConfig 获取 |

### 6.2 硬编码消除原则

- 所有可调参数必须在 `config.yaml` 中有对应配置段
- `config.yaml` 中所有值必须有默认值（`config.go` 中 `SetDefault`）
- 环境变量引用使用 `${ENV_VAR}` 语法，由 viper 自动展开
- 新增配置段需在 `ainspection config validate` 中校验

### 6.3 注意：不再有 `adapter` 字段

旧版设计中 config.yaml 包含 `agents.<name>.adapter` 字段用于选择 adapter 类型。v2 细化后，agent name 与 Agent Adapter 的映射是 1:1 的（`claude` → `claude_cli`，`kimi` → `kimi_cli`），不再需要额外的 `adapter` 配置字段。仅需 `type` 字段显式选择通信方式（`claude_cli` / `llm_native`）。

---

## 7. 模块迁移计划

### 阶段 1: 新模块创建 (不影响现有代码)
1. 创建 `internal/adapter/types.go` (接口定义)
2. 创建 `internal/adapter/injection.go` (合并旧 adapter_*.go 逻辑)
3. 创建 `internal/adapter/{claude,kimi,codex,qwen,gemini}_cli.go` (CLI 封装)

### 阶段 2: 旧模块迁移
4. `internal/llm/client.go` → `internal/adapter/llm_native.go` (迁移 + 实现 AgentAdapter)
5. `internal/skill/adapter_{claude,kimi,qwen}.go` → 合并到 `injection.go` 后删除
6. `internal/skill/native.go` → 删除

### 阶段 3: 集成与切换
7. `internal/skill/injector.go` → 重构为调用 injection
8. `internal/skill/executor.go` → 简化分发逻辑
9. `internal/orchestrator/pipeline.go` → LLM 调用改为通过 adapter

---

## 8. 保留不变的部分

以下模块的设计与实现与 v1 保持一致，详见各 agent 设计文档：

| 模块 | 文档 | 变更 |
|------|------|------|
| Tree 管理 | `docs/task-context.md` | 不变 |
| Session 管理 | `docs/agents-workflow.md` | 不变 |
| MCTS 引擎 | `docs/agents-mcts.md` | 不变 |
| Planner | `docs/agents-planner.md` | 不变 |
| Evaluator | `docs/agents-evaluator.md` | 不变 |
| 安全沙箱 | `docs/agents-security.md` | 局部更新 (新增 hook 安全) |
| 离线评测 | `docs/agents-eval.md` | 不变 |

---

> 本文档随 AGENTS.md 同步更新。v2 重点：§3 adapter + §4 skill 重构 + §5 沙箱增强 + §6 硬编码消除。
