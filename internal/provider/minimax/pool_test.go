package minimax

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/gorilla/websocket"

	"github.com/chuanmingliu/golive/internal/provider"
)

// accountsT2A is a vendor with accounts: it notes which key each connection
// came with, and refuses task_start for any key in noVoice the way the real
// one refuses an account that asks for a voice it does not own.
type accountsT2A struct {
	noVoice map[string]bool

	mu     sync.Mutex
	dials  []string
	voices []string // "key=voice_id", one per task_start
}

func (f *accountsT2A) asked() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.voices...)
}

func (f *accountsT2A) dialled() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.dials...)
}

func newAccountsT2A(t *testing.T, noVoice ...string) (*accountsT2A, string) {
	t.Helper()
	f := &accountsT2A{noVoice: map[string]bool{}}
	for _, k := range noVoice {
		f.noVoice[k] = true
	}
	up := websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		key := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		f.mu.Lock()
		f.dials = append(f.dials, key)
		f.mu.Unlock()
		conn, err := up.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer conn.Close()
		write := func(v any) { data, _ := json.Marshal(v); _ = conn.WriteMessage(websocket.TextMessage, data) }
		write(map[string]any{"event": "connected_success"})
		for {
			_, data, err := conn.ReadMessage()
			if err != nil {
				return
			}
			var in struct {
				Event string `json:"event"`
				Voice struct {
					ID string `json:"voice_id"`
				} `json:"voice_setting"`
			}
			_ = json.Unmarshal(data, &in)
			if in.Event != "task_start" {
				continue
			}
			f.mu.Lock()
			f.voices = append(f.voices, key+"="+in.Voice.ID)
			f.mu.Unlock()
			if f.noVoice[key] {
				write(map[string]any{"event": "task_failed", "base_resp": map[string]any{
					"status_code": 2042, "status_msg": "you don't have access to this voice_id"}})
				return
			}
			write(map[string]any{"event": "task_started"})
		}
	}))
	t.Cleanup(srv.Close)
	return f, "ws" + strings.TrimPrefix(srv.URL, "http") + "/ws/v1/t2a_v2"
}

func poolClient(endpoint string, keys ...string) *TTS {
	c := testClient(endpoint)
	c.APIKey, c.APIKeys, c.rests = keys[0], keys, &keyRests{}
	return c
}

// A session is on the first account unless it is asked for another by lane.
func TestSessionOpensOnTheAccountItsLaneNames(t *testing.T) {
	f, endpoint := newAccountsT2A(t)
	c := poolClient(endpoint, "key-a", "key-b")
	if c.Accounts() != 2 {
		t.Fatalf("Accounts() = %d; want 2", c.Accounts())
	}
	for i, ctx := range []context.Context{
		context.Background(),
		provider.WithLane(context.Background(), 1),
		provider.WithLane(context.Background(), 2),
	} {
		s, err := c.Open(ctx, provider.TTSOptions{SampleRate: 16000})
		if err != nil {
			t.Fatalf("open %d: %v", i, err)
		}
		defer s.Close()
		want := []int{0, 1, 0}[i]
		if got := s.(interface{ Account() int }).Account(); got != want {
			t.Errorf("open %d is on account %d; want %d", i, got+1, want+1)
		}
	}
	if got := strings.Join(f.dialled(), " "); got != "key-a key-b key-a" {
		t.Errorf("the vendor saw the keys %q; want key-a key-b key-a", got)
	}
}

// A cloned voice belongs to the account that made it. An account added to the
// pool without it is refused at task_start — and must cost the session one
// handshake, not its voice, and must not be asked again for a while.
func TestARefusedAccountIsRestedAndTheNextTakesTheSession(t *testing.T) {
	f, endpoint := newAccountsT2A(t, "key-b")
	c := poolClient(endpoint, "key-a", "key-b")

	ctx := provider.WithLane(context.Background(), 1)
	s, err := c.Open(ctx, provider.TTSOptions{SampleRate: 16000})
	if err != nil {
		t.Fatalf("the session failed with the refused account: %v", err)
	}
	defer s.Close()
	if got := s.(interface{ Account() int }).Account(); got != 0 {
		t.Errorf("the session is on account %d; want the first, the one that has the voice", got+1)
	}
	s2, err := c.Open(ctx, provider.TTSOptions{SampleRate: 16000})
	if err != nil {
		t.Fatal(err)
	}
	defer s2.Close()
	if got := strings.Join(f.dialled(), " "); got != "key-b key-a key-a" {
		t.Errorf("the vendor saw the keys %q; want key-b once, then key-a for this session and the next", got)
	}
}

// With nowhere else to go, a refusal is still the caller's error.
func TestARefusalWithNoOtherAccountFailsTheSession(t *testing.T) {
	_, endpoint := newAccountsT2A(t, "key-a")
	c := poolClient(endpoint, "key-a")
	if _, err := c.Open(context.Background(), provider.TTSOptions{SampleRate: 16000}); err == nil {
		t.Fatal("a session opened on an account the vendor refused")
	} else if !strings.Contains(err.Error(), "2042") {
		t.Errorf("the error does not carry the vendor's reason: %v", err)
	}
}

// A cloned voice has a different id on each account that holds a copy of it.
// The configured voice is asked of each account by that account's own name
// for it; a voice the session asked for by some other name is passed on as it
// is.
func TestEachAccountIsAskedForTheVoiceByItsOwnName(t *testing.T) {
	f, endpoint := newAccountsT2A(t)
	c := poolClient(endpoint, "key-a", "key-b")
	c.VoiceID, c.VoiceIDs = "voice-on-a", []string{"", "voice-on-b"}

	second := provider.WithLane(context.Background(), 1)
	for _, open := range []struct {
		ctx   context.Context
		voice string
	}{
		{context.Background(), ""},
		{second, ""},
		{second, "voice-on-a"}, // the gateway naming the configured voice
		{second, "someone-else"},
	} {
		s, err := c.Open(open.ctx, provider.TTSOptions{SampleRate: 16000, Voice: open.voice})
		if err != nil {
			t.Fatal(err)
		}
		defer s.Close()
	}
	want := "key-a=voice-on-a key-b=voice-on-b key-b=voice-on-b key-b=someone-else"
	if got := strings.Join(f.asked(), " "); got != want {
		t.Errorf("the vendor was asked for\n  %s\nwant\n  %s", got, want)
	}
	if got := c.OnAccount(1).VoiceID; got != "voice-on-b" {
		t.Errorf("the second account by itself is configured with voice %q; want its own", got)
	}
}

// The second account's settings are written beside the first's, numbered.
func TestNumberedSettingsPairAKeyWithItsVoice(t *testing.T) {
	t.Setenv("MINIMAX_TTS_API_KEY", "key-a")
	t.Setenv("MINIMAX_TTS_VOICE_ID", "voice-on-a")
	t.Setenv("MINIMAX_TTS_API_KEY_2", "key-b")
	t.Setenv("MINIMAX_TTS_VOICE_ID_2", "voice-on-b")
	t.Setenv("MINIMAX_TTS_API_KEY_3", "key-c") // an account with no voice of its own
	t.Setenv("MINIMAX_TTS_VOICE_ID_4", "nobody's")
	t.Setenv("MINIMAX_TTS_API_KEYS", "")
	t.Setenv("MINIMAX_TTS_API_KEYS_FILE", "")
	c, err := New()
	if err != nil {
		t.Fatal(err)
	}
	if c.Accounts() != 3 {
		t.Fatalf("%d accounts; want 3", c.Accounts())
	}
	for n, want := range []string{"voice-on-a", "voice-on-b", "voice-on-a"} {
		if got := c.voiceFor(n, ""); got != want {
			t.Errorf("account %d is asked for %q; want %q", n+1, got, want)
		}
	}
	if !c.OwnVoice(1) || c.OwnVoice(2) {
		t.Errorf("OwnVoice: second %v, third %v; want true, false", c.OwnVoice(1), c.OwnVoice(2))
	}
}

// And a field left empty for the owner to fill in is no account at all.
func TestAnEmptyNumberedKeyIsNotAnAccount(t *testing.T) {
	t.Setenv("MINIMAX_TTS_API_KEY", "key-a")
	t.Setenv("MINIMAX_TTS_VOICE_ID", "voice-on-a")
	t.Setenv("MINIMAX_TTS_API_KEY_2", "")
	t.Setenv("MINIMAX_TTS_VOICE_ID_2", "")
	t.Setenv("MINIMAX_TTS_API_KEYS", "")
	t.Setenv("MINIMAX_TTS_API_KEYS_FILE", "")
	c, err := New()
	if err != nil {
		t.Fatal(err)
	}
	if c.Accounts() != 1 || c.voiceFor(0, "") != "voice-on-a" {
		t.Errorf("%d accounts, voice %q; want the one account and its voice", c.Accounts(), c.voiceFor(0, ""))
	}
}
