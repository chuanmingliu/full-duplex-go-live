package simulator

import (
	"errors"
	"fmt"
)

const AudioBoundaryEvidence = "application_audio_boundary"

type BoundaryTurn struct {
	CustomerEndMS     int64 `json:"customer_end_ms"`
	AssistantStartMS  int64 `json:"assistant_start_ms"`
	HasCustomerEnd    bool  `json:"has_customer_end"`
	HasAssistantStart bool  `json:"has_assistant_start"`
	Overlapped        bool  `json:"overlapped"`
	Failed            bool  `json:"failed"`
}
type Gap struct {
	ValueMS  int64  `json:"value_ms"`
	Valid    bool   `json:"valid"`
	Reason   string `json:"reason,omitempty"`
	Evidence string `json:"evidence"`
}

func AssistantGap(t BoundaryTurn) Gap {
	g := Gap{Evidence: AudioBoundaryEvidence}
	switch {
	case t.Failed:
		g.Reason = "failed call or turn"
	case !t.HasCustomerEnd:
		g.Reason = "missing customer speech end"
	case !t.HasAssistantStart:
		g.Reason = "missing assistant response"
	case t.Overlapped:
		g.Reason = "overlapping speech"
	case t.AssistantStartMS < t.CustomerEndMS:
		g.Reason = "backwards timestamp"
	default:
		g.Valid = true
		g.ValueMS = t.AssistantStartMS - t.CustomerEndMS
	}
	return g
}

// Observations must use one receiver-local monotonic clock, at actual successful
// audio writes and received frames. Generation/enqueue events are not delivery.
type Observation struct {
	ID        string `json:"id,omitempty"`
	Kind      string `json:"kind"`
	Role      string `json:"role,omitempty"`
	AtMS      int64  `json:"at_ms"`
	NonSilent bool   `json:"non_silent,omitempty"`
	End       bool   `json:"end,omitempty"`
	Text      string `json:"text,omitempty"`
}
type observer struct {
	lastMS                              int64
	seen                                map[string]bool
	assistantSpeaking, customerSpeaking bool
	assistantTurns, customerTurns       []BoundaryTurn
	activeRemoteTurn                    int
	overlaps                            int
	overlapStartMS, overlapMS           int64
	overlapActive                       bool
	customerStartMS                     int64
	retainTranscript                    bool
	invalid                             int
	answered, greetings, ended          int
	transcript                          []TranscriptEntry
}
type TranscriptEntry struct {
	Role string `json:"role"`
	AtMS int64  `json:"at_ms"`
	Text string `json:"text"`
}

func newObserver() *observer {
	return &observer{lastMS: -1, activeRemoteTurn: -1, seen: map[string]bool{}}
}
func (o *observer) observe(v Observation) error {
	if v.ID != "" && o.seen[v.ID] {
		return nil
	}
	if v.AtMS < 0 || v.AtMS < o.lastMS {
		o.invalid++
		return errors.New("observation timestamp went backwards")
	}
	if v.ID != "" {
		if len(o.seen) < 4096 {
			o.seen[v.ID] = true
		} else {
			return errors.New("observation id capacity reached")
		}
	}
	o.lastMS = v.AtMS
	kind := v.Kind
	switch kind {
	case "assistant_audio":
		if !v.End && !v.NonSilent {
			return nil
		}
		if v.End {
			if !o.assistantSpeaking {
				return nil
			}
			kind = "remote_speech_ended"
		} else {
			if o.assistantSpeaking {
				return nil
			}
			kind = "remote_speech_started"
		}
	case "customer_audio":
		if !v.End && !v.NonSilent {
			return nil
		}
		if v.End {
			if !o.customerSpeaking {
				return nil
			}
			kind = "customer_speech_ended"
		} else {
			if o.customerSpeaking {
				return nil
			}
			kind = "customer_speech_started"
		}
	}
	switch kind {
	case "remote_speech_started":
		if o.assistantSpeaking {
			return nil
		}
		o.assistantSpeaking = true
		o.activeRemoteTurn = -1
		if len(o.customerTurns) < 256 {
			o.activeRemoteTurn = len(o.customerTurns)
			o.customerTurns = append(o.customerTurns, BoundaryTurn{Overlapped: o.customerSpeaking})
		}
		if o.customerSpeaking {
			o.overlaps++
			o.overlapStartMS = v.AtMS
			o.overlapActive = true
		}
		if len(o.assistantTurns) > 0 {
			p := &o.assistantTurns[len(o.assistantTurns)-1]
			if !p.HasAssistantStart {
				p.AssistantStartMS = v.AtMS
				p.HasAssistantStart = true
				p.Overlapped = p.Overlapped || o.customerSpeaking
			}
		} else if o.customerSpeaking {
			if len(o.assistantTurns) < 256 {
				o.assistantTurns = append(o.assistantTurns, BoundaryTurn{AssistantStartMS: v.AtMS, HasAssistantStart: true, Overlapped: true})
			}
		}
	case "remote_speech_ended":
		o.endRemote(v.AtMS)
	case "customer_speech_started":
		if o.customerSpeaking {
			return nil
		}
		o.customerSpeaking = true
		o.customerStartMS = v.AtMS
		if len(o.assistantTurns) < 256 {
			o.assistantTurns = append(o.assistantTurns, BoundaryTurn{Overlapped: o.assistantSpeaking})
		}
		if o.assistantSpeaking {
			o.overlaps++
			o.overlapStartMS = v.AtMS
			o.overlapActive = true
		}
		if len(o.customerTurns) > 0 && !o.customerTurns[len(o.customerTurns)-1].HasAssistantStart {
			pending := &o.customerTurns[len(o.customerTurns)-1]
			pending.AssistantStartMS = v.AtMS
			pending.HasAssistantStart = true
			pending.Overlapped = pending.Overlapped || o.assistantSpeaking
		} else if len(o.customerTurns) < 256 {
			o.customerTurns = append(o.customerTurns, BoundaryTurn{AssistantStartMS: v.AtMS, HasAssistantStart: true, Overlapped: o.assistantSpeaking})
		}
	case "customer_speech_ended":
		o.endCustomer(v.AtMS)
	case "transcript":
		if v.Role != "assistant" && v.Role != "customer" {
			return errors.New("transcript requires normalized assistant or customer role")
		}
		if o.retainTranscript && len(o.transcript) < 256 {
			o.transcript = append(o.transcript, TranscriptEntry{Role: v.Role, AtMS: v.AtMS, Text: v.Text})
		}
	case "answered":
		o.answered = 1
	case "greeting":
		o.greetings++
	case "ended":
		o.ended = 1
	default:
		return fmt.Errorf("unsupported observation kind %q", kind)
	}
	return nil
}
func (o *observer) assistantGaps() []Gap {
	r := make([]Gap, 0, len(o.assistantTurns))
	for _, t := range o.assistantTurns {
		r = append(r, AssistantGap(t))
	}
	return r
}
func (o *observer) customerGaps() []Gap {
	r := make([]Gap, 0, len(o.customerTurns))
	for _, t := range o.customerTurns {
		g := AssistantGap(t)
		if !t.HasCustomerEnd {
			g.Reason = "missing assistant speech end"
		} else if !t.HasAssistantStart {
			g.Reason = "missing customer response"
		}
		r = append(r, g)
	}
	return r
}

// endCustomer may resolve a delayed silence inference using the last successful
// audio write timestamp. It deliberately does not rewrite the observation clock.
func (o *observer) endCustomer(atMS int64) {
	if !o.customerSpeaking {
		return
	}
	if o.overlapActive {
		if atMS > o.overlapStartMS {
			o.overlapMS += atMS - o.overlapStartMS
		} else if o.overlaps > 0 {
			o.overlaps--
		}
		o.overlapActive = false
	}
	o.customerSpeaking = false
	if len(o.assistantTurns) > 0 {
		p := &o.assistantTurns[len(o.assistantTurns)-1]
		if !p.HasCustomerEnd {
			p.CustomerEndMS = atMS
			p.HasCustomerEnd = true
			if p.HasAssistantStart && p.AssistantStartMS >= atMS {
				p.Overlapped = false
			}
			return
		}
	}
	if len(o.assistantTurns) < 256 {
		o.assistantTurns = append(o.assistantTurns, BoundaryTurn{CustomerEndMS: atMS, HasCustomerEnd: true, Overlapped: o.assistantSpeaking})
	}
}

func (o *observer) endRemote(atMS int64) {
	if !o.assistantSpeaking {
		return
	}
	if o.overlapActive {
		if atMS > o.overlapStartMS {
			o.overlapMS += atMS - o.overlapStartMS
		} else if o.overlaps > 0 {
			o.overlaps--
		}
		o.overlapActive = false
	}
	o.assistantSpeaking = false
	// Customer utterances may append additional rows during the remote silence
	// debounce. Resolve the row reserved at this remote utterance's onset.
	if o.activeRemoteTurn >= 0 && o.activeRemoteTurn < len(o.customerTurns) {
		p := &o.customerTurns[o.activeRemoteTurn]
		p.CustomerEndMS = atMS
		p.HasCustomerEnd = true
		if p.HasAssistantStart && p.AssistantStartMS >= atMS {
			p.Overlapped = false
		}
		if len(o.assistantTurns) > 0 && o.customerStartMS >= atMS {
			pending := &o.assistantTurns[len(o.assistantTurns)-1]
			if !pending.HasAssistantStart {
				pending.Overlapped = false
			}
		}
	}
	o.activeRemoteTurn = -1
}
