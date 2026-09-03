package server

// The BEHAVIOURAL half of the aihub#255 / #267 / #340 gate.
//
// queryparam_gate_test.go proves every parse goes through the shared readers.
// It cannot prove the readers behave as the policy says, and it cannot prove the
// handler picked the right one — so this file runs the real handlers over real
// HTTP requests and asserts the two rules at each site the three wis name.
//
// Every case here runs against a NIL POOL, and that is load-bearing rather than
// convenient: a 400 reached with no database means the rejection happened BEFORE
// the query, which is the whole point of validating at the edge. If a guard ever
// stops firing, the handler falls through to a DB call on a nil pool and panics
// instead of quietly going green. (Same technique as
// TestHandleRecall_RejectsPipedTypeValue and TestCreateWorkItem_ViewerGets403
// BeforeDBWrite.)

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/labstack/echo/v4"
	"github.com/stretchr/testify/require"

	"github.com/GMISWE/ieops-aihub/internal/domain"
)

func policyTestUser() *UserContext {
	return &UserContext{
		UserID:       "u_qp_policy",
		Role:         "member",
		ProjectRoles: map[string]string{"p_qp_policy": "viewer"},
	}
}

// getReadyQueueRequest issues one GET /v1/work_items/ready against a nil pool.
func getReadyQueueRequest(t *testing.T, rawQuery string) *httptest.ResponseRecorder {
	t.Helper()
	e := echo.New()
	req := httptest.NewRequest(http.MethodGet, "/v1/work_items/ready?"+rawQuery, nil)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	setUser(c, policyTestUser())
	if err := handleGetReadyQueue(nil)(c); err != nil {
		e.HTTPErrorHandler(err, c)
	}
	return rec
}

// listEventsRequest issues one GET /v1/events against a nil pool.
func listEventsRequest(t *testing.T, rawQuery string) *httptest.ResponseRecorder {
	t.Helper()
	e := echo.New()
	req := httptest.NewRequest(http.MethodGet, "/v1/events?"+rawQuery, nil)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	setUser(c, policyTestUser())
	if err := handleListEvents(nil)(c); err != nil {
		e.HTTPErrorHandler(err, c)
	}
	return rec
}

// recallRequestNilPool issues one GET /v1/memories against a nil pool.
func recallRequestNilPool(t *testing.T, rawQuery string) *httptest.ResponseRecorder {
	t.Helper()
	c, rec := newRecallRequest(t, rawQuery, policyTestUser())
	if err := handleRecall(nil)(c); err != nil {
		c.Echo().HTTPErrorHandler(err, c)
	}
	return rec
}

// TestPolicyRule1_MalformedParamsAreRejectedEverywhere is the cross-endpoint
// sweep: ONE table over FOUR handlers, so a fix applied to three of them fails
// here rather than reading as done.
//
// This is the assertion the three wis are really about. Before the fix MOST rows
// below returned HTTP 200 with a substituted value and no way for the caller to
// find out — measured on origin/main 3753044, not inferred.
//
// ⚠️ "most", not "every", and the exception is the point of saying so: `ready_only`
// and `include_step_state` ALREADY returned 400 on origin/main (aihub#280 got
// there first, via the since-absorbed parseListWIBool). Those rows prove nothing about this
// change; they are here so a future edit that routes the booleans through a
// laxer reader is caught, which is a different job from demonstrating the fix.
// A universal claim labelled "measured" with a counterexample inside its own
// table is worse than no claim, because the next reader trusts the label.
func TestPolicyRule1_MalformedParamsAreRejectedEverywhere(t *testing.T) {
	for _, tc := range []struct {
		name     string
		run      func(t *testing.T, q string) *httptest.ResponseRecorder
		query    string
		mustName string // the offending value the message has to quote
	}{
		// ── GET /v1/work_items (aihub#255, aihub#267) ───────────────────────
		{"work_items limit not a number", listWIRec, "project=testproject&limit=abc", "abc"},
		// 🔴 The one that motivates using Atoi over fmt.Sscanf. Sscanf("12abc")
		// succeeded with n=12, so a caller's malformed page size became a page of
		// 12 — a request they never made, substituted silently.
		{"work_items limit numeric prefix", listWIRec, "project=testproject&limit=12abc", "12abc"},
		{"work_items limit boolean", listWIRec, "project=testproject&limit=true", "true"},
		{"work_items status not in vocabulary", listWIRec, "project=testproject&status=open", "open"},
		{"work_items status one bad among good", listWIRec, "project=testproject&status=queued,open,running", "open"},
		{"work_items ready_only not a bool", listWIRec, "project=testproject&ready_only=yes", "yes"},

		// ── GET /v1/work_items/ready (aihub#340) ────────────────────────────
		{"ready max not a number", getReadyQueueRequest, "project=p_qp_policy&max=abc", "abc"},
		{"ready max numeric prefix", getReadyQueueRequest, "project=p_qp_policy&max=5x", "5x"},

		// ── GET /v1/memories (aihub#340) ────────────────────────────────────
		{"recall similarity_threshold garbage", recallRequestNilPool, "project=p_qp_policy&similarity_threshold=notanumber", "notanumber"},
		{"recall similarity_threshold above 1", recallRequestNilPool, "project=p_qp_policy&similarity_threshold=5", "5"},
		{"recall similarity_threshold negative", recallRequestNilPool, "project=p_qp_policy&similarity_threshold=-1", "-1"},
		{"recall min_strength garbage", recallRequestNilPool, "project=p_qp_policy&min_strength=notanumber", "notanumber"},
		{"recall min_strength negative", recallRequestNilPool, "project=p_qp_policy&min_strength=-2", "-2"},
		{"recall recency_weight garbage", recallRequestNilPool, "project=p_qp_policy&recency_weight=notanumber", "notanumber"},
		{"recall top_k garbage", recallRequestNilPool, "project=p_qp_policy&top_k=abc", "abc"},
		{"recall include_archived not a bool", recallRequestNilPool, "project=p_qp_policy&include_archived=yes", "yes"},
		// NaN and ±Inf parse fine in Go and compare false against every bound, so
		// they would sail through a naive range check and disable a filter from
		// the inside.
		{"recall similarity_threshold NaN", recallRequestNilPool, "project=p_qp_policy&similarity_threshold=NaN", "NaN"},
		{"recall min_strength Inf", recallRequestNilPool, "project=p_qp_policy&min_strength=Inf", "Inf"},

		// ── GET /v1/events (aihub#340, same class) ──────────────────────────
		{"events limit garbage", listEventsRequest, "project=p_qp_policy&limit=lots", "lots"},
		{"events pinned_first not a bool", listEventsRequest, "project=p_qp_policy&pinned_first=yes", "yes"},
		// 🔴 `since` on this endpoint was never validated: it went raw into an
		// `e.created_at > $N` bind, and `?since=abc` came back 200 with
		// {"events":null} — measured against a running server, not read off the
		// code. That is the same shape as similarity_threshold=notanumber: a
		// filter the caller believes is applied, returning an empty page that
		// looks like an answer. The identically-named param on /v1/work_items has
		// always been strict.
		{"events since not a timestamp", listEventsRequest, "project=p_qp_policy&since=abc", "abc"},
		// 🔴 "sent, but names nothing". This one is a REGRESSION GUARD against
		// this very change: routing `types` through a CSV reader that drops empty
		// entries turned `?types=,` from "match nothing" into "no filter at all",
		// i.e. the whole unfiltered stream, for a caller who asked to narrow it.
		{"events types names nothing", listEventsRequest, "project=p_qp_policy&types=,", "types"},
		{"recall type names nothing", recallRequestNilPool, "project=p_qp_policy&type=,", "type"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rec := tc.run(t, tc.query)
			require.Equal(t, http.StatusBadRequest, rec.Code,
				"a malformed parameter must be rejected, not substituted (queryparam.go Rule 1). body=%s", rec.Body.String())

			var body map[string]any
			require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
			msg, _ := body["message"].(string)
			// The message IS the fix from the caller's side. A 400 saying only
			// "bad request" swaps a silent failure for a mute one, and the caller
			// still cannot tell which of the eight params they got wrong.
			require.Contains(t, msg, tc.mustName,
				"the message must quote the offending value so the caller can find it")
		})
	}
}

// listWIRec adapts the existing GET /v1/work_items helper to the table's shape.
func listWIRec(t *testing.T, q string) *httptest.ResponseRecorder {
	t.Helper()
	return listWIRequest(t, nil, q)
}

// TestPolicyRule1_LegalValuesStillWork is the negative control, and the table
// above is worth nothing without it: "reject everything" satisfies every row up
// there. Each case here is the well-formed neighbour of a rejected one.
func TestPolicyRule1_LegalValuesStillWork(t *testing.T) {
	for _, tc := range []struct {
		name  string
		query string
		check func(t *testing.T, f domain.ListWorkItemsFilter)
	}{
		{"limit absent", "project=testproject", func(t *testing.T, f domain.ListWorkItemsFilter) {
			require.Equal(t, 50, f.Limit, "an absent limit takes the default")
		}},
		{"limit legal", "project=testproject&limit=17", func(t *testing.T, f domain.ListWorkItemsFilter) {
			require.Equal(t, 17, f.Limit)
		}},
		// Forwarded rather than corrected here, so the bound and its disclosure
		// stay in one place. The pre-fix handler turned this into 50 before the
		// domain ever saw it, which is why `limit=-5` was reported as no
		// adjustment at all.
		{"limit negative reaches the domain", "project=testproject&limit=-5", func(t *testing.T, f domain.ListWorkItemsFilter) {
			require.Equal(t, -5, f.Limit, "a non-positive limit is forwarded for the domain to normalise and disclose")
		}},
		{"every legal status", "project=testproject&status=queued,running,paused,blocked,wrapped,failed,cancelled",
			func(t *testing.T, f domain.ListWorkItemsFilter) {
				require.Equal(t, domain.WorkItemStatusValues(), f.Status,
					"all seven schema values must be accepted, in the caller's order")
			}},
		{"status absent", "project=testproject", func(t *testing.T, f domain.ListWorkItemsFilter) {
			require.Empty(t, f.Status)
		}},
		{"status with spaces", "project=testproject&status=queued,%20running", func(t *testing.T, f domain.ListWorkItemsFilter) {
			require.Equal(t, []string{"queued", "running"}, f.Status, "entries are trimmed before the vocabulary check")
		}},
		{"ready_only spellings", "project=testproject&ready_only=True", func(t *testing.T, f domain.ListWorkItemsFilter) {
			require.True(t, f.ReadyOnly)
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f, _, rec := captureListWIFilter(t, tc.query)
			require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
			tc.check(t, f)
		})
	}
}

// TestStatusFilterIsBoundedByConstruction is the aihub#255 assertion, and it is
// deliberately NOT phrased as "the list is at most N".
//
// A cardinality cap would be a number somebody has to keep in step with the
// load. The allowlist plus deduplication makes the bound STRUCTURAL: whatever a
// caller sends, the list that reaches SQL cannot contain a value outside the
// vocabulary and cannot contain one twice, so its length cannot exceed the
// vocabulary's. The measurement below sends 6,000 elements — the shape
// tether#146 measured on the proxy side, where a 49,459-byte `?status=` reached
// aihub — and asserts BOTH outcomes a caller can get, since 6,000 legal-but-
// repeated values and 6,000 bogus ones fail in different ways.
func TestStatusFilterIsBoundedByConstruction(t *testing.T) {
	t.Run("6000 bogus values are rejected, not queried", func(t *testing.T) {
		parts := make([]string, 6000)
		for i := range parts {
			parts[i] = "bogus"
		}
		rec := listWIRequest(t, nil, "project=testproject&status="+strings.Join(parts, ","))
		require.Equal(t, http.StatusBadRequest, rec.Code,
			"an unknown status used to reach Postgres as a 6000-element ANY() and return 200 with zero rows")
	})

	t.Run("6000 repeated legal values collapse to the vocabulary", func(t *testing.T) {
		parts := make([]string, 0, 6000)
		for i := 0; i < 6000; i++ {
			parts = append(parts, domain.WorkItemStatusValues()[i%7])
		}
		f, _, rec := captureListWIFilter(t, "project=testproject&status="+strings.Join(parts, ","))
		require.Equal(t, http.StatusOK, rec.Code, "every value is legal, so this request is legal")
		require.Len(t, f.Status, 7,
			"deduplication is what makes the bound structural: 6000 legal entries must reach SQL as at most the vocabulary")
		require.ElementsMatch(t, domain.WorkItemStatusValues(), f.Status)
	})
}
