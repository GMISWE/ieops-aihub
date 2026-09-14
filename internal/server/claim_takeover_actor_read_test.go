package server

// aihub#679 — handleClaimWorkItem's cross-user force_takeover check is the ONLY
// copy of that rule, so the read it depends on may not fail silently.
//
// 🔴 WHY THIS FILE EXISTS AT ALL. The security review that produced aihub#679
// flagged `pool.QueryRow(… actor_user_id …).Scan(&x) //nolint:errcheck` in
// router.go and could not tell whether it mattered, because it assumed
// domain.FnForceTakeover re-checked. Two sites carry that line and they answer
// differently:
//
//	router.go handleForceTakeover  -> domain.FnForceTakeover re-reads actor_user_id
//	                                  with its error RETURNED as dbErr and re-applies
//	                                  `isSelf || maintainer/admin`. Second copy exists.
//	                                  The router's own failure mode is fail-CLOSED:
//	                                  a failed read leaves actorUserID "" and minRole
//	                                  stays the stricter "maintainer".
//	router.go handleClaimWorkItem  -> domain.FnClaimWorkItem takes NEITHER callerRole
//	                                  NOR callerProjectRoles in its signature, and its
//	                                  takeover branch reads, verbatim:
//	                                    } else if req.ForceOver {
//	                                        // Explicit force_takeover request — caller
//	                                        // must be maintainer/admin (handled upstream)
//	                                  No second copy. The router IS the check, and a
//	                                  failed read left currentActorUserID == "", which
//	                                  the guard's own `!= ""` arm reads as "nobody to
//	                                  take this from" — fail-OPEN.
//
// So the two halves of this file are the two halves of that finding: the
// behavioural arm pins that the rule is enforced at all (nothing in the repo
// covered it before), and the source arm pins that the read it rests on cannot
// be skipped.
//
// The behavioural arm is DB-gated like the rest of this package and is listed in
// internal/citest/dbtestcov/gated_tests.txt. The source arm needs no database
// and runs on every `go test ./...`, which is deliberate: the error handling is
// what a future edit would quietly drop, and a gate that only runs where a
// database is configured is not where you want that one.

import (
	"bytes"
	"context"
	"encoding/json"
	"go/ast"
	"go/parser"
	"go/printer"
	"go/token"
	"net/http"
	"net/http/httptest"
	"os"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/GMISWE/ieops-aihub/internal/auth"
	"github.com/GMISWE/ieops-aihub/internal/citest/testname"
	"github.com/GMISWE/ieops-aihub/internal/domain"
)

// ── the source arm ───────────────────────────────────────────────────────────

// funcBodySource returns the named top-level func's body with comments removed.
//
// Comments are dropped by parsing without ParseComments rather than by a regexp,
// because this file's own subject matter — `//nolint:errcheck`, the quoted
// "handled upstream" line — appears in the comments AROUND the code being
// asserted on. A matcher that saw those would report the defect as present on a
// fixed tree and as absent on a broken one, in whichever direction happened to
// be wrong.
func funcBodySource(t *testing.T, path, funcName string) string {
	t.Helper()
	src, err := os.ReadFile(path)
	require.NoError(t, err)
	fset := token.NewFileSet()
	// parser.SkipObjectResolution only; NOT parser.ParseComments — that is the
	// comment-stripping.
	file, err := parser.ParseFile(fset, path, src, parser.SkipObjectResolution)
	require.NoError(t, err)
	for _, decl := range file.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok || fn.Name == nil || fn.Name.Name != funcName || fn.Body == nil {
			continue
		}
		var buf bytes.Buffer
		require.NoError(t, printer.Fprint(&buf, fset, fn.Body))
		return regexp.MustCompile(`\s+`).ReplaceAllString(buf.String(), " ")
	}
	t.Fatalf("could not find func %s in %s — was it renamed? update this guard", funcName, path)
	return ""
}

// TestClaimPathForceTakeoverActorReadIsNotDiscarded is the aihub#679 source arm.
func TestClaimPathForceTakeoverActorReadIsNotDiscarded(t *testing.T) {
	const routerPath = "router.go"

	// ── the control, first ───────────────────────────────────────────────────
	//
	// Every assertion below is a substring test over printed source. gofmt
	// spacing, a rename, or a printer change turns all of them into "not found",
	// which for the negative ones is a FALSE PASS. So the matchers are first run
	// against source that really carries both shapes.
	t.Run("control_the_matchers_find_what_they_look_for", func(t *testing.T) {
		dir := t.TempDir()
		ctrlPath := dir + "/ctrl.go"
		require.NoError(t, os.WriteFile(ctrlPath, []byte(
			"package p\n"+
				"func handleClaimWorkItem() {\n"+
				"\t// a comment mentioning Scan(&currentActorUserID) that must not match\n"+
				"\tpool.QueryRow(ctx, q, id).Scan(&currentActorUserID)\n"+
				"\tif err != nil && !errors.Is(err, pgx.ErrNoRows) {\n\t\treturn e\n\t}\n"+
				"}\n"), 0o600))
		body := funcBodySource(t, ctrlPath, "handleClaimWorkItem")
		if !strings.Contains(body, ".Scan(&currentActorUserID)") {
			t.Fatal("the discarded-read matcher cannot find a discarded read in a control " +
				"that has one — the negative assertion below would pass on any tree")
		}
		if !strings.Contains(body, "errors.Is(err, pgx.ErrNoRows)") {
			t.Fatal("the error-handling matcher cannot find error handling in a control " +
				"that has it — the positive assertion below is unsatisfiable")
		}
		if strings.Contains(body, "a comment mentioning") {
			t.Fatal("funcBodySource did not strip comments, so every matcher below can be " +
				"satisfied by prose describing the defect instead of by the code")
		}
	})

	body := funcBodySource(t, routerPath, "handleClaimWorkItem")

	t.Run("the_read_is_not_a_bare_discarded_scan", func(t *testing.T) {
		// 🔴 THE DEFECT SHAPE. A Scan whose result goes nowhere: Go permits it
		// because QueryRow().Scan() is a call expression, errcheck flags it, and
		// the flag was suppressed. On this path that is not an unlogged error, it
		// is a permission check that does not run.
		const discarded = ").Scan(&currentActorUserID)"
		// The fixed form assigns first, so the bare statement form is what to
		// forbid. Printed source renders the assignment as
		// "err := pool.QueryRow(...).Scan(&currentActorUserID)".
		idx := strings.Index(body, discarded)
		if idx >= 0 {
			prefix := body[:idx]
			if !strings.Contains(prefix[max0(len(prefix)-220):], ":=") {
				t.Errorf("handleClaimWorkItem still discards the result of the actor_user_id " +
					"Scan. domain.FnClaimWorkItem does not re-check the maintainer/admin rule " +
					"— its signature has no callerRole or callerProjectRoles and its takeover " +
					"branch says \"handled upstream\" — so a failed read here leaves " +
					"currentActorUserID empty, the `!= \"\"` guard short-circuits, and an " +
					"ordinary writer takes over another user's running attempt.")
			}
		}
	})

	t.Run("the_error_is_inspected_and_ErrNoRows_is_the_only_tolerated_one", func(t *testing.T) {
		if !strings.Contains(body, "errors.Is(err, pgx.ErrNoRows)") {
			t.Error("handleClaimWorkItem no longer distinguishes pgx.ErrNoRows from a real " +
				"database failure on the actor_user_id read. Both directions are wrong: " +
				"treating every error as fatal breaks the dangling current_attempt_id case " +
				"(migration 0002 has no FK, and the domain deliberately claims fresh there), " +
				"while treating every error as \"no holder\" is the aihub#679 fail-open.")
		}
		if !strings.Contains(body, "failed to load the current attempt to authorize force_takeover") {
			t.Error("the refusal that a failed actor_user_id read must produce is gone. A " +
				"permission gate whose input could not be read has to refuse; there is no " +
				"second copy of this rule downstream to catch it.")
		}
	})

	t.Run("the_sibling_site_still_consults_its_read_only_on_success", func(t *testing.T) {
		// The handleForceTakeover site is fail-CLOSED and stays that way. Its
		// value can only RELAX minRole from maintainer to writer, so it must be
		// read only when the query succeeded. This is asserted rather than left to
		// its comment because the two sites look alike and the next person to
		// "make them consistent" would be editing the one that is already correct.
		ft := funcBodySource(t, routerPath, "handleForceTakeover")
		if !strings.Contains(ft, "err == nil && actorUserID == u.UserID") {
			t.Error("handleForceTakeover no longer guards its self-takeover relaxation on the " +
				"read having SUCCEEDED. The value it reads can only lower the required role, " +
				"so consulting it after a failed read is the one way this site can start " +
				"failing open.")
		}
	})
}

// ── the behavioural arm ──────────────────────────────────────────────────────

// TestClaimPathCrossUserForceTakeoverRequiresMaintainer pins the rule the source
// arm above protects: it had NO test anywhere in the repo before aihub#679.
//
// 🔴 A SOURCE PROBE ALONE WOULD BE PINNING A RULE NOBODY HAD MEASURED. The error
// path itself cannot be driven from here — reaching it needs the actor_user_id
// read to fail while domain.GetWorkItem, three statements earlier on the same
// pool, succeeded — so what this arm establishes is the other premise: that the
// check exists, fires, and refuses the caller the source arm is protecting it
// for. Without it, "the read is now error-checked" would be a fact about a
// branch with no demonstrated consequence.
func TestClaimPathCrossUserForceTakeoverRequiresMaintainer(t *testing.T) {
	pool := serverTestPool(t)
	ctx := context.Background()
	base := testname.Sanitize(t.Name())

	project := "p_" + base
	ownerUID := "u_" + base + "o"  // holds the running attempt
	writerUID := "u_" + base + "w" // plain writer on the project — must be refused
	maintUID := "u_" + base + "m"  // maintainer — must be allowed
	writerKey := "pfk_" + writerUID
	maintKey := "pfk_" + maintUID

	seedUser := func(uid, role, key, keyID string) {
		keys, err := json.Marshal([]map[string]any{{"id": keyID, "key_hash": auth.HashKey(key)}})
		require.NoError(t, err)
		_, err = pool.Exec(ctx, `
			INSERT INTO users(id,email,display_name,user_type,role,api_keys)
			VALUES($1,$1||'@test.local',$1,'human',$2,$3)
			ON CONFLICT (id) DO UPDATE SET api_keys=EXCLUDED.api_keys, role=EXCLUDED.role`,
			uid, role, keys)
		require.NoError(t, err)
	}
	seedUser(ownerUID, "writer", "pfk_"+ownerUID, "k679own")
	seedUser(writerUID, "writer", writerKey, "k679wri")
	seedUser(maintUID, "writer", maintKey, "k679mnt")

	members, err := json.Marshal([]map[string]any{
		{"user_id": ownerUID, "role": "writer"},
		{"user_id": writerUID, "role": "writer"},
		{"user_id": maintUID, "role": "maintainer"},
	})
	require.NoError(t, err)
	_, err = pool.Exec(ctx,
		`INSERT INTO projects(name,owner_user_id,members) VALUES($1,$2,$3)
		 ON CONFLICT (name) DO UPDATE SET members=EXCLUDED.members, owner_user_id=EXCLUDED.owner_user_id`,
		project, ownerUID, members)
	require.NoError(t, err)
	resetProjectWorkItems(t, pool, project)

	wiType := "fix_bug"
	seedClaimed := func(idem, machine, secret string) string {
		wi, aerr := domain.CreateWorkItem(ctx, pool, &domain.CreateWorkItemRequest{
			Project: project, Goal: "rotate the shared signing key before the audit window",
			Scenario: "coding", WIType: &wiType,
			Source: "human", ForceCreate: true, ForceReason: "aihub#679 fixture",
		}, ownerUID, "owner-agent", nil, "")
		require.Nil(t, aerr, "seed wi: %+v", aerr)
		// ⚠️ RETRIED ON 40001, and this is not defensive padding — it was measured.
		// FnClaimWorkItem opens a SERIALIZABLE transaction, so seeding a claim
		// races every other test in this package that claims anything, and the
		// whole package shares one database. Run alone this fixture is green; run
		// under `go test ./...` it failed with CONFLICT_SERIALIZATION_FAILURE
		// ("could not serialize access due to read/write dependencies") out of
		// emit attempt_started.
		//
		// Retrying is the server's own published contract for that code
		// (details.retryable=true, and the message says "retry the request"), not
		// a way to make a flaky assertion pass: the code is returned INSTEAD of
		// doing the work, with the transaction rolled back, so there is nothing
		// half-done to retry over. The SAME idempotency_key is deliberately
		// reused — a claim that rolled back left no run_attempts row, and if one
		// did survive the key returns that attempt rather than making a second.
		//
		// It is bounded and the exhaustion message says what it means, because a
		// loop that retried forever would turn a real deadlock into a test that
		// hangs the suite.
		var aerr2 *domain.AihubError
		for attempt := range 5 {
			_, aerr2 = domain.FnClaimWorkItem(ctx, pool, wi.ID, &domain.ClaimRequest{
				IdempotencyKey: idem,
				SessionInfo:    domain.SessionInfo{MachineID: machine, SessionSecret: secret},
			}, ownerUID, "", "owner-agent")
			if aerr2 == nil || aerr2.Code != domain.ErrConflictSerializationFailure {
				break
			}
			time.Sleep(time.Duration(attempt+1) * 50 * time.Millisecond)
		}
		require.Nil(t, aerr2, "claim wi (after retrying serialization failures): %+v", aerr2)
		return wi.ID
	}

	ts := httptest.NewServer(NewRouter(pool, []byte("claim-takeover-test-cookie-secret")))
	t.Cleanup(ts.Close)

	claimAs := func(t *testing.T, key, wiID, idem, machine, secret string, force bool) (int, string) {
		t.Helper()
		b, err := json.Marshal(map[string]any{
			"idempotency_key": idem,
			"force_takeover":  force,
			"session_info":    map[string]any{"machine_id": machine, "session_secret": secret},
		})
		require.NoError(t, err)
		r, err := http.NewRequest(http.MethodPost,
			ts.URL+"/v1/work_items/"+wiID+"/claim", bytes.NewReader(b))
		require.NoError(t, err)
		r.Header.Set("Authorization", "Bearer "+key)
		r.Header.Set("Content-Type", "application/json")
		resp, err := http.DefaultClient.Do(r)
		require.NoError(t, err)
		defer resp.Body.Close() //nolint:errcheck
		var buf bytes.Buffer
		_, err = buf.ReadFrom(resp.Body)
		require.NoError(t, err)
		return resp.StatusCode, buf.String()
	}

	const secretA = "s3cr3t-0123456789abcdef0123456789abcdef0123456789abcdef01234570"
	const secretB = "s3cr3t-0123456789abcdef0123456789abcdef0123456789abcdef01234571"

	t.Run("a_plain_writer_is_refused", func(t *testing.T) {
		wiID := seedClaimed("idem-679-a", "m679a", secretA)
		status, body := claimAs(t, writerKey, wiID, "idem-679-a-taker", "m679t", secretB, true)
		if status != http.StatusForbidden {
			t.Fatalf("a plain writer took over another user's running attempt through the "+
				"claim path: %d %s\nThis is the rule domain.FnClaimWorkItem defers to the "+
				"router for (\"handled upstream\"), so if the router does not enforce it, "+
				"nothing does.", status, body)
		}
		if !strings.Contains(body, "maintainer or admin") {
			t.Errorf("the refusal does not say what is required: %s", body)
		}
	})

	t.Run("control_a_maintainer_is_allowed", func(t *testing.T) {
		// 🔴 WITHOUT THIS THE ARM ABOVE IS SATISFIED BY REFUSING EVERYONE, which
		// would break every legitimate recovery from a dead agent — the operation
		// force_takeover exists for.
		wiID := seedClaimed("idem-679-b", "m679b", secretA)
		status, body := claimAs(t, maintKey, wiID, "idem-679-b-taker", "m679t2", secretB, true)
		if status != http.StatusOK {
			t.Fatalf("a maintainer was refused a cross-user force_takeover: %d %s", status, body)
		}
	})

	t.Run("control_the_same_writer_may_retake_its_own_attempt", func(t *testing.T) {
		// The self-takeover path needs only writer, and it runs through the SAME
		// guard — `currentActorUserID != u.UserID` is what separates them. A fix
		// that refused on any non-empty actor would pass the deny arm and break
		// every agent resuming its own work from a second machine.
		wiID := seedClaimed("idem-679-c", "m679c", secretA)
		status, body := claimAs(t, "pfk_"+ownerUID, wiID, "idem-679-c-retake", "m679c2", secretB, true)
		if status != http.StatusOK {
			t.Fatalf("the ORIGINAL holder was refused a self-takeover of its own attempt: "+
				"%d %s", status, body)
		}
	})
}

// max0 clamps a negative index to zero for the bounded look-back above.
func max0(i int) int {
	if i < 0 {
		return 0
	}
	return i
}
