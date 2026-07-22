package orchestrator

import (
	"context"
	"math/rand"
	"time"

	"git.qingteng.cn/ms/ainspection/internal/mcts"
)

// RolloutSimulator 控制 MCTS Rollout 过程。
//
// Simulation 阶段从当前节点出发，执行多步 Rollout（动作链推演），
// 到达终止条件后返回累积奖励值，替代单次评分。
type RolloutSimulator struct {
	locateActions []LocateRolloutAction // locate 阶段动作池
	fixActions    []FixRolloutAction    // fix 阶段动作池
	rng           *rand.Rand
	executor      *RolloutExecutor // 可选，真实动作执行器
}

// NewRolloutSimulator 创建 Rollout 模拟器。
func NewRolloutSimulator() *RolloutSimulator {
	return &RolloutSimulator{
		locateActions: DefaultLocateActions(),
		fixActions:    DefaultFixActions(),
		rng:           rand.New(rand.NewSource(time.Now().UnixNano())),
	}
}

// WithExecutor 注入真实动作执行器。
// 当 executor 不为 nil 且动作类型已启用时，真实执行替代随机模拟。
// 真实执行失败时自动降级到随机模拟值。
func (s *RolloutSimulator) WithExecutor(exec *RolloutExecutor) *RolloutSimulator {
	s.executor = exec
	return s
}

// RolloutConfig 单次 Rollout 的配置。
type RolloutConfig struct {
	MaxDepth  int     // 最大 Rollout 深度（步数）
	BaseScore float64 // 基础分（单次评分结果）
	Stage     string  // "locate" 或 "fix"
}

// DefaultRolloutConfig 返回默认 Rollout 配置。
func DefaultRolloutConfig(stage string) RolloutConfig {
	if stage == "fix" {
		return RolloutConfig{MaxDepth: 3, BaseScore: 0.5, Stage: "fix"}
	}
	return RolloutConfig{MaxDepth: 4, BaseScore: 0.5, Stage: "locate"}
}

// RolloutResult Rollout 执行结果。
type RolloutResult struct {
	FinalScore   float64 // 最终奖励值 (0-1)
	StepsTaken   int     // 执行的步数
	TerminatedAt string  // 终止原因
}

// INSTRUMENT: rollout-simulate-locate — locate 阶段 Rollout 仿真，动作链推演
// LAYER: L2
// STATUS: implemented
// SimulateLocate 从 locate 节点出发执行 Rollout。
//
// 选择 1-3 个未执行动作，快速推演至终止条件，返回 Rollout 奖励。
// 若 executor 已注入且动作类型已启用，执行真实动作；失败时自动降级到模拟值。
func (s *RolloutSimulator) SimulateLocate(ctx context.Context, baseScore float64, evidenceCount int, sourceCtx *mcts.SourceContext) RolloutResult {
	cfg := DefaultRolloutConfig("locate")
	cfg.BaseScore = baseScore

	// 随机选择动作（1-3 个）
	available := make([]LocateRolloutAction, len(s.locateActions))
	copy(available, s.locateActions)
	s.shuffleLocate(available)

	n := 1 + s.rng.Intn(3) // 1-3 个动作
	if n > len(available) {
		n = len(available)
	}
	selected := available[:n]

	// 执行动作链
	currentScore := baseScore
	stepsTaken := 0

	for _, action := range selected {
		// 检查终止条件
		if s.shouldTerminateLocate(currentScore, evidenceCount, stepsTaken) {
			return RolloutResult{
				FinalScore:   currentScore,
				StepsTaken:   stepsTaken,
				TerminatedAt: "terminal_condition",
			}
		}

		// 计算动作影响：优先真实执行，失败回退到模拟
		impact := s.evalLocateWithExecutor(ctx, action, stepsTaken+1, sourceCtx)
		currentScore = clampScore(currentScore + impact)

		stepsTaken++
		if stepsTaken >= cfg.MaxDepth {
			break
		}
	}

	return RolloutResult{
		FinalScore:   currentScore,
		StepsTaken:   stepsTaken,
		TerminatedAt: "max_depth",
	}
}

// evalLocateWithExecutor 评估 locate 动作影响：真实执行或随机模拟。
func (s *RolloutSimulator) evalLocateWithExecutor(ctx context.Context, action LocateRolloutAction, depth int, sourceCtx *mcts.SourceContext) float64 {
	// 尝试真实执行
	if s.executor != nil && s.executor.IsLocateEnabled(action.Type) {
		impact, _, err := s.executor.ExecuteLocateAction(ctx, action, sourceCtx, "")
		if err == nil {
			return impact
		}
		// 真实执行失败 → 降级到随机模拟
	}
	return s.evaluateLocateAction(action, depth)
}

// INSTRUMENT: rollout-simulate-fix — fix 阶段 Rollout 仿真，修复动作链推演
// LAYER: L2
// STATUS: implemented
// SimulateFix 从 fix 节点出发执行 Rollout。
//
// 若 executor 已注入且动作类型已启用，执行真实动作；失败时自动降级到模拟值。
func (s *RolloutSimulator) SimulateFix(ctx context.Context, baseScore float64, diffLength int, sourceCtx *mcts.SourceContext, testCtx *mcts.TestContext) RolloutResult {
	cfg := DefaultRolloutConfig("fix")
	cfg.BaseScore = baseScore

	available := make([]FixRolloutAction, len(s.fixActions))
	copy(available, s.fixActions)
	s.shuffleFix(available)

	n := 1 + s.rng.Intn(2) // 1-2 个动作
	if n > len(available) {
		n = len(available)
	}
	selected := available[:n]

	currentScore := baseScore
	stepsTaken := 0

	for _, action := range selected {
		if s.shouldTerminateFix(currentScore, diffLength, stepsTaken) {
			return RolloutResult{
				FinalScore:   currentScore,
				StepsTaken:   stepsTaken,
				TerminatedAt: "terminal_condition",
			}
		}

		impact := s.evalFixWithExecutor(ctx, action, stepsTaken+1, sourceCtx, testCtx)
		currentScore = clampScore(currentScore + impact)

		stepsTaken++
		if stepsTaken >= cfg.MaxDepth {
			break
		}
	}

	return RolloutResult{
		FinalScore:   currentScore,
		StepsTaken:   stepsTaken,
		TerminatedAt: "max_depth",
	}
}

// evalFixWithExecutor 评估 fix 动作影响：真实执行或随机模拟。
func (s *RolloutSimulator) evalFixWithExecutor(ctx context.Context, action FixRolloutAction, depth int, sourceCtx *mcts.SourceContext, testCtx *mcts.TestContext) float64 {
	if s.executor != nil && s.executor.IsFixEnabled(action.Type) {
		impact, _, err := s.executor.ExecuteFixAction(ctx, action, sourceCtx, testCtx, "")
		if err == nil {
			return impact
		}
	}
	return s.evaluateFixAction(action, depth)
}

// shouldTerminateLocate 检查 locate Rollout 终止条件。
func (s *RolloutSimulator) shouldTerminateLocate(score float64, evidenceCount, depth int) bool {
	// 假设已确认：分数 ≥ 0.9
	if score >= 0.9 && evidenceCount >= 1 {
		return true
	}
	// 假设已排除：分数 < 0.1
	if score < 0.1 {
		return true
	}
	return false
}

// shouldTerminateFix 检查 fix Rollout 终止条件。
func (s *RolloutSimulator) shouldTerminateFix(score float64, diffLength, depth int) bool {
	// 方案已优秀：分数 ≥ 0.95
	if score >= 0.95 {
		return true
	}
	// 方案不可行：分数 < 0.1
	if score < 0.1 {
		return true
	}
	return false
}

// evaluateLocateAction 评估单个 locate 动作对分数的影响。
//
// 不同动作类型有不同的影响权重和方向：
//   - StaticAnalysis: 正面影响（+0.05 ~ +0.15）
//   - DynamicProbe: 正面影响，找到证据时加分
//   - HypothesisPropose: 可能正面或负面
//   - InfoAggregate: 小幅正面
//   - UserQuery: 中性，微小正面（获得确认）
func (s *RolloutSimulator) evaluateLocateAction(action LocateRolloutAction, depth int) float64 {
	base := 0.05  // 基础影响
	variance := 0.05 // 随机方差

	switch action.Type {
	case LocateStaticAnalysis:
		base = 0.08
	case LocateDynamicProbe:
		base = 0.10
	case LocateHypothesisPropose:
		base = 0.03
		// 可能产生负面效果（假设不成立）
		if s.rng.Float64() < 0.3 {
			base = -0.05
		}
	case LocateInfoAggregate:
		base = 0.06
	case LocateUserQuery:
		base = 0.02
	}

	noise := (s.rng.Float64() - 0.5) * variance
	return base + noise
}

// evaluateFixAction 评估单个 fix 动作对分数的影响。
func (s *RolloutSimulator) evaluateFixAction(action FixRolloutAction, depth int) float64 {
	base := 0.05
	variance := 0.04

	switch action.Type {
	case FixTemplateInstantiate:
		base = 0.10
	case FixMutation:
		base = 0.05
		if s.rng.Float64() < 0.4 {
			base = -0.03 // 变异可能引入问题
		}
	case FixTestRun:
		base = 0.12 // 测试通过是强正面信号
	case FixStaticCheck:
		base = 0.08
	case FixPatchMinify:
		base = 0.06
	case FixUserConfirm:
		base = 0.02
	}

	noise := (s.rng.Float64() - 0.5) * variance
	return base + noise
}

// shuffleLocate 随机打乱动作列表。
func (s *RolloutSimulator) shuffleLocate(actions []LocateRolloutAction) {
	s.rng.Shuffle(len(actions), func(i, j int) {
		actions[i], actions[j] = actions[j], actions[i]
	})
}

// shuffleFix 随机打乱动作列表。
func (s *RolloutSimulator) shuffleFix(actions []FixRolloutAction) {
	s.rng.Shuffle(len(actions), func(i, j int) {
		actions[i], actions[j] = actions[j], actions[i]
	})
}
