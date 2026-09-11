package mcp_test

// aihub#449 / aihub#411 T2-20 — the ready queue's section count is ONE number,
// written in four places, and this test is what keeps the four the same.
//
// The four copies are:
//
//	struct       internal/domain/work_items.go (ReadyQueue) — the field list
//	schema       the pf_get_ready_queue description in tools_lifecycle.go
//	design doc   the Ready Queue block of docs/design/polyforge-v1-design.md
//	skill doc    plugins/polyforge/skills/pf-status/SKILL.md — the operator's view
//
// Before this wi the first three said seven, six and six-with-three-impossible-
// fields respectively, and nothing anywhere went red. Each copy is individually
// correct-looking — that is the whole difficulty — so the only thing that can
// catch the drift is an assertion that reads all of them and compares them.
//
// The fourth copy was the proof: the skill doc said "six segments" from
// aihub#449 until a person fixed it by hand in 1.1.31, because it was the one
// copy this gate did not read (aihub#560). Reading it here changes nothing
// under plugins/ — a test that reads a file is not a plugin change and forces
// no version bump.
//
// 🔴 Why the STRUCT is the authority and not the schema: the struct is what
// marshals, so it is the only one of the three a caller can observe. The other
// two are descriptions of it, and a description that disagrees with what ships
// is the defect this test names.
//
// No database needed:
//
//	go test ./internal/mcp/ -run TestReadyQueueSection

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

const (
	// readyQueueStructFile is the marshalling authority, relative to this
	// package's directory (where `go test` runs).
	readyQueueStructFile = "../domain/work_items.go"
	// readyQueueDesignDoc holds the third copy.
	readyQueueDesignDoc = "../../docs/design/polyforge-v1-design.md"
	// readyQueueDesignHeading opens the block inside it. The block is fenced, so
	// the heading plus the fence bounds it without any line numbers — docs/ may
	// not carry those (scripts/pf_docs_contract_check.py, C1).
	readyQueueDesignHeading = "#### Ready Queue (Layer 3)"
	// readyQueueSegmentFloor guards against the AST walk finding nothing: a
	// struct this test could not parse would otherwise agree with a description
	// that named no count, and every arm below would be vacuous. Measured
	// 2026-09-08: 7 segments.
	readyQueueSegmentFloor = 6
)

// readyQueueDeadFields are the four the design doc drew and no code could
// produce: `unblocked_at` had no writer anywhere (deleted from ReadyItem by
// aihub#449), `expires_at` went with the ownership model in v1.21, `kind` was
// deleted from work_items in v1.22, and `owner_user_type` — the one aihub#411
// T2-20 did not list — has never existed on RunningItem at all, zero hits in Go
// across the tree. They are listed here so that putting one back is a decision
// someone has to make in this file, rather than a paragraph somebody re-adds to
// a design doc nothing checks.
var readyQueueDeadFields = []string{"unblocked_at", "expires_at", "kind", "owner_user_type"}

// readyQueueSegments returns the json names of ReadyQueue's segment fields, in
// declaration order.
//
// A SEGMENT is a slice of an *Item type. That discriminator is what separates
// the six-plus-one queue sections from `request_adjusted`, which is a slice too
// but is a disclosure envelope rather than a section of the queue — and the
// distinction matters, because request_adjusted KEEPS its `omitempty` on purpose
// (internal/domain/request_adjusted.go) while no segment may carry one.
func readyQueueSegments(t *testing.T) []string {
	t.Helper()
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, readyQueueStructFile, nil, parser.ParseComments)
	if err != nil {
		t.Fatalf("parse %s: %v", readyQueueStructFile, err)
	}

	var st *ast.StructType
	ast.Inspect(file, func(n ast.Node) bool {
		ts, ok := n.(*ast.TypeSpec)
		if ok && ts.Name.Name == "ReadyQueue" {
			if s, ok := ts.Type.(*ast.StructType); ok {
				st = s
			}
		}
		return st == nil
	})
	if st == nil || st.Fields == nil {
		t.Fatalf("ReadyQueue not found in %s — this test cannot pass by not finding its "+
			"subject", readyQueueStructFile)
	}

	var segments []string
	for _, fld := range st.Fields.List {
		arr, ok := fld.Type.(*ast.ArrayType)
		if !ok || arr.Len != nil {
			continue
		}
		elem, ok := arr.Elt.(*ast.Ident)
		if !ok || !strings.HasSuffix(elem.Name, "Item") {
			continue
		}
		if fld.Tag == nil {
			t.Errorf("ReadyQueue has an untagged segment field %v — it would marshal under "+
				"its Go name, which is not the published key", fld.Names)
			continue
		}
		tag := strings.Trim(fld.Tag.Value, "`")
		jsonTag := reflectStructTag(tag, "json")
		name, opts, _ := strings.Cut(jsonTag, ",")
		if strings.Contains(opts, "omitempty") {
			t.Errorf("ReadyQueue.%v (json %q) carries `omitempty`. A SEGMENT may not: its "+
				"absence would then mean both \"this section is empty\" and \"this server "+
				"predates the section\", and the empty case is a fact the Orchestrator acts "+
				"on. That is the rule internal/domain/request_adjusted.go states — an absent "+
				"key is acceptable only while the absence asserts NOTHING — and it is why "+
				"stale_running lost its omitempty in aihub#449 while request_adjusted keeps "+
				"one. Initialise the slice in newReadyQueue instead; a nil slice marshals to "+
				"`null`, which is worse than either.", fld.Names, name)
		}
		segments = append(segments, name)
	}

	if len(segments) < readyQueueSegmentFloor {
		t.Fatalf("only %d ReadyQueue segment(s) found, floor is %d — the walk is broken and "+
			"every comparison below would be vacuous", len(segments), readyQueueSegmentFloor)
	}
	return segments
}

// reflectStructTag pulls one key out of a struct tag without dragging in
// reflect.StructTag's whole surface. The tags here are the plain
// `json:"..."` shape and nothing else.
func reflectStructTag(tag, key string) string {
	for _, part := range strings.Fields(tag) {
		prefix := key + ":\""
		if strings.HasPrefix(part, prefix) {
			return strings.TrimSuffix(strings.TrimPrefix(part, prefix), "\"")
		}
	}
	return ""
}

// TestReadyQueueSectionCountIsOneNumber compares the schema's count with the
// struct's.
func TestReadyQueueSectionCountIsOneNumber(t *testing.T) {
	segments := readyQueueSegments(t)

	tools := liveContract(t)
	rq, ok := tools[readyQueueTool]
	if !ok {
		t.Fatalf("%s is not published — this test cannot pass by not finding its subject",
			readyQueueTool)
	}

	m := regexp.MustCompile(`\((\d+)-section\)`).FindStringSubmatch(rq.Description)
	if m == nil {
		t.Fatalf("%s's description states no section count: %q.\nIt has said one since the "+
			"tool was added, and the count is the half a caller plans around — dropping it "+
			"silently is how the schema and the struct got to disagree in the first place "+
			"(aihub#411 T2-20). Write \"(%d-section)\".", readyQueueTool, rq.Description,
			len(segments))
	}
	said, err := strconv.Atoi(m[1])
	if err != nil {
		t.Fatalf("unreadable section count %q in %s's description", m[1], readyQueueTool)
	}
	if said != len(segments) {
		t.Errorf("%s's description says %d sections; internal/domain/work_items.go "+
			"(ReadyQueue) marshals %d: %s.\nThe struct is the authority — it is the one a "+
			"caller can observe — so fix the description, and fix the Ready Queue block of "+
			"docs/design/polyforge-v1-design.md and the segment count in %s in the same "+
			"change.", readyQueueTool, said, len(segments), strings.Join(segments, ", "),
			readyQueueSkillDoc)
	}
	t.Logf("ready queue: %d segments (%s), description says %d",
		len(segments), strings.Join(segments, ", "), said)
}

// TestReadyQueueDesignDocDrawsTheSameSegments is the third copy: the design
// doc's response sketch, which is what a reader planning a Layer 3 orchestrator
// reads before they ever see the struct. aihub#186's design was written from it.
func TestReadyQueueDesignDocDrawsTheSameSegments(t *testing.T) {
	segments := readyQueueSegments(t)

	raw, err := os.ReadFile(filepath.Clean(readyQueueDesignDoc))
	if err != nil {
		t.Fatalf("read %s: %v", readyQueueDesignDoc, err)
	}
	block, ok := readyQueueDesignBlock(string(raw))
	if !ok {
		t.Fatalf("%q not found in %s, or it is not followed by a fenced block — retarget this "+
			"test rather than letting it pass by finding nothing",
			readyQueueDesignHeading, readyQueueDesignDoc)
	}

	for _, seg := range segments {
		if !regexp.MustCompile(`(?m)^\s*` + regexp.QuoteMeta(seg) + `\s*:`).MatchString(block) {
			t.Errorf("the Ready Queue block of %s does not draw the %q segment, which "+
				"internal/domain/work_items.go (ReadyQueue) marshals on every response. A "+
				"reader plans against this sketch: aihub#186's orchestrator design was "+
				"written from it.", readyQueueDesignDoc, seg)
		}
	}
	for _, dead := range readyQueueDeadFields {
		if regexp.MustCompile(`(?m)^[^-]*\b` + regexp.QuoteMeta(dead) + `\b`).MatchString(block) {
			t.Errorf("the Ready Queue block of %s draws %q outside a comment. No code in "+
				"this repository can produce it: unblocked_at never had a writer, expires_at "+
				"went with the ownership model in v1.21, kind was deleted in v1.22, and "+
				"owner_user_type has never been a field on RunningItem. Drawing "+
				"a field that cannot exist is how aihub#186 got planned against a no-op "+
				"(aihub#387, aihub#449).", readyQueueDesignDoc, dead)
		}
	}
}

// readyQueueDesignBlock returns the fenced block that follows the Ready Queue
// heading. Bounded by the heading and the fence rather than by line numbers,
// which docs/ may not carry and this test may not depend on.
func readyQueueDesignBlock(doc string) (string, bool) {
	_, after, found := strings.Cut(doc, readyQueueDesignHeading)
	if !found {
		return "", false
	}
	_, after, found = strings.Cut(after, "```")
	if !found {
		return "", false
	}
	block, _, found := strings.Cut(after, "```")
	if !found {
		return "", false
	}
	return block, true
}

// ─── aihub#543 probe wave 1: the four dead fields, in the Go tree ────────────
//
// readyQueueResponseTypes are the types that marshal into a ready-queue
// response. A dead field can only come back on one of these.
var readyQueueResponseTypes = []string{
	"ReadyQueue", "ReadyItem", "RunningItem", "StalledItem", "PausedItem",
}

// readyQueueDeadGoIdent is the Go name of the one dead field that ever existed
// here as a struct field: ReadyItem.UnblockedAt, deleted by aihub#449.
//
// A string literal rather than a symbol, obviously — the point is that nothing
// in the tree declares it — and the scan below reads IDENTIFIERS out of parsed
// syntax rather than bytes, so this literal, and the two comments in
// internal/domain/work_items.go that discuss the deletion, are invisible to it.
// A textual grep would find all three and report the field as live, which is the
// mirror of the failure this arm is about.
const readyQueueDeadGoIdent = "UnblockedAt"

// readyQueueDeadFieldFloor bounds how many struct fields the walk read across the
// five response types. A parse that found the types and no fields would agree
// with any dead-field list at all.
const readyQueueDeadFieldFloor = 15

// TestTheDeadReadyQueueFieldsAreDeclaredNowhereInGo holds the pf_get_ready_queue
// card's two claims about the fields aihub#449 and aihub#387 disposed of:
// `UnblockedAt` is deleted rather than written — "no code in this repository ever
// wrote it" — and `owner_user_type` has never been carried by `RunningItem`.
//
// 🔴 What it adds to TestReadyQueueDesignDocDrawsTheSameSegments above, which
// already names the same four. That arm reads the DESIGN DOC and refuses a
// drawing of a field no code can produce. Nothing read the code: a dead field
// re-added to RunningItem in Go would satisfy it completely — and would then be
// real, so the doc would be right and the card wrong, which is the harder
// direction to notice. The two arms are the two authorities.
//
// ─── Recorded mutants ──────────────────────────────────────────────────────
//
// Applied to this tree; the verdict is what ran.
//
//	── enforcement side (the code, the card untouched) ──
//	M24 put ReadyItem.UnblockedAt back, tagged unblocked_at
//	                                            RED  both halves: the tag census and
//	                                                 the identifier scan
//	M25 add RunningItem.OwnerUserType, tagged owner_user_type
//	                                            RED  the tag census
//	M26 retag RunningItem.OwnerDisplay as owner_user_type
//	                                            RED  the tag census — a dead name can
//	                                                 arrive on a live field, not only
//	                                                 on a new one
//
//	── publication side (the card, the code untouched) ──
//	M27 delete the sentence from the card        RED  K12 POPULATION_MOVED
//	G6  control: reword it, citation untouched GREEN  K12 owns the publication side
//
// ⚠️ Renaming the RunningItem TYPE was tried and is not a mutant this arm can
// answer: the rename does not compile, so the red belongs to the build. The type
// census is exercised by the both-ways comparison against the struct instead.
func TestTheDeadReadyQueueFieldsAreDeclaredNowhereInGo(t *testing.T) {
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, readyQueueStructFile, nil, parser.SkipObjectResolution)
	if err != nil {
		t.Fatalf("parse %s: %v", readyQueueStructFile, err)
	}

	// ── half one: no response type carries a dead field's json name.
	seenType := map[string]bool{}
	fields := 0
	for _, want := range readyQueueResponseTypes {
		ast.Inspect(file, func(n ast.Node) bool {
			ts, ok := n.(*ast.TypeSpec)
			if !ok || ts.Name.Name != want {
				return true
			}
			st, ok := ts.Type.(*ast.StructType)
			if !ok || st.Fields == nil {
				return true
			}
			seenType[want] = true
			for _, fld := range st.Fields.List {
				fields++
				if fld.Tag == nil {
					continue
				}
				name, _, _ := strings.Cut(
					reflectStructTag(strings.Trim(fld.Tag.Value, "`"), "json"), ",")
				for _, dead := range readyQueueDeadFields {
					if name != dead {
						continue
					}
					t.Errorf("%s.%v marshals as %q, which readyQueueDeadFields records as a "+
						"field no code in this repository can produce. Either it now has a "+
						"writer — in which case it is alive, the four-field list above shrinks, "+
						"and the Ready Queue block of %s may draw it after all — or it is a "+
						"field that will marshal as a zero value forever, which is how "+
						"aihub#186's orchestrator came to be designed against a no-op "+
						"(aihub#387, aihub#449).", want, fld.Names, name, readyQueueDesignDoc)
				}
			}
			return true
		})
	}
	for _, want := range readyQueueResponseTypes {
		if !seenType[want] {
			t.Errorf("no struct type %s in %s. This arm censuses the types that marshal into a "+
				"ready-queue response, and one it cannot find is one it does not check — a "+
				"rename here silently narrows what the four dead fields are refused from.",
				want, readyQueueStructFile)
		}
	}
	if fields < readyQueueDeadFieldFloor {
		t.Errorf("the walk read %d field(s) across the %d response type(s), floor is %d — a "+
			"census that read no fields agrees with any dead-field list, including this one",
			fields, len(readyQueueResponseTypes), readyQueueDeadFieldFloor)
	}

	// ── half two: the identifier exists nowhere in the tree's Go syntax.
	//
	// Repo-wide and including _test.go, because the claim on the card is
	// repo-wide: "no code in this repository ever wrote it". A writer needs the
	// identifier, and an identifier is what this looks for.
	parsed := 0
	var writers []string
	err = filepath.WalkDir(cardsRepoRoot, func(path string, d os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if d.IsDir() {
			switch d.Name() {
			case ".git", "vendor", "node_modules":
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(d.Name(), ".go") {
			return nil
		}
		f, perr := parser.ParseFile(token.NewFileSet(), path, nil, parser.SkipObjectResolution)
		if perr != nil {
			return fmt.Errorf("parse %s: %w", path, perr)
		}
		parsed++
		ast.Inspect(f, func(n ast.Node) bool {
			if id, ok := n.(*ast.Ident); ok && id.Name == readyQueueDeadGoIdent {
				writers = append(writers, path)
			}
			return true
		})
		return nil
	})
	if err != nil {
		t.Fatalf("walk %s: %v — an unparseable tree makes the absence below meaningless, so "+
			"this is a failure rather than a smaller census", cardsRepoRoot, err)
	}
	if parsed < 200 {
		t.Errorf("the walk parsed only %d Go file(s) under %s — far too few for this tree, and "+
			"a walk that parses nothing reports every identifier as absent", parsed, cardsRepoRoot)
	}
	if len(writers) > 0 {
		t.Errorf("the identifier %s appears in %v. The pf_get_ready_queue card states that no "+
			"code in this repository ever wrote it, which is the whole reason aihub#387's "+
			"disposition for a no-writer field applied and aihub#449 could delete it with the "+
			"response bytes unchanged. A writer makes it a live field with a contract, and the "+
			"card, the design doc and readyQueueDeadFields all have to change together.",
			readyQueueDeadGoIdent, writers)
	}

	t.Logf("dead fields: %d field(s) across %d response type(s) carry none of %v; %s appears in "+
		"0 of %d parsed Go files", fields, len(seenType), readyQueueDeadFields,
		readyQueueDeadGoIdent, parsed)
}

// ─── aihub#560: the fourth copy — the pf-status skill doc ────────────────────

// readyQueueSkillDoc is the fourth copy of the section count, relative to the
// repository root (cardsRepoRoot, contract_cards_gate_test.go — the same root
// every repo-root read in this package goes through). It is the copy an
// OPERATOR reads: the /pf-status skill renders the queue from this file's
// instructions, so a wrong count here mis-renders every status call. It said
// "six segments" from aihub#449 until 1.1.31, when a person noticed by hand —
// the exact drift the gate above was built for, on the one copy the gate did
// not read.
const readyQueueSkillDoc = "plugins/polyforge/skills/pf-status/SKILL.md"

// readyQueueCountWords spells the counts this gate can read back out of prose,
// index = value. The skill doc writes its count as a WORD ("seven segments"),
// not a digit, so the digit regexp the schema arm uses cannot serve here.
var readyQueueCountWords = []string{
	"zero", "one", "two", "three", "four", "five", "six",
	"seven", "eight", "nine", "ten", "eleven", "twelve",
}

// TestReadyQueueSectionCountInSkillDoc is the fourth copy: every spelled-out
// "<count> segments" in the pf-status skill doc must state the number
// internal/domain/work_items.go (ReadyQueue) marshals.
//
// What counts as a copy, and what does not:
//
//   - "<word> segments" (space, plural) where <word> spells a number — a copy.
//     "seven segments" appears in the skill doc's Purpose line and in its
//     global-view mechanic, and both bound what an operator expects back.
//   - "three-segment output" (hyphen, singular) — NOT a copy. That is the
//     skill's RENDER format, a different number that has nothing to do with
//     how many sections the queue carries, and pinning it here would red the
//     gate on a truth.
//   - "Highlight segments", "the segments" — no count stated, nothing to pin.
//
// Reading plugins/ is all this test does to it. It changes no plugin byte, so
// it forces no version bump and can land at any time.
func TestReadyQueueSectionCountInSkillDoc(t *testing.T) {
	segments := readyQueueSegments(t)
	if len(segments) >= len(readyQueueCountWords) {
		t.Fatalf("ReadyQueue marshals %d segments, which readyQueueCountWords cannot spell — "+
			"extend the table, it ends at %q", len(segments),
			readyQueueCountWords[len(readyQueueCountWords)-1])
	}
	want := readyQueueCountWords[len(segments)]

	wordValue := map[string]int{}
	for v, w := range readyQueueCountWords {
		wordValue[w] = v
	}

	path := filepath.Join(cardsRepoRoot, filepath.FromSlash(readyQueueSkillDoc))
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v — this test cannot pass by not finding its subject",
			readyQueueSkillDoc, err)
	}

	countRe := regexp.MustCompile(`\b([A-Za-z]+) segments\b`)
	found := 0
	for i, line := range strings.Split(string(raw), "\n") {
		for _, m := range countRe.FindAllStringSubmatch(line, -1) {
			said, ok := wordValue[strings.ToLower(m[1])]
			if !ok {
				continue // "Highlight segments" — a mention, not a count
			}
			found++
			if said != len(segments) {
				t.Errorf("%s:%d says %q — %d sections; internal/domain/work_items.go "+
					"(ReadyQueue) marshals %d: %s.\nThe struct is the authority — it is the one "+
					"a caller can observe — so write %q, and fix the schema description and the "+
					"Ready Queue block of docs/design/polyforge-v1-design.md in the same change. "+
					"This is the copy that sat on \"six\" from aihub#449 to 1.1.31 because "+
					"nothing read it (aihub#560).", readyQueueSkillDoc, i+1, m[0], said,
					len(segments), strings.Join(segments, ", "), want+" segments")
			}
		}
	}
	if found == 0 {
		t.Fatalf("%s spells out no segment count anywhere (no \"<count> segments\" phrase). It "+
			"has stated one since the skill was written, and the count is what an operator "+
			"plans a status render around — dropping it silently is the same defect as the "+
			"schema dropping \"(N-section)\" (aihub#411 T2-20). Write %q, or retarget this "+
			"test rather than letting it pass by finding nothing.",
			readyQueueSkillDoc, want+" segments")
	}
	if !t.Failed() {
		t.Logf("skill doc: %d spelled-out count(s) in %s, all say %q, struct marshals %d",
			found, readyQueueSkillDoc, want+" segments", len(segments))
	}
}
