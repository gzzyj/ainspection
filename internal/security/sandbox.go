package security

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"git.qingteng.cn/ms/ainspection/internal/adapter"
)

// Sandbox 会话级沙箱接口。
//
// 每个 session 独占一个 working dir，实现轻量但有效的隔离。
// 不依赖容器 runtime（docker/podman），而是通过:
//   - 独占目录 + CommandExecutor 强制 cwd
//   - FSGuard 路径校验拒绝越界
//   - 7 天 hot retention + 压缩归档
type Sandbox interface {
	// SetupSession 创建 session 独占的 working dir，完成完整 5 步流程：
	//  ① mkdir 标准子目录
	//  ② symlink source → repoPaths（需 fsGuard 校验）
	//  ③ 获取 adapter = registry.Get(agentName)
	//  ④ adapter.Setup(ctx, sandboxPath, cfg) ← 写入 LLM 配置 + skill/hook
	//  ⑤ 返回 sandboxPath
	//
	// agentName 为空时跳过 ③④；cfg 为零值时跳过 ④。
	SetupSession(ctx context.Context, sessionID string, agentName string, cfg adapter.AgentSetupConfig) (string, error)

	// CleanupSession 标记 session 为可清理（7 天 hot 后压缩）。
	CleanupSession(sessionID string) error

	// CompressSession 将 session 目录压缩为 tar.gz 归档，成功后删除源目录。
	CompressSession(sessionID string) error
}

// sandboxImpl 默认实现。
type sandboxImpl struct {
	root             string // ~/.ainspection/sessions/
	hotRetentionDays int    // 7
	adapterRegistry  *adapter.Registry
	repoPaths        []string
	fsGuard          FSGuard
}

// NewSandbox 创建 Sandbox 实例。
func NewSandbox(root string, retentionDays int, registry *adapter.Registry, repoPaths []string, fsGuard FSGuard) Sandbox {
	if root == "" {
		home, _ := os.UserHomeDir()
		root = filepath.Join(home, ".ainspection", "sessions")
	}
	if retentionDays <= 0 {
		retentionDays = 7
	}
	return &sandboxImpl{
		root:             root,
		hotRetentionDays: retentionDays,
		adapterRegistry:  registry,
		repoPaths:        repoPaths,
		fsGuard:          fsGuard,
	}
}

// NewSandboxSimple 创建无 adapter/fsguard 的简化 Sandbox（仅目录操作，用于测试）。
func NewSandboxSimple(root string, retentionDays int) Sandbox {
	return NewSandbox(root, retentionDays, nil, nil, nil)
}

// SetupSession 完整 5 步流程。
func (s *sandboxImpl) SetupSession(ctx context.Context, sessionID string, agentName string, cfg adapter.AgentSetupConfig) (string, error) {
	workingDir := filepath.Join(s.root, sessionID)

	// ① mkdir 标准子目录
	subdirs := []string{"input", "output", "patches", "signals", "scratch"}
	for _, sub := range subdirs {
		dir := filepath.Join(workingDir, sub)
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return "", fmt.Errorf("sandbox: mkdir %s: %w", dir, err)
		}
	}

	// ② symlink source → repoPaths（需 fsGuard 校验）
	if len(s.repoPaths) > 0 && s.fsGuard != nil {
		repoPath := filepath.Join(workingDir, "repo")
		src := s.repoPaths[0]
		if err := s.fsGuard.ValidateSymlinkTarget(src); err != nil {
			return "", fmt.Errorf("sandbox: validate symlink target %s: %w", src, err)
		}
		if err := os.Symlink(src, repoPath); err != nil && !os.IsExist(err) {
			return "", fmt.Errorf("sandbox: symlink %s -> %s: %w", src, repoPath, err)
		}
	}

	// ③ 获取 adapter
	if agentName != "" && s.adapterRegistry != nil {
		// ④ adapter.Setup
		agentType := adapter.ResolveAgentType(agentName)
		if agentType != "" {
			a, err := s.adapterRegistry.Get(agentType)
			if err != nil {
				return "", fmt.Errorf("sandbox: get adapter %q: %w", agentName, err)
			}
			if err := a.Setup(ctx, workingDir, cfg); err != nil {
				return "", fmt.Errorf("sandbox: adapter setup: %w", err)
			}
		}
	}

	// ⑤ 返回 sandboxPath
	return workingDir, nil
}

// CleanupSession 标记 session 为可清理。
// P0 实现: 记录到期时间到 session 目录的 .retention 文件。
// 完整 GC 由后台定时任务处理（P2）。
func (s *sandboxImpl) CleanupSession(sessionID string) error {
	workingDir := filepath.Join(s.root, sessionID)

	if _, err := os.Stat(workingDir); os.IsNotExist(err) {
		return nil // 已清理，幂等
	}

	// 写 retention 标记文件
	retentionFile := filepath.Join(workingDir, ".retention")
	msg := fmt.Sprintf("hot_retention_days=%d", s.hotRetentionDays)
	if err := os.WriteFile(retentionFile, []byte(msg), 0o600); err != nil {
		return fmt.Errorf("sandbox: write retention file: %w", err)
	}

	return nil
}

// SessionDir 返回指定 session 的沙箱路径（不创建）。
func (s *sandboxImpl) SessionDir(sessionID string) string {
	return filepath.Join(s.root, sessionID)
}

// CompressSession 将 session 目录压缩为 tar.gz，成功后删除源目录。
func (s *sandboxImpl) CompressSession(sessionID string) error {
	sessionDir := filepath.Join(s.root, sessionID)

	if _, err := os.Stat(sessionDir); os.IsNotExist(err) {
		return nil // 目录不存在，视为已清理
	}

	archivePath := sessionDir + ".tar.gz"

	f, err := os.Create(archivePath)
	if err != nil {
		return fmt.Errorf("sandbox: create archive %s: %w", archivePath, err)
	}
	defer f.Close()

	gw := gzip.NewWriter(f)
	defer gw.Close()

	tw := tar.NewWriter(gw)
	defer tw.Close()

	baseDir := filepath.Base(sessionDir)
	err = filepath.Walk(sessionDir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}

		// 计算相对路径
		relPath, err := filepath.Rel(sessionDir, path)
		if err != nil {
			return fmt.Errorf("rel path: %w", err)
		}
		if relPath == "." {
			return nil
		}

		// tar header
		link := ""
		if info.Mode()&os.ModeSymlink != 0 {
			link, err = os.Readlink(path)
			if err != nil {
				return fmt.Errorf("readlink %s: %w", path, err)
			}
		}

		hdr, err := tar.FileInfoHeader(info, link)
		if err != nil {
			return fmt.Errorf("tar header %s: %w", path, err)
		}
		hdr.Name = filepath.Join(baseDir, relPath)

		if err := tw.WriteHeader(hdr); err != nil {
			return fmt.Errorf("write tar header: %w", err)
		}

		if info.IsDir() || link != "" {
			return nil
		}

		src, err := os.Open(path)
		if err != nil {
			return fmt.Errorf("open %s: %w", path, err)
		}
		defer src.Close()

		if _, err := io.Copy(tw, src); err != nil {
			return fmt.Errorf("copy %s: %w", path, err)
		}
		return nil
	})
	if err != nil {
		// 压缩失败，不删除源目录
		os.Remove(archivePath)
		return fmt.Errorf("sandbox: compress %s: %w", sessionID, err)
	}

	// 压缩成功，删除源目录
	if err := os.RemoveAll(sessionDir); err != nil {
		return fmt.Errorf("sandbox: remove session dir after compress: %w", err)
	}

	return nil
}
