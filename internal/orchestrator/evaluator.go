package orchestrator

import (
	"context"

	"git.qingteng.cn/ms/ainspection/internal/tree"
)

// Evaluator 独立审查 Agent 接口。
// Evaluator 在独立 session 中运行，使用不同模型（默认 Sonnet），
// 避免 Generator 自评偏差。
//
// 实现位置：internal/orchestrator/evaluator_llm.go
// P1 完整实现：evaluatorLLMImpl 提供 LLM 双路径（native + CLI）审查。
type Evaluator interface {
	// ReviewFinding 审查 locate 阶段的根因定位（Review #1）。
	ReviewFinding(ctx context.Context, taskID string, node *tree.Node) (*ReviewReport, error)

	// ReviewFix 审查 fix 阶段的 diff（Review #2，主 review）。
	ReviewFix(ctx context.Context, taskID string, node *tree.Node, patches []Patch) (*ReviewReport, error)

	// ReviewVerification 审查 verify 阶段的验证结果（Review #3）。
	ReviewVerification(ctx context.Context, taskID string, node *tree.Node, report *VerifyReport) (*ReviewReport, error)
}

// ReviewReport 审查报告。
type ReviewReport struct {
	Passed     bool              `yaml:"passed"`     // 是否通过审查
	Score      int               `yaml:"score"`      // 1-10 综合评分
	Dimensions []ReviewDimension `yaml:"dimensions"` // 分维度评价
	Blockers   []string          `yaml:"blockers"`   // 阻断项（必须修复）
	Warnings   []string          `yaml:"warnings"`   // 建议项（可选修复）
	Confidence float64           `yaml:"confidence"` // Evaluator 自身置信度
}

// ReviewDimension 单个审查维度的评分。
type ReviewDimension struct {
	Name    string `yaml:"name"`    // 维度名称
	Score   int    `yaml:"score"`   // 1-10
	Comment string `yaml:"comment"` // 评语
}

// Patch 表示一个 diff patch。
type Patch struct {
	FilePath string // 相对于 repo_path 的文件路径
	Content  []byte // unified diff 内容
}

// VerifyReport verify 阶段验证结果。
type VerifyReport struct {
	DeploySuccess   bool     `yaml:"deploy_success"`
	HTTPProbePassed bool     `yaml:"http_probe_passed"`
	LintPassed      bool     `yaml:"lint_passed"`
	TestPassed      bool     `yaml:"test_passed"`
	NewErrors       []string `yaml:"new_errors"`
	SignalSummary   string   `yaml:"signal_summary"` // 信号恢复摘要
}

// MCTSScorer MCTS Simulation 评分接口。
// MCTS 引擎的 Simulation 步骤通过此接口获取独立 Evaluator 评分，
// 每次调用启动一个短生命周期 Evaluator 子 session。
//
// 完整实现属于 P1（EV-6）。
type MCTSScorer interface {
	// ScoreLocate 给 HypothesisNode 打分（locate 4 维加权）。
	ScoreLocate(ctx context.Context, h HypothesisForScore) (float64, error)

	// ScoreFix 给 PlanStepNode 打分（fix 4 维加权）。
	ScoreFix(ctx context.Context, step PlanStepForScore, candidate DiffForScore) (float64, error)
}

// HypothesisForScore MCTS locate 阶段 Simulation 评分输入。
type HypothesisForScore struct {
	Hypothesis string   `json:"hypothesis"`
	Evidence   []string `json:"evidence"`
	Depth      int      `json:"depth"`
}

// PlanStepForScore MCTS fix 阶段 Simulation 评分输入。
type PlanStepForScore struct {
	StepID   string `json:"step_id"`
	Action   string `json:"action"`
	Target   string `json:"target"`
	Approach string `json:"approach"`
}

// DiffForScore 候选 diff 的评分输入。
type DiffForScore struct {
	Content  []byte `json:"content"`
	FilePath string `json:"file_path"`
}

// GatingFromReport 从 ReviewReport 生成门控决策。
func GatingFromReport(r *ReviewReport, retryCount, maxRetry int) GatingDecision {
	if r.Passed && r.Score >= 7 {
		return GatingDecision{Allowed: true}
	}
	if !r.Passed {
		if retryCount >= maxRetry {
			return GatingDecision{
				Allowed: false,
				Reason:  "同一阶段 review " + itoa(retryCount) + " 次不通过，转人工",
				Blocked: true,
			}
		}
		return GatingDecision{
			Allowed: false,
			Reason:  "review 不通过，有 " + itoa(len(r.Blockers)) + " 个阻断项",
			Blocked: false,
		}
	}
	// passed=true, score < 7: 允许通过但记录 warnings
	return GatingDecision{Allowed: true}
}

// itoa 简单的 int → string 转换，避免导入 strconv。
func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	neg := false
	if n < 0 {
		neg = true
		n = -n
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}
