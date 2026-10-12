package audio

import (
	"math"
	"strings"
	"testing"
)

// tone builds a signal with speech-like energy and zero-crossing behaviour:
// a low fundamental plus two harmonics under a syllable-rate envelope.
func tone(rate, ms int, amp float64) []float32 {
	n := rate * ms / 1000
	out := make([]float32, n)
	for i := range out {
		t := float64(i) / float64(rate)
		env := 0.62 + 0.38*math.Sin(2*math.Pi*4.5*t)
		v := amp * env * (math.Sin(2*math.Pi*140*t) +
			0.5*math.Sin(2*math.Pi*280*t) +
			0.25*math.Sin(2*math.Pi*560*t)) / 1.75
		out[i] = float32(v)
	}
	return out
}

func quiet(rate, ms int) []float32 {
	return make([]float32, rate*ms/1000)
}

func feed(v *VAD, samples []float32) []Decision {
	var decisions []Decision
	frame := v.FrameSamples()
	for off := 0; off+frame <= len(samples); off += frame {
		d := v.Push(samples[off : off+frame])
		if d.Kind != DecisionNone {
			decisions = append(decisions, d)
		}
	}
	return decisions
}

func TestVADOpensAndClosesAnUtterance(t *testing.T) {
	const rate = RatePipeline
	v := NewVAD(VADConfig{SampleRate: rate})

	signal := append(quiet(rate, 300), tone(rate, 1200, 0.45)...)
	signal = append(signal, quiet(rate, 800)...)

	decisions := feed(v, signal)

	var started, stopped int
	var stop Decision
	for _, d := range decisions {
		switch d.Kind {
		case DecisionStarted:
			started++
		case DecisionStopped:
			stopped++
			stop = d
		}
	}
	if started != 1 {
		t.Fatalf("expected exactly 1 utterance start, got %d (decisions: %d)", started, len(decisions))
	}
	if stopped != 1 {
		t.Fatalf("expected exactly 1 utterance stop, got %d", stopped)
	}
	if stop.ActiveMS < 600 {
		t.Errorf("expected at least 600ms of voiced audio, got %.0fms", stop.ActiveMS)
	}
	// The utterance must include the pre-roll pad, so it is longer than the
	// voiced part alone.
	gotMS := float64(len(stop.Utterance)) / float64(rate) * 1000
	if gotMS <= stop.ActiveMS {
		t.Errorf("utterance (%.0fms) should exceed active speech (%.0fms) by the pre-roll pad", gotMS, stop.ActiveMS)
	}
}

func TestVADSnapshotsReconstructTheUtterance(t *testing.T) {
	const rate = RatePipeline
	v := NewVAD(VADConfig{SampleRate: rate})

	signal := append(quiet(rate, 200), tone(rate, 1400, 0.45)...)
	signal = append(signal, quiet(rate, 800)...)

	var assembled []float32
	var stopped *Decision
	frame := v.FrameSamples()
	for off := 0; off+frame <= len(signal); off += frame {
		d := v.Push(signal[off : off+frame])
		assembled = append(assembled, d.Snapshot...)
		if d.Kind == DecisionStopped {
			copied := d
			stopped = &copied
			break
		}
	}
	if stopped == nil {
		t.Fatal("utterance never closed")
	}
	if len(assembled) != len(stopped.Utterance) {
		t.Fatalf("snapshots totalled %d samples but the utterance is %d; a streaming ASR would see a different signal than a batch one",
			len(assembled), len(stopped.Utterance))
	}
	for i := range assembled {
		if assembled[i] != stopped.Utterance[i] {
			t.Fatalf("snapshot audio diverges from the utterance at sample %d", i)
		}
	}
}

func TestVADIgnoresShortNoise(t *testing.T) {
	const rate = RatePipeline
	v := NewVAD(VADConfig{SampleRate: rate})

	// A 60 ms click, well under MinSpeechMS, then a long pause.
	signal := append(quiet(rate, 300), tone(rate, 60, 0.6)...)
	signal = append(signal, quiet(rate, 900)...)

	for _, d := range feed(v, signal) {
		if d.Kind == DecisionStarted {
			t.Fatalf("a 60ms click opened an utterance")
		}
	}
}

func TestVADRaisesTheBarWhileAssistantSpeaks(t *testing.T) {
	const rate = RatePipeline
	cfg := VADConfig{SampleRate: rate, BargeInMarginDB: 20, BargeInMinSpeechMS: 320}
	v := NewVAD(cfg)
	v.SetAssistantSpeaking(true)

	// Echo of our own voice through a speakerphone: real energy, ~28 dB down
	// on the talker, and the raised margin puts it under the barge-in bar.
	signal := append(quiet(rate, 200), tone(rate, 1200, 0.02)...)
	for _, d := range feed(v, signal) {
		if d.Kind == DecisionStarted {
			t.Fatalf("leaked playback audio was treated as a barge-in")
		}
	}

	// The same detector must still hear a real interruption.
	v2 := NewVAD(cfg)
	v2.SetAssistantSpeaking(true)
	loud := append(quiet(rate, 200), tone(rate, 1200, 0.5)...)
	var opened bool
	for _, d := range feed(v2, loud) {
		if d.Kind == DecisionStarted {
			opened = true
			if !d.BargeIn {
				t.Error("an utterance opened during playback should be flagged as a barge-in")
			}
		}
	}
	if !opened {
		t.Fatal("a loud interruption during playback was not detected")
	}
}

// TestEchoFloorMeasuresWhatComesBack pins the diagnostic that names the cause
// of an assistant interrupting itself.
//
// The noise floor cannot learn the echo level, because of a trap in its own
// rules: it adapts only on frames it believes are not speech, so once echo is
// loud enough to read as voiced it stops feeding the floor, the floor never
// rises to meet it, and the echo reads as voiced forever. A real call shows the
// signature plainly — noise_floor_db pinned at -60 while barge-ins open at
// active_ms=320 exactly, the configured minimum, over and over. Not somebody
// starting to talk, whose energy overshoots the bar; a signal sitting precisely
// on it.
//
// The echo floor measures it instead, and stays out of the voiced decision.
// Energy alone cannot separate steady echo from a steady voice at the same
// level, and every variant that tried either let the first burst through or
// muted a caller talking over the assistant — much the worse failure. The
// engine's floor hold is the fix; this is the number that tells an operator to
// reach for barge_in_margin_db.
func TestEchoFloorMeasuresWhatComesBack(t *testing.T) {
	const rate = RatePipeline
	cfg := DefaultVADConfig()
	cfg.SampleRate = rate
	v := NewVAD(cfg)

	// Silence while we are not speaking teaches it nothing.
	for _, d := range feed(v, quiet(rate, 400)) {
		_ = d
	}
	if v.EchoFloorDB() != cfg.NoiseFloorDB {
		t.Errorf("echo floor moved to %.1f dB while the assistant was silent", v.EchoFloorDB())
	}

	// Our own voice returning while we speak is what it measures.
	v.SetAssistantSpeaking(true)
	for i := 0; i < 10; i++ {
		feed(v, append(tone(rate, 200, 0.02), quiet(rate, 100)...))
	}
	if v.EchoFloorDB() <= cfg.NoiseFloorDB {
		t.Errorf("echo floor stayed at %.1f dB with echo present", v.EchoFloorDB())
	}
	if v.EchoFloorDB() > cfg.MaxNoiseFloorDB {
		t.Errorf("echo floor ran past its bound: %.1f dB", v.EchoFloorDB())
	}
}

// TestVADUtteranceIsTheInputNotADoubledOnset pins the audio a recognizer is
// handed against the audio that actually arrived.
//
// The snapshot test above compares the detector with itself, so it could not
// see the defect this one exists for: the frames that proved an utterance real
// were held both in the pre-roll ring and in the candidate, and promotion
// concatenated the two. The first fifth of a second of every utterance went to
// the recognizer twice. Comparing against the input is the only check that
// catches it.
func TestVADUtteranceIsTheInputNotADoubledOnset(t *testing.T) {
	const rate = RatePipeline
	// The full default profile: a zero SpeechPadMS means "no pad", and the pad
	// is half of what this test is about.
	v := NewVAD(DefaultVADConfig())

	signal := append(quiet(rate, 600), tone(rate, 1000, 0.45)...)
	signal = append(signal, quiet(rate, 800)...)

	frame := v.FrameSamples()
	var stop *Decision
	consumed := 0
	for off := 0; off+frame <= len(signal); off += frame {
		d := v.Push(signal[off : off+frame])
		consumed = off + frame
		if d.Kind == DecisionStopped {
			copied := d
			stop = &copied
			break
		}
	}
	if stop == nil {
		t.Fatal("utterance never closed")
	}

	// The utterance is a contiguous run of input ending at the frame that
	// closed it.
	begin := consumed - len(stop.Utterance)
	if begin < 0 {
		t.Fatalf("utterance is %d samples but only %d were fed; audio was duplicated",
			len(stop.Utterance), consumed)
	}
	for i, s := range stop.Utterance {
		if s != signal[begin+i] {
			t.Fatalf("utterance diverges from the input at sample %d; the onset was duplicated or reordered", i)
		}
	}

	// And it carries the whole configured pad in front of the first voiced
	// sample, not the sliver left over once the candidate had overwritten it.
	cfg := DefaultVADConfig()
	onset := 600 * rate / 1000
	padMS := float64(onset-begin) / float64(rate) * 1000
	if padMS < float64(cfg.SpeechPadMS)-float64(cfg.FrameMS) {
		t.Errorf("pre-roll pad is %.0f ms; want about %d", padMS, cfg.SpeechPadMS)
	}
	if got, want := stop.StartMS, int64(float64(begin)/float64(rate)*1000); got != want {
		t.Errorf("start_ms is %d but the utterance begins at %d ms of input", got, want)
	}
}

// TestVADKeepsThePrerollAcrossADiscardedCandidate covers the other half of the
// same bookkeeping: a cough that never became an utterance must still be part
// of "the audio just before now" when real speech follows it.
func TestVADKeepsThePrerollAcrossADiscardedCandidate(t *testing.T) {
	const rate = RatePipeline
	v := NewVAD(DefaultVADConfig())

	// 80 ms of noise, a 200 ms gap that discards it, then real speech.
	signal := append(quiet(rate, 400), tone(rate, 80, 0.45)...)
	signal = append(signal, quiet(rate, 200)...)
	signal = append(signal, tone(rate, 900, 0.45)...)
	signal = append(signal, quiet(rate, 800)...)

	frame := v.FrameSamples()
	consumed := 0
	for off := 0; off+frame <= len(signal); off += frame {
		d := v.Push(signal[off : off+frame])
		consumed = off + frame
		if d.Kind != DecisionStopped {
			continue
		}
		begin := consumed - len(d.Utterance)
		for i, s := range d.Utterance {
			if s != signal[begin+i] {
				t.Fatalf("utterance diverges from the input at sample %d", i)
			}
		}
		return
	}
	t.Fatal("utterance never closed")
}

func TestVADReportsCandidatesAndResumption(t *testing.T) {
	const rate = RatePipeline
	v := NewVAD(VADConfig{SampleRate: rate})
	frame := v.FrameSamples()
	push := func(samples []float32) {
		for off := 0; off+frame <= len(samples); off += frame {
			v.Push(samples[off : off+frame])
		}
	}

	push(quiet(rate, 200))
	if v.CandidateMS() != 0 {
		t.Fatalf("silence produced a %.0f ms candidate", v.CandidateMS())
	}
	// Under min_speech_ms: a candidate, not yet an utterance.
	push(tone(rate, 100, 0.45))
	if v.CandidateMS() <= 0 {
		t.Fatal("voiced audio under the minimum should be reported as a candidate")
	}
	push(tone(rate, 500, 0.45))
	if v.CandidateMS() != 0 {
		t.Error("an open utterance is no longer a candidate")
	}
	if v.VoicedRunMS() <= 0 {
		t.Error("an utterance that is being spoken should report a voiced run")
	}

	push(quiet(rate, 200))
	if v.VoicedRunMS() != 0 {
		t.Errorf("voiced run is %.0f ms during a pause", v.VoicedRunMS())
	}
	if v.TrailingSilenceMS() < 180 {
		t.Errorf("trailing silence is %.0f ms after a 200 ms pause", v.TrailingSilenceMS())
	}
	push(tone(rate, 100, 0.45))
	if got := v.VoicedRunMS(); got < 60 || got > 120 {
		t.Errorf("voiced run after resuming is %.0f ms; want about 100", got)
	}
}

// TestVADPatienceIsPerUtterance checks that a longer silence threshold holds an
// utterance open through a pause that would otherwise have closed it, and that
// the override is gone by the next one.
func TestVADPatienceIsPerUtterance(t *testing.T) {
	const rate = RatePipeline
	v := NewVAD(VADConfig{SampleRate: rate})
	base := v.MinSilenceMS()

	feed(v, append(quiet(rate, 200), tone(rate, 800, 0.45)...))
	v.SetMinSilenceMS(base + 400)

	if d := feed(v, quiet(rate, base+200)); len(d) != 0 && d[len(d)-1].Kind == DecisionStopped {
		t.Fatal("the utterance closed inside the extended silence window")
	}
	closed := false
	for _, d := range feed(v, quiet(rate, 400)) {
		if d.Kind == DecisionStopped {
			closed = true
		}
	}
	if !closed {
		t.Fatal("the utterance never closed once the extended window had passed")
	}
	if got := v.MinSilenceMS(); got != base {
		t.Errorf("the override outlived its utterance: min silence is %d, want %d", got, base)
	}
}

// A stop says how long the speaker had already been quiet. That pause is part
// of what a caller waits through, and it is not always the configured one: a
// session may ask for a shorter silence, and a turn held open mid-thought
// closes on a longer one.
func TestVADStopCarriesThePauseItWasJudgedOn(t *testing.T) {
	for _, silence := range []int{200, 380} {
		cfg := DefaultVADConfig()
		cfg.MinSilenceMS = silence
		v := NewVAD(cfg)
		var stop *Decision
		audio := append(tone(cfg.SampleRate, 600, 0.3), quiet(cfg.SampleRate, 900)...)
		for _, d := range feed(v, audio) {
			if d.Kind == DecisionStopped {
				d := d
				stop = &d
			}
		}
		if stop == nil {
			t.Fatalf("min silence %d: the utterance never closed", silence)
		}
		if got := int(stop.SilenceMS); got < silence || got > silence+cfg.FrameMS {
			t.Errorf("min silence %d: the stop reports a pause of %d ms", silence, got)
		}
	}
}

// TestVADKeepsTheBarUpWhileTheLineStillCarriesTheAssistant: on a phone call the
// end of the assistant's sentence comes back through the caller's handset
// after this side has finished sending it. Heard as speech, it opens a turn
// nobody took — and that turn's "words" cancel whatever the caller says next.
func TestVADKeepsTheBarUpWhileTheLineStillCarriesTheAssistant(t *testing.T) {
	const rate = RatePipeline
	// The tail of our own voice returning: well above an idle line, well under
	// somebody talking into the handset.
	echo := func() []float32 { return append(quiet(rate, 100), tone(rate, 400, 0.02)...) }
	opens := func(v *VAD, samples []float32) bool {
		for _, d := range feed(v, samples) {
			if d.Kind == DecisionStarted {
				return true
			}
		}
		return false
	}
	finishSpeaking := func(cfg VADConfig) *VAD {
		v := NewVAD(cfg)
		v.SetAssistantSpeaking(true)
		feed(v, quiet(rate, 400))
		v.SetAssistantSpeaking(false)
		return v
	}
	base := VADConfig{SampleRate: rate, BargeInMarginDB: 20, BargeInMinSpeechMS: 320}

	if !opens(finishSpeaking(base), echo()) {
		t.Fatal("with no echo tail the returning voice should open an utterance; this test proves nothing otherwise")
	}

	tailed := base
	tailed.EchoTailMS = 700
	v := finishSpeaking(tailed)
	if opens(v, echo()) {
		t.Fatal("the assistant's own voice, still on the line, was taken for the caller")
	}
	// The caller answering at once is still heard.
	if !opens(finishSpeaking(tailed), append(quiet(rate, 100), tone(rate, 600, 0.4)...)) {
		t.Fatal("a caller replying straight after the assistant was not heard")
	}
	// And once the line has drained, a quiet caller is too.
	feed(v, quiet(rate, 400))
	if !opens(v, echo()) {
		t.Fatal("the raised bar outlived the echo tail")
	}
}

// echoReturns feeds samples and takes a measurement after every frame, as the
// engine does, returning the times handed out.
func echoReturns(v *VAD, samples []float32) []float64 {
	var out []float64
	frame := v.FrameSamples()
	for off := 0; off+frame <= len(samples); off += frame {
		v.Push(samples[off : off+frame])
		if ms, _, ok := v.TakeEchoReturn(); ok {
			out = append(out, ms)
		}
	}
	return out
}

// returningVoice is the assistant's own voice as a phone line hands it back:
// about -30 dBFS, far over an idle line and far under a caller.
func returningVoice(rate, ms int) []float32 { return tone(rate, ms, 0.1) }

// The phone leg has no clock of its own, but the line returns what is said
// into it, so the time from the assistant starting to its voice arriving back
// on a quiet input is a measurement of the trip there and back.
func TestEchoReturnIsTimedFromTheAssistantStartingToItsVoiceComingBack(t *testing.T) {
	const rate = RatePipeline
	v := NewVAD(VADConfig{SampleRate: rate})

	if got := echoReturns(v, quiet(rate, 400)); len(got) != 0 {
		t.Fatalf("an echo return of %v ms was reported before the assistant had said anything", got)
	}
	v.SetAssistantSpeaking(true)
	got := echoReturns(v, append(quiet(rate, 300), returningVoice(rate, 600)...))
	if len(got) != 1 {
		t.Fatalf("the voice came back once, 300 ms after it left; measurements: %v", got)
	}
	if math.Abs(got[0]-300) > v.FrameMS() {
		t.Errorf("echo return timed at %.0f ms; the voice came back after 300", got[0])
	}
	if ms, _, ok := v.TakeEchoReturn(); ok {
		t.Errorf("the same measurement (%.0f ms) was handed out a second time", ms)
	}
}

// The level that comes with the time is that of the frame the voice arrived in.
func TestEchoReturnCarriesTheLevelOfWhatCameBack(t *testing.T) {
	const rate = RatePipeline
	v := NewVAD(VADConfig{SampleRate: rate})
	feed(v, quiet(rate, 400))
	v.SetAssistantSpeaking(true)
	feed(v, quiet(rate, 300))
	back := returningVoice(rate, 20)
	v.Push(back)
	_, level, ok := v.TakeEchoReturn()
	if !ok {
		t.Fatal("no echo return was timed")
	}
	if want := DBFS(RMS(back)); math.Abs(level-want) > 0.01 {
		t.Errorf("level reported as %.1f dB; the frame that came back was at %.1f", level, want)
	}
	if level < -36 || level > -24 {
		t.Errorf("the returning voice in this test is at %.1f dB; it is meant to be about -30", level)
	}
}

// A sound after the assistant starts is its echo only if the line was quiet
// before. A caller between two words is not a quiet line, and their next word
// would be timed as an echo that came back in a few tens of milliseconds.
func TestNoEchoReturnIsTimedWhenTheCallerIsMakingSound(t *testing.T) {
	const rate = RatePipeline

	t.Run("in a gap between syllables", func(t *testing.T) {
		v := NewVAD(VADConfig{SampleRate: rate})
		feed(v, append(quiet(rate, 400), tone(rate, 100, 0.45)...))
		feed(v, quiet(rate, 60))
		v.SetAssistantSpeaking(true)
		got := echoReturns(v, append(quiet(rate, 60), tone(rate, 600, 0.45)...))
		if len(got) != 0 {
			t.Fatalf("the caller's next sound was timed as the assistant's echo: %v ms", got)
		}
	})

	t.Run("in a pause inside an utterance that is still open", func(t *testing.T) {
		v := NewVAD(VADConfig{SampleRate: rate})
		feed(v, append(quiet(rate, 400), tone(rate, 600, 0.45)...))
		// Long enough to count as a quiet line, short of ending the utterance.
		feed(v, quiet(rate, 240))
		if v.ActiveMS() == 0 {
			t.Fatal("the utterance has closed; this test is about one that is still open")
		}
		v.SetAssistantSpeaking(true)
		got := echoReturns(v, append(quiet(rate, 60), tone(rate, 600, 0.45)...))
		if len(got) != 0 {
			t.Fatalf("the caller carrying on was timed as the assistant's echo: %v ms", got)
		}
	})
}

// Sound in the very frame the assistant starts in left the caller before the
// assistant had said anything.
func TestASoundInTheFrameTheAssistantStartsInIsNotItsEcho(t *testing.T) {
	const rate = RatePipeline
	v := NewVAD(VADConfig{SampleRate: rate})
	feed(v, quiet(rate, 400))
	v.SetAssistantSpeaking(true)
	if got := echoReturns(v, returningVoice(rate, 600)); len(got) != 0 {
		t.Fatalf("sound that was already arriving was timed as an echo: %v ms", got)
	}
}

// A line with echo cancellation returns nothing. The wait ends, and the caller
// answering two seconds later is not an echo return of two seconds.
func TestNoEchoReturnIsTimedWhenNothingComesBack(t *testing.T) {
	const rate = RatePipeline
	v := NewVAD(VADConfig{SampleRate: rate})
	feed(v, quiet(rate, 400))
	v.SetAssistantSpeaking(true)
	if got := echoReturns(v, quiet(rate, 1600)); len(got) != 0 {
		t.Fatalf("an echo return was timed on a silent line: %v ms", got)
	}
	if got := echoReturns(v, returningVoice(rate, 600)); len(got) != 0 {
		t.Fatalf("a sound after the wait had run out was timed as an echo: %v ms", got)
	}
}

// The origin is the start of the answer. Being told again that the assistant is
// speaking, part-way through, must not move it.
func TestEchoReturnIsTimedFromTheStartOfTheAnswerNotAReminderOfIt(t *testing.T) {
	const rate = RatePipeline
	v := NewVAD(VADConfig{SampleRate: rate})
	feed(v, quiet(rate, 400))
	v.SetAssistantSpeaking(true)
	feed(v, quiet(rate, 100))
	v.SetAssistantSpeaking(true)
	got := echoReturns(v, append(quiet(rate, 200), returningVoice(rate, 600)...))
	if len(got) != 1 {
		t.Fatalf("the voice came back once; measurements: %v", got)
	}
	if math.Abs(got[0]-300) > v.FrameMS() {
		t.Errorf("echo return timed at %.0f ms; the answer began 300 ms before its voice came back", got[0])
	}
}

// echoMisses feeds samples and returns the reason of every attempt to time the
// echo that came to nothing.
func echoMisses(v *VAD, samples []float32) []string {
	var out []string
	frame := v.FrameSamples()
	for off := 0; off+frame <= len(samples); off += frame {
		v.Push(samples[off : off+frame])
		if miss, ok := v.TakeEchoMiss(); ok {
			out = append(out, miss.Reason)
		}
	}
	return out
}

// The first real call with the estimator reported nothing, on a line that
// echoes at -28 dB, and nothing said why. Each attempt that comes to nothing
// now says which of the conditions failed, once.
func TestAnEchoThatIsNotTimedSaysWhy(t *testing.T) {
	const rate = RatePipeline
	one := func(t *testing.T, got []string, want string) {
		t.Helper()
		if len(got) != 1 || !strings.Contains(got[0], want) {
			t.Fatalf("reasons given: %q; want one, about %q", got, want)
		}
	}

	t.Run("the line was not quiet", func(t *testing.T) {
		v := NewVAD(VADConfig{SampleRate: rate})
		feed(v, append(quiet(rate, 400), tone(rate, 100, 0.45)...))
		feed(v, quiet(rate, 60))
		v.SetAssistantSpeaking(true)
		one(t, echoMisses(v, quiet(rate, 400)), "not been quiet")
	})
	t.Run("the caller was mid-utterance", func(t *testing.T) {
		v := NewVAD(VADConfig{SampleRate: rate})
		feed(v, append(quiet(rate, 400), tone(rate, 600, 0.45)...))
		feed(v, quiet(rate, 240))
		v.SetAssistantSpeaking(true)
		one(t, echoMisses(v, quiet(rate, 100)), "mid-utterance")
	})
	t.Run("nothing came back", func(t *testing.T) {
		v := NewVAD(VADConfig{SampleRate: rate})
		feed(v, quiet(rate, 400))
		v.SetAssistantSpeaking(true)
		one(t, echoMisses(v, quiet(rate, 2000)), "nothing came back")
	})
	t.Run("a sound in the frame it began in", func(t *testing.T) {
		v := NewVAD(VADConfig{SampleRate: rate})
		feed(v, quiet(rate, 400))
		v.SetAssistantSpeaking(true)
		one(t, echoMisses(v, returningVoice(rate, 200)), "in the frame the assistant began in")
	})
	t.Run("and a measurement is not a miss", func(t *testing.T) {
		v := NewVAD(VADConfig{SampleRate: rate})
		feed(v, quiet(rate, 400))
		v.SetAssistantSpeaking(true)
		if got := echoMisses(v, append(quiet(rate, 300), returningVoice(rate, 300)...)); len(got) != 0 {
			t.Fatalf("an echo that was timed was also reported as not timed: %q", got)
		}
	})
}
