package journal

// Synchronous tool speech carries the original caller's monotonic clock even
// through several model/tool rounds. Keep it separate from quotation results.
func (cb *callBuild) joinContinuations(e *LatencyEvidence) (map[string]int, map[string]int) {
	content, audio := map[string]int{}, map[string]int{}
	children := map[string]int{}
	for _, link := range cb.continuations {
		children[link.Get("turn")]++
	}
	for _, link := range cb.continuations {
		child := cb.turns[link.Get("turn")]
		result, valid := cb.linkedContent(link)
		if !valid || children[link.Get("turn")] != 1 {
			e.InvalidLinks++
			e.Unverified++
			continue
		}
		// A validated silent continuation does not create a response sample.
		if child.latency.Get("first_audio_present") == "false" {
			continue
		}
		e.ContinuationRows = append(e.ContinuationRows, result)
		origin := cb.turns[result.OriginTurn]
		if origin == nil || origin.latency == nil || origin.latency.Get("truncated") != "false" {
			continue
		}
		value := result.OffsetMS + result.ChildContentMS
		if origin.latency.Get("first_content_present") == "false" {
			if previous, exists := content[result.OriginTurn]; !exists || value < previous {
				content[result.OriginTurn] = value
			}
		}
		if origin.latency.Get("first_audio_present") == "false" {
			value = result.OffsetMS + child.latency.N("first_audio_out_ms")
			if previous, exists := audio[result.OriginTurn]; !exists || value < previous {
				audio[result.OriginTurn] = value
			}
		}
	}
	e.ContinuationsJoined = len(content)
	return content, audio
}
