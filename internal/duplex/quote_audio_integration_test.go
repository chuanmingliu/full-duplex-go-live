package duplex

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/chuanmingliu/golive/internal/provider"
	"github.com/chuanmingliu/golive/internal/tools"
)

type quoteWarmTTS struct {
	key   string
	calls atomic.Int32
}

func (f *quoteWarmTTS) Name() string { return "quote-audio-test" }
func (f *quoteWarmTTS) OpenObserved(ctx context.Context, opts provider.TTSOptions, observe func(int)) (provider.TTSStream, error) {
	observe(0)
	return f.Open(ctx, opts)
}
func (f *quoteWarmTTS) Accounts() int         { return 1 }
func (f *quoteWarmTTS) AccountKey(int) string { return f.key }
func (f *quoteWarmTTS) AccountRPM(int) int    { return 100000 }
func (f *quoteWarmTTS) Open(context.Context, provider.TTSOptions) (provider.TTSStream, error) {
	return nil, fmt.Errorf("foreground should not open during preparation")
}
func (f *quoteWarmTTS) OpenBackground(_ context.Context, opts provider.TTSOptions, _ int) (provider.IdentifiedTTSStream, error) {
	return &quoteWarmStream{parent: f, opts: opts}, nil
}

type quoteWarmStream struct {
	parent *quoteWarmTTS
	opts   provider.TTSOptions
}

func (s *quoteWarmStream) SampleRate() int { return s.opts.SampleRate }
func (s *quoteWarmStream) Close() error    { return nil }
func (s *quoteWarmStream) EffectiveIdentity() provider.AudioIdentity {
	return provider.AudioIdentity{Provider: s.parent.Name(), Account: s.parent.key, Model: "fixture", Voice: s.opts.Voice, Rate: s.opts.SampleRate, Speed: s.opts.Speed, Language: s.opts.Language, Settings: "fixture"}
}
func (s *quoteWarmStream) Synthesize(context.Context, string) (<-chan provider.TTSChunk, error) {
	s.parent.calls.Add(1)
	ch := make(chan provider.TTSChunk, 1)
	ch <- provider.TTSChunk{PCM: []byte{0, 16, 0, 16}}
	close(ch)
	return ch, nil
}

func TestQuoteAudioPreparationHasNoSpeechOrMemorySideEffects(t *testing.T) {
	cfg := testConfig()
	cfg.Duplex.QuoteReadoutWarmMax = 1
	dir := t.TempDir()
	tokenFile := filepath.Join(dir, "token")
	if err := os.WriteFile(tokenFile, []byte("fixture-token"), 0600); err != nil {
		t.Fatal(err)
	}
	cfg.ToolsTokenFile = tokenFile
	qs, err := tools.ParseQuoteSpeech([]byte(`{"prehook":"wait","posthook":"ready","posthook_status":"quote","template":"Total {{ total }} dollars."}`))
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/quote/ready" || r.URL.Query().Get("call") != "out-fixture" || r.Header.Get("Authorization") != "Bearer fixture-token" {
			t.Error("incorrect scoped snapshot request")
		}
		json.NewEncoder(w).Encode(tools.QuoteReadySnapshot{Call: "out-fixture", Quotes: []tools.ReadyQuote{{ID: "result", ExpiresAt: time.Now().Add(time.Minute), Result: json.RawMessage(`{"status":"quote","total":123}`)}}})
	}))
	defer server.Close()
	cfg.ToolsBaseURL = server.URL
	raw := &quoteWarmTTS{key: dir}
	tts := provider.HedgeTTSPool(raw, 2, 100000, 0, provider.TTSPool{}, nil)
	factory, err := provider.BackgroundTTSFor(tts)
	if err != nil {
		t.Fatal(err)
	}
	var emitted atomic.Int32
	e := New(Options{Cfg: cfg, CallID: "out-fixture", Voice: "voice-A"}, Deps{TTS: tts, BackgroundTTS: factory, Emit: func(any) { emitted.Add(1) }})
	e.ctx = context.Background()
	defer e.Close()
	memory := tools.NewMemory(nil)
	before, version := memory.Snapshot()
	e.tools = &toolState{byName: map[string]tools.Tool{"quote_total": (tools.Tool{Name: "quote_total"}).WithSpeech(qs)}, memory: memory}
	e.startQuoteAudio()
	if e.quoteAudio == nil {
		t.Fatal("configured preparation did not start")
	}
	select {
	case <-e.quoteAudio.done:
	case <-time.After(4 * time.Second):
		t.Fatal("bounded preparation did not finish")
	}
	text := firstQuoteSegment(qs.Readout([]byte(`{"status":"quote","total":123}`)), e.segmentConfig())
	if _, ok := e.quoteAudio.get(text); !ok {
		t.Fatal("completed authoritative prefix not available")
	}
	after, afterVersion := memory.Snapshot()
	if emitted.Load() != 0 || string(before) != string(after) || version != afterVersion || raw.calls.Load() != 1 {
		t.Fatal("preparation spoke, mutated memory, or exceeded attempt limit")
	}
}
