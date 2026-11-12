package duplex

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/chuanmingliu/golive/internal/live"
	"github.com/chuanmingliu/golive/internal/provider/deepseek"
	"github.com/chuanmingliu/golive/internal/provider/mock"
	"github.com/chuanmingliu/golive/internal/requestlog"
)

func TestSessionRequestRecordingIncludesRenderedSkill(t *testing.T) {
	received := make(chan []byte, 4)
	endpoint := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			w.WriteHeader(http.StatusOK)
			return
		}
		body, _ := io.ReadAll(r.Body)
		received <- body
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, "data: {\"choices\":[{\"delta\":{\"content\":\"好的。\"},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n")
	}))
	defer endpoint.Close()
	directory := t.TempDir()
	recording, err := requestlog.Open(directory)
	if err != nil {
		t.Fatal(err)
	}
	opts := testConfig()
	opts.ToolsMemory = true
	opts.Duplex.BackendPrefixWarm = false
	opts.Duplex.BackendPrefixWarmStart = false
	opts.SkillsMCP = skillFixture(t)
	collector := newCollector(opts.ClientRate)
	llm := &deepseek.LLM{BaseURL: endpoint.URL, APIKey: "fixture", DefaultModel: "fixture", HTTP: endpoint.Client(), Requests: recording}
	engine := New(Options{Cfg: opts, SessionID: "recording-session", Instructions: "原始业务提示", ClientRate: opts.ClientRate}, Deps{ASR: mock.NewASR(""), LLM: llm, TTS: mock.NewTTS(), Emit: collector.emit, Log: slog.New(slog.NewTextHandler(io.Discard, nil))})
	ctx, cancel := context.WithCancel(context.Background())
	engine.Start(ctx)
	defer func() { engine.Close(); cancel() }()
	engine.SubmitItem(live.ResponseItem{Type: "message", Role: "user", Content: live.TextContent("input_text", "修改报价")}, "")
	engine.ContinueResponse("")
	var wire []byte
	select {
	case wire = <-received:
	case <-time.After(4 * time.Second):
		t.Fatal("no request")
	}
	waitFor(t, "recording completion", time.Second, func() bool {
		paths, _ := filepath.Glob(filepath.Join(directory, "recording-session", "*", "metadata.json"))
		if len(paths) != 1 {
			return false
		}
		data, _ := os.ReadFile(paths[0])
		var metadata requestlog.Metadata
		json.Unmarshal(data, &metadata)
		return !metadata.FinishedAt.IsZero()
	})
	paths, _ := filepath.Glob(filepath.Join(directory, "recording-session", "*", "metadata.json"))
	raw, _ := os.ReadFile(paths[0])
	var metadata requestlog.Metadata
	json.Unmarshal(raw, &metadata)
	if metadata.SessionID != "recording-session" || metadata.TurnID == "" || metadata.ResponseID == "" || metadata.Purpose != "turn" {
		t.Fatal(metadata)
	}
	saved, _ := os.ReadFile(filepath.Join(filepath.Dir(paths[0]), "request.json"))
	if !bytes.Equal(saved, wire) || !bytes.Contains(saved, []byte("原始业务提示")) || !bytes.Contains(saved, []byte("Ask the caller to open WeChat")) {
		t.Fatal("recording lost exact wire bytes, original prompt or rendered skill")
	}
}
