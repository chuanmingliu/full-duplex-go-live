package duplex

import (
	"time"

	"github.com/chuanmingliu/golive/internal/audio"
)

// Feed the existing speculative recognizer while a playback interruption is
// accumulating VAD evidence. Results remain private in lazyASR until the VAD
// confirms the utterance; the usual text, echo and speaker gates still decide
// whether to interrupt. No prompt, provider or threshold changes are involved.
func (e *Engine) prefeedBargeInASR() {
	if e.asrStandby == nil || time.Now().Before(e.asrPrefeedRetryAt) || (!e.speaking.Load() && e.asrStandbySamples == 0) {
		return
	}
	pcm, start := e.vad.CandidateSnapshot()
	if len(pcm) == 0 {
		return
	}
	if e.asrStandbySamples > 0 && (start != e.asrStandbyStartMS || len(pcm) < e.asrStandbySamples) {
		e.closeStandby()
		e.asrPrefeedRetryAt = time.Now().Add(standbyMaxAge)
		return
	}
	if err := e.asrStandby.Write(audio.EncodePCM16(pcm[e.asrStandbySamples:])); err != nil {
		e.closeStandby()
		e.asrPrefeedRetryAt = time.Now().Add(standbyMaxAge)
		return
	}
	e.asrStandbyStartMS = start
	e.asrStandbySamples = len(pcm)
}

func (e *Engine) writeASROpening(pcm []float32) {
	if e.asrRun == nil {
		return
	}
	n := e.asrRun.prefedSamples
	e.asrRun.prefedSamples = 0
	if n > len(pcm) {
		// Never omit later audio to compensate for an inconsistent prefix.
		// Fall back to a fresh recognition of the complete opening instead.
		start := e.asrRun.startMS
		e.openASR(start)
		n = 0
	}
	e.writeASR(pcm[n:])
}
