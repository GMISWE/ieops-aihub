package server

// create_work_item_workflow_test.go — aihub#720 slice C, the handler half of
// the create-time composition-mode boundary.
//
// The RULES live in domain (resolveCreateWorkflowMode;
// create_work_item_workflow_mode_test.go holds the combination table) and the
// DB-gated half (what the create PERSISTS, what the claim refuses) lives in
// create_work_item_workflow_mode_db_test.go. What neither of those can say is
// what the ROUTE does with the refusal: that c.Bind accepts the field, that
// writeError serialises it with the 400 the code maps to, and that the wire
// body carries the code and the machine-readable reason a composer branches
// on. That is this file.
//
// All arms run with a NIL pool. For the refusal arms that is one constraint
// over TestCreateWorkItem_ViewerGets403BeforeDBWrite's technique; for the
// accepted-value arm it IS the assertion, because the mode guard sits before
// pool.Begin by design (a contradictory request must not first spend an
// embedding or a wi_seq on its way to the refusal — the aihub#396 reasoning
// the domain file states): an accepted mode reaches the nil pool and panics,
// a refused one answers 400, and a guard that had drifted below the
// transaction would show up here as a panic on the REFUSAL arms.
//
// No database:
//
//	GOWORK=off go test ./internal/server/ -run 'TestCreateWorkItemRoute_.*Compose|TestCreateWorkItemRoute_Accepted' -count=1 -v

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/labstack/echo/v4"
)

// composeAdminUser is a caller checkProjectAccess passes without a database
// lookup (u.Role == "admin" short-circuits in middleware.go), so the create
// path proceeds to the domain guards — which is where these arms aim.
func composeAdminUser() *UserContext {
	return &UserContext{
		UserID:      "u_admin",
		Email:       "admin@test.example",
		DisplayName: "Admin User",
		UserType:    "human",
		Role:        "admin",
		ProjectRoles: map[string]string{
			"testproject": "maintainer",
		},
		APIKeyID: "k_admin",
	}
}

// composeWireReply is one driven request's outcome: the status the handler
// wrote, the decoded JSON envelope, and whether the nil pool was reached
// (the recovered panic — see the file header for why that is the pass
// condition of the accepted-value arms, not a failure).
type composeWireReply struct {
	status      int
	envelope    map[string]any
	reachedPool bool
}

// postWorkItemsCompose drives the real handler with a body over a NIL pool.
// The result is NAMED so the deferred recover can set reachedPool after the
// return value is chosen — the panic happens inside handler(c), and without
// the named result the recover would mutate a local the caller never sees.
func postWorkItemsCompose(t *testing.T, body string) (reply composeWireReply) {
	t.Helper()
	e := echo.New()
	req := httptest.NewRequest(http.MethodPost, "/v1/work_items", strings.NewReader(body))
	req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
	rec := httptest.NewRecorder()

	c := e.NewContext(req, rec)
	setUser(c, composeAdminUser())

	// The nil pool's Begin panics; that panic is the accepted-value arms'
	// signal that the guard let the request through, so it is recovered and
	// reported rather than allowed to fail the run.
	defer func() {
		if r := recover(); r != nil {
			reply.reachedPool = true
		}
	}()
	handler := handleCreateWorkItem(nil)
	if err := handler(c); err != nil {
		e.HTTPErrorHandler(err, c)
	}

	reply.status = rec.Code
	if rec.Body.Len() > 0 {
		if err := json.Unmarshal(rec.Body.Bytes(), &reply.envelope); err != nil {
			t.Fatalf("response body is not JSON: %q (%v)", rec.Body.String(), err)
		}
	}
	return
}

// composeWireReason reads details.reason out of a decoded error envelope.
func composeWireReason(t *testing.T, wire map[string]any) string {
	t.Helper()
	details, ok := wire["details"].(map[string]any)
	if !ok {
		t.Fatalf("no details object in the envelope: %#v", wire)
	}
	reason, _ := details["reason"].(string)
	return reason
}

// composeWireJSON re-encodes for failure messages only.
func composeWireJSON(t *testing.T, v map[string]any) string {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		return "(unencodable)"
	}
	return string(b)
}

// TestCreateWorkItemRoute_RejectsIllegalWorkflowModeAs400ComposeFailed is the
// illegal-value arm: a workflow_mode outside the enum must answer 400
// COMPOSE_FAILED with reason "invalid_workflow_mode" — not a 500 from the
// migration's CHECK constraint two transactions later, and not a silent
// legacy fall-back (the exact defect class the explicit column closes).
func TestCreateWorkItemRoute_RejectsIllegalWorkflowModeAs400ComposeFailed(t *testing.T) {
	reply := postWorkItemsCompose(t,
		`{"project":"testproject","goal":"probe the mode vocabulary","workflow_mode":"yaml"}`)

	if reply.reachedPool {
		t.Fatalf("an illegal workflow_mode reached the transaction — the guard must sit before pool.Begin")
	}
	if reply.status != http.StatusBadRequest {
		t.Errorf("status = %d, want 400; body: %s", reply.status, composeWireJSON(t, reply.envelope))
	}
	if reply.envelope["code"] != "COMPOSE_FAILED" {
		t.Errorf("wire code = %#v, want COMPOSE_FAILED", reply.envelope["code"])
	}
	if got := composeWireReason(t, reply.envelope); got != "invalid_workflow_mode" {
		t.Errorf("details.reason = %q, want invalid_workflow_mode", got)
	}
}

// TestCreateWorkItemRoute_ModeContradictionsAnswer400ComposeFailed walks the
// contradictory half of the combination table through the real Bind →
// handler → writeError path, one refusal reason per arm. The pure table itself
// is TestResolveCreateWorkflowMode (domain); this pins that each reason
// survives the route as 400 + code + details.reason — the three fields a
// composer branches on.
func TestCreateWorkItemRoute_ModeContradictionsAnswer400ComposeFailed(t *testing.T) {
	const steps = `"steps":[{"id":"s1","skill_id":"grill-me","skill_version":1,"models":[]}]`
	cases := []struct {
		name string
		body string
		// wantReason is the machine-readable reason resolveCreateWorkflowMode
		// returns for this combination — asserted so a rewording of the message
		// cannot silently break a composer branching on the reason.
		wantReason string
	}{
		{
			name:       "legacy mode with steps",
			body:       `{"project":"testproject","goal":"probe","workflow_mode":"legacy","requires_human_session":false,` + steps + `}`,
			wantReason: "steps_with_legacy_mode",
		},
		{
			name:       "pending mode with steps",
			body:       `{"project":"testproject","goal":"probe","workflow_mode":"pending","requires_human_session":false,` + steps + `}`,
			wantReason: "steps_with_pending_mode",
		},
		{
			name:       "db mode without steps",
			body:       `{"project":"testproject","goal":"probe","workflow_mode":"db"}`,
			wantReason: "db_mode_requires_steps",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			reply := postWorkItemsCompose(t, tc.body)
			if reply.reachedPool {
				t.Fatalf("a contradictory combination reached the transaction — the guard must sit before pool.Begin")
			}
			if reply.status != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400; body: %s", reply.status, composeWireJSON(t, reply.envelope))
			}
			if reply.envelope["code"] != "COMPOSE_FAILED" {
				t.Fatalf("wire code = %#v, want COMPOSE_FAILED", reply.envelope["code"])
			}
			if got := composeWireReason(t, reply.envelope); got != tc.wantReason {
				t.Errorf("details.reason = %q, want %q", got, tc.wantReason)
			}
		})
	}
}

// TestCreateWorkItemRoute_AcceptedModeValuesReachTheTransaction pins the other
// side of the boundary: 'legacy' and 'pending' (without steps) are ACCEPTED by
// the mode guard, so the request proceeds to the transaction. Over a nil pool
// "proceeds" is observable only as the recovered panic — the refusal arms
// above prove nothing panics when the guard refuses, so here a panic is
// unambiguous: it means the request got PAST every pure guard.
func TestCreateWorkItemRoute_AcceptedModeValuesReachTheTransaction(t *testing.T) {
	for _, mode := range []string{"legacy", "pending"} {
		t.Run(mode, func(t *testing.T) {
			reply := postWorkItemsCompose(t,
				`{"project":"testproject","goal":"probe accepted mode values","workflow_mode":"`+mode+`"}`)
			if !reply.reachedPool {
				t.Errorf("mode %q was stopped before the transaction (status %d, body %s) — "+
					"an accepted mode must pass the guard",
					mode, reply.status, composeWireJSON(t, reply.envelope))
			}
		})
	}
}
