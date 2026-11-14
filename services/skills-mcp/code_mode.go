package main

import (
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/google/jsonschema-go/jsonschema"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	codemode "github.com/thehumanworks/codemode-go"
	"github.com/thehumanworks/codemode-go/mcptool"
)

//go:embed code-mode/SKILL.md
var codeModeSkill string

func codeModeInstructions(selectionEnabled bool) string {
	if selectionEnabled {
		return codeModeSkill
	}
	instructions, _, _ := strings.Cut(codeModeSkill, "## Select companion skills")
	return instructions
}

const codeModeSkillURI = "skill://mcp-code-mode/runtime/SKILL.md"

const codeModeDescription = `Execute JavaScript as an async function body. Discover tools with return await search("quotation") and inspect signatures with return await describe("tools.quote_total"). Call tools with await tools.<name>({...}), then return a compact JSON result. Only returned values and captured console output reach the model. Read skill://mcp-code-mode/runtime/SKILL.md for the execution contract.
For business calls, the caller supplies call, memory and memory_version as outer execute_code arguments. These fields are bound by the host for every nested call. Supply catalog_revision from the skill metadata to reject a stale plan. Discovery and computation can omit call context.
Each execution has a fresh sandbox, a 90-second default wall-clock deadline (180 seconds maximum), 64 MiB JS heap, 16 host calls including discovery, serialized tool dispatch, and bounded output. Errors can occur after earlier calls completed. Check outcomes before retrying a business operation.`

// Shared across catalog revisions, so hot publication cannot reset admission limits.
var codeModeSlots = make(chan struct{}, 4)

func newCodeRegistry(s *mcp.Server, revision int) *mcptool.Registry {
	return mcptool.New(s, mcptool.Options{
		Name:           "execute_code",
		Namespace:      "tools",
		Mode:           mcptool.ModeOnly,
		Description:    codeModeDescription,
		TypesURI:       "codemode://tools/typescript",
		MaxTimeout:     180 * time.Second,
		MaxResultChars: 24 << 10,
		Sandbox: codemode.Options{
			Timeout:        90 * time.Second,
			MemoryLimit:    64 << 20,
			MaxCalls:       16,
			MaxConcurrency: 1,
			MaxLogChars:    4 << 10,
		},
		Params: map[string]*jsonschema.Schema{
			"call":             {Type: "string", Description: "Host-provided call ID, required for business tools."},
			"state":            {Type: "object", AdditionalProperties: &jsonschema.Schema{Type: "string"}, Description: "Host-provided confirmed state projection. Overrides state on every nested skill call."},
			"memory":           {Type: "object", Description: "Host-provided confirmed call-memory snapshot."},
			"memory_version":   {Type: "integer", Description: "Nonnegative version of the memory snapshot."},
			"catalog_revision": {Type: "integer", Description: "Expected published catalog revision."},
		},
		Prepare: func(ctx context.Context, args map[string]any) (*mcptool.RunConfig, error) {
			return prepareCodeRun(ctx, args, revision)
		},
	})
}

func prepareCodeRun(ctx context.Context, args map[string]any, revision int) (*mcptool.RunConfig, error) {
	code, _ := args["code"].(string)
	if len(code) > 32<<10 {
		return nil, errors.New("code exceeds 32768 bytes")
	}
	if expected, ok := args["catalog_revision"].(float64); ok && expected != float64(revision) {
		return nil, errors.New("catalog revision changed; discover tools again before execution")
	}
	call, _ := args["call"].(string)
	if len(call) > 256 || (call != "" && strings.TrimSpace(call) == "") {
		return nil, errors.New("call must contain 1–256 bytes")
	}
	version, _ := args["memory_version"].(float64)
	// JavaScript and the MCP adapter decode numbers as IEEE-754 doubles.
	if version < 0 || version > 9007199254740991 {
		return nil, errors.New("memory_version must be a nonnegative safe integer")
	}
	snapshot := args["memory"]
	if snapshot == nil {
		snapshot = map[string]any{}
	}
	state := args["state"]
	if state == nil {
		state = map[string]string{}
	}
	stateJSON, err := json.Marshal(state)
	if err != nil || len(stateJSON) > 8192 {
		return nil, errors.New("state exceeds 8192 bytes")
	}
	memory, err := json.Marshal(snapshot)
	if err != nil || len(memory) > 64<<10 {
		return nil, errors.New("memory exceeds 65536 bytes")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	select {
	case codeModeSlots <- struct{}{}:
	default:
		return nil, errors.New("code execution capacity reached; retry later")
	}
	return &mcptool.RunConfig{
		Context: ctx,
		Cleanup: func() { <-codeModeSlots },
		Args: func(name string, raw json.RawMessage) json.RawMessage {
			if name != "quote_total" && !strings.HasPrefix(name, "skills_") {
				return raw
			}
			var object map[string]json.RawMessage
			if json.Unmarshal(raw, &object) != nil {
				// Leave malformed input for the registered tool's schema validator.
				return raw
			}
			if object == nil {
				object = make(map[string]json.RawMessage)
			}
			if strings.HasPrefix(name, "skills_") {
				object["state"] = stateJSON
				bound, _ := json.Marshal(object)
				return bound
			}
			object["call"], _ = json.Marshal(call)
			object["memory"] = memory
			object["memory_version"], _ = json.Marshal(version)
			bound, _ := json.Marshal(object)
			return bound
		},
	}, nil
}
