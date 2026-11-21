package skillsmcp

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"unicode/utf8"
)

type EventResponse struct {
	Revision  int      `json:"revision"`
	Event     string   `json:"event"`
	Enabled   bool     `json:"enabled"`
	Templates []string `json:"templates"`
}

// EventResponse reads the current binding with one bounded MCP request.
// It sends no transcript, customer identity or memory to the catalog service.
func (c *Client) EventResponse(ctx context.Context, event string) (EventResponse, error) {
	var response EventResponse
	if event != "addV" {
		return response, errors.New("unsupported callback event")
	}
	ctx, cancel := c.deadline(ctx)
	defer cancel()
	uri := "golive://event-responses/" + event
	var result struct {
		Contents []struct {
			URI  string `json:"uri"`
			MIME string `json:"mimeType"`
			Text string `json:"text"`
		} `json:"contents"`
	}
	if err := c.rpc(ctx, "resources/read", map[string]any{"uri": uri}, &result); err != nil {
		return response, err
	}
	if len(result.Contents) != 1 || result.Contents[0].URI != uri || result.Contents[0].MIME != "application/json" || len(result.Contents[0].Text) > 20000 {
		return response, errors.New("invalid event response resource")
	}
	if json.Unmarshal([]byte(result.Contents[0].Text), &response) != nil || response.Event != event || response.Revision < 1 || len(response.Templates) > 12 || (response.Enabled && len(response.Templates) == 0) {
		return EventResponse{}, errors.New("invalid event response binding")
	}
	for _, text := range response.Templates {
		if text == "" || text != strings.TrimSpace(text) || utf8.RuneCountInString(text) > 300 || strings.ContainsAny(text, "\r\n\x00") {
			return EventResponse{}, errors.New("invalid event response template")
		}
	}
	return response, nil
}
