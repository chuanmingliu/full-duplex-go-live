package main

import (
	"context"
	"encoding/json"

	"github.com/modelcontextprotocol/go-sdk/jsonrpc"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

const extension = "io.modelcontextprotocol/skills"

type skillParams struct {
	mcp.ParamsBase
	URI    string `json:"uri,omitempty"`
	Cursor string `json:"cursor,omitempty"`
}
type listResult struct {
	mcp.ResultBase
	mcp.Cacheable
	ResultType string  `json:"resultType"`
	Skills     []entry `json:"skills"`
}
type getResult struct {
	mcp.ResultBase
	mcp.Cacheable
	ResultType string `json:"resultType"`
	Skill      entry  `json:"skill"`
}

func addText(s *mcp.Server, uri, name, desc, mime, body string) {
	s.AddResource(&mcp.Resource{URI: uri, Name: name, Description: desc, MIMEType: mime}, func(context.Context, *mcp.ReadResourceRequest) (*mcp.ReadResourceResult, error) {
		return &mcp.ReadResourceResult{Contents: []*mcp.ResourceContents{{URI: uri, MIMEType: mime, Text: body}}}, nil
	})
}
func buildServer(c Catalog, b backend) *mcp.Server {
	caps := &mcp.ServerCapabilities{Resources: &mcp.ResourceCapabilities{}, Tools: &mcp.ToolCapabilities{}, Extensions: map[string]any{extension: map[string]any{}}}
	s := mcp.NewServer(&mcp.Implementation{Name: "xiaocui-skills", Version: "1.1.0"}, &mcp.ServerOptions{Capabilities: caps, Instructions: "Use outbound-turn to select skills for a business event. Discover skills with skills/list or skill://index.json and read matched resources on demand.", SetCacheable: func(_ context.Context, _ mcp.Request, h *mcp.Cacheable) { h.TTLMs = 0; h.CacheScope = "private" }})
	entries := []entry{}
	index := []map[string]string{}
	if c.Quote.Enabled {
		mcp.AddTool(s, &mcp.Tool{Name: "quote_total", Description: c.Quote.Description}, b.quote(c.Quote))
	}
	registerOutboundPrompt(s, c)
	registerEventResponses(s, c)
	for _, sk := range c.Skills {
		if !c.available(sk) {
			continue
		}
		entries = append(entries, sk.entry())
		index = append(index, map[string]string{"type": "skill-md", "name": sk.Name, "description": sk.Description, "url": sk.uri()})
		addText(s, sk.uri(), sk.Name, sk.Description, "text/markdown", sk.markdown())
	}
	data, _ := json.Marshal(map[string]any{"$schema": "https://schemas.agentskills.io/discovery/0.2.0/schema.json", "skills": index})
	addText(s, "skill://index.json", "Skill catalog", "Available quotation workflows", "application/json", string(data))
	cache := mcp.Cacheable{CacheScope: "private"}
	invalid := func(msg string) error { return &jsonrpc.Error{Code: jsonrpc.CodeInvalidParams, Message: msg} }
	err := mcp.AddReceivingCustomMethod(s, "skills/list", func(_ context.Context, _ *mcp.ServerSession, p *skillParams) (*listResult, error) {
		if p != nil && p.Cursor != "" {
			return nil, invalid("unknown cursor")
		}
		return &listResult{Cacheable: cache, ResultType: "complete", Skills: entries}, nil
	})
	if err != nil {
		panic(err)
	}
	err = mcp.AddReceivingCustomMethod(s, "skills/get", func(_ context.Context, _ *mcp.ServerSession, p *skillParams) (*getResult, error) {
		for _, e := range entries {
			if p != nil && p.URI == e.URI {
				return &getResult{Cacheable: cache, ResultType: "complete", Skill: e}, nil
			}
		}
		return nil, invalid("unknown skill URI")
	})
	if err != nil {
		panic(err)
	}
	return s
}
