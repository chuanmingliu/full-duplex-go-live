package provider

import (
	"context"
	"sync"
	"time"
)

// ConnectionObservedTTS reports each physical connection attempt immediately
// before dialing. The observer remains attached to the returned stream so that
// internal reconnects are counted too. Failed attempts are never refunded.
type ConnectionObservedTTS interface {
	TTS
	OpenObserved(context.Context, TTSOptions, func(account int)) (TTSStream, error)
}

// ConsumerTimeout is the longest a stream lets an output consumer stop reading
// before cancelling its segment. Adapters preserve this bound when proxying PCM.
type ConsumerTimeout interface {
	ConsumerTimeout() time.Duration
}

func consumerTimeout(stream TTSStream) time.Duration {
	if s, ok := stream.(ConsumerTimeout); ok && s.ConsumerTimeout() > 0 {
		return s.ConsumerTimeout()
	}
	return 30 * time.Second
}

// connectionAttempt consumes a prepaid spare opening once on its requested
// account. A refused or skipped destination keeps that reservation; every other
// physical dial, including all failovers and reconnects, is separately charged.
func (h *hedgedTTS) connectionAttempt(prepaid int) func(int) {
	var mu sync.Mutex
	used := false
	return func(account int) {
		mu.Lock()
		defer mu.Unlock()
		if account == prepaid && !used {
			used = true
			return
		}
		h.account(account).spend(1, true)
	}
}
