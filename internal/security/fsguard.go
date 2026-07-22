package security

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// FSGuard 文件系统边界守护接口。
type FSGuard interface {
	// Resolve 校验并解析路径。路径越界返回 error。
	// taskID: 当前任务 ID，用于展开 ~/.ainspection/tasks/<task-id>/ 模式
	// sessionID: 当前 session ID，用于展开 ~/.ainspection/sessions/<sid>/ 模式
	// path: 待校验的路径
	// op: 操作类型（读/写）
	Resolve(taskID, sessionID string, path string, op OpType) (string, error)

	// ValidateSymlinkTarget 校验 symlink 目标路径是否合法。
	// 检查: 路径存在 + 不包含 .git + 在 repoPaths 白名单内。
	ValidateSymlinkTarget(targetPath string) error
}

// fsGuardImpl 默认实现。
type fsGuardImpl struct {
	allowedRead  []string
	allowedWrite []string
	repoPaths    []string // services[].repo_path
}

// NewFSGuard 创建 FSGuard。
func NewFSGuard(allowedRead, allowedWrite, repoPaths []string) FSGuard {
	return &fsGuardImpl{
		allowedRead:  allowedRead,
		allowedWrite: allowedWrite,
		repoPaths:    repoPaths,
	}
}

// Resolve 校验并解析路径。
func (g *fsGuardImpl) Resolve(taskID, sessionID string, path string, op OpType) (string, error) {
	resolved := g.expandPath(path, taskID, sessionID)

	// 拒绝系统目录
	if g.isBlockedPath(resolved) {
		return "", fmt.Errorf("fs_guard: path %s is blocked (system directory)", path)
	}

	// 根据操作类型选择允许列表
	var allowed []string
	if op == OpRead {
		allowed = g.allowedRead
	} else {
		allowed = g.allowedWrite
	}

	// 检查路径是否在允许范围内
	for _, allow := range allowed {
		expanded := g.expandPath(allow, taskID, sessionID)
		if g.isPathWithin(resolved, expanded) {
			return resolved, nil
		}
	}

	return "", fmt.Errorf("fs_guard: path %s not in allowed %s paths", path, op)
}

// ValidateSymlinkTarget 校验 symlink 目标路径是否合法。
func (g *fsGuardImpl) ValidateSymlinkTarget(targetPath string) error {
	absPath, err := filepath.Abs(targetPath)
	if err != nil {
		return fmt.Errorf("fs_guard: resolve symlink target %s: %w", targetPath, err)
	}

	// 目标路径必须存在
	if _, err := os.Stat(absPath); os.IsNotExist(err) {
		return fmt.Errorf("fs_guard: symlink target %s does not exist", targetPath)
	}

	// 禁止指向 .git 目录
	if strings.Contains(absPath, "/.git/") || strings.HasSuffix(absPath, "/.git") {
		return fmt.Errorf("fs_guard: symlink target %s contains .git", targetPath)
	}

	// 必须在 repoPaths 白名单内
	for _, repo := range g.repoPaths {
		absRepo, err := filepath.Abs(repo)
		if err != nil {
			continue
		}
		if g.isPathWithin(absPath, absRepo) {
			return nil
		}
	}

	return fmt.Errorf("fs_guard: symlink target %s not in repo paths whitelist", targetPath)
}

// expandPath 展开 ~ 和模板变量。
func (g *fsGuardImpl) expandPath(path string, taskID, sessionID string) string {
	path = strings.ReplaceAll(path, "<task-id>", taskID)
	path = strings.ReplaceAll(path, "<session-id>", sessionID)

	if strings.HasPrefix(path, "~/") {
		home, _ := os.UserHomeDir()
		path = filepath.Join(home, path[2:])
	}
	return filepath.Clean(path)
}

// isBlockedPath 检查是否为被禁止的系统路径。
func (g *fsGuardImpl) isBlockedPath(path string) bool {
	blocked := []string{"/etc/", "/proc/", "/sys/", "/dev/", "/boot/"}
	absPath, err := filepath.Abs(path)
	if err != nil {
		return true // 无法解析的路径视为 blocked
	}

	for _, b := range blocked {
		if strings.HasPrefix(absPath, b) {
			return true
		}
	}

	// 禁止直接操作 .git 目录
	if strings.Contains(absPath, "/.git/") || strings.HasSuffix(absPath, "/.git") {
		return true
	}

	// 禁止网络路径
	if strings.HasPrefix(absPath, "//") || strings.HasPrefix(absPath, "\\\\") {
		return true
	}

	return false
}

// isPathWithin 判断 path 是否在 parent 目录之内。
func (g *fsGuardImpl) isPathWithin(path, parent string) bool {
	absPath, err := filepath.Abs(path)
	if err != nil {
		return false
	}
	absParent, err := filepath.Abs(parent)
	if err != nil {
		return false
	}

	// 确保 parent 以分隔符结尾，避免 /var/a 匹配 /var/abc
	if !strings.HasSuffix(absParent, string(filepath.Separator)) {
		absParent += string(filepath.Separator)
	}

	return strings.HasPrefix(absPath, absParent) || absPath == absParent[:len(absParent)-1]
}
