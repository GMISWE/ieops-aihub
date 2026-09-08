package mcp

// aihub#442, the sweep half — and it is in an INTERNAL test package on purpose.
//
// TestUpdateStepSchemaDoesNotPromiseALease (tools_step_contract_test.go) already
// guards every string pf_update_step PUBLISHES, but it lives in package mcp_test
// and so cannot reach validateNextStepArgs, whose message is produced only when
// a caller trips the guard. That gap is not academic: aihub#398 found the
// "refreshes the lease" wording had drifted into THIS copy of the message while
// the server-side twin already said step_started_at, so the one string a
// published-strings test cannot see is also the one that actually drifted.
//
// The claim being defended is aihub#416's owner ruling: aihub implemented leases
// early and abandoned them deliberately. Migration 0004 removed
// run_attempts.expires_at ("After claim, ownership is permanent; no expires_at")
// and pf_renew_lease answers 410, so a message telling an agent that a heartbeat
// refreshes a lease describes a mechanism that does not exist and implies its
// ownership depends on pinging. The word stays dead.

import (
	"strings"
	"testing"
)

func TestValidateNextStepArgsDoesNotResurrectTheWordLease(t *testing.T) {
	err := validateNextStepArgs("verify", "", "in_progress", true)
	if err == nil {
		t.Fatal("heartbeat + next_step must still be refused; with no error there is no message to check " +
			"and every assertion below would pass vacuously")
	}
	if strings.Contains(err.Error(), "lease") {
		t.Errorf("validateNextStepArgs claims a lease again: %s\n"+
			"There is no lease — migration 0004 removed run_attempts.expires_at and pf_renew_lease "+
			"answers 410. This exact message is where the claim drifted to once already (aihub#398).",
			err.Error())
	}
	// The positive half: a message emptied of content would satisfy the negative
	// check on its own, and the point of the sentence is to say what a heartbeat
	// DOES instead.
	if !strings.Contains(err.Error(), "step_started_at") {
		t.Errorf("validateNextStepArgs no longer says what a heartbeat actually refreshes: %s", err.Error())
	}
}
