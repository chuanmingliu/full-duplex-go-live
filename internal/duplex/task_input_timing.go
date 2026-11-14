package duplex

// Capture the originating input once, independently of whether its speculative
// answer ever plays. Never borrow the engine's current caller clock at delivery.
func taskOriginFromTurn(turn *Turn) taskOrigin {
	timing := turn.Timings()
	return taskOrigin{turnID: turn.ID, speechEndAt: timing.SpeechEndAt,
		inputKnown:  !timing.SpeechEndAt.IsZero() && timing.SpeechEndMS >= 0 && timing.EndSilenceMS >= 0,
		speechEndMS: timing.SpeechEndMS, endSilenceMS: timing.EndSilenceMS}
}

func (origin taskOrigin) appendTiming(attrs []any, result *Turn) []any {
	attrs = append(attrs, "origin_turn", origin.turnID,
		"origin_to_result_start_ms", result.Timings().StartedAt.Sub(origin.speechEndAt).Milliseconds())
	if origin.inputKnown {
		attrs = append(attrs, "origin_timing_version", 1,
			"origin_speech_end_ms", origin.speechEndMS, "origin_end_silence_ms", origin.endSilenceMS)
	}
	return attrs
}
