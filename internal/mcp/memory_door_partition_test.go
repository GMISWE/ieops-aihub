package mcp

// aihub#543 probe wave 2, lane L5 — `docs/mcp-cards/pf_save_artifact.md`'s
// "Its mirror is `validatePfRememberArgs`, which refuses that same prefix, and
// between them the two tools partition the four prefixes":
//
//	-> TestTheTwoMemoryDoorsPartitionEveryTypePrefix
//
// ─── Why the two existing arms are not this claim ──────────────────────────
//
// TestSaveArtifactTypeIsEnforced drives one validator over a hand-written list
// of types, and TestValidatePfRememberArgs drives the other over three
// methodology names. Both are one-sided: each says what ITS door refuses.
// "Partition" is a claim about the PAIR — every legal memory-type prefix has
// exactly one door, and no prefix has two or none — and it is quantified over
// domain.MemoryTypePrefixes rather than over a list written here, because a
// FIFTH prefix arriving with no door is the way this becomes false and a
// hand-written list is exactly what would not notice.
//
// 🔴 The failure that shape prevents is not hypothetical one layer down:
// aihub#499 found that `pf_save_artifact(type="fact.note")` with a live claim
// put a non-artifact through the artifact door, because the server BRANCHES on
// the prefix and refuses nothing. A per-door test could not see it; only the
// pair can.
//
// No database:
//
//	go test ./internal/mcp/ -run TestTheTwoMemoryDoors -count=1 -v

import (
	"strings"
	"testing"

	"github.com/GMISWE/ieops-aihub/internal/domain"
)

// TestTheTwoMemoryDoorsPartitionEveryTypePrefix holds the pair of hop-2
// validators to a partition of domain.MemoryTypePrefixes.
//
// Three properties, and no two of them fail together:
//
//	exhaustive  every prefix is accepted by at least one door — a prefix with
//	            none is a memory type the toolset publishes and no tool can write
//	disjoint    no prefix is accepted by both — that is the aihub#499 defect,
//	            a type going through the wrong door under a live claim
//	bounded     a type with NO legal prefix is refused by both, so "partition"
//	            describes the prefixes and is not a way of saying "anything goes"
//
// MUTANTS (applied to this tree; the verdict is what ran):
//
//	M32 enforcement: delete the methodology arm from validatePfRememberArgs
//	                                            RED  the disjoint arm names
//	                                                 methodology.
//	M33 enforcement: delete the prefix arm from validatePfSaveArtifactArgs
//	                                            RED  the disjoint arm names all
//	                                                 three other prefixes, and the
//	                                                 bounded arm
//	M34 enforcement: add a fifth entry to domain.MemoryTypePrefixes
//	                                            RED  the exhaustive arm — which is
//	                                                 the direction a hand-written
//	                                                 list of prefixes could not see
//	M35 publication: delete this arm's citation from both card sentences that
//	    carry it                                RED  K12 — the partition sentence
//	                                                 names no other arm, so it
//	                                                 lands back in the debt column
//	                                                 and the pf_save_artifact
//	                                                 ledger row stops matching
func TestTheTwoMemoryDoorsPartitionEveryTypePrefix(t *testing.T) {
	// FLOOR. Both loops below are over this list, so an empty one makes the
	// partition hold vacuously; and the card's sentence counts it, so a change to
	// the count is a change to the card.
	if len(domain.MemoryTypePrefixes) != 4 {
		t.Fatalf("domain.MemoryTypePrefixes carries %d prefix(es) (%v), and the card says the two "+
			"tools partition FOUR. Rewrite the sentence in the same diff that moves the list.",
			len(domain.MemoryTypePrefixes), domain.MemoryTypePrefixes)
	}

	// The two doors, called with everything each one requires apart from `type`,
	// so a refusal below is about the type and never about a missing field.
	artifactDoor := func(memType string) error {
		return validatePfSaveArtifactArgs(map[string]any{
			"type": memType, "work_item_id": "wi_probe543", "content": "body",
		})
	}
	rememberDoor := func(memType string) error {
		return validatePfRememberArgs(map[string]any{
			"project": "aihub", "type": memType, "content": "body", "visibility": "project",
		})
	}

	for _, prefix := range domain.MemoryTypePrefixes {
		// A concrete type under the prefix, and deliberately NOT one of the six
		// suggested methodology kinds or the published memory types: the claim is
		// about the PREFIX, and a name that also appears on a suggestion list
		// could be accepted by something keyed on the name instead.
		memType := prefix + "probe543"
		t.Run(memType, func(t *testing.T) {
			artifactErr := artifactDoor(memType)
			rememberErr := rememberDoor(memType)

			switch {
			case artifactErr == nil && rememberErr == nil:
				t.Errorf("%q is accepted by BOTH doors. That is the aihub#499 defect: the server "+
					"only branches on the prefix, so whichever tool is called decides which "+
					"credential rule applies to the same stored row", memType)
			case artifactErr != nil && rememberErr != nil:
				t.Errorf("%q is refused by BOTH doors (%v / %v), so a prefix the toolset publishes "+
					"as a legal memory type cannot be written by any tool", memType,
					artifactErr, rememberErr)
			}

			// Which door it is, named rather than left to the pair above: a
			// partition that swapped the two would satisfy every assertion so far.
			if prefix == domain.MethodologyTypePrefix {
				if artifactErr != nil {
					t.Errorf("%q was refused by pf_save_artifact (%v) — the methodology prefix is "+
						"this tool's whole reason to exist", memType, artifactErr)
				}
				if rememberErr != nil && !strings.Contains(rememberErr.Error(), "pf_save_artifact") {
					t.Errorf("pf_remember refused %q without pointing at pf_save_artifact: %v. A "+
						"partition the caller cannot see is two refusals rather than one door.",
						memType, rememberErr)
				}
				return
			}
			if rememberErr != nil {
				t.Errorf("%q was refused by pf_remember (%v), which is the door for every "+
					"non-methodology prefix", memType, rememberErr)
			}
			if artifactErr != nil && !strings.Contains(artifactErr.Error(), "pf_remember") {
				t.Errorf("pf_save_artifact refused %q without pointing at pf_remember: %v", memType, artifactErr)
			}
		})
	}

	// Bounded, and MEASURED asymmetric: without this the partition claim would be
	// satisfied by one door accepting everything.
	//
	// 🔴 The two doors do NOT both refuse a name with no legal prefix, and that is
	// worth recording rather than assuming: validatePfSaveArtifactArgs refuses it
	// at hop 2, and validatePfRememberArgs does not look at the prefix at all —
	// its only type arm is the methodology one. So "the two tools partition the
	// four prefixes" is exactly true and no more: the partition is over the
	// PREFIXES, and a type carrying none of them is refused one hop later, by
	// domain.Remember's own MemoryTypePrefixes loop, which
	// internal/domain/memory_type_check_test.go holds equal to the
	// memories_type_check CHECK.
	//
	// A nil pool is safe for that call and only for it: the prefix check is the
	// first statement in Remember and returns before anything touches the
	// database.
	for _, memType := range []string{"spec", "playbook", "methodology", "note.thing"} {
		t.Run("no legal prefix/"+memType, func(t *testing.T) {
			if err := artifactDoor(memType); err == nil {
				t.Errorf("pf_save_artifact accepted %q, which carries no legal memory-type prefix", memType)
			}
			if err := rememberDoor(memType); err != nil {
				t.Errorf("validatePfRememberArgs now refuses %q (%v). That is a tighter contract "+
					"than the card describes — it says the two doors partition the four PREFIXES, "+
					"and the out-of-prefix refusal is domain.Remember's. Widen the sentence in the "+
					"same diff.", memType, err)
			}
			_, _, err := domain.Remember(t.Context(), nil, &domain.RememberRequest{
				Project: "aihub", Type: memType, Content: "body", Visibility: "project",
			})
			if err == nil {
				t.Fatalf("domain.Remember accepted %q. Nothing then refuses a prefix-less type "+
					"before the INSERT, and memories_type_check turns a caller mistake into the "+
					"driver's constraint text — the aihub#433 shape", memType)
			}
			if !strings.Contains(err.Error(), domain.MemoryTypePrefixGloss()) {
				t.Errorf("domain.Remember refused %q without naming the four prefixes (%v), so a "+
					"caller who reached the wrong door is not told which doors exist", memType, err)
			}
		})
	}
}
