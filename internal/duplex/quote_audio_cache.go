package duplex

import (
	"context"
	"errors"
	"sync"
	"time"
)

const (
	quoteAudioEntryBytes = 256 << 10
	quoteAudioTotalBytes = 1 << 20
	quoteAudioPollLimit  = 300
)

type quoteAudioResult struct {
	expires time.Time
	raw     []byte
}

type preparedQuoteAudio struct {
	audio    PhraseAudio
	identity string
}

type quoteAudioEntry struct {
	preparedQuoteAudio
	expires time.Time
}

// quoteAudioCache belongs to one immutable engine/voice configuration. Its
// worker only prepares audio; normal validated tool delivery decides what is
// spoken. Private quote text and PCM never enter the process-wide phrase cache.
type quoteAudioCache struct {
	ctx          context.Context
	cancel       context.CancelFunc
	done         chan struct{}
	closeSpeech  func()
	closeOnce    sync.Once
	fetch        func(context.Context) ([]quoteAudioResult, error)
	render       func([]byte) string
	prepare      func(context.Context, string) (preparedQuoteAudio, bool, error)
	interval     time.Duration
	initialDelay time.Duration
	speechOnce   sync.Once
	max          int

	mu       sync.Mutex
	closed   bool
	entries  map[string]quoteAudioEntry
	attempts int
	bytes    int
	identity string
}

func (q *quoteAudioCache) start(ctx context.Context) {
	q.ctx, q.cancel = context.WithCancel(ctx)
	q.done = make(chan struct{})
	q.entries = make(map[string]quoteAudioEntry)
	go func() {
		defer close(q.done)
		defer q.retireSpeech()
		if q.initialDelay > 0 {
			timer := time.NewTimer(q.initialDelay)
			select {
			case <-q.ctx.Done():
				timer.Stop()
				return
			case <-timer.C:
			}
		}
		for poll := 0; poll < quoteAudioPollLimit; poll++ {
			if q.ctx.Err() != nil {
				return
			}
			rows, err := q.fetch(q.ctx)
			if err == nil && !q.consume(rows) {
				return
			}
			timer := time.NewTimer(q.interval)
			select {
			case <-q.ctx.Done():
				timer.Stop()
				return
			case <-timer.C:
			}
		}
	}()
}

func (q *quoteAudioCache) consume(rows []quoteAudioResult) bool {
	for _, row := range rows {
		if q.ctx.Err() != nil {
			return false
		}
		if !time.Now().Before(row.expires) {
			continue
		}
		text := q.render(row.raw)
		if text == "" {
			continue
		}
		q.mu.Lock()
		_, exists := q.entries[text]
		full := q.attempts >= q.max || q.closed
		q.mu.Unlock()
		if full {
			return false
		}
		if exists {
			continue
		}
		prepared, attempted, err := q.prepare(q.ctx, text)
		q.mu.Lock()
		if attempted {
			q.attempts++
		}
		if err == nil && attempted && !q.closed && q.ctx.Err() == nil && time.Now().Before(row.expires) &&
			len(prepared.audio.PCM) > 0 && len(prepared.audio.PCM)%2 == 0 && len(prepared.audio.PCM) <= quoteAudioEntryBytes &&
			prepared.audio.Rate > 0 && prepared.identity != "" && q.bytes+len(prepared.audio.PCM) <= quoteAudioTotalBytes {
			if q.identity == "" {
				q.identity = prepared.identity
			}
			if q.identity == prepared.identity {
				prepared.audio.PCM = append([]byte(nil), prepared.audio.PCM...)
				q.entries[text] = quoteAudioEntry{preparedQuoteAudio: prepared, expires: row.expires}
				q.bytes += len(prepared.audio.PCM)
			}
		}
		full = q.attempts >= q.max || q.closed
		q.mu.Unlock()
		// A started operation that fails is not retried. The provider retires its
		// strict connection, and another account must not silently multiply work.
		if full || (attempted && err != nil) || errors.Is(err, errQuoteAudioRetired) {
			return false
		}
	}
	return true
}

var errQuoteAudioRetired = errors.New("quote audio provider retired")

func (q *quoteAudioCache) get(text string) (PhraseAudio, bool) {
	if q == nil {
		return PhraseAudio{}, false
	}
	q.mu.Lock()
	defer q.mu.Unlock()
	entry, ok := q.entries[text]
	if q.closed || !ok || !time.Now().Before(entry.expires) {
		return PhraseAudio{}, false
	}
	out := entry.audio
	out.PCM = append([]byte(nil), out.PCM...)
	return out, true
}

func (q *quoteAudioCache) close() {
	if q == nil {
		return
	}
	q.closeOnce.Do(func() {
		q.mu.Lock()
		q.closed = true
		q.mu.Unlock()
		q.cancel()
		// Closing the stream unblocks provider socket reads before waiting for the
		// worker. Cancellation by itself need not unblock a vendor read.
		q.retireSpeech()
		<-q.done
		q.mu.Lock()
		q.entries = nil
		q.bytes = 0
		q.identity = ""
		q.mu.Unlock()
	})
}

func (q *quoteAudioCache) retireSpeech() { q.speechOnce.Do(q.closeSpeech) }
