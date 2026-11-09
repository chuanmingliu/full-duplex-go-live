package duplex

import (
	"encoding/json"

	"github.com/chuanmingliu/golive/internal/live"
)

// ApplyBusinessEvent accepts a trusted, session-scoped backend notification.
// User transcripts and model tool calls cannot manufacture this state. Repeated
// addV delivery is idempotent for the lifetime of the session.
func (e *Engine) ApplyBusinessEvent(name string) bool {
	if name != "addV" || e.tools == nil || e.tools.memory == nil {
		return false
	}
	e.post(func() {
		if e.wechatAdded || e.tools == nil || e.tools.memory == nil {
			return
		}
		patch := json.RawMessage(`{"wechat":{"status":"added"}}`)
		version, err := e.tools.memory.Patch(patch)
		if err != nil {
			return
		}
		e.wechatAdded = true
		// An authoritative completion can arrive after retrieval or during speech.
		// Replan only the active WeChat answer; native tool tasks keep their state.
		if turn := e.wechatTurn; turn != nil && e.tracker.IsCurrent(turn) && e.assistantHasFloor() {
			e.ensureUserRecorded(turn)
			e.cancelBackend()
			e.gen.Bump()
			e.abortSpeech()
			next := e.tracker.Begin("", false)
			next.skillScope = skillScope(turn)
			e.specTurn = nil
			e.commitTurn(next)
			e.startBackend(next, "business_event")
		}
		e.deps.Emit(live.MemoryEvent{Envelope: live.Envelope{Type: live.ExtMemory}, Version: version, Patch: patch})
	})
	return true
}

// protectBusinessState preserves the backend's completed state through model
// updates, including whole-object replacement and deletion attempts.
func (e *Engine) protectBusinessState(raw json.RawMessage) (json.RawMessage, bool) {
	var patch map[string]any
	if json.Unmarshal(raw, &patch) != nil || patch == nil {
		return raw, true
	}
	wechat, _ := patch["wechat"].(map[string]any)
	if !e.wechatAdded {
		if wechat["status"] == "added" {
			return nil, false
		}
		return raw, true
	}
	if _, changed := patch["wechat"]; !changed {
		return raw, true
	}
	if wechat == nil {
		wechat = map[string]any{}
	}
	wechat["status"] = "added"
	patch["wechat"] = wechat
	out, _ := json.Marshal(patch)
	return out, true
}
