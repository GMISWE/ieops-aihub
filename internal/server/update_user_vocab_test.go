package server

// aihub#496 — the behavioural half: PATCH /v1/admin/users/:id must refuse an
// out-of-vocabulary role with a 400 that NAMES THE FIELD, the rejected value and
// the legal set, from the same vocabulary handleCreateUser is judged against.
//
// The nil pool is the instrument, as in create_user_vocab_test.go: any DB access
// panics, so reaching a 400 proves validation runs before the UPDATE, and
// reaching a panic proves a legal value got PAST validation.
//
//	go test ./internal/server/ -run TestUpdateUser_ -v

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/labstack/echo/v4"

	"github.com/GMISWE/ieops-aihub/internal/domain"
)

// updateUserRequest drives handleUpdateUser with the given JSON body.
func updateUserRequest(t *testing.T, pool *pgxpool.Pool, body map[string]any) *httptest.ResponseRecorder {
	t.Helper()
	raw, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("marshal body: %v", err)
	}
	e := echo.New()
	req := httptest.NewRequest(http.MethodPatch, "/v1/admin/users/u_probe", strings.NewReader(string(raw)))
	req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	c.SetParamNames("id")
	c.SetParamValues("u_probe")

	if err := handleUpdateUser(pool)(c); err != nil {
		e.HTTPErrorHandler(err, c)
	}
	return rec
}

// TestUpdateUser_IllegalRoleNamesTheField is the discriminating arm.
//
// 🔴 Read the assertion carefully: it is on the STRUCTURED `details`, not on a
// substring of the message. The check this replaces answered
// `400 "role must be writer or admin"` — a message that already contains the
// words "role", "writer" and "admin", so every substring assertion an obvious
// test would make passes on the unfixed code. What it never carried is a
// `details` object, so the rejected VALUE was absent and no caller could parse
// the field name out of prose. `details.field` is the property that actually
// moves.
func TestUpdateUser_IllegalRoleNamesTheField(t *testing.T) {
	// `maintainer` is the realistic mistake rather than a nonsense string: it is
	// a legal PROJECT MEMBER role and an illegal global one, so a caller
	// conflating the two vocabularies sends exactly it.
	rec := updateUserRequest(t, nil, map[string]any{"role": "maintainer"})
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d (body: %s)", rec.Code, rec.Body.String())
	}

	var got struct {
		Code    string `json:"code"`
		Message string `json:"message"`
		Details struct {
			Field   string   `json:"field"`
			Got     string   `json:"got"`
			Allowed []string `json:"allowed"`
		} `json:"details"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode error body %s: %v", rec.Body.String(), err)
	}
	if got.Details.Field != "role" {
		t.Errorf("details.field = %q, want \"role\" — the 400 does not name the field it refused, "+
			"which is the whole difference from the message-only check this replaces (body: %s)",
			got.Details.Field, rec.Body.String())
	}
	if got.Details.Got != "maintainer" {
		t.Errorf("details.got = %q, want \"maintainer\" — the answer does not echo the rejected "+
			"value (body: %s)", got.Details.Got, rec.Body.String())
	}
	want := domain.UserGlobalRoleList()
	if strings.Join(got.Details.Allowed, ",") != strings.Join(want, ",") {
		t.Errorf("details.allowed = %v, want %v — the legal set must come from the package that "+
			"enforces it, not from a copy typed into the handler", got.Details.Allowed, want)
	}
}

// TestUpdateUser_LegalRolesReachTheDB is the anti-vacuity arm: a validator that
// refused everything would satisfy the arm above. Every value the domain calls
// legal must get PAST validation and reach the UPDATE, which the nil pool then
// panics on; recovering that panic IS the assertion.
func TestUpdateUser_LegalRolesReachTheDB(t *testing.T) {
	for _, role := range domain.UserGlobalRoleList() {
		t.Run(role, func(t *testing.T) {
			defer func() {
				if r := recover(); r == nil {
					t.Errorf("expected the nil pool to panic at the UPDATE, which is how this test "+
						"observes that role %q got past validation", role)
				}
			}()
			rec := updateUserRequest(t, nil, map[string]any{"role": role})
			t.Fatalf("handleUpdateUser returned %d without touching the pool (body: %s) — the legal "+
				"role %q was refused before the UPDATE", rec.Code, rec.Body.String(), role)
		})
	}
}

// TestUpdateUser_OmittedRoleIsNotValidated pins that absent still means absent.
// `role` is optional and the handler binds it as *string; a validator moved
// outside the nil check would reject every call that only renames a user, and
// "" is not a legal role so it would do so with this very message.
func TestUpdateUser_OmittedRoleIsNotValidated(t *testing.T) {
	defer func() {
		if r := recover(); r == nil {
			t.Errorf("expected the nil pool to panic at the UPDATE — a request that omits role " +
				"entirely is being validated as though it had sent \"\"")
		}
	}()
	rec := updateUserRequest(t, nil, map[string]any{"display_name": "Renamed"})
	t.Fatalf("handleUpdateUser returned %d without touching the pool (body: %s) — a display-name-only "+
		"update was refused", rec.Code, rec.Body.String())
}
