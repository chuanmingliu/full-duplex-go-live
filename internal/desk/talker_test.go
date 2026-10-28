package desk

import (
	"bufio"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"
)

// testdata/talker_golden.json is the output of Codex's prompt.py (the
// training's renderer and clause rule) on a few cases; the desk must render
// and judge to the character, or the page would show a prompt the model
// was not trained on.
func TestTheTalkersPromptIsRenderedAsTheTrainingRenderedIt(t *testing.T) {
	raw, err := os.ReadFile("testdata/talker_golden.json")
	if err != nil {
		t.Fatal(err)
	}
	var g struct {
		Render []struct {
			In struct {
				Context  []talkerMessage `json:"context"`
				Caller   string          `json:"caller"`
				Opener   string          `json:"opener"`
				MaxTurns *int            `json:"max_context_turns"`
			} `json:"in"`
			Prompt string `json:"prompt"`
		} `json:"render"`
		Judge [][2]*string `json:"judge"`
	}
	if err := json.Unmarshal(raw, &g); err != nil {
		t.Fatal(err)
	}
	for i, c := range g.Render {
		req := talkerRequest{Context: c.In.Context, Caller: c.In.Caller, Opener: c.In.Opener, MaxTurns: c.In.MaxTurns}
		got, err := renderTalkerPrompt(req.Context, req.Caller, req.Opener, req.maxTurns(), talkerMaxChars)
		if err != nil {
			t.Fatalf("case %d: %v", i, err)
		}
		if got != c.Prompt {
			t.Errorf("case %d: the prompt differs from the training's\n got: %q\nwant: %q", i, got, c.Prompt)
		}
	}
	for i, j := range g.Judge {
		clause, verdict := talkerJudge(*j[0], true)
		if j[1] == nil {
			if verdict == "ok" {
				t.Errorf("judge %d: %q accepted as %q; the training rejects it", i, *j[0], clause)
			}
			continue
		}
		if verdict != "ok" || clause != *j[1] {
			t.Errorf("judge %d: %q → %q (%s); the training says %q", i, *j[0], clause, verdict, *j[1])
		}
	}
	// Without EOS an unpunctuated answer is not a clause yet.
	if _, v := talkerJudge("三千多", false); v != "incomplete" {
		t.Errorf("an unfinished answer judged %q", v)
	}
	if c, v := talkerJudge("三千多", true); v != "ok" || c != "三千多" {
		t.Errorf("a finished unpunctuated answer judged %q %q", c, v)
	}
}

// The attempt streams the Mac's tokens through to the browser and times
// the first token and the complete clause from the request; the numbers,
// never the words, go to the work log.
func TestATalkerAttemptIsStreamedAndTimed(t *testing.T) {
	var gotPrompt string
	mac := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/models" {
			_, _ = w.Write([]byte(`{"data":[{"id":"local-talker"}]}`))
			return
		}
		var req map[string]any
		_ = json.NewDecoder(r.Body).Decode(&req)
		gotPrompt, _ = req["prompt"].(string)
		w.Header().Set("Content-Type", "text/event-stream")
		fl := w.(http.Flusher)
		for _, tok := range []string{"三千", "二百", "，", "一年。"} {
			time.Sleep(15 * time.Millisecond)
			fmt.Fprintf(w, "data: %s\n\n", must(json.Marshal(map[string]any{"content": tok, "stop": false})))
			fl.Flush()
		}
		fmt.Fprintf(w, "data: %s\n\n", must(json.Marshal(map[string]any{"content": "", "stop": true, "stopped_eos": true, "tokens_predicted": 5, "tokens_cached": 150,
			"timings": map[string]any{"prompt_n": 20, "prompt_ms": 12.5, "predicted_n": 5, "predicted_ms": 60.0}})))
		fl.Flush()
	}))
	defer mac.Close()
	dir := t.TempDir()
	s, err := Open(Config{Dir: dir, Now: time.Now, Talker: &Talker{URL: mac.URL}})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	srv := httptest.NewServer(s.Handler())
	defer srv.Close()
	tok := s.tokens["owner"]

	status := get(t, srv.URL+"/api/talker", tok)
	if status["up"] != true || status["model"] != "local-talker" {
		t.Fatalf("status %v", status)
	}
	if n := len(status["openers"].([]any)); n == 0 {
		t.Fatal("no opener scenarios for the page")
	}

	body := `{"context":[{"role":"user","content":"我想问保费"},{"role":"assistant","content":"好的。"}],"caller":"三者险一百万多少钱","opener":"价格嘛，"}`
	req, _ := http.NewRequest(http.MethodPost, srv.URL+"/api/talker", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+tok)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var tokens []string
	var done talkerResult
	sc := bufio.NewScanner(resp.Body)
	ev := ""
	for sc.Scan() {
		line := sc.Text()
		if e, ok := strings.CutPrefix(line, "event: "); ok {
			ev = e
		} else if d, ok := strings.CutPrefix(line, "data: "); ok {
			switch ev {
			case "token":
				var v struct{ Text string }
				_ = json.Unmarshal([]byte(d), &v)
				tokens = append(tokens, v.Text)
			case "done":
				_ = json.Unmarshal([]byte(d), &done)
			case "error":
				t.Fatalf("error event: %s", d)
			}
		}
	}
	if strings.Join(tokens, "") != "三千二百，一年。" {
		t.Errorf("tokens %q", tokens)
	}
	if done.Clause != "三千二百，" || done.Verdict != "ok" || !done.EOS {
		t.Errorf("result %+v", done)
	}
	if done.TTFTMs < 10 || done.ClauseMs < done.TTFTMs || done.TotalMs < done.ClauseMs {
		t.Errorf("timings ttft %.1f clause %.1f total %.1f", done.TTFTMs, done.ClauseMs, done.TotalMs)
	}
	if done.PromptN != 20 || done.CachedN != 130 || done.Tokens != 5 {
		t.Errorf("counts %+v", done)
	}
	if !strings.Contains(gotPrompt, "<|im_start|>user\n三者险一百万多少钱<|im_end|>\n"+talkerPrefill+"价格嘛，\n") {
		t.Errorf("the Mac got the prompt %q", gotPrompt)
	}
	lines, _, _ := s.work.read("talker-web", 0, 10)
	if len(lines) != 1 || !strings.HasPrefix(lines[0].Text, "metric: ttft=") || strings.Contains(lines[0].Text, "三者") {
		t.Errorf("work log %+v", lines)
	}
}

func must(b []byte, err error) []byte {
	if err != nil {
		panic(err)
	}
	return b
}

func get(t *testing.T, url, tok string) map[string]any {
	t.Helper()
	req, _ := http.NewRequest(http.MethodGet, url, nil)
	req.Header.Set("Authorization", "Bearer "+tok)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var v map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&v); err != nil {
		t.Fatal(err)
	}
	return v
}
