package simulator

import (
	"context"
	"strings"
	"time"
)

const (
	CustomerGoalToolName     = "complete_customer_goal"
	CustomerFarewellActionID = "__customer_farewell"
	CustomerFarewellHold     = 5 * time.Second
	customerReplyWait        = 3 * time.Second
)

// An omitted setting enables natural customer hangup; false preserves the
// previous behavior of waiting for the caller or a scenario limit.
func (s Scenario) CustomerHangupEnabled() bool {
	return s.HangupAfterGoal == nil || *s.HangupAfterGoal
}

func (s Scenario) CustomerFarewell() string {
	if strings.HasPrefix(strings.ToLower(s.Language), "zh") {
		return "谢谢，再见。"
	}
	return "Thank you. Goodbye."
}

type customerHangup struct {
	farewellDone bool
	replyStarted bool
}

// GoalReached accepts only the private customer model's completion signal for
// this live session. It is independent of optional transcript retention.
func (c *RunController) GoalReached(ctx context.Context, b Binding) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	r, ok := c.runs[b.RunID]
	if !ok {
		return ErrNotFound
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if !exactBinding(r.state.Binding, b) || b.SessionID == "" || r.state.Phase != Connected || r.scenario.Goal == "" || !r.scenario.CustomerHangupEnabled() {
		return ErrInvalidTransition
	}
	c.beginCustomerHangup(r, "customer_goal_reached")
	return nil
}

// The caller holds the controller lock for all helpers below, except the
// scheduled callbacks that acquire it themselves.
func (c *RunController) beginCustomerHangup(r *run, reason string) {
	if r.state.Phase != Connected || r.mediaDisconnected || r.customerHangup != nil || !r.scenario.CustomerHangupEnabled() {
		return
	}
	r.customerHangup = &customerHangup{}
	if r.idle != nil {
		r.idle.Stop()
		r.idle = nil
	}
	r.idleGeneration++
	c.record(r, reason, "")
	id := r.state.Binding.RunID
	c.schedule(r, 0, func() {
		c.performAction(id, Action{ID: CustomerFarewellActionID, Kind: "speak", Text: r.scenario.CustomerFarewell(), MaxHoldMS: CustomerFarewellHold.Milliseconds()})
	})
}

func (c *RunController) scriptExhausted(r *run) {
	if len(r.scenario.Actions) == 0 || r.customerHangup != nil {
		return
	}
	for _, a := range r.scenario.Actions {
		// Explicit hangup actions retain their existing immediate behavior.
		if a.Kind == "hangup" {
			return
		}
		settled := false
		for _, outcome := range r.actions {
			if outcome.ID == a.ID {
				switch outcome.Result {
				case "completed", "skipped", "failed", "timed_out", "cancelled":
					settled = true
				}
			}
			if settled {
				break
			}
		}
		if !settled {
			return
		}
	}
	c.beginCustomerHangup(r, "customer_script_exhausted")
}

func (c *RunController) customerFarewellOutcome(r *run, outcome ActionOutcome) {
	if r.customerHangup == nil || r.state.Phase != Connected || r.customerHangup.farewellDone {
		return
	}
	if outcome.Result != "completed" {
		if outcome.Result == "failed" || outcome.Result == "timed_out" || outcome.Result == "cancelled" {
			b := r.state.Binding
			c.schedule(r, 0, func() {
				_ = c.Handle(context.Background(), Event{Kind: "failed", Binding: b, AtMS: c.RelativeMS(), Reason: "customer farewell failed"})
			})
		}
		return
	}
	r.customerHangup.farewellDone = true
	// An assistant already speaking when the farewell finishes may be replying
	// with overlap. Its next observed end still bounds the wait.
	r.customerHangup.replyStarted = r.observer.assistantSpeaking
	b := r.state.Binding
	c.schedule(r, customerReplyWait, func() { c.finishCustomerHangup(b) })
}

func (c *RunController) customerHangupRemoteStarted(r *run) {
	if h := r.customerHangup; h != nil && h.farewellDone {
		h.replyStarted = true
	}
}

func (c *RunController) customerHangupRemoteEnded(r *run) {
	if h := r.customerHangup; h != nil && h.farewellDone && h.replyStarted {
		b := r.state.Binding
		c.schedule(r, 0, func() { c.finishCustomerHangup(b) })
	}
}

func (c *RunController) finishCustomerHangup(b Binding) {
	c.mu.Lock()
	r := c.runs[b.RunID]
	ready := r != nil && r.state.Phase == Connected && exactBinding(r.state.Binding, b) && r.customerHangup != nil && r.customerHangup.farewellDone
	c.mu.Unlock()
	if ready {
		_ = c.Handle(context.Background(), Event{Kind: "stop", Binding: b, AtMS: c.RelativeMS(), Reason: "customer_hangup"})
	}
}
