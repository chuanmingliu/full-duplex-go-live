package duplex

import (
	"context"
	"os"
	"strings"
	"time"

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
	e.log.Info("focus: on", "mode", d.Focus, "lag_ms", fs.lag.Milliseconds(), "veto_share", e.focusVetoShare(), "min_voiced_ms", e.focusMinVoicedMS())
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
			if a.Others < e.focusVetoShare() || a.VoicedMS < e.focusMinVoicedMS() {
				continue
			}
		}
		delete(fs.pending, id)
		fs.judged++
		others := a.VoicedMS >= e.focusMinVoicedMS() && a.Others >= e.focusVetoShare()
		t := p.turn.Timings()
		heard := !t.FirstAudioOut.IsZero()
		e.log.Debug("focus: utterance attributed", "turn", p.turn.ID, "primary_share", round2(a.Primary), "others_share", round2(a.Others),
			"voiced_ms", a.VoicedMS, "coverage", round2(a.Coverage), "primary_speaker", fs.tracker.Primary(),
			"labelled_lag_ms", now-labelledTo, "another_voice", others, "heard", heard)
		e.deps.Emit(live.FocusEvent{Envelope: live.Envelope{Type: live.ExtFocus}, TurnID: p.turn.ID,
			PrimaryShare: round2(a.Primary), OthersShare: round2(a.Others), VoicedMS: a.VoicedMS, Coverage: round2(a.Coverage),
			Primary: fs.tracker.Primary(), AnotherVoice: others, Vetoed: others && !heard && e.opts.Cfg.Duplex.Focus == "veto"})
		if !others {
			continue
		}
		if e.opts.Cfg.Duplex.Focus != "veto" {
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
		"barge_ins_judged", fs.bargeIns, "barge_ins_kept", fs.kept, "echo_speaker", fs.tracker.Echo(),
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
	case a.Others >= e.focusVetoShare():
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
		Primary: fs.tracker.Primary(), AnotherVoice: verdict == bargeInKeep, Vetoed: verdict == bargeInKeep && e.opts.Cfg.Duplex.Focus == "veto"})
	if verdict == bargeInKeep {
		if e.opts.Cfg.Duplex.Focus != "veto" {
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
