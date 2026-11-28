package duplex

import (
	"testing"

	"github.com/chuanmingliu/golive/internal/provider"
)

func TestStreamingGuardRemovesInnerMonologueBeforeSpeechAndTranscript(t *testing.T) {
	deltas := []provider.LLMDelta{
		{Text: "对，乘客这一档您还没定，"},
		{Text: "我"}, {Text: "不"}, {Text: "猜"}, {Text: "。"},
		{Text: "乘客的医保外用药有共享保额、1万、2万、5万、10万、20万，您想选哪一档？"},
	}
	want := deltas[0].Text + deltas[5].Text
	got := runReadoutBackend(t, "", "", deltas, "")
	if got.output != want || got.audioMS == 0 || got.deltas != want || got.response != want || got.completedText != want {
		t.Fatalf("expected clean audio and transcript; got %+v, want %q", got, want)
	}
}
