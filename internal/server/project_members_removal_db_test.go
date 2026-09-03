package server

// aihub#333, the HTTP hop: does `expected_removals` survive the journey from a
// real PATCH body into domain.UpdateProject, and does an UNDECLARED shrink come
// back as a 412 with the names it refused to remove?
//
// This is the hop assertion, not the semantics. What the removal rule accepts
// and refuses is pinned in internal/domain (projects_members_removal_test.go for
// the pure set arithmetic, projects_members_removal_db_test.go for the
// transaction). What can only be checked here is that the parameter EXISTS as
// far as echo's binder is concerned: handleUpdateProject binds the body into
// domain.UpdateProjectRequest and names no field, so a precondition can be
// present in the MCP schema, present in the struct, and still be dropped on this
// hop — and a dropped precondition is indistinguishable at the call site from
// one that passed. aihub#241 shipped exactly that, twice.
//
// Written with raw JSON bodies rather than domain types so that it COMPILES
// against the pre-aihub#333 tree and fails there at RUNTIME, by actually losing
// two members. A build failure would only demonstrate that a symbol is new.
//
//	AIHUB_TEST_DB=postgres://postgres:testpass@localhost:5440/aihub_test?sslmode=disable \
//	go test ./internal/server/ -run 'TestProjectMembersRemovalHTTP' -v -count=1
//
// Requires migration 0032_projects_members_version.sql. aihub#333 itself needs
// no migration: expected_removals is a request-only precondition and is never
// stored.

import (
	"fmt"
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// membersRemovalSeed is the three-member list both halves of the property start
// from, as a PATCH body. Written out rather than built from a slice so the test
// asserts against literal wire bytes.
const membersRemovalSeed = `{"members":[` +
	`{"user_id":"u_one","role":"viewer"},` +
	`{"user_id":"u_two","role":"writer"},` +
	`{"user_id":"u_three","role":"maintainer"}]}`

// ── half one: an UNINTENDED shrink fails ────────────────────────────────────
//
// The caller holds the CORRECT members_version — nobody raced them — and sends
// a list that is short by two. aihub#260's compare-and-set cannot see this: the
// version matches, so the guard passes and the two members are gone. That is
// the whole of aihub#333.
func TestProjectMembersRemovalHTTPUndeclaredShrinkIs412(t *testing.T) {
	s := newMembersVersionStack(t)

	status, body := s.patch(t, membersRemovalSeed)
	require.Equal(t, http.StatusOK, status, "seed write failed: %v", body)
	require.Equal(t, float64(1), body["members_version"])

	status, body = s.patch(t, `{"members":[{"user_id":"u_one","role":"viewer"}],"members_version":1}`)
	assert.Equal(t, http.StatusPreconditionFailed, status,
		"a PATCH that removes u_two and u_three without declaring them returned %d. The caller's "+
			"members_version was CURRENT, so aihub#260's compare-and-set passed and two people lost "+
			"access with no error — a version counter cannot tell \"I mean to remove two\" from \"I lost "+
			"two\" (body: %v)", status, body)
	assert.Equal(t, "PROJECT_MEMBERS_UNDECLARED_REMOVAL", body["code"])

	// The names have to come back, or the caller learns only that something is
	// wrong. This is the half that reaches the operator's eyes.
	details, ok := body["details"].(map[string]any)
	require.True(t, ok, "the 412 envelope carries no details, so the caller cannot see WHO it was about "+
		"to remove (body: %v)", body)
	undeclared := fmt.Sprint(details["undeclared_removals"])
	assert.Contains(t, undeclared, "u_two")
	assert.Contains(t, undeclared, "u_three")
	assert.NotContains(t, undeclared, "u_one", "u_one was in the submitted list; it is not being removed")

	// And nothing was written: not the members, not the counter. A refusal that
	// still wrote would be worse than no refusal.
	status, body = s.req(t, http.MethodGet, "/v1/projects/"+s.project, "")
	require.Equal(t, http.StatusOK, status)
	assert.Equal(t, float64(1), body["members_version"],
		"the refused write advanced members_version, so it reached the UPDATE; a guard that fires after "+
			"the write is not a guard")
	members := fmt.Sprint(body["members"])
	assert.Contains(t, members, "u_two")
	assert.Contains(t, members, "u_three")
}

// ── half two: an INTENDED shrink succeeds ───────────────────────────────────
//
// Without this, a server that refused EVERY members write would pass the test
// above. That is not a fix, it is an outage: `members` is live access-control
// data and removing somebody is a routine, legitimate operation.
//
// It is also the assertion that proves the parameter survives the binder. The
// test above is green on the pre-aihub#333 tree if the check is implemented and
// `expected_removals` is dropped on this hop; this one is not.
func TestProjectMembersRemovalHTTPDeclaredShrinkSucceeds(t *testing.T) {
	s := newMembersVersionStack(t)

	status, body := s.patch(t, membersRemovalSeed)
	require.Equal(t, http.StatusOK, status, "seed write failed: %v", body)

	status, body = s.patch(t, `{"members":[{"user_id":"u_one","role":"viewer"}],`+
		`"members_version":1,"expected_removals":["u_two","u_three"]}`)
	require.Equal(t, http.StatusOK, status,
		"a shrink that declared exactly the two user_ids it removes was refused (%d). An intended removal "+
			"must still succeed, or aihub#333 has replaced silent data loss with an outage (body: %v)",
		status, body)
	assert.Equal(t, float64(2), body["members_version"],
		"a successful declared shrink must still advance the counter, or the next writer's token is stale")

	members := fmt.Sprint(body["members"])
	assert.Contains(t, members, "u_one")
	assert.NotContains(t, members, "u_two", "the declared removal did not take effect")
	assert.NotContains(t, members, "u_three")
}
