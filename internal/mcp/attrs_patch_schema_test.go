package mcp_test

// aihub#288, hop 1 of the attrs_patch contract.
//
// The server-side merge is worth nothing if the parameter cannot reach it, and
// the failure mode on this hop is silent: the MCP SDK drops arguments that the
// published InputSchema does not declare. No error, no warning — the caller
// sends attrs_patch, the handler never sees it, and the update quietly becomes
// a no-op. That is exactly how it behaves on the pre-fix build, and it is the
// same class of defect aihub#280 catalogues for pf_list_work_items.
//
// So this asserts the contract at both ends of the wire, against the schema the
// server actually PUBLISHES (tools/list over a real session) rather than
// against the source text that produces it:
//
//   - pf_update_work_item declares attrs_patch and attrs_unset, with the right
//     JSON-schema types;
//   - the names it publishes are the names domain.UpdateWorkItemRequest binds,
//     so a rename on either side fails here instead of degrading into a silent
//     drop two layers away;
//   - the `attrs` description tells callers it destroys unsent keys. A caller
//     holding the old mental model will keep calling the destructive path
//     correctly-but-unintentionally, so the description IS part of the fix.

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/GMISWE/ieops-aihub/internal/domain"
	"github.com/GMISWE/ieops-aihub/internal/mcp"
)

// publishedTool returns the tool as advertised over tools/list. nil client and
// config are safe: registration only calls AddTool and never invokes a handler
// (same technique as internal/cli's enumerateMCPTools).
func publishedTool(t *testing.T, name string) *sdkmcp.Tool {
	t.Helper()
	ctx := context.Background()

	server := mcp.New(nil, nil)
	cTransport, sTransport := sdkmcp.NewInMemoryTransports()

	serverCtx, cancel := context.WithCancel(ctx)
	t.Cleanup(cancel)
	go func() {
		session, err := server.Connect(serverCtx, sTransport)
		if err != nil {
			return
		}
		_ = session.Wait()
	}()

	client := sdkmcp.NewClient(&sdkmcp.Implementation{Name: "attrs-patch-schema-test", Version: "1.0.0"}, nil)
	clientSession, err := client.Connect(ctx, cTransport, nil)
	if err != nil {
		t.Fatalf("client connect: %v", err)
	}
	t.Cleanup(func() { _ = clientSession.Close() })

	for tool, iterErr := range clientSession.Tools(ctx, nil) {
		if iterErr != nil {
			t.Fatalf("tools iteration: %v", iterErr)
		}
		if tool.Name == name {
			return tool
		}
	}
	t.Fatalf("tool %q is not published at all", name)
	return nil
}

// schemaProps decodes the published InputSchema into its property map.
func schemaProps(t *testing.T, tool *sdkmcp.Tool) map[string]struct {
	Type        string `json:"type"`
	Description string `json:"description"`
} {
	t.Helper()
	raw, err := json.Marshal(tool.InputSchema)
	if err != nil {
		t.Fatalf("marshal InputSchema: %v", err)
	}
	var schema struct {
		Properties map[string]struct {
			Type        string `json:"type"`
			Description string `json:"description"`
		} `json:"properties"`
	}
	if err := json.Unmarshal(raw, &schema); err != nil {
		t.Fatalf("decode InputSchema: %v", err)
	}
	if len(schema.Properties) == 0 {
		t.Fatalf("published InputSchema for %q has no properties: %s", tool.Name, raw)
	}
	return schema.Properties
}

func TestUpdateWorkItemToolPublishesAttrsPatch(t *testing.T) {
	props := schemaProps(t, publishedTool(t, "pf_update_work_item"))

	for name, wantType := range map[string]string{
		"attrs":       "object",
		"attrs_patch": "object",
		"attrs_unset": "array",
	} {
		p, ok := props[name]
		if !ok {
			t.Errorf("pf_update_work_item does not publish %q — the MCP SDK drops undeclared arguments silently, so callers cannot reach it at all", name)
			continue
		}
		if p.Type != wantType {
			t.Errorf("%q is published as type %q, want %q", name, p.Type, wantType)
		}
		if p.Description == "" {
			t.Errorf("%q is published with no description", name)
		}
	}
}

// TestUpdateWorkItemToolDocumentsDestructiveAttrs guards the half of the fix
// that lives in prose. attrs keeps its REPLACE semantics deliberately, so the
// only thing standing between a caller and the aihub#284 data loss is the
// description steering them to attrs_patch.
func TestUpdateWorkItemToolDocumentsDestructiveAttrs(t *testing.T) {
	props := schemaProps(t, publishedTool(t, "pf_update_work_item"))

	attrsDesc := props["attrs"].Description
	if !strings.Contains(attrsDesc, "attrs_patch") {
		t.Errorf("the `attrs` description must point callers at attrs_patch, otherwise they keep using the destructive path by default; got: %q", attrsDesc)
	}
	if !strings.Contains(strings.ToUpper(attrsDesc), "REPLACE") || !strings.Contains(strings.ToUpper(attrsDesc), "DELETE") {
		t.Errorf("the `attrs` description must say it REPLACEs the object and DELETEs unsent keys; got: %q", attrsDesc)
	}

	patchDesc := props["attrs_patch"].Description
	// The merge is shallow and `null` stores a null rather than deleting. Both
	// differ from RFC 7396, which is what a reader is most likely to assume, so
	// both have to be stated or the description is actively misleading.
	for _, want := range []string{"null", "attrs_unset"} {
		if !strings.Contains(patchDesc, want) {
			t.Errorf("the `attrs_patch` description must mention %q so its semantics are not mistaken for RFC 7396 merge-patch; got: %q", want, patchDesc)
		}
	}
}

// TestPublishedAttrsNamesBindToDomainFields closes the loop: the names the tool
// advertises must be the names the request struct decodes. A rename on either
// side alone would leave both this package and the domain package compiling and
// their own tests passing, while the parameter silently stopped arriving —
// which is the failure this whole work item is about, one layer up.
func TestPublishedAttrsNamesBindToDomainFields(t *testing.T) {
	props := schemaProps(t, publishedTool(t, "pf_update_work_item"))

	// The body is built from the names the schema actually publishes, with a
	// value chosen from the published TYPE — not from hardcoded literals, which
	// would keep passing after a rename on the MCP side.
	body := map[string]any{}
	for name, p := range props {
		if !strings.HasPrefix(name, "attrs_") {
			continue
		}
		switch p.Type {
		case "object":
			body[name] = map[string]any{"k": "v"}
		case "array":
			body[name] = []string{"k"}
		default:
			t.Fatalf("%q is published with unexpected type %q", name, p.Type)
		}
	}
	if len(body) != 2 {
		t.Fatalf("expected exactly attrs_patch and attrs_unset to be published, got %v", body)
	}

	encoded, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("marshal body: %v", err)
	}
	var req domain.UpdateWorkItemRequest
	if err := json.Unmarshal(encoded, &req); err != nil {
		t.Fatalf("decode into UpdateWorkItemRequest: %v", err)
	}

	if req.AttrsPatch == nil {
		t.Errorf("the published `attrs_patch` name does not bind to UpdateWorkItemRequest.AttrsPatch — it would be dropped at c.Bind")
	}
	if req.AttrsUnset == nil {
		t.Errorf("the published `attrs_unset` name does not bind to UpdateWorkItemRequest.AttrsUnset — it would be dropped at c.Bind")
	}
}
