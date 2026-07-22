package common

import "github.com/spf13/cobra"

// 全局 flag 名称常量。
const (
	FlagConfig  = "config"
	FlagTaskID  = "task-id"
	FlagService = "service"
	FlagVerbose = "verbose"
)

// RegisterGlobalFlags 在 root command 上注册全局 flag。
func RegisterGlobalFlags(cmd *cobra.Command) {
	cmd.PersistentFlags().String(FlagConfig, "", "配置文件路径 (默认 ~/.ainspection/config.yaml)")
	cmd.PersistentFlags().String(FlagTaskID, "", "任务 ID")
	cmd.PersistentFlags().String(FlagService, "", "目标 service 名称")
	cmd.PersistentFlags().BoolP(FlagVerbose, "v", false, "详细输出")
}
