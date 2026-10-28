package desk

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strings"
	"time"
	"unicode"

	"github.com/chuanmingliu/golive/internal/tools"
)

// The talker, tried from the web.
//
// Codex trained a Qwen3-0.6B "talker" on the Mac (10-28): given the
// conversation so far, the caller's words and the opener the engine has
// already said at the pause, it writes the answer's first short clause — the
// thing the chat model's first token is waited for on every turn. It is
// served there by llama-server. The owner asked (18:31) for a page to
// experience it directly: this is that page, on the desk, which is on the
// home network like the Mac. The desk renders the prompt exactly as the
// training did (Codex's prompt.py, the contract below), streams the Mac's
// completion to the browser as it comes, and judges the clause by the same
// rule the evaluation used. What the page shows beside the words is what
// the engine would see: the time to the first token and to a complete
// clause, counted from the request, next to the 380 ms pause the opener
// covers.
//
// Nothing typed on the page is logged: the attempt's numbers go to the work
// log stream `talker-web` (ttft, clause, total, verdict) so they chart on
// the Work log page; the words stay in the browser and in the Mac's slot.

// Talker is where the model is served.
type Talker struct {
	// URL is llama-server's base, e.g. http://192.168.71.101:18081; empty
	// turns the page off.
	URL string
	// Timeout bounds one completion.
	Timeout time.Duration
	// Client is the HTTP client; the default has no proxy.
	Client *http.Client
}

// The prompt contract, shared with the training (prompt.py).
const (
	talkerSystem = "你是电话对话助手。根据对话和来电者刚说的话，紧接已经说出的开场词，" +
		"只写回答的第一个自然短分句，正文不超过12个字，以标点结束。" +
		"不要重复开场词，不要解释，不要编造未提供的信息。"
	talkerEnd        = "<|im_end|>"
	talkerPrefill    = "<|im_start|>assistant\n<think>\n\n</think>\n\n"
	talkerClauseMax  = 12
	talkerBoundaries = "，。！？；：,.!?;:\n"
	talkerMaxTurns   = 6
	talkerMaxChars   = 768
	talkerPredict    = 48
)

var (
	talkerSpecial = regexp.MustCompile(`<\|[^\n]*?\|>|</?think>`)
	talkerAcks    = map[string]bool{"嗯": true, "嗯嗯": true, "好": true, "好的": true, "好呀": true, "好啊": true, "明白": true, "了解": true, "收到": true, "行": true, "行的": true}
)

// talkerMessage is one prior turn.
type talkerMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

// talkerRequest is what the page sends.
type talkerRequest struct {
	Context  []talkerMessage `json:"context"`
	Caller   string          `json:"caller"`
	Opener   string          `json:"opener"`
	MaxTurns *int            `json:"max_context_turns,omitempty"`
	Predict  int             `json:"n_predict,omitempty"`
}

func talkerWrap(role, text string) string {
	// Literal template delimiters in speech must not create another role.
	text = talkerSpecial.ReplaceAllStringFunc(text, func(m string) string {
		return strings.NewReplacer("<", "＜", ">", "＞").Replace(m)
	})
	return "<|im_start|>" + role + "\n" + text + talkerEnd + "\n"
}

// renderTalkerPrompt is prompt.py's render_prompt: no-thinking Qwen ChatML
// ending in the already-spoken opener. Old exchanges are dropped whole to
// fit the character budget; the current turn is never shortened.
func renderTalkerPrompt(ctx []talkerMessage, caller, opener string, maxTurns, maxChars int) (string, error) {
	if maxTurns < 0 || maxChars < 1 {
		return "", fmt.Errorf("invalid prompt budget")
	}
	var groups [][]talkerMessage
	var pending *talkerMessage
	for _, m := range ctx {
		if strings.TrimSpace(m.Content) == "" {
			continue
		}
		switch m.Role {
		case "user":
			u := m
			pending = &u
		case "assistant":
			if pending != nil {
				groups = append(groups, []talkerMessage{*pending, m})
				pending = nil
			} else if len(groups) == 0 {
				groups = append(groups, []talkerMessage{m})
			}
		}
	}
	if maxTurns == 0 {
		groups = nil
	} else if len(groups) > maxTurns {
		groups = groups[len(groups)-maxTurns:]
	}
	head := talkerWrap("system", talkerSystem)
	// A formatting newline after the opener keeps the tokenizer from merging
	// across the prompt/label boundary; identical in training and serving.
	tail := talkerWrap("user", caller) + talkerPrefill + talkerSpecial.ReplaceAllString(opener, "") + "\n"
	if len([]rune(head+tail)) > maxChars {
		return "", fmt.Errorf("the caller's words and the opener exceed the prompt budget (%d characters)", maxChars)
	}
	for len(groups) > 0 {
		var middle strings.Builder
		for _, g := range groups {
			for _, m := range g {
				middle.WriteString(talkerWrap(m.Role, m.Content))
			}
		}
		if len([]rune(head+middle.String()+tail)) <= maxChars {
			return head + middle.String() + tail, nil
		}
		groups = groups[1:]
	}
	return head + tail, nil
}

// talkerVisible counts content characters: punctuation and spaces do not
// spend the cap.
func talkerVisible(s string) int {
	n := 0
	for _, r := range s {
		if !unicode.IsSpace(r) && !unicode.IsPunct(r) {
			n++
		}
	}
	return n
}

// talkerJudge is prompt.py's first_clause with the reasons spelled out:
// the clause the engine would say, and why not when there is none.
//   - "ok": a complete clause within the cap
//   - "empty", "special": nothing usable
//   - "ack": a bare acknowledgement (嗯, 好的…), which the opener already was
//   - "long": the first clause is over 12 visible characters
//   - "incomplete": no boundary and the model did not stop (n_predict hit)
func talkerJudge(text string, eos bool) (clause, verdict string) {
	text = strings.TrimSpace(text)
	if text == "" {
		return "", "empty"
	}
	if talkerSpecial.MatchString(text) {
		return "", "special"
	}
	rs := []rune(text)
	end := len(rs)
	for i, r := range rs {
		if strings.ContainsRune(talkerBoundaries, r) {
			end = i
			break
		}
	}
	if end < len(rs) {
		end++
		for end < len(rs) && strings.ContainsRune(talkerBoundaries, rs[end]) {
			end++
		}
	} else if !eos {
		return text, "incomplete"
	}
	clause = strings.TrimSpace(string(rs[:end]))
	n := talkerVisible(clause)
	var norm strings.Builder
	for _, r := range clause {
		if !unicode.IsSpace(r) && !unicode.IsPunct(r) {
			norm.WriteRune(r)
		}
	}
	switch {
	case n == 0:
		return "", "empty"
	case talkerAcks[norm.String()]:
		return clause, "ack"
	case n > talkerClauseMax:
		return clause, "long"
	}
	return clause, "ok"
}

// talkerOpeners is the engine's default scenarios, for the page to pick an
// opener the way the engine would (tools.Openers.Match).
func talkerOpeners() tools.Openers {
	o, err := tools.ParseOpeners([]byte(tools.DefaultOpeners))
	if err != nil {
		return tools.Openers{}
	}
	return o
}

func (t *Talker) client() *http.Client {
	if t.Client != nil {
		return t.Client
	}
	return &http.Client{Transport: &http.Transport{Proxy: nil}}
}

func (t *Talker) timeout() time.Duration {
	if t.Timeout > 0 {
		return t.Timeout
	}
	return 30 * time.Second
}

// probe asks the server whether it is up and what it serves.
func (t *Talker) probe(ctx context.Context) (up bool, model string, ms int64) {
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	start := time.Now()
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(t.URL, "/")+"/v1/models", nil)
	resp, err := t.client().Do(req)
	if err != nil {
		return false, "", 0
	}
	defer resp.Body.Close()
	ms = time.Since(start).Milliseconds()
	var body struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&body) == nil && len(body.Data) > 0 {
		model = body.Data[0].ID
	}
	return resp.StatusCode == http.StatusOK, model, ms
}

// talkerResult is what one attempt came to.
type talkerResult struct {
	Text       string  `json:"text"`
	Clause     string  `json:"clause"`
	Verdict    string  `json:"verdict"`
	EOS        bool    `json:"eos"`
	TTFTMs     float64 `json:"ttft_ms"`
	ClauseMs   float64 `json:"clause_ms"`
	TotalMs    float64 `json:"total_ms"`
	Tokens     int     `json:"tokens"`
	PromptN    int     `json:"prompt_tokens"`
	CachedN    int     `json:"cached_tokens"`
	PromptMs   float64 `json:"prompt_ms"`
	PredictMs  float64 `json:"predict_ms"`
	PromptRune int     `json:"prompt_chars"`
}

// mountTalker adds the page's routes.
func (s *Service) mountTalker(mux *http.ServeMux) {
	t := s.cfg.Talker
	mux.HandleFunc("GET /api/talker", s.authed(func(w http.ResponseWriter, r *http.Request, who string) error {
		if t == nil || t.URL == "" {
			writeJSON(w, http.StatusOK, map[string]any{"enabled": false})
			return nil
		}
		up, model, ms := t.probe(r.Context())
		writeJSON(w, http.StatusOK, map[string]any{"enabled": true, "url": t.URL, "up": up, "model": model, "probe_ms": ms,
			"openers": talkerOpeners().Scenarios, "clause_max": talkerClauseMax, "system": talkerSystem, "n_predict": talkerPredict})
		return nil
	}))
	// Renders the prompt without asking the model: what the page shows under
	// "the prompt".
	mux.HandleFunc("POST /api/talker/prompt", s.authed(func(w http.ResponseWriter, r *http.Request, who string) error {
		var req talkerRequest
		if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&req); err != nil {
			return fail(http.StatusBadRequest, "not JSON: %v", err)
		}
		p, err := renderTalkerPrompt(req.Context, req.Caller, req.Opener, req.maxTurns(), talkerMaxChars)
		if err != nil {
			return fail(http.StatusBadRequest, "%v", err)
		}
		writeJSON(w, http.StatusOK, map[string]any{"prompt": p, "chars": len([]rune(p))})
		return nil
	}))
	// The attempt: an SSE stream of the model's text as it arrives, then the
	// result. The timings are the desk's clock from the request's send.
	mux.HandleFunc("POST /api/talker", s.authed(func(w http.ResponseWriter, r *http.Request, who string) error {
		if t == nil || t.URL == "" {
			return fail(http.StatusNotFound, "no talker is configured")
		}
		var req talkerRequest
		if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&req); err != nil {
			return fail(http.StatusBadRequest, "not JSON: %v", err)
		}
		if strings.TrimSpace(req.Caller) == "" {
			return fail(http.StatusBadRequest, "say what the caller said")
		}
		prompt, err := renderTalkerPrompt(req.Context, req.Caller, req.Opener, req.maxTurns(), talkerMaxChars)
		if err != nil {
			return fail(http.StatusBadRequest, "%v", err)
		}
		fl, ok := w.(http.Flusher)
		if !ok {
			return fail(http.StatusNotImplemented, "streaming is not possible here")
		}
		predict := req.Predict
		if predict <= 0 || predict > 128 {
			predict = talkerPredict
		}
		body, _ := json.Marshal(map[string]any{
			"prompt": prompt, "n_predict": predict, "stream": true, "cache_prompt": true,
			"temperature": 0, "seed": 0, "stop": []string{talkerEnd},
		})
		ctx, cancel := context.WithTimeout(r.Context(), t.timeout())
		defer cancel()
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-store")
		w.WriteHeader(http.StatusOK)
		send := func(ev string, v any) {
			b, _ := json.Marshal(v)
			_, _ = io.WriteString(w, "event: "+ev+"\ndata: "+string(b)+"\n\n")
			fl.Flush()
		}
		res, err := t.complete(ctx, body, func(text string, ms float64) { send("token", map[string]any{"text": text, "ms": ms}) })
		if err != nil {
			send("error", map[string]any{"error": err.Error()})
			return nil
		}
		res.PromptRune = len([]rune(prompt))
		send("done", res)
		// Numbers only to the work log; never the words.
		s.work.append("talker-web", who, []string{fmt.Sprintf("metric: ttft=%.0f clause=%.0f total=%.0f tokens=%d prompt_tokens=%d cached=%d verdict=%s",
			res.TTFTMs, res.ClauseMs, res.TotalMs, res.Tokens, res.PromptN, res.CachedN, res.Verdict)}, s.cfg.Now())
		return nil
	}))
}

func (r talkerRequest) maxTurns() int {
	if r.MaxTurns != nil && *r.MaxTurns >= 0 {
		return *r.MaxTurns
	}
	return talkerMaxTurns
}

// complete streams llama-server's native /completion and times it.
func (t *Talker) complete(ctx context.Context, body []byte, onToken func(string, float64)) (talkerResult, error) {
	var res talkerResult
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(t.URL, "/")+"/completion", bytes.NewReader(body))
	if err != nil {
		return res, err
	}
	req.Header.Set("Content-Type", "application/json")
	start := time.Now()
	resp, err := t.client().Do(req)
	if err != nil {
		return res, fmt.Errorf("the talker could not be reached: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return res, fmt.Errorf("the talker answered HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(raw)))
	}
	var text strings.Builder
	sc := bufio.NewScanner(resp.Body)
	sc.Buffer(make([]byte, 64<<10), 1<<20)
	clauseAt := 0.0
	for sc.Scan() {
		line := sc.Text()
		data, ok := strings.CutPrefix(line, "data: ")
		if !ok {
			continue
		}
		var ev struct {
			Content string `json:"content"`
			Stop    bool   `json:"stop"`
			Timings struct {
				PromptN   int     `json:"prompt_n"`
				PromptMs  float64 `json:"prompt_ms"`
				PredictN  int     `json:"predicted_n"`
				PredictMs float64 `json:"predicted_ms"`
				CacheN    int     `json:"cache_n"`
			} `json:"timings"`
			TokensCached int `json:"tokens_cached"`
			TokensPred   int `json:"tokens_predicted"`
			StoppedEOS   bool `json:"stopped_eos"`
			StoppedWord  bool `json:"stopped_word"`
		}
		if json.Unmarshal([]byte(data), &ev) != nil {
			continue
		}
		ms := float64(time.Since(start).Microseconds()) / 1000
		if ev.Content != "" {
			if text.Len() == 0 && strings.TrimSpace(ev.Content) != "" {
				res.TTFTMs = ms
			}
			text.WriteString(ev.Content)
			onToken(ev.Content, ms)
			if clauseAt == 0 {
				if c, v := talkerJudge(text.String(), false); v != "incomplete" && c != "" {
					clauseAt = ms
				}
			}
		}
		if ev.Stop {
			res.EOS = ev.StoppedEOS || ev.StoppedWord
			res.Tokens = ev.TokensPred
			res.PromptN = ev.Timings.PromptN
			res.PromptMs = ev.Timings.PromptMs
			res.PredictMs = ev.Timings.PredictMs
			res.CachedN = ev.TokensCached - ev.Timings.PromptN
			if ev.Timings.CacheN > 0 {
				res.CachedN = ev.Timings.CacheN
			}
			if res.CachedN < 0 {
				res.CachedN = 0
			}
			break
		}
	}
	if err := sc.Err(); err != nil {
		return res, fmt.Errorf("the stream broke: %v", err)
	}
	res.TotalMs = float64(time.Since(start).Microseconds()) / 1000
	res.Text = text.String()
	res.Clause, res.Verdict = talkerJudge(res.Text, res.EOS)
	if res.Verdict == "ok" || res.Verdict == "long" || res.Verdict == "ack" {
		if clauseAt == 0 {
			clauseAt = res.TotalMs
		}
		res.ClauseMs = clauseAt
	}
	return res, nil
}
