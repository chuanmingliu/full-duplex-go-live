package desk

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
)

// Scenarios is how a scenario for the simulated customer is written, checked
// and reserved from the desk, without the simulator's own workbench (which
// listens on the box's loopback only) and without hand-editing JSON on the
// box. Shipped scenarios (Codex's, under scenarios/simulator) are read-only;
// the owner's go under scenarios/custom. Every scenario is checked by the
// simulator itself (POST /sim/v1/validate) before it is saved or reserved.
//
// A scenario can also be drafted from a real call in the journal: the
// caller's side of that conversation is read on the box and handed to the
// chat model with instructions to describe such a customer — persona, goal,
// how they behave over a long conversation, what to expect of the assistant
// — with every particular (names, plates, dates, numbers) replaced by an
// invented one. The transcript never leaves the box except to that model,
// which heard the call in the first place; the draft comes back synthetic,
// and is only a draft until the owner saves it.
type Scenarios struct {
	Dir          string // ~/golive-sim/scenarios: simulator/ (shipped) and custom/
	LastRun      string // ~/golive-sim/data/last-run, so `call sim` dials what was reserved here
	ProvidersEnv string // ~/golive-sim/providers.env, for DEEPSEEK_API_KEY (the simulator's own key)
	ChatURL      string // the chat model's base URL; DeepSeek's by default
	ChatModel    string

	calls *Calls // the simulator and the journal, the same way the calls page reaches them
}

var scenarioName = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,63}$`)

// ScenarioRow is one entry of the list.
type ScenarioRow struct {
	Name     string `json:"name"`
	Source   string `json:"source"` // shipped or custom
	ID       string `json:"id"`
	Language string `json:"language"`
	Goal     string `json:"goal,omitempty"`
	Turns    int    `json:"max_remote_turns"`
	Duration int64  `json:"max_duration_ms"`
	Modified string `json:"modified"`
}

func (sc *Scenarios) path(name string) (string, string, error) {
	if !scenarioName.MatchString(name) {
		return "", "", fail(http.StatusBadRequest, "a scenario name is lower-case letters, digits and dashes")
	}
	if p := filepath.Join(sc.Dir, "custom", name+".json"); fileExists(p) {
		return p, "custom", nil
	}
	if p := filepath.Join(sc.Dir, "simulator", name+".json"); fileExists(p) {
		return p, "shipped", nil
	}
	return filepath.Join(sc.Dir, "custom", name+".json"), "custom", nil
}

func fileExists(p string) bool {
	st, err := os.Stat(p)
	return err == nil && !st.IsDir()
}

func (sc *Scenarios) list() ([]ScenarioRow, error) {
	var rows []ScenarioRow
	for _, src := range []string{"shipped", "custom"} {
		dir := filepath.Join(sc.Dir, map[string]string{"shipped": "simulator", "custom": "custom"}[src])
		entries, err := os.ReadDir(dir)
		if err != nil {
			continue
		}
		for _, e := range entries {
			if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
				continue
			}
			data, err := os.ReadFile(filepath.Join(dir, e.Name()))
			if err != nil {
				continue
			}
			var s struct {
				ID       string `json:"id"`
				Language string `json:"language"`
				Goal     string `json:"goal"`
				Turns    int    `json:"max_remote_turns"`
				Duration int64  `json:"max_duration_ms"`
			}
			_ = json.Unmarshal(data, &s)
			info, _ := e.Info()
			mod := ""
			if info != nil {
				mod = info.ModTime().Format(time.RFC3339)
			}
			rows = append(rows, ScenarioRow{Name: strings.TrimSuffix(e.Name(), ".json"), Source: src, ID: s.ID, Language: s.Language,
				Goal: s.Goal, Turns: s.Turns, Duration: s.Duration, Modified: mod})
		}
	}
	sort.Slice(rows, func(i, j int) bool {
		if rows[i].Source != rows[j].Source {
			return rows[i].Source == "custom"
		}
		return rows[i].Name < rows[j].Name
	})
	return rows, nil
}

// validate asks the simulator; its answer is returned as it is.
func (sc *Scenarios) validate(ctx context.Context, body []byte) (int, json.RawMessage, error) {
	return sc.simPost(ctx, "/sim/v1/validate", body, nil)
}

func (sc *Scenarios) simPost(ctx context.Context, path string, body []byte, headers map[string]string) (int, json.RawMessage, error) {
	if sc.calls == nil || sc.calls.Simulator == "" {
		return 0, nil, errors.New("no simulator configured")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(sc.calls.Simulator, "/")+path, bytes.NewReader(body))
	if err != nil {
		return 0, nil, err
	}
	req.Header.Set("Authorization", "Bearer "+sc.calls.simToken())
	req.Header.Set("Content-Type", "application/json")
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := sc.calls.httpClient().Do(req)
	if err != nil {
		return 0, nil, err
	}
	defer resp.Body.Close()
	out, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return resp.StatusCode, nil, err
	}
	if !json.Valid(out) {
		out, _ = json.Marshal(map[string]string{"error": strings.TrimSpace(string(out))})
	}
	return resp.StatusCode, out, nil
}

// Draft is what the chat model is asked for, and what the page gets back.
type Draft struct {
	Persona      string            `json:"persona"`
	Goal         string            `json:"goal"`
	Behavior     []string          `json:"behavior"`
	Facts        map[string]string `json:"facts"`
	Greeting     string            `json:"greeting"`
	Expectations []struct {
		ID          string `json:"id"`
		Description string `json:"description"`
	} `json:"expectations"`
}

// draftFromCall reads a real call's conversation from the journal and asks
// the chat model for a synthetic customer in its image.
func (sc *Scenarios) draftFromCall(ctx context.Context, call string, maxTurns int) (map[string]any, error) {
	var jc struct {
		Call struct {
			ID         string  `json:"id"`
			DurationS  float64 `json:"duration_s"`
			Transcript []struct {
				Who  string `json:"who"`
				Text string `json:"text"`
			} `json:"transcript"`
		} `json:"call"`
		SimRun string `json:"sim_run"`
	}
	status, err := sc.calls.get(ctx, sc.calls.Journal, "/api/calls/"+url.PathEscape(call), "", &jc)
	if err != nil || status != http.StatusOK {
		return nil, fail(http.StatusNotFound, "the journal has no call %s", call)
	}
	if jc.SimRun != "" {
		return nil, fail(http.StatusBadRequest, "that was a simulated call; draft from a real one")
	}
	if len(jc.Call.Transcript) < 4 {
		return nil, fail(http.StatusBadRequest, "that call has too little conversation to draft from")
	}
	var lines []string
	callerTurns := 0
	for _, s := range jc.Call.Transcript {
		who := "助手"
		if s.Who == "caller" {
			who = "客户"
			callerTurns++
		}
		lines = append(lines, who+"："+strings.TrimSpace(s.Text))
	}
	key, err := envValue(sc.ProvidersEnv, "DEEPSEEK_API_KEY")
	if err != nil || key == "" {
		return nil, fail(http.StatusFailedDependency, "no DEEPSEEK_API_KEY in the simulator's providers.env; the draft needs the chat model")
	}
	if maxTurns <= 0 {
		maxTurns = 40
	}
	system := `你是语音对话测试的场景设计师。下面是一通真实电话录音的文字记录：助手是保险公司的外呼坐席，客户是接电话的人。
请根据这位客户在整通电话中的说话方式、关心的问题、犹豫和拒绝的原因、最终的态度，设计一个"模拟客户"的测试场景，用于让一个合成客户在多轮对话里扮演这类人，和助手对话 ` + fmt.Sprint(maxTurns) + ` 轮左右。
严格要求：
1. 所有具体信息都必须是虚构的：不要出现真实姓名、车牌、手机号、金额、日期、地址、公司名。用合理的虚构值代替，并把它们放进 facts。
2. persona：两三句话描述这位客户的性格、处境和说话风格。
3. goal：这位客户在这通电话里想达到什么（或想避免什么）。
4. behavior：8 到 12 条，按对话推进的顺序写：开头怎么接、会问什么、会质疑什么、什么情况下会同意或拒绝、怎么结束。每条一句话，让合成客户能照着做很多轮。
5. greeting：客户接起电话说的第一句话，像真人。
6. expectations：4 到 8 条对助手的检查项，来自这通电话里助手做得好或不好的地方；每条有 id（小写英文和下划线）和 description（中文，一句话，可独立判断）。
只输出一个 JSON 对象，键为 persona, goal, behavior, facts, greeting, expectations，不要任何别的文字。`
	payload := map[string]any{
		"model": sc.ChatModel,
		"messages": []map[string]string{
			{"role": "system", "content": system},
			{"role": "user", "content": strings.Join(lines, "\n")},
		},
		"temperature":     0.7,
		"max_tokens":      2000,
		"response_format": map[string]string{"type": "json_object"},
	}
	body, _ := json.Marshal(payload)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(sc.ChatURL, "/")+"/chat/completions", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+key)
	req.Header.Set("Content-Type", "application/json")
	resp, err := (&http.Client{Timeout: 90 * time.Second}).Do(req)
	if err != nil {
		return nil, fail(http.StatusBadGateway, "the chat model did not answer: %v", hideKey(err.Error(), key))
	}
	defer resp.Body.Close()
	out, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode != http.StatusOK {
		return nil, fail(http.StatusBadGateway, "the chat model answered %d", resp.StatusCode)
	}
	var completion struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	}
	if json.Unmarshal(out, &completion) != nil || len(completion.Choices) == 0 {
		return nil, fail(http.StatusBadGateway, "the chat model's answer had no choices")
	}
	var d Draft
	text := strings.TrimSpace(completion.Choices[0].Message.Content)
	text = strings.TrimPrefix(strings.TrimSuffix(text, "```"), "```json")
	if err := json.Unmarshal([]byte(text), &d); err != nil {
		return nil, fail(http.StatusBadGateway, "the chat model's answer was not the JSON asked for")
	}
	if len(d.Behavior) > 16 {
		d.Behavior = d.Behavior[:16]
	}
	if len(d.Expectations) > 16 {
		d.Expectations = d.Expectations[:16]
	}
	if d.Facts == nil {
		d.Facts = map[string]string{}
	}
	turns := maxTurns
	if turns > 100 {
		turns = 100
	}
	id := "from-call-" + strings.ToLower(strings.TrimPrefix(call, "out-"))
	return map[string]any{
		"schema_version":    1,
		"id":                id,
		"language":          "zh-CN",
		"persona":           d.Persona,
		"facts":             d.Facts,
		"greeting":          d.Greeting,
		"answer_delay_ms":   1500,
		"max_duration_ms":   290000,
		"max_remote_turns":  turns,
		"idle_timeout_ms":   20000,
		"actions":           []any{},
		"retain_transcript": true,
		"retain_audio":      false,
		"goal":              d.Goal,
		"behavior":          d.Behavior,
		"expectations":      d.Expectations,
		"expected_outcome":  "conversation",
		"_drafted_from":     map[string]any{"call": call, "caller_turns": callerTurns, "duration_s": jc.Call.DurationS},
	}, nil
}

func envValue(path, key string) (string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	for _, line := range strings.Split(string(data), "\n") {
		if v, ok := strings.CutPrefix(strings.TrimSpace(line), key+"="); ok {
			return strings.Trim(strings.TrimSpace(v), `"'`), nil
		}
	}
	return "", nil
}

func hideKey(s, key string) string {
	if key == "" {
		return s
	}
	return strings.ReplaceAll(s, key, "<key>")
}

// Mount adds the scenarios API behind the desk's tokens.
func (sc *Scenarios) Mount(mux *http.ServeMux, authed func(func(http.ResponseWriter, *http.Request, string) error) http.HandlerFunc, calls *Calls) {
	if sc == nil || sc.Dir == "" {
		return
	}
	sc.calls = calls
	if sc.ChatURL == "" {
		sc.ChatURL = "https://api.deepseek.com"
	}
	if sc.ChatModel == "" {
		sc.ChatModel = "deepseek-chat"
	}
	mux.HandleFunc("GET /api/scenarios", authed(func(w http.ResponseWriter, r *http.Request, who string) error {
		rows, err := sc.list()
		if err != nil {
			return err
		}
		if rows == nil {
			rows = []ScenarioRow{}
		}
		writeJSON(w, http.StatusOK, map[string]any{"scenarios": rows})
		return nil
	}))
	// Real calls to draft from: the journal's list, simulated ones left out.
	mux.HandleFunc("GET /api/scenarios/calls", authed(func(w http.ResponseWriter, r *http.Request, who string) error {
		var ov struct {
			Calls []struct {
				ID        string  `json:"id"`
				StartedAt string  `json:"started_at"`
				DurationS float64 `json:"duration_s"`
				Kind      string  `json:"kind"`
				Summary   struct {
					Turns int `json:"turns"`
				} `json:"summary"`
			} `json:"calls"`
		}
		ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
		defer cancel()
		if _, err := calls.get(ctx, calls.Journal, "/api/overview", "", &ov); err != nil {
			return fail(http.StatusBadGateway, "the journal did not answer: %v", err)
		}
		out := []map[string]any{}
		for _, c := range ov.Calls {
			if c.Kind == "simulated" || c.Summary.Turns < 2 {
				continue
			}
			out = append(out, map[string]any{"id": c.ID, "started_at": c.StartedAt, "duration_s": c.DurationS, "turns": c.Summary.Turns})
			if len(out) >= 40 {
				break
			}
		}
		writeJSON(w, http.StatusOK, map[string]any{"calls": out})
		return nil
	}))
	mux.HandleFunc("GET /api/scenarios/{name}", authed(func(w http.ResponseWriter, r *http.Request, who string) error {
		p, src, err := sc.path(r.PathValue("name"))
		if err != nil {
			return err
		}
		data, err := os.ReadFile(p)
		if err != nil {
			return fail(http.StatusNotFound, "no scenario %s", r.PathValue("name"))
		}
		var raw json.RawMessage = data
		writeJSON(w, http.StatusOK, map[string]any{"name": r.PathValue("name"), "source": src, "scenario": raw})
		return nil
	}))
	mux.HandleFunc("POST /api/scenarios/validate", authed(func(w http.ResponseWriter, r *http.Request, who string) error {
		body, err := io.ReadAll(io.LimitReader(r.Body, 256<<10))
		if err != nil || !json.Valid(body) {
			return fail(http.StatusBadRequest, "the scenario is not JSON")
		}
		status, out, err := sc.validate(r.Context(), body)
		if err != nil {
			return fail(http.StatusBadGateway, "the simulator did not answer: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = w.Write(out)
		return nil
	}))
	mux.HandleFunc("PUT /api/scenarios/{name}", authed(func(w http.ResponseWriter, r *http.Request, who string) error {
		name := r.PathValue("name")
		p, src, err := sc.path(name)
		if err != nil {
			return err
		}
		if src == "shipped" {
			return fail(http.StatusForbidden, "%s is a shipped scenario; save it under another name", name)
		}
		body, err := io.ReadAll(io.LimitReader(r.Body, 256<<10))
		if err != nil || !json.Valid(body) {
			return fail(http.StatusBadRequest, "the scenario is not JSON")
		}
		// The simulator's word first; a scenario it would refuse is not saved.
		status, out, err := sc.validate(r.Context(), stripPrivate(body))
		if err != nil {
			return fail(http.StatusBadGateway, "the simulator did not answer: %v", err)
		}
		if status != http.StatusOK {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(status)
			_, _ = w.Write(out)
			return nil
		}
		var pretty bytes.Buffer
		_ = json.Indent(&pretty, stripPrivate(body), "", "  ")
		pretty.WriteByte('\n')
		if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
			return err
		}
		if err := os.WriteFile(p, pretty.Bytes(), 0o600); err != nil {
			return err
		}
		writeJSON(w, http.StatusOK, map[string]any{"saved": name, "path": p, "by": who})
		return nil
	}))
	mux.HandleFunc("POST /api/scenarios/{name}/prepare", authed(func(w http.ResponseWriter, r *http.Request, who string) error {
		name := r.PathValue("name")
		p, _, err := sc.path(name)
		if err != nil {
			return err
		}
		data, err := os.ReadFile(p)
		if err != nil {
			return fail(http.StatusNotFound, "no scenario %s", name)
		}
		key := name + "-" + time.Now().Format("20060102-150405")
		status, out, err := sc.simPost(r.Context(), "/sim/v1/runs", stripPrivate(data), map[string]string{"Idempotency-Key": key})
		if err != nil {
			return fail(http.StatusBadGateway, "the simulator did not answer: %v", err)
		}
		if status == http.StatusOK && sc.LastRun != "" {
			var run struct {
				RunID string `json:"run_id"`
			}
			if json.Unmarshal(out, &run) == nil && run.RunID != "" {
				_ = os.MkdirAll(filepath.Dir(sc.LastRun), 0o700)
				_ = os.WriteFile(sc.LastRun, []byte(run.RunID+"\n"), 0o600)
			}
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = w.Write(out)
		return nil
	}))
	mux.HandleFunc("POST /api/scenarios/draft", authed(func(w http.ResponseWriter, r *http.Request, who string) error {
		var in struct {
			Call  string `json:"call"`
			Turns int    `json:"turns"`
		}
		if err := readJSON(r, &in); err != nil || in.Call == "" {
			return fail(http.StatusBadRequest, "say which call: {\"call\": \"out-…\", \"turns\": 40}")
		}
		ctx, cancel := context.WithTimeout(r.Context(), 100*time.Second)
		defer cancel()
		draft, err := sc.draftFromCall(ctx, in.Call, in.Turns)
		if err != nil {
			return err
		}
		writeJSON(w, http.StatusOK, map[string]any{"scenario": draft})
		return nil
	}))
}

// stripPrivate removes the desk's own notes on a scenario (keys beginning
// with an underscore) before it goes to the simulator, which is strict.
func stripPrivate(body []byte) []byte {
	var m map[string]json.RawMessage
	if json.Unmarshal(body, &m) != nil {
		return body
	}
	changed := false
	for k := range m {
		if strings.HasPrefix(k, "_") {
			delete(m, k)
			changed = true
		}
	}
	if !changed {
		return body
	}
	out, _ := json.Marshal(m)
	return out
}
