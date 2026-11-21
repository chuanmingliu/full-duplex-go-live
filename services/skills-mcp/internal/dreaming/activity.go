package dreaming

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

// Activity checks every configured voice engine. Unknown health fails closed.
type Activity struct {
	URLs   []string
	Client *http.Client
	mu     sync.Mutex
	state  ActivityState
}

type ActivityState struct {
	Status    string    `json:"status"`
	Sessions  int       `json:"sessions"`
	CheckedAt time.Time `json:"checked_at"`
	IdleSince time.Time `json:"idle_since,omitempty"`
	Error     string    `json:"error,omitempty"`
}

func LocalClient() *http.Client {
	return &http.Client{Timeout: 3 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
}

// ValidateServiceURL accepts an operator-configured origin, never a browser URL.
func ValidateServiceURL(raw string) error {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || strings.Trim(u.Path, "/") != "" || (u.Scheme != "http" && u.Scheme != "https") {
		return errors.New("invalid service origin")
	}
	return nil
}

func ReadJSON(ctx context.Context, client *http.Client, endpoint string, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return err
	}
	resp, err := client.Do(req)
	if err != nil {
		return errors.New("service unavailable")
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return errors.New("service returned an error")
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, (4<<20)+1))
	if err != nil || len(body) > 4<<20 {
		return errors.New("service response exceeds limit")
	}
	return json.Unmarshal(body, out)
}

func (a *Activity) Check(ctx context.Context) ActivityState {
	a.mu.Lock()
	defer a.mu.Unlock()
	state := ActivityState{Status: "unknown", CheckedAt: time.Now().UTC()}
	if len(a.URLs) == 0 {
		state.Error = "Configure the voice engine health origin"
	}
	for _, origin := range a.URLs {
		var health struct {
			Status   string `json:"status"`
			Sessions *int   `json:"sessions"`
		}
		if err := ReadJSON(ctx, a.Client, strings.TrimRight(origin, "/")+"/healthz", &health); err != nil || health.Status != "ok" || health.Sessions == nil || *health.Sessions < 0 {
			state.Error = "Voice engine activity unavailable"
			break
		}
		state.Sessions += *health.Sessions
	}
	if ctx.Err() != nil {
		return a.state
	}
	if state.Error == "" {
		state.Status = "busy"
		if state.Sessions == 0 {
			state.Status = "idle"
			state.IdleSince = a.state.IdleSince
			if state.IdleSince.IsZero() {
				state.IdleSince = state.CheckedAt
			}
		}
	}
	a.state = state
	return state
}
func (a *Activity) State() ActivityState {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.state
}
