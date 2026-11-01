package tools

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strings"
	"time"
)

// HangupRules say when a call is over. They are a config the owner edits
// on the lead service's panel (`hangup-rules`, versioned like the quote
// rules), so a new case — a wrong number, a complaint, a caller who went
// quiet — is a line added there and not a build. Two kinds:
//
//   - model rules are judged by the chat model: they are written into the
//     end_call tool's description, each with the farewell to say first, and
//     the model calls end_call when one applies;
//   - timed and keyword rules are judged by the engine itself: silence
//     (the caller has said nothing for N seconds), duration (the call has
//     run N seconds), keyword (the caller's words match a pattern). The
//     engine says the rule's farewell and hangs up;
//   - an assistant rule is judged on the assistant's own words: when a
//     reply it has just finished saying matches the pattern (再见, 拜拜), the
//     call ends — the model said goodbye and forgot the tool, which it did
//     twice on the 09:59 call of 10-20. No farewell is added: it was the
//     reply.
type HangupRules struct {
	Version int          `json:"version,omitempty"`
	Rules   []HangupRule `json:"rules"`
	// ModelMinTurns is how many turns the caller must have had answered
	// before any model rule may end the call; a rule's own MinTurns
	// overrides it. The 09:43 call of 10-23 was ended as "refused" on one
	// seven-character transcript 17 s in (ISS-054).
	ModelMinTurns int `json:"model_min_turns,omitempty"`
}

// HangupRule is one case. Kind is model, silence, duration, keyword or
// assistant.
type HangupRule struct {
	ID       string `json:"id"`
	Name     string `json:"name,omitempty"`
	Kind     string `json:"kind"`
	When     string `json:"when,omitempty"`     // model: the condition, in the model's language
	Seconds  int    `json:"seconds,omitempty"`  // silence, duration
	Pattern  string `json:"pattern,omitempty"`  // keyword: a regular expression on the caller's words; assistant: on the assistant's
	Farewell string `json:"farewell,omitempty"` // what to say before hanging up
	Enabled  *bool  `json:"enabled,omitempty"`  // nil is enabled
	// MinTurns, on a model rule: the caller must have had this many turns
	// answered before the rule may end the call (0: the set's model_min_turns).
	MinTurns int `json:"min_turns,omitempty"`

	re *regexp.Regexp
}

// On reports whether the rule is enabled.
func (r HangupRule) On() bool { return r.Enabled == nil || *r.Enabled }

// HangupRulesName is the config's name on the lead service.
const HangupRulesName = "hangup-rules"

// DefaultHangupRules is what a lead service starts with: the cases the
// owner's prompt already describes, written down where they can be edited.
const DefaultHangupRules = `{
  "rules": [
    {"id": "goodbye", "name": "客户道别", "kind": "model",
     "when": "客户明确表示再见、挂了、先这样、不聊了",
     "farewell": "好的，再见。"},
    {"id": "refused_twice", "name": "客户再次拒绝", "kind": "model",
     "when": "客户明确拒绝且已按规则挽留过一次，再次拒绝", "min_turns": 2,
     "farewell": "好的，那不打扰您了，再见。"},
    {"id": "wrong_number", "name": "打错电话", "kind": "model",
     "when": "对方表示不是本人、打错了、不认识这辆车",
     "farewell": "抱歉打扰了，再见。"},
    {"id": "done", "name": "流程完成", "kind": "model",
     "when": "本次通话的事项已经办完，客户没有其他问题",
     "farewell": "好的，那就先这样，再见。"},
    {"id": "said_goodbye", "name": "助手已道别", "kind": "assistant",
     "pattern": "再见|拜拜|不打扰您了"},
    {"id": "complaint", "name": "投诉或拒接", "kind": "keyword",
     "pattern": "投诉|别再打|不要再打|骚扰",
     "farewell": "好的，不再打扰您，再见。"},
    {"id": "silence", "name": "长时间无人应答", "kind": "silence", "seconds": 25,
     "farewell": "您好？听不到您的声音，我稍后再联系您，再见。"},
    {"id": "too_long", "name": "通话过长", "kind": "duration", "seconds": 900,
     "farewell": "今天先聊到这里，后续有问题随时联系我，再见。"}
  ]
}`

// ParseHangupRules reads a rules config, compiling the keyword patterns. A
// rule that cannot be compiled is dropped with an error for the log, not
// the whole set.
func ParseHangupRules(raw []byte) (HangupRules, []error) {
	var hr HangupRules
	if err := json.Unmarshal(raw, &hr); err != nil {
		return HangupRules{}, []error{err}
	}
	var errs []error
	kept := hr.Rules[:0]
	for _, r := range hr.Rules {
		r.Kind = strings.ToLower(strings.TrimSpace(r.Kind))
		switch r.Kind {
		case "model":
			if strings.TrimSpace(r.When) == "" {
				errs = append(errs, fmt.Errorf("rule %q: a model rule needs 'when'", r.ID))
				continue
			}
		case "silence", "duration":
			if r.Seconds <= 0 {
				errs = append(errs, fmt.Errorf("rule %q: %s needs 'seconds'", r.ID, r.Kind))
				continue
			}
		case "keyword", "assistant":
			re, err := regexp.Compile(r.Pattern)
			if err != nil || strings.TrimSpace(r.Pattern) == "" {
				errs = append(errs, fmt.Errorf("rule %q: bad pattern: %v", r.ID, err))
				continue
			}
			r.re = re
		default:
			errs = append(errs, fmt.Errorf("rule %q: unknown kind %q", r.ID, r.Kind))
			continue
		}
		kept = append(kept, r)
	}
	hr.Rules = kept
	return hr, errs
}

// Matches reports whether a keyword or assistant rule matches the words.
func (r HangupRule) Matches(text string) bool {
	return (r.Kind == "keyword" || r.Kind == "assistant") && r.re != nil && r.re.MatchString(text)
}

// ModelRules are the rules the model judges, enabled ones only.
func (hr HangupRules) ModelRules() []HangupRule {
	var out []HangupRule
	for _, r := range hr.Rules {
		if r.On() && r.Kind == "model" {
			out = append(out, r)
		}
	}
	return out
}

// EngineRules are the rules the engine judges, enabled ones only.
func (hr HangupRules) EngineRules() []HangupRule {
	var out []HangupRule
	for _, r := range hr.Rules {
		if r.On() && r.Kind != "model" {
			out = append(out, r)
		}
	}
	return out
}

// EndCallTool returns the end_call tool with the model rules written into
// its description: the cases, numbered, each with its farewell, so the
// model knows both when to hang up and what to say first.
func (hr HangupRules) EndCallTool() Tool {
	t := EndCallTool
	rules := hr.ModelRules()
	if len(rules) == 0 {
		return t
	}
	var b strings.Builder
	b.WriteString("挂断电话。出现下列任一情况时，先说出对应的告别语，再调用本工具（同一句回复里完成），并在 rule 参数中填写该情况的编号：")
	for i, r := range rules {
		fmt.Fprintf(&b, "\n%d. [%s] %s", i+1, r.ID, strings.TrimSpace(r.When))
		if r.Farewell != "" {
			fmt.Fprintf(&b, "，告别语：“%s”", strings.TrimSpace(r.Farewell))
		}
	}
	b.WriteString("\n其他情况不要调用：对话未结束、客户还有问题时继续对话。调用后系统会在当前这句话播放完后挂断。")
	t.Description = b.String()
	t.Parameters = json.RawMessage(`{"type":"object","properties":{"rule":{"type":"string","description":"适用情况的编号（方括号内的标识）"},"reason":{"type":"string","description":"结束原因，几个字即可"}}}`)
	return t
}

// MinTurnsFor is how many answered turns of the caller the rule named (by
// id, or by its words in reason) requires before the model may end the
// call: the rule's own MinTurns, else the set's ModelMinTurns.
func (hr HangupRules) MinTurnsFor(rule, reason string) int {
	for _, r := range hr.ModelRules() {
		if r.ID == rule || (rule == "" && reason != "" && (strings.Contains(r.When, reason) || strings.Contains(reason, r.Name))) {
			if r.MinTurns > 0 {
				return r.MinTurns
			}
			break
		}
	}
	return hr.ModelMinTurns
}

// FetchHangupRules reads the config from the lead service: GET
// <base>/api/configs/hangup-rules with the tools' bearer token. The
// service answers {"version": n, "body": {...}} or the body itself; both
// are read.
func FetchHangupRules(base, token string, timeout time.Duration) (HangupRules, []error) {
	if base == "" {
		return HangupRules{}, []error{fmt.Errorf("no base url")}
	}
	req, err := http.NewRequest(http.MethodGet, strings.TrimRight(base, "/")+"/api/configs/"+HangupRulesName, nil)
	if err != nil {
		return HangupRules{}, []error{err}
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	client := &http.Client{Timeout: timeout}
	resp, err := client.Do(req)
	if err != nil {
		return HangupRules{}, []error{err}
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return HangupRules{}, []error{err}
	}
	if resp.StatusCode != http.StatusOK {
		return HangupRules{}, []error{fmt.Errorf("HTTP %d", resp.StatusCode)}
	}
	// The lead service answers {"config": {"name","version","body",...}}.
	var env struct {
		Config struct {
			Version int             `json:"version"`
			Body    json.RawMessage `json:"body"`
		} `json:"config"`
	}
	if json.Unmarshal(raw, &env) == nil && len(env.Config.Body) > 0 {
		hr, errs := ParseHangupRules(env.Config.Body)
		hr.Version = env.Config.Version
		return hr, errs
	}
	return ParseHangupRules(raw)
}
