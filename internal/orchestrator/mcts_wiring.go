package orchestrator

import (
	"git.qingteng.cn/ms/ainspection/internal/config"
	"git.qingteng.cn/ms/ainspection/internal/llm"
	"git.qingteng.cn/ms/ainspection/internal/mcts"
	"git.qingteng.cn/ms/ainspection/internal/prompt"
)

// INSTRUMENT: mcts-wiring-mixed — Phase 2 混合模式接线：LLM 评分 + 规则式 expand
// LAYER: L1
// STATUS: implemented
// NewMCTSRunnerWired 创建接线完成的 MCTSRunner，LLM 评分 + 规则式 expand。
//
// Phase 2 混合模式：LLM 四维评分（mctsScorerImpl）+ 规则式 expander。
// expander 暂用规则式（ruleLocateExpand/ruleFixExpand），后续可替换为 LLM expander。
//
// llmClient 为 nil 时自动回退到纯规则式（等效于 NewMCTSRunner）。
func NewMCTSRunnerWired(cfg *config.Config, renderer prompt.Renderer,
	llmClient llm.Client, model string) MCTSRunner {

	if llmClient == nil {
		return NewMCTSRunner(cfg)
	}

	scorer := NewMCTSScorer(renderer, llmClient, model)

	// 从 config 读取 score 参数
	if cfg != nil {
		locateScoreCfg := cfg.MCTS.Locate
		scorer.WithScoreParams(locateScoreCfg.GetScoreMaxTokens(), locateScoreCfg.GetScoreTemperature())
	}

	return NewMCTSRunnerWithLLM(cfg,
		mcts.MakeLocateExpander(ruleLocateExpand),
		scorer.ToLocateScorer(),
		mcts.MakeFixExpander(ruleFixExpand),
		scorer.ToFixScorer(),
	)
}

// NewMCTSRunnerWiredFull 创建全 LLM 驱动的 MCTSRunner（expander + scorer 均用 LLM）。
//
// Phase 2 完整模式：LLM expander + LLM 四维评分。
// inputYAML 和 planJSON 分别为 locate 和 fix 阶段的上下文数据。
// agentCfg 为运行配置，如果为 nil 则从 cfg 中获取默认 agent 配置。
func NewMCTSRunnerWiredFull(
	cfg *config.Config,
	renderer prompt.Renderer,
	llmClient llm.Client,
	model string,
	inputYAML []byte,
	planJSON []byte,
	skills []string,
	agentCfg AgentRunConfig,
) MCTSRunner {

	if llmClient == nil {
		return NewMCTSRunner(cfg)
	}

	scorer := NewMCTSScorer(renderer, llmClient, model)
	if cfg != nil {
		locateScoreCfg := cfg.MCTS.Locate
		scorer.WithScoreParams(locateScoreCfg.GetScoreMaxTokens(), locateScoreCfg.GetScoreTemperature())
	}

	locateExpander := NewLocateExpander(renderer, llmClient, inputYAML, skills, agentCfg)
	fixExpander := NewFixExpander(renderer, llmClient, planJSON, skills, agentCfg)

	return NewMCTSRunnerWithLLM(cfg,
		locateExpander,
		scorer.ToLocateScorer(),
		fixExpander,
		scorer.ToFixScorer(),
	)
}
