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
// file pattern, and regenerating is not a silent pass: the regenerated diff has
// to be committed, so a schema change surfaces as a card diff a reviewer reads.
// Hand-typing 232 parameter rows would instead guarantee transcription errors
// this gate would report as code defects.
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
//	K4 non-vacuity     a missing heading, a parameter never named in the prose,
//	                   or a "written" card with no hop-4 body
//	K5 stale exemption a "pending" card that HAS a hop-4 body, or more pending
//	                   cards than the ceiling allows
//	K6 anchor          a cited .go path that does not exist, or a cited symbol
//	                   that the cited file does not declare
//	K7 corpus drift    response_keys_observed disagreeing with the checked-in
//	                   aihub#412 corpus record
//	K8 floors          the walk itself finding too little to have measured
//	                   anything
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
)

// maxPendingCards is a CEILING ON DEBT, not a floor on a measurement, so unlike
// every constant above it IS set at the measured value on purpose. Lowering it
// happens for free as cards get written; raising it has to be a deliberate edit
// somebody signs off on. Measured 2026-09-08: 0 cards are pending.
const maxPendingCards = 0

// minHop4Body is how many characters of hop-4 prose a "written" card must carry.
//
// It is the arm that separates a card from a generated shell. K1-K3 are all
// satisfied by an empty file with a correct machine block, which is exactly the
// artifact a generator produces — and "the file exists" is not the claim this
// gate is supposed to certify. The same number is what makes a "pending" card
// falsifiable in the other direction (K5).
const minHop4Body = 200

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
	// has called versus a tool whose results carry no top-level keys. K7 checks
	// both directions.
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

// sectionBody returns the text under the first heading with the given prefix, up
// to the next level-2 heading.
func sectionBody(prose, headingPrefix string) (string, bool) {
	lines := strings.Split(prose, "\n")
	start := -1
	for i, l := range lines {
		if strings.HasPrefix(l, headingPrefix) {
			start = i + 1
			break
		}
	}
	if start < 0 {
		return "", false
	}
	var out []string
	for _, l := range lines[start:] {
		if strings.HasPrefix(l, "## ") {
			break
		}
		out = append(out, l)
	}
	return strings.TrimSpace(strings.Join(out, "\n")), true
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

	pending := 0
	for name, c := range cards {
		lt, published := tools[name]
		if !published {
			continue // K2 reports it
		}

		for _, h := range requiredCardHeadings {
			if _, ok := sectionBody(c.prose, h); !ok {
				t.Errorf("K4 CARD_HEADING_MISSING: %s has no %q section. The six sections are "+
					"the card's shape; a card missing one is silent about a hop rather than "+
					"saying it has nothing to report there.", c.path, h)
			}
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

		hop4, _ := sectionBody(c.prose, "## hop 4")
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
	t.Logf("K4/K5: %d cards checked, %d pending (ceiling %d)", len(cards), pending, maxPendingCards)
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
	t.Logf("K7: %d cards compared against the aihub#412 corpus records", compared)
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
