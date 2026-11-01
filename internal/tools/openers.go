package tools

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
	"unicode/utf8"
)

// Openers keyed by what the caller asked.
//
// The engine opens every answer at the pause with a short phrase from the
// phrase cache (duplex.opener), before the chat model's first clause is in.
// A fixed 好的，/嗯， carries no meaning; ConvFill (arXiv 2511.07397) showed
// that a filler grounded in the caller's words reads as responsiveness rather
// than as a tic. A talker model cannot beat the cache on latency — it needs a
// generation and a synthesis — so the middle road is this config: scenarios,
// each a few keywords and a few phrases that fit them (a question about the
// price → "价格的话，"; a tier → "这个保额，"), chosen at the early final by
// substring match, said from the cache at 0 ms like any opener, and the model
// told to continue from it. The owner edits the scenarios on the lead
// service's panel; saving one synthesizes its phrases into golive's cache at
// once, so the first caller to hit it hears it at the pause (the owner, 11:27
// on 10-27).

// OpenersName is the lead service's config.
const OpenersName = "openers"

// OpenerScenario is one kind of caller turn and the openers that fit it.
type OpenerScenario struct {
	ID string `json:"id"`
	// Name is for the panel.
	Name string `json:"name,omitempty"`
	// Keywords: the turn matches when any of them is a substring of the
	// caller's words. Matched in the order the scenarios are listed; the
	// first that matches wins.
	Keywords []string `json:"keywords"`
	// Kind limits the scenario to turns that read as a "question", as a
	// "statement", or "" / "any" for both.
	Kind string `json:"kind,omitempty"`
	// Phrases are the openers, each a short clause ending in a comma.
	Phrases []string `json:"phrases"`
	// Enabled false keeps a scenario without using it.
	Enabled *bool `json:"enabled,omitempty"`
}

func (s OpenerScenario) enabled() bool { return s.Enabled == nil || *s.Enabled }

// Openers is the config.
type Openers struct {
	Version   int              `json:"version,omitempty"`
	Scenarios []OpenerScenario `json:"scenarios"`
}

// OpenerMaxRunes bounds a phrase (the comma included): an opener is said at
// the pause and the answer's own first word waits for its end, so every
// character is latency — four characters render at the 540 ms cap (the
// 12:03 simulated call of 10-27: content 920 on every scenario turn), a
// bare 嗯， at ~440.
const OpenerMaxRunes = 5

// DefaultOpeners is seeded on a lead service that has none: the scenarios
// of a motor-insurance call, in the order they are tried.
const DefaultOpeners = `{
  "scenarios": [
    {"id": "doubt", "name": "顾虑/比较", "keywords": ["别家", "其他公司", "比较", "再看看", "考虑", "不划算", "太贵", "便宜", "没必要", "不想", "犹豫"], "phrases": ["您的顾虑，", "明白，"]},
    {"id": "price", "name": "问价格", "keywords": ["多少钱", "价格", "保费", "费用", "多少", "贵", "优惠", "折"], "phrases": ["价格嘛，", "保费呢，", "费用上，"]},
    {"id": "tier", "name": "问保额/档位", "keywords": ["保额", "三者", "三百万", "二百万", "一百万", "五百万", "万的", "档"], "phrases": ["保额嘛，", "这个档，"]},
    {"id": "coverage", "name": "问险种", "keywords": ["车损", "座位", "驾乘", "划痕", "玻璃", "涉水", "交强", "商业险", "险种", "保什么", "赔不赔", "赔吗"], "phrases": ["这一项，", "这个险，"]},
    {"id": "claim", "name": "问理赔/出险", "keywords": ["出险", "出过", "理赔", "赔付", "报案", "事故", "撞"], "phrases": ["理赔嘛，", "出险呢，"]},
    {"id": "process", "name": "问流程/手续", "keywords": ["怎么办", "怎么买", "手续", "流程", "资料", "多久", "什么时候", "生效", "付款", "支付"], "phrases": ["流程上，", "手续嘛，"]},
    {"id": "yes", "name": "客户同意", "kind": "statement", "keywords": ["可以", "行吧", "好的", "没问题", "就这样", "那就", "要了", "办吧"], "phrases": ["好的，", "行，"]},
    {"id": "no", "name": "客户拒绝", "kind": "statement", "keywords": ["不要", "不用", "算了", "不需要", "先不"], "phrases": ["明白，", "好，"]}
  ]
}`

// ParseOpeners reads the config and checks it.
func ParseOpeners(raw []byte) (Openers, error) {
	var o Openers
	if err := json.Unmarshal(raw, &o); err != nil {
		return o, fmt.Errorf("not JSON: %w", err)
	}
	seen := map[string]bool{}
	for i, s := range o.Scenarios {
		if strings.TrimSpace(s.ID) == "" {
			return o, fmt.Errorf("scenario %d has no id", i+1)
		}
		if seen[s.ID] {
			return o, fmt.Errorf("scenario id %q is used twice", s.ID)
		}
		seen[s.ID] = true
		switch s.Kind {
		case "", "any", "question", "statement":
		default:
			return o, fmt.Errorf("scenario %q: kind must be question, statement or any", s.ID)
		}
		var kws []string
		for _, k := range s.Keywords {
			if k = strings.TrimSpace(k); k != "" {
				kws = append(kws, k)
			}
		}
		if len(kws) == 0 {
			return o, fmt.Errorf("scenario %q has no keywords", s.ID)
		}
		var phs []string
		for _, p := range s.Phrases {
			p = strings.TrimSpace(p)
			if p == "" {
				continue
			}
			if utf8.RuneCountInString(p) > OpenerMaxRunes {
				return o, fmt.Errorf("scenario %q: the opener %q is over %d characters — it is said before the answer and the answer waits for it", s.ID, p, OpenerMaxRunes)
			}
			phs = append(phs, p)
		}
		if len(phs) == 0 {
			return o, fmt.Errorf("scenario %q has no phrases", s.ID)
		}
		o.Scenarios[i].Keywords, o.Scenarios[i].Phrases = kws, phs
	}
	return o, nil
}

// Match finds the first enabled scenario whose kind fits the turn and one of
// whose keywords is in the caller's words.
func (o Openers) Match(transcript string, question bool) *OpenerScenario {
	t := strings.TrimSpace(transcript)
	if t == "" {
		return nil
	}
	for i := range o.Scenarios {
		s := &o.Scenarios[i]
		if !s.enabled() {
			continue
		}
		switch s.Kind {
		case "question":
			if !question {
				continue
			}
		case "statement":
			if question {
				continue
			}
		}
		for _, k := range s.Keywords {
			if strings.Contains(t, k) {
				return s
			}
		}
	}
	return nil
}

// Phrases is every opener of every scenario, once each, in order: what the
// cache must hold.
func (o Openers) Phrases() []string {
	seen := map[string]bool{}
	var out []string
	for _, s := range o.Scenarios {
		for _, p := range s.Phrases {
			if !seen[p] {
				seen[p] = true
				out = append(out, p)
			}
		}
	}
	return out
}

// FetchOpeners reads the config from the lead service.
func FetchOpeners(base, token string, timeout time.Duration) (Openers, error) {
	if base == "" {
		return Openers{}, fmt.Errorf("no base url")
	}
	req, err := http.NewRequest(http.MethodGet, strings.TrimRight(base, "/")+"/api/configs/"+OpenersName, nil)
	if err != nil {
		return Openers{}, err
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := (&http.Client{Timeout: timeout}).Do(req)
	if err != nil {
		return Openers{}, err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return Openers{}, err
	}
	if resp.StatusCode != http.StatusOK {
		return Openers{}, fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	var env struct {
		Config struct {
			Version int             `json:"version"`
			Body    json.RawMessage `json:"body"`
		} `json:"config"`
	}
	if err := json.Unmarshal(raw, &env); err != nil || len(env.Config.Body) == 0 {
		return Openers{}, fmt.Errorf("the config is not readable")
	}
	o, err := ParseOpeners(env.Config.Body)
	if err != nil {
		return Openers{}, err
	}
	o.Version = env.Config.Version
	return o, nil
}
