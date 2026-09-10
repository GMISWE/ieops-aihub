package domain

import (
	"context"
	"strings"
	"testing"
)

// aihub#591 — "The server 400s without it."
//
// The pf_claim_work_item card publishes, one bullet under `session_info.machine_id`,
// that the server 400s a claim carrying none — and nothing held it: the refusal
// lives at the top of FnClaimWorkItem and no test drove the branch. It was also the
// measured specimen of the recogniser's token-in-the-previous-sentence blind spot,
// so the sentence that promised this behaviour was invisible to the ledger AND
// unheld at once — the pairing this wi exists to end.
//
// nil pool, deliberately: the three required-field refusals sit above BeginTx, so a
// test that passes with no database PROVES the refusal happens before any state is
// touched — the TestPredictConflicts_RejectsUnknownTypeBeforeTouchingDB technique.
// If someone moves the checks below the transaction open, this test starts panicking
// or erroring differently, which is the right kind of loud.
func TestClaimRefusesAMissingMachineIDBeforeTouchingDB(t *testing.T) {
	cases := []struct {
		name    string
		req     *ClaimRequest
		wantSub string
	}{
		{
			name: "no machine_id — the card's published 400",
			req: &ClaimRequest{
				IdempotencyKey: "k-591",
				SessionInfo:    SessionInfo{SessionSecret: "s-591"},
			},
			wantSub: "machine_id",
		},
		{
			name:    "no idempotency_key",
			req:     &ClaimRequest{SessionInfo: SessionInfo{MachineID: "m", SessionSecret: "s"}},
			wantSub: "idempotency_key",
		},
		{
			name: "no session_secret",
			req: &ClaimRequest{
				IdempotencyKey: "k-591",
				SessionInfo:    SessionInfo{MachineID: "m"},
			},
			wantSub: "session_secret",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			resp, err := FnClaimWorkItem(context.Background(), nil, "wi_x", tc.req, "u", "", "d")
			if err == nil {
				t.Fatalf("FnClaimWorkItem accepted the request; resp=%+v — the card says the "+
					"server 400s without %s, and an acceptance here means the claim path went "+
					"on toward the database with an incomplete credential", resp, tc.wantSub)
			}
			if err.HTTPStatus != 400 {
				t.Errorf("HTTPStatus = %d, want 400 — the card publishes a 400, and a different "+
					"status is a different contract", err.HTTPStatus)
			}
			if !strings.Contains(err.Message, tc.wantSub) {
				t.Errorf("refusal %q does not name %q — a caller has to be told WHICH field is "+
					"missing, or the repair is a guess", err.Message, tc.wantSub)
			}
		})
	}
}
