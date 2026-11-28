package journal

// Synchronous tool speech carries the original caller's monotonic clock even
// through several model/tool rounds. Keep it separate from quotation results.
type continuationInput struct {
	AudioMS   int
	ContentMS int
}

func (cb *callBuild) joinContinuations(e *LatencyEvidence) (map[string]int, map[string]int) {
	content, audio := map[string]int{}, map[string]int{}
	children := map[string]int{}
	blocked := map[string]bool{}
	clocks := map[string][2]int{}
	e.continuationInputs = map[string]continuationInput{}
	for _, link := range cb.continuations {
		children[link.Get("turn")]++
		id := link.Get("origin_turn")
		clock := [2]int{link.N("origin_speech_end_ms"), link.N("origin_end_silence_ms")}
		if previous, ok := clocks[id]; ok && previous != clock && !blocked[id] {
			blocked[id] = true
			e.InvalidLinks++
			e.Unverified++
		}
		clocks[id] = clock
	}
	for _, link := range cb.continuations {
		child := cb.turns[link.Get("turn")]
		result, valid := cb.linkedContent(link)
		if !valid || children[link.Get("turn")] != 1 {
			blocked[link.Get("origin_turn")] = true
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
		if origin == nil || origin.latency == nil {
			if origin != nil && len(origin.outputText) > 0 {
				blocked[result.OriginTurn] = true
			}
			sample := continuationInput{AudioMS: result.PauseMS + result.OffsetMS + result.ChildAudioMS, ContentMS: result.TotalMS}
			if previous, ok := e.continuationInputs[result.OriginTurn]; ok {
				sample.AudioMS = min(previous.AudioMS, sample.AudioMS)
				sample.ContentMS = min(previous.ContentMS, sample.ContentMS)
			}
			e.continuationInputs[result.OriginTurn] = sample
		}
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
	for id := range blocked {
		delete(e.continuationInputs, id)
	}
	e.ContinuationsJoined = len(content)
	return content, audio
}
