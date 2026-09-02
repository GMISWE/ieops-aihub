package mcp

// aihub#148 — pf_recall's parameter contract, hop 1 (published InputSchema) and
// hop 2 (the query string handed to pkg/client).
//
// pf_list_work_items has had a guard of this shape since aihub#224/#280.
// pf_recall had none, and drifted: `similarity_threshold` was published in the
// InputSchema, was fully and carefully implemented in
// internal/domain/memory_vector.go — down to a comment explaining how it
// interacts with COUNT(*) and with Postgres's bind protocol — and was carried by
// NEITHER hop in between. Measured live on 2026-08-29, read-only against
// production:
//
//	GET /v1/memories?project=ieops&query=<noise>&top_k=10
//	  no threshold                n=10 total=181 sims=[0.3997 0.3848 0.3853] … 0.3455
//	  &similarity_threshold=0.99  n=10 total=181 sims=[0.3997 0.3848 0.3853] … 0.3455
//
// Byte-identical. 0.99 should have returned nothing.
//
// ─── Scope: this file is hops 1 and 2 of four ────────────────────────────────
//
//	hop 1  published MCP InputSchema      recallSchema()
//	hop 2  MCP args -> HTTP query string  buildRecallParams()
//	hop 3  query param -> RecallRequest   handleRecall (internal/server)
//	hop 4  request field -> SQL           RecallWithVector (internal/domain)
//
// A parameter contract with N hops needs N assertions, and this file makes two
// of them. Where the other two live:
//
//	hops 3+4  internal/server/routes_memory_similarity_threshold_db_test.go
//	hops 1→4  internal/mcp/recall_threshold_e2e_db_test.go (through the real
//	          router, the real client and the real MCP handler)
//
// Copying pf_list_work_items' guard alone would have been the trap: it covers
// hop 1↔2 only, and hop 3 was equally empty here. A green subset assertion in
// this file says nothing about whether the server parses what it receives.

import (
	"fmt"
	"testing"
)

// recallLocalOnlyParams are published params that this process CONSUMES rather
// than forwards. Each needs a reason, because "published but not forwarded" is
// otherwise indistinguishable from the defect this file exists to catch.
var recallLocalOnlyParams = map[string]string{
	"fields": "aihub#313: the projection is applied to the response this process hands " +
		"the model, and this process is the last hop before the model — there is no " +
		"server hop for it to be dropped on. Forwarding it would ADD one (handleRecall " +
		"would ignore the query param), i.e. a fresh instance of aihub#148.",
}

// recallUnpublishedForwardedParams are forwarded but deliberately not published.
// The reverse direction of the same drift, and harmless only when it is
// intentional — so it is written down rather than tolerated by omission.
var recallUnpublishedForwardedParams = map[string]string{
	"cursor": "paging is driven by next_cursor from a previous response, not composed " +
		"by the model; publishing it would invite an invented cursor.",
	"recall_algo": "a plugin-build opt-in (POLYFORGE_RECALL_ALGO) into the opt3 L1 " +
		"lexical-relevance path, deliberately kept out of the model-visible contract.",
}

// recallWireProbes is hop 2 stated as VALUES: for each published param, the JSON
// shapes a caller may actually put on the wire, and the query value each must
// produce. "" means "correctly not forwarded".
//
// Values, not declared types, because type agreement is exactly what stayed
// green while `ready_only: "true"` and `status: ["wrapped"]` were being
// discarded (aihub#280). Nothing validates the published type at call time: the
// MCP SDK's untyped AddTool — the form every aihub tool uses — checks the schema
// shape at registration and then stores the handler with no per-call validator,
// so a wrongly-typed argument arrives at the decoder unchanged.
var recallWireProbes = map[string][]struct {
	shape any
	want  string
}{
	"project":      {{shape: "aihub", want: "aihub"}},
	"query":        {{shape: "token cost", want: "token cost"}},
	"visibility":   {{shape: "project", want: "project"}},
	"work_item_id": {{shape: "wi_abc", want: "wi_abc"}},
	// THE PARAMETER THIS WI IS ABOUT. 0.99 is the value the live probe used;
	// 0 is the off value and must stay off (see TestRecallThresholdHasNoDefault);
	// the quoted form is the mirror-image of defect 2 pointed at defect 1's own
	// parameter — numArg alone reads "0.99" as 0, which is this tool's "not
	// specified", so the filter would vanish exactly as it did before.
	"similarity_threshold": {
		{shape: float64(0.99), want: "0.99"},
		{shape: float64(0.5), want: "0.5"},
		{shape: float64(0), want: ""},
		{shape: "0.99", want: "0.99"},
		{shape: "not a number", want: ""},
	},
	// Published as a string; "max results: 10" is most naturally written as a
	// JSON number, and strArg returned "" for one — so the page size was dropped
	// and the server's default of 20 applied silently (aihub#148 defect 2).
	"top_k": {
		{shape: "5", want: "5"},
		{shape: float64(5), want: "5"},
		{shape: float64(200), want: "200"},
	},
	// wi section C: the params nobody had checked. min_strength and
	// recency_weight were the two the forwarding block already handled, and the
	// numeric-string spelling is now accepted for them too.
	"min_strength": {
		{shape: float64(0.7), want: "0.7"},
		{shape: "0.7", want: "0.7"},
		{shape: float64(0), want: ""},
	},
	"recency_weight": {
		{shape: float64(0.9), want: "0.9"},
		{shape: "0.9", want: "0.9"},
		{shape: float64(0), want: ""},
	},
	"include_archived": {
		{shape: true, want: "true"},
		{shape: false, want: ""},
		{shape: "true", want: "true"},
		{shape: float64(1), want: "true"},
	},
	// aihub#289's shape contract: an ARRAY of names, joined with commas. The
	// single-string form still has to work — the server splits on ',' and
	// rejects '|' with a 400 rather than matching nothing.
	"type": {
		{shape: []any{"experience.*", "rule.work"}, want: "experience.*,rule.work"},
		{shape: []any{"rule.work"}, want: "rule.work"},
		{shape: "experience.*,rule.work", want: "experience.*,rule.work"},
		{shape: []any{}, want: ""},
		{shape: float64(3), want: ""},
	},
	// Local-only: published, deliberately absent from the wire.
	"fields": {
		{shape: "brief", want: ""},
	},
}

// TestRecallEveryPublishedParamHasAWireProbe is the completeness half. Without
// it, adding a param and forgetting its probe would leave the value assertions
// silently narrower than the contract — which is how the contract drifted in the
// first place.
func TestRecallEveryPublishedParamHasAWireProbe(t *testing.T) {
	published := schemaPropTypes(t, recallSchema())
	if len(published) == 0 {
		t.Fatal("recallSchema() published no properties at all")
	}
	for name := range published {
		if len(recallWireProbes[name]) == 0 {
			t.Errorf("pf_recall publishes %q but recallWireProbes has no value probe for it — "+
				"a name or type check cannot tell whether the value reaches the server", name)
		}
	}
	for name := range recallWireProbes {
		if _, ok := published[name]; !ok {
			t.Errorf("recallWireProbes covers %q, which pf_recall's schema does not publish", name)
		}
	}
	// Every local-only exemption must name a param that is actually published;
	// a stale exemption would silently excuse a real drop.
	for name := range recallLocalOnlyParams {
		if _, ok := published[name]; !ok {
			t.Errorf("recallLocalOnlyParams exempts %q, which is not published — stale exemption", name)
		}
	}
}

// TestRecallPublishedSchemaIsASubsetOfWhatIsForwarded is THE assertion this wi
// adds: every param pf_recall advertises must survive hop 2, except the ones
// explicitly documented as consumed in-process.
//
// Stated as a subset over the SCHEMA rather than over a hand-written list, so a
// param added to the schema tomorrow is covered the day it is added.
func TestRecallPublishedSchemaIsASubsetOfWhatIsForwarded(t *testing.T) {
	published := schemaPropTypes(t, recallSchema())
	for name := range published {
		if reason, local := recallLocalOnlyParams[name]; local {
			// The exemption must be true, not merely claimed.
			got := buildRecallParams(map[string]any{"project": "aihub", name: recallWireProbes[name][0].shape})
			if got.Has(name) {
				t.Errorf("%q is documented as local-only (%s) but WAS forwarded as %q",
					name, reason, got.Get(name))
			}
			continue
		}
		probe := firstForwardingProbe(t, name)
		got := buildRecallParams(map[string]any{"project": "aihub", name: probe})
		if !got.Has(name) {
			t.Errorf("pf_recall publishes %q and buildRecallParams drops it (%#v -> query %v). "+
				"The schema states a contract the transport does not keep: the caller's argument "+
				"vanishes with no error at any hop. This is aihub#148's exact signature — it is "+
				"how similarity_threshold went from fully implemented in domain to unreachable.",
				name, probe, got)
		}
	}
}

// firstForwardingProbe returns the first probe shape for name that is supposed
// to produce a query param. A param whose only probes are "" would make the
// subset test above vacuous, so that is an error rather than a skip.
func firstForwardingProbe(t *testing.T, name string) any {
	t.Helper()
	for _, p := range recallWireProbes[name] {
		if p.want != "" {
			return p.shape
		}
	}
	t.Fatalf("no probe for %q expects to be forwarded, so the subset assertion would be vacuous", name)
	return nil
}

// TestRecallForwardsEveryPublishedParamByValue is the core guard at value level:
// every shape in recallWireProbes must produce the query value it claims.
func TestRecallForwardsEveryPublishedParamByValue(t *testing.T) {
	for name, probes := range recallWireProbes {
		for _, probe := range probes {
			t.Run(fmt.Sprintf("%s=%#v", name, probe.shape), func(t *testing.T) {
				got := buildRecallParams(map[string]any{"project": "aihub", name: probe.shape})
				if name == "project" {
					// project is also the fixed argument; assert it alone.
					got = buildRecallParams(map[string]any{name: probe.shape})
				}
				if v := got.Get(name); v != probe.want {
					t.Errorf("%s=%#v forwarded as %q, want %q — this caller's argument is silently dropped",
						name, probe.shape, v, probe.want)
				}
			})
		}
	}
}

// TestRecallThresholdHasNoDefault pins the decision the measurement forced.
//
// On project=ieops with limit=200: a punctuation-only noise query scored 0.4712
// at its WORST hit, while a real Chinese query whose top hit was the correct
// answer scored 0.4798 at its BEST — 0.0086 apart, in the wrong direction for
// most of the noise query's page. There is no global cutoff that blocks noise
// and passes true hits, so making the knob reachable is the whole job and
// turning it on by default would delete true positives to remove nothing.
func TestRecallThresholdHasNoDefault(t *testing.T) {
	for _, args := range []map[string]any{
		{"project": "aihub"},
		{"project": "aihub", "query": "token cost"},
		{"project": "aihub", "query": "token cost", "top_k": float64(50)},
		{"project": "aihub", "similarity_threshold": float64(0)},
	} {
		got := buildRecallParams(args)
		if got.Has("similarity_threshold") {
			t.Errorf("args %v forwarded similarity_threshold=%q; the filter must be OFF unless the "+
				"caller sets it (a global cutoff cannot separate noise from signal on this corpus)",
				args, got.Get("similarity_threshold"))
		}
	}
}

// TestRecallForwardsNothingTheCallerDidNotSend: a param the caller omitted must
// not appear, or the server filters on a value nobody asked for.
func TestRecallForwardsNothingTheCallerDidNotSend(t *testing.T) {
	t.Setenv("POLYFORGE_RECALL_ALGO", "")
	got := buildRecallParams(map[string]any{"project": "aihub", "query": "token cost"})
	want := map[string]string{"project": "aihub", "query": "token cost"}
	if len(got) != len(want) {
		t.Fatalf("query string has %d params, want %d: %v", len(got), len(want), got)
	}
	for k, v := range want {
		if got.Get(k) != v {
			t.Errorf("query param %q = %q, want %q", k, got.Get(k), v)
		}
	}
}

// TestRecallUnpublishedForwardedParamsAreDocumented closes the reverse
// direction: something forwarded but not published is discoverable by nobody, so
// it must at least be deliberate. Both entries are also asserted to still work,
// because an undocumented param is the easiest thing to break unnoticed.
func TestRecallUnpublishedForwardedParamsAreDocumented(t *testing.T) {
	t.Setenv("POLYFORGE_RECALL_ALGO", "")
	published := schemaPropTypes(t, recallSchema())
	for name := range recallUnpublishedForwardedParams {
		if _, ok := published[name]; ok {
			t.Errorf("%q is now published; move it into recallWireProbes and drop the exemption", name)
			continue
		}
		got := buildRecallParams(map[string]any{"project": "aihub", name: "probe-value"})
		if got.Get(name) != "probe-value" {
			t.Errorf("%q is documented as forwarded-but-unpublished, yet it was not forwarded: query %v", name, got)
		}
	}
	// The env fallback is the only way recall_algo reaches the server for a
	// plugin build that never passes it explicitly.
	t.Setenv("POLYFORGE_RECALL_ALGO", "l1_lexical")
	if got := buildRecallParams(map[string]any{"project": "aihub"}); got.Get("recall_algo") != "l1_lexical" {
		t.Errorf("POLYFORGE_RECALL_ALGO did not reach the wire: query %v", got)
	}
	// An explicit argument must win over the environment.
	if got := buildRecallParams(map[string]any{"project": "aihub", "recall_algo": "recency"}); got.Get("recall_algo") != "recency" {
		t.Errorf("explicit recall_algo must win over POLYFORGE_RECALL_ALGO: query %v", got)
	}
}
