package server

// aihub#454: the request-path half of the "attrs_patch has no length cap" claim.
//
// jsonObjectParamErr (internal/domain/work_items.go) ships that sentence to
// callers, and it is a statement about the whole path, not about one function.
// The only thing pinning it was TestValidateAttrsPatch_NoSizeCap, which calls
// validateAttrsPatch and nothing else — so a cap introduced as transport
// middleware would have made the shipped message false with every test green.
// That is the same defect this wi's other half found in aihub#428's minting-point
// scan: a guarded surface narrower than the self-description.
//
// What this file adds is the layer that test could not reach from inside
// internal/domain: a 200KB attrs_patch through the REAL router, asserting the
// answer is the ordinary refusal and not a size one.
//
// ⚠️ It stops at authentication, and that boundary is the honest description of
// its reach rather than an accident. BearerAuth answers 401 before it touches
// the pool (middleware.go), which is what makes the arm runnable with a nil pool
// and no database — but it also means the request never reaches the handler. So
// this covers caps that fire ON THEIR OWN, ahead of auth: echo's
// middleware.BodyLimit, a Content-Length gate, a read-and-reject wrapper. A cap
// that only manifests further down — inside the handler, in the MCP tool layer,
// or as a CHECK constraint on the column — is invisible here, and those holes
// are recorded in TestValidateAttrsPatch_NoSizeCap's comment rather than papered
// over by a test that appears to cover them.

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/labstack/echo/v4"
)

// attrsPatchMeasuredMax is the largest attrs_patch body measured accepted
// end-to-end (2026-09-07: 199,983 bytes, HTTP 200). Probing at the measured
// ceiling rather than at some round number keeps the arm anchored to evidence.
const attrsPatchMeasuredMax = 200 << 10

// bodyCapProbe sends one PATCH /v1/work_items/:id carrying an attrs_patch object
// of roughly `payload` bytes and reports what came back.
//
// It returns the status AND the error code because either alone is too coarse: a
// cap that answers 400 shares its status with a dozen ordinary refusals, and a
// cap that answers 401 by accident would slip past a status-only check.
func bodyCapProbe(t *testing.T, e *echo.Echo, payload int) (int, string) {
	t.Helper()
	body, err := json.Marshal(map[string]any{
		"attrs_patch": map[string]string{"k": strings.Repeat("x", payload)},
	})
	if err != nil {
		t.Fatalf("building the fixture failed: %v", err)
	}
	req := httptest.NewRequest(http.MethodPatch, "/v1/work_items/wi_nosuchitem", bytes.NewReader(body))
	req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	var decoded map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &decoded)
	code, _ := decoded["code"].(string)
	return rec.Code, code
}

// TestNoRequestBodySizeCapAheadOfAuth is the arm itself: a 200KB attrs_patch and
// a 32-byte one must get the SAME answer out of the real router.
//
// Comparing the two rather than asserting a fixed status is deliberate. "Not 413"
// would pass a cap that answers 400, and "exactly 401 UNAUTHORIZED" would need
// updating every time the auth refusal is reworded, which is how a gate ends up
// being edited to green rather than read. Size is the only difference between the
// two requests, so any difference in the answer is the size being noticed.
func TestNoRequestBodySizeCapAheadOfAuth(t *testing.T) {
	e := NewRouter(nil, []byte("attrs-patch-size-cap-test-cookie-secret"))

	smallStatus, smallCode := bodyCapProbe(t, e, 32)
	bigStatus, bigCode := bodyCapProbe(t, e, attrsPatchMeasuredMax)

	// The control has to be the ordinary refusal, or the comparison below is
	// comparing two failures and would pass on a router that rejects everything.
	if smallStatus != http.StatusUnauthorized {
		t.Fatalf("the 32-byte control answered %d %q, want 401 — this arm compares a big request "+
			"against a small one, so a control that is not the ordinary auth refusal makes every "+
			"comparison below meaningless", smallStatus, smallCode)
	}

	if bigStatus != smallStatus || bigCode != smallCode {
		t.Errorf(`a %d-byte attrs_patch answered %d %q where a 32-byte one answered %d %q.
Size is the ONLY difference between the two requests, so something in the transport is now
measuring the body. If that cap was added on purpose, jsonObjectParamErr's shipped
"attrs_patch has no length cap" (internal/domain/work_items.go) is now false and must change in
the same commit — the measurement it rests on is 199,983 bytes accepted end-to-end, 2026-09-07.
(aihub#420, arm added by aihub#454)`,
			attrsPatchMeasuredMax, bigStatus, bigCode, smallStatus, smallCode)
	}
}

// contentLengthCap is echo middleware.BodyLimit's shape: refuse on the declared
// length, before anything reads the body.
//
// It is hand-rolled rather than imported so that a test mutant does not drag
// echo's middleware package — and its transitive dependencies — into go.mod for
// the sake of six lines. The behaviour under test is the 413 on Content-Length,
// which is what BodyLimit does first.
func contentLengthCap(limit int64) echo.MiddlewareFunc {
	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error {
			if c.Request().ContentLength > limit {
				return echo.NewHTTPError(http.StatusRequestEntityTooLarge, "request body too large")
			}
			return next(c)
		}
	}
}

// readAndRejectCap is the other realistic shape: read the body, then refuse on
// its actual size with an ordinary 400. It answers the same status as a dozen
// legitimate refusals, which is exactly why the probe compares an error CODE
// too rather than a status alone.
func readAndRejectCap(limit int) echo.MiddlewareFunc {
	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error {
			raw, err := io.ReadAll(c.Request().Body)
			if err != nil {
				return err
			}
			if len(raw) > limit {
				return c.JSON(http.StatusBadRequest, map[string]any{
					"code": "BAD_REQUEST", "message": "attrs_patch is too long",
				})
			}
			c.Request().Body = io.NopCloser(bytes.NewReader(raw))
			return next(c)
		}
	}
}

// TestBodyCapProbeCanSeeACap is the discriminating-power proof, and without it
// the arm above is a comparison that has never been observed to differ.
//
// Each mutant installs a cap the arm is supposed to catch and asserts the probe
// reports a DIFFERENT answer for the big body than for the small one. A probe
// that had gone blind — wrong route, wrong header, a body echo never reads —
// would report "same" here and pass the real router forever.
func TestBodyCapProbeCanSeeACap(t *testing.T) {
	cases := []struct {
		name string
		mw   echo.MiddlewareFunc
	}{
		{"content-length cap (echo middleware.BodyLimit's shape)", contentLengthCap(64 << 10)},
		{"read-and-reject cap answering an ordinary 400", readAndRejectCap(64 << 10)},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e := NewRouter(nil, []byte("attrs-patch-size-cap-test-cookie-secret"))
			// echo composes e.Use middleware at request time, so installing it
			// after the routes are registered still puts it ahead of them —
			// which is where a body cap would go.
			e.Use(tc.mw)

			smallStatus, smallCode := bodyCapProbe(t, e, 32)
			bigStatus, bigCode := bodyCapProbe(t, e, attrsPatchMeasuredMax)

			if smallStatus != http.StatusUnauthorized {
				t.Fatalf("the mutant changed the SMALL request's answer too (%d %q) — it is capping "+
					"more than size, so it does not model the thing being detected",
					smallStatus, smallCode)
			}
			if bigStatus == smallStatus && bigCode == smallCode {
				t.Errorf("the probe cannot see this cap: %d-byte body answered %d %q, same as the "+
					"32-byte control. TestNoRequestBodySizeCapAheadOfAuth is therefore a comparison "+
					"that cannot fail, and it would stay green through the very change it exists to "+
					"catch", attrsPatchMeasuredMax, bigStatus, bigCode)
			}
		})
	}
}
