package mcp

import (
	"encoding/json"
	"slices"
	"strings"
	"testing"

	"github.com/GMISWE/ieops-aihub/internal/domain"
)

// aihub#224 adds `sort`/`order` to pf_list_work_items. The defect class this
// guards against is the one mem_1SJ12mCz records: a param published in the MCP
// InputSchema but missing from the handler's forwarding loop. Nothing rejects
// the caller's argument, the server never sees it, and the schema is stating a
// contract the transport does not keep.
//
// ─── Scope: this file is hop 2 of four (aihub#280) ──────────────────────────
//
// A pf_list_work_items param has to survive four hops to do anything, and a
// green assertion on one of them says nothing about the other three:
//
//	hop 1  published MCP InputSchema           listWorkItemsSchema()
//	hop 2  MCP → HTTP query string             the three forwarding tables
//	hop 3  query param → ListWorkItemsFilter   handleListWorkItems
//	hop 4  filter field → SQL                  buildListWorkItemsWhere / ListWorkItems
//
// **A parameter contract with N hops needs N assertions.** Where each lives:
//
//	hops 1+2  this file
//	hop 3     internal/server/router_list_wi_params_test.go
//	hop 4     internal/domain/work_items_list_filters_test.go
//	1→4 e2e   internal/server/routes_wi_list_params_db_test.go (DB-gated)
//
// History, because the shape of the near-miss is the lesson. This comment used
// to end with an accurate inventory of six params that died at hop 3 or hop 4
// — `milestone`, `since`, `ready_only`, `include_step_state`, `source`,
// `f.Milestone`/`f.ReadyOnly` — followed by "that pre-existing end-to-end gap
// is tracked separately". It was not tracked separately. There was no wi
// number and no issue link, so "separately" pointed at nothing, and the gap
// went untouched for long enough that /pf-release was computing release scope
// from a `since` the server discarded, and pf-status/pf-retro/pf-execute were
// each opening with a call that returned 400. The guard never lied about its
// own boundary; the deferral it declared just had nowhere to land. aihub#280
// is that landing point — hence the hop table above, which names a file per
// hop instead of naming none.

// schemaPropTypes decodes a rendered tool schema into propName → declared type.
func schemaPropTypes(t *testing.T, raw json.RawMessage) map[string]string {
	t.Helper()
	var schema struct {
		Properties map[string]struct {
			Type string   `json:"type"`
			Enum []string `json:"enum"`
		} `json:"properties"`
	}
	if err := json.Unmarshal(raw, &schema); err != nil {
		t.Fatalf("input schema is not valid JSON: %v", err)
	}
	out := map[string]string{}
	for name, p := range schema.Properties {
		out[name] = p.Type
	}
	return out
}

// The core guard: every property the schema publishes must be forwarded by one
// of the three tables, and every forwarded key must be published. Either gap is
// a silently-dropped param.
func TestListWorkItemsToolForwardsEveryPublishedParam(t *testing.T) {
	published := schemaPropTypes(t, listWorkItemsSchema())

	// Each table declares which published JSON types its decoder can actually
	// read. strArg reads only strings, boolArg only booleans, csvArg either a
	// string or an array — so a CSV param may legitimately be published as
	// either. Anything else is a decoder that cannot read what is advertised.
	forwarded := map[string][]string{}
	for _, k := range listWorkItemsStringParams {
		forwarded[k] = []string{"string"}
	}
	for _, k := range listWorkItemsBoolParams {
		forwarded[k] = []string{"boolean"}
	}
	for _, k := range listWorkItemsCSVParams {
		forwarded[k] = []string{"string", "array"}
	}

	for name, typ := range published {
		accepted, ok := forwarded[name]
		if !ok {
			t.Errorf("schema publishes %q but the handler never forwards it — callers' argument is silently dropped (mem_1SJ12mCz)", name)
			continue
		}
		if !slices.Contains(accepted, typ) {
			t.Errorf("param %q is published as %q but its decoder only reads %v — the argument would be dropped",
				name, typ, accepted)
		}
	}
	for name := range forwarded {
		if _, ok := published[name]; !ok {
			t.Errorf("handler forwards %q but the schema does not publish it — callers cannot discover it", name)
		}
	}

	// No param may sit in two tables: two decoders would race to Set() the same
	// query key and which one wins would depend on loop order.
	seen := map[string]string{}
	for table, keys := range map[string][]string{
		"string": listWorkItemsStringParams,
		"bool":   listWorkItemsBoolParams,
		"csv":    listWorkItemsCSVParams,
	} {
		for _, k := range keys {
			if prev, dup := seen[k]; dup {
				t.Errorf("param %q appears in both the %s and %s forwarding tables", k, prev, table)
			}
			seen[k] = table
		}
	}
}

// Hop 2, the half the table-agreement test above cannot see: agreeing on a name
// is not the same as the decoder being able to read the value. `ids` and
// `status` are the two params real callers send as arrays, and strArg returns ""
// for a non-string — so before aihub#280 the name agreed, the tables agreed, and
// the value was still discarded. These assert the actual decode.
func TestListWorkItemsCSVArgAcceptsArrayAndString(t *testing.T) {
	for _, tc := range []struct {
		name string
		args map[string]any
		want string
	}{
		// The literal shape pf-retro / pf-status / pf-execute send.
		{"array of one", map[string]any{"ids": []any{"wi_abc"}}, "wi_abc"},
		{"array of several", map[string]any{"ids": []any{"wi_a", "wi_b"}}, "wi_a,wi_b"},
		// The literal shape pf-release sent for status.
		{"status as array", map[string]any{"status": []any{"wrapped"}}, "wrapped"},
		// CSV strings must keep working unchanged.
		{"plain string", map[string]any{"ids": "wi_a,wi_b"}, "wi_a,wi_b"},
		// Non-specification must stay distinguishable from an empty selection.
		{"absent", map[string]any{}, ""},
		{"nil", map[string]any{"ids": nil}, ""},
		{"empty array", map[string]any{"ids": []any{}}, ""},
		{"array of non-strings", map[string]any{"ids": []any{1, true}}, ""},
		{"wrong scalar type", map[string]any{"ids": 42}, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			key := "ids"
			if _, ok := tc.args["status"]; ok {
				key = "status"
			}
			if got := csvArg(tc.args, key); got != tc.want {
				t.Errorf("csvArg(%#v, %q) = %q, want %q", tc.args, key, got, tc.want)
			}
		})
	}

	// The regression that motivated csvArg: strArg cannot read the array form.
	// If this ever starts returning the value, csvArg has become redundant —
	// which is worth knowing, not worth silently keeping.
	if got := strArg(map[string]any{"ids": []any{"wi_abc"}}, "ids"); got != "" {
		t.Errorf("strArg on an array returned %q; csvArg exists because it returns \"\"", got)
	}
}

// Hop 2 end-to-end: the exact argument maps the polyforge skills send must come
// out of buildListWorkItemsParams as query params the server can act on.
//
// These are transcribed from the real call sites, not invented, because the bug
// was that the real call sites' shapes differed from the published ones:
//
//	pf-status / pf-retro   pf_list_work_items(ids=[<id>], include_step_state=true)
//	pf-execute             pf_list_work_items(ids=[<id>])
//	pf-release cut         pf_list_work_items(project=…, status=["wrapped"], since=…, limit=50)
//	pf-release promote     pf_list_work_items(project=…, scenario="release", label="alpha", status=["wrapped"], limit=10)
func TestListWorkItemsForwardsRealSkillCallShapes(t *testing.T) {
	for _, tc := range []struct {
		name string
		args map[string]any
		want map[string]string
	}{
		{
			name: "pf-status / pf-retro single wi view",
			args: map[string]any{"ids": []any{"wi_m9NscbfS"}, "include_step_state": true},
			want: map[string]string{"ids": "wi_m9NscbfS", "include_step_state": "true"},
		},
		{
			name: "pf-execute opening call",
			args: map[string]any{"ids": []any{"wi_m9NscbfS"}},
			want: map[string]string{"ids": "wi_m9NscbfS"},
		},
		{
			name: "pf-release cut: release scope since the last release",
			args: map[string]any{
				"project": "aihub",
				"status":  []any{"wrapped"},
				"since":   "2026-08-01T00:00:00Z",
				"limit":   "50",
			},
			want: map[string]string{
				"project": "aihub", "status": "wrapped",
				"since": "2026-08-01T00:00:00Z", "limit": "50",
			},
		},
		{
			// Transcribed as-is, including scenario="release" — which is what
			// pf-release really sends. Asserting the transport forwards it is
			// correct and is all this test claims. Whether it *matches* is a
			// separate question with a separate answer: work_items.scenario is
			// CHECKed to ('coding','writing','data') and CreateWorkItem rejects
			// all but 'coding', so no row can hold 'release' and this filter now
			// correctly returns nothing. Making release wis real is aihub#176;
			// the call site carries a note saying so.
			name: "pf-release promote: release wis only",
			args: map[string]any{
				"project": "aihub", "scenario": "release", "label": "alpha",
				"status": []any{"wrapped"}, "limit": "10",
			},
			want: map[string]string{
				"project": "aihub", "scenario": "release", "label": "alpha",
				"status": "wrapped", "limit": "10",
			},
		},
		{
			name: "multi-status CSV string keeps working",
			args: map[string]any{"project": "aihub", "status": "running,paused"},
			want: map[string]string{"project": "aihub", "status": "running,paused"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := buildListWorkItemsParams(tc.args)
			for k, want := range tc.want {
				if got.Get(k) != want {
					t.Errorf("query param %q = %q, want %q — this skill's filter is dropped on the way to the server",
						k, got.Get(k), want)
				}
			}
			// Nothing extra: a param the caller did not send must not appear,
			// or the server would filter on a value nobody asked for.
			if len(got) != len(tc.want) {
				t.Errorf("query string has %d params, want %d: %v", len(got), len(tc.want), got)
			}
		})
	}
}

// A boolean that is false must not be forwarded at all. Sending
// `ready_only=false` would be read by the handler as "not specified" today, but
// forwarding it makes the two indistinguishable on the wire — the exact
// zero-value-vs-absent confusion this contract keeps tripping over.
func TestListWorkItemsOmitsFalseBooleans(t *testing.T) {
	got := buildListWorkItemsParams(map[string]any{
		"project": "aihub", "ready_only": false, "include_step_state": false,
	})
	for _, k := range listWorkItemsBoolParams {
		if _, present := got[k]; present {
			t.Errorf("%q=false must not be forwarded; got %v", k, got)
		}
	}
}

// `kind` is published as a deprecated alias of `wi_type`, so both must reach the
// server. Publishing only one of a synonym pair is how the alias silently stops
// working (aihub#280).
func TestListWorkItemsPublishesBothTypeSpellings(t *testing.T) {
	published := schemaPropTypes(t, listWorkItemsSchema())
	for _, name := range []string{"wi_type", "kind"} {
		if _, ok := published[name]; !ok {
			t.Errorf("schema must publish %q: /ui and handleListWorkItems both treat wi_type and kind as synonyms", name)
		}
	}
	var schema struct {
		Properties map[string]struct {
			Description string `json:"description"`
		} `json:"properties"`
	}
	if err := json.Unmarshal(listWorkItemsSchema(), &schema); err != nil {
		t.Fatalf("input schema is not valid JSON: %v", err)
	}
	// A caller reading the schema has to be able to tell which spelling to use;
	// two identically-described params for one filter is the "third spelling"
	// failure in slow motion.
	if desc := schema.Properties["kind"].Description; !strings.Contains(strings.ToUpper(desc), "DEPRECATED") ||
		!strings.Contains(desc, "wi_type") {
		t.Errorf("kind's description must mark it deprecated and point at wi_type; got %q", desc)
	}
}

// The published enums must be the server's enforced sets, not a retyped copy
// that can drift from the validator.
func TestListWorkItemsToolPublishesServerEnums(t *testing.T) {
	var schema struct {
		Properties map[string]struct {
			Enum        []string `json:"enum"`
			Description string   `json:"description"`
		} `json:"properties"`
	}
	if err := json.Unmarshal(listWorkItemsSchema(), &schema); err != nil {
		t.Fatalf("input schema is not valid JSON: %v", err)
	}

	for _, tc := range []struct {
		param string
		want  []string
	}{
		{"sort", domain.ListWorkItemsSortValues()},
		{"order", domain.ListWorkItemsOrderValues()},
	} {
		p, ok := schema.Properties[tc.param]
		if !ok {
			t.Errorf("schema has no %q property", tc.param)
			continue
		}
		// Compare as sets: the contract is *which* values are legal. Pinning the
		// order would over-constrain — the schema dump consumed by Contract Lint
		// sorts enum values anyway.
		got := map[string]bool{}
		for _, v := range p.Enum {
			got[v] = true
		}
		for _, want := range tc.want {
			if !got[want] {
				t.Errorf("%s enum %v is missing the enforced value %q", tc.param, p.Enum, want)
			}
			delete(got, want)
		}
		for extra := range got {
			t.Errorf("%s enum publishes %q, which the server does not enforce", tc.param, extra)
		}
		// Every published value must actually pass the validator.
		for _, v := range p.Enum {
			var err *domain.AihubError
			if tc.param == "sort" {
				_, _, err = domain.NormalizeListWorkItemsSort(v, "")
			} else {
				_, _, err = domain.NormalizeListWorkItemsSort("", v)
			}
			if err != nil {
				t.Errorf("published %s value %q is rejected by the server: %v", tc.param, v, err)
			}
		}
	}

	// sort=closed_at narrows the result set. That is a surprise unless the
	// schema says so, since it is the caller's only contract.
	sortDesc := schema.Properties["sort"].Description
	if !strings.Contains(sortDesc, domain.ListWorkItemsSortClosedAt) ||
		!strings.Contains(strings.ToLower(sortDesc), "only closed") {
		t.Errorf("sort description must state that %s returns only closed items; got %q",
			domain.ListWorkItemsSortClosedAt, sortDesc)
	}
}
