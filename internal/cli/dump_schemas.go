package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"sort"

	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/GMISWE/ieops-aihub/internal/mcp"
)

// RunDumpMCPSchemas enumerates all registered pf_ tools and writes the
// contract JSON to w (default: os.Stdout).
//
// Output format (keys sorted, no timestamp — byte-identical across runs):
//
//	{
//	  "generated_from": "<git-sha>",
//	  "tools": {
//	    "<tool_name>": {
//	      "description": "...",
//	      "params": {
//	        "<param>": {
//	          "type": "...",
//	          "required": bool,
//	          "enum": [...]   // only present when schema carries enum
//	        }
//	      }
//	    }
//	  }
//	}
func RunDumpMCPSchemas(ctx context.Context, gitSHA string, w io.Writer) error {
	if w == nil {
		w = os.Stdout
	}

	tools, err := enumerateMCPTools(ctx)
	if err != nil {
		return fmt.Errorf("enumerate tools: %w", err)
	}

	out, err := buildContractJSON(gitSHA, tools)
	if err != nil {
		return fmt.Errorf("build contract: %w", err)
	}
	_, err = w.Write(out)
	return err
}

// enumerateMCPTools creates a Server with nil dependencies (registration never
// calls handlers, so nil client/config is safe) and uses an in-memory transport
// pair to call tools/list and return the full tool slice.
func enumerateMCPTools(ctx context.Context) ([]*sdkmcp.Tool, error) {
	// nil client and config: tool registration only calls s.mcp.AddTool(...)
	// which doesn't invoke the handler, so this is safe.
	server := mcp.New(nil, nil)

	cTransport, sTransport := sdkmcp.NewInMemoryTransports()

	// Start server session in background goroutine.
	serverCtx, serverCancel := context.WithCancel(ctx)
	defer serverCancel()

	serverErrCh := make(chan error, 1)
	go func() {
		session, err := server.Connect(serverCtx, sTransport)
		if err != nil {
			serverErrCh <- err
			return
		}
		serverErrCh <- session.Wait()
	}()

	// Connect client.
	client := sdkmcp.NewClient(&sdkmcp.Implementation{
		Name:    "dump-mcp-schemas",
		Version: "1.0.0",
	}, nil)

	clientSession, err := client.Connect(ctx, cTransport, nil)
	if err != nil {
		return nil, fmt.Errorf("client connect: %w", err)
	}
	defer func() { _ = clientSession.Close() }()

	// List all tools (paginated).
	var allTools []*sdkmcp.Tool
	for tool, iterErr := range clientSession.Tools(ctx, nil) {
		if iterErr != nil {
			return nil, fmt.Errorf("tools iteration: %w", iterErr)
		}
		allTools = append(allTools, tool)
	}

	return allTools, nil
}

// contractSchema is the top-level output structure.
type contractSchema struct {
	GeneratedFrom string                   `json:"generated_from"`
	Tools         map[string]contractTool  `json:"tools"`
}

// contractTool is one tool entry in the contract.
type contractTool struct {
	Description string                    `json:"description"`
	Params      map[string]contractParam  `json:"params"`
}

// contractParam is one parameter entry.
type contractParam struct {
	Type     string   `json:"type"`
	Required bool     `json:"required"`
	Enum     []string `json:"enum,omitempty"`
}

// buildContractJSON converts the raw tool list into the contract JSON bytes.
// Keys are sorted at every level to ensure byte-identical output across runs.
func buildContractJSON(gitSHA string, tools []*sdkmcp.Tool) ([]byte, error) {
	toolsMap := make(map[string]contractTool, len(tools))

	for _, t := range tools {
		params, err := extractParams(t.InputSchema)
		if err != nil {
			return nil, fmt.Errorf("tool %q: extract params: %w", t.Name, err)
		}
		toolsMap[t.Name] = contractTool{
			Description: t.Description,
			Params:      params,
		}
	}

	contract := contractSchema{
		GeneratedFrom: gitSHA,
		Tools:         toolsMap,
	}

	return marshalDeterministic(contract)
}

// extractParams parses a JSON Schema object (as received from the MCP client –
// already unmarshalled into map[string]any by the SDK) and returns the params
// map for the contract.
func extractParams(rawSchema any) (map[string]contractParam, error) {
	params := map[string]contractParam{}

	if rawSchema == nil {
		return params, nil
	}

	// The SDK delivers InputSchema as map[string]any on the client side.
	schemaMap, ok := rawSchema.(map[string]any)
	if !ok {
		// Try round-tripping through JSON (e.g. json.RawMessage held as []byte).
		b, err := json.Marshal(rawSchema)
		if err != nil {
			return params, nil
		}
		if err := json.Unmarshal(b, &schemaMap); err != nil {
			return params, nil
		}
	}

	propsAny, ok := schemaMap["properties"]
	if !ok {
		return params, nil
	}
	propsMap, ok := propsAny.(map[string]any)
	if !ok {
		return params, nil
	}

	// Build required set.
	required := map[string]bool{}
	if reqAny, ok := schemaMap["required"]; ok {
		if reqSlice, ok := reqAny.([]any); ok {
			for _, r := range reqSlice {
				if s, ok := r.(string); ok {
					required[s] = true
				}
			}
		}
	}

	for name, defAny := range propsMap {
		def, ok := defAny.(map[string]any)
		if !ok {
			continue
		}

		typ := ""
		if v, ok := def["type"]; ok {
			if s, ok := v.(string); ok {
				typ = s
			}
		}

		cp := contractParam{
			Type:     typ,
			Required: required[name],
		}

		// Extract enum if present.
		if enumAny, ok := def["enum"]; ok {
			if enumSlice, ok := enumAny.([]any); ok {
				for _, e := range enumSlice {
					if s, ok := e.(string); ok {
						cp.Enum = append(cp.Enum, s)
					}
				}
				// Sort enum for determinism.
				sort.Strings(cp.Enum)
			}
		}

		params[name] = cp
	}

	return params, nil
}

// marshalDeterministic marshals v as indented JSON with sorted keys.
// Go's encoding/json already sorts map keys, so this is straightforward.
func marshalDeterministic(v any) ([]byte, error) {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return nil, err
	}
	// Append a trailing newline for shell-friendly output.
	b = append(b, '\n')
	return b, nil
}
