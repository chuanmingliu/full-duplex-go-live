package minimax

import (
	"context"
	"log/slog"
	"net/http"
	"os"
	"strconv"
	"strings"
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
// Every account must be able to speak in the same voice, and a cloned or
// designed voice belongs to the account that made it: another account asking
// for it by the first account's id is refused at task_start. So an account
// that has its own copy of the voice says what it calls it, in the numbered
// form, written beside the settings there have always been:
//
//	MINIMAX_TTS_API_KEY=…        MINIMAX_TTS_VOICE_ID=…        MINIMAX_TTS_RPM=…      the first account
//	MINIMAX_TTS_API_KEY_2=…      MINIMAX_TTS_VOICE_ID_2=…      MINIMAX_TTS_RPM_2=…    the second
//	MINIMAX_TTS_API_KEY_3=…      MINIMAX_TTS_VOICE_ID_3=…      MINIMAX_TTS_RPM_3=…    and so on to 9
//
// (the last being what that account allows in a minute, when it is known).
//
// A numbered key with no voice beside it is asked for the first account's
// voice, which is right for a voice every account has (the vendor's own) and
// is refused otherwise — which rests that key (below) and leaves the pool as it
// was before the key was added. A numbered voice with no key is nothing.
//
// Two clones are two voices, however alike. The pool keeps a whole answer on
// one account where it can, but which account speaks changes from turn to
// turn; clone from the same recording, and listen to both (ttsprobe -pool
// -save) before a caller does.

// accountVoices is each key's own voice id, from the numbered settings.
func accountVoices(keys []string) []string {
	byKey := map[string]string{}
	for n := 2; n <= 9; n++ {
		suffix := "_" + strconv.Itoa(n)
		key := strings.Trim(strings.TrimSpace(os.Getenv("MINIMAX_TTS_API_KEY"+suffix)), `"'`)
		voice := strings.Trim(strings.TrimSpace(os.Getenv("MINIMAX_TTS_VOICE_ID"+suffix)), `"'`)
		if key != "" && voice != "" {
			byKey[key] = voice
		}
	}
	out := make([]string, len(keys))
	for i, k := range keys {
		if i > 0 {
			out[i] = byKey[k]
		}
	}
	return out
}

// accountRPMs is what each account is known to allow, from MINIMAX_TTS_RPM
// (the first account) and MINIMAX_TTS_RPM_2 … _9, written beside the key they
// describe. Accounts are on different tiers: of the two this was written for,
// one has carried 31 requests in a minute and the other refuses past about a
// dozen. 0, or nothing, is "not known".
func accountRPMs(keys []string) []int {
	byKey := map[string]int{}
	for n := 2; n <= 9; n++ {
		suffix := "_" + strconv.Itoa(n)
		key := strings.Trim(strings.TrimSpace(os.Getenv("MINIMAX_TTS_API_KEY"+suffix)), `"'`)
		if rpm, err := strconv.Atoi(strings.TrimSpace(os.Getenv("MINIMAX_TTS_RPM" + suffix))); err == nil && rpm > 0 && key != "" {
			byKey[key] = rpm
		}
	}
	out := make([]int, len(keys))
	for i, k := range keys {
		if i == 0 {
			if rpm, err := strconv.Atoi(strings.TrimSpace(os.Getenv("MINIMAX_TTS_RPM"))); err == nil && rpm > 0 {
				out[0] = rpm
			}
			continue
		}
		out[i] = byKey[k]
	}
	return out
}

// AccountRPM implements provider.AccountLimiter.
func (t *TTS) AccountRPM(n int) int {
	if n >= 0 && n < len(t.RPMs) {
		return t.RPMs[n]
	}
	return 0
}

// voiceFor is the voice id to ask account n for.
//
// A session that asks for nothing, or for the configured voice by the first
// account's name for it, gets the configured voice — which on account n is
// whatever that account calls it. A session that asks for some other voice
// gets exactly what it asked for, on whichever account: there is no knowing
// what another account would call a voice this configuration has never heard
// of.
func (t *TTS) voiceFor(n int, asked string) string {
	if asked != "" && asked != t.VoiceID {
		return asked
	}
	if n < len(t.VoiceIDs) && t.VoiceIDs[n] != "" {
		return t.VoiceIDs[n]
	}
	return t.VoiceID
}

// OwnVoice reports whether account n has its own id for the voice.
func (t *TTS) OwnVoice(n int) bool {
	return n == 0 || (n < len(t.VoiceIDs) && t.VoiceIDs[n] != "")
}

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
	n %= len(keys)
	c := *t
	c.APIKey, c.APIKeys, c.VoiceIDs, c.rests = keys[n], nil, nil, &keyRests{}
	c.RPMs = []int{t.AccountRPM(n)}
	// Its own name for the voice becomes the configured one: this client has
	// one account, and that account is its first.
	c.VoiceID = t.voiceFor(n, "")
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
