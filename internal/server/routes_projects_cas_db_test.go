package server

// aihub#260, the HTTP hop: does `members_version` survive the journey from a
// real PATCH body into domain.UpdateProject, and does the 409 come back with
// enough for the caller to retry?
//
// This exists as its own file rather than being folded into the domain tests
// because the domain function being correct says nothing about whether the
// parameter reaches it. handleUpdateProject binds the body into
// domain.UpdateProjectRequest and names no field, so a parameter can be present
// in the MCP schema, present in the struct, and still be dropped here — and at
// the call site a dropped precondition is indistinguishable from one that
// passed. This repo has shipped exactly that failure before.
//
// Real router, real auth middleware, real echo binder, real Postgres.
//
//	AIHUB_TEST_DB=postgres://postgres:testpass@localhost:5440/aihub_test?sslmode=disable \
//	go test ./internal/server/ -run 'TestProjectMembersVersionHTTP' -v -count=1
//
// Requires migration 0032_projects_members_version.sql.

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/GMISWE/ieops-aihub/internal/auth"
	"github.com/GMISWE/ieops-aihub/internal/citest/testname"
)

const membersVersionTestKey = "pfk_members_version_http_test_key"

// membersVersionStack stands up the real router against AIHUB_TEST_DB with an
// admin user whose API key is membersVersionTestKey, and a project it owns.
type membersVersionStack struct {
	url     string
	project string
}

func newMembersVersionStack(t *testing.T) *membersVersionStack {
	t.Helper()
	dbURL := os.Getenv("AIHUB_TEST_DB")
	if dbURL == "" {
		t.Skip("set AIHUB_TEST_DB to run this integration test")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dbURL)
	require.NoError(t, err)
	t.Cleanup(pool.Close)

	uid := "u_" + testname.Sanitize(t.Name())
	project := "p_" + testname.Sanitize(t.Name())
	keys, err := json.Marshal([]map[string]any{{"id": "k_mv", "key_hash": auth.HashKey(membersVersionTestKey)}})
	require.NoError(t, err)
	_, err = pool.Exec(ctx, `
		INSERT INTO users(id,email,display_name,user_type,role,api_keys)
		VALUES($1,$1||'@test.local',$1,'human','admin',$2)
		ON CONFLICT (id) DO UPDATE SET api_keys=EXCLUDED.api_keys, role='admin'`, uid, keys)
	require.NoError(t, err)
	// Reset members and the counter so a previous run of the same test cannot
	// decide this one's expected numbers.
	_, err = pool.Exec(ctx,
		`INSERT INTO projects(name,owner_user_id) VALUES($1,$2)
		 ON CONFLICT (name) DO UPDATE SET members='[]'::jsonb, members_version=0, owner_user_id=EXCLUDED.owner_user_id`,
		project, uid)
	require.NoError(t, err)
	_, err = pool.Exec(ctx, `UPDATE projects SET members='[]'::jsonb, members_version=0 WHERE name=$1`, project)
	require.NoError(t, err)

	ts := httptest.NewServer(NewRouter(pool, []byte("members-version-test-cookie-secret")))
	t.Cleanup(ts.Close)
	return &membersVersionStack{url: ts.URL, project: project}
}

// req issues an authenticated request and returns the status and decoded body.
func (s *membersVersionStack) req(t *testing.T, method, path, body string) (int, map[string]any) {
	t.Helper()
	var rdr io.Reader
	if body != "" {
		rdr = strings.NewReader(body)
	}
	r, err := http.NewRequest(method, s.url+path, rdr)
	require.NoError(t, err)
	r.Header.Set("Authorization", "Bearer "+membersVersionTestKey)
	if body != "" {
		r.Header.Set("Content-Type", "application/json")
	}
	resp, err := http.DefaultClient.Do(r)
	require.NoError(t, err)
	defer resp.Body.Close() //nolint:errcheck
	raw, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	var decoded map[string]any
	require.NoError(t, json.Unmarshal(raw, &decoded), "body was %q", raw)
	return resp.StatusCode, decoded
}

func (s *membersVersionStack) patch(t *testing.T, body string) (int, map[string]any) {
	t.Helper()
	return s.req(t, http.MethodPatch, "/v1/projects/"+s.project, body)
}

// A stale members_version sent over real HTTP must reach the domain and come
// back as 409 CONFLICT_CAS_FAILED. A handler that dropped the field would
// answer 200 here with the members overwritten.
func TestProjectMembersVersionHTTPStaleVersionIs409(t *testing.T) {
	s := newMembersVersionStack(t)

	status, body := s.patch(t, `{"members":[{"user_id":"u_keep","role":"viewer"}]}`)
	require.Equal(t, http.StatusOK, status, "seed write failed: %v", body)
	require.Equal(t, float64(1), body["members_version"],
		"the PATCH response does not report the new members_version, so a caller would have to re-read to chain writes")

	status, body = s.patch(t, `{"members":[{"user_id":"u_clobber","role":"writer"}],"members_version":0}`)
	assert.Equal(t, http.StatusConflict, status,
		"a stale members_version over HTTP produced %d, not 409 — the precondition did not reach the domain "+
			"(body: %v)", status, body)
	assert.Equal(t, "CONFLICT_CAS_FAILED", body["code"])

	details, ok := body["details"].(map[string]any)
	require.True(t, ok, "the 409 envelope carries no details; the caller cannot retry without a second read (body: %v)", body)
	assert.Equal(t, float64(1), details["current_members_version"])
	assert.Equal(t, float64(0), details["expected_members_version"])

	// And nothing was written.
	status, body = s.req(t, http.MethodGet, "/v1/projects/"+s.project, "")
	require.Equal(t, http.StatusOK, status)
	assert.Equal(t, float64(1), body["members_version"])
	assert.Contains(t, fmt.Sprint(body["members"]), "u_keep")
	assert.NotContains(t, fmt.Sprint(body["members"]), "u_clobber")
}

// The mutation guard for the case above: the predicate must be able to MATCH.
// A WHERE clause that never matched would also make the 409 test pass.
func TestProjectMembersVersionHTTPCurrentVersionSucceeds(t *testing.T) {
	s := newMembersVersionStack(t)

	status, body := s.patch(t, `{"members":[{"user_id":"u_one","role":"viewer"}]}`)
	require.Equal(t, http.StatusOK, status, "%v", body)
	require.Equal(t, float64(1), body["members_version"])

	status, body = s.patch(t,
		`{"members":[{"user_id":"u_one","role":"viewer"},{"user_id":"u_two","role":"writer"}],"members_version":1}`)
	require.Equal(t, http.StatusOK, status, "a PATCH carrying the CURRENT members_version was rejected: %v", body)
	assert.Equal(t, float64(2), body["members_version"], "a successful guarded write must still advance the counter")
	assert.Contains(t, fmt.Sprint(body["members"]), "u_two")
}

// The version has to be READABLE before a write, or the guard is unusable. Both
// read endpoints must carry it: GET /v1/projects/:name and the list that
// pf_list_projects is built on.
func TestProjectMembersVersionHTTPIsReadableFromBothReadPaths(t *testing.T) {
	s := newMembersVersionStack(t)

	status, body := s.patch(t, `{"members":[{"user_id":"u_one","role":"viewer"}]}`)
	require.Equal(t, http.StatusOK, status, "%v", body)

	status, one := s.req(t, http.MethodGet, "/v1/projects/"+s.project, "")
	require.Equal(t, http.StatusOK, status)
	got, present := one["members_version"]
	require.True(t, present, "GET /v1/projects/:name does not return members_version; the caller has nothing to pass back")
	assert.Equal(t, float64(1), got)

	status, listed := s.req(t, http.MethodGet, "/v1/projects", "")
	require.Equal(t, http.StatusOK, status)
	items, ok := listed["items"].([]any)
	require.True(t, ok, "list response has no items array: %v", listed)
	var found map[string]any
	for _, it := range items {
		p, _ := it.(map[string]any)
		if p != nil && p["name"] == s.project {
			found = p
			break
		}
	}
	require.NotNil(t, found, "the seeded project is missing from GET /v1/projects")
	v, present := found["members_version"]
	require.True(t, present,
		"GET /v1/projects does not return members_version — pf_list_projects is where a caller reads the token, "+
			"so a guard nobody can find the input for is a guard nobody passes")
	assert.Equal(t, float64(1), v)
}

// members_version 0 must appear in the JSON rather than being dropped by an
// omitempty, or "no guard on this server" and "version is 0" become the same
// observation.
func TestProjectMembersVersionHTTPZeroIsSerialised(t *testing.T) {
	s := newMembersVersionStack(t)

	status, body := s.req(t, http.MethodGet, "/v1/projects/"+s.project, "")
	require.Equal(t, http.StatusOK, status)
	v, present := body["members_version"]
	require.True(t, present, "members_version is absent at version 0 — an omitempty would make a fresh project "+
		"indistinguishable from a server that does not implement the guard")
	assert.Equal(t, float64(0), v)
}

// A precondition with nothing to guard must be rejected rather than answered
// with a 200 the caller reads as "the guard passed".
func TestProjectMembersVersionHTTPGuardWithNothingToWriteIs400(t *testing.T) {
	s := newMembersVersionStack(t)

	status, body := s.patch(t, `{"members_version":0}`)
	assert.Equal(t, http.StatusBadRequest, status,
		"a members_version with no fields to write returned %d; a silent 200 here reads as a passed guard", status)
	assert.Equal(t, "BAD_REQUEST", body["code"])
	assert.Contains(t, fmt.Sprint(body["message"]), "changes nothing")
}
