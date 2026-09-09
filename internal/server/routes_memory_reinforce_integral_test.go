package server

// aihub#459 — a non-integral strength_delta is the caller's error.
//
// WHAT THE RULING WAS
// -------------------
// memories.base_strength is SMALLINT while Go and the published schema both say
// `number`, so an in-range fractional value could never be stored as stated.
// aihub#475 measured what actually happened — pgx truncates toward zero,
// client-side, with no error — and made the RESPONSE honest about it, leaving
// the disposition open. The owner ruled on 2026-09-09: integers. Refuse, rather
// than round in Go (which answers 200 while storing a number the caller did not
// name) or widen the column (which changes what the Ebbinghaus recomputation in
// SQL multiplies).
//
// WHY THE DELTA AND NOT ONLY THE SUM
// ----------------------------------
// This handler never receives a base_strength; it receives an addend. Checking
// only the sum would be sufficient for storage and useless for the caller: a
// delta of 0.5 on a row stored at 3 is a well-formed sum of 3.5, and telling
// somebody "3.5 is not a whole number" points at a value they never typed.
// Refusing the delta names the argument they chose. It also makes the invariant
// total rather than incidental: a stored value is SMALLINT and the clamp's
// bounds are integers, so an integral delta guarantees an integral sum, and the
// truncation above stops being reachable through this endpoint at all.
//
// WHY A NIL POOL
// --------------
// The claim is not merely "a fractional delta is refused" but "it is refused
// BEFORE the first query", which is what makes the answer a fact about the
// caller's argument rather than about the memory they named. A nil pool proves
// it in both directions at once: reaching the DB panics, so a refusal that
// returns 400 cannot have touched it, and an ACCEPTED delta can only demonstrate
// that it got past the guard by dying on the pool. Mirrors
// routes_memory_methodology_test.go's strategy for the aihub#210 gates.

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/labstack/echo/v4"

	"github.com/GMISWE/ieops-aihub/internal/domain"
)

// reinforceNilPool sends one body at handleReinforceMemory with no database and
// reports which of the two outcomes happened: a non-nil panic means the request
// got past every pre-query guard, a recorder means it did not.
//
// The recover is load-bearing rather than tidy — an escaping panic kills the
// package test binary, which would report this file's failure as every other
// test in internal/server failing too.
func reinforceNilPool(t *testing.T, body string) (rec *httptest.ResponseRecorder, panicked any) {
	t.Helper()
	defer func() { panicked = recover() }()

	e := echo.New()
	req := httptest.NewRequest(http.MethodPatch, "/v1/memories/mem_x/reinforce", strings.NewReader(body))
	req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
	rec = httptest.NewRecorder()
	c := e.NewContext(req, rec)
	c.SetParamNames("id")
	c.SetParamValues("mem_x")
	setUser(c, methodologyWriterUser())
	if err := handleReinforceMemory(nil)(c); err != nil {
		e.HTTPErrorHandler(err, c)
	}
	return rec, nil
}

func TestReinforceRefusesFractionalStrengthDeltaBeforeThePool(t *testing.T) {
	for _, delta := range []string{"0.5", "-0.5", "2.5", "1.0000001", "0.1"} {
		t.Run(delta, func(t *testing.T) {
			rec, panicked := reinforceNilPool(t,
				`{"additional_context":"why","work_item_id":"wi_x","strength_delta":`+delta+`}`)
			if panicked != nil {
				t.Fatalf("strength_delta=%s reached the (nil) pool: nothing refuses a "+
					"fractional delta, so it is still added to the stored value and "+
					"truncated toward zero on the way back in", delta)
			}
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("strength_delta=%s: expected 400, got %d (body: %s)",
					delta, rec.Code, rec.Body.String())
			}

			// The message has to name the argument the caller sent. "400" alone
			// would be satisfied by the missing-additional_context rejection two
			// lines above the guard, which is a different fact entirely.
			var parsed struct {
				Message string `json:"message"`
				Code    string `json:"code"`
			}
			if err := json.Unmarshal(rec.Body.Bytes(), &parsed); err != nil {
				t.Fatalf("400 body is not the standard error envelope: %v (%s)",
					err, rec.Body.String())
			}
			if parsed.Code != string(domain.ErrBadRequest) {
				t.Errorf("expected code %s, got %q — a fractional delta is the CALLER's "+
					"error (aihub#411 T1-6)", domain.ErrBadRequest, parsed.Code)
			}
			if !strings.HasPrefix(parsed.Message, "strength_delta ") {
				t.Errorf("the rejection must OPEN by naming strength_delta, or a caller "+
					"cannot tell which of four arguments the server means; got %q",
					parsed.Message)
			}
			if !strings.Contains(parsed.Message, "whole number") {
				t.Errorf("the rejection must say why; got %q", parsed.Message)
			}
		})
	}
}

// TestReinforceAcceptsWholeAndAbsentStrengthDelta is the control, and without it
// the test above is satisfied by a handler that 400s every reinforce.
//
// Both arms reach the pool and therefore panic. That is the assertion: there is
// no way to observe "got past the guard" without a database except by watching
// it die on the absence of one.
func TestReinforceAcceptsWholeAndAbsentStrengthDelta(t *testing.T) {
	for _, body := range []string{
		`{"additional_context":"why","work_item_id":"wi_x","strength_delta":1}`,
		`{"additional_context":"why","work_item_id":"wi_x","strength_delta":-1}`,
		`{"additional_context":"why","work_item_id":"wi_x","strength_delta":0}`,
		`{"additional_context":"why","work_item_id":"wi_x","strength_delta":2}`,
		// Absent is the common case and a distinct one: the guard must not fire
		// on a nil pointer, which is the shape a "check it unconditionally"
		// refactor would break while every arm above stayed green.
		`{"additional_context":"why","work_item_id":"wi_x"}`,
	} {
		rec, panicked := reinforceNilPool(t, body)
		if panicked == nil {
			t.Errorf("%s carries a legal strength_delta and was answered %d (%s) instead of "+
				"proceeding to the pool — the guard is refusing deltas the column accepts",
				body, rec.Code, strings.TrimSpace(rec.Body.String()))
		}
	}
}
