package duplex

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"strings"
	"testing"

	"github.com/chuanmingliu/golive/internal/provider"
	"github.com/chuanmingliu/golive/internal/provider/mock"
)

func TestAuthoritativeAddV(t *testing.T) {
	cfg := testConfig()
	cfg.ToolsMemory = true
	e := New(Options{Cfg: cfg}, Deps{ASR: mock.NewASR(""), LLM: &skillScenarioLLM{}, TTS: mock.NewTTS(), Emit: newCollector(cfg.ClientRate).emit, Log: slog.New(slog.NewTextHandler(io.Discard, nil))})
	e.Start(context.Background())
	defer e.Close()
	if e.ApplyBusinessEvent("unknown") {
		t.Fatal("unsupported event accepted")
	}
	probeBarrier(e)
	if !e.ApplyBusinessEvent("addV") {
		t.Fatal("addV rejected")
	}
	probeBarrier(e)
	before, version := e.MemorySnapshot()
	e.ApplyBusinessEvent("addV")
	probeBarrier(e)
	after, repeated := e.MemorySnapshot()
	if version != repeated || string(before) != string(after) || !strings.Contains(string(after), `"status":"added"`) {
		t.Fatal("non-idempotent completion")
	}
	for _, raw := range []string{`{"wechat":null}`, `{"wechat":"consented"}`, `{"wechat":{"status":"declined"},"quote":{"三者险":"300万"}}`} {
		var output string
		e.post(func() { output = e.applyMemoryCall(provider.ToolCall{Arguments: raw}) })
		probeBarrier(e)
		state, _ := e.MemorySnapshot()
		if !strings.Contains(output, `"ok":true`) || !strings.Contains(string(state), `"status":"added"`) {
			t.Fatal("model undid backend completion", output, string(state))
		}
	}
}

func TestModelCannotInventAddV(t *testing.T) {
	e := &Engine{}
	_, ok := e.protectBusinessState(json.RawMessage(`{"wechat":{"status":"added"}}`))
	if ok {
		t.Fatal("model invented backend completion")
	}
	raw := json.RawMessage(`{"wechat":{"status":"consented"}}`)
	got, ok := e.protectBusinessState(raw)
	if !ok || string(raw) != string(got) {
		t.Fatal("confirmed consent rejected")
	}
}

func TestInterruptedRequestPreservesContextAndIdentifiesLatestIntent(t *testing.T) {
	cfg := testConfig()
	c := newCollector(cfg.ClientRate)
	e := New(Options{Cfg: cfg}, Deps{ASR: mock.NewASR(""), LLM: &skillScenarioLLM{}, TTS: mock.NewTTS(), Emit: c.emit, Log: slog.New(slog.NewTextHandler(io.Discard, nil))})
	e.Start(context.Background())
	defer e.Close()
	var messages []provider.Message
	e.post(func() {
		e.lastCommitted = &Turn{ID: "old", Transcript: "怎么找微信服务通知？"}
		e.interrupt()
		turn := &Turn{ID: "new", Transcript: "保险从什么时候开始生效？"}
		e.commitTurn(turn)
		messages = e.snapshotMessages(turn, false)
	})
	probeBarrier(e)
	if got := latestUserText(messages); got != "保险从什么时候开始生效？" {
		t.Fatalf("caller context lost: %s", got)
	}
	if !strings.Contains(dump(messages), "怎么找微信服务通知？") || !strings.Contains(dump(messages), "上一轮回答被来电人打断") {
		t.Fatal("latest request missing from recovery policy")
	}
}

func TestBusinessNotesAndCompletionAssertion(t *testing.T) {
	if businessStateNote(json.RawMessage(`{"quote":{}}`)) != "" {
		t.Fatal("unrelated memory gained WeChat instructions")
	}
	cases := []struct {
		text string
		want bool
	}{
		{"微信已经添加成功了，不用再搜索服务通知了。", true},
		{"微信已经添加成功了，请搜索服务通知。", false},
		{"请打开微信。", false},
	}
	for _, tc := range cases {
		row := realSkillRow{Trace: []*modelProbeCall{{Text: tc.text}}}
		if acknowledgesAdded(row) != tc.want {
			t.Fatalf("incorrect completion assertion for %q", tc.text)
		}
	}
}
