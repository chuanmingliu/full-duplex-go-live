package simulator

import "context"

// MediaDisconnected allows native call-end signaling to catch up with media
// teardown. It shares the bounded control deadline, rather than racing a
// separate, shorter server timer against gateway confirmation.
func (c *RunController) MediaDisconnected(ctx context.Context, b Binding) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	r, ok := c.runs[b.RunID]
	if !ok {
		return ErrNotFound
	}
	if !exactBinding(r.state.Binding, b) || b.SessionID == "" {
		return ErrInvalidTransition
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if r.state.Phase.Terminal() || r.mediaDisconnected {
		return nil
	}
	r.mediaDisconnected = true
	r.cancel()
	if r.state.Phase != Ending {
		r.settlePendingAudioBoundaries()
		c.cancelTimers(r)
	}
	c.schedule(r, c.opts.SetupTimeout, func() {
		_ = c.Handle(context.Background(), Event{Kind: "failed", Binding: b, AtMS: c.RelativeMS(), Reason: "media connection closed without hangup confirmation"})
	})
	return nil
}
