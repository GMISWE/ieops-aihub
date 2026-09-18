package workflow

import (
	"encoding/json"
	"testing"
)

func TestRHSMatrixAndOmittedDefault(t *testing.T) {
	for _, tc := range []struct {
		wi         bool
		step       *bool
		unattended bool
	}{
		{false, nil, true}, {false, boolp(false), true}, {false, boolp(true), true},
		{true, nil, true}, {true, boolp(false), true}, {true, boolp(true), false},
	} {
		f, b := safeFlow()
		f.Steps[0].RHS = tc.step
		v, e := Validate(f, b, safeContext(f))
		if e != nil {
			t.Fatal(e)
		}
		if v.Unattended(tc.wi) != tc.unattended {
			t.Fatalf("WI=%t step=%v unattended=%t", tc.wi, tc.step, v.Unattended(tc.wi))
		}
	}
	f, b := safeFlow()
	b[0].Contract.Runtime.Interactive = true
	if _, e := Validate(f, b, safeContext(f)); e == nil {
		t.Fatal("omitted step rhs allowed interactive-only skill")
	}
	f.Steps[0].RHS = boolp(true)
	v, e := Validate(f, b, safeContext(f))
	if e != nil {
		t.Fatal(e)
	}
	if _, e := Decide(v, nil, PolicyInput{}); e == nil {
		t.Fatal("WI rhs=false allowed interactive-only skill")
	}
}

func TestWorkerApprovalCannotGrant(t *testing.T) {
	raw := `{"status":"completed","approval":{"decision":"approved","actor_id":"human"}}`
	var result StepResult
	if e := json.Unmarshal([]byte(raw), &result); e == nil {
		t.Fatal("worker-supplied approval accepted")
	}
}

func TestTrustedGrantRequiredAndWriteRequiresGates(t *testing.T) {
	f, b := safeFlow()
	grants := safeContext(f)
	delete(grants.Grants, "review")
	if _, e := Validate(f, b, grants); e == nil {
		t.Fatal("missing controller grant accepted")
	}
	grants = safeContext(f)
	grants.Grants["review"] = StepGrant{AuthorityReadOnly, IsolationShared}
	if _, e := Validate(f, b, grants); e == nil {
		t.Fatal("missing independent reviewer accepted")
	}
}
