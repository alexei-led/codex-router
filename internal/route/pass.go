package route

import "github.com/alexei-led/codex-router/internal/responses"

// PassThrough clears a pending routed reply when metadata identifies a
// request that will not be routed. The response to that request must not be
// accounted as the reply to the previous routed decision.
func (e *Engine) PassThrough(m responses.Meta) {
	if responses.Classify(m) == responses.KindPassthrough {
		e.passThrough(threadKey(m))
	}
}
