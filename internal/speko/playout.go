package speko

// trimIdleSilence retains 100 ms of leading digital silence. The caller only
// applies it before speech starts, or after at least 200 ms without voiced
// playback. Never remove a voiced sample or silence behind queued speech.
// Continuous GPT-Live silence must not become a growing playout delay.
func trimIdleSilence(queue []byte, zero byte) ([]byte, int) {
	const keep = 800 // 100 ms at 8 kHz
	prefix := 0
	for prefix < len(queue) && (queue[prefix] == zero || queue[prefix] == zero^0x80) {
		prefix++
	}
	remove := max(0, prefix-keep)
	return queue[remove:], remove
}
