package domain

// aihub#543 probe wave 2 — the column claim three cards make about
// pf_list_work_items and nothing held.
//
// Two sentences on two cards rest on the same fact about the SQL:
//
//	pf_list_work_items  "That projection is why `content` is always null here"
//	pf_get_work_item    "its response is projected and its `content` is null by
//	                     design"
//
// The MCP-side arms (internal/mcp/list_wi_slim_e2e_test.go) hold what the
// PROJECTION deletes, and their own reason for deleting `content` is quoted from
// this layer — "neither list query SELECTs wi.content, so this is null on every
// row this endpoint has ever returned". That quoted premise had no arm. So a
// query that started selecting the column would leave every projection test
// green — the projection deletes the key either way — while the card's stated
// REASON became false and the ~20 kB body it exists to keep off the wire started
// travelling on every list page.
//
// 🔴 Why this is a source census and not a call. buildListWorkItemsQuery can be
// called with no pool and its text read, but listWorkItemsByVector builds its
// statement INSIDE the function and sends it, so a live database is the only way
// to reach it by calling — and a DB-gated arm would not run on CI's plain unit
// step, which is where a claim this cheap belongs. wi_vector.go's own comment
// calls the two SELECTs a lockstep pair ("Same 26-column SELECT as
// buildListWorkItemsQuery"); this walk is what makes that comment enforced
// rather than aspirational.
//
//	GOWORK=off go test ./internal/domain/ -run TestWorkItemListSelect -count=1 -v

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// fullRecordSelectMarker identifies the work-item list SELECTs, by the pair of
// columns only they project.
//
// Chosen as a CODE PROPERTY rather than by file and line, for the reason
// rowserr's Loop.Key argument gives: a key that moves when anything above it is
// edited stops matching silently. Measured 2026-09-10 over internal/domain:
// `FROM work_items wi` appears at 15 sites and this pair at exactly 2 — the
// paging query and the vector query. The other 13 are the conflict scans, the GC
// sweeps and the seven ready-queue segments, none of which returns a whole
// record and none of which the two cards are about.
//
// The comparison runs on a WHITESPACE-NORMALISED column list (selectBlocksIn
// joins strings.Fields), so a line break inserted between the two column names
// does not break the marker — measured, mutant G3 below, because the first draft
// of this comment claimed the opposite and would have sent a future reader to
// re-pin a constant that needed no re-pinning. What DOES break it is reordering
// or renaming the pair, and that lands on the floor Fatal, which says so.
const fullRecordSelectMarker = "wi.declared_resources, wi.resources_version"

// floorFullRecordSelects is how many such SELECTs must be found.
//
// A walk that finds NONE asserts nothing about any query, and every check below
// would pass over an empty population — the vacuous green the floors in this
// repo exist to refuse. Two is the measured count and also the structural
// minimum: the text path and the vector path are separate statements because
// only one of them can carry a cosine.
const floorFullRecordSelects = 2

// TestWorkItemListSelectsCarryTheCASTokenAndNotTheBody is the census.
//
// ─── Recorded mutants ──────────────────────────────────────────────────────
//
// Every one was applied to this tree and the verdict is what RAN.
//
//	── enforcement side (the queries, the projection untouched) ──
//	M24 add wi.content to buildListWorkItemsQuery's column list
//	                                        RED  the ~20 kB body the two cards
//	                                             say is never on this response
//	                                             starts travelling, and every
//	                                             projection arm stays green
//	M25 drop wi.resources_version from the vector query's column list
//	                                        RED  ON THE FLOOR, and the attribution
//	                                             is corrected here (2026-09-10,
//	                                             aihub#543's review round): the
//	                                             column is HALF THE MARKER, so
//	                                             dropping it removes that site from
//	                                             the population and the floor Fatal
//	                                             names it. The per-site check that
//	                                             used to be credited with this was
//	                                             dead by construction — every site
//	                                             in the population contains the
//	                                             marker, hence the column — and it
//	                                             is gone. The compare-and-set token
//	                                             is still held, by the same
//	                                             mechanism as M27 below
//	M27 reorder the pair to `wi.resources_version, wi.declared_resources`
//	                                        RED  on the FLOOR, which is the
//	                                             correct answer: the marker no
//	                                             longer matches and the message
//	                                             says to fix it in the same diff
//
//	── declaration side (the projection, the queries untouched) ──
//	M26 stop internal/mcp/list_wi_slim.go deleting the `content` key
//	                                        RED  the key returns as an explicit
//	                                             null, so "an absent key means
//	                                             null" stops describing this
//	                                             response
//
//	── control ──
//	G3  break the column pair across a line inside the same SELECT
//	                                      GREEN  the walk normalises whitespace,
//	                                             so a reformat is not a false
//	                                             alarm — and this is why the
//	                                             comment on fullRecordSelectMarker
//	                                             says what it says
func TestWorkItemListSelectsCarryTheCASTokenAndNotTheBody(t *testing.T) {
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatalf("read the domain package directory: %v", err)
	}

	type site struct {
		file    string
		columns string
	}
	var sites []site
	filesWalked := 0

	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		raw, rerr := os.ReadFile(filepath.Clean(name))
		if rerr != nil {
			t.Fatalf("read %s: %v", name, rerr)
		}
		filesWalked++
		src := string(raw)
		for _, block := range selectBlocksIn(src) {
			if strings.Contains(block, fullRecordSelectMarker) {
				sites = append(sites, site{file: name, columns: block})
			}
		}
	}

	if filesWalked < 10 {
		t.Fatalf("the walk read only %d non-test .go file(s) in this package — it is looking at "+
			"the wrong directory, and every assertion below would be about nothing", filesWalked)
	}
	if len(sites) < floorFullRecordSelects {
		t.Fatalf("found %d work-item full-record SELECT(s) carrying %q, floor is %d.\n"+
			"    A walk that finds nothing classifies nothing: the two checks below would pass "+
			"over an empty population, which is the same green as a repo whose queries are "+
			"correct. Either the column pair was reformatted (in which case fix "+
			"fullRecordSelectMarker in the same diff) or a query lost the CAS token.",
			len(sites), fullRecordSelectMarker, floorFullRecordSelects)
	}

	for _, s := range sites {
		// The half the two cards' `content` sentences rest on.
		if strings.Contains(s.columns, "wi.content") {
			t.Errorf("%s: a work-item list SELECT projects wi.content.\n"+
				"    Both the pf_list_work_items and pf_get_work_item cards say `content` is null "+
				"on every row this endpoint returns, and internal/mcp/list_wi_slim.go deletes the "+
				"key on that premise — so the projection tests stay green either way while a body "+
				"of up to 20 kB per item starts crossing the wire and the cards' stated reason "+
				"becomes false. If the column is wanted, that is a decision about the response "+
				"size, and the cards say otherwise today.\n    SELECT: %s", s.file, s.columns)
		}
		// 🔴 There is deliberately NO second check for wi.resources_version here.
		// One stood at this spot and was DEAD BY CONSTRUCTION: `sites` holds only
		// blocks containing fullRecordSelectMarker, and that marker is
		// "wi.declared_resources, wi.resources_version" — so every member of the
		// population contains the column the check asked about, and it could not
		// fail. Removed 2026-09-10 by aihub#543's review round.
		//
		// The claim it was written for is not lost: dropping wi.resources_version
		// from a query drops the MARKER, that site leaves the population, and the
		// floor Fatal above fires naming the file and telling the reader to fix
		// fullRecordSelectMarker in the same diff. That is where mutant M25 really
		// landed, not here.
	}

	// The two paths must both be in the population, named rather than counted:
	// a count of 2 is satisfied by finding the same file twice.
	seen := map[string]bool{}
	for _, s := range sites {
		seen[s.file] = true
	}
	for _, want := range []string{"work_items.go", "wi_vector.go"} {
		if !seen[want] {
			t.Errorf("no full-record work-item SELECT was found in %s. The text path and the "+
				"vector path are separate statements, wi_vector.go calls them a lockstep pair, "+
				"and a walk that reaches only one of them measures half the contract while "+
				"reporting all of it. Files found: %v", want, objectKeysSortedDomain(seen))
		}
	}

	// The DECLARATION half, and the reason it is here rather than in a second
	// file: the two cards do not claim that the column is unselected, they claim
	// that "an absent `content` key means null" — which is the CONJUNCTION of a
	// query that does not project the column and a projection that deletes the
	// key anyway. Either half alone leaves the published invariant false: with
	// the delete gone the key comes back as an explicit null (a different
	// response shape from the one the card describes), and with the column
	// selected the delete starts removing a real body.
	//
	// Read as source text, the way internal/server/project_visibility_gate_test.go
	// reads ../domain/work_items.go, because the projection lives in another
	// package and importing it here would invert the dependency.
	projection := filepath.Join("..", "mcp", "list_wi_slim.go")
	raw, err := os.ReadFile(projection)
	if err != nil {
		t.Fatalf("read %s: %v — the card's \"an absent key means null\" invariant is this file "+
			"AND the SELECTs above together, so an arm that cannot read one of them cannot "+
			"answer at all", projection, err)
	}
	if !strings.Contains(string(raw), `delete(m, "content")`) {
		t.Errorf("%s no longer deletes the `content` key.\n"+
			"    Both cards publish \"an absent key means null\" for this response and the "+
			"pf_list_work_items card says `content` is always null here. With the column "+
			"unselected (asserted above) and the delete gone, the key comes back as an "+
			"explicit null instead of being absent — which is a different response shape from "+
			"the published one, and the one arm that would notice is this pairing.", projection)
	}

	t.Logf("work-item full-record SELECTs: %d site(s) across %v; projection %s deletes content",
		len(sites), objectKeysSortedDomain(seen), projection)
}

// selectBlockRe matches a SELECT column list up to its FROM.
//
// (?s) so a multi-line statement is one match, and non-greedy so two statements
// in one file do not merge into a single block — which would make a `wi.content`
// in either of them read as belonging to both, and would let a query lose
// wi.resources_version while its neighbour's copy kept the check green.
var selectBlockRe = regexp.MustCompile(`(?is)\bSELECT\b(.*?)\bFROM\b`)

func selectBlocksIn(src string) []string {
	var out []string
	for _, m := range selectBlockRe.FindAllStringSubmatch(src, -1) {
		out = append(out, strings.Join(strings.Fields(m[1]), " "))
	}
	return out
}

// objectKeysSortedDomain renders a set's keys, sorted, for failure text.
func objectKeysSortedDomain(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	for i := 1; i < len(out); i++ {
		for j := i; j > 0 && out[j] < out[j-1]; j-- {
			out[j], out[j-1] = out[j-1], out[j]
		}
	}
	return out
}
