package domain

// aihub#444, the behavioural half (aihub#411 decision table §6.2 T2-5): what the
// three event_type sets DO to a real database and a real caller.
//
// The pure half is event_types_test.go, which holds the sets to each other and
// to the migration text. That is not the same claim as the two BEHAVING the
// way the sets say — a containment can hold while the code consults the wrong
// map, and a Go mirror can equal the CHECK while the INSERT still 500s past it
// — so these run against Postgres.
//
//	AIHUB_TEST_DB=postgres://postgres:testpass@localhost:5432/aihub_test?sslmode=disable \
//	  go test ./internal/domain/ -run 'TestEmitEventAdminFlag|TestEmitEventNullWorkItem|TestMigration0036' -v -count=1

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/require"
)

// emitAs calls EmitEvent the way POST /v1/events does and returns the error as
// its code plus message, so a test can assert on the REFUSAL rather than on a
// string that happens to contain a word.
//
// AttemptID is left empty on purpose: EmitEvent verifies an attempt credential
// only when both work_item_id and attempt_id are present, so omitting it removes
// the credential path from the arms below. What is under test here is the admin
// gate and the work_item_id gate, and a failing credential would satisfy a naive
// "was it refused?" assertion for entirely the wrong reason.
func emitAs(t *testing.T, pool *pgxpool.Pool, callerUserID, wiID, eventType, role string, admin bool) (id, errMsg string, status int) {
	t.Helper()
	id, err := EmitEvent(context.Background(), pool, &EmitEventRequest{
		WorkItemID: wiID,
		EventType:  eventType,
		Payload:    json.RawMessage(`{"probe":"aihub444"}`),
		Admin:      admin,
	}, callerUserID, "probe", role)
	if err == nil {
		return id, "", 0
	}
	var aerr *AihubError
	require.ErrorAs(t, err, &aerr, "EmitEvent returned a bare error rather than an AihubError, so the "+
		"HTTP status a caller sees cannot be asserted at all")
	// The STATUS, not just the text: the whole point of the work_item_id arm is
	// which code the caller receives, and a 500 and a 400 can carry the same words.
	return "", string(aerr.Code) + ": " + aerr.Message, aerr.HTTPStatus
}

// TestEmitEventAdminFlag_DeclaringItIsNeverWorseThanOmittingIt is the inversion
// itself, as the four-cell table §6.2 T2-5 describes.
//
// Before aihub#444, admin_gc_manual was in adminOnlyEventTypes and NOT in
// adminEventWhitelist, so:
//
//	role   admin flag   before        after
//	admin  true         403 refused   accepted
//	admin  (omitted)    accepted      accepted
//	other  true         403 refused   403 refused
//	other  (omitted)    403 refused   403 refused
//
// The top-left cell is the whole defect and the assertion below is written to
// fail on it specifically. It is not enough to check that the call now succeeds:
// the property that must hold for EVERY admin-only type is that an admin who
// DECLARES the event is never refused where an admin who says nothing is
// accepted. A flag that punishes honesty teaches callers to stop setting it, and
// then the audit trail stops distinguishing deliberate admin actions from
// incidental ones — which is the only thing the flag is for.
//
// The two non-admin rows are the H6 forgery guard and must NOT move. They are
// asserted in the same function so that a change which "fixes" the inversion by
// dropping the admin-only check fails here rather than passing quietly.
func TestEmitEventAdminFlag_DeclaringItIsNeverWorseThanOmittingIt(t *testing.T) {
	pool := setupLatestTestDB(t)
	uid := testUser(t, pool)
	project := testProject(t, pool, uid)
	wi := seedWI(t, pool, project, uid)

	// Anti-vacuity: the whole table is about the admin-only set, so an empty one
	// would make every loop below pass without testing anything.
	require.NotEmpty(t, AdminOnlyEventTypes)

	for _, typ := range AdminOnlyEventTypes {
		t.Run(typ, func(t *testing.T) {
			idFlagged, errFlagged, _ := emitAs(t, pool, uid, wi.ID, typ, "admin", true)
			idBare, errBare, _ := emitAs(t, pool, uid, wi.ID, typ, "admin", false)

			require.Empty(t, errBare,
				"an admin emitting %q without the flag was refused (%s) — the H6 guard is meant to "+
					"check the ROLE, not to forbid the type", typ, errBare)
			require.NotEmpty(t, idBare)

			require.Empty(t, errFlagged,
				"THE INVERSION: an admin emitting %q with admin:true was refused (%s) while the same "+
					"admin omitting the flag succeeded. Declaring an admin event must never be "+
					"stricter than not declaring it; AdminEventWhitelist is derived from "+
					"AdminOnlyEventTypes (event_types.go) precisely so this cannot recur.",
				typ, errFlagged)
			require.NotEmpty(t, idFlagged)

			// H6 stays. Both non-admin cells refuse, and they refuse for the
			// admin-only reason rather than for the flag.
			_, errNonAdminFlagged, _ := emitAs(t, pool, uid, wi.ID, typ, "member", true)
			_, errNonAdminBare, _ := emitAs(t, pool, uid, wi.ID, typ, "member", false)
			require.NotEmpty(t, errNonAdminFlagged, "a non-admin emitted %q with admin:true", typ)
			require.NotEmpty(t, errNonAdminBare,
				"a non-admin emitted %q by simply omitting admin:true — that is the H6 forgery the "+
					"admin-only set exists to block", typ)
			require.Contains(t, errNonAdminBare, "requires admin role",
				"the bare non-admin refusal for %q no longer comes from the admin-only check", typ)
		})
	}
}

// TestEmitEventAdminFlag_TheWhitelistStillRefusesSomething is the anti-vacuity
// arm for the fix above.
//
// Widening AdminEventWhitelist is the obvious way to make the inversion go away,
// and widening it to EVERYTHING would make the previous test pass while deleting
// the §5.2 H10 gate. So one ordinary type is asserted to be refused under
// admin:true: the whitelist has to still be a whitelist.
func TestEmitEventAdminFlag_TheWhitelistStillRefusesSomething(t *testing.T) {
	pool := setupLatestTestDB(t)
	uid := testUser(t, pool)
	project := testProject(t, pool, uid)
	wi := seedWI(t, pool, project, uid)

	_, errFlagged, _ := emitAs(t, pool, uid, wi.ID, "note", "admin", true)
	require.NotEmpty(t, errFlagged,
		"an admin marked a plain `note` as admin:true and it was accepted — AdminEventWhitelist has "+
			"been widened until it gates nothing (design section 5.2 H10)")
	require.Contains(t, errFlagged, "not in the admin whitelist")

	// The refusal now names the whitelist, because a 403 that says only "not in
	// the whitelist" leaves the caller with no way to find out what is.
	for _, typ := range AdminEventWhitelist {
		require.Contains(t, errFlagged, typ,
			"the admin-whitelist 403 does not name %q, so the caller cannot see the set they missed", typ)
	}

	// Without the flag the same event is ordinary and lands.
	id, errBare, _ := emitAs(t, pool, uid, wi.ID, "note", "admin", false)
	require.Empty(t, errBare)
	require.NotEmpty(t, id)
}

// TestEmitEventNullWorkItem_IsA400NotA500 is aihub#411 §6.1 T1-4 applied to the
// one CHECK that had no Go mirror.
//
// Before this change the request went all the way to the INSERT, Postgres raised
// SQLSTATE 23514 on chk_evt_work_item_id, and EmitEvent wrapped it as
// ErrInternalError — a caller error reported as a server fault, which sends the
// reader to the server logs instead of to their own request. That is aihub#433's
// failure mode, and the policy in work_item_fields.go says the CHECK is the last
// line of defence and never the caller-facing one.
//
// Both directions are asserted from the same table, because "refuses everything"
// and "refuses nothing" both satisfy a one-sided version of this test.
func TestEmitEventNullWorkItem_IsA400NotA500(t *testing.T) {
	pool := setupLatestTestDB(t)
	uid := testUser(t, pool)

	// The accepted arms below write rows with NO work item, so testProject's
	// per-project reset cannot reach them and they would accumulate across runs.
	// That is not merely untidy: MEASURED here, a leftover admin_gc_manual row
	// with a NULL work_item_id makes `goose down` over migration 0036 fail at its
	// VALIDATE step, because the Down section narrows the constraint back to
	// 0026's 21 names. The migration is right to refuse; the test is wrong to
	// leave the row.
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(),
			`DELETE FROM agent_events WHERE work_item_id IS NULL AND actor_user_id = $1`, uid)
	})

	cases := []struct {
		eventType string
		allowed   bool
		why       string
	}{
		{"system_gc", true, "on the CHECK's list since 0025"},
		{"memory_gc", true, "on the list, and emitted by nothing — a caller is the only source"},
		{"admin_gc_manual", true, "added to the list by migration 0036 (aihub#444)"},
		{"note", false, "the type callers send most, and it always belongs to a work item"},
		{"step_started", false, "meaningless without the work item whose step it is"},
		{"definitely_not_an_event_type", false, "off every list"},
	}

	var allowedCount, refusedCount int
	for _, tc := range cases {
		if tc.allowed {
			allowedCount++
		} else {
			refusedCount++
		}
	}
	require.Greater(t, allowedCount, 2, "too few accepted types to detect an over-strict guard")
	require.Greater(t, refusedCount, 2, "too few refused types to detect a guard that refuses nothing")

	for _, tc := range cases {
		t.Run(tc.eventType, func(t *testing.T) {
			id, errMsg, status := emitAs(t, pool, uid, "", tc.eventType, "admin", false)

			if tc.allowed {
				require.Empty(t, errMsg,
					"%q may be filed with no work item (%s) but was refused: %s", tc.eventType, tc.why, errMsg)
				require.NotEmpty(t, id)
				return
			}

			require.NotEmpty(t, errMsg, "%q was accepted with no work item (%s)", tc.eventType, tc.why)
			require.Equal(t, 400, status,
				"%q with no work item was refused with HTTP %d (%s) — it must be a 400 naming "+
					"work_item_id, not the driver's constraint text arriving as a 500 (aihub#433)",
				tc.eventType, status, errMsg)
			require.True(t, strings.HasPrefix(errMsg, string(ErrBadRequest)+":"),
				"%q with no work item was refused as %q", tc.eventType, errMsg)
			require.Contains(t, errMsg, "work_item_id is required")
			require.Contains(t, errMsg, "chk_evt_work_item_id",
				"the 400 does not name the constraint it mirrors, so a reader cannot check the list")
			// aihub#543 lane L5: the card says EmitEvent "names the constraint AND
			// ITS LIST". The constraint name alone sends a reader to the
			// migrations; the list is what lets them fix the call from the error.
			// Built from the Go mirror rather than written out, so a reordering or
			// an added type moves both sides together.
			require.Contains(t, errMsg, strings.Join(NullWorkItemEventTypes, ", "),
				"the 400 names chk_evt_work_item_id but not the %d types it admits, so a caller "+
					"is told which rule they broke and not which values satisfy it",
				len(NullWorkItemEventTypes))
		})
	}

	// aihub#543 lane L5 — the complement, and the half no arm in this tree held:
	// the card's "`event_type` is still a free string … every other string is
	// accepted". Every case above carries NO work item, so all of them are
	// answered by the null-work-item guard; a Go vocabulary check added tomorrow
	// would be invisible to them because the guard refuses first.
	//
	// A subtest of this function rather than a new top-level one, per aihub#543
	// spec §3.3 rule 2: gated_tests.txt and ci.yml already name it, so this costs
	// no manifest edit.
	//
	// MUTANTS (applied to this tree against a migrated database; the verdict is
	// what ran):
	//
	//	M47 add a `slices.Contains(EventVocabulary, req.EventType)` refusal to
	//	    EmitEvent                            RED  this subtest, and the
	//	                                              pre-existing
	//	                                              definitely_not_an_event_type
	//	                                              case with it
	//	M48 install CHECK (event_type IN ('note')) NOT VALID on agent_events
	//	                                         RED  this subtest, plus three
	//	                                              pre-existing arms — a column
	//	                                              vocabulary breaks all of them,
	//	                                              which is the point
	t.Run("an off-vocabulary type WITH a work item is accepted", func(t *testing.T) {
		project := testProject(t, pool, uid)
		wi := seedWI(t, pool, project, uid)

		// Two values, because a guard keyed on either published list is red on
		// only one of them: artifact_action IS in EventVocabulary (retired, no
		// publisher left), and the other is in nothing at all.
		for _, typ := range []string{"artifact_action", "definitely_not_an_event_type"} {
			require.NotContains(t, setOf(NullWorkItemEventTypes), typ,
				"%q has joined the null-work-item list, so this arm no longer distinguishes "+
					"\"accepted because free\" from \"accepted because listed\"", typ)

			id, errMsg, status := emitAs(t, pool, uid, wi.ID, typ, "member", false)
			require.Empty(t, errMsg,
				"%q was refused with HTTP %d (%s). agent_events.event_type is TEXT NOT NULL with "+
					"no vocabulary CHECK (TestEventTypeCarriesNoVocabularyCheckAtHead) and the "+
					"published 45-name list is advisory, so a refusal here means the column or a "+
					"Go guard has closed the set and the card's hop 0-1 is now false",
				typ, status, errMsg)
			require.NotEmpty(t, id, "%q was accepted and no event id came back", typ)
		}
	})
}

// TestMigration0036_AdmitsAdminGcManualWithNoWorkItem is the column's own answer,
// taken independently of Go.
//
// The Go guard above refuses first, so every assertion there would pass over a
// database that still carries 0026's 21-name predicate — and then the first
// direct HTTP caller, or any future code path that inserts without going through
// EmitEvent, would hit the 23514 this work item exists to remove. The INSERT here
// bypasses EmitEvent entirely for exactly that reason.
func TestMigration0036_AdmitsAdminGcManualWithNoWorkItem(t *testing.T) {
	pool := setupLatestTestDB(t)
	ctx := context.Background()

	var validated bool
	var def string
	err := pool.QueryRow(ctx, `
		SELECT convalidated, pg_get_constraintdef(oid)
		  FROM pg_constraint
		 WHERE conrelid = 'agent_events'::regclass AND conname = 'chk_evt_work_item_id'`).
		Scan(&validated, &def)
	require.NoError(t, err, "chk_evt_work_item_id is not on this database — apply migration 0036 "+
		"before running this suite; without it the assertions below pass for the wrong reason")
	require.True(t, validated,
		"chk_evt_work_item_id is NOT VALID. 0036 widens a predicate 0026 had already validated, so "+
			"VALIDATE cannot fail on pre-existing rows — a NOT VALID constraint here means the "+
			"migration's VALIDATE step was dropped, not that the data is dirty")

	// Every name the Go mirror carries is in the constraint text. This is the
	// same equality event_types_test.go proves against the FILE; here it is
	// proved against the constraint the database actually installed, which is
	// what a stale or hand-patched database would disagree with.
	for _, typ := range NullWorkItemEventTypes {
		require.Contains(t, def, "'"+typ+"'",
			"the installed chk_evt_work_item_id does not carry %q, so Go is WIDER than the column and "+
				"a 400 has turned back into a 500", typ)
	}

	insert := func(id, eventType string) error {
		_, err := pool.Exec(ctx,
			`INSERT INTO agent_events (id, event_type, payload, created_at)
			 VALUES ($1, $2, '{}'::jsonb, clock_timestamp())`, id, eventType)
		return err
	}

	require.NoError(t, insert(NewID("evt"), "admin_gc_manual"),
		"the column still refuses admin_gc_manual with a NULL work_item_id — migration 0036 has not "+
			"been applied, and the Go fix alone would turn the 403 into a 500")

	err = insert(NewID("evt"), "definitely_not_an_event_type")
	require.Error(t, err,
		"the column accepted an off-list type with a NULL work_item_id — chk_evt_work_item_id has "+
			"been widened into uselessness, and the Go mirror is now the only thing checking")
	require.Contains(t, err.Error(), "chk_evt_work_item_id")

	// aihub#543 lane L5 — the OTHER side of the disjunction, which is what makes
	// the pf_emit_event card's "no CHECK behind it … every other string is
	// accepted" a statement about the column and not only about Go.
	// chk_evt_work_item_id reads `work_item_id IS NOT NULL OR event_type IN (…)`,
	// so the list above governs only the NULL case; with a work item present the
	// column takes any string. Asserted here because the arm above proves the
	// opposite for the NULL case, and the two together are the disjunction —
	// separately, either one reads as a vocabulary.
	//
	// A subtest of a function gated_tests.txt and ci.yml already name, per
	// aihub#543 spec §3.3 rule 2.
	t.Run("the column takes any type when a work item is present", func(t *testing.T) {
		uid := testUser(t, pool)
		project := testProject(t, pool, uid)
		wi := seedWI(t, pool, project, uid)

		_, insErr := pool.Exec(ctx,
			`INSERT INTO agent_events (id, work_item_id, event_type, payload, created_at)
			 VALUES ($1, $2, $3, '{}'::jsonb, clock_timestamp())`,
			NewID("evt"), wi.ID, "definitely_not_an_event_type")
		require.NoError(t, insErr,
			"the column refused an off-list event_type on a row that DOES carry a work item. "+
				"chk_evt_work_item_id is disjunctive on work_item_id IS NOT NULL, so this insert "+
				"is unconstrained by it; a failure here means some other constraint has closed "+
				"the vocabulary and the card's hop 0-1 must be rewritten")
	})

	// Leave the table as it was found: these rows carry no work item, so
	// seedWI's per-project cleanup would never reach them.
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(),
			`DELETE FROM agent_events WHERE work_item_id IS NULL AND event_type = 'admin_gc_manual'
			   AND payload = '{}'::jsonb`)
	})
}
