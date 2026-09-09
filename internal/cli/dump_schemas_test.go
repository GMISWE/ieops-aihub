package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"testing"
)

// TestDumpMCPSchemas_Deterministic verifies that two consecutive runs produce
// byte-identical output (no timestamps, stable map ordering).
func TestDumpMCPSchemas_Deterministic(t *testing.T) {
	ctx := context.Background()

	var buf1, buf2 bytes.Buffer
	if err := RunDumpMCPSchemas(ctx, "testsha", &buf1); err != nil {
		t.Fatalf("first run: %v", err)
	}
	if err := RunDumpMCPSchemas(ctx, "testsha", &buf2); err != nil {
		t.Fatalf("second run: %v", err)
	}

	if !bytes.Equal(buf1.Bytes(), buf2.Bytes()) {
		t.Errorf("output is not deterministic: run1 len=%d run2 len=%d", buf1.Len(), buf2.Len())
	}
}

// TestDumpMCPSchemas_Completeness verifies the output contains key known tools
// and that at least one tool with an enum param has a non-empty enum array.
func TestDumpMCPSchemas_Completeness(t *testing.T) {
	ctx := context.Background()
	var buf bytes.Buffer
	if err := RunDumpMCPSchemas(ctx, "testsha", &buf); err != nil {
		t.Fatalf("RunDumpMCPSchemas: %v", err)
	}

	var schema struct {
		GeneratedFrom string `json:"generated_from"`
		Tools         map[string]struct {
			Description string `json:"description"`
			Params      map[string]struct {
				Type     string   `json:"type"`
				Required bool     `json:"required"`
				Enum     []string `json:"enum"`
			} `json:"params"`
		} `json:"tools"`
	}
	if err := json.Unmarshal(buf.Bytes(), &schema); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	// Check generated_from is set.
	if schema.GeneratedFrom != "testsha" {
		t.Errorf("generated_from = %q, want %q", schema.GeneratedFrom, "testsha")
	}

	// Check required known tools are present.
	requiredTools := []string{
		"pf_recall",
		"pf_claim_work_item",
		"pf_save_artifact",
		"pf_remember",
		"pf_emit_event",
		"pf_update_step",
		"pf_get_work_item",
	}
	for _, name := range requiredTools {
		if _, ok := schema.Tools[name]; !ok {
			t.Errorf("tool %q is missing from schema", name)
		}
	}

	// Verify the dump carries enum values at all.
	//
	// The specimen used to be pf_remember.type and aihub#445 retired it: that
	// enum listed 13 curated names while the server enforces a PREFIX, so it was
	// withdrawn rather than corrected, and pf_remember.type now publishes no enum
	// at all. What the assertion is FOR — proving the dump does not silently drop
	// `enum` — is unchanged, so it only needs a different specimen.
	//
	// pf_create_user.role, not pf_save_artifact.type, and the difference is the
	// point of aihub#445. role is a closed set the server actually refuses to go
	// outside (aihub#463: domain.UserGlobalRoleList, validated by handleCreateUser
	// with a 400, mirroring the users.role CHECK). pf_save_artifact.type was at
	// the time published as a 6-value enum that NOTHING enforced — no client-side
	// check, and internal/domain/memory.go (Remember) accepts any methodology.*
	// name — so it was the same published-but-unenforced shape aihub#445
	// withdrew, and pinning a test to it would have entrenched it. aihub#499 then
	// withdrew that one too; the arm below is what this paragraph was waiting
	// for, and it asserts the ABSENCE of the enum, so the specimen still cannot
	// move back here.
	userTool, ok := schema.Tools["pf_create_user"]
	if !ok {
		t.Fatal("pf_create_user missing from schema")
	}
	roleParam, ok := userTool.Params["role"]
	if !ok {
		t.Fatal("pf_create_user.params.role missing")
	}
	if len(roleParam.Enum) == 0 {
		t.Errorf("pf_create_user.params.role enum is empty, want non-empty")
	}

	// pf_remember.type must NOT carry one, for the same aihub#445 reason. Without
	// this arm the swap above would be satisfied by a dump that had quietly kept
	// publishing the withdrawn list.
	rememberTool, ok := schema.Tools["pf_remember"]
	if !ok {
		t.Fatal("pf_remember missing from schema")
	}
	rememberType, ok := rememberTool.Params["type"]
	if !ok {
		t.Fatal("pf_remember.params.type missing")
	}
	if len(rememberType.Enum) != 0 {
		t.Errorf("pf_remember.params.type publishes enum %v; the accepted set is a prefix rule, "+
			"not a closed list, so no enum can state it (aihub#445)", rememberType.Enum)
	}

	// pf_save_artifact.type must not carry one either (aihub#499). The paragraph
	// above named it as the parameter aihub#445 left behind and declined to
	// assert on it, because pinning a published-but-unenforced enum would
	// entrench it. aihub#499 withdrew it — measured live 2026-09-09, 3 of the
	// 1,185 methodology.* rows in production are off those six — and enforced
	// domain.MethodologyTypePrefix instead, so the assertion the paragraph was
	// waiting for is now the opposite one and belongs here.
	artifactTool, ok := schema.Tools["pf_save_artifact"]
	if !ok {
		t.Fatal("pf_save_artifact missing from schema")
	}
	artifactType, ok := artifactTool.Params["type"]
	if !ok {
		t.Fatal("pf_save_artifact.params.type missing")
	}
	if len(artifactType.Enum) != 0 {
		t.Errorf("pf_save_artifact.params.type publishes enum %v; what the tool enforces is the "+
			"methodology. PREFIX (validatePfSaveArtifactArgs), and the six names are suggestions, "+
			"so no enum can state the accepted set (aihub#499)", artifactType.Enum)
	}

	// Verify that pf_claim_work_item requires work_item_id.
	claimTool, ok := schema.Tools["pf_claim_work_item"]
	if !ok {
		t.Fatal("pf_claim_work_item missing from schema")
	}
	wiIDParam, ok := claimTool.Params["work_item_id"]
	if !ok {
		t.Fatal("pf_claim_work_item.params.work_item_id missing")
	}
	if !wiIDParam.Required {
		t.Errorf("pf_claim_work_item.params.work_item_id required = false, want true")
	}
}

// TestDumpMCPSchemas_ValidJSON verifies the output is valid JSON.
func TestDumpMCPSchemas_ValidJSON(t *testing.T) {
	ctx := context.Background()
	var buf bytes.Buffer
	if err := RunDumpMCPSchemas(ctx, "abc42", &buf); err != nil {
		t.Fatalf("RunDumpMCPSchemas: %v", err)
	}
	var v any
	if err := json.Unmarshal(buf.Bytes(), &v); err != nil {
		t.Errorf("output is not valid JSON: %v", err)
	}
}
