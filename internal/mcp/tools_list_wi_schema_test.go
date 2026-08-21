package mcp

import (
	"encoding/json"
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
// Scope, stated precisely so the guarantee is not overread (code_review finding 7):
// this asserts only that the published schema and the two forwarding tables
// agree — i.e. the MCP layer forwards to the HTTP query string everything it
// advertises. It says nothing about whether the *server* then honours the param.
// Several already do not: handleListWorkItems parses no `milestone`, `since`,
// `ready_only` or `include_step_state`, and although it reads `source` into
// filter.Source, buildListWorkItemsWhere never consumes f.Source, f.Milestone or
// f.ReadyOnly. That pre-existing end-to-end gap is tracked separately; it is out
// of aihub#224's scope and these tests do not and cannot cover it.

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
// of the two tables, and every forwarded key must be published. Either gap is a
// silently-dropped param.
func TestListWorkItemsToolForwardsEveryPublishedParam(t *testing.T) {
	published := schemaPropTypes(t, listWorkItemsSchema())

	forwarded := map[string]string{}
	for _, k := range listWorkItemsStringParams {
		forwarded[k] = "string"
	}
	for _, k := range listWorkItemsBoolParams {
		forwarded[k] = "boolean"
	}

	for name, typ := range published {
		fwdType, ok := forwarded[name]
		if !ok {
			t.Errorf("schema publishes %q but the handler never forwards it — callers' argument is silently dropped (mem_1SJ12mCz)", name)
			continue
		}
		if fwdType != typ {
			t.Errorf("param %q is published as %q but forwarded as %q", name, typ, fwdType)
		}
	}
	for name := range forwarded {
		if _, ok := published[name]; !ok {
			t.Errorf("handler forwards %q but the schema does not publish it — callers cannot discover it", name)
		}
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
