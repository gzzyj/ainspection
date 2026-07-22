// Package prompt 提供 prompt 模板的加载、渲染和管理。
//
// 所有 13 份 .tmpl 文件使用 Go text/template 语法，由本 package 的 Renderer 集中渲染。
// 每个模板的变量通过对应的 Go struct 定义，避免分散在各调用方。
package prompt

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"text/template"

	"gopkg.in/yaml.v3"
)

// Frontmatter prompt 模板的 YAML 头信息。
type Frontmatter struct {
	Name        string `yaml:"name"`
	Description string `yaml:"description"`
	Version     string `yaml:"version"`
}

// Template 加载并解析后的 prompt 模板。
type Template struct {
	Frontmatter Frontmatter
	tmpl        *template.Template
	name        string
}

// Renderer prompt 模板渲染引擎接口。
type Renderer interface {
	// Render 渲染指定名称的模板。
	Render(name string, data any) (string, error)

	// RenderToBytes 渲染模板并返回字节数组。
	RenderToBytes(name string, data any) ([]byte, error)
}

// rendererImpl Renderer 的内存实现，启动时一次性加载全部模板。
type rendererImpl struct {
	templates map[string]*Template // key = 模板名（如 "get-system"）
}

// NewRenderer 从指定目录加载所有 .tmpl 文件并创建 Renderer。
func NewRenderer(dir string) (Renderer, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, fmt.Errorf("read prompt dir %s: %w", dir, err)
	}

	r := &rendererImpl{
		templates: make(map[string]*Template),
	}

	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".tmpl") {
			continue
		}

		t, err := r.loadTemplate(filepath.Join(dir, entry.Name()))
		if err != nil {
			return nil, fmt.Errorf("load template %s: %w", entry.Name(), err)
		}

		name := strings.TrimSuffix(entry.Name(), ".tmpl")
		r.templates[name] = t
	}

	return r, nil
}

// loadTemplate 解析单个 .tmpl 文件：先提取 YAML frontmatter，再编译 Go template。
func (r *rendererImpl) loadTemplate(path string) (*Template, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}

	content := string(raw)

	// 提取 YAML frontmatter（--- ... ---）
	var fm Frontmatter
	tmplContent := content

	if strings.HasPrefix(content, "---\n") {
		endIdx := strings.Index(content[4:], "\n---\n")
		if endIdx > 0 {
			fmYaml := content[4 : 4+endIdx]
			if err := yaml.Unmarshal([]byte(fmYaml), &fm); err != nil {
				return nil, fmt.Errorf("parse frontmatter: %w", err)
			}
			tmplContent = content[4+endIdx+5:] // 跳过 frontmatter + 分隔线
		}
	}

	tmpl, err := template.New(filepath.Base(path)).Parse(tmplContent)
	if err != nil {
		return nil, fmt.Errorf("parse template: %w", err)
	}

	name := strings.TrimSuffix(filepath.Base(path), ".tmpl")
	return &Template{
		Frontmatter: fm,
		tmpl:        tmpl,
		name:        name,
	}, nil
}

// Render 渲染指定名称的模板。
func (r *rendererImpl) Render(name string, data any) (string, error) {
	t, ok := r.templates[name]
	if !ok {
		return "", fmt.Errorf("template %q not found (available: %v)", name, r.listNames())
	}

	var buf bytes.Buffer
	if err := t.tmpl.Execute(&buf, data); err != nil {
		return "", fmt.Errorf("execute template %q: %w", name, err)
	}

	return buf.String(), nil
}

// RenderToBytes 渲染模板并返回字节数组。
func (r *rendererImpl) RenderToBytes(name string, data any) ([]byte, error) {
	s, err := r.Render(name, data)
	if err != nil {
		return nil, err
	}
	return []byte(s), nil
}

// listNames 返回所有已加载的模板名。
func (r *rendererImpl) listNames() []string {
	names := make([]string, 0, len(r.templates))
	for k := range r.templates {
		names = append(names, k)
	}
	return names
}

// GetTemplate 返回指定模板的 Frontmatter 信息。
func (r *rendererImpl) GetTemplate(name string) (*Template, error) {
	t, ok := r.templates[name]
	if !ok {
		return nil, fmt.Errorf("template %q not found", name)
	}
	return t, nil
}

// --- 所有模板变量的 Go struct 定义 ---

// GetInput get-system.tmpl 的模板变量。
type GetInput struct {
	IssueURL    string
	Desc        string
	Service     string
	JiraContent string // jira-query skill 拉回的原始内容
	TraceID     string // 已知的 trace ID，可选
}

// LocateInput locate-system.tmpl 的模板变量。
type LocateInput struct {
	InputYAML     string // input.yaml 内容
	ParentSummary string // 父节点（get 阶段）摘要
	Service       string
	Skills        []SkillDesc
	AgentConfig   AgentDesc
	TraceID       string // 已知的 trace ID，可选
}

// LocateDiscloseInput locate-disclose.tmpl 的模板变量。
type LocateDiscloseInput struct {
	Findings            []FindingData
	DiscardedHypotheses []DiscardedHypothesisData
	SignalsSummary      string
	CurrentRound        int
	MaxRounds           int
}

// FindingData 单个 found 的假设数据。
type FindingData struct {
	Hypothesis     string
	Confidence     float64
	Evidence       []string
	ConfidenceSelf float64
}

// DiscardedHypothesisData 已排除的假设数据。
type DiscardedHypothesisData struct {
	Hypothesis      string
	EvidenceAgainst string
}

// MCTSExpandInput mcts-expand.tmpl 的模板变量。
type MCTSExpandInput struct {
	Stage           string // "locate" | "fix"
	CurrentNode     string // 当前节点描述（假设或 plan.step）
	Depth           int
	MaxDepth        int
	BranchingFactor int
	ParentSummary   string
}

// PlanInput plan-system.tmpl 的模板变量（Planner Agent 独立 session）。
type PlanInput struct {
	Findings            []FindingData
	DiscardedHypotheses []DiscardedHypothesisData
	ParentSummary       string
	UserDirectives      []string
	AvailableSkills     []SkillDesc
}

// FixInput fix-system.tmpl 的模板变量。
type FixInput struct {
	PlanJSON      string // plan.json 内容
	Service       string
	RepoPath      string
	Skills        []SkillDesc
	AgentConfig   AgentDesc
	ParentSummary string
}

// VerifyInput verify-system.tmpl 的模板变量。
type VerifyInput struct {
	Service       string
	RepoPath      string
	Patches       []string // 应用的 diff 文件列表
	Skills        []SkillDesc
	ParentSummary string
}

// CommitInput commit-system.tmpl 的模板变量。
type CommitInput struct {
	Service       string
	RepoPath      string
	Patches       []string
	JiraID        string
	ParentSummary string
}

// ReviewLocateInput review-locate.tmpl 的模板变量。
type ReviewLocateInput struct {
	InputYAML string
	Findings  []FindingData
	Signals   string
}

// ReviewFixInput review-fix.tmpl 的模板变量。
type ReviewFixInput struct {
	InputYAML string
	PlanJSON  string
	Diffs     []string // 各 patch 的 unified diff
}

// ReviewVerifyInput review-verify.tmpl 的模板变量。
type ReviewVerifyInput struct {
	InputYAML     string
	PlanJSON      string
	DeploySuccess bool
	HTTPStatus    int
	TestPassed    bool
	NewErrors     []string
}

// Review1Input review1-system.tmpl 的模板变量（locate 后审查）。
type Review1Input struct {
	Findings []FindingData // locate 产出的 findings
	Stage    string        // 审查来源阶段
	NodeID   string        // 被审查节点 ID
}

// Review2Input review2-system.tmpl 的模板变量（fix 后审查）。
type Review2Input struct {
	Diff     string // unified diff 内容
	PlanJSON string // plan.json 内容
	Stage    string // 审查来源阶段
	NodeID   string // 被审查节点 ID
}

// Review3Input review3-system.tmpl 的模板变量（verify 后审查）。
type Review3Input struct {
	VerifyPassed bool   // verify 阶段判定 passed/failed
	Stage        string // 审查来源阶段
	NodeID       string // 被审查节点 ID
}

// ProfileAnalyzeInput profile-analyze.tmpl 的模板变量。
type ProfileAnalyzeInput struct {
	Service     string // 服务名
	ProfileType string // cpu | heap | goroutine | mutex | block
	Top         string // go tool pprof -top 文本输出（嵌入式表格）
	Tree        string // go tool pprof -tree 文本输出（含 callers/callees 上下文）
	Traces      string // go tool pprof -traces 文本输出（采样级调用栈）
}

// ProfileFixInput profile-fix.tmpl 的模板变量。
type ProfileFixInput struct {
	PlanJSON string // plan 阶段输出的 plan.json
	Analysis string // profile-analyze 的输出
	Service  string
	RepoPath string
	Skills   []SkillDesc
}

// RunOrchestratorInput run-orchestrator.tmpl 的模板变量。
type RunOrchestratorInput struct {
	Stages      []StageDesc
	GateRules   []GateRuleDesc
	ToolAssigns []ToolAssignDesc
}

// StageDesc 阶段描述。
type StageDesc struct {
	Name        string
	Description string
	Skills      []string
	GatingTo    string
}

// GateRuleDesc 门控规则描述。
type GateRuleDesc struct {
	From       string
	To         string
	Condition  string
	BlockedMsg string
}

// ToolAssignDesc 工具分配描述。
type ToolAssignDesc struct {
	Stage        string
	L1Skills     []string
	L2Native     []string
	L3Bash       []string
}

// --- 辅助类型 ---

// SkillDesc skill 描述（精简版，仅注入 prompt 所需字段）。
type SkillDesc struct {
	Name        string
	Description string
	Parameters  []ParamDesc
}

// ParamDesc skill 参数描述。
type ParamDesc struct {
	Name     string
	Type     string
	Required bool
	Enum     []string
}

// AgentDesc agent 配置描述。
type AgentDesc struct {
	Name  string
	Model string
}
