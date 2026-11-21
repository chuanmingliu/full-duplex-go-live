package main

import (
	"context"

	"github.com/modelcontextprotocol/go-sdk/jsonrpc"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/thehumanworks/codemode-go/mcptool"
)

type skillSelectionParams struct {
	mcp.ParamsBase
	skillRequest
}

type skillSelectionResult struct {
	mcp.ResultBase
	skillReply
}

// The same operations serve MCP custom methods and sandbox-callable tools.
// Existing tools/list on /mcp continues to expose the business tools.
func registerSkillSelection(s *mcp.Server, index *skillIndex, registry *mcptool.Registry) {
	operations := []struct {
		name        string
		description string
		run         func(context.Context, skillRequest) (skillReply, error)
	}{
		{"search", "Search eligible skills using query or conversation context. Auto uses configured description embeddings with reported lexical fallback; search_mode selects semantic or lexical explicitly. Event and state filters apply. Returns a bounded ranked list and revision. Full instructions are loaded separately.", index.search},
		{"describe", "Read current skill manifests including priority, dependencies and conflicts. Requires revision and eligible skill URIs.", index.describe},
		{"resolve", "Select a compatible set of skills and required dependencies within max_skills and max_bytes. Requires revision. Higher priority wins; ties follow input URI order. Reports rejected candidates.", index.resolve},
		{"load", "Batch load skill manifests and exact Markdown from one revision within the selection budgets. Returns unresolved if any candidate or dependency cannot be included. Verify manifest digests before using text.", index.load},
	}
	for _, operation := range operations {
		run := operation.run
		err := mcp.AddReceivingCustomMethod(s, "skills/"+operation.name, func(ctx context.Context, _ *mcp.ServerSession, p *skillSelectionParams) (*skillSelectionResult, error) {
			if p == nil {
				p = &skillSelectionParams{}
			}
			out, err := run(ctx, p.skillRequest)
			if err != nil {
				return nil, &jsonrpc.Error{Code: jsonrpc.CodeInvalidParams, Message: err.Error()}
			}
			return &skillSelectionResult{skillReply: out}, nil
		})
		if err != nil {
			panic(err)
		}
		if registry != nil {
			mcptool.AddTool(registry, &mcp.Tool{Name: "skills_" + operation.name, Description: operation.description}, func(ctx context.Context, _ *mcp.CallToolRequest, in skillRequest) (*mcp.CallToolResult, skillReply, error) {
				out, err := run(ctx, in)
				return nil, out, err
			})
		}
	}
}
