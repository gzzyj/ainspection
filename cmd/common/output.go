package common

import (
	"fmt"
	"io"
	"os"
	"strings"
	"text/tabwriter"
)

// OutputFormat 输出格式（P0 仅实现 table，json/yaml 后续补充）。
type OutputFormat int

const (
	FormatTable OutputFormat = iota
	FormatJSON
	FormatYAML
)

// OutputWriter 统一输出封装。
type OutputWriter struct {
	w      io.Writer
	format OutputFormat
}

// NewOutputWriter 创建一个输出写入器。
func NewOutputWriter(format OutputFormat) *OutputWriter {
	return &OutputWriter{w: os.Stdout, format: format}
}

// Table 输出表格格式的数据。
// headers 为列标题，rows 为数据行（每行字段数与 headers 相同）。
func (o *OutputWriter) Table(headers []string, rows [][]string) {
	w := tabwriter.NewWriter(o.w, 0, 0, 2, ' ', 0)

	// 表头
	fmt.Fprintln(w, strings.Join(headers, "\t"))

	// 数据行
	for _, row := range rows {
		fmt.Fprintln(w, strings.Join(row, "\t"))
	}

	w.Flush()
}

// Line 输出单行文本。
func (o *OutputWriter) Line(format string, args ...any) {
	fmt.Fprintf(o.w, format+"\n", args...)
}

// OK 输出 [OK] 前缀的成功信息。
func OK(format string, args ...any) {
	fmt.Printf("[OK]   "+format+"\n", args...)
}

// Warn 输出 [WARN] 前缀的警告信息。
func Warn(format string, args ...any) {
	fmt.Printf("[WARN] "+format+"\n", args...)
}

// Err 输出 [ERR] 前缀的错误信息。
func Err(format string, args ...any) {
	fmt.Printf("[ERR]  "+format+"\n", args...)
}
