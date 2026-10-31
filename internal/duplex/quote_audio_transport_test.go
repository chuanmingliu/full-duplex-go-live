package duplex

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/chuanmingliu/golive/internal/provider"
	"github.com/chuanmingliu/golive/internal/segment"
	"github.com/chuanmingliu/golive/internal/tools"
)

type preparedStreamFixture struct {
	chunks chan provider.TTSChunk
	close  func()
	cancel context.CancelFunc
}

func (f *preparedStreamFixture) TrySynthesize(context.Context, string) (<-chan provider.TTSChunk, bool, error) {
	if f.cancel != nil {
		f.cancel()
	}
	return f.chunks, true, nil
}
func (f *preparedStreamFixture) EffectiveIdentity() (provider.AudioIdentity, bool) {
	return provider.AudioIdentity{Provider: "fake", Voice: "voice-A", Rate: 16000}, true
}
func (f *preparedStreamFixture) Close() error {
	if f.close != nil {
		f.close()
	}
	return nil
}

func TestQuoteAudioCancelledCompletionCannotCachePartialPCM(t *testing.T) {
	// Both a cancelled context and queued data/EOF can be selectable. EOF alone
	// is not evidence of successful synthesis after cancellation.
	for i := 0; i < 512; i++ {
		ctx, cancel := context.WithCancel(context.Background())
		ch := make(chan provider.TTSChunk, 1)
		ch <- provider.TTSChunk{PCM: []byte{1, 2}}
		close(ch)
		speech := &preparedStreamFixture{chunks: ch, cancel: cancel}
		got, attempted, err := prepareQuoteAudio(ctx, speech, "test")
		if !attempted || err == nil || len(got.audio.PCM) > 0 {
			t.Fatal("cancelled channel completion accepted partial PCM")
		}
	}
}

func TestQuoteAudioPreparationRequiresCompleteErrorFreeStream(t *testing.T) {
	for _, lateError := range []bool{false, true} {
		ch := make(chan provider.TTSChunk, 2)
		ch <- provider.TTSChunk{PCM: []byte{1, 2}}
		if lateError {
			ch <- provider.TTSChunk{Err: errors.New("late failure")}
		}
		close(ch)
		got, attempted, err := prepareQuoteAudio(context.Background(), &preparedStreamFixture{chunks: ch}, "test")
		if !attempted || (err != nil) != lateError {
			t.Fatalf("attempted=%v err=%v", attempted, err)
		}
		if lateError && len(got.audio.PCM) > 0 {
			t.Fatal("partial PCM returned")
		}
		if !lateError && (got.audio.Rate != 16000 || len(got.audio.PCM) != 2 || got.identity == "") {
			t.Fatal("complete PCM or identity lost")
		}
	}
}

func TestQuoteAudioSnapshotUsesAuthExactCallAndBounds(t *testing.T) {
	valid := tools.QuoteReadySnapshot{Call: "out-test", Quotes: []tools.ReadyQuote{{ID: "id", ExpiresAt: time.Now().Add(time.Minute), Result: json.RawMessage(`{"status":"quote","total":123}`)}}}
	for _, mode := range []string{"valid", "wrong-call", "too-large", "non-quote", "expired", "redirect"} {
		t.Run(mode, func(t *testing.T) {
			target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { t.Error("authorization followed a redirect") }))
			defer target.Close()
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Header.Get("Authorization") != "Bearer fixture" {
					t.Error("token missing")
				}
				value := valid
				value.Quotes = append([]tools.ReadyQuote(nil), valid.Quotes...)
				switch mode {
				case "wrong-call":
					value.Call = "out-other"
				case "too-large":
					w.Write([]byte(strings.Repeat("x", quoteAudioResponseBytes+1)))
					return
				case "non-quote":
					value.Quotes[0].Result = json.RawMessage(`{"status":"need"}`)
				case "expired":
					value.Quotes[0].ExpiresAt = time.Now().Add(-time.Second)
				case "redirect":
					http.Redirect(w, r, target.URL, http.StatusFound)
					return
				}
				json.NewEncoder(w).Encode(value)
			}))
			defer server.Close()
			client := &http.Client{Timeout: time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
			rows, err := fetchReadyQuoteAudio(context.Background(), client, server.URL, "fixture", "out-test")
			if mode == "valid" {
				if err != nil || len(rows) != 1 {
					t.Fatalf("rows=%d err=%v", len(rows), err)
				}
			} else if len(rows) != 0 {
				t.Fatal("invalid snapshot accepted")
			}
		})
	}
}

func TestQuoteAudioFirstSegmentMatchesNormalSegmenter(t *testing.T) {
	cfg := segment.Config{FirstChunkChars: 6, MinChunkChars: 24, MaxChunkChars: 120}
	for _, text := range []string{"三者险三百万一百元，另一项二百元。", "A complete short quote.", "", strings.Repeat("x", 240)} {
		s := segment.New(cfg)
		parts := s.Push(text)
		if tail := s.FlushFirst(); tail != "" {
			parts = append(parts, tail)
		}
		got := firstQuoteSegment(text, cfg)
		want := ""
		if len(parts) > 0 && len([]rune(strings.TrimSpace(parts[0]))) <= 120 && speakable(parts[0]) {
			want = strings.TrimSpace(parts[0])
		}
		if got != want {
			t.Fatalf("first segment differs: got %q want %q", got, want)
		}
	}
}
