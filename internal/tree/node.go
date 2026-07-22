package tree

import (
	"fmt"
	"os"
	"path/filepath"
	"time"

	"gopkg.in/yaml.v3"
)

// —————— NodeOps 实现 ——————

type nodeOpsImpl struct{}

// NewNodeOps 创建 NodeOps 实例。
func NewNodeOps() NodeOps {
	return &nodeOpsImpl{}
}

// Create 创建新节点：分配 node-id，创建 nodes/<id>/ 目录，写入 input/output/meta.yaml，
// 更新 tree.yaml 拓扑和 context.yaml 指针。
//
// 前置条件：父节点 status=completed（root 节点例外，允许从 pending 状态创建子节点）。
func (n *nodeOpsImpl) Create(t *Task, parentID string, in NodeInput) (*Node, error) {
	dir := taskDir(t.TaskID)

	unlock, err := Lock(dir)
	if err != nil {
		return nil, err
	}
	defer unlock()

	// 加载 tree.yaml
	tree, err := loadTreeYAML(dir)
	if err != nil {
		return nil, err
	}

	// 校验父节点
	parent, ok := tree.Nodes[parentID]
	if !ok {
		return nil, fmt.Errorf("parent node %q not found", parentID)
	}
	if parentID != "root" && parent.Status != StatusDone {
		return nil, fmt.Errorf("parent node %q status is %s, expected completed", parentID, parent.Status)
	}

	// 生成 node-id
	tree.Seq++
	nodeID := fmt.Sprintf("n%d-%s", tree.Seq, in.Stage)
	now := time.Now()

	node := &Node{
		ID:        nodeID,
		Parent:    parentID,
		Children:  []string{},
		Status:    StatusPending,
		Stage:     in.Stage,
		AgentRole: in.AgentRole,
		CreatedAt: now,
	}

	// 更新父节点 children
	parent.Children = append(parent.Children, nodeID)
	tree.Nodes[parentID] = parent

	// 在 tree.yaml 中注册新节点
	tree.Nodes[nodeID] = NodeYAML{
		Parent:    parentID,
		Children:  []string{},
		Status:    StatusPending,
		Stage:     in.Stage,
		AgentRole: in.AgentRole,
		CreatedAt: now,
	}

	// 更新 main branch 路径
	for i := range tree.Branches {
		if tree.Branches[i].Name == "main" {
			tree.Branches[i].Path = append(tree.Branches[i].Path, nodeID)
		}
	}

	// 写 tree.yaml
	if err := writeYAML(filepath.Join(dir, "tree.yaml"), tree); err != nil {
		return nil, err
	}

	// 创建节点目录 + 写 input/output/meta.yaml
	nodeDir := filepath.Join(dir, "nodes", nodeID)
	if err := os.MkdirAll(nodeDir, 0o755); err != nil {
		return nil, fmt.Errorf("mkdir node dir %s: %w", nodeID, err)
	}

	// 继承父节点的 input.yaml（如果是 root，input 已存在）
	parentInput := filepath.Join(dir, "nodes", parentID, "input.yaml")
	targetInput := filepath.Join(nodeDir, "input.yaml")
	if data, err := os.ReadFile(parentInput); err == nil {
		os.WriteFile(targetInput, data, 0o644)
	}

	meta := NodeMeta{
		NodeID:    nodeID,
		Parent:    parentID,
		Stage:     in.Stage,
		AgentRole: in.AgentRole,
		Status:    StatusPending,
		CreatedAt: now,
	}
	if err := writeYAML(filepath.Join(nodeDir, "meta.yaml"), meta); err != nil {
		return nil, err
	}

	output := &NodeOutput{NodeID: nodeID}
	if err := writeYAML(filepath.Join(nodeDir, "output.yaml"), output); err != nil {
		return nil, err
	}

	// 更新 task 的 current_node_id
	t.CurrentNodeID = nodeID

	return node, nil
}

// Complete 标记节点为已完成：更新 status、记录 completed_at、生成 summary.md。
// 内部自动调用 summary.go 的生成算法。
func (n *nodeOpsImpl) Complete(t *Task, nodeID string) error {
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

	nodeYAML, ok := tree.Nodes[nodeID]
	if !ok {
		return fmt.Errorf("node %q not found", nodeID)
	}

	now := time.Now()
	nodeYAML.Status = StatusDone
	nodeYAML.CompletedAt = &now
	tree.Nodes[nodeID] = nodeYAML

	if err := writeYAML(filepath.Join(dir, "tree.yaml"), tree); err != nil {
		return err
	}

	// 更新 meta.yaml
	nodeDir := filepath.Join(dir, "nodes", nodeID)
	meta := NodeMeta{
		NodeID:    nodeID,
		Parent:    nodeYAML.Parent,
		Stage:     nodeYAML.Stage,
		AgentRole: nodeYAML.AgentRole,
		Status:    StatusDone,
		CreatedAt: nodeYAML.CreatedAt,
	}
	if err := writeYAML(filepath.Join(nodeDir, "meta.yaml"), meta); err != nil {
		return err
	}

	// 生成 summary.md
	outputPath := filepath.Join(nodeDir, "output.yaml")
	summary, err := GenerateSummary(outputPath)
	if err != nil {
		return fmt.Errorf("generate summary: %w", err)
	}

	summaryPath := filepath.Join(dir, "summary.md")
	if err := os.WriteFile(summaryPath, []byte(summary), 0o644); err != nil {
		return fmt.Errorf("write summary.md: %w", err)
	}

	return nil
}

// Read 从文件系统读取单个节点的完整信息。
func (n *nodeOpsImpl) Read(t *Task, nodeID string) (*Node, error) {
	dir := taskDir(t.TaskID)

	tree, err := loadTreeYAML(dir)
	if err != nil {
		return nil, err
	}

	ny, ok := tree.Nodes[nodeID]
	if !ok {
		return nil, fmt.Errorf("node %q not found", nodeID)
	}

	return &Node{
		ID:           nodeID,
		Parent:       ny.Parent,
		Children:     ny.Children,
		Status:       ny.Status,
		Stage:        ny.Stage,
		AgentRole:    ny.AgentRole,
		BranchReason: ny.BranchReason,
		CreatedAt:    ny.CreatedAt,
		CompletedAt:  ny.CompletedAt,
	}, nil
}

// ReadInput 读取节点的 input.yaml（返回解析后的 map）。
func ReadInput(taskID, nodeID string) (map[string]any, error) {
	path := filepath.Join(taskDir(taskID), "nodes", nodeID, "input.yaml")
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read input.yaml: %w", err)
	}

	var input map[string]any
	if err := yaml.Unmarshal(data, &input); err != nil {
		return nil, fmt.Errorf("parse input.yaml: %w", err)
	}
	return input, nil
}

// ReadOutput 读取节点的 output.yaml。
func ReadOutput(taskID, nodeID string) (*NodeOutput, error) {
	path := filepath.Join(taskDir(taskID), "nodes", nodeID, "output.yaml")
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read output.yaml: %w", err)
	}

	var out NodeOutput
	if err := yaml.Unmarshal(data, &out); err != nil {
		return nil, fmt.Errorf("parse output.yaml: %w", err)
	}
	return &out, nil
}

// WriteOutput 写入节点的 output.yaml。
func WriteOutput(taskID, nodeID string, out *NodeOutput) error {
	path := filepath.Join(taskDir(taskID), "nodes", nodeID, "output.yaml")
	return writeYAML(path, out)
}

// —————— 内部辅助 ——————

func loadTreeYAML(dir string) (*TreeYAML, error) {
	path := filepath.Join(dir, "tree.yaml")
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read tree.yaml: %w", err)
	}

	var tree TreeYAML
	if err := yaml.Unmarshal(data, &tree); err != nil {
		return nil, fmt.Errorf("parse tree.yaml: %w", err)
	}
	return &tree, nil
}
