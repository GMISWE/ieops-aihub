// Package mcp implements the polyforge MCP server with all 32 pf_ tools.
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

// registerAll registers all 32 pf_ tools.
func (s *Server) registerAll() {
	s.registerLifecycleTools()
	s.registerEventTools()
	s.registerMemoryTools()
	s.registerConflictTools()
	s.registerStepTools()
	s.registerReleaseTools()
	s.registerDependencyTools()
	s.registerCodingTools()
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
