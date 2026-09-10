package domain

import (
	"strings"
	"testing"
)

// aihub#591 — the isolation-level pair, pinned.
//
// Two cards state the same two-sided claim: pf_claim_work_item's path opens
// SERIALIZABLE while pf_force_takeover's opens READ COMMITTED, which is why the
// same commit-window interleaving comes back 409 CONFLICT_SERIALIZATION_FAILURE
// with the row unchanged on the claim path (aihub#430) and comes back as a
// SILENT DISPLACEMENT on the takeover path (aihub#451,
// force_takeover_commit_window_db_test.go). One sentence cannot be true of both
// isolation levels — the cards say so in as many words — so the split itself is
// load-bearing, and until 2026-09-10 nothing pinned it: aihub#543's wave-1
// checkpoint rated this the cheapest high-value unheld claim in the scoped set
// (spec §1.4.2), and a test-file comment asserting FnForceTakeover "is
// SERIALIZABLE" had already been through review while being false.
//
// The pin is on the SOURCE, deliberately. A live-DB reading of
// current_setting('transaction_isolation') inside each path would need a hook in
// both functions; the BeginTx options ARE the behaviour (pgx serialises IsoLevel
// straight into the BEGIN statement), and the comment-stripped source is the
// same instrument this package already trusts for the aihub#359 dead-branch ban.
// READ COMMITTED is pinned as pool.Begin plus the ABSENCE of any BeginTx /
// Serializable in the takeover body: pgx.TxOptions{} serialises to a bare BEGIN,
// so the default is PostgreSQL's default_transaction_isolation, read committed
// everywhere this repo deploys — the same reading
// force_takeover_commit_window_db_test.go documents for its interleaving.
func TestClaimOpensSerializableAndTakeoverOpensReadCommitted(t *testing.T) {
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
			"card publishes READ COMMITTED — the default a bare BEGIN gets — and " +
			"force_takeover_commit_window_db_test.go measured the displacement window that " +
			"only exists at that level. If the open moved, re-measure the window and move " +
			"both cards' sentences with it.")
	}
	if strings.Contains(takeoverBody, "BeginTx") || strings.Contains(takeoverBody, "Serializable") {
		t.Errorf("FnForceTakeover now carries BeginTx/Serializable. Its card publishes the " +
			"OPPOSITE — 'that path opens SERIALIZABLE while this one opens READ COMMITTED' — " +
			"and aihub#451's measured displacement semantics depend on it. Raising the " +
			"takeover's isolation is a behaviour change the card set has to move with, not a " +
			"drive-by: see force_takeover_commit_window_db_test.go before pinning anything " +
			"new here.")
	}
}

// collapse mirrors bodyOf's whitespace collapsing for text that is not a single
// function body, so the negative control compares like with like.
func collapse(s string) string {
	return strings.Join(strings.Fields(s), " ")
}
