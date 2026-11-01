package duplex

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/chuanmingliu/golive/internal/metrics"
	"github.com/chuanmingliu/golive/internal/provider"
)

// errASROpen marks a result that reports a failure to connect, as opposed to
// one from a recognizer that connected and then failed.
var errASROpen = errors.New("asr open failed")

// lazyASR is a recognition stream that is usable before its connection exists.
//
// Opening a recognizer is a TCP, TLS and WebSocket handshake to a vendor, and
// the engine used to do it inline on its loop goroutine at the moment an
// utterance was declared. Two things were wrong with that. Everything the loop
// does — the detector, speculation, floor control, every other session event —
// stopped for as long as the handshake took. And the handshake only started
// once the detector was sure, so a short utterance could be over before the
// recognizer had heard any of it.
//
// A lazyASR returns immediately and dials in the background. Audio written
// before the connection is up is kept in order and flushed the moment it is,
// which is what lets the engine dial early — on the first suspicion of speech,
// see Engine.maybePredialASR — and still lose nothing if the handshake finishes
// late.
type lazyASR struct {
	results chan provider.ASRResult

	mu        sync.Mutex
	stream    provider.ASRStream
	pending   [][]byte
	sendDone  bool
	closed    bool
	failed    bool
	startedAt time.Time
	readyIn   time.Duration
}

// dialLazyASR starts opening a recognition stream and returns at once.
func dialLazyASR(ctx context.Context, asr provider.ASR, opts provider.ASROptions) *lazyASR {
	l := &lazyASR{
		results:   make(chan provider.ASRResult, 32),
		startedAt: time.Now(),
	}
	go l.open(ctx, asr, opts)
	return l
}

func (l *lazyASR) open(ctx context.Context, asr provider.ASR, opts provider.ASROptions) {
	defer close(l.results)

	stream, err := asr.Open(ctx, opts)
	if err != nil && ctx.Err() == nil && !l.isClosed() {
		// One retry, as before: a recognizer's front door fails transiently
		// often enough that giving up on the first refusal loses whole
		// utterances.
		metrics.Default().Retry("asr")
		select {
		case <-time.After(150 * time.Millisecond):
		case <-ctx.Done():
		}
		if ctx.Err() == nil && !l.isClosed() {
			stream, err = asr.Open(ctx, opts)
		}
	}
	if err != nil {
		l.mu.Lock()
		l.failed = true
		l.pending = nil
		closed := l.closed
		l.mu.Unlock()
		if !closed && ctx.Err() == nil {
			l.forward(ctx, provider.ASRResult{Err: fmt.Errorf("%w: %v", errASROpen, err)})
		}
		return
	}

	// Deliver what was waiting, in order. The network writes happen outside
	// the lock — Write is called from the engine's loop and must never wait on
	// a vendor — so audio that arrives meanwhile joins the queue behind them,
	// and the stream is only handed over once the queue is seen empty under
	// the lock. Nothing can slip in ahead of audio that was waiting.
	var sendDone bool
	for {
		l.mu.Lock()
		if l.closed {
			l.mu.Unlock()
			_ = stream.Close()
			return
		}
		batch := l.pending
		l.pending = nil
		if len(batch) == 0 {
			l.stream = stream
			l.readyIn = time.Since(l.startedAt)
			sendDone = l.sendDone
			l.mu.Unlock()
			break
		}
		l.mu.Unlock()
		for _, pcm := range batch {
			_ = stream.Write(pcm)
		}
	}

	if sendDone {
		_ = stream.CloseSend()
	}
	for r := range stream.Results() {
		if !l.forward(ctx, r) {
			_ = stream.Close()
			return
		}
	}
}

func (l *lazyASR) forward(ctx context.Context, r provider.ASRResult) bool {
	select {
	case l.results <- r:
		return true
	case <-ctx.Done():
		return false
	}
}

func (l *lazyASR) isClosed() bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.closed
}

// maxPendingASRBytes bounds audio held for a connection that has not come up:
// ten seconds at the pipeline rate. A handshake that slow has failed in every
// way that matters, and the buffer must not grow without limit behind it.
const maxPendingASRBytes = provider.PipelineRate * 2 * 10

// Write implements provider.ASRStream. It never blocks on the network.
func (l *lazyASR) Write(pcm []byte) error {
	if len(pcm) == 0 {
		return nil
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.closed || l.failed || l.sendDone {
		return nil
	}
	if l.stream != nil {
		return l.stream.Write(pcm)
	}
	held := 0
	for _, p := range l.pending {
		held += len(p)
	}
	if held+len(pcm) > maxPendingASRBytes {
		return fmt.Errorf("asr: connection still not open; dropping audio")
	}
	buf := make([]byte, len(pcm))
	copy(buf, pcm)
	l.pending = append(l.pending, buf)
	return nil
}

// Results implements provider.ASRStream.
func (l *lazyASR) Results() <-chan provider.ASRResult { return l.results }

// CloseSend implements provider.ASRStream. Before the connection is up it is
// remembered and sent as soon as the buffered audio has been.
func (l *lazyASR) CloseSend() error {
	l.mu.Lock()
	if l.closed || l.sendDone {
		l.mu.Unlock()
		return nil
	}
	l.sendDone = true
	stream := l.stream
	l.mu.Unlock()
	if stream != nil {
		return stream.CloseSend()
	}
	return nil
}

// Close implements provider.ASRStream.
func (l *lazyASR) Close() error {
	l.mu.Lock()
	if l.closed {
		l.mu.Unlock()
		return nil
	}
	l.closed = true
	l.pending = nil
	stream := l.stream
	l.mu.Unlock()
	if stream != nil {
		return stream.Close()
	}
	return nil
}

// usable reports whether a stream dialled ahead of time is still worth
// adopting: not failed, not closed, and not so old that the far end has
// probably given up on a connection that never sent audio.
func (l *lazyASR) usable(maxAge time.Duration) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	return !l.closed && !l.failed && time.Since(l.startedAt) < maxAge
}

// didFail reports whether the connection could not be opened.
func (l *lazyASR) didFail() bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.failed
}

// ready reports whether the connection is up, and how long that took.
func (l *lazyASR) ready() (bool, time.Duration) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.stream != nil, l.readyIn
}
