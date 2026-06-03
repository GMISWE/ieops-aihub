package server

// Unit tests for POST /v1/memories/:id/commit/:commit_id/reply.
//
// Strategy: mirror handleResolveCommit tests — override commitMemoryProjectFn
// to avoid DB hits and verify the handler dispatches to domain.ReplyCommit with
// the right arguments, returns {ok:true} on success, and rejects empty body.

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/labstack/echo/v4"
)

// newV1ReplyRequest builds a Bearer-authed JSON POST for
// POST /v1/memories/:id/commit/:commit_id/reply.
func newV1ReplyRequest(t *testing.T, memID, commitID string, body map[string]any, uc *UserContext) (echo.Context, *httptest.ResponseRecorder) {
	t.Helper()
	e := echo.New()
	raw, _ := json.Marshal(body)
	req := httptest.NewRequest(http.MethodPost,
		"/v1/memories/"+memID+"/commit/"+commitID+"/reply",
		bytes.NewReader(raw))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	c.SetParamNames("id", "commit_id")
	c.SetParamValues(memID, commitID)
	if uc != nil {
		setUser(c, uc)
	}
	return c, rec
}

// TestV1ReplyCommit_Success verifies happy path: 200 {ok:true}, domain called with right args.
func TestV1ReplyCommit_Success(t *testing.T) {
	cleanupProject := withCommitMemoryProjectOverride("testproject", "active", nil)
	defer cleanupProject()

	var calledMemID, calledCommitID, calledBody string
	prev := doReplyCommitFn
	defer func() { doReplyCommitFn = prev }()
	doReplyCommitFn = func(_ context.Context, _ *pgxpool.Pool, memID, commitID, _, _, body string) error {
		calledMemID = memID
		calledCommitID = commitID
		calledBody = body
		return nil
	}

	c, rec := newV1ReplyRequest(t, "mem_v1", "cm_v1", map[string]any{"body": "hello from v1"}, writerUser("testproject"))
	if err := handleV1ReplyCommit(nil)(c); err != nil {
		t.Fatalf("handler error: %v", err)
	}
	if rec.Code != http.StatusOK {
		t.Errorf("status: got %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	var resp map[string]bool
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("response not JSON: %v; body=%s", err, rec.Body.String())
	}
	if !resp["ok"] {
		t.Errorf("response ok: got false, want true")
	}
	if calledMemID != "mem_v1" || calledCommitID != "cm_v1" || calledBody != "hello from v1" {
		t.Errorf("domain args: memID=%q commitID=%q body=%q", calledMemID, calledCommitID, calledBody)
	}
}

// TestV1ReplyCommit_EmptyBody verifies that an empty body returns 400 without calling domain.
func TestV1ReplyCommit_EmptyBody(t *testing.T) {
	called := false
	prev := doReplyCommitFn
	defer func() { doReplyCommitFn = prev }()
	doReplyCommitFn = func(_ context.Context, _ *pgxpool.Pool, _, _, _, _, _ string) error {
		called = true
		return nil
	}

	c, rec := newV1ReplyRequest(t, "mem_v1", "cm_v1", map[string]any{"body": ""}, writerUser("testproject"))
	if err := handleV1ReplyCommit(nil)(c); err != nil {
		// Error return also signals 4xx.
		if called {
			t.Errorf("domain must not be called for empty body")
		}
		return
	}
	if rec.Code != http.StatusBadRequest {
		t.Errorf("status: got %d, want 400 for empty body", rec.Code)
	}
	if called {
		t.Errorf("domain must not be called for empty body")
	}
}

// TestV1ReplyCommit_NonWriter verifies that a caller without writer access is rejected.
func TestV1ReplyCommit_NonWriter(t *testing.T) {
	cleanupProject := withCommitMemoryProjectOverride("otherproject", "active", nil)
	defer cleanupProject()

	called := false
	prev := doReplyCommitFn
	defer func() { doReplyCommitFn = prev }()
	doReplyCommitFn = func(_ context.Context, _ *pgxpool.Pool, _, _, _, _, _ string) error {
		called = true
		return nil
	}

	// userWithProjects("testproject") has no access to "otherproject"
	c, rec := newV1ReplyRequest(t, "mem_v1", "cm_v1", map[string]any{"body": "hi"}, userWithProjects("testproject"))
	if err := handleV1ReplyCommit(nil)(c); err == nil && rec.Code != http.StatusForbidden {
		t.Errorf("should return 403 for non-writer; code=%d body=%s", rec.Code, rec.Body.String())
	}
	if called {
		t.Errorf("domain must not be called on auth failure")
	}
}
