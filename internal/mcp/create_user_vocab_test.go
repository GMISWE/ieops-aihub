package mcp_test

// aihub#463 — pf_create_user published two closed vocabularies as prose:
// "User type: human or machine" and "Global role: writer or admin". Both are
// two-value CHECK constraints on the users table (0001_initial.sql), so the
// description was a sentence ABOUT a set rather than the set.
//
// aihub#396 settled the same question for the work-item fields, and aihub#411
// §6.1 T1-4 is why it has to be settled per DB CHECK rather than per field —
// "a per-field fix produces a fourth instance".
//
// ─── What the enum is worth here, stated exactly ───────────────────────────
//
// aihub#396's note says "the go-sdk validates an enum before the handler runs".
// Read on go-sdk v1.6.0, that holds only for the GENERIC registration path:
// applySchema -> resolved.Validate is wired in toolForErr, reached from the
// top-level AddTool[In, Out]. polyforge registers through the untyped METHOD
// (*mcp.Server).AddTool (internal/mcp/server.go, addTool), and Server.callTool
// on that path calls the handler directly with no schema step. So the enum
// constrains the CLIENT and not this process — measured below rather than
// asserted, because a claim of "the SDK will reject it" that is false is exactly
// the kind of belief that leaves the server unguarded.
//
// The refusal that IS load-bearing is domain.ValidateUserType /
// ValidateUserGlobalRole, called by handleCreateUser
// (internal/server/create_user_vocab_test.go).
//
//	go test ./internal/mcp/ -run TestCreateUserVocab -v

import (
	"sort"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/GMISWE/ieops-aihub/internal/domain"
)

// TestCreateUserVocabulariesArePublishedAsEnums asserts the IDENTITY of the
// published set and the set domain enforces, not a hard-coded list: a list
// written here would be a third copy of the vocabulary and would stay green
// while all three drifted away from the migration. (domain's side of that chain
// is TestUsersVocabulariesMatchTheMigrations, which parses the SQL.)
func TestCreateUserVocabulariesArePublishedAsEnums(t *testing.T) {
	wantType := append([]string(nil), domain.UserTypeList()...)
	sort.Strings(wantType)
	wantRole := append([]string(nil), domain.UserGlobalRoleList()...)
	sort.Strings(wantRole)

	// Anti-vacuity: an empty domain list would make the comparisons trivially
	// satisfiable by an empty enum — a published enum matching nothing.
	if len(wantType) == 0 || len(wantRole) == 0 {
		t.Fatalf("domain publishes %d user types and %d global roles — one vocabulary is empty",
			len(wantType), len(wantRole))
	}

	for _, tc := range []struct {
		param string
		want  []string
	}{
		{"user_type", wantType},
		{"role", wantRole},
	} {
		t.Run(tc.param, func(t *testing.T) {
			got := enumOfProp(t, "pf_create_user", tc.param)
			assert.Equal(t, tc.want, got,
				"the published enum and the vocabulary the server enforces must be one set; a "+
					"caller offered a value the server refuses is the same defect as a caller "+
					"not being told about a value it accepts")
		})
	}
}

// TestCreateUserVocabEnumDoesNotRefuseInProcess is the measurement behind the
// paragraph above, and it is GREEN on both arms by design: it is not evidence
// for this change, it is what stops the enum from being MISTAKEN for the guard.
//
// An out-of-vocabulary role travels the whole in-process path and arrives in the
// POST body. If a future SDK upgrade starts validating untyped tools, this test
// goes red and the comments claiming otherwise get corrected with it — which is
// the only way a note about somebody else's code stays true.
func TestCreateUserVocabEnumDoesNotRefuseInProcess(t *testing.T) {
	f := newFakeAihub(t)
	// `maintainer` is a legal PROJECT MEMBER role and an illegal global one, so
	// it is the mistake a caller conflating the two vocabularies actually makes.
	callTool(t, f, "pf_create_user", map[string]any{
		"display_name": "Probe 463",
		"role":         "maintainer",
	})

	calls := f.recorded()
	if len(calls) != 1 {
		t.Fatalf("expected exactly one HTTP call, got %d (%v) — if this is zero, something in "+
			"this process refused the value and the enum IS a guard; update the comments in "+
			"internal/domain/user_fields.go and internal/mcp/tools_users.go, which say it is not",
			len(calls), f.paths())
	}
	if got := calls[0].Path; got != "/v1/admin/users" {
		t.Fatalf("pf_create_user posted to %q, want /v1/admin/users", got)
	}
	if got := calls[0].Body["role"]; got != "maintainer" {
		t.Errorf("role reached the POST body as %#v, want \"maintainer\" — the published enum "+
			"neither refuses nor rewrites it in this process", got)
	}
}
