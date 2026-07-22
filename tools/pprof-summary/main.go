// pprof-summary 薄包装 go tool pprof CLI，生成 LLM 可读的 profile 摘要。
//
// 用法:
//
//	pprof-summary --service=kernel --profile-type=cpu
//	pprof-summary --input=/tmp/cpu.pb.gz --format=top-only
//	pprof-summary --input=after.pb.gz --diff-base=before.pb.gz
//
// 输出 JSON: {"meta": {...}, "top": "...", "tree": "...", "traces": "..."}
//
// 数据源:
//   - Pyroscope: HTTP GET /pyroscope/render?query=...&format=pprof
//   - 本地文件: --input <file>
//
// 内部调用 go tool pprof -top/-tree/-traces，原生文本输出直接嵌入 JSON。
package main

import (
	"compress/gzip"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"regexp"
	"strings"
	"time"
)

// Meta 存放 profile 元信息。
type Meta struct {
	ProfileType  string `json:"profile_type"`
	TotalSamples string `json:"total_samples"`
	Duration     string `json:"duration,omitempty"`
	TimeRange    string `json:"time_range,omitempty"`
	Source       string `json:"source"`
	DiffBase     string `json:"diff_base,omitempty"`
}

// Output 是 pprof-summary 的 JSON 顶层结构。
type Output struct {
	Meta   *Meta  `json:"meta"`
	Top    string `json:"top,omitempty"`
	Tree   string `json:"tree,omitempty"`
	Traces string `json:"traces,omitempty"`
}

func main() {
	var (
		service      string
		profileType  string
		topN         int
		pyroscopeURL string
		from         string
		until        string
		inputFile    string
		format       string
		diffBase     string
		function     string
	)

	flag.StringVar(&service, "service", "", "服务名（必填，除非指定 --input）")
	flag.StringVar(&profileType, "profile-type", "cpu", "cpu|heap|goroutine|mutex|block")
	flag.IntVar(&topN, "top-n", 15, "TopN 数量")
	flag.StringVar(&pyroscopeURL, "pyroscope-url", "http://10.106.19.42:4040", "Pyroscope 地址")
	flag.StringVar(&from, "from", "now-5m", "时间范围起点")
	flag.StringVar(&until, "until", "now", "时间范围终点")
	flag.StringVar(&inputFile, "input", "", "本地 pprof 文件路径（指定后跳过 Pyroscope 查询）")
	flag.StringVar(&format, "format", "full", "输出格式: full|top-only|tree-only|traces-only")
	flag.StringVar(&diffBase, "diff-base", "", "对比基线文件（与 --input 配合使用）")
	flag.StringVar(&function, "function", "", "聚焦函数 regex（传递给 pprof -peek）")

	flag.Usage = func() {
		fmt.Fprintf(os.Stderr, "Usage: pprof-summary [flags]\n\nFlags:\n")
		flag.PrintDefaults()
	}
	flag.Parse()

	// --- flag 校验 ---

	if inputFile == "" && service == "" {
		fmt.Fprintln(os.Stderr, "pprof-summary: --service is required when --input is not specified")
		os.Exit(2)
	}

	validFormats := map[string]bool{"full": true, "top-only": true, "tree-only": true, "traces-only": true}
	if !validFormats[format] {
		fmt.Fprintf(os.Stderr, "pprof-summary: invalid --format %q (must be full|top-only|tree-only|traces-only)\n", format)
		os.Exit(2)
	}

	validProfileTypes := map[string]bool{"cpu": true, "heap": true, "goroutine": true, "mutex": true, "block": true}
	if !validProfileTypes[profileType] {
		fmt.Fprintf(os.Stderr, "pprof-summary: invalid --profile-type %q\n", profileType)
		os.Exit(2)
	}

	if _, err := exec.LookPath("go"); err != nil {
		fmt.Fprintln(os.Stderr, "pprof-summary: go not found in PATH")
		os.Exit(1)
	}

	// --- 1. 获取 pprof 数据 ---

	var pprofFile string
	var source string
	var timeRange string

	if inputFile != "" {
		if _, err := os.Stat(inputFile); os.IsNotExist(err) {
			fmt.Fprintf(os.Stderr, "pprof-summary: input file not found: %s\n", inputFile)
			os.Exit(1)
		}
		pprofFile = inputFile
		source = fmt.Sprintf("local: %s", inputFile)
	} else {
		var err error
		pprofFile, err = fetchFromPyroscope(pyroscopeURL, service, profileType, from, until)
		if err != nil {
			fmt.Fprintf(os.Stderr, "pprof-summary: pyroscope error: %v\n", err)
			os.Exit(1)
		}
		defer os.Remove(pprofFile)
		source = fmt.Sprintf("pyroscope: %s", pyroscopeURL)
		timeRange = fmt.Sprintf("%s ~ %s", from, until)
	}

	// --- 2. 构建 pprof 参数 ---

	pprofArgs := []string{}
	if diffBase != "" {
		pprofArgs = append(pprofArgs, "-diff_base="+diffBase)
	}

	meta := &Meta{
		ProfileType: profileType,
		Source:      source,
		TimeRange:   timeRange,
	}
	if diffBase != "" {
		meta.DiffBase = diffBase
	}

	out := &Output{Meta: meta}

	doTop := format == "full" || format == "top-only"
	doTree := format == "full" || format == "tree-only"
	doTraces := format == "full" || format == "traces-only"

	// --- 3. 调用 go tool pprof ---

	if doTop {
		args := append([]string{}, pprofArgs...)
		args = append(args, "-top", fmt.Sprintf("-nodecount=%d", topN), pprofFile)
		text, err := runPprof(args...)
		if err != nil {
			fmt.Fprintln(os.Stderr, err.Error())
			os.Exit(1)
		}
		out.Top = text
		meta.TotalSamples = extractTotal(text)
	}

	if doTree {
		args := append([]string{}, pprofArgs...)
		args = append(args, "-tree", pprofFile)
		text, err := runPprof(args...)
		if err != nil {
			fmt.Fprintln(os.Stderr, err.Error())
			os.Exit(1)
		}
		out.Tree = text
	}

	if doTraces {
		args := append([]string{}, pprofArgs...)
		args = append(args, "-traces", pprofFile)
		text, err := runPprof(args...)
		if err != nil {
			fmt.Fprintln(os.Stderr, err.Error())
			os.Exit(1)
		}
		out.Traces = text
	}

	if function != "" {
		args := append([]string{}, pprofArgs...)
		args = append(args, "-peek", function, pprofFile)
		text, err := runPprof(args...)
		if err != nil {
			fmt.Fprintf(os.Stderr, "pprof-summary: pprof -peek error: %v\n", err)
			os.Exit(1)
		}
		out.Top = text
	}

	// 0 样本的 profile：用 "(empty)" 占位空字段。
	if meta.TotalSamples == "0" || meta.TotalSamples == "" {
		if strings.TrimSpace(out.Top) == "" && doTop {
			out.Top = "(empty)"
		}
		if strings.TrimSpace(out.Tree) == "" && doTree {
			out.Tree = "(empty)"
		}
		if strings.TrimSpace(out.Traces) == "" && doTraces {
			out.Traces = "(empty)"
		}
	}

	// --- 4. 输出 JSON ---

	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	if err := enc.Encode(out); err != nil {
		fmt.Fprintf(os.Stderr, "pprof-summary: json encode error: %v\n", err)
		os.Exit(1)
	}
}

// fetchFromPyroscope 从 Pyroscope 拉取 profile 并写入临时文件。
func fetchFromPyroscope(baseURL, service, profileType, from, until string) (string, error) {
	query := fmt.Sprintf("%s.%s", service, profileType)

	u, err := url.Parse(baseURL)
	if err != nil {
		return "", fmt.Errorf("invalid pyroscope URL: %w", err)
	}
	u = u.JoinPath("/pyroscope/render")
	u.RawQuery = url.Values{
		"query":  {query},
		"from":   {from},
		"until":  {until},
		"format": {"pprof"},
	}.Encode()

	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Get(u.String())
	if err != nil {
		return "", fmt.Errorf("HTTP request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		return "", fmt.Errorf("%d %s: %s", resp.StatusCode, http.StatusText(resp.StatusCode), strings.TrimSpace(string(body)))
	}

	tmpFile, err := os.CreateTemp("", "ainspection-pprof-*.pb.gz")
	if err != nil {
		return "", fmt.Errorf("temp file error: %w", err)
	}

	reader := io.Reader(resp.Body)
	if resp.Header.Get("Content-Encoding") == "gzip" {
		gzReader, err := gzip.NewReader(resp.Body)
		if err != nil {
			tmpFile.Close()
			os.Remove(tmpFile.Name())
			return "", fmt.Errorf("gzip decode error: %w", err)
		}
		defer gzReader.Close()
		reader = gzReader
	}

	if _, err := io.Copy(tmpFile, reader); err != nil {
		tmpFile.Close()
		os.Remove(tmpFile.Name())
		return "", fmt.Errorf("write temp file error: %w", err)
	}

	if err := tmpFile.Close(); err != nil {
		os.Remove(tmpFile.Name())
		return "", fmt.Errorf("close temp file error: %w", err)
	}

	return tmpFile.Name(), nil
}

// runPprof 执行 go tool pprof 并返回文本输出。
func runPprof(args ...string) (string, error) {
	cmdArgs := append([]string{"tool", "pprof"}, args...)
	cmd := exec.Command("go", cmdArgs...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("pprof-summary: pprof parse error:\n%s", string(out))
	}
	return string(out), nil
}

// totalRe 从 pprof -top 输出首行提取总采样值。
// 格式: "Showing nodes accounting for X, Y% of Z total"
var totalRe = regexp.MustCompile(`of\s+(\S+)\s+total`)

func extractTotal(topOutput string) string {
	lines := strings.SplitN(topOutput, "\n", 3)
	for _, line := range lines {
		if m := totalRe.FindStringSubmatch(line); m != nil {
			return m[1]
		}
	}
	return ""
}
