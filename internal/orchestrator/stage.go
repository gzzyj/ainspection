package orchestrator

import (
	"context"
	"fmt"

	"git.qingteng.cn/ms/ainspection/internal/tree"
)

// defaultStageGates 返回所有阶段门控定义的默认实现。
// 每个门控在进入下一阶段前检查前置条件。
func defaultStageGates() []StageGate {
	return []StageGate{
		{
			From: StageGet, To: StageLocate,
			Check: func(ctx context.Context, t *tree.Task) GatingDecision {
				if t.Status != tree.StatusScopeDefined {
					return GatingDecision{
						Allowed: false,
						Reason:  "get 阶段未完成，缺少 scope_defined 状态",
						Blocked: false,
					}
				}
				return GatingDecision{Allowed: true}
			},
		},
		{
			From: StageLocate, To: StageReview1,
			Check: func(ctx context.Context, t *tree.Task) GatingDecision {
				if t.Status != tree.StatusExpectationLocked {
					return GatingDecision{
						Allowed: false,
						Reason:  "expectation_locked 未设置，等待用户确认定位结果",
						Blocked: false,
					}
				}
				return GatingDecision{Allowed: true}
			},
		},
		{
			From: StageReview1, To: StagePlan,
			Check: func(ctx context.Context, t *tree.Task) GatingDecision {
				// review#1 通过后状态保持 expectation_locked，进入 Planner
				if t.Status == tree.StatusBlocked {
					return GatingDecision{
						Allowed: false,
						Reason:  "review #1 未通过，需补充证据",
						Blocked: false,
					}
				}
				return GatingDecision{Allowed: true}
			},
		},
		{
			From: StagePlan, To: StageFix,
			Check: func(ctx context.Context, t *tree.Task) GatingDecision {
				if t.Status != tree.StatusFixing {
					return GatingDecision{
						Allowed: false,
						Reason:  "plan 阶段未完成，缺少 plan.json",
						Blocked: false,
					}
				}
				return GatingDecision{Allowed: true}
			},
		},
		{
			From: StageFix, To: StageReview2,
			Check: func(ctx context.Context, t *tree.Task) GatingDecision {
				// fix 需完成 go build + diff-validate
				if t.Status == tree.StatusBlocked {
					return GatingDecision{
						Allowed: false,
						Reason:  "fix 阶段构建或 diff 验证失败",
						Blocked: false,
					}
				}
				return GatingDecision{Allowed: true}
			},
		},
		{
			From: StageReview2, To: StageVerify,
			Check: func(ctx context.Context, t *tree.Task) GatingDecision {
				if t.Status == tree.StatusBlocked {
					return GatingDecision{
						Allowed: false,
						Reason:  "review #2 不通过，需修正 fix",
						Blocked: false,
					}
				}
				if t.Status != tree.StatusVerifying {
					return GatingDecision{
						Allowed: false,
						Reason:  "review #2 未完成",
						Blocked: false,
					}
				}
				return GatingDecision{Allowed: true}
			},
		},
		{
			From: StageVerify, To: StageReview3,
			Check: func(ctx context.Context, t *tree.Task) GatingDecision {
				if t.Status == tree.StatusBlocked {
					return GatingDecision{
						Allowed: false,
						Reason:  "verify 阶段部署或接口验证失败",
						Blocked: false,
					}
				}
				return GatingDecision{Allowed: true}
			},
		},
		{
			From: StageReview3, To: StageCommit,
			Check: func(ctx context.Context, t *tree.Task) GatingDecision {
				if t.Status == tree.StatusBlocked {
					return GatingDecision{
						Allowed: false,
						Reason:  "review #3 不通过，需补充验证",
						Blocked: false,
					}
				}
				if t.Status != tree.StatusCommitting {
					return GatingDecision{
						Allowed: false,
						Reason:  "review #3 未完成",
						Blocked: false,
					}
				}
				return GatingDecision{Allowed: true}
			},
		},
	}
}

// checkGating 检查从 fromStage 到 toStage 的转换是否被允许。
func checkGating(gates []StageGate, from, to Stage, t *tree.Task) GatingDecision {
	for _, g := range gates {
		if g.From == from && g.To == to {
			return g.Check(context.Background(), t)
		}
	}
	return GatingDecision{
		Allowed: false,
		Reason:  fmt.Sprintf("未定义的门控转换: %s → %s", from, to),
		Blocked: true,
	}
}

// stageIndex 返回 stage 在 pipelineOrder 中的位置，-1 表示未找到。
func stageIndex(s Stage) int {
	for i, ps := range pipelineOrder {
		if ps == s {
			return i
		}
	}
	return -1
}

// nextStage 返回流水线中的下一个阶段，空字符串表示已是最后一个。
func nextStage(s Stage) Stage {
	idx := stageIndex(s)
	if idx < 0 || idx >= len(pipelineOrder)-1 {
		return ""
	}
	return pipelineOrder[idx+1]
}

// retryExceeded 检查指定阶段的重试次数是否超限。
func retryExceeded(t *tree.Task, stage Stage, maxRetry int) bool {
	count := t.RetryCount[string(stage)]
	return count >= maxRetry
}
