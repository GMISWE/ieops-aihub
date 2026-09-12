// Package clampdisclosure enumerates every clamp in the tree and requires each
// one to either DISCLOSE what it changed or carry a named, dated waiver.
//
// # The defect this exists to prevent
//
// aihub#314 settled one disclosure convention for "the server changed the value
// you sent": a single `request_adjusted` list that every clamp appends to
// (internal/domain/request_adjusted.go). Its reach was written in prose — "every
// clamp" — and clamps live in code, so a new clamp that disclosed nothing was
// invisible to everything except a human reading the whole tree.
//
// That is not hypothetical. It happened TWICE, and both times a person found it
// by reading rather than a gate by failing:
//
//   - ReadyQueue's `max` clamp answered `max=5000` and `max=200` with identical
//     bytes. Found by review, closed by aihub#432 (102655f, PR #379).
//   - pf_reinforce_memory's strength saturation appends nothing. Found by the
//     aihub#521 executor while reading the aihub#411 decision table, and the
//     owner then ruled (2026-09-09, PR #426) to KEEP it undisclosed.
//
// aihub#411 T1-12 records both: §1 the convention, §6.1 the second exemption.
// What neither of them could record is a reach, because the reach was a sentence.
// aihub#532 was filed on the prediction that follows — a third clamp, found the
// same way, costing a third piece of bookkeeping — and the owner's answer
// (2026-09-09, "#532 选A") was: build the gate.
//
// # What counts as a clamp, decided by a CODE PROPERTY
//
// Not by the word "clamp" in a comment, and not by a list of known parameters.
// The property is: the site takes a value it already holds, SUBSTITUTES THE
// BOUND IT WAS JUST COMPARED AGAINST, and carries on. Three spellings:
//
//	if v > CEIL { v = CEIL }        // clamp
//	if v > CEIL { return CEIL }     // clamp
//	v = min(v, CEIL)                // clamp
//
// The discriminator earns its keep on what it EXCLUDES, and both exclusions are
// shapes this repo actually contains:
//
//	if v <= 0 { v = 20 }            // DEFAULT BACKFILL, not a clamp: the value
//	                                // compared against (0) is not the value
//	                                // assigned (20). Nothing the caller sent was
//	                                // changed — there was nothing there.
//
//	if v > peak { peak = v }        // MAX TRACKING, not a clamp: the assignment
//	                                // moves the BOUND, not the value. The
//	                                // operands are the same two as a clamp's and
//	                                // the roles are swapped, which is why the
//	                                // recogniser compares texts rather than
//	                                // matching a shape loosely.
//
// Both are in the tree (RecallWithVector's `topK <= 0`, idempotency.go's
// PeakBytes) and both stay out of the report, held there by fixture arms.
//
// # Known blind spots, stated because invisible reads as compliant
//
//   - A clamp spelled as a `switch { case v > CEIL: v = CEIL }` is not
//     recognised. No such site exists today; the floor arm is what catches a
//     recogniser that stops seeing the population it polices.
//   - Disclosure is resolved WITHIN ONE FILE, at hop 2 (see Disclosed). A clamp
//     whose disclosing wrapper lives in another file of the same package reads
//     as undisclosed and goes RED. That is the safe direction — the gate's
//     failure mode is noise a human resolves, not silence — and it costs
//     nothing today: all three disclosing sites keep the clamp and the append in
//     one file.
//   - CallerParamNames covers QUERY parameters only, so a JSON BODY field's name
//     is not in the vocabulary. That is why the vocabulary check is a floor on
//     ScopeNote and not the classifier: the one live waiver (`strength_delta`)
//     is a body field, and it is held by being in the waiver table with a kind
//     and a date, not by name matching.
package clampdisclosure

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// ─── The sites ──────────────────────────────────────────────────────────────

// Form is how a clamp is spelled at its site.
type Form string

const (
	// FormIfAssign is `if v > CEIL { v = CEIL }`.
	FormIfAssign Form = "if-assign"
	// FormIfReturn is `if v > CEIL { return CEIL }`.
	FormIfReturn Form = "if-return"
	// FormMinMax is `v = min(v, CEIL)` or `return max(v, FLOOR)`.
	FormMinMax Form = "min-max"
)

// Site is one clamp.
type Site struct {
	// File is slash-separated and relative to the root passed to ScanDir.
	File string
	// Line is the line of the `if` or of the assignment.
	Line int
	// Func names the enclosing declaration, methods as "(Recv).Name".
	Func string
	// Value is the source text of the thing being clamped, e.g. "topK".
	Value string
	// Bound is the source text of the bound it is clamped to.
	Bound string
	// Form is how it is spelled.
	Form Form
	// Disclosed is true when this site's own function, or a function in the
	// same file that calls it, appends to request_adjusted.
	Disclosed bool
}

// Key is the ledger key for a site: "<file>:<func>:<value>".
//
// WITHOUT the line number, for the reason internal/citest/rowserr/rowserr.go
// (Loop.Key) states: a ledger entry is a statement about a site, and a line
// number changes whenever anything above it is edited, so a keyed-by-line entry
// stops matching silently — or starts matching a different site that moved onto
// that line.
//
// WITHOUT the bound, too, and that half is a decision rather than a copy. A
// two-sided saturation is ONE clamp:
//
//	if s > MaxBaseStrength { s = MaxBaseStrength }
//	if s < MinBaseStrength { s = MinBaseStrength }
//
// is the reinforce clamp, and the aihub#506 ruling that exempted it exempted the
// saturation, not the ceiling separately from the floor. Keying on the bound
// would split it into two entries an author could answer differently — and a
// ledger that lets one decision be recorded twice is a ledger where the two
// copies drift. The cost is the mirror of rowserr's: two UNRELATED clamps on one
// variable in one function share a key, so one entry would cover both. Nothing
// in the tree has that shape.
func (s Site) Key() string { return s.File + ":" + s.Func + ":" + s.Value }

func (s Site) String() string {
	return fmt.Sprintf("%s:%d %s clamps %s to %s (%s)", s.File, s.Line, s.Func, s.Value, s.Bound, s.Form)
}

// ─── The ledger: two tables, because the entries are two different claims ───

// ScopeNote records a clamp the convention does not reach: the clamped value
// never came from a caller, so there is no request to report an adjustment to.
//
// 🔴 This is a SCOPE EXCLUSION and not a waiver, and the tables are separate for
// that reason. A waiver says "this should disclose and does not"; a scope note
// says "there is nothing here to disclose". Filing eleven of the second kind in
// with the first would bury the one live exemption under bookkeeping — which is
// exactly the failure aihub#411 T1-12 §6.1 describes, where the reach of the
// convention became "every clamp except one an owner exempted" and no response
// carried that fact.
type ScopeNote struct {
	// Origin says what the value IS, in enough detail to be falsified by
	// reading the function. "internal" is not an origin.
	Origin string
}

// WaiverKind separates the two kinds of exemption aihub#411 T1-12 §6.1 found to be
// different in kind, plus the state a newly-found site sits in before anybody
// has ruled on it.
type WaiverKind string

const (
	// KindAcceptedContract is an owner-accepted divergence: disclosure was
	// available and was DECLINED. It is not waiting for anything.
	KindAcceptedContract WaiverKind = "accepted-contract"
	// KindStructural is a site with no disclosure channel to use: the response
	// is not a JSON document with a place to put the field. Provisional by
	// nature — it closes when the channel appears, the way ReadyQueue's did.
	KindStructural WaiverKind = "structural"
	// KindPendingAdjudication is a site this gate FOUND, that is neither
	// disclosed nor ruled on. It must name the work item carrying the question,
	// because "pending" with nobody asked is the silence this package exists to
	// end.
	KindPendingAdjudication WaiverKind = "pending-adjudication"
)

// Describe is the kind's own account of what it claims, used in failure text so
// a reader sees the claim rather than the label.
func (k WaiverKind) Describe() string {
	switch k {
	case KindAcceptedContract:
		return "an owner accepted this divergence; disclosure was available and was declined"
	case KindStructural:
		return "this response has no place to put the field; the waiver closes when one exists"
	case KindPendingAdjudication:
		return "found by this gate, not yet ruled on; the named work item carries the question"
	}
	return ""
}

// Waiver exempts a clamp on a CALLER-SUPPLIED value from disclosing it.
type Waiver struct {
	// Kind is which of the three claims above this entry makes.
	Kind WaiverKind
	// Param is the caller-facing parameter name, so the entry can be read
	// against a request rather than against the source.
	Param string
	// Reason is why this site does not disclose.
	Reason string
	// Decided is the date of the decision, YYYY-MM-DD.
	Decided string
	// Citation is where the decision is written down — a work item, a ruling,
	// a merge, or the file that argues it.
	Citation string
}

// clampsOutsideTheConvention names every clamp whose value never came from a
// caller. Keyed by Site.Key().
//
// Each Origin was written by reading the enclosing function, and every one of
// them is falsifiable that way. They are the answer to the question the two
// missed clamps never had to answer out loud: where did this value come from?
var clampsOutsideTheConvention = map[string]ScopeNote{
	"internal/citest/dbtestcov/main.go:SplitRunPattern:cs": {
		Origin: "a bracket-nesting counter while splitting a `go test -run` pattern, in CI " +
			"tooling that serves no request at all; floored at 0 because an unmatched ']' is legal",
	},
	"internal/citest/cardclaims/cardclaims.go:cleanLine:o": {
		Origin: "a byte offset into ONE LINE of a contract card's own markdown, after the " +
			"classification markers have been cut out of it and the remainder trimmed. Floored " +
			"at 0 because a marker sitting in the leading whitespace lands ahead of the trimmed " +
			"text; capped at len(trimmed) for the trailing case. Nobody sends a card its own " +
			"offsets — this is CI tooling reading files in the repo, and it serves no request",
	},

	"internal/domain/conflicts.go:lastActiveAgeSeconds:age": {
		Origin: "the age of a run_attempts.last_active_at reading, computed here from two " +
			"different clocks. It is REPORTED to a caller (last_active_age_seconds) and was never " +
			"SENT by one, which is the distinction the convention turns on",
	},
	"internal/domain/lexical.go:lexicalSnippet:matchAt": {
		Origin: "a byte offset of a case-insensitive token match inside one line of a stored " +
			"document, recomputed against the trimmed line while building a lexical hit's " +
			"snippet (aihub#360); floored at 0 because ToLower can shift byte offsets on a " +
			"handful of code points. Derived from stored content, never a request field — the " +
			"caller's query reaches this function only as the token being searched for",
	},
	"internal/domain/memory.go:MemoryStrength:stabilityDays": {
		Origin: "a decay constant derived from the memory's own type by ComputeStabilityDays, " +
			"never a request field; the guard is against dividing by zero",
	},
	"internal/domain/memory_unmatched.go:UnmatchedTypes:end": {
		Origin: "a slice index into the caller's type list while chunking it at 256 for " +
			"Postgres's 1664-entry target-list cap. Every entry is still queried, so no value of " +
			"the caller's is changed — only the number of statements it takes",
	},
	"internal/domain/resource_events.go:emitResourceEvents:n": {
		Origin: "the row count of one INSERT batch, taken from len(evs) of events this server " +
			"generated itself while releasing locks",
	},
	"internal/embedding/budget.go:(*budgetProvider).EmbedBatch:n": {
		Origin: "len(texts), used only to scale a context timeout; floored at 1 so an empty " +
			"batch does not compute a budget that has already expired",
	},
	"internal/mcp/tools_lifecycle.go:newClaimBranchNames:budget": {
		Origin: "the characters left for a branch-name suffix after the stem, computed from " +
			"claimBranchMaxTotal; a git ref-length budget, not a request parameter",
	},
	"internal/render/diagram.go:SetDiagramCompileTimeout:d": {
		Origin: "a process-wide compile budget set from an env var by InitDiagramCompileTimeout " +
			"(or by a test); the operator sets it, no request carries it",
	},
	"internal/render/svg_block.go:(*svgBlockParser).Open:budget": {
		Origin: "the per-parse work budget goldmark's parser context carries between block " +
			"parsers; floored at 0 after subtracting the steps this call spent",
	},
	"internal/render/svg_block.go:findSVGBlockEnd:hardLimit": {
		Origin: "a byte offset into the markdown being parsed, capped at len(source) so the " +
			"lookahead cannot run off the end",
	},
	"internal/render/svg_block.go:findSVGBlockEnd:scanLimit": {
		Origin: "the same lookahead's fence-boundary offset, capped by hardLimit above; both " +
			"are positions in a document, not values anybody sent",
	},
}

// disclosureWaivers names every clamp on a caller-supplied value that does not
// disclose it. Keyed by Site.Key().
//
// 🔴 Three entries, three different kinds, and the difference is the point
// aihub#411 T1-12 §6.1 makes: "two exemptions are on record, only one is live, and
// it is not the one this ruling names". ReadyQueue's was STRUCTURAL and
// provisional and closed the moment a field existed (aihub#432); reinforce's is
// an ACCEPTED divergence with no gap to close. Recording them under one label
// would lose exactly the fact that told the two apart.
var disclosureWaivers = map[string]Waiver{
	// The seed. aihub#506's owner ruling reads "Option 1: KEEP the clamp, write
	// it into the contract … No 400, no response-shape change", so disclosure
	// here is the option that was DECLINED — not one nobody got to.
	//
	// Both lines of the saturation (> Max and < Min) are this one entry, because
	// the ruling exempted the saturation and not the ceiling separately from the
	// floor. See Site.Key on why the bound is not part of the key.
	//
	// Where the caller CAN read it: hop 1, in strength_delta's own published
	// description, held by internal/mcp/tools_memory_test.go
	// (TestPublishedStrengthDeltaSaysWhatReinforceEnforces).
	"internal/server/routes_memory.go:handleReinforceMemory:newBaseStrength": {
		Kind:  KindAcceptedContract,
		Param: "strength_delta",
		Reason: "the stored strength plus the caller's delta saturates into " +
			"[MinBaseStrength, MaxBaseStrength] and the 200 carries no disclosure key. The owner " +
			"was asked and chose to keep the clamp and document it in the contract; the two losing " +
			"candidates (a 400 on an overflowing sum, and a `clamped: true` field) are recorded in " +
			"docs/mcp-cards/pf_reinforce_memory.md",
		Decided:  "2026-09-09",
		Citation: "aihub#506 attrs.owner_ruling_2026_09_09; PR #426, merge 0be8bbc; aihub#411 T1-12 §6.1",
	},

	// Found by this census, not by the vocabulary check — `n` looks like nothing
	// in particular, and only reading the function shows it is a page size a
	// browser sent. That is worth saying plainly: the vocabulary check is a
	// floor on ScopeNote, not the classifier.
	"internal/server/queryparam.go:queryIntLenientUI:n": {
		Kind:  KindStructural,
		Param: "limit",
		Reason: "the /ui page-size reader, called only from ui_handlers_memory.go and " +
			"ui_handlers_wi.go with (\"limit\", 50, 200). request_adjusted is a field on a JSON " +
			"response body and this handler answers with server-rendered HTML, so there is no " +
			"channel to disclose through. The surface is separately contracted for a caller that " +
			"is a browser following links this server generated — see the /ui exemption note in " +
			"internal/server/queryparam.go, which the gate test there already bounds by refusing " +
			"a call to this reader from any file not named ui_*.go",
		Decided:  "2026-09-09",
		Citation: "internal/server/queryparam.go, \"The /ui exemption, declared here rather than taken quietly\" (aihub#340); enumerated by aihub#532",
	},
}

// Waivers and ScopeNotes hand out copies of the ledger so a test can drive the
// validators over a modified table without mutating the real one.
func Waivers() map[string]Waiver {
	out := make(map[string]Waiver, len(disclosureWaivers))
	for k, v := range disclosureWaivers {
		out[k] = v
	}
	return out
}

func ScopeNotes() map[string]ScopeNote {
	out := make(map[string]ScopeNote, len(clampsOutsideTheConvention))
	for k, v := range clampsOutsideTheConvention {
		out[k] = v
	}
	return out
}

// ─── The gate's two verdicts ────────────────────────────────────────────────

// Violation is one thing wrong, already rendered.
type Violation struct {
	// Where is the site or ledger key the problem is about.
	Where string
	// Problem is the message, including what to do about it.
	Problem string
}

func (v Violation) String() string { return v.Where + ": " + v.Problem }

// Violations matches the scanned sites against the ledger. Every site must land
// in exactly one of three states, and everything else is reported:
//
//	disclosed                    nothing to record
//	named in disclosureWaivers   a decision, with a kind and a date
//	named in clampsOutsideTheConvention   not a caller value
//
// callerParams is the caller-facing parameter vocabulary from CallerParamNames.
// It is what stops a scope note from being a rubber stamp: a site clamping
// something spelled like a published parameter cannot be declared out of scope.
func Violations(sites []Site, waivers map[string]Waiver, notes map[string]ScopeNote, callerParams map[string]bool) []Violation {
	var out []Violation
	seenWaiver, seenNote := map[string]bool{}, map[string]bool{}

	for _, s := range sites {
		key := s.Key()
		w, waived := waivers[key]
		n, scoped := notes[key]
		if waived {
			seenWaiver[key] = true
		}
		if scoped {
			seenNote[key] = true
		}

		switch {
		case waived && scoped:
			out = append(out, Violation{s.String(), fmt.Sprintf(
				"is in BOTH ledger tables. They make incompatible claims — the waiver says the value "+
					"came from a caller and is not disclosed, the scope note says no caller value is "+
					"involved. Delete whichever is false.\n    waiver: %s / %s\n    scope note: %s",
				w.Kind, w.Reason, n.Origin)})
		case s.Disclosed && (waived || scoped):
			out = append(out, Violation{s.String(), fmt.Sprintf(
				"discloses through request_adjusted AND carries a ledger entry. The entry is now "+
					"false and will outlive the reason it was written; delete it from %s.",
				tableOf(waived))})
		case s.Disclosed, waived:
			// Nothing to report. A waiver's own fields are checked by
			// LedgerProblems, not here.
		case scoped:
			if callerParams[normalizeParam(s.Value)] {
				out = append(out, Violation{s.String(), fmt.Sprintf(
					"is declared outside the convention, but %q is a PUBLISHED REQUEST PARAMETER "+
						"(%s reads it from the query string). A scope note is not available for a "+
						"caller's own value.\n    Either disclose it — bound it in a function whose "+
						"caller appends to request_adjusted, the shape ListWorkItems and Recall use — "+
						"or move it to disclosureWaivers with a kind, a reason, a date and a citation.\n"+
						"    stated origin was: %s",
					s.Value, "internal/server/queryparam.go", n.Origin)})
			}
		default:
			out = append(out, Violation{s.String(), fmt.Sprintf(
				"clamps %s to %s and nothing tells the caller.\n"+
					"    aihub#314's convention is that every clamp appends to request_adjusted "+
					"(internal/domain/request_adjusted.go). This site does neither that nor carries a "+
					"ledger entry, so nothing accounts for it — which is the state the two clamps "+
					"before it were in when a person, not a gate, found them (aihub#432, aihub#506).\n"+
					"    Three exits, and they are not interchangeable:\n"+
					"      1. DISCLOSE — bound the value in a function whose caller appends to "+
					"request_adjusted, the way ListWorkItems wraps NormalizeListWorkItemsLimit.\n"+
					"      2. If the value never came from a caller, add to clampsOutsideTheConvention "+
					"in internal/citest/clampdisclosure/clampdisclosure.go, stating what the value IS:\n"+
					"           %q: {Origin: \"...\"},\n"+
					"      3. If it did come from a caller and will not disclose, that is a DECISION "+
					"somebody has to own. Add to disclosureWaivers with a kind, a param, a reason, a "+
					"date and a citation:\n"+
					"           %q: {Kind: ..., Param: \"...\", Reason: \"...\", Decided: \"YYYY-MM-DD\", Citation: \"...\"},",
				s.Value, s.Bound, s.Key(), s.Key())})
		}
	}

	for key := range waivers {
		if !seenWaiver[key] {
			out = append(out, Violation{key, "disclosureWaivers names a site that no longer exists (or " +
				"whose function or variable was renamed). Delete it.\n    A stale exemption is worse " +
				"than none: it exempts whatever lands in that function next, without anybody deciding to."})
		}
	}
	for key := range notes {
		if !seenNote[key] {
			out = append(out, Violation{key, "clampsOutsideTheConvention names a site that no longer " +
				"exists (or whose function or variable was renamed). Delete it."})
		}
	}

	sort.Slice(out, func(i, j int) bool { return out[i].Where < out[j].Where })
	return out
}

func tableOf(waived bool) string {
	if waived {
		return "disclosureWaivers"
	}
	return "clampsOutsideTheConvention"
}

// LedgerProblems validates the ledger's own entries, independently of what the
// tree currently contains.
//
// 🔴 The three kinds carry DIFFERENT obligations here, which is what keeps
// WaiverKind from being a decorative label: only KindPendingAdjudication must
// name a work item. A kind that demanded nothing extra could be renamed to any
// other kind with no test noticing, and the distinction aihub#411 T1-12 §6.1 drew
// — structural-and-provisional versus accepted-and-live — would exist only in
// prose again, which is the condition this package was built to end.
func LedgerProblems(waivers map[string]Waiver, notes map[string]ScopeNote) []string {
	var out []string
	for _, key := range sortedKeys(waivers) {
		w := waivers[key]
		switch w.Kind {
		case KindAcceptedContract, KindStructural, KindPendingAdjudication:
		default:
			out = append(out, fmt.Sprintf("waiver %s has kind %q, which is not one of %s/%s/%s",
				key, w.Kind, KindAcceptedContract, KindStructural, KindPendingAdjudication))
			continue
		}
		if strings.TrimSpace(w.Param) == "" {
			out = append(out, fmt.Sprintf("waiver %s names no Param. The entry has to be readable "+
				"against a request, not only against the source.", key))
		}
		if strings.TrimSpace(w.Reason) == "" {
			out = append(out, fmt.Sprintf("waiver %s states no Reason", key))
		}
		if strings.TrimSpace(w.Citation) == "" {
			out = append(out, fmt.Sprintf("waiver %s cites nothing. An undisclosed clamp is a "+
				"decision, and a decision with no record is indistinguishable from an oversight.", key))
		}
		if _, err := time.Parse("2006-01-02", w.Decided); err != nil {
			out = append(out, fmt.Sprintf("waiver %s has Decided=%q, which is not a YYYY-MM-DD date. "+
				"An undated exemption cannot be re-examined, because nothing says when it was reasonable.",
				key, w.Decided))
		}
		if w.Kind == KindPendingAdjudication && !mentionsWorkItem(w.Citation) {
			out = append(out, fmt.Sprintf("waiver %s is %s but its Citation (%q) names no work item "+
				"(expected a \"<project>#<seq>\" reference). %s — with nobody asked, this entry is the "+
				"silence the gate exists to end, wearing the gate's own badge.",
				key, KindPendingAdjudication, w.Citation, KindPendingAdjudication.Describe()))
		}
	}
	for _, key := range sortedNoteKeys(notes) {
		if strings.TrimSpace(notes[key].Origin) == "" {
			out = append(out, fmt.Sprintf("scope note %s states no Origin. The whole content of a "+
				"scope note is what the value IS; without it the entry only says \"somebody looked\".", key))
		}
	}
	sort.Strings(out)
	return out
}

// mentionsWorkItem reports whether s carries a "<project>#<seq>" reference.
func mentionsWorkItem(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] != '#' || i == 0 || i+1 >= len(s) {
			continue
		}
		if !isWordByte(s[i-1]) {
			continue
		}
		if s[i+1] >= '0' && s[i+1] <= '9' {
			return true
		}
	}
	return false
}

func isWordByte(b byte) bool {
	return b == '-' || b == '_' ||
		(b >= 'a' && b <= 'z') || (b >= 'A' && b <= 'Z') || (b >= '0' && b <= '9')
}

func sortedKeys(m map[string]Waiver) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func sortedNoteKeys(m map[string]ScopeNote) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// ─── The caller-parameter vocabulary ────────────────────────────────────────

// CallerParamNames returns the query-parameter names internal/server reads,
// taken from the string literal every reader in queryparam.go is called with.
//
// DERIVED rather than listed. A hand-kept list of "parameters that matter" is
// the whitelist shape request_adjusted.go was designed to avoid: it is edited by
// whoever remembers, and a parameter missing from it is silently unpoliced.
//
// Its reach is stated at the package doc: query parameters only.
func CallerParamNames(root string) (map[string]bool, error) {
	out := map[string]bool{}
	dir := filepath.Join(root, "internal", "server")
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", dir, err)
	}
	fset := token.NewFileSet()
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".go") || strings.HasSuffix(e.Name(), "_test.go") {
			continue
		}
		file, perr := parser.ParseFile(fset, filepath.Join(dir, e.Name()), nil, parser.SkipObjectResolution)
		if perr != nil {
			return nil, fmt.Errorf("parse %s: %w", e.Name(), perr)
		}
		ast.Inspect(file, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			id, ok := call.Fun.(*ast.Ident)
			if !ok || !strings.HasPrefix(id.Name, "query") {
				return true
			}
			for _, arg := range call.Args {
				lit, ok := arg.(*ast.BasicLit)
				if !ok || lit.Kind != token.STRING {
					continue
				}
				if name := strings.Trim(lit.Value, `"`+"`"); name != "" {
					out[normalizeParam(name)] = true
				}
			}
			return true
		})
	}
	return out, nil
}

// normalizeParam folds a parameter name and a Go identifier onto one spelling,
// so `top_k` in a query string and `topK` in the code compare equal.
func normalizeParam(s string) string {
	return strings.ToLower(strings.ReplaceAll(s, "_", ""))
}

// ─── The scanner ────────────────────────────────────────────────────────────

// ScanDir walks root and returns every clamp in a non-test .go file, sorted by
// file then line.
//
// Skipped: _test.go files, and any directory named testdata, vendor, .git or
// node_modules. Test files are out because a clamp in a test changes no
// caller's request; testdata is out because a fixture must not be reported as a
// violation of the repo it is a fixture for.
//
// 🔴 There is NO package carve-out. Scanning only the packages that serve API
// responses would be cheaper and would leave an unstated hole exactly where the
// last two clamps hid: the question a new clamp has to answer is "did this value
// come from a caller?", and a directory name is not that question's answer. The
// price is eleven one-line scope notes, paid once.
func ScanDir(root string) ([]Site, error) {
	var out []Site
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			switch d.Name() {
			case "testdata", "vendor", ".git", "node_modules":
				return fs.SkipDir
			}
			return nil
		}
		name := d.Name()
		if !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			return nil
		}
		rel, relErr := filepath.Rel(root, path)
		if relErr != nil {
			return relErr
		}
		sites, scanErr := ScanFile(path, filepath.ToSlash(rel))
		if scanErr != nil {
			return scanErr
		}
		out = append(out, sites...)
		return nil
	})
	if err != nil {
		return nil, err
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].File != out[j].File {
			return out[i].File < out[j].File
		}
		return out[i].Line < out[j].Line
	})
	return out, nil
}

// ScanFile parses one file. reportAs is the name used in the returned Sites.
func ScanFile(path, reportAs string) ([]Site, error) {
	src, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	return ScanSource(src, reportAs)
}

// ScanSource is the whole analysis, over one file's bytes. Exported so the
// recogniser's behaviour can be measured on fixtures without a repo.
func ScanSource(src []byte, reportAs string) ([]Site, error) {
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, reportAs, src, parser.SkipObjectResolution)
	if err != nil {
		return nil, fmt.Errorf("parse %s: %w", reportAs, err)
	}

	type fnInfo struct {
		display   string
		discloses bool
		calls     map[string]bool
		params    map[string]bool
	}
	// Collected over the WHOLE file before disclosure is resolved: a
	// disclosing wrapper may be declared after the function it wraps, and a
	// single pass would miss exactly those.
	infos := map[string]*fnInfo{}
	var order []string
	var sitesByFunc = map[string][]Site{}

	for _, decl := range file.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok || fn.Body == nil {
			continue
		}
		plain := fn.Name.Name
		display := plain
		if fn.Recv != nil && len(fn.Recv.List) > 0 {
			display = "(" + exprText(fn.Recv.List[0].Type) + ")." + plain
		}
		info := &fnInfo{display: display, calls: map[string]bool{}, params: paramNames(fn)}
		ast.Inspect(fn.Body, func(n ast.Node) bool {
			switch v := n.(type) {
			case *ast.CallExpr:
				if callee := calleeName(v.Fun); callee != "" {
					info.calls[callee] = true
					if callee == "appendIntAdjustment" {
						info.discloses = true
					}
				}
			case *ast.AssignStmt:
				for _, lhs := range v.Lhs {
					if sel, ok := lhs.(*ast.SelectorExpr); ok && sel.Sel.Name == requestAdjustedField {
						info.discloses = true
					}
				}
			case *ast.KeyValueExpr:
				if id, ok := v.Key.(*ast.Ident); ok && id.Name == requestAdjustedField {
					info.discloses = true
				}
			}
			return true
		})
		// A function declared twice in one file does not compile, so the plain
		// name is a unique key here.
		infos[plain] = info
		order = append(order, plain)
		sitesByFunc[plain] = clampsIn(fset, fn, display, reportAs, info.params)
	}

	var out []Site
	for _, plain := range order {
		disclosed := infos[plain].discloses
		if !disclosed {
			// Hop 2: a caller in this file that discloses. This is the shape
			// aihub#432 established at hop 1 (newReadyQueue clamps and appends)
			// widened by aihub#532 to the split shape the other two disclosing
			// sites use: Recall appends around normalizeRecallTopK, and
			// ListWorkItems appends around NormalizeListWorkItemsLimit. Without
			// hop 2 those two read as undisclosed, and a gate that reports the
			// two correct sites is a gate somebody switches off.
			for _, other := range order {
				if other != plain && infos[other].discloses && infos[other].calls[plain] {
					disclosed = true
					break
				}
			}
		}
		for _, s := range sitesByFunc[plain] {
			s.Disclosed = disclosed
			out = append(out, s)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Line < out[j].Line })
	return out, nil
}

// requestAdjustedField is the struct field every disclosing response carries.
const requestAdjustedField = "RequestAdjusted"

// clampsIn finds the clamps in one function body.
func clampsIn(fset *token.FileSet, fn *ast.FuncDecl, display, reportAs string, params map[string]bool) []Site {
	var out []Site
	add := func(pos token.Pos, value, bound string, form Form) {
		out = append(out, Site{
			File: reportAs, Line: fset.Position(pos).Line, Func: display,
			Value: value, Bound: bound, Form: form,
		})
	}
	ast.Inspect(fn.Body, func(n ast.Node) bool {
		switch v := n.(type) {
		case *ast.IfStmt:
			cond, ok := v.Cond.(*ast.BinaryExpr)
			if !ok || v.Body == nil {
				return true
			}
			switch cond.Op {
			case token.GTR, token.LSS, token.GEQ, token.LEQ:
			default:
				return true
			}
			value, bound := exprText(cond.X), exprText(cond.Y)
			// EVERY statement in the branch, not just a lone one: a clamp with a
			// log line beside it is still a clamp, and requiring the body to
			// hold nothing else would make "add a comment-worthy line" a way out
			// of the gate.
			for _, st := range v.Body.List {
				switch s := st.(type) {
				case *ast.AssignStmt:
					if len(s.Lhs) == 1 && len(s.Rhs) == 1 &&
						exprText(s.Lhs[0]) == value && exprText(s.Rhs[0]) == bound {
						add(v.Pos(), value, bound, FormIfAssign)
					}
				case *ast.ReturnStmt:
					if len(s.Results) == 1 && exprText(s.Results[0]) == bound {
						add(v.Pos(), value, bound, FormIfReturn)
					}
				}
			}
		case *ast.AssignStmt:
			if len(v.Lhs) != 1 || len(v.Rhs) != 1 {
				return true
			}
			target := exprText(v.Lhs[0])
			if value, bound, ok := minMaxClamp(v.Rhs[0], func(arg string) bool { return arg == target }); ok {
				add(v.Pos(), value, bound, FormMinMax)
			}
		case *ast.ReturnStmt:
			if len(v.Results) != 1 {
				return true
			}
			if value, bound, ok := minMaxClamp(v.Results[0], func(arg string) bool { return params[arg] }); ok {
				add(v.Pos(), value, bound, FormMinMax)
			}
		}
		return true
	})
	return out
}

// minMaxClamp recognises min/max/math.Min/math.Max over exactly two arguments,
// where isValue picks which argument is the value being clamped.
//
// The two callers pass different pickers, and the difference is the whole
// reason this is a parameter. In `v = min(v, CEIL)` the value is the assignment
// TARGET, which is unambiguous. In `return min(v, CEIL)` there is no target, so
// the value is taken to be whichever argument is a PARAMETER of the enclosing
// function — the shape a normalizer has. If neither or both qualify the site is
// still reported, with the first argument as the value, because a clamp nobody
// can name is worse than one named imprecisely.
func minMaxClamp(e ast.Expr, isValue func(string) bool) (value, bound string, ok bool) {
	call, isCall := e.(*ast.CallExpr)
	if !isCall || len(call.Args) != 2 {
		return "", "", false
	}
	switch fn := call.Fun.(type) {
	case *ast.Ident:
		if fn.Name != "min" && fn.Name != "max" {
			return "", "", false
		}
	case *ast.SelectorExpr:
		base, isIdent := fn.X.(*ast.Ident)
		if !isIdent || base.Name != "math" || (fn.Sel.Name != "Min" && fn.Sel.Name != "Max") {
			return "", "", false
		}
	default:
		return "", "", false
	}
	a, b := exprText(call.Args[0]), exprText(call.Args[1])
	switch {
	case isValue(a) && !isValue(b):
		return a, b, true
	case isValue(b) && !isValue(a):
		return b, a, true
	case isValue(a) && isValue(b):
		return a, b, true
	}
	return "", "", false
}

// paramNames returns the names of fn's parameters and named results.
func paramNames(fn *ast.FuncDecl) map[string]bool {
	out := map[string]bool{}
	collect := func(fl *ast.FieldList) {
		if fl == nil {
			return
		}
		for _, f := range fl.List {
			for _, n := range f.Names {
				out[n.Name] = true
			}
		}
	}
	if fn.Type != nil {
		collect(fn.Type.Params)
		collect(fn.Type.Results)
	}
	return out
}

// calleeName is the plain name of a called function: `f` for f(), `Pkg.F` and
// `x.F` both give `F`. Coarse on purpose — the disclosure resolver only needs to
// know whether a name was called in this file.
func calleeName(fun ast.Expr) string {
	switch v := fun.(type) {
	case *ast.Ident:
		return v.Name
	case *ast.SelectorExpr:
		return v.Sel.Name
	}
	return ""
}

// exprText renders an expression to a stable string for comparison. It is not
// go/printer output: the comparison only has to be consistent within one file,
// and a hand-rolled renderer keeps the comparison free of formatting choices.
func exprText(e ast.Expr) string {
	var sb strings.Builder
	writeExpr(&sb, e)
	return sb.String()
}

func writeExpr(sb *strings.Builder, e ast.Expr) {
	switch v := e.(type) {
	case *ast.Ident:
		sb.WriteString(v.Name)
	case *ast.SelectorExpr:
		writeExpr(sb, v.X)
		sb.WriteString("." + v.Sel.Name)
	case *ast.BasicLit:
		sb.WriteString(v.Value)
	case *ast.CallExpr:
		writeExpr(sb, v.Fun)
		sb.WriteString("(")
		for i, a := range v.Args {
			if i > 0 {
				sb.WriteString(",")
			}
			writeExpr(sb, a)
		}
		sb.WriteString(")")
	case *ast.IndexExpr:
		writeExpr(sb, v.X)
		sb.WriteString("[")
		writeExpr(sb, v.Index)
		sb.WriteString("]")
	case *ast.BinaryExpr:
		writeExpr(sb, v.X)
		sb.WriteString(v.Op.String())
		writeExpr(sb, v.Y)
	case *ast.UnaryExpr:
		sb.WriteString(v.Op.String())
		writeExpr(sb, v.X)
	case *ast.ParenExpr:
		sb.WriteString("(")
		writeExpr(sb, v.X)
		sb.WriteString(")")
	case *ast.StarExpr:
		sb.WriteString("*")
		writeExpr(sb, v.X)
	default:
		fmt.Fprintf(sb, "<%T>", e)
	}
}
