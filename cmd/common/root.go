package common

import (
	"github.com/spf13/cobra"

	"git.qingteng.cn/ms/ainspection/cmd/config"
	"git.qingteng.cn/ms/ainspection/cmd/doctor"
	"git.qingteng.cn/ms/ainspection/cmd/eval"
	"git.qingteng.cn/ms/ainspection/cmd/run"
	"git.qingteng.cn/ms/ainspection/cmd/session"
)

// NewRootCmd 创建 cobra root command 并注册所有子命令。
func NewRootCmd() *cobra.Command {
	root := &cobra.Command{
		Use:   "ainspection",
		Short: "AI Inspection — 基于多 Agent 协作的自动化问题检测与修复系统",
		Long: `ainspection 通过 Go CLI 编排多个 Agent session（Generator + Evaluator + Planner），
实现从报错/性能告警到根因定位、修复、部署验证、MR 创建的全自动闭环。

命令树:
  run       从 Issue URL/描述启动作业流水线
  config    管理 ainspection 配置
  session   管理运行中的 session（列表/恢复/回滚/分支/终止）
  eval      离线评测 (P2)
  doctor    环境诊断检查`,
		SilenceUsage:  true,
		SilenceErrors: true,
	}

	RegisterGlobalFlags(root)

	// 注册子命令
	root.AddCommand(run.New())
	root.AddCommand(config.New())
	root.AddCommand(session.New())
	root.AddCommand(eval.New())
	root.AddCommand(doctor.New())

	return root
}
