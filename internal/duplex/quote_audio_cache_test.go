package duplex

import (
	"context"
	"errors"
	"fmt"
	"sync/atomic"
	"testing"
	"time"
)

func cacheFixture() *quoteAudioCache {
	return &quoteAudioCache{ctx: context.Background(), max: 4, entries: map[string]quoteAudioEntry{},
		render: func(raw []byte) string { return string(raw) },
		prepare: func(context.Context, string) (preparedQuoteAudio, bool, error) {
			return preparedQuoteAudio{PhraseAudio{PCM: []byte{1, 2}, Rate: 16000}, "voice-A"}, true, nil
		}}
}

func ready(text string) quoteAudioResult {
	return quoteAudioResult{time.Now().Add(time.Minute), []byte(text)}
}

func TestQuoteAudioBoundedUniquePreparation(t *testing.T) {
	q := cacheFixture()
	var calls []string
	q.prepare = func(_ context.Context, s string) (preparedQuoteAudio, bool, error) {
		calls = append(calls, s)
		return preparedQuoteAudio{PhraseAudio{PCM: []byte{1, 2}, Rate: 16000}, "voice-A"}, true, nil
	}
	rows := []quoteAudioResult{ready("a"), ready("a"), ready("b"), ready("c"), ready("d"), ready("e")}
	if q.consume(rows) {
		t.Fatal("worker continued after its four-attempt budget")
	}
	if fmt.Sprint(calls) != "[a b c d]" || q.attempts != 4 || len(q.entries) != 4 {
		t.Fatalf("calls=%v attempts=%d entries=%d", calls, q.attempts, len(q.entries))
	}
	pcm, ok := q.get("b")
	if !ok || pcm.Rate != 16000 {
		t.Fatal("prepared exact text missing")
	}
	pcm.PCM[0] = 99
	again, _ := q.get("b")
	if again.PCM[0] != 1 {
		t.Fatal("returned PCM aliases private cache")
	}
	if _, ok := q.get("b changed"); ok {
		t.Fatal("changed authoritative text matched")
	}
}

func TestQuoteAudioAdmissionAndFailureAccounting(t *testing.T) {
	q := cacheFixture()
	denied := true
	q.prepare = func(context.Context, string) (preparedQuoteAudio, bool, error) {
		if denied {
			return preparedQuoteAudio{}, false, errors.New("no capacity")
		}
		return preparedQuoteAudio{}, true, errors.New("provider failed after admission")
	}
	if !q.consume([]quoteAudioResult{ready("a")}) || q.attempts != 0 {
		t.Fatal("refused admission consumed budget or retired")
	}
	denied = false
	if q.consume([]quoteAudioResult{ready("a"), ready("b")}) || q.attempts != 1 || len(q.entries) != 0 {
		t.Fatal("failed provider work retried or cached")
	}
}

func TestQuoteAudioValidityAndIdentity(t *testing.T) {
	q := cacheFixture()
	q.consume([]quoteAudioResult{{time.Now().Add(-time.Second), []byte("expired")}, ready("a")})
	if q.attempts != 1 {
		t.Fatal("expired quote synthesized")
	}
	q.prepare = func(context.Context, string) (preparedQuoteAudio, bool, error) {
		return preparedQuoteAudio{PhraseAudio{PCM: []byte{3, 4}, Rate: 16000}, "voice-B"}, true, nil
	}
	q.consume([]quoteAudioResult{ready("b")})
	if _, ok := q.get("b"); ok {
		t.Fatal("different effective voice cached")
	}
	entry := q.entries["a"]
	entry.expires = time.Now().Add(-time.Second)
	q.entries["a"] = entry
	if _, ok := q.get("a"); ok {
		t.Fatal("expired prepared quote played")
	}
}

func TestQuoteAudioRejectsPartialOrOversizedAudio(t *testing.T) {
	for _, n := range []int{0, 1, quoteAudioEntryBytes + 2} {
		t.Run(fmt.Sprint(n), func(t *testing.T) {
			q := cacheFixture()
			q.prepare = func(context.Context, string) (preparedQuoteAudio, bool, error) {
				return preparedQuoteAudio{PhraseAudio{PCM: make([]byte, n), Rate: 16000}, "voice-A"}, true, nil
			}
			q.consume([]quoteAudioResult{ready("a")})
			if _, ok := q.get("a"); ok {
				t.Fatal("invalid audio cached")
			}
		})
	}
	q := cacheFixture()
	q.prepare = func(context.Context, string) (preparedQuoteAudio, bool, error) {
		return preparedQuoteAudio{PhraseAudio{PCM: []byte{1, 2}, Rate: 16000}, "voice-A"}, true, errors.New("late chunk error")
	}
	q.consume([]quoteAudioResult{ready("a")})
	if _, ok := q.get("a"); ok {
		t.Fatal("partial audio cached after error")
	}
}

func TestQuoteAudioCloseUnblocksWorkerAndClearsPrivateAudio(t *testing.T) {
	q := cacheFixture()
	started := make(chan struct{})
	unblock := make(chan struct{})
	var closed atomic.Int32
	q.interval = time.Hour
	q.fetch = func(context.Context) ([]quoteAudioResult, error) { return []quoteAudioResult{ready("a")}, nil }
	q.prepare = func(context.Context, string) (preparedQuoteAudio, bool, error) {
		close(started)
		<-unblock
		return preparedQuoteAudio{PhraseAudio{PCM: []byte{1, 2}, Rate: 16000}, "voice-A"}, true, nil
	}
	q.closeSpeech = func() {
		if closed.Add(1) == 1 {
			close(unblock)
		}
	}
	q.start(context.Background())
	<-started
	done := make(chan struct{})
	go func() { q.close(); q.close(); close(done) }()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("close waited before unblocking provider")
	}
	if _, ok := q.get("a"); ok || len(q.entries) != 0 || q.bytes != 0 || closed.Load() != 1 {
		t.Fatal("close retained audio, allowed late cache write, or closed twice")
	}
}

func TestQuoteAudioCloseCancelsFetch(t *testing.T) {
	q := cacheFixture()
	started := make(chan struct{})
	q.interval = time.Hour
	q.fetch = func(ctx context.Context) ([]quoteAudioResult, error) {
		close(started)
		<-ctx.Done()
		return nil, ctx.Err()
	}
	q.closeSpeech = func() {}
	q.start(context.Background())
	<-started
	q.close()
}
