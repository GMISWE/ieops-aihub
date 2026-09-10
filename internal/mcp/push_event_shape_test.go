package mcp_test

// aihub#543 probe wave 1 — the `docs/mcp-cards/pf_ship.md` hop-2-3 sentence
// about what the fused tool leaves on the timeline:
//
//	"Strictly a superset: this `push` event also carries `sha`, which
//	 `pf_push`'s does not."
//
// 🔴 WHY NOTHING HELD IT. The sentence is the load-bearing half of the claim
// above it — "shipping in one call leaves the same timeline as shipping in
// three" — and it is the half that can be false while the other reads true. A
// timeline event is not a tool response: no card lists it, K10 walks responses
// and cannot see it, and the only place either payload is built is two literal
// map[string]any composites forty lines apart in
// internal/mcp/tools_coding.go's registerCodingTools. Dropping `sha` from
// pf_ship's, or adding it to pf_push's, breaks nothing that runs — a wi timeline
// is written best-effort and read by humans, so the tools would keep answering
// ok=true with the audit trail quietly changed under them.
//
// The two directions are asserted, not one: "superset" is false if pf_ship's
// event loses the key AND false if pf_push's gains it. A one-sided arm would
// call the second case a pass while the sentence it holds became wrong.
//
// The key name, the event type and the compared tool are all read out of the
// card's own sentence rather than written here, so editing the sentence reddens
// this arm as surely as editing either payload does.
//
// No database. git and a stub gh are needed and are skipped for explicitly:
//
//	GOWORK=off go test ./internal/mcp/ -run TestShipPushEvent -count=1 -v

import (
	"os"
	"regexp"
	"strings"
	"testing"
	"time"
)

// shipCardPath is the card carrying the sentence.
const shipCardPath = "../../docs/mcp-cards/pf_ship.md"

// supersetClaim is what the card's sentence promises.
type supersetClaim struct {
	// EventType is the timeline event both tools emit.
	EventType string
	// Key is the payload field pf_ship's carries and the other tool's does not.
	Key string
	// OtherTool is the tool whose event omits it.
	OtherTool string
}

// supersetClaimRe reads the three tokens out of the sentence. Anchored on the
// card's own wording so a rewrite that changed the claim cannot leave a stale
// assertion standing: no match is a loud failure, not a skip.
var supersetClaimRe = regexp.MustCompile(
	"this `([a-z_]+)` event also carries `([a-z_]+)`,\\s+which `(pf_[a-z_]+)`'s does not")

// publishedSupersetClaim parses the sentence, failing rather than guessing.
func publishedSupersetClaim(t *testing.T) supersetClaim {
	t.Helper()
	raw, err := os.ReadFile(shipCardPath)
	if err != nil {
		t.Fatalf("read %s: %v — the card is the published side of this claim", shipCardPath, err)
	}
	// Collapse the hard wrap: the sentence spans two lines in the card and the
	// tokens it names sit on either side of the break.
	flat := strings.Join(strings.Fields(string(raw)), " ")
	m := supersetClaimRe.FindStringSubmatch(flat)
	if m == nil {
		t.Fatalf("%s no longer states the push-event superset in the form this arm reads, so "+
			"there is no published claim left to check. If the sentence went, this file goes with "+
			"it; if it was reworded, the pattern here has to follow it — leaving a matching "+
			"failure in place is what keeps the two honest.", shipCardPath)
	}
	c := supersetClaim{EventType: m[1], Key: m[2], OtherTool: m[3]}
	t.Logf("card claims: the %q event from pf_ship carries %q and %s's does not",
		c.EventType, c.Key, c.OtherTool)
	return c
}

// pushEventPayload drives one tool and returns the payload of the single
// timeline event of the given type that it emitted.
//
// Exactly one: two events of the same type from one call would make "the payload"
// ambiguous, and picking the first would hide the second.
func pushEventPayload(t *testing.T, tool, eventType string, extra map[string]any) map[string]any {
	t.Helper()
	root := newResolveWorkspace(t)
	r := newResolveRepo(t, root)
	fakeGHForResolve(t, `[]`) // no PR on the branch -> push, then create one
	writeResolveCanonical(t, map[string]string{"aihub": r.wt})

	args := map[string]any{"work_item_id": resolveCanonical, "repo": "aihub"}
	for k, v := range extra {
		args[k] = v
	}

	f := newFakeAihub(t)
	out, isErr := callToolBounded(t, f, tool, args, 60*time.Second)
	if isErr {
		t.Fatalf("%s failed: %v", tool, out)
	}
	// pf_ship reports failure as a payload rather than an error result, so `ok`
	// is the real verdict for it.
	if ok, present := out["ok"].(bool); present && !ok {
		t.Fatalf("%s did not succeed (stage=%v error=%v); a call that never pushed emits no "+
			"push event and this arm would then be reading an empty set",
			tool, out["stage"], out["error"])
	}

	var found []map[string]any
	for _, c := range f.recorded() {
		if c.Path != "/v1/events" || c.Body == nil {
			continue
		}
		if c.Body["event_type"] != eventType {
			continue
		}
		payload, ok := c.Body["payload"].(map[string]any)
		if !ok {
			t.Fatalf("%s emitted a %q event whose payload is %T, not an object",
				tool, eventType, c.Body["payload"])
		}
		found = append(found, payload)
	}
	if len(found) != 1 {
		t.Fatalf("%s emitted %d %q event(s), want exactly 1. Zero means this arm compares "+
			"nothing; two means \"the payload\" is ambiguous and the first would mask the "+
			"second. Events seen: %v", tool, len(found), eventType, f.paths())
	}
	return found[0]
}

// TestShipPushEventCarriesTheShaTheStandalonePushOmits is the superset sentence,
// in both directions.
//
// Mutants, all applied to the tree and run (2026-09-10):
//
//	M1  drop "sha" from pf_ship's push event payload         RED (superset arm)
//	M2  add "sha" to pf_push's push event payload            RED (omission arm)
//	M3  the card names `commit_sha` instead                  RED (both arms)
//	M4  the card compares against `pf_wrap`, whose push
//	    event does carry the key                             RED
//	M5  drop "branch" from pf_ship's payload, so neither
//	    event contains the other                             RED (containment arm)
//	M6  green control: reword the prose around the three
//	    backticked tokens                                    GREEN
func TestShipPushEventCarriesTheShaTheStandalonePushOmits(t *testing.T) {
	claim := publishedSupersetClaim(t)

	// The floor: both tools must be the ones the claim is about, or the two
	// halves below are comparing something else. A card naming a tool this
	// process does not publish is caught by K1/K2; a card naming one that
	// emits no such event is caught by pushEventPayload's exact-one check.
	if claim.OtherTool == "pf_ship" {
		t.Fatalf("the card compares pf_ship's %q event with its own; there is no superset claim "+
			"in that", claim.EventType)
	}

	shipped := pushEventPayload(t, "pf_ship", claim.EventType, map[string]any{
		"message":  "feat: the fused push",
		"pr_title": "fused",
		"pr_body":  "body",
	})
	standalone := pushEventPayload(t, claim.OtherTool, claim.EventType, nil)

	// Direction 1: the fused tool really carries it.
	got, has := shipped[claim.Key]
	if !has {
		t.Errorf("pf_ship's %q event payload is %v, with no %q — the card says shipping in one "+
			"call leaves a strict SUPERSET of the timeline shipping in three leaves, and this key "+
			"is the whole of the difference", claim.EventType, shipped, claim.Key)
	} else if s, _ := got.(string); strings.TrimSpace(s) == "" {
		t.Errorf("pf_ship's %q event carries %q=%v, which identifies nothing. The timeline is "+
			"the only durable record of what a ship delivered; an empty sha there is the same "+
			"as no sha", claim.EventType, claim.Key, got)
	}

	// Direction 2: the standalone tool really does not. Asserted rather than
	// assumed, because a superset claim is equally false when the other side
	// catches up — and the two payloads are independent literals in one file,
	// which is the shape that drifts INTO agreement.
	if v, has := standalone[claim.Key]; has {
		t.Errorf("%s's %q event payload now carries %q=%v too, so pf_ship's is no longer a "+
			"strict superset and the card's sentence is false in the other direction. Either the "+
			"sentence goes or one of the payloads does", claim.OtherTool, claim.EventType, claim.Key, v)
	}

	// And the common ground has to actually be common, or "superset" is a claim
	// about two unrelated payloads. Every key the standalone event carries must
	// be in the fused one.
	if len(standalone) == 0 {
		t.Fatalf("%s's %q event payload is empty, so the containment check below asserts nothing",
			claim.OtherTool, claim.EventType)
	}
	for k := range standalone {
		if _, has := shipped[k]; !has {
			t.Errorf("%s's %q event carries %q and pf_ship's does not, so the two timelines "+
				"differ in BOTH directions and neither contains the other: fused=%v standalone=%v",
				claim.OtherTool, claim.EventType, k, keysOf(shipped), keysOf(standalone))
		}
	}
}
