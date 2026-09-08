package server

// aihub#463 — the behavioural half: POST /v1/admin/users must refuse an
// out-of-vocabulary user_type or role with a 400 that names the field and the
// legal values, BEFORE the INSERT.
//
// Before this change neither column was checked in Go. The value reached the
// INSERT, violated the CHECK in 0001_initial.sql (SQLSTATE 23514), and this
// handler answered `internalError(c, "failed to create user")` — which discards
// the pgx error, so the caller got:
//
//	500 INTERNAL_ERROR  failed to create user
//
// naming neither the field, nor the value, nor the legal set, and telling the
// caller to retry something that can never succeed.
//
// The nil pool is the instrument, as in router_list_wi_sort_test.go: any DB
// access panics, so reaching a 400 proves validation runs first. That is also
// why the accepting direction is not asserted here — a legal value would reach
// pool.QueryRow and panic; domain's own tests cover it without a database.
//
//	go test ./internal/server/ -run TestCreateUser_ -v

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

// createUserRequest drives handleCreateUser with the given JSON body.
func createUserRequest(t *testing.T, pool *pgxpool.Pool, body map[string]any) *httptest.ResponseRecorder {
	t.Helper()
	raw, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("marshal body: %v", err)
	}
	e := echo.New()
	req := httptest.NewRequest(http.MethodPost, "/v1/admin/users", strings.NewReader(string(raw)))
	req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)

	if err := handleCreateUser(pool)(c); err != nil {
		e.HTTPErrorHandler(err, c)
	}
	return rec
}

func TestCreateUser_IllegalVocabularyRejectedBeforeDB(t *testing.T) {
	for _, tc := range []struct {
		name    string
		body    map[string]any
		field   string
		got     string
		allowed []string
	}{
		{
			name:    "user_type",
			body:    map[string]any{"display_name": "Probe", "email": "probe@example.com", "user_type": "bot"},
			field:   "user_type",
			got:     "bot",
			allowed: domain.UserTypeList(),
		},
		{
			// `maintainer` is the realistic mistake rather than a nonsense
			// string: it is a legal PROJECT MEMBER role and an illegal global
			// one, so a caller conflating the two vocabularies sends exactly it.
			name:    "role",
			body:    map[string]any{"display_name": "Probe", "email": "probe@example.com", "role": "maintainer"},
			field:   "role",
			got:     "maintainer",
			allowed: domain.UserGlobalRoleList(),
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rec := createUserRequest(t, nil, tc.body)
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("expected 400, got %d (body: %s)", rec.Code, rec.Body.String())
			}
			body := rec.Body.String()
			// The field, the rejected value and every legal value must be IN the
			// answer — that is the entire difference from the 500 this replaces,
			// which carried none of the three.
			for _, want := range append([]string{tc.field, tc.got}, tc.allowed...) {
				if !strings.Contains(body, want) {
					t.Errorf("the 400 does not mention %q, so the caller cannot self-correct; got %s",
						want, body)
				}
			}
		})
	}
}

// TestCreateUser_IllegalUserTypeIsNotReportedAsAnEmailProblem pins the ORDER,
// which is not incidental.
//
// The email branch below the validators reads req.UserType: an illegal value
// falls through `if req.UserType == "machine"`, leaves email nil for a caller
// who supplied none, and would be answered "email is required for human users" —
// a 400 about the wrong field, which is worse than the 500 because it is
// plausible. Moving the validators below that branch reintroduces it silently.
func TestCreateUser_IllegalUserTypeIsNotReportedAsAnEmailProblem(t *testing.T) {
	rec := createUserRequest(t, nil, map[string]any{"display_name": "Probe", "user_type": "bot"})
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d (body: %s)", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if strings.Contains(body, "email is required") {
		t.Errorf("an illegal user_type was reported as a missing email: %s", body)
	}
	if !strings.Contains(body, "user_type") {
		t.Errorf("the 400 does not name user_type: %s", body)
	}
}

// TestCreateUser_DefaultsSurviveValidation is the anti-vacuity arm: both fields
// are OPTIONAL, so a validator placed before the defaults — or one that treats
// "" as illegal — would reject every call that omits them, which is most of
// them. It must get past validation and reach the DB, which the nil pool then
// panics on; recovering that panic is the assertion that validation passed.
func TestCreateUser_DefaultsSurviveValidation(t *testing.T) {
	defer func() {
		if r := recover(); r == nil {
			t.Errorf("expected the nil pool to panic at the INSERT, which is how this test " +
				"observes that a request omitting user_type and role got PAST validation")
		}
	}()
	rec := createUserRequest(t, nil, map[string]any{"display_name": "Probe", "email": "probe@example.com"})
	t.Fatalf("handleCreateUser returned %d without touching the pool (body: %s) — the request "+
		"was refused before the INSERT, so the defaults human/writer are being rejected",
		rec.Code, rec.Body.String())
}
