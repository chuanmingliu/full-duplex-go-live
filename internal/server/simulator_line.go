package server

import (
	"context"
	"sync"
	"time"
)

const (
	simulatorLineGap   = 100 * time.Millisecond
	simulatorLineFrame = 20 * time.Millisecond
	// Simulator media is pinned to 16 kHz mono PCM16, independently of the
	// gateway's negotiated SIP codec.
	simulatorLineFrameBytes = 16000 / 50 * 2
)

// simulatorInputLine supplies the missing pause when an inbound gateway uses
// silence suppression. Only the engine receives these frames; they are not
// received-wire evidence, transport activity, recordings or outbound audio.
// The mutex orders real audio, clock samples, silence and shutdown together.
type simulatorInputLine struct {
	mu        sync.Mutex
	ctx       context.Context
	cancel    context.CancelFunc
	done      chan struct{}
	push      func([]byte)
	lastInput time.Time
	lastFill  time.Time
	closed    bool
	silence   [simulatorLineFrameBytes]byte
}

// open runs the answer gate while holding the ingress lock so the first real
// packet cannot overtake activation. A rejected gate never starts a filler.
func (l *simulatorInputLine) open(ctx context.Context, activate func() bool, push func([]byte)) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.closed || l.push != nil || ctx.Err() != nil || !activate() {
		return false
	}
	l.ctx, l.cancel = context.WithCancel(ctx)
	l.push = push
	l.lastInput = time.Now()
	l.done = make(chan struct{})
	go l.run()
	return true
}

func (l *simulatorInputLine) receive(pcm []byte, beforePush func()) bool {
	if len(pcm) < 2 {
		// Empty append messages do not keep a silent line alive.
		return false
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.closed || l.push == nil || l.ctx.Err() != nil {
		return false
	}
	l.lastInput = time.Now()
	if beforePush != nil {
		// Retain the actual receive boundary before engine processing can
		// produce ASR or output events. Clock-generated frames skip it.
		beforePush()
	}
	l.push(pcm)
	return true
}

func (l *simulatorInputLine) run() {
	defer close(l.done)
	tick := time.NewTimer(simulatorLineFrame)
	defer tick.Stop()
	for {
		select {
		case <-l.ctx.Done():
			return
		case <-tick.C:
			l.mu.Lock()
			// Sample after acquiring the lock: a queued ticker timestamp
			// must never overtake fresh audio or replay a delayed clock.
			l.fillLocked(time.Now())
			l.mu.Unlock()
			// Start the next period after this check. A delayed iteration
			// cannot leave a queued tick that immediately sends a second frame.
			tick.Reset(simulatorLineFrame)
		}
	}
}

func (l *simulatorInputLine) fillLocked(now time.Time) {
	if l.closed || l.push == nil || l.ctx.Err() != nil || now.Sub(l.lastInput) < simulatorLineGap || now.Sub(l.lastFill) < simulatorLineFrame {
		return
	}
	// At most one frame per wall-clock period; never catch up a delayed
	// scheduler with a burst of historical silence into VAD/ASR.
	l.lastFill = now
	l.push(l.silence[:])
}

func (l *simulatorInputLine) stop() {
	l.mu.Lock()
	l.closed = true
	if l.cancel != nil {
		l.cancel()
	}
	done := l.done
	l.mu.Unlock()
	if done != nil {
		<-done
	}
}
