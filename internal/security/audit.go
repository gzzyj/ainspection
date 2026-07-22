package security

import (
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

// AuditLogger 审计日志接口。
//
// 所有安全相关操作（CommandExecutor / FSGuard / SkillInjector）均调用
// AuditLogger.Append 记录。输出 jsonl 格式，按天轮转，30 天 retention。
type AuditLogger interface {
	// Append 追加一条审计记录。
	Append(record Record) error
}

// auditLoggerImpl 默认实现。
type auditLoggerImpl struct {
	dir           string
	retentionDays int
	mu            sync.Mutex
	currentDate   string
	currentFile   *os.File
}

// NewAuditLogger 创建审计日志记录器。
// 启动时自动清理超过 retentionDays 的旧文件。
func NewAuditLogger(dir string, retentionDays int) AuditLogger {
	if dir == "" {
		home, _ := os.UserHomeDir()
		dir = filepath.Join(home, ".ainspection", "audit")
	}
	if retentionDays <= 0 {
		retentionDays = 30
	}
	l := &auditLoggerImpl{
		dir:           dir,
		retentionDays: retentionDays,
	}
	l.cleanupOldFiles()
	return l
}

// Append 追加一条审计记录（jsonl 格式）。
func (l *auditLoggerImpl) Append(record Record) error {
	l.mu.Lock()
	defer l.mu.Unlock()

	if record.TS.IsZero() {
		record.TS = time.Now()
	}

	today := record.TS.Format("20060102")

	// 日期变更时切换文件
	if today != l.currentDate {
		if l.currentFile != nil {
			l.currentFile.Close()
		}
		if err := l.openFile(today); err != nil {
			return err
		}
		l.currentDate = today
	}

	// 懒加载：首次调用时打开文件
	if l.currentFile == nil {
		if err := l.openFile(today); err != nil {
			return err
		}
		l.currentDate = today
	}

	data, err := json.Marshal(record)
	if err != nil {
		return fmt.Errorf("audit: marshal record: %w", err)
	}

	if _, err := l.currentFile.Write(append(data, '\n')); err != nil {
		return fmt.Errorf("audit: write record: %w", err)
	}

	return nil
}

// openFile 打开当天的审计日志文件。
func (l *auditLoggerImpl) openFile(date string) error {
	if err := os.MkdirAll(l.dir, 0o755); err != nil {
		return fmt.Errorf("audit: mkdir: %w", err)
	}

	path := filepath.Join(l.dir, date+".jsonl")
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return fmt.Errorf("audit: open %s: %w", path, err)
	}

	l.currentFile = f
	return nil
}

// —————— 便捷记录函数 ——————

// NewExecRecord 创建命令执行的审计记录。
func NewExecRecord(taskID, sessionID, agent, cmd, result, reason string) Record {
	return Record{
		Component: "command_executor",
		Action:    "exec",
		TaskID:    taskID,
		SessionID: sessionID,
		Agent:     agent,
		Result:    result,
		Reason:    reason,
		Args:      map[string]any{"cmd": cmd},
	}
}

// NewFSRecord 创建文件系统操作的审计记录。
func NewFSRecord(taskID, sessionID, agent, path string, op OpType, result, reason string) Record {
	return Record{
		Component: "fs_guard",
		Action:    op.String(),
		TaskID:    taskID,
		SessionID: sessionID,
		Agent:     agent,
		Result:    result,
		Reason:    reason,
		Args:      map[string]any{"path": path, "op": op.String()},
	}
}

// NewInjectRecord 创建 skill 注入的审计记录。
func NewInjectRecord(taskID, sessionID, agent string, skillCount int, result string) Record {
	return Record{
		Component: "skill_injector",
		Action:    "inject",
		TaskID:    taskID,
		SessionID: sessionID,
		Agent:     agent,
		Result:    result,
		Args:      map[string]any{"skill_count": skillCount},
	}
}

// —————— 内部辅助 ——————

// cleanupOldFiles 清理超过 retentionDays 的审计日志文件。
func (l *auditLoggerImpl) cleanupOldFiles() {
	entries, err := os.ReadDir(l.dir)
	if err != nil {
		if os.IsNotExist(err) {
			return
		}
		log.Printf("[audit] cleanup: read dir: %v", err)
		return
	}

	cutoff := time.Now().AddDate(0, 0, -l.retentionDays)

	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".jsonl") {
			continue
		}
		dateStr := strings.TrimSuffix(entry.Name(), ".jsonl")
		if len(dateStr) != 8 {
			continue
		}
		year, err := strconv.Atoi(dateStr[0:4])
		if err != nil {
			continue
		}
		month, err := strconv.Atoi(dateStr[4:6])
		if err != nil {
			continue
		}
		day, err := strconv.Atoi(dateStr[6:8])
		if err != nil {
			continue
		}
		fileDate := time.Date(year, time.Month(month), day, 0, 0, 0, 0, time.UTC)
		if fileDate.Before(cutoff) {
			path := filepath.Join(l.dir, entry.Name())
			if err := os.Remove(path); err != nil {
				log.Printf("[audit] cleanup: remove %s: %v", path, err)
			}
		}
	}
}
