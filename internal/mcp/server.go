// Package mcp implements the polyforge MCP server and every pf_ tool it
// publishes.
//
// The tool count is deliberately not written here. It used to be, and it was
// wrong by eighteen: nothing makes a number in a comment go red when a tool is
// added. `polyforge dump-mcp-schemas` emits the authoritative list.
package mcp

import (
	"context"
	"fmt"
	"os"

	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/GMISWE/ieops-aihub/internal/config"
	"github.com/GMISWE/ieops-aihub/pkg/client"
)

// MinAihubVersion is the minimum aihub server version required.
const MinAihubVersion = "1.0.0"

// Server wraps the official MCP Go SDK server with polyforge tools.
type Server struct {
	mcp    *sdkmcp.Server
	client *client.Client
	cfg    *config.Config
}

// New creates a new polyforge MCP server with all tools registered.
func New(cfg *config.Config, aihubClient *client.Client) *Server {
	s := &Server{
		mcp: sdkmcp.NewServer(&sdkmcp.Implementation{
			Name:    "polyforge",
			Version: "1.0.0",
		}, nil),
		client: aihubClient,
		cfg:    cfg,
	}
	s.registerAll()
	return s
}

// Serve starts the MCP server on stdio and blocks until ctx is cancelled.
func (s *Server) Serve(ctx context.Context) error {
	// Startup: version check (non-fatal warning only)
	if err := s.checkAihubVersion(ctx); err != nil {
		fmt.Fprintf(os.Stderr, "WARNING: aihub version check failed: %v\n", err)
	}

	// Startup: scan for in-progress steps from prior sessions
	s.startupScan()

	transport := &sdkmcp.StdioTransport{}
	session, err := s.mcp.Connect(ctx, transport, nil)
	if err != nil {
		return fmt.Errorf("mcp connect: %w", err)
	}
	return session.Wait()
}

func (s *Server) checkAihubVersion(ctx context.Context) error {
	var resp struct {
		Version string `json:"version"`
	}
	if err := s.client.Health(ctx, &resp); err != nil {
		return err
	}
	// Full semver comparison is a follow-up; just log the version for now.
	fmt.Fprintf(os.Stderr, "aihub version: %s (min required: %s)\n", resp.Version, MinAihubVersion)
	return nil
}

func (s *Server) startupScan() {
	states, err := config.FindStateFiles()
	if err != nil {
		fmt.Fprintf(os.Stderr, "WARNING: state scan failed: %v\n", err)
		return
	}
	for _, sf := range states {
		if sf.AttemptID != "" && sf.Claimed {
			fmt.Fprintf(os.Stderr, "⚠️  polyforge: wi %s has an active attempt %s from a prior session — use /pf3-resume to continue\n",
				sf.WIID, sf.AttemptID)
		}
	}
}

// Connect connects the underlying SDK server to the given transport and returns
// the server session. This is used by the dump-mcp-schemas CLI command to
// enumerate tools via an in-memory transport without a real aihub connection.
func (s *Server) Connect(ctx context.Context, transport sdkmcp.Transport) (*sdkmcp.ServerSession, error) {
	return s.mcp.Connect(ctx, transport, nil)
}

// addTool is the ONE registration path, and the only place a tool may be added.
//
// It exists so aihub#389's unknown-parameter disclosure is computed once against
// each tool's own published schema instead of 50 times in 50 handlers — see
// unknown_params.go for the defect and for why phase 1 reports rather than
// rejects. Everything else about registration is unchanged: the tool and the
// handler are passed straight through to the SDK.
//
// 🔴 DO NOT CALL s.mcp.AddTool DIRECTLY. A tool registered around this wrapper
// silently loses the disclosure while every other tool keeps it, which is the
// worst of the three possible states — the contract would hold for 50 tools and
// not for the 51st, and nothing about the response would say which. Two things
// make that fail loudly rather than quietly: TestEveryToolIsRegisteredThroughAddTool
// scans this package for the bare call, and TestEveryRegisteredToolDisclosesUnknownParams
// drives every tool the server actually publishes and asserts the disclosure
// comes back, so a bypass is caught behaviourally even if the scan is fooled.
//
// The published names are computed at registration, not per call: the schema is
// immutable after AddTool, and doing it per call would re-parse the same JSON on
// every request.
//
// A schema this cannot parse is a programming error in a schema literal, not a
// caller's problem. It is logged and the tool is still registered with an empty
// published set — which discloses every argument as unknown, loudly and on the
// first call, rather than silently disabling the check for that one tool.
//
// Every tool registered here also has a CONTRACT CARD under docs/mcp-cards/
// (indexed by docs/mcp-tools.md) recording what each of its parameters and
// response fields does, hop by hop. Editing a Tool literal in this package's
// tools_*.go — its description, a parameter description, or the schema itself —
// falsifies that card, and contract_cards_gate_test.go goes red on the always-on
// Unit tests step (K3 on either hash, K9 on a cell still quoting the old text)
// rather than letting the stale card ship. Read the card against the new text
// BEFORE regenerating its machine block — regenerating first is green with stale
// prose. docs/mcp-cards/README.md has the recipe.
func (s *Server) addTool(t *sdkmcp.Tool, h sdkmcp.ToolHandler) {
	published, err := publishedParamNames(t.InputSchema)
	if err != nil {
		fmt.Fprintf(os.Stderr, "WARNING: polyforge: cannot read %s's InputSchema (%v);"+
			" every argument to it will be reported as unpublished\n", t.Name, err)
	}
	name := t.Name
	s.mcp.AddTool(t, func(ctx context.Context, req *sdkmcp.CallToolRequest) (*sdkmcp.CallToolResult, error) {
		// Computed BEFORE the handler runs, because a handler is free to mutate
		// the arguments map it decodes (several default fields into it), and the
		// diff must describe what the CALLER sent.
		unknown := unknownParamNames(req.Params.Arguments, published)
		res, herr := h(ctx, req)
		discloseUnknownParams(name, unknown, res)
		return res, herr
	})
}

// registerAll registers all pf_ tools.
//
// Invariant: registration must work with nil dependencies (s.client/s.cfg are
// only touched inside handler closures). cmd/polyforge's dump-mcp-schemas and
// the CI publish-schemas job rely on this — do NOT read deps or register
// conditionally here, or the exported schema will silently drift from the
// running server's toolset.
func (s *Server) registerAll() {
	s.registerLifecycleTools()
	s.registerEventTools()
	s.registerMemoryTools()
	s.registerConflictTools()
	s.registerStepTools()
	s.registerDependencyTools()
	s.registerCodingTools()
	s.registerProjectTools()
	s.registerUserTools()
}

// jsonResult marshals v to JSON and returns a text content result.
func jsonResult(v any) (*sdkmcp.CallToolResult, error) {
	b, err := marshalJSON(v)
	if err != nil {
		return nil, err
	}
	return &sdkmcp.CallToolResult{
		Content: []sdkmcp.Content{&sdkmcp.TextContent{Text: string(b)}},
	}, nil
}

// errResult returns an error result (IsError = true).
func errResult(err error) (*sdkmcp.CallToolResult, error) {
	return &sdkmcp.CallToolResult{
		IsError: true,
		Content: []sdkmcp.Content{&sdkmcp.TextContent{Text: err.Error()}},
	}, nil
}
