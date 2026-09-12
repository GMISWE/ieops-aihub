package rowserr

import (
	"strings"
	"testing"
)

const errArmAllowlistFile = "err_arm_allowlist.txt"

// TestNoRowsErrCheckArmLeaksTheError is the aihub#623 gate.
//
// Like its two siblings it is a MECHANISM gate: it names no file and no
// function, it asks "does a rows.Err() check anywhere let the error fall out
// of its arm", which a check added next month cannot answer differently. The
// specimen is FnClaimWorkItem's idempotent lock re-query (fixed in aihub#608):
// the Err arm returned only for class 40 and fell through for everything else,
// publishing a truncated AcquiredLocks — while the aihub#386 gate, which
// polices never-ASKING, correctly called that loop checked. The adjudicated
// best-effort remainder is the ledger beside this file, each entry citing the
// written policy that makes its fall-through defensible.
func TestNoRowsErrCheckArmLeaksTheError(t *testing.T) {
	root := repoRoot(t)
	arms, err := ScanDirErrArms(root)
	if err != nil {
		t.Fatalf("scanning %s: %v", root, err)
	}
	allow, err := ParseAllowlist(errArmAllowlistFile)
	if err != nil {
		t.Fatalf("reading %s: %v", errArmAllowlistFile, err)
	}

	usedAllowEntries := map[string]bool{}
	for _, a := range arms {
		if allow[a.Key()] {
			usedAllowEntries[a.Key()] = true
			continue
		}
		t.Errorf("%s checks rows.Err() but the error can leak out of the check.\n"+
			"    fall-through: the non-nil arm does not end in a return or a process-ending call, so\n"+
			"    execution continues and the partial result is published as the complete one — the shape\n"+
			"    that made FnClaimWorkItem answer an idempotent re-claim with truncated AcquiredLocks\n"+
			"    (aihub#608): its arm returned only what retryConflictErr classified and fell through for\n"+
			"    every other error. published-as-success: the arm returns, but its final result is the\n"+
			"    literal nil — the failure leaves as a normal answer. narrowed-condition: the guard's\n"+
			"    condition runs the arm for only SOME non-nil errors; the rest fall through by\n"+
			"    construction. unjudged-check-form: the Err() call sits in a shape this analysis cannot\n"+
			"    judge — restructure it into `if err := rows.Err(); err != nil { return … }`.\n"+
			"    Fix: make the arm terminate — `return …, dbErrCause(err, \"…\")` in domain code. If the\n"+
			"    fall-through is genuinely best-effort BY WRITTEN POLICY, add this exact line to\n"+
			"    internal/citest/rowserr/%s citing that policy:\n        %s",
			a, errArmAllowlistFile, a.Key())
	}

	// A stale entry is itself a failure: it pre-authorises whatever lands on
	// that key next. As in the swallow gate, this arm doubles as the liveness
	// check for the detector — the ledger is non-empty by design, so a
	// detector that stops seeing arms turns EVERY entry stale and fails here,
	// rather than passing on an empty census.
	for key := range allow {
		if !usedAllowEntries[key] {
			t.Errorf("allowlist entry %q in %s matches no leaking Err arm — delete it, or the detector "+
				"has gone blind (in which case every entry fails here at once; fix the detector).",
				key, errArmAllowlistFile)
		}
	}
}

// ─── The detector's own behaviour, on fixtures ───────────────────────────────
//
// Same discipline as the siblings: every arm is a source shape whose verdict
// is known by construction, red and green both covered — a detector that flags
// everything and one that flags nothing each pass half.

func scanOneErrArm(t *testing.T, src string) []ErrArm {
	t.Helper()
	arms, err := ScanSourceErrArms([]byte(src), "fixture.go")
	if err != nil {
		t.Fatalf("scanning fixture: %v", err)
	}
	return arms
}

func TestErrArmDetectorRejectsTheShapesItWasWrittenFor(t *testing.T) {
	cases := []struct {
		name  string
		src   string
		shape string
	}{{
		// The specimen: FnClaimWorkItem's pre-aihub#608 Err arm, verbatim in
		// shape — only the class-40 half returns, everything else falls
		// through into publishing the partial drain.
		name: "only retryConflictErr, no else-return",
		src: `package p
func f() (*R, *AihubError) {
	rows, qerr := pool.Query(ctx, "SELECT 1")
	for rows.Next() {
		if err := rows.Scan(&s); err != nil {
			return nil, dbErrCause(err, "scan")
		}
	}
	if err := rows.Err(); err != nil {
		if aerr := retryConflictErr(err, "load locks"); aerr != nil {
			return nil, aerr
		}
	}
	return publish(), nil
}`,
		shape: "fall-through",
	}, {
		// The /ui decoration shape: the arm logs and control walks out.
		name: "log-only arm",
		src: `package p
func f() F {
	rows, qerr := pool.Query(ctx, "SELECT 1")
	for rows.Next() {
	}
	if err := rows.Err(); err != nil {
		fmt.Fprintf(os.Stderr, "rows: %v\n", err)
	}
	return out
}`,
		shape: "fall-through",
	}, {
		// A continue in the Err arm targets an OUTER loop: skip this
		// resource's failure, keep the rest — a best-effort skip that has to
		// be adjudicated, not waved through.
		name: "continue to an outer loop",
		src: `package p
func f() error {
	for _, r := range resources {
		rows, qerr := pool.Query(ctx, "SELECT 1", r)
		for rows.Next() {
		}
		if err := rows.Err(); err != nil {
			continue
		}
	}
	return nil
}`,
		shape: "fall-through",
	}, {
		// checkDedup / unblockDependentWI's shape: the arm terminates, but by
		// republishing the failure as a normal answer.
		name: "arm returns literal nil",
		src: `package p
func f() *AihubError {
	rows, qerr := pool.Query(ctx, "SELECT 1")
	for rows.Next() {
	}
	if err := rows.Err(); err != nil {
		if aerr := retryConflictErr(err, "x"); aerr != nil {
			return aerr
		}
		return nil
	}
	return nil
}`,
		shape: "published-as-success",
	}, {
		// "Only judge ErrNoRows" and its relatives: a condition narrower than
		// err != nil falls through for the unmatched errors by construction,
		// whatever the arm does.
		name: "narrowed condition",
		src: `package p
func f() error {
	rows, qerr := pool.Query(ctx, "SELECT 1")
	for rows.Next() {
	}
	if err := rows.Err(); err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return err
	}
	return nil
}`,
		shape: "narrowed-condition",
	}, {
		// Success-only Err guard with no else: the failure path is empty by
		// construction.
		name: "success-only guard without else",
		src: `package p
func f() *R {
	rows, qerr := pool.Query(ctx, "SELECT 1")
	for rows.Next() {
	}
	if rows.Err() == nil {
		return build()
	}
	return build()
}`,
		shape: "fall-through",
	}, {
		// A bare log argument: the check exists only inside a Printf. The
		// aihub#386 gate counts this as "checked"; this gate does not let it
		// pass unaccounted.
		name: "Err as a log argument",
		src: `package p
func f() {
	rows, qerr := pool.Query(ctx, "SELECT 1")
	for rows.Next() {
	}
	log.Printf("done: %v", rows.Err())
}`,
		shape: "unjudged-check-form",
	}, {
		// Asked and thrown away.
		name: "Err assigned to blank",
		src: `package p
func f() {
	rows, qerr := pool.Query(ctx, "SELECT 1")
	for rows.Next() {
	}
	_ = rows.Err()
}`,
		shape: "unjudged-check-form",
	}, {
		// Assigned to a real ident that nothing ever consults.
		name: "Err assigned, never consulted",
		src: `package p
func f() int {
	rows, qerr := pool.Query(ctx, "SELECT 1")
	for rows.Next() {
	}
	err := rows.Err()
	return count
}`,
		shape: "unjudged-check-form",
	}}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			arms := scanOneErrArm(t, tc.src)
			if len(arms) != 1 {
				t.Fatalf("detector saw %d arms, want exactly 1: %v", len(arms), arms)
			}
			if arms[0].Shape != tc.shape {
				t.Errorf("shape = %q, want %q", arms[0].Shape, tc.shape)
			}
		})
	}
}

func TestErrArmDetectorAcceptsTheCompliantShapes(t *testing.T) {
	cases := []struct{ name, src string }{{
		// The repo's dominant idiom.
		name: "arm returns the error",
		src: `package p
func f() (*R, *AihubError) {
	rows, qerr := pool.Query(ctx, "SELECT 1")
	for rows.Next() {
	}
	if err := rows.Err(); err != nil {
		return nil, dbErrCause(err, "rows")
	}
	return r, nil
}`,
	}, {
		// The aihub#608 FIXED FnClaimWorkItem shape: classify, then the
		// non-conflict half returns too.
		name: "classified then unconditional return",
		src: `package p
func f() (*R, *AihubError) {
	rows, qerr := pool.Query(ctx, "SELECT 1")
	for rows.Next() {
	}
	if err := rows.Err(); err != nil {
		if aerr := retryConflictErr(err, "load locks"); aerr != nil {
			return nil, aerr
		}
		return nil, dbErrCause(err, "load locks")
	}
	return r, nil
}`,
	}, {
		// An if/else whose branches BOTH return terminates; the simple
		// "last statement must be a return" rule would misread this.
		name: "if-else with both branches returning",
		src: `package p
func f() error {
	rows, qerr := pool.Query(ctx, "SELECT 1")
	for rows.Next() {
	}
	if err := rows.Err(); err != nil {
		if isClass40(err) {
			return conflictErr(err)
		} else {
			return dbErr(err)
		}
	}
	return nil
}`,
	}, {
		// The gc.go shape: the failure is recorded in the result the arm
		// returns — an ident, not the literal nil.
		name: "arm records the error and returns the carrier",
		src: `package p
func f() *GCResult {
	rows, qerr := pool.Query(ctx, "SELECT 1")
	for rows.Next() {
	}
	if err := rows.Err(); err != nil {
		result.Error = fmt.Sprintf("rows: %v", err)
		return result
	}
	return result
}`,
	}, {
		// cmd/ shape: the process ends, the failure surfaces as exit 1.
		name: "arm exits the process",
		src: `package p
func f() {
	rows, qerr := pool.Query(ctx, "SELECT 1")
	for rows.Next() {
	}
	if err := rows.Err(); err != nil {
		fmt.Fprintln(os.Stderr, "rows:", err)
		os.Exit(1)
	}
}`,
	}, {
		// The error leaves through a return expression (routes_step's second
		// half, and any classifier-wrapped return).
		name: "Err inside a return",
		src: `package p
func f() (*R, error) {
	rows, qerr := pool.Query(ctx, "SELECT 1")
	for rows.Next() {
	}
	return r, rows.Err()
}`,
	}, {
		// routes_step's first half: the call is the condition, the arm returns.
		name: "call-as-condition with returning arm",
		src: `package p
func f() (*R, error) {
	rows, qerr := pool.Query(ctx, "SELECT 1")
	for rows.Next() {
	}
	if rows.Err() != nil {
		return nil, rows.Err()
	}
	return r, nil
}`,
	}, {
		// The split form: assign, then judge.
		name: "assign then if",
		src: `package p
func f() error {
	rows, qerr := pool.Query(ctx, "SELECT 1")
	for rows.Next() {
	}
	err := rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	return nil
}`,
	}, {
		// The split form ending in a return of the ident.
		name: "assign then return",
		src: `package p
func f() error {
	rows, qerr := pool.Query(ctx, "SELECT 1")
	for rows.Next() {
	}
	err := rows.Err()
	return err
}`,
	}, {
		// Success-only guard whose else surfaces the failure.
		name: "success-only guard with returning else",
		src: `package p
func f() (*R, error) {
	rows, qerr := pool.Query(ctx, "SELECT 1")
	for rows.Next() {
	}
	if rows.Err() == nil {
		return r, nil
	} else {
		return nil, rows.Err()
	}
}`,
	}, {
		// A void function's early return: nothing is published, so returning
		// bare is the whole repair available to it (attachStepState's would-be
		// shape, same ruling as the swallow detector's).
		name: "void function returns on Err",
		src: `package p
func f() {
	rows, qerr := pool.Query(ctx, "SELECT 1")
	for rows.Next() {
	}
	if err := rows.Err(); err != nil {
		return
	}
	use(out)
}`,
	}, {
		// ctx.Err() and a bufio.Scanner's Err() are not rows; the recognition
		// excludes them, so this analysis has nothing to say about either.
		name: "non-rows Err calls are ignored",
		src: `package p
func f(ctx context.Context) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	sc := bufio.NewScanner(r)
	for sc.Scan() {
	}
	if err := sc.Err(); err != nil {
		log.Printf("scan: %v", err)
	}
	return nil
}`,
	}}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if arms := scanOneErrArm(t, tc.src); len(arms) != 0 {
				t.Errorf("detector flagged a compliant shape: %v", arms)
			}
		})
	}
}

// TestErrArmDetectorSeesTheLedgeredPopulation pins the adjudicated remainder:
// the detector must still see every ledgered arm, and the count arm keeps "the
// detector went blind" and "the tree is clean" from sharing an exit code.
// Measured 2026-09-12 on the aihub#620 tree: 8 leaking arms across the 7
// ledger keys (fetchWIFacets holds two Err arms under one key), 6 fall-through
// and 2 published-as-success, all adjudicated best-effort with the written
// policies cited in the ledger.
func TestErrArmDetectorSeesTheLedgeredPopulation(t *testing.T) {
	root := repoRoot(t)
	arms, err := ScanDirErrArms(root)
	if err != nil {
		t.Fatalf("scanning %s: %v", root, err)
	}
	const wantArms = 8
	if len(arms) != wantArms {
		var got []string
		for _, a := range arms {
			got = append(got, a.String())
		}
		t.Errorf("detector sees %d leaking Err arms, want %d.\n"+
			"    More: a new check lets its error leak — adjudicate it (make the arm terminate, or\n"+
			"    ledger it with the written policy) and re-pin. Fewer: a ledgered site was fixed (delete\n"+
			"    its entry and lower this), or the detector went blind (the stale-entry arm of the main\n"+
			"    gate fires too).\n"+
			"    Current census:\n        %s", len(arms), wantArms, strings.Join(got, "\n        "))
	}
}

// TestErrArmKeyOmitsTheLineNumber pins the same property Loop.Key and
// Swallow.Key carry: a ledger entry keyed on a line number silently expires
// when an edit above it moves the line.
func TestErrArmKeyOmitsTheLineNumber(t *testing.T) {
	a := ErrArm{File: "a/b.go", Line: 10, Func: "f", Rows: "rows"}
	b := ErrArm{File: "a/b.go", Line: 999, Func: "f", Rows: "rows"}
	if a.Key() != b.Key() {
		t.Errorf("Key() differs for the same arm at two line numbers (%q vs %q)", a.Key(), b.Key())
	}
	if strings.Contains(a.Key(), "10") {
		t.Errorf("Key() %q contains the line number", a.Key())
	}
}
