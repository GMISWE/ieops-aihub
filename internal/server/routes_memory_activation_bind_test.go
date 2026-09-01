package server

// aihub#236 code-review round 1, finding 3.
//
// RememberRequest's activation trio is tagged `json:"-"` so a client cannot pin
// a memory to the top of recall by POSTing activation_count=9999. That tag alone
// is NOT a sufficient guard: echo's DefaultBinder dispatches application/xml and
// text/xml bodies to encoding/xml, which does not read json tags and falls back
// to the exported Go field name. handleRemember therefore zeroes the three
// fields immediately after c.Bind.
//
// These tests exercise the binder against the real domain.RememberRequest — no
// database required, so unlike the DB-gated ranking tests they actually run in
// CI. They deliberately assert at the binding seam rather than through the full
// HTTP handler, because the handler needs a live pool; the seam is where the
// defect was and where a regression would reappear.

import (
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/GMISWE/ieops-aihub/internal/domain"
	"github.com/labstack/echo/v4"
)

func mustReadSource(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile(name)
	if err != nil {
		t.Fatalf("read %s: %v", name, err)
	}
	return string(b)
}

// bindBody runs echo's binder over a body with the given content type and
// returns the populated request struct, mirroring handleRemember's first step.
func bindBody(t *testing.T, contentType, body string) domain.RememberRequest {
	t.Helper()
	e := echo.New()
	r := httptest.NewRequest(http.MethodPost, "/v1/memories", strings.NewReader(body))
	r.Header.Set(echo.HeaderContentType, contentType)
	c := e.NewContext(r, httptest.NewRecorder())

	var req domain.RememberRequest
	if err := c.Bind(&req); err != nil {
		t.Fatalf("bind (%s): %v", contentType, err)
	}
	return req
}

// sanitizeRememberBinding applies the same clearing handleRemember does after
// c.Bind. Kept as a named helper so the assertion below documents exactly which
// production step it is standing in for.
func sanitizeRememberBinding(req *domain.RememberRequest) {
	req.LastActivatedAt = nil
	req.LastActivatedBy = nil
	req.ActivationCount = 0
}

func assertNoActivationState(t *testing.T, label string, req domain.RememberRequest) {
	t.Helper()
	if req.ActivationCount != 0 {
		t.Errorf("%s: ActivationCount = %d, want 0 — a client can inflate stability_days "+
			"and make the memory immune to decay and to the GC sweep", label, req.ActivationCount)
	}
	if req.LastActivatedAt != nil {
		t.Errorf("%s: LastActivatedAt = %v, want nil — a client can pin a memory to the "+
			"top of every recall in the project", label, req.LastActivatedAt)
	}
	if req.LastActivatedBy != nil {
		t.Errorf("%s: LastActivatedBy = %v, want nil", label, *req.LastActivatedBy)
	}
}

const activationInjectionJSON = `{
	"project": "p", "type": "fact.note", "content": "x",
	"activation_count": 9999,
	"last_activated_at": "2030-01-01T00:00:00Z",
	"last_activated_by": "u_attacker",
	"ActivationCount": 4242,
	"LastActivatedBy": "u_attacker"
}`

const activationInjectionXML = `<RememberRequest>
	<Project>p</Project><Type>fact.note</Type><Content>x</Content>
	<ActivationCount>9999</ActivationCount>
	<LastActivatedBy>u_attacker</LastActivatedBy>
</RememberRequest>`

// JSON was already safe via the struct tags; this pins that it stays safe.
func TestRememberBinding_JSONCannotSetActivationState(t *testing.T) {
	req := bindBody(t, echo.MIMEApplicationJSON, activationInjectionJSON)
	if req.Project != "p" || req.Type != "fact.note" {
		t.Fatalf("ordinary fields must still bind, else the test is vacuous: "+
			"project=%q type=%q", req.Project, req.Type)
	}
	assertNoActivationState(t, "json body, raw bind", req)
}

// THE ACTUAL HOLE. encoding/xml ignores `json:"-"`, so the raw bind DOES
// populate the trio. Verify that first (so the test proves the threat is real
// rather than assuming it away), then verify handleRemember's clearing step
// closes it.
func TestRememberBinding_XMLInjectionIsClearedBeforeUse(t *testing.T) {
	req := bindBody(t, echo.MIMEApplicationXML, activationInjectionXML)
	if req.Project != "p" {
		t.Fatalf("ordinary fields must still bind, else the test is vacuous: project=%q", req.Project)
	}
	if req.ActivationCount != 9999 {
		t.Skipf("echo no longer binds XML into json:\"-\" fields (got ActivationCount=%d); "+
			"the clearing step is now belt-and-braces rather than load-bearing",
			req.ActivationCount)
	}

	sanitizeRememberBinding(&req)
	assertNoActivationState(t, "xml body, after handleRemember's clearing step", req)
}

// text/xml is a second dispatch branch in echo's binder; cover it too.
func TestRememberBinding_TextXMLInjectionIsClearedBeforeUse(t *testing.T) {
	req := bindBody(t, echo.MIMETextXML, activationInjectionXML)
	sanitizeRememberBinding(&req)
	assertNoActivationState(t, "text/xml body, after handleRemember's clearing step", req)
}

// Guard against the fix rotting: if someone deletes the three assignments in
// handleRemember, this catches it by scanning the handler source for them.
func TestHandleRemember_StillClearsActivationStateAfterBind(t *testing.T) {
	src := mustReadSource(t, "routes_memory.go")
	for _, want := range []string{
		"req.LastActivatedAt = nil",
		"req.LastActivatedBy = nil",
		"req.ActivationCount = 0",
	} {
		if !strings.Contains(src, want) {
			t.Errorf("routes_memory.go no longer contains %q — handleRemember must clear "+
				"activation state after c.Bind, because json:\"-\" does not stop echo's "+
				"XML binder (aihub#236)", want)
		}
	}
}
