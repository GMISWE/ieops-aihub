package rowserr

import (
	"strings"
	"testing"
)

const swallowAllowlistFile = "scan_swallow_allowlist.txt"

// TestNoDrainLoopSwallowsARowScanFailure is the aihub#608 gate.
//
// Like TestEveryRowsLoopChecksRowsErr it is a MECHANISM gate: it names no file
// and no function, it asks "does a drain loop anywhere discard a row-level
// Scan failure", which a loop added next month cannot answer differently. The
// aihub#608 inventory (GetReadyQueue's seven segments, PredictConflicts' six
// drains, recall's two paths, ListEvents, ListChildren/ListDependencies,
// textDedupCheck, FnClaimWorkItem's idempotent lock re-query, BearerAuth's and
// loadUserByAPIKeyID's membership drains, handleListUsers) is in that wi's
// commit; the adjudicated best-effort remainder is the allowlist beside this
// file, each entry with its reason.
func TestNoDrainLoopSwallowsARowScanFailure(t *testing.T) {
	root := repoRoot(t)
	swallows, err := ScanDirSwallows(root)
	if err != nil {
		t.Fatalf("scanning %s: %v", root, err)
	}
	allow, err := ParseAllowlist(swallowAllowlistFile)
	if err != nil {
		t.Fatalf("reading %s: %v", swallowAllowlistFile, err)
	}

	usedAllowEntries := map[string]bool{}
	for _, s := range swallows {
		if allow[s.Key()] {
			usedAllowEntries[s.Key()] = true
			continue
		}
		t.Errorf("%s discards a row-level Scan failure inside a drain loop.\n"+
			"    The shape spells \"drop this row, publish the rest as the complete result\". On pgx v5 a\n"+
			"    failed Scan poisons the rows, so what actually happens forks on this loop's rows.Err()\n"+
			"    arm: if it returns, the call fails under the WRONG site's message and only because of an\n"+
			"    undocumented driver side effect; if it logs or swallows, the truncated result really is\n"+
			"    published as complete (aihub#206 lost stalled[] rows this way; aihub#608's BearerAuth\n"+
			"    finding authenticated callers with partial ProjectRoles). Fix: surface the Scan error at\n"+
			"    the site — `return …, dbErrCause(err, \"failed to scan <what> row\")` in domain code — and\n"+
			"    widen the scan target instead if the column can hold a LEGITIMATE NULL.\n"+
			"    If this drain is genuinely best-effort, add this exact line to\n"+
			"    internal/citest/rowserr/%s with the reason beside it:\n        %s",
			s, swallowAllowlistFile, s.Key())
	}

	// A stale entry is itself a failure: it pre-authorises whatever lands on
	// that key next. This arm doubles as the liveness check for the detector —
	// the allowlist is non-empty by design, so a detector that stops seeing
	// swallows turns EVERY entry stale and fails here, rather than passing on
	// an empty census (the aihub#386 "seeing nothing and seeing nothing wrong
	// share an exit code" problem, answered structurally this time).
	for key := range allow {
		if !usedAllowEntries[key] {
			t.Errorf("allowlist entry %q in %s matches no drain-loop swallow — delete it, or the detector "+
				"has gone blind (in which case every entry fails here at once; fix the detector).",
				key, swallowAllowlistFile)
		}
	}
}

// ─── The detector's own behaviour, on fixtures ───────────────────────────────
//
// Same discipline as the Err() scanner's fixtures: every arm is a source shape
// whose verdict is known by construction, red and green both covered — a
// detector that flags everything and one that flags nothing each pass half.

func scanOneSwallow(t *testing.T, src string) []Swallow {
	t.Helper()
	swallows, err := ScanSourceSwallows([]byte(src), "fixture.go")
	if err != nil {
		t.Fatalf("scanning fixture: %v", err)
	}
	return swallows
}

func TestSwallowDetectorRejectsTheShapesItWasWrittenFor(t *testing.T) {
	cases := []struct {
		name  string
		src   string
		shape string
	}{{
		// The exact pre-aihub#608 GetReadyQueue shape, all seven segments.
		name: "bare continue on a Scan error",
		src: `package p
func f() {
	rows, err := pool.Query(ctx, "SELECT 1")
	for rows.Next() {
		var s string
		if err := rows.Scan(&s); err != nil {
			continue
		}
		out = append(out, s)
	}
}`,
		shape: "guarded-continue",
	}, {
		// The pre-aihub#608 recallText shape: the log does not stop the partial
		// result from publishing, so it is still a swallow ("don't just move
		// the log" is the wi's own words).
		name: "log-and-continue on a Scan error",
		src: `package p
func f() {
	rows, err := pool.Query(ctx, "SELECT 1")
	for rows.Next() {
		var s string
		if err := rows.Scan(&s); err != nil {
			fmt.Fprintf(os.Stderr, "scan: %v\n", err)
			continue
		}
		out = append(out, s)
	}
}`,
		shape: "guarded-continue",
	}, {
		// The pre-aihub#608 recallText DERIVATION: the scan happens in a helper
		// that takes the rows, so there is no `.Scan` selector to key on.
		name: "helper-scan error continued",
		src: `package p
func f() {
	rows, err := pool.Query(ctx, "SELECT 1")
	for rows.Next() {
		m, err := scanMemoryLite(rows)
		if err != nil {
			continue
		}
		items = append(items, m)
	}
}`,
		shape: "guarded-continue",
	}, {
		// The pre-aihub#608 textDedupCheck / FnClaimWorkItem shape: the failure
		// path is empty by construction.
		name: "success-only guard, if-init",
		src: `package p
func f() {
	rows, err := pool.Query(ctx, "SELECT 1")
	for rows.Next() {
		var s string
		if err := rows.Scan(&s); err == nil {
			out = append(out, s)
		}
	}
}`,
		shape: "success-only",
	}, {
		// The pre-aihub#608 fetchWIFacets shape: the call is the condition.
		name: "success-only guard, call as condition",
		src: `package p
func f() {
	rows, err := pool.Query(ctx, "SELECT 1")
	for rows.Next() {
		var s string
		if rows.Scan(&s) == nil {
			out = append(out, s)
		}
	}
}`,
		shape: "success-only",
	}, {
		// aihub#549's shape one loop deeper: guarded, returning, and still
		// publishing the partial result as a normal answer.
		name: "error arm returns the result with a nil error",
		src: `package p
func f() (int, error) {
	rows, err := pool.Query(ctx, "SELECT 1")
	for rows.Next() {
		var s string
		if err := rows.Scan(&s); err != nil {
			return result, nil
		}
		out = append(out, s)
	}
	return result, nil
}`,
		shape: "published-as-success",
	}}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			swallows := scanOneSwallow(t, tc.src)
			if len(swallows) != 1 {
				t.Fatalf("detector saw %d swallows, want exactly 1: %v", len(swallows), swallows)
			}
			if swallows[0].Shape != tc.shape {
				t.Errorf("shape = %q, want %q", swallows[0].Shape, tc.shape)
			}
		})
	}
}

func TestSwallowDetectorAcceptsTheCompliantShapes(t *testing.T) {
	cases := []struct{ name, src string }{{
		// The aihub#608 fixed shape.
		name: "Scan error fails the call",
		src: `package p
func f() (*R, *AihubError) {
	rows, err := pool.Query(ctx, "SELECT 1")
	for rows.Next() {
		var s string
		if err := rows.Scan(&s); err != nil {
			return nil, dbErrCause(err, "failed to scan row")
		}
		out = append(out, s)
	}
	return r, nil
}`,
	}, {
		// Close-then-return is still a return (FnAcquireLocks' heldRows shape).
		name: "Scan error closes then returns",
		src: `package p
func f() (*R, *AihubError) {
	rows, err := pool.Query(ctx, "SELECT 1")
	for rows.Next() {
		var s string
		if err := rows.Scan(&s); err != nil {
			rows.Close()
			return nil, dbErrCause(err, "failed to scan row")
		}
		out = append(out, s)
	}
	return r, nil
}`,
	}, {
		// A post-scan FILTER is not an error guard: no scan-derived condition.
		name: "filter continue is not a swallow",
		src: `package p
func f() {
	rows, err := pool.Query(ctx, "SELECT 1")
	for rows.Next() {
		var s string
		if err := rows.Scan(&s); err != nil {
			return
		}
		if !keep(s) {
			continue
		}
		out = append(out, s)
	}
}`,
	}, {
		// cmd/aihub-embed-backfill's shape: the failure ends the process, so it
		// surfaces as a non-zero exit rather than a shrunken result.
		name: "Scan error exits the process",
		src: `package p
func f() {
	rows, err := pool.Query(ctx, "SELECT 1")
	for rows.Next() {
		var s string
		if err := rows.Scan(&s); err != nil {
			fmt.Fprintln(os.Stderr, "scan:", err)
			os.Exit(1)
		}
		out = append(out, s)
	}
}`,
	}, {
		// A success-only guard whose else surfaces the failure is compliant.
		name: "success-only guard with a returning else",
		src: `package p
func f() (*R, error) {
	rows, err := pool.Query(ctx, "SELECT 1")
	for rows.Next() {
		var s string
		if err := rows.Scan(&s); err == nil {
			out = append(out, s)
		} else {
			return nil, err
		}
	}
	return r, nil
}`,
	}, {
		// A void function's early return is not "published as success": there is
		// no result to publish (attachStepState's would-be repair shape).
		name: "void function returns on Scan error",
		src: `package p
func f() {
	rows, err := pool.Query(ctx, "SELECT 1")
	for rows.Next() {
		var s string
		if err := rows.Scan(&s); err != nil {
			return
		}
		out = append(out, s)
	}
}`,
	}, {
		// A loop over something that is not query rows is none of this gate's
		// business, whatever its body does (the html.Tokenizer lesson).
		name: "non-rows iterator is ignored",
		src: `package p
func f() {
	z := html.NewTokenizer(r)
	for z.Next() {
		if err := z.Scan(&s); err != nil {
			continue
		}
	}
}`,
	}}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if swallows := scanOneSwallow(t, tc.src); len(swallows) != 0 {
				t.Errorf("detector flagged a compliant shape: %v", swallows)
			}
		})
	}
}

// TestSwallowDetectorSeesTheLedgeredPopulation pins the adjudicated remainder:
// the detector must still see every allowlisted swallow, and the count arm
// keeps "the detector went blind" and "the tree is clean" from sharing an exit
// code. Measured 2026-09-12 on the aihub#608 tree: 11 swallows across the 10
// allowlist keys (fetchWIFacets holds two loops under one key).
func TestSwallowDetectorSeesTheLedgeredPopulation(t *testing.T) {
	root := repoRoot(t)
	swallows, err := ScanDirSwallows(root)
	if err != nil {
		t.Fatalf("scanning %s: %v", root, err)
	}
	const wantSwallows = 11
	if len(swallows) != wantSwallows {
		var got []string
		for _, s := range swallows {
			got = append(got, s.String())
		}
		t.Errorf("detector sees %d swallows, want %d.\n"+
			"    More: a new drain discards its Scan error — adjudicate it (fail the call, or ledger it\n"+
			"    with a reason) and re-pin. Fewer: a ledgered site was fixed (delete its entry and lower\n"+
			"    this), or the detector went blind (the stale-entry arm of the main gate fires too).\n"+
			"    Current census:\n        %s", len(swallows), wantSwallows, strings.Join(got, "\n        "))
	}
}
