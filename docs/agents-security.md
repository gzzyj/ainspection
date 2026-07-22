# Agent 安全设计

> 定义 ainspection Agent 的安全边界：命令白名单、文件系统隔离、敏感操作审批、沙箱执行。
> **权威规范见** [`../AGENTS.md`](../AGENTS.md) 第 3 章。

---

## 1. 设计原则

1. **默认拒绝**：Agent 只能执行预定义的命令，未列出的默认拒绝
2. **最小权限**：Agent 只能读写任务目录和项目源码目录
3. **敏感操作审批**：git push、skaffold deploy 等操作默认需用户确认
4. **纵深防御**：命令白名单 → 文件系统边界 → 审批门控 → 沙箱执行，四层递进
5. **沙箱即会话级 working dir**（**session ≡ 节点**）：每个 session 独占 `~/.ainspection/sessions/<id>/`，FSGuard 校验路径不越界；不依赖容器 runtime（详见 §5）

---

## 2. 命令白名单

### 2.1 配置定义

```yaml
# config/config.yaml
security:
  allowed_commands:
    # 版本控制
    - pattern: "git (status|diff|log|branch|checkout|add|commit)"
      auto_approve: true
    - pattern: "git push"
      auto_approve: false       # 需用户审批
    - pattern: "glab mr (create|view|list)"
      auto_approve: false

    # 构建部署
    - pattern: "go (build|vet|test|mod) .*"
      auto_approve: true
    - pattern: "skaffold (build|deploy|run)"
      auto_approve: false       # 部署需用户审批
    - pattern: "golangci-lint run .*"
      auto_approve: true

    # 集群操作（只读）
    - pattern: "kubectl (get|describe|logs|port-forward) .*"
      auto_approve: true
    - pattern: "kubectl (apply|delete|edit|exec) .*"
      auto_approve: false       # 写操作需审批

    # 代码质量
    - pattern: "gofumpt .*"
      auto_approve: true
    - pattern: "goimports .*"
      auto_approve: true

    # 工具
    - pattern: "diff-validate .*"
      auto_approve: true
    - pattern: "pprof-summary .*"
      auto_approve: true

  # 禁止的命令模式（即使匹配 allowed_commands 也拒绝）
  blocked_patterns:
    - ".*rm\\s+-rf.*"
    - ".*\\|.*"              # 禁止管道（防命令注入）
    - ".*&&.*"              # 禁止链式命令
    - ".*;.*"               # 禁止分号分隔
    - ".*\\$\\(.*"           # 禁止命令替换
    - ".*`.*"               # 禁止反引号
```

### 2.2 执行流程

```
Agent 发起命令
      │
      ▼
shlex 解析命令（防注入）
      │
      ▼
检查 blocked_patterns ──→ 匹配？ → 拒绝 + 记录日志
      │
      ▼ 不匹配
检查 allowed_commands ──→ 不匹配？ → 提示用户审批
      │
      ▼ 匹配
auto_approve=true？ ──→ 否 → 提示用户审批
      │
      ▼ 是
在沙箱中执行（如配置了 sandbox）
      │
      ▼
返回结果给 Agent
```

### 2.3 实现接口

```go
// internal/security/executor.go
type CommandExecutor interface {
    // Execute 执行命令，返回是否被允许
    Execute(ctx context.Context, cmd string) (ExecResult, error)
}

type ExecResult struct {
    Allowed      bool
    NeedsApproval bool
    Stdout       string
    Stderr       string
    ExitCode     int
}
```

---

## 3. 文件系统边界

### 3.1 读写范围

| 路径                                | 读 | 写 | 说明                       |
| ----------------------------------- | -- | -- | -------------------------- |
| `~/.ainspection/sessions/<sid>/`    | ✅ | ✅ | **会话级 working dir（沙箱独占）** |
| `~/.ainspection/tasks/<task-id>/`   | ✅ | ✅ | 任务工作目录               |
| `<services[].repo_path>/`           | ✅ | ✅ | 业务系统源码（生成 patch） |
| `~/.kube/config`                    | ✅ | ❌ | 只读 kubeconfig            |
| `<services[].repo_path>/.git/`      | ❌ | ❌ | 禁止直接操作 .git          |
| 系统目录 (`/etc/`, `/proc/`)       | ❌ | ❌ | 禁止访问                   |
| 网络路径                            | ❌ | ❌ | 禁止访问                   |

### 3.2 实现

```go
// internal/security/fsguard.go
type FSGuard interface {
    // Resolve 校验并解析路径，路径越界返回 error
    Resolve(taskID string, path string, op OpType) (string, error)
}

type OpType int
const (
    OpRead  OpType = iota
    OpWrite
)
```

---

## 4. 敏感操作审批

### 4.1 审批分级

| 等级                | 示例操作                                       | 审批要求                           |
| ------------------- | ---------------------------------------------- | ---------------------------------- |
| **L0 自动**   | `go build`, `git status`, `kubectl logs` | 无需审批                           |
| **L1 通知**   | `git commit`, `kubectl port-forward`       | 执行后通知用户                     |
| **L2 确认**   | `skaffold deploy`, `glab mr create`        | 弹窗确认（超时 60s 默认拒绝）      |
| **L3 双因素** | `git push --force`, `kubectl delete`       | 需用户在终端输入 `yes` + JIRA-ID（远程校验） |

**L3 JIRA-ID 校验**（D2）：

用户输入 JIRA-ID 后调 `jira-query` skill 校验三件，任一不通过 → 拒绝执行：

1. issue 远程存在
2. issue 状态为 `In Progress` 或 `Open`（非 Closed）
3. issue ID 与 `context.yaml.issue_ref` 匹配

**Jira 挡机降级**：jira-query 调用失败（超时 / 5xx / 网络错） → 降级为本地正则校验 `^[A-Z]+-\d+$` + 匹配 `context.yaml.issue_ref`，并在 `audit.log` 记 `warning: jira_unreachable_local_fallback`。

### 4.2 审批交互协议

```
Agent: 即将执行 skaffold deploy（L2 确认）
       部署服务: order-svc
       变更摘要: 添加 payments 表索引
       审批超时: 60s
       输入 y/n:
```

---

## 5. 沙箱执行（会话级 working dir）

每个 Agent session 启动时由 `internal/security/sandbox.go` 独占创建 working dir，实现"轻量但有效"的隔离：

```yaml
security:
  sandbox:
    enabled: true
    working_dir_root: "~/.ainspection/sessions/"
    hot_retention_days: 7
    archive_format: "tar.gz"
```

**生命周期**：

```
session 启动
  └─ Sandbox.SetupSession(sessionID)
       ├─ mkdir -p ~/.ainspection/sessions/<id>/{input,output,patches,signals,scratch}/
       └─ session.WorkingDir = "<root>/<id>/"
        ↓
session 运行（CommandExecutor 强制 cmd.Dir = WorkingDir；FSGuard 拒绝越界）
        ↓
session=completed
  └─ Sandbox.CleanupSession(sessionID)
       └─ 标记 hot 7 天，过期由后台 GC 压 tar.gz 长存
```

**与传统容器沙箱的取舍**：

- ✅ **采用**：每 session 独占目录、cwd 强制、FSGuard 路径校验、7 天压缩归档
- ❌ **不采用**：docker/podman 容器、namespace 隔离、seccomp、no_network；理由是 ainspection 在受信主机上运行（开发者本地或专用 jump host），容器 runtime 依赖会增加部署复杂度，收益有限

如果未来需要不可信主机执行，可在此基础上叠加 bubblewrap / rootless container 一层。

### 5.1 v2 沙箱目录增强

每个 session 的沙箱内增加 agent CLI 专属配置子目录和源码 symlink：

```
sessions/<session-id>/
├── .claude/skills/   # Claude CLI 原生 skill 文件 (Agent Adapter 注入)
├── .claude/hooks/    # Claude CLI 原生 hook 配置
├── .kimi/  .codex/  .qwen/  .gemini/   (按需创建)
├── input/  output/  patches/  signals/  scratch/
└── <service-name>/   # 源码 symlink → config.services[].repo_path (只读)
```

---

## 7. Hook 安全 (v2)

### 7.1 各 CLI Hook 支持差异

不同 Agent CLI 的 hook 机制不同，Security 模块需感知差异：

| Agent CLI | Hook 配置方式 | 支持状态 | ainspection 处理 |
|-----------|-------------|---------|-----------------|
| Claude Code | `.claude/hooks/` 目录 | 完整支持 | `InjectHooks()` 写入目录 |
| Kimi CLI | `config.toml` 中 `[[hooks]]` 数组 | 完整支持 | `InjectHooks()` 写入 TOML 格式的 `[[hooks]]` 条目 |
| Codex CLI | `.codex/hooks.json` | 完整支持 | `InjectHooks()` 写入 JSON 文件 |
| Qwen Code | `.qwen/hooks/` 目录 | **nightly 功能** | `Setup()` 时做可用性检测，不可用则跳过并记 warning 日志 |
| Gemini CLI | `.gemini/hooks/` 目录 | 完整支持 | `InjectHooks()` 写入目录 |

> Kimi hook 通过 `config.toml` 的 `[[hooks]]` TOML 数组格式定义。Qwen hook 是 nightly 功能，启动前应检测可用性，不可用则跳过 hook 注入。

### 7.2 Hook 注入安全约束

当 Agent Adapter 注入 hook 时，需满足以下约束：

1. **Hook 命令白名单**：hook 执行的命令必须在 `allowed_commands` 中注册
2. **路径约束**：hook 脚本只能读写沙箱目录内的路径（FSGuard 校验）
3. **超时限制**：单个 hook 执行超时由 `HookDef.TimeoutS` 控制，默认 30s
4. **审计记录**：每个 hook 的执行记录（事件/命令/结果）写入 `audit.log`
5. **Adapter 感知**：Hook 注入失败时应区分「不支持」和「注入错误」——`ErrNotSupported` 只记录 warning，注入错误记录 error 并阻塞 session 启动

### 7.3 支持的 Hook 事件

| 事件 | 触发时机 | 安全级别 |
|------|---------|---------|
| `pre_tool_use` | agent CLI 调用工具前 | L1 通知 |
| `post_tool_use` | agent CLI 调用工具后 | L1 通知 |
| `session_start` | agent CLI session 启动 | L0 自动 |
| `session_end` | agent CLI session 结束 | L0 自动 |
| `pre_command` | bash 命令执行前 | L2 确认（写操作） |

### 7.4 Hook 注入流程

```
HookDef (中立定义)
  │
  ▼
Agent Adapter 自身的 InjectHooks() 转为对应 CLI 原生 hook 格式
  │
  ├─ Claude: 写入 sandbox/.claude/hooks/
  ├─ Codex:  写入 sandbox/.codex/hooks.json
  ├─ Qwen:   写入 sandbox/.qwen/hooks/ (可用性检测)
  ├─ Gemini: 写入 sandbox/.gemini/hooks/
  └─ Kimi:   写入 config.toml 的 [[hooks]] 数组
  │
  ▼
agent CLI 启动时自动加载
  │
  ▼
Hook 执行 → security.CommandExecutor 校验 → audit.Log
```

---

## 8. Source Symlink 安全约束 (v2 新增)

### 8.1 symlink 安全规则

Source 挂载使用 symlink 方式，需满足以下约束：

1. **目标白名单**：symlink 目标路径必须在 `config.yaml services[].repo_path` 白名单内
2. **FSGuard 额外校验**：`ValidateSymlinkTarget()` 在创建 symlink 前校验目标：
   - 目标路径存在且为目录
   - 目标路径在白名单内（精确匹配，不允许路径穿越）
   - 目标路径不包含 `.git` 子路径（防止直接操作 git 内部）
3. **只读访问**：symlink 目标为只读，Agent 写操作仅限 `sandbox/patches/` 目录
4. **并发安全**：多个 session 同时访问同一 repo 为只读，无竞争条件
5. **依赖隔离**：Go 项目使用 `go mod vendor` 的 vendor 目录，不 clone 外部仓库

### 8.2 FSGuard 增强

```go
// ValidateSymlinkTarget 校验 symlink 目标路径是否安全。
// 目标必须在 services 白名单内，且不包含禁止路径。
func (g *FSGuard) ValidateSymlinkTarget(targetPath string) error {
    // 1. 解析为绝对路径
    abs, err := filepath.Abs(targetPath)
    if err != nil {
        return fmt.Errorf("symlink target resolution failed: %w", err)
    }

    // 2. 检查是否在白名单内
    if !g.isInServiceWhitelist(abs) {
        return fmt.Errorf("symlink target %s not in service whitelist", abs)
    }

    // 3. 拒绝 .git 路径
    if strings.Contains(abs, ".git") {
        return fmt.Errorf("symlink target contains .git: %s", abs)
    }

    return nil
}
```

### 8.3 与传统安全约束的关系

Source symlink 安全约束是现有 FSGuard 的扩展：
- 现有 FSGuard 校验 agent 发起的文件访问不越界（§3）
- 新增 `ValidateSymlinkTarget()` 在沙箱初始化阶段校验 symlink 目标合规（§8.2）
- CommandExecutor 强制 `cmd.Dir = session.WorkingDir`（不变）
- 审计日志继续记录所有文件访问和命令执行（不变）

---

## 6. 审计日志

`internal/security/audit.go` 是独立模块，提供 `Logger.Append(record)` 接口；CommandExecutor / FSGuard / SkillInjector 全部调用 audit 记录敏感操作。

```go
type Logger interface {
    Append(record Record) error
}

type Record struct {
    Ts        time.Time              `json:"ts"`
    TaskID    string                 `json:"task_id"`
    SessionID string                 `json:"session_id"`
    Agent     string                 `json:"agent"`
    Component string                 `json:"component"`  // command_executor | fs_guard | skill_injector
    Action    string                 `json:"action"`     // exec | read | write | inject
    Args      map[string]any         `json:"args"`
    Result    string                 `json:"result"`     // approved | blocked | failed
    Reason    string                 `json:"reason,omitempty"`
}
```

**输出**：jsonl 行式，按天轮转，30 天 retention：

```jsonl
{"ts":"2026-05-09T14:30:52Z","task_id":"20260509-143052-a3f2","session_id":"...-1-locate","agent":"claude","component":"command_executor","action":"exec","args":{"cmd":"git push"},"result":"approved","reason":"user_yes_jira_MS-1234"}
{"ts":"2026-05-09T14:31:03Z","task_id":"20260509-143052-a3f2","session_id":"...-1-locate","agent":"claude","component":"command_executor","action":"exec","args":{"cmd":"rm -rf /tmp/*"},"result":"blocked","reason":"blocked_pattern: .*rm\\s+-rf.*"}
```

文件位置 `~/.ainspection/audit/<yyyymmdd>.jsonl`；30 天后自动删除。
