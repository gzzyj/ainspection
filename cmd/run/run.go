// Package run 提供 ainspection run 子命令。
// run 从 Issue URL 或问题描述启动一次完整的诊断修复流水线。
package run

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/spf13/cobra"

	"git.qingteng.cn/ms/ainspection/internal/config"
	"git.qingteng.cn/ms/ainspection/internal/orchestrator"
)

// New 创建 run 子命令。
func New() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "run",
		Short: "从 Issue URL/描述启动诊断修复流水线",
		Long: `从 Jira Issue URL、文字描述或服务名启动一次完整的诊断修复流水线。

流水线阶段: get → locate → review1 → plan → fix → review2 → verify → review3 → commit

示例:
  ainspection run --issue https://jira.example.com/browse/JIRA-1234
  ainspection run --desc "order-svc 响应超时" --service order-svc
  ainspection run --profile order-svc`,
		RunE: run,
	}

	cmd.Flags().String("issue", "", "Jira issue URL")
	cmd.Flags().String("desc", "", "问题文本描述")
	cmd.Flags().String("service", "", "目标 service 名称 (对应 config.services[].name)")
	cmd.Flags().String("profile", "", "性能分析目标服务名 (启动 profile 流水线)")
	cmd.Flags().String("trace-id", "", "已知的 trace ID，用于跨服务调用链关联定位")

	return cmd
}

func run(cmd *cobra.Command, args []string) error {
	issue, _ := cmd.Flags().GetString("issue")
	desc, _ := cmd.Flags().GetString("desc")
	service, _ := cmd.Flags().GetString("service")
	profile, _ := cmd.Flags().GetString("profile")
	traceID, _ := cmd.Flags().GetString("trace-id")

	// 至少需要一个输入
	if issue == "" && desc == "" && service == "" && profile == "" {
		return fmt.Errorf("请指定 --issue / --desc / --service / --profile 其中之一")
	}

	// 1. 加载 Config
	configPath, _ := cmd.Flags().GetString("config")
	cfg, loadErr := config.Load(configPath)
	if loadErr != nil {
		return fmt.Errorf("load config: %w", loadErr)
	}

	// 2. 校验 service
	if service != "" {
		if svcCfg := cfg.GetServiceConfig(service); svcCfg == nil {
			return fmt.Errorf("service %q 不在 config.services 中", service)
		}
	}
	if profile != "" {
		if svcCfg := cfg.GetServiceConfig(profile); svcCfg == nil {
			return fmt.Errorf("profile target %q 不在 config.services 中", profile)
		}
		// --profile 等同于 --service，但标记为 profile 流水线
		service = profile
	}

	// 3. 创建 Pipeline
	fmt.Fprintf(os.Stderr, "[run] 正在初始化流水线...\n")
	pipeline, err := orchestrator.NewPipelineFromConfig(cfg)
	if err != nil {
		return fmt.Errorf("init pipeline: %w", err)
	}

	// 4. 构建 RunSpec
	spec := orchestrator.RunSpec{
		IssueURL: issue,
		Desc:     desc,
		Service:  service,
		Profile:  profile,
		TraceID:  traceID,
	}

	// 5. 设置信号处理（Ctrl+C 优雅退出）
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		<-sigCh
		fmt.Fprintf(os.Stderr, "\n[run] 收到中断信号，正在停止...\n")
		cancel()
	}()

	// 6. 执行流水线
	fmt.Fprintf(os.Stderr, "[run] 启动流水线: issue=%q desc=%q service=%q profile=%q trace_id=%q\n",
		issue, desc, service, profile, traceID)

	startTime := time.Now()
	status, runErr := pipeline.Run(ctx, spec)

	elapsed := time.Since(startTime).Round(time.Second)

	if runErr != nil {
		fmt.Fprintf(os.Stderr, "[run] 流水线执行出错: %v\n", runErr)
	}

	// 7. 输出执行摘要
	fmt.Println()
	fmt.Println("========== 流水线执行摘要 ==========")
	if status != nil {
		fmt.Printf("Task ID:      %s\n", status.TaskID)
		fmt.Printf("当前阶段:     %s\n", status.CurrentStage)
		fmt.Printf("总耗时:       %s\n", elapsed)
		fmt.Printf("阶段数:       %d\n", len(status.StageResults))
		fmt.Println()
		fmt.Println("阶段结果:")
		for _, r := range status.StageResults {
			statusIcon := "OK"
			if r.Error != nil {
				statusIcon = "ERROR"
			} else if r.Status == "blocked" {
				statusIcon = "BLOCKED"
			}
			fmt.Printf("  [%s] %-10s 节点=%s  耗时=%s",
				statusIcon, r.Stage, r.NodeID, r.Duration.Round(time.Second))
			if r.Error != nil {
				fmt.Printf("  error=%v", r.Error)
			}
			fmt.Println()
		}
	} else {
		fmt.Printf("状态: 无 (status=nil)\n")
		fmt.Printf("总耗时: %s\n", elapsed)
	}
	fmt.Println("=====================================")

	if runErr != nil {
		return fmt.Errorf("pipeline failed: %w", runErr)
	}

	if status != nil {
		fmt.Fprintf(os.Stderr, "\n[run] 流水线完成: task=%s\n", status.TaskID)
	}
	return nil
}
