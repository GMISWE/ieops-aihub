package mcp_test

// aihub#458 — the layer-0 contract-card gate.
//
// ─── What a card is, and what this file is for ─────────────────────────────
//
// docs/mcp-cards/<tool>.md holds one CONTRACT CARD per published MCP tool: a
// generated machine block pinning the tool's published surface, and hand-written
// prose walking hops 0-5 for every parameter and response field.
//
// The prose half exists because universal_contract_gate_test.go says, in its own
// header, what it cannot measure: hop 4, "the bound field is ACTED ON", whose
// census "needs a tool->domain-function map — a hand-written list, which is the
// thing this file exists to replace". A card IS that hand-written list. The two
// files are complements, not rivals: that one quantifies four mechanical
// properties over every tool, this one keeps a human-written description of each
// tool honest about the surface it describes.
//
// ─── Why the machine block is generated, and why that is still a gate ──────
//
// The block is written by TestGenerateContractCards (PF_CARDS_REGEN=1) and
// COMMITTED. The gate then compares committed against live. That is the golden
// file pattern, and hand-typing 232 parameter rows would instead guarantee
// transcription errors this gate would report as code defects.
//
// 🔴 This section used to argue that regenerating "is not a silent pass: the
// regenerated diff has to be committed, so a schema change surfaces as a card diff
// a reviewer reads." aihub#473 MEASURED that and it is false. Changing a published
// parameter description, running PF_CARDS_REGEN=1 and re-running the gate came back
// green with the card still quoting the OLD text verbatim; the entire diff was
// input_schema_sha256 moving from one 64-hex string to another, and both are
// equally unreadable. The real claim is narrower — regeneration surfaces THAT
// something changed, never what — so the arm that reads the prose is K9 below, and
// the two are not interchangeable.
//
// The generator writes ONLY the first fenced json block of an existing card. A
// generator that could overwrite prose is a generator that eventually will.
//
// ─── The instrument is the one CI already trusts ───────────────────────────
//
// The live surface comes from cli.RunDumpMCPSchemas — the same call
// `polyforge dump-mcp-schemas` makes and the same JSON the Contract Lint job
// checks the shipped plugin against. Not a second registry reader: a second
// reader is a second thing that can disagree with the server, and then the cards
// would be pinned to something no other check looks at.
//
// ⚠️ That instrument carries tool descriptions but NOT per-parameter
// descriptions, which is why a card pins parameter NAME / TYPE / REQUIRED / ENUM
// from it and pins prose only by hash. Copying 40 kB of descriptions into docs/
// would create a second copy of the schema's own prose whose only failure mode is
// disagreeing with it; a hash costs one line and fails on exactly the same event.
//
// 🔴 TWO hashes, and the second one exists because the first was measured
// insufficient DURING THIS WORK. `description_sha256` covers the tool
// description; `input_schema_sha256` covers the serialised InputSchema, which is
// where every PARAMETER description lives. While this change was in review,
// aihub#433 landed and rewrote pf_remember's base_strength description from
// "(0-1)" to "1-5 (default 3)" and pf_recall's min_strength alongside it — a
// contract change that falsified three cards outright — and with only the first
// hash the gate stayed GREEN through it, because the contract JSON carries no
// per-property descriptions to hash. The second hash is taken from the live SDK
// session instead, which does. A gate that misses the drift it was built for is
// worse than none, and this one missed it once.
//
// ─── The arms ──────────────────────────────────────────────────────────────
//
//	K1 coverage        a published tool with no card
//	K2 orphan          a card naming a tool the registry does not publish
//	K3 schema drift    a card's params, description hash or InputSchema hash
//	                   disagreeing with live
//	K4 non-vacuity     a required section missing, out of order or EMPTY, a
//	                   parameter never named in the prose, a thin hop 0-1 on a
//	                   tool with no parameters to quantify over, or a "written"
//	                   card with no hop-4 body
//	K5 stale exemption a "pending" card that HAS a hop-4 body, or more pending
//	                   cards than the ceiling allows
//	K6 anchor          a cited .go path that does not exist, a cited symbol the
//	                   cited file does not declare, or a bare filename cited
//	                   with no directory
//	K7 corpus drift    response_keys_observed disagreeing with the checked-in
//	                   aihub#412 corpus record, in either direction including
//	                   null against a record that exists
//	K8 floors          the walk itself finding too little to have measured
//	                   anything
//	K9 verbatim quote  a hop 0-1 table cell quoting text the tool does not
//	                   publish — the arm that survives PF_CARDS_REGEN=1
//
// K10 is NOT in this file and does not run in the always-on step. It lives in
// card_response_keys_live_e2e_db_test.go, is gated on AIHUB_TEST_DB, and is the
// only arm that reaches a server: it drives 42 of the 48 published tools and
// refuses any top-level key a live response carries that neither the card nor
// docs/mcp-cards/live-response-keys.json declares. Every arm above compares one
// checked-in file with another, so K10 is what makes the card set a claim about
// the running system rather than about itself.
//
// ─── K6 is deliberately NOT delegated to the docs-contract script ──────────
//
// Measured on this tree rather than assumed: scripts/pf_docs_contract_check.py's
// C1 scans markdown_files(DOCS), i.e. all of docs/, so the cards are already
// covered for line-number citations. Its C2 — the arm that checks a referenced
// path EXISTS — takes markdown_files(SUPERPOWERS) plus docs/mcp-tools.md, and
// nothing else. docs/mcp-cards/ is outside it, and per aihub#406 C2 verifies the
// path rather than the symbol even where it does run.
//
// So K6 carries both checks here. Three reasons for self-gating rather than
// waiting on aihub#406/#439: those two are queued and unclaimed, so depending on
// them parks this gate behind unstarted work; an anchor arm living in the same
// file as the schema arm cannot be reorganised away from the cards it protects
// (C2's own guard comment records that hazard about docs/superpowers/); and if
// #406 later widens C2 to docs/** with symbol checking, K6 becomes redundant and
// that wi can delete it. A redundant check is cheap. An absent check reports
// green.
//
// No database needed:
//
//	go test ./internal/mcp/ -run TestContractCard -v
//
// Regenerate the machine blocks after a schema change:
//
//	PF_CARDS_REGEN=1 go test ./internal/mcp/ -run TestGenerateContractCards

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"

	"github.com/GMISWE/ieops-aihub/internal/cli"
)

// ─────────────────────────────── locations ───────────────────────────────────

const (
	// cardsRepoRoot is the repository root as seen from this package's directory,
	// which is where `go test` runs.
	cardsRepoRoot = "../.."
	cardsDirRel   = "docs/mcp-cards"
	// cardsCorpusDirRel holds aihub#412's per-tool hop-5 records. The cards copy
	// the key list out of it and K7 compares the copy, so the two cannot drift.
	cardsCorpusDirRel = "docs/audits/aihub-412-corpus-facts/response-keys"
)

// ───────────────────────────────── floors ────────────────────────────────────

// Every floor here exists for the reason universal_contract_gate_test.go states
// for its own set: a scanner that sees nothing exits 0 exactly like a clean
// repo. Each sits far below what is measured today and far above zero, so it
// fails on a broken walk rather than on ordinary change.
//
// 🔴 None of them is set AT the measurement. floorTools was, once, and aihub#448
// had to lower it because floor == measured fails on any legitimate REMOVAL —
// and unpublishing dead tools is standing policy here (aihub#387/#394/#448). The
// same reasoning applies to every number below.
const (
	// floorCards is the card-side twin of floorTools, which this file reuses from
	// universal_contract_gate_test.go rather than restating. Measured 2026-09-08:
	// 48 cards for 48 published tools.
	floorCards = 40
	// floorCardParams bounds the total parameter rows the cards pin. Measured
	// 2026-09-08: 232 across the 48 tools.
	floorCardParams = 180
	// floorCardAnchors bounds how many file+symbol citations the anchor arm
	// actually resolved. Without it, a card set that cited nothing would pass K6
	// by having nothing to check. Measured 2026-09-08: 207 across 30 files.
	floorCardAnchors = 60
	// floorCardSections bounds how many required sections K4 found and measured.
	// The arm quantifies over sections, so a card set the walk could not split
	// into sections would satisfy it by having none. Measured 2026-09-08: 288
	// (48 cards x 6 sections).
	floorCardSections = 200
	// floorCardCorpus bounds how many cards K7 compared against a corpus record.
	// Without it, deleting the corpus records AND the card lists together leaves
	// K7 comparing nothing and reporting green. Measured 2026-09-08: 42 of 48
	// cards have a record.
	floorCardCorpus = 30
	// floorCardQuotes bounds how many verbatim quotes K9 checked against the live
	// schema. A card set that quoted nothing would pass that arm by quoting
	// nothing. Measured 2026-09-08: 28 leading-quote hop 0-1 cells across 21 cards.
	floorCardQuotes = 12
)

// maxPendingCards is a CEILING ON DEBT, not a floor on a measurement, so unlike
// every constant above it IS set at the measured value on purpose. Lowering it
// happens for free as cards get written; raising it has to be a deliberate edit
// somebody signs off on. Measured 2026-09-08: 0 cards are pending.
const maxPendingCards = 0

// maxHistoricalQuoteRows is the same kind of ceiling for K9's escape hatch: the
// number of hop 0-1 table rows allowed to carry cardHistoricalMarker and quote
// text the live schema no longer publishes. Measured 2026-09-08: 0.
//
// 🔴 The ceiling is the reason the marker is safe to offer at all. An exemption
// that costs one comment on one line is cheaper than reading the schema, so it
// becomes the compliant path and the arm quietly stops measuring anything; an
// exemption that also costs an edit to this constant, in a diff somebody signs,
// costs more than compliance. Lowering it is free, raising it is a decision.
const maxHistoricalQuoteRows = 0

// minHop4Body is how many characters of hop-4 prose a "written" card must carry.
//
// It is the arm that separates a card from a generated shell. K1-K3 are all
// satisfied by an empty file with a correct machine block, which is exactly the
// artifact a generator produces — and "the file exists" is not the claim this
// gate is supposed to certify. The same number is what makes a "pending" card
// falsifiable in the other direction (K5).
const minHop4Body = 200

// minCardSectionBody is how many characters of prose EVERY required section must
// carry, hop 4 included (where minHop4Body then raises the bar further).
//
// 🔴 Until aihub#473 hop 4 was the only section with a length at all, and the
// other five were certified by the presence of a heading STRING. Measured on the
// tree: emptying `## hop 5`, `## Policy` and `## Open` in a card while keeping
// their headings left the whole gate green, as did reducing a card to its machine
// block, six bare headings and a hop-4 body — five of six sections empty, nothing
// red. The arm's own error message claims the six sections mean "a card missing
// one is silent about a hop rather than saying it has nothing to report there",
// and a heading with nothing under it is precisely the silence it says it stops.
//
// 16 rather than something nearer the measurement: the shortest real section in
// the set is 31 characters (`- Nothing this card can settle.`, the convention for
// a section with nothing to report), so this sits at about half of it — far above
// zero, far above every `TODO` cardSkeleton emits, and with room for a
// legitimately terse section. A floor set AT the measurement fails on the next
// honest edit, which is the aihub#448 lesson the floors above already carry.
const minCardSectionBody = 16

// minNoParamHop01 is the hop 0-1 floor for a tool that publishes NO parameters.
//
// It exists because K4 PARAM_UNDOCUMENTED — the arm that keeps hop 0-1 honest for
// every other card — quantifies over published parameters, so for a tool with none
// it is vacuously satisfied and says nothing whatever. Three published tools are in
// that position: pf_whoami, pf_list_projects and pf_list_users. Measured 2026-09-08
// their hop 0-1 sections run 359, 390 and 189 characters, so this sits below all
// three while being far more than a heading and a sentence.
const minNoParamHop01 = 120

// ───────────────────────────── the card format ───────────────────────────────

// cardParam mirrors the shape internal/cli's contract JSON emits for one
// parameter, so a card block and the live dump are comparable by decoding both
// into the same type. Deliberately NOT a copy of the schema property: per-property
// descriptions are not in that contract at all (see the file header).
type cardParam struct {
	Type     string   `json:"type"`
	Required bool     `json:"required"`
	Enum     []string `json:"enum,omitempty"`
}

// cardBlock is the generated, committed machine half of a contract card.
type cardBlock struct {
	Tool              string `json:"tool"`
	DescriptionSHA256 string `json:"description_sha256"`
	// InputSchemaSHA256 covers the whole serialised InputSchema, i.e. every
	// PARAMETER description as well as the structure below. See the file header:
	// the contract JSON the other fields come from omits per-property
	// descriptions, so without this a parameter's published meaning can change
	// while every other arm stays green.
	InputSchemaSHA256 string               `json:"input_schema_sha256"`
	Params            map[string]cardParam `json:"params"`
	// ResponseKeysObserved is nil when aihub#412's corpus holds no record for
	// this tool, and that is a different fact from an empty list — a tool nobody
	// has called versus a tool whose results carry no top-level keys.
	//
	// 🔴 "K7 checks both directions" is what this comment used to claim, and
	// aihub#473 measured it false in one combination: equalStrings compares
	// lengths, so nil and [] are the same value to it, and a corpus record with
	// null keys therefore accepted a card value of null where the generator writes
	// []. K7 CORPUS_NULL_MASKS_RECORD is what makes the claim true now.
	//
	// ⚠️ And it is a claim about the COPY only. Both sides of K7 are checked-in
	// files, so a key the live tool still returns can be dropped from the card and
	// the corpus record in one change and K7 stays green — probed on pf_whoami's
	// `role`. What refuses that edit is K10 in card_response_keys_live_e2e_db_test.go
	// (aihub#482), which reads the key back off a live server; re-running the same
	// two-sided delete leaves this gate green and reddens that one.
	ResponseKeysObserved []string `json:"response_keys_observed"`
	Hop4Coverage         string   `json:"hop4_coverage"`
}

// card is one parsed card file.
type card struct {
	tool  string // from the filename
	path  string
	block cardBlock
	prose string // everything after the machine block
	body  string // the whole file, for the anchor arm
}

// requiredCardHeadings are matched as PREFIXES, so the readable remainder of each
// heading is free text while its identity is fixed.
var requiredCardHeadings = []string{
	"## hop 0-1",
	"## hop 2-3",
	"## hop 4",
	"## hop 5",
	"## Policy",
	"## Open",
}

// ─────────────────────────── the live surface ────────────────────────────────

// liveTool is one tool as the contract JSON describes it.
type liveTool struct {
	Description string               `json:"description"`
	Params      map[string]cardParam `json:"params"`
}

// liveContract runs the same instrument `polyforge dump-mcp-schemas` runs and
// decodes its output.
func liveContract(t *testing.T) map[string]liveTool {
	t.Helper()
	var buf bytes.Buffer
	if err := cli.RunDumpMCPSchemas(context.Background(), "", &buf); err != nil {
		t.Fatalf("dump MCP schemas: %v", err)
	}
	var out struct {
		Tools map[string]liveTool `json:"tools"`
	}
	if err := json.Unmarshal(buf.Bytes(), &out); err != nil {
		t.Fatalf("decode contract JSON: %v", err)
	}
	if len(out.Tools) < floorTools {
		t.Fatalf("the contract dump published %d tool(s), floor is %d — that is the instrument "+
			"being broken, not the toolset shrinking, and every assertion quantified over it "+
			"would be vacuous", len(out.Tools), floorTools)
	}
	return out.Tools
}

// liveInputSchemaHashes returns sha256 over each tool's SERIALISED InputSchema.
//
// A second instrument, and it has to be: cli.RunDumpMCPSchemas' contract JSON
// carries no per-property descriptions, so it cannot see a parameter's published
// meaning change. This reads the tools off a real SDK session — the same
// newContractGate harness universal_contract_gate_test.go uses, not a third
// registry reader — where InputSchema arrives whole.
//
// Marshalled rather than hashed in place because the SDK hands the schema over as
// a decoded map: encoding/json sorts map keys, so the bytes are deterministic
// across runs and the hash is stable for an unchanged schema.
func liveInputSchemaHashes(t *testing.T) map[string]string {
	t.Helper()
	_, tools := newContractGate(t)
	out := make(map[string]string, len(tools))
	for _, tool := range tools {
		b, err := json.Marshal(tool.InputSchema)
		if err != nil {
			t.Fatalf("marshal %s InputSchema: %v", tool.Name, err)
		}
		out[tool.Name] = descriptionSHA(string(b))
	}
	if len(out) < floorTools {
		t.Fatalf("the session published %d tool(s), floor is %d — instrument failure",
			len(out), floorTools)
	}
	return out
}

// liveSchemaProse returns, per tool, every string the tool PUBLISHES as prose:
// its description, plus every string VALUE anywhere in the serialised InputSchema
// — per-parameter descriptions, enum values, titles.
//
// The same instrument as liveInputSchemaHashes and for the same reason: the
// contract JSON cli.RunDumpMCPSchemas emits carries no per-property descriptions,
// and a per-parameter description is precisely what a card quotes. Marshalled and
// re-decoded rather than reflected over the SDK type, so this walks the same bytes
// the K3 hash covers and the two cannot disagree about what the schema is.
//
// Property NAMES are excluded on purpose. They are published text, but including
// them only widens the haystack a claimed-verbatim quote can accidentally match
// in, and every parameter name is already checked by K3 exactly.
//
// Joined with newlines because a quoted cell is single-line by construction
// (cardQuotedCell's class excludes \n), so no match can straddle two published
// strings and be counted as occurring within one.
func liveSchemaProse(t *testing.T) map[string]string {
	t.Helper()
	_, tools := newContractGate(t)
	out := make(map[string]string, len(tools))
	for _, tool := range tools {
		b, err := json.Marshal(tool.InputSchema)
		if err != nil {
			t.Fatalf("marshal %s InputSchema: %v", tool.Name, err)
		}
		var decoded any
		if err := json.Unmarshal(b, &decoded); err != nil {
			t.Fatalf("re-decode %s InputSchema: %v", tool.Name, err)
		}
		published := append([]string{tool.Description}, jsonStringValues(decoded)...)
		out[tool.Name] = strings.Join(published, "\n")
	}
	if len(out) < floorTools {
		t.Fatalf("the session published %d tool(s), floor is %d — instrument failure",
			len(out), floorTools)
	}
	return out
}

// jsonStringValues collects every string VALUE in a decoded JSON tree. Map keys
// are walked for their values and skipped themselves; see liveSchemaProse.
func jsonStringValues(v any) []string {
	switch node := v.(type) {
	case string:
		return []string{node}
	case []any:
		var out []string
		for _, e := range node {
			out = append(out, jsonStringValues(e)...)
		}
		return out
	case map[string]any:
		keys := make([]string, 0, len(node))
		for k := range node {
			keys = append(keys, k)
		}
		sort.Strings(keys) // deterministic, so a failure message is reproducible
		var out []string
		for _, k := range keys {
			out = append(out, jsonStringValues(node[k])...)
		}
		return out
	}
	return nil
}

// ───────────────────────────── reading the cards ─────────────────────────────

var cardJSONBlock = regexp.MustCompile("(?s)```json\\n(.*?)\\n```")

// readCards parses every card file in docs/mcp-cards, excluding the README.
func readCards(t *testing.T) map[string]*card {
	t.Helper()
	dir := filepath.Join(cardsRepoRoot, cardsDirRel)
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read %s: %v — the cards are the subject of this gate, so a missing "+
			"directory is a failure, not an empty pass", cardsDirRel, err)
	}
	cards := make(map[string]*card)
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".md") || name == "README.md" {
			continue
		}
		path := filepath.Join(dir, name)
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read %s: %v", path, err)
		}
		body := string(raw)
		m := cardJSONBlock.FindStringSubmatchIndex(body)
		if m == nil {
			t.Errorf("%s/%s: no fenced ```json block. Every card carries its machine block "+
				"first; regenerate with PF_CARDS_REGEN=1.", cardsDirRel, name)
			continue
		}
		var blk cardBlock
		if err := json.Unmarshal([]byte(body[m[2]:m[3]]), &blk); err != nil {
			t.Errorf("%s/%s: machine block is not valid JSON: %v", cardsDirRel, name, err)
			continue
		}
		cards[strings.TrimSuffix(name, ".md")] = &card{
			tool:  strings.TrimSuffix(name, ".md"),
			path:  path,
			block: blk,
			prose: body[m[1]:],
			body:  body,
		}
	}
	return cards
}

// cardSection is one level-2 section of a card's prose, as the FILE structures it
// rather than as a substring search happens to find it.
type cardSection struct {
	heading string // the whole heading line, e.g. "## hop 4 — what it actually does"
	body    string // trimmed prose under it, up to the next level-2 heading
}

// cardSections splits a card's prose into its level-2 sections, in document order.
//
// 🔴 This replaced a per-heading strings.HasPrefix search over the whole prose.
// That search already required a heading at COLUMN 0 and this keeps that; the two
// properties it did NOT have are the ones below, and each was a probe that passed
// against it (aihub#473):
//
//   - A heading counts only OUTSIDE a fenced code block, at both ends of a
//     section. Moving `## hop 0-1`, `## hop 2-3`, `## hop 5` and `## Policy`
//     inside a ```text fence — where they render as literal text and the card
//     shows nothing under any of them — left every arm in this file green.
//   - Sections come back IN DOCUMENT ORDER, which is what makes the order
//     checkable at all. A per-heading search finds each heading wherever it is and
//     structurally cannot tell you the card is inside out.
//
// Neither is what the old search got wrong about column 0 — it got that right.
// The failure was reading only its boolean, which is cardSectionBody's note.
//
// A fence toggles on any line whose trimmed form opens with ``` or ~~~, the same
// delimiters cardJSONBlock's own machine block uses. Deliberately not a CommonMark
// parser: the only question here is "is this line inside code", and a card needing
// more nesting than that to read is a card to simplify, not a parser to grow.
func cardSections(prose string) []cardSection {
	var out []cardSection
	var bodies [][]string
	inFence := false
	for _, l := range strings.Split(prose, "\n") {
		trimmed := strings.TrimSpace(l)
		if strings.HasPrefix(trimmed, "```") || strings.HasPrefix(trimmed, "~~~") {
			inFence = !inFence
		} else if !inFence && strings.HasPrefix(l, "## ") {
			out = append(out, cardSection{heading: l})
			bodies = append(bodies, nil)
			continue
		}
		if n := len(bodies); n > 0 {
			bodies[n-1] = append(bodies[n-1], l)
		}
	}
	for i := range out {
		out[i].body = strings.TrimSpace(strings.Join(bodies[i], "\n"))
	}
	return out
}

// cardSectionBody returns the body of the first section whose heading starts with
// the given prefix. The bool says the section EXISTS; an existing section can
// still be empty, and keeping those two answers apart is the whole point — the
// arm this replaced read the bool alone and therefore certified that a heading
// string occurred.
func cardSectionBody(secs []cardSection, headingPrefix string) (string, bool) {
	for _, s := range secs {
		if strings.HasPrefix(s.heading, headingPrefix) {
			return s.body, true
		}
	}
	return "", false
}

// requiredSectionOrder returns the index into requiredCardHeadings of each
// required section, in the order the file presents them. Sections a card adds
// beyond the six are ignored rather than reported: the six are a floor on shape,
// not a ban on saying more.
func requiredSectionOrder(secs []cardSection) []int {
	var out []int
	for _, s := range secs {
		for i, h := range requiredCardHeadings {
			if strings.HasPrefix(s.heading, h) {
				out = append(out, i)
				break
			}
		}
	}
	return out
}

// strictlyAscending is false for an out-of-order sequence AND for a repeated
// value, so it reports a duplicated heading as well as a reordered one. A card
// with two `## Policy` sections has one the reader will never find.
func strictlyAscending(v []int) bool {
	for i := 1; i < len(v); i++ {
		if v[i] <= v[i-1] {
			return false
		}
	}
	return true
}

// ─────────────────────────────── K1 / K2 / K8 ────────────────────────────────

func TestContractCardsCoverEveryPublishedTool(t *testing.T) {
	tools := liveContract(t)
	cards := readCards(t)

	if len(cards) < floorCards {
		t.Fatalf("found %d card(s) under %s, floor is %d — the walk is broken, and every "+
			"per-card assertion below it would be vacuous", len(cards), cardsDirRel, floorCards)
	}

	// K1: every published tool has a card.
	var missing []string
	for name := range tools {
		if _, ok := cards[name]; !ok {
			missing = append(missing, name)
		}
	}
	sort.Strings(missing)
	for _, name := range missing {
		t.Errorf("K1 CARD_MISSING: %s is published but %s/%s.md does not exist. A tool with no "+
			"card is a tool whose hop-4 behaviour is recorded nowhere; write one, or (if the "+
			"tool is being retired) unpublish it.", name, cardsDirRel, name)
	}

	// K2: no card for a tool the registry does not publish. This is the staleness
	// half, and it is the one usually left out: a card for a withdrawn tool
	// documents a contract nobody can call and reads exactly like a live one.
	var orphan []string
	for name := range cards {
		if _, ok := tools[name]; !ok {
			orphan = append(orphan, name)
		}
	}
	sort.Strings(orphan)
	for _, name := range orphan {
		t.Errorf("K2 CARD_ORPHAN: %s/%s.md describes a tool the registry does not publish. "+
			"Delete the card in the same change that unpublished the tool — a card that "+
			"outlives its tool is indistinguishable from one that describes a live one.",
			cardsDirRel, name)
	}

	t.Logf("K1/K2: %d published tools, %d cards", len(tools), len(cards))
}

// ────────────────────────────────── K3 ───────────────────────────────────────

func TestContractCardsMatchTheLiveSchema(t *testing.T) {
	tools := liveContract(t)
	schemaHashes := liveInputSchemaHashes(t)
	cards := readCards(t)

	pinned := 0
	for name, lt := range tools {
		c, ok := cards[name]
		if !ok {
			continue // K1 reports it
		}
		if c.block.Tool != name {
			t.Errorf("K3 CARD_NAME_MISMATCH: %s names tool %q. The filename is the identity; "+
				"regenerate with PF_CARDS_REGEN=1.", c.path, c.block.Tool)
		}
		want := descriptionSHA(lt.Description)
		if c.block.DescriptionSHA256 != want {
			t.Errorf("K3 DESCRIPTION_DRIFT: %s pins description_sha256=%s but the live "+
				"description hashes to %s. The tool description changed; RE-READ the card's "+
				"hop 0-1 section against the new text — it may now be describing a promise "+
				"the tool no longer makes — then regenerate with PF_CARDS_REGEN=1.",
				c.path, short(c.block.DescriptionSHA256), short(want))
		}
		if wantSchema, ok := schemaHashes[name]; ok && c.block.InputSchemaSHA256 != wantSchema {
			t.Errorf("K3 INPUT_SCHEMA_DRIFT: %s pins input_schema_sha256=%s but the live "+
				"InputSchema hashes to %s. Something in the published schema changed that the "+
				"contract JSON cannot see — almost always a PARAMETER DESCRIPTION, i.e. what a "+
				"caller is actually told this argument means. RE-READ the card's hop 0-1 table "+
				"against the new text before regenerating with PF_CARDS_REGEN=1; this arm exists "+
				"because aihub#433 changed two such descriptions and every other arm stayed green.",
				c.path, short(c.block.InputSchemaSHA256), short(wantSchema))
		}
		for pname, want := range lt.Params {
			got, ok := c.block.Params[pname]
			if !ok {
				t.Errorf("K3 PARAM_UNCARDED: %s publishes %q and %s does not pin it. A "+
					"parameter added without a card line is a parameter with no recorded "+
					"hop-4 behaviour.", name, pname, c.path)
				continue
			}
			pinned++
			if got.Type != want.Type {
				t.Errorf("K3 PARAM_TYPE_DRIFT: %s.%s is %q live and %q in %s",
					name, pname, want.Type, got.Type, c.path)
			}
			if got.Required != want.Required {
				t.Errorf("K3 PARAM_REQUIRED_DRIFT: %s.%s required=%v live and %v in %s — "+
					"required is the half of a schema a caller plans around, so this is a "+
					"contract change, not a formatting one",
					name, pname, want.Required, got.Required, c.path)
			}
			if !equalStrings(got.Enum, want.Enum) {
				t.Errorf("K3 PARAM_ENUM_DRIFT: %s.%s enum is %v live and %v in %s",
					name, pname, want.Enum, got.Enum, c.path)
			}
		}
		for pname := range c.block.Params {
			if _, ok := lt.Params[pname]; !ok {
				t.Errorf("K3 PARAM_WITHDRAWN: %s pins %q, which %s no longer publishes. "+
					"Withdrawing a parameter is a contract change the card has to record "+
					"rather than keep describing.", c.path, pname, name)
			}
		}
	}

	if pinned < floorCardParams {
		t.Errorf("K8 FLOOR_PARAMS: only %d parameter row(s) were compared, floor is %d — the "+
			"comparison found almost nothing, which is the instrument failing rather than "+
			"the schema shrinking", pinned, floorCardParams)
	}
	t.Logf("K3: %d parameter rows compared against the live contract", pinned)
}

// ───────────────────────────────── K4 / K5 ───────────────────────────────────

func TestContractCardsAreNotVacuous(t *testing.T) {
	tools := liveContract(t)
	cards := readCards(t)

	pending, sections := 0, 0
	for name, c := range cards {
		lt, published := tools[name]
		if !published {
			continue // K2 reports it
		}

		secs := cardSections(c.prose)

		// The six sections have to be PRESENT, NON-EMPTY and IN ORDER. Presence
		// alone was the whole arm until aihub#473 measured what that certifies:
		// four heading strings inside a code fence, in any order, satisfied it.
		for _, h := range requiredCardHeadings {
			body, ok := cardSectionBody(secs, h)
			if !ok {
				t.Errorf("K4 CARD_HEADING_MISSING: %s has no %q section at column 0 outside a "+
					"code fence. The six sections are the card's shape; a card missing one is "+
					"silent about a hop rather than saying it has nothing to report there.",
					c.path, h)
				continue
			}
			sections++
			if len(body) < minCardSectionBody {
				t.Errorf("K4 CARD_SECTION_EMPTY: %s's %q section carries %d character(s), "+
					"minimum %d. The heading is not the claim. An empty section reads as "+
					"answered and asserts nothing, which is the silence CARD_HEADING_MISSING "+
					"above says it stops — a section with nothing to report has to SAY so, as "+
					"in %q.", c.path, h, len(body), minCardSectionBody,
					"- Nothing this card can settle.")
			}
		}

		if order := requiredSectionOrder(secs); !strictlyAscending(order) {
			got := make([]string, 0, len(order))
			for _, i := range order {
				got = append(got, requiredCardHeadings[i])
			}
			t.Errorf("K4 CARD_HEADING_ORDER: %s presents its sections as %v; the required order "+
				"is %v (a repeat counts as out of order — a second `## Policy` is one the reader "+
				"never reaches). The order IS the hop sequence: hops 0 to 5 are a path a request "+
				"takes, and a card that reports the end before the beginning makes its reader "+
				"reconstruct the path from the headings.", c.path, got, requiredCardHeadings)
		}

		// A tool with NO published parameters gets nothing from PARAM_UNDOCUMENTED
		// below, which quantifies over parameters. This is the arm that stands in
		// for it; without it three cards had no hop 0-1 protection whatever.
		if hop01, ok := cardSectionBody(secs, "## hop 0-1"); ok &&
			len(lt.Params) == 0 && len(hop01) < minNoParamHop01 {
			t.Errorf("K4 HOP01_THIN_NOPARAMS: %s documents a tool with no published parameters "+
				"and its hop 0-1 section carries %d character(s), minimum %d. Every other card "+
				"has hop 0-1 held honest by PARAM_UNDOCUMENTED, which is vacuous for a tool with "+
				"nothing to quantify over — so for pf_whoami, pf_list_projects and pf_list_users "+
				"this is the only arm that asks hop 0-1 to say anything.",
				c.path, len(hop01), minNoParamHop01)
		}

		// Every published parameter has to be NAMED IN BACKTICKS in the prose.
		// Backticks and not bare text: `type`, `status` and `content` occur in any
		// English sentence, so a bare-substring check would pass on prose that
		// never mentions the parameter at all.
		for pname := range lt.Params {
			if !strings.Contains(c.prose, "`"+pname+"`") {
				t.Errorf("K4 PARAM_UNDOCUMENTED: %s never names `%s` in its prose, though %s "+
					"publishes it. Add its hop row — an unmentioned parameter is the case "+
					"this gate exists to make loud.", c.path, pname, name)
			}
		}

		hop4, _ := cardSectionBody(secs, "## hop 4")
		switch c.block.Hop4Coverage {
		case "written":
			if len(strings.TrimSpace(hop4)) < minHop4Body {
				t.Errorf("K4 HOP4_VACUOUS: %s is marked hop4_coverage=\"written\" but its "+
					"hop-4 section is %d characters (minimum %d). A machine block with no "+
					"prose behind it satisfies every other arm here and asserts nothing.",
					c.path, len(strings.TrimSpace(hop4)), minHop4Body)
			}
		case "pending":
			pending++
			if len(strings.TrimSpace(hop4)) >= minHop4Body {
				t.Errorf("K5 PENDING_STALE: %s carries a full hop-4 section and is still "+
					"marked hop4_coverage=\"pending\". An exemption that outlives its gap is "+
					"one nobody removes, and it silently excuses the next one. Flip it to "+
					"\"written\" and lower maxPendingCards in the same change.", c.path)
			}
		default:
			t.Errorf("K4 COVERAGE_UNKNOWN: %s has hop4_coverage=%q; the only values are "+
				"\"written\" and \"pending\".", c.path, c.block.Hop4Coverage)
		}
	}

	if pending > maxPendingCards {
		t.Errorf("K5 PENDING_CEILING: %d card(s) are pending, ceiling is %d. Raising the "+
			"ceiling is a deliberate decision somebody signs off on; lowering it is free.",
			pending, maxPendingCards)
	}
	if sections < floorCardSections {
		t.Errorf("K8 FLOOR_SECTIONS: only %d required section(s) were found and measured, floor "+
			"is %d — the section walk found almost nothing, and every length assertion above it "+
			"would be vacuous", sections, floorCardSections)
	}
	t.Logf("K4/K5: %d cards checked, %d sections measured, %d pending (ceiling %d)",
		len(cards), sections, pending, maxPendingCards)
}

// ────────────────────────────────── K6 ───────────────────────────────────────

// cardGoPathRef matches a backticked, path-qualified Go file, optionally followed
// by a parenthesised backticked symbol:
//
//	`internal/server/routes_step.go`
//	`internal/server/routes_step.go` (`handleGetStep`)
//
// The same anchor form scripts/pf_docs_contract_check.py's C1 error message tells
// authors to use. The path half is what C2 would check if it globbed this
// directory; the symbol half is what aihub#406 says C2 does not check anywhere.
var cardGoPathRef = regexp.MustCompile("`((?:[\\w.-]+/)+[\\w.-]+\\.go)`(?: \\(`([\\w.]+)`\\))?")

// cardBareGoFileRef matches a backticked Go filename with NO directory component,
// which K6 REPORTS rather than resolves.
//
// 🔴 Until aihub#473 this shape was invisible: cardGoPathRef requires at least one
// "/", so an injected `totally_made_up_nonexistent.go` was not an anchor the arm
// could see at all, while `internal/nope/alsofake.go` beside it was reported. Zero
// cards used the form, so this was a latent authoring hazard rather than a live
// defect — but the next author to write `routes_step.go` got silence, and
// scripts/pf_docs_contract_check.py's C2 does not glob this directory either.
//
// Banned rather than globbed for, deliberately. A bare filename need not identify
// a file: measured 2026-09-08, 8 .go basenames in this repo's 376 are used more
// than once and main.go alone is used 5 times, so a glob would resolve some names
// to whichever match it hit first. An anchor that passes while pointing somewhere
// the author did not mean is worse than one that goes red, and rejecting the form
// outright costs the author one directory name. It is also the rule C1 already
// enforces against line numbers, for the same reason — a citation has to identify
// exactly one place.
//
// The class excludes "/", so this cannot also match a path-qualified anchor: there
// is no backtick inside `internal/server/routes_step.go` for a match to start at.
var cardBareGoFileRef = regexp.MustCompile("`([\\w.-]+\\.go)`")

func TestContractCardAnchorsResolve(t *testing.T) {
	dir := filepath.Join(cardsRepoRoot, cardsDirRel)
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read %s: %v", cardsDirRel, err)
	}

	// One parse per referenced file, however many cards cite it.
	declared := map[string]map[string]bool{}
	resolved := 0

	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".md") {
			continue
		}
		path := filepath.Join(dir, e.Name())
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read %s: %v", path, err)
		}
		for _, m := range cardBareGoFileRef.FindAllStringSubmatch(string(raw), -1) {
			t.Errorf("K6 ANCHOR_BARE_FILENAME: %s/%s cites `%s` with no directory. Cite the "+
				"repo-relative path (`<dir>/%s`): 8 of this repo's 376 .go basenames are used "+
				"more than once, so a bare name need not identify a file — nothing can check it "+
				"and the next reader guesses which one was meant. This form was reported by "+
				"nothing at all before aihub#473: cardGoPathRef requires a `/`, so it was the "+
				"one anchor shape the gate could not see.",
				cardsDirRel, e.Name(), m[1], m[1])
		}

		for _, m := range cardGoPathRef.FindAllStringSubmatch(string(raw), -1) {
			ref, sym := m[1], m[2]
			abs := filepath.Join(cardsRepoRoot, ref)
			if _, statErr := os.Stat(abs); statErr != nil {
				t.Errorf("K6 ANCHOR_PATH_MISSING: %s/%s cites `%s`, which does not exist. "+
					"Either the file moved (fix the anchor) or the card describes code that "+
					"never landed (say so under ## Open).", cardsDirRel, e.Name(), ref)
				continue
			}
			resolved++
			if sym == "" {
				continue
			}
			names, ok := declared[ref]
			if !ok {
				names = declaredNames(t, abs)
				declared[ref] = names
			}
			// A dotted anchor (`Type.Method`) is resolved on its last segment, which
			// is the method or field name the file declares.
			leaf := sym
			if i := strings.LastIndex(leaf, "."); i >= 0 {
				leaf = leaf[i+1:]
			}
			if !names[leaf] {
				t.Errorf("K6 ANCHOR_SYMBOL_MISSING: %s/%s cites `%s` (`%s`), and that file "+
					"declares no such symbol. A semantic anchor that resolves to nothing "+
					"rots exactly as quietly as the line number it replaced — which is the "+
					"whole reason line numbers are banned here.",
					cardsDirRel, e.Name(), ref, sym)
			}
		}
	}

	if resolved < floorCardAnchors {
		t.Errorf("K8 FLOOR_ANCHORS: only %d anchor(s) resolved, floor is %d — a card set that "+
			"cites nothing passes this arm by having nothing to check", resolved, floorCardAnchors)
	}
	t.Logf("K6: %d anchors resolved across %d referenced files", resolved, len(declared))
}

// declaredNames returns every top-level name a Go file declares: funcs and
// methods, types, consts, vars, and struct field names.
func declaredNames(t *testing.T, path string) map[string]bool {
	t.Helper()
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, path, nil, parser.SkipObjectResolution)
	if err != nil {
		t.Fatalf("parse %s: %v — the anchor arm cannot report a missing symbol from a file "+
			"it could not read, so this is a failure rather than a skip", path, err)
	}
	names := map[string]bool{}
	for _, d := range f.Decls {
		switch decl := d.(type) {
		case *ast.FuncDecl:
			names[decl.Name.Name] = true
		case *ast.GenDecl:
			for _, spec := range decl.Specs {
				switch s := spec.(type) {
				case *ast.TypeSpec:
					names[s.Name.Name] = true
					if st, ok := s.Type.(*ast.StructType); ok && st.Fields != nil {
						for _, fld := range st.Fields.List {
							for _, n := range fld.Names {
								names[n.Name] = true
							}
						}
					}
				case *ast.ValueSpec:
					for _, n := range s.Names {
						names[n.Name] = true
					}
				}
			}
		}
	}
	return names
}

// ────────────────────────────────── K7 ───────────────────────────────────────

func TestContractCardsMatchTheCorpusResponseKeys(t *testing.T) {
	cards := readCards(t)
	compared := 0
	for name, c := range cards {
		corpusPath := filepath.Join(cardsRepoRoot, cardsCorpusDirRel, name+".json")
		raw, err := os.ReadFile(corpusPath)
		if err != nil {
			if c.block.ResponseKeysObserved != nil {
				t.Errorf("K7 CORPUS_INVENTED: %s lists response_keys_observed but aihub#412's "+
					"corpus holds no record for %s. Those keys came from somewhere this gate "+
					"cannot check; the honest value is null.", c.path, name)
			}
			continue
		}
		var rec struct {
			Keys []string `json:"observed_top_level_keys"`
		}
		if err := json.Unmarshal(raw, &rec); err != nil {
			t.Fatalf("decode %s: %v", corpusPath, err)
		}

		// A record EXISTS, so the card's list may not be null.
		//
		// 🔴 This is the one direction equalStrings below structurally cannot see.
		// It compares lengths, so nil and [] are the same value to it, and the
		// header's claim that "K7 checks both directions" was false for exactly
		// that pair (aihub#473 measured it: a corpus record whose
		// observed_top_level_keys is null accepted a card value of null, while the
		// generator writes []). corpusKeys is the invariant — it maps a record with
		// null keys to [] — so null on a card means "that corpus holds no record
		// for this tool", and the record sitting here contradicts it. Reported
		// instead of the drift below rather than as well: one fault, one error.
		if c.block.ResponseKeysObserved == nil {
			t.Errorf("K7 CORPUS_NULL_MASKS_RECORD: %s carries response_keys_observed=null while "+
				"aihub#412's corpus DOES hold a record for %s. null is the value that means the "+
				"corpus has nothing on this tool; an empty list means it has a record and the "+
				"results carried no top-level keys — the README calls those a different fact, "+
				"and this arm is what makes that true. Regenerate with PF_CARDS_REGEN=1, which "+
				"writes [] here.", c.path, name)
			continue
		}

		want := append([]string(nil), rec.Keys...)
		sort.Strings(want)
		got := append([]string(nil), c.block.ResponseKeysObserved...)
		sort.Strings(got)
		if !equalStrings(got, want) {
			t.Errorf("K7 CORPUS_DRIFT: %s lists response_keys_observed=%v, the corpus record "+
				"says %v. The card copies that file so a reader gets the whole contract in "+
				"one place; regenerate with PF_CARDS_REGEN=1 rather than editing either by "+
				"hand.", c.path, got, want)
		}
		compared++
	}
	if compared < floorCardCorpus {
		t.Errorf("K8 FLOOR_CORPUS: only %d card(s) were compared against a corpus record, floor "+
			"is %d. Every arm above is per-record, so deleting the records and the card lists "+
			"in one change leaves this test comparing nothing and reporting green — which is "+
			"the shape of pass this floor exists to refuse", compared, floorCardCorpus)
	}
	t.Logf("K7: %d cards compared against the aihub#412 corpus records", compared)
}

// ────────────────────────────────── K9 ───────────────────────────────────────
//
// 🔴 Why this arm exists: "regenerate the hash and leave the prose" was the
// cheapest COMPLIANT path through this gate.
//
// The header above argues regeneration is not a silent pass because "the
// regenerated diff has to be committed, so a schema change surfaces as a card diff
// a reviewer reads". Measured on the tree (aihub#473), that backstop is thinner
// than stated: changing pf_remember's base_strength description, running
// PF_CARDS_REGEN=1 and re-running the gate came back GREEN with the card still
// carrying the OLD text verbatim in its hop 0-1 table, plus a paragraph asserting
// the old range had been fixed. The whole diff a reviewer sees for that is one
// line — input_schema_sha256 moving from one 64-hex string to another. Old and new
// are equally unreadable, so "a card diff a reviewer reads" reduces to "a reviewer
// notices a hash moved and independently decides to go re-read prose the diff does
// not show".
//
// K3 fires on the same event and still should: it is the arm that says SOMETHING
// changed. This one says WHAT, by name, and it survives regeneration because it
// reads the prose the generator refuses to touch.

// cardQuotedCell matches a table cell that OPENS with a double-quoted string, and
// captures that leading quote.
//
// Scoped to the START of a cell rather than to "any quoted run in the section",
// because the two forms claim different things and the cards use both. A cell that
// opens with a quote is a claim that the quote is VERBATIM published text, whatever
// the author adds after it. A quote INSIDE a sentence is the author's own words
// about the text, and that is deliberate throughout: pf_get_ready_queue's prose
// quotes the withdrawn `"non-conflicting"`, pf_list_work_items writes ABSENT means
// "no step state", pf_remember writes `fields="brief"` in a code span. Measured
// 2026-09-08: over the whole hop 0-1 section this arm would have reported 6 such
// glosses as drift; scoped this way it checks 28 real quotes and reports none.
//
// 🔴 NOT anchored at the end, and that was a defect while this was being written.
// With a `$` the rule was "the cell is nothing but a quote", which meant appending
// ANY text to the cell removed it from the arm's view entirely — including the
// exemption marker below, so a marked row was silently unchecked rather than
// counted against the ceiling, and the ceiling measured nothing. Probed: the
// marker plus a stale quote came back green. Reading the LEADING quote instead
// makes trailing text a note rather than an escape, and it picked up three more
// genuine quotes that carried a trailing gloss (`"Work item ID" — a slug or a
// canonical id`) and had been invisible for the same reason.
//
// Anchored on the cell delimiter and applied to the whole row, rather than to the
// pieces of a row split on "|". Splitting first would cut a quote containing a
// pipe in half and then report the fragment as drift — and published descriptions
// do carry pipes (`private|project|team|admin`), so that is a false red waiting
// for the first card that quotes one. It also makes the separator row a non-match
// for free, since |---|---| holds no quotes.
var cardQuotedCell = regexp.MustCompile(`\|\s*"([^"\n]+)"`)

// cardHistoricalMarker exempts ONE hop 0-1 table row whose quote is deliberately
// no longer live — a parameter whose published description changed, where the card
// documents what it used to say.
//
// An HTML comment because it cannot occur by accident (zero cards contained one
// when this arm was written), renders as nothing so the table stays readable, and
// is greppable so every exemption in the tree can be COUNTED — which is what
// maxHistoricalQuoteRows then does. Scoped to the row, so exempting one parameter
// does not quietly exempt its neighbours, and falsifiable in both directions: a
// marked row whose quote is still live is reported too, on the K5 reasoning that
// an exemption outliving its gap is one nobody removes.
const cardHistoricalMarker = "<!-- historical -->"

func TestContractCardQuotesAreVerbatim(t *testing.T) {
	published := liveSchemaProse(t)
	cards := readCards(t)

	checked, historical := 0, 0
	for name, c := range cards {
		haystack, ok := published[name]
		if !ok {
			continue // K2 reports it
		}
		hop01, ok := cardSectionBody(cardSections(c.prose), "## hop 0-1")
		if !ok {
			continue // K4 reports it
		}
		for _, row := range strings.Split(hop01, "\n") {
			line := strings.TrimSpace(row)
			if !strings.HasPrefix(line, "|") || !strings.HasSuffix(line, "|") {
				continue
			}
			exempt := strings.Contains(line, cardHistoricalMarker)
			for _, m := range cardQuotedCell.FindAllStringSubmatch(line, -1) {
				quoted := m[1]
				if exempt {
					historical++
					if strings.Contains(haystack, quoted) {
						t.Errorf("K9 HISTORICAL_STILL_LIVE: %s marks a row %s and quotes %q, "+
							"which %s still publishes. The marker says this text is history; "+
							"while it is current the exemption hides a live quote from the arm "+
							"that checks it, and an exemption that outlives its gap is one "+
							"nobody removes. Drop the marker.",
							c.path, cardHistoricalMarker, quoted, name)
					}
					continue
				}
				checked++
				if !strings.Contains(haystack, quoted) {
					t.Errorf("K9 QUOTE_NOT_VERBATIM: %s quotes %q as a whole hop 0-1 table cell, "+
						"and no text %s publishes contains it — not the tool description, not "+
						"any string in the live InputSchema. A whole-cell quote is a claim to be "+
						"verbatim, so this card is telling a caller the tool promises something "+
						"it does not. Re-read the live description and fix the QUOTE; "+
						"regenerating the machine block does not touch it, which is the hole "+
						"this arm closes. If the withdrawn text is being documented on purpose, "+
						"mark that row %s and raise maxHistoricalQuoteRows in the same change.",
						c.path, quoted, name, cardHistoricalMarker)
				}
			}
		}
	}

	if historical > maxHistoricalQuoteRows {
		t.Errorf("K9 HISTORICAL_CEILING: %d quote(s) are exempted by %s, ceiling is %d. The "+
			"marker is an escape hatch and this is the price of using it: raising the ceiling is "+
			"an edit to this file somebody signs, which is the only thing that keeps the hatch "+
			"from being cheaper than reading the schema.",
			historical, cardHistoricalMarker, maxHistoricalQuoteRows)
	}
	if checked < floorCardQuotes {
		t.Errorf("K9 FLOOR_QUOTES: only %d verbatim quote(s) were checked, floor is %d — a card "+
			"set that quotes nothing satisfies this arm by quoting nothing, which is the same "+
			"green as one that quotes correctly", checked, floorCardQuotes)
	}
	t.Logf("K9: %d verbatim hop 0-1 quotes checked against the live schema, %d exempted",
		checked, historical)
}

// ──────────────────────────────── generator ──────────────────────────────────

// TestGenerateContractCards writes the machine block of every card from the live
// contract and the checked-in corpus.
//
// It is a test rather than a cmd/ program because the instrument, the floors and
// the format are all here, and a second binary would be a second place for them
// to be defined. Skipped unless PF_CARDS_REGEN is set, so an ordinary `go test
// ./...` never writes to docs/.
//
// 🔴 It rewrites ONLY the first fenced json block of an existing card. The prose
// is the part no generator can produce, and a generator that could overwrite it
// would eventually be run by somebody who did not mean to.
func TestGenerateContractCards(t *testing.T) {
	if os.Getenv("PF_CARDS_REGEN") == "" {
		t.Skip("set PF_CARDS_REGEN=1 to rewrite docs/mcp-cards machine blocks")
	}
	tools := liveContract(t)
	schemaHashes := liveInputSchemaHashes(t)
	dir := filepath.Join(cardsRepoRoot, cardsDirRel)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", dir, err)
	}

	names := make([]string, 0, len(tools))
	for name := range tools {
		names = append(names, name)
	}
	sort.Strings(names)

	for _, name := range names {
		lt := tools[name]
		blk := cardBlock{
			Tool:                 name,
			DescriptionSHA256:    descriptionSHA(lt.Description),
			InputSchemaSHA256:    schemaHashes[name],
			Params:               lt.Params,
			ResponseKeysObserved: corpusKeys(t, name),
			Hop4Coverage:         "pending",
		}
		path := filepath.Join(dir, name+".md")
		// readErr is kept in its own variable deliberately: it decides whether this
		// card is being CREATED or UPDATED, and reusing `err` for the marshal below
		// silently turned every update into a create in the first draft.
		existing, readErr := os.ReadFile(path)
		if readErr == nil {
			// Keep whatever hop4_coverage the card already declares: it is a
			// statement about the PROSE, which this generator does not touch.
			var prev cardBlock
			if m := cardJSONBlock.FindStringSubmatch(string(existing)); m != nil {
				if json.Unmarshal([]byte(m[1]), &prev) == nil && prev.Hop4Coverage != "" {
					blk.Hop4Coverage = prev.Hop4Coverage
				}
			}
		}
		rendered, err := json.MarshalIndent(blk, "", "  ")
		if err != nil {
			t.Fatalf("marshal %s: %v", name, err)
		}
		fenced := "```json\n" + string(rendered) + "\n```"

		var out string
		if readErr == nil {
			loc := cardJSONBlock.FindStringIndex(string(existing))
			if loc == nil {
				t.Fatalf("%s has no machine block to replace", path)
			}
			out = string(existing[:loc[0]]) + fenced + string(existing[loc[1]:])
		} else {
			out = cardSkeleton(name, fenced)
		}
		if writeErr := os.WriteFile(path, []byte(out), 0o644); writeErr != nil {
			t.Fatalf("write %s: %v", path, writeErr)
		}
	}
	t.Logf("regenerated machine blocks for %d cards", len(names))
}

func cardSkeleton(name, fenced string) string {
	return fmt.Sprintf(`# %s — contract card

%s

## hop 0-1 — what the caller is told

TODO

## hop 2-3 — what leaves this process, and what binds it

TODO

## hop 4 — what it actually does

TODO

## hop 5 — what comes back

TODO

## Policy

TODO

## Open

TODO
`, name, fenced)
}

func corpusKeys(t *testing.T, tool string) []string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(cardsRepoRoot, cardsCorpusDirRel, tool+".json"))
	if err != nil {
		return nil
	}
	var rec struct {
		Keys []string `json:"observed_top_level_keys"`
	}
	if err := json.Unmarshal(raw, &rec); err != nil {
		t.Fatalf("decode corpus record for %s: %v", tool, err)
	}
	if rec.Keys == nil {
		return []string{}
	}
	sort.Strings(rec.Keys)
	return rec.Keys
}

// ───────────────────────────────── helpers ───────────────────────────────────

func descriptionSHA(desc string) string {
	sum := sha256.Sum256([]byte(desc))
	return hex.EncodeToString(sum[:])
}

func short(sha string) string {
	if len(sha) <= 12 {
		return sha
	}
	return sha[:12]
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
