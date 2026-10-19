package duplex

import (
	"context"
	"os"
	"strings"
	"time"

	"github.com/chuanmingliu/golive/internal/audio"
	"github.com/chuanmingliu/golive/internal/focus"
	"github.com/chuanmingliu/golive/internal/live"
	"github.com/chuanmingliu/golive/internal/metrics"
)

// Focus: keeping the conversation on one voice. See internal/focus for the
// idea; this file is where the engine uses it.
//
// The caller's audio is fed to a speaker-activity model in the background
// (focusPush). The model's labels run about half a second behind the audio,
// which is too late to decide whether a turn is over — the final transcript
// is in hand ~210 ms after the last sound — and early enough to abandon an
// answer, which takes ~650 ms more to become audio. So every turn begun on
// the caller's words is also noted here with the span of audio it answers
// (focusNote), and on each tick the span's attribution is asked for
// (judgeFocus): once most of it is labelled, an utterance that was mostly
// another voice's, with the primary silent, has its answer abandoned — before
// the first audio when that is still possible, with a "too late" in the log
// when it is not.
//
// The primary speaker is whoever first said enough after the greeting; the
// assistant's own voice coming back down the line is masked out by the
// moments the assistant was speaking, with a tail for the phone leg.

// focusState is the engine's side of the focus channel.
type focusState struct {
	scorer  focus.Scorer
	tracker *focus.Tracker
	// speakFrom is the audio-clock ms at which the assistant began speaking,
	// or -1 while it is silent.
	speakFrom int64
	// pending are turns awaiting a verdict, by turn id.
	pending map[string]*focusPending
	judged  int
	vetoed  int
	late    int
	// bargeIns is how many sounds over the answer were judged by the
	// labels, and kept how many of those kept the floor.
	bargeIns int
	kept     int
	// enrolments is how many times a target-speaker service has been told
	// the caller's voice (see enrolFocus); ignored counts utterances let go
	// by as not the caller's, with no turn made of them.
	enrolments int
	offered    int
	ignored    int
	// quietCloses counts turns taken because the caller's voice went quiet
	// while the line did not (maybeCloseOnCallerQuiet).
	quietCloses int
	// distrust says the enrolment did not match what followed (see
	// checkEnrolment): verdicts are logged and not acted on.
	distrust     bool
	checked      bool
	enrolledAtMS int64
	// lag is the model's own: its input buffer. Compute is on top.
	lag time.Duration
}

type focusPending struct {
	turn    *Turn
	startMS int64
	endMS   int64 // provisional while the utterance is open
	open    bool
	noted   time.Time
}

// focusEchoTailMS is how long after the assistant falls silent its voice can
// still be coming back down the line.
const focusEchoTailMS = 600

// focusEnrolMinMS is the least voiced audio an enrolment candidate may carry.
const focusEnrolMinMS = 600

// startFocus connects to the diarizer, when the profile asks for one. A
// connection that cannot be made is a session without focus, logged once.
func (e *Engine) startFocus(ctx context.Context) {
	d := e.opts.Cfg.Duplex
	if d.Focus == "" || (d.FocusURL == "" && e.focusDial == nil) {
		return
	}
	token := ""
	if d.FocusTokenFile != "" {
		if b, err := os.ReadFile(d.FocusTokenFile); err == nil {
			token = strings.TrimSpace(string(b))
		} else {
			e.log.Warn("focus: the token file could not be read; connecting without one", "file", d.FocusTokenFile, "err", err)
		}
	}
	dial := e.focusDial
	if dial == nil {
		dial = func(ctx context.Context) (focus.Scorer, error) { return focus.Dial(ctx, d.FocusURL, token, e.log) }
	}
	client, err := dial(ctx)
	if err != nil {
		e.log.Warn("focus: the diarizer is not available; this session goes on without focus", "err", err)
		return
	}
	fs := &focusState{scorer: client, tracker: focus.NewTracker(focus.Config{}), speakFrom: -1,
		pending: map[string]*focusPending{}, lag: client.Lag()}
	e.focus = fs
	e.log.Info("focus: on", "mode", d.Focus, "lag_ms", fs.lag.Milliseconds(), "veto_share", e.focusVetoShare(), "min_voiced_ms", e.focusMinVoicedMS(), "profile", d.FocusProfile)
	if en, ok := client.(focus.Enroller); ok && d.FocusProfile != "" {
		if err := en.EnrolProfile(d.FocusProfile); err != nil {
			e.log.Warn("focus: the kept profile could not be asked for", "err", err)
		}
	}
	e.wg.Add(1)
	go func() {
		defer e.wg.Done()
		focus.Run(ctx, client, fs.tracker, nil)
	}()
}

func (e *Engine) focusVetoShare() float64 {
	if v := e.opts.Cfg.Duplex.FocusVetoShare; v > 0 {
		return v
	}
	return 0.7
}

func (e *Engine) focusMinVoicedMS() int64 {
	if v := e.opts.Cfg.Duplex.FocusMinVoicedMS; v > 0 {
		return int64(v)
	}
	return 240
}

// focusPush hands the caller's audio (pipeline rate) to the model.
func (e *Engine) focusPush(pcm []byte, atMS int64) {
	if e.focus == nil || len(pcm) == 0 {
		return
	}
	_ = e.focus.scorer.Push(pcm, atMS)
}

// focusSpeaking notes the assistant's voice going out, for masking.
func (e *Engine) focusSpeaking(speaking bool) {
	fs := e.focus
	if fs == nil {
		return
	}
	now := e.inputMS.Load()
	if speaking {
		if fs.speakFrom < 0 {
			fs.speakFrom = now
		}
		return
	}
	if fs.speakFrom >= 0 {
		fs.tracker.Mask(fs.speakFrom, now+focusEchoTailMS)
		fs.speakFrom = -1
	}
}

// focusNote records that turn answers the caller's audio from startMS to the
// end of the utterance (endMS, or the present if it is still open).
func (e *Engine) focusNote(turn *Turn, startMS, endMS int64, open bool) {
	fs := e.focus
	if fs == nil || turn == nil || turn.Transcript == "" {
		return
	}
	if p := fs.pending[turn.ID]; p != nil {
		p.turn, p.endMS, p.open = turn, endMS, open
		return
	}
	fs.pending[turn.ID] = &focusPending{turn: turn, startMS: startMS, endMS: endMS, open: open, noted: time.Now()}
}

// focusUtteranceClosed fixes the end of any pending span still open.
func (e *Engine) focusUtteranceClosed(endMS int64) {
	fs := e.focus
	if fs == nil {
		return
	}
	for _, p := range fs.pending {
		if p.open {
			p.endMS, p.open = endMS, false
		}
	}
}

// judgeFocus is called on each tick.
func (e *Engine) judgeFocus() {
	fs := e.focus
	if fs == nil || len(fs.pending) == 0 {
		return
	}
	now := e.inputMS.Load()
	for id, p := range fs.pending {
		if p.turn.Dead() || (!e.tracker.IsCurrent(p.turn) && e.liveSpeculation() != p.turn) {
			delete(fs.pending, id)
			continue
		}
		endMS := p.endMS
		if p.open {
			endMS = now
		}
		a := fs.tracker.Attribute(p.startMS, endMS)
		labelledTo := fs.tracker.LabelledTo()
		if !a.Known {
			if labelledTo > endMS+1500 {
				delete(fs.pending, id) // nobody enrolled yet; nothing to compare with
			}
			continue
		}
		if a.Coverage < 0.8 {
			if labelledTo > endMS+2000 || time.Since(p.noted) > 4*time.Second {
				// The labels never came; give up on this one quietly.
				delete(fs.pending, id)
			}
			continue
		}
		if p.open {
			// Judged on what is labelled so far; the rest comes when the
			// utterance closes. Only a clear "somebody else" acts now.
			if a.Others < e.focusVetoShare() || a.VoicedMS < e.focusMinVoicedMS() || a.Primary > 0 {
				continue
			}
		}
		delete(fs.pending, id)
		fs.judged++
		// Another voice's only when the caller is in none of it: under a
		// show the caller's own windows score as a mixture and come out
		// half "other", and four of the caller's answers were dropped on
		// that (the 23:20 call). A mixture is the caller talking.
		others := a.VoicedMS >= e.focusMinVoicedMS() && a.Others >= e.focusVetoShare() && a.Primary == 0
		t := p.turn.Timings()
		heard := !t.FirstAudioOut.IsZero()
		e.log.Debug("focus: utterance attributed", "turn", p.turn.ID, "primary_share", round2(a.Primary), "others_share", round2(a.Others),
			"voiced_ms", a.VoicedMS, "coverage", round2(a.Coverage), "primary_speaker", fs.tracker.Primary(),
			"labelled_lag_ms", now-labelledTo, "another_voice", others, "heard", heard)
		e.deps.Emit(live.FocusEvent{Envelope: live.Envelope{Type: live.ExtFocus}, TurnID: p.turn.ID,
			PrimaryShare: round2(a.Primary), OthersShare: round2(a.Others), VoicedMS: a.VoicedMS, Coverage: round2(a.Coverage),
			Primary: fs.tracker.Primary(), AnotherVoice: others, Vetoed: others && !heard && e.focusActs()})
		if !others {
			continue
		}
		if !e.focusActs() {
			e.log.Info("focus: an utterance was mostly another voice (observing only)", "turn", p.turn.ID, "others_share", round2(a.Others))
			continue
		}
		if heard {
			fs.late++
			e.log.Info("focus: another voice's utterance was answered before the labels came", "turn", p.turn.ID,
				"others_share", round2(a.Others), "first_audio_out_ms", t.FirstAudioOut.Sub(t.SpeechEndAt).Milliseconds())
			metrics.Default().FocusLate()
			continue
		}
		fs.vetoed++
		e.log.Info("focus: vetoing the answer: the utterance was another voice's", "turn", p.turn.ID,
			"others_share", round2(a.Others), "voiced_ms", a.VoicedMS, "waited_ms", time.Since(p.noted).Milliseconds())
		metrics.Default().FocusVetoed()
		e.vetoTurn(p.turn)
	}
}

// vetoTurn drops an answer and the words it answered, before anybody heard
// either.
func (e *Engine) vetoTurn(turn *Turn) {
	// Its words are not the caller's: they do not go into the conversation.
	if e.recordedUser == nil {
		e.recordedUser = map[string]bool{}
	}
	e.recordedUser[turn.ID] = true
	if e.lastCommitted == turn {
		e.lastCommitted = nil
	}
	if e.liveSpeculation() == turn {
		e.abandonSpeculation("the utterance was another voice's")
		return
	}
	turn.kill()
	e.forgetDelegation(delegationIDOf(turn))
	e.cancelBackend()
	if e.delegatedTurn == turn {
		e.delegatedTurn = nil
	}
	if e.tracker.IsCurrent(turn) {
		e.gen.Bump()
		e.abortSpeech()
		e.tracker.Abandon()
	}
	e.watch.Rearm()
}

// focusSummary is one line at the end of the call.
func (e *Engine) focusSummary() {
	fs := e.focus
	if fs == nil {
		return
	}
	e.log.Info("focus: this call", "judged", fs.judged, "vetoed", fs.vetoed, "too_late", fs.late,
		"barge_ins_judged", fs.bargeIns, "barge_ins_kept", fs.kept, "quiet_closes", fs.quietCloses, "echo_speaker", fs.tracker.Echo(),
		"primary_speaker", fs.tracker.Primary(), "handovers", fs.tracker.Handovers())
	_ = fs.scorer.Close()
}

func round2(f float64) float64 { return float64(int(f*100+0.5)) / 100 }

// bargeInVerdict is what the labels say about a sound over the answer.
type bargeInVerdict int

const (
	// bargeInYield: the caller's voice, or no labels to say otherwise in
	// time; the answer is cut as it always was.
	bargeInYield bargeInVerdict = iota
	// bargeInKeep: not the caller's voice; the answer carries on.
	bargeInKeep
	// bargeInWait: the labels are not in yet and there is time to wait.
	bargeInWait
)

// focusBargeInWait is how long a barge-in with words is held for labels.
func (e *Engine) focusBargeInWait() time.Duration {
	v := e.opts.Cfg.Duplex.FocusBargeInWaitMS
	if v < 0 {
		return 0
	}
	if v == 0 {
		v = 900
	}
	return time.Duration(v) * time.Millisecond
}

// judgeBargeInFocus decides a barge-in the recognizer has made words of,
// from the speaker labels over the sound so far (see Tracker.AttributeOver).
// The caller's voice yields; another voice, or the assistant's own return
// with nobody else in it, keeps the floor; labels not in yet are waited for,
// up to focus_barge_in_wait_ms from the hold's start, after which the words
// decide as they did before. In observe mode the verdict is logged and the
// words decide.
func (e *Engine) judgeBargeInFocus(text string) bargeInVerdict {
	h, fs := e.hold, e.focus
	if h == nil || fs == nil || e.focusBargeInWait() == 0 {
		return bargeInYield
	}
	now := e.inputMS.Load()
	a := fs.tracker.AttributeOver(e.utterStartMS, now)
	echo := fs.tracker.Echo()
	waited := time.Since(h.started)
	verdict, why := bargeInYield, ""
	primaryMS := int64(a.Primary * float64(a.VoicedMS))
	switch {
	case !a.Known:
		why = "no primary speaker known yet"
	case primaryMS >= 160:
		why = "the caller's voice"
	case a.Coverage < 0.6:
		verdict, why = bargeInWait, "labels not in yet"
	case a.VoicedMS == 0:
		verdict, why = bargeInKeep, "nobody but the assistant's own return"
	case a.Others >= e.focusVetoShare() && primaryMS == 0:
		verdict, why = bargeInKeep, "another voice"
	default:
		verdict, why = bargeInWait, "undecided"
	}
	if h.kept {
		// Already let go by: only the caller's own voice changes that.
		if primaryMS < 160 {
			return bargeInKeep
		}
	} else if verdict == bargeInWait && waited >= e.focusBargeInWait() {
		verdict, why = bargeInYield, why+"; waited out"
	}
	if !h.words {
		h.words = true
		fs.bargeIns++
	}
	if verdict == bargeInWait {
		return bargeInWait
	}
	if h.kept && verdict == bargeInKeep {
		return bargeInKeep // already said so
	}
	e.log.Info("focus: a sound over the answer was judged", "verdict", map[bargeInVerdict]string{bargeInYield: "yield", bargeInKeep: "keep"}[verdict],
		"why", why, "primary_ms", primaryMS, "others_share", round2(a.Others), "voiced_ms", a.VoicedMS,
		"coverage", round2(a.Coverage), "echo_speaker", echo, "waited_ms", waited.Milliseconds(), "text", text,
		"mode", e.opts.Cfg.Duplex.Focus)
	e.deps.Emit(live.FocusEvent{Envelope: live.Envelope{Type: live.ExtFocus}, TurnID: "", BargeIn: true,
		PrimaryShare: round2(a.Primary), OthersShare: round2(a.Others), VoicedMS: a.VoicedMS, Coverage: round2(a.Coverage),
		Primary: fs.tracker.Primary(), AnotherVoice: verdict == bargeInKeep, Vetoed: verdict == bargeInKeep && e.focusActs()})
	if verdict == bargeInKeep {
		if !e.focusActs() {
			// Observing: say what would have happened and cut as before.
			return bargeInYield
		}
		if !h.kept {
			fs.kept++
			h.kept = true
			e.abandonSpeculation("the voice over the answer was not the caller's")
		}
		return bargeInKeep
	}
	return bargeInYield
}

// focusCandidates is how many of the caller's utterances are offered to the
// service as their voice. The service enrols the largest group of them that
// agree with each other (edge/tsv_server.py): whoever picked up is learned
// from the call, and one bad window — the 09:41 call on 10-16 — is outvoted
// rather than ruling the call. A kept profile, when one was loaded, is only
// extended.
const focusCandidates = 6

// enrolFocus offers a target-speaker service an utterance as the caller's
// voice: one the caller made alone — long enough to carry a voice, not
// over the assistant, nothing of the assistant's in it.
func (e *Engine) enrolFocus(startMS, endMS int64, activeMS float64, bargeIn bool) {
	fs := e.focus
	if fs == nil || fs.offered >= focusCandidates {
		return
	}
	en, ok := fs.scorer.(focus.Enroller)
	if !ok {
		return
	}
	if e.uttVerdict == uttIgnored || e.uttVerdict == uttNoted {
		return // the labels already said this one was not the caller's
	}
	// Only the part of the utterance the caller made alone is offered: from
	// the end of the assistant's voice and its echo tail, if they overlap
	// the start. A fast talker answers within the tail of every reply and
	// interrupts the rest; on the 19:25 call of 10-19 (a metro station) no
	// utterance was clean from its first millisecond, the caller was never
	// enrolled, and a station announcement cut an answer that the labels
	// would have kept. The voiced part of the remainder is taken as
	// proportional to its length.
	from := startMS
	if until := fs.tracker.MaskedUntil(startMS, endMS+focusEchoTailMS); until >= 0 {
		if until >= endMS-focusEnrolMinMS {
			return // the assistant's voice reaches into the whole of it
		}
		from = until
		activeMS = activeMS * float64(endMS-from) / float64(endMS-startMS)
	}
	if activeMS < focusEnrolMinMS {
		return
	}
	if err := en.Enrol(from, endMS, e.opts.Cfg.Duplex.FocusProfile); err != nil {
		e.log.Warn("focus: the caller's voice could not be offered", "err", err)
		return
	}
	fs.offered++
	e.log.Info("focus: offering the caller's voice", "from_ms", from, "to_ms", endMS, "voiced_ms", int(activeMS), "trimmed_ms", from-startMS, "barge_in", bargeIn, "offered", fs.offered)
}

// readEnrolments takes the service's answers: a profile found means the
// call is enrolled before the caller has spoken.
func (e *Engine) readEnrolments() {
	fs := e.focus
	en, ok := fs.scorer.(focus.Enroller)
	if !ok {
		return
	}
	for {
		select {
		case a := <-en.Enrolled():
			switch {
			case a.Profile != "" && a.Found:
				if fs.enrolments == 0 {
					fs.enrolments = 1
					fs.enrolledAtMS = e.inputMS.Load()
					fs.tracker.Pin(0)
					e.log.Info("focus: the caller's voice is enrolled from the kept profile", "profile", a.Profile)
				}
			case a.Profile != "":
				e.log.Info("focus: no kept profile; the caller's voice is learned on this call", "profile", a.Profile)
			case a.Rejected:
				e.log.Info("focus: an offered utterance disagreed with the enrolment; not taken", "candidates", a.Candidates)
			default:
				first := fs.enrolments == 0
				fs.enrolments++
				if first {
					fs.enrolledAtMS = e.inputMS.Load()
					fs.tracker.Pin(0)
				}
				e.log.Info("focus: the caller's voice is enrolled", "candidates", a.Candidates, "agreeing", a.Agreeing,
					"tentative", a.Tentative, "voiced_ms", a.VoicedMS, "saved", a.Saved)
			}
		default:
			return
		}
	}
}

// focusActs reports whether verdicts are acted on: veto mode, and an
// enrolment that has not been found wanting.
func (e *Engine) focusActs() bool {
	return e.opts.Cfg.Duplex.Focus == "veto" && (e.focus == nil || !e.focus.distrust)
}

// checkEnrolment is a sanity check on the enrolment, once: when the first
// seconds of voice after it are mostly "not the caller", the enrolment is
// the wrong voice (a television speaking first, a bad window), and acting
// on its labels would ignore the caller. The channel then observes only.
func (e *Engine) checkEnrolment() {
	fs := e.focus
	if fs == nil {
		return
	}
	e.readEnrolments()
	if fs.enrolments == 0 || fs.distrust || fs.checked {
		return
	}
	caller, others := fs.tracker.SpokenSince(fs.enrolledAtMS)
	if caller+others < 8000 {
		return
	}
	fs.checked = true
	if float64(caller)/float64(caller+others) < 0.3 {
		fs.distrust = true
		e.log.Warn("focus: the enrolment does not match what followed; observing only",
			"caller_ms", caller, "others_ms", others)
	}
}

// judgeUtteranceFocus is called on each tick while an utterance is open: is
// the open utterance the caller's? With labels a window behind the audio,
// the answer comes while the words are still arriving — before anything is
// prepared on them. Not the caller's (another voice, or nothing voiced for
// a while though the gate is open) and the utterance is let go by: no
// guess, no turn, no interruption, unless the caller's voice joins in. The
// verdict is logged either way; only veto mode acts on it.
func (e *Engine) judgeUtteranceFocus() {
	fs := e.focus
	if fs == nil {
		return
	}
	e.checkEnrolment()
	if !e.userOpen || e.uttVerdict == uttCaller {
		return
	}
	now := e.inputMS.Load()
	to := fs.tracker.LabelledTo()
	if to > now {
		to = now
	}
	span := to - e.utterStartMS
	if span < 300 {
		return
	}
	a := fs.tracker.AttributeOver(e.utterStartMS, to)
	if !a.Known {
		return
	}
	primaryMS := int64(a.Primary * float64(a.VoicedMS))
	switch {
	case primaryMS >= 160:
		if e.uttVerdict == uttIgnored {
			e.log.Info("focus: the caller's voice joined the utterance; taking it up", "primary_ms", primaryMS, "span_ms", span)
			e.stopIgnoring()
		}
		e.uttVerdict = uttCaller
	case e.uttVerdict == uttIgnored || e.uttVerdict == uttNoted:
		// Still not the caller's.
	case a.VoicedMS >= e.focusMinVoicedMS() && a.Others >= e.focusVetoShare() && primaryMS == 0:
		e.ignoreUtterance("another voice", a, span)
	case a.VoicedMS == 0 && span >= 800:
		e.ignoreUtterance("nothing voiced", a, span)
	}
}

// maybeCloseOnCallerQuiet is the third way a turn ends, after the detector's
// pause and the settled close: the caller's own voice has been absent, by
// the labels, for duplex.focus_quiet_close_ms while the gate stayed open on
// sound that is not theirs. On the 09:37 call of 10-19 a noisy room kept
// one utterance open for nine seconds (half its voiced time another
// source's), the recognizer kept making words of the noise so the settled
// close never saw them stand still, and the caller waited 1.5 s for an
// answer the engine had ready in 625. The labels lag the audio by tens of
// milliseconds here (a target-speaker service), so the pause is judged on
// the labelled range only.
func (e *Engine) maybeCloseOnCallerQuiet() {
	fs := e.focus
	quiet := int64(e.opts.Cfg.Duplex.FocusQuietCloseMS)
	if fs == nil || quiet <= 0 || !e.focusActs() || !e.userOpen || e.uttVerdict != uttCaller || e.muted || e.hold != nil {
		return
	}
	to := fs.tracker.LabelledTo()
	if now := e.inputMS.Load(); to > now {
		to = now
	}
	since := to - quiet
	if since < e.utterStartMS {
		return
	}
	primaryMS, othersMS := fs.tracker.SpokenSince(since)
	if primaryMS > 0 {
		return
	}
	e.log.Info("listen: the caller's voice has been quiet though the line has not; taking the turn",
		"caller_quiet_ms", quiet, "others_ms", othersMS, "labelled_to_ms", to,
		"noise_floor_db", round1(e.vad.NoiseFloorDB()))
	stop := e.vad.Stop()
	if stop.Kind != audio.DecisionStopped {
		return
	}
	stop.SilenceMS = float64(quiet)
	fs.quietCloses++
	e.handleDecision(stop)
}

type uttVerdict int

const (
	uttUnjudged uttVerdict = iota
	uttCaller
	uttIgnored
	// uttNoted: judged not the caller's and logged, with nothing acted on
	// (observe mode, or a distrusted enrolment).
	uttNoted
)

func (e *Engine) ignoreUtterance(why string, a focus.Attribution, span int64) {
	fs := e.focus
	fs.ignored++
	mode := e.opts.Cfg.Duplex.Focus
	e.log.Info("focus: the open utterance is not the caller's", "why", why, "others_share", round2(a.Others),
		"voiced_ms", a.VoicedMS, "span_ms", span, "coverage", round2(a.Coverage), "mode", mode,
		"acting", e.focusActs())
	e.deps.Emit(live.FocusEvent{Envelope: live.Envelope{Type: live.ExtFocus}, Utterance: true,
		PrimaryShare: round2(a.Primary), OthersShare: round2(a.Others), VoicedMS: a.VoicedMS, Coverage: round2(a.Coverage),
		Primary: fs.tracker.Primary(), AnotherVoice: true, Vetoed: e.focusActs()})
	if !e.focusActs() {
		e.uttVerdict = uttNoted
		return
	}
	e.uttVerdict = uttIgnored
	if e.hold != nil && !e.hold.kept {
		fs.kept++
		e.hold.kept = true
		e.hold.words = true
	}
	e.abandonSpeculation("the utterance is not the caller's")
}

// ignoring reports whether the open utterance is being let go by.
func (e *Engine) ignoring() bool { return e.uttVerdict == uttIgnored }

func (e *Engine) stopIgnoring() {
	e.uttVerdict = uttUnjudged
	if e.hold != nil && e.hold.kept {
		e.releaseHold("the caller's voice joined in")
	}
}
