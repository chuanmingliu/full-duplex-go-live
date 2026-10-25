package provider

import (
	"context"
	"errors"
	"log/slog"
	"time"
)

// HedgeLLM returns an LLM that asks inner the same thing more than once and
// answers with whichever copy speaks first.
//
// A chat model's time to its first token is the largest single term between a
// caller finishing a sentence and hearing a syllable, and it is not one
// number: the same request to the same model, seconds apart, was measured
// anywhere from 170 to 880 ms. A turn pays whatever it happens to draw. Two
// draws and the better of them is a different distribution — the median of
// the faster of two is roughly the 30th percentile of one — and the price is a
// second request whose output is thrown away.
//
// copies is how many requests a completion may use, and after how long to wait
// for a first token before sending the next; zero sends them together. A copy
// that fails before producing anything is replaced at once, so a hedged
// completion also survives one refused connection. Once a copy has produced
// something the others are cancelled and its stream is the completion: copies
// are never mixed.
//
// copies < 2 returns inner unchanged.
func HedgeLLM(inner LLM, copies int, after time.Duration, log *slog.Logger) LLM {
	return HedgeLLMOnClause(inner, copies, after, 0, log)
}

// HedgeLLMOnClause is HedgeLLM with the race decided on the first clause:
// a copy wins once it has produced clauseChars characters of text (or a tool
// call, or has ended), not at its first token. clauseChars <= 1 is the
// first-token race.
func HedgeLLMOnClause(inner LLM, copies int, after time.Duration, clauseChars int, log *slog.Logger) LLM {
	if copies < 2 || inner == nil {
		return inner
	}
	if log == nil {
		log = slog.Default()
	}
	return &hedgedLLM{inner: inner, copies: copies, after: after, clause: clauseChars, log: log, lane: ReserveLanes(copies)}
}

type hedgedLLM struct {
	inner  LLM
	copies int
	after  time.Duration
	// clause is how many characters a copy must have produced to win; 0 or 1
	// is the first token.
	clause int
	log    *slog.Logger
	// lane is the first of this wrapper's lanes in the provider's pool: copy n
	// of every completion goes to lane+n. See WithLane.
	lane int
}

func (h *hedgedLLM) Name() string { return h.inner.Name() }

// Prewarm implements Prewarmer when the model behind does: every lane this
// wrapper's copies will use is made ready, not only the first.
func (h *hedgedLLM) Prewarm(ctx context.Context) error {
	p, ok := h.inner.(Prewarmer)
	if !ok {
		return nil
	}
	errs := make(chan error, h.copies)
	for n := 0; n < h.copies; n++ {
		go func(n int) { errs <- p.Prewarm(WithLane(ctx, h.lane+n)) }(n)
	}
	var first error
	for n := 0; n < h.copies; n++ {
		if err := <-errs; err != nil && first == nil {
			first = err
		}
	}
	return first
}

// errHedgeEmpty is a copy that closed its stream without a word.
var errHedgeEmpty = errors.New("provider: the completion ended without output")

// hedgeLead is a copy's first news: the stream it will go on with and what it
// has said so far, or why there is no stream.
type hedgeLead struct {
	n      int
	ch     <-chan LLMDelta
	said   []LLMDelta
	err    error
	closed bool
}

// streamEvery sends one copy down every lane and lets each finish. What comes
// back is the first lane's completion, or the first that succeeded.
func (h *hedgedLLM) streamEvery(ctx context.Context, req LLMRequest) (<-chan LLMDelta, error) {
	out := make(chan LLMDelta, 64)
	go func() {
		defer close(out)
		type done struct {
			n      int
			deltas []LLMDelta
			err    error
		}
		results := make(chan done, h.copies)
		for n := 0; n < h.copies; n++ {
			go func(n int) {
				ch, err := h.inner.Stream(WithLane(ctx, h.lane+n), req)
				if err != nil {
					results <- done{n: n, err: err}
					return
				}
				var got []LLMDelta
				for d := range ch {
					if d.Err != nil {
						err = d.Err
						continue
					}
					got = append(got, d)
				}
				results <- done{n: n, deltas: got, err: err}
			}(n)
		}
		best := done{n: -1}
		var lastErr error
		for i := 0; i < h.copies; i++ {
			r := <-results
			if r.err != nil {
				lastErr = r.err
				continue
			}
			if best.n < 0 || r.n < best.n {
				best = r
			}
		}
		if best.n < 0 {
			if lastErr != nil {
				out <- LLMDelta{Err: lastErr}
			}
			return
		}
		for _, d := range best.deltas {
			select {
			case out <- d:
			case <-ctx.Done():
				return
			}
		}
	}()
	return out, nil
}

func (h *hedgedLLM) Stream(ctx context.Context, req LLMRequest) (<-chan LLMDelta, error) {
	if EveryCopy(ctx) {
		return h.streamEvery(ctx, req)
	}
	out := make(chan LLMDelta, 64)
	all, cancelAll := context.WithCancel(ctx)
	started := time.Now()

	go func() {
		defer close(out)
		defer cancelAll()

		leads := make(chan hedgeLead, h.copies)
		cancels := make([]context.CancelFunc, 0, h.copies)
		launch := func() {
			n := len(cancels)
			actx, cancel := context.WithCancel(WithLane(all, h.lane+n))
			cancels = append(cancels, cancel)
			go func() {
				ch, err := h.inner.Stream(actx, req)
				if err != nil {
					leads <- hedgeLead{n: n, err: err}
					return
				}
				// What counts as speaking first is output: a word, or a call.
				// A provider may open with deltas that carry neither. On the
				// clause race, enough words for the synthesizer's first
				// segment; a call wins at once either way.
				var said []LLMDelta
				chars := 0
				for d := range ch {
					if d.Err != nil {
						leads <- hedgeLead{n: n, err: d.Err}
						return
					}
					said = append(said, d)
					chars += len([]rune(d.Text))
					if d.ToolCall != nil || (chars > 0 && chars >= h.clause) {
						leads <- hedgeLead{n: n, ch: ch, said: said}
						return
					}
				}
				if chars > 0 {
					// Ended before the clause: a short answer, whole. It is
					// as good a lead as any — nothing more is coming.
					leads <- hedgeLead{n: n, ch: ch, said: said}
					return
				}
				if len(said) == 0 {
					leads <- hedgeLead{n: n, err: errHedgeEmpty}
					return
				}
				// Ended having said nothing audible — usage only. That is a
				// whole completion all the same, and an empty one.
				leads <- hedgeLead{n: n, said: said, closed: true}
			}()
		}
		var timer <-chan time.Time
		next := func() {
			timer = nil
			if len(cancels) >= h.copies {
				return
			}
			if len(cancels) > 0 && h.after > 0 {
				timer = time.After(h.after)
				return
			}
			for len(cancels) < h.copies {
				launch()
				if h.after > 0 {
					timer = time.After(h.after)
					return
				}
			}
		}
		send := func(d LLMDelta) bool {
			select {
			case out <- d:
				return true
			case <-ctx.Done():
				return false
			}
		}

		next()
		failed := 0
		var lastErr error
		var empty *hedgeLead
		for {
			select {
			case <-ctx.Done():
				return
			case <-timer:
				launch()
				next()
			case lead := <-leads:
				if lead.err != nil || lead.closed {
					failed++
					if lead.err != nil {
						lastErr = lead.err
					} else {
						l := lead
						empty = &l
					}
					if len(cancels) < h.copies {
						// Do not sit out the hedge delay behind a copy that
						// has already been lost.
						launch()
						next()
					}
					if failed < len(cancels) {
						continue
					}
					switch {
					case empty != nil:
						for _, d := range empty.said {
							if !send(d) {
								return
							}
						}
					case lastErr != nil:
						send(LLMDelta{Err: lastErr})
					}
					return
				}
				for i, cancel := range cancels {
					if i != lead.n {
						cancel()
					}
				}
				if len(cancels) > 1 {
					h.log.Debug("llm hedge: a copy answered first",
						"copy", lead.n+1, "of", len(cancels),
						"ms", time.Since(started).Milliseconds(), "on", map[bool]string{true: "clause", false: "token"}[h.clause > 1])
				}
				for _, d := range lead.said {
					if !send(d) {
						return
					}
				}
				for d := range lead.ch {
					if !send(d) {
						return
					}
				}
				return
			}
		}
	}()
	return out, nil
}
