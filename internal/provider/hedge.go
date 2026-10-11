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
	if copies < 2 || inner == nil {
		return inner
	}
	if log == nil {
		log = slog.Default()
	}
	return &hedgedLLM{inner: inner, copies: copies, after: after, log: log}
}

type hedgedLLM struct {
	inner  LLM
	copies int
	after  time.Duration
	log    *slog.Logger
}

func (h *hedgedLLM) Name() string { return h.inner.Name() }

// Prewarm implements Prewarmer when the model behind does.
func (h *hedgedLLM) Prewarm(ctx context.Context) error {
	if p, ok := h.inner.(Prewarmer); ok {
		return p.Prewarm(ctx)
	}
	return nil
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

func (h *hedgedLLM) Stream(ctx context.Context, req LLMRequest) (<-chan LLMDelta, error) {
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
			actx, cancel := context.WithCancel(all)
			cancels = append(cancels, cancel)
			go func() {
				ch, err := h.inner.Stream(actx, req)
				if err != nil {
					leads <- hedgeLead{n: n, err: err}
					return
				}
				// What counts as speaking first is output: a word, or a call.
				// A provider may open with deltas that carry neither.
				var said []LLMDelta
				for d := range ch {
					if d.Err != nil {
						leads <- hedgeLead{n: n, err: d.Err}
						return
					}
					said = append(said, d)
					if d.Text != "" || d.ToolCall != nil {
						leads <- hedgeLead{n: n, ch: ch, said: said}
						return
					}
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
						"ms", time.Since(started).Milliseconds())
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
