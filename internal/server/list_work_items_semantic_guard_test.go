package server

// aihub#477. handleListWorkItems' semantic-search combination guard —
// "query/similar_to does not combine with sort, order, or cursor" — used to
// answer that question by re-reading the raw query string, while each of the
// three parameters had already been read, and given an answer, by its own
// reader a few lines away. The two readings did not agree.
//
// The disagreement is entirely about WHITESPACE, because that is the only input
// on which "trimmed" and "untrimmed" differ:
//
//	queryCursor                  TrimSpace, then "" means no cursor  (its doc:
//	                             "?cursor=%20 reads as no cursor, i.e. page one")
//	NormalizeListWorkItemsSort   TrimSpace+ToLower, then "" means
//	                             "the caller did not ask" and defaults
//	the guard, before this        `c.QueryParam(x) != ""`, untrimmed
//
// So `?query=foo&cursor=%20` came back 400 for combining with a cursor the
// handler's own cursor reader had already thrown away, and `?query=foo&sort=%20`
// came back 400 for combining with a sort the normalizer would have defaulted.
// Neither request contains the combination it was refused for.
//
// ─── What this file has to discriminate ────────────────────────────────────
//
// "The whitespace spellings stop 400ing" is satisfied completely by deleting
// the guard, which would be a much worse bug than the one being fixed: it is
// the aihub#267/#271 family, a parameter silently ignored rather than refused.
// So the moved-input arm is paired with a control arm that pins every
// combination that must STILL be refused, and the moved-input arm asserts
// equality against the same request without the parameter rather than just a
// 200 — "sort=%20 behaves exactly as if no sort had been sent" is the property,
// and it is the one that fails if the two readings drift apart again.
//
// DB-free through the listWorkItemsFn seam (captureListWIFilter), so this runs
// on CI's plain unit-test step.

import (
	"net/http"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/GMISWE/ieops-aihub/internal/domain"
)

// semanticSources are the two ways to ask for similarity ordering. Both inherit
// the same rejection, so every case below runs against both — a fix applied to
// the `query` path only would otherwise look complete.
//
// similar_to is a bare identifier: it never reaches a lookup here because the
// fake seam stands in for domain.ListWorkItems, which is where the source is
// resolved.
var semanticSources = []string{"query=foo", "similar_to=wi_source"}

// whitespaceSpellings are the values that are non-empty on the wire and empty
// to every reader in queryparam.go. `+` is included because that is what a form
// or a browser sends for a space, not a hand-written escape.
var whitespaceSpellings = []string{"%20", "%20%20", "+", "%09"}

// TestListWorkItems_SemanticGuardHonoursEachParamsOwnReader is the arm that was
// red before the fix.
//
// Asserted as EQUALITY against the same request with the parameter dropped, not
// as "not a 400": the claim is that a whitespace-only sort/order/cursor is
// indistinguishable from an absent one all the way to the filter, which is what
// makes the guard and the parameter's own reader agree rather than merely both
// permit.
func TestListWorkItems_SemanticGuardHonoursEachParamsOwnReader(t *testing.T) {
	for _, source := range semanticSources {
		baseline, _, baseRec := captureListWIFilter(t, "project=testproject&"+source)
		require.Equal(t, http.StatusOK, baseRec.Code,
			"the control request must be accepted or nothing below means anything: %s",
			baseRec.Body.String())

		for _, param := range []string{"sort", "order", "cursor"} {
			for _, ws := range whitespaceSpellings {
				t.Run(source+"/"+param+"="+ws, func(t *testing.T) {
					got, _, rec := captureListWIFilter(t,
						"project=testproject&"+source+"&"+param+"="+ws)
					require.Equal(t, http.StatusOK, rec.Code,
						"%s=%s carries no %s — %s reads it as absent, so refusing the request "+
							"for combining with one is a 400 for a combination it does not "+
							"contain: %s",
						param, ws, param, readerFor(param), rec.Body.String())
					require.Equal(t, baseline, got,
						"%s=%s must reach the domain layer exactly as an absent %s does",
						param, ws, param)
				})
			}
		}
	}
}

// readerFor names the reader that already decided about a parameter, so the
// failure message points at the disagreeing half rather than restating the
// assertion.
func readerFor(param string) string {
	if param == "cursor" {
		return "queryCursor"
	}
	return "NormalizeListWorkItemsSort"
}

// TestListWorkItems_SemanticGuardStillRefusesRealCombinations is the control.
//
// Every row here is 400 before AND after the fix. Without it the arm above is
// satisfied by deleting the guard outright — which would silently drop a
// parameter the caller supplied, i.e. reintroduce the defect class
// (aihub#267/#271) the guard exists for, while every assertion about
// whitespace went green.
func TestListWorkItems_SemanticGuardStillRefusesRealCombinations(t *testing.T) {
	for _, source := range semanticSources {
		for _, combo := range []string{
			"sort=created_at",
			"sort=closed_at",
			"order=asc",
			"order=desc",
			"cursor=2026-01-02T03:04:05.123456789Z",
			// Leading whitespace around a REAL value: trimming makes this a
			// supplied sort, not an absent one, so it must stay refused. This is
			// the row that fails if the fix over-reaches from "trim" to
			// "ignore anything with a space in it".
			"sort=%20closed_at",
			"cursor=%202026-01-02T03:04:05Z",
		} {
			t.Run(source+"/"+combo, func(t *testing.T) {
				_, reachedProject, rec := captureListWIFilter(t,
					"project=testproject&"+source+"&"+combo)
				require.Equal(t, http.StatusBadRequest, rec.Code,
					"%s+%s names a real combination and must still be refused loudly rather "+
						"than served with the parameter dropped: %s",
					source, combo, rec.Body.String())
				require.Contains(t, rec.Body.String(), "does not combine with",
					"the refusal must be the combination guard's, not some other 400 "+
						"that happens to share the status code: %s", rec.Body.String())
				// The seam records the project only when it is called at all, so
				// an empty one is "the domain was never entered" — the half of
				// the contract a status assertion cannot see.
				require.Empty(t, reachedProject,
					"a refused combination must be refused before the query runs")
			})
		}
	}
}

// TestListWorkItems_WhitespaceSortIsAbsentWithoutSemanticSearch pins the same
// inputs on the non-semantic path, where the guard never runs at all.
//
// It is the negative control for the guard's SCOPE: if `sort=%20` were somehow
// answered differently with and without a `query=`, the parameter would mean
// two things on one endpoint — which is the shape this whole change is removing,
// just relocated.
func TestListWorkItems_WhitespaceSortIsAbsentWithoutSemanticSearch(t *testing.T) {
	baseline, _, baseRec := captureListWIFilter(t, "project=testproject")
	require.Equal(t, http.StatusOK, baseRec.Code, baseRec.Body.String())
	require.Equal(t, domain.ListWorkItemsSortCreatedAt, baseline.Sort)
	require.Equal(t, domain.ListWorkItemsOrderDesc, baseline.Order)

	for _, param := range []string{"sort", "order", "cursor"} {
		for _, ws := range whitespaceSpellings {
			t.Run(param+"="+ws, func(t *testing.T) {
				got, _, rec := captureListWIFilter(t, "project=testproject&"+param+"="+ws)
				require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
				require.Equal(t, baseline, got,
					"%s=%s must be absent on the plain path too", param, ws)
			})
		}
	}
}
