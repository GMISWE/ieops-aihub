package main

import (
	"net/http"
	"testing"

	"github.com/GMISWE/ieops-aihub/internal/render"
)

// TestApplyServerTimeouts is the guard for aihub#250's second half: the server
// must not go back to running with unbounded connection timeouts.
func TestApplyServerTimeouts(t *testing.T) {
	var s http.Server
	applyServerTimeouts(&s)

	for _, tc := range []struct {
		name string
		got  interface{ String() string }
	}{
		{"ReadHeaderTimeout", s.ReadHeaderTimeout},
		{"ReadTimeout", s.ReadTimeout},
		{"WriteTimeout", s.WriteTimeout},
		{"IdleTimeout", s.IdleTimeout},
	} {
		if tc.got.String() == "0s" {
			t.Errorf("%s is unset — an unbounded connection timeout is the defect aihub#250 fixed", tc.name)
		}
	}
}

// TestWriteTimeoutClearsDiagramBudget pins the interaction between the two
// halves of this fix. WriteTimeout bounds the whole response; the d2 budget
// bounds one figure, and a document renders several in turn (diagram_gate's
// narrowerLayout can compile a wide one twice). If someone later raises the
// compile budget past WriteTimeout, or lowers WriteTimeout onto it, legitimate
// renders start getting cut at the connection — the same user-visible failure
// this wi set out to prevent, arriving through the fix itself.
//
// The 2x factor is a floor, not a claim that two compiles is the worst case: it
// is the smallest ratio at which a single re-laid-out wide figure still fits.
//
// This checks the MAX rather than the default, because the max is what an
// operator can actually dial the budget up to via DIAGRAM_COMPILE_TIMEOUT. A
// test against the default would pass while a configured deployment violated the
// very relationship it claims to protect.
func TestWriteTimeoutClearsDiagramBudget(t *testing.T) {
	budget := render.MaxDiagramCompileTimeout

	if render.DefaultDiagramCompileTimeout > budget {
		t.Fatalf("default budget (%s) exceeds the max (%s)", render.DefaultDiagramCompileTimeout, budget)
	}
	if serverWriteTimeout <= budget {
		t.Fatalf("WriteTimeout (%s) must exceed the largest configurable d2 budget (%s)", serverWriteTimeout, budget)
	}
	if serverWriteTimeout < 2*budget {
		t.Errorf("WriteTimeout (%s) leaves no room for a re-laid-out wide figure (2 x %s)", serverWriteTimeout, budget)
	}
}
