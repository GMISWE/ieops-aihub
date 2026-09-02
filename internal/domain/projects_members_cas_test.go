package domain

// aihub#260, the half that runs everywhere: the SQL buildProjectUpdate compiles.
//
// These are deliberately NOT DB-gated. The two ways this exact pattern has
// failed before are both properties of the compiled statement, not of any
// observable outcome against a database:
//
//   - "the counter never advanced" (aihub#241's first attempt) is the SET clause
//     saying `members_version = <something the caller sent> + 1` instead of
//     `members_version = members_version + 1`.
//
//   - "there was no compare-and-set at all" (aihub#241's second attempt) is a
//     WHERE clause with no members_version predicate while the parameter is
//     accepted and looks wired.
//
// The first of those is, today, not detectable from behaviour at all:
// UpdateProject holds SELECT ... FOR UPDATE across the write, so two racing
// writers are serialised and a version computed in Go would still come out
// 0 -> 1 -> 2 in any sequential test. What makes the in-database form
// load-bearing is that UpdateProject's own pre-transaction read
// (checkProjectAccess) happens BEFORE the lock — so a Go-computed next value
// derived from it is stale by construction. A statement-shape assertion is the
// only thing that fails the moment someone writes it that way, which is why
// these live here rather than in the DB-gated file next door.

import (
	"encoding/json"
	"strings"
	"testing"
)

// casTestMembers is a legal members list.
var casTestMembers = []MemberInput{
	{UserID: "u_alice", Role: "writer"},
	{UserID: "u_bob", Role: "viewer"},
}

func mustBuildProjectUpdate(t *testing.T, req *UpdateProjectRequest) projectUpdate {
	t.Helper()
	upd, aerr := buildProjectUpdate(req, "p_probe")
	if aerr != nil {
		t.Fatalf("buildProjectUpdate returned an error: %v", aerr)
	}
	return upd
}

// setClauseOf returns the text between "SET " and " WHERE ", i.e. only what the
// statement writes. Assertions about what must NOT be stored have to be scoped
// to this, or the WHERE predicate's own `members_version=$n` would satisfy them
// and turn a negative assertion into a false pass.
func setClauseOf(t *testing.T, query string) string {
	t.Helper()
	start := strings.Index(query, " SET ")
	end := strings.Index(query, " WHERE ")
	if start < 0 || end < 0 || end < start {
		t.Fatalf("could not split SET/WHERE out of %q", query)
	}
	return query[start+len(" SET ") : end]
}

// whereClauseOf returns the text between " WHERE " and " RETURNING ".
func whereClauseOf(t *testing.T, query string) string {
	t.Helper()
	start := strings.Index(query, " WHERE ")
	end := strings.Index(query, " RETURNING ")
	if start < 0 || end < 0 || end < start {
		t.Fatalf("could not split WHERE/RETURNING out of %q", query)
	}
	return query[start+len(" WHERE ") : end]
}

// ─── failure mode 1: the counter must advance in the database ────────────────

// A members write must increment members_version with Postgres reading the
// stored value. This is the assertion that fails if anyone recomputes it in Go.
func TestBuildProjectUpdate_MembersWriteIncrementsVersionInSQL(t *testing.T) {
	upd := mustBuildProjectUpdate(t, &UpdateProjectRequest{Members: &casTestMembers})

	set := setClauseOf(t, upd.Query)
	if !strings.Contains(set, "members_version = members_version + 1") {
		t.Errorf("the SET clause does not increment members_version in the database.\n"+
			"got SET: %s\n"+
			"A version computed in Go would be derived from UpdateProject's pre-transaction read, "+
			"which happens before the row lock, so two racing writers would compute the same next "+
			"value and the counter would stop being a usable CAS token (aihub#241 failure mode 1).", set)
	}
	// The increment must carry no placeholder: a `members_version=$n` in the SET
	// clause is precisely the Go-computed form this exists to forbid.
	if strings.Contains(set, "members_version=$") || strings.Contains(set, "members_version = $") {
		t.Errorf("the SET clause binds members_version to a parameter — it is being computed outside "+
			"the database.\ngot SET: %s", set)
	}
}

// The increment is keyed on `members` being written, NOT on "any update". This
// is the property that makes a dedicated counter better than a CAS on
// updated_at: an unrelated edit must not invalidate a members guard somebody is
// holding.
func TestBuildProjectUpdate_UnrelatedWriteDoesNotTouchMembersVersion(t *testing.T) {
	desc := "a new description, nothing to do with membership"
	upd := mustBuildProjectUpdate(t, &UpdateProjectRequest{Description: &desc})

	set := setClauseOf(t, upd.Query)
	if strings.Contains(set, "members_version") {
		t.Errorf("a description-only update advances members_version.\ngot SET: %s\n"+
			"Then any edit would 409 a members compare-and-set that is still perfectly valid, "+
			"and this counter would be no better than updated_at.", set)
	}
}

// ─── failure mode 2: the version must become a WHERE predicate ──────────────

func TestBuildProjectUpdate_SuppliedVersionAddsAWherePredicate(t *testing.T) {
	v := 7
	upd := mustBuildProjectUpdate(t, &UpdateProjectRequest{Members: &casTestMembers, MembersVersion: &v})

	if !upd.CAS {
		t.Error("CAS is false although members_version was supplied — UpdateProject keys its 409 off this flag, " +
			"so a conflict would surface as a 500 instead")
	}
	where := whereClauseOf(t, upd.Query)
	if !strings.Contains(where, "members_version=$") {
		t.Errorf("the WHERE clause carries no members_version predicate.\ngot WHERE: %s\n"+
			"Accepting the parameter without making it a precondition is aihub#241 failure mode 2: "+
			"a stale writer still wins, silently.", where)
	}
	if got := upd.Args[len(upd.Args)-1]; got != 7 {
		t.Errorf("the supplied version is not the last bound argument: got %#v, want 7", got)
	}
}

// The version is a precondition and nothing else — it must never be STORED.
// aihub#241's second attempt did exactly that: passing the version changed what
// got written and guarded nothing.
func TestBuildProjectUpdate_SuppliedVersionIsNeverStored(t *testing.T) {
	v := 7
	upd := mustBuildProjectUpdate(t, &UpdateProjectRequest{Members: &casTestMembers, MembersVersion: &v})

	set := setClauseOf(t, upd.Query)
	if strings.Contains(set, "members_version=$") || strings.Contains(set, "members_version = $") {
		t.Errorf("the caller's members_version is being written into the row.\ngot SET: %s", set)
	}
	if !strings.Contains(set, "members_version = members_version + 1") {
		t.Errorf("a CAS write must still advance the counter.\ngot SET: %s", set)
	}
}

// Omitting the version must keep the historical unconditional overwrite, which
// every caller that exists today depends on.
func TestBuildProjectUpdate_OmittedVersionAddsNoPredicate(t *testing.T) {
	upd := mustBuildProjectUpdate(t, &UpdateProjectRequest{Members: &casTestMembers})

	if upd.CAS {
		t.Error("CAS is true although no members_version was supplied")
	}
	where := whereClauseOf(t, upd.Query)
	if strings.Contains(where, "members_version") {
		t.Errorf("an update with no members_version still carries a precondition.\ngot WHERE: %s\n"+
			"That would break every existing caller, none of which sends one.", where)
	}
	if where != "name=$2" {
		t.Errorf("WHERE = %q, want exactly %q", where, "name=$2")
	}
}

// ─── statement shape ────────────────────────────────────────────────────────

// The new predicate takes the placeholder after `name`, so an off-by-one here
// would bind the version where the name belongs. Assert the whole statement for
// a request that exercises every field.
func TestBuildProjectUpdate_PlaceholdersAndArgsLineUp(t *testing.T) {
	desc := "d"
	visible := false
	scenario := "git@github.com:GMISWE/polyforge-coding.git"
	v := 3
	upd := mustBuildProjectUpdate(t, &UpdateProjectRequest{
		Description:    &desc,
		Visible:        &visible,
		Scenario:       &scenario,
		Repos:          json.RawMessage(`[{"name":"r","url":"u"}]`),
		Members:        &casTestMembers,
		MembersVersion: &v,
	})

	wantSet := "description=$1, visible=$2, scenario=$3, repos=$4, members=$5, members_version = members_version + 1"
	if got := setClauseOf(t, upd.Query); got != wantSet {
		t.Errorf("SET  = %q\nwant = %q", got, wantSet)
	}
	wantWhere := "name=$6 AND members_version=$7"
	if got := whereClauseOf(t, upd.Query); got != wantWhere {
		t.Errorf("WHERE = %q\nwant  = %q", got, wantWhere)
	}
	if len(upd.Args) != 7 {
		t.Fatalf("len(Args) = %d, want 7 (5 written columns + name + version); Args=%#v", len(upd.Args), upd.Args)
	}
	if upd.Args[5] != "p_probe" {
		t.Errorf("Args[5] = %#v, want the project name", upd.Args[5])
	}
	if upd.Args[6] != 3 {
		t.Errorf("Args[6] = %#v, want the supplied version 3", upd.Args[6])
	}
}

// The statement must return the updated row including the new counter, so a
// caller that just wrote members holds the next token without a second read.
func TestBuildProjectUpdate_ReturnsTheNewVersion(t *testing.T) {
	upd := mustBuildProjectUpdate(t, &UpdateProjectRequest{Members: &casTestMembers})
	if !strings.Contains(upd.Query, "members_version") ||
		!strings.Contains(upd.Query[strings.Index(upd.Query, " RETURNING "):], "members_version") {
		t.Errorf("the RETURNING list does not include members_version — the caller would have to re-read "+
			"to find the token for its next write.\ngot: %s", upd.Query)
	}
}

func TestBuildProjectUpdate_NoFieldsProducesNoStatement(t *testing.T) {
	upd := mustBuildProjectUpdate(t, &UpdateProjectRequest{})
	if !upd.Empty {
		t.Errorf("Empty = false for a request with no fields; Query=%q", upd.Query)
	}
	if upd.Query != "" {
		t.Errorf("Query = %q, want empty", upd.Query)
	}
}

// projectUpdateWritesSomething gates the "guard with nothing to guard" 400 and
// is a second spelling of buildProjectUpdate's Empty. Drift between them is
// silent in both directions, so pin them together over every field.
func TestProjectUpdateWritesSomethingAgreesWithBuild(t *testing.T) {
	desc := "d"
	visible := true
	scenario := ""
	cases := map[string]*UpdateProjectRequest{
		"nothing":      {},
		"description":  {Description: &desc},
		"visible":      {Visible: &visible},
		"scenario":     {Scenario: &scenario},
		"repos":        {Repos: json.RawMessage(`[{"name":"r","url":"u"}]`)},
		"repos null":   {Repos: json.RawMessage(`null`)},
		"repos empty":  {Repos: json.RawMessage(``)},
		"members":      {Members: &casTestMembers},
		"members none": {Members: &[]MemberInput{}},
	}
	for label, req := range cases {
		upd := mustBuildProjectUpdate(t, req)
		if got, want := projectUpdateWritesSomething(req), !upd.Empty; got != want {
			t.Errorf("%s: projectUpdateWritesSomething=%v but buildProjectUpdate.Empty=%v — "+
				"they disagree, so either a real write is rejected as \"changes nothing\" or a "+
				"members_version is accepted with no statement to guard", label, got, upd.Empty)
		}
	}
}

// An empty members list is still a members WRITE (it removes everyone), so it
// must advance the counter like any other.
func TestBuildProjectUpdate_EmptyMembersListStillCounts(t *testing.T) {
	empty := []MemberInput{}
	upd := mustBuildProjectUpdate(t, &UpdateProjectRequest{Members: &empty})
	if upd.Empty {
		t.Fatal("clearing the member list was treated as no update at all")
	}
	if !strings.Contains(setClauseOf(t, upd.Query), "members_version = members_version + 1") {
		t.Error("clearing the member list does not advance members_version, so a stale writer holding " +
			"the old version could still overwrite the (now empty) list unnoticed")
	}
}

// ─── the 400 for a guard with nothing to guard ──────────────────────────────

// Runs against a nil pool: the check is before any database access, so this is
// real behaviour rather than a source scan.
func TestUpdateProject_MembersVersionWithNothingToWriteIs400(t *testing.T) {
	v := 4
	p, err := UpdateProject(t.Context(), nil, "p_probe",
		&UserRecord{ID: "u_probe", Role: "admin"},
		UpdateProjectRequest{MembersVersion: &v})
	if err == nil {
		t.Fatalf("a members_version with nothing to write was accepted; project=%+v", p)
	}
	if err.HTTPStatus != 400 {
		t.Errorf("HTTPStatus = %d, want 400", err.HTTPStatus)
	}
	if !strings.Contains(err.Message, "changes nothing") {
		t.Errorf("the error should say the request writes nothing; got %q", err.Message)
	}
}

// The mirror image, and the guard against the check above being over-tight: a
// members_version alongside a real members write must NOT be rejected before
// the database is reached. Reaching the nil pool (a panic) is the outcome under
// test, exactly as TestCreateWorkItem_ValidTypeIsNotRejectedByValidation does it.
func TestUpdateProject_MembersVersionWithAWriteIsNotRejectedEarly(t *testing.T) {
	defer func() { _ = recover() }()
	v := 4
	if _, err := UpdateProject(t.Context(), nil, "p_probe",
		&UserRecord{ID: "u_probe", Role: "admin"},
		UpdateProjectRequest{Members: &casTestMembers, MembersVersion: &v}); err != nil && err.HTTPStatus == 400 {
		t.Errorf("a legal members + members_version request was rejected: %s", err.Message)
	}
}

// ─── the 409 payload ────────────────────────────────────────────────────────

// The conflict must name the current version so the caller can retry, and must
// be a 409 rather than a 400 — the caller's payload was well formed.
func TestMembersCASConflictErr_ReportsBothVersions(t *testing.T) {
	err := membersCASConflictErr(0, 3)
	if err.HTTPStatus != 409 {
		t.Errorf("HTTPStatus = %d, want 409", err.HTTPStatus)
	}
	if err.Code != ErrConflictCASFailed {
		t.Errorf("Code = %s, want %s", err.Code, ErrConflictCASFailed)
	}
	details, ok := err.Details.(map[string]any)
	if !ok {
		t.Fatalf("Details = %#v, want a map the caller can read the current version out of", err.Details)
	}
	if details["current_members_version"] != 3 {
		t.Errorf("details.current_members_version = %#v, want 3 — without it the caller cannot retry "+
			"without a second read", details["current_members_version"])
	}
	if details["expected_members_version"] != 0 {
		t.Errorf("details.expected_members_version = %#v, want 0", details["expected_members_version"])
	}
	// The numbers must not be transposed in the prose either: "is 3, not the
	// expected 0" and "is 0, not the expected 3" are both plausible sentences
	// and only one of them tells the caller what to send next.
	if !strings.Contains(err.Message, "members_version is 3, not the expected 0") {
		t.Errorf("message does not state current-then-expected: %q", err.Message)
	}
}

// UpdateProject must execute exactly the statement buildProjectUpdate compiled.
//
// A source-scan guard, and deliberately so: this is the one aihub#241 invariant
// with no behavioural test that can fail. Rewriting
// `members_version = members_version + 1` into a literal computed in Go from the
// row read under SELECT ... FOR UPDATE produces identical results in every test
// above — the row lock serialises the two writers, so the Go-computed value is
// current. It is still the form aihub#241 records as a failure, because its
// correctness rests entirely on that lock rather than on the arithmetic, and the
// lock is not what anybody reading the SET clause would check.
//
// Mutation-verified: this guard is the ONLY thing in the repo that goes red when
// UpdateProject patches upd.Query on its way to Exec.
//
// The negative assertion is paired with a positive one on purpose: bodyOf
// truncating early (or the function being renamed) would silently satisfy a lone
// "does not contain" check.
func TestUpdateProjectExecutesTheCompiledStatementUnmodified(t *testing.T) {
	body := bodyOf(t, sourceOf(t, "projects.go"), "UpdateProject")
	if !strings.Contains(body, "tx.QueryRow(ctx, upd.Query, upd.Args...)") {
		t.Fatal("UpdateProject no longer executes buildProjectUpdate's compiled query directly — " +
			"update this guard, and make sure whatever replaced it still runs the statement verbatim")
	}
	if strings.Contains(body, "upd.Query =") {
		t.Error("UpdateProject rewrites the statement buildProjectUpdate compiled. Every assertion about " +
			"the SET clause lives in projects_members_cas_test.go and is made against buildProjectUpdate's " +
			"output, so a rewrite here is invisible to all of them — including the one that requires " +
			"members_version to be incremented by Postgres rather than computed in Go (aihub#241 failure mode 1)")
	}
}
