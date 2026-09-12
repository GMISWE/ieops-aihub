package mcp_test

import "testing"

// aihub#504: embedded_len is the per-item embedding-truncation warning — the
// server sends it only on items whose stored vector embeds a strict prefix of
// their content (internal/domain/memory.go, finalizeEmbeddedLen). These two
// probes pin what each recall mode does with it, driven through the real tool
// against a fake aihub, same harness as recall_projection_test.go.
//
// The full-mode direction is already implied by the delete-list gates
// (TestRecallResultCarriesEveryMemoryField reflects over domain.Memory and
// covers every field the day it is added), but embedded_len is the field whose
// SILENT loss this wi exists to end — the memory it warns about is precisely
// the one the caller cannot diagnose otherwise — so its arrival gets a named
// probe a card can cite rather than a reflection walk a reader has to trust.

func TestRecallFullModeForwardsEmbeddedLen(t *testing.T) {
	payload := fullRecallResponse(t)
	payload["items"].([]any)[0].(map[string]any)["embedded_len"] = float64(6000)

	item := firstRecallItem(t, recallAgainst(t, payload, nil))
	got, present := item["embedded_len"]
	if !present || got != float64(6000) {
		t.Errorf("full mode must hand the model the embedded_len the server sent "+
			"(got %#v, present=%v) — this is the only signal that an item's semantic "+
			"ranking saw a prefix of its content, and dropping it re-creates the "+
			"silent truncation aihub#504 removed", got, present)
	}
}

func TestBriefRecallItemDropsEmbeddedLen(t *testing.T) {
	payload := fullRecallResponse(t)
	payload["items"].([]any)[0].(map[string]any)["embedded_len"] = float64(6000)

	item := firstRecallItem(t, recallAgainst(t, payload, map[string]any{"fields": "brief"}))
	if got, present := item["embedded_len"]; present {
		t.Errorf("brief mode forwarded embedded_len (%#v). Brief is a keep-list by "+
			"declaration (recall_slim.go boundary 3): it is lossy on purpose, and its "+
			"escape hatch is the id — adding fields to it is a decision for briefFields, "+
			"not a drive-by", got)
	}
	if _, present := item["id"]; !present {
		t.Error("brief item lost its id — the escape hatch that makes brief's lossiness a contract rather than a defect")
	}
}
