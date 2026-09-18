package workflow

import "testing"

func TestOrdinaryEventOrderCannotBorrowEarlierGatesForLaterWriter(t *testing.T) {
	base, bindings := safeFlow()
	flow := Flow{Version: base.Version}
	ctx := ExecutionContext{Grants: map[string]StepGrant{}}
	for _, suffix := range []string{"A", "B"} {
		for _, original := range base.Steps[:3] {
			s := original
			s.ID += suffix
			s.Inputs = append([]InputRef(nil), original.Inputs...)
			for i := range s.Inputs {
				s.Inputs[i].StepID += suffix
			}
			flow.Steps = append(flow.Steps, s)
			ctx.Grants[s.ID] = safeContext(base).Grants[original.ID]
		}
	}
	ship := base.Steps[3]
	ship.Inputs = append([]InputRef(nil), ship.Inputs...)
	for i := range ship.Inputs {
		ship.Inputs[i].StepID += "B"
	}
	flow.Steps = append(flow.Steps, ship)
	ctx.Grants["ship"] = safeContext(base).Grants["ship"]
	validated, err := Validate(flow, bindings, ctx)
	if err != nil {
		t.Fatal(err)
	}
	makeResult := func(kind, suffix, producer string) StepResult {
		r := resultFor(kind, 1, producer)
		r.StepID += suffix
		r.StepAttemptID += suffix
		return r
	}
	good := []StepResult{
		makeResult("write", "A", "writerA"), makeResult("review", "A", "reviewerA"), makeResult("verify", "A", "verifierA"),
		makeResult("write", "B", "writerB"), makeResult("review", "B", "reviewerB"), makeResult("verify", "B", "verifierB"), resultFor("ship", 1, "shipper"),
	}
	if d, err := Decide(validated, good, policy(good)); err != nil || d != DecisionAdvance {
		t.Fatalf("ordered control: %s %v", d, err)
	}
	bad := []StepResult{good[0], good[4], good[5], good[3], good[1], good[2], good[6]}
	bad[4].ProducerID = "writerB"
	bad[5].ProducerID = "writerB"
	if _, err := Decide(validated, bad, policy(bad)); err == nil {
		t.Fatal("out-of-order self-review borrowing accepted")
	}
}
