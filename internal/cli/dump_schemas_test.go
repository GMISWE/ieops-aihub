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

	// Verify pf_remember has a "type" param with a non-empty enum.
	rememberTool, ok := schema.Tools["pf_remember"]
	if !ok {
		t.Fatal("pf_remember missing from schema")
	}
	typeParam, ok := rememberTool.Params["type"]
	if !ok {
		t.Fatal("pf_remember.params.type missing")
	}
	if len(typeParam.Enum) == 0 {
		t.Errorf("pf_remember.params.type enum is empty, want non-empty")
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
