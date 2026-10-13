package simulator

import (
	"errors"
	"fmt"
)

var ErrInvalidTransition = errors.New("invalid transition")

type Binding struct {
	RunID     string `json:"run_id"`
	CallID    string `json:"call_id,omitempty"`
	SessionID string `json:"session_id,omitempty"`
}
type Phase string

const (
	Prepared  Phase = "prepared"
	Waiting   Phase = "waiting"
	Ringing   Phase = "ringing"
	Answering Phase = "answering"
	Connected Phase = "connected"
	Ending    Phase = "ending"
	Completed Phase = "completed"
	Rejected  Phase = "rejected"
	Expired   Phase = "expired"
	Failed    Phase = "failed"
	Cancelled Phase = "cancelled"
	Aborted   Phase = "aborted"
)

func (p Phase) Terminal() bool {
	return p == Completed || p == Rejected || p == Expired || p == Failed || p == Cancelled || p == Aborted
}

type State struct {
	Phase            Phase   `json:"phase"`
	Binding          Binding `json:"binding"`
	AnswerSeen       bool    `json:"answer_seen"`
	VoiceReady       bool    `json:"voice_ready"`
	GreetingReleased bool    `json:"greeting_released"`
}
type Event struct {
	Kind    string  `json:"kind"`
	Binding Binding `json:"binding"`
	AtMS    int64   `json:"at_ms"`
	Reason  string  `json:"reason,omitempty"`
}
type Effect struct {
	Kind    string
	Binding Binding
}

func Advance(s State, e Event) (State, []Effect, error) {
	invalid := func() (State, []Effect, error) {
		return s, nil, fmt.Errorf("%w: %s in %s or mismatched binding", ErrInvalidTransition, e.Kind, s.Phase)
	}
	if e.AtMS < 0 || s.Binding.RunID == "" || e.Binding.RunID != s.Binding.RunID {
		return invalid()
	}
	// Reservation-local events cannot acquire a call binding through callbacks.
	if s.Binding.CallID == "" {
		if e.Kind != "incoming" && (e.Binding.CallID != "" || e.Binding.SessionID != "") {
			return invalid()
		}
		switch e.Kind {
		case "ended", "answered", "voice_ready", "answer_due", "associate":
			return invalid()
		}
	}
	if s.Binding.CallID != "" && e.Binding.CallID != s.Binding.CallID {
		return invalid()
	}
	if s.Binding.SessionID != "" && e.Binding.SessionID != "" && e.Binding.SessionID != s.Binding.SessionID {
		return invalid()
	}
	if s.Phase.Terminal() {
		switch e.Kind {
		case "ended", "stop", "failed", "answer_due", "answered", "voice_ready", "expired":
			return s, nil, nil
		default:
			return invalid()
		}
	}
	next := s
	if e.Binding.SessionID != "" {
		next.Binding.SessionID = e.Binding.SessionID
	}
	var effects []Effect
	effect := func(k string) { effects = append(effects, Effect{Kind: k, Binding: next.Binding}) }
	switch e.Kind {
	case "associate":
		if s.Binding.CallID == "" || e.Binding.SessionID == "" || (s.Phase != Answering && s.Phase != Connected) {
			return invalid()
		}
	case "reserve":
		if s.Phase != Prepared {
			return invalid()
		}
		next.Phase = Waiting
	case "incoming":
		if s.Phase == Ringing || s.Phase == Answering || s.Phase == Connected {
			return next, nil, nil
		}
		if s.Phase != Waiting || e.Binding.CallID == "" {
			return invalid()
		}
		next.Binding = e.Binding
		next.Phase = Ringing
	case "answer_due":
		if s.Phase != Ringing {
			return invalid()
		}
		next.Phase = Answering
		effect("accept")
	case "answered", "voice_ready":
		if s.Phase != Answering && s.Phase != Connected {
			return invalid()
		}
		if e.Kind == "answered" {
			next.AnswerSeen = true
		} else {
			if next.Binding.SessionID == "" {
				return invalid()
			}
			next.VoiceReady = true
		}
		if next.AnswerSeen && next.VoiceReady && !next.GreetingReleased {
			next.Phase = Connected
			next.GreetingReleased = true
			effect("open_greeting")
		}
	case "expired":
		if s.Phase != Waiting {
			return invalid()
		}
		next.Phase = Expired
		effect("finalize")
	case "stop":
		if s.Phase == Ending {
			return next, nil, nil
		}
		if s.Binding.CallID == "" {
			next.Phase = Cancelled
			effect("finalize")
		} else {
			next.Phase = Ending
			effect("close_voice")
			effect("hangup")
		}
	case "ended":
		next.Phase = Completed
		effect("close_voice")
		effect("finalize")
	case "failed":
		next.Phase = Failed
		if next.Binding.CallID != "" {
			effect("close_voice")
			effect("hangup")
		}
		effect("finalize")
	default:
		return invalid()
	}
	return next, effects, nil
}
