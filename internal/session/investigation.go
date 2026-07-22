package session

import (
	"fmt"
	"os"
	"path/filepath"
	"time"

	"gopkg.in/yaml.v3"
)

// InvestigationEntry 一条异常事件记录（对应 docs/investigation-log.md schema）。
type InvestigationEntry struct {
	TS         time.Time      `yaml:"ts"`
	Level      string         `yaml:"level"`  // error | warning | info
	Source     string         `yaml:"source"` // 写入者标识
	Stage      string         `yaml:"stage"`
	SessionID  string         `yaml:"session_id"`
	NodeID     string         `yaml:"node_id"`
	Category   string         `yaml:"category"`
	Summary    string         `yaml:"summary"`
	Detail     map[string]any `yaml:"detail,omitempty"`
	RetryCount int            `yaml:"retry_count"`
}

// InvestigationLog investigation.log.yaml 的完整结构。
type InvestigationLog struct {
	TaskID  string               `yaml:"task_id"`
	Entries []InvestigationEntry `yaml:"entries"`
}

// InvestigationLogger 异常事件日志写入接口。
type InvestigationLogger interface {
	Append(taskID string, entry InvestigationEntry) error
	Get(taskID string) (*InvestigationLog, error)
}

type investigationLoggerImpl struct{}

// NewInvestigationLogger 创建调查日志记录器。
func NewInvestigationLogger() InvestigationLogger {
	return &investigationLoggerImpl{}
}

// investigationPath 返回 investigation.log.yaml 的路径。
func investigationPath(taskID string) string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".ainspection", "tasks", taskID, "investigation.log.yaml")
}

// Append 追加一条异常事件。
func (l *investigationLoggerImpl) Append(taskID string, entry InvestigationEntry) error {
	path := investigationPath(taskID)

	log, err := l.load(path)
	if err != nil || log == nil {
		log = &InvestigationLog{TaskID: taskID}
	}

	if entry.TS.IsZero() {
		entry.TS = time.Now()
	}

	log.Entries = append(log.Entries, entry)

	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("mkdir investigation dir: %w", err)
	}

	data, err := yaml.Marshal(log)
	if err != nil {
		return fmt.Errorf("marshal investigation log: %w", err)
	}

	return os.WriteFile(path, data, 0o644)
}

// Get 获取 task 的所有异常事件。
func (l *investigationLoggerImpl) Get(taskID string) (*InvestigationLog, error) {
	return l.load(investigationPath(taskID))
}

func (l *investigationLoggerImpl) load(path string) (*InvestigationLog, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}

	var log InvestigationLog
	if err := yaml.Unmarshal(data, &log); err != nil {
		return nil, fmt.Errorf("parse investigation log: %w", err)
	}
	return &log, nil
}

// —————— 便捷构造函数 ——————

// NewBaselineFailureEntry 创建 baseline 失败的条目。
func NewBaselineFailureEntry(sessionID, nodeID, command string, exitCode int, stderr string) InvestigationEntry {
	return InvestigationEntry{
		Level:    "error",
		Source:   "baseline",
		Category: "baseline_failure",
		Summary:  fmt.Sprintf("baseline check failed: %s (exit %d)", command, exitCode),
		Detail: map[string]any{
			"command":   command,
			"exit_code": exitCode,
			"stderr":    stderr,
		},
		SessionID: sessionID,
		NodeID:    nodeID,
	}
}

// NewStageRetryExceededEntry 创建重试超限的条目。
func NewStageRetryExceededEntry(sessionID, nodeID, stage string, retryCount, maxRetry int, lastError string) InvestigationEntry {
	return InvestigationEntry{
		Level:    "error",
		Source:   "orchestrator",
		Stage:    stage,
		Category: "stage_retry_exceeded",
		Summary:  fmt.Sprintf("%s stage retry exceeded (%d/%d)", stage, retryCount, maxRetry),
		Detail: map[string]any{
			"stage":         stage,
			"retry_count":   retryCount,
			"max_per_stage": maxRetry,
			"last_error":    lastError,
		},
		RetryCount: retryCount,
		SessionID:  sessionID,
		NodeID:     nodeID,
	}
}
