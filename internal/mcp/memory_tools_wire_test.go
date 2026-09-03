package mcp_test

// aihub#325 — every memory tool's published contract, asserted on the BYTES it
// puts on the wire.
//
// ─── Why this is not another schema-vs-map comparison ────────────────────────
//
// aihub#148 built the same guard for pf_recall alone: published InputSchema ⊆
// what buildRecallParams forwards. The very next tool along had the identical
// defect and the guard could not see it, because it was written for one tool.
// pf_reinforce_memory declared work_item_id REQUIRED, refused the call without
// one, and built a body that omitted it — so every non-methodology reinforce got
//
//	400  work_item_id is required when attempt_id/session_secret are provided
//
// This file closes that by covering the whole file's worth of tools, and by
// asserting one hop further out than aihub#148 did:
//
//	hop 1  published InputSchema        read back from ListTools, not from the
//	                                    schema functions — what the MODEL sees
//	hop 2  MCP args -> HTTP request     the real handler, the real pkg/client,
//	                                    and the real request bytes
//
// Reading the schema back through ListTools matters: a builder can be perfect
// and still unreachable if the handler stops calling it, and a schema function
// can be perfect and still not be the one registered. Both are invisible to a
// test that calls the two functions directly.
//
// 🔴 Values, not names or types. Renames and nesting are normal here — `html`
// lands as `rendered_html`, `memory_id` lands as `payload.artifact_key` or as a
// URL path segment — so a key-set comparison would report three false drops and
// would still have missed this wi's real one. The landing of each published
// property is written down and checked as a value.
//
// Needs no database: the aihub server is an httptest recorder.

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"testing"

	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/GMISWE/ieops-aihub/internal/config"
	"github.com/GMISWE/ieops-aihub/internal/mcp"
	"github.com/GMISWE/ieops-aihub/pkg/client"
)

// ─── landings ────────────────────────────────────────────────────────────────

type wireLanding int

const (
	// landBody: the value must appear in the JSON request body at `at`, a
	// dot-separated path ("work_item_id", "payload.artifact_key").
	landBody wireLanding = iota
	// landPath: the value must appear as a segment of the request URL path.
	landPath
	// landLocal: consumed by this process and deliberately never sent. Needs a
	// reason, because "published but not forwarded" is otherwise
	// indistinguishable from the defect this file exists to catch.
	landLocal
)

type wireProbe struct {
	shape   any         // what a caller sends
	landing wireLanding //
	at      string      // body path for landBody
	want    any         // the value that must arrive (defaults to shape)
	reason  string      // required for landLocal
	// dropBase names base arguments to omit for this probe's call. Only
	// pf_save_artifact needs it: `content` and `path` are two spellings of the
	// same field and sending both is a documented error.
	dropBase []string
}

type toolWire struct {
	base   map[string]any       // the arguments every call for this tool sends
	probes map[string]wireProbe // published property -> where its value must land
}

const (
	probeWI     = "wi_probe325"
	probeMemory = "mem_probe325"
	probeCommit = "cm_probe325"
)

// memoryToolWire is hop 2 stated as VALUES, per tool, per published property.
var memoryToolWire = map[string]toolWire{
	// Body is the argument map itself — every published property is on the wire
	// by construction. Stated rather than assumed: the handler could start
	// projecting the map tomorrow.
	"pf_remember": {
		base: map[string]any{
			"project": "p_probe", "type": "experience.debug",
			"content": "body", "visibility": "project",
		},
		probes: map[string]wireProbe{
			"project":              {shape: "p_probe", landing: landBody, at: "project"},
			"type":                 {shape: "experience.debug", landing: landBody, at: "type"},
			"content":              {shape: "a memory body", landing: landBody, at: "content"},
			"visibility":           {shape: "team", landing: landBody, at: "visibility"},
			"work_item_id":         {shape: probeWI, landing: landBody, at: "work_item_id"},
			"base_strength":        {shape: 0.7, landing: landBody, at: "base_strength"},
			"attrs":                {shape: map[string]any{"k": "v"}, landing: landBody, at: "attrs"},
			"expires_at":           {shape: "2030-01-01T00:00:00Z", landing: landBody, at: "expires_at"},
			"dedup_mode":           {shape: "off", landing: landBody, at: "dedup_mode"},
			"related_memory_ids":   {shape: []any{"mem_a", "mem_b"}, landing: landBody, at: "related_memory_ids"},
			"context_snippet":      {shape: "surrounding text", landing: landBody, at: "context_snippet"},
			"supersedes_memory_id": {shape: "mem_old", landing: landBody, at: "supersedes_memory_id"},
		},
	},
	"pf_get_memory": {
		base:   map[string]any{"memory_id": probeMemory},
		probes: map[string]wireProbe{"memory_id": {shape: probeMemory, landing: landPath}},
	},
	"pf_activate_memory": {
		base:   map[string]any{"memory_id": probeMemory},
		probes: map[string]wireProbe{"memory_id": {shape: probeMemory, landing: landPath}},
	},
	// THE TOOL THIS WI IS ABOUT.
	"pf_reinforce_memory": {
		base: map[string]any{
			"memory_id": probeMemory, "additional_context": "why", "work_item_id": probeWI,
		},
		probes: map[string]wireProbe{
			"memory_id":          {shape: probeMemory, landing: landPath},
			"additional_context": {shape: "what the reinforcement adds", landing: landBody, at: "additional_context"},
			"strength_delta":     {shape: 0.5, landing: landBody, at: "strength_delta"},
			// 🔴 aihub#325. The server verifies the attempt credentials against
			// this id and records it as attrs.reinforcements[].from_wi; without
			// it the non-methodology gate answers 400 to every call.
			"work_item_id": {shape: probeWI, landing: landBody, at: "work_item_id"},
		},
	},
	"pf_update_memory": {
		base: map[string]any{"memory_id": probeMemory, "work_item_id": probeWI},
		probes: map[string]wireProbe{
			"memory_id":     {shape: probeMemory, landing: landPath},
			"content":       {shape: "the new body", landing: landBody, at: "content"},
			"visibility":    {shape: "team", landing: landBody, at: "visibility"},
			"tags":          {shape: []any{"t1", "t2"}, landing: landBody, at: "tags"},
			"base_strength": {shape: 0.9, landing: landBody, at: "base_strength"},
			"work_item_id":  {shape: probeWI, landing: landBody, at: "work_item_id"},
		},
	},
	"pf_redact_memory": {
		base: map[string]any{"memory_id": probeMemory, "reason": "why"},
		probes: map[string]wireProbe{
			"memory_id": {shape: probeMemory, landing: landPath},
			// Was 🔴 here: `reason` is REQUIRED by the schema and reached the
			// wire (this probe was already green), and then handleRedactMemory
			// bound no body at all, so every redaction's stated reason was
			// discarded at hop 3. Tracked separately as aihub#349 and FIXED
			// by aihub#175: the handler now binds it and domain.Redact stores it
			// in memories.redaction_reason and in the memory_redacted event's
			// payload. This probe covers hops 1-2 (tool arg -> HTTP body);
			// hop 3 (body -> row) is asserted end-to-end by
			// server.TestRedactMemory_ProjectWriterIsAuditedWithReason.
			"reason": {shape: "superseded by a newer note", landing: landBody, at: "reason"},
		},
	},
	"pf_save_artifact": {
		base: map[string]any{
			"type": "methodology.spec", "work_item_id": probeWI, "content": "artifact body",
		},
		probes: map[string]wireProbe{
			"type":         {shape: "methodology.plan", landing: landBody, at: "type"},
			"work_item_id": {shape: probeWI, landing: landBody, at: "work_item_id"},
			"content":      {shape: "the artifact body", landing: landBody, at: "content"},
			// `path` is the same field spelled as a file: the local process reads
			// it and the CONTENT is what goes on the wire. Landing on `content`,
			// not being absent, is what makes this a forwarding assertion rather
			// than an exemption.
			"path": {
				shape: "artifact.md", landing: landBody, at: "content",
				want: "read off disk by the MCP process\n", dropBase: []string{"content"},
			},
			"structured_payload":   {shape: map[string]any{"criteria": []any{"a"}}, landing: landBody, at: "structured_payload"},
			"visibility":           {shape: "team", landing: landBody, at: "visibility"},
			"supersedes_memory_id": {shape: "mem_old", landing: landBody, at: "supersedes_memory_id"},
			// Renamed on the way out.
			"html": {shape: "<p>rendered</p>", landing: landBody, at: "rendered_html"},
		},
	},
	"pf_adopt_artifact":  artifactActionWire,
	"pf_close_artifact":  artifactActionWire,
	"pf_ignore_artifact": artifactActionWire,
	"pf_resolve_commit": {
		base: map[string]any{"memory_id": probeMemory, "commit_id": probeCommit, "reply": "ok"},
		probes: map[string]wireProbe{
			"memory_id": {shape: probeMemory, landing: landPath},
			"commit_id": {shape: probeCommit, landing: landPath},
			"reply":     {shape: "resolved by rewriting the section", landing: landBody, at: "reply"},
		},
	},
}

// artifactActionWire is shared by adopt/close/ignore, which register the same
// schema and the same builder.
var artifactActionWire = toolWire{
	base: map[string]any{"work_item_id": probeWI, "memory_id": probeMemory},
	probes: map[string]wireProbe{
		"work_item_id": {shape: probeWI, landing: landBody, at: "work_item_id"},
		// Nested AND renamed.
		"memory_id":     {shape: probeMemory, landing: landBody, at: "payload.artifact_key"},
		"artifact_type": {shape: "methodology.spec", landing: landBody, at: "payload.artifact_type"},
	},
}

// memoryToolsNotCoveredHere are tools registered by tools_memory.go that this
// file deliberately does not probe. An entry is a claim that the contract is
// asserted somewhere else, by name, so it can be checked.
var memoryToolsNotCoveredHere = map[string]string{
	"pf_recall": "its two hops are guarded param-by-param in recall_params_wiring_test.go " +
		"(schema ⊆ forwarded, by value) and recall_wire_query_test.go (the RawQuery sent), " +
		"with hops 3-4 in internal/server and all four joined in recall_threshold_e2e_db_test.go. " +
		"pf_recall also sends no body — it is the only memory tool whose parameters travel as a " +
		"query string — so probing it here would duplicate aihub#148's table in a second place " +
		"that could drift from it.",
}

// ─── the recorder ────────────────────────────────────────────────────────────

type recordedRequest struct {
	method string
	path   string
	body   map[string]any
}

// memoryWireStack is the real MCP server and the real pkg/client pointed at an
// httptest recorder, plus a workspace root holding the credential state file the
// mutating tools read.
type memoryWireStack struct {
	session *sdkmcp.ClientSession
	mu      sync.Mutex
	last    *recordedRequest
}

func newMemoryWireStack(t *testing.T) *memoryWireStack {
	t.Helper()
	s := &memoryWireStack{}

	root := t.TempDir()
	t.Setenv("POLYFORGE_WORKSPACE_ROOT", root)
	if err := config.WriteStateFile(&config.StateFile{
		WIID: probeWI, AttemptID: "ra_probe", ClaimEpoch: 3,
		SessionSecret: "secret_probe", Claimed: true,
	}); err != nil {
		t.Fatalf("write state file: %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, "artifact.md"),
		[]byte("read off disk by the MCP process\n"), 0o600); err != nil {
		t.Fatalf("write artifact fixture: %v", err)
	}

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rec := &recordedRequest{method: r.Method, path: r.URL.Path}
		var decoded map[string]any
		if err := json.NewDecoder(r.Body).Decode(&decoded); err == nil {
			rec.body = decoded
		}
		s.mu.Lock()
		s.last = rec
		s.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		// Enough for every handler under test to decode a result and return.
		_, _ = w.Write([]byte(`{"ok":true,"id":"mem_reply","memory_id":"mem_reply"}`))
	}))
	t.Cleanup(ts.Close)

	mcpServer := mcp.New(nil, client.New(ts.URL, "pfk_probe"))
	cTransport, sTransport := sdkmcp.NewInMemoryTransports()
	ctx := t.Context()
	go func() {
		srv, err := mcpServer.Connect(ctx, sTransport)
		if err != nil {
			return
		}
		_ = srv.Wait()
	}()
	cl := sdkmcp.NewClient(&sdkmcp.Implementation{Name: "memory-wire", Version: "1.0.0"}, nil)
	session, err := cl.Connect(ctx, cTransport, nil)
	if err != nil {
		t.Fatalf("mcp client connect: %v", err)
	}
	t.Cleanup(func() { _ = session.Close() })
	s.session = session
	return s
}

// call invokes a tool and returns the request the aihub server received.
func (s *memoryWireStack) call(t *testing.T, tool string, args map[string]any) *recordedRequest {
	t.Helper()
	s.mu.Lock()
	s.last = nil
	s.mu.Unlock()

	res, err := s.session.CallTool(t.Context(), &sdkmcp.CallToolParams{Name: tool, Arguments: args})
	if err != nil {
		t.Fatalf("call %s(%v): %v", tool, args, err)
	}
	if res.IsError {
		text := ""
		if tc, ok := res.Content[0].(*sdkmcp.TextContent); ok {
			text = tc.Text
		}
		t.Fatalf("call %s(%v) returned an error, so no request reached the wire: %s", tool, args, text)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.last == nil {
		t.Fatalf("call %s(%v) made no HTTP request at all", tool, args)
	}
	return s.last
}

// publishedSchemas returns each tool's published property set, read back from
// ListTools — hop 1 exactly as a model receives it.
func (s *memoryWireStack) publishedSchemas(t *testing.T) map[string]map[string]any {
	t.Helper()
	res, err := s.session.ListTools(t.Context(), nil)
	if err != nil {
		t.Fatalf("list tools: %v", err)
	}
	out := map[string]map[string]any{}
	for _, tool := range res.Tools {
		var schema struct {
			Properties map[string]any `json:"properties"`
		}
		raw, err := json.Marshal(tool.InputSchema)
		if err != nil {
			t.Fatalf("re-marshal %s schema: %v", tool.Name, err)
		}
		if err := json.Unmarshal(raw, &schema); err != nil {
			t.Fatalf("%s input schema is not valid JSON: %v", tool.Name, err)
		}
		out[tool.Name] = schema.Properties
	}
	return out
}

// ─── the tools this file must cover, read off the source ─────────────────────

var toolNameRE = regexp.MustCompile(`Name:\s+"(pf_[a-z_]+)"`)

// memoryToolNames returns the tools registered by tools_memory.go.
//
// Read from the source rather than hand-listed: a hand-written list of "the
// memory tools" would rot exactly as silently as the contract gap this file
// exists to catch — the next tool added to that file would simply not be
// covered, and nothing would say so. (dbtestcov audits ci.yml the same way, and
// for the same reason.)
func memoryToolNames(t *testing.T) []string {
	t.Helper()
	src, err := os.ReadFile("tools_memory.go")
	if err != nil {
		t.Fatalf("read tools_memory.go: %v", err)
	}
	seen := map[string]bool{}
	var names []string
	for _, m := range toolNameRE.FindAllStringSubmatch(string(src), -1) {
		if !seen[m[1]] {
			seen[m[1]] = true
			names = append(names, m[1])
		}
	}
	if len(names) < 10 {
		t.Fatalf("only %d tool names found in tools_memory.go (%v); the extraction regex has "+
			"stopped matching and every completeness check below would pass vacuously", len(names), names)
	}
	sort.Strings(names)
	return names
}

// ─── the guards ──────────────────────────────────────────────────────────────

// TestMemoryToolsEveryPublishedPropertyHasAWireProbe is the completeness half.
// Without it, adding a property and forgetting its probe would leave the value
// assertions silently narrower than the contract — which is how the contract
// drifted in the first place.
func TestMemoryToolsEveryPublishedPropertyHasAWireProbe(t *testing.T) {
	s := newMemoryWireStack(t)
	published := s.publishedSchemas(t)

	for _, tool := range memoryToolNames(t) {
		props, registered := published[tool]
		if !registered {
			t.Errorf("tools_memory.go registers %q but ListTools does not publish it", tool)
			continue
		}
		if reason, skipped := memoryToolsNotCoveredHere[tool]; skipped {
			if _, alsoProbed := memoryToolWire[tool]; alsoProbed {
				t.Errorf("%q is both exempted (%s) and probed; drop one", tool, reason)
			}
			continue
		}
		wire, covered := memoryToolWire[tool]
		if !covered {
			t.Errorf("tools_memory.go registers %q and this file neither probes it nor documents "+
				"why it is exempt. A tool with no probe is a tool whose published parameters may "+
				"never reach the server, which is aihub#325's exact signature.", tool)
			continue
		}
		for name := range props {
			if _, ok := wire.probes[name]; !ok {
				t.Errorf("%s publishes %q with no wire probe — a name or type check cannot tell "+
					"whether the value reaches the server", tool, name)
			}
		}
		for name := range wire.probes {
			if _, ok := props[name]; !ok {
				t.Errorf("%s has a wire probe for %q, which its schema does not publish — stale probe", tool, name)
			}
		}
	}
	// A tool exempted here must actually exist in that file, or the exemption is
	// excusing nothing.
	inFile := map[string]bool{}
	for _, n := range memoryToolNames(t) {
		inFile[n] = true
	}
	for tool := range memoryToolsNotCoveredHere {
		if !inFile[tool] {
			t.Errorf("%q is exempted but is not registered by tools_memory.go — stale exemption", tool)
		}
	}
	for tool := range memoryToolWire {
		if !inFile[tool] {
			t.Errorf("%q is probed here but is not registered by tools_memory.go — stale probe", tool)
		}
	}
}

// TestMemoryToolsForwardEveryPublishedPropertyByValue is THE assertion: for each
// published property, a call carrying it must put its value where the table
// says, in the bytes the aihub server receives.
//
// One call per property, rather than one call per tool with everything set at
// once: it isolates each parameter (so a property that only survives when some
// other one is also present is caught), and it is the only way to probe
// pf_save_artifact's `content` and `path`, which are mutually exclusive.
func TestMemoryToolsForwardEveryPublishedPropertyByValue(t *testing.T) {
	s := newMemoryWireStack(t)

	for _, tool := range memoryToolNames(t) {
		wire, covered := memoryToolWire[tool]
		if !covered {
			continue // reported by the completeness test above
		}
		names := make([]string, 0, len(wire.probes))
		for name := range wire.probes {
			names = append(names, name)
		}
		sort.Strings(names)

		for _, name := range names {
			probe := wire.probes[name]
			t.Run(fmt.Sprintf("%s/%s", tool, name), func(t *testing.T) {
				args := map[string]any{}
				for k, v := range wire.base {
					args[k] = v
				}
				for _, drop := range probe.dropBase {
					delete(args, drop)
				}
				args[name] = probe.shape

				want := probe.want
				if want == nil {
					want = probe.shape
				}
				req := s.call(t, tool, args)

				switch probe.landing {
				case landLocal:
					if probe.reason == "" {
						t.Fatalf("%s/%s is marked local-only with no reason", tool, name)
					}
					if _, present := lookupPath(req.body, probe.at); present {
						t.Errorf("%s publishes %q as consumed in-process (%s) but it WAS sent: %v",
							tool, name, probe.reason, req.body)
					}
				case landPath:
					if !strings.Contains(req.path, fmt.Sprintf("%v", want)) {
						t.Errorf("%s publishes %q and the request path %q does not carry its value %v",
							tool, name, req.path, want)
					}
				case landBody:
					got, present := lookupPath(req.body, probe.at)
					if !present {
						t.Errorf("%s publishes %q and the request body has nothing at %q: %s\n"+
							"The schema states a contract the transport does not keep: the caller's "+
							"argument vanishes with no error at any hop, and the server rejects or "+
							"silently mis-handles the call. This is aihub#325's exact signature.",
							tool, name, probe.at, mustJSON(t, req.body))
						return
					}
					if mustJSON(t, got) != mustJSON(t, want) {
						t.Errorf("%s: %q=%#v arrived at body.%s as %s, want %s",
							tool, name, probe.shape, probe.at, mustJSON(t, got), mustJSON(t, want))
					}
				}
			})
		}
	}
}

// TestMemoryToolsSendCredentialsWithTheirWorkItem is the invariant aihub#325
// broke, stated once for every tool that injects credentials.
//
// The server's rule is a PAIR: enforceMethodologyAttemptGate rejects a request
// that carries attempt_id/session_secret without the work_item_id they belong
// to. Any future tool that starts injecting credentials inherits that rule, and
// this catches it whether or not anyone remembers to add a probe.
func TestMemoryToolsSendCredentialsWithTheirWorkItem(t *testing.T) {
	s := newMemoryWireStack(t)
	checked := 0
	for _, tool := range memoryToolNames(t) {
		wire, covered := memoryToolWire[tool]
		if !covered {
			continue
		}
		req := s.call(t, tool, wire.base)
		if _, hasAttempt := lookupPath(req.body, "attempt_id"); !hasAttempt {
			continue // not a credentialed tool
		}
		checked++
		got, present := lookupPath(req.body, "work_item_id")
		if !present || got == "" {
			t.Errorf("%s sends attempt_id/session_secret with no work_item_id: %s\n"+
				"The server verifies the credential against that work item and answers 400 "+
				"BAD_REQUEST(\"work_item_id is required when attempt_id/session_secret are "+
				"provided\") without it.", tool, mustJSON(t, req.body))
		}
	}
	if checked == 0 {
		t.Fatal("no tool was found to send credentials at all; the assertion above ran over nothing")
	}
}

// ─── helpers ─────────────────────────────────────────────────────────────────

// lookupPath walks a dot-separated path into a decoded JSON body.
func lookupPath(body map[string]any, path string) (any, bool) {
	if body == nil || path == "" {
		return nil, false
	}
	var cur any = body
	for _, seg := range strings.Split(path, ".") {
		m, ok := cur.(map[string]any)
		if !ok {
			return nil, false
		}
		cur, ok = m[seg]
		if !ok {
			return nil, false
		}
	}
	return cur, true
}

func mustJSON(t *testing.T, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal %#v: %v", v, err)
	}
	return string(b)
}
