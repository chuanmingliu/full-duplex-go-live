package duplex

import (
	"context"
	"errors"
	"fmt"
	"math/rand"
	"strings"
	"time"

	"github.com/chuanmingliu/golive/internal/audio"
	"github.com/chuanmingliu/golive/internal/endpoint"
	"github.com/chuanmingliu/golive/internal/live"
	"github.com/chuanmingliu/golive/internal/metrics"
	"github.com/chuanmingliu/golive/internal/provider"
	"github.com/chuanmingliu/golive/internal/tools"
)

// This file is the listen channel past the detector: the recognizer's
// connection, the caller's transcript, and the decision to start answering
// before the turn is formally over.

const (
	// standbyMaxAge is how long a recognizer connection dialled on suspicion
	// is kept without audio. Vendors drop a silent realtime connection after
	// ten to fifteen seconds; this stays well inside that.
	standbyMaxAge = 5 * time.Second
	// resumeVoicedMS is how much continuous voice, after a pause, counts as
	// the speaker carrying on. One voiced frame is a click; four is a syllable.
	resumeVoicedMS = 80
	// finalWaitMax is how long a closed utterance waits for the recognizer's
	// final transcript before its last hypothesis is taken as one. A final
	// normally follows the audio by a tenth of a second or two; a recognizer
	// that has said nothing for this long is not going to.
	finalWaitMax = 3 * time.Second
	// transcriptHoldback is how many trailing characters of an agreed prefix
	// are kept off the append-only transcript, because the last word of a
	// hypothesis is the one a recognizer is most likely to change its mind on.
	transcriptHoldback = 2
	// earlyFinalMinVoicedMS is how much voice a recognition must have heard
	// before it may be ended early in a pause. The first of an utterance has
	// always heard more; this is for the ones after it, so that a click in the
	// pause does not buy another connection. A click is two or three frames; a
	// syllable is five and up, and at 160 this missed a real one — "行吗", said
	// as an afterthought on a call, whose answer then began only when the turn
	// closed.
	earlyFinalMinVoicedMS = 100
	// continuationQuietMS is how little voice a continuation may have heard
	// and still be known to hold no words: two frames, which is a click.
	continuationQuietMS = 40
)

// --- recognizer connection ---

func (e *Engine) asrOptions() provider.ASROptions {
	return provider.ASROptions{
		SampleRate: provider.PipelineRate,
		Language:   e.opts.Language,
		Interim:    true,
	}
}

// maybePredialASR starts the recognizer's handshake on the first voiced audio
// of a possible utterance, instead of when the detector is finally sure.
//
// The detector needs vad.min_speech_ms of voice before it will call something
// an utterance — two hundred milliseconds, more during playback. That used to
// be dead time followed by a handshake of about the same length, so the
// recognizer first heard anything roughly 400 ms into the caller's sentence and
// spent the rest of it catching up. For a one-word reply it never did: the
// utterance was over, and the turn closed, before a single hypothesis came
// back. Dialling on suspicion lets the two waits overlap.
//
// The cost of a false alarm is one connection that never carries audio and is
// closed a few seconds later, at most once per standbyMaxAge.
func (e *Engine) maybePredialASR() {
	if !e.opts.Cfg.Duplex.ASRPredial || e.deps.ASR == nil || e.muted || e.asrStandby != nil {
		return
	}
	// A run still waiting on its final transcript does not stop the next
	// utterance from being prepared for.
	if e.asrRun != nil && !e.asrRun.sendDone {
		return
	}
	if e.vad.CandidateMS() <= 0 {
		return
	}
	// A recognizer that refused the last dial of this kind is not asked again
	// on every voiced frame. An utterance that is actually confirmed still
	// dials at once, in openASR; this only stops the speculative ones.
	if time.Now().Before(e.standbyRetryAt) {
		return
	}
	e.asrStandby = dialLazyASR(e.ctx, e.deps.ASR, e.asrOptions())
}

func (e *Engine) closeStandby() {
	if e.asrStandby != nil {
		_ = e.asrStandby.Close()
		e.asrStandby = nil
	}
}

// expireStandby drops a connection dialled for something that turned out not
// to be speech.
func (e *Engine) expireStandby() {
	if e.asrStandby != nil && !e.asrStandby.usable(standbyMaxAge) {
		if e.asrStandby.didFail() {
			e.standbyRetryAt = time.Now().Add(standbyMaxAge)
			e.log.Debug("listen: the recognizer refused a connection dialled ahead of speech; "+
				"not dialling ahead again for a while", "for_ms", standbyMaxAge.Milliseconds())
		}
		e.closeStandby()
	}
}

// expireFinalWait gives up on a final transcript that is not coming.
func (e *Engine) expireFinalWait() {
	run := e.asrRun
	if run == nil || !run.sendDone || run.ended || time.Since(run.sendDoneAt) < finalWaitMax {
		return
	}
	if !run.finalSeen {
		e.log.Warn("asr: no final transcript arrived; closing the recognizer and going on without one",
			"run", run.id, "waited_ms", time.Since(run.sendDoneAt).Milliseconds())
	}
	run.ended = true
	e.asrRun = nil
	_ = run.stream.Close()
	e.concludeWithoutFinal(run)
	e.drainDeferred()
}

// concludeWithoutFinal ends an utterance whose recognizer finished — closed its
// stream, or went silent — without a final transcript.
//
// Recognizers do this: on a read error after the audio has all been sent, on
// their own timeout, on a dropped connection. The last hypothesis is then the
// best account of what was said that will ever exist, and the caller did say
// it. Treating it as the final is what keeps the turn from being stranded —
// before, a guess already being answered stayed unconfirmed until the caller
// spoke again, with its delegation and any function call held back the whole
// time, and with no guess in flight the utterance was simply never answered.
func (e *Engine) concludeWithoutFinal(run *asrRun) {
	if run.finalSeen || !run.sendDone {
		return
	}
	text := strings.TrimSpace(run.lastText)
	if text != "" {
		e.log.Warn("asr: the recognizer ended without a final transcript; taking its last hypothesis as one",
			"run", run.id, "text", text)
	}
	e.conclude(run, text)
}

// conclude ends an utterance on text, as its final transcript would have.
func (e *Engine) conclude(run *asrRun, text string) {
	run.finalSeen = true
	if e.hold != nil && e.judgeHold(text, true) {
		return
	}
	if e.ignoring() && !e.userOpen {
		// Not the caller's: no turn is made of it.
		e.log.Debug("focus: the utterance went by; no turn made of it", "chars", len([]rune(text)))
		e.uttVerdict = uttUnjudged
		return
	}
	if e.hold == nil && text != "" {
		e.emitInputTranscript(run, text, true)
	}
	e.onFinalTranscript(run, text)
}

func (e *Engine) openASR(startMS int64) {
	e.closeASR(true)
	if e.deps.ASR == nil {
		return
	}
	e.startRun(startMS, "")
}

// startRun opens a recognition and makes it the current one. itemID names the
// utterance it belongs to when that is one already being recognized.
func (e *Engine) startRun(startMS int64, itemID string) *asrRun {
	// Adopt the connection dialled when this utterance was only a suspicion,
	// if there is one; otherwise dial now. Either way this returns at once —
	// the loop goroutine no longer waits on a vendor's handshake — and the
	// audio written in the meantime is delivered, in order, when it completes.
	stream := e.asrStandby
	e.asrStandby = nil
	predialled := stream != nil && stream.usable(standbyMaxAge)
	if !predialled {
		if stream != nil {
			_ = stream.Close()
		}
		stream = dialLazyASR(e.ctx, e.deps.ASR, e.asrOptions())
	}

	e.asrSeq++
	if itemID == "" {
		itemID = fmt.Sprintf("user_%d", e.asrSeq)
	}
	run := &asrRun{
		id:       e.asrSeq,
		stream:   stream,
		itemID:   itemID,
		startMS:  startMS,
		endMS:    startMS,
		openedAt: time.Now(),
	}
	e.asrRun = run
	up, took := stream.ready()
	e.log.Debug("asr: stream opened",
		"provider", e.deps.ASR.Name(),
		"run", run.id,
		"item", run.itemID,
		"start_ms", startMS,
		"predialled", predialled,
		"connected", up,
		"connect_ms", took.Milliseconds())

	go func(id uint64, s provider.ASRStream) {
		for res := range s.Results() {
			select {
			case e.asrCh <- asrEvent{run: id, res: res}:
			case <-e.ctx.Done():
				return
			}
		}
		select {
		case e.asrCh <- asrEvent{run: id, done: true}:
		case <-e.ctx.Done():
		}
	}(run.id, stream)
	return run
}

// closeASR ends the current recognition. hard tears the stream down without
// waiting for a final result; the soft form asks for one.
func (e *Engine) closeASR(hard bool) {
	if hard && e.earlyRun != nil {
		_ = e.earlyRun.stream.Close()
		e.earlyRun = nil
	}
	run := e.asrRun
	if run == nil {
		return
	}
	if hard {
		e.asrRun = nil
		_ = run.stream.Close()
		return
	}
	if !run.sendDone {
		run.sendDone = true
		run.sendDoneAt = time.Now()
		if err := run.stream.CloseSend(); err != nil {
			e.log.Warn("asr close-send failed", "err", err)
		}
	}
	if run.ended {
		// The stream had already finished, so no final is coming to clear it.
		e.asrRun = nil
		e.concludeWithoutFinal(run)
	}
}

// awaitingFinal reports whether an utterance has closed and its final
// transcript is still on the way.
func (e *Engine) awaitingFinal() bool {
	return e.asrRun != nil && e.asrRun.sendDone && !e.asrRun.ended && !e.asrRun.finalSeen
}

func (e *Engine) onASREvent(ev asrEvent) {
	if early := e.earlyRun; early != nil && early.id == ev.run {
		e.onEarlyRunEvent(early, ev)
		return
	}
	run := e.asrRun
	if run == nil || run.id != ev.run {
		return // a stale stream's result; the utterance it belongs to is gone
	}
	if ev.done {
		run.ended = true
		// The recognizer finished the utterance without ever producing a word.
		// Same conclusion as an empty final, reached by a provider that closes
		// rather than sending one: there was no speech, so the assistant keeps
		// the floor instead of waiting for the hold to time out and yielding to
		// a noise.
		if e.hold != nil && strings.TrimSpace(run.lastText) == "" {
			e.log.Debug("listen: the recognizer ended with no words; keeping the floor",
				"held_ms", time.Since(e.hold.started).Milliseconds())
			e.hold = nil
		}
		if run.sendDone {
			e.asrRun = nil
			e.concludeWithoutFinal(run)
			e.drainDeferred()
		}
		return
	}
	if ev.res.Err != nil {
		if errors.Is(ev.res.Err, errASROpen) {
			metrics.Default().ProviderError("asr")
			e.log.Error("asr open failed", "err", ev.res.Err)
			e.deps.Emit(live.NewError("server_error", "asr_unavailable", ev.res.Err.Error(), ""))
			return
		}
		e.log.Warn("asr error", "err", ev.res.Err)
		e.deps.Emit(live.NewError("server_error", "asr_error", ev.res.Err.Error(), ""))
		return
	}

	raw := strings.TrimSpace(ev.res.Text)
	if raw == "" && !ev.res.Final {
		return
	}
	// A continuation hears only what followed the pause its predecessor was
	// ended in; the utterance is both.
	run.raw = raw
	text := joinTranscript(run.prefix, raw)
	if ev.res.Final {
		run.finalSeen = true
		e.log.Debug("asr: final",
			"run", run.id,
			"text", text,
			"elapsed_ms", time.Since(run.openedAt).Milliseconds())
	} else if text != run.lastText {
		e.log.Debug("asr: partial", "run", run.id, "text", text)
	}
	// Before anything else: if an interruption is being held pending the
	// transcript, this is the evidence it was waiting for.
	if e.hold != nil && e.judgeHold(text, ev.res.Final) {
		e.noteWords(run, text)
		return
	}
	// While the hold is still undecided the utterance may yet be swallowed
	// whole, and a transcript cannot be taken back, so nothing is shown for
	// it. The next event after the hold resolves catches the client up.
	if e.hold == nil && !e.ignoring() {
		e.emitInputTranscript(run, text, ev.res.Final)
	}

	e.noteWords(run, text)
	if ev.res.Final {
		e.onFinalTranscript(run, text)
		e.drainDeferred()
		return
	}

	// A guess already in flight was made on words that have since changed.
	// Finding that out now, rather than when the final transcript lands, is
	// the difference between restarting the backend and restarting it late.
	if spec := e.liveSpeculation(); spec != nil && !sameWords(text, spec.Transcript) {
		e.abandonSpeculation("the transcript moved on")
	}
	// The utterance is over and this is the newest account of it: nothing
	// better is coming before the final, so start on it now.
	if run.sendDone {
		e.speculateAtClose()
	}
	// Otherwise no speculation check here. Whether to guess mid-utterance
	// depends on whether the speaker has paused, which is an acoustic question,
	// so it is answered on the audio path in maybeSpeculate.
}

// --- a final asked for early ---

// maybeEarlyFinal asks the recognizer for its final word on the utterance so
// far, a little way into a pause, and carries on listening on a new connection.
//
// Starting the answer during the pause, on the words so far, is the largest
// head start a cascade has — and it depends on those words being there. A
// streaming recognizer's hypotheses trail the audio by a few hundred
// milliseconds; measured against the one in use, the last partial at the close
// of a 280 ms pause was still short of the sentence on most turns, so the guess
// made there was wrong, was thrown away when the final arrived, and the answer
// began from nothing a round trip after the turn had closed. The pause was
// waited through in full and bought nothing.
//
// What a recognizer does do promptly is finish: told the audio has ended, it
// returns the final in about a tenth of a second. So the audio is ended here,
// EarlyFinalMS into the pause, and the answer is begun on that final while the
// rest of the pause runs out. The turn is still not taken until the detector
// says so, and nothing of the answer is heard before then.
//
// Should the caller carry on, the connection that just finished cannot hear
// them, so a continuation is opened at the same moment and given every frame
// from here. Its words are joined to what was settled, and the guess is
// dropped as soon as there are any — the same rule as for any other guess.
func (e *Engine) maybeEarlyFinal() {
	d := e.opts.Cfg.Duplex
	if d.EarlyFinalMS <= 0 || !e.opts.Speculative || e.deps.ASR == nil {
		return
	}
	// Not held back while an interruption is being weighed. That weighing
	// waits on words, and on a real call it waited for a hypothesis that
	// trailed the audio by 340 ms: the caller said three syllables over the
	// assistant, stopped, and the turn had all but closed before anything
	// arrived to say they had spoken. The final is those words, sooner.
	if !e.userOpen || e.muted || e.earlyRun != nil {
		return
	}
	run := e.asrRun
	if run == nil || run.sendDone || run.ended {
		return
	}
	if e.vad.TrailingSilenceMS() < float64(d.EarlyFinalMS) {
		return
	}
	// Only a recognition that has heard speech is worth a final. The one that
	// opened the utterance always has; a continuation has only if the caller
	// did carry on.
	if e.vad.ActiveMS()-run.activeAtOpen < earlyFinalMinVoicedMS {
		return
	}

	run.sendDone = true
	run.sendDoneAt = time.Now()
	if err := run.stream.CloseSend(); err != nil {
		e.log.Warn("asr close-send failed", "err", err)
	}
	e.earlyRun = run
	e.earlyFinals++

	next := e.startRun(run.startMS, run.itemID)
	next.continuation = true
	next.endMS = run.endMS
	next.activeAtOpen = e.vad.ActiveMS()
	// Until the final lands, the best account of what came before is the last
	// hypothesis; and to everything that reads this run, the utterance so far
	// is still the utterance so far.
	next.prefix = run.lastText
	next.lastText = run.lastText
	next.changedAt, next.changedAtMS, next.activeAtChange = run.changedAt, run.changedAtMS, run.activeAtChange
	next.prevHyp, next.committed, next.shown, next.held = run.prevHyp, run.committed, run.shown, run.held
	e.log.Debug("asr: asking for a final on the words so far; a continuation is listening",
		"run", run.id, "continuation", next.id,
		"pause_ms", int(e.vad.TrailingSilenceMS()), "so_far", run.lastText)
}

// onEarlyRunEvent takes what a recognition ended early still has to say, which
// is its final or the news that there will not be one.
func (e *Engine) onEarlyRunEvent(run *asrRun, ev asrEvent) {
	text := run.lastText
	switch {
	case ev.done:
		e.log.Debug("asr: a recognition ended early closed without a final; taking its last hypothesis",
			"run", run.id, "text", text)
	case ev.res.Err != nil:
		e.log.Warn("asr error on a recognition ended early; taking its last hypothesis",
			"run", run.id, "err", ev.res.Err)
	case ev.res.Final:
		text = joinTranscript(run.prefix, strings.TrimSpace(ev.res.Text))
	default:
		// A hypothesis for audio already sent. The final is right behind it.
		return
	}
	e.earlyRun = nil
	run.finalSeen = true
	_ = run.stream.Close()
	e.onEarlyFinal(run, text)
}

// expireEarlyFinal gives up on an early final that is not coming.
func (e *Engine) expireEarlyFinal() {
	run := e.earlyRun
	if run == nil || time.Since(run.sendDoneAt) < finalWaitMax {
		return
	}
	e.log.Warn("asr: no final arrived for a recognition ended early; going on with its last hypothesis",
		"run", run.id, "waited_ms", time.Since(run.sendDoneAt).Milliseconds())
	e.earlyRun = nil
	run.finalSeen = true
	_ = run.stream.Close()
	e.onEarlyFinal(run, run.lastText)
}

// onEarlyFinal settles the words up to the pause and, if the pause is still
// running, begins the answer on them.
func (e *Engine) onEarlyFinal(run *asrRun, text string) {
	cur := e.asrRun
	if cur == nil || !cur.continuation || cur.finalSeen {
		return // the utterance has been concluded without it
	}
	e.log.Debug("asr: final for the words so far",
		"run", run.id, "text", text,
		"took_ms", time.Since(run.sendDoneAt).Milliseconds(),
		"pause_ms", int(e.vad.TrailingSilenceMS()))
	cur.prefix = text
	full := joinTranscript(text, cur.raw)
	// An interruption being weighed is weighed on this, as on any hypothesis:
	// words that are not an acknowledgement give the floor up now. Not as a
	// final, though — the caller may not have finished — and not when there
	// are no words: silence so far settles nothing while the utterance is
	// still open.
	if e.hold != nil && full != "" && e.judgeHold(full, false) {
		e.noteWords(cur, full)
		return
	}
	if e.hold == nil {
		e.emitInputTranscript(cur, full, false)
	}
	e.noteWords(cur, full)

	if spec := e.liveSpeculation(); spec != nil && !sameWords(spec.Transcript, full) {
		e.abandonSpeculation("the final for the words so far is not what was guessed")
	}
	if cur.sendDone {
		// The turn closed in the meantime, on a continuation that had heard
		// something and owes a final of its own.
		e.speculateAtClose()
		return
	}
	if !e.userOpen || cur.raw != "" || e.vad.VoicedRunMS() >= resumeVoicedMS {
		return // they are talking again; what they say decides the rest
	}
	e.speculateOnFinal(full)
}

// speculateOnFinal begins the answer on words the recognizer has finished
// with, in a pause that has not yet ended the turn.
//
// acknowledging reports whether text is the caller acknowledging the assistant
// rather than taking a turn: a listed murmur, said while the assistant has the
// floor.
//
// The list (user_backchannel_phrases) is about the floor: "嗯" over an answer
// does not stop it. It was also keeping an answer from being begun early on
// any listed word, wherever it was said — and "嗯", "对", "好" said into a
// silence are not murmurs, they are the caller answering a question. Such a
// turn is answered either way; the only thing the list did to it was take away
// its head start, which made the shortest turns of a call its slowest (four of
// the five turns over target on one call were answers to "嗯。"). The longer the
// list, the more turns that was.
//
// A hold counts as the assistant having the floor: it exists because the
// caller began over the answer, and the answer may have ended in the moment
// between then and the words arriving. On a real call "对对" said over the
// last word of an answer was begun on as a turn, held, and said back to the
// caller 2.7 s later — "嗯，您再试一下" to someone agreeing — and counted as
// the slowest turn of the call.
func (e *Engine) acknowledging(text string) bool {
	return e.bcSet.Matches(text) && (e.hold != nil || e.assistantHasFloor())
}

// The gates are maybeSpeculate's, less the ones that exist because a partial
// may not be the whole of what was said: this is.
func (e *Engine) speculateOnFinal(text string) {
	if !e.opts.Speculative || e.muted || e.hold != nil || e.ignoring() || text == "" || e.liveSpeculation() != nil {
		return
	}
	if e.acknowledging(text) {
		e.specWhy = "the words so far are an acknowledgement, not a question"
		return
	}
	if e.opts.Cfg.Duplex.IgnoreHesitations && endpoint.Hesitation(text) {
		e.specWhy = "the words so far are only a hesitation"
		return
	}
	class := e.classify(withoutForcedStop(text))
	if class == endpoint.Incomplete && !e.opts.Cfg.Duplex.SpeculateWhilePatient {
		e.specWhy = "the words so far read as an unfinished thought"
		return
	}
	// No floor on length here. speculative_min_chars keeps a guess from being
	// made on a fragment of a hypothesis — "今天" on its way to being a
	// sentence. This is not a hypothesis: the caller has stopped, the
	// recognizer has been told so and has said what it heard, and if they do
	// carry on the connection listening for that will say so and the guess is
	// dropped. And the short turns are the common ones: on two calls, every
	// one of the twelve turns answered without a head start was a "嗯。" or a
	// "嗯嗯。", and they were four of the five slowest of the second call.
	e.startSpeculativeTurn(text)
}

// closeUtteranceASR ends recognition for an utterance the detector has closed.
//
// Ordinarily that is asking for the final and waiting for it. When the final
// was asked for early and the caller said nothing after, it is already here —
// or already asked for — and the continuation that was listening in case they
// carried on has only a pause to report.
func (e *Engine) closeUtteranceASR(d audio.Decision) {
	run := e.asrRun
	if run == nil || !run.continuation || run.sendDone || d.ActiveMS-run.activeAtOpen > continuationQuietMS {
		e.closeASR(false)
		return
	}
	_ = run.stream.Close()
	if prev := e.earlyRun; prev != nil {
		// The pause ended the turn before the early final arrived. It is now
		// simply this utterance's final, and is waited for as one.
		e.earlyRun = nil
		prev.endMS = run.endMS
		e.asrRun = prev
		return
	}
	run.sendDone = true
	run.sendDoneAt = time.Now()
	run.ended = true
	e.asrRun = nil
	e.log.Debug("asr: the turn closed on words already final", "run", run.id, "text", run.prefix)
	e.conclude(run, run.prefix)
	e.drainDeferred()
}

// pauseWords is the utterance so far as it should be read for whether the
// speaker has finished.
//
// A recognizer told that the audio has ended puts a full stop on whatever it
// was given. On the final for a whole turn that is its opinion and it had the
// audio; on a final asked for in a pause it is punctuation by default, and it
// was being read as the recognizer saying the sentence was over — on a real
// call "就是。", "呃。" and "我想问你一下啊。" were each taken as a finished turn
// and answered. A question mark is still evidence; the full stop is not.
func pauseWords(run *asrRun) string {
	if run.continuation && run.raw == "" {
		return withoutForcedStop(run.lastText)
	}
	return run.lastText
}

func withoutForcedStop(text string) string {
	return strings.TrimRight(strings.TrimSpace(text), "。.")
}

// joinTranscript puts what earlier recognitions of an utterance settled on in
// front of what the current one has heard.
//
// A recognizer closes whatever it is given with a full stop. One that was
// given the first half of a sentence has closed a clause, not a sentence.
func joinTranscript(prefix, text string) string {
	if prefix == "" {
		return text
	}
	if text == "" {
		return prefix
	}
	r := []rune(prefix)
	switch r[len(r)-1] {
	case '。':
		r[len(r)-1] = '，'
	case '.':
		r[len(r)-1] = ','
	}
	sep := ""
	if last, first := r[len(r)-1], []rune(text)[0]; last < 0x80 && first < 0x80 && last != ' ' {
		sep = " "
	}
	return string(r) + sep + text
}

// --- the caller's transcript ---

// emitInputTranscript reports a hypothesis two ways.
//
// golive.input_transcript.updated carries the whole of it, every time it
// changes. It is the caption that keeps up: a recognizer that swaps a word it
// emitted half a second ago simply sends the row again.
//
// session.input_transcript.delta is the gpt-live-1 event, and it is
// append-only — a client concatenates fragments and has no way to be told one
// was wrong. A streaming recognizer cannot honour that with its raw output, so
// what goes out here is only what it has stopped changing: the prefix this
// hypothesis shares with the one before it, less the last couple of
// characters, which are the likeliest to move. The remainder follows when the
// utterance is final. Should the final transcript contradict something already
// sent — rare, and gpt-live-1 itself says transcripts can be wrong — the
// fragment sent is what follows the point of agreement, and the updated event
// carries the truth.
func (e *Engine) emitInputTranscript(run *asrRun, text string, final bool) {
	if text != run.shown || final {
		if text != "" || run.shown != "" {
			e.deps.Emit(live.InputTranscriptEvent{
				Envelope: live.Envelope{Type: live.ExtInputTranscript},
				ItemID:   run.itemID,
				Text:     text,
				StartMS:  run.startMS,
				EndMS:    run.endMS,
				Final:    final,
			})
		}
		run.shown = text
	}

	var delta string
	if final {
		delta = strings.TrimPrefix(text, commonPrefix(run.committed, text))
		run.committed += delta
	} else {
		stable := dropLastRunes(commonPrefix(run.prevHyp, text), transcriptHoldback)
		if len(stable) > len(run.committed) && strings.HasPrefix(stable, run.committed) {
			delta = stable[len(run.committed):]
			run.committed = stable
		}
		run.prevHyp = text
	}
	if delta == "" && (!final || run.committed == "") {
		return
	}
	e.deps.Emit(live.TranscriptDelta{
		Envelope: live.Envelope{Type: live.ServerInputTranscriptDelta},
		Delta:    delta,
		StartMS:  run.startMS,
		EndMS:    run.endMS,
		ItemID:   run.itemID,
		Final:    final,
	})
}

// commonPrefix returns the longest prefix a and b share, cut on a character
// boundary.
func commonPrefix(a, b string) string {
	ar, br := []rune(a), []rune(b)
	n := 0
	for n < len(ar) && n < len(br) && ar[n] == br[n] {
		n++
	}
	return string(ar[:n])
}

func dropLastRunes(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return ""
	}
	return string(r[:len(r)-n])
}

// --- reading a pause ---

// classify is endpoint.Classify, memoized on the text and switched off with
// duplex.semantic_endpointing.
func (e *Engine) classify(text string) endpoint.Class {
	if !e.opts.Cfg.Duplex.SemanticEndpointing {
		return endpoint.Neutral
	}
	if text != e.classText {
		e.classText = text
		e.classVal = endpoint.Classify(text)
	}
	return e.classVal
}

// applyPatience gives a speaker who has stopped mid-thought longer before the
// turn is taken from them.
//
// vad.min_silence_ms is one number for every pause, and the right number for
// "明天会下雨吗" is too short for "我想问一下那个". Being cut off there is the
// most disliked thing a voice agent does, and it is also slow: the half
// sentence is answered, the caller talks over the answer, and the turn starts
// again from nothing. While the words so far end on something nobody stops on,
// the detector is told to wait duplex.patient_silence_ms instead.
//
// It only ever lengthens the wait. A caller who really had finished on "然后"
// gets an answer a few hundred milliseconds later than otherwise, which is the
// whole of the downside.
func (e *Engine) applyPatience() {
	d := e.opts.Cfg.Duplex
	if !d.SemanticEndpointing || d.PatientSilenceMS <= e.opts.Cfg.VAD.MinSilenceMS {
		return
	}
	run := e.asrRun
	if !e.userOpen || run == nil {
		return
	}
	want := 0
	if run.lastText != "" && e.classify(pauseWords(run)) == endpoint.Incomplete {
		want = d.PatientSilenceMS
	}
	if want == e.patience {
		return
	}
	e.patience = want
	e.vad.SetMinSilenceMS(want)
	if want > 0 {
		e.log.Debug("listen: the caller seems to be mid-thought; waiting longer before taking the turn",
			"text", run.lastText, "silence_ms", want)
	}
}

// noteWords records a hypothesis, and when it last changed.
func (e *Engine) noteWords(run *asrRun, text string) {
	if text != run.lastText || run.changedAt.IsZero() {
		run.changedAt = time.Now()
		run.changedAtMS = e.vad.ClockMS()
		run.activeAtChange = e.vad.ActiveMS()
	}
	run.lastText = text
}

// maybeSettle ends a turn the detector cannot.
//
// The detector closes an utterance on silence, and silence is a property of
// the line as much as of the speaker. A caller in a room, in a car, or on a
// carrier that fills pauses with noise can finish a question and never produce
// the 380 quiet milliseconds that say so: a real call held "今天天气怎么样啊？"
// open for 3.3 seconds that way, with the transcript complete and unchanged
// the whole time.
//
// The recognizer is the witness that does not depend on the line. Speech makes
// words and noise does not, so a hypothesis that has stood still for
// duplex.settled_close_ms, through sound that was mostly not voiced, is a
// finished turn. Both halves are needed: a transcript also stands still when
// the recognizer has fallen behind someone who is still talking, and what
// tells that apart is that they are, audibly, still talking.
func (e *Engine) maybeSettle() {
	d := e.opts.Cfg.Duplex
	if d.SettledCloseMS <= 0 || !e.userOpen || e.muted || e.hold != nil {
		return
	}
	// "No new words" is being judged on hypotheses that trail the audio. With
	// a final for the words so far asked for and a tenth of a second away, it
	// is about to be judged on the words themselves: on a real call the turn
	// was taken 200 ms after the caller's last sound, five milliseconds before
	// that final arrived with half a sentence the hypotheses had not reached.
	if e.earlyRun != nil {
		return
	}
	run := e.asrRun
	if run == nil || run.sendDone || run.changedAt.IsZero() || strings.TrimSpace(run.lastText) == "" {
		return
	}
	wait := d.SettledCloseMS
	if e.patience > wait {
		// Words that end on something nobody stops on get the longer wait
		// here too.
		wait = e.patience
	}
	// Timed on the audio, not the wall: what matters is how much the caller
	// has been heard to say nothing new through, and audio that has not
	// arrived yet is not evidence of anything.
	quiet := e.vad.ClockMS() - run.changedAtMS
	if quiet < float64(wait) {
		return
	}
	voiced := e.vad.ActiveMS() - run.activeAtChange
	if voiced*2 > quiet {
		return
	}
	e.log.Info("listen: no new words and the line never went quiet; taking the turn",
		"words_unchanged_ms", int(quiet),
		"voiced_since_ms", int(voiced),
		"text", run.lastText,
		"noise_floor_db", round1(e.vad.NoiseFloorDB()))
	stop := e.vad.Stop()
	if stop.Kind != audio.DecisionStopped {
		return
	}
	// The caller's last word was at least this long ago. The detector's own
	// figure is how long the line has been quiet, which is the thing that
	// could not be relied on.
	stop.SilenceMS = quiet
	e.handleDecision(stop)
}

// --- answering early ---

// liveSpeculation returns the turn currently running on a guess, if any.
func (e *Engine) liveSpeculation() *Turn {
	if e.specTurn != nil && e.tracker.IsCurrent(e.specTurn) && !e.specTurn.Committed() {
		return e.specTurn
	}
	return nil
}

// maybeSpeculate decides whether to start the backend before the VAD has
// finished waiting out the end of the turn.
//
// It runs on the audio path, once per frame, because the trigger is acoustic:
// the speaker has gone quiet for a while but not yet long enough for
// vad.min_silence_ms to close the utterance. That window — the difference
// between speculative_stable_ms and min_silence_ms, plus however long the final
// transcript takes to arrive — is the head start speculation buys, and it is
// free whenever the guess holds.
//
// The earlier version triggered on a transcript that had stopped changing,
// which sounds equivalent and is not. A recognizer running a few hundred
// milliseconds behind the speaker also stops changing, so the watch fired
// mid-utterance on a prefix, and almost every speculative turn was immediately
// revised: the guess was not merely wrong, it was wrong by construction. Tying
// the clock to a pause the microphone can actually hear fixes that, and
// Unsettle restarts it the instant the speaker resumes.
//
// Two things were still missing, and both made a wrong guess cost more than it
// should. A guess made on a pause in the middle of a sentence was left running
// when the speaker carried on — nothing noticed until the final transcript
// disagreed with it, by which time it might be talking over them — and the
// watch, having fired once, could not fire again, so the pause that really did
// end the turn got no head start. Now a resumption abandons the guess at once
// and re-arms the watch.
func (e *Engine) maybeSpeculate() {
	if !e.opts.Speculative || e.muted {
		return
	}
	if (e.hold != nil && e.hold.kept) || e.ignoring() {
		// The labels said this utterance is not the caller's: nothing in it
		// is a turn to prepare for.
		return
	}
	// Sound resuming after the pause is not, by itself, the speaker carrying
	// on. It used to be taken for that and the guess abandoned on the spot,
	// which is right in a quiet room and wrong on a line that is never quiet:
	// a real call asked one question, went silent, and had its guess thrown
	// away and remade eight times in three seconds by the noise on the line,
	// while the answer it kept discarding sat finished. What says the speaker
	// carried on is new words — onASREvent abandons the guess the moment the
	// transcript moves — and nothing of the guess is audible until the turn is
	// confirmed, so keeping it costs nothing if they did.
	if spec := e.liveSpeculation(); spec != nil && e.userOpen &&
		e.vad.VoicedRunMS() >= resumeVoicedMS && !e.resumeNoted {
		e.resumeNoted = true
		e.log.Debug("listen: sound resumed after the pause; keeping the guess until the words change",
			"turn", spec.ID)
	}

	// One guess at a time. A guess can now be begun off this path, on a final
	// asked for early in the pause, and the watch below knows nothing of it.
	if e.liveSpeculation() != nil {
		return
	}
	// A final for the words so far has been asked for and is a round trip
	// away. Until it lands, the words in hand are the hypothesis that was
	// trailing the audio when it was asked for — on the real recognizer,
	// "你好，今天天气" for "你好，今天天气怎么样？" — and a guess made on them is
	// made to be thrown away fifty milliseconds later.
	if e.earlyRun != nil {
		return
	}
	run := e.asrRun
	if run == nil || run.lastText == "" {
		e.specWhy = "no partial transcript yet"
		return
	}
	if run.sendDone || e.watch.Fired() {
		return
	}
	// Tested against the partial itself, not against whether a floor hold is
	// pending.
	//
	// The two are different questions — the hold decides whether to yield the
	// floor, speculation decides whether to start the backend early — and
	// keying this on the hold conflated them. In practice the hold releases on
	// the first partial that cannot become an acknowledgement, so by the time
	// the text is a question the hold is already gone and the old condition
	// rarely bit; the case it did cover is this one, stated directly. It also
	// catches an acknowledgement long enough to clear speculative_min_chars
	// ("好的好的"), which the old form missed whenever no hold existed.
	if e.acknowledging(run.lastText) {
		e.specWhy = "the partial so far is an acknowledgement, not a question"
		return
	}

	d := e.opts.Cfg.Duplex
	class := e.classify(pauseWords(run))
	if class == endpoint.Incomplete {
		// "我想问一下那个" is not a question yet. Guessing on it would start an
		// answer to half a sentence; the turn will be taken at the end of the
		// longer wait applyPatience has asked for, if the rest never comes.
		e.watch.Unsettle()
		e.specWhy = "the words so far read as an unfinished thought"
		return
	}
	// The length floor exists to keep a fragment from being answered. Words
	// that read as a whole turn — "多少钱", "可以" — are not fragments however
	// short they are.
	if n := len([]rune(run.lastText)); n < d.SpeculativeMinChars && class != endpoint.Complete {
		e.specWhy = fmt.Sprintf("partial is %d characters; speculative_min_chars is %d",
			n, d.SpeculativeMinChars)
		return
	}
	if e.vad.TrailingSilenceMS() <= 0 {
		// Still talking. Anything the recognizer has emitted so far is a
		// prefix, however settled it looks.
		e.watch.Unsettle()
		e.specWhy = "the speaker never paused long enough before the turn closed"
		return
	}

	// Quiet for the window, and the transcript still for the settle time. A
	// hypothesis landing mid-pause holds the guess back only until it has
	// settled, not for the whole window again — see StabilityWatch.ObserveFor.
	//
	// The window is shorter when the words read as finished. Those are the
	// turns where the rest of the silence threshold is pure waiting, and a
	// guess that proves early is abandoned the moment the speaker resumes.
	windowMS := d.SpeculativeStableMS
	if class == endpoint.Complete && d.SpeculativeEagerMS > 0 && d.SpeculativeEagerMS < windowMS {
		windowMS = d.SpeculativeEagerMS
	}
	settleMS := d.SpeculativeSettleMS
	if settleMS <= 0 || settleMS > windowMS {
		settleMS = windowMS
	}
	e.specWhy = fmt.Sprintf(
		"the pause was never quiet for %d ms with the transcript still for %d ms before the %d ms silence threshold closed the turn",
		windowMS, settleMS, e.vad.MinSilenceMS())
	if e.watch.ObserveFor(run.lastText, time.Now(),
		time.Duration(windowMS)*time.Millisecond, time.Duration(settleMS)*time.Millisecond) {
		e.startSpeculativeTurn(run.lastText)
	}
}

// speculateAtClose starts the backend the moment an utterance closes, on the
// last hypothesis, without waiting for the recognizer's final word on it.
//
// By then the caller has been silent for the whole silence threshold, so the
// recognizer has had that long to catch up and the last partial is, almost
// always, the final transcript minus its punctuation. Waiting for the final
// before starting cost its full round trip on every turn that had not already
// speculated — every short reply, every turn where the recognizer was a little
// slow. When the final agrees the turn is simply confirmed; when it does not,
// the turn is revised exactly as a mid-pause guess would have been.
func (e *Engine) speculateAtClose() {
	if !e.opts.Speculative || e.muted || e.hold != nil || e.ignoring() {
		return
	}
	run := e.asrRun
	if run == nil || !run.sendDone || run.ended || e.liveSpeculation() != nil {
		return
	}
	if run.lastText == "" || e.acknowledging(run.lastText) {
		return
	}
	if e.opts.Cfg.Duplex.IgnoreHesitations && endpoint.Hesitation(run.lastText) {
		return
	}
	e.startSpeculativeTurn(run.lastText)
}

func (e *Engine) startSpeculativeTurn(text string) {
	if e.hangingUp() {
		// Nothing after the farewell is answered, and a turn begun here
		// would become the current one and abort the farewell's speech
		// (16:38 on 10-21: the silence farewell cut at 1.0 s by the
		// caller's own reply to it).
		return
	}
	turn := e.tracker.Begin(text, true)
	e.specTurn = turn
	e.resumeNoted = false
	e.log.Debug("think: speculating on a stable partial",
		"turn", turn.ID, "text", text, "utterance_open", e.userOpen)
	e.beginGeneration(turn, "speculative", true)
}

// abandonSpeculation gives up a guess whose premise has gone, and makes room
// for another.
//
// Nothing the turn would have committed the service to has been sent — see
// Turn.emitOrHold — so abandoning it is silent apart from any audio that had
// already started, which is cut and accounted for like any other interruption.
func (e *Engine) abandonSpeculation(reason string) {
	spec := e.specTurn
	e.specTurn = nil
	if spec == nil {
		return
	}
	e.log.Debug("think: abandoning the speculative turn",
		"turn", spec.ID, "reason", reason, "guess", spec.Transcript)
	spec.kill()
	e.forgetDelegation(delegationIDOf(spec))
	e.cancelBackend()
	if e.tracker.IsCurrent(spec) {
		if spec.Opened != "" {
			// The opener was said at the base pause and the caller went on:
			// it finishes as an 嗯 of listening (one word), the rest of the
			// guess is dropped without a sound, and the fragment that follows
			// within 1.5 s gets no second opener.
			if pipe := e.speech.Load(); pipe != nil {
				pipe.Abort()
			}
			e.player.FinishOpenerAsBackchannel(spec.ID)
			e.skipOpenerUntil = time.Now().Add(openerSkipAfterBackchannel)
			e.log.Info("speak: the opener became a backchannel; the caller went on", "turn", spec.ID, "opener_ms", spec.OpenedMS, "reason", reason)
		} else {
			e.abortSpeech()
		}
		e.tracker.Abandon()
	}
	if e.delegatedTurn == spec {
		e.delegatedTurn = nil
	}
	e.watch.Rearm()
	e.specAbandoned++
	metrics.Default().SpeculationAbandoned()
}

func (e *Engine) onFinalTranscript(run *asrRun, text string) {
	if text == "" {
		// Nothing was said after all. Drop any speculation so its audio never
		// reaches the user.
		e.abandonSpeculation("the recognizer found no words")
		return
	}
	if e.opts.Cfg.Duplex.IgnoreHesitations && endpoint.Hesitation(text) {
		// "呃。" and then nothing: the caller drew breath to speak and has not
		// yet. There is no question in it to answer, and answering it talks
		// over the one that is coming.
		e.log.Debug("listen: only a hesitation; leaving the turn with the caller", "text", text)
		e.abandonSpeculation("the caller only hesitated")
		return
	}

	// The caller's words, final: the keyword rules' moment.
	e.checkHangupRules(text)
	if e.tools != nil && e.tools.endCall != nil && !e.tools.endCall.byModel {
		// A rule is ending the call; the farewell is being said and nothing
		// else should be answered.
		e.abandonSpeculation("the call is ending")
		return
	}
	if spec := e.liveSpeculation(); spec != nil {
		if sameWords(spec.Transcript, text) {
			// The guess held: the audio already flowing is correct, and the
			// final transcript costs nothing. This is the whole point of
			// speculating.
			e.sayOpener(spec)
			e.tracker.Commit(spec.ID, spec.Revision)
			e.specTurn = nil
			e.commitTurn(spec)
			e.log.Debug("think: speculation held; keeping the audio already in flight",
				"turn", spec.ID, "text", text)
			e.specHeld++
			metrics.Default().SpeculationHeld()
			return
		}
		e.log.Debug("think: speculation missed; regenerating",
			"turn", spec.ID, "guess", spec.Transcript, "final", text)
		spec.kill()
		e.forgetDelegation(delegationIDOf(spec))
		e.cancelBackend()
		if spec.Opened != "" {
			// The opener said at the pause stands; only the guessed answer
			// behind it is dropped, and the revised one follows the opener.
			// No Interrupt (it would cut the word) and no grace (the
			// revised answer shares the turn id and must not be dropped at
			// the opener's mark): the guessed content is cancelled at the
			// pipe, what was queued of it is stale once Revise bumps the
			// generation, and the opener plays out.
			if pipe := e.speech.Load(); pipe != nil {
				pipe.Abort()
			}
		} else {
			e.abortSpeech()
		}
		revised := e.tracker.Revise(spec.ID, text, true)
		e.specTurn = nil
		e.specAbandoned++
		metrics.Default().SpeculationAbandoned()
		if revised != nil {
			if spec.Opened != "" {
				// Said already, at the pause; the revised answer follows it,
				// and the opener's audio stamps come with it.
				revised.InheritOpener(spec)
			}
			e.sayOpener(revised)
			e.commitTurn(revised)
			e.beginGeneration(revised, "revision", true)
			return
		}
	}

	e.specTurn = nil
	if e.hangingUp() {
		return
	}
	turn := e.tracker.Begin(text, false)
	turn.MarkTranscriptFinal()
	e.sayOpener(turn)
	e.commitTurn(turn)
	// A turn that waited for the final transcript pays the backend's whole
	// time-to-first-token where a speculative one would have spent it during
	// the pause. When speculation is switched on and still never fires, that is
	// worth one line rather than a silent loss — a real call showed every
	// delegation arriving as final_transcript with nothing to say why.
	if e.opts.Speculative && e.specWhy != "" {
		e.log.Info("think: answered without speculating",
			"turn", turn.ID, "reason", e.specWhy, "transcript", text)
	}
	e.specWhy = ""
	e.beginGeneration(turn, "final_transcript", true)
}

// commitTurn confirms a turn: whatever was being held for it goes out, and if
// it carries the caller's words it becomes the turn the conversation is owed an
// answer to.
func (e *Engine) commitTurn(turn *Turn) {
	turn.commit(e.deps.Emit)
	if turn.Transcript == "" {
		return
	}
	if e.tools != nil {
		e.tools.callerTurns++
	}
	// Something the caller said earlier and was never answered — they carried
	// on after a pause that closed the turn, or asked again before the answer
	// came — is still part of what they are asking now. It goes into the
	// conversation here, before this turn takes its place as the one owed an
	// answer; left to beginGeneration it was already too late, because by then
	// "the last confirmed turn" was this one.
	if prev := e.lastCommitted; prev != nil && prev.ID != turn.ID {
		e.ensureUserRecorded(prev)
	}
	e.lastCommitted = turn
}

// writeASR is kept beside the code that opens the stream it writes to.
func (e *Engine) writeASR(samples []float32) {
	if e.asrRun == nil || len(samples) == 0 || e.asrRun.sendDone {
		return
	}
	if err := e.asrRun.stream.Write(audio.EncodePCM16(samples)); err != nil {
		e.log.Warn("asr write failed", "err", err)
	}
}

// openerMaxMS caps an opener's audio. The answer's own first word cannot
// start before the opener ends, so from the caller's last sound the content
// figure is at least the pause plus this: at 650 it was 1030 on every turn
// that drew a long opener (the 10:46 call of 10-26: content p95 1069 from
// the last sound, the opener binding on 14 of 18 turns, p95 of its length
// 650). At 540 the floor is 920.
const openerMaxMS = 540

// openerTailMS is how much of the rendered pause after the opener's word is
// kept: the gap the caller hears between the opener and the answer.
const openerTailMS = 80

// openerLeadMS is how much of the rendered quiet before the opener's word is
// kept, so its onset is not clipped.
const openerLeadMS = 20

// segmentLeadMS is the same for the answer's own first segment.
const segmentLeadMS = 20

// openerContinuationMS: a continuation at least this long after a
// backchanneled opener is a real stretch of speech and opens as usual; a
// shorter one is a fragment and gets no second opener.
const openerContinuationMS = 1000

// sayOpener opens the answer to a turn just judged over with a word from the
// phrase cache, before the chat model's first clause is in — if the answer
// has produced no audio yet, and nothing is being said. The opener is the
// turn's first audio; the pipe says the model's own opener once if it is the
// same words (Turn.Opened). Loop goroutine, before the turn is committed, so
// the pipe's first segment — held until the commit — queues behind it.
func (e *Engine) sayOpener(turn *Turn) {
	d := e.opts.Cfg.Duplex
	if !d.Opener || len(d.OpenerPhrases) == 0 || turn == nil || turn.Scripted || e.hangingUp() {
		return
	}
	if turn.Opened != "" || !turn.Timings().FirstAudio.IsZero() {
		return
	}
	if !e.player.FreeForOpener() {
		e.log.Debug("speak: no opener; the floor is busy", "turn", turn.ID, "active", e.player.Active())
		return
	}
	fragment := time.Now().Before(e.skipOpenerUntil) && e.lastActiveMS < openerContinuationMS
	e.skipOpenerUntil = time.Time{}
	phrase := turn.OpenerPlanned
	if fragment {
		// The opener before this one turned out to be mid-sentence and
		// became a backchannel moments ago, and what followed was a short
		// fragment: a second 嗯 on it is the tic the owner heard (23:36 on
		// 10-25). By default the answer begins when it is ready — which put
		// the first audio of those turns at the content's ~1000 ms (14 such
		// turns on the 10:00 simulated call of 10-26, 6 of 20 on the 21:31
		// call of 10-27). With opener_on_fragments the answer opens with a
		// phrase of another kind, so the pair is 嗯，… 好的，… and not 嗯，嗯.
		phrase = ""
		if d.OpenerOnFragments {
			phrase = e.planFragmentOpener(turn)
		}
		if phrase == "" {
			e.log.Debug("speak: no opener on a short fragment after a backchannel", "turn", turn.ID, "voiced_ms", e.lastActiveMS)
			return
		}
		e.log.Debug("speak: a fragment after a backchannel opens with a phrase of another kind", "turn", turn.ID, "voiced_ms", e.lastActiveMS, "opener", phrase)
	}
	if phrase == "" {
		phrase = e.planOpener(turn)
	}
	if phrase == "" {
		return
	}
	cached, ok := Phrases().Get(e.phraseKey(phrase))
	if !ok {
		e.log.Debug("speak: no opener in the cache yet", "text", phrase)
		return
	}
	pcm, err := audio.ResamplePCM16(cached.PCM, cached.Rate, e.opts.ClientRate)
	if err != nil || len(pcm) == 0 {
		return
	}
	// The synthesizer renders the opener's comma as a pause of several hundred
	// milliseconds (好的， came back as 660–700 ms of audio for ~350 of voice);
	// the answer follows at once, so the pause is cut short — otherwise the
	// opener's length, not the model, decided when the answer began (bench H
	// of 10-25: every content figure exactly 380 + the opener's length; the
	// 10:46 call of 10-26 again, with 120 ms kept).
	pcm = audio.TrimTrailingSilence(pcm, e.opts.ClientRate, -40, openerTailMS)
	// The rendering opens with a quiet head as well, before the first syllable.
	// The opener is the turn's first audio, so that head is heard as the pause
	// running on — and the answer's own first word, which waits for the
	// opener's end, is later by the same amount on every turn the opener
	// binds (14 of 18 on the 10:46 call of 10-26). Cut to 20 ms.
	rendered := len(pcm)
	pcm = audio.TrimLeadingSilence(pcm, e.opts.ClientRate, -40, openerLeadMS)
	leadMS := int(audio.PCM16(e.opts.ClientRate).DurationMS(make([]byte, rendered-len(pcm))))
	// A one-word opener is 400–500 ms of voice; a faint tail above the trim's
	// floor once left one at 1000 ms (the 09:16 call of 10-26), which then set
	// when the answer began. Capped, with a short fade so the cut is not heard.
	if max := audio.PCM16(e.opts.ClientRate).BytesForMS(openerMaxMS); len(pcm) > max {
		pcm = pcm[:max]
	}
	// The join to the answer is a cut between two renderings; a fade on the
	// opener's last 60 ms takes the edge off it (the owner, 09:37 on 10-26:
	// "we need a smooth opener").
	pcm = audio.FadeOutPCM16(append([]byte(nil), pcm...), e.opts.ClientRate, 60)
	turn.Opened = phrase
	turn.OpenedAt = time.Now()
	turn.OpenedMS = int64(audio.PCM16(e.opts.ClientRate).DurationMS(pcm))
	e.lastSaidOpener = phrase
	turn.MarkFirstSegment()
	seg := Segment{Gen: turn.Generation, TurnID: turn.ID, Revision: turn.Revision}
	begin, body, mark := seg, seg, seg
	begin.Kind, begin.Text = SegBegin, phrase
	body.Kind, body.PCM = SegAudio, pcm
	mark.Kind, mark.Text = SegMark, phrase
	e.player.Enqueue(begin)
	turn.MarkFirstAudio()
	e.player.Enqueue(body)
	e.player.Enqueue(mark)
	turn.AppendResponse(phrase)
	e.log.Debug("speak: opened the answer from the phrase cache at the pause", "turn", turn.ID, "text", phrase,
		"audio_ms", int(audio.PCM16(e.opts.ClientRate).DurationMS(pcm)), "lead_ms", leadMS)
}

// planFragmentOpener chooses the opener for a fragment said after a
// backchannel: never the phrase just said as the backchannel, and never an
// 嗯 after an 嗯 (the tic the owner heard on 10-25) — 嗯，… 好的，… and 好的，…
// 嗯，… both read as listening; a scenario phrase that fits the caller's
// words if there is one, else a statement opener; one not said in the last
// three turns when the pool allows it. The phrase planned for this turn when
// the answer was begun (the model was told to continue from it) serves when
// it passes. "" when nothing does.
func (e *Engine) planFragmentOpener(turn *Turn) string {
	d := e.opts.Cfg.Duplex
	if !d.Opener || turn == nil {
		return ""
	}
	isEn := func(p string) bool { return strings.HasPrefix(p, "嗯") }
	allowed := func(p string) bool {
		return p != "" && p != e.lastSaidOpener && !(isEn(p) && isEn(e.lastSaidOpener))
	}
	if planned := turn.OpenerPlanned; allowed(planned) {
		return planned
	}
	question := endpoint.ReadsAsQuestion(turn.Transcript)
	pool := d.OpenerPhrases
	if sc := e.openers.Match(turn.Transcript, question); sc != nil {
		pool = sc.Phrases
	}
	var fresh, any []string
	for _, p := range pool {
		if !allowed(p) {
			continue
		}
		any = append(any, p)
		recent := false
		for _, r := range e.recentOpeners {
			if r == p && r != turn.OpenerPlanned {
				recent = true
				break
			}
		}
		if !recent {
			fresh = append(fresh, p)
		}
	}
	if len(fresh) == 0 {
		fresh = any
	}
	if len(fresh) == 0 {
		e.log.Debug("speak: no opener of another kind for the fragment", "turn", turn.ID, "pool", len(pool), "last", e.lastSaidOpener)
		return ""
	}
	phrase := fresh[rand.Intn(len(fresh))]
	e.recentOpeners = append(e.recentOpeners, phrase)
	if len(e.recentOpeners) > 3 {
		e.recentOpeners = e.recentOpeners[len(e.recentOpeners)-3:]
	}
	turn.OpenerPlanned = phrase
	return phrase
}

// planOpener chooses the opener for a turn's answer from the pool that fits
// how the caller's words read — a question gets 嗯，/这个呀，/我看一下，…, a
// statement 好的，/明白，/行，… — never one said in the last three turns, and
// records it on the turn so the model is told to continue from it.
func (e *Engine) planOpener(turn *Turn) string {
	d := e.opts.Cfg.Duplex
	if !d.Opener || turn == nil || turn.OpenerPlanned != "" {
		return turn.OpenerPlanned
	}
	question := endpoint.ReadsAsQuestion(turn.Transcript)
	pool := d.OpenerPhrases
	if question && len(d.OpenerQuestionPhrases) > 0 {
		pool = d.OpenerQuestionPhrases
	}
	// A scenario from the panel that fits the caller's words gives an opener
	// that carries meaning (价格的话，) instead of a bare 嗯，: the ConvFill
	// idea at the cache's latency (tools/openers.go).
	scenario := ""
	generic := pool
	if sc := e.openers.Match(turn.Transcript, question); sc != nil {
		pool, scenario = sc.Phrases, sc.ID
	}
	// The least recently said phrase of the pool, never the one said last;
	// when every phrase of a small scenario pool has been said this call,
	// the profile's generic pool joins the candidates. Random choice among
	// "not in the last three" let 保费呢， come twice in five price questions
	// (the owner, 10:07 on 10-28: "I hear 保费呢 more than once").
	if e.openerUses == nil {
		e.openerUses = map[string]int{}
	}
	candidates := pool
	allSaid := len(pool) > 0
	for _, p := range pool {
		if e.openerUses[p] == 0 {
			allSaid = false
		}
	}
	if allSaid {
		candidates = append(append([]string(nil), pool...), generic...)
	}
	var phrase string
	best := -1
	last := ""
	if n := len(e.recentOpeners); n > 0 {
		last = e.recentOpeners[n-1]
	}
	for _, p := range rand.Perm(len(candidates)) {
		c := candidates[p]
		if c == last && len(candidates) > 1 {
			continue
		}
		if best < 0 || e.openerUses[c] < best {
			phrase, best = c, e.openerUses[c]
		}
	}
	if phrase == "" {
		return ""
	}
	e.openerTurn++
	e.openerUses[phrase] = e.openerTurn
	e.recentOpeners = append(e.recentOpeners, phrase)
	if len(e.recentOpeners) > 3 {
		e.recentOpeners = e.recentOpeners[len(e.recentOpeners)-3:]
	}
	turn.OpenerPlanned = phrase
	if scenario != "" {
		e.log.Debug("speak: the opener fits the caller's words", "turn", turn.ID, "scenario", scenario, "opener", phrase)
	}
	return phrase
}

// loadOpeners reads the lead service's scenario-keyed openers for this
// session and makes sure their phrases are in the cache (a panel save warms
// them too, through /v1/phrases; this covers a restart in between).
func (e *Engine) loadOpeners() {
	cfg := e.opts.Cfg
	if !cfg.Duplex.Opener || cfg.ToolsBaseURL == "" {
		return
	}
	o, err := tools.FetchOpeners(cfg.ToolsBaseURL, e.toolsToken(), 2*time.Second)
	if err != nil {
		e.log.Warn("speak: the lead service's openers could not be read; the profile's pools stand", "err", err)
		return
	}
	e.openers = o
	e.log.Info("speak: openers by scenario from the lead service", "version", o.Version, "scenarios", len(o.Scenarios), "phrases", len(o.Phrases()))
	if e.deps.TTS != nil && cfg.Duplex.PhraseCache {
		texts := o.Phrases()
		go func() {
			ctx, cancel := context.WithTimeout(e.ctx, 3*time.Minute)
			defer cancel()
			if n, err := WarmTexts(ctx, cfg, e.deps.TTS, texts, e.log); err != nil {
				e.log.Warn("speak: the openers could not all be cached", "added", n, "err", err)
			} else if n > 0 {
				e.log.Info("speak: openers cached", "added", n)
			}
		}()
	}
}

// openerNote tells the model the opener the engine says for this turn, so
// its first clause continues the sentence from it instead of starting one
// of its own beside it.
func openerNote(phrase string) string {
	return fmt.Sprintf("【系统提示】这句回答的开头“%s”已由系统替你说出。请从这个开头之后自然地接着说下去，让语气连贯；不要再说这个开头，也不要再加“好的”“嗯”之类的语气词。", phrase)
}

// maybeOpenDuringPatience says the opener at the base pause even when the
// turn is being given longer because the words read as unfinished: a
// listener's 嗯 at a pause is what a person does while the other is still
// thinking, and if the caller goes on it becomes exactly that (the backchannel
// rule). The answer itself still waits for the turn to be judged over. On
// the 09:31 call of 10-26 the two turns over 1000 on the content figure were
// 600 ms patience waits; this takes the opener's length off them.
func (e *Engine) maybeOpenDuringPatience() {
	d := e.opts.Cfg.Duplex
	if !d.Opener || e.patience <= 0 || !e.userOpen {
		return
	}
	spec := e.liveSpeculation()
	if spec == nil || spec.Opened != "" || spec.OpenerPlanned == "" {
		return
	}
	if float64(e.opts.Cfg.VAD.MinSilenceMS) > e.vad.TrailingSilenceMS() {
		return
	}
	e.sayOpener(spec)
}
