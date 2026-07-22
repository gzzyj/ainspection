// Package main 是 ainspection CLI 的唯一入口。
// 仅负责装配 cobra root command 和启动，不含业务逻辑。
package main

import (
	"os"

	"git.qingteng.cn/ms/ainspection/cmd/common"
)

func main() {
	root := common.NewRootCmd()
	if err := root.Execute(); err != nil {
		os.Exit(1)
	}
}
