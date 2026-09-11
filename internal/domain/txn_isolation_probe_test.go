package domain

import (
	"strings"
	"testing"
)

// aihub#591 — the isolation-level split, pinned. aihub#546 renamed it to what
// the source actually says.
//
// Two cards state the same two-sided claim: pf_claim_work_item's path pins
// SERIALIZABLE while pf_force_takeover's opens a bare pool.Begin — no isolation
// pinned at all — which is why the same commit-window interleaving comes back
// 409 CONFLICT_SERIALIZATION_FAILURE with the row unchanged on the claim path
// (aihub#430) and comes back as a SILENT DISPLACEMENT on the takeover path
// (aihub#451, force_takeover_commit_window_db_test.go). One sentence cannot be
// true of both sides of that split — the cards say so in as many words — so the
// split itself is load-bearing, and until 2026-09-10 nothing pinned it:
// aihub#543's wave-1 checkpoint rated this the cheapest high-value unheld claim
// in the scoped set (spec §1.4.2), and a test-file comment asserting
// FnForceTakeover "is SERIALIZABLE" had already been through review while being
// false.
//
// The pin is on the SOURCE, deliberately. A live-DB reading of
// current_setting('transaction_isolation') inside each path would need a hook in
// both functions; the BeginTx options ARE the behaviour (pgx serialises IsoLevel
// straight into the BEGIN statement), and the comment-stripped source is the
// same instrument this package already trusts for the aihub#359 dead-branch ban.
// What is pinned for the takeover is the ABSENCE of a pin: pool.Begin plus no
// BeginTx / Serializable anywhere in the body. pgx.TxOptions{} serialises to a
// bare BEGIN, which carries no isolation clause, so the transaction runs at
// default_transaction_isolation — decided by the database, the role or the DSN,
// not by this repo's code (aihub#497 measured that internal/db/db.go's
// pgxpool.New pins nothing either). That is read committed at the deployed
// defaults — the reading force_takeover_commit_window_db_test.go documents for
// its interleaving — but it is a configuration fact, not a code fact, and
// callers must not assume the takeover path cannot answer a class-40 rollback:
// 40P01 arrives at any isolation level, 40001 wherever configuration raises the
// default, both surfaced as the retryable 409 since aihub#497.
func TestClaimOpensSerializableAndTakeoverOpensBareBegin(t *testing.T) {
	code := stripComments(t, sourceOf(t, claimSourceFile))

	const serializableOpen = "pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.Serializable})"

	// ── Negative controls first: every matcher below must fire on the shape it
	// exists to find, or a whitespace change in gofmt output clears every tree.
	ctrl := stripComments(t, "package p\nfunc f() {\n"+
		"\ttx, err := pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.Serializable})\n"+
		"\t_, _ = tx, err\n}\n")
	if !strings.Contains(collapse(ctrl), serializableOpen) {
		t.Fatal("the serializable-open matcher does not find the shape it exists to find — " +
			"re-printed source must still carry it, or the positive assertion below is " +
			"satisfied by nothing and the negative one refuses everything")
	}

	claimBody := bodyOf(t, code, "FnClaimWorkItem")
	takeoverBody := bodyOf(t, code, "FnForceTakeover")

	if !strings.Contains(claimBody, serializableOpen) {
		t.Errorf("FnClaimWorkItem no longer opens its transaction with %s. The claim card "+
			"publishes that this path is SERIALIZABLE — it is why a commit-window "+
			"interleaving is a retryable 409 with the row unchanged (aihub#430) instead of "+
			"a silent displacement — and aihub#334 documents the same fact beside the "+
			"INSERT. If the isolation level really changed, the pf_claim_work_item and "+
			"pf_force_takeover cards both carry sentences that are now false; fix those in "+
			"the same change or this arm is pinning a stale contract.", serializableOpen)
	}

	if !strings.Contains(takeoverBody, "pool.Begin(ctx)") {
		t.Errorf("FnForceTakeover no longer opens its transaction with pool.Begin(ctx). Its " +
			"card publishes that no isolation level is pinned here — a bare BEGIN runs at " +
			"default_transaction_isolation, whatever the database, role or DSN sets — and " +
			"force_takeover_commit_window_db_test.go measured the displacement window that " +
			"only exists at the read committed default. If the open moved, re-measure the " +
			"window and move both cards' sentences with it.")
	}
	if strings.Contains(takeoverBody, "BeginTx") || strings.Contains(takeoverBody, "Serializable") {
		t.Errorf("FnForceTakeover now carries BeginTx/Serializable. Its card publishes the " +
			"OPPOSITE — 'that path pins SERIALIZABLE while this one opens a bare pool.Begin, " +
			"no isolation pinned' — and aihub#451's measured displacement semantics depend " +
			"on the unpinned default. Raising the takeover's isolation is a behaviour change " +
			"the card set has to move with, not a drive-by: see " +
			"force_takeover_commit_window_db_test.go before pinning anything new here.")
	}
}

// collapse mirrors bodyOf's whitespace collapsing for text that is not a single
// function body, so the negative control compares like with like.
func collapse(s string) string {
	return strings.Join(strings.Fields(s), " ")
}
