// Package config 提供 ainspection config 子命令。
package config

import (
	"fmt"

	"github.com/spf13/cobra"
)

// New 创建 config 子命令。
func New() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "config",
		Short: "管理 ainspection 配置",
		Long:  "查看、设置和校验 ainspection 配置文件。",
	}

	cmd.AddCommand(newGetCmd())
	cmd.AddCommand(newSetCmd())
	cmd.AddCommand(newValidateCmd())

	return cmd
}

func newGetCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "get <key>",
		Short: "获取配置项的值",
		Long:  "获取指定配置项的值。不指定 key 时显示全部配置。",
		RunE: func(cmd *cobra.Command, args []string) error {
			key := "all"
			if len(args) > 0 {
				key = args[0]
			}
			fmt.Printf("config get: key=%s (not implemented yet)\n", key)
			return nil
		},
	}
}

func newSetCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "set <key> <value>",
		Short: "设置配置项的值",
		Long:  "设置指定配置项的值并持久化到配置文件。",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			fmt.Printf("config set: %s=%s (not implemented yet)\n", args[0], args[1])
			return nil
		},
	}
}

func newValidateCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "validate",
		Short: "校验配置文件",
		Long:  "检查配置文件是否存在、格式是否正确、agent API key 是否配置。",
		RunE: func(cmd *cobra.Command, args []string) error {
			fmt.Println("config validate (not implemented yet)")
			return nil
		},
	}
}
