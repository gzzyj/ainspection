package eval

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"

	"git.qingteng.cn/ms/ainspection/internal/prompt"
)

// CasesDir 评测 case 存放目录。
func CasesDir() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return filepath.Join("/tmp", ".ainspection", "evals", "cases")
	}
	return filepath.Join(home, ".ainspection", "evals", "cases")
}

// CaseRunner case 回归评测执行器。
type CaseRunner struct {
	renderer prompt.Renderer
}

// NewCaseRunner 创建 case runner。
func NewCaseRunner(r prompt.Renderer) *CaseRunner {
	return &CaseRunner{renderer: r}
}

// Run 加载指定 case 并运行 prompt 链渲染 + schema 对比。
func (cr *CaseRunner) Run(caseID string) (*CaseResult, error) {
	caseDir := filepath.Join(CasesDir(), caseID)

	ec, err := loadCase(caseDir)
	if err != nil {
		return nil, fmt.Errorf("load case %s: %w", caseID, err)
	}

	result := &CaseResult{
		CaseID:       caseID,
		StageResults: make(map[string]StageCheck),
	}

	// 定义 stage → template → input data 的映射
	stages := []struct {
		name     string
		template string
		data     any
	}{
		{"get", "get-system", buildGetData(ec.Input.Get)},
		{"locate", "locate-system", buildLocateData(ec.Input.Locate)},
		{"plan", "plan-system", buildPlanData(ec.Input.Plan)},
		{"fix", "fix-system", buildFixData(ec.Input.Fix)},
	}

	allPassed := true
	for _, s := range stages {
		output, err := cr.renderer.Render(s.template, s.data)
		if err != nil {
			result.StageResults[s.name] = StageCheck{
				Passed:  false,
				Missing: []string{fmt.Sprintf("render error: %v", err)},
			}
			allPassed = false
			continue
		}

		if len(strings.TrimSpace(output)) == 0 {
			result.StageResults[s.name] = StageCheck{
				Passed:  false,
				Missing: []string{"rendered output is empty"},
			}
			allPassed = false
			continue
		}

		// 结构校验: 对比期望 schema 中的字段是否在输出中出现
		check := checkSchema(output, ec.ExpectedSchema, s.name)
		result.StageResults[s.name] = check
		if !check.Passed {
			allPassed = false
		}
	}

	result.Passed = allPassed
	return result, nil
}

// ListCases 列出所有可用的 case ID。
func ListCases() ([]string, error) {
	dir := CasesDir()
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}

	var ids []string
	for _, e := range entries {
		if e.IsDir() {
			ids = append(ids, e.Name())
		}
	}
	return ids, nil
}

// loadCase 从 case 目录加载 input.yaml 和 expected_schema.yaml。
func loadCase(dir string) (*EvalCase, error) {
	ec := &EvalCase{
		ID: filepath.Base(dir),
	}

	// 加载 input.yaml
	inputPath := filepath.Join(dir, "input.yaml")
	inputRaw, err := os.ReadFile(inputPath)
	if err != nil {
		return nil, fmt.Errorf("read input.yaml: %w", err)
	}
	if err := yaml.Unmarshal(inputRaw, &ec.Input); err != nil {
		return nil, fmt.Errorf("parse input.yaml: %w", err)
	}

	// 加载 expected_schema.yaml (可选)
	schemaPath := filepath.Join(dir, "expected_schema.yaml")
	schemaRaw, err := os.ReadFile(schemaPath)
	if err != nil {
		if !os.IsNotExist(err) {
			return nil, fmt.Errorf("read expected_schema.yaml: %w", err)
		}
		// 无 schema 文件时, 仅校验渲染不崩溃
		return ec, nil
	}

	// 解析为 map，提取 schema
	var raw map[string]any
	if err := yaml.Unmarshal(schemaRaw, &raw); err != nil {
		return nil, fmt.Errorf("parse expected_schema.yaml: %w", err)
	}

	ec.ExpectedSchema = CaseSchema{Stages: make(map[string]SchemaField)}
	for k, v := range raw {
		sf := parseSchemaField(v)
		ec.ExpectedSchema.Stages[k] = sf
	}

	return ec, nil
}

// parseSchemaField 递归解析 schema 值。
func parseSchemaField(v any) SchemaField {
	switch val := v.(type) {
	case string:
		return SchemaField{Type: val}
	case map[string]any:
		sf := SchemaField{Children: make(map[string]SchemaField)}
		for ck, cv := range val {
			sf.Children[ck] = parseSchemaField(cv)
		}
		return sf
	default:
		return SchemaField{Type: fmt.Sprintf("%T", v)}
	}
}

// checkSchema 检查渲染输出中是否包含 schema 定义的字段名。
func checkSchema(output string, schema CaseSchema, stageName string) StageCheck {
	check := StageCheck{Passed: true}

	for schemaKey, sf := range schema.Stages {
		// schema key 格式: "plan_output" → 对应 stage "plan"
		expectedStage := strings.TrimSuffix(schemaKey, "_output")
		if expectedStage != stageName {
			continue
		}

		// 递归检查字段是否在输出中出现
		checkFieldPresence(output, sf, "", &check)
	}

	return check
}

// checkFieldPresence 递归检查字段名是否在文本中出现。
func checkFieldPresence(text string, sf SchemaField, prefix string, check *StageCheck) {
	for name, child := range sf.Children {
		fullName := name
		if prefix != "" {
			fullName = prefix + "." + name
		}

		if !strings.Contains(text, name) {
			check.Passed = false
			check.Missing = append(check.Missing, fullName)
		}

		if len(child.Children) > 0 {
			checkFieldPresence(text, child, fullName, check)
		}
	}
}

// --- 从 CaseInput 构建各 stage 的 Input 数据 ---

func buildGetData(raw map[string]any) prompt.GetInput {
	return prompt.GetInput{
		IssueURL:    getString(raw, "issue_url"),
		Desc:        getString(raw, "desc"),
		Service:     getString(raw, "service"),
		JiraContent: getString(raw, "jira_content"),
	}
}

func buildLocateData(raw map[string]any) prompt.LocateInput {
	return prompt.LocateInput{
		InputYAML:     encodeYAML(raw),
		ParentSummary: "get 阶段结论",
		Service:       getString(raw, "service"),
		Skills:        buildSkillDescs(raw["skills"]),
		AgentConfig:   buildAgentDesc(raw["agent_config"]),
	}
}

func buildPlanData(raw map[string]any) prompt.PlanInput {
	return prompt.PlanInput{
		Findings:        buildFindings(raw["findings"]),
		ParentSummary:   "locate 阶段结论",
		AvailableSkills: buildSkillDescs(raw["skills"]),
	}
}

func buildFixData(raw map[string]any) prompt.FixInput {
	return prompt.FixInput{
		PlanJSON:      getString(raw, "plan_json"),
		Service:       getString(raw, "service"),
		RepoPath:      getString(raw, "repo_path"),
		Skills:        buildSkillDescs(raw["skills"]),
		AgentConfig:   AgentDescDefault(),
		ParentSummary: "plan 阶段输出",
	}
}

func getString(m map[string]any, key string) string {
	if v, ok := m[key]; ok {
		if s, ok := v.(string); ok {
			return s
		}
	}
	return ""
}

func encodeYAML(data any) string {
	if data == nil {
		return ""
	}
	b, err := yaml.Marshal(data)
	if err != nil {
		return fmt.Sprintf("%v", data)
	}
	return string(b)
}

func buildSkillDescs(v any) []prompt.SkillDesc {
	if v == nil {
		return nil
	}
	list, ok := v.([]any)
	if !ok {
		return nil
	}
	var out []prompt.SkillDesc
	for _, item := range list {
		m, ok := item.(map[string]any)
		if !ok {
			continue
		}
		out = append(out, prompt.SkillDesc{
			Name:        getString(m, "name"),
			Description: getString(m, "description"),
		})
	}
	return out
}

func buildAgentDesc(v any) prompt.AgentDesc {
	if v == nil {
		return AgentDescDefault()
	}
	m, ok := v.(map[string]any)
	if !ok {
		return AgentDescDefault()
	}
	return prompt.AgentDesc{
		Name:  getString(m, "name"),
		Model: getString(m, "model"),
	}
}

// AgentDescDefault 返回默认的 agent 配置。
func AgentDescDefault() prompt.AgentDesc {
	return prompt.AgentDesc{Name: "claude", Model: "opus"}
}

func buildFindings(v any) []prompt.FindingData {
	if v == nil {
		return nil
	}
	list, ok := v.([]any)
	if !ok {
		return nil
	}
	var out []prompt.FindingData
	for _, item := range list {
		m, ok := item.(map[string]any)
		if !ok {
			continue
		}
		fd := prompt.FindingData{
			Hypothesis: getString(m, "hypothesis"),
			Confidence: getFloat(m, "confidence"),
		}
		if evidence, ok := m["evidence"].([]any); ok {
			for _, e := range evidence {
				if s, ok := e.(string); ok {
					fd.Evidence = append(fd.Evidence, s)
				}
			}
		}
		out = append(out, fd)
	}
	return out
}

func getFloat(m map[string]any, key string) float64 {
	if v, ok := m[key]; ok {
		switch n := v.(type) {
		case float64:
			return n
		case int:
			return float64(n)
		}
	}
	return 0
}
