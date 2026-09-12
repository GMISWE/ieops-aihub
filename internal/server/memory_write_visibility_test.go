package server

// aihub#627: a write request must not confirm what a read hides.
//
// The defect this file pins: every single-memory WRITE entry (annotate, reply,
// resolve, edit, delete, activate, redact, reinforce, update/supersede,
// unshare) gated on project-writer only. The per-memory visibility rule that
// aihub#379 unified for READS (private is author-only, admin tier is
// global-admin-only, denial byte-identical to "no such id") never ran, so a
// project writer could annotate a private memory they could not read, and the
// request's acceptance - or its distinctive refusal - confirmed the row's
// existence.
//
// The contract, mirroring memory_visibility_uniform_test.go's shape:
//
//   - for "private and not the author" and "admin tier and not an admin",
//     every write entry answers the shared errNotVisible() 404, and the bytes
//     are identical (a) to the same entry's answer for an id that does not
//     exist and (b) to the READ answer (handleGetMemory) for the same row and
//     caller - the wi's acceptance criterion verbatim;
//   - a member merely short of writer gets that same 404 for an invisible row
//     (the role 403 would otherwise be an existence oracle for viewers), but
//     KEEPS the explanatory 403 for a row they can see (aihub#377's positive
//     control);
//   - the author and a global admin still get through to the mutation.
//
// Mutant criterion: remove any one handler's checkMemoryWriteAccess call (or
// the checkMemoryVisibility call in the handlers that gate inline) and that
// entry's denial case here goes red.

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/labstack/echo/v4"

	"github.com/GMISWE/ieops-aihub/internal/domain"
)

const (
	wvMemID    = "mem_wv627"
	wvCommitID = "cmt_wv627"
)

// wvRow is the memory row the suite probes: private (or admin-tier) in
// testproj, authored by u_author (authorUser's id).
func wvRow(visibility string) *domain.Memory {
	return &domain.Memory{
		ID:           wvMemID,
		Project:      "testproj",
		Type:         "fact.note",
		Status:       "active",
		Visibility:   visibility,
		AuthorUserID: "u_author",
		Content:      "AIHUB627-SECRET",
	}
}

// nonAuthorWriter is the attacker of the wi's goal statement: full writer on
// the row's project, not its author, not a global admin.
func nonAuthorWriter() *UserContext {
	u := writerUser("testproj")
	return u
}

// wvInvoke drives one handler with params/body and returns the recorder.
func wvInvoke(t *testing.T, h echo.HandlerFunc, caller *UserContext, method, target, contentType, body string, paramNames, paramValues []string) *httptest.ResponseRecorder {
	t.Helper()
	e := echo.New()
	var req *http.Request
	if body == "" {
		req = httptest.NewRequest(method, target, nil)
	} else {
		req = httptest.NewRequest(method, target, strings.NewReader(body))
	}
	if contentType != "" {
		req.Header.Set(echo.HeaderContentType, contentType)
	}
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	if len(paramNames) > 0 {
		c.SetParamNames(paramNames...)
		c.SetParamValues(paramValues...)
	}
	setUser(c, caller)
	if err := h(c); err != nil {
		e.HTTPErrorHandler(err, c)
	}
	return rec
}

// writeVisEntry is one single-memory write entry.
type writeVisEntry struct {
	name string
	// prepare installs the entry's loader fake for the given row (or, when
	// missing is true, a loader failing as for an absent id) plus a
	// success-path mutation stub; it returns the restore stack and a flag
	// reporting whether the mutation stub ran.
	prepare func(t *testing.T, mem *domain.Memory, missing bool) (func(), *bool)
	invoke  func(t *testing.T, caller *UserContext) *httptest.ResponseRecorder
	// The author/admin positive control's expected response. posMutates says
	// the mutation stub must have run; entries whose positive control is a
	// post-gate refusal probe (update, remember) or a no-op (unshare) instead
	// pin posStatus/posContains. posRow, when set, transforms the row for the
	// positive arms only (update's probe needs a redacted head).
	posStatus          int
	posContains        string
	posMutates         bool
	posRow             func(mem *domain.Memory) *domain.Memory
	skipPositiveReason string
	// viewerRoleRefusalOK marks the one entry (POST /v1/memories) whose
	// project-writer check runs on the CALLER'S OWN request body before the
	// target gate: a viewer's 403 there names the project the caller chose
	// themselves and is byte-identical whether the target exists or not, so
	// it discloses nothing about the row. The byte-identity assertion still
	// applies; only the 404 demand is waived for the viewer case.
	viewerRoleRefusalOK bool
}

// prepareSeamRow drives the shared commitMemoryProjectFn seam - the loader
// behind checkMemoryWriteAccess - and stubs one do*Fn mutation seam.
func prepareSeamRow(mem *domain.Memory, missing bool, stub func() (func(), *bool)) (func(), *bool) {
	var restoreLoad func()
	if missing {
		restoreLoad = withCommitMemoryMetaOverride(memWriteMeta{}, errors.New("no rows"))
	} else {
		restoreLoad = withCommitMemoryMetaOverride(memWriteMeta{
			Project:      mem.Project,
			Status:       mem.Status,
			Visibility:   mem.Visibility,
			AuthorUserID: mem.AuthorUserID,
		}, nil)
	}
	restoreStub, mutated := stub()
	return func() { restoreStub(); restoreLoad() }, mutated
}

func stubReplyCommit() (func(), *bool) {
	called := false
	prev := doReplyCommitFn
	doReplyCommitFn = func(_ context.Context, _ *pgxpool.Pool, _, _, _, _, _ string) error {
		called = true
		return nil
	}
	return func() { doReplyCommitFn = prev }, &called
}

func stubResolveCommit() (func(), *bool) {
	called := false
	prev := doResolveCommitFn
	doResolveCommitFn = func(_ context.Context, _ *pgxpool.Pool, _, _, _, _, _ string) error {
		called = true
		return nil
	}
	return func() { doResolveCommitFn = prev }, &called
}

func stubCommitMemory() (func(), *bool) {
	called := false
	prev := doCommitMemoryFn
	doCommitMemoryFn = func(_ context.Context, _ *pgxpool.Pool, _, _, _, _ string, _ domain.CommitAnchorArgs) error {
		called = true
		return nil
	}
	return func() { doCommitMemoryFn = prev }, &called
}

func stubArtifactCommit() (func(), *bool) {
	called := false
	prev := doArtifactCommitFn
	doArtifactCommitFn = func(_ context.Context, _ *pgxpool.Pool, _, _, _, _ string, _ domain.CommitAnchorArgs) error {
		called = true
		return nil
	}
	return func() { doArtifactCommitFn = prev }, &called
}

func stubEditCommit() (func(), *bool) {
	called := false
	prev := doEditCommitFn
	doEditCommitFn = func(_ context.Context, _ *pgxpool.Pool, _, _, _, _, _, _ string) error {
		called = true
		return nil
	}
	return func() { doEditCommitFn = prev }, &called
}

func stubDeleteCommit() (func(), *bool) {
	called := false
	prev := doDeleteCommitFn
	doDeleteCommitFn = func(_ context.Context, _ *pgxpool.Pool, _, _, _, _, _ string) error {
		called = true
		return nil
	}
	return func() { doDeleteCommitFn = prev }, &called
}

func stubActivate() (func(), *bool) {
	called := false
	prev := doActivateFn
	doActivateFn = func(_ context.Context, _ *pgxpool.Pool, _, _, _ string) (*domain.ActivateResponse, error) {
		called = true
		return &domain.ActivateResponse{ActivationCount: 1}, nil
	}
	return func() { doActivateFn = prev }, &called
}

func stubRedact() (func(), *bool) {
	called := false
	prev := doRedactFn
	doRedactFn = func(_ context.Context, _ *pgxpool.Pool, _, _, _, _, _ string) error {
		called = true
		return nil
	}
	return func() { doRedactFn = prev }, &called
}

func memoryWriteEntries() []writeVisEntry {
	form := echo.MIMEApplicationForm
	jsonCT := echo.MIMEApplicationJSON
	idOnly := []string{"id"}
	idVal := []string{wvMemID}
	idCommit := []string{"id", "commit_id"}
	idCommitVal := []string{wvMemID, wvCommitID}

	return []writeVisEntry{
		{
			name: "POST /ui/memories/:id/commit",
			prepare: func(_ *testing.T, mem *domain.Memory, missing bool) (func(), *bool) {
				return prepareSeamRow(mem, missing, stubCommitMemory)
			},
			invoke: func(t *testing.T, caller *UserContext) *httptest.ResponseRecorder {
				return wvInvoke(t, handleUICommitMemory(nil), caller, http.MethodPost,
					"/ui/memories/"+wvMemID+"/commit", form, "body=hi", idOnly, idVal)
			},
			posStatus:  http.StatusSeeOther,
			posMutates: true,
		},
		{
			name: "POST /ui/memories/:id/commit/:commit_id/edit",
			prepare: func(_ *testing.T, mem *domain.Memory, missing bool) (func(), *bool) {
				return prepareSeamRow(mem, missing, stubEditCommit)
			},
			invoke: func(t *testing.T, caller *UserContext) *httptest.ResponseRecorder {
				return wvInvoke(t, handleUIEditCommit(nil), caller, http.MethodPost,
					"/ui/memories/"+wvMemID+"/commit/"+wvCommitID+"/edit", form, "body=hi", idCommit, idCommitVal)
			},
			posStatus:  http.StatusSeeOther,
			posMutates: true,
		},
		{
			name: "POST /ui/memories/:id/commit/:commit_id/delete",
			prepare: func(_ *testing.T, mem *domain.Memory, missing bool) (func(), *bool) {
				return prepareSeamRow(mem, missing, stubDeleteCommit)
			},
			invoke: func(t *testing.T, caller *UserContext) *httptest.ResponseRecorder {
				return wvInvoke(t, handleUIDeleteCommit(nil), caller, http.MethodPost,
					"/ui/memories/"+wvMemID+"/commit/"+wvCommitID+"/delete", form, "", idCommit, idCommitVal)
			},
			posStatus:  http.StatusSeeOther,
			posMutates: true,
		},
		{
			name: "POST /ui/memories/:id/commit/:commit_id/reply",
			prepare: func(_ *testing.T, mem *domain.Memory, missing bool) (func(), *bool) {
				return prepareSeamRow(mem, missing, stubReplyCommit)
			},
			invoke: func(t *testing.T, caller *UserContext) *httptest.ResponseRecorder {
				return wvInvoke(t, handleUIReplyCommit(nil), caller, http.MethodPost,
					"/ui/memories/"+wvMemID+"/commit/"+wvCommitID+"/reply", form, "body=hi", idCommit, idCommitVal)
			},
			posStatus:  http.StatusSeeOther,
			posMutates: true,
		},
		{
			name: "POST /ui/memories/:id/commit/:commit_id/resolve",
			prepare: func(_ *testing.T, mem *domain.Memory, missing bool) (func(), *bool) {
				return prepareSeamRow(mem, missing, stubResolveCommit)
			},
			invoke: func(t *testing.T, caller *UserContext) *httptest.ResponseRecorder {
				return wvInvoke(t, handleUIResolveCommit(nil), caller, http.MethodPost,
					"/ui/memories/"+wvMemID+"/commit/"+wvCommitID+"/resolve", form, "reply=ok", idCommit, idCommitVal)
			},
			posStatus:  http.StatusSeeOther,
			posMutates: true,
		},
		{
			name: "POST /ui/artifacts/:id/commit",
			prepare: func(_ *testing.T, mem *domain.Memory, missing bool) (func(), *bool) {
				return prepareSeamRow(mem, missing, stubArtifactCommit)
			},
			invoke: func(t *testing.T, caller *UserContext) *httptest.ResponseRecorder {
				return wvInvoke(t, handleUIArtifactCommit(nil), caller, http.MethodPost,
					"/ui/artifacts/"+wvMemID+"/commit", form, "body=hi", idOnly, idVal)
			},
			posStatus:  http.StatusSeeOther,
			posMutates: true,
		},
		{
			name: "POST /ui/artifacts/:id/commit/:commit_id/reply",
			prepare: func(_ *testing.T, mem *domain.Memory, missing bool) (func(), *bool) {
				return prepareSeamRow(mem, missing, stubReplyCommit)
			},
			invoke: func(t *testing.T, caller *UserContext) *httptest.ResponseRecorder {
				return wvInvoke(t, handleUIArtifactReplyCommit(nil), caller, http.MethodPost,
					"/ui/artifacts/"+wvMemID+"/commit/"+wvCommitID+"/reply", form, "body=hi", idCommit, idCommitVal)
			},
			posStatus:  http.StatusSeeOther,
			posMutates: true,
		},
		{
			name: "POST /ui/artifacts/:id/commit/:commit_id/resolve",
			prepare: func(_ *testing.T, mem *domain.Memory, missing bool) (func(), *bool) {
				return prepareSeamRow(mem, missing, stubResolveCommit)
			},
			invoke: func(t *testing.T, caller *UserContext) *httptest.ResponseRecorder {
				return wvInvoke(t, handleUIArtifactResolveCommit(nil), caller, http.MethodPost,
					"/ui/artifacts/"+wvMemID+"/commit/"+wvCommitID+"/resolve", form, "reply=ok", idCommit, idCommitVal)
			},
			posStatus:  http.StatusSeeOther,
			posMutates: true,
		},
		{
			name: "POST /v1/memories/:id/commit/:commit_id/reply",
			prepare: func(_ *testing.T, mem *domain.Memory, missing bool) (func(), *bool) {
				return prepareSeamRow(mem, missing, stubReplyCommit)
			},
			invoke: func(t *testing.T, caller *UserContext) *httptest.ResponseRecorder {
				return wvInvoke(t, handleV1ReplyCommit(nil), caller, http.MethodPost,
					"/v1/memories/"+wvMemID+"/commit/"+wvCommitID+"/reply", jsonCT, `{"body":"hi"}`, idCommit, idCommitVal)
			},
			posStatus:  http.StatusOK,
			posMutates: true,
		},
		{
			name: "POST /v1/memories/:id/commit/:commit_id/resolve",
			prepare: func(_ *testing.T, mem *domain.Memory, missing bool) (func(), *bool) {
				return prepareSeamRow(mem, missing, stubResolveCommit)
			},
			invoke: func(t *testing.T, caller *UserContext) *httptest.ResponseRecorder {
				return wvInvoke(t, handleResolveCommit(nil), caller, http.MethodPost,
					"/v1/memories/"+wvMemID+"/commit/"+wvCommitID+"/resolve", jsonCT, `{"reply":"ok"}`, idCommit, idCommitVal)
			},
			posStatus:  http.StatusOK,
			posMutates: true,
		},
		{
			name: "POST /v1/memories/:id/activate",
			prepare: func(_ *testing.T, mem *domain.Memory, missing bool) (func(), *bool) {
				return prepareSeamRow(mem, missing, stubActivate)
			},
			invoke: func(t *testing.T, caller *UserContext) *httptest.ResponseRecorder {
				return wvInvoke(t, handleActivateMemory(nil), caller, http.MethodPost,
					"/v1/memories/"+wvMemID+"/activate", "", "", idOnly, idVal)
			},
			posStatus:  http.StatusOK,
			posMutates: true,
		},
		{
			name: "PATCH /v1/memories/:id/redact",
			prepare: func(_ *testing.T, mem *domain.Memory, missing bool) (func(), *bool) {
				return prepareSeamRow(mem, missing, stubRedact)
			},
			invoke: func(t *testing.T, caller *UserContext) *httptest.ResponseRecorder {
				return wvInvoke(t, handleRedactMemory(nil), caller, http.MethodPatch,
					"/v1/memories/"+wvMemID+"/redact", jsonCT, `{"reason":"test"}`, idOnly, idVal)
			},
			posStatus:  http.StatusOK,
			posMutates: true,
		},
		{
			name: "PATCH /v1/memories/:id/reinforce",
			prepare: func(_ *testing.T, mem *domain.Memory, missing bool) (func(), *bool) {
				restore, _ := prepareSeamRow(mem, missing, func() (func(), *bool) {
					b := false
					return func() {}, &b
				})
				return restore, nil
			},
			invoke: func(t *testing.T, caller *UserContext) *httptest.ResponseRecorder {
				return wvInvoke(t, handleReinforceMemory(nil), caller, http.MethodPatch,
					"/v1/memories/"+wvMemID+"/reinforce", jsonCT, `{"additional_context":"more"}`, idOnly, idVal)
			},
			skipPositiveReason: "the mutation reads the row inline (no seam), so the author/admin " +
				"arm would need a database; the gate it shares with the twelve entries above has " +
				"its positive control there",
		},
		{
			name: "PATCH /v1/memories/:id/update",
			prepare: func(t *testing.T, mem *domain.Memory, missing bool) (func(), *bool) {
				if missing {
					return withResolveLatestOverride(t, wvMemID, nil,
						domain.NewErr(domain.ErrNotFound, "memory not found")), nil
				}
				return withResolveLatestOverride(t, wvMemID, mem, nil), nil
			},
			invoke: func(t *testing.T, caller *UserContext) *httptest.ResponseRecorder {
				return wvInvoke(t, handleUpdateMemory(nil), caller, http.MethodPatch,
					"/v1/memories/"+wvMemID+"/update", jsonCT, `{"content":"new"}`, idOnly, idVal)
			},
			// The positive control is a post-gate probe: a redacted head is
			// refused AFTER visibility and role pass, so reaching that 403
			// proves the author/admin got through both gates with no database.
			posStatus:   http.StatusForbidden,
			posContains: "cannot update a redacted memory",
			posRow: func(mem *domain.Memory) *domain.Memory {
				cp := *mem
				cp.Status = "redacted"
				return &cp
			},
		},
		{
			name: "POST /v1/memories with supersedes_memory_id",
			prepare: func(_ *testing.T, mem *domain.Memory, missing bool) (func(), *bool) {
				if missing {
					return withLoadMemoryOverride(nil,
						domain.NewErr(domain.ErrNotFound, "memory not found")), nil
				}
				return withLoadMemoryOverride(mem, nil), nil
			},
			invoke: func(t *testing.T, caller *UserContext) *httptest.ResponseRecorder {
				body := `{"project":"testproj","type":"methodology.spec","content":"x","supersedes_memory_id":"` + wvMemID + `"}`
				return wvInvoke(t, handleRemember(nil), caller, http.MethodPost,
					"/v1/memories", jsonCT, body, nil, nil)
			},
			// Post-gate probe: the C5 methodology block sits AFTER the
			// supersede-target gate and refuses this credential-less request
			// with a 400 that names the caller's own request, not the target.
			// Reaching it proves the author/admin passed the target gate.
			posStatus:           http.StatusBadRequest,
			posContains:         "work_item_id is required for methodology.* artifacts",
			viewerRoleRefusalOK: true,
		},
		{
			name: "DELETE /v1/artifacts/:id/share",
			prepare: func(_ *testing.T, mem *domain.Memory, missing bool) (func(), *bool) {
				if missing {
					return withLoadMemoryOverride(nil,
						domain.NewErr(domain.ErrNotFound, "memory not found")), nil
				}
				return withLoadMemoryOverride(mem, nil), nil
			},
			invoke: func(t *testing.T, caller *UserContext) *httptest.ResponseRecorder {
				return wvInvoke(t, handleUnshareArtifact(nil), caller, http.MethodDelete,
					"/v1/artifacts/"+wvMemID+"/share", "", "", idOnly, idVal)
			},
			// A non-public row is a no-op 200 that echoes the tier - which is
			// exactly why the invisible arms above must 404 first. The echo is
			// the positive control: the caller may see this row.
			posStatus:   http.StatusOK,
			posContains: `"visibility":`,
		},
	}
}

// readDenialBytes is the acceptance criterion's other half: what the READ
// entry (GET /v1/memories/:id) answers the same caller for the same row.
func readDenialBytes(t *testing.T, mem *domain.Memory, caller *UserContext) (int, string) {
	t.Helper()
	restore := withLoadMemoryOverride(mem, nil)
	defer restore()
	rec := wvInvoke(t, handleGetMemory(nil), caller, http.MethodGet,
		"/v1/memories/"+wvMemID, "", "", []string{"id"}, []string{wvMemID})
	return rec.Code, rec.Body.String()
}

// TestMemoryWriteVisibilityDenialIsUniform asserts the aihub#627 contract on
// every write entry: for a row the caller cannot READ, the write answer is
// 404, byte-identical to the same entry's no-such-id answer AND to the read
// answer, carries the shared notVisibleMessage, ran no mutation, and leaks no
// content.
func TestMemoryWriteVisibilityDenialIsUniform(t *testing.T) {
	cases := []struct {
		name     string
		mem      *domain.Memory
		caller   func() *UserContext
		isViewer bool
	}{
		// The wi's goal statement: a full writer on the project, not the author.
		{name: "private_not_author_writer", mem: wvRow("private"), caller: nonAuthorWriter},
		// Even the author: the admin tier is about global role.
		{name: "admin_tier_not_admin", mem: wvRow("admin"), caller: authorUser},
		// A member short of writer: the role 403 must NOT fire for an
		// invisible row - a 403 there confirms existence to every viewer.
		{name: "private_member_viewer", mem: wvRow("private"), caller: otherViewerUser, isViewer: true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			readStatus, readBody := readDenialBytes(t, tc.mem, tc.caller())
			if readStatus != http.StatusNotFound {
				t.Fatalf("read control: got %d, want 404 - the read side (aihub#379) must hide this row first", readStatus)
			}

			for _, entry := range memoryWriteEntries() {
				t.Run(entry.name, func(t *testing.T) {
					restore, mutated := entry.prepare(t, tc.mem, false)
					denied := entry.invoke(t, tc.caller())
					restore()

					restore, _ = entry.prepare(t, nil, true)
					missing := entry.invoke(t, tc.caller())
					restore()

					if tc.isViewer && entry.viewerRoleRefusalOK {
						// The refusal is about the caller's own request; the
						// property that matters is indistinguishability.
						if denied.Code != http.StatusForbidden {
							t.Fatalf("%s: viewer got %d, want the own-request 403; body=%s",
								entry.name, denied.Code, denied.Body.String())
						}
						if denied.Body.String() != missing.Body.String() {
							t.Fatalf("%s: viewer's answers for an invisible and a missing target differ - an oracle.\nhidden:  %s\nmissing: %s",
								entry.name, denied.Body.String(), missing.Body.String())
						}
						if mutated != nil && *mutated {
							t.Fatalf("%s: the mutation ran for a viewer", entry.name)
						}
						return
					}

					if denied.Code != http.StatusNotFound {
						t.Fatalf("%s: got %d, want 404 - a write on an invisible row must hide its existence (aihub#627); body=%s",
							entry.name, denied.Code, denied.Body.String())
					}
					if denied.Body.String() != missing.Body.String() {
						t.Fatalf("%s: the hidden-row denial and the no-such-id answer differ - that difference is an existence oracle.\nhidden:  %s\nmissing: %s",
							entry.name, denied.Body.String(), missing.Body.String())
					}
					if denied.Body.String() != readBody {
						t.Fatalf("%s: the write denial and the READ denial differ - the wi's acceptance is that they are the same answer.\nwrite: %s\nread:  %s",
							entry.name, denied.Body.String(), readBody)
					}
					if !strings.Contains(denied.Body.String(), notVisibleMessage) {
						t.Fatalf("%s: denial does not carry the shared notVisibleMessage; body=%s", entry.name, denied.Body.String())
					}
					if strings.Contains(denied.Body.String(), "AIHUB627-SECRET") {
						t.Fatalf("%s: denied content leaked into the response body", entry.name)
					}
					if mutated != nil && *mutated {
						t.Fatalf("%s: the mutation ran despite the visibility denial", entry.name)
					}
				})
			}
		})
	}
}

// TestMemoryWriteVisibilityPositiveControls guards against satisfying the
// uniformity above by refusing everyone: the author and a global admin still
// get through every entry's gate on the same private row.
func TestMemoryWriteVisibilityPositiveControls(t *testing.T) {
	for _, caller := range []struct {
		name string
		u    func() *UserContext
	}{
		{name: "author", u: authorUser},
		{name: "admin", u: adminUser},
	} {
		t.Run(caller.name, func(t *testing.T) {
			for _, entry := range memoryWriteEntries() {
				t.Run(entry.name, func(t *testing.T) {
					if entry.skipPositiveReason != "" {
						t.Skipf("no in-process positive arm: %s", entry.skipPositiveReason)
					}
					mem := wvRow("private")
					if entry.posRow != nil {
						mem = entry.posRow(mem)
					}
					restore, mutated := entry.prepare(t, mem, false)
					rec := entry.invoke(t, caller.u())
					restore()

					if rec.Code != entry.posStatus {
						t.Fatalf("%s: caller %s got %d, want %d; body=%s",
							entry.name, caller.name, rec.Code, entry.posStatus, rec.Body.String())
					}
					if entry.posContains != "" && !strings.Contains(rec.Body.String(), entry.posContains) {
						t.Fatalf("%s: caller %s: body %q does not contain %q",
							entry.name, caller.name, rec.Body.String(), entry.posContains)
					}
					if entry.posMutates && (mutated == nil || !*mutated) {
						t.Fatalf("%s: caller %s passed the gate but the mutation stub never ran", entry.name, caller.name)
					}
				})
			}
		})
	}
}

// TestMemoryWriteRoleShortfallStillExplains is the aihub#377 positive control
// restated for the write entries: a member short of writer, asking to write a
// row they CAN see, keeps the explanatory 403. The visibility gate runs first
// but must not eat this refusal - only invisible rows trade it for the 404.
func TestMemoryWriteRoleShortfallStillExplains(t *testing.T) {
	// otherViewerUser can see a project-tier row but is only a viewer.
	mem := wvRow("project")
	for _, entry := range memoryWriteEntries() {
		t.Run(entry.name, func(t *testing.T) {
			restore, mutated := entry.prepare(t, mem, false)
			rec := entry.invoke(t, otherViewerUser())
			restore()

			if rec.Code != http.StatusForbidden {
				t.Fatalf("%s: a MEMBER short of writer asking about a VISIBLE row must keep the explanatory 403, got %d; body=%s",
					entry.name, rec.Code, rec.Body.String())
			}
			if body := rec.Body.String(); strings.Contains(body, notVisibleMessage) {
				t.Fatalf("%s: the role shortfall must not use the not-visible wording; body=%s", entry.name, body)
			}
			if mutated != nil && *mutated {
				t.Fatalf("%s: the mutation ran for a viewer", entry.name)
			}
		})
	}
}
