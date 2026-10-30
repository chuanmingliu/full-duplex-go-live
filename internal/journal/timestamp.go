package journal

// The opener log records readiness, not output. Its timestamp can anchor a
// turn only when that turn's metrics confirm output and give the readiness
// offset from speech close. Applying the separate output offset preserves
// player delays instead of treating the queue time as the playout time.
func (t *turnBuild) confirmedOpener() bool {
	if t.openerAt.IsZero() || t.latency == nil {
		return false
	}
	l := *t.latency
	_, ready := l.Int("tts_first_audio_ms")
	_, output := l.Int("first_audio_out_ms")
	return ready && output && l.Get("opened") == "true" && l.Get("first_audio_present") == "true"
}
