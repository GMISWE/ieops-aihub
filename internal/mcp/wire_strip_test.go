package mcp

// aihub#586 — the strip's own unit surface: the family roster, the census the
// roster answers, and the byte-level behaviour of the projection. The wire
// behaviour per tool (a real session, a recorded request, the disclosure) is
// next door in wire_strip_family_test.go; the live /share consequence is in
// remember_strip_e2e_db_test.go.

import (
	"encoding/json"
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/GMISWE/ieops-aihub/internal/domain"
)

// TestWireStrippedToolsAreExactlyTheMemoryWriteFamily pins the roster to the
// owner's ruling (aihub#586, 2026-09-10): the three wholesale-forwarding memory
// tools, no fewer and no more.
//
// Fewer re-opens the vulnerability for the dropped tool. More is not free
// either: the strip makes an unpublished argument unreachable, so adding a tool
// here is a behaviour change for any caller relying on an unpublished name the
// way pf_remember callers relied on `tags` before aihub#425 published it —
// widening wants the census wire_strip.go describes, then an entry, then a card
// sentence, in that order.
func TestWireStrippedToolsAreExactlyTheMemoryWriteFamily(t *testing.T) {
	want := []string{"pf_remember", "pf_save_artifact", "pf_update_memory"}
	got := make([]string, 0, len(wireStrippedTools))
	for name, on := range wireStrippedTools {
		if !on {
			t.Errorf("wireStrippedTools[%q] = false — a false entry is a tool someone half-"+
				"removed; delete the key or set it true", name)
		}
		got = append(got, name)
	}
	sort.Strings(got)
	if !reflect.DeepEqual(got, want) {
		t.Errorf("wireStrippedTools = %v, want %v — the set is the aihub#586 ruling, and "+
			"either direction of drift needs the reasoning in wire_strip.go redone, not just "+
			"this list edited", got, want)
	}
}

// rememberBindableWireNames is every top-level JSON name domain.RememberRequest
// binds from a request body — the names a POST /v1/memories caller can set,
// read off the struct's own tags so a field added tomorrow is counted the day
// it is added. Untagged exported fields would bind under their Go names;
// RememberRequest has none today, and if one appears it lands in this census
// too rather than being silently exempt.
func rememberBindableWireNames() []string {
	tp := reflect.TypeOf(domain.RememberRequest{})
	var names []string
	for i := 0; i < tp.NumField(); i++ {
		f := tp.Field(i)
		if !f.IsExported() {
			continue
		}
		name := strings.Split(f.Tag.Get("json"), ",")[0]
		if name == "-" {
			continue
		}
		if name == "" {
			name = f.Name
		}
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// TestRememberUnpublishedBindableKeysAreExactlyTheCensus is the census the
// aihub#586 fix rests on: which names bind into domain.RememberRequest that
// pf_remember's schema does not publish. The strip makes every one of them
// unreachable through this tool, so the list going STALE is safe — what this
// arm guards is the list going SILENTLY LONGER: a new bound field is a new name
// the disclosure will report and the card should know about, and a new name
// that should probably not be bindable from the wire at all without a decision.
//
// On a red: either publish the new name (hop 1, with a description that prices
// it) or add it here AND to the census in wire_strip.go AND to the card's
// closed-class paragraph — the three copies are one fact, and this arm is what
// keeps the other two honest.
func TestRememberUnpublishedBindableKeysAreExactlyTheCensus(t *testing.T) {
	published, err := publishedParamNames(rememberSchema())
	if err != nil {
		t.Fatalf("read pf_remember's own schema: %v", err)
	}
	if len(published) == 0 {
		t.Fatalf("pf_remember publishes no properties — this census would then report every " +
			"bound field, which is a fact about a broken schema rather than about the class")
	}
	var unpublished []string
	for _, name := range rememberBindableWireNames() {
		if _, ok := published[name]; !ok {
			unpublished = append(unpublished, name)
		}
	}
	want := []string{"attempt_id", "claim_epoch", "rendered_html", "session_secret", "structured_payload"}
	if !reflect.DeepEqual(unpublished, want) {
		t.Errorf("bound-but-unpublished census = %v, want %v (measured 2026-09-10).\n"+
			"A NEW name here is a new member of the class aihub#586 closed: the strip already "+
			"covers it, but the disclosure will start naming it and docs/mcp-cards/pf_remember.md "+
			"enumerates the class — update the census in wire_strip.go and the card, or publish "+
			"the name. A MISSING name means a field stopped binding; retire it from both.",
			unpublished, want)
	}
}

// TestStripUnpublishedArgsKeepsPublishedValueBytes holds the "published keys
// unaffected byte-for-byte" half of the ruling at the level where it is
// decided: values travel as json.RawMessage, so nothing is re-encoded through
// float64 — the same trap mergeAdjustmentIntoObject documents — and the three
// degenerate inputs take the documented fallbacks.
func TestStripUnpublishedArgsKeepsPublishedValueBytes(t *testing.T) {
	published := map[string]struct{}{"kept_int": {}, "kept_str": {}}

	// 9007199254740993 = 2^53+1: survives as RawMessage, corrupts through float64.
	raw := json.RawMessage(`{"kept_int":9007199254740993,"kept_str":"café A","dropped":{"nested":true}}`)
	out := stripUnpublishedArgs(raw, published)
	var decoded map[string]json.RawMessage
	if err := json.Unmarshal(out, &decoded); err != nil {
		t.Fatalf("stripped output is not JSON: %v", err)
	}
	if got := string(decoded["kept_int"]); got != "9007199254740993" {
		t.Errorf("kept_int = %s, want the literal 9007199254740993 — the value went through a "+
			"float64 and lost its low bit", got)
	}
	if got := string(decoded["kept_str"]); got != `"café A"` {
		// encoding/json re-emits RawMessage bytes untouched, so the caller's own
		// spelling — non-ASCII included — is what must survive.
		t.Errorf("kept_str = %s, want the caller's own bytes %s", got, `"café A"`)
	}
	if _, present := decoded["dropped"]; present {
		t.Errorf("unpublished key survived the strip: %s", out)
	}

	// Degenerate inputs: empty, unparseable, and an empty published set.
	if got := stripUnpublishedArgs(nil, published); got != nil {
		t.Errorf("nil in, %s out — want nil unchanged", got)
	}
	broken := json.RawMessage(`{"not json`)
	if got := stripUnpublishedArgs(broken, published); string(got) != string(broken) {
		t.Errorf("unparseable input was rewritten to %s — the handler owns that failure", got)
	}
	all := stripUnpublishedArgs(raw, map[string]struct{}{})
	var none map[string]json.RawMessage
	if err := json.Unmarshal(all, &none); err != nil || len(none) != 0 {
		t.Errorf("an empty published set must strip everything (the loud-failure path for an "+
			"unreadable schema); got %s", all)
	}
}
