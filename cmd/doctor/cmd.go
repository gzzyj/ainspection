// Package doctor 提供 ainspection doctor 子命令。
package doctor

import (
	"fmt"
	"os"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"git.qingteng.cn/ms/ainspection/internal/config"
	"git.qingteng.cn/ms/ainspection/internal/doctor"
)

// New 创建 doctor 子命令。
func New() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "doctor",
		Short: "环境诊断检查",
		Long: `检查 ainspection 运行所需的工具链、API key、服务连通性。

检查清单:
  工具链:   go / kubectl / skaffold / glab / golangci-lint / gofumpt / goimports
  API key:  claude / kimi / qwen / gitlab / lark
  服务连通: loki / prometheus / tempo / k8s context / git remote
  配置文件: config.yaml 存在 + services 数组非空
  沙箱目录: working_dir_root 可写

退出码: 0 成功(可能含 warn) / 1 用户错误 / 2 硬失败`,
		RunE: run,
	}

	return cmd
}

func run(cmd *cobra.Command, args []string) error {
	configPath, _ := cmd.Flags().GetString("config")

	// 尝试加载 config
	var cfg *config.Config
	var loadErr error

	if configPath != "" {
		cfg, loadErr = config.Load(configPath)
	} else {
		// doctor 可脱离 config 运行（仅检查工具链和沙箱）
		cfg, loadErr = config.Load("")
	}

	// config 加载失败不阻塞——仅跳过依赖 config 的检查项
	if loadErr != nil {
		fmt.Fprintf(os.Stderr, "[WARN] config: %v\n", loadErr)
	}

	checker := doctor.NewChecker(cfg)
	results := checker.RunAll()

	// 输出表格
	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "STATUS\tCHECK\tDETAIL")
	for _, r := range results {
		fmt.Fprintf(w, "[%s]\t%s\t%s\n", r.Status, r.Name, r.Detail)
	}
	w.Flush()

	// 退出码
	switch {
	case doctor.HasHardFailure(results):
		fmt.Fprintln(os.Stderr, "\nexit 2 (hard failures present)")
		os.Exit(2)
	case doctor.HasWarning(results):
		fmt.Println("\nexit 0 (warnings present)")
	default:
		fmt.Println("\nexit 0 (all checks passed)")
	}

	return nil
}
