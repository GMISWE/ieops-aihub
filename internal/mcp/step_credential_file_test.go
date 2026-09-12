package mcp_test

// aihub#543 probe wave 1 — `docs/mcp-cards/pf_update_step.md`'s hop 4 closing
// claim, which is what makes this tool's own credential deletion exceptional:
//
//	"`pf_complete_attempt` and `pf_wrap` delete the state file on SUCCESS only,
//	 so nothing else in the batch changes what the client does with the file."
//	    -> TestTerminalToolsDeleteTheCredentialFileOnlyOnSuccess  (the SUCCESS
//	                                                               ONLY half)
//	    -> TestOnlyThreeCallSitesDeleteTheCredentialFile           (the "nothing
//	                                                               else" half)
//
// What was already held, and what was not. The delete-on-success itself is
// covered for both tools — state_resolve_wiring_test.go's
// TestSlugAddressedWrapCompletesUnderTheCanonicalID for pf_wrap, and
// stale_credential_delete_test.go for pf_update_step's own stale-credential
// exits. The word doing the work in the sentence above is ONLY, and nothing held
// it: no arm asserted what becomes of the credential when a terminal call FAILS.
// attempt_terminal_credential_dbgated_test.go has two refusal arms and both read
// the error text; neither looks at the state directory. So a handler that deleted
// the credential before reading the server's answer — or on a paused attempt,
// which is the one status whose whole point is that the file survives — was the
// same green. That is the direction with the unrecoverable cost: the credential is
// the only copy, and a wrongly-deleted one cannot be re-derived, only re-claimed.
//
// No database and no aihub: the delete is a client-side filesystem effect
// reached from an HTTP status a fake supplies directly.
//
// ⚠️ Workspace safety. Every arm goes through sandboxedWorkspace, which points
// POLYFORGE_WORKSPACE_ROOT at a temp dir and then verifies config.StateDir() is
// really inside it before anything is written — the same guard
// stale_credential_delete_test.go states its reasons for, and for the same
// reason: this file's subject is deleting files out of a state directory.
//
//	GOWORK=off go test ./internal/mcp/ -run 'TestTerminalToolsDelete|TestOnlyThreeCallSites' -count=1

import (
	"go/ast"
	"go/parser"
	"go/token"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/GMISWE/ieops-aihub/internal/config"
)

const (
	// credWIID is the canonical id the credential file is keyed under.
	credWIID = "wi_cred566"
	// credSecret is the credential whose survival or deletion every arm reads.
	credSecret = "s3cr3t-cred566"
)

// writeCredentialFile writes the post-claim state file and returns its path.
func writeCredentialFile(t *testing.T, worktrees map[string]string) string {
	t.Helper()
	if err := config.WriteStateFile(&config.StateFile{
		WIID:          credWIID,
		Project:       "aihub",
		AttemptID:     "ra_cred566",
		ClaimEpoch:    2,
		SessionSecret: credSecret,
		Claimed:       true,
		Worktrees:     worktrees,
	}); err != nil {
		t.Fatalf("write state file: %v", err)
	}
	path := filepath.Join(config.StateDir(), credWIID+".json")
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("precondition: the credential file is not on disk at %q: %v", path, err)
	}
	return path
}

// requireCredentialSurvives is the assertion the failure arms are about, with
// the reason stated where it fires rather than in each caller.
func requireCredentialSurvives(t *testing.T, path, when string) {
	t.Helper()
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("%s: the credential file %s is GONE. A session_secret is issued once by a claim and "+
			"stored nowhere else, so deleting it on anything but a confirmed terminal transition "+
			"strands the work item: the attempt is still running server-side, and the only recovery "+
			"left is a force takeover. (%v)", when, path, err)
	}
}

// TestTerminalToolsDeleteTheCredentialFileOnlyOnSuccess drives the two terminal
// tools through one successful and three unsuccessful transitions.
//
// MUTANTS (applied to this tree; the verdict is what ran):
//
//	M15 enforcement: in pf_complete_attempt, move the DeleteStateFile block
//	    ABOVE the s.client.CompleteAttempt call
//	                                        RED  the server-error arm
//	M16 enforcement: widen its status gate to delete on "paused" too
//	                                        RED  the paused arm
//	M17 enforcement: in pf_wrap, delete before the CompleteAttempt call
//	                                        RED  the pf_wrap arm
//	M18 publication: remove EVERY citation from the card sentence
//	                                        RED  K12 — the sentence lands back in
//	                                             the debt column
//	M18b publication: remove only THIS arm's citation, leaving the wrap's
//	    success-path citation in place     GREEN measured, and recorded because it
//	                                             is a limit worth knowing: K12 binds
//	                                             the SENTENCE, so a unit naming
//	                                             several arms is retired by any one
//	                                             of them. Nothing in the ledger
//	                                             isolates one arm of a
//	                                             conjunct-bearing bullet (aihub#543
//	                                             spec §1.3 says so in as many words)
//	M19 control, must stay GREEN: reword an adjacent card sentence the
//	    recogniser does not flag           GREEN measured — the arm is bound to
//	                                             behaviour, not to card churn
func TestTerminalToolsDeleteTheCredentialFileOnlyOnSuccess(t *testing.T) {
	t.Run("pf_complete_attempt deletes it on a wrapped 200", func(t *testing.T) {
		sandboxedWorkspace(t)
		path := writeCredentialFile(t, nil)

		f := newFakeAihub(t)
		f.on("/v1/work_items/"+credWIID+"/complete", func(map[string]any) (int, any) {
			return http.StatusOK, map[string]any{"status": "wrapped"}
		})
		result, isErr := callToolBounded(t, f, "pf_complete_attempt", map[string]any{
			"work_item_id": credWIID, "status": "wrapped", "derived": []any{},
		}, 20*time.Second)
		if isErr {
			t.Fatalf("pf_complete_attempt failed against a 200: %v", result)
		}
		if _, err := os.Stat(path); err == nil {
			t.Fatalf("CONTROL: the credential file survived a confirmed wrap. Without this arm the "+
				"three below are satisfied by a tool that never deletes anything, which is the opposite "+
				"defect — a live-looking credential for an attempt that has ended (%s)", path)
		} else if !os.IsNotExist(err) {
			t.Fatalf("stat %s: %v", path, err)
		}
	})

	t.Run("pf_complete_attempt keeps it when the server refuses", func(t *testing.T) {
		sandboxedWorkspace(t)
		path := writeCredentialFile(t, nil)

		f := newFakeAihub(t)
		f.on("/v1/work_items/"+credWIID+"/complete", func(map[string]any) (int, any) {
			return http.StatusInternalServerError, map[string]any{
				"code": "INTERNAL_ERROR", "message": "commit attempt"}
		})
		result, isErr := callToolBounded(t, f, "pf_complete_attempt", map[string]any{
			"work_item_id": credWIID, "status": "wrapped", "derived": []any{},
		}, 20*time.Second)
		if !isErr {
			t.Fatalf("a 500 from the server must reach the caller as an error, not a success: %v", result)
		}
		requireCredentialSurvives(t, path, "pf_complete_attempt against a 500")
		if sf, err := config.ReadStateFile(credWIID); err != nil {
			t.Fatalf("the file is present but unreadable: %v", err)
		} else if sf.SessionSecret != credSecret {
			t.Errorf("the credential was rewritten (session_secret %q, want %q) — surviving as a "+
				"different value is not surviving", sf.SessionSecret, credSecret)
		}
	})

	t.Run("pf_complete_attempt keeps it on a paused 200", func(t *testing.T) {
		sandboxedWorkspace(t)
		path := writeCredentialFile(t, nil)

		f := newFakeAihub(t)
		f.on("/v1/work_items/"+credWIID+"/complete", func(map[string]any) (int, any) {
			return http.StatusOK, map[string]any{"status": "paused"}
		})
		result, isErr := callToolBounded(t, f, "pf_complete_attempt", map[string]any{
			"work_item_id": credWIID, "status": "paused", "pause_reason": "aihub#543 probe",
		}, 20*time.Second)
		if isErr {
			t.Fatalf("pausing must succeed: %v", result)
		}
		requireCredentialSurvives(t, path,
			"pf_complete_attempt(status=paused) — a pause that took the credential with it could "+
				"never be resumed")
	})

	t.Run("pf_wrap keeps it when the completion refuses", func(t *testing.T) {
		root := sandboxedWorkspace(t)
		r := newResolveRepo(t, root)
		// The fake gh reports a PR that already covers HEAD, so coding.Wrap takes
		// its idempotent no-op branch and this arm never depends on a push
		// succeeding — the same setup TestSlugAddressedWrapCompletesUnderTheCanonicalID
		// uses, with the completion answering an error instead of a 200.
		fakeGHForResolve(t, `[{"url":"https://example.invalid/pr/4","number":4,"state":"OPEN","baseRefName":"main","commits":[{"oid":"`+r.head+`"}]}]`)
		path := writeCredentialFile(t, map[string]string{"aihub": r.wt})

		f := newFakeAihub(t)
		f.on("/v1/work_items/"+credWIID+"/complete", func(map[string]any) (int, any) {
			return http.StatusConflict, map[string]any{
				"code": "CONFLICT_EPOCH_MISMATCH", "message": "claim_epoch mismatch"}
		})
		result, isErr := callToolBounded(t, f, "pf_wrap", map[string]any{
			"work_item_id": credWIID, "repo": "aihub",
			"pr_title": "aihub#543 probe", "pr_body": "body",
			"derived": []any{},
		}, 60*time.Second)
		if !isErr {
			t.Fatalf("a wrap whose completion was refused must be reported as an error: %v", result)
		}
		requireCredentialSurvives(t, path, "pf_wrap whose complete_attempt answered 409")
		// FLOOR: the completion really was attempted. A wrap that failed earlier
		// (no worktree, no gh) would satisfy the survival assertion without ever
		// reaching the branch this arm is about.
		var completes int
		for _, p := range f.paths() {
			if strings.HasSuffix(p, "/complete") {
				completes++
			}
		}
		if completes != 1 {
			t.Fatalf("FLOOR: pf_wrap made %d completion call(s) (paths: %v) — this arm is about the "+
				"delete that follows a REFUSED completion, so a run that never got there proves nothing",
				completes, f.paths())
		}
	})
}

// TestOnlyThreeCallSitesDeleteTheCredentialFile is the "nothing else in the
// batch changes what the client does with the file" half.
//
// 🔴 A census over the package rather than a check on the three known files,
// because the claim is about everything ELSE — and an arm that read only the
// files it already knew about could not see the fourth site arrive, which is the
// only way the sentence becomes false. The population is every `.go` file in
// this package; the verdict is the exact set of enclosing functions.
//
// It parses the AST rather than counting a substring: a `config.DeleteStateFile`
// inside a comment or a string is not a call site, and the point of naming the
// enclosing function is that a call moved into a different tool is reported
// rather than absorbed by the count staying the same.
//
// MUTANTS (applied to this tree; the verdict is what ran):
//
//	M20 enforcement: add a fourth call site (in pf_pause_attempt's handler)
//	                                        RED  names the new function
//	M21 enforcement: delete one of the known sites
//	                                        RED  the missing-site half
//	M22 publication: = M18, because this arm's citation sits in the same SENTENCE
//	    as the one above and K12 classifies sentences
//	                                        RED  K12, with the caveat M18b records
func TestOnlyThreeCallSitesDeleteTheCredentialFile(t *testing.T) {
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatalf("read the package directory: %v", err)
	}

	// file -> the functions in it that call config.DeleteStateFile. Keyed by
	// function so a call migrating between tools is visible; several calls in one
	// function are one site, because the sentence is about which tools touch the
	// file.
	found := map[string]map[string]bool{}
	scanned := 0
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		scanned++
		fset := token.NewFileSet()
		file, perr := parser.ParseFile(fset, name, nil, parser.SkipObjectResolution)
		if perr != nil {
			t.Fatalf("parse %s: %v", name, perr)
		}
		for _, decl := range file.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Body == nil {
				continue
			}
			ast.Inspect(fn.Body, func(n ast.Node) bool {
				call, ok := n.(*ast.CallExpr)
				if !ok {
					return true
				}
				sel, ok := call.Fun.(*ast.SelectorExpr)
				if !ok || sel.Sel.Name != "DeleteStateFile" {
					return true
				}
				pkg, ok := sel.X.(*ast.Ident)
				if !ok || pkg.Name != "config" {
					return true
				}
				if found[name] == nil {
					found[name] = map[string]bool{}
				}
				found[name][fn.Name.Name] = true
				return true
			})
		}
	}
	if scanned < 10 {
		t.Fatalf("FLOOR: the walk read only %d non-test .go file(s) in this package — a census that "+
			"reads nothing finds nothing and agrees with any expectation", scanned)
	}

	// The expected sites, with what each one is. registerCodingTools holds
	// pf_wrap and registerLifecycleTools holds pf_complete_attempt; both are
	// closures inside those registrars, so the enclosing top-level function is
	// what the AST reports.
	want := map[string]string{
		"tools_coding.go/registerCodingTools":       "pf_wrap, after a confirmed completion",
		"tools_lifecycle.go/registerLifecycleTools": "pf_complete_attempt, on a terminal status only",
		"tools_step.go/deleteStaleCredential":       "pf_update_step's two stale-credential exits",
	}
	var got []string
	for file, fns := range found {
		for fn := range fns {
			got = append(got, file+"/"+fn)
		}
	}
	sort.Strings(got)

	for _, site := range got {
		if _, expected := want[site]; !expected {
			t.Errorf("K566 NEW_CREDENTIAL_DELETE_SITE: %s deletes the client's credential file, and "+
				"docs/mcp-cards/pf_update_step.md publishes that only pf_complete_attempt and pf_wrap "+
				"do (plus this tool's own stale-credential exits). Either the sentence is now false, or "+
				"this site belongs in the expected set with a reason. Sites found: %v", site, got)
		}
	}
	for site, what := range want {
		file, fn, _ := strings.Cut(site, "/")
		if !found[file][fn] {
			t.Errorf("K566 MISSING_CREDENTIAL_DELETE_SITE: %s no longer calls config.DeleteStateFile "+
				"(%s). If the cleanup moved, name the new site here; if it was removed, the card sentence "+
				"and the tools' own descriptions both need correcting. Sites found: %v", site, what, got)
		}
	}
}
