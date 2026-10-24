package desk

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"sort"
	"strings"
	"sync"
	"time"
)

// Calls is the desk's view of the calls placed to the simulated customer:
// each one paired with what the two services that saw it recorded — the
// simulator's run (its report: answered, greeting, checks, failures) and the
// call journal's call (golive's turns, latencies, the recording). The
// pairing is `phone-brain.sh call sim`'s doing: it writes one line per dial,
// {"at","call","run"}, to the dials file, and that is the only place the
// two ids meet. A real phone call never appears here; the journal marks it
// as real and this page as simulated, so the two are never confused.
//
// Nothing is copied: the report is fetched from the simulator with its
// control token (read from its tokens file, server side, never sent to the
// page), the call from the journal, and the recording is played from the
// journal's own page, embedded.
type Calls struct {
	Dials     string // the dials file
	Simulator string // the simulator's control URL, e.g. http://127.0.0.1:18865
	SimTokens string // its tokens.env, for SIM_CONTROL_TOKEN
	Journal   string // the journal's URL as this process reaches it
	// JournalPort is the journal's port as the page's browser reaches it, on
	// the same host as the desk; the page embeds the journal's call view.
	JournalPort int
	Log         func(msg string, args ...any)

	mu      sync.Mutex
	reports map[string]json.RawMessage // run id → terminal report, kept
	client  *http.Client
}

// Dial is one line of the dials file.
type Dial struct {
	At   time.Time `json:"at"`
	Call string    `json:"call"`
	Run  string    `json:"run"`
}

// CallRow is what the page lists.
type CallRow struct {
	At   time.Time `json:"at"`
	Call string    `json:"call"`
	Run  string    `json:"run"`
	// From the simulator: the run's phase and, once ended, the report's
	// scenario, why it ended, what failed and what the checks said.
	Phase    string            `json:"phase,omitempty"`
	Scenario string            `json:"scenario,omitempty"`
	Ended    string            `json:"ended,omitempty"`
	Failures []string          `json:"failures,omitempty"`
	Checks   map[string]string `json:"checks,omitempty"`
	// GapsMS is the simulator's measure of the assistant: the valid
	// assistant response gaps, at its application audio boundary.
	GapsMS []int `json:"gaps_ms,omitempty"`
	// From the journal: whether it has the call, and golive's figures.
	Journal   bool    `json:"journal"`
	DurationS float64 `json:"duration_s,omitempty"`
	Turns     int     `json:"turns,omitempty"`
	P50       int     `json:"p50,omitempty"`
	P95       int     `json:"p95,omitempty"`
	Over      int     `json:"over,omitempty"`
	HasAudio  bool    `json:"has_audio"`
	Build     string  `json:"build,omitempty"`
}

func (c *Calls) httpClient() *http.Client {
	if c.client == nil {
		c.client = &http.Client{Timeout: 4 * time.Second}
	}
	return c.client
}

// dials reads the dials file, newest first.
func (c *Calls) dials(limit int) ([]Dial, error) {
	f, err := os.Open(c.Dials)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	defer f.Close()
	var out []Dial
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		var d Dial
		if json.Unmarshal(sc.Bytes(), &d) == nil && d.Call != "" && d.Run != "" {
			out = append(out, d)
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].At.After(out[j].At) })
	if limit > 0 && len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

func (c *Calls) simToken() string {
	data, err := os.ReadFile(c.SimTokens)
	if err != nil {
		return ""
	}
	for _, line := range strings.Split(string(data), "\n") {
		if v, ok := strings.CutPrefix(strings.TrimSpace(line), "SIM_CONTROL_TOKEN="); ok {
			return strings.Trim(strings.TrimSpace(v), `"'`)
		}
	}
	return ""
}

// get fetches JSON from one of the two services; a service that is down or
// does not know the id is an empty answer, not an error for the page.
func (c *Calls) get(ctx context.Context, base, path, token string, into any) (int, error) {
	if base == "" {
		return 0, nil
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(base, "/")+path, nil)
	if err != nil {
		return 0, err
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := c.httpClient().Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return resp.StatusCode, err
	}
	if resp.StatusCode != http.StatusOK {
		return resp.StatusCode, nil
	}
	if into != nil {
		return resp.StatusCode, json.Unmarshal(body, into)
	}
	return resp.StatusCode, nil
}

// report is a run's terminal report, fetched once and kept.
func (c *Calls) report(ctx context.Context, run string) (json.RawMessage, error) {
	c.mu.Lock()
	if r, ok := c.reports[run]; ok {
		c.mu.Unlock()
		return r, nil
	}
	c.mu.Unlock()
	var raw json.RawMessage
	status, err := c.get(ctx, c.Simulator, "/sim/v1/runs/"+url.PathEscape(run)+"/report", c.simToken(), &raw)
	if err != nil || status != http.StatusOK {
		return nil, err
	}
	c.mu.Lock()
	if c.reports == nil {
		c.reports = map[string]json.RawMessage{}
	}
	c.reports[run] = raw
	c.mu.Unlock()
	return raw, nil
}

var terminal = map[string]bool{"completed": true, "rejected": true, "expired": true, "failed": true, "cancelled": true, "aborted": true}

// Rows pairs each dial with what the simulator and the journal know.
func (c *Calls) Rows(ctx context.Context, limit int) ([]CallRow, error) {
	ds, err := c.dials(limit)
	if err != nil {
		return nil, err
	}
	rows := make([]CallRow, 0, len(ds))
	token := c.simToken()
	for _, d := range ds {
		row := CallRow{At: d.At, Call: d.Call, Run: d.Run}
		var snap struct {
			Phase string `json:"phase"`
		}
		if _, err := c.get(ctx, c.Simulator, "/sim/v1/runs/"+url.PathEscape(d.Run), token, &snap); err == nil {
			row.Phase = snap.Phase
		}
		if terminal[row.Phase] {
			if raw, _ := c.report(ctx, d.Run); raw != nil {
				var rep struct {
					Scenario string   `json:"scenario_id"`
					Ended    string   `json:"termination_reason"`
					Failures []string `json:"failures"`
					Checks   []struct {
						Name   string `json:"name"`
						Result string `json:"result"`
					} `json:"checks"`
					Gaps []struct {
						MS    int  `json:"value_ms"`
						Valid bool `json:"valid"`
					} `json:"assistant_response_gaps"`
				}
				if json.Unmarshal(raw, &rep) == nil {
					row.Scenario, row.Ended, row.Failures = rep.Scenario, rep.Ended, rep.Failures
					row.Checks = map[string]string{}
					for _, ch := range rep.Checks {
						row.Checks[ch.Name] = ch.Result
					}
					for _, g := range rep.Gaps {
						if g.Valid {
							row.GapsMS = append(row.GapsMS, g.MS)
						}
					}
				}
			}
		}
		var jc struct {
			Call struct {
				DurationS float64 `json:"duration_s"`
				Build     string  `json:"build"`
				Summary   struct {
					Turns int `json:"turns"`
					P50   int `json:"p50"`
					P95   int `json:"p95"`
					Over  int `json:"over"`
				} `json:"summary"`
			} `json:"call"`
			Audio struct {
				Available bool `json:"available"`
			} `json:"audio"`
		}
		if status, err := c.get(ctx, c.Journal, "/api/calls/"+url.PathEscape(d.Call), "", &jc); err == nil && status == http.StatusOK {
			row.Journal = true
			row.DurationS, row.Build = jc.Call.DurationS, jc.Call.Build
			row.Turns, row.P50, row.P95, row.Over = jc.Call.Summary.Turns, jc.Call.Summary.P50, jc.Call.Summary.P95, jc.Call.Summary.Over
			row.HasAudio = jc.Audio.Available
		}
		rows = append(rows, row)
	}
	return rows, nil
}

// Mount adds the calls API to the desk's mux, behind its tokens.
func (c *Calls) Mount(mux *http.ServeMux, authed func(func(http.ResponseWriter, *http.Request, string) error) http.HandlerFunc) {
	if c == nil || c.Dials == "" {
		return
	}
	mux.HandleFunc("GET /api/calls", authed(func(w http.ResponseWriter, r *http.Request, who string) error {
		ctx, cancel := context.WithTimeout(r.Context(), 12*time.Second)
		defer cancel()
		rows, err := c.Rows(ctx, 60)
		if err != nil {
			return fail(http.StatusInternalServerError, "reading the dials: %v", err)
		}
		if rows == nil {
			rows = []CallRow{}
		}
		writeJSON(w, http.StatusOK, map[string]any{"calls": rows, "journal_port": c.JournalPort})
		return nil
	}))
	mux.HandleFunc("GET /api/calls/{run}/report", authed(func(w http.ResponseWriter, r *http.Request, who string) error {
		raw, err := c.report(r.Context(), r.PathValue("run"))
		if err != nil {
			return fail(http.StatusBadGateway, "the simulator did not answer: %v", err)
		}
		if raw == nil {
			return fail(http.StatusNotFound, "no report for that run yet")
		}
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Cache-Control", "no-store")
		_, _ = w.Write(raw)
		return nil
	}))
}

func (c *Calls) String() string {
	return fmt.Sprintf("dials=%s simulator=%s journal=%s", c.Dials, c.Simulator, c.Journal)
}
