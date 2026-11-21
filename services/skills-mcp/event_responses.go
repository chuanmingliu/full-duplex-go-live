package main

import (
	"encoding/json"
	"errors"
	"strings"
	"unicode/utf8"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type EventResponse struct {
	Enabled   bool     `json:"enabled"`
	Templates []string `json:"templates"`
}

func validateEventResponses(responses map[string]EventResponse) error {
	for event, response := range responses {
		if event != "addV" {
			return errors.New("supported callback event is addV")
		}
		if len(response.Templates) > 12 || (response.Enabled && len(response.Templates) == 0) {
			return errors.New("enabled event responses require 1–12 templates")
		}
		seen := map[string]bool{}
		for _, text := range response.Templates {
			if text != strings.TrimSpace(text) || text == "" || utf8.RuneCountInString(text) > 300 || strings.ContainsAny(text, "\r\n\x00") || seen[text] {
				return errors.New("event templates must be distinct single lines of 1–300 characters")
			}
			seen[text] = true
		}
	}
	return nil
}

func registerEventResponses(s *mcp.Server, c Catalog) {
	// A missing binding is explicitly disabled, including catalogs predating
	// event responses. Each stateless read observes one published revision.
	response := c.EventResponses["addV"]
	body, _ := json.Marshal(struct {
		Revision int    `json:"revision"`
		Event    string `json:"event"`
		EventResponse
	}{c.Revision, "addV", response})
	addText(s, "golive://event-responses/addV", "WeChat added response", "Spoken templates for a trusted addV callback", "application/json", string(body))
}
