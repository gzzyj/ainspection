// Package doctor 提供环境诊断检查逻辑（P1-1）。
package doctor

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"git.qingteng.cn/ms/ainspection/internal/config"
)

// Status 检查结果状态。
type Status string

const (
	StatusPass Status = "OK"
	StatusWarn Status = "WARN"
	StatusFail Status = "FAIL"
)

// CheckResult 单条检查结果。
type CheckResult struct {
	Name   string // 检查项名称（如 "go", "claude API key"）
	Status Status
	Detail string // 版本号或错误详情
}

// Checker 环境诊断检查器。
type Checker struct {
	cfg *config.Config
}

// NewChecker 创建 Checker 实例。
func NewChecker(cfg *config.Config) *Checker {
	return &Checker{cfg: cfg}
}

// RunAll 执行所有检查并返回结果列表。
func (c *Checker) RunAll() []CheckResult {
	var results []CheckResult

	results = append(results, c.checkToolChain()...)
	results = append(results, c.checkAPIKeys()...)
	results = append(results, c.checkServiceConnectivity()...)
	results = append(results, c.checkConfig()...)
	results = append(results, c.checkSandbox())
	results = append(results, c.checkProfilingDeps()...)

	return results
}

// HasHardFailure 判断结果中是否包含硬失败（exit 2）。
func HasHardFailure(results []CheckResult) bool {
	for _, r := range results {
		if r.Status == StatusFail {
			return true
		}
	}
	return false
}

// HasWarning 判断结果中是否包含警告。
func HasWarning(results []CheckResult) bool {
	for _, r := range results {
		if r.Status == StatusWarn {
			return true
		}
	}
	return false
}

// —————— 工具链检查 ——————

// requiredTools 定义必需的工具及其版本提取命令。
var requiredTools = []struct {
	name    string
	verArgs []string
}{
	{"go", []string{"version"}},
	{"kubectl", []string{"version", "--client=true"}},
	{"skaffold", []string{"version"}},
	{"glab", []string{"version"}},
	{"golangci-lint", []string{"version"}},
	{"gofumpt", []string{"-version"}},
	{"goimports", []string{"-h"}}, // goimports 无 --version，用 -h 检测存在
	{"docker", []string{"version"}},
}

func (c *Checker) checkToolChain() []CheckResult {
	var results []CheckResult
	details := make([]string, 0, len(requiredTools))
	allOK := true

	for _, t := range requiredTools {
		path, err := exec.LookPath(t.name)
		if err != nil {
			results = append(results, CheckResult{
				Name: t.name, Status: StatusFail,
				Detail: "not found in PATH",
			})
			allOK = false
			continue
		}

		ver := ""
		if len(t.verArgs) > 0 {
			ver = getVersion(path, t.verArgs)
		}
		if ver == "" {
			ver = "installed"
		}

		details = append(details, fmt.Sprintf("%s(%s)", t.name, ver))
	}

	if allOK {
		results = append(results, CheckResult{
			Name:   "Tool chain",
			Status: StatusPass,
			Detail: strings.Join(details, "/"),
		})
	}

	return results
}

// getVersion 执行 cmd --version 并提取首行作为版本。
func getVersion(cmdPath string, args []string) string {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	out, err := exec.CommandContext(ctx, cmdPath, args...).Output()
	if err != nil {
		return "unknown"
	}

	line := strings.SplitN(strings.TrimSpace(string(out)), "\n", 2)[0]
	line = strings.TrimSpace(line)

	// 截取版本号（取前 50 字符）
	if len(line) > 50 {
		line = line[:50]
	}
	return line
}

// —————— API Key 检查 ——————

func (c *Checker) checkAPIKeys() []CheckResult {
	if c.cfg == nil {
		return []CheckResult{{Name: "API keys", Status: StatusWarn, Detail: "config not loaded"}}
	}

	var missing []string

	for name, agent := range c.cfg.Agents {
		if agent.APIKey == "" {
			missing = append(missing, name)
		}
	}

	if c.cfg.GitLab.Token == "" {
		missing = append(missing, "gitlab")
	}

	if c.cfg.Lark.AppSecret == "" {
		missing = append(missing, "lark")
	}

	if len(missing) > 0 {
		return []CheckResult{{
			Name:   "API keys",
			Status: StatusFail,
			Detail: fmt.Sprintf("missing: %s", strings.Join(missing, ", ")),
		}}
	}

	agentNames := make([]string, 0, len(c.cfg.Agents))
	for name := range c.cfg.Agents {
		agentNames = append(agentNames, name)
	}

	return []CheckResult{{
		Name:   "API keys",
		Status: StatusPass,
		Detail: fmt.Sprintf("%s/gitlab/lark configured", strings.Join(agentNames, "/")),
	}}
}

// —————— 服务连通性检查 ——————

func (c *Checker) checkServiceConnectivity() []CheckResult {
	if c.cfg == nil {
		return nil
	}

	var results []CheckResult

	// Loki
	if url := c.cfg.Observability.LokiURL; url != "" {
		results = append(results, c.checkHTTP("Loki", url))
	}

	// Prometheus
	if url := c.cfg.Observability.PrometheusURL; url != "" {
		results = append(results, c.checkHTTP("Prometheus", url))
	}

	// Tempo
	if url := c.cfg.Observability.TempoURL; url != "" {
		results = append(results, c.checkHTTP("Tempo", url))
	}

	// k8s context
	if c.cfg.K3S.Kubeconfig != "" {
		results = append(results, c.checkK8sContext())
	}

	// git remote (从 services[0].repo_path 获取)
	if len(c.cfg.Services) > 0 {
		results = append(results, c.checkGitRemote(c.cfg.Services[0].RepoPath))
	}

	return results
}

func (c *Checker) checkHTTP(name, url string) CheckResult {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return CheckResult{Name: name, Status: StatusWarn, Detail: fmt.Sprintf("bad url: %v", err)}
	}

	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return CheckResult{Name: name, Status: StatusWarn, Detail: fmt.Sprintf("connect failed: %v", err)}
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 200 && resp.StatusCode < 500 {
		return CheckResult{Name: name, Status: StatusPass, Detail: fmt.Sprintf("%d %s", resp.StatusCode, http.StatusText(resp.StatusCode))}
	}
	return CheckResult{Name: name, Status: StatusWarn, Detail: fmt.Sprintf("status %d", resp.StatusCode)}
}

func (c *Checker) checkK8sContext() CheckResult {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	args := []string{"cluster-info"}
	if c.cfg.K3S.Kubeconfig != "" {
		args = append(args, "--kubeconfig", c.cfg.K3S.Kubeconfig)
	}
	if c.cfg.K3S.Context != "" {
		args = append(args, "--context", c.cfg.K3S.Context)
	}

	cmd := exec.CommandContext(ctx, "kubectl", args...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return CheckResult{
			Name:   "k8s context",
			Status: StatusWarn,
			Detail: fmt.Sprintf("kubectl cluster-info failed: %v", err),
		}
	}

	// 提取第一行作为摘要
	line := strings.SplitN(string(out), "\n", 2)[0]
	return CheckResult{
		Name:   "k8s context",
		Status: StatusPass,
		Detail: strings.TrimSpace(line),
	}
}

func (c *Checker) checkGitRemote(repoPath string) CheckResult {
	if repoPath == "" {
		return CheckResult{Name: "git remote", Status: StatusWarn, Detail: "no repo_path configured"}
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, "git", "-C", repoPath, "remote", "-v")
	out, err := cmd.CombinedOutput()
	if err != nil {
		return CheckResult{
			Name:   "git remote",
			Status: StatusWarn,
			Detail: fmt.Sprintf("git remote failed: %v", err),
		}
	}

	line := strings.SplitN(strings.TrimSpace(string(out)), "\n", 2)[0]
	if len(line) > 80 {
		line = line[:80]
	}
	return CheckResult{
		Name:   "git remote",
		Status: StatusPass,
		Detail: strings.TrimSpace(line),
	}
}

// —————— 配置文件检查 ——————

func (c *Checker) checkConfig() []CheckResult {
	if c.cfg == nil {
		return []CheckResult{{Name: "Config file", Status: StatusWarn, Detail: "not loaded"}}
	}

	if len(c.cfg.Services) == 0 {
		return []CheckResult{{Name: "Config file", Status: StatusFail, Detail: "services 数组为空"}}
	}

	svcNames := make([]string, len(c.cfg.Services))
	for i, s := range c.cfg.Services {
		svcNames[i] = s.Name
	}

	return []CheckResult{{
		Name:   "Config file",
		Status: StatusPass,
		Detail: fmt.Sprintf("%d service(s): %s", len(c.cfg.Services), strings.Join(svcNames, ", ")),
	}}
}

// —————— 沙箱目录检查 ——————

func (c *Checker) checkSandbox() CheckResult {
	root := "~/.ainspection/sessions/"
	if c.cfg != nil && c.cfg.Security.Sandbox.WorkingDirRoot != "" {
		root = c.cfg.Security.Sandbox.WorkingDirRoot
	} else if c.cfg != nil {
		root = c.cfg.GetDataDir() + "/sessions/"
	}

	expanded := expandPath(root)

	// 检查目录是否存在 + 可写
	if err := os.MkdirAll(expanded, 0o755); err != nil {
		return CheckResult{
			Name:   "Sandbox",
			Status: StatusFail,
			Detail: fmt.Sprintf("%s not writable: %v", root, err),
		}
	}

	// 创建测试文件验证可写
	testFile := filepath.Join(expanded, ".doctor-write-test")
	if err := os.WriteFile(testFile, []byte("test"), 0o644); err != nil {
		return CheckResult{
			Name:   "Sandbox",
			Status: StatusFail,
			Detail: fmt.Sprintf("%s not writable: %v", root, err),
		}
	}
	os.Remove(testFile)

	return CheckResult{
		Name:   "Sandbox",
		Status: StatusPass,
		Detail: fmt.Sprintf("%s writable", root),
	}
}

// —————— Profiling 运行时依赖检查 ——————

func (c *Checker) checkProfilingDeps() []CheckResult {
	var results []CheckResult

	// 1. docker daemon (FAIL)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, "docker", "info")
	if err := cmd.Run(); err != nil {
		results = append(results, CheckResult{
			Name:   "profiling: docker daemon",
			Status: StatusFail,
			Detail: fmt.Sprintf("docker info failed: %v", err),
		})
	} else {
		results = append(results, CheckResult{
			Name:   "profiling: docker daemon",
			Status: StatusPass,
			Detail: "docker daemon running",
		})
	}

	// 2. kubectl cluster (WARN)
	kctx, kcancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer kcancel()

	kiArgs := []string{"cluster-info"}
	if c.cfg != nil && c.cfg.K3S.Kubeconfig != "" {
		kiArgs = append(kiArgs, "--kubeconfig", c.cfg.K3S.Kubeconfig)
	}
	if c.cfg != nil && c.cfg.K3S.Context != "" {
		kiArgs = append(kiArgs, "--context", c.cfg.K3S.Context)
	}

	kcmd := exec.CommandContext(kctx, "kubectl", kiArgs...)
	if out, err := kcmd.CombinedOutput(); err != nil {
		results = append(results, CheckResult{
			Name:   "profiling: kubectl cluster",
			Status: StatusWarn,
			Detail: fmt.Sprintf("kubectl cluster-info failed: %v", err),
		})
	} else {
		line := strings.SplitN(string(out), "\n", 2)[0]
		results = append(results, CheckResult{
			Name:   "profiling: kubectl cluster",
			Status: StatusPass,
			Detail: strings.TrimSpace(line),
		})
	}

	// 3. Alloy DaemonSet (WARN — 首次部署前可能不存在)
	dsCtx, dsCancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer dsCancel()

	dsArgs := []string{"get", "daemonset", "alloy", "-n", "ainspection"}
	if c.cfg != nil && c.cfg.K3S.Kubeconfig != "" {
		dsArgs = append(dsArgs, "--kubeconfig", c.cfg.K3S.Kubeconfig)
	}
	if c.cfg != nil && c.cfg.K3S.Context != "" {
		dsArgs = append(dsArgs, "--context", c.cfg.K3S.Context)
	}

	dsCmd := exec.CommandContext(dsCtx, "kubectl", dsArgs...)
	if err := dsCmd.Run(); err != nil {
		results = append(results, CheckResult{
			Name:   "profiling: Alloy DaemonSet",
			Status: StatusWarn,
			Detail: "Alloy DaemonSet 未部署（首次部署前正常）",
		})
	} else {
		results = append(results, CheckResult{
			Name:   "profiling: Alloy DaemonSet",
			Status: StatusPass,
			Detail: "Alloy DaemonSet 已部署",
		})
	}

	return results
}

// —————— 辅助 ——————

func expandPath(path string) string {
	if strings.HasPrefix(path, "~/") {
		home, err := os.UserHomeDir()
		if err == nil {
			path = filepath.Join(home, path[2:])
		}
	}
	return path
}

