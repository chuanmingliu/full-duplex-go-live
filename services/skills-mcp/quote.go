package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"io"
	"net/http"
	"strings"
	"time"
)

// Input mirrors the CRM's working-memory tool envelope. A change and a comparison
// both carry the base selection so re-quoting doesn't lose unchanged coverages.
type quoteInput struct {
	Call          string           `json:"call" jsonschema:"Host-provided call ID for this quotation."`
	Memory        map[string]any   `json:"memory,omitempty" jsonschema:"Current call memory including the full confirmed plan under quote."`
	MemoryVersion int              `json:"memory_version,omitempty" jsonschema:"Version of the supplied call memory."`
	Config        map[string]any   `json:"config,omitempty" jsonschema:"Changes to the current coverage selection."`
	Configs       []map[string]any `json:"configs,omitempty" jsonschema:"Alternative coverage changes for one comparison."`
}
type backend struct {
	url, token string
	client     *http.Client
}

func (b backend) quote(cfg ToolConfig) mcp.ToolHandlerFor[quoteInput, map[string]any] {
	return func(ctx context.Context, _ *mcp.CallToolRequest, in quoteInput) (*mcp.CallToolResult, map[string]any, error) {
		if strings.TrimSpace(in.Call) == "" || len(in.Call) > 256 || in.MemoryVersion < 0 || len(in.Configs) > 16 {
			return nil, nil, errors.New("provide a call ID, a nonnegative memory version and at most 16 alternatives")
		}
		data, err := json.Marshal(map[string]any{"tool": "quote_total", "call": in.Call, "memory": in.Memory, "memory_version": in.MemoryVersion, "arguments": map[string]any{"config": in.Config, "configs": in.Configs}})
		if err != nil {
			return nil, nil, errors.New("invalid quotation arguments")
		}
		ctx, cancel := context.WithTimeout(ctx, time.Duration(cfg.TimeoutSeconds)*time.Second)
		defer cancel()
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, b.url, bytes.NewReader(data))
		if err != nil {
			return nil, nil, errors.New("invalid CRM endpoint")
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", "Bearer "+b.token)
		res, err := b.client.Do(req)
		if err != nil {
			return nil, nil, errors.New("CRM quotation unavailable or timed out")
		}
		defer res.Body.Close()
		if res.StatusCode != 200 {
			return nil, nil, fmt.Errorf("CRM quotation failed (HTTP %d)", res.StatusCode)
		}
		data, err = io.ReadAll(io.LimitReader(res.Body, (1<<20)+1))
		var out map[string]any
		if err != nil || len(data) > 1<<20 || json.Unmarshal(data, &out) != nil || (out["status"] != "need" && out["status"] != "quote") {
			return nil, nil, errors.New("CRM returned an unrecognized quotation result")
		}
		return nil, out, nil
	}
}
