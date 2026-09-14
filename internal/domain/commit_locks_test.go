package domain

// aihub#366 — the parts of the commit gate that need no database.
//
// The coverage predicate is the half that decides whether a changed file gets a
// lock at all, and it is decided ENTIRELY by key-form matching. Every way it can
// be wrong is a way the gate quietly stops protecting something:
//
//	too strict -> the gate re-acquires a lock the attempt already holds under an
//	              older key form, or worse, reports a conflict against itself
//	too loose  -> a file is called "already covered" on the strength of a lock
//	              that names a different repo, and is committed unprotected
//
// The second is the dangerous direction and it is not hypothetical: it is
// exactly what an unqualified probe does, which is why the endpoint refuses a
// request with no repo. The subtest that demonstrates it is below, so the
// requirement is a measured fact rather than a comment.
//
// The acquire / conflict / orphan-reclaim half needs Postgres and is not here;
// see the work item for why its CI wiring is blocked.

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"strings"
	"testing"

	"github.com/GMISWE/ieops-aihub/pkg/client"
)

// exprText (embed_writer_parity_test.go) renders an expression back to source.

// coveredBy is the production coverage test, as FnReconcileCommitLocks runs it.
func coveredBy(project, repo, path string, held []string) bool {
	item := DeclaredResourceItem{Type: "path", URI: "file:" + path, Repo: repo, Intent: "write"}
	_, _, probe := derivedLockProbe(item, project)
	return anyMatches(probe, held)
}

func TestCommitGateCoverage_KeyForms(t *testing.T) {
	const project = "aihub"
	const path = "internal/domain/work_items.go"

	cases := []struct {
		name string
		repo string
		held []string
		want bool
		why  string
	}{
		{
			name: "repo-qualified key covers the same path in that repo",
			repo: "aihub",
			held: []string{project + ":aihub:" + path},
			want: true,
			why:  "the ordinary case: the lock this very gate would have taken",
		},
		{
			name: "legacy unqualified key still covers it",
			repo: "aihub",
			held: []string{project + ":" + path},
			want: true,
			why: "every lock written before aihub#261 has this shape, and a gate that " +
				"demanded the qualified form would take a SECOND row for a file the " +
				"attempt already holds — the 'same path, twice, under two names' hazard",
		},
		{
			// 🔴 The dangerous direction. A lock naming another repo protects
			// another repo's file; treating it as coverage commits this one with
			// nothing behind it.
			name: "another repo's key does NOT cover it",
			repo: "aihub",
			held: []string{project + ":some-other-repo:" + path},
			want: false,
			why:  "repo-relative paths collide across repos; that is the whole reason for the repo segment",
		},
		{
			name: "a different file in the same repo does not cover it",
			repo: "aihub",
			held: []string{project + ":aihub:internal/domain/memory.go"},
			want: false,
		},
		{
			name: "a parent directory's key does not cover it",
			repo: "aihub",
			held: []string{project + ":aihub:internal/domain"},
			want: false,
			why: "prefix overlap is lockConflictProbe.Overlaps (PredictConflicts' advisory rule 3), " +
				"not Matches; coverage must use the hard rule or a directory-shaped declaration " +
				"would silently absorb every file under it",
		},
		{
			name: "another project's key does not cover it",
			repo: "aihub",
			held: []string{"some-other-project:aihub:" + path},
			want: false,
		},
		{
			name: "holding nothing covers nothing",
			repo: "aihub",
			held: nil,
			want: false,
			why:  "the aihub#357 shape: declared_resources was an empty array, so claim took zero locks",
		},
		{
			// This subtest is the JUSTIFICATION for rejecting an empty repo, and
			// it asserts the WRONG answer on purpose: with no repo the probe
			// carries a "<project>:%:<path>" LIKE arm, so another repo's lock
			// reads as coverage. Against a competitor that arm is conservative;
			// against the attempt's own locks it inverts into a hole. The 400 in
			// FnReconcileCommitLocks is what keeps this path unreachable, and
			// TestCommitGate_RepoIsRequired below pins it.
			name: "with NO repo, another repo's key wrongly reads as coverage",
			repo: "",
			held: []string{project + ":some-other-repo:" + path},
			want: true,
			why:  "measured, not desired — it is why repo is a required field",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := coveredBy(project, tc.repo, path, tc.held)
			if got != tc.want {
				t.Fatalf("covered=%v want=%v for repo=%q held=%v.%s",
					got, tc.want, tc.repo, tc.held, prefixWhy(tc.why))
			}
		})
	}
}

func prefixWhy(why string) string {
	if why == "" {
		return ""
	}
	return " " + why
}

// TestCommitGate_RepoIsRequired pins the validation, and pins that it happens
// BEFORE any database work: the call is made with a nil pool, so a version that
// opened a transaction first would panic instead of returning the 400.
func TestCommitGate_RepoIsRequired(t *testing.T) {
	resp, aerr := FnReconcileCommitLocks(context.Background(), nil, "wi_whatever",
		&ReconcileCommitLocksRequest{
			AttemptID: "att_x", ClaimEpoch: 1, SessionSecret: "s",
			Paths: []string{"internal/domain/work_items.go"},
		})
	if aerr == nil {
		t.Fatal("an empty repo was accepted; coverage would then be computed with a probe " +
			"that matches every repo's copy of the path")
	}
	if aerr.Code != ErrBadRequest {
		t.Errorf("code = %v, want %v", aerr.Code, ErrBadRequest)
	}
	if resp != nil {
		t.Errorf("resp = %+v, want nil", resp)
	}
}

// TestCommitLockConflictErr_NamesEveryHolder covers the refusal payload.
//
// The owner's decision was "block, and report WHO holds it and WHICH files".
// A 409 that says only "locked" sends the author to pf_read_events to find out
// whose it is — which is the round-trip the decision exists to avoid — so the
// holder identity and the full blocked-path list are asserted as contract.
func TestCommitLockConflictErr_NamesEveryHolder(t *testing.T) {
	aerr := commitLockConflictErr([]commitLockConflict{
		{Path: "a.go", ResourceKey: "p:r:a.go", AttemptID: "att_1", ActorDisplay: "someone", WorkItemSlug: "aihub#1"},
		{Path: "b.go", ResourceKey: "p:r:b.go", AttemptID: "att_2", ActorDisplay: "other", WorkItemSlug: "aihub#2"},
	}, false)

	if aerr.Code != ErrConflictLockTaken {
		t.Fatalf("code = %v, want %v — pf_commit keys its 'refused' wording on this code", aerr.Code, ErrConflictLockTaken)
	}
	for _, want := range []string{"a.go", "b.go", "someone", "aihub#1"} {
		if !strings.Contains(aerr.Message, want) {
			t.Errorf("message %q does not mention %q", aerr.Message, want)
		}
	}

	// The details ride to the caller through the client's formatDetails, so they
	// have to survive a JSON round-trip.
	raw, err := json.Marshal(aerr.Details)
	if err != nil {
		t.Fatalf("marshal details: %v", err)
	}
	var got struct {
		Conflicts []struct {
			Path         string `json:"path"`
			AttemptID    string `json:"attempt_id"`
			ActorDisplay string `json:"actor_display"`
			WorkItemSlug string `json:"work_item_slug"`
		} `json:"conflicts"`
		ConflictWith map[string]any `json:"conflict_with"`
		BlockedPaths []string       `json:"blocked_paths"`
	}
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("unmarshal details: %v", err)
	}

	// EVERY conflict, not just the first. Reporting one at a time would send the
	// author round the loop once per file.
	if len(got.Conflicts) != 2 {
		t.Fatalf("conflicts = %d, want 2; a refusal that names one file at a time costs "+
			"the author one re-run per blocked file", len(got.Conflicts))
	}
	if got.Conflicts[1].ActorDisplay != "other" || got.Conflicts[1].WorkItemSlug != "aihub#2" {
		t.Errorf("second conflict lost its holder: %+v", got.Conflicts[1])
	}
	// conflict_with is the shape claim and acquire_locks already publish; a
	// caller keyed on it must not start reading nothing.
	if got.ConflictWith["attempt_id"] != "att_1" {
		t.Errorf("conflict_with = %v, want the first holder in the established shape", got.ConflictWith)
	}
	if len(got.BlockedPaths) != 2 {
		t.Errorf("blocked_paths = %v, want both paths", got.BlockedPaths)
	}
}

// TestCommitLockConflictErr_ShipsTheRemedy checks the two things about the
// advice that CAN be checked from here, and deliberately does not pretend to
// check the third.
//
// 🔴 WHAT THIS TEST DOES NOT DO IS THE POINT. Its predecessor asserted that the
// advice CONTAINED "git restore --staged" and did NOT contain the broken
// remedy, and called that "pins the remedy". It does not. Two rewrites were
// injected against it and both passed:
//
//	"You could unstage them with `git restore --staged <paths>`, but the quick
//	 way is to narrow the retry: paths=[the ones that are yours]."   → green
//	"Do NOT run `git restore --staged <paths>` — it loses your work. Narrow the
//	 retry instead with paths=[yours]."                              → green
//
// The first recommends a remedy that does not work while naming the one that
// does; the second forbids the working one outright. A substring assertion
// cannot tell any of the three apart, because what distinguishes them is which
// sequence a reader ends up performing — a behaviour, not a spelling. So the
// question "does the remedy work?" is answered by executing it, in
// TestCommitGateWire_RefusalAdviceIsExecutableAndWorks, and what is left here is
// only what that test cannot see: that the envelope actually carries the
// string, and that a prohibition (which has no executable form) survives.
func TestCommitLockConflictErr_ShipsTheRemedy(t *testing.T) {
	aerr := commitLockConflictErr([]commitLockConflict{
		{Path: "a.go", ResourceKey: "p:r:a.go", AttemptID: "att_1", ActorDisplay: "someone", WorkItemSlug: "aihub#1"},
	}, false)
	details, ok := aerr.Details.(map[string]any)
	if !ok {
		t.Fatalf("details is %T, want map[string]any", aerr.Details)
	}
	advice, _ := details["advice"].(string)
	if advice != CommitLockRefusalAdvice {
		t.Errorf("details.advice = %q, want CommitLockRefusalAdvice. The executable test in "+
			"internal/mcp runs THAT constant, so a refusal shipping anything else is shipping "+
			"a remedy nothing has executed", advice)
	}

	// A prohibition has no executable form — there is no sequence to run that
	// demonstrates "do not do this" — so it is asserted textually here, which is
	// the one place a substring assertion is the right instrument. A takeover
	// evicts an attempt that is actively editing the file.
	if !strings.Contains(advice, "takeover") {
		t.Errorf("advice %q dropped the force-takeover warning", advice)
	}
}

// TestCommitLockConflictErr_MergeShipsADifferentRemedy is aihub#662's half of
// the same question: a refusal has TWO remedies and the wrong one destroys data.
//
// 🔴 WHAT IS AND IS NOT ASSERTED HERE, because the file's own history is a
// catalogue of substring assertions that checked nothing. The question "is the
// merge remedy safe?" is answered by EXECUTION, in
// TestCommitGateWire_MergeRefusalIsNotTheDestructiveRemedy, which builds a real
// refused merge, measures that the ORDINARY recipe deletes the other parent's
// file, and requires the shipped merge advice not to be that recipe. What is
// left for here is the part that test cannot see: that the selector actually
// selects, and that the merge string carries no numbered recipe for anything to
// run.
//
// The recipe assertion is structural rather than textual and it is the one that
// bites: adviceRecipe (internal/mcp) turns "(1) … (2) …" into commands a test
// EXECUTES verbatim against the worktree. A merge advice that grew a numbered
// step would be handing a reader a local sequence to perform, and there is no
// safe one — so "numbers no steps" is a contract, not a style, and it is
// checked by the same parser rather than by looking for words.
func TestCommitLockConflictErr_MergeShipsADifferentRemedy(t *testing.T) {
	conflicts := []commitLockConflict{
		{Path: "a.go", ResourceKey: "p:r:a.go", AttemptID: "att_1", ActorDisplay: "someone", WorkItemSlug: "aihub#1"},
	}

	ordinary, _ := commitLockConflictErr(conflicts, false).Details.(map[string]any)
	merged, _ := commitLockConflictErr(conflicts, true).Details.(map[string]any)
	ordinaryAdvice, _ := ordinary["advice"].(string)
	mergeAdvice, _ := merged["advice"].(string)

	if mergeAdvice == ordinaryAdvice {
		t.Fatalf("a merge refusal shipped the ordinary remedy. Following it deletes a file the "+
			"other parent contributed — measured in "+
			"TestCommitGateWire_MergeRefusalIsNotTheDestructiveRemedy — so this is the defect "+
			"aihub#662 exists to remove, not a cosmetic difference:\n%s", mergeAdvice)
	}
	if mergeAdvice != CommitLockMergeRefusalAdvice {
		t.Errorf("details.advice = %q, want CommitLockMergeRefusalAdvice", mergeAdvice)
	}
	if ordinaryAdvice != CommitLockRefusalAdvice {
		t.Errorf("the NON-merge refusal changed too: advice = %q. This test's premise is that "+
			"merge-ness selects between two remedies, not that it rewrites one", ordinaryAdvice)
	}

	// The structural half: no "(N) " marker anywhere, which is what makes
	// adviceRecipe return nothing for it.
	for n := 1; n <= 9; n++ {
		if marker := fmt.Sprintf("(%d) ", n); strings.Contains(CommitLockMergeRefusalAdvice, marker) {
			t.Errorf("the merge advice numbers a step %q. adviceRecipe parses that into a command "+
				"and internal/mcp EXECUTES it against the refused worktree — and there is no local "+
				"sequence that lands this commit without either keeping the contested file or "+
				"deleting it. A numbered step here is a recipe nothing can safely follow:\n%s",
				marker, CommitLockMergeRefusalAdvice)
		}
	}

	// Two prohibitions, both asserted textually for the reason the test above
	// gives: "do not do this" has no executable form. The first is the same
	// takeover warning; the second is the whole point of this string existing.
	if !strings.Contains(CommitLockMergeRefusalAdvice, "takeover") {
		t.Errorf("the merge advice dropped the force-takeover warning: %s", CommitLockMergeRefusalAdvice)
	}
	if !strings.Contains(CommitLockMergeRefusalAdvice, "git restore --staged") {
		t.Errorf("the merge advice never names `git restore --staged`. It is the command a reader "+
			"arrives already intending to run — it is what every other refusal tells them — so "+
			"the remedy has to name it in order to forbid it: %s", CommitLockMergeRefusalAdvice)
	}
}

// TestCommitGateRefusalIsGivenTheRequestsMergeFlag closes the gap the test above
// cannot see: that FnReconcileCommitLocks actually HANDS req.Merge to the
// selector.
//
// 🔴 IT EXISTS BECAUSE A MUTANT SURVIVED. Replacing `req.Merge` with `false` at
// both commitLockConflictErr call sites stayed GREEN across the whole suite,
// including the CI database job. Every behavioural test that reaches the
// selector reaches it directly (the one above) or through internal/mcp's fake
// server, which calls CommitLockAdviceFor ITSELF on the body it received and
// never executes this function at all. So the two lines that decide which
// remedy a real refusal ships were held by nothing, and they are the whole
// point of the change.
//
// The behavioural version needs Postgres — FnReconcileCommitLocks opens a
// SERIALIZABLE transaction before it can refuse anything — and a DB-gated test
// here would also have to be named in ci.yml's `-run` regexes or it would run
// nowhere. Reading the source costs one function and runs on every `go test`.
//
// ⚠️ It checks the ARGUMENT, not the value: a version that assigned
// `req.Merge = false` a few lines earlier would pass. That is what a database
// would buy, and it is not bought here.
func TestCommitGateRefusalIsGivenTheRequestsMergeFlag(t *testing.T) {
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "commit_locks.go", nil, parser.SkipObjectResolution)
	if err != nil {
		t.Fatalf("parse commit_locks.go: %v", err)
	}

	var fn *ast.FuncDecl
	for _, decl := range file.Decls {
		d, ok := decl.(*ast.FuncDecl)
		if ok && d.Recv == nil && d.Name.Name == "FnReconcileCommitLocks" {
			fn = d
			break
		}
	}
	if fn == nil {
		t.Fatal("commit_locks.go no longer declares FnReconcileCommitLocks; an arm that cannot " +
			"find its target checks nothing")
	}

	var calls []*ast.CallExpr
	ast.Inspect(fn.Body, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		if id, ok := call.Fun.(*ast.Ident); ok && id.Name == "commitLockConflictErr" {
			calls = append(calls, call)
		}
		return true
	})
	// Two: the phase-1 collected refusal and the phase-2 acquisition race.
	const wantCalls = 2
	if len(calls) != wantCalls {
		t.Fatalf("FnReconcileCommitLocks builds %d refusal(s), want %d (the phase-1 collected "+
			"conflicts and the phase-2 race). A new exit that forgot the flag is exactly what "+
			"this arm is for, so re-derive this number against the code rather than moving it",
			len(calls), wantCalls)
	}

	for i, call := range calls {
		if len(call.Args) != 2 {
			t.Errorf("refusal %d passes %d argument(s), want 2 (conflicts, merge)", i+1, len(call.Args))
			continue
		}
		sel, ok := call.Args[1].(*ast.SelectorExpr)
		if !ok {
			t.Errorf("refusal %d decides the remedy from %s, not from the request. A literal here "+
				"pins every refusal to one remedy, and the ordinary one DELETES a file the other "+
				"parent contributed when it is followed on a merge (measured in "+
				"TestCommitGateWire_MergeRefusalIsNotTheDestructiveRemedy)", i+1, exprText(call.Args[1]))
			continue
		}
		base, okBase := sel.X.(*ast.Ident)
		if sel.Sel.Name != "Merge" || !okBase || base.Name != "req" {
			t.Errorf("refusal %d passes %s, want req.Merge — the flag the client sends because "+
				"MERGE_HEAD is in ITS worktree and this process has never seen it",
				i+1, exprText(call.Args[1]))
		}
	}
}

// TestReconcileCommitLocksRequest_MergeDecodesFromTheWireKey pins the json tag
// against the key internal/mcp actually sends.
//
// The tag and the client's map literal are two copies of one string with
// nothing forcing them to agree — the shape coding.BaseMovedMarker's comment
// describes. A typo on either side decodes to the zero value, which is `false`,
// which is the DESTRUCTIVE answer for a merge, and no error is raised anywhere.
func TestReconcileCommitLocksRequest_MergeDecodesFromTheWireKey(t *testing.T) {
	var req ReconcileCommitLocksRequest
	if err := json.Unmarshal([]byte(`{"repo":"aihub","paths":["a.go"],"merge":true}`), &req); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if !req.Merge {
		t.Errorf("a body carrying \"merge\":true decoded to Merge=false. The zero value is the "+
			"ordinary-commit answer, so a tag typo silently restores the destructive remedy for "+
			"every merge: %+v", req)
	}
	// The negative control: an omitted key must stay false, because that is what
	// makes a client too old to send it behave exactly as before.
	var old ReconcileCommitLocksRequest
	if err := json.Unmarshal([]byte(`{"repo":"aihub","paths":["a.go"]}`), &old); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if old.Merge {
		t.Error("a body with no `merge` key decoded to Merge=true, so every ordinary commit from " +
			"an older client would be handed the merge remedy, which lands no commit at all")
	}
}

// TestCommitLockConflictErr_AdviceSurvivesTheRenderLimit is the byte budget, and
// it is a different budget from the one an earlier version of this file
// enforced.
//
// That version held the advice to 237 bytes so as not to push `conflicts` off
// the end of pkg/client.formatDetails' cap. Measured, that bought nothing: with
// realistic values the compacted envelope is already 606 bytes at ONE conflict,
// so `conflicts` is truncated away regardless, and the other two keys the cut
// can reach — `blocked_paths` and `conflict_with` — carry nothing the
// untruncated Message does not already state. What the budget must actually
// protect is this string itself: it sorts FIRST alphabetically, so it is the one
// key that is never pushed off the end and always cut in half instead. Half a
// two-step remedy is worse than no remedy, because it reads complete.
//
// Two assertions rather than one, because the arithmetic and the artefact can
// disagree: the first computes the budget from client.DetailsRenderLimit, the
// second cuts the REAL assembled envelope and looks for the whole string in
// what is left. A padding mutant that pushes the advice to 708 bytes trips both,
// which is what shows the threshold is real rather than always-satisfied.
// Both remedies are measured, not just the ordinary one (aihub#662): the merge
// advice is the LONGER of the two and it is the one that ships when the stakes
// are highest, so exempting it from the budget would leave exactly the string
// that must not arrive half-written as the only one nothing checks.
func TestCommitLockConflictErr_AdviceSurvivesTheRenderLimit(t *testing.T) {
	// `{"advice":"` is what precedes the value once Go sorts the keys.
	const advicePrefix = len(`{"advice":"`)
	budget := client.DetailsRenderLimit - advicePrefix

	for _, tc := range []struct {
		name   string
		advice string
		merge  bool
	}{
		{"ordinary", CommitLockRefusalAdvice, false},
		{"merge", CommitLockMergeRefusalAdvice, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			escaped, err := json.Marshal(tc.advice)
			if err != nil {
				t.Fatalf("marshal advice: %v", err)
			}
			// Minus the surrounding quotes: what actually occupies the envelope.
			got := len(escaped) - 2
			if got > budget {
				t.Errorf("the advice is %d escaped bytes and the budget is %d, so formatDetails cuts it "+
					"mid-sentence and the caller is handed half a remedy. Shorten it, or change "+
					"client.DetailsRenderLimit and this test together", got, budget)
			}
			t.Logf("advice = %d escaped bytes of a %d-byte budget (cap %d, prefix %d)",
				got, budget, client.DetailsRenderLimit, advicePrefix)

			// And it must survive in the assembled envelope, not just in isolation.
			raw, err := json.Marshal(commitLockConflictErr([]commitLockConflict{{
				Path: "internal/domain/commit_locks.go", ResourceKey: "aihub:aihub:internal/domain/commit_locks.go",
				AttemptID: "ra_q0f0brdY", ActorDisplay: "dahe", WorkItemSlug: "aihub#366",
			}}, tc.merge).Details)
			if err != nil {
				t.Fatalf("marshal details: %v", err)
			}
			var compact bytes.Buffer
			if err := json.Compact(&compact, raw); err != nil {
				t.Fatalf("compact: %v", err)
			}
			cut := compact.String()
			if len(cut) > client.DetailsRenderLimit {
				cut = cut[:client.DetailsRenderLimit]
			}
			if !strings.Contains(cut, string(escaped[1:len(escaped)-1])) {
				t.Errorf("the advice does not survive the %d-byte cut of the real envelope (%d bytes):\n%s",
					client.DetailsRenderLimit, compact.Len(), cut)
			}
		})
	}
}
