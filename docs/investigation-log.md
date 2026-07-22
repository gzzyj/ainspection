# investigation.log.yaml Schema

> 定义 `investigation.log.yaml` 的格式、位置、写入者和生命周期。
> 用于记录 session 基线验证失败、review 退回、阶段重试超限等异常事件的完整审计线索。

---

## 1. 位置

```
~/.ainspection/tasks/<task-id>/investigation.log.yaml
```

- **task 级别**（非 session 级别）：一个 task 的所有异常事件记录在同一个文件中
- **追加模式**：每次异常事件作为新的 entry 追加到 entries 列表末尾
- 文件随 task 创建时初始化（空 entries 列表）

---

## 2. Schema

```yaml
# investigation.log.yaml
task_id: "20260510-093015-a3f2"
entries:
  - ts: "2026-05-10T09:35:12+08:00"
    level: error                    # error | warning | info
    source: baseline                # 写入者标识
    stage: ""                       # 关联的阶段（空表示 session 启动阶段）
    session_id: "20260510-093015-a3f2-1-generator"
    node_id: "root"
    category: baseline_failure      # 事件分类（见 §3）
    summary: "go build 失败：./internal/handler/order.go:45: syntax error"
    detail:                         # 结构化详情（category 不同则字段不同）
      command: "go build ./..."
      exit_code: 1
      stderr: "./internal/handler/order.go:45: syntax error: unexpected newline"
    retry_count: 0                  # 当前阶段的已有重试次数
```

---

## 3. 事件分类 (category)

| category | 触发条件 | 写入者 | level |
|----------|---------|--------|-------|
| `baseline_failure` | session 启动时 go build/vet/test 失败 | `internal/session/baseline.go` | error |
| `review_rejected` | Evaluator review 不通过 | `internal/orchestrator/pipeline.go` | warning |
| `review_blocked` | 同一阶段 review 2 次不通过，转人工 | `internal/orchestrator/pipeline.go` | error |
| `stage_retry_exceeded` | 任务级重试超限（retry_count >= max_per_stage） | `internal/orchestrator/pipeline.go` | error |
| `llm_invalid_output` | LLM 返回无法解析的输出 | `internal/orchestrator/pipeline.go` | warning |
| `llm_fallback` | LLM 无效输出降级到备选 agent | `internal/orchestrator/pipeline.go` | warning |
| `confidence_diverged` | confidence \|self - evaluator\| > 阈值 | `internal/orchestrator/evaluator.go` | warning |
| `approval_denied` | 用户拒绝敏感操作审批 | `internal/security/executor.go` | info |
| `sandbox_violation` | FSGuard 拒绝越界访问 | `internal/security/fsguard.go` | error |
| `command_blocked` | 命令被白名单/blocked_patterns 拒绝 | `internal/security/executor.go` | warning |

---

## 4. detail 字段（按 category）

### baseline_failure

```yaml
detail:
  command: "go build ./..."
  exit_code: 1
  stderr: "<完整的 stderr 输出>"
```

### review_rejected / review_blocked

```yaml
detail:
  review_stage: "review1"         # review1 | review2 | review3
  score: 5
  blockers: ["migration 文件名不符合规范"]
  warnings: ["建议确认索引区分度"]
  report_path: "nodes/n3-review-1/output.yaml"
```

### stage_retry_exceeded

```yaml
detail:
  stage: "fix"
  retry_count: 2
  max_per_stage: 2
  last_error: "go build 失败"
```

### llm_invalid_output

```yaml
detail:
  agent: "claude"
  invalid_reason: "expected JSON object, got malformed yaml at line 5"
  retry_count: 2
  fallback_agent: "qwen"
```

### llm_fallback

```yaml
detail:
  previous_agent: "claude"
  fallback_agent: "qwen"
  invalid_reason: "JSON 解析失败连续 2 次"
```

### confidence_diverged

```yaml
detail:
  finding_hypothesis: "payment-svc 慢查询"
  confidence_self: 0.92
  confidence_evaluator: 0.65
  divergence: 0.27
  threshold: 0.2
```

### approval_denied

```yaml
detail:
  command: "skaffold deploy"
  reason: "user_timeout"          # user_timeout | user_denied | jira_verification_failed
```

### sandbox_violation

```yaml
detail:
  path: "/etc/passwd"
  operation: "read"
  session_working_dir: "~/.ainspection/sessions/xxx/"
```

### command_blocked

```yaml
detail:
  command: "rm -rf /tmp/cache"
  blocked_by: "blocked_pattern: .*rm\\s+-rf.*"
```

---

## 5. 写入接口

```go
// InvestigationLogger 异常事件日志写入接口。
// 实现位置：internal/session/investigation.go
type InvestigationLogger interface {
    // Append 追加一条异常事件到 task 的 investigation.log.yaml。
    Append(taskID string, entry InvestigationEntry) error

    // Get 读取 task 的全部异常事件。
    Get(taskID string) ([]InvestigationEntry, error)
}

type InvestigationEntry struct {
    TS         time.Time         `yaml:"ts"`
    Level      string            `yaml:"level"`
    Source     string            `yaml:"source"`
    Stage      string            `yaml:"stage"`
    SessionID  string            `yaml:"session_id"`
    NodeID     string            `yaml:"node_id"`
    Category   string            `yaml:"category"`
    Summary    string            `yaml:"summary"`
    Detail     map[string]any    `yaml:"detail"`
    RetryCount int               `yaml:"retry_count"`
}
```

---

## 6. 生命周期

- **创建**：task 创建时初始化空的 investigation.log.yaml
- **追加**：每次异常事件由对应的写入者调用 `Append()`
- **读取**：`ainspection session list` 可展示异常历史；`ainspection doctor` 可检查
- **清理**：随 task 目录一起管理（P0 不实现 GC）
- **不可变性**：entries 仅追加，不修改、不删除历史条目
