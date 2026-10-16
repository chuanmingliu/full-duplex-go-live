package simclient

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/chuanmingliu/golive/internal/simulator"
)

type Client struct {
	base, token string
	http        *http.Client
	// RequestTimeout may shorten the ten-second request bound.
	RequestTimeout time.Duration
}

func New(base, token string) (*Client, error) {
	u, err := Endpoint(base)
	if err != nil {
		return nil, err
	}
	if u.Path != "" && u.Path != "/" || strings.TrimSpace(token) == "" || strings.ContainsAny(token, "\r\n") {
		return nil, errors.New("invalid control configuration")
	}
	return &Client{base: strings.TrimSuffix(u.String(), "/"), token: token, http: &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return errors.New("redirect rejected") }}, RequestTimeout: 10 * time.Second}, nil
}
func (c *Client) request(ctx context.Context, method, path, key string, body any, out any) error {
	timeout := c.RequestTimeout
	if timeout <= 0 || timeout > 10*time.Second {
		timeout = 10 * time.Second
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	var raw []byte
	var err error
	if body != nil {
		raw, err = json.Marshal(body)
		if err != nil {
			return errors.New("cannot encode request")
		}
	}
	req, err := http.NewRequestWithContext(ctx, method, c.base+"/sim/v1/"+path, bytes.NewReader(raw))
	if err != nil {
		return errors.New("cannot create control request")
	}
	// A failed reservation is ambiguous: never let Transport replay its body.
	req.GetBody = nil
	req.Header.Set("Authorization", "Bearer "+c.token)
	req.Header.Set("Content-Type", "application/json")
	if key != "" {
		req.Header.Set("Idempotency-Key", key)
	}
	res, err := c.http.Do(req)
	if err != nil {
		return errors.New("control request failed")
	}
	defer res.Body.Close()
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		return fmt.Errorf("control request returned HTTP %d", res.StatusCode)
	}
	raw, err = io.ReadAll(io.LimitReader(res.Body, MaxArtifactBytes+1))
	if err != nil || len(raw) > MaxArtifactBytes {
		return errors.New("invalid control response size")
	}
	if out != nil {
		if err = StrictJSON(raw, out); err != nil {
			return errors.New("invalid control response")
		}
	}
	return nil
}
func (c *Client) Prepare(ctx context.Context, key string, s simulator.Scenario) (simulator.Snapshot, error) {
	var snap simulator.Snapshot
	err := c.request(ctx, "POST", "runs", key, s, &snap)
	return snap, err
}
func (c *Client) Get(ctx context.Context, id string) (simulator.Snapshot, error) {
	var snap simulator.Snapshot
	if !SafeID(id) {
		return snap, errors.New("invalid run identity")
	}
	err := c.request(ctx, "GET", "runs/"+id, "", nil, &snap)
	return snap, err
}
func (c *Client) Report(ctx context.Context, id string) (simulator.Report, error) {
	var report simulator.Report
	if !SafeID(id) {
		return report, errors.New("invalid run identity")
	}
	err := c.request(ctx, "GET", "runs/"+id+"/report", "", nil, &report)
	return report, err
}
func (c *Client) Stop(ctx context.Context, id string) error {
	if !SafeID(id) {
		return errors.New("invalid run identity")
	}
	return c.request(ctx, "POST", "runs/"+id+"/stop", "", nil, nil)
}
