// Package eval 提供 ainspection 离线评测功能 (L2 Smoke Test + Case Regression)。
package eval

import "time"

// SmokeResult 单个模板的 smoke test 结果。
type SmokeResult struct {
	Template     string   // 模板名（如 "get-system"）
	Passed       bool     // 模板解析+执行是否成功
	CharCount    int      // 渲染输出字符数
	InputFields  []string // 使用的 Input 字段名
	Error        string   // 失败时的错误信息
}

// CaseResult 单个 case 的回归评测结果。
type CaseResult struct {
	CaseID       string               // case 名称（如 "error-fix-1"）
	Passed       bool                  // 全部 stage 结构校验通过
	StageResults map[string]StageCheck // 各 stage 的结构校验结果
	Error        string                // 失败信息
}

// StageCheck 单个 stage 的输出结构校验结果。
type StageCheck struct {
	Passed    bool     // 结构校验通过
	Missing   []string // 缺失的字段
	ExtraType []string // 类型不匹配的字段
}

// EvalReport 评测报告（聚合 smoke + case 结果）。
type EvalReport struct {
	GeneratedAt  time.Time
	SmokeResults []SmokeResult
	CaseResults  []CaseResult
}

// Passed returns true if all tests (smoke + case) passed.
func (r *EvalReport) Passed() bool {
	for _, s := range r.SmokeResults {
		if !s.Passed {
			return false
		}
	}
	for _, c := range r.CaseResults {
		if !c.Passed {
			return false
		}
	}
	return true
}

// SmokePassed returns the count of passed smoke tests.
func (r *EvalReport) SmokePassed() (passed, total int) {
	total = len(r.SmokeResults)
	for _, s := range r.SmokeResults {
		if s.Passed {
			passed++
		}
	}
	return
}

// CasePassed returns the count of passed case tests.
func (r *EvalReport) CasePassed() (passed, total int) {
	total = len(r.CaseResults)
	for _, c := range r.CaseResults {
		if c.Passed {
			passed++
		}
	}
	return
}

// EvalCase 从 YAML 文件加载的评测 case 定义。
type EvalCase struct {
	ID             string
	Input          CaseInput
	ExpectedSchema CaseSchema
}

// CaseInput 各 stage 的输入数据（对应 input.yaml）。
type CaseInput struct {
	Get    map[string]any `yaml:"get"`
	Locate map[string]any `yaml:"locate"`
	Plan   map[string]any `yaml:"plan"`
	Fix    map[string]any `yaml:"fix"`
}

// CaseSchema 期望的输出 schema（对应 expected_schema.yaml）。
type CaseSchema struct {
	Stages map[string]SchemaField `yaml:",inline"`
}

// SchemaField 单个字段的 schema 定义。
type SchemaField struct {
	Type     string                 `yaml:"type"`
	Children map[string]SchemaField `yaml:"children,omitempty"`
}
