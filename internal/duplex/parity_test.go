package duplex

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/chuanmingliu/golive/internal/config"
	"github.com/chuanmingliu/golive/internal/live"
	"github.com/chuanmingliu/golive/internal/provider"
	"github.com/chuanmingliu/golive/internal/provider/mock"
)

// --- harness ---

// recordingLLM passes requests through and keeps a copy of each.
type recordingLLM struct {
	inner provider.LLM
	mu    sync.Mutex
	reqs  []provider.LLMRequest
}

func (r *recordingLLM) Name() string { return "recording" }

func (r *recordingLLM) Stream(ctx context.Context, req provider.LLMRequest) (<-chan provider.LLMDelta, error) {
	r.mu.Lock()
	kept := req
	kept.Messages = append([]provider.Message(nil), req.Messages...)
	r.reqs = append(r.reqs, kept)
	r.mu.Unlock()
	return r.inner.Stream(ctx, req)
}

func (r *recordingLLM) requests() []provider.LLMRequest {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]provider.LLMRequest(nil), r.reqs...)
}

// sequenceASR hears a different phrase on each utterance, in order, and the
// last one from then on.
type sequenceASR struct {
	mu      sync.Mutex
	phrases []string
	n       int
}

func (s *sequenceASR) Name() string { return "sequence" }

func (s *sequenceASR) Open(ctx context.Context, opts provider.ASROptions) (provider.ASRStream, error) {
	s.mu.Lock()
	i := s.n
	if i >= len(s.phrases) {
		i = len(s.phrases) - 1
	}
	s.n++
	s.mu.Unlock()
	asr := mock.NewASR(s.phrases[i])
	asr.VoicedOnly = true
	return asr.Open(ctx, opts)
}

// voicedASR is the mock recognizer hearing only voiced audio, which is what
// makes its hypothesis stop growing when the speaker does.
func voicedASR(phrase string) *mock.ASR {
	asr := mock.NewASR(phrase)
	asr.VoicedOnly = true
	return asr
}

func toolLLM() *mock.LLM {
	llm := mock.NewLLM()
	llm.DelayPerRune = time.Millisecond
	llm.UseTools = true
	return llm
}

var lookupTool = json.RawMessage(`{"type":"function","function":{"name":"lookup"}}`)

func startEngine(t *testing.T, opts Options, deps Deps) *Engine {
	t.Helper()
	if deps.TTS == nil {
		deps.TTS = mock.NewTTS()
	}
	if opts.ClientRate == 0 {
		opts.ClientRate = opts.Cfg.ClientRate
	}
	if opts.SessionID == "" {
		opts.SessionID = "test"
	}
	e := New(opts, deps)
	ctx, cancel := context.WithCancel(context.Background())
	e.Start(ctx)
	t.Cleanup(func() { e.Close(); cancel() })
	return e
}

// functionCalls returns the function_call items announced in response.event.
func (c *collector) functionCalls() []struct{ CallID, Name, Arguments, DelegationID string } {
	c.mu.Lock()
	defer c.mu.Unlock()
	var out []struct{ CallID, Name, Arguments, DelegationID string }
	for _, ev := range c.events {
		env, ok := ev.(live.ResponseEventEnvelope)
		if !ok {
			continue
		}
		var inner struct {
			Type string `json:"type"`
			Item struct {
				Type      string `json:"type"`
				CallID    string `json:"call_id"`
				Name      string `json:"name"`
				Arguments string `json:"arguments"`
			} `json:"item"`
		}
		if json.Unmarshal(env.Event, &inner) != nil {
			continue
		}
		if inner.Type == "response.output_item.done" && inner.Item.Type == "function_call" {
			out = append(out, struct{ CallID, Name, Arguments, DelegationID string }{
				inner.Item.CallID, inner.Item.Name, inner.Item.Arguments, env.DelegationID,
			})
		}
	}
	return out
}

func (c *collector) delegations() []live.DelegationCreatedEvent {
	c.mu.Lock()
	defer c.mu.Unlock()
	var out []live.DelegationCreatedEvent
	for _, ev := range c.events {
		if d, ok := ev.(live.DelegationCreatedEvent); ok {
			out = append(out, d)
		}
	}
	return out
}

func (c *collector) errorCodes() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	var out []string
	for _, ev := range c.events {
		if e, ok := ev.(live.ErrorEvent); ok {
			out = append(out, e.Error.Code)
		}
	}
	return out
}

func (c *collector) allOutput() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	var b strings.Builder
	for _, ev := range c.events {
		if d, ok := ev.(live.TranscriptDelta); ok && d.Type == live.ServerOutputTranscriptDelta {
			b.WriteString(d.Delta)
		}
	}
	return b.String()
}

// assertWellFormed checks a conversation against the rules every chat backend
// enforces on function calls: a tool result follows the assistant message that
// asked for it, every call gets a result, and one role does not speak twice
// running where that is refused.
func assertWellFormed(t *testing.T, msgs []provider.Message) {
	t.Helper()
	for i, m := range msgs {
		switch {
		case m.Role == provider.RoleTool:
			j := i - 1
			for j >= 0 && msgs[j].Role == provider.RoleTool {
				j--
			}
			if j < 0 || msgs[j].Role != provider.RoleAssistant {
				t.Fatalf("message %d is a tool result with no assistant call in front of it:\n%s", i, dump(msgs))
			}
			found := false
			for _, c := range msgs[j].ToolCalls {
				if c.ID == m.ToolCallID {
					found = true
				}
			}
			if !found {
				t.Fatalf("message %d answers call %q, which the assistant message before it never made:\n%s",
					i, m.ToolCallID, dump(msgs))
			}
		case len(m.ToolCalls) > 0:
			for k, c := range m.ToolCalls {
				at := i + 1 + k
				if at >= len(msgs) || msgs[at].Role != provider.RoleTool {
					t.Fatalf("message %d calls %q and no result follows it:\n%s", i, c.ID, dump(msgs))
				}
			}
		case m.Role == provider.RoleUser && i > 0 && msgs[i-1].Role == provider.RoleUser:
			t.Fatalf("messages %d and %d are both from the user:\n%s", i-1, i, dump(msgs))
		}
	}
}

func dump(msgs []provider.Message) string {
	var b strings.Builder
	for i, m := range msgs {
		b.WriteString("  ")
		b.WriteString(string(rune('0' + i%10)))
		b.WriteString(" ")
		b.WriteString(m.Role)
		if len(m.ToolCalls) > 0 {
			b.WriteString(" calls")
			for _, c := range m.ToolCalls {
				b.WriteString(" " + c.ID)
			}
		}
		if m.ToolCallID != "" {
			b.WriteString(" answers " + m.ToolCallID)
		}
		b.WriteString(": " + m.Content + "\n")
	}
	return b.String()
}

func parityConfig() config.Config {
	cfg := testConfig()
	cfg.Duplex.Speculative = true
	return cfg
}

// --- speculation that knows when it was wrong ---

// TestSpeculationIsAbandonedWhenTheSpeakerCarriesOn covers a pause in the
// middle of a sentence, which is where guessing used to go wrong twice over.
//
// The guess made in the pause was left running when the speaker carried on:
// nothing noticed until the final transcript disagreed with it, and by then it
// could be talking over them. And the watch had fired its one shot, so the
// pause that really did end the turn got no head start at all. Both halves are
// asserted here — a second guess is made at the real end, and the first leaves
// nothing behind on the standard protocol.
func TestSpeculationIsAbandonedWhenTheSpeakerCarriesOn(t *testing.T) {
	cfg := parityConfig()
	c := newCollector(cfg.ClientRate)
	llm := mock.NewLLM()
	llm.DelayPerRune = 2 * time.Millisecond
	e := startEngine(t, Options{Cfg: cfg, Delegation: live.DelegationResponses, Speculative: true},
		Deps{ASR: voicedASR("你好帮我查一下明天的天气"), LLM: llm, Emit: c.emit})

	speak(e, cfg.ClientRate, 1200)
	pause(e, cfg.ClientRate, 300) // long enough to guess, too short to end the turn
	waitFor(t, "a guess inside the mid-sentence pause", 2*time.Second, func() bool {
		return len(c.speculativeHints()) == 1
	})
	if n := c.count(live.ServerDelegationCreated); n != 0 {
		t.Fatalf("a delegation was announced on a guess, before the turn was confirmed")
	}
	// No audio is being pushed, so the detector's clock is stopped mid-pause
	// while the backend and the synthesizer — both instant here — finish the
	// whole answer to half a sentence. None of it may be heard.
	time.Sleep(300 * time.Millisecond)
	if ms, said := c.audioMS(), c.allOutput(); ms != 0 || said != "" {
		t.Fatalf("the answer to a guess was played before the turn was confirmed: %v ms, %q", ms, said)
	}

	speak(e, cfg.ClientRate, 900)
	pause(e, cfg.ClientRate, 700)

	waitFor(t, "a second guess at the real end of the turn", 3*time.Second, func() bool {
		return len(c.speculativeHints()) >= 2
	})
	waitFor(t, "the confirmed turn's delegation", 3*time.Second, func() bool {
		return c.count(live.ServerDelegationCreated) > 0
	})
	time.Sleep(300 * time.Millisecond)

	hints := c.speculativeHints()
	if len([]rune(hints[1])) <= len([]rune(hints[0])) {
		t.Errorf("the second guess %q should be on more of the sentence than the first %q", hints[1], hints[0])
	}
	ds := c.delegations()
	if len(ds) != 1 {
		t.Fatalf("%d delegations were announced; the abandoned guess must leave none, and the turn exactly one", len(ds))
	}
	if ds[0].Delegation.Transcript != hints[1] {
		t.Errorf("the delegation is for %q; want the whole sentence %q", ds[0].Delegation.Transcript, hints[1])
	}
	if ds[0].Delegation.Reason != "speculative" {
		t.Errorf("the turn was answered from %q; the second guess should have held", ds[0].Delegation.Reason)
	}
	if c.count(live.ServerError) != 0 {
		t.Errorf("errors: %v", c.errorCodes())
	}

	// What is heard is the one answer, to the whole sentence, and it starts
	// after the turn it answers was announced.
	waitFor(t, "the answer", 3*time.Second, func() bool { return c.audioMS() > 0 })
	created := c.eventIndex(func(a any) bool { _, ok := a.(live.DelegationCreatedEvent); return ok })
	audible := c.eventIndex(func(a any) bool { _, ok := a.(live.OutputAudioDelta); return ok })
	if audible < created {
		t.Errorf("audio (event %d) went out before the turn was confirmed (event %d)", audible, created)
	}
	if said := c.allOutput(); strings.Count(said, "我听到你说") != 1 || !strings.Contains(said, hints[1]) {
		t.Errorf("want one answer, to %q; heard %q", hints[1], said)
	}
	// And the abandoned guess left nothing in the conversation: it opens with
	// the caller's sentence, whole, not with a fragment of an answer to half
	// of it.
	history := e.History()
	assertWellFormed(t, history)
	if len(history) == 0 || history[0].Role != provider.RoleUser || history[0].Content != hints[1] {
		t.Errorf("the conversation should open with the caller's whole sentence:\n%s", dump(history))
	}
}

// TestSpeculativeFunctionCallWaitsForTheTurnToBeConfirmed is the rule that makes
// speculation safe next to tools.
//
// A guess can be abandoned; a function call an application has run cannot. The
// backend is started on the guess all the same — that is the head start — but
// what it asks for is not announced until the caller's turn is certain.
func TestSpeculativeFunctionCallWaitsForTheTurnToBeConfirmed(t *testing.T) {
	cfg := parityConfig()
	c := newCollector(cfg.ClientRate)
	e := startEngine(t, Options{
		Cfg: cfg, Delegation: live.DelegationResponses, Speculative: true,
		Tools: []json.RawMessage{lookupTool},
	}, Deps{ASR: voicedASR("你好帮我查一下明天的天气"), LLM: toolLLM(), Emit: c.emit})

	// "你好帮我查一" is already enough for the mock to call the tool.
	speak(e, cfg.ClientRate, 1200)
	pause(e, cfg.ClientRate, 300)
	waitFor(t, "a guess inside the pause", 2*time.Second, func() bool {
		return len(c.speculativeHints()) == 1
	})
	time.Sleep(150 * time.Millisecond) // the mock backend has long since made its call
	if n := len(c.functionCalls()); n != 0 {
		t.Fatalf("a function call was announced on a guess: %+v", c.functionCalls())
	}
	// Nor is the line the model says alongside the call: "let me check" is a
	// promise too.
	if said := c.allOutput(); said != "" || c.audioMS() != 0 {
		t.Fatalf("the holding line was spoken on a guess: %q", said)
	}

	speak(e, cfg.ClientRate, 900)
	pause(e, cfg.ClientRate, 700)
	waitFor(t, "the confirmed turn's function call", 4*time.Second, func() bool {
		return len(c.functionCalls()) > 0
	})
	time.Sleep(300 * time.Millisecond)

	calls := c.functionCalls()
	if len(calls) != 1 {
		t.Fatalf("%d function calls were announced, want exactly the confirmed turn's: %+v", len(calls), calls)
	}
	if !strings.Contains(calls[0].Arguments, "明天") {
		t.Errorf("the call is for the half sentence, not the whole one: %s", calls[0].Arguments)
	}
	created := c.eventIndex(func(a any) bool { _, ok := a.(live.DelegationCreatedEvent); return ok })
	called := c.eventIndex(func(a any) bool {
		env, ok := a.(live.ResponseEventEnvelope)
		return ok && strings.Contains(string(env.Event), "function_call")
	})
	if created < 0 || called < created {
		t.Errorf("the function call (event %d) arrived before its delegation was announced (event %d)", called, created)
	}
}

// TestShortReplyStartsTheBackendWhenTheTurnCloses covers the turns that never
// speculated at all: too short to guess on mid-pause, so they waited for the
// recognizer's final transcript before anything started. The last partial is
// that transcript in everything but punctuation by the time the turn closes.
func TestShortReplyStartsTheBackendWhenTheTurnCloses(t *testing.T) {
	cfg := parityConfig()
	c := newCollector(cfg.ClientRate)
	asr := voicedASR("北京")
	asr.FinalDelay = 300 * time.Millisecond
	llm := mock.NewLLM()
	llm.DelayPerRune = 2 * time.Millisecond
	e := startEngine(t, Options{Cfg: cfg, Delegation: live.DelegationResponses, Speculative: true},
		Deps{ASR: asr, LLM: llm, Emit: c.emit})

	speak(e, cfg.ClientRate, 500)
	pause(e, cfg.ClientRate, 1000)
	waitFor(t, "the turn's delegation", 3*time.Second, func() bool {
		return c.count(live.ServerDelegationCreated) > 0
	})

	hint := c.eventIndex(func(a any) bool { _, ok := a.(live.SpeculativeDelegationEvent); return ok })
	final := c.eventIndex(func(a any) bool {
		d, ok := a.(live.TranscriptDelta)
		return ok && d.Type == live.ServerInputTranscriptDelta && d.Final
	})
	if hint < 0 {
		t.Fatal("the backend was not started until the final transcript arrived")
	}
	if final < 0 || hint > final {
		t.Fatalf("work began at event %d, after the final transcript at event %d; the wait for it bought nothing",
			hint, final)
	}
	c.mu.Lock()
	lead := c.at[final].Sub(c.at[hint])
	c.mu.Unlock()
	if lead < 200*time.Millisecond {
		t.Errorf("head start over the final transcript was %v; the recognizer takes 300 ms to send it", lead)
	}
	if got := c.delegations()[0].Delegation.Reason; got != "speculative" {
		t.Errorf("delegation reason is %q, want speculative", got)
	}
}

// TestPauseMidThoughtIsNotTakenAsTheEndOfTheTurn is semantic endpointing: the
// same half-second pause closes the turn after a finished-sounding sentence and
// does not after "我想问一下那个".
func TestPauseMidThoughtIsNotTakenAsTheEndOfTheTurn(t *testing.T) {
	run := func(t *testing.T, semantic bool) int {
		cfg := parityConfig()
		cfg.Duplex.SemanticEndpointing = semantic
		cfg.Duplex.PatientSilenceMS = 900
		c := newCollector(cfg.ClientRate)
		llm := mock.NewLLM()
		llm.DelayPerRune = 2 * time.Millisecond
		e := startEngine(t, Options{Cfg: cfg, Delegation: live.DelegationResponses, Speculative: true},
			Deps{ASR: voicedASR("我想问一下那个保单的事情"), LLM: llm, Emit: c.emit})

		speak(e, cfg.ClientRate, 1300) // "我想问一下那个"
		pause(e, cfg.ClientRate, 560)  // longer than vad.min_silence_ms
		return c.count(live.ExtSpeechStopped)
	}
	if n := run(t, true); n != 0 {
		t.Errorf("the turn was taken %d time(s) while the caller was mid-thought", n)
	}
	if n := run(t, false); n != 1 {
		t.Errorf("with semantic endpointing off the fixed threshold should have closed the turn once; got %d", n)
	}
}

// --- the recognizer's connection ---

type slowOpenASR struct {
	inner provider.ASR
	delay time.Duration
	mu    sync.Mutex
	opens []time.Time
}

func (s *slowOpenASR) Name() string { return "slow-open" }

func (s *slowOpenASR) Open(ctx context.Context, opts provider.ASROptions) (provider.ASRStream, error) {
	s.mu.Lock()
	s.opens = append(s.opens, time.Now())
	s.mu.Unlock()
	select {
	case <-time.After(s.delay):
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	return s.inner.Open(ctx, opts)
}

func (s *slowOpenASR) firstOpen() (time.Time, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.opens) == 0 {
		return time.Time{}, false
	}
	return s.opens[0], true
}

// TestRecognizerIsDialledBeforeTheUtteranceIsConfirmed pins two things about a
// recognizer whose handshake is slow: it starts before the detector is sure,
// and none of the audio spoken while it was connecting is lost or repeated.
func TestRecognizerIsDialledBeforeTheUtteranceIsConfirmed(t *testing.T) {
	cfg := parityConfig()
	cfg.Duplex.Speculative = false
	c := newCollector(cfg.ClientRate)
	asr := &slowOpenASR{inner: voicedASR("你好帮我查一下明天的天气"), delay: 250 * time.Millisecond}
	llm := mock.NewLLM()
	llm.DelayPerRune = 2 * time.Millisecond
	e := startEngine(t, Options{Cfg: cfg, Delegation: live.DelegationResponses},
		Deps{ASR: asr, LLM: llm, Emit: c.emit})

	speak(e, cfg.ClientRate, 800)
	pause(e, cfg.ClientRate, 700)
	waitFor(t, "a final transcript", 3*time.Second, func() bool {
		return c.eventIndex(func(a any) bool {
			d, ok := a.(live.TranscriptDelta)
			return ok && d.Type == live.ServerInputTranscriptDelta && d.Final
		}) >= 0
	})

	opened, ok := asr.firstOpen()
	if !ok {
		t.Fatal("the recognizer was never dialled")
	}
	started := c.eventIndex(func(a any) bool {
		s, ok := a.(live.SpeechEvent)
		return ok && s.Type == live.ExtSpeechStarted
	})
	c.mu.Lock()
	declared := c.at[started]
	c.mu.Unlock()
	if !opened.Before(declared) {
		t.Errorf("the recognizer was dialled %v after the utterance was declared; it should start on the first "+
			"voiced audio, a detector's worth of time earlier", opened.Sub(declared))
	}
	// 800 ms of voice at 180 ms a character is four characters, exactly. More
	// would mean audio was sent twice; fewer, that the handshake swallowed some.
	if got := c.inputTranscript(); got != "你好帮我" {
		t.Errorf("heard %q; want exactly the 800 ms that were spoken, 你好帮我", got)
	}
}

// --- the caller's transcript ---

// revisingASR emits a scripted run of hypotheses, one per chunk of audio, and
// a final one when the utterance ends. The second corrects itself, the way a
// streaming recognizer does.
type revisingASR struct {
	steps []string
	final string
}

func (r *revisingASR) Name() string { return "revising" }

func (r *revisingASR) Open(ctx context.Context, _ provider.ASROptions) (provider.ASRStream, error) {
	return &revisingStream{asr: r, results: make(chan provider.ASRResult, 16)}, nil
}

type revisingStream struct {
	asr     *revisingASR
	results chan provider.ASRResult
	mu      sync.Mutex
	bytes   int
	sent    int
	closed  bool
}

func (s *revisingStream) Write(pcm []byte) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil
	}
	s.bytes += len(pcm)
	// One hypothesis for every 250 ms of audio at 16 kHz.
	for s.sent < len(s.asr.steps) && s.bytes >= (s.sent+1)*8000 {
		s.results <- provider.ASRResult{Text: s.asr.steps[s.sent]}
		s.sent++
	}
	return nil
}

func (s *revisingStream) Results() <-chan provider.ASRResult { return s.results }

func (s *revisingStream) CloseSend() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil
	}
	s.closed = true
	s.results <- provider.ASRResult{Text: s.asr.final, Final: true}
	close(s.results)
	return nil
}

func (s *revisingStream) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.closed {
		s.closed = true
		close(s.results)
	}
	return nil
}

// TestInputTranscriptIsAppendOnly is the gpt-live-1 contract for the caller's
// captions: a client concatenates fragments and never has to take one back.
//
// The recognizer here mishears a word and corrects it. The mistake must show
// up where corrections are possible — the golive extension that carries the
// whole hypothesis — and never in the standard stream, where it could not be
// undone.
func TestInputTranscriptIsAppendOnly(t *testing.T) {
	cfg := parityConfig()
	cfg.Duplex.Speculative = false
	c := newCollector(cfg.ClientRate)
	asr := &revisingASR{
		steps: []string{"今天", "今天天七", "今天天气", "今天天气怎么"},
		final: "今天天气怎么样",
	}
	llm := mock.NewLLM()
	llm.DelayPerRune = 2 * time.Millisecond
	e := startEngine(t, Options{Cfg: cfg, Delegation: live.DelegationResponses},
		Deps{ASR: asr, LLM: llm, Emit: c.emit})

	speak(e, cfg.ClientRate, 1500)
	pause(e, cfg.ClientRate, 700)
	waitFor(t, "the final fragment", 3*time.Second, func() bool {
		return c.eventIndex(func(a any) bool {
			d, ok := a.(live.TranscriptDelta)
			return ok && d.Type == live.ServerInputTranscriptDelta && d.Final
		}) >= 0
	})

	if got := c.inputTranscript(); got != asr.final {
		t.Errorf("the appended fragments read %q; want %q", got, asr.final)
	}
	c.mu.Lock()
	fragments, sawMistake, lastWhole := 0, false, ""
	for _, ev := range c.events {
		switch v := ev.(type) {
		case live.TranscriptDelta:
			if v.Type == live.ServerInputTranscriptDelta {
				fragments++
				if strings.Contains(v.Delta, "七") {
					t.Errorf("the misheard word reached the append-only stream in %q", v.Delta)
				}
			}
		case live.InputTranscriptEvent:
			if strings.Contains(v.Text, "七") {
				sawMistake = true
			}
			lastWhole = v.Text
		}
	}
	c.mu.Unlock()
	if fragments < 2 {
		t.Errorf("the transcript arrived in %d fragment(s); settled text should stream before the utterance ends", fragments)
	}
	if !sawMistake {
		t.Error("the revisable transcript never showed the hypothesis the recognizer later corrected")
	}
	if lastWhole != asr.final {
		t.Errorf("the revisable transcript ended on %q; want %q", lastWhole, asr.final)
	}
}

// TestOutputTranscriptCarriesOnlyWhatWasSpoken: the assistant's transcript is
// reported as each sentence starts to play, so a reply cut short leaves the
// unspoken rest of it out of the record.
func TestOutputTranscriptCarriesOnlyWhatWasSpoken(t *testing.T) {
	cfg := parityConfig()
	cfg.Duplex.OnNewQuery = "cut"
	c := newCollector(cfg.ClientRate)
	e := newSlowEngine(t, cfg, c)

	speak(e, cfg.ClientRate, 1200)
	pause(e, cfg.ClientRate, 700)
	waitFor(t, "assistant audio", 6*time.Second, func() bool { return c.audioMS() > 300 })
	speak(e, cfg.ClientRate, 900)
	waitFor(t, "the interruption", 4*time.Second, func() bool {
		return c.count(live.ExtAudioTruncated) > 0
	})

	full := mock.Reply("你好帮我查一下明")
	said := c.outputTranscript("item_1")
	if said == "" {
		t.Fatal("nothing was reported as said, though audio played")
	}
	if len(said) >= len(mock.Reply("")) && strings.Contains(said, "测试打断") {
		t.Errorf("the transcript holds the whole reply, including what was never spoken: %q", said)
	}
	if !strings.HasPrefix(normalizeForCompare(full), normalizeForCompare(said)) &&
		!strings.HasPrefix(normalizeForCompare(mock.Reply("你好帮我查一下明天的天气")), normalizeForCompare(said)) {
		// The exact user text depends on how much the mock recognizer heard;
		// what matters is that what was reported is the start of the reply.
		if !strings.HasPrefix(said, "好的") {
			t.Errorf("the transcript %q is not the beginning of the reply", said)
		}
	}
	heard := c.find(func(a any) bool { _, ok := a.(live.AudioTruncatedEvent); return ok }).(live.AudioTruncatedEvent)
	if !strings.HasPrefix(normalizeForCompare(said), normalizeForCompare(heard.Text)) {
		t.Errorf("heard %q is not within what was reported as said, %q", heard.Text, said)
	}
}

// --- function calls ---

func TestFunctionCallRoundTrip(t *testing.T) {
	cfg := parityConfig()
	cfg.Duplex.Speculative = false
	c := newCollector(cfg.ClientRate)
	llm := &recordingLLM{inner: toolLLM()}
	e := startEngine(t, Options{
		Cfg: cfg, Delegation: live.DelegationResponses,
		Backend: BackendOptions{Tools: []json.RawMessage{lookupTool}, Instructions: "只报天气。"},
	}, Deps{ASR: voicedASR("帮我查一下明天的天气"), LLM: llm, Emit: c.emit})

	speak(e, cfg.ClientRate, 2000)
	pause(e, cfg.ClientRate, 700)
	waitFor(t, "a function call", 4*time.Second, func() bool { return len(c.functionCalls()) == 1 })
	call := c.functionCalls()[0]
	if call.Name != "lookup" {
		t.Fatalf("called %q, want lookup", call.Name)
	}
	delegation := c.delegations()[0].Delegation
	if call.DelegationID != delegation.ID {
		t.Errorf("the call is reported under %q, the delegation is %q", call.DelegationID, delegation.ID)
	}
	if delegation.Type != "delegation" || delegation.Target != live.DelegationResponses || delegation.ResponseID == "" {
		t.Errorf("delegation is malformed: %+v", delegation)
	}

	// response.create before the result is in is the application's mistake,
	// and it is told so rather than left waiting.
	e.ContinueResponse("early")
	waitFor(t, "an error for the missing result", 2*time.Second, func() bool {
		for _, code := range c.errorCodes() {
			if code == "tool_outputs_missing" {
				return true
			}
		}
		return false
	})

	e.SubmitItem(live.ResponseItem{Type: "function_call_output", CallID: call.CallID, Output: "明天上海晴，二十度"}, "out")
	e.ContinueResponse("go")
	waitFor(t, "the result to be read back", 6*time.Second, func() bool {
		return strings.Contains(c.allOutput(), "查到了")
	})

	reqs := llm.requests()
	if len(reqs) != 2 {
		t.Fatalf("the backend was called %d times, want 2", len(reqs))
	}
	if sys := reqs[0].Messages[0]; sys.Role != provider.RoleSystem || !strings.Contains(sys.Content, "只报天气。") {
		t.Errorf("delegation.responses.instructions did not reach the backend: %+v", sys)
	}
	second := reqs[1].Messages
	assertWellFormed(t, second)
	n := len(second)
	if n < 3 || second[n-1].Role != provider.RoleTool || second[n-1].ToolCallID != call.CallID ||
		len(second[n-2].ToolCalls) != 1 || second[n-2].ToolCalls[0].ID != call.CallID {
		t.Fatalf("the continuation does not end in the call and its result:\n%s", dump(second))
	}
	sawQuestion := false
	for _, m := range second {
		if m.Role == provider.RoleUser && strings.Contains(m.Content, "帮我查一下明天的天气") {
			sawQuestion = true
		}
	}
	if !sawQuestion {
		t.Errorf("the continuation has lost the question the call was made for:\n%s", dump(second))
	}
	// The continuation is the same delegation, not a new one.
	if ds := c.delegations(); ds[len(ds)-1].Delegation.ID != delegation.ID {
		t.Errorf("the continuation was announced as %q; it belongs to %q", ds[len(ds)-1].Delegation.ID, delegation.ID)
	}
}

// TestConversationContinuesWhileAFunctionIsPending: the caller keeps talking
// while a function runs, and nothing about the pending call leaks into the
// requests made in the meantime.
func TestConversationContinuesWhileAFunctionIsPending(t *testing.T) {
	cfg := parityConfig()
	cfg.Duplex.Speculative = false
	c := newCollector(cfg.ClientRate)
	llm := &recordingLLM{inner: toolLLM()}
	e := startEngine(t, Options{
		Cfg: cfg, Delegation: live.DelegationResponses, Tools: []json.RawMessage{lookupTool},
	}, Deps{
		ASR: &sequenceASR{phrases: []string{"帮我查一下保单", "今天星期几"}},
		LLM: llm, Emit: c.emit,
	})

	speak(e, cfg.ClientRate, 1300)
	pause(e, cfg.ClientRate, 700)
	waitFor(t, "a function call", 4*time.Second, func() bool { return len(c.functionCalls()) == 1 })
	call := c.functionCalls()[0]
	waitFor(t, "the holding line to finish", 4*time.Second, func() bool { return !e.assistantHasFloor() })

	// The application is still working. The caller asks something else.
	speak(e, cfg.ClientRate, 1000)
	pause(e, cfg.ClientRate, 700)
	waitFor(t, "an answer to the second question", 4*time.Second, func() bool {
		return len(llm.requests()) == 2
	})
	between := llm.requests()[1].Messages
	assertWellFormed(t, between)
	for _, m := range between {
		if m.Role == provider.RoleTool || len(m.ToolCalls) > 0 {
			t.Fatalf("a call with no result yet is in the conversation:\n%s", dump(between))
		}
	}
	waitFor(t, "the second answer to finish", 20*time.Second, func() bool { return !e.assistantHasFloor() })

	// Now the result arrives, late, and is still read back.
	e.SubmitItem(live.ResponseItem{Type: "function_call_output", CallID: call.CallID, Output: "保单有效"}, "")
	e.ContinueResponse("")
	waitFor(t, "the late result to be read back", 6*time.Second, func() bool {
		return strings.Contains(c.allOutput(), "查到了，保单有效")
	})
	assertWellFormed(t, llm.requests()[2].Messages)
	assertWellFormed(t, e.History())
}

// TestUnansweredWordsAreCarriedIntoTheNextTurn: a caller who goes on after a
// pause that closed the turn is asking one question, not two, and the backend
// has to see all of it.
func TestUnansweredWordsAreCarriedIntoTheNextTurn(t *testing.T) {
	cfg := parityConfig()
	cfg.Duplex.Speculative = false
	c := newCollector(cfg.ClientRate)
	llm := &recordingLLM{inner: &slowBackend{delay: 4 * time.Second}}
	e := startEngine(t, Options{Cfg: cfg, Delegation: live.DelegationResponses}, Deps{
		ASR: &sequenceASR{phrases: []string{"我想查一下保单", "就是去年买的那个"}},
		LLM: llm, Emit: c.emit,
	})

	speak(e, cfg.ClientRate, 1300)
	pause(e, cfg.ClientRate, 600)
	speak(e, cfg.ClientRate, 1500)
	pause(e, cfg.ClientRate, 700)
	waitFor(t, "the second request to the backend", 4*time.Second, func() bool {
		return len(llm.requests()) == 2
	})

	msgs := llm.requests()[1].Messages
	assertWellFormed(t, msgs)
	last := msgs[len(msgs)-1]
	if last.Role != provider.RoleUser ||
		!strings.Contains(last.Content, "我想查一下保单") || !strings.Contains(last.Content, "就是去年买的那个") {
		t.Fatalf("the backend was asked %q; the first half of the request is missing:\n%s", last.Content, dump(msgs))
	}
}

func TestTypedInputIsAnsweredLikeASpokenTurn(t *testing.T) {
	cfg := parityConfig()
	c := newCollector(cfg.ClientRate)
	llm := &recordingLLM{inner: mock.NewLLM()}
	e := startEngine(t, Options{Cfg: cfg, Delegation: live.DelegationResponses},
		Deps{ASR: voicedASR("你好"), LLM: llm, Emit: c.emit})

	e.SubmitItem(live.ResponseItem{
		Type: "message", Role: "user", Content: live.TextContent("input_text", "明天上海的天气"),
	}, "item")
	time.Sleep(300 * time.Millisecond)
	if n := len(llm.requests()); n != 0 {
		t.Fatalf("typed input was answered before response.create asked for it")
	}

	e.ContinueResponse("create")
	waitFor(t, "an answer", 4*time.Second, func() bool { return c.audioMS() > 200 })
	if got := c.delegations()[0].Delegation.Reason; got != "typed_input" {
		t.Errorf("delegation reason is %q, want typed_input", got)
	}
	msgs := llm.requests()[0].Messages
	if last := msgs[len(msgs)-1]; last.Role != provider.RoleUser || last.Content != "明天上海的天气" {
		t.Errorf("the backend was asked %+v", last)
	}

	e.SubmitItem(live.ResponseItem{Type: "image", Role: "user"}, "bad")
	waitFor(t, "an error for an unsupported item", 2*time.Second, func() bool {
		for _, code := range c.errorCodes() {
			if code == "unsupported_item" {
				return true
			}
		}
		return false
	})
}

// --- client delegation ---

// TestClientDelegationIsAnnouncedOnlyOnceTheTurnIsCertain: a delegation is a
// promise gpt-live-1 never takes back, so the standard event waits for the
// caller's turn to be confirmed. The hint that goes out early says what it is.
func TestClientDelegationIsAnnouncedOnlyOnceTheTurnIsCertain(t *testing.T) {
	cfg := parityConfig()
	c := newCollector(cfg.ClientRate)
	e := startEngine(t, Options{Cfg: cfg, Delegation: live.DelegationClient, Speculative: true},
		Deps{ASR: voicedASR("你好帮我查一下明天的天气"), LLM: mock.NewLLM(), Emit: c.emit})

	speak(e, cfg.ClientRate, 1200)
	pause(e, cfg.ClientRate, 300)
	waitFor(t, "an early hint", 2*time.Second, func() bool { return len(c.speculativeHints()) == 1 })
	abandoned := c.find(func(a any) bool { _, ok := a.(live.SpeculativeDelegationEvent); return ok }).(live.SpeculativeDelegationEvent)

	// An application quick enough to act on the hint answers the guess before
	// the caller has finished. That answer is to a question nobody asked.
	e.AppendCommentary(abandoned.DelegationID, "这是对半句话的回答。", "eager")

	speak(e, cfg.ClientRate, 900)
	pause(e, cfg.ClientRate, 700)
	waitFor(t, "the delegation", 3*time.Second, func() bool { return c.count(live.ServerDelegationCreated) > 0 })
	time.Sleep(300 * time.Millisecond)

	ds := c.delegations()
	if len(ds) != 1 {
		t.Fatalf("%d delegations announced, want 1", len(ds))
	}
	d := ds[0]
	if d.Delegation.Target != live.DelegationClient || d.Delegation.Type != "delegation" || d.OffsetMS <= 0 {
		t.Errorf("delegation is malformed: %+v", d)
	}
	if d.Delegation.ID == abandoned.DelegationID {
		t.Errorf("the confirmed delegation reuses the id of the guess that was abandoned")
	}
	if c.audioMS() != 0 {
		t.Errorf("golive spoke %v ms by itself; in this mode the application does the talking", c.audioMS())
	}

	// A result for the guess that was abandoned has nothing to attach to.
	e.AppendCommentary(abandoned.DelegationID, "这是给一个不存在的委托的回答。", "stale")
	waitFor(t, "the stale result to be refused", 2*time.Second, func() bool {
		for _, code := range c.errorCodes() {
			if code == "unknown_delegation_id" {
				return true
			}
		}
		return false
	})
	if c.audioMS() != 0 {
		t.Error("a result for an abandoned guess was spoken")
	}

	e.AppendCommentary(d.Delegation.ID, "明天上海晴。", "real")
	waitFor(t, "the application's answer to be spoken", 4*time.Second, func() bool {
		return strings.Contains(c.allOutput(), "明天上海晴")
	})
	if said := c.allOutput(); strings.Contains(said, "半句话") {
		t.Errorf("the answer to an abandoned guess was spoken: %q", said)
	}
	ack := c.find(func(a any) bool {
		v, ok := a.(live.AckEvent)
		return ok && v.Type == live.ServerCommentaryAppended && v.ClientEventID == "real"
	}).(live.AckEvent)
	if ack.ClientEventID != "real" || ack.DelegationID != d.Delegation.ID || ack.StartMS == nil {
		t.Errorf("acknowledgement is malformed: %+v", ack)
	}
}

// TestCommentaryWaitsForTheCallerToFinish: an application's result that lands
// while the caller is mid-sentence is held until they stop.
func TestCommentaryWaitsForTheCallerToFinish(t *testing.T) {
	cfg := parityConfig()
	cfg.Duplex.Speculative = false
	c := newCollector(cfg.ClientRate)
	e := startEngine(t, Options{Cfg: cfg, Delegation: live.DelegationClient},
		Deps{ASR: voicedASR("你好帮我查一下明天的天气"), LLM: mock.NewLLM(), Emit: c.emit})

	speak(e, cfg.ClientRate, 1200)
	pause(e, cfg.ClientRate, 700)
	waitFor(t, "the delegation", 3*time.Second, func() bool { return c.count(live.ServerDelegationCreated) > 0 })
	id := c.delegations()[0].Delegation.ID

	done := make(chan struct{})
	go func() {
		defer close(done)
		speak(e, cfg.ClientRate, 1600)
	}()
	time.Sleep(600 * time.Millisecond)
	e.AppendCommentary(id, "查到了，明天上海晴。", "")
	e.AppendCommentary(id, "气温二十度。", "")
	<-done
	if c.audioMS() != 0 {
		t.Fatalf("%v ms of audio went out over the caller's own sentence", c.audioMS())
	}

	pause(e, cfg.ClientRate, 700)
	waitFor(t, "both updates, once the caller had stopped", 6*time.Second, func() bool {
		return strings.Contains(c.allOutput(), "气温二十度")
	})
	said := c.allOutput()
	if a, b := strings.Index(said, "明天上海晴"), strings.Index(said, "气温二十度"); a < 0 || b < a {
		t.Errorf("two updates sent in order were not said whole and in order: %q", said)
	}
}

// TestClientAutoAnswersSmallTalkAndDelegatesWork: in auto mode the backend
// plays gpt-live-1's conversational model — it talks, and it decides when the
// application is needed.
func TestClientAutoAnswersSmallTalkAndDelegatesWork(t *testing.T) {
	cfg := parityConfig()
	cfg.Duplex.Speculative = false
	c := newCollector(cfg.ClientRate)
	llm := &recordingLLM{inner: toolLLM()}
	e := startEngine(t, Options{
		Cfg: cfg, Delegation: live.DelegationClient, ClientDelegation: "auto",
	}, Deps{
		ASR: &sequenceASR{phrases: []string{"你好呀", "帮我查一下保单"}},
		LLM: llm, Emit: c.emit,
	})

	speak(e, cfg.ClientRate, 600)
	pause(e, cfg.ClientRate, 700)
	waitFor(t, "an answer to small talk", 4*time.Second, func() bool { return c.audioMS() > 200 })
	if n := c.count(live.ServerDelegationCreated); n != 0 {
		t.Fatalf("small talk was delegated to the application")
	}
	req := llm.requests()[0]
	if len(req.Tools) != 1 || !strings.Contains(string(req.Tools[0]), `"delegate"`) {
		t.Fatalf("the conversational model was not offered the delegate function: %s", req.Tools)
	}
	if c.count(live.ServerResponseEvent) != 0 {
		t.Error("the conversational model's own stream was reported as a backend response")
	}
	waitFor(t, "the answer to finish", 12*time.Second, func() bool { return !e.assistantHasFloor() })

	speak(e, cfg.ClientRate, 1300)
	pause(e, cfg.ClientRate, 700)
	waitFor(t, "the model to delegate", 4*time.Second, func() bool { return c.count(live.ServerDelegationCreated) == 1 })
	d := c.delegations()[0]
	if d.Delegation.Target != live.DelegationClient || d.Delegation.Reason != "model" ||
		!strings.Contains(d.Delegation.Request, "保单") {
		t.Errorf("delegation is malformed: %+v", d.Delegation)
	}
	waitFor(t, "the holding line", 4*time.Second, func() bool {
		return strings.Contains(c.allOutput(), "我查一下")
	})
	waitFor(t, "the holding line to finish", 4*time.Second, func() bool { return !e.assistantHasFloor() })

	e.AppendCommentary(d.Delegation.ID, "保单有效期到明年三月。", "")
	waitFor(t, "the application's result", 4*time.Second, func() bool {
		return strings.Contains(c.allOutput(), "明年三月")
	})
	waitFor(t, "it to finish", 4*time.Second, func() bool { return !e.assistantHasFloor() })
	time.Sleep(200 * time.Millisecond)
	assertWellFormed(t, e.History())
}

// --- the phrase cache ---

type countingTTS struct {
	inner provider.TTS
	mu    sync.Mutex
	texts []string
}

func (c *countingTTS) Name() string { return c.inner.Name() }

func (c *countingTTS) Open(ctx context.Context, opts provider.TTSOptions) (provider.TTSStream, error) {
	s, err := c.inner.Open(ctx, opts)
	if err != nil {
		return nil, err
	}
	return &countingStream{TTSStream: s, parent: c}, nil
}

func (c *countingTTS) synthesized() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]string(nil), c.texts...)
}

type countingStream struct {
	provider.TTSStream
	parent *countingTTS
}

func (s *countingStream) Synthesize(ctx context.Context, text string) (<-chan provider.TTSChunk, error) {
	s.parent.mu.Lock()
	s.parent.texts = append(s.parent.texts, text)
	s.parent.mu.Unlock()
	return s.TTSStream.Synthesize(ctx, text)
}

// TestGreetingPlaysFromThePhraseCache: the greeting is the same sentence on
// every call, so after the first time it is not synthesized again.
func TestGreetingPlaysFromThePhraseCache(t *testing.T) {
	Phrases().Reset()
	t.Cleanup(Phrases().Reset)

	cfg := parityConfig()
	cfg.Greeting = "你好，我是语音助手，有什么可以帮你的？"
	tts := &countingTTS{inner: mock.NewTTS()}

	added, err := WarmPhrases(context.Background(), cfg, tts, nil)
	if err != nil || added == 0 {
		t.Fatalf("warming the cache added %d phrases, err %v", added, err)
	}
	warmed := len(tts.synthesized())

	for i := 0; i < 2; i++ {
		c := newCollector(cfg.ClientRate)
		e := startEngine(t, Options{
			Cfg: cfg, Delegation: live.DelegationResponses, Greeting: cfg.Greeting, Language: cfg.Language,
		}, Deps{ASR: voicedASR("你好"), LLM: mock.NewLLM(), TTS: tts, Emit: c.emit})
		e.Greet()
		waitFor(t, "the greeting", 3*time.Second, func() bool {
			return strings.Contains(c.allOutput(), "有什么可以帮你的")
		})
		if c.audioMS() <= 0 {
			t.Fatal("the greeting was reported but no audio went out")
		}
	}
	if got := len(tts.synthesized()); got != warmed {
		t.Errorf("the greeting was synthesized %d more time(s) after it was cached: %v",
			got-warmed, tts.synthesized()[warmed:])
	}

	// A different voice is different audio, and is not served the cached one.
	c := newCollector(cfg.ClientRate)
	e := startEngine(t, Options{
		Cfg: cfg, Delegation: live.DelegationResponses, Greeting: cfg.Greeting,
		Language: cfg.Language, Voice: "another-voice",
	}, Deps{ASR: voicedASR("你好"), LLM: mock.NewLLM(), TTS: tts, Emit: c.emit})
	e.Greet()
	waitFor(t, "the greeting in another voice", 3*time.Second, func() bool { return c.audioMS() > 0 })
	if got := len(tts.synthesized()); got == warmed {
		t.Error("a session with a different voice was played the default voice's cached greeting")
	}
}

// --- history ---

func TestHistoryTrimKeepsFunctionExchangesWhole(t *testing.T) {
	cfg := parityConfig()
	cfg.HistoryTurns = 0
	cfg.HistoryMaxChars = 60
	e := New(Options{Cfg: cfg, SessionID: "trim"}, Deps{Emit: func(any) {}})

	long := strings.Repeat("长", 30)
	e.history = []provider.Message{
		{Role: provider.RoleUser, Content: long},
		{Role: provider.RoleAssistant, ToolCalls: []provider.ToolCall{{ID: "c1", Name: "lookup"}}},
		{Role: provider.RoleTool, ToolCallID: "c1", Content: long},
		{Role: provider.RoleAssistant, Content: "查到了。"},
		{Role: provider.RoleUser, Content: "谢谢"},
	}
	e.trimHistory()
	assertWellFormed(t, e.history)
	if len(e.history) == 0 || e.history[0].Role == provider.RoleTool {
		t.Fatalf("the trim left a function result with no call in front of it:\n%s", dump(e.history))
	}
}

// TestHistoryTrimsInStepsSoThePrefixHolds: trimming to exactly the limit on
// every turn changes the front of the prompt on every request, which defeats a
// provider's prefix cache on precisely the long prompts it exists for.
func TestHistoryTrimsInStepsSoThePrefixHolds(t *testing.T) {
	cfg := parityConfig()
	cfg.HistoryTurns = 0
	cfg.HistoryMaxChars = 200
	e := New(Options{Cfg: cfg, SessionID: "trim"}, Deps{Emit: func(any) {}})

	turn := func(i int) {
		e.history = append(e.history,
			provider.Message{Role: provider.RoleUser, Content: strings.Repeat("问", 10)},
			provider.Message{Role: provider.RoleAssistant, Content: strings.Repeat("答", 10)})
		e.trimHistory()
	}
	trims := 0
	for i := 0; i < 40; i++ {
		before := len(e.history)
		turn(i)
		if len(e.history) < before+2 {
			trims++
		}
		chars := 0
		for _, m := range e.history {
			chars += len([]rune(m.Content))
		}
		if chars > cfg.HistoryMaxChars {
			t.Fatalf("history is %d characters after a trim; the limit is %d", chars, cfg.HistoryMaxChars)
		}
	}
	if trims == 0 {
		t.Fatal("history was never trimmed")
	}
	if trims > 15 {
		t.Errorf("history was trimmed on %d of 40 turns; it should be cut back in steps, not every turn", trims)
	}
	if got := e.UsageEvent().ContextWindow.UsageRatio; got <= 0 || got > 1 {
		t.Errorf("context usage ratio is %v; want a fraction of the limit", got)
	}
}
