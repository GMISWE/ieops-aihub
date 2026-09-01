package server

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/labstack/echo/v4"
)

func scopeGuardCtx() (echo.Context, *httptest.ResponseRecorder) {
	e := echo.New()
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	rec := httptest.NewRecorder()
	return e.NewContext(req, rec), rec
}

func TestCheckProjectAccess_ScopeGuard(t *testing.T) {
	scope := "projB"
	cases := []struct {
		name    string
		u       *UserContext
		project string
		wantErr bool
	}{
		{"in_scope_member", &UserContext{Role: "writer", ProjectScope: &scope, ProjectRoles: map[string]string{"projB": "writer"}}, "projB", false},
		{"out_of_scope_member", &UserContext{Role: "writer", ProjectScope: &scope, ProjectRoles: map[string]string{"projA": "writer"}}, "projA", true},
		{"out_of_scope_admin", &UserContext{Role: "admin", ProjectScope: &scope}, "projA", true},
		{"in_scope_admin", &UserContext{Role: "admin", ProjectScope: &scope}, "projB", false},
		{"unscoped_admin_any", &UserContext{Role: "admin"}, "projA", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c, _ := scopeGuardCtx()
			err := checkProjectAccess(c, tc.u, tc.project, "viewer")
			if tc.wantErr && err == nil {
				// Out-of-scope calls must be denied with a 403 (writeError sets
				// the HTTP status; checkProjectAccess's return value here just
				// signals the caller to stop before touching the DB).
				t.Errorf("expected denial, got nil")
			}
			if !tc.wantErr && err != nil {
				t.Errorf("expected allow, got %v", err)
			}
		})
	}
}

func TestUIScopeBlocks(t *testing.T) {
	scope := "projB"
	cases := []struct {
		name    string
		u       *UserContext
		project string
		want    bool
	}{
		{"nil_scope_never_blocks", &UserContext{Role: "admin"}, "projA", false},
		{"in_scope_not_blocked", &UserContext{Role: "admin", ProjectScope: &scope}, "projB", false},
		{"out_of_scope_blocked", &UserContext{Role: "admin", ProjectScope: &scope}, "projA", true},
		{"nil_user_never_blocks", nil, "projA", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := uiScopeBlocks(tc.u, tc.project); got != tc.want {
				t.Errorf("uiScopeBlocks() = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestCheckProjectAccessSoft_Scope(t *testing.T) {
	scope := "projB"
	cases := []struct {
		name    string
		u       *UserContext
		project string
		wantErr bool
	}{
		{"scoped_admin_denied_out_of_scope", &UserContext{Role: "admin", ProjectScope: &scope}, "projA", true},
		{"scoped_admin_allowed_in_scope", &UserContext{Role: "admin", ProjectScope: &scope}, "projB", false},
		{"unscoped_admin_allowed_anywhere", &UserContext{Role: "admin"}, "projA", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := checkProjectAccessSoft(tc.u, tc.project)
			if tc.wantErr && err == nil {
				t.Errorf("expected denial, got nil")
			}
			if !tc.wantErr && err != nil {
				t.Errorf("expected allow, got %v", err)
			}
		})
	}
}
