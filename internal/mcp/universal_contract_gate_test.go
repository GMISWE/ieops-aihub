package mcp_test

// aihub#419 — THE layer-1 universal contract gate.
//
// ─── What replaces what ────────────────────────────────────────────────────
//
// Four gates already in this package each quantify over ONE tool:
//
//	ready_queue_param_wiring_test.go   (#387)  pf_get_ready_queue, hops 1 vs 3
//	claim_param_contract_test.go       (#394)  pf_claim_work_item, hop 4
//	claim_response_projection_test.go  (#388)  pf_claim_work_item, the response
//	unknown_params_test.go             (#389)  every tool, one mechanism
//
// Between them they found four contract defects in one week, each by hand, each
// after the tool had shipped. The measured size of the surface they audit is 50
// tools and 237 published parameters, so a per-tool audit is not a plan — and it
// covers nothing added after the audit. This file keeps their mechanisms and
// changes the QUANTIFIER: every tool the registry publishes, every parameter it
// publishes, measured the day it is added.
//
// It deliberately REUSES their derivations rather than restating them —
// serverASTFiles, requestReaderFuncs (the fixpoint that derives package server's
// own query-parameter readers), readerCallParam and domainASTFiles are all
// declared next door in package mcp_test and are called from here. A second copy
// of the fixpoint would be a second thing to keep true.
//
// ─── The four gates, and the hop each one can see ──────────────────────────
//
//	G1 PARAM_NOT_FORWARDED     hop 1→2  a published parameter must leave this
//	                                    process, or be consumed here on purpose
//	G2 PATH_NOT_ROUTED         hop 2→3  a URL this process calls must be a URL
//	                                    package server routes
//	G3 KEEP_LIST_PROJECTION    response a field the server sends that no struct
//	                                    here knows about must still reach the model
//	G4 BOUND_FIELD_UNPUBLISHED hop 3 ←  a name the server's handler binds or reads
//	                                    must be reachable by some MCP caller
//
// 🔴 What this file does NOT measure, stated because the obvious reading is
// wrong: #394's hop 4, "the bound field is ACTED ON". That census is
// intra-function over one named domain function, and quantifying it over 50
// tools needs a tool→domain-function map — a hand-written list, which is the
// thing this file exists to replace. **A parameter can pass all four gates here
// and still be inert.** claim_param_contract_test.go remains the instrument for
// that question, and extending it to another tool is still worth doing.
//
// ─── Why the discriminator is the RECORDED REQUEST, not the source text ────
//
// G1 could have been written as a source scan for the parameter's name in the
// handler. It is not, for the reason #389's header gives about its own
// structural half: a scan can be fooled by a helper, a loop or a rename, and it
// answers "is the name mentioned" when the question is "does the argument leave
// the process". So every G1 verdict comes from an httptest server that recorded
// what this process actually sent — method, path, RAW QUERY STRING and body.
//
// The query string is why this file carries its own recorder instead of using
// tools_fusion_test.go's: that one records path and body only, and pf_recall,
// pf_list_work_items, pf_read_events and pf_get_ready_queue carry their entire
// parameter set in the query. Against a body-only recorder every one of their
// parameters would read as "not forwarded" — a gate reporting 60 false
// violations gets switched off in a week.
//
// ─── Baseline, and why a stale entry FAILS ─────────────────────────────────
//
// testdata/universal_contract_baseline.json records the violations that were
// already there on the day this landed, each with the wi that owns the fix. The
// gate fails on a violation with no entry — the ratchet — AND on an entry that
// matches no violation. The second half is the one that is usually left out: an
// exemption that outlives its defect is an exemption nobody will ever remove,
// and it silently excuses the NEXT instance of the same defect in the same
// place. There are no waiting exemptions here; a fixed violation must have its
// line deleted in the same change.
//
// No database needed:
//
//	go test ./internal/mcp/ -run TestContract -v

import (
	"context"
	"encoding/json"
	"fmt"
	"go/ast"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"
	"unicode"

	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/GMISWE/ieops-aihub/internal/mcp"
	"github.com/GMISWE/ieops-aihub/pkg/client"
)

// ─────────────────────────────── floor arms ──────────────────────────────────

// The liveness floors, in one place so they read as a set. Every one of them
// exists because the gate above it REPORTS violations, and a scanner that sees
// nothing exits 0 exactly like a clean repo: "no violations" is the same verdict
// whether the toolset is honest or the walk is broken. Each number is far below
// what is measured today (the values in the comments) and far above zero, so it
// fails on a broken harness rather than on ordinary growth.
const (
	floorTools       = 50  // measured 2026-09-07: 50
	floorParams      = 200 // measured: 237 across those 50
	floorRoutes      = 40  // measured: 80 route registrations in package server
	floorToolsOnWire = 25  // measured: 46 of 50 tools make at least one HTTP call
	floorProjections = 20  // measured: 46 tool results G3 could inspect
	floorBoundFields = 20  // measured: 278 server-side names G4 resolved

	// floorStrongParams bounds how much of G1 may rest on the WEAKER of its two
	// measurements. A token-proved verdict says "this argument's own value left
	// the process"; a key-presence verdict says only "a key of that name was in
	// the request", which a handler writing that key from config would satisfy
	// with the argument doing nothing. Without a floor the gate could silently
	// degrade to all-key-presence — every arm still green, nothing red, and the
	// discriminating half gone.
	floorStrongParams = 120 // measured: 203 of 237 verdicts token-proved
)

// ─────────────────────────── the baseline & its shape ────────────────────────

const contractBaselinePath = "testdata/universal_contract_baseline.json"

// The gate names. A baseline entry naming anything else is checked by
// TestContractBaselineFileIsWellFormed — an entry under a misspelt gate would
// otherwise be an exemption no gate ever looks at and no gate ever calls stale,
// which is the one shape of dead exemption this file's staleness rule cannot
// catch by itself (each gate only audits its own entries).
const (
	gateParamNotForwarded     = "PARAM_NOT_FORWARDED"
	gatePathNotRouted         = "PATH_NOT_ROUTED"
	gateKeepListProjection    = "KEEP_LIST_PROJECTION"
	gateBoundFieldUnpublished = "BOUND_FIELD_UNPUBLISHED"
)

func knownGates() map[string]bool {
	return map[string]bool{
		gateParamNotForwarded:     true,
		gatePathNotRouted:         true,
		gateKeepListProjection:    true,
		gateBoundFieldUnpublished: true,
	}
}

// contractBaselineEntry is one recorded pre-existing violation. Shape borrowed
// from scripts/pf_contract_lint_baseline.json, including its rule that every
// entry MUST carry a wi: a baseline line without an owner is a decision to live
// with the defect disguised as a decision to fix it later.
type contractBaselineEntry struct {
	Gate    string `json:"gate"`
	Tool    string `json:"tool"`
	Subject string `json:"subject"`
	WI      string `json:"wi"`
	Note    string `json:"note"`
}

func (e contractBaselineEntry) key() string { return e.Gate + "\x00" + e.Tool + "\x00" + e.Subject }

// contractBaseline is a loaded baseline plus the bookkeeping that makes stale
// entries detectable: every lookup marks its entry used, and each gate asserts
// that all of ITS entries were used.
type contractBaseline struct {
	entries []contractBaselineEntry
	byKey   map[string]contractBaselineEntry
	used    map[string]bool
}

func loadContractBaseline(t *testing.T) *contractBaseline {
	t.Helper()
	raw, err := os.ReadFile(contractBaselinePath)
	if err != nil {
		t.Fatalf("read %s: %v — the baseline file is part of the gate, not an optional "+
			"companion to it; without it every pre-existing violation reads as new and the "+
			"gate is red for reasons nobody in this change caused", contractBaselinePath, err)
	}
	var entries []contractBaselineEntry
	if err := json.Unmarshal(raw, &entries); err != nil {
		t.Fatalf("parse %s: %v", contractBaselinePath, err)
	}
	b := &contractBaseline{
		entries: entries,
		byKey:   make(map[string]contractBaselineEntry, len(entries)),
		used:    map[string]bool{},
	}
	for _, e := range entries {
		b.byKey[e.key()] = e
	}
	return b
}

// baselined reports whether this violation is recorded, and marks the entry
// used so the staleness check can see it.
func (b *contractBaseline) baselined(gate, tool, subject string) (contractBaselineEntry, bool) {
	k := contractBaselineEntry{Gate: gate, Tool: tool, Subject: subject}.key()
	e, ok := b.byKey[k]
	if ok {
		b.used[k] = true
	}
	return e, ok
}

// assertNoStaleEntries is the half that makes this a ratchet in both
// directions. Scoped to one gate because the four gates are four test
// functions: each audits its own entries, and TestContractBaselineFileIsWellFormed
// checks that no entry names a gate outside the four.
func (b *contractBaseline) assertNoStaleEntries(t *testing.T, gate string) {
	t.Helper()
	for _, e := range b.entries {
		if e.Gate != gate || b.used[e.key()] {
			continue
		}
		t.Errorf("stale baseline entry: %s / %s / %s (wi %s) matched no violation this run.\n"+
			"Either the defect is fixed — in which case DELETE this line, in the same change "+
			"that fixed it — or the gate no longer looks where the entry points, which is worse "+
			"than the original defect because it is an exemption with nothing behind it. Note "+
			"recorded: %s", e.Gate, e.Tool, e.Subject, e.WI, e.Note)
	}
}

// ──────────────────────── the recording fake aihub ───────────────────────────

// contractRecordedCall is one outbound HTTP request, as the far end saw it.
// RawQuery is recorded and not merely the path: see the header.
type contractRecordedCall struct {
	Method   string
	Path     string
	RawQuery string
	Body     map[string]any
	BodyRaw  string
}

// contractFake answers EVERY path with the same payload, and records every
// request. Answering everything is deliberate: this gate iterates the whole
// registry, so a per-path handler table would silently become the list of tools
// the gate covers.
type contractFake struct {
	server  *httptest.Server
	mu      sync.Mutex
	calls   []contractRecordedCall
	payload map[string]any
}

func newContractFake(t *testing.T, payload map[string]any) *contractFake {
	t.Helper()
	f := &contractFake{payload: payload}
	f.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		var raw strings.Builder
		dec := json.NewDecoder(io.TeeReader(r.Body, &raw))
		_ = dec.Decode(&body)

		f.mu.Lock()
		f.calls = append(f.calls, contractRecordedCall{
			Method: r.Method, Path: r.URL.Path, RawQuery: r.URL.RawQuery,
			Body: body, BodyRaw: raw.String(),
		})
		payload := f.payload
		f.mu.Unlock()

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(payload)
	}))
	t.Cleanup(f.server.Close)
	return f
}

func (f *contractFake) reset() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = nil
}

func (f *contractFake) recordedCalls() []contractRecordedCall {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]contractRecordedCall, len(f.calls))
	copy(out, f.calls)
	return out
}

// requestText is the canonical serialisation a probe token is looked for in:
// method, path, query and body together. All four hops a parameter could ride
// on are in one string, so the search cannot miss a parameter merely because it
// travelled somewhere the assertion did not think to look.
func (c contractRecordedCall) requestText() string {
	return c.Method + " " + c.Path + "?" + c.RawQuery + " " + c.BodyRaw
}

// bodyKeys returns the body's keys, descending one level into nested objects
// and arrays-of-objects. One level, because a parameter forwarded as a field of
// a sub-object (session_info, items[]) is still forwarded, while descending
// without limit would start matching values that happen to be maps.
func (c contractRecordedCall) bodyKeys() map[string]bool {
	out := map[string]bool{}
	var add func(m map[string]any, depth int)
	add = func(m map[string]any, depth int) {
		for k, v := range m {
			out[k] = true
			if depth == 0 {
				continue
			}
			switch vv := v.(type) {
			case map[string]any:
				add(vv, depth-1)
			case []any:
				for _, e := range vv {
					if em, ok := e.(map[string]any); ok {
						add(em, depth-1)
					}
				}
			}
		}
	}
	add(c.Body, 1)
	return out
}

func (c contractRecordedCall) queryKeys() map[string]bool {
	out := map[string]bool{}
	for _, pair := range strings.Split(c.RawQuery, "&") {
		if pair == "" {
			continue
		}
		k, _, _ := strings.Cut(pair, "=")
		out[k] = true
	}
	return out
}

// ───────────────────────── the live registry session ─────────────────────────

// contractHarness is one MCP server, one client session and one recording fake,
// reused across every probe of one tool. Reused rather than rebuilt per
// parameter because 237 parameters × (server + session + httptest) is the
// difference between a gate that runs in CI and one that times out.
type contractHarness struct {
	fake     *contractFake
	session  *sdkmcp.ClientSession
	worktree string // the claimed worktree the coding tools act on
}

func newContractHarness(t *testing.T, payload map[string]any) *contractHarness {
	t.Helper()
	f := newContractFake(t, payload)
	server := mcp.New(nil, client.New(f.server.URL, "test-key"))
	cTransport, sTransport := sdkmcp.NewInMemoryTransports()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go func() {
		session, err := server.Connect(ctx, sTransport)
		if err != nil {
			return
		}
		_ = session.Wait()
	}()
	cl := sdkmcp.NewClient(&sdkmcp.Implementation{Name: "aihub419-contract", Version: "1.0.0"}, nil)
	session, err := cl.Connect(context.Background(), cTransport, nil)
	if err != nil {
		t.Fatalf("client connect: %v", err)
	}
	t.Cleanup(func() { _ = session.Close() })
	return &contractHarness{fake: f, session: session}
}

// newContractGate is the setup all four gates share: an isolated workspace with
// a real git clone, a recording fake, a live session, and a claim already made
// so the coding tools have a worktree to act on.
func newContractGate(t *testing.T) (*contractHarness, []*sdkmcp.Tool) {
	t.Helper()
	w := newClaimWorkspace(t)
	h := newContractHarness(t, contractProbePayload())
	// The path the seed claim below will create, derived from the payload's own
	// slug: project "aihub", seq 419 (see contractProbePayload).
	h.worktree = filepath.Join(w.root, "pf.aihub-419", "aihub")
	tools := h.tools(t)
	seedClaimedWorktree(t, h)
	return h, tools
}

// dirty leaves an uncommitted file in the probe worktree.
//
// Not cosmetic: pf_commit answers "nothing to commit, working tree clean" and
// makes no request at all against a clean tree, so without this its five
// parameters — and pf_ship's and pf_wrap's commit phase — are unmeasurable for a
// reason the harness caused. A unique name each time, because the previous probe
// may have committed the last one.
func (h *contractHarness) dirty(t *testing.T) {
	t.Helper()
	if h.worktree == "" {
		return
	}
	if _, err := os.Stat(h.worktree); err != nil {
		return // the claim has not created it yet
	}
	dirtyCounter++
	name := filepath.Join(h.worktree, fmt.Sprintf("pf419-dirty-%d.txt", dirtyCounter))
	if err := os.WriteFile(name, []byte("pf419"), 0o644); err != nil {
		t.Fatalf("dirty the probe worktree: %v", err)
	}
}

var dirtyCounter int

// tools lists the whole published registry, with the floor arm attached.
func (h *contractHarness) tools(t *testing.T) []*sdkmcp.Tool {
	t.Helper()
	var out []*sdkmcp.Tool
	for tool, err := range h.session.Tools(context.Background(), nil) {
		if err != nil {
			t.Fatalf("tools iteration: %v", err)
		}
		out = append(out, tool)
	}
	if len(out) < floorTools {
		t.Fatalf("tools/list published %d tool(s), floor is %d — this is the session being "+
			"broken, not the toolset shrinking, and every assertion quantified over it would "+
			"be vacuous", len(out), floorTools)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// call invokes one tool and hands back the recorded requests it caused plus its
// decoded result. The recorder is reset first, so what comes back belongs to
// this call and not to the previous parameter's probe.
func (h *contractHarness) call(t *testing.T, tool string, args map[string]any) ([]contractRecordedCall, map[string]any, bool) {
	t.Helper()
	h.fake.reset()
	res, err := h.session.CallTool(context.Background(), &sdkmcp.CallToolParams{
		Name: tool, Arguments: args,
	})
	if err != nil {
		// Transport-level failure means the call never reached the handler, so
		// this probe measured nothing. It must read as neither a pass nor as the
		// defect.
		t.Fatalf("transport error calling %s: %v", tool, err)
	}
	var decoded map[string]any
	for _, c := range res.Content {
		tc, ok := c.(*sdkmcp.TextContent)
		if !ok {
			continue
		}
		var m map[string]any
		if err := json.Unmarshal([]byte(tc.Text), &m); err == nil {
			decoded = m
			break
		}
	}
	return h.fake.recordedCalls(), decoded, res.IsError
}

// ──────────────────────── published parameters, per tool ─────────────────────

// contractParam is one published parameter as the schema describes it. Enum is
// read from the schema rather than tabulated, so a probe for an enum parameter
// sends a LEGAL value without anybody maintaining a list — which matters
// because `type` is an enum on two tools with two disjoint vocabularies
// (memory types vs methodology.* artifact types), and a name-keyed table would
// have to get that right by hand.
type contractParam struct {
	Name string
	Type string
	// []any, not []string: a numeric or boolean enum would fail to decode into
	// []string and take the whole schema read down with it, turning "somebody
	// added an integer enum" into "this gate cannot read pf_x's InputSchema".
	Enum     []any
	Required bool
}

func contractParamsOf(t *testing.T, tool *sdkmcp.Tool) map[string]contractParam {
	t.Helper()
	raw, err := json.Marshal(tool.InputSchema)
	if err != nil {
		t.Fatalf("marshal InputSchema for %s: %v", tool.Name, err)
	}
	var schema struct {
		Properties map[string]struct {
			Type string `json:"type"`
			Enum []any  `json:"enum"`
		} `json:"properties"`
		Required []string `json:"required"`
	}
	if err := json.Unmarshal(raw, &schema); err != nil {
		t.Fatalf("decode InputSchema for %s: %v", tool.Name, err)
	}
	required := map[string]bool{}
	for _, r := range schema.Required {
		required[r] = true
	}
	out := map[string]contractParam{}
	for name, p := range schema.Properties {
		out[name] = contractParam{Name: name, Type: p.Type, Enum: p.Enum, Required: required[name]}
	}
	return out
}

func sortedParamNames(params map[string]contractParam) []string {
	out := make([]string, 0, len(params))
	for n := range params {
		out = append(out, n)
	}
	sort.Strings(out)
	return out
}

// ───────────────────────────── probe values ──────────────────────────────────

// The probe token prefixes. The parameter UNDER TEST always carries
// probeTokenPrefix and every other argument carries fillTokenPrefix, so finding
// the probe token in the recorded request proves THAT parameter travelled — not
// merely that some argument did, and not that the handler happens to always
// send a key of that name.
const (
	probeTokenPrefix = "pf419probe-"
	fillTokenPrefix  = "pf419fill-"
)

// elementShapes are the array/object parameters whose ELEMENT shape is
// validated somewhere on the way out, so a bare string would be rejected before
// the request is built and the probe would measure nothing.
//
// Each shape still carries the probe token, which is what keeps the table from
// weakening the gate: the token is what the assertion looks for, so a shape
// entry cannot make a parameter look forwarded when it is not.
//
// Checked non-stale by TestContractProbeTableIsNotStale: an entry naming a
// parameter no tool publishes is a dead line, and a dead line here is a
// parameter silently probed with the wrong shape once the name comes back.
var elementShapes = map[string]func(token string) any{
	"declared_resources": func(tok string) any {
		return []any{map[string]any{"type": "path", "uri": "file:" + tok, "intent": "write"}}
	},
	"requested_locks": func(tok string) any {
		return []any{map[string]any{"resource_type": "file_scope", "resource_key": tok}}
	},
	"items": func(tok string) any {
		return []any{map[string]any{"goal": tok, "wi_type": "chore"}}
	},
	"members": func(tok string) any {
		return []any{map[string]any{"user_id": tok, "role": "writer"}}
	},
	"attrs": func(tok string) any { return map[string]any{"pf419": tok} },
	"attrs_patch": func(tok string) any {
		return map[string]any{"pf419": tok}
	},
	"payload":            func(tok string) any { return map[string]any{"pf419": tok} },
	"structured_payload": func(tok string) any { return map[string]any{"pf419": tok} },
}

// semanticValues are the string parameters whose VALUE has to be plausible for
// the call to get as far as building a request — an id shape the handler parses,
// a repo that exists in the workspace, a status the handler switches on.
//
// The token is appended where the value tolerates it and omitted where it does
// not; a parameter whose value cannot carry the token falls back to key-presence
// detection, and probeValue reports which of the two applies.
var semanticValues = map[string]string{
	"work_item_id":   "wi_01PF419PROBE",
	"blocked_wi_id":  "wi_01PF419BLOCKD",
	"blocking_wi_id": "wi_01PF419BLOCKG",
	"memory_id":      "mem_pf419probe",
	"commit_id":      "mc_pf419probe",
	"user_id":        "u_pf419probe",
	"id":             "u_pf419probe",
	"key_id":         "ak_pf419probe",
	"repo":           "aihub",
	"project":        "pf419project",
	"name":           "pf419name",
	"status":         "wrapped",
	"kind":           "pf419kind",
	"event_type":     "note",
	"visibility":     "project",
	"wi_type":        "chore",
	"role":           "writer",
}

// semanticValuesNotDistinctive names the semanticValues entries whose value
// cannot serve as its own probe token, because the value is a word that occurs
// in the request for other reasons ("aihub" is both the project and the repo) or
// is one of a handful of legal constants. Everything else in semanticValues is
// distinctive enough to be searched for verbatim, which is what makes the
// path-borne ids measurable at all.
//
// Checked against semanticValues by TestContractProbeTableIsNotStale: a name
// here that semanticValues does not define would weaken detection for a
// parameter nobody meant to weaken it for.
var semanticValuesNotDistinctive = map[string]bool{
	// `repo` alone: it must name a repo the probe workspace's .polyforge.yaml
	// declares, so it cannot be a made-up token, and "aihub" also occurs in the
	// request for other reasons.
	"repo": true,
	// The legal-constant parameters. Each is a value the handler switches on, so
	// it cannot carry a token; each also travels in the BODY, where key-presence
	// plus a control is a sound measurement.
	"status": true, "event_type": true,
	"visibility": true, "wi_type": true, "role": true,
}

// semanticValuesByTool overrides semanticValues for one tool, keyed
// "<tool>.<param>".
//
// One entry, and it is load-bearing: `status` means different things to
// pf_complete_attempt ("wrapped") and pf_update_step ("in_progress|completed|
// failed"), and pf_update_step's handler only forwards next_step when status is
// "completed" — its schema says so. Probed with the global "wrapped" the gate
// reported next_step and next_step_attempt_id unforwarded, having measured the
// path on which they correctly are not sent.
//
// Checked non-stale by TestContractProbeTableIsNotStale.
var semanticValuesByTool = map[string]string{
	"pf_update_step.status": "completed",
}

// probeValue invents the value one parameter is probed with, and returns the
// TOKEN that proves that value travelled — empty when the value cannot carry
// one, which routes the verdict to key-presence plus a control.
//
// A parameter whose declared type this cannot shape is a FATAL, never a skip.
// That direction is the whole point: a silently unprobed parameter is exactly
// the hole this gate exists to close, and it would be invisible — the gate would
// report green over a parameter it never sent.
func probeValue(t *testing.T, tool string, p contractParam, underTest bool) (any, string) {
	t.Helper()
	prefix := fillTokenPrefix
	if underTest {
		prefix = probeTokenPrefix
	}
	tok := prefix + p.Name

	if v, ok := semanticValuesByTool[tool+"."+p.Name]; ok {
		if semanticValuesNotDistinctive[p.Name] {
			return v, ""
		}
		return v, v
	}
	if shape, ok := elementShapes[p.Name]; ok {
		return shape(tok), tok
	}
	if len(p.Enum) > 0 {
		// A legal value, and it cannot carry the token — enum membership is the
		// point. Key-presence plus a control covers it.
		return p.Enum[0], ""
	}
	switch p.Type {
	case "string":
		if v, ok := semanticValues[p.Name]; ok {
			// The value has to stay parseable — an id the handler splits, a repo the
			// workspace declares — so the token cannot be appended. Where the value
			// is itself distinctive it IS the token, and that is not a nicety: a
			// parameter that travels in the URL PATH (work_item_id, memory_id and
			// every other id here) appears in no body and no query, so key-presence
			// cannot see it at all and would report every one of them unforwarded.
			// The three values that are not distinctive — repo, project, name, all
			// necessarily "aihub" because the probe workspace declares exactly one
			// repo in one project — fall through to key-presence.
			if semanticValuesNotDistinctive[p.Name] {
				return v, ""
			}
			return v, v
		}
		return tok, tok
	case "boolean":
		return true, ""
	case "number", "integer":
		return float64(7), ""
	case "array":
		return []any{tok}, tok
	case "object":
		return map[string]any{"pf419": tok}, tok
	}
	t.Fatalf("%s publishes %q with declared type %q, which probeValue cannot shape. Extend it: "+
		"a parameter this table cannot build a value for is a parameter the gate never sends, "+
		"and the gate would then report green over it.", tool, p.Name, p.Type)
	return nil, ""
}

// exclusiveParams records the published parameters a tool refuses to accept
// TOGETHER, so the maximal probe rung does not defeat itself.
//
// Measured, not guessed: pf_save_artifact answers "provide content OR path, not
// both", and a maximal argument set therefore made NO request — which reported
// six of its eight parameters unforwarded when every one of them is forwarded
// correctly with a legal argument set. Six fake defects from one line of
// validation.
//
// Both directions of each pair are listed so the table reads the same whichever
// member is under test, and every name is checked published by
// TestContractProbeTableIsNotStale.
var exclusiveParams = map[string][]string{
	"content":     {"path"},
	"path":        {"content"},
	"attrs":       {"attrs_patch", "attrs_unset"},
	"attrs_patch": {"attrs"},
	"attrs_unset": {"attrs"},
}

// suppressorParams are parameters that make the handler ignore the others, so
// the maximal rung must leave them out unless they are the one under test.
//
// Also measured: pf_update_step's own schema says heartbeat=true "returns early
// and DISCARDS step_id/status", and with it in the maximal set the gate reported
// next_step and next_step_attempt_id unforwarded — it had measured the heartbeat
// path, on which they genuinely are not sent, and called that the contract.
var suppressorParams = map[string]string{
	"heartbeat": "pf_update_step returns early on heartbeat=true and discards every other " +
		"argument, so including it measures the heartbeat path rather than the tool",
}

// probeArgsFull fills EVERY published parameter, with the token on the one under
// test, minus whatever the parameter under test excludes and minus the
// suppressors. The parameter under test is placed FIRST so nothing can exclude
// it — the whole point of the rung is to probe that one.
func probeArgsFull(t *testing.T, tool string, params map[string]contractParam, underTest string) (map[string]any, string) {
	t.Helper()
	order := make([]string, 0, len(params))
	if _, ok := params[underTest]; ok {
		order = append(order, underTest)
	}
	for _, name := range sortedParamNames(params) {
		if name != underTest {
			order = append(order, name)
		}
	}

	args := map[string]any{}
	added := map[string]bool{}
	token := ""
	for _, name := range order {
		if name != underTest {
			if _, suppresses := suppressorParams[name]; suppresses {
				continue
			}
			conflict := false
			for _, other := range exclusiveParams[name] {
				if added[other] {
					conflict = true
				}
			}
			if conflict {
				continue
			}
		}
		v, tok := probeValue(t, tool, params[name], name == underTest)
		args[name] = v
		added[name] = true
		if name == underTest {
			token = tok
		}
	}
	return args, token
}

// probeArgs builds the argument map for one probe: every REQUIRED parameter
// filled, plus the parameter under test.
func probeArgs(t *testing.T, tool string, params map[string]contractParam, underTest string) (map[string]any, string) {
	t.Helper()
	args := map[string]any{}
	token := ""
	for _, name := range sortedParamNames(params) {
		p := params[name]
		if !p.Required && name != underTest {
			continue
		}
		v, tok := probeValue(t, tool, p, name == underTest)
		args[name] = v
		if name == underTest {
			token = tok
		}
	}
	return args, token
}

// probeArgsFor escalates: the minimal argument set first, the maximal one only
// if the minimal produced no request at all.
//
// Why two rungs and not just the maximal set. `required` in the published schema
// is not the same claim as "enough to get a request built": pf_read_events
// declares NOTHING required and still needs a work_item_id or a project before
// the handler will call out, so a required-only probe of its `limit` sent one
// argument, got an error, and reported `limit` unforwarded — five fake defects
// from one tool. But the maximal set cannot be the FIRST rung either: several
// tools publish parameters that are mutually exclusive by contract
// (pf_update_work_item's attrs vs attrs_patch is rejected with a 400), so
// sending everything is itself a way to get no request. Minimal, then maximal,
// then report — and the report is what is left after both.
func probeArgsFor(t *testing.T, h *contractHarness, tool string, params map[string]contractParam,
	underTest string) ([]contractRecordedCall, string) {
	t.Helper()
	args, token := probeArgs(t, tool, params, underTest)
	calls, _, _ := h.callSeeded(t, tool, args)
	if len(calls) > 0 {
		return calls, token
	}
	fullArgs, fullToken := probeArgsFull(t, tool, params, underTest)
	calls, _, _ = h.callSeeded(t, tool, fullArgs)
	return calls, fullToken
}

// controlValue returns the value the CONTROL call sends for a parameter whose
// probe value cannot carry a token, and reports whether a control is possible.
//
// The control is what turns key-presence from a coincidence into a measurement.
// A handler that always writes a key of that name into the body — from config,
// from the state file, from a default — makes "the key is present" green with
// the argument doing nothing, which is aihub#394's signature one hop earlier. So
// for these parameters the verdict is: the key is present in the probe AND the
// control did not produce the identical key/value pair.
func controlValue(p contractParam) (any, bool, bool) {
	if !p.Required {
		// The strongest control available: send the call without the parameter at
		// all. If the key is in the body anyway, the handler is not putting it
		// there on the caller's behalf.
		return nil, true, true // omit
	}
	if len(p.Enum) > 1 {
		return p.Enum[len(p.Enum)-1], false, true
	}
	switch p.Type {
	case "boolean":
		return false, false, true
	case "number", "integer":
		return float64(11), false, true
	}
	if v, ok := semanticControlValues[p.Name]; ok {
		return v, false, true
	}
	return nil, false, false
}

// semanticControlValues are second legal values for the required string
// parameters whose first value has to be semantically plausible. Only the ones
// that HAVE a second legal value are here; `repo`, `project` and `name` do not
// (the probe workspace declares exactly one repo in one project, and a value
// outside it changes which error the handler returns rather than which value it
// forwards), so those fall back to key-presence and are counted in the log.
var semanticControlValues = map[string]string{
	"status":     "paused",
	"event_type": "progress",
	"visibility": "private",
	"wi_type":    "feature",
	"role":       "reader",
}

// forwardVerdict is how a parameter was judged, kept so the log can report which
// measurement each verdict rests on rather than only the verdict.
type forwardVerdict struct {
	forwarded bool
	how       string
	strong    bool // true when a unique token proved it, not key-presence
}

// paramReachedTheWire is the G1 decision procedure.
func paramReachedTheWire(t *testing.T, h *contractHarness, tool string, p contractParam,
	params map[string]contractParam, probeCalls []contractRecordedCall, token string) forwardVerdict {
	t.Helper()

	if token != "" {
		for _, c := range probeCalls {
			if strings.Contains(c.requestText(), token) {
				return forwardVerdict{forwarded: true, how: "token in " + c.Method + " " + c.Path, strong: true}
			}
		}
		return forwardVerdict{forwarded: false, how: "token absent from every request", strong: true}
	}

	present, probeVal := keyInCalls(probeCalls, p.Name)
	if !present {
		return forwardVerdict{forwarded: false, how: "no request carries a key of that name"}
	}

	ctlVal, omit, haveControl := controlValue(p)
	if !haveControl {
		return forwardVerdict{forwarded: true, how: "key-presence only (no control value exists)"}
	}
	ctlArgs, _ := probeArgs(t, tool, params, "")
	if omit {
		delete(ctlArgs, p.Name)
	} else {
		ctlArgs[p.Name] = ctlVal
	}
	ctlCalls, _, _ := h.callSeeded(t, tool, ctlArgs)
	if len(ctlCalls) == 0 {
		ctlArgs, _ = probeArgsFull(t, tool, params, "")
		if omit {
			delete(ctlArgs, p.Name)
		} else {
			ctlArgs[p.Name] = ctlVal
		}
		ctlCalls, _, _ = h.callSeeded(t, tool, ctlArgs)
	}
	ctlPresent, ctlValSeen := keyInCalls(ctlCalls, p.Name)
	if !ctlPresent || ctlValSeen != probeVal {
		return forwardVerdict{forwarded: true, how: "key present, control differs", strong: true}
	}
	return forwardVerdict{forwarded: false,
		how: fmt.Sprintf("key present with value %q in BOTH probe and control — the handler "+
			"writes that key whatever the argument says", probeVal)}
}

// keyInCalls reports whether any recorded request carries a key of that name,
// and the value it carried, as text.
func keyInCalls(calls []contractRecordedCall, name string) (bool, string) {
	for _, c := range calls {
		if c.Body != nil {
			if v, ok := c.Body[name]; ok {
				return true, fmt.Sprint(v)
			}
		}
		if c.bodyKeys()[name] {
			return true, "<nested>"
		}
		for _, pair := range strings.Split(c.RawQuery, "&") {
			k, v, _ := strings.Cut(pair, "=")
			if k == name {
				return true, v
			}
		}
	}
	return false, ""
}

// contractFutureField is the key G3 plants in every server answer: a name no
// struct in this process has ever heard of.
const contractFutureField = "some_field_added_after_this_gate_was_written"

// contractProbePayload is the one answer the fake gives every path. It carries
// the ids the handlers act on (a claim keys its state file on `id`, and derives
// a branch from `slug`/`project`), an empty list pair for the list-shaped tools,
// and the future field G3 looks for.
func contractProbePayload() map[string]any {
	return map[string]any{
		"ok":                true,
		"id":                "wi_01PF419PROBE",
		"slug":              "aihub#419",
		"project":           "aihub",
		"items":             []any{},
		"total":             0,
		"attempt_id":        "ra_pf419",
		"claim_epoch":       1,
		contractFutureField: "arrived",
	}
}

// localConsumptionParams is the reasoned allowlist for G1: a published parameter
// this process spends itself and deliberately does not forward.
//
// Keyed "<tool>.<param>". Every entry needs a reason, because "published and not
// forwarded" is otherwise indistinguishable from the defect this gate exists to
// catch — and every entry is checked NON-STALE by the gate: an entry whose
// parameter IS forwarded fails, and an entry naming a parameter no tool
// publishes any more fails. Neither can rot into a silent exemption.
var localConsumptionParams = map[string]string{
	// The response projections. This process is the LAST hop before the model, so
	// a parameter that shapes what the model is handed is consumed exactly where
	// it is read — there is no server hop for it to be dropped on. tools_memory.go
	// carries the long version of this argument for `fields`, including why
	// forwarding it would be strictly worse (it would become a third instance of
	// aihub#148).
	"pf_recall.fields":          "consumed here: selects the response projection (slimRecallResultMode). aihub#313 decided this deliberately — see the comment at its call site in tools_memory.go.",
	"pf_get_work_item.brief":    "consumed here: omits the content field from the result this process returns.",
	"pf_update_work_item.brief": "consumed here: replaces the content body with content_len in the result this process returns.",

	// The coding tools. Each of these is spent on a local git or gh invocation,
	// and none of them has a server-side counterpart to reach.
	"pf_commit.workspace_root": "consumed here: passed to coding.WorktreePath to locate the worktree this commit runs in.",
	"pf_commit.paths":          "consumed here: the git pathspec staged by the local commit.",
	"pf_push.workspace_root":   "consumed here: passed to coding.WorktreePath to locate the worktree this push runs in.",
	"pf_ship.workspace_root":   "consumed here: passed to coding.WorktreePath to locate the worktree this ship runs in.",
	"pf_ship.paths":            "consumed here: the git pathspec staged by the local commit.",
	"pf_ship.pr_title":         "consumed here: an argument to gh pr create.",
	"pf_ship.pr_body":          "consumed here: an argument to gh pr create.",
	"pf_ship.pr_base":          "consumed here: the base branch handed to gh pr create when a new PR has to be opened.",

	// pf_save_artifact reads the file and forwards its CONTENT, which is why
	// `content` and `path` are mutually exclusive (see exclusiveParams).
	"pf_save_artifact.path": "consumed here: the local markdown file is READ in this process and its text is forwarded as `content`, so the path itself has nothing to do on the wire.",
}

// toolsWithNoWireHop are the tools that make no HTTP call at all in this
// harness, so G1/G2/G3 have nothing to observe for them.
//
// 🔴 This is NOT a skip list, and the difference matters: a tool here is a tool
// whose parameters the gate cannot measure, so the entry records WHY and the
// gate checks it non-stale — a tool that does call out while listed here fails.
// Without that, "the gate saw nothing" and "the gate approves" would be the same
// verdict, which is the failure mode every floor arm in this file exists to
// prevent.
var toolsWithNoWireHop = map[string]string{
	"pf_diff": "no aihub hop at all: the handler runs git diff in the worktree and returns the " +
		"text. Measured: it answers successfully with zero outbound requests, so every " +
		"parameter it publishes is consumed in this process. What those parameters DO is " +
		"covered by tools_coding_test.go, not here.",
	"pf_pr": "the probe workspace's git remote is a local bare repository, so `gh pr create` " +
		"fails with \"none of the git remotes point to a known GitHub host\" before the tool " +
		"reaches any aihub call. A hermetic test cannot have a GitHub host, so this is a " +
		"standing limit of the harness rather than something to fix — stated here because " +
		"'the gate saw nothing' must not read as 'the gate approves'.",
	"pf_wrap": "same as pf_pr: its ship phase calls `gh pr list` first and fails in a hermetic " +
		"workspace, so its aihub hop (CompleteAttempt) is never reached. Its parameters are " +
		"outside this gate; claim_committed_side_effect_test.go and tools_fusion_test.go " +
		"exercise the wrap sequence with the state file seeded directly.",
}

// seedClaimedWorktree runs a real claim through the harness before the sweep,
// so the six coding tools (pf_commit, pf_diff, pf_push, pf_pr, pf_ship, pf_wrap)
// have the thing they act on.
//
// Without it those tools fail on their FIRST local step — no worktree, no state
// file — and never reach the aihub call at the end, which would put all 33 of
// their parameters outside the gate. "The harness could not get that far" is a
// coverage hole, and a coverage hole that reads as a pass is what every floor
// arm in this file exists to prevent, so the harness goes as far as it can and
// whatever is STILL unreachable is written down in toolsWithNoWireHop with the
// reason.
//
// The claim is driven through the registered tool rather than faked on disk: the
// state file's shape and the worktree layout are the claim handler's business,
// and a hand-written copy of them here would be a second thing to keep true.
func seedClaimedWorktree(t *testing.T, h *contractHarness) {
	t.Helper()
	calls, result, isErr := h.call(t, "pf_claim_work_item", map[string]any{
		"work_item_id":    "wi_01PF419PROBE",
		"idempotency_key": "pf419-seed-claim",
	})
	if isErr || len(calls) == 0 {
		t.Fatalf("the seed claim failed (isErr=%v, %d request(s), result %v) — the coding tools "+
			"would then be unmeasurable for a reason this harness caused rather than one the "+
			"code has", isErr, len(calls), result)
	}
	// The worktree path is DERIVED from contractProbePayload's slug, so editing
	// that payload could silently point h.worktree at nothing: dirty() would
	// no-op, pf_commit would answer "nothing to commit", and G1 would report a
	// correctly wired tool as unmeasurable. Assert the two agree instead of
	// letting them drift.
	if _, err := os.Stat(h.worktree); err != nil {
		t.Fatalf("the seed claim did not create %s (%v). That path is derived from "+
			"contractProbePayload's slug/project; if either changed, change it here too — "+
			"otherwise every coding tool silently becomes unmeasurable.", h.worktree, err)
	}
}

// callSeeded calls a tool and, if nothing left the process, re-seeds the claim
// and tries once more.
//
// It exists because of a real trap this sweep walked into: the tools are
// iterated in name order, `pf_complete_attempt` sits in the middle of that
// order, and completing an attempt DELETES the state file every later
// credential-checked tool authenticates with. The first run of this gate
// therefore reported "made NO request at all" for eleven tools that are in fact
// wired correctly — a verdict caused entirely by the harness, and one that would
// have gone into the baseline as eleven fake defects.
//
// Retry-on-empty rather than a list of state-destroying tools: a list would have
// to be kept true, and its failure mode is silent (a new terminal tool added
// below the list makes everything after it unmeasurable again). This shape
// self-corrects, and it makes the surviving "no request" verdicts stronger — they
// are what is left after a retry with fresh credentials.
func (h *contractHarness) callSeeded(t *testing.T, tool string, args map[string]any) ([]contractRecordedCall, map[string]any, bool) {
	t.Helper()
	h.dirty(t)
	calls, res, isErr := h.call(t, tool, args)
	if len(calls) > 0 {
		return calls, res, isErr
	}
	reseedClaim(t, h)
	h.dirty(t)
	return h.call(t, tool, args)
}

// reseedClaimCounter keeps each re-seed's idempotency key distinct: resending a
// key returns the EXISTING attempt (aihub#392), which is a different code path
// from the one that writes a fresh state file.
var reseedClaimCounter int

func reseedClaim(t *testing.T, h *contractHarness) {
	t.Helper()
	reseedClaimCounter++
	_, _, _ = h.call(t, "pf_claim_work_item", map[string]any{
		"work_item_id":    "wi_01PF419PROBE",
		"idempotency_key": fmt.Sprintf("pf419-reseed-%d", reseedClaimCounter),
	})
}

// TestContractEveryPublishedParamLeavesTheProcess is G1.
func TestContractEveryPublishedParamLeavesTheProcess(t *testing.T) {
	baseline := loadContractBaseline(t)
	h, tools := newContractGate(t)

	probed, strong, onWire := 0, 0, 0
	var violations, keyOnly []string
	for _, tool := range tools {
		params := contractParamsOf(t, tool)
		madeCall := false
		// Per-tool, because a tool that never reached the wire needs ONE verdict
		// about the tool, not one per parameter: thirty "not forwarded" lines for
		// pf_pr all say the same thing, and burying the cause under them is how a
		// reader concludes the tool is broken when the harness simply could not
		// get that far.
		var perParam []string
		for _, name := range sortedParamNames(params) {
			p := params[name]
			calls, token := probeArgsFor(t, h, tool.Name, params, name)
			probed++
			if len(calls) > 0 {
				madeCall = true
			}
			v := paramReachedTheWire(t, h, tool.Name, p, params, calls, token)
			if v.strong && v.forwarded {
				strong++
			} else if v.forwarded {
				keyOnly = append(keyOnly, tool.Name+"."+name)
			}
			reason, isLocal := localConsumptionParams[tool.Name+"."+name]
			if isLocal {
				if v.forwarded {
					t.Errorf("localConsumptionParams claims %s.%s is consumed here and never "+
						"forwarded (%s), but it WAS forwarded: %s. Stale exemption — an "+
						"allowlist entry that has stopped being true excuses the next real "+
						"instance of this defect in the same place.", tool.Name, name, reason, v.how)
				}
				continue
			}
			if v.forwarded {
				continue
			}
			if e, ok := baseline.baselined(gateParamNotForwarded, tool.Name, name); ok {
				t.Logf("baselined: %s.%s (%s) — %s", tool.Name, name, e.WI, e.Note)
				continue
			}
			perParam = append(perParam, fmt.Sprintf("%s.%s (%s)", tool.Name, name, v.how))
		}

		reason, listed := toolsWithNoWireHop[tool.Name]
		switch {
		case madeCall:
			onWire++
			if listed {
				t.Errorf("toolsWithNoWireHop says %s makes no HTTP call (%s), but it made one — "+
					"stale entry, and while it stands every parameter of this tool is exempt "+
					"from G1 for a reason that is no longer true", tool.Name, reason)
			}
			violations = append(violations, perParam...)
		case listed:
			t.Logf("%s: no wire hop, so its %d parameter(s) are outside G1 — %s",
				tool.Name, len(params), reason)
		case len(params) > 0:
			violations = append(violations, fmt.Sprintf("%s made NO request at all — none of its "+
				"%d parameter(s) could be measured, even with a maximal argument set and a "+
				"re-seeded claim", tool.Name, len(params)))
		}
	}

	// Floors first: a violation list is only meaningful next to what was measured.
	if probed < floorParams {
		t.Fatalf("probed only %d parameter(s) across %d tools, floor is %d — the arg synthesis "+
			"is skipping parameters and an empty verdict list would mean nothing",
			probed, len(tools), floorParams)
	}
	if onWire < floorToolsOnWire {
		t.Fatalf("only %d of %d tools made any HTTP call, floor is %d — the fake is not being "+
			"reached, so 'not forwarded' below would be the harness talking about itself",
			onWire, len(tools), floorToolsOnWire)
	}
	if strong < floorStrongParams {
		t.Fatalf("only %d of %d parameter verdicts rest on a unique probe token, floor is %d — "+
			"the gate has degraded to key-presence, which goes green on a key the handler "+
			"writes whatever the caller sends", strong, probed, floorStrongParams)
	}
	t.Logf("G1: %d parameters over %d tools; %d verdicts token-proved, %d key-presence-only (%v)",
		probed, len(tools), strong, len(keyOnly), keyOnly)

	for _, v := range violations {
		t.Errorf("PARAM_NOT_FORWARDED %s\n"+
			"The tool publishes this parameter and nothing carrying it left this process. A "+
			"caller who sets it gets a response identical to one sent without it, with no error "+
			"at any hop — aihub#148's signature, found again by hand in aihub#387 and aihub#394. "+
			"Fix it, withdraw it from the InputSchema, or — if this process consumes it here on "+
			"purpose — add it to localConsumptionParams with the reason. If it is a "+
			"pre-existing violation you are not fixing in this change, add it to %s with the wi "+
			"that owns the fix.", v, contractBaselinePath)
	}
	baseline.assertNoStaleEntries(t, gateParamNotForwarded)
}

// ─────────────── the server-side route census (G2 and G4's map) ──────────────

// serverRoute is one route package server registers.
type serverRoute struct {
	Method  string
	Pattern string // full path INCLUDING the group prefix, echo syntax (":id")
	Handler string
	Where   string
}

// echoRouteMethods are the registration methods a route can be declared with.
var echoRouteMethods = map[string]bool{
	"GET": true, "POST": true, "PATCH": true, "PUT": true, "DELETE": true,
}

// routeGroupPrefixes maps the receiver identifier a route is registered on to
// the path prefix that receiver carries.
//
// Derived from the Group() calls it describes: `v1 := e.Group("/v1", …)`,
// `admin := v1.Group("/admin", …)`, `gc := v1.Group("/admin/gc", …)`, and the
// /ui group threaded into the ui_* files as a parameter. An identifier NOT in
// this table is a FATAL rather than a skipped route: silently skipping is how a
// census stops covering the half of the server somebody just added, and it
// would take G2 with it — an unrouted path would then read as "no route parsed
// for that prefix", which is indistinguishable from "routed correctly".
var routeGroupPrefixes = map[string]string{
	"e":        "",
	"v1":       "/v1",
	"admin":    "/v1/admin",
	"gc":       "/v1/admin/gc",
	"uiGroup":  "/ui",
	"g":        "/ui",
	"uiRoutes": "/ui",
}

// serverRoutes censuses every route registration in package server.
func serverRoutes(t *testing.T) []serverRoute {
	t.Helper()
	fset, files := serverASTFiles(t)

	var out []serverRoute
	unknownGroups := map[string][]string{}
	for name, f := range files {
		ast.Inspect(f, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok || len(call.Args) < 2 {
				return true
			}
			sel, ok := call.Fun.(*ast.SelectorExpr)
			if !ok || !echoRouteMethods[sel.Sel.Name] {
				return true
			}
			recv, ok := sel.X.(*ast.Ident)
			if !ok {
				return true
			}
			pattern, ok := firstStringLit(call.Args[:1])
			if !ok {
				return true
			}
			prefix, known := routeGroupPrefixes[recv.Name]
			if !known {
				unknownGroups[recv.Name] = append(unknownGroups[recv.Name],
					fset.Position(call.Pos()).String())
				return true
			}
			out = append(out, serverRoute{
				Method:  sel.Sel.Name,
				Pattern: prefix + pattern,
				Handler: routeHandlerName(call.Args[1]),
				Where:   name + ":" + fmt.Sprint(fset.Position(call.Pos()).Line),
			})
			return true
		})
	}
	for group, sites := range unknownGroups {
		t.Errorf("route registrations on the unknown receiver %q at %v — extend "+
			"routeGroupPrefixes with the prefix that receiver carries. Left alone, every route "+
			"registered on it is invisible to this census, and G2 cannot then tell an unrouted "+
			"URL from one whose route it simply never parsed.", group, sites)
	}
	if len(out) < floorRoutes {
		t.Fatalf("parsed only %d route(s) from package server, floor is %d — the census is "+
			"broken, and G2's verdict that every path is routed would be vacuous",
			len(out), floorRoutes)
	}
	return out
}

// routeHandlerName names the handler a route registration points at, for both
// shapes in use: `handleX(pool)` and a bare `handleX`.
func routeHandlerName(e ast.Expr) string {
	switch v := e.(type) {
	case *ast.Ident:
		return v.Name
	case *ast.CallExpr:
		if id, ok := v.Fun.(*ast.Ident); ok {
			return id.Name
		}
	}
	return ""
}

// matchRoute finds the route a concrete request took, by echo's own matching
// rule reduced to what these routes use: equal segment counts, with ":param"
// and a trailing "*" as wildcards. A literal segment beats a wildcard, which is
// why the two passes are separate — /v1/work_items/ready and
// /v1/work_items/:id are both registered, and picking the wildcard for the
// literal path would attribute the request to the wrong handler.
func matchRoute(routes []serverRoute, method, path string) (serverRoute, bool) {
	want := strings.Split(strings.Trim(path, "/"), "/")
	best, found := serverRoute{}, false
	bestLiterals := -1
	for _, r := range routes {
		if r.Method != method {
			continue
		}
		got := strings.Split(strings.Trim(r.Pattern, "/"), "/")
		if len(got) != len(want) && (len(got) == 0 || !strings.HasSuffix(r.Pattern, "*")) {
			continue
		}
		literals, ok := 0, true
		for i := range got {
			if strings.HasSuffix(got[i], "*") {
				break
			}
			if i >= len(want) {
				ok = false
				break
			}
			if strings.HasPrefix(got[i], ":") {
				continue
			}
			if got[i] != want[i] {
				ok = false
				break
			}
			literals++
		}
		if ok && literals > bestLiterals {
			best, found, bestLiterals = r, true, literals
		}
	}
	return best, found
}

// observedCall is one (method, path) the MCP layer was seen to call, and the
// tool that caused it.
type observedCall struct {
	tool string
	call contractRecordedCall
}

// observeOnce calls one tool with the widest argument set that produces a
// request: the maximal rung first, the minimal one as a fallback.
//
// Maximal first because G4 asks which names this process DERIVES on the
// caller's behalf, and only a maximal body shows them all. Minimal as a
// fallback because a maximal set can itself be what stops the request: pf_commit
// staging a `paths` value that matches no file dies at `git add` with a pathspec
// error, which is the harness's argument being wrong rather than the tool being
// wrong.
//
// ⚠️ It must go through probeArgsFull, not a bare loop over probeValue. An
// earlier version built the maximal set itself and so skipped exclusiveParams and
// suppressorParams: pf_save_artifact got `content` AND `path` (rejected outright)
// and pf_update_step got `heartbeat` (which discards everything else), so both
// vanished from the observed set — and every field only THEY send was then
// reported as an unreachable server-side name. Seven false positives, from a
// duplicated argument builder that was one line shorter.
func observeOnce(t *testing.T, h *contractHarness, tool string, params map[string]contractParam) ([]contractRecordedCall, map[string]any, bool) {
	t.Helper()
	args, _ := probeArgsFull(t, tool, params, "")
	calls, res, isErr := h.callSeeded(t, tool, args)
	if len(calls) > 0 {
		return calls, res, isErr
	}
	args, _ = probeArgs(t, tool, params, "")
	return h.callSeeded(t, tool, args)
}

// observeEveryTool records what every tool in the registry sends.
func observeEveryTool(t *testing.T, h *contractHarness, tools []*sdkmcp.Tool) []observedCall {
	t.Helper()
	var out []observedCall
	for _, tool := range tools {
		calls, _, _ := observeOnce(t, h, tool.Name, contractParamsOf(t, tool))
		for _, c := range calls {
			out = append(out, observedCall{tool: tool.Name, call: c})
		}
	}
	return out
}

// TestContractEveryPathTheMCPLayerCallsIsRouted is G2.
//
// A path this process calls that the server does not route answers 404 for
// every caller of that tool, forever, and the tool's own tests would not
// notice: a fake aihub answers whatever path it is asked for, which is exactly
// what the fake in this file does. The only way to catch it without a live
// server is to compare the URLs against the route table, which is what this
// does.
func TestContractEveryPathTheMCPLayerCallsIsRouted(t *testing.T) {
	baseline := loadContractBaseline(t)
	h, tools := newContractGate(t)
	routes := serverRoutes(t)
	observed := observeEveryTool(t, h, tools)

	if len(observed) < floorToolsOnWire {
		t.Fatalf("observed only %d outbound request(s) from %d tools, floor is %d — nothing was "+
			"measured and 'every path is routed' would be vacuous", len(observed), len(tools), floorToolsOnWire)
	}

	seen := map[string]bool{}
	matched := 0
	for _, o := range observed {
		key := o.call.Method + " " + o.call.Path
		if seen[key] {
			continue
		}
		seen[key] = true
		if _, ok := matchRoute(routes, o.call.Method, o.call.Path); ok {
			matched++
			continue
		}
		if e, ok := baseline.baselined(gatePathNotRouted, o.tool, key); ok {
			t.Logf("baselined: %s calls unrouted %s (%s) — %s", o.tool, key, e.WI, e.Note)
			continue
		}
		t.Errorf("PATH_NOT_ROUTED %s calls %q and package server registers no route for it.\n"+
			"Every real call of this tool answers 404 while every test against a fake aihub "+
			"passes, because a fake answers whatever path it is asked. Routes parsed: %d.",
			o.tool, key, len(routes))
	}
	// Liveness for the matcher itself: if matchRoute were broken in the other
	// direction it would match nothing, every path would be reported, and that is
	// loud. If it were broken to match everything, nothing would be reported and
	// THAT is silent — so require that most paths matched AND that the matcher can
	// still say no.
	if matched == 0 {
		t.Fatalf("not one of %d distinct paths matched a route — the matcher is broken, not the "+
			"server", len(seen))
	}
	if _, ok := matchRoute(routes, "GET", "/v1/pf419-no-such-route"); ok {
		t.Error("matchRoute matched a path that cannot exist — it answers yes to everything, so " +
			"its verdict that every observed path is routed carries no information")
	}
	t.Logf("G2: %d distinct paths from %d tools, %d matched, against %d routes",
		len(seen), len(tools), matched, len(routes))
	baseline.assertNoStaleEntries(t, gatePathNotRouted)
}

// toolsThatComposeTheirOwnResult are the tools whose result is not a projection
// of the server's answer at all, so G3's question does not apply to them.
//
// The distinction is not cosmetic. A keep-list projection DROPS something the
// server said the model needs; these tools never claimed to relay the server's
// answer in the first place — pf_commit and pf_push POST an event and get
// {"ok":true} back, which has nothing in it for anybody. Written down rather
// than skipped, with the same non-stale rule as every other allowlist here: a
// tool listed here that DOES forward the planted field fails, because then it is
// relaying after all and the exemption is hiding a real projection.
var toolsThatComposeTheirOwnResult = map[string]string{
	"pf_commit": "its result is git facts (branch, head_sha, committed) built in this process; " +
		"its only aihub call is an event POST whose answer is {\"ok\":true}.",
	"pf_push": "same as pf_commit: the result is the push outcome (branch, base_sha_at_push) " +
		"and the aihub call is an event POST.",
	"pf_ship": "its result is the commit/push/PR outcome plus a lock-gate summary, composed " +
		"here from several steps; there is no single server answer to relay.",
}

// TestContractEveryToolResultPassesThroughAnUnknownServerField is G3.
//
// The generalisation of aihub#388's shape test. A keep-list projection cannot
// pass this however complete it is today: the planted field is one no struct in
// this process has ever heard of, so only a projection that DELETES named keys
// and forwards the rest can carry it.
//
// Four instances of this defect are on record in this package alone
// (aihub#249/#269/#289 in recall_slim.go and aihub#388's four claim fields), and
// every one of them was a field somebody added server-side that silently never
// reached the model. This is the arm that makes the fifth instance impossible to
// add quietly.
func TestContractEveryToolResultPassesThroughAnUnknownServerField(t *testing.T) {
	baseline := loadContractBaseline(t)
	h, tools := newContractGate(t)

	inspected, passed := 0, 0
	for _, tool := range tools {
		params := contractParamsOf(t, tool)
		calls, result, isErr := observeOnce(t, h, tool.Name, params)
		if len(calls) == 0 || result == nil || isErr {
			// Nothing to inspect: either the tool never asked the server (so the
			// planted field was never in play) or it answered with an error string
			// rather than a JSON object. Both are recorded, neither is a pass.
			continue
		}
		inspected++
		// Anywhere in the serialised result, not only at the top level: a tool may
		// legitimately wrap the server's answer instead of projecting it —
		// pf_batch_create_work_items returns each per-item response verbatim inside
		// `created[]`, so a top-level-only assertion would have called the one tool
		// in the registry that forwards MOST faithfully a keep-list.
		blob, err := json.Marshal(result)
		if err != nil {
			t.Fatalf("marshal %s result: %v", tool.Name, err)
		}
		if strings.Contains(string(blob), contractFutureField) {
			passed++
			if reason, composes := toolsThatComposeTheirOwnResult[tool.Name]; composes {
				t.Errorf("toolsThatComposeTheirOwnResult says %s does not relay the server's "+
					"answer (%s), but the planted field came through — stale entry, and while "+
					"it stands a real keep-list in this tool would be exempt", tool.Name, reason)
			}
			continue
		}
		if reason, composes := toolsThatComposeTheirOwnResult[tool.Name]; composes {
			t.Logf("%s: composes its own result — %s", tool.Name, reason)
			continue
		}
		if e, ok := baseline.baselined(gateKeepListProjection, tool.Name, contractFutureField); ok {
			t.Logf("baselined: %s drops unknown server fields (%s) — %s", tool.Name, e.WI, e.Note)
			continue
		}
		t.Errorf("KEEP_LIST_PROJECTION %s: the server answered with %q and the tool result does "+
			"not carry it.\n"+
			"The projection is a KEEP-LIST, so the cheapest outcome of the server adding a field "+
			"is that it silently disappears — four times already in this package "+
			"(aihub#249/#269/#289, aihub#388). Make the projection DELETE named keys and forward "+
			"everything else (internal/mcp/list_wi_slim.go is the shape). Result keys: %v",
			tool.Name, contractFutureField, sortedAnyKeys(result))
	}

	if inspected < floorProjections {
		t.Fatalf("only %d tool result(s) could be inspected, floor is %d — the harness is not "+
			"getting answers back, so 'every projection forwards unknown fields' would be "+
			"vacuous", inspected, floorProjections)
	}
	if passed == 0 {
		t.Fatalf("not one of %d inspected results carried the planted field — that is the plant "+
			"failing to reach the fake's answer, not 50 broken projections", inspected)
	}
	t.Logf("G3: %d of %d tool results inspected, %d forwarded the unknown field", inspected, len(tools), passed)
	baseline.assertNoStaleEntries(t, gateKeepListProjection)
}

// ───────────── G4: what the server reads, and who can reach it ───────────────

// structJSONFields indexes every `type X struct` in packages server and domain
// by its json field names, following embedded structs.
//
// Read as SOURCE for both packages rather than by reflection over imported
// types, for the reason claim_param_contract_test.go gives about its own
// census: what is being measured is a property of the code, and a source walk
// covers the anonymous `var req struct{…}` shape that half the handlers in
// router.go use and that no reflection over a named type can reach.
func structJSONFields(t *testing.T) map[string][]string {
	t.Helper()
	_, serverFiles := serverASTFiles(t)
	_, domainFiles := domainASTFiles(t)

	decls := map[string]*ast.StructType{}
	for _, set := range []map[string]*ast.File{serverFiles, domainFiles} {
		for _, f := range set {
			for _, d := range f.Decls {
				gd, ok := d.(*ast.GenDecl)
				if !ok {
					continue
				}
				for _, spec := range gd.Specs {
					ts, ok := spec.(*ast.TypeSpec)
					if !ok {
						continue
					}
					if st, ok := ts.Type.(*ast.StructType); ok {
						decls[ts.Name.Name] = st
					}
				}
			}
		}
	}
	if len(decls) < 20 {
		t.Fatalf("indexed only %d struct type(s) across packages server and domain — the walk "+
			"is broken, and G4's reader set would be silently empty", len(decls))
	}
	out := map[string][]string{}
	for name, st := range decls {
		out[name] = jsonFieldsOf(st, decls, 3)
	}
	return out
}

// jsonFieldsOf reads a struct's json names, descending into embedded structs by
// name up to depth. Depth-limited rather than unbounded so a recursive type
// cannot hang the census.
func jsonFieldsOf(st *ast.StructType, decls map[string]*ast.StructType, depth int) []string {
	var out []string
	if st == nil || st.Fields == nil {
		return out
	}
	for _, fld := range st.Fields.List {
		if len(fld.Names) == 0 {
			// Embedded: its json fields are bound as if they were declared here.
			if depth > 0 {
				if id, ok := fld.Type.(*ast.Ident); ok {
					out = append(out, jsonFieldsOf(decls[id.Name], decls, depth-1)...)
				}
				if sel, ok := fld.Type.(*ast.SelectorExpr); ok {
					out = append(out, jsonFieldsOf(decls[sel.Sel.Name], decls, depth-1)...)
				}
			}
			continue
		}
		if goName := fld.Names[0].Name; goName == "" || !unicode.IsUpper([]rune(goName)[0]) {
			// Unexported: encoding/json never binds it whatever its tag says, so
			// counting it would report a name no caller could ever have reached —
			// handleRemember's workItemIDPreResolved is exactly this, and it was
			// reported as an unreachable bound field until this line existed.
			continue
		}
		name := jsonTagName(fld)
		if name == "" || name == "-" {
			continue
		}
		out = append(out, name)
	}
	return out
}

// jsonTagName returns a field's json name, falling back to the Go field name
// when it carries no tag — which is what encoding/json itself does, so a field
// with no tag IS bindable under its Go name and must not read as unbindable.
func jsonTagName(fld *ast.Field) string {
	goName := fld.Names[0].Name
	if fld.Tag == nil {
		return goName
	}
	tag := strings.Trim(fld.Tag.Value, "`")
	idx := strings.Index(tag, "json:\"")
	if idx < 0 {
		return goName
	}
	rest := tag[idx+len("json:\""):]
	val, _, _ := strings.Cut(rest, "\"")
	name, _, _ := strings.Cut(val, ",")
	if name == "" {
		return goName
	}
	return name
}

// handlerReaderSet is every caller-supplied name one route handler consumes:
// the json fields of whatever it Binds, plus the query/form parameters the
// derived reader census finds it reading.
//
// The walk is intra-function, and that limit is the safe direction — a name
// consumed inside a helper the census does not follow makes G4 report a
// reachable name as unreachable, which fails on correct code and gets the
// census extended, rather than passing a real unreachable field. Same trade
// ready_queue_param_wiring_test.go documents for its own census.
func handlerReaderSet(t *testing.T, handler string, structs map[string][]string,
	files map[string]*ast.File, readers map[string]bool) (map[string]bool, bool) {
	t.Helper()
	var body *ast.BlockStmt
	for _, f := range files {
		for _, d := range f.Decls {
			fd, ok := d.(*ast.FuncDecl)
			if !ok || fd.Recv != nil || fd.Name.Name != handler || fd.Body == nil {
				continue
			}
			body = fd.Body
		}
	}
	if body == nil {
		return nil, false
	}

	out := map[string]bool{}
	// Bound structs. `c.Bind(&req)` names the variable, so the declared type is
	// looked up in the same body: `var req domain.ClaimRequest` or the anonymous
	// `var req struct{…}`.
	bound := map[string]bool{}
	ast.Inspect(body, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok || len(call.Args) != 1 {
			return true
		}
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok || sel.Sel.Name != "Bind" {
			return true
		}
		unary, ok := call.Args[0].(*ast.UnaryExpr)
		if !ok {
			return true
		}
		if id, ok := unary.X.(*ast.Ident); ok {
			bound[id.Name] = true
		}
		return true
	})
	if len(bound) > 0 {
		ast.Inspect(body, func(n ast.Node) bool {
			vs, ok := n.(*ast.ValueSpec)
			if !ok || len(vs.Names) != 1 || !bound[vs.Names[0].Name] || vs.Type == nil {
				return true
			}
			switch tp := vs.Type.(type) {
			case *ast.StructType:
				for _, f := range jsonFieldsOf(tp, nil, 0) {
					out[f] = true
				}
			case *ast.Ident:
				for _, f := range structs[tp.Name] {
					out[f] = true
				}
			case *ast.SelectorExpr:
				for _, f := range structs[tp.Sel.Name] {
					out[f] = true
				}
			}
			return true
		})
	}
	// Query and form parameters, via the reader set derived next door.
	ast.Inspect(body, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		if name, ok := readerCallParam(call, readers); ok {
			out[name] = true
		}
		return true
	})
	return out, true
}

// serverNamesNoToolCanReach is the reasoned allowlist for G4: a name a route
// handler consumes that no MCP tool publishes, on purpose.
//
// Every entry needs a reason and is checked non-stale, same rule as
// localConsumptionParams. The legitimate shapes are (a) a name another client
// supplies — the /ui pages POST to some of these routes too — and (b) a header
// or an idempotency key this process derives rather than exposing.
var serverNamesNoToolCanReach = map[string]string{
	"handleClaimWorkItem.task_branches": "derived by the MCP handler from the worktrees the " +
		"claim is about to create (aihub#356) — the caller cannot know the branch names, and " +
		"the whole point is that the CLIENT reports what it will check out. The reason and its " +
		"limits are recorded in claim_param_contract_test.go's " +
		"claimFieldsDeliberatelyUnpublished; this entry points at it rather than restating it, " +
		"so the two cannot drift apart.",
	"handleRecall.limit": "an accepted ALIAS for the published top_k: routes_memory.go reads " +
		"queryInt(c, \"top_k\") and falls back to queryInt(c, \"limit\"). The capability is " +
		"reachable under the name pf_recall does publish, so this is compatibility surface " +
		"rather than an unreachable knob.",

	// ─── The two halves of the pause route's shared request struct (aihub#424) ──
	//
	// handlePauseAttempt binds domain.CompleteAttemptRequest, which /complete also
	// binds, so this census sees every field of it on BOTH routes. Two of them
	// cannot do anything here, and the reason is in the handler rather than in the
	// struct — which is exactly the shape that has to be written down: publishing
	// either one on pf_pause_attempt would advertise a switch that selects
	// nothing, aihub#394's signature, and deleting either is impossible without
	// splitting a struct /complete legitimately needs whole.
	"handlePauseAttempt.status": "handlePauseAttempt OVERWRITES it — `req.Status = \"paused\"` " +
		"is the statement right after c.Bind's error check and before any read — so no " +
		"caller-supplied value can ever take " +
		"effect on this route. The route IS the status; POST /pause is how a caller says " +
		"\"paused\", and /complete is where status is a real choice (it publishes it, and 44 of " +
		"605 observed calls send \"paused\" through it).",
	"handlePauseAttempt.force_terminate_step": "inert on this route, provably rather than by " +
		"convention: FnCompleteAttempt's only read of the field is `if req.Status == \"paused\" " +
		"|| req.ForceTerminateStep`, and handlePauseAttempt has just forced Status to \"paused\", " +
		"so the disjunction is already true whatever the caller sent. The capability it names — " +
		"terminate an in-progress step instead of answering 409 — is what pausing does " +
		"unconditionally. It stays published on pf_complete_attempt, where Status is the " +
		"caller's and the flag therefore decides something.",
}

// TestContractEveryServerReadNameIsReachableFromSomeTool is G4.
//
// The reverse direction of G1, and the generalisation of aihub#394's
// TestClaimPathActsOnNothingUnpublished: a name the server honours that no tool
// publishes is unreachable by any MCP caller, because the SDK drops undeclared
// arguments with no error at all.
func TestContractEveryServerReadNameIsReachableFromSomeTool(t *testing.T) {
	baseline := loadContractBaseline(t)
	h, tools := newContractGate(t)
	routes := serverRoutes(t)
	structs := structJSONFields(t)
	_, files := serverASTFiles(t)
	readers := requestReaderFuncs(t, files)
	observed := observeEveryTool(t, h, tools)

	// Reachability is computed PER ROUTE, as the union over every tool observed
	// calling it: what the tool publishes, plus what this process was seen adding
	// on the caller's behalf.
	//
	// Per route and not per tool, because the claim under test is "reachable by
	// SOME MCP caller" — and several tools share a route. pf_batch_create_work_items
	// and pf_create_work_item both POST /v1/work_items, and the batch tool sends
	// only goal and wi_type per item, so a per-tool reading reported all sixteen
	// CreateWorkItemRequest fields unreachable while pf_create_work_item publishes
	// every one of them. Sixteen false positives from choosing the wrong
	// quantifier, on a gate whose entire subject is quantifiers.
	toolParams := map[string]map[string]bool{}
	for _, tool := range tools {
		set := map[string]bool{}
		for name := range contractParamsOf(t, tool) {
			set[name] = true
		}
		toolParams[tool.Name] = set
	}
	reachable := map[string]map[string]bool{}
	callers := map[string]map[string]bool{}
	for _, o := range observed {
		route, ok := matchRoute(routes, o.call.Method, o.call.Path)
		if !ok {
			continue
		}
		key := route.Method + " " + route.Pattern
		if reachable[key] == nil {
			reachable[key] = map[string]bool{}
			callers[key] = map[string]bool{}
		}
		callers[key][o.tool] = true
		for name := range toolParams[o.tool] {
			reachable[key][name] = true
		}
		for k := range o.call.bodyKeys() {
			reachable[key][k] = true
		}
		for k := range o.call.queryKeys() {
			reachable[key][k] = true
		}
	}

	resolved, checked := 0, 0
	reported := map[string]bool{}
	for _, o := range observed {
		route, ok := matchRoute(routes, o.call.Method, o.call.Path)
		if !ok || route.Handler == "" {
			continue // G2 owns the unrouted case; reporting it twice adds nothing.
		}
		routeKey := route.Method + " " + route.Pattern
		names, found := handlerReaderSet(t, route.Handler, structs, files, readers)
		if !found {
			t.Errorf("route %s %s points at %q and no such function exists in package server — "+
				"the census cannot read what that route consumes, so every name it honours is "+
				"outside this gate", o.call.Method, route.Pattern, route.Handler)
			continue
		}
		resolved += len(names)
		for _, name := range sortedKeys(names) {
			if reachable[routeKey][name] {
				checked++
				continue
			}
			subject := route.Handler + "." + name
			if _, exempt := serverNamesNoToolCanReach[subject]; exempt {
				continue
			}
			if reported[subject] {
				continue
			}
			reported[subject] = true
			if e, ok := baseline.baselined(gateBoundFieldUnpublished, o.tool, subject); ok {
				t.Logf("baselined: %s (%s) — %s", subject, e.WI, e.Note)
				continue
			}
			t.Errorf("BOUND_FIELD_UNPUBLISHED %s consumes %q on %s %s and no tool that calls "+
				"that route (%v) publishes or sends it.\n"+
				"No MCP caller can reach it: the SDK drops an argument the InputSchema does not "+
				"declare, with no error at any hop (aihub#387's signature in reverse). Publish "+
				"it, stop reading it, or record it in serverNamesNoToolCanReach with the reason "+
				"it stays hidden — a name another client supplies is a legitimate reason, and "+
				"writing it down is what makes it checkable.",
				route.Handler, name, o.call.Method, route.Pattern, sortedKeys(callers[routeKey]))
		}
	}

	if resolved < floorBoundFields {
		t.Fatalf("the census resolved only %d server-side name(s) across %d observed request(s), "+
			"floor is %d — it is reading nothing, and 'every name is reachable' would be vacuous",
			resolved, len(observed), floorBoundFields)
	}
	if checked == 0 {
		t.Fatalf("not one server-side name was found reachable from the tool that calls it — the " +
			"reachability set is empty, so every name would be reported and the census is what " +
			"is broken")
	}
	// The allowlist's own staleness, checked against the whole census rather than
	// per request: an entry naming a handler/name pair that is now reachable, or
	// that no handler reads any more, is an exemption with nothing behind it.
	for subject, reason := range serverNamesNoToolCanReach {
		handler, name, ok := strings.Cut(subject, ".")
		if !ok {
			t.Errorf("serverNamesNoToolCanReach key %q is not <handler>.<name>", subject)
			continue
		}
		read := false
		nowReachable := false
		for _, o := range observed {
			route, matched := matchRoute(routes, o.call.Method, o.call.Path)
			if !matched || route.Handler != handler {
				continue
			}
			set, found := handlerReaderSet(t, handler, structs, files, readers)
			if found && set[name] {
				read = true
			}
			if reachable[route.Method+" "+route.Pattern][name] {
				nowReachable = true
			}
		}
		switch {
		case !read:
			t.Errorf("serverNamesNoToolCanReach exempts %s (%s), but %s does not read %q on any "+
				"route this gate observed — stale exemption", subject, reason, handler, name)
		case nowReachable:
			t.Errorf("serverNamesNoToolCanReach exempts %s (%s), but a tool calling that route "+
				"now publishes or sends it — stale exemption", subject, reason)
		}
	}
	t.Logf("G4: %d server-side name occurrences resolved over %d observed requests (summed per "+
		"request, so a route called twice counts twice), %d reachable", resolved, len(observed), checked)
	baseline.assertNoStaleEntries(t, gateBoundFieldUnpublished)
}

// sortedAnyKeys is the map[string]any counterpart of the sortedKeys next door
// in ready_queue_param_wiring_test.go, which this file reuses for bool maps.
func sortedAnyKeys(m map[string]any) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// ───────────────────────── the file's own hygiene ────────────────────────────

// TestContractBaselineFileIsWellFormed is the arm that keeps the baseline from
// becoming a place to hide things.
//
// Each gate can only call ITS OWN entries stale, so an entry under a gate name
// no gate uses would be audited by nobody: a permanent exemption produced by a
// typo. This is where that is caught, along with the rule
// scripts/pf_contract_lint_baseline.json enforces at load time — every entry
// carries a wi.
func TestContractBaselineFileIsWellFormed(t *testing.T) {
	b := loadContractBaseline(t)
	gates := knownGates()
	seen := map[string]bool{}
	for i, e := range b.entries {
		if !gates[e.Gate] {
			t.Errorf("entry %d names gate %q, which no gate in this file uses (%v) — nothing "+
				"would ever check it and nothing would ever call it stale, so it is a permanent "+
				"exemption produced by a typo", i, e.Gate, sortedKeys(gates))
		}
		if e.Tool == "" || e.Subject == "" {
			t.Errorf("entry %d (%s) has an empty tool or subject, so it can never match a "+
				"violation and can never be reported stale either: %+v", i, e.Gate, e)
		}
		if !strings.Contains(e.WI, "#") {
			t.Errorf("entry %d (%s / %s / %s) has no wi reference. A baseline line without an "+
				"owner is a decision to live with the defect wearing the costume of a decision "+
				"to fix it later.", i, e.Gate, e.Tool, e.Subject)
		}
		if e.Note == "" {
			t.Errorf("entry %d (%s / %s / %s) has no note — the next reader needs to know what "+
				"was found, not only that something was", i, e.Gate, e.Tool, e.Subject)
		}
		if seen[e.key()] {
			t.Errorf("entry %d duplicates %s / %s / %s — the second copy can never be marked "+
				"used, so it would report stale forever", i, e.Gate, e.Tool, e.Subject)
		}
		seen[e.key()] = true
	}
	t.Logf("baseline: %d entr(ies)", len(b.entries))
}

// TestContractProbeTableIsNotStale audits the arg-synthesis tables the same way
// the gates audit their allowlists.
//
// A dead entry here is not harmless: elementShapes and semanticValues decide
// what VALUE each parameter is probed with, so an entry that has stopped
// matching a published parameter is a shape waiting to be applied to the wrong
// thing the day that name comes back — and a probe sent with the wrong shape is
// rejected before the request is built, which reads as "not forwarded" and puts
// a false violation in front of whoever added the parameter.
func TestContractProbeTableIsNotStale(t *testing.T) {
	_, tools := newContractGate(t)

	published := map[string]bool{}
	// Per TOOL as well as per name: an entry keyed "<tool>.<param>" whose param is
	// published by some OTHER tool would otherwise pass this check while
	// exempting nothing — a dead exemption that reads as a live one, which is the
	// exact failure this test exists to prevent one level up.
	//
	// Not hypothetical. The first version of this check compared names only, and
	// localConsumptionParams shipped with "pf_list_work_items.fields" in it —
	// `fields` is published by pf_recall and by nothing else, so the entry
	// exempted nothing at all while reading like a considered decision.
	// Tightening the check found it on the first run, in this file's own tables.
	publishedByTool := map[string]bool{}
	total := 0
	for _, tool := range tools {
		for name := range contractParamsOf(t, tool) {
			published[name] = true
			publishedByTool[tool.Name+"."+name] = true
			total++
		}
	}
	if total < floorParams {
		t.Fatalf("the registry publishes %d parameter(s), floor is %d — nothing below would mean "+
			"anything", total, floorParams)
	}
	for name := range elementShapes {
		if !published[name] {
			t.Errorf("elementShapes has a shape for %q, which no tool publishes any more", name)
		}
	}
	for name := range semanticValues {
		if !published[name] {
			t.Errorf("semanticValues pins %q, which no tool publishes any more", name)
		}
	}
	for name := range semanticControlValues {
		if !published[name] {
			t.Errorf("semanticControlValues pins %q, which no tool publishes any more", name)
		}
	}
	for key := range semanticValuesByTool {
		if !publishedByTool[key] {
			t.Errorf("semanticValuesByTool names %q, which that tool does not publish any more "+
				"— the override is dead and the global value silently applies again", key)
		}
	}
	for name := range exclusiveParams {
		if !published[name] {
			t.Errorf("exclusiveParams names %q, which no tool publishes any more — the entry "+
				"now excludes a parameter that does not exist, so the maximal probe rung is "+
				"quietly narrower than it reads", name)
		}
	}
	for name := range suppressorParams {
		if !published[name] {
			t.Errorf("suppressorParams names %q, which no tool publishes any more", name)
		}
	}
	for name := range semanticValuesNotDistinctive {
		if _, ok := semanticValues[name]; !ok {
			t.Errorf("semanticValuesNotDistinctive names %q, which semanticValues does not "+
				"define — the entry weakens detection for a parameter nobody chose to weaken "+
				"it for", name)
		}
	}
	for key := range localConsumptionParams {
		if !publishedByTool[key] {
			t.Errorf("localConsumptionParams names %q, which that tool does not publish any "+
				"more — stale exemption", key)
		}
	}
	names := map[string]bool{}
	for _, tool := range tools {
		names[tool.Name] = true
	}
	for tool := range toolsWithNoWireHop {
		if !names[tool] {
			t.Errorf("toolsWithNoWireHop names %q, which the registry does not publish any more", tool)
		}
	}
	for tool := range toolsThatComposeTheirOwnResult {
		if !names[tool] {
			t.Errorf("toolsThatComposeTheirOwnResult names %q, which the registry does not "+
				"publish any more", tool)
		}
	}
	t.Logf("probe tables: %d element shapes, %d semantic values, %d controls over %d published parameters",
		len(elementShapes), len(semanticValues), len(semanticControlValues), total)
}
