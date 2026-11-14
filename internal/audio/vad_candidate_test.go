package audio

import (
	"reflect"
	"testing"
)

func TestCandidateSnapshotIsExactOpeningPrefix(t *testing.T) {
	v := NewVAD(VADConfig{SampleRate: RatePipeline, MinSpeechMS: 320})
	signal := append(quiet(RatePipeline, 300), tone(RatePipeline, 600, .45)...)
	var prefix []float32
	var start int64
	for off := 0; off+v.FrameSamples() <= len(signal); off += v.FrameSamples() {
		d := v.Push(signal[off : off+v.FrameSamples()])
		if d.Kind == DecisionStarted {
			if len(prefix) == 0 || start != d.StartMS || !reflect.DeepEqual(prefix, d.Snapshot[:len(prefix)]) {
				t.Fatal("candidate audio is not an exact, once-only opening prefix")
			}
			if pcm, _ := v.CandidateSnapshot(); len(pcm) != 0 {
				t.Fatal("confirmed utterance still exposed as a candidate")
			}
			return
		}
		pcm, at := v.CandidateSnapshot()
		if len(pcm) > 0 {
			if len(prefix) > 0 && (at != start || !reflect.DeepEqual(prefix, pcm[:len(prefix)])) {
				t.Fatal("candidate prefix changed while accumulating")
			}
			prefix, start = pcm, at
			// The returned memory must not alias the detector's buffers.
			copyPCM, _ := v.CandidateSnapshot()
			copyPCM[0] = 99
		}
	}
	t.Fatal("no confirmed utterance")
}

func TestRejectedCandidateSnapshotDisappears(t *testing.T) {
	v := NewVAD(VADConfig{SampleRate: RatePipeline, MinSpeechMS: 320})
	feed(v, tone(RatePipeline, 80, .45))
	if pcm, _ := v.CandidateSnapshot(); len(pcm) == 0 {
		t.Fatal("candidate missing")
	}
	feed(v, quiet(RatePipeline, 500))
	if pcm, _ := v.CandidateSnapshot(); len(pcm) != 0 {
		t.Fatal("rejected candidate retained")
	}
}
