package session

import (
	"fmt"
	"log"
)

// Monitor 上下文使用率监控器。
//
// callback-based 设计（非轮询）：
//   - Manager 注册 ThresholdHandler 到 session.onThreshold
//   - 每次 TrackTokens 后 Monitor 同步检查阈值
//   - 超过阈值时调用回调，回调内由 Manager 执行 ForkOnThreshold
type Monitor struct {
	thresholdPct float64 // 阈值百分比（默认 40）
}

// NewMonitor 创建一个 Monitor 实例。
func NewMonitor(thresholdPct float64) *Monitor {
	if thresholdPct <= 0 {
		thresholdPct = float64(DefaultThresholdPct)
	}
	return &Monitor{thresholdPct: thresholdPct}
}

// Watch 将 session 纳入监控：注入阈值回调。
// 回调中应调用 Manager.Fork(s, ForkOnThreshold)。
func (m *Monitor) Watch(s *Session, onFork func(s *Session) (*Session, error)) {
	s.onThreshold = func(sess *Session) {
		log.Printf("[monitor] session %s usage %.1f%% exceeded threshold %.0f%%, triggering passive reset",
			sess.ID, sess.Usage*100, m.thresholdPct)

		newSess, err := onFork(sess)
		if err != nil {
			log.Printf("[monitor] passive reset failed for session %s: %v", sess.ID, err)
			return
		}
		log.Printf("[monitor] passive reset complete: %s → %s", sess.ID, newSess.ID)
	}
}

// Check 检查 session 是否超过阈值，若超过则触发回调。
// 应在每次 TrackTokens 之后调用。
func (m *Monitor) Check(s *Session) {
	if s.onThreshold == nil {
		return
	}

	threshold := m.thresholdPct / 100.0
	if s.Usage >= threshold {
		s.onThreshold(s)
	}
}

// TrackAndCheck 便捷方法：先 Track 后 Check。
func (m *Monitor) TrackAndCheck(s *Session, inputTokens, outputTokens int) {
	TrackTokens(s, inputTokens, outputTokens)
	m.Check(s)
}

// RegisterCompleteHook 注册节点完成时的主动 Reset 回调。
// tree.NodeOps.Complete 后应调用 Manager.Fork(parent, ForkOnComplete)。
func RegisterCompleteHook(s *Session, onComplete func(s *Session)) {
	s.onComplete = onComplete
}

// TriggerComplete 触发完成回调。
func TriggerComplete(s *Session) error {
	if s.onComplete == nil {
		return fmt.Errorf("session %s has no complete hook registered", s.ID)
	}
	s.onComplete(s)
	return nil
}

// IsOverThreshold 判断 session 是否超过监控阈值。
func (m *Monitor) IsOverThreshold(s *Session) bool {
	return s.Usage >= m.thresholdPct/100.0
}
