package minimax

import (
	"context"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"github.com/chuanmingliu/golive/internal/provider"
)

// A pool of accounts.
//
// A synthesis account has a requests-a-minute allowance, and one phone call
// uses most of one: sixteen to twenty-two segments a minute were measured on
// calls, before a second request for any of them. So the cure for a slow first
// audio — ask twice, play whichever speaks first — was something a single
// account could afford only for the odd segment. A second account is a second
// allowance, and a second place for a request to go that shares no queue with
// the first.
//
// Keys come from MINIMAX_TTS_API_KEY, MINIMAX_TTS_API_KEYS and
// MINIMAX_TTS_API_KEYS_FILE (config.EnvKeys). A session is opened on the
// account its context names (provider.WithLane; lane n is account n, modulo
// the pool) or, named nothing, on the first: the one there has always been.
// Who asks for which lane, and how much each account is trusted with, is
// provider.HedgeTTS's business; this file only keeps accounts apart.
//
// Every account must be able to speak in the configured voice. A cloned or
// designed voice belongs to the account that made it, and another account
// asking for it is refused at task_start — which rests that key (below) and
// leaves the pool as it was before the key was added.

func (t *TTS) keys() []string {
	if len(t.APIKeys) > 0 {
		return t.APIKeys
	}
	return []string{t.APIKey}
}

// Accounts implements provider.Pooled.
func (t *TTS) Accounts() int { return len(t.keys()) }

// OnAccount returns a client like this one whose pool is only account n
// (from 0): for measuring one account by itself.
func (t *TTS) OnAccount(n int) *TTS {
	keys := t.keys()
	c := *t
	c.APIKey, c.APIKeys, c.rests = keys[n%len(keys)], nil, &keyRests{}
	return &c
}

// Account reports which account of the pool this session is on, from 0.
func (s *stream) Account() int { return s.account }

// keyRests is when each account may be used again, by its place in the pool.
type keyRests struct {
	mu    sync.Mutex
	until map[int]time.Time
}

func (r *keyRests) resting(n int, now time.Time) bool {
	if r == nil {
		return false
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	return now.Before(r.until[n])
}

func (r *keyRests) set(n int, until time.Time) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.until == nil {
		r.until = map[int]time.Time{}
	}
	r.until[n] = until
}

// order is the accounts to try for a new session, most wanted first: the one
// the context names, then the others in turn, leaving out any that is resting.
// If every account is resting it is the wanted one alone, since a refusal is
// no worse than not asking.
func (t *TTS) order(ctx context.Context) []int {
	n := len(t.keys())
	want := 0
	if lane, ok := provider.Lane(ctx); ok {
		if lane < 0 {
			lane = -lane
		}
		want = lane % n
	}
	now := time.Now()
	var out []int
	for i := 0; i < n; i++ {
		if k := (want + i) % n; !t.rests.resting(k, now) {
			out = append(out, k)
		}
	}
	if len(out) == 0 {
		out = []int{want}
	}
	return out
}

// refusal is the vendor saying no to a connection: at the HTTP upgrade, or in
// answer to connected_success or task_start.
type refusal struct {
	http   int
	status int
	err    error
}

func (r *refusal) Error() string { return r.err.Error() }
func (r *refusal) Unwrap() error { return r.err }

// The vendor's status codes that are about pace rather than about the account:
// requests a minute, characters a minute, connections, growth of rate. A key
// refused for one of these wants a moment, not ten minutes.
var paceCodes = map[int]bool{1002: true, 1039: true, 1041: true, 2045: true}

// rest takes account n out of use for a while if err is the vendor refusing
// it, and reports whether it was. Anything else — a timeout, a reset — is the
// network's, would meet the next account as well, and is the caller's error.
//
// It is deliberately not a list of the codes that mean "this key is bad":
// wrong key, no credit, no access to this voice, usage limit — the vendor adds
// to them, and what they have in common is that the next request on this key
// will be refused too. The one line this logs names the key by its position.
func (t *TTS) rest(n int, err error) bool {
	r, ok := err.(*refusal)
	if !ok {
		return false
	}
	d := 10 * time.Minute
	if paceCodes[r.status] || r.http == http.StatusTooManyRequests {
		d = 15 * time.Second
	}
	if t.rests == nil {
		t.rests = &keyRests{}
	}
	t.rests.set(n, time.Now().Add(d))
	slog.Warn("tts: an account was refused; resting it",
		"provider", t.Name(), "account", n+1, "of", len(t.keys()),
		"http_status", r.http, "status", r.status, "for_s", int(d.Seconds()), "err", err)
	return true
}
