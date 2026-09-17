package minimax

import (
	"encoding/json"
	"sync"
	"testing"
	"time"
)

// TestEndsSentenceDecidesWhoFlushes covers the one thing task_flush turns on.
//
// MiniMax synthesizes a buffered segment immediately when it ends in
// sentence-ending punctuation and otherwise waits for more text. golive's first
// segment of a turn is deliberately six characters long, to get a syllable out
// early, and six characters usually end in nothing at all — so the segment that
// exists purely to be fast is the one the server holds on to. Getting this
// predicate wrong either wastes that (no flush) or flushes every sentence
// needlessly.
func TestEndsSentenceDecidesWhoFlushes(t *testing.T) {
	complete := []string{
		"好的。",
		"明天下午两点。",
		"要我先占住吗？",
		"Sure.",
		"Really?",
		"Okay!",
		"好的。”", // punctuation inside a closing quote still ends the sentence
		"是的。）",
		"第一行\n",
	}
	for _, s := range complete {
		if !endsSentence(s) {
			t.Errorf("endsSentence(%q) = false; this would flush a segment the server was about to synthesize anyway", s)
		}
	}

	partial := []string{
		"",
		"好的",
		"明天下午",
		"我看一下",   // a holding filler: no punctuation, and the most latency-sensitive text there is
		"两点到四点，", // a comma only accumulates; the server waits
		"Sure",
		"well,",
	}
	for _, s := range partial {
		if endsSentence(s) {
			t.Errorf("endsSentence(%q) = true; this segment would sit in the server's buffer unflushed", s)
		}
	}
}

// TestTaskStartOmitsUnsetExpressiveness pins the difference between "not
// configured" and "configured to nothing".
//
// Sending emotion:"" would pin every sentence of a call to an empty mood rather
// than letting MiniMax choose per sentence, which is the more natural default
// across a whole conversation. The same argument applies to the other optional
// blocks: absent is a different instruction from empty.
func TestTaskStartOmitsUnsetExpressiveness(t *testing.T) {
	bare := &stream{
		tts:   &TTS{Model: "speech-2.8-turbo", LanguageBoost: "auto"},
		rate:  24000,
		voice: "v1",
		speed: 1.2,
	}
	start := bare.taskStart()
	for _, key := range []string{"pronunciation_dict"} {
		if _, ok := start[key]; ok {
			t.Errorf("task_start carries %q when it was never configured", key)
		}
	}
	voice, _ := start["voice_setting"].(map[string]any)
	for _, key := range []string{"emotion", "voice_modify", "timber_weights"} {
		if _, ok := voice[key]; ok {
			t.Errorf("voice_setting carries %q when it was never configured", key)
		}
	}
	// vol has a documented range of (0,10], so zero is not a legal value to
	// send — an unset field must become the default, not 0.
	if got := voice["vol"]; got != 1.0 {
		t.Errorf("vol = %v, want the default 1.0; zero is outside the documented range", got)
	}

	full := &stream{
		tts: &TTS{
			Model:             "speech-2.8-turbo",
			LanguageBoost:     "Chinese",
			Emotion:           "calm",
			Vol:               2,
			Pitch:             -1,
			ContinuousSound:   true,
			VoiceModify:       map[string]any{"intensity": 20},
			PronunciationTone: []string{"golive/勾莱夫"},
			TimberWeights:     []map[string]any{{"voice_id": "a", "weight": 60}},
		},
		rate:  24000,
		voice: "v1",
		speed: 1,
	}
	start = full.taskStart()
	if start["continuous_sound"] != true {
		t.Error("continuous_sound did not reach task_start")
	}
	dict, _ := start["pronunciation_dict"].(map[string]any)
	if dict == nil || len(dict["tone"].([]string)) != 1 {
		t.Errorf("pronunciation_dict = %v, want one tone entry", start["pronunciation_dict"])
	}
	voice, _ = start["voice_setting"].(map[string]any)
	if voice["emotion"] != "calm" || voice["vol"] != 2.0 || voice["pitch"] != -1.0 {
		t.Errorf("voice_setting = %v", voice)
	}
	if _, ok := voice["voice_modify"]; !ok {
		t.Error("voice_modify did not reach voice_setting")
	}
	// And the whole thing must survive encoding: a map with a value the JSON
	// encoder rejects would fail at send time, on a live call.
	if _, err := json.Marshal(start); err != nil {
		t.Fatalf("task_start is not encodable: %v", err)
	}
}

// TestBidiOnlyFeaturesAreGatedByTheEndpoint is the regression test for shipping
// task_flush and task_cancel without noticing they live on a different URL.
//
// MiniMax has two WebSocket T2A endpoints that differ by one suffix, share auth
// and task_start, and return identical audio frames. Only /ws/v1/t2a_v2_bidi
// accepts task_flush and task_cancel; on the plain one they come back as 2202
// illegal event. Sending them there breaks a turn, and it breaks it in a way
// that reads as a synthesis fault rather than a URL that is one word short.
func TestBidiOnlyFeaturesAreGatedByTheEndpoint(t *testing.T) {
	cases := []struct {
		endpoint string
		wantBidi bool
	}{
		{"wss://api.minimaxi.com/ws/v1/t2a_v2", false},
		{"wss://api.minimax.io/ws/v1/t2a_v2", false},
		{"wss://api.minimaxi.com/ws/v1/t2a_v2_bidi", true},
		{"wss://api.minimax.io/ws/v1/t2a_v2_bidi", true},
	}
	for _, c := range cases {
		// Both features asked for, as the defaults ask for them.
		tts := &TTS{
			Endpoint:             c.endpoint,
			FlushPartialSegments: true,
			CancelOnAbandon:      true,
			warnOnce:             &sync.Once{},
		}
		if got := tts.Bidi(); got != c.wantBidi {
			t.Errorf("Bidi(%q) = %v, want %v", c.endpoint, got, c.wantBidi)
		}
		if got := tts.bidiOnly("task_flush"); got != c.wantBidi {
			t.Errorf("bidiOnly on %q = %v; a feature the endpoint rejects must not be sent",
				c.endpoint, got)
		}
	}

	// A client built with no warnOnce must not panic: ttsprobe copies a client
	// per variant, and a test can build one as a literal.
	bare := &TTS{Endpoint: "wss://example.invalid/ws/v1/t2a_v2"}
	if bare.bidiOnly("task_cancel") {
		t.Error("a non-bidi endpoint allowed a bidi-only feature")
	}
}

func TestKeepAliveIsOptional(t *testing.T) {
	s := &stream{tts: &TTS{KeepAliveEvery: 0}}
	s.startKeepAlive()
	if s.stopKA != nil {
		t.Fatal("a zero interval must start no goroutine")
	}
	s.KeepAlive() // must not panic on a stream with no connection
	_ = s.Close()

	s = &stream{tts: &TTS{KeepAliveEvery: time.Minute}}
	s.startKeepAlive()
	if s.stopKA == nil {
		t.Fatal("expected a keepalive goroutine")
	}
	// Close must stop it, and twice must be safe: the engine closes a stream it
	// is also dropping.
	_ = s.Close()
	_ = s.Close()
}
