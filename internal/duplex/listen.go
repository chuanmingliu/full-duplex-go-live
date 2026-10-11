package duplex

import (
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/chuanmingliu/golive/internal/audio"
	"github.com/chuanmingliu/golive/internal/endpoint"
	"github.com/chuanmingliu/golive/internal/live"
	"github.com/chuanmingliu/golive/internal/metrics"
	"github.com/chuanmingliu/golive/internal/provider"
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
	run.finalSeen = true
	text := strings.TrimSpace(run.lastText)
	if text != "" {
		e.log.Warn("asr: the recognizer ended without a final transcript; taking its last hypothesis as one",
			"run", run.id, "text", text)
	}
	if e.hold != nil && e.judgeHold(text, true) {
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
	run := &asrRun{
		id:       e.asrSeq,
		stream:   stream,
		itemID:   fmt.Sprintf("user_%d", e.asrSeq),
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
}

// closeASR ends the current recognition. hard tears the stream down without
// waiting for a final result; the soft form asks for one.
func (e *Engine) closeASR(hard bool) {
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

	text := strings.TrimSpace(ev.res.Text)
	if text == "" && !ev.res.Final {
		return
	}
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
	if e.hold == nil {
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
	if spec := e.liveSpeculation(); spec != nil &&
		normalizeForCompare(text) != normalizeForCompare(spec.Transcript) {
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
	if run.lastText != "" && e.classify(run.lastText) == endpoint.Incomplete {
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
	if e.bcSet.Matches(run.lastText) {
		e.specWhy = "the partial so far is an acknowledgement, not a question"
		return
	}

	d := e.opts.Cfg.Duplex
	class := e.classify(run.lastText)
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
	if !e.opts.Speculative || e.muted || e.hold != nil {
		return
	}
	run := e.asrRun
	if run == nil || !run.sendDone || run.ended || e.liveSpeculation() != nil {
		return
	}
	if run.lastText == "" || e.bcSet.Matches(run.lastText) {
		return
	}
	e.startSpeculativeTurn(run.lastText)
}

func (e *Engine) startSpeculativeTurn(text string) {
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
		e.abortSpeech()
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

	if spec := e.liveSpeculation(); spec != nil {
		if normalizeForCompare(spec.Transcript) == normalizeForCompare(text) {
			// The guess held: the audio already flowing is correct, and the
			// final transcript costs nothing. This is the whole point of
			// speculating.
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
		e.abortSpeech()
		revised := e.tracker.Revise(spec.ID, text, true)
		e.specTurn = nil
		e.specAbandoned++
		metrics.Default().SpeculationAbandoned()
		if revised != nil {
			e.commitTurn(revised)
			e.beginGeneration(revised, "revision", true)
			return
		}
	}

	e.specTurn = nil
	turn := e.tracker.Begin(text, false)
	turn.MarkTranscriptFinal()
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
