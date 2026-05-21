// Command polyforge runs the polyforge v1 MCP server (stdio JSON-RPC 2.0).
package main

import (
	"fmt"

	"github.com/GMISWE/ieops-aihub/internal/version"
)

func main() {
	fmt.Printf("polyforge MCP server %s (%s)\n", version.Version, version.GitCommit)
	// TODO(wi-4): wire up mark3labs/mcp-go + 32 pf_ tools
}
