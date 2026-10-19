package simulator

import "time"

const endingDeadlineReason = "termination deadline; missing gateway ended confirmation"

// receiverStop tracks an intentional policy stop separately from native SIP
// evidence. A close callback can arrive inside Hangup, before it returns an error.
type receiverStop struct {
	requestedAt                                 time.Time
	effectsSettled, effectsSucceeded, completed bool
	cleanupPending                              bool
	terminalEvent                               *Event
}

func receiverStopReason(reason string) string {
	switch reason {
	case "duration limit", "duration_limit":
		return "duration_limit"
	case "remote turn limit", "remote_turn_limit":
		return "remote_turn_limit"
	case "inactivity limit", "inactivity":
		return "inactivity"
	case "customer_hangup":
		return reason
	}
	return ""
}

func localStopCompletionSignal(e Event) bool {
	return e.Kind == "failed" && (e.Reason == "Active Call control disconnected" || e.Reason == endingDeadlineReason)
}

// deferReceiverTerminal runs only after Advance has validated the binding.
// Preserve the native event in preference to a close if both arrive during the
// command. Real media/control errors still fail immediately.
func (r *run) deferReceiverTerminal(e Event) bool {
	p := r.receiverStop
	if r.state.Phase != Ending || p == nil || p.effectsSettled || (e.Kind != "ended" && !localStopCompletionSignal(e)) {
		return false
	}
	if p.terminalEvent == nil || e.Kind == "ended" {
		p.terminalEvent = &e
	}
	return true
}

func (r *run) canCompleteReceiverStop(e Event) bool {
	p := r.receiverStop
	return r.state.Phase == Ending && p != nil && p.effectsSucceeded && r.hangupSent && len(r.failures) == 0 && localStopCompletionSignal(e)
}
