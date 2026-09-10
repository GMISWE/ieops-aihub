package server

// aihub#543 probe wave 2, lane L5 — `docs/mcp-cards/pf_save_artifact.md`'s
// Policy ⚠️, the consequence of `aihub#499` keeping the type set OPEN:
//
//	"an off-list `methodology.*` type IS stored, but it is not pre-rendered and
//	 does not appear in the work item's artifact-links section —
//	 `internal/domain/memory.go` (`defaultRenderTypes`) and
//	 `internal/server/ui_handlers_wi.go` (`fetchArtifactLinks`) both name the six
//	 literally"
//	    -> TestOffListMethodologyTypeIsNeitherPreRenderedNorLinked
//
// and the reader half of the same card's hop 4:
//
//	"a stringified payload was stored under that key as a string, answered 200,
//	 and then read back by `internal/server/routes_artifacts.go`
//	 (`reviewPayload`), which expects an object and finds nothing"
//	    -> TestStringifiedStructuredPayloadRendersNoReviewChrome
//
// ─── Why this is not already held ──────────────────────────────────────────
//
// TestSaveArtifactTypeDescriptionStatesWhatIsEnforced requires the published
// `type` description to CARRY the disclosure ("NOT pre-rendered"), which is the
// publication half. Nothing held the behaviour it discloses. The two sites are
// in different packages and both write the six names out as literals, so the
// day one of them grows a prefix match — the obvious "fix" for the three
// measured methodology.playbook rows — the description keeps warning about a
// limitation that has gone, and a reader budgets work for it.
//
// 🔴 Driven, not scanned. fetchArtifactLinks' type list is asserted from the
// RecallRequest the handler really built, through the recallFn seam and a nil
// pool, because a source scan for six string literals would go green on a
// handler that built the list and then ignored it.
//
// No database:
//
//	go test ./internal/server/ -run TestOffListMethodologyType -count=1 -v

import (
	"context"
	"encoding/json"
	"sort"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/GMISWE/ieops-aihub/internal/domain"
)

// offListMethodologyType is the live value aihub#499 measured — three rows on
// ieops wi_TYllxcv1, operator handover documents with no slot among the six —
// and it is the reason the names were withdrawn instead of enforced. A change
// that makes it render or link has reversed §6.2 T2-6's leniency ruling; a
// change that makes it refused has reversed it the other way.
const offListMethodologyType = "methodology.playbook"

// TestOffListMethodologyTypeIsNeitherPreRenderedNorLinked holds both halves of
// the card's ⚠️ against the two sites it names.
//
// MUTANTS (applied to this tree; the verdict is what ran):
//
//	M36 enforcement: add offListMethodologyType to domain.defaultRenderTypes
//	                                            RED  the render arm
//	M37 enforcement: replace fetchArtifactLinks' six literals with
//	    domain.MethodologyTypePrefix + "*"      RED  the artifact-links arm
//	M38 enforcement: drop one of the six from fetchArtifactLinks' list
//	                                            RED  the artifact-links arm from
//	                                                 the OTHER direction — the six
//	                                                 that DO link have to keep
//	                                                 linking, or "off-list" stops
//	                                                 naming a difference
//	M39 publication: delete this arm's citation from the card sentence, which names
//	    no other                                RED  K12 — the sentence lands back
//	                                                 in the debt column and the
//	                                                 pf_save_artifact ledger row
//	                                                 stops matching
func TestOffListMethodologyTypeIsNeitherPreRenderedNorLinked(t *testing.T) {
	// Anti-vacuity: every assertion below is relative to this list, so an empty
	// one would make "the six" a claim about nothing.
	if len(domain.MethodologyTypeEnum) == 0 {
		t.Fatal("domain.MethodologyTypeEnum is empty; \"names the six literally\" is then a claim " +
			"about an empty set and both halves below hold vacuously")
	}
	if domain.IsRenderType(offListMethodologyType) {
		t.Fatalf("%q is a configured render type in this process, so it is not an off-list name "+
			"and the arms below are measuring the wrong thing", offListMethodologyType)
	}

	t.Run("the six are pre-rendered and an off-list name is not", func(t *testing.T) {
		for _, ty := range domain.MethodologyTypeEnum {
			if !domain.IsRenderType(ty) {
				t.Errorf("%q is one of the six suggested kinds and is NOT a render type — the "+
					"published `type` description promises the six render, so dropping one makes "+
					"that promise false rather than making this arm pass", ty)
			}
		}
		// The negative direction is the card's sentence. Already asserted above as
		// a precondition; restated here so a reader of the failure sees which of
		// the two claims moved.
		if domain.IsRenderType(offListMethodologyType) {
			t.Errorf("%q is now pre-rendered on save. The published `type` description discloses "+
				"that an off-list name is NOT, and that disclosure is now false — correct it in "+
				"the same change", offListMethodologyType)
		}
	})

	t.Run("the artifact-links section asks for the six by name", func(t *testing.T) {
		var asked []string
		calls := 0
		withFakeRecall(t, func(_ context.Context, _ *pgxpool.Pool, req *domain.RecallRequest) (*domain.RecallResponse, error) {
			calls++
			asked = append([]string(nil), req.Types...)
			return &domain.RecallResponse{}, nil
		})

		wi := &domain.WorkItem{ID: "wi_probe543", Project: "p1"}
		if links := fetchArtifactLinks(context.Background(), nil, wiTestUser(), wi); len(links) != 0 {
			t.Errorf("the stub returned no items and fetchArtifactLinks produced %v", links)
		}

		// FLOOR: the seam was really taken. Without it an empty `asked` would
		// satisfy the "off-list name absent" assertion by never having run.
		if calls != 1 {
			t.Fatalf("fetchArtifactLinks made %d recall call(s), want exactly 1 — the type list "+
				"below comes from that request, so a walk that made none asserts nothing", calls)
		}

		got := append([]string(nil), asked...)
		want := append([]string(nil), domain.MethodologyTypeEnum...)
		sort.Strings(got)
		sort.Strings(want)
		if len(got) != len(want) {
			t.Fatalf("the artifact-links recall asked for types %v, want exactly the six suggested "+
				"kinds %v. A wider list means an off-list methodology.* name now appears in the "+
				"section and the card's ⚠️ is false; a narrower one means a kind the description "+
				"promises has stopped linking.", got, want)
		}
		for i := range want {
			if got[i] != want[i] {
				t.Fatalf("the artifact-links recall asked for types %v, want exactly %v", got, want)
			}
		}
		for _, ty := range asked {
			if ty == offListMethodologyType {
				t.Errorf("the artifact-links recall names %q, so an off-list type DOES appear in "+
					"the section", offListMethodologyType)
			}
		}
	})
}

// TestStringifiedStructuredPayloadRendersNoReviewChrome is the CONSEQUENCE half
// of the card's `structured_payload` bullet, and the reason a 400 was the right
// answer rather than a coercion.
//
// The guard that now refuses a stringified payload is held by
// internal/domain/json_object_params_test.go. What no arm held is what the
// stringified value DID once stored: `reviewPayload` unmarshals the value under
// `attrs.structured_payload` into a struct, a JSON string is not that struct, and
// buildReviewHTML then returns "" — the plain-document path, with no verdict, no
// findings and no error anywhere. That silence is why the card calls it "finds
// nothing", and it is the shape a reader would otherwise have to take on trust.
//
// 🔴 Both directions in one function. The empty string alone is what a broken
// harness also produces, so the object arm is what makes the string arm mean
// something.
//
// MUTANTS (applied to this tree; the verdict is what ran):
//
//	M44 enforcement: decode a JSON-string payload before unmarshalling it into
//	    reviewPayload — i.e. coerce, the repair aihub#465 declined
//	                                            RED  the stringified arm
//	M45 publication: delete this arm's citation clause from the card sentence
//	                                            RED  K12
//
//	── recorded GREEN, and this arm's stated ceiling ──
//	M45a enforcement: set hasPayload = true INSIDE the err == nil block
//	                                            GREEN unreachable: unmarshalling a
//	                                                  JSON string into the struct
//	                                                  errors, so that block never
//	                                                  runs for this fixture
//	M45b enforcement: set hasPayload = true whenever attrs carries the key at all
//	                                            GREEN the builder emits nothing for
//	                                                  a payload with no usable
//	                                                  field, so both "nothing" paths
//	                                                  converge on one empty string.
//	                                                  ⚠️ An arm on the returned HTML
//	                                                  therefore cannot tell "the
//	                                                  struct did not bind" from "it
//	                                                  bound and was empty"; what it
//	                                                  does hold is the observable
//	                                                  the card claims — no chrome —
//	                                                  and M44 is what makes that
//	                                                  observable load-bearing
func TestStringifiedStructuredPayloadRendersNoReviewChrome(t *testing.T) {
	// The object shape a real quick review stores, and the same bytes wrapped as
	// a JSON string — which is exactly what a hand-escaping caller sent 19 times
	// before aihub#465.
	const object = `{"result":"WARN","level":"quick","issues":[{"severity":"warning","text":"x"}]}`
	stringified, err := json.Marshal(object)
	if err != nil {
		t.Fatalf("marshal the stringified fixture: %v", err)
	}

	mem := func(payload string) *domain.Memory {
		return &domain.Memory{
			ID:    "mem_probe543",
			Type:  "methodology.review",
			Attrs: json.RawMessage(`{"structured_payload":` + payload + `}`),
		}
	}

	// FLOOR and control: the object shape renders chrome. Without it, "the string
	// renders nothing" is satisfied by a reader that renders nothing ever.
	good := buildReviewHTML(mem(object))
	if !strings.Contains(good, "WARN") {
		t.Fatalf("a well-formed structured_payload produced no verdict banner (%q) — the reader "+
			"is broken, and the assertion below would then pass for the wrong reason", good)
	}

	bad := buildReviewHTML(mem(string(stringified)))
	if bad != "" {
		t.Errorf("a JSON-STRING structured_payload produced review chrome (%q). The card says "+
			"reviewPayload expects an object and finds nothing in this case, which is why "+
			"aihub#465 made it a 400 instead of decoding the string; if the reader now copes, the "+
			"400 is a refusal of something that works and the card must say so", bad)
	}
}
