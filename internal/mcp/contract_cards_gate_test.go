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
//	K11 open honesty   an `## Open` bullet asserting an unfalsifiable negative
//	                   ("nobody has made", "is unmeasured", "was not updated by"),
//	                   or naming an aihub#NNN with no date — the arm that reads
//	                   the one section no other arm reads at all; and an
//	                   openCitationWaivers entry whose stated count has drifted
//	                   from what the arm actually waived
//
// The list jumps K9 to K11 because K10 is taken; it is the DB-gated arm and lives
// elsewhere, described next.
//
// K10 is NOT in this file and does not run in the always-on step. It lives in
// card_response_keys_live_e2e_db_test.go, is gated on AIHUB_TEST_DB, and is the
// only arm that reaches a server: it drives 39 of the 45 published tools and
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
	"strconv"
	"strings"
	"testing"

	"github.com/GMISWE/ieops-aihub/internal/citest/cardclaims"
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
	// universal_contract_gate_test.go rather than restating. K1/K2 prints the card
	// and tool counts these bound.
	//
	// ⚠️ Re-derive these, do not adjust them by arithmetic, and do NOT write the
	// derived value into the comments here. Each arm PRINTS the number it
	// measured, so
	//
	//	GOWORK=off go test ./internal/mcp/ -run '^TestContractCard' -count=1 -v
	//
	// is the whole recipe, and each constant below names the arm whose printed
	// line carries its current value.
	//
	// 🔴 Why the values are no longer restated here (aihub#493). They used to be,
	// each one carrying the date it was taken on, and the date stopped nothing:
	// aihub#446 found two stale against the very tree they were written on
	// (floorCardParams claimed 232 where K3 printed 231; floorCardAnchors claimed
	// 207 across 30 files where K6 printed 258 across 38), aihub#483 confirmed
	// five more stale, and re-running the arms one day later — after 21 unrelated
	// PRs — put SEVEN of the nine back out of date. Nothing compared a number in
	// this block against the arm that prints it, so the only thing a restated
	// value could do was be wrong. measured_floor_comment_gate_test.go
	// (TestMeasuredFloorCommentsCarryNoValue) now keeps them out.
	floorCards = 40
	// floorCardParams bounds the total parameter rows the cards pin against the
	// live contract. Current value: the K3 line.
	floorCardParams = 180
	// floorCardAnchors bounds how many file+symbol citations the anchor arm
	// actually resolved. Without it, a card set that cited nothing would pass K6
	// by having nothing to check. Current value: the K6 line.
	floorCardAnchors = 60
	// floorCardSections bounds how many required sections K4 found and measured.
	// The arm quantifies over sections, so a card set the walk could not split
	// into sections would satisfy it by having none. Current value: the K4/K5
	// line.
	floorCardSections = 200
	// floorCardCorpus bounds how many cards K7 compared against a corpus record.
	// Without it, deleting the corpus records AND the card lists together leaves
	// K7 comparing nothing and reporting green. Current value: the K7 line.
	floorCardCorpus = 30
	// floorCardQuotes bounds how many verbatim quotes K9 checked against the live
	// schema. A card set that quoted nothing would pass that arm by quoting
	// nothing. Current value: the K9 line.
	floorCardQuotes = 12
	// floorOpenBullets bounds how many `## Open` bullets K11 read. The arm
	// quantifies over bullets, so a card set whose Open sections were emptied down
	// to K4's 16-character floor would satisfy it by asserting nothing. Current
	// value: the first count on the K11 line.
	floorOpenBullets = 40
	// floorOpenCitations bounds how many of those bullets named a work item, i.e.
	// how many the date half of K11 actually checked. Without it, deleting every
	// citation from every Open section leaves that half comparing nothing and
	// reporting green — and deleting the citation is exactly the cheap way to
	// comply with a rule about citations. Current value: the "naming a work item"
	// count on the K11 line.
	floorOpenCitations = 14
)

// maxPendingCards is a CEILING ON DEBT, not a floor on a measurement, so unlike
// every constant above it IS set at the measured value on purpose — so the
// constant IS the number and there is nothing left to restate in prose. Lowering
// it happens for free as cards get written; raising it has to be a deliberate
// edit somebody signs off on. K4/K5 prints the pending count beside this
// ceiling, which is where to read it from.
const maxPendingCards = 0

// maxHistoricalQuoteRows is the same kind of ceiling for K9's escape hatch: the
// number of hop 0-1 table rows allowed to carry cardHistoricalMarker and quote
// text the live schema no longer publishes. K9 prints the exempted count beside
// the quotes it checked, and this ceiling is the measurement, so it is not
// restated in prose here.
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
// a file: this repo has .go basenames carried by more than one file, main.go by
// several, so a glob would resolve some names to whichever match it hit first.
// The counts are deliberately not written here — they moved twice in two days
// while this comment claimed one pair (see the floor block above and
// measured_floor_comment_gate_test.go). Re-derive with
//
//	git ls-files '*.go' | xargs -n1 basename | sort | uniq -c | awk '$1>1'
//
// An anchor that passes while pointing somewhere the author did not mean is
// worse than one that goes red, and rejecting the form outright costs the author
// one directory name. It is also the rule C1 already enforces against line
// numbers, for the same reason — a citation has to identify exactly one place.
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
				"repo-relative path (`<dir>/%s`): this repo carries .go basenames used by more "+
				"than one file, so a bare name need not identify a file — nothing can check it "+
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

// ─────────────────────────────────── K11 ─────────────────────────────────────
//
// K11 (aihub#483) reads the `## Open` section, which until now no arm read at all.
// The number skips 10 because K10 is taken: it lives in
// card_response_keys_live_e2e_db_test.go and is the DB-gated arm, so reusing the
// number would make two different arms answer to one name in CI output.
//
// ─── The hole was measured, not anticipated ────────────────────────────────
//
// aihub#476 swept every card's `## Open` section against the tree it described.
// **5 of 48 carried a FALSE assertion**; the other 43 were true or correctly
// hedged. Every arm above was GREEN through all five, and not by accident: they
// compare a generated machine block against the live schema, and an Open section
// is prose no machine block covers. K4 requires the section to be non-empty and
// K9 reads quotes out of hop 0-1 — nothing read a sentence here for truth.
//
// The five split into two shapes with DIFFERENT AUTHORITIES, and the obvious
// detector sees only the first:
//
//   - SHAPE 1, one card (pf_update_memory): a negative about a PUBLISHED STRING —
//     "was not updated by aihub#433 and still states no range" — where the schema
//     did carry the range, and had since c069570. Authority is the schema, so this
//     is checkable offline.
//   - SHAPE 2, four cards (pf_list_dependencies, pf_list_projects,
//     pf_update_project, pf_whoami): a negative about a MEASUREMENT — "needs a DB
//     read nobody has made", "is unmeasured" — where the measurement existed, in
//     the cited work item's own attrs. **Authority is the aihub database, not this
//     repo.**
//
// ─── Why this arm is a phrase ban and not a fact-checker ───────────────────
//
// A shape-2 fact-checker has to read wi status and attrs AT GATE TIME, i.e. reach
// a live aihub. Measured rather than assumed: contract-lint.yml sets AIHUB_TEST_DB
// zero times, this file touches no DB or network, and ci.yml's always-on `Unit
// tests` step deliberately does not set it — per its aihub#303 comment block the
// real DB coverage is exactly the union of the `-run` regexes on the steps that DO,
// with a manifest that reddens on an unlisted DB-gated test. So shape 2 done
// properly costs a new AIHUB_TEST_DB-gated CI step plus a manifest entry.
// aihub#476 recommended against paying that here and aihub#483 carried the
// recommendation; **it is NOT built, and this comment is the record that it is
// not.** Whoever wants it should note that a live read makes the gate's colour
// depend on a database, which no other arm in this file does.
//
// What IS built is the cheap thing that covers BOTH shapes without prose parsing:
// ban the FORM all five took. **All five were undated absolutes** — a claim about
// the whole world, at no particular time, that no reader can re-check. The two
// halves below are independent, and neither is a fact-checker: they refuse the
// form in which a false claim is unfalsifiable, which is a strictly weaker and
// strictly cheaper thing than refusing a false claim.
//
// ─── A measurement that corrects aihub#476's own proposal ──────────────────
//
// Its detector was scoped to "an Open bullet that CITES an aihub#NNN". Re-measured
// against the five historical bullets in cf9f7e1's parent: **only 1 of the 5 cited
// a work item in the bullet itself.** pf_list_dependencies, pf_list_projects,
// pf_update_project and pf_whoami all cited `§6.4 item 1` and named no wi at all.
// So the citation scope would have caught 1 of 5, and the DATE requirement — which
// only fires on a citation — would have caught 0 of 5. The phrase ban is therefore
// unscoped, and it catches 5 of 5. The date half is kept anyway, for the different
// and smaller reason stated on it.
var openUnfalsifiableNegatives = []struct {
	name string
	re   *regexp.Regexp
	seen string
}{
	{
		name: "nobody has <verb>",
		// Present perfect specifically, because that is the universal negative: not
		// "this line did not do it" but "no one, anywhere, ever has". Measured
		// 2026-09-08: the cards use "nobody" 8 times across 6 sentences — "Nobody
		// should tidy", "a step nobody completed", "a guard nobody can find … is a
		// guard nobody passes" (two cards) and "nobody noticed" (two cards) — and not
		// one is in this tense, so the narrow form costs no false red. The looser
		// "nobody <verb>ed" would have reddened 3 of those 8, all true.
		re:   regexp.MustCompile(`(?i)\bno(?:body|[ -]one)\s+has\b`),
		seen: "3 of the 5: pf_list_dependencies, pf_update_project and pf_whoami all wrote \"needs a DB read nobody has made\"",
	},
	{
		name: "is unmeasured",
		// Adjacent to the copula only. pf_whoami's CORRECTED bullet says "the urgency
		// is therefore no longer unmeasured", which is true and must stay green, so
		// banning the bare word would red the very card this arm was built from.
		re:   regexp.MustCompile(`(?i)\b(?:is|are|was|were|remains?|stays?|still)\s+unmeasured\b`),
		seen: "2 of the 5: pf_list_projects wrote \"whether any live row holds `maintainer` is unmeasured\" and pf_whoami \"the urgency is unmeasured\"",
	},
	{
		name: "was not updated by",
		re:   regexp.MustCompile(`(?i)\b(?:was|were|is|are|has|have|had)\s+(?:been\s+)?not\s+(?:been\s+)?updated\s+by\b`),
		seen: "1 of the 5, and the only SHAPE 1 instance: pf_update_memory wrote \"was not updated by `aihub#433` and still states no range\"",
	},
}

// A family this list deliberately does NOT carry, named here rather than left as a
// silent gap: "<subject> was never read / made / measured". It is the same
// universal negative — docs/mcp-cards/pf_remember.md's `## Open` carries "the live
// distinct `type` set was never read", which is shape 2 exactly. It is left out
// because measured on this tree the pattern's precision is one in two:
// docs/mcp-cards/pf_recall.md says "the knob was never read", a claim about what
// the CODE does that is offline-checkable and true. A pattern that reds a true
// sentence as often as a false one teaches the next author to route around the arm
// instead of to write a checkable claim, which is the failure mode
// maxHistoricalQuoteRows exists to prevent one level up. Separating the two needs
// the subject, i.e. prose parsing, which is what this arm is built to avoid.

// openWorkItemRef matches a work-item citation. Measured 2026-09-08: every `x#N`
// form in the card set is `aihub#N` (58 distinct), so a wider pattern would buy
// nothing and could match a PR or issue reference, which is a different claim.
var openWorkItemRef = regexp.MustCompile(`\baihub#\d+\b`)

// openISODate matches a real ISO calendar date. Month and day are range-checked so
// the requirement cannot be satisfied by something date-SHAPED — a version, a
// dotted identifier, a hash prefix — which an unchecked `\d\d-\d\d` would accept.
var openISODate = regexp.MustCompile(`\b20\d{2}-(?:0[1-9]|1[0-2])-(?:0[1-9]|[12]\d|3[01])\b`)

// openCitationWaivers exempts a card from the DATE half only — never from the
// phrase ban — and every entry states who holds the file, when it can go, and
// HOW MANY undated citations it covers.
//
// 🔴 This is the K5/K9 shape: an exemption that is NAMED, carries its reason, and
// is falsifiable in both directions. A waived card that turns out to have NO
// undated citation is reported as a stale waiver, so the entry deletes itself by
// going red the moment the gap it covers closes — which is a stronger property
// than maxHistoricalQuoteRows has, and it is available here only because the
// waiver names a card rather than counting rows.
//
// 🔴 The reason MUST state a count, and aihub#494 measured why on 2026-09-09. A
// waiver names a CARD; the thing it excuses is a CITATION, and one card can hold
// several. The single entry this map has ever held read "its one undated citation
// is `aihub#459`" while docs/mcp-cards/pf_remember.md carried TWO undated bullets,
// and the arm logged "2 undated citation(s) waived" directly beneath a reason
// saying one. The second bullet was exempt under a sentence that never mentioned
// it, and the self-emptying property went with it: undatedHere could not reach 0
// while the unmentioned bullet stayed undated, so STALE_WAIVER could not fire
// however completely the gap the reason DESCRIBED had closed. The count is
// therefore checked against this arm's own tally, and a reason stating none is
// itself a failure — without that half, omitting the count is the cheapest way to
// comply with a rule about counts, which is the maxHistoricalQuoteRows argument
// applied to prose instead of to a ceiling.
//
// 🟢 EMPTY since aihub#459 (2026-09-09), and that is the arm working as designed
// rather than an absence of need. The single entry waived
// docs/mcp-cards/pf_remember.md because aihub#445 held that file with a live
// attempt while its Open bullets went undated. aihub#445 is now `wrapped` (closed
// 2026-09-08), the file is free, and both of that card's Open citations have been
// dated — one re-checked, one deleted because aihub#459 landed the ruling it was
// waiting for. The exemption's own reason said "delete this entry once #445 lands;
// the arm will already be telling you to", and it was: STALE_WAIVER fires the
// moment the gap closes, which is what makes this map self-emptying rather than a
// list that accumulates.
var openCitationWaivers = map[string]string{}

// openWaiverNumberWords is the spelled-out half of a waiver's count claim.
//
// It stops at ten on purpose. A card holding more than ten undated citations is a
// card to fix rather than one to waive, and past that range the digit is the
// clearer way to write it anyway — so this covers the forms a reason actually uses
// without turning into a general English number parser.
var openWaiverNumberWords = map[string]int{
	"no": 0, "zero": 0, "one": 1, "two": 2, "three": 3, "four": 4, "five": 5,
	"six": 6, "seven": 7, "eight": 8, "nine": 9, "ten": 10,
}

// openWaiverCountClaim matches the count a waiver's reason states about the card it
// covers: "its one undated citation is `aihub#459`", "2 undated citations", "no
// undated citation".
//
// 🔴 Anchored on the NOUN and not on a bare number, for the reason openISODate
// range-checks its months rather than accepting anything date-shaped. A waiver
// reason is dense with digits that are not counts — `aihub#445`, `aihub#483` and an
// ISO date all appear in the only entry this map has held — and a pattern reading
// one of those as the claim would compare this arm's tally against a work-item
// number and report a mismatch that means nothing.
var openWaiverCountClaim = regexp.MustCompile(
	`(?i)\b(no|zero|one|two|three|four|five|six|seven|eight|nine|ten|\d{1,3})\s+undated\s+citation`)

// openWaiverClaimedCounts returns the DISTINCT counts a reason states, in order of
// first appearance. None means the reason makes no count claim at all; more than
// one means it makes claims that disagree with each other. In neither case is there
// a single number for the arm to check, and the two are reported separately because
// the edit that fixes them differs.
func openWaiverClaimedCounts(reason string) []int {
	var counts []int
	seen := map[int]bool{}
	for _, m := range openWaiverCountClaim.FindAllStringSubmatch(reason, -1) {
		tok := strings.ToLower(m[1])
		n, ok := openWaiverNumberWords[tok]
		if !ok {
			parsed, err := strconv.Atoi(tok)
			if err != nil {
				// Unreachable: the pattern admits only the words above and \d{1,3}.
				continue
			}
			n = parsed
		}
		if seen[n] {
			continue
		}
		seen[n] = true
		counts = append(counts, n)
	}
	return counts
}

// openWaiverProblems is the whole waiver half of K11 for ONE card, returned as
// plain strings.
//
// 🔴 It is a function rather than inline arm code because openCitationWaivers is
// EMPTY on a healthy tree — which is the arm working as designed, and also means
// every branch below is unreachable from the card set. An arm whose triggers only
// run on the day somebody adds an exemption is an arm nobody finds out is broken
// until the day they rely on it, so the triggers are exercised against fixtures in
// TestOpenCitationWaiverIsCheckedAgainstItsOwnCount instead. That is not a
// hypothetical: the count check exists because the one entry this map ever held
// carried a count that was wrong for its entire life and nothing looked at it.
func openWaiverProblems(cardPath, reason string, undatedHere int) []string {
	var problems []string
	claims := openWaiverClaimedCounts(reason)

	switch {
	case undatedHere == 0:
		problems = append(problems, fmt.Sprintf(
			"K11 STALE_WAIVER: openCitationWaivers exempts %s from the date requirement, "+
				"and that card now has no undated citation. The exemption has outlived its "+
				"gap, which is the K5 failure in another place — delete the entry. Its "+
				"recorded reason was: %s", cardPath, reason))
	case len(claims) == 1 && claims[0] != undatedHere:
		problems = append(problems, fmt.Sprintf(
			"K11 STALE_WAIVER: openCitationWaivers exempts %s from the date requirement "+
				"and its reason claims %d undated citation(s), but this arm waived %d. A "+
				"waiver names a CARD while what it excuses is a CITATION, so the two numbers "+
				"drifting apart is not bookkeeping. Waiving MORE than the reason claims means "+
				"bullets are exempt under a sentence that never mentions them, and while any "+
				"of those stays undated the tally cannot reach 0, so the self-emptying arm "+
				"above can never fire either — that is the case aihub#494 measured on "+
				"2026-09-09. Waiving FEWER means the reason describes a gap that is already "+
				"part-closed. Either way, re-read the card and rewrite the entry against what "+
				"is actually there. Its recorded reason was: %s",
			cardPath, claims[0], undatedHere, reason))
	}

	switch {
	case len(claims) == 0:
		problems = append(problems, fmt.Sprintf(
			"K11 WAIVER_NO_COUNT: openCitationWaivers exempts %s with a reason that states "+
				"no count of the undated citations it covers, so there is nothing to compare "+
				"against the %d this arm waived. Say it in the form the arm logs, e.g. \"its "+
				"one undated citation is `aihub#NNN`\" or \"2 undated citations\". The count "+
				"is required rather than encouraged because it is the only thing keeping a "+
				"card-scoped exemption from silently covering a citation nobody signed off "+
				"on, and an optional one is dodged by leaving the count out. Its recorded "+
				"reason was: %s", cardPath, undatedHere, reason))
	case len(claims) > 1:
		problems = append(problems, fmt.Sprintf(
			"K11 WAIVER_COUNT_AMBIGUOUS: openCitationWaivers exempts %s with a reason "+
				"stating %d counts that disagree (%v) of the undated citations it covers, so "+
				"there is no single claim to check against the %d this arm waived. Picking "+
				"the first would make the check depend on sentence order, which is not "+
				"something a reader would predict — state the count once. Its recorded reason "+
				"was: %s", cardPath, len(claims), claims, undatedHere, reason))
	}

	return problems
}

// TestContractCardOpenSectionsAreFalsifiable is K11.
//
// It asserts nothing about whether an Open bullet is TRUE. It asserts that a bullet
// is written in a form somebody could later find false: no universal negative about
// what has been measured anywhere, and a date on any claim about a work item, whose
// state lives in a database this gate cannot read.
//
// ⚠️ The date does not make the claim true, and the arm cannot tell a considered
// as-of from one copied off the line above. What it removes is the UNDATED
// ABSOLUTE, which is the form all five false bullets took: "needs a DB read nobody
// has made" has no as-of, so a reader in six months cannot tell a claim that was
// wrong when written from one that has merely aged. Compare the compliant form the
// card set already uses — pf_emit_event's "needs a DB read this line could not
// make", which scopes the negative to the author instead of the world, and
// pf_list_dependencies' "a live-DB read dated 2026-09-08, which dates rather than
// pins". Both survive this arm, and both say what to re-run.
func TestContractCardOpenSectionsAreFalsifiable(t *testing.T) {
	cards := readCards(t)

	names := make([]string, 0, len(cards))
	for name := range cards {
		names = append(names, name)
	}
	sort.Strings(names)

	bullets, cited, waived := 0, 0, 0
	for _, name := range names {
		c := cards[name]
		openBody, ok := cardSectionBody(cardSections(c.prose), "## Open")
		if !ok {
			continue // K4 reports a missing section
		}
		reason, isWaived := openCitationWaivers[name]
		undatedHere := 0

		for _, bullet := range cardOpenBullets(openBody) {
			// 🔴 aihub#543: strip HTML comments before every check below. cardOpenBullets
			// joins a bullet's lines verbatim, so an in-card classification marker — which
			// renders as nothing — became part of the text K11 reads, and its own date and
			// aihub#NNN then satisfied the date requirement for a VISIBLE sentence that
			// carried neither. Measured on pf_predict_conflicts: deleting every visible
			// date from a bullet left this arm green because the marker still carried one.
			// A reader of the rendered card sees only the prose, so that is exactly the
			// undated absolute this arm exists to refuse.
			bullet = cardStripHTMLComments(bullet)
			bullets++
			for _, p := range openUnfalsifiableNegatives {
				hit := p.re.FindString(bullet)
				if hit == "" {
					continue
				}
				t.Errorf("K11 UNFALSIFIABLE_NEGATIVE: %s says %q in its `## Open` section:\n"+
					"    %s\n"+
					"That is the %q form, and it is what aihub#476 found FALSE on %s. A "+
					"negative about what anyone has ever measured cannot be checked from this "+
					"repo, so it reads as green forever whether or not it is true — and there "+
					"is no date escape for it, because timestamping a claim about the whole "+
					"world still leaves nothing to re-run. Say instead who would hold the "+
					"answer and when you looked: \"not recorded in `aihub#NNN`'s attrs as of "+
					"2026-09-08\", or scope it to yourself the way pf_emit_event does with "+
					"\"a DB read this line could not make\".",
					c.path, hit, truncateBullet(bullet), p.name, p.seen)
			}

			if !openWorkItemRef.MatchString(bullet) {
				continue
			}
			cited++
			if openISODate.MatchString(bullet) {
				continue
			}
			undatedHere++
			if isWaived {
				waived++
				continue
			}
			t.Errorf("K11 UNDATED_CITATION: %s has an `## Open` bullet naming %s and carrying "+
				"no date:\n    %s\n"+
				"An Open bullet that names a work item is making a claim about that work "+
				"item's STATE — it is open, it decided X, it left Y behind — and that state "+
				"lives in the aihub database, which no arm in this file can read. Undated, "+
				"the claim is true on the day it is written and silently wrong afterwards, "+
				"which is how 5 of 48 cards came to assert defects that were already fixed. "+
				"Add the date you last checked the cited item, e.g. \"still open (`paused`) "+
				"at the last re-check, 2026-09-08\" or \"wrapped 2026-09-07; re-checked "+
				"2026-09-08\".",
				c.path, strings.Join(openWorkItemRef.FindAllString(bullet, -1), ", "),
				truncateBullet(bullet))
		}

		if isWaived {
			for _, problem := range openWaiverProblems(c.path, reason, undatedHere) {
				t.Error(problem)
			}
		}
	}

	for name := range openCitationWaivers {
		if _, ok := cards[name]; !ok {
			t.Errorf("K11 WAIVER_ORPHAN: openCitationWaivers names %q, which is not a card "+
				"under %s. A waiver for a file that does not exist exempts nothing and hides "+
				"that the exemption was never removed.", name, cardsDirRel)
		}
	}

	if bullets < floorOpenBullets {
		t.Errorf("K11 FLOOR_OPEN_BULLETS: only %d `## Open` bullet(s) were read, floor is %d "+
			"— a card set whose Open sections say nothing passes this arm by saying nothing, "+
			"which is the same green as one that says something checkable",
			bullets, floorOpenBullets)
	}
	if cited < floorOpenCitations {
		t.Errorf("K11 FLOOR_OPEN_CITATIONS: only %d `## Open` bullet(s) named a work item, "+
			"floor is %d — deleting the citation is the cheapest way to satisfy a rule about "+
			"citations, and this is what makes that cost an edit to this file",
			cited, floorOpenCitations)
	}
	t.Logf("K11: %d `## Open` bullets read across %d cards, %d naming a work item, %d "+
		"undated citation(s) waived", bullets, len(cards), cited, waived)
}

// cardOpenBullets splits an `## Open` section body into its top-level `- ` items,
// each returned as ONE LOGICAL LINE.
//
// Joined rather than kept as lines because a markdown line break is a rendering
// artifact: "needs a DB read nobody has made" wraps differently in each of the
// three cards that carried it, and a per-line scan would see the phrase in one and
// miss it in another for no reason a reader could predict.
//
// Nothing in the section escapes the scan. Text that is not under any bullet joins
// the nearest one instead of being skipped — a false claim moved out of a list is
// the same false claim — and fenced content is joined too. The fence is tracked for
// exactly one purpose, so that a `- ` inside a code sample does not start a new
// bullet and split a claim in half; it is not an exemption. Measured 2026-09-08: no
// card's Open section contains a fence, so this costs nothing today and closes the
// route on the first one that does.
func cardOpenBullets(body string) []string {
	var out []string
	var cur []string
	inFence := false
	flush := func() {
		if len(cur) > 0 {
			out = append(out, strings.Join(cur, " "))
			cur = nil
		}
	}
	for _, l := range strings.Split(body, "\n") {
		trimmed := strings.TrimSpace(l)
		if strings.HasPrefix(trimmed, "```") || strings.HasPrefix(trimmed, "~~~") {
			inFence = !inFence
		} else if !inFence && strings.HasPrefix(l, "- ") {
			flush()
		}
		if trimmed != "" {
			cur = append(cur, trimmed)
		}
	}
	flush()
	return out
}

// cardStripHTMLComments removes every HTML comment from a bullet, so an arm that
// checks what a READER can see is not satisfied by text the reader cannot.
// Deliberately unanchored: any comment, not only this repo's markers, because the
// property is "renders as nothing", not "is one of ours".
func cardStripHTMLComments(s string) string {
	return strings.TrimSpace(cardHTMLComment.ReplaceAllString(s, " "))
}

var cardHTMLComment = regexp.MustCompile(`(?s)<!--.*?-->`)

// truncateBullet keeps a K11 failure message readable when the offending bullet is
// one of the long ones — pf_update_work_item's run past 1,000 characters. The head
// is enough to find the bullet in the file, which is what the message is for.
func truncateBullet(b string) string {
	const max = 160
	// Sliced by RUNE, not by byte. Card prose is full of em dashes and § signs, so a
	// byte slice at a fixed offset lands mid-rune often enough to matter, and the
	// replacement character it produces would appear in the very message somebody is
	// reading to find the sentence.
	r := []rune(b)
	if len(r) <= max {
		return b
	}
	return string(r[:max]) + " […]"
}

// TestOpenCitationWaiverIsCheckedAgainstItsOwnCount drives the waiver half of K11
// directly, against fixtures rather than against the card set.
//
// 🔴 It has to. openCitationWaivers is EMPTY on a healthy tree, so running the arm
// over docs/mcp-cards/ executes none of those branches at all — the arm is green
// there whether the checks work or not, which is the exact shape the floors in this
// file exist to refuse one level up.
//
// The last two cases are the historical entry verbatim. That entry claimed "one
// undated citation" while docs/mcp-cards/pf_remember.md carried two, and the arm
// logged "2 undated citation(s) waived" beneath it for the entry's whole life with
// nothing red. Pinning the regression to the reason's own TEXT rather than to a
// paraphrase is deliberate: a paraphrase is written after the fix, by someone who
// already knows what the check looks for, so it cannot show the check would have
// caught the thing that actually happened.
func TestOpenCitationWaiverIsCheckedAgainstItsOwnCount(t *testing.T) {
	const historicalReason = "aihub#445 holds docs/mcp-cards/pf_remember.md with a live attempt " +
		"(status `running`, checked 2026-09-08) and that card's first Open bullet IS " +
		"#445's subject — §6.4 item 4, the memory-type CHECK. Editing it from aihub#483 " +
		"would take the file lock out from under a rebase in flight. Its one undated " +
		"citation is `aihub#459`, which was still `queued` at that check, so the bullet " +
		"is TRUE — only undated. Delete this entry once #445 lands; the arm will already " +
		"be telling you to."

	cases := []struct {
		name string
		// reason is the string a waiver entry would carry.
		reason string
		// undated is what the arm tallied for that card.
		undated int
		// want is the failure NAME of each expected problem, in order. Names rather
		// than whole messages so rewording a message does not red this test, while
		// dropping or confusing a check still does.
		want []string
		// contains pins the load-bearing values inside the message, which a name
		// alone cannot: a mismatch report naming the wrong two numbers is useless
		// and would otherwise pass.
		contains []string
	}{
		{
			name:    "count matches the tally, spelled out",
			reason:  "aihub#500 holds the file, checked 2026-09-09; its two undated citations are `aihub#1` and `aihub#2`.",
			undated: 2,
		},
		{
			name:    "count matches the tally, as a digit",
			reason:  "aihub#500 holds the file, checked 2026-09-09; 2 undated citations, both `queued`.",
			undated: 2,
		},
		{
			name:     "waives more than the reason claims",
			reason:   "aihub#500 holds the file, checked 2026-09-09; its one undated citation is `aihub#1`.",
			undated:  3,
			want:     []string{"K11 STALE_WAIVER"},
			contains: []string{"claims 1 undated citation(s)", "waived 3"},
		},
		{
			name:     "waives fewer than the reason claims",
			reason:   "aihub#500 holds the file, checked 2026-09-09; its three undated citations are all `queued`.",
			undated:  1,
			want:     []string{"K11 STALE_WAIVER"},
			contains: []string{"claims 3 undated citation(s)", "waived 1"},
		},
		{
			name:     "a reason claiming none, over a card that has some",
			reason:   "aihub#500 holds the file, checked 2026-09-09; there are no undated citations left.",
			undated:  2,
			want:     []string{"K11 STALE_WAIVER"},
			contains: []string{"claims 0 undated citation(s)", "waived 2"},
		},
		{
			// The pre-existing half, kept under test because the new branches sit in
			// the same switch and could shadow it.
			name:     "the gap has closed",
			reason:   "aihub#500 holds the file, checked 2026-09-09; its one undated citation is `aihub#1`.",
			undated:  0,
			want:     []string{"K11 STALE_WAIVER"},
			contains: []string{"now has no undated citation"},
		},
		{
			// A correct count of zero is still an exemption that exempts nothing, and
			// the gap-closed branch must win rather than the count agreeing its way to
			// silence.
			name:     "claims none and covers none",
			reason:   "aihub#500 holds the file, checked 2026-09-09; no undated citation remains.",
			undated:  0,
			want:     []string{"K11 STALE_WAIVER"},
			contains: []string{"now has no undated citation"},
		},
		{
			name:     "no count stated at all",
			reason:   "aihub#500 holds docs/mcp-cards/pf_recall.md with a live attempt, checked 2026-09-09. Delete this entry once it lands.",
			undated:  1,
			want:     []string{"K11 WAIVER_NO_COUNT"},
			contains: []string{"the 1 this arm waived"},
		},
		{
			name:     "no count stated, and the gap has closed",
			reason:   "aihub#500 holds the file with a live attempt, checked 2026-09-09.",
			undated:  0,
			want:     []string{"K11 STALE_WAIVER", "K11 WAIVER_NO_COUNT"},
			contains: []string{"now has no undated citation"},
		},
		{
			// Repeating the SAME count is ordinary English, not a contradiction — a
			// reason routinely names the gap and then says when it closes. Without
			// this case the ambiguity check reddens a correct waiver, which is the
			// expensive kind of false red: it teaches the next author that the count
			// rule is something to route around.
			name:    "the same count stated twice",
			reason:  "aihub#500 holds the file, checked 2026-09-09; its one undated citation is `aihub#1`, so delete this entry once that one undated citation is dated.",
			undated: 1,
		},
		{
			name:     "two counts that disagree",
			reason:   "aihub#500 holds the file, checked 2026-09-09; its one undated citation is `aihub#1`, and the other 2 undated citations are `aihub#2` and `aihub#3`.",
			undated:  3,
			want:     []string{"K11 WAIVER_COUNT_AMBIGUOUS"},
			contains: []string{"[1 2]", "the 3 this arm waived"},
		},
		{
			// Proves the pattern reads the NOUN and not the digits. Every number in
			// this reason except the count is a work-item reference or a date, and a
			// bare-number pattern would take `aihub#445` as the claim.
			name:    "work-item numbers and dates are not counts",
			reason:  "aihub#445 held it and aihub#483 wrote this entry, re-checked 2026-09-09; its 2 undated citations are `aihub#459` and `aihub#476`.",
			undated: 2,
		},
		{
			name:     "the historical entry, against the card it was written for",
			reason:   historicalReason,
			undated:  2,
			want:     []string{"K11 STALE_WAIVER"},
			contains: []string{"claims 1 undated citation(s)", "waived 2"},
		},
		{
			// The same entry against the card it DESCRIBED. Its count was right about
			// one bullet; what it never mentioned was the second one. Without this
			// case a check that reddened every waiver unconditionally would pass the
			// case above and look like a working count check.
			name:    "the historical entry, against the card its reason described",
			reason:  historicalReason,
			undated: 1,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := openWaiverProblems("docs/mcp-cards/fixture.md", tc.reason, tc.undated)

			names := make([]string, 0, len(got))
			for _, g := range got {
				name, _, ok := strings.Cut(g, ":")
				if !ok {
					t.Fatalf("problem carries no `NAME:` prefix, so no reader can tell "+
						"which check produced it:\n    %s", g)
				}
				names = append(names, name)
			}
			if !equalStrings(names, tc.want) {
				t.Errorf("problem names = %v, want %v\nfull output:\n%s",
					names, tc.want, strings.Join(got, "\n\n"))
			}

			joined := strings.Join(got, "\n")
			for _, want := range tc.contains {
				if !strings.Contains(joined, want) {
					t.Errorf("message does not contain %q, so it does not tell the reader "+
						"what it measured:\n%s", want, joined)
				}
			}
		})
	}
}

// TestOpenCitationWaiverCheckIsWiredIntoTheArm pins the one thing the fixture
// table above structurally cannot.
//
// 🔴 openCitationWaivers is empty, so deleting the openWaiverProblems call from
// TestContractCardOpenSectionsAreFalsifiable changes NOTHING that runs: the arm
// still passes over every card, the fixtures still pass against the function, and
// the waiver checks are simply never reached by the gate. That is a live-looking
// green over a disconnected check — the failure this file's floors refuse for
// counts, applied to a call site. Reading the source is the only instrument
// available here, because the branch it guards cannot be entered from a card set
// that waives nothing.
func TestOpenCitationWaiverCheckIsWiredIntoTheArm(t *testing.T) {
	const (
		gateFile = "contract_cards_gate_test.go"
		armName  = "TestContractCardOpenSectionsAreFalsifiable"
		callName = "openWaiverProblems"
	)

	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, gateFile, nil, parser.SkipObjectResolution)
	if err != nil {
		t.Fatalf("parse %s: %v — this arm cannot report a missing call site from a file "+
			"it could not read, so this is a failure rather than a skip", gateFile, err)
	}

	var arm *ast.FuncDecl
	for _, d := range f.Decls {
		if fn, ok := d.(*ast.FuncDecl); ok && fn.Recv == nil && fn.Name.Name == armName {
			arm = fn
			break
		}
	}
	if arm == nil {
		t.Fatalf("%s declares no func %s. If K11's arm was renamed, rename it here too — "+
			"a wiring check that cannot find the thing it checks reports green forever",
			gateFile, armName)
	}

	called := false
	ast.Inspect(arm.Body, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		if id, ok := call.Fun.(*ast.Ident); ok && id.Name == callName {
			called = true
		}
		return !called
	})
	if !called {
		t.Errorf("%s does not call %s, so every waiver check is dead code from the gate's "+
			"side: openCitationWaivers is empty, so nothing about that shows up as a "+
			"failing card, and the fixtures in the test above go on passing against a "+
			"function the arm no longer consults. Restore the call.", armName, callName)
	}
}

// ─────────────────────────────────── K12 ─────────────────────────────────────
//
// K12 (aihub#543) is the RATCHET. It reads every prose sentence of all 45
// contract cards, decides by form which of them assert something a test could
// hold, and refuses new debt: an assertable sentence that neither cites its arm
// nor carries a named classification marker makes the recorded count rise, and a
// rise is red.
//
// ─── The hole it closes, and why a probe family alone would not ────────────
//
// aihub#543 was filed on a measured gap: structural gates cannot check
// "behaviour = description". The 2026-09-08 wave produced 13 false card
// statements plus aihub#511's behavioural regression, and the 2026-09-09
// clear-batch produced 21 more findings — 30+ in two days, every one caught by a
// human re-reading prose and NONE by a gate. K1-K11 were green through all of
// them, and not by accident: they compare a generated machine block against the
// live schema, and a false sentence is prose no machine block covers.
//
// The counter-shape is a PROBE — one published claim encoded as an executable
// assertion — and the tree already holds a dozen. But the cards hold on the order
// of a thousand and a half prose units, of which this recogniser calls the larger
// part candidate-assertable. A plan that proposes hundreds of probes is a plan
// that does not finish, which is why the ACCOUNT of them ships first and the
// probes draw the account down.
//
// ⚠️ Do not read that against the 995 in aihub#543 §0.1. This walk is deliberately
// NOT that sizer: §1.3 says the sizer "counts a bullet list as fewer units than a
// reader would … a population sizer, not the unit definition", and an early draft
// of this arm reproduced it exactly and inherited the defect — one citation
// retired every claim merged into its unit, and a "used to" anywhere in a merged
// run ejected the live claims either side of it. Bullets are hard boundaries here,
// so the population is larger and the two numbers are not comparable. The K12 line
// prints what this walk measures; take it from there.
//
// 🔴 So this arm ships FIRST and ALONE, ahead of every probe wave — the owner's
// Q7 ruling of 2026-09-10. Built second, a wave lands ~50 probes and the other
// ~450 claims stay exactly as invisible as they are today, at a cost of two
// waves. Built first, every one of them is COUNTED from the day it lands, the
// count only moves in a diff somebody signs, and the probes then draw it down.
//
// ─── Phase 2: the roster is the whole card set ─────────────────────────────
//
// Wave 1 (aihub#566/#567/#568, 2026-09-10) drew the ten phase-1 cards to ZERO
// unclassified in a day, which met the owner's recorded continue-(a) criterion, so
// phase 2 widened k12Cards from ten cards to all 45 (aihub#572). What that cost
// and bought, measured on this tree:
//
//	                        10 scoped   45 scoped
//	sentences read                 353        1414
//	candidate-assertable           147         626
//	citing an arm                  125         152
//	unclassified                     0         452
//
// 🔴 Those 452 are the phase-2 backlog, entered at their MEASURED values in one
// change and nothing else: no probe, no marker, no card sentence edited. Filing
// them is what makes them countable, and from here the number can only move in a
// diff somebody signs. 12 of the 35 newly scoped cards already cited a resolvable
// arm before anyone probed them (27 sentences), which is why the rows carry Cited
// rather than assuming zero.
//
// ⚠️ The 626 is not 45/10 × 147. Two things pull against each other: the newly
// scoped cards are slightly thinner on average (13.7 candidates each against phase
// 1's 14.7), yet the three WIDEST cards in the repo are all among them —
// pf_update_work_item 45, pf_remember 41, pf_recall 34, against phase 1's widest at
// 23 — because spec §2.3 held width back behind the harness argument. Note the
// ranking: §2.3 calls pf_recall and pf_update_work_item "the two widest cards in
// the repo", which is the §0.1 SIZER's ordering, and by this walk pf_remember is
// second. §9.2 says the two counts are not comparable; this is what that looks
// like when someone orders a work queue by it.
//
// ─── What "classified" means, and where the classification lives ───────────
//
// Q4, ruled 2026-09-10: IN-CARD markers, with only the numbers in Go. A
// per-sentence ledger in Go would be a partial second copy of the cards' prose,
// whose only failure mode is disagreeing with the original — the same argument
// the cards' own README makes for storing description_sha256 rather than the
// description. An inline HTML comment is the shape K9's cardHistoricalMarker
// already uses: it cannot occur by accident, it renders as nothing, and it is
// greppable, which is what lets every classification in the tree be counted.
//
//	assertable + probed    the sentence CITES ITS ARM in its own prose — a
//	                       backticked *_test.go path or Test… symbol, both of
//	                       which K6 already resolves. No marker, no ledger row.
//	assertable + unprobed  <!-- probe-waiver: kind=… | decided=… | citation=… |
//	                       reason=… -->. THIS IS DEBT.
//	not assertable         <!-- prose-only: because=… -->, from a closed
//	                       six-value vocabulary.
//	anything else          UNCLASSIFIED — the grandfathering escape, held at its
//	                       measured value by the ledger below.
//
// ─── Why the ledger is an equality and not a ceiling ───────────────────────
//
// maxPendingCards above is a one-sided ceiling and this deliberately is not. The
// difference is what the number counts. A pending CARD is a file somebody has not
// written yet, and its count falls as a side effect of unrelated work, so
// requiring the constant to track it would red the gate on somebody else's diff.
// These counts move only when somebody edits a card sentence or lands a probe —
// and a number that moves only on purpose can be required to be exact. That buys
// the second direction: a gap that CLOSES has to be signed too, because a row
// left high is a vacated slot the next unclassified sentence takes silently.
//
// ⚠️ "K12 is green" is not on its own evidence of anything, which is why this arm
// prints its floors. An empty k12Cards set is green, and so is a recogniser that
// has stopped recognising. Read the K12 line, not the exit code — the aihub#493
// distinction.
//
//	GOWORK=off go test ./internal/mcp/ -run TestContractCardClaims -count=1 -v
//
// ─── Recorded mutants ──────────────────────────────────────────────────────
//
// Every one was applied to this tree and the verdict below is what RAN, not what
// was expected. Both directions are covered, because one direction is not a gate:
// a publication-side mutant edits a CARD and leaves the gate alone, an
// enforcement-side mutant edits the GATE and leaves the cards alone. M10 and M17b
// are controls — a mutant that must stay GREEN — and they are the reason the rest
// mean anything.
//
//	── publication side ──
//	M1  add an assertable sentence with neither a cited arm nor a marker
//	                                             RED  K12 DEBT_GROWTH
//	M2  delete the one waiver marker in the tree RED  K12 DEBT_GROWTH
//	M6  move that marker onto a table row        RED  K12 MARKER_ORPHAN + DEBT_GROWTH
//	M7  file a marker on a card outside the set  RED  K12 MARKER_OUT_OF_SCOPE
//	    ⚠️ M7 and M7b were applied when 35 cards sat outside the set. Phase 2 leaves
//	    NONE, so neither is reproducible as written; the finding they exercise is now
//	    reached only through P3 and P7 below, which put a card outside the roster
//	    first. Kept rather than deleted because the wave-0 verdict is what it is.
//	M7b the same, written `<!--prose-only:` with no space — the spacing a literal
//	    fence admitted and the parser did too    RED  K12 MARKER_OUT_OF_SCOPE
//	M11 the SWAP: add one assertable sentence AND cite one previously-unclassified
//	    one, so every class column is unchanged  RED  K12 POPULATION_MOVED
//	M12 cite a probe nobody ever wrote           RED  K12 ARM_CITATION_UNRESOLVED
//	M13 put a marker inside the generated machine block, where the walk cannot see
//	    it and PF_CARDS_REGEN=1 would delete it  RED  K12 MARKER_OUTSIDE_PROSE
//	M14 misspell the marker name (`probe_waiver`)
//	                                             RED  K12 MARKER_NAME_UNRECOGNISED
//	                                                  + K12 DEBT_GROWTH
//	M15 append a second `kind=` to downgrade a known-defect row to
//	    accepted-unprobed and skip its checks    RED  K12 DUPLICATE_FIELD
//	M17a delete the VISIBLE dates from the bullet carrying a marker, leaving only
//	    the marker's own date behind             RED  K11 UNDATED_CITATION
//
//	── enforcement side ──
//	M3  raise one ledger row by one, card untouched
//	                                             RED  K12 STALE_DEBT
//	M4  IsCandidate stops recognising            RED  K12 FLOOR_CANDIDATES
//	                                                  + 10× FLOOR_CARD_CANDIDATES
//	                                                  + 10× STALE_DEBT + 6× STALE_MARKER,
//	                                                  and 5 cardclaims fixtures
//	M5  delete the LedgerProblems call           RED  TestCardClaimsLedger…
//	M5b keep the call and discard its result with `_ =` — the nerve cut a
//	    call-site-only pin passes                RED  TestCardClaimsLedger…
//	M8  drop one card from k12Cards              RED  K12 SCOPE_SHRANK
//	                                                  + K12 LEDGER_UNSCOPED
//	M9  widen CitesAnArm from a *_test.go path to any .go path — the route by which
//	    a card retires debt by naming the implementation
//	                                             RED  23× K12 ARM_CITATION_UNRESOLVED
//	                                                  + TestCitingAnArmIsNarrower…
//	M16 restore the §0.1 sizer's bullet-blind splitting (both halves: no forced
//	    list boundaries and the narrow start class)
//	                                             RED  9× K12 STALE_DEBT + 2 fixtures
//
//	── controls, which must stay GREEN ──
//	M10  reword a sentence the recogniser does not flag
//	                                             GREEN the arm is bound to the
//	                                                   population, not to card churn
//	M17b M17a's card with the K11 comment-strip REMOVED
//	                                             GREEN this is the hole itself: with
//	                                                   the strip gone, an invisible
//	                                                   marker's date satisfies K11 for
//	                                                   a visible sentence that has none
//
// ─── Recorded mutants — phase 2 (aihub#572, roster 10 -> 45) ───────────────
//
// Applied to this tree on 2026-09-10, reverted after each run; every one was
// checked to have changed the tree (sha256 before/after, or a git rename) so that
// a green verdict cannot be a mutant that never landed. Seven red, one green
// control.
//
//	P1 drop one card from the widened roster, constant and ledger untouched
//	                                             RED  K12 SCOPE_SHRANK
//	                                                  + K12 ROSTER_INCOMPLETE
//	                                                  + K12 LEDGER_UNSCOPED
//	P2 delete one newly filed ledger row         RED  K12 LEDGER_MISSING
//	P3 the CONSTANT-BUMP ESCAPE: drop a card from the roster, lower
//	   k12ContractCards to 44, and delete that card's row — the diff somebody would
//	   write to make P1 green
//	                                             RED  K12 SCOPE_COUNT_DRIFT, which is
//	                                                  the whole reason the constant is
//	                                                  compared against the DISK and not
//	                                                  only against the roster
//	                                                  + K12 ROSTER_INCOMPLETE
//	                                                  + 5× K12 MARKER_OUT_OF_SCOPE
//	P4 raise one newly filed row's Unclassified by one, card untouched
//	                                             RED  K12 STALE_DEBT
//	P5 the NERVE CUT: unscopedCardNames stops comparing and returns nothing
//	                                             RED  TestUnscopedCardNames… (3 cases)
//	                                             🔴 and TestEveryCardIsInsideTheScopedSet
//	                                                  stayed GREEN, which is the whole
//	                                                  argument for the fixture: with the
//	                                                  roster complete its loop body runs
//	                                                  zero times, so the live arm cannot
//	                                                  notice its own comparison dying
//	P7 rename a card file and leave the roster alone — the ONE shape both count
//	   comparisons miss, because 45 cards and 45 roster entries still agree
//	                                             RED  K12 ROSTER_INCOMPLETE
//	                                                  + K12 SCOPE_ORPHAN
//	                                                  + K12 LEDGER_ORPHAN
//	                                                  and NO SCOPE_SHRANK, which is what
//	                                                  makes the two directions both
//	                                                  necessary rather than redundant
//
//	P8 add a 46th card file and leave the roster alone — the arriving-unwatched case
//	   this whole conversion is about
//	                                             RED  K12 SCOPE_COUNT_DRIFT
//	                                                  + K12 ROSTER_INCOMPLETE naming the
//	                                                  new file
//
//	── control, which must stay GREEN ──
//	P6 swap two roster entries' order            GREEN the roster is a set; its order
//	                                                   is documentation of aihub#543's
//	                                                   dispatch sequence and nothing
//	                                                   asserts on it
//
// 🔴 M11, M12, M13, M14, M15, M5b, M7b, M16 and M17a all come from a clean-context
// review of this arm's FIRST version, which every one of them walked straight
// through. They are recorded together rather than quietly fixed because the list
// is the honest answer to "how much would you trust this": a ratchet that can be
// fooled is worse than none, since it is the thing everyone else stops checking.

// k12Cards is the population this arm walks: since phase 2 (aihub#572) that is
// EVERY contract card in the repo, ordered the way aihub#543 dispatches them —
// the ten phase-1 tools of spec §2.2, the six thin phase-1b cards of the same
// section's tail, then the three phase-2 bands of §2.3.
//
// 🔴 Pinned LITERALLY, and it stays pinned now that it covers everything. The
// obvious phase-2 move was to delete the set and let the walk enumerate
// docs/mcp-cards itself, and that is the one edit this arm must not make: a
// population read off the filesystem shrinks whenever a card file is deleted or
// renamed, and it shrinks SILENTLY, because a smaller walk agrees with a smaller
// ledger and floorCards above leaves several cards of room. The literal roster is
// what gives SCOPE_SHRANK, SCOPE_ORPHAN and LEDGER_UNSCOPED a subject to compare
// against, and the set still only GROWS.
//
// It is checked in every direction it can be wrong, because an arm that quietly
// measures less than its title claims is the failure every floor in this file
// exists to catch:
//
//	roster wider than the tree    an entry that is no longer a card
//	                              (K12 SCOPE_ORPHAN), and a ledger row for a
//	                              non-card (K12 LEDGER_ORPHAN)
//	roster narrower than the tree a card on disk the roster omits
//	                              (K12 ROSTER_INCOMPLETE, in
//	                              TestEveryCardIsInsideTheScopedSet) — this is the
//	                              direction the roster cannot check about itself
//	roster vs its declared size   dropping an entry (K12 SCOPE_SHRANK)
//	roster vs the ledger          a scoped card with no row (K12 LEDGER_MISSING),
//	                              a row for an unscoped card (K12 LEDGER_UNSCOPED)
var k12Cards = []string{
	// Phase 1 — spec §2.2, in that document's dispatch order. Wave 1 drew all ten
	// to zero unclassified (aihub#566/#567/#568).
	"pf_predict_conflicts", // measured untrustworthy in both directions
	"pf_claim_work_item",   // issues the credential every later call authenticates with
	"pf_force_takeover",    // irreversible, silent to the party it evicts, branched on
	"pf_get_ready_queue",   // the dispatch input; aihub#387 already withdrew one field
	"pf_update_step",       // step state and the heartbeat lease
	"pf_complete_attempt",  // terminal transition
	"pf_acquire_locks",     // the fourth lock writer
	"pf_commit",            // reaches the working tree and takes commit-time locks
	"pf_ship",              // commit + push + PR in one call
	"pf_wrap",              // terminal success

	// Phase 1b — spec §2.2's tail: six thin cards on the harness phase 1 already
	// loaded. Deferred out of wave 1 rather than skipped, so they enter here.
	"pf_pr",
	"pf_push",
	"pf_diff",
	"pf_resolve_commit",
	"pf_pause_attempt",
	"pf_cancel_work_item",

	// Phase 2 band 1 — spec §2.3 data-write: the widest prose surface in the repo,
	// and a wrong claim here is STORED and found later.
	"pf_update_work_item",
	"pf_remember",
	"pf_create_work_item",
	"pf_save_artifact",
	"pf_update_memory",
	"pf_reinforce_memory",
	"pf_emit_event",
	"pf_batch_create_work_items",
	"pf_redact_memory",
	"pf_activate_memory",
	"pf_create_dependency",
	"pf_remove_dependency",

	// Phase 2 band 2 — spec §2.3 identity/authz: a wrong claim is an access decision.
	"pf_create_user",
	"pf_update_user",
	"pf_create_api_key",
	"pf_revoke_api_key",
	"pf_rotate_identifier",
	"pf_create_project",
	"pf_update_project",
	"pf_whoami",

	// Phase 2 band 3 — spec §2.3 retrieval: a wrong claim reads as "nothing there",
	// which is the aihub#270 class of defect.
	"pf_recall",
	"pf_list_work_items",
	"pf_read_events",
	"pf_get_memory",
	"pf_get_work_item",
	"pf_get_step",
	"pf_list_dependencies",
	"pf_list_projects",
	"pf_list_users",
}

// k12ContractCards is how many pf_* cards docs/mcp-cards holds, which since phase 2
// is also how many this arm scopes: 10 in aihub#543 spec §2.2, 6 in that section's
// phase-1b tail, and 29 in the three §2.3 bands.
//
// It is here so that silently dropping a card from the set above is red rather
// than a smaller measurement nobody notices — the same reason the floors exist,
// applied to the population's own definition. Lowering it is legitimate only in the
// same diff that removes the card and its ledger row, and floorCards bounds how far
// that can go before K1 objects too.
const k12ContractCards = 45

// k12Ledger records what each scoped card currently holds, per class.
//
// 🔴 A column per waiver kind, not one number for "waived". With a single column
// the cheapest way to make debt disappear would be to relabel a row
// `accepted-unprobed`; with a column each, a relabel moves a number from one
// column to another in a diff somebody signs. Candidates and Cited are recorded
// for the sharper reason cardclaims.Census documents: without them, a change that
// adds one assertable sentence while citing one previously-unclassified sentence
// nets to zero and passes green. ProseOnly is carried because declaring a claim
// unassertable is the cheapest escape of all, and it is NOT debt — this gate
// cannot check whether a `because` is honest, which is polyforge-scenario#20's
// reviewer's job.
//
// 🟡 Every Unclassified below is GRANDFATHERED, not accepted. The ten phase-1
// cards reached 0 in wave 1; the 35 rows phase 2 adds are entered at their
// measured values and classify nothing, because the owner's Q2 ruling is to
// ATTEMPT full coverage and the ratchet is the account book, not a renunciation of
// it. Probe waves draw these to zero. Only `structurally-unreachable` is a
// terminal state, and a row reaching 0 is as much a signed change as a row rising:
// a vacated slot is where the next unclassified sentence hides.
//
// ⚠️ Do not adjust a row by arithmetic. Every failure prints the replacement line
// ready to paste, which is the dbtestcov shape and exists so a number is never
// re-derived by hand.
var k12Ledger = map[string]cardclaims.Census{
	"pf_acquire_locks":           {Candidates: 14, Cited: 13, Unclassified: 0, PendingImplementation: 0, KnownDefect: 0, StructurallyUnreachable: 0, AcceptedUnprobed: 0, ProseOnly: 1},
	"pf_activate_memory":         {Candidates: 4, Cited: 4, Unclassified: 0, PendingImplementation: 0, KnownDefect: 0, StructurallyUnreachable: 0, AcceptedUnprobed: 0, ProseOnly: 0},
	"pf_batch_create_work_items": {Candidates: 16, Cited: 0, Unclassified: 16, PendingImplementation: 0, KnownDefect: 0, StructurallyUnreachable: 0, AcceptedUnprobed: 0, ProseOnly: 0},
	"pf_cancel_work_item":        {Candidates: 9, Cited: 8, Unclassified: 0, PendingImplementation: 0, KnownDefect: 0, StructurallyUnreachable: 0, AcceptedUnprobed: 0, ProseOnly: 1},
	"pf_claim_work_item":         {Candidates: 22, Cited: 21, Unclassified: 0, PendingImplementation: 0, KnownDefect: 0, StructurallyUnreachable: 0, AcceptedUnprobed: 0, ProseOnly: 1},
	"pf_commit":                  {Candidates: 8, Cited: 7, Unclassified: 0, PendingImplementation: 0, KnownDefect: 0, StructurallyUnreachable: 0, AcceptedUnprobed: 0, ProseOnly: 1},
	"pf_complete_attempt":        {Candidates: 14, Cited: 14, Unclassified: 0, PendingImplementation: 0, KnownDefect: 0, StructurallyUnreachable: 0, AcceptedUnprobed: 0, ProseOnly: 0},
	"pf_create_api_key":          {Candidates: 6, Cited: 6, Unclassified: 0, PendingImplementation: 0, KnownDefect: 0, StructurallyUnreachable: 0, AcceptedUnprobed: 0, ProseOnly: 0},
	"pf_create_dependency":       {Candidates: 6, Cited: 1, Unclassified: 5, PendingImplementation: 0, KnownDefect: 0, StructurallyUnreachable: 0, AcceptedUnprobed: 0, ProseOnly: 0},
	"pf_create_project":          {Candidates: 8, Cited: 8, Unclassified: 0, PendingImplementation: 0, KnownDefect: 0, StructurallyUnreachable: 0, AcceptedUnprobed: 0, ProseOnly: 0},
	"pf_create_user":             {Candidates: 14, Cited: 12, Unclassified: 0, PendingImplementation: 0, KnownDefect: 0, StructurallyUnreachable: 0, AcceptedUnprobed: 0, ProseOnly: 2},
	"pf_create_work_item":        {Candidates: 25, Cited: 0, Unclassified: 25, PendingImplementation: 0, KnownDefect: 0, StructurallyUnreachable: 0, AcceptedUnprobed: 0, ProseOnly: 0},
	"pf_diff":                    {Candidates: 6, Cited: 0, Unclassified: 6, PendingImplementation: 0, KnownDefect: 0, StructurallyUnreachable: 0, AcceptedUnprobed: 0, ProseOnly: 0},
	"pf_emit_event":              {Candidates: 21, Cited: 18, Unclassified: 0, PendingImplementation: 0, KnownDefect: 0, StructurallyUnreachable: 1, AcceptedUnprobed: 0, ProseOnly: 2},
	"pf_force_takeover":          {Candidates: 18, Cited: 16, Unclassified: 0, PendingImplementation: 1, KnownDefect: 0, StructurallyUnreachable: 0, AcceptedUnprobed: 0, ProseOnly: 1},
	"pf_get_memory":              {Candidates: 6, Cited: 6, Unclassified: 0, PendingImplementation: 0, KnownDefect: 0, StructurallyUnreachable: 0, AcceptedUnprobed: 0, ProseOnly: 0},
	"pf_get_ready_queue":         {Candidates: 23, Cited: 16, Unclassified: 0, PendingImplementation: 0, KnownDefect: 0, StructurallyUnreachable: 0, AcceptedUnprobed: 0, ProseOnly: 7},
	"pf_get_step":                {Candidates: 12, Cited: 1, Unclassified: 11, PendingImplementation: 0, KnownDefect: 0, StructurallyUnreachable: 0, AcceptedUnprobed: 0, ProseOnly: 0},
	"pf_get_work_item":           {Candidates: 15, Cited: 12, Unclassified: 0, PendingImplementation: 0, KnownDefect: 0, StructurallyUnreachable: 0, AcceptedUnprobed: 0, ProseOnly: 3},
	"pf_list_dependencies":       {Candidates: 12, Cited: 0, Unclassified: 12, PendingImplementation: 0, KnownDefect: 0, StructurallyUnreachable: 0, AcceptedUnprobed: 0, ProseOnly: 0},
	"pf_list_projects":           {Candidates: 11, Cited: 0, Unclassified: 11, PendingImplementation: 0, KnownDefect: 0, StructurallyUnreachable: 0, AcceptedUnprobed: 0, ProseOnly: 0},
	"pf_list_users":              {Candidates: 5, Cited: 0, Unclassified: 5, PendingImplementation: 0, KnownDefect: 0, StructurallyUnreachable: 0, AcceptedUnprobed: 0, ProseOnly: 0},
	"pf_list_work_items":         {Candidates: 22, Cited: 19, Unclassified: 0, PendingImplementation: 0, KnownDefect: 0, StructurallyUnreachable: 0, AcceptedUnprobed: 0, ProseOnly: 3},
	"pf_pause_attempt":           {Candidates: 13, Cited: 12, Unclassified: 0, PendingImplementation: 0, KnownDefect: 0, StructurallyUnreachable: 0, AcceptedUnprobed: 0, ProseOnly: 1},
	"pf_pr":                      {Candidates: 12, Cited: 9, Unclassified: 0, PendingImplementation: 0, KnownDefect: 0, StructurallyUnreachable: 0, AcceptedUnprobed: 0, ProseOnly: 3},
	"pf_predict_conflicts":       {Candidates: 20, Cited: 15, Unclassified: 0, PendingImplementation: 0, KnownDefect: 2, StructurallyUnreachable: 0, AcceptedUnprobed: 0, ProseOnly: 3},
	"pf_push":                    {Candidates: 7, Cited: 0, Unclassified: 7, PendingImplementation: 0, KnownDefect: 0, StructurallyUnreachable: 0, AcceptedUnprobed: 0, ProseOnly: 0},
	"pf_read_events":             {Candidates: 13, Cited: 12, Unclassified: 0, PendingImplementation: 0, KnownDefect: 0, StructurallyUnreachable: 0, AcceptedUnprobed: 0, ProseOnly: 1},
	"pf_recall":                  {Candidates: 34, Cited: 28, Unclassified: 0, PendingImplementation: 0, KnownDefect: 0, StructurallyUnreachable: 0, AcceptedUnprobed: 0, ProseOnly: 6},
	"pf_redact_memory":           {Candidates: 7, Cited: 7, Unclassified: 0, PendingImplementation: 0, KnownDefect: 0, StructurallyUnreachable: 0, AcceptedUnprobed: 0, ProseOnly: 0},
	"pf_reinforce_memory":        {Candidates: 22, Cited: 20, Unclassified: 0, PendingImplementation: 0, KnownDefect: 0, StructurallyUnreachable: 0, AcceptedUnprobed: 0, ProseOnly: 2},
	"pf_remember":                {Candidates: 46, Cited: 38, Unclassified: 0, PendingImplementation: 0, KnownDefect: 0, StructurallyUnreachable: 0, AcceptedUnprobed: 0, ProseOnly: 8},
	"pf_remove_dependency":       {Candidates: 3, Cited: 0, Unclassified: 3, PendingImplementation: 0, KnownDefect: 0, StructurallyUnreachable: 0, AcceptedUnprobed: 0, ProseOnly: 0},
	"pf_resolve_commit":          {Candidates: 9, Cited: 1, Unclassified: 8, PendingImplementation: 0, KnownDefect: 0, StructurallyUnreachable: 0, AcceptedUnprobed: 0, ProseOnly: 0},
	"pf_revoke_api_key":          {Candidates: 5, Cited: 5, Unclassified: 0, PendingImplementation: 0, KnownDefect: 0, StructurallyUnreachable: 0, AcceptedUnprobed: 0, ProseOnly: 0},
	"pf_rotate_identifier":       {Candidates: 7, Cited: 7, Unclassified: 0, PendingImplementation: 0, KnownDefect: 0, StructurallyUnreachable: 0, AcceptedUnprobed: 0, ProseOnly: 0},
	"pf_save_artifact":           {Candidates: 22, Cited: 20, Unclassified: 0, PendingImplementation: 0, KnownDefect: 0, StructurallyUnreachable: 0, AcceptedUnprobed: 0, ProseOnly: 2},
	"pf_ship":                    {Candidates: 8, Cited: 6, Unclassified: 0, PendingImplementation: 0, KnownDefect: 0, StructurallyUnreachable: 0, AcceptedUnprobed: 0, ProseOnly: 2},
	"pf_update_memory":           {Candidates: 10, Cited: 9, Unclassified: 0, PendingImplementation: 0, KnownDefect: 0, StructurallyUnreachable: 0, AcceptedUnprobed: 0, ProseOnly: 1},
	"pf_update_project":          {Candidates: 14, Cited: 11, Unclassified: 0, PendingImplementation: 0, KnownDefect: 0, StructurallyUnreachable: 0, AcceptedUnprobed: 0, ProseOnly: 3},
	"pf_update_step":             {Candidates: 17, Cited: 14, Unclassified: 0, PendingImplementation: 0, KnownDefect: 0, StructurallyUnreachable: 1, AcceptedUnprobed: 0, ProseOnly: 2},
	"pf_update_user":             {Candidates: 17, Cited: 14, Unclassified: 0, PendingImplementation: 0, KnownDefect: 0, StructurallyUnreachable: 0, AcceptedUnprobed: 0, ProseOnly: 3},
	"pf_update_work_item":        {Candidates: 47, Cited: 40, Unclassified: 0, PendingImplementation: 0, KnownDefect: 0, StructurallyUnreachable: 0, AcceptedUnprobed: 0, ProseOnly: 7},
	"pf_whoami":                  {Candidates: 11, Cited: 0, Unclassified: 11, PendingImplementation: 0, KnownDefect: 0, StructurallyUnreachable: 0, AcceptedUnprobed: 0, ProseOnly: 0},
	"pf_wrap":                    {Candidates: 3, Cited: 3, Unclassified: 0, PendingImplementation: 0, KnownDefect: 0, StructurallyUnreachable: 0, AcceptedUnprobed: 0, ProseOnly: 0},
}

const (
	// floorK12Sentences bounds how many card sentences the walk actually split out
	// of the scoped cards. A walk that splits nothing classifies nothing and every
	// count below matches a ledger of zeroes, which is the same green as a card set
	// with no debt. Re-pinned by phase 2 over 45 cards instead of 10. Current
	// value: the K12 line.
	floorK12Sentences = 700
	// floorK12Candidates bounds how many of those the recogniser called assertable.
	// This is the one that fails when the recogniser stops recognising — the
	// specific way this arm can rot into a live-looking green. Re-pinned by phase 2;
	// note it does NOT fall as probes land, because citing a sentence moves it
	// between columns INSIDE Candidates. Current value: the K12 line.
	floorK12Candidates = 380
	// floorK12ArmIndex bounds how many top-level Test functions the citation
	// resolver found in the tree. Without it an index that walked the wrong root
	// resolves nothing, every citation is reported unresolved, and the repair a
	// reader would reach for is deleting the citations. Deliberately NOT re-pinned
	// by phase 2: it bounds the TREE's test population, not the card roster, so
	// widening the roster is no evidence about it. Current value: the K12 line.
	floorK12ArmIndex = 400
)

// TestContractCardClaimsAreClassified is K12.
//
// It asserts nothing about whether a card sentence is TRUE — K11's limit, and this
// arm inherits it. It asserts that every sentence which ASSERTS something is
// either pointed at the arm that holds it or carried, by name and by date, in a
// count that cannot move without somebody signing the diff.
func TestContractCardClaimsAreClassified(t *testing.T) {
	cards := readCards(t)

	// 🔴 Built once, and a failure to build it is a FAILURE rather than a skip. Every
	// citation resolves against this index, so an empty one makes every cited
	// sentence read as debt — which would be a loud wrong answer, but the opposite
	// mistake (treating an unbuildable index as "nothing resolves, carry on") is the
	// quiet one, and this arm exists to refuse quiet.
	armIndex, err := cardclaims.BuildArmIndex(cardsRepoRoot)
	if err != nil {
		t.Fatalf("build the arm index from %s: %v — K12 cannot tell a citation that "+
			"resolves from one that does not without it, and answering green while unable "+
			"to check is the failure this whole arm is about", cardsRepoRoot, err)
	}
	if len(armIndex.Funcs) < floorK12ArmIndex {
		t.Errorf("K12 FLOOR_ARM_INDEX: the arm index found only %d top-level Test function(s) "+
			"in the tree, floor is %d. An index that found nothing resolves nothing, so every "+
			"citation in every card would be reported unresolved — and an index that found "+
			"too FEW silently turns real citations into debt. Current value: the K12 line.",
			len(armIndex.Funcs), floorK12ArmIndex)
	}

	if len(k12Cards) != k12ContractCards {
		t.Errorf("K12 SCOPE_SHRANK: k12Cards names %d card(s), and docs/mcp-cards holds %d. "+
			"Dropping one shrinks what this arm measures without shrinking what it claims to "+
			"measure, which is the same failure the floors below refuse for counts. Add the "+
			"card back, or move the number and say why in the same diff.",
			len(k12Cards), k12ContractCards)
	}
	// 🔴 A DIFFERENT finding from SCOPE_SHRANK above, deliberately, because this one
	// fires in both directions: docs/mcp-cards can also GROW past the constant, and
	// calling that "shrank" would be a message false in one of its two halves —
	// the defect class aihub#543 §9.6 records for the drift classifier.
	if len(cards) != k12ContractCards {
		t.Errorf("K12 SCOPE_COUNT_DRIFT: docs/mcp-cards holds %d card(s) and k12ContractCards "+
			"says %d. Since phase 2 the roster is the WHOLE card set, so the two cannot "+
			"differ without some card being either walked by nothing or named by nothing. "+
			"TestEveryCardIsInsideTheScopedSet names the cards no roster entry covers, and "+
			"SCOPE_ORPHAN below names the roster entries with no file.",
			len(cards), k12ContractCards)
	}

	scoped := make(map[string]bool, len(k12Cards))
	tallies := make(map[string]cardclaims.CardTally, len(k12Cards))
	sentences, candidates, cited := 0, 0, 0

	for _, name := range k12Cards {
		c, ok := cards[name]
		if !ok {
			t.Errorf("K12 SCOPE_ORPHAN: k12Cards names %q, which is not a card under %s. A "+
				"scoped set naming a file that does not exist measures one card fewer than it "+
				"says it does.", name, cardsDirRel)
			continue
		}
		scoped[name] = true
		tally := cardclaims.Tally(c.path, c.prose, armIndex)

		// 🔴 Tally walks the PROSE, which starts after the machine block. A marker
		// before or inside that block is invisible to it, and the out-of-scope arm
		// skips scoped cards entirely — so without this the head of every scoped card
		// is a blind spot where a marker looks filed and classifies nothing.
		bodyMarkers, bodyUnrec := cardclaims.ScanAll(c.body, false)
		proseMarkers, proseUnrec := cardclaims.ScanAll(c.prose, false)
		if len(bodyMarkers) > len(proseMarkers) || len(bodyUnrec) > len(proseUnrec) {
			t.Errorf("K12 MARKER_OUTSIDE_PROSE: %s carries %d marker(s) and %d marker-shaped "+
				"comment(s) in the file but only %d and %d in the prose this walk reads. The "+
				"difference is ahead of or inside the generated machine block, where nothing "+
				"classifies anything — and PF_CARDS_REGEN=1 rewrites that block, so a marker "+
				"there is deleted by an unrelated regeneration without a word.",
				c.path, len(bodyMarkers), len(bodyUnrec), len(proseMarkers), len(proseUnrec))
		}
		tallies[name] = tally
		sentences += tally.Sentences
		candidates += tally.Census.Candidates
		cited += tally.Census.Cited

		for _, p := range tally.Problems {
			t.Error(p)
		}
		if tally.Census.Candidates == 0 {
			t.Errorf("K12 FLOOR_CARD_CANDIDATES: %s produced %d sentence(s) and NO "+
				"candidate-assertable one. Every phase-1 card describes behaviour, so a zero "+
				"here is the recogniser failing on this card rather than a card that promises "+
				"nothing — and a card contributing nothing to the population contributes "+
				"nothing to the ledger either, silently.", c.path, tally.Sentences)
		}
	}

	// The whole ledger half, in both directions. Kept in one call so the wiring
	// check below has something to look for.
	for _, p := range cardclaims.LedgerProblems(tallies, k12Ledger, cardNames(cards)) {
		t.Error(p)
	}

	if sentences < floorK12Sentences {
		t.Errorf("K12 FLOOR_SENTENCES: the walk split only %d sentence(s) out of %d scoped "+
			"card(s), floor is %d — a walk that splits nothing classifies nothing, and a "+
			"ledger of zeroes then matches perfectly", sentences, len(scoped), floorK12Sentences)
	}
	if candidates < floorK12Candidates {
		t.Errorf("K12 FLOOR_CANDIDATES: the recogniser called only %d sentence(s) "+
			"candidate-assertable, floor is %d — this is the number that falls when the "+
			"recogniser stops recognising, and every count below it would then agree with a "+
			"ledger nobody had to change", candidates, floorK12Candidates)
	}

	total := cardclaims.Census{}
	for _, name := range k12Cards {
		d := tallies[name].Census
		total.Unclassified += d.Unclassified
		total.PendingImplementation += d.PendingImplementation
		total.KnownDefect += d.KnownDefect
		total.StructurallyUnreachable += d.StructurallyUnreachable
		total.AcceptedUnprobed += d.AcceptedUnprobed
		total.ProseOnly += d.ProseOnly
	}
	t.Logf("K12: %d sentence(s) read across %d scoped card(s), %d candidate-assertable, "+
		"%d citing an arm; debt unclassified=%d pending-implementation=%d known-defect=%d "+
		"structurally-unreachable=%d accepted-unprobed=%d, prose-only=%d",
		sentences, len(scoped), candidates, cited,
		total.Unclassified, total.PendingImplementation, total.KnownDefect,
		total.StructurallyUnreachable, total.AcceptedUnprobed, total.ProseOnly)
}

// cardNames is the card set as a lookup, so LedgerProblems can tell a row naming a
// non-card from a row naming a card outside the scoped set. The two are different
// mistakes and the edit that fixes them differs.
func cardNames(cards map[string]*card) map[string]bool {
	out := make(map[string]bool, len(cards))
	for name := range cards {
		out[name] = true
	}
	return out
}

// TestEveryCardIsInsideTheScopedSet is what the out-of-scope marker fence became
// when phase 2 (aihub#572) widened k12Cards from ten cards to all 45.
//
// 🔴 CONVERTED rather than retired, because retiring it is the trap. The fence's
// population was "the cards K12 does not walk", wave 0 had 35 of them, and phase 2
// leaves ZERO — an arm whose population is empty is green forever, which is the
// vacuous shape every floor in this file refuses. And the hole it guarded did not
// close, it MOVED: SCOPE_SHRANK compares k12Cards against a constant, so a 46th
// card added to docs/mcp-cards leaves roster and constant both at 45 and arrives
// watched by nothing — no ledger row bounds it, not one of its sentences is
// counted, and a classification marker on it would be written, reviewed, and then
// read by no arm. That is exactly the wave-0 hole, at card 46 instead of card 11.
//
// So the check runs the other way round: every card ON DISK must be in the roster.
// A marker on a card that is not gets its own finding as well, because a marker
// nothing counts is strictly worse than a card nothing walks — the reader of that
// card sees a classification the gate cannot see.
func TestEveryCardIsInsideTheScopedSet(t *testing.T) {
	cards := readCards(t)

	// Order does not matter here: unscopedCardNames sorts what it reports, which is
	// what keeps the failure output stable across Go's randomised map iteration.
	names := make([]string, 0, len(cards))
	for name := range cards {
		names = append(names, name)
	}

	for _, name := range unscopedCardNames(names, k12Cards) {
		c := cards[name]
		t.Errorf("K12 ROSTER_INCOMPLETE: %s is a card and is not in k12Cards, so no ledger "+
			"row bounds it and not one of its sentences is counted. Add it to k12Cards with "+
			"its measured ledger row, and raise k12ContractCards, in the same change — since "+
			"phase 2 the roster is meant to be COMPLETE, not merely growing.", c.path)

		// 🔴 The same matcher K12 parses with, not a literal "<!-- probe-waiver:".
		// A literal admits exactly one spacing; the parser admits any. Measured in wave
		// 0: a marker written without the space after `<!--` was invisible to this arm
		// on all 35 unscoped cards while parsing perfectly everywhere else. And
		// fence-aware in the other direction, so a card that DOCUMENTS the syntax in a
		// code sample is not reddened for quoting it.
		markers, unrecognised := cardclaims.ScanAll(c.body, true)
		for _, m := range markers {
			t.Errorf("K12 MARKER_OUT_OF_SCOPE: %s carries %s and is not in k12Cards, so "+
				"nothing counts it and nothing will report it stale. Add the card to k12Cards "+
				"with its ledger row in the same change — or drop the marker.", c.path, m.Raw)
		}
		for _, raw := range unrecognised {
			t.Errorf("K12 MARKER_NAME_UNRECOGNISED: %s carries %s outside the scoped set. "+
				"Whether or not the name were spelled correctly this card is not walked, so "+
				"the comment classifies nothing.", c.path, raw)
		}
	}

	// 🔴 The floor is on the CARDS READ, not on the comparisons made. Every card
	// either matches the roster or is reported, so counting the loop's own turns
	// would be a tautology that is satisfied by reading no cards at all — which is
	// the one way this arm can be green while checking nothing.
	if len(cards) < floorCards {
		t.Errorf("K12 FLOOR_ROSTER: only %d card(s) were compared against the roster, floor "+
			"is %d — an arm that reads fewer cards than the tree holds reports green about "+
			"the ones it never saw, and this arm's whole job is the cards nobody listed",
			len(cards), floorCards)
	}
}

// unscopedCardNames returns the cards the tree holds that the roster does not
// name, sorted, and nothing else — a roster entry with no card behind it is
// SCOPE_ORPHAN's business and stays there, because the two are different mistakes
// and the edit that fixes them differs.
//
// 🔴 Extracted from the arm above rather than inlined, and this is the whole point
// of the extraction: on a healthy tree the roster is COMPLETE, so the arm's loop
// body runs zero times and nothing exercises the comparison. That is the shape
// aihub#543 §4.5 already refuses for the ledger — "a fixture test … because the
// tables will be near-empty on a healthy tree" — and a guard whose live population
// is empty by design needs it more, not less.
func unscopedCardNames(cards, roster []string) []string {
	scoped := make(map[string]bool, len(roster))
	for _, name := range roster {
		scoped[name] = true
	}
	var out []string
	for _, name := range cards {
		if !scoped[name] {
			out = append(out, name)
		}
	}
	sort.Strings(out)
	return out
}

// TestUnscopedCardNamesFindsTheGap is the fixture arm for the comparison above.
//
// The last two cases are positive controls rather than decoration: one pins that a
// roster entry with no card is NOT reported here, so the conversion did not quietly
// absorb SCOPE_ORPHAN's job, and one pins the wave-0 state — an empty roster
// reports every card — so a mutant that inverts the comparison cannot pass by
// reporting nothing.
func TestUnscopedCardNamesFindsTheGap(t *testing.T) {
	tree := []string{"pf_a", "pf_b", "pf_c"}
	for _, tc := range []struct {
		name   string
		cards  []string
		roster []string
		want   []string
	}{
		{"complete roster reports nothing", tree, []string{"pf_a", "pf_b", "pf_c"}, nil},
		{"one card missing from the roster", tree, []string{"pf_a", "pf_c"}, []string{"pf_b"}},
		{"two missing, reported sorted", tree, []string{"pf_b"}, []string{"pf_a", "pf_c"}},
		{"roster order is irrelevant", tree, []string{"pf_c", "pf_a", "pf_b"}, nil},
		{"a roster entry with no card is not this arm's finding", tree,
			[]string{"pf_a", "pf_b", "pf_c", "pf_gone"}, nil},
		{"an empty roster reports every card", tree, nil, tree},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := unscopedCardNames(tc.cards, tc.roster)
			if !equalStrings(got, tc.want) {
				t.Errorf("unscopedCardNames(%v, %v) = %v, want %v — this comparison is the only "+
					"thing standing between a newly added card and arriving unwatched, and on a "+
					"healthy tree it is the only thing that exercises it at all",
					tc.cards, tc.roster, got, tc.want)
			}
		})
	}
}

// TestCardClaimsLedgerCheckIsWiredIntoTheArm pins the one thing a fixture table
// structurally cannot.
//
// 🔴 cardclaims.LedgerProblems is exercised against fixtures in its own package,
// and those fixtures go on passing whether or not K12 still calls it — while a
// healthy tree's ledger MATCHES, so deleting the call reddens nothing. That is a
// live-looking green over a disconnected check, and reading the source is the only
// instrument available for it.
//
// 🔴 It checks that the RESULT IS CONSUMED, not merely that the call appears.
// Checking for the call alone is satisfied by `_ = cardclaims.LedgerProblems(…)`,
// which severs the nerve while leaving the call site in place — a one-character
// edit that passes a wiring check whose whole purpose is to refuse it. So the call
// must be the range expression of a loop whose body fails the test.
//
// ⚠️ TestOpenCitationWaiverCheckIsWiredIntoTheArm above has the same weaker shape
// and is NOT changed here: it is existing K11 code and this wave is scoped to K12.
// Named rather than left as a silent gap.
func TestCardClaimsLedgerCheckIsWiredIntoTheArm(t *testing.T) {
	const (
		gateFile = "contract_cards_gate_test.go"
		armName  = "TestContractCardClaimsAreClassified"
		callName = "LedgerProblems"
	)

	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, gateFile, nil, parser.SkipObjectResolution)
	if err != nil {
		t.Fatalf("parse %s: %v — this arm cannot report a missing call site from a file it "+
			"could not read, so this is a failure rather than a skip", gateFile, err)
	}

	var arm *ast.FuncDecl
	for _, d := range f.Decls {
		if fn, ok := d.(*ast.FuncDecl); ok && fn.Recv == nil && fn.Name.Name == armName {
			arm = fn
			break
		}
	}
	if arm == nil {
		t.Fatalf("%s declares no func %s. If K12's arm was renamed, rename it here too — a "+
			"wiring check that cannot find the thing it checks reports green forever",
			gateFile, armName)
	}

	called, consumed := false, false
	ast.Inspect(arm.Body, func(n ast.Node) bool {
		rng, ok := n.(*ast.RangeStmt)
		if !ok {
			return true
		}
		call, ok := rng.X.(*ast.CallExpr)
		if !ok {
			return true
		}
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok || sel.Sel.Name != callName {
			return true
		}
		called = true
		ast.Inspect(rng.Body, func(inner ast.Node) bool {
			c, ok := inner.(*ast.CallExpr)
			if !ok {
				return true
			}
			s, ok := c.Fun.(*ast.SelectorExpr)
			if !ok {
				return true
			}
			switch s.Sel.Name {
			case "Error", "Errorf", "Fatal", "Fatalf":
				consumed = true
			}
			return !consumed
		})
		return !consumed
	})

	if !called {
		t.Errorf("%s does not range over cardclaims.%s, so the whole ledger half is dead "+
			"code from the gate's side: a healthy tree's tallies MATCH the ledger, so nothing "+
			"about that shows up as a failing card, and the fixtures in the cardclaims "+
			"package go on passing against a function the arm no longer consults. Restore "+
			"the call.", armName, callName)
	} else if !consumed {
		t.Errorf("%s calls cardclaims.%s but its findings never reach t.Error or t.Fatal, so "+
			"every ledger problem is computed and thrown away. That is worse than no call at "+
			"all: the wiring reads as present to anyone grepping for it, and the arm is green "+
			"on a tree the ledger disagrees with.", armName, callName)
	}
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
