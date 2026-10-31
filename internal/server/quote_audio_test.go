package server

import (
	"fmt"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/chuanmingliu/golive/internal/config"
)

func TestQuoteAudioCallClaimNeverResetsOnReconnect(t *testing.T) {
	cfg := config.Default()
	cfg.Duplex.QuoteReadoutWarmMax = 4
	s := NewServer(cfg, nil)
	var winners atomic.Int32
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if s.claimQuoteAudio("out-same-call") {
				winners.Add(1)
			}
		}()
	}
	wg.Wait()
	if winners.Load() != 1 {
		t.Fatalf("same call got %d budgets", winners.Load())
	}
	if s.claimQuoteAudio("out-same-call") {
		t.Fatal("later session reset budget")
	}
	if !s.claimQuoteAudio("out-another-call") {
		t.Fatal("another call denied below capacity")
	}
}

func TestQuoteAudioCallClaimDisabledAndBounded(t *testing.T) {
	cfg := config.Default()
	s := NewServer(cfg, nil)
	if s.claimQuoteAudio("out-disabled") {
		t.Fatal("default enabled")
	}
	s.cfg.Duplex.QuoteReadoutWarmMax = 4
	for _, call := range []string{"", "bad|id", "bad/id"} {
		if s.claimQuoteAudio(call) {
			t.Fatal("invalid call claimed")
		}
	}
	for i := 0; i < maxQuoteAudioCalls; i++ {
		if !s.claimQuoteAudio(fmt.Sprintf("out-%d", i)) {
			t.Fatal("capacity denied early")
		}
	}
	if s.claimQuoteAudio("out-over-cap") {
		t.Fatal("unbounded call claim map")
	}
	if len(s.quoteAudioCalls) != maxQuoteAudioCalls {
		t.Fatal("capacity changed")
	}
}
