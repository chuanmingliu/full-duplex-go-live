package duplex

import (
	"math"
	"strings"
	"time"

	"github.com/chuanmingliu/golive/internal/live"
	"github.com/chuanmingliu/golive/internal/provider"
)

// This file is the conversation the backend is shown: what goes into it, what
// is trimmed from it, and what it costs.

// appendUserMessage adds the caller's words, joining them onto a user message
// already at the end rather than starting a second one.
//
// Two user messages in a row is what a turn that was never answered leaves
// behind, and it is the ordinary result of someone pausing mid-request: "我想
// 查一下保单" — the turn closes — "就是去年买的那个". Those are one request, and
// some chat backends refuse consecutive messages from the same role outright.
func appendUserMessage(msgs []provider.Message, text string) []provider.Message {
	text = strings.TrimSpace(text)
	if text == "" {
		return msgs
	}
	if n := len(msgs); n > 0 && msgs[n-1].Role == provider.RoleUser {
		// A copy: the slice may share its array with conversation history.
		merged := msgs[n-1]
		merged.Content = strings.TrimSpace(merged.Content + "\n" + text)
		out := append(msgs[:n-1:n-1], merged)
		return out
	}
	return append(msgs, provider.Message{Role: provider.RoleUser, Content: text})
}

// ensureUserRecorded puts a turn's words into conversation history, once.
//
// They used to be written only when the turn's answer finished playing, as a
// side effect of recording the answer. A turn with no audible answer therefore
// left no trace at all — and there are three ordinary ways to have one. The
// caller speaks again before the answer arrives. The backend answers with a
// function call rather than words. Or the turn is a client delegation the
// application has not answered yet. In each case the next request to the
// backend was missing the question it most needed, and the model answered the
// second half of a sentence.
func (e *Engine) ensureUserRecorded(turn *Turn) {
	if turn == nil || turn.Transcript == "" || e.recordedUser[turn.ID] {
		return
	}
	if len(e.recordedUser) > 64 {
		e.recordedUser = map[string]bool{}
	}
	e.recordedUser[turn.ID] = true
	e.history = appendUserMessage(e.history, turn.Transcript)
	e.noteContext()
}

// recordTurn commits what was actually heard to conversation history. Turns
// that were cut off contribute only their audible prefix; anything else would
// have the model reasoning from sentences the user never received.
func (e *Engine) recordTurn(turnID, spoken string) {
	spoken = strings.TrimSpace(spoken)
	e.post(func() {
		if IsBackchannel(turnID) {
			return // backchannels are not conversation
		}
		turn := e.tracker.Current()
		if turn != nil && turn.ID == turnID {
			turn.SetSpoken(spoken)
			// A guess that was never confirmed was made on a prefix of what
			// the caller is still saying. The whole of it arrives with the
			// turn that replaces this one; the prefix is not a message.
			if turn.Committed() {
				e.ensureUserRecorded(turn)
			}
		}
		if spoken != "" {
			e.history = append(e.history, provider.Message{
				Role:    provider.RoleAssistant,
				Content: spoken,
			})
		}
		e.trimHistory()
	})
}

// trimHistory bounds what is sent to the backend, by turns and by size.
//
// The turn cap alone is not a bound on cost. history_turns: 16 is sixteen
// exchanges whatever their length, and a sales agent's answers run to fifty
// characters each while a caller's questions run to four — so the same setting
// describes wildly different prompts, and the one it produces on a real call is
// the expensive end. Since time-to-first-token tracks prompt size and nothing
// else in this cascade comes close to it as a cost, the size needs its own
// limit.
//
// Oldest first, and always in whole messages: half an exchange is worse than
// none, because the model then reads an answer with no question or a question
// with no answer and infers a conversation that did not happen.
//
// And in steps, not one message at a time. Trimming to exactly the limit on
// every turn means that once a conversation is long enough to be trimmed at
// all, the front of the history is different on every single request — which
// is the one thing guaranteed to defeat a provider's prefix cache, on exactly
// the long prompts the cache exists for. Cutting back to well under the limit
// instead leaves the prefix byte-identical for several turns at a time, so the
// provider can reuse it, at the price of the oldest exchange or two leaving a
// little sooner than it strictly had to.
func (e *Engine) trimHistory() {
	defer e.noteContext()

	// What is never trimmed: the caller's latest words and everything after
	// them. That is the exchange being answered right now — the question, and
	// if a function was called for it, the call and its result. A function
	// result can be larger than the whole size limit by itself, and trimming
	// "oldest first until it fits" then ate its way through the entire
	// conversation and finally the result too, leaving the backend to continue
	// a response with nothing in front of it.
	tail := len(e.history) - 1
	for i := len(e.history) - 1; i >= 0; i-- {
		if e.history[i].Role == provider.RoleUser {
			tail = i
			break
		}
	}
	if tail < 0 {
		return
	}

	cut := 0
	if max := e.opts.Cfg.HistoryTurns * 2; max > 0 && len(e.history) > max {
		keep := max * 3 / 4
		if keep < 2 {
			keep = 2
		}
		cut = len(e.history) - keep
	}

	maxChars := e.opts.Cfg.HistoryMaxChars
	total := 0
	for _, m := range e.history[min(cut, tail):] {
		total += len([]rune(m.Content))
	}
	overSize := maxChars > 0 && total > maxChars
	if overSize {
		target := maxChars * 7 / 10
		if cut > tail {
			cut = tail
		}
		for total > target && cut < tail {
			total -= len([]rune(e.history[cut].Content))
			cut++
		}
	}
	if cut > tail {
		cut = tail
	}
	// Then on to the start of an exchange. A history that opens on an answer,
	// or on a function result, has the model inferring a question that was
	// never asked — and a result with no call in front of it is refused by
	// every chat backend outright.
	for cut < tail && e.history[cut].Role != provider.RoleUser && e.history[cut].Role != provider.RoleSystem {
		total -= len([]rune(e.history[cut].Content))
		cut++
	}
	if cut == 0 {
		return
	}
	dropped := cut
	e.history = append([]provider.Message(nil), e.history[cut:]...)
	e.dropOrphanedToolResults()
	e.log.Debug("think: history trimmed",
		"dropped_messages", dropped, "kept_messages", len(e.history),
		"chars", total, "limit_chars", maxChars, "over_size", overSize)
}

// dropOrphanedToolResults removes function results left at the front of history
// with no call in front of them, which every chat backend rejects.
func (e *Engine) dropOrphanedToolResults() {
	n := 0
	for n < len(e.history) && e.history[n].Role == provider.RoleTool {
		n++
	}
	if n > 0 {
		e.history = append([]provider.Message(nil), e.history[n:]...)
	}
}

// noteContext publishes how full the conversational context is, for
// session.usage.updated. The loop goroutine calls it whenever history or the
// instructions change, so readers on other goroutines never touch either.
func (e *Engine) noteContext() {
	ratio := 0.0
	if max := e.opts.Cfg.HistoryMaxChars; max > 0 {
		chars := 0
		for _, m := range e.history {
			chars += len([]rune(m.Content))
		}
		ratio = float64(chars) / float64(max)
	}
	if max := e.opts.Cfg.HistoryTurns * 2; max > 0 {
		if r := float64(len(e.history)) / float64(max); r > ratio {
			ratio = r
		}
	}
	if ratio > 1 {
		ratio = 1
	}
	e.ctxRatio.Store(math.Float64bits(ratio))
}

// Usage reports cumulative session cost drivers.
func (e *Engine) Usage() live.UsageBody {
	return live.UsageBody{
		Seconds:                  time.Since(e.startAt).Seconds(),
		InputAudioSeconds:        float64(e.inputMS.Load()) / 1000,
		OutputAudioSeconds:       float64(e.outputMS.Load()) / 1000,
		BackendInputTokens:       int(e.inTok.Load()),
		BackendOutputTokens:      int(e.outTok.Load()),
		BackendCachedInputTokens: int(e.cachedTok.Load()),
	}
}

// UsageEvent is session.usage.updated: a cumulative snapshot.
func (e *Engine) UsageEvent() live.Usage {
	return live.Usage{
		Envelope:      live.Envelope{Type: live.ServerUsageUpdated},
		Usage:         e.Usage(),
		ContextWindow: live.ContextWindow{UsageRatio: math.Float64frombits(e.ctxRatio.Load())},
	}
}

// History returns a copy of the conversation as the backend would be shown it,
// without the instructions. It is what a stored session keeps so that it can be
// forked.
//
// It works on an engine that has stopped, which is when it matters most: a
// session that runs into its time limit is stopped by the same deadline that
// ends the call, and its conversation was being saved as empty.
func (e *Engine) History() []provider.Message {
	done := make(chan []provider.Message, 1)
	e.post(func() { done <- e.historySnapshot() })
	select {
	case h := <-done:
		return h
	case <-e.loopDone:
	case <-time.After(2 * time.Second):
		return nil
	}
	// The loop has exited. If it ran the request on its way out, that is the
	// answer; otherwise nothing owns the state any more and it can be read
	// here.
	select {
	case h := <-done:
		return h
	default:
		return e.historySnapshot()
	}
}

// Instructions returns the session's instructions as they now stand, including
// anything session.instructions.append has added.
func (e *Engine) Instructions() string {
	e.instrMu.RLock()
	defer e.instrMu.RUnlock()
	return e.instr
}

// historySnapshot is History's body. Loop goroutine, or after it has exited.
func (e *Engine) historySnapshot() []provider.Message {
	out := append([]provider.Message(nil), e.history...)
	// Words the caller has said that no answer has yet carried into history
	// are still part of the conversation being saved.
	if t := e.lastCommitted; t != nil && !e.recordedUser[t.ID] {
		out = appendUserMessage(out, t.Transcript)
	}
	// So is an answer that is being spoken right now. It reaches history when
	// it ends or is cut off; a session that is closed or forked mid-sentence
	// would otherwise keep the question and lose the answer the caller was
	// listening to.
	if id, heard := e.player.Heard(); strings.TrimSpace(heard) != "" && !IsBackchannel(id) {
		out = append(out, provider.Message{Role: provider.RoleAssistant, Content: strings.TrimSpace(heard)})
	}
	return out
}
