package mcp_test

// aihub#495 — the "no length cap" a caller is TOLD about must be the absence of a
// cap the server actually has.
//
// WHAT WAS UNBOUND
// ----------------
// aihub#486 appended `jsonObjectPropNote` to six parameter descriptions, closing
// with "Size is never the reason for that 400: this field has no length cap".
// Nothing held that sentence against anything. The one assertion that looks like
// it does — internal/domain's TestSizeNoteMatchesTheCapThatActuallyExists —
// checks `jsonObjectParamSizeNote`, the sentence the DOMAIN ERROR carries, and
// never reads the schema text at all. So hop 1 and hop 4 made the same claim from
// two independent strings, and the failure the note itself warns about (`payload`
// IS capped, so the unconditional wording would be false there) was one copy-paste
// away from shipping green on the published side.
//
// WHY THIS GATE READS THE DOMAIN TABLE RATHER THAN RESTATING THE CAPS
// ------------------------------------------------------------------
// A gate that hard-coded "attrs, attrs_patch and structured_payload are uncapped"
// would be a THIRD hand-typed copy of the fact, and the first one to go stale
// silently — exactly the shape aihub#474 removed from the goal cap and aihub#434
// from the visibility vocabulary. `domain.JSONObjectParamSizeNote` is the
// table that domain-side test already checks against real behaviour: a 200 KB
// object accepted for the three uncapped fields, a 70 KB `payload` refused. Two
// links, both of them checked, and neither of them a sentence someone typed twice.
//
// WHAT IT CAN AND CANNOT SEE
// --------------------------
// It is a content check over published text. It cannot tell a description that
// explains the guard from one that merely contains the words, and it does not
// try. What it makes impossible is the three ways this claim can quietly become
// false: publishing "no cap" for a field that grows one, publishing it for the
// field that already has one, and deleting it from a field that still has none.
//
// No database needed:
//
//	go test ./internal/mcp/ -run TestPublishedNoLengthCap -v

import (
	"context"
	"encoding/json"
	"sort"
	"strings"
	"testing"

	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/GMISWE/ieops-aihub/internal/domain"
	"github.com/GMISWE/ieops-aihub/internal/mcp"
)

// noLengthCapClaim is the closing sentence of internal/mcp's jsonObjectPropNote,
// quoted here as the thing being checked rather than imported: the gate is about
// what a caller READS, so it must match the published bytes even if the constant
// is renamed or spelled out inline at a new site.
const noLengthCapClaim = "this field has no length cap"

// uncappedNote is the substring domain's own table uses to mean "no cap". It is
// the same needle TestSizeNoteMatchesTheCapThatActuallyExists checks the
// behaviour against, so the two arms agree on what the table is saying.
const uncappedNote = "no length cap"

// jsonbClaimSiteFloor is the number of published parameters carrying the claim
// when this gate was written (aihub#495, 2026-09-09): `attrs` on
// pf_create_work_item, pf_batch_create_work_items (nested under `items`),
// pf_update_work_item and pf_remember, plus `attrs_patch` on
// pf_update_work_item and `structured_payload` on pf_save_artifact.
//
// A floor, not an equality: publishing the note on a seventh uncapped jsonb
// parameter is the right thing to do and must not need this number edited. What
// it stops is the walk finding nothing — every arm below quantifies over what the
// walk returns, so a broken walk passes them all by asserting nothing.
const jsonbClaimSiteFloor = 6

// publishedParam is one (tool, parameter, description) triple as the live SDK
// session publishes it.
type publishedParam struct {
	tool  string
	param string
	desc  string
}

func (p publishedParam) String() string { return p.tool + "." + p.param }

// TestPublishedNoLengthCapClaimIsBoundToTheDomainTable is the aihub#495 arm.
func TestPublishedNoLengthCapClaimIsBoundToTheDomainTable(t *testing.T) {
	params := allPublishedParamDescriptions(t)
	if len(params) < 100 {
		t.Fatalf("the parameter walk found only %d published parameters across the whole "+
			"registry, which is far below the surface this repo publishes — the walk is broken "+
			"and every arm below would be vacuously true", len(params))
	}

	claimed := map[string][]publishedParam{}
	for _, p := range params {
		if strings.Contains(p.desc, noLengthCapClaim) {
			claimed[p.param] = append(claimed[p.param], p)
		}
	}

	sites := 0
	for _, ps := range claimed {
		sites += len(ps)
	}
	if sites < jsonbClaimSiteFloor {
		t.Errorf("only %d published parameter description(s) carry %q, against a floor of %d.\n\n"+
			"aihub#486 put it on six. If a parameter was withdrawn on purpose, lower "+
			"jsonbClaimSiteFloor in the same change and say which; if it was not, a caller has "+
			"just lost the sentence that tells them size is not what their 400 was about.",
			sites, noLengthCapClaim, jsonbClaimSiteFloor)
	}

	// Arm 1 — a published "no cap" must be a cap the server does not have.
	for _, param := range sortedClaimedParamNames(claimed) {
		note := domain.JSONObjectParamSizeNote(param)
		switch {
		case note == "":
			t.Errorf("SIZE_CLAIM_UNGUARDED: %v publish %q for %q, but "+
				"domain.JSONObjectParamSizeNote(%q) is empty — the domain does not guard that "+
				"field at all.\n\nThe published sentence is a promise about a 400 the caller will "+
				"never get from this parameter, and there is nothing on the server side keeping "+
				"the promise true. Guard the field (internal/domain, validateJSONObjectParam) or "+
				"drop the note from the description.",
				claimed[param], noLengthCapClaim, param, param)
		case !strings.Contains(note, uncappedNote):
			t.Errorf("SIZE_CLAIM_CONTRADICTED: %v publish %q for %q, while the domain table says "+
				"%q.\n\nThis is the aihub#465 defect on the published side: `payload`'s note exists "+
				"precisely because the unconditional wording is FALSE for a capped field, and a "+
				"caller who reads \"size is never the reason\" and sends a 2 MB value gets a "+
				"refusal the description told them could not happen.",
				claimed[param], noLengthCapClaim, param, note)
		}
	}

	// Arm 2 — a field the domain says IS capped must not publish the claim, and a
	// field it says is NOT capped must publish it somewhere.
	//
	// The second half is the direction arm 1 structurally cannot see: arm 1 walks
	// the descriptions that make the claim, so deleting a claim removes it from
	// arm 1's view instead of failing it. That is how aihub#397's finding was lost
	// once already — a repair undone by a shortening that left no red test behind.
	for _, field := range domain.GuardedJSONObjectParams() {
		note := domain.JSONObjectParamSizeNote(field)
		if strings.Contains(note, uncappedNote) {
			if len(claimed[field]) == 0 {
				t.Errorf("SIZE_CLAIM_WITHDRAWN: the domain table says %q (%q), and NO published "+
					"parameter description says so.\n\naihub#486's whole subject is that the guard "+
					"shipped enforced and unpublished; hop 1 is the only thing a tool caller ever "+
					"sees, so a fact stated only in internal/domain is a fact the caller learns by "+
					"being refused. If the parameter is no longer published at all, remove its entry "+
					"from jsonObjectParamSizeNote in the same change.", field, note)
			}
			continue
		}
		// The capped field. It must not carry the uncapped sentence anywhere.
		for _, p := range params {
			if p.param != field || !strings.Contains(p.desc, noLengthCapClaim) {
				continue
			}
			t.Errorf("SIZE_CLAIM_ON_A_CAPPED_FIELD: %v publishes %q, but the domain table says %q.\n\n"+
				"This is the exact mistake internal/domain's own header records: the first draft of "+
				"the aihub#465 fix copied attrs_patch's unconditional wording onto all four fields, "+
				"which is false for a capped one. Publishing a constraint the server does not keep "+
				"and denying one it does are the same defect.\n\nLive description was:\n%s",
				p, noLengthCapClaim, note, p.desc)
		}
	}
}

// sortedClaimedParamNames keeps the failure output stable across runs; Go map iteration
// order is randomised, and a gate whose message reorders itself between runs is
// one a reader cannot diff.
func sortedClaimedParamNames(m map[string][]publishedParam) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// allPublishedParamDescriptions returns every (tool, parameter, description)
// triple in the live registry, including parameters nested inside an array's
// entry schema.
//
// The recursion is not optional: pf_batch_create_work_items publishes its whole
// per-item field set under `items`, so a top-level-only walk reports that tool as
// having two parameters and says nothing about the six that matter here. Same
// reason publishedGoalDescriptions recurses.
func allPublishedParamDescriptions(t *testing.T) []publishedParam {
	t.Helper()
	out := []publishedParam{}
	for _, tool := range publishedToolList(t) {
		b, err := json.Marshal(tool.InputSchema)
		if err != nil {
			t.Fatalf("marshal %s InputSchema: %v", tool.Name, err)
		}
		var decoded any
		if err := json.Unmarshal(b, &decoded); err != nil {
			t.Fatalf("re-decode %s InputSchema: %v", tool.Name, err)
		}
		collectParamDescriptions(tool.Name, decoded, &out)
	}
	return out
}

func collectParamDescriptions(tool string, node any, out *[]publishedParam) {
	switch n := node.(type) {
	case map[string]any:
		if props, ok := n["properties"].(map[string]any); ok {
			for name, raw := range props {
				if p, ok := raw.(map[string]any); ok {
					if desc, ok := p["description"].(string); ok {
						*out = append(*out, publishedParam{tool: tool, param: name, desc: desc})
					}
				}
			}
		}
		for _, v := range n {
			collectParamDescriptions(tool, v, out)
		}
	case []any:
		for _, v := range n {
			collectParamDescriptions(tool, v, out)
		}
	}
}

// publishedToolList is publishedTool's plural, over one in-memory session.
//
// Deliberately NOT newContractGate: that harness builds an isolated workspace
// with a real git clone and a seeded claim so the coding tools have a worktree to
// act on, none of which a read of the published schema text needs. And
// deliberately not a loop over publishedTool, which stands up a fresh server,
// transport and session per name — 45 of them to read one registry.
func publishedToolList(t *testing.T) []*sdkmcp.Tool {
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

	client := sdkmcp.NewClient(&sdkmcp.Implementation{Name: "jsonb-size-claim-test", Version: "1.0.0"}, nil)
	clientSession, err := client.Connect(ctx, cTransport, nil)
	if err != nil {
		t.Fatalf("client connect: %v", err)
	}
	t.Cleanup(func() { _ = clientSession.Close() })

	var out []*sdkmcp.Tool
	for tool, iterErr := range clientSession.Tools(ctx, nil) {
		if iterErr != nil {
			t.Fatalf("tools iteration: %v", iterErr)
		}
		out = append(out, tool)
	}
	if len(out) == 0 {
		t.Fatal("the live registry published no tools at all")
	}
	return out
}
