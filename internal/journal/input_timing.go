package journal

// A versioned input snapshot proves the caller clock without claiming that
// the calling turn played. Legacy links still require their playback record.
// Any supplied playback evidence must agree; duplicate/malformed records do
// not become acceptable merely because an input snapshot is also available.
func linkedOriginTiming(link Line, origin *turnBuild) (pause, audio int, hasAudio, input, valid bool) {
	for _, key := range []string{"origin_timing_version", "origin_speech_end_ms", "origin_end_silence_ms"} {
		if _, exists := link.kv[key]; exists {
			input = true
		}
	}
	if input {
		version, versionOK := link.Int("origin_timing_version")
		end, endOK := link.Int("origin_speech_end_ms")
		var pauseOK bool
		pause, pauseOK = link.Int("origin_end_silence_ms")
		if !versionOK || version != 1 || !endOK || end < 0 || !pauseOK || pause < 0 {
			return
		}
	}
	if origin == nil || origin.latency == nil {
		valid = input
		return
	}
	if origin.latencyCount != 1 {
		return
	}
	o := *origin.latency
	op, hasPause := o.Int("end_silence_ms")
	if !hasPause || op < 0 || input && op != pause || o.Get("response_to_speech") != "true" ||
		!boolKnown(o, "first_audio_present") || !boolKnown(o, "first_content_present") ||
		!boolKnown(o, "truncated") || !boolKnown(o, "opened") {
		return
	}
	pause = op
	hasAudio = o.Get("first_audio_present") == "true"
	if hasAudio {
		var known bool
		audio, known = o.Int("first_audio_out_ms")
		total, fits := latencySum(audio, pause)
		if !known || !fits || total < 0 {
			return
		}
	}
	cancelled := o.Get("first_content_present") == "false" && o.Get("truncated") == "true"
	valid = input || hasAudio && !cancelled
	return
}
