package domain

import (
	"encoding/json"
	"testing"
)

// ─── projectNameRe ────────────────────────────────────────────────────────────

func TestProjectNameRe_Valid(t *testing.T) {
	valid := []string{
		"a",
		"abc",
		"my-project",
		"my_project",
		"proj123",
		"a0",
		"marketplace",
		"aihub",
		"ieops-v2",
		"a" + "bcdefghijklmnopqrstuvwxyz0123456789012", // 40 chars total (max)
	}
	for _, name := range valid {
		if !projectNameRe.MatchString(name) {
			t.Errorf("expected %q to be valid project name", name)
		}
	}
}

func TestProjectNameRe_Invalid(t *testing.T) {
	invalid := []string{
		"",
		"A",           // uppercase
		"0abc",        // starts with digit
		"-abc",        // starts with dash
		"_abc",        // starts with underscore
		"abc!",        // invalid char
		"ABC",         // uppercase
		"my project",  // space
		"my.project",  // dot
		// 41 chars (over limit of 40):
		"a" + "bcdefghijklmnopqrstuvwxyz012345678901234",
	}
	for _, name := range invalid {
		if projectNameRe.MatchString(name) {
			t.Errorf("expected %q to be invalid project name", name)
		}
	}
}

// ─── validateRepos ────────────────────────────────────────────────────────────

func TestValidateRepos_Empty(t *testing.T) {
	if aerr := validateRepos(nil); aerr != nil {
		t.Errorf("nil repos: got %v, want nil", aerr)
	}
	if aerr := validateRepos(json.RawMessage("[]")); aerr != nil {
		t.Errorf("empty array: got %v, want nil", aerr)
	}
}

func TestValidateRepos_Valid(t *testing.T) {
	repos := json.RawMessage(`[
		{"name":"aihub","url":"https://github.com/GMISWE/ieops-aihub"},
		{"name":"marketplace","url":"https://github.com/GMISWE/GMI-marketplace"}
	]`)
	if aerr := validateRepos(repos); aerr != nil {
		t.Errorf("valid repos: got %v, want nil", aerr)
	}
}

func TestValidateRepos_DuplicateName(t *testing.T) {
	repos := json.RawMessage(`[
		{"name":"aihub","url":"https://github.com/GMISWE/ieops-aihub"},
		{"name":"aihub","url":"https://github.com/GMISWE/other"}
	]`)
	aerr := validateRepos(repos)
	if aerr == nil {
		t.Fatal("expected error for duplicate repo name")
	}
	if aerr.Code != ErrRepoDuplicateName {
		t.Errorf("code = %q, want REPO_DUPLICATE_NAME", aerr.Code)
	}
}

func TestValidateRepos_DuplicateURL(t *testing.T) {
	repos := json.RawMessage(`[
		{"name":"aihub","url":"https://github.com/GMISWE/same"},
		{"name":"other","url":"https://github.com/GMISWE/same"}
	]`)
	aerr := validateRepos(repos)
	if aerr == nil {
		t.Fatal("expected error for duplicate repo URL")
	}
	if aerr.Code != ErrRepoDuplicateURL {
		t.Errorf("code = %q, want REPO_DUPLICATE_URL", aerr.Code)
	}
}

func TestValidateRepos_InvalidJSON(t *testing.T) {
	aerr := validateRepos(json.RawMessage(`not-json`))
	if aerr == nil {
		t.Fatal("expected error for invalid JSON")
	}
	if aerr.Code != ErrBadRequest {
		t.Errorf("code = %q, want BAD_REQUEST", aerr.Code)
	}
}

// ─── roleLevel ────────────────────────────────────────────────────────────────

func TestRoleLevel(t *testing.T) {
	tests := []struct {
		role string
		want int
	}{
		{"viewer", 1},
		{"writer", 2},
		{"owner", 3},
		{"", 0},
		{"unknown", 0},
	}
	for _, tt := range tests {
		if got := roleLevel(tt.role); got != tt.want {
			t.Errorf("roleLevel(%q) = %d, want %d", tt.role, got, tt.want)
		}
	}
}

// ─── Project struct JSON roundtrip ────────────────────────────────────────────

func TestProject_JSONRoundtrip(t *testing.T) {
	desc := "test project"
	prefix := "pi_ab12ef34"
	p := Project{
		Name:             "test-project",
		Description:      &desc,
		Visible:          true,
		IdentifierPrefix: &prefix,
		Repos:            json.RawMessage(`[{"name":"r","url":"u"}]`),
		Members:          json.RawMessage(`[{"user_id":"u_abc","role":"writer"}]`),
		WISeq:            42,
		Scenario:         "coding",
		OwnerUserID:      "u_owner",
	}

	b, err := json.Marshal(&p)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var got Project
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got.Name != p.Name {
		t.Errorf("Name: got %q, want %q", got.Name, p.Name)
	}
	if got.WISeq != p.WISeq {
		t.Errorf("WISeq: got %d, want %d", got.WISeq, p.WISeq)
	}
	if got.OwnerUserID != p.OwnerUserID {
		t.Errorf("OwnerUserID: got %q, want %q", got.OwnerUserID, p.OwnerUserID)
	}
	if got.IdentifierPrefix == nil || *got.IdentifierPrefix != prefix {
		t.Errorf("IdentifierPrefix: got %v, want %q", got.IdentifierPrefix, prefix)
	}
}

// ─── CreateProjectRequest JSON roundtrip ─────────────────────────────────────

func TestCreateProjectRequest_JSONRoundtrip(t *testing.T) {
	vis := true
	scen := "coding"
	req := CreateProjectRequest{
		Name:        "my-proj",
		Visible:     &vis,
		Scenario:    &scen,
		Repos:       json.RawMessage(`[]`),
	}
	b, err := json.Marshal(&req)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var got CreateProjectRequest
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got.Name != req.Name {
		t.Errorf("Name mismatch: %q vs %q", got.Name, req.Name)
	}
}

// ─── Error codes HTTP status ──────────────────────────────────────────────────

func TestProjectErrorCodes_HTTPStatus(t *testing.T) {
	tests := []struct {
		code ErrCode
		want int
	}{
		{ErrProjectNotFound, 404},
		{ErrProjectAlreadyExists, 409},
		{ErrProjectNameInvalid, 400},
		{ErrProjectAccessDenied, 403},
		{ErrProjectHasWorkItems, 400},
		{ErrRepoDuplicateName, 400},
		{ErrRepoDuplicateURL, 400},
		{ErrInvalidProjectIdentifier, 400},
	}
	for _, tt := range tests {
		got := codeToHTTPStatus(tt.code)
		if got != tt.want {
			t.Errorf("codeToHTTPStatus(%q) = %d, want %d", tt.code, got, tt.want)
		}
	}
}

// ─── joinStrings ──────────────────────────────────────────────────────────────

func TestJoinStrings(t *testing.T) {
	if got := joinStrings(nil, ", "); got != "" {
		t.Errorf("got %q, want empty", got)
	}
	if got := joinStrings([]string{"a"}, ", "); got != "a" {
		t.Errorf("got %q, want %q", got, "a")
	}
	if got := joinStrings([]string{"a", "b", "c"}, ", "); got != "a, b, c" {
		t.Errorf("got %q, want %q", got, "a, b, c")
	}
}
