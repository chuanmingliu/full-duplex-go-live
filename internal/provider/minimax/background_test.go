package minimax

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
	"github.com/gorilla/websocket"
)

func TestBackgroundOpenIsStrictAndKeepsRealIdentity(t *testing.T) {
	f, endpoint := newAccountsT2A(t, "key-b")
	c := poolClient(endpoint, "key-a", "key-b")
	c.VoiceIDs = []string{"", "voice-b"}
	if _, err := c.OpenBackground(context.Background(), provider.TTSOptions{}, 1); err == nil {
		t.Fatal("refused account succeeded")
	}
	if got := strings.Join(f.dialled(), ","); got != "key-b" {
		t.Fatalf("strict open failed over: %s", got)
	}
	s, err := c.OpenBackground(context.Background(), provider.TTSOptions{SampleRate: 48000, Model: "ignored", Language: "ignored"}, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	id := s.EffectiveIdentity()
	if id.Rate != 24000 || id.Model != c.Model || id.Language != c.LanguageBoost || id.Voice != c.VoiceID || id.Account != c.AccountKey(0) {
		t.Fatalf("wrong effective identity: %+v", id)
	}
	encoded, _ := json.Marshal(id)
	if strings.Contains(string(encoded), "key-a") || strings.Contains(string(encoded), "key-b") {
		t.Fatal("identity leaked credential")
	}
	other := *c
	other.APIKeys = []string{"key-b", "key-a"}
	if other.AccountKey(1) != c.AccountKey(0) {
		t.Fatal("account identity depends on pool position")
	}
	other.Endpoint += "_bidi"
	if other.AccountKey(1) != c.AccountKey(0) {
		t.Fatal("endpoint protocol split the account allowance")
	}
}

func TestBackgroundNeverReconnects(t *testing.T) {
	f, endpoint := newFakeT2A(t, false)
	c := testClient(endpoint)
	s, err := c.OpenBackground(context.Background(), provider.TTSOptions{}, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	raw := s.(*stream)
	raw.dropConn()
	if _, err = s.Synthesize(context.Background(), "test."); err == nil {
		t.Fatal("dead background connection reconnected")
	}
	if err = raw.Prewarm(context.Background()); err == nil {
		t.Fatal("prewarm reconnected")
	}
	starts := 0
	for _, e := range f.events() {
		if e == "task_start" {
			starts++
		}
	}
	if starts != 1 {
		t.Fatalf("task starts: %d", starts)
	}
}

func TestBackgroundIdentityIncludesEffectiveSettings(t *testing.T) {
	_, endpoint := newFakeT2A(t, false)
	c := testClient(endpoint)
	s, err := c.OpenBackground(context.Background(), provider.TTSOptions{}, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	before := s.EffectiveIdentity()
	c.Pitch = 2
	next, err := c.OpenBackground(context.Background(), provider.TTSOptions{}, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer next.Close()
	if before.Settings == next.EffectiveIdentity().Settings {
		t.Fatal("effective voice setting missing from identity")
	}
	if s.EffectiveIdentity() != before {
		t.Fatal("identity changed after opening")
	}
}

func TestBackgroundCancellationClosesBlockedRead(t *testing.T) {
	entered, release := make(chan struct{}), make(chan struct{})
	f := &fakeT2A{beforeContinue: func() { close(entered); <-release }}
	defer close(release)
	c := testClient(serveFakeT2A(t, f))
	c.RPMs = []int{100}
	h := provider.HedgeTTSPool(c, 2, 100, time.Second, provider.TTSPool{AccountRPM: 100}, nil)
	factory, err := provider.BackgroundTTSFor(h)
	if err != nil {
		t.Fatal(err)
	}
	b := factory.New(context.Background(), provider.TTSOptions{})
	defer b.Close()
	ctx, cancel := context.WithCancel(context.Background())
	ch, attempted, err := b.TrySynthesize(ctx, "test.")
	if err != nil || !attempted {
		t.Fatal(err)
	}
	<-entered
	cancel()
	timer := time.NewTimer(time.Second)
	defer timer.Stop()
	seen := false
	for {
		select {
		case v, ok := <-ch:
			if !ok {
				if !seen {
					t.Fatal("cancelled audio looked complete")
				}
				return
			}
			if v.Err != nil {
				seen = true
			}
		case <-timer.C:
			t.Fatal("cancel left read blocked")
		}
	}
}

func TestBackgroundCanceledBeforeOpenDoesNotDial(t *testing.T) {
	f, endpoint := newFakeT2A(t, false)
	c := testClient(endpoint)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := c.OpenBackground(ctx, provider.TTSOptions{}, 0); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancel: %v", err)
	}
	if len(f.events()) != 0 {
		t.Fatal("cancelled open reached provider")
	}
}

func TestBackgroundCloseDuringHandshake(t *testing.T) {
	accepted := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		up := websocket.Upgrader{}
		c, err := up.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer c.Close()
		close(accepted)
		// No greeting: opening is blocked on a socket read until Close.
		_, _, _ = c.ReadMessage()
	}))
	defer server.Close()
	c := testClient("ws" + strings.TrimPrefix(server.URL, "http"))
	c.RPMs = []int{100}
	c.OpenTimeout = 10 * time.Second
	factory, err := provider.BackgroundTTSFor(provider.HedgeTTSPool(c, 2, 100, 0, provider.TTSPool{}, nil))
	if err != nil {
		t.Fatal(err)
	}
	b := factory.New(context.Background(), provider.TTSOptions{})
	defer b.Close()
	done := make(chan error, 1)
	go func() {
		_, attempted, err := b.TrySynthesize(context.Background(), "test.")
		if !attempted {
			done <- errors.New("open attempt not counted")
			return
		}
		done <- err
	}()
	<-accepted
	b.Close()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("closed handshake succeeded")
		}
	case <-time.After(time.Second):
		t.Fatal("close did not unblock handshake")
	}
	if _, valid := b.EffectiveIdentity(); valid {
		t.Fatal("failed opening produced valid identity")
	}
}
