package eval

import (
	"strings"

	"git.qingteng.cn/ms/ainspection/internal/prompt"
)

// smokeTemplate 定义单个模板的 smoke test 用例。
type smokeTemplate struct {
	Name       string
	InputData  any
	InputNames []string // Go struct 字段名列表
}

// allSmokeTemplates 返回全部 16 个模板的 smoke test 用例定义。
func allSmokeTemplates() []smokeTemplate {
	return []smokeTemplate{
		{
			Name: "get-system",
			InputData: prompt.GetInput{
				IssueURL:    "https://jira.example.com/PROJ-123",
				Desc:        "payment-svc 偶发 P99 飙升到 500ms",
				Service:     "payment-svc",
				JiraContent: "Issue body: performance degradation observed...",
			},
			InputNames: []string{"IssueURL", "Desc", "Service", "JiraContent"},
		},
		{
			Name: "locate-system",
			InputData: prompt.LocateInput{
				InputYAML:     "service: payment-svc\nissue: P99 latency spike",
				ParentSummary: "get 阶段结论: payment-svc 存在性能问题",
				Service:       "payment-svc",
				Skills: []prompt.SkillDesc{
					{Name: "grep-source", Description: "搜索源码"},
					{Name: "loki-query", Description: "查询日志"},
				},
				AgentConfig: prompt.AgentDesc{Name: "claude", Model: "opus"},
			},
			InputNames: []string{"InputYAML", "ParentSummary", "Service", "Skills", "AgentConfig"},
		},
		{
			Name: "locate-disclose",
			InputData: prompt.LocateDiscloseInput{
				Findings: []prompt.FindingData{
					{
						Hypothesis:     "JSON 序列化无 sync.Pool 导致频繁分配",
						Confidence:     0.85,
						Evidence:       []string{"runtime.mallocgc flat 18%", "json.Marshal alloc 12%"},
						ConfidenceSelf: 0.9,
					},
				},
				DiscardedHypotheses: []prompt.DiscardedHypothesisData{
					{Hypothesis: "网络延迟", EvidenceAgainst: "tcpdump 未发现异常"},
				},
				SignalsSummary: "CPU profile: mallocgc 18%",
				CurrentRound:   1,
				MaxRounds:      3,
			},
			InputNames: []string{"Findings", "DiscardedHypotheses", "SignalsSummary", "CurrentRound", "MaxRounds"},
		},
		{
			Name: "mcts-expand",
			InputData: prompt.MCTSExpandInput{
				Stage:           "fix",
				CurrentNode:     "sync.Pool 优化 JSON 编解码",
				Depth:           1,
				MaxDepth:        3,
				BranchingFactor: 3,
				ParentSummary:   "根因: JSON 序列化内存分配过多",
			},
			InputNames: []string{"Stage", "CurrentNode", "Depth", "MaxDepth", "BranchingFactor", "ParentSummary"},
		},
		{
			Name: "plan-system",
			InputData: prompt.PlanInput{
				Findings: []prompt.FindingData{
					{
						Hypothesis:     "JSON 序列化无 sync.Pool 导致频繁分配",
						Confidence:     0.85,
						Evidence:       []string{"runtime.mallocgc flat 18%"},
						ConfidenceSelf: 0.9,
					},
				},
				DiscardedHypotheses: []prompt.DiscardedHypothesisData{
					{Hypothesis: "网络延迟", EvidenceAgainst: "tcpdump 未发现异常"},
				},
				ParentSummary:   "locate 阶段结论: JSON 序列化内存问题",
				UserDirectives:  []string{"优先使用 sync.Pool 优化"},
				AvailableSkills: []prompt.SkillDesc{{Name: "diff-validate", Description: "校验 diff"}},
			},
			InputNames: []string{"Findings", "DiscardedHypotheses", "ParentSummary", "UserDirectives", "AvailableSkills"},
		},
		{
			Name: "fix-system",
			InputData: prompt.FixInput{
				PlanJSON:      `{"version":"1.0","goal":"优化 JSON 序列化内存分配","steps":[{"id":"step1","action":"add sync.Pool","target":"serializer.go","approach":"引入 sync.Pool 复用 bytes.Buffer","estimated_impact":"减少 50% alloc","risk":"并发安全","confidence_self":0.85}]}`,
				Service:       "payment-svc",
				RepoPath:      "/tmp/mock/payment-svc",
				Skills:        []prompt.SkillDesc{{Name: "diff-validate", Description: "校验 diff"}},
				AgentConfig:   prompt.AgentDesc{Name: "claude", Model: "opus"},
				ParentSummary: "plan 阶段输出: sync.Pool 优化方案",
			},
			InputNames: []string{"PlanJSON", "Service", "RepoPath", "Skills", "AgentConfig", "ParentSummary"},
		},
		{
			Name: "verify-system",
			InputData: prompt.VerifyInput{
				Service:       "payment-svc",
				RepoPath:      "/tmp/mock/payment-svc",
				Patches:       []string{"patches/0001-add-sync-pool.diff"},
				Skills:        []prompt.SkillDesc{{Name: "go-build", Description: "编译检查"}, {Name: "go-test", Description: "运行测试"}},
				ParentSummary: "fix 阶段: sync.Pool 优化已完成",
			},
			InputNames: []string{"Service", "RepoPath", "Patches", "Skills", "ParentSummary"},
		},
		{
			Name: "commit-system",
			InputData: prompt.CommitInput{
				Service:       "payment-svc",
				RepoPath:      "/tmp/mock/payment-svc",
				Patches:       []string{"patches/0001-add-sync-pool.diff"},
				JiraID:        "PROJ-123",
				ParentSummary: "verify 阶段: 部署验证通过",
			},
			InputNames: []string{"Service", "RepoPath", "Patches", "JiraID", "ParentSummary"},
		},
		{
			Name: "review-locate",
			InputData: prompt.ReviewLocateInput{
				InputYAML: "service: payment-svc\nissue: P99 latency",
				Findings: []prompt.FindingData{
					{Hypothesis: "JSON 序列化内存分配", Confidence: 0.85, Evidence: []string{"mallocgc 18%"}, ConfidenceSelf: 0.9},
				},
				Signals: "CPU profile: mallocgc 18%, json.Marshal 12%",
			},
			InputNames: []string{"InputYAML", "Findings", "Signals"},
		},
		{
			Name: "review-fix",
			InputData: prompt.ReviewFixInput{
				InputYAML: "service: payment-svc",
				PlanJSON:  `{"goal":"优化 JSON 序列化"}`,
				Diffs:     []string{"diff --git a/serializer.go b/serializer.go\n+ pool := sync.Pool{...}"},
			},
			InputNames: []string{"InputYAML", "PlanJSON", "Diffs"},
		},
		{
			Name: "review-verify",
			InputData: prompt.ReviewVerifyInput{
				InputYAML:     "service: payment-svc",
				PlanJSON:      `{"goal":"优化 JSON 序列化"}`,
				DeploySuccess: true,
				HTTPStatus:    200,
				TestPassed:    true,
				NewErrors:     []string{},
			},
			InputNames: []string{"InputYAML", "PlanJSON", "DeploySuccess", "HTTPStatus", "TestPassed", "NewErrors"},
		},
		{
			Name: "review1-system",
			InputData: prompt.Review1Input{
				Findings: []prompt.FindingData{
					{Hypothesis: "JSON 序列化内存分配", Confidence: 0.85, Evidence: []string{"mallocgc 18%"}, ConfidenceSelf: 0.9},
				},
				Stage:  "locate",
				NodeID: "node-locate-001",
			},
			InputNames: []string{"Findings", "Stage", "NodeID"},
		},
		{
			Name: "review2-system",
			InputData: prompt.Review2Input{
				Diff:     "diff --git a/serializer.go b/serializer.go\n+ pool := sync.Pool{...}",
				PlanJSON: `{"goal":"优化 JSON 序列化","steps":[{"id":"step1","action":"add sync.Pool"}]}`,
				Stage:    "fix",
				NodeID:   "node-fix-001",
			},
			InputNames: []string{"Diff", "PlanJSON", "Stage", "NodeID"},
		},
		{
			Name: "review3-system",
			InputData: prompt.Review3Input{
				VerifyPassed: true,
				Stage:        "verify",
				NodeID:       "node-verify-001",
			},
			InputNames: []string{"VerifyPassed", "Stage", "NodeID"},
		},
		{
			Name: "profile-analyze",
			InputData: prompt.ProfileAnalyzeInput{
				Service:     "payment-svc",
				ProfileType: "cpu",
				Top:         "flat  flat%   sum%   cum   cum%\n18s  18.00% 18.00% 20s  20.00%  runtime.mallocgc",
				Tree:        "runtime.mallocgc: 18% flat\n  called by: json.Marshal (12%), fmt.Sprintf (6%)",
				Traces:      "samples/total=%d\ntrace 1: runtime.mallocgc -> json.Marshal -> handler.ServeHTTP",
			},
			InputNames: []string{"Service", "ProfileType", "Top", "Tree", "Traces"},
		},
		{
			Name: "profile-fix",
			InputData: prompt.ProfileFixInput{
				PlanJSON: `{"version":"1.0","goal":"减少内存分配","steps":[{"id":"step1","action":"sync.Pool","target":"serializer.go"}]}`,
				Analysis: "根因分析: JSON 序列化中频繁分配 bytes.Buffer 导致 GC 压力",
				Service:  "payment-svc",
				RepoPath: "/tmp/mock/payment-svc",
				Skills:   []prompt.SkillDesc{{Name: "diff-validate", Description: "校验 diff"}},
			},
			InputNames: []string{"PlanJSON", "Analysis", "Service", "RepoPath", "Skills"},
		},
	}
}

// SmokeRunner 16 模板 smoke test 执行器。
type SmokeRunner struct {
	renderer prompt.Renderer
}

// NewSmokeRunner 创建 smoke test runner。
func NewSmokeRunner(r prompt.Renderer) *SmokeRunner {
	return &SmokeRunner{renderer: r}
}

// Run 执行全部 16 个模板的 smoke test。
func (sr *SmokeRunner) Run() []SmokeResult {
	templates := allSmokeTemplates()
	results := make([]SmokeResult, 0, len(templates))

	for _, st := range templates {
		result := SmokeResult{
			Template:    st.Name,
			InputFields: st.InputNames,
		}

		output, err := sr.renderer.Render(st.Name, st.InputData)
		if err != nil {
			result.Passed = false
			result.Error = err.Error()
			results = append(results, result)
			continue
		}

		result.CharCount = len(output)

		if len(strings.TrimSpace(output)) == 0 {
			result.Passed = false
			result.Error = "rendered output is empty"
			results = append(results, result)
			continue
		}

		result.Passed = true
		results = append(results, result)
	}

	return results
}
