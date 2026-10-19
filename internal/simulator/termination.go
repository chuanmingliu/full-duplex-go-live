package simulator

// Termination describes observed ending evidence separately from whether the run
// passed. A local request is not proof that a SIP BYE was sent or received.
type Termination struct {
	Side                 string `json:"side"`
	Evidence             string `json:"evidence"`
	GatewayEnded         bool   `json:"gateway_ended"`
	LocalHangupRequested bool   `json:"local_hangup_requested"`
	NativeInitiator      string `json:"native_initiator,omitempty"`
	NativeReason         string `json:"native_reason,omitempty"`
	LocalReason          string `json:"local_reason,omitempty"`
}

func defaultTermination(t Termination) Termination {
	if t.Side == "" {
		t.Side = "unknown"
	}
	if t.Evidence == "" {
		t.Evidence = "not_observed"
	}
	return t
}

func nativeTermination(initiator, reason string) Termination {
	t := Termination{Side: "unknown", Evidence: "native_gateway_hangup", GatewayEnded: true}
	switch initiator {
	case "caller", "callee", "system":
		t.NativeInitiator = initiator
	}
	switch reason {
	case "ByCaller", "ByCallee", "ByRefer", "BySystem", "Autohangup", "InactivityTimeout", "NoAnswer", "NoBalance", "AnswerMachine", "ServerUnavailable", "Canceled", "Rejected", "Failed":
		t.NativeReason = reason
	case "":
	default:
		t.NativeReason = "other"
	}
	switch t.NativeInitiator {
	case "caller":
		t.Side = "caller"
	case "callee":
		t.Side = "receiver_gateway"
	case "system":
		switch t.NativeReason {
		case "Autohangup", "InactivityTimeout":
			t.Side = "receiver_gateway"
		}
	}
	return t
}

func (r *run) recordTermination(e Event) {
	if e.Kind == "stop" && r.termination.LocalReason == "" {
		// Controller-generated stop reasons only; never persist caller-supplied text.
		switch e.Reason {
		case "stopped", "duration limit", "remote turn limit", "inactivity limit", "duration_limit", "remote_turn_limit", "inactivity", "scripted hangup", "customer_hangup":
			r.termination.LocalReason = e.Reason
		}
	}
	if e.Kind == "failed" && e.Reason == "Active Call control disconnected" && !r.termination.GatewayEnded {
		r.termination.Evidence = "control_disconnected"
		// Preserve a local stop decision, but never infer one from cleanup Hangup.
		if r.termination.LocalReason == "" {
			r.termination.Side = "unknown"
		}
	}
	if e.Kind != "ended" {
		return
	}
	requested, localReason := r.termination.LocalHangupRequested, r.termination.LocalReason
	if e.Termination != nil {
		r.termination = nativeTermination(e.Termination.NativeInitiator, e.Termination.NativeReason)
		if e.Termination.Evidence == "native_gateway_track_end" {
			r.termination = Termination{Side: "unknown", GatewayEnded: true, Evidence: e.Termination.Evidence}
		}
	} else {
		r.termination.GatewayEnded = true
	}
	r.termination.LocalHangupRequested = requested
	r.termination.LocalReason = localReason
}

func (r *run) terminationReason() string {
	if r.state.Phase == Completed && r.termination.LocalHangupRequested {
		if reason := receiverStopReason(r.termination.LocalReason); reason != "" {
			return reason
		}
	}
	if r.state.Phase == Completed && r.termination.GatewayEnded {
		if r.termination.LocalHangupRequested && r.termination.LocalReason == "customer_hangup" {
			return "customer_hangup"
		}
		if r.termination.NativeInitiator == "caller" && r.termination.NativeReason != "Canceled" {
			return "remote_hangup"
		}
		if r.reason == "remote_hangup" && !r.termination.LocalHangupRequested {
			return "remote_hangup"
		}
		if r.termination.LocalReason != "" {
			return r.termination.LocalReason
		}
	}
	return redact(r.reason)
}
