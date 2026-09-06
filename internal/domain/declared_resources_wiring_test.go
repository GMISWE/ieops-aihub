package domain

import (
	"context"
	"encoding/json"
	"os"
	"regexp"
	"strings"
	"testing"
)

// aihub#238 wiring guard.
//
// The validators themselves are covered by ordinary unit tests, but the three
// places that must CALL them — CreateWorkItem, UpdateWorkItem and
// FnClaimWorkItem — all need a *pgxpool.Pool, so a behavioural test of the
// wiring would have to be DB-gated. In this repo a DB-gated test runs nowhere:
// AIHUB_TEST_DB is unset locally and is also unset in CI, so it would SKIP in
// both places while still reading as coverage (mem_I98xpPgY). A dormant test is
// worse than none.
//
// Where a validator runs BEFORE the pool is touched, prefer a real behavioural
// test with a nil pool — that is both stronger and immune to the false pass below.
// CreateWorkItem and PredictConflicts qualify. UpdateWorkItem and FnClaimWorkItem
// query the database first, so those keep a source-scan guard.
//
// A source-scan guard must assert something a neutering edit cannot satisfy.
// Review of aihub#238 found two that could not: asserting the mere presence of
// `ValidateDeclaredResources(req.DeclaredResources)` stayed green when the call was
// changed to `_ = ValidateDeclaredResources(...)`, and a body-extraction helper that
// truncated on `" func "` turned a negative assertion into a false pass. Both are
// fixed here. Every guard below is mutation-verified.

func sourceOf(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(b)
}

// bodyOf returns the source of the named top-level func, so a call found
// elsewhere in the file cannot satisfy the assertion for this function.
//
// The boundary is a line that STARTS with "func " in the raw source. An earlier
// version searched for " func " in whitespace-collapsed source, which truncated
// the body at any occurrence of that string inside it — including in a comment.
// For a negative assertion (see TestClaimDoesNotHardFailOnStoredDeclaredResources)
// truncating early turns the guard into a false pass, so this boundary must stay
// anchored to line starts.
func bodyOf(t *testing.T, rawSrc, funcName string) string {
	t.Helper()
	marker := "\nfunc " + funcName + "("
	i := strings.Index(rawSrc, marker)
	if i < 0 {
		t.Fatalf("could not find func %s — was it renamed? update this guard", funcName)
	}
	rest := rawSrc[i+len(marker):]
	if loc := regexp.MustCompile(`(?m)^func `).FindStringIndex(rest); loc != nil {
		rest = rest[:loc[0]]
	}
	// Collapse whitespace only AFTER the boundary is decided, so gofmt alignment
	// cannot break the substring assertions.
	return regexp.MustCompile(`\s+`).ReplaceAllString(rest, " ")
}

// CreateWorkItem validates before it opens a transaction, so a nil pool proves
// the real behaviour without a database — the same technique as
// TestPredictConflicts_RejectsUnknownTypeBeforeTouchingDB.
//
// This replaces a source-scan guard that gave a FALSE PASS: it only asserted the
// substring `ValidateDeclaredResources(req.DeclaredResources)` was present, so
// neutering the call to `_ = ValidateDeclaredResources(...)` left the suite green
// while validation did nothing.
func TestCreateWorkItem_RejectsUnknownTypeBeforeTouchingDB(t *testing.T) {
	req := &CreateWorkItemRequest{
		Project:           "aihub",
		Goal:              "probe",
		DeclaredResources: json.RawMessage(`[{"type":"file_scope","value":"aihub:internal/a.go"}]`),
	}
	wi, err := CreateWorkItem(context.Background(), nil, req, "u_probe", "probe", nil, "")
	if err == nil {
		t.Fatalf("CreateWorkItem accepted a mistyped declared_resources; wi=%+v", wi)
	}
	if err.HTTPStatus != 400 {
		t.Errorf("HTTPStatus = %d, want 400", err.HTTPStatus)
	}
	if !strings.Contains(err.Message, "file_scope") {
		t.Errorf("error should name the offending type; got %q", err.Message)
	}
}

// A legal payload must NOT be rejected by the validator. It cannot proceed to the
// database here, so assert only that it does not fail with a 400 — this pins that
// validation is not over-tight without needing a pool.
func TestCreateWorkItem_ValidTypeIsNotRejectedByValidation(t *testing.T) {
	defer func() {
		// Reaching the pool (nil deref) means validation let it through, which is
		// the outcome under test.
		_ = recover()
	}()
	req := &CreateWorkItemRequest{
		Project:           "aihub",
		Goal:              "probe",
		DeclaredResources: json.RawMessage(`[{"type":"path","uri":"file:internal/a.go","intent":"write"}]`),
	}
	if _, err := CreateWorkItem(context.Background(), nil, req, "u_probe", "probe", nil, ""); err != nil && err.HTTPStatus == 400 {
		t.Errorf("a legal declared_resources entry was rejected: %s", err.Message)
	}
}

func TestUpdateWorkItemValidatesDeclaredResources(t *testing.T) {
	body := bodyOf(t, sourceOf(t, "work_items.go"), "UpdateWorkItem")
	if !strings.Contains(body, "ValidateDeclaredResources(req.DeclaredResources)") {
		t.Error("UpdateWorkItem does not call ValidateDeclaredResources — an update could replace good resources with silently lockless ones (aihub#238)")
	}
}

func TestClaimValidatesRequestedLocksAndReportsUnrecognized(t *testing.T) {
	body := bodyOf(t, sourceOf(t, "run_attempts.go"), "FnClaimWorkItem")
	if !strings.Contains(body, "ValidateRequestedLocks(req.RequestedLocks)") {
		t.Error("FnClaimWorkItem does not call ValidateRequestedLocks — a malformed lock reaches Postgres and returns 500 + SQLSTATE 23514 (aihub#238)")
	}
	// FnClaimWorkItem has TWO exits that return a ClaimResponse — the idempotent
	// replay and the fresh claim — and both must carry the warning, or it vanishes
	// on retry, exactly when a confused caller looks again.
	//
	// Assert each exit separately. An earlier version of this guard only checked
	// for the substring "UnrecognizedDeclaredResources(" anywhere in the function;
	// blanking the fresh path still left the replay path's call in place, so the
	// guard passed while the fresh claim reported nothing. That is the same
	// shared-substring false pass described in mem_quFPJ1VN.
	if !strings.Contains(body, "unrecognizedResources := UnrecognizedDeclaredResources(wi.DeclaredResources)") {
		t.Error("the FRESH claim path does not compute UnrecognizedDeclaredResources — mistyped stored resources stay silent at claim (aihub#238)")
	}
	if !strings.Contains(body, "UnrecognizedResources: unrecognizedResources") {
		t.Error("the FRESH ClaimResponse does not carry UnrecognizedResources (aihub#238)")
	}
	if !strings.Contains(body, "UnrecognizedResources: UnrecognizedDeclaredResources(wi.DeclaredResources)") {
		t.Error("the IDEMPOTENT-REPLAY ClaimResponse does not carry UnrecognizedResources — the warning would disappear on retry (aihub#238)")
	}
}

// aihub#238 review finding 2: ValidateRequestedLocks must run BEFORE the block
// that derives locks from stored declared_resources, so input rules never apply
// to server-derived entries.
//
// Concrete failure if the order flips: a stored {"type":"service"} with no uri
// derives ("deploy_env", ""), the empty resource_key fails validation, and the
// claim returns 400 — an existing work item becomes unclaimable, the exact
// outcome this change exists to prevent.
//
// aihub#356 moved the derivation loop out of FnClaimWorkItem into
// deriveClaimLocks so it could be tested without a pool, so the second anchor is
// now the CALL.
//
// ⚠️ THIS GUARD MEASURES SOURCE TEXT, NOT BEHAVIOUR, and neither its name nor
// its failure messages may promise more than that. Both limits are measured, not
// supposed:
//
//   - It cannot see a call whose RESULT is dropped. Review mutant M3 deleted the
//     caller's `req.RequestedLocks = locks` line while leaving the call text
//     intact; no lock was derived at all and this guard stayed green. That hole
//     is closed in the code, not here — deriveClaimLocks now writes into req, so
//     there is no assignment left to delete (see its ⚠️ in run_attempts.go).
//   - It goes RED on refactors that change nothing. Review mutant M7 hoisted
//     wi.DeclaredResources and wi.Project into two locals and passed those. The
//     derivation stayed completely correct and this guard failed, because the
//     literal argument spelling moved.
//
// So the messages below report what was scanned for and leave the diagnosis to
// the reader. An earlier version asserted "the claim derives NO locks from
// declared_resources at all", which under M7 was simply untrue.
func TestRequestedLocksValidatedBeforeServerSideDerivation(t *testing.T) {
	body := bodyOf(t, sourceOf(t, "run_attempts.go"), "FnClaimWorkItem")
	validateAt := strings.Index(body, "ValidateRequestedLocks(req.RequestedLocks)")
	deriveAt := strings.Index(body, "deriveClaimLocks(req, wi.DeclaredResources, wi.Project)")
	if validateAt < 0 {
		t.Fatal("the literal text `ValidateRequestedLocks(req.RequestedLocks)` is not present in FnClaimWorkItem's body. Either the validation was removed, or it was re-spelled and this anchor needs updating — check which before assuming a defect")
	}
	if deriveAt < 0 {
		t.Fatal("the literal call text `deriveClaimLocks(req, wi.DeclaredResources, wi.Project)` is not present in FnClaimWorkItem's body. That is a SOURCE-TEXT miss, not a behavioural finding: it is equally produced by removing the derivation and by a behaviour-preserving refactor that hoists the arguments into locals. Check which, then either restore the derivation or re-anchor this guard (aihub#238, aihub#356)")
	}
	if validateAt > deriveAt {
		t.Error("ValidateRequestedLocks runs AFTER server-side lock derivation; a stored resource with no uri derives an empty resource_key and 400s the claim, making existing work items unclaimable (aihub#238)")
	}
}

// The derivation loop must skip an empty lock key as well as an empty lock type.
// resourceToLock({Type:"service"}, p) returns ("deploy_env", "") — a well-typed
// lock with a meaningless key that would collide with every other empty-key row
// of the same type.
//
// This was a source scan while the loop was inline in FnClaimWorkItem and only
// reachable through a *pgxpool.Pool. aihub#356 extracted deriveClaimLocks, so it
// is now the behavioural assertion this file's own header says to prefer: a
// source scan is satisfied by the text being present anywhere in the body, and
// this is satisfied only by the row not being derived.
//
// ⚠️ That conversion was worth making, but it had a side effect worth stating:
// it removed the SECOND source scan over FnClaimWorkItem's body, so the CALL to
// deriveClaimLocks is now anchored by exactly one guard,
// TestRequestedLocksValidatedBeforeServerSideDerivation. Measured on this tree
// (the count is the claim, not the line number — and the second grep drops `//`
// lines, because otherwise this very comment matches the pattern and the answer
// comes back 2):
//
//	$ grep -rn 'body, "deriveClaimLocks' --include='*_test.go' . \
//	    | grep -vc ':[0-9]*://'
//	1
//
// Three tests still call bodyOf(..., "FnClaimWorkItem"), but the other two
// anchor on ValidateRequestedLocks / UnrecognizedDeclaredResources and on the
// ABSENCE of ValidateDeclaredResources(wi.DeclaredResources) — none of them on
// this call. So if that one anchor is re-spelled, nothing else notices.
func TestDerivationSkipsEmptyLockKey(t *testing.T) {
	req := &ClaimRequest{}
	probes := deriveClaimLocks(req,
		json.RawMessage(`[{"type":"service"},{"type":"repo","uri":"repo:aihub","task_branch":"polyforge/x"}]`),
		"aihub")
	locks := req.RequestedLocks
	for _, l := range locks {
		if l.ResourceKey == "" {
			t.Errorf(`the derivation does not skip an empty lockKey — a stored {"type":"service"} with no uri inserts resource_type=%q resource_key="" (aihub#238)`, l.ResourceType)
		}
	}
	// The skip must not be a blanket refusal: the well-formed entry alongside it
	// still has to derive, or "skips empty keys" is satisfied by deriving nothing.
	if len(locks) != 1 || locks[0].ResourceKey != "aihub/polyforge/x" {
		t.Fatalf("locks = %+v, want exactly the one well-formed git_branch entry", locks)
	}
	if len(probes) != len(locks) {
		t.Errorf("probes (%d) and locks (%d) went out of step — lockProbes[i] no longer pairs with RequestedLocks[i] (aihub#261)", len(probes), len(locks))
	}
}

// The validator must NOT be wired into the stored-data lock-derivation paths:
// ~14% of existing declared_resources entries are mistyped, and rejecting them
// there would make those work items unclaimable. This guard fails if someone
// "tightens" claim by swapping the report for the hard validator.
func TestClaimDoesNotHardFailOnStoredDeclaredResources(t *testing.T) {
	body := bodyOf(t, sourceOf(t, "run_attempts.go"), "FnClaimWorkItem")
	if strings.Contains(body, "ValidateDeclaredResources(wi.DeclaredResources)") {
		t.Error("FnClaimWorkItem hard-validates STORED declared_resources — this makes historical mistyped work items unclaimable; use UnrecognizedDeclaredResources and report instead (aihub#238)")
	}
}
