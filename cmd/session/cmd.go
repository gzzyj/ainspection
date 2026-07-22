// Package session 提供 ainspection session 子命令。
package session

import (
	"fmt"

	"github.com/spf13/cobra"
)

// New 创建 session 子命令。
func New() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "session",
		Short: "管理运行中的 session",
		Long: `管理 ainspection 的任务 session。

session ≡ 节点：节点是 session 的状态机面，runtime 是其运行时面。
CLI rollback/branch 仅操作状态机，不启动 LLM。`,
	}

	cmd.AddCommand(newListCmd())
	cmd.AddCommand(newResumeCmd())
	cmd.AddCommand(newRollbackCmd())
	cmd.AddCommand(newBranchCmd())
	cmd.AddCommand(newKillCmd())

	return cmd
}

func newListCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "列出当前 task 的所有 session",
		RunE: func(cmd *cobra.Command, args []string) error {
			fmt.Println("session list (not implemented yet)")
			return nil
		},
	}
}

func newResumeCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "resume <session-id>",
		Short: "恢复指定 session",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			fmt.Printf("session resume: %s (not implemented yet)\n", args[0])
			return nil
		},
	}
}

func newRollbackCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "rollback",
		Short: "回滚到历史节点（仅切换树指针，不启动 LLM）",
		Long:  "将 context.yaml 的 current_node_id 切换到目标节点。只操作状态机，不启动 LLM。",
		RunE: func(cmd *cobra.Command, args []string) error {
			to, _ := cmd.Flags().GetString("to")
			fmt.Printf("session rollback --to %s (not implemented yet)\n", to)
			return nil
		},
	}
	cmd.Flags().String("to", "", "目标节点 ID (必填)")
	cmd.MarkFlagRequired("to")
	return cmd
}

func newBranchCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "branch",
		Short: "从历史节点分叉创建新分支（仅创建空节点，不启动 LLM）",
		Long:  "从指定节点分叉，继承源节点的 input + user_directives。只创建空节点，不启动 LLM。",
		RunE: func(cmd *cobra.Command, args []string) error {
			from, _ := cmd.Flags().GetString("from")
			fmt.Printf("session branch --from %s (not implemented yet)\n", from)
			return nil
		},
	}
	cmd.Flags().String("from", "", "源节点 ID (必填)")
	cmd.MarkFlagRequired("from")
	return cmd
}

func newKillCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "kill <session-id>",
		Short: "终止指定 session",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			fmt.Printf("session kill: %s (not implemented yet)\n", args[0])
			return nil
		},
	}
}
