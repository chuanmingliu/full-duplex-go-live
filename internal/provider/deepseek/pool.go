package deepseek

import (
	"context"
	"log/slog"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/chuanmingliu/golive/internal/provider"
)

// A pool of keys and connections.
//
// One key on one connection is how this client began, and it is why sending
// four copies of a completion stopped helping. The copies went out as four
// streams on a single HTTP/2 connection, so a lost packet stalled all four;
// they drew on one account, so whatever the vendor does to an account's
// concurrent requests it did to all four. They were measured slow together —
// one turn in ten with every copy over 550 ms, where independent draws would
// have made it one in a hundred.
//
// A lane is one key and one connection of its own. Copy n of a completion goes
// to lane n (provider.WithLane), so copies share neither. With one key the
// lanes still give each copy its own connection; with several, its own account
// as well.
//
// Keys come from three places, in this order, with duplicates dropped:
//
//	DEEPSEEK_API_KEY=sk-…                 the one there has always been
//	DEEPSEEK_API_KEYS=sk-…,sk-…,sk-…      more, separated by commas or spaces
//	DEEPSEEK_API_KEYS_FILE=/path/to/file  more, one a line; # starts a comment
//
// (and likewise for any other provider's key variable). Nothing here ever
// logs a key: a key is referred to by its position in that list.

// minLanes is how many connections the pool keeps apart when there are fewer
// keys than that: enough for the copies a completion is sent in.
const minLanes = 4

// maxLanes bounds what DEEPSEEK_LANES can ask for.
const maxLanes = 16

type lane struct {
	n      int
	keyN   int
	key    string
	client *http.Client
	// restUntil is when this lane's key may be used again, in Unix
	// nanoseconds; zero when it is in use. See rest.
	restUntil atomic.Int64
}

// SingleKey returns a client like this one whose pool holds only the first
// key: the same number of lanes, each still a connection of its own, all on one
// account. It is for measuring what the other keys are worth.
func (l *LLM) SingleKey() *LLM {
	l.build()
	return &LLM{name: l.name, BaseURL: l.BaseURL, APIKey: l.keys[0], DefaultModel: l.DefaultModel,
		HTTP: newHTTPClient(), keys: l.keys[:1]}
}

// Keys reports how many keys the pool holds, and Lanes how many connections it
// keeps apart.
func (l *LLM) Keys() int  { l.build(); return len(l.keys) }
func (l *LLM) Lanes() int { l.build(); return len(l.lanes) }

func (l *LLM) build() {
	l.lanesOnce.Do(func() {
		if len(l.keys) == 0 {
			l.keys = []string{l.APIKey}
		}
		n := len(l.keys)
		if n < minLanes {
			n = minLanes
		}
		// More copies of a completion than that want more lanes than that: a
		// copy that shares a connection with another is not a copy apart.
		// DEEPSEEK_LANES says how many, up to a sane ceiling.
		if want, err := strconv.Atoi(strings.TrimSpace(os.Getenv("DEEPSEEK_LANES"))); err == nil && want > n {
			if want > maxLanes {
				want = maxLanes
			}
			n = want
		}
		for i := 0; i < n; i++ {
			client := l.HTTP
			if i > 0 || client == nil {
				client = newHTTPClient()
			}
			l.lanes = append(l.lanes, &lane{n: i, keyN: i % len(l.keys), key: l.keys[i%len(l.keys)], client: client})
		}
		slog.Info("llm: pool ready", "provider", l.Name(), "keys", len(l.keys), "lanes", len(l.lanes))
	})
}

// pick chooses the lane for a request: the one its context names, or the next
// in turn. A lane whose key is resting is passed over for the next that is
// not; if every key is resting the request goes where it was going, since a
// refusal is no worse than not asking.
func (l *LLM) pick(ctx context.Context) *lane {
	l.build()
	want, pinned := provider.Lane(ctx)
	if !pinned {
		want = int(l.next.Add(1) - 1)
	}
	if want < 0 {
		want = -want
	}
	now := time.Now().UnixNano()
	for i := 0; i < len(l.lanes); i++ {
		ln := l.lanes[(want+i)%len(l.lanes)]
		if ln.restUntil.Load() <= now {
			return ln
		}
	}
	return l.lanes[want%len(l.lanes)]
}

// rest takes a key out of use for a while after the vendor has refused it.
//
// A key that is wrong or out of credit will be refused again, at once, on every
// request sent to it — which in a pool means one copy of every completion
// wasted for as long as the process runs. A key that is being rate limited
// wants a moment. Either way the other lanes carry on, and the one line this
// logs names the key by its position, never by its value.
func (l *LLM) rest(ln *lane, status int) {
	var d time.Duration
	switch status {
	case http.StatusUnauthorized, http.StatusPaymentRequired, http.StatusForbidden:
		d = 10 * time.Minute
	case http.StatusTooManyRequests:
		d = 15 * time.Second
	default:
		return
	}
	until := time.Now().Add(d).UnixNano()
	for _, other := range l.lanes {
		if other.keyN == ln.keyN {
			other.restUntil.Store(until)
		}
	}
	slog.Warn("llm: a key was refused; resting it",
		"provider", l.Name(), "key", ln.keyN+1, "of", len(l.keys),
		"http_status", status, "for_s", int(d.Seconds()))
}
