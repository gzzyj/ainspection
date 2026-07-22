package tree

import (
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// —————— Rollback 实现 ——————

type rollbackImpl struct{}

// NewRollback 创建 Rollback 实例。
func NewRollback() Rollback {
	return &rollbackImpl{}
}

// Rollback 回滚到目标节点：仅切换 context.yaml 的 current_node_id 指针。
//
// 不删除任何历史节点或分支；不启动 LLM。
func (r *rollbackImpl) Rollback(t *Task, targetNodeID string) error {
	dir := taskDir(t.TaskID)

	unlock, err := Lock(dir)
	if err != nil {
		return err
	}
	defer unlock()

	tree, err := loadTreeYAML(dir)
	if err != nil {
		return err
	}

	if _, ok := tree.Nodes[targetNodeID]; !ok {
		return fmt.Errorf("target node %q not found", targetNodeID)
	}

	// 仅切换指针
	t.CurrentNodeID = targetNodeID
	return writeYAML(filepath.Join(dir, "context.yaml"), t)
}

// Branch 从历史节点分叉创建新分支。
//
// 继承源节点的 input.yaml + user_directives。
// 原分支保持不变，新节点创建为 status=pending。
// 不启动 LLM。
func (r *rollbackImpl) Branch(t *Task, fromNodeID, reason string) (*Node, error) {
	dir := taskDir(t.TaskID)

	unlock, err := Lock(dir)
	if err != nil {
		return nil, err
	}
	defer unlock()

	tree, err := loadTreeYAML(dir)
	if err != nil {
		return nil, err
	}

	src, ok := tree.Nodes[fromNodeID]
	if !ok {
		return nil, fmt.Errorf("source node %q not found", fromNodeID)
	}

	// 生成新 node-id
	tree.Seq++
	nodeID := fmt.Sprintf("n%d-%s-branch", tree.Seq, src.Stage)
	now := time.Now()

	// 新节点继承源节点的 stage
	newNodeYAML := NodeYAML{
		Parent:       src.Parent,
		Children:     []string{},
		Status:       StatusPending,
		Stage:        src.Stage,
		AgentRole:    src.AgentRole,
		BranchReason: reason,
		CreatedAt:    now,
	}
	tree.Nodes[nodeID] = newNodeYAML

	// 将新节点加入父节点的 children
	if parent, ok := tree.Nodes[src.Parent]; ok {
		parent.Children = append(parent.Children, nodeID)
		tree.Nodes[src.Parent] = parent
	}

	// 新增分支记录
	branchName := fmt.Sprintf("branch-%s", nodeID)
	tree.Branches = append(tree.Branches, BranchYAML{
		Name: branchName,
		Path: append(collectPath(tree.Nodes, src.Parent), nodeID),
	})

	if err := writeYAML(filepath.Join(dir, "tree.yaml"), tree); err != nil {
		return nil, err
	}

	// 创建节点目录
	nodeDir := filepath.Join(dir, "nodes", nodeID)
	if err := os.MkdirAll(nodeDir, 0o755); err != nil {
		return nil, fmt.Errorf("mkdir branch node: %w", err)
	}

	// 继承源节点的 input.yaml
	srcInput := filepath.Join(dir, "nodes", fromNodeID, "input.yaml")
	if data, err := os.ReadFile(srcInput); err == nil {
		os.WriteFile(filepath.Join(nodeDir, "input.yaml"), data, 0o644)
	}

	// 继承源节点的 user_directives
	srcOutput, _ := ReadOutput(t.TaskID, fromNodeID)
	output := &NodeOutput{
		NodeID:         nodeID,
		UserDirectives: srcOutput.UserDirectives,
	}
	if err := writeYAML(filepath.Join(nodeDir, "output.yaml"), output); err != nil {
		return nil, err
	}

	meta := NodeMeta{
		NodeID:    nodeID,
		Parent:    src.Parent,
		Stage:     src.Stage,
		AgentRole: src.AgentRole,
		Status:    StatusPending,
		CreatedAt: now,
	}
	if err := writeYAML(filepath.Join(nodeDir, "meta.yaml"), meta); err != nil {
		return nil, err
	}

	// 更新 context.yaml 指针
	t.CurrentNodeID = nodeID

	return &Node{
		ID:           nodeID,
		Parent:       src.Parent,
		Status:       StatusPending,
		Stage:        src.Stage,
		AgentRole:    src.AgentRole,
		BranchReason: reason,
		CreatedAt:    now,
	}, nil
}

// Replay 重放指定节点：在新 session 中加载目标节点的 input.yaml 重新执行。
//
// 输出写入新节点（非覆盖源节点）。新节点 id 为 <seq>-<stage>-replay。
// 注意：P0 阶段仅创建节点（状态机变更），LLM 启动由 orchestrator 负责。
func (r *rollbackImpl) Replay(t *Task, nodeID string) (*Node, error) {
	dir := taskDir(t.TaskID)

	unlock, err := Lock(dir)
	if err != nil {
		return nil, err
	}
	defer unlock()

	tree, err := loadTreeYAML(dir)
	if err != nil {
		return nil, err
	}

	src, ok := tree.Nodes[nodeID]
	if !ok {
		return nil, fmt.Errorf("target node %q not found", nodeID)
	}

	// 生成新 node-id
	tree.Seq++
	replayID := fmt.Sprintf("n%d-%s-replay", tree.Seq, src.Stage)
	now := time.Now()

	newNodeYAML := NodeYAML{
		Parent:    src.Parent,
		Children:  []string{},
		Status:    StatusPending,
		Stage:     src.Stage,
		AgentRole: src.AgentRole,
		CreatedAt: now,
	}
	tree.Nodes[replayID] = newNodeYAML

	if parent, ok := tree.Nodes[src.Parent]; ok {
		parent.Children = append(parent.Children, replayID)
		tree.Nodes[src.Parent] = parent
	}

	if err := writeYAML(filepath.Join(dir, "tree.yaml"), tree); err != nil {
		return nil, err
	}

	// 创建节点目录，拷贝 input.yaml
	nodeDir := filepath.Join(dir, "nodes", replayID)
	if err := os.MkdirAll(nodeDir, 0o755); err != nil {
		return nil, fmt.Errorf("mkdir replay node: %w", err)
	}

	srcInput := filepath.Join(dir, "nodes", nodeID, "input.yaml")
	if data, err := os.ReadFile(srcInput); err == nil {
		os.WriteFile(filepath.Join(nodeDir, "input.yaml"), data, 0o644)
	}

	output := &NodeOutput{NodeID: replayID}
	if err := writeYAML(filepath.Join(nodeDir, "output.yaml"), output); err != nil {
		return nil, err
	}

	meta := NodeMeta{
		NodeID:    replayID,
		Parent:    src.Parent,
		Stage:     src.Stage,
		AgentRole: src.AgentRole,
		Status:    StatusPending,
		CreatedAt: now,
	}
	if err := writeYAML(filepath.Join(nodeDir, "meta.yaml"), meta); err != nil {
		return nil, err
	}

	t.CurrentNodeID = replayID

	return &Node{
		ID:        replayID,
		Parent:    src.Parent,
		Status:    StatusPending,
		Stage:     src.Stage,
		AgentRole: src.AgentRole,
		CreatedAt: now,
	}, nil
}

// Merge 将子分支的 findings 合并到目标节点的 output.yaml。
//
// 合并策略：追加非重复的 findings（按 hypothesis 文本去重）。
// 不启动 LLM。
func (r *rollbackImpl) Merge(t *Task, branchNodeID, targetNodeID string) error {
	dir := taskDir(t.TaskID)

	unlock, err := Lock(dir)
	if err != nil {
		return err
	}
	defer unlock()

	// 读取源和目标 output.yaml
	branchOut, err := ReadOutput(t.TaskID, branchNodeID)
	if err != nil {
		return fmt.Errorf("read branch output: %w", err)
	}

	targetOut, err := ReadOutput(t.TaskID, targetNodeID)
	if err != nil {
		return fmt.Errorf("read target output: %w", err)
	}

	// 按 hypothesis 文本去重合并
	existing := make(map[string]bool)
	for _, f := range targetOut.Findings {
		existing[f.Hypothesis] = true
	}

	for _, f := range branchOut.Findings {
		if !existing[f.Hypothesis] {
			targetOut.Findings = append(targetOut.Findings, f)
			existing[f.Hypothesis] = true
		}
	}

	return WriteOutput(t.TaskID, targetNodeID, targetOut)
}

// —————— 辅助函数 ——————

// collectPath 从 root 到指定节点收集路径（用于分支路径记录）。
func collectPath(nodes map[string]NodeYAML, nodeID string) []string {
	var path []string
	current := nodeID
	for current != "" {
		path = append([]string{current}, path...)
		n, ok := nodes[current]
		if !ok {
			break
		}
		current = n.Parent
	}
	return path
}
