package mcp_test

// aihub#495 — the memory visibility vocabulary a caller is SHOWN must be the
// vocabulary the write path accepts.
//
// WHAT WAS WRONG
// --------------
// pf_remember published `private|project|team|admin`, hand-typed, and the column
// has FIVE legal values: migration 0023 added `public` for unauthenticated
// artifact sharing (aihub#96), and aihub#434 mirrored the CHECK into Go —
// "Mirrored EXACTLY, 'public' included" — so the validator has accepted it since.
// Four of five is not a rounding error here. `public` is the one tier that
// changes WHO CAN READ the row: internal/server/router.go routes GET /share/:id
// to internal/server/routes_artifacts.go (`handleSharedArtifact`), which is
// unauthenticated and gates on `visibility == "public"`. That handler's own
// header already records the reachability — "`public` is settable by a project
// writer straight from POST /v1/memories … so it is not by itself a deliberate
// publication" — which is a fact about THIS tool, written on the read side, and
// published nowhere the caller of this tool could see it.
//
// WHY A GATE AND NOT JUST THE FIX
// -------------------------------
// The description is now derived from domain.MemoryVisibilityList() rather than
// typed, so it cannot omit a value by neglect. What it can still lose is the
// derivation: someone shortens the string back to a literal, and the tree is
// green again with a four-value ladder. That is the aihub#434 gap re-opening by
// the cheapest available edit, and it is the whole reason aihub#474 anchored the
// goal cap on a constant instead of on a number. So this arm asserts the SET,
// both directions — a value the column accepts and hop 1 hides fails, and a value
// hop 1 offers and the column refuses fails too.
//
// ⚠️ SCOPE — two other tools publish this same column and are NOT checked here.
// pf_save_artifact's `visibility` carries the same four-value literal, and
// pf_update_memory's names no values at all ("New visibility (omit to keep
// current)"). Both are real, both are somebody else's file scope in this batch,
// and adding them to the map below is the whole change needed when that lands.
// They are named rather than silently skipped because an arm that quietly
// measures less than its title claims is the failure rhsThirdStateTools' floor
// exists to catch.
//
// No database needed:
//
//	go test ./internal/mcp/ -run TestPublishedMemoryVisibility -v

import (
	"encoding/json"
	"sort"
	"strings"
	"testing"

	"github.com/GMISWE/ieops-aihub/internal/domain"
)

// visibilityVocabTools maps each tool whose `visibility` description must publish
// the full column vocabulary to the parameter that carries it.
var visibilityVocabTools = map[string]string{
	"pf_remember": "visibility",
}

func TestPublishedMemoryVisibilityVocabularyIsTheEnforcedOne(t *testing.T) {
	legal := domain.MemoryVisibilityList()
	if len(legal) < 5 {
		t.Fatalf("domain.MemoryVisibilityList() returned %v — fewer than the five values "+
			"memories_visibility_check has carried since migration 0023. Either the mirror lost a "+
			"value or the CHECK did; settle which before touching the published text.", legal)
	}

	for toolName, param := range visibilityVocabTools {
		tool := publishedTool(t, toolName)
		b, err := json.Marshal(tool.InputSchema)
		if err != nil {
			t.Fatalf("marshal %s InputSchema: %v", toolName, err)
		}
		desc, present := publishedParamDescription(t, b, param)
		if !present {
			t.Errorf("%s no longer publishes %q. If it was withdrawn on purpose, delete its entry "+
				"from visibilityVocabTools in the same change.", toolName, param)
			continue
		}

		published := pipedVocabularyIn(desc)
		if len(published) == 0 {
			t.Errorf("VISIBILITY_VOCAB_UNPUBLISHED: %s's %q description names no pipe-separated "+
				"value set at all.\n\nThe parameter is REQUIRED on this tool, so the description is "+
				"the only place a caller can learn what to send; without it every legal value is a "+
				"guess and every illegal one is a 400.\n\nLive description was:\n%s",
				toolName, param, desc)
			continue
		}

		if !equalStringSets(published, legal) {
			t.Errorf("VISIBILITY_VOCAB_DRIFT: %s publishes %v for %q, while the write path accepts "+
				"%v (domain.MemoryVisibilityList, mirroring memories_visibility_check).\n\n"+
				"A hidden legal value and an offered illegal one are both this failure. The one that "+
				"filed aihub#495 was `public`: legal since migration 0023, accepted in Go since "+
				"aihub#434, and absent from this string for as long as both.\n\n"+
				"Build the string from domain.MemoryVisibilityList() rather than retyping it — that "+
				"is what makes this arm unfailable rather than merely satisfied.\n\n"+
				"Live description was:\n%s",
				toolName, published, param, legal, desc)
		}

		// The consequence half. Publishing `public` as the fifth item of a ladder
		// would satisfy the set arm above and still tell a caller nothing about
		// what it does — and what it does is remove the auth requirement, which is
		// not something the other four tiers do to each other.
		for _, needle := range []string{"public", "/share/:id", "NO auth"} {
			if strings.Contains(desc, needle) {
				continue
			}
			t.Errorf("VISIBILITY_CONSEQUENCE_UNPUBLISHED: %s's %q description no longer contains "+
				"%q.\n\n`public` is the tier internal/server/router.go's GET /share/:id gates on, and "+
				"that route is unauthenticated. Listing the value without its consequence publishes "+
				"the affordance and withholds the reason to be careful with it.\n\n"+
				"Live description was:\n%s", toolName, param, needle, desc)
		}
	}
}

// pipedVocabularyIn extracts the pipe-separated value set from a description.
//
// It reads the longest whitespace-delimited run containing a `|` rather than
// splitting the whole string, so surrounding prose — which is where the
// consequence sentence lives — is not mistaken for vocabulary. Returns nil when
// the description names no such run, which the caller reports as its own failure
// rather than as an empty set that happens to differ.
func pipedVocabularyIn(desc string) []string {
	best := ""
	for _, field := range strings.Fields(desc) {
		trimmed := strings.Trim(field, ".,;:()`\"'")
		if strings.Contains(trimmed, "|") && len(trimmed) > len(best) {
			best = trimmed
		}
	}
	if best == "" {
		return nil
	}
	out := []string{}
	for _, v := range strings.Split(best, "|") {
		if v = strings.TrimSpace(v); v != "" {
			out = append(out, v)
		}
	}
	sort.Strings(out)
	return out
}

func equalStringSets(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	x, y := append([]string(nil), a...), append([]string(nil), b...)
	sort.Strings(x)
	sort.Strings(y)
	for i := range x {
		if x[i] != y[i] {
			return false
		}
	}
	return true
}
