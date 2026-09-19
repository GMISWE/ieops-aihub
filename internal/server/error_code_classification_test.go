package server

// error_code_classification_test.go — aihub#720 slice C, the writeError wire
// half of the two new composition error codes.
//
// domain's own TestCodeToHTTPStatus holds the code→status TABLE
// (ErrComposeFailed→400, ErrConflictComposePending→409). What that test cannot
// see is the handler half: writeError serialises e.HTTPStatus as the actual
// response status and the code as the body's "code" key, which is the pair an
// MCP caller (and pf-doctor) reads off the wire. A regression that mapped the
// code to the right status in the table but re-typed it on the way out — or
// mapped COMPOSE_PENDING into the 400 family, which the conflict-style
// refusal would survive by message alone — would pass the domain table and
// change nothing a server client can observe. This file pins the wire pair.
//
// No database; writeError is pure:
//
//	GOWORK=off go test ./internal/server/ -run TestComposeErrorCodesClassifyOnTheWire -count=1 -v

import (
	"encoding/json"
	"net/http/httptest"
	"testing"

	"github.com/labstack/echo/v4"

	"github.com/GMISWE/ieops-aihub/internal/domain"
)

// TestComposeErrorCodesClassifyOnTheWire pins the HTTP classification of
// aihub#720's two new codes at the writeError boundary:
//
//   - COMPOSE_FAILED is a 400: request-content failure, the same family as the
//     shape guards — a composition request that cannot be satisfied, not a
//     state conflict.
//   - COMPOSE_PENDING is a 409: the claim itself was well-formed; the work
//     item's state (no pinned generation yet) is what refuses it.
//
// The 409 arm is the only handler-layer coverage COMPOSE_PENDING gets in this
// slice: the refusal itself needs a stored pending row (DB-gated,
// TestCreateWorkItemWorkflowMode_PendingIsStoredAndUnclaimable), so what this
// file holds is the classification half — that a claim refused with that code
// arrives on the wire as a conflict, not as a bad request.
func TestComposeErrorCodesClassifyOnTheWire(t *testing.T) {
	cases := []struct {
		code       domain.ErrCode
		wantStatus int
	}{
		{domain.ErrComposeFailed, 400},
		{domain.ErrConflictComposePending, 409},
	}
	for _, tc := range cases {
		t.Run(string(tc.code), func(t *testing.T) {
			e := echo.New()
			rec := httptest.NewRecorder()
			c := e.NewContext(httptest.NewRequest("GET", "/", nil), rec)

			if err := writeError(c, domain.NewErr(tc.code, "probe message")); err != nil {
				t.Fatalf("writeError returned an error: %v", err)
			}
			if rec.Code != tc.wantStatus {
				t.Errorf("status = %d, want %d", rec.Code, tc.wantStatus)
			}
			var wire map[string]any
			if err := json.Unmarshal(rec.Body.Bytes(), &wire); err != nil {
				t.Fatalf("body is not JSON: %q (%v)", rec.Body.String(), err)
			}
			if wire["code"] != string(tc.code) {
				t.Errorf("wire code = %#v, want %q", wire["code"], tc.code)
			}
			if wire["message"] != "probe message" {
				t.Errorf("wire message = %#v, want the caller-supplied message verbatim", wire["message"])
			}
		})
	}
}
