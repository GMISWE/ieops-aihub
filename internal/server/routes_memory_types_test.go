package server

// aihub#289 — GET /v1/memories must not accept a `type` value with '|' in it, and must
// not accept it SILENTLY.
//
// The bug: `type` is a list. Three SKILL.md templates taught `type="a|b|c"`, and nothing
// in the chain — MCP tool, this handler, or either SQL builder — ever split on '|'. The
// whole string arrived as ONE type name, matched no row on the exact branch and none on
// the LIKE branch, and returned `{"items":null,"total":0}`. No error. No warning. That
// empty set is indistinguishable from "this project holds no such memory", so an agent
// following the Memory-First rule read it as "no prior experience exists".
//
// These tests need NO DATABASE, deliberately, and that is load-bearing twice over:
//
//  1. A DB-gated test SKIPS when AIHUB_TEST_DB is unset while `go test` still prints ok
//     and exits 0 — so it would be worth nothing in any run that lacks a database.
//  2. The nil pool IS the assertion that the rejection happens BEFORE the query: if the
//     guard ever stops firing, the handler falls through to domain.Recall(ctx, nil, ...)
//     and panics instead of quietly going green. (Same technique as
//     TestCreateWorkItem_ViewerGets403BeforeDBWrite in router_auth_test.go.)

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/GMISWE/ieops-aihub/internal/domain"
)

func typeGuardUser() *UserContext {
	return &UserContext{
		UserID:       "u_types_test",
		Role:         "member",
		ProjectRoles: map[string]string{"p_types_test": "viewer"},
	}
}

func TestHandleRecall_RejectsPipedTypeValue(t *testing.T) {
	for _, tc := range []struct {
		name     string
		rawQuery string
		offender string
	}{
		{
			name:     "the exact string the templates taught",
			rawQuery: "project=p_types_test&type=" + urlEnc("methodology.spec|methodology.plan|fact.*|rule.*|experience.*"),
			offender: "methodology.spec|methodology.plan|fact.*|rule.*|experience.*",
		},
		{
			name:     "the memory-first fragment's shorter form",
			rawQuery: "project=p_types_test&type=" + urlEnc("experience.*|rule.*"),
			offender: "experience.*|rule.*",
		},
		{
			// A pipe in ANY entry is rejected, not just a lone entry — otherwise a
			// caller mixing the two spellings gets the working half plus a silently
			// dead half, which is the same failure wearing a healthier-looking total.
			name:     "one bad entry among good ones",
			rawQuery: "project=p_types_test&type=" + urlEnc("rule.work,fact.a|fact.b,experience.*"),
			offender: "fact.a|fact.b",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c, rec := newRecallRequest(t, tc.rawQuery, typeGuardUser())

			err := handleRecall(nil)(c)
			require.NoError(t, err, "the guard writes the response itself; it must not also bubble an error")
			require.Equal(t, http.StatusBadRequest, rec.Code)

			var body map[string]any
			require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))

			msg, _ := body["message"].(string)
			// The message is the entire fix from the caller's side, so assert what it
			// has to carry: WHICH value is wrong, and WHAT to write instead. A 400
			// saying only "bad request" would swap a silent failure for a mute one.
			require.Contains(t, msg, tc.offender, "the message must name the offending value")
			require.Contains(t, msg, "not a separator", "the message must say why")
			require.Contains(t, msg, `type=["a.b","c.*"]`, "the message must show the working form")
		})
	}
}

// The other half of the guard: the forms that MUST still work. Without this the check
// above is satisfied by a handler that rejects every type filter ever sent.
func TestHandleRecall_AcceptsPipeFreeTypeValues(t *testing.T) {
	for _, rawQuery := range []string{
		"project=p_types_test&type=rule.work",
		"project=p_types_test&type=rule.work,experience.*",
		"project=p_types_test&type=" + urlEnc("methodology.spec,methodology.plan,fact.*,rule.*,experience.*"),
		"project=p_types_test", // no type filter at all
	} {
		t.Run(rawQuery, func(t *testing.T) {
			c, rec := newRecallRequest(t, rawQuery, typeGuardUser())

			// With no '|' present the handler must get past the guard and reach the
			// query — which, on a nil pool, panics. That panic is the PROOF it got
			// through: a 400 here would mean the guard over-fired.
			require.Panics(t, func() { _ = handleRecall(nil)(c) },
				"a pipe-free type filter must reach domain.Recall (the nil pool panics there); "+
					"got status %d body %s instead", rec.Code, rec.Body.String())
		})
	}
}

// firstPipedType is the whole decision, isolated. Kept separate from the handler tests so
// the boundary cases are readable rather than buried in query strings.
func TestFirstPipedType(t *testing.T) {
	for _, tc := range []struct {
		types     []string
		want      string
		wantFound bool
	}{
		{nil, "", false},
		{[]string{}, "", false},
		{[]string{"rule.work"}, "", false},
		{[]string{"rule.work", "experience.*"}, "", false},
		{[]string{"a|b"}, "a|b", true},
		{[]string{"rule.work", "a|b"}, "a|b", true},
		{[]string{"a|b", "c|d"}, "a|b", true}, // first offender wins
		{[]string{"|"}, "|", true},
		{[]string{"trailing|"}, "trailing|", true},
	} {
		got, found := firstPipedType(tc.types)
		require.Equal(t, tc.wantFound, found, "types=%v", tc.types)
		require.Equal(t, tc.want, got, "types=%v", tc.types)
	}
}

// urlEnc percent-encodes the characters that matter in these query strings. Written out
// rather than using url.Values.Encode() so the test bodies above read as the literal
// query a caller sends.
func urlEnc(s string) string {
	r := strings.NewReplacer("|", "%7C", "*", "%2A", ",", "%2C")
	return r.Replace(s)
}

// TestHandleRecall_ReportsUnmatchedTypes closes the one hop the domain tests cannot
// reach: handleRecall computing unmatched_types and it surviving into the JSON body.
//
// The chain from a wrong type name to a model that knows about it is four hops —
// domain.UnmatchedTypes computes it, handleRecall assigns it, RecallResponse
// serialises it, slimRecallResult forwards it to the MCP surface — and hops 1 and 4
// are covered elsewhere (memory_unmatched_test.go, recall_slim_test.go). A contract
// with four hops needs four assertions; without this one, deleting the assignment in
// handleRecall leaves every other test in this change green.
//
// DB-gated, so it is wired into ci.yml as a narrowly scoped step that greps for its
// own "--- PASS:" line and rejects a SKIP — without that, an unset AIHUB_TEST_DB makes
// `go test` print ok and exit 0, and this file's whole reason for existing evaporates.
//
//	AIHUB_TEST_DB=postgres://postgres:testpass@localhost:5432/aihub_test?sslmode=disable \
//	  go test ./internal/server/ -run TestHandleRecall_ReportsUnmatchedTypes -v -count=1
func TestHandleRecall_ReportsUnmatchedTypes(t *testing.T) {
	pool := setupStepTestDB(t)
	uid, project := seedStepTestUserAndProject(t, pool)
	_, err := pool.Exec(context.Background(), `DELETE FROM memories WHERE project=$1`, project)
	require.NoError(t, err)
	seedRecallTestMemories(t, pool, project, uid, 3) // all type fact.note

	uc := &UserContext{
		UserID:       uid,
		DisplayName:  uid,
		Role:         "writer",
		ProjectRoles: map[string]string{project: "viewer"},
	}

	decode := func(t *testing.T, rawQuery string) domain.RecallResponse {
		t.Helper()
		c, rec := newRecallRequest(t, rawQuery, uc)
		require.NoError(t, handleRecall(pool)(c))
		require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
		var resp domain.RecallResponse
		require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
		return resp
	}

	t.Run("a type that matches nothing is reported in the body", func(t *testing.T) {
		resp := decode(t, "project="+project+"&type=zzz.definitely.not.a.type")
		require.Empty(t, resp.Items)
		require.Equal(t, 0, resp.Total)
		require.Equal(t, []string{"zzz.definitely.not.a.type"}, resp.UnmatchedTypes,
			"an empty result set with no explanation is the failure this work item removes")
	})

	t.Run("partial: rows come back AND the dead entry is named", func(t *testing.T) {
		resp := decode(t, "project="+project+"&type=fact.note,rule.nonexistent")
		require.Len(t, resp.Items, 3, "the good entry still returns its rows")
		require.Equal(t, []string{"rule.nonexistent"}, resp.UnmatchedTypes,
			"a healthy-looking result must still disclose the entry that contributed nothing")
	})

	t.Run("healthy recalls carry no field at all", func(t *testing.T) {
		c, rec := newRecallRequest(t, "project="+project+"&type=fact.note", uc)
		require.NoError(t, handleRecall(pool)(c))
		require.Equal(t, http.StatusOK, rec.Code)
		require.NotContains(t, rec.Body.String(), "unmatched_types",
			"omitempty must keep the field off the wire when there is nothing to report, "+
				"so the majority of recalls pay no tokens for it")
	})

	t.Run("no type filter, no field", func(t *testing.T) {
		c, rec := newRecallRequest(t, "project="+project, uc)
		require.NoError(t, handleRecall(pool)(c))
		require.Equal(t, http.StatusOK, rec.Code)
		require.NotContains(t, rec.Body.String(), "unmatched_types")
	})
}
