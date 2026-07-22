package session

// DefaultContextWindow 未显式配置时的默认 context window（Claude Opus ~200K tokens）。
const DefaultContextWindow = 200000

// DefaultThresholdPct 上下文使用率阈值（百分比），超过触发被动 Reset。
const DefaultThresholdPct = 40

// TrackTokens 累计 input/output token 用量并更新 Usage 比率。
// 应在每次 LLM 调用返回后调用。
func TrackTokens(s *Session, inputTokens, outputTokens int) {
	s.tokenUsage.InputTokens += inputTokens
	s.tokenUsage.OutputTokens += outputTokens

	window := s.ContextWindow
	if window <= 0 {
		window = DefaultContextWindow
	}
	s.tokenUsage.ContextWindow = window

	s.Usage = s.tokenUsage.UsageRatio()
}

// GetTokenUsage 返回 token 消耗详情。
func GetTokenUsage(s *Session) TokenUsage {
	return s.tokenUsage
}

// ResetTokenUsage 重置 token 计数器（用于 Context Reset 后的新 session）。
func ResetTokenUsage(s *Session) {
	s.tokenUsage = TokenUsage{ContextWindow: s.ContextWindow}
	s.Usage = 0
}

// SetContextWindow 配置 context window 上限。
func SetContextWindow(s *Session, window int) {
	if window <= 0 {
		window = DefaultContextWindow
	}
	s.ContextWindow = window
	s.tokenUsage.ContextWindow = window
}

// IsAboveThreshold 检查是否超过阈值（默认 40%）。
func IsAboveThreshold(s *Session) bool {
	return s.Usage*100 >= float64(DefaultThresholdPct)
}
