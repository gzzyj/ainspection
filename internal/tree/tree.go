package tree

import (
	"crypto/rand"
	"fmt"
	"math/big"
	"os"
	"path/filepath"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// TaskRoot 返回任务存储根目录。
func TaskRoot() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".ainspection", "tasks")
}

// TaskRootWithDataDir 基于指定的 dataDir 返回任务存储根目录。
func TaskRootWithDataDir(dataDir string) string {
	return filepath.Join(expandDataDir(dataDir), "tasks")
}

// taskDir 返回指定 task 的存储目录。
func taskDir(taskID string) string {
	return filepath.Join(TaskRoot(), taskID)
}

// expandDataDir 展开 dataDir 中的 ~。
func expandDataDir(dataDir string) string {
	if strings.HasPrefix(dataDir, "~/") {
		home, _ := os.UserHomeDir()
		return filepath.Join(home, dataDir[2:])
	}
	return dataDir
}

// —————— Manager 实现 ——————

type managerImpl struct {
	dataDir string
}

// NewManager 创建 Manager 实例（使用默认数据目录）。
func NewManager() Manager {
	return &managerImpl{}
}

// NewManagerWithDataDir 创建 Manager 实例（使用指定的数据目录）。
func NewManagerWithDataDir(dataDir string) Manager {
	return &managerImpl{dataDir: expandDataDir(dataDir)}
}

// NewTask 创建新任务：生成 task-id，初始化目录结构、context.yaml 和 tree.yaml。
func (m *managerImpl) NewTask(spec TaskSpec) (*Task, error) {
	taskID := genTaskID()
	dir := m.resolveTaskDir(taskID)

	unlock, err := Lock(dir)
	if err != nil {
		return nil, err
	}
	defer unlock()

	now := time.Now()
	t := &Task{
		TaskID:           taskID,
		RootNodeID:       "root",
		TreeVersion:      1,
		CreatedAt:        now,
		UpdatedAt:        now,
		Status:           StatusPending,
		IssueRef:         extractIssueRef(spec.IssueURL),
		Service:          spec.Service,
		RetryCount:       map[string]int{"locate": 0, "fix": 0, "verify": 0, "max_per_stage": 2},
		BaselineVerified: false,
	}

	// 创建目录结构
	for _, sub := range []string{"nodes", "patches", "signals"} {
		if err := os.MkdirAll(filepath.Join(dir, sub), 0o755); err != nil {
			return nil, fmt.Errorf("mkdir %s: %w", sub, err)
		}
	}

	// 创建 root 节点目录
	rootDir := filepath.Join(dir, "nodes", "root")
	if err := os.MkdirAll(rootDir, 0o755); err != nil {
		return nil, fmt.Errorf("mkdir root node: %w", err)
	}

	// 写 root 节点的 input.yaml
	inputYAML := map[string]any{
		"service":   spec.Service,
		"issue_url": spec.IssueURL,
		"desc":      spec.Desc,
	}
	if spec.TraceID != "" {
		inputYAML["trace_id"] = spec.TraceID
	}
	if err := writeYAML(filepath.Join(rootDir, "input.yaml"), inputYAML); err != nil {
		return nil, err
	}
	if err := writeYAML(filepath.Join(rootDir, "output.yaml"), &NodeOutput{NodeID: "root"}); err != nil {
		return nil, err
	}

	// 写 tree.yaml
	tree := &TreeYAML{
		TaskID: taskID,
		Nodes: map[string]NodeYAML{
			"root": {
				Parent:    "",
				Children:  []string{},
				Status:    StatusPending,
				Stage:     "init",
				CreatedAt: now,
			},
		},
		Branches: []BranchYAML{{Name: "main", Path: []string{"root"}}},
		Seq:      0,
	}
	if err := writeYAML(filepath.Join(dir, "tree.yaml"), tree); err != nil {
		return nil, err
	}

	// 写 context.yaml（初始状态 current_node_id 指向 root）
	t.CurrentNodeID = "root"
	if err := writeYAML(filepath.Join(dir, "context.yaml"), t); err != nil {
		return nil, err
	}

	return t, nil
}

// LoadTask 从文件系统加载任务。
func (m *managerImpl) LoadTask(taskID string) (*Task, error) {
	dir := m.resolveTaskDir(taskID)
	path := filepath.Join(dir, "context.yaml")

	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read context.yaml: %w", err)
	}

	var t Task
	if err := yaml.Unmarshal(data, &t); err != nil {
		return nil, fmt.Errorf("parse context.yaml: %w", err)
	}

	return &t, nil
}

// Save 将任务状态写回 context.yaml。
func (m *managerImpl) Save(t *Task) error {
	dir := m.resolveTaskDir(t.TaskID)

	unlock, err := Lock(dir)
	if err != nil {
		return err
	}
	defer unlock()

	t.UpdatedAt = time.Now()
	t.TreeVersion++

	return writeYAML(filepath.Join(dir, "context.yaml"), t)
}

// —————— 辅助 ——————

// resolveTaskDir 返回 task 存储目录，优先使用注入的 dataDir。
func (m *managerImpl) resolveTaskDir(taskID string) string {
	if m.dataDir != "" {
		return filepath.Join(m.dataDir, "tasks", taskID)
	}
	return taskDir(taskID)
}

// genTaskID 生成格式为 <yyyymmdd-HHMMSS>-<random4> 的 task-id。
func genTaskID() string {
	now := time.Now()
	prefix := now.Format("20060102-150405")
	suffix := randomHex(4)
	return prefix + "-" + suffix
}

func randomHex(n int) string {
	const charset = "0123456789abcdef"
	b := make([]byte, n)
	for i := range b {
		idx, _ := rand.Int(rand.Reader, big.NewInt(int64(len(charset))))
		b[i] = charset[idx.Int64()]
	}
	return string(b)
}

// extractIssueRef 从 Jira URL 中提取 issue key（如 JIRA-1234）。
func extractIssueRef(url string) string {
	if url == "" {
		return ""
	}
	// 简单提取：URL 中最后一个 / 之后的部分
	for i := len(url) - 1; i >= 0; i-- {
		if url[i] == '/' {
			return url[i+1:]
		}
	}
	return url
}

func writeYAML(path string, v any) error {
	data, err := yaml.Marshal(v)
	if err != nil {
		return fmt.Errorf("marshal yaml for %s: %w", path, err)
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		return fmt.Errorf("write %s: %w", path, err)
	}
	return nil
}
