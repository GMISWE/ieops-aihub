package server

// aihub#289 / B4 — /ui/memories is the SECOND reader of the `type` query parameter.
//
// handleRecall got the piped-type guard; this page did not. It built req.Types itself and
// called domain.Recall directly, so `/ui/memories?type=experience.*|rule.*` reproduced the
// original bug verbatim: no error, no diagnostic, an empty card grid under the words "No
// memories match the current filters" — and the piped value round-tripped back into the
// page's own links. Two readers of one parameter with opposite behaviour is how the next
// variant of this bug gets in, so both now go through parseRecallTypes.
//
// These need no database: recallMemoriesFn is overridden, which also makes the positive
// control exact — the pipe-free cases assert the recall was REACHED and with which types,
// rather than inferring it.

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/require"

	"github.com/GMISWE/ieops-aihub/internal/domain"
)

func uiTypeUser() *UserContext {
	return &UserContext{
		UserID:       "u_ui_types",
		DisplayName:  "u_ui_types",
		Role:         "member",
		ProjectRoles: map[string]string{"p_ui_types": "viewer"},
	}
}

func TestUIMemories_RejectsPipedTypeValue(t *testing.T) {
	for _, tc := range []struct {
		name     string
		target   string
		offender string
	}{
		{
			name:     "the memory-first form",
			target:   "/ui/memories?project=p_ui_types&type=experience.%2A%7Crule.%2A",
			offender: "experience.*|rule.*",
		},
		{
			name:     "the pf-spec template form",
			target:   "/ui/memories?project=p_ui_types&type=methodology.spec%7Cfact.%2A",
			offender: "methodology.spec|fact.*",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			// The recall must NOT be reached. Overridden with an empty result so a
			// regression that bypasses the guard fails on the assertions below rather
			// than nil-dereferencing the pool.
			capture, cleanup := withRecallOverride(nil)
			defer cleanup()

			c, rec := newMemoriesRequest(t, tc.target, uiTypeUser())
			require.NoError(t, handleUIMemories(nil, pageTemplate("memories.html.tmpl"))(c))
			require.Equal(t, http.StatusOK, rec.Code, "the page renders; the message is what changes")

			require.Empty(t, capture.Project,
				"the recall must not run for a rejected filter — reaching it means the "+
					"guard did not fire")

			body := rec.Body.String()
			require.Contains(t, body, "not a separator",
				"the page must explain the failure. An empty grid with no message IS the bug.")
			require.Contains(t, body, tc.offender, "the message must name the offending value")
			require.NotContains(t, body, "No memories match the current filters",
				"the misleading empty-state must not be what a piped type shows")
		})
	}
}

// TestUIMemories_PipeFreeTypeReachesTheRecall is the other half. Without it the check
// above is satisfied by a page that rejects every type filter ever supplied.
func TestUIMemories_PipeFreeTypeReachesTheRecall(t *testing.T) {
	for _, tc := range []struct {
		target string
		want   []string
	}{
		{"/ui/memories?project=p_ui_types&type=rule.work", []string{"rule.work"}},
		{"/ui/memories?project=p_ui_types&type=experience.%2A", []string{"experience.*"}},
		{"/ui/memories?project=p_ui_types", nil},
	} {
		t.Run(tc.target, func(t *testing.T) {
			capture, cleanup := withRecallOverride(nil)
			defer cleanup()

			c, rec := newMemoriesRequest(t, tc.target, uiTypeUser())
			require.NoError(t, handleUIMemories(nil, pageTemplate("memories.html.tmpl"))(c))
			require.Equal(t, http.StatusOK, rec.Code)

			require.Equal(t, "p_ui_types", capture.Project,
				"a pipe-free filter must REACH the recall; the guard must not over-fire")
			require.Equal(t, tc.want, capture.Types)
			require.NotContains(t, rec.Body.String(), "not a separator")
		})
	}
}

// TestParseRecallTypes covers the helper both surfaces now share.
func TestParseRecallTypes(t *testing.T) {
	for _, tc := range []struct {
		raw       string
		wantType  []string
		wantBad   string
		wantEmpty bool
	}{
		{"", nil, "", false},
		{"rule.work", []string{"rule.work"}, "", false},
		{"rule.work,experience.*", []string{"rule.work", "experience.*"}, "", false},
		{"a|b", nil, "a|b", false},
		{"rule.work,a|b", nil, "a|b", false},
		{"a|b,c|d", nil, "a|b", false},
		// aihub#340: entries are trimmed now, so the spelling a human writes
		// works instead of silently matching nothing on its second entry.
		{"rule.work, experience.*", []string{"rule.work", "experience.*"}, "", false},
		// 🔴 "sent, but names nothing" is its own outcome and must not collapse
		// into "no filter". It used to reach SQL as IN ('','') and match NOTHING;
		// trimming empties would otherwise turn it into the UNFILTERED stream,
		// which is the same silence pointing the other way.
		{",", nil, "", true},
		{" , , ", nil, "", true},
		{"   ", nil, "", false}, // all whitespace is "not sent"
	} {
		types, bad, empty := parseRecallTypes(tc.raw)
		require.Equal(t, tc.wantBad, bad, "raw=%q", tc.raw)
		require.Equal(t, tc.wantType, types, "raw=%q", tc.raw)
		require.Equal(t, tc.wantEmpty, empty, "raw=%q", tc.raw)
		if bad != "" || empty {
			require.Nil(t, types,
				"a rejected filter must not also hand back types — a caller that ignored "+
					"the error would then run an unguarded query")
		}
	}
}

// TestPipedTypeMessage_IsActionable pins what the shared message must carry. The API 400
// and the UI banner render this same string, so a vaguer rewrite degrades both at once.
func TestPipedTypeMessage_IsActionable(t *testing.T) {
	msg := pipedTypeMessage("experience.*|rule.*")
	for _, want := range []string{
		"experience.*|rule.*", // which value
		"not a separator",     // why
		`type=["a.b","c.*"]`,  // what to write instead
	} {
		require.Contains(t, msg, want)
	}
	require.False(t, strings.HasPrefix(msg, "Error"), "the message is embedded in prose surfaces")
}

// withUnmatchedOverride swaps the aihub#289 diagnostic for the duration of a test, behind
// the same seam as withRecallOverride. Needed because the handler must not reach the
// database directly — see unmatchedTypesFn in ui_handlers_memory.go.
func withUnmatchedOverride(unmatched []string, unavailable string) (calls *int, cleanup func()) {
	prev := unmatchedTypesFn
	n := 0
	unmatchedTypesFn = func(_ context.Context, _ *pgxpool.Pool, _ *domain.RecallRequest) ([]string, string) {
		n++
		return unmatched, unavailable
	}
	return &n, func() { unmatchedTypesFn = prev }
}

// TestUIMemories_ShowsUnmatchedTypeNotice covers the UI half of the diagnostic: the page
// used to render "No memories match the current filters" for a type that does not exist in
// the project at all, conflating a wrong filter with an empty project — the same conflation
// that made the API's silent empty set dangerous.
func TestUIMemories_ShowsUnmatchedTypeNotice(t *testing.T) {
	t.Run("empty result explains the type matched nothing", func(t *testing.T) {
		_, cleanupRecall := withRecallOverride(nil)
		defer cleanupRecall()
		calls, cleanup := withUnmatchedOverride([]string{"zzz.nope"}, "")
		defer cleanup()

		c, rec := newMemoriesRequest(t, "/ui/memories?project=p_ui_types&type=zzz.nope", uiTypeUser())
		require.NoError(t, handleUIMemories(nil, pageTemplate("memories.html.tmpl"))(c))

		body := rec.Body.String()
		require.Equal(t, 1, *calls, "the diagnostic must actually be consulted")
		require.Contains(t, body, "No memory of type zzz.nope exists in this project")
		require.NotContains(t, body, "No memories match the current filters",
			"the misleading empty-state must be replaced, not merely accompanied")
	})

	t.Run("a broken diagnostic says so rather than going quiet", func(t *testing.T) {
		_, cleanupRecall := withRecallOverride(nil)
		defer cleanupRecall()
		_, cleanup := withUnmatchedOverride(nil, "type diagnostic unavailable: boom")
		defer cleanup()

		c, rec := newMemoriesRequest(t, "/ui/memories?project=p_ui_types&type=rule.work", uiTypeUser())
		require.NoError(t, handleUIMemories(nil, pageTemplate("memories.html.tmpl"))(c))
		require.Contains(t, rec.Body.String(), "type diagnostic unavailable")
	})

	t.Run("nothing to report renders the ordinary empty state", func(t *testing.T) {
		_, cleanupRecall := withRecallOverride(nil)
		defer cleanupRecall()
		_, cleanup := withUnmatchedOverride(nil, "")
		defer cleanup()

		c, rec := newMemoriesRequest(t, "/ui/memories?project=p_ui_types&type=rule.work", uiTypeUser())
		require.NoError(t, handleUIMemories(nil, pageTemplate("memories.html.tmpl"))(c))
		require.Contains(t, rec.Body.String(), "No memories match the current filters",
			"with nothing to report the page must fall back to its normal empty state")
	})

	t.Run("no type filter never consults the diagnostic", func(t *testing.T) {
		_, cleanupRecall := withRecallOverride(nil)
		defer cleanupRecall()
		calls, cleanup := withUnmatchedOverride([]string{"should.not.appear"}, "")
		defer cleanup()

		c, rec := newMemoriesRequest(t, "/ui/memories?project=p_ui_types", uiTypeUser())
		require.NoError(t, handleUIMemories(nil, pageTemplate("memories.html.tmpl"))(c))
		require.Equal(t, 0, *calls, "an unfiltered recall must not pay for the diagnostic")
		require.NotContains(t, rec.Body.String(), "should.not.appear")
	})
}
