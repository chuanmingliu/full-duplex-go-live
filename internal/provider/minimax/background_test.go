package minimax

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/chuanmingliu/golive/internal/provider"
	"github.com/gorilla/websocket"
)

var backgroundTestSequence atomic.Uint64

func backgroundTestClient(endpoint string) *TTS {
	c := testClient(endpoint)
	// Admission follows the credential across factories and endpoint variants.
	// Every fixture invocation, including -count repeats, needs its own account.
	c.APIKey = "background-test-" + strconv.FormatUint(backgroundTestSequence.Add(1), 10)
	return c
}

type heldBackgroundClose struct {
	*TTS
	closing, release chan struct{}
}

func (c *heldBackgroundClose) OpenBackground(ctx context.Context, opts provider.TTSOptions, account int) (provider.IdentifiedTTSStream, error) {
	s, err := c.TTS.OpenBackground(ctx, opts, account)
	if err != nil {
		return nil, err
	}
	return &heldBackgroundStream{IdentifiedTTSStream: s, closing: c.closing, release: c.release}, nil
}

type heldBackgroundStream struct {
	provider.IdentifiedTTSStream
	closing, release chan struct{}
}

func (s *heldBackgroundStream) Close() error {
	close(s.closing)
	<-s.release
	return s.IdentifiedTTSStream.Close()
}

func backgroundFixture(t *testing.T, c provider.TTS) *provider.BackgroundTTSStream {
	t.Helper()
	factory, err := provider.BackgroundTTSFor(provider.HedgeTTSPool(c, 2, 100, 0, provider.TTSPool{AccountRPM: 100}, nil))
	if err != nil {
		t.Fatal(err)
	}
	b := factory.New(context.Background(), provider.TTSOptions{})
	t.Cleanup(func() { _ = b.Close() })
	return b
}

func TestBackgroundFixturesDoNotShareClosingAccount(t *testing.T) {
	_, endpoint := newFakeT2A(t, false)
	first := backgroundTestClient(endpoint)
	closing, release := make(chan struct{}), make(chan struct{})
	defer close(release)
	b := backgroundFixture(t, &heldBackgroundClose{TTS: first, closing: closing, release: release})
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	ch, attempted, err := b.TrySynthesize(ctx, "test.")
	if err != nil || !attempted {
		t.Fatalf("first fixture: attempted=%t, err=%v", attempted, err)
	}
	for chunk := range ch {
		if chunk.Err != nil {
			t.Fatal(chunk.Err)
		}
	}
	b.Close()
	select {
	case <-closing:
	case <-ctx.Done():
		t.Fatal("fixture close did not start")
	}
	// Production must keep a real account reserved until its raw Close finishes.
	// Only two of its 100 budget units have been spent, so this denial is activity.
	sameAccount := testClient(endpoint)
	sameAccount.APIKey = first.APIKey
	if _, attempted, err := backgroundFixture(t, sameAccount).TrySynthesize(ctx, "test."); attempted || !errors.Is(err, provider.ErrBackgroundUnavailable) {
		t.Fatalf("closing account was reused: attempted=%t, err=%v", attempted, err)
	}
	// A separate test invocation must not inherit that real account reservation.
	next := backgroundFixture(t, backgroundTestClient(endpoint))
	ch, attempted, err = next.TrySynthesize(ctx, "test.")
	if err != nil || !attempted {
		t.Fatalf("independent fixture inherited closing account: attempted=%t, err=%v", attempted, err)
	}
	for chunk := range ch {
		if chunk.Err != nil {
			t.Fatal(chunk.Err)
		}
	}
}

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
	c := backgroundTestClient(serveFakeT2A(t, f))
	c.RPMs = []int{100}
	h := provider.HedgeTTSPool(c, 2, 100, time.Second, provider.TTSPool{AccountRPM: 100}, nil)
	factory, err := provider.BackgroundTTSFor(h)
	if err != nil {
		t.Fatal(err)
	}
	b := factory.New(context.Background(), provider.TTSOptions{})
	defer b.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ch, attempted, err := b.TrySynthesize(ctx, "test.")
	if err != nil || !attempted {
		t.Fatal(err)
	}
	select {
	case <-entered:
	case chunk, ok := <-ch:
		t.Fatalf("synthesis completed before fixture entry: open=%t, err=%v", ok, chunk.Err)
	case <-time.After(2 * time.Second):
		t.Fatal("synthesis did not reach fixture")
	}
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
	c := backgroundTestClient("ws" + strings.TrimPrefix(server.URL, "http"))
	c.RPMs = []int{100}
	c.OpenTimeout = 10 * time.Second
	factory, err := provider.BackgroundTTSFor(provider.HedgeTTSPool(c, 2, 100, 0, provider.TTSPool{}, nil))
	if err != nil {
		t.Fatal(err)
	}
	b := factory.New(context.Background(), provider.TTSOptions{})
	defer b.Close()
	type openResult struct {
		attempted bool
		err       error
	}
	done := make(chan openResult, 1)
	go func() {
		_, attempted, err := b.TrySynthesize(context.Background(), "test.")
		done <- openResult{attempted: attempted, err: err}
	}()
	select {
	case <-accepted:
	case result := <-done:
		t.Fatalf("open completed before handshake: attempted=%t, err=%v", result.attempted, result.err)
	case <-time.After(2 * time.Second):
		t.Fatal("background connection did not reach handshake")
	}
	b.Close()
	select {
	case result := <-done:
		if !result.attempted || result.err == nil {
			t.Fatalf("closed handshake: attempted=%t, err=%v", result.attempted, result.err)
		}
	case <-time.After(time.Second):
		t.Fatal("close did not unblock handshake")
	}
	if _, valid := b.EffectiveIdentity(); valid {
		t.Fatal("failed opening produced valid identity")
	}
}
