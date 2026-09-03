package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/GMISWE/ieops-aihub/internal/config"
	"github.com/GMISWE/ieops-aihub/pkg/client"
)

// ─── fake aihub ────────────────────────────────────────────────────────────
//
// This stands in for GET /v1/work_items and GET /v1/work_items/:id, and it
// reproduces the ONE server behaviour that makes aihub#307 possible rather than
// an idealised list endpoint: a page size the caller did not name yields a SHORT
// page plus a cursor, so code that reads page 1 and stops silently sees part of
// the set.
//
// Measured against production on 2026-08-31, project ieops, 127 open items:
//
//	limit absent -> 50 rows + next_cursor
//	limit=50     -> 50 rows + next_cursor
//	limit=200    -> 127 rows, no next_cursor
//	limit=500    -> 50 rows + next_cursor   <- the "just ask for a big number" fix
//
// ⚠️ The last line is HISTORY as of aihub#267: limit=500 now yields 200 rows, not
// 50. The fake is deliberately NOT updated, and this note is why. What these
// tests exercise is the cursor walk — "does the caller keep paging until the
// cursor runs out" — and that is driven by "a short page plus a cursor", which
// the first three lines still produce for every deployed and undeployed server.
// Changing the fake to the new value would weaken the fixture (a 200-row page
// covers the 127-item case in one shot, so the walk would stop being exercised)
// while proving nothing extra. It is modelling a WORSE server than the real one
// on purpose, which is the right direction for a fake to be wrong in.
//
// A fake that returned everything for any limit would let the pre-fix code pass,
// which is the only thing these tests exist to prevent.

type fakeWI struct {
	Slug   string
	ID     string
	Status string
}

type fakeAihub struct {
	items []fakeWI
	// hideFromList drops a slug from every list response while leaving it
	// readable by id. It models "the listing is wrong in some way we have not
	// thought of yet" — which is the case the per-item re-check exists for.
	hideFromList map[string]bool
	// failListPage, when non-zero, makes the Nth list page (1-based) return 500.
	failListPage int
	// repeatCursor makes every page hand back the cursor it was given, so the
	// walk can never advance. Without this the repeated-cursor guard was dead
	// code to the suite: the fake's cursor is a monotonic offset.
	repeatCursor bool
	// neverEndCursor makes every page hand back a fresh cursor forever, so the
	// page budget is the only thing that ends the walk.
	neverEndCursor bool
	// omitItems drops the items key from the response entirely.
	omitItems bool
	// badCursorType returns next_cursor as a number rather than a string or null.
	badCursorType bool

	mu        sync.Mutex
	listQuery []url.Values
	getSlugs  []string
}

func (f *fakeAihub) start(t *testing.T) *client.Client {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(f.serve))
	t.Cleanup(srv.Close)
	return client.New(srv.URL, "test-key")
}

func (f *fakeAihub) serve(w http.ResponseWriter, r *http.Request) {
	const listPath = "/v1/work_items"
	if r.URL.Path == listPath {
		f.serveList(w, r)
		return
	}
	if strings.HasPrefix(r.URL.Path, listPath+"/") {
		f.serveGet(w, strings.TrimPrefix(r.URL.Path, listPath+"/"))
		return
	}
	http.NotFound(w, r)
}

func (f *fakeAihub) serveList(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	f.mu.Lock()
	f.listQuery = append(f.listQuery, q)
	f.mu.Unlock()

	project := q.Get("project")
	wantStatus := map[string]bool{}
	for _, s := range strings.Split(q.Get("status"), ",") {
		if s = strings.TrimSpace(s); s != "" {
			wantStatus[s] = true
		}
	}

	// The server's clamp, reproduced exactly: out of range means "unset".
	limit, _ := strconv.Atoi(q.Get("limit"))
	if limit <= 0 || limit > 200 {
		limit = 50
	}

	var matched []fakeWI
	for _, it := range f.items {
		if project != "" && !strings.HasPrefix(it.Slug, project+"#") {
			continue
		}
		if len(wantStatus) > 0 && !wantStatus[it.Status] {
			continue
		}
		if f.hideFromList[it.Slug] {
			continue
		}
		matched = append(matched, it)
	}

	start, _ := strconv.Atoi(q.Get("cursor")) // "" -> 0
	page := start/limit + 1
	if f.failListPage != 0 && page == f.failListPage {
		w.WriteHeader(http.StatusInternalServerError)
		_ = json.NewEncoder(w).Encode(map[string]any{"code": "INTERNAL_ERROR", "message": "synthetic page failure"})
		return
	}

	end := start + limit
	if end > len(matched) {
		end = len(matched)
	}
	body := map[string]any{"items": itemsJSON(matched[start:end])}
	if f.omitItems {
		delete(body, "items")
	}
	switch {
	case f.repeatCursor:
		body["next_cursor"] = q.Get("cursor") // never advances; "" on page 1
		if body["next_cursor"] == "" {
			body["next_cursor"] = "0"
		}
	case f.neverEndCursor:
		body["next_cursor"] = strconv.Itoa(start + 1)
	case f.badCursorType:
		body["next_cursor"] = 12345 // neither string nor null
	case end < len(matched):
		body["next_cursor"] = strconv.Itoa(end)
	default:
		body["next_cursor"] = nil // the real server emits JSON null
	}
	_ = json.NewEncoder(w).Encode(body)
}

func (f *fakeAihub) serveGet(w http.ResponseWriter, slug string) {
	f.mu.Lock()
	f.getSlugs = append(f.getSlugs, slug)
	f.mu.Unlock()

	for _, it := range f.items {
		if it.Slug == slug || it.ID == slug {
			_ = json.NewEncoder(w).Encode(map[string]any{
				"id": it.ID, "slug": it.Slug, "status": it.Status,
			})
			return
		}
	}
	w.WriteHeader(http.StatusNotFound)
	_ = json.NewEncoder(w).Encode(map[string]any{
		"code": "NOT_FOUND", "message": fmt.Sprintf("work item %q not found", slug),
	})
}

func itemsJSON(in []fakeWI) []map[string]any {
	out := make([]map[string]any, 0, len(in))
	for _, it := range in {
		out = append(out, map[string]any{"id": it.ID, "slug": it.Slug, "status": it.Status})
	}
	return out
}

// ─── fixtures ──────────────────────────────────────────────────────────────

// ieopsFixture builds `n` work items for project ieops, all paused, then
// overrides the statuses named in `override` (index -> status). The index is the
// item's position in the full result set, which is what the bug was sensitive
// to: production had the misclassified worktrees at positions 62/64/65/82/111
// and the correctly-kept ones at 28 and 49 — the boundary was exactly the
// server's default page of 50.
func ieopsFixture(n int, override map[int]string) []fakeWI {
	out := make([]fakeWI, 0, n)
	for i := 0; i < n; i++ {
		st := "paused"
		if s, ok := override[i]; ok {
			st = s
		}
		out = append(out, fakeWI{
			Slug:   fmt.Sprintf("ieops#%d", 1000+i),
			ID:     fmt.Sprintf("wi_%08d", 1000+i),
			Status: st,
		})
	}
	return out
}

func workspaceWithWorktrees(t *testing.T, dirs ...string) string {
	t.Helper()
	root := t.TempDir()
	for _, d := range dirs {
		if err := os.MkdirAll(filepath.Join(root, d, "aihub"), 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", d, err)
		}
		// A file inside, so "the directory is gone" is a real observation and not
		// an empty-dir artefact.
		if err := os.WriteFile(filepath.Join(root, d, "aihub", "WORK.txt"), []byte("uncommitted"), 0o644); err != nil {
			t.Fatalf("write %s: %v", d, err)
		}
	}
	return root
}

func ieopsCfg() *config.Config {
	return &config.Config{Projects: map[string]config.Project{"ieops": {}}}
}

func runWorktreeCheck(t *testing.T, f *fakeAihub, root string, opts doctorOpts) (checkResult, string) {
	t.Helper()
	if opts.forceRemove == nil {
		opts.forceRemove = map[string]string{}
	}
	var buf bytes.Buffer
	res := checkWorktrees(context.Background(), f.start(t), ieopsCfg(), root, opts, &buf)
	return res, buf.String()
}

// ─── acceptance probes ─────────────────────────────────────────────────────

// TestOrphanScanKeepsActiveWorkItemsPastTheFirstPage is the positive probe from
// aihub#307: a running work item whose position in the result set is past the
// server's default page must NOT be reported as an orphan.
//
// It is deliberately not enough on its own — an implementation that reports no
// orphans ever also passes it. TestOrphanScanStillReportsTerminalWorkItems is
// the other half and must be kept alongside it.
func TestOrphanScanKeepsActiveWorkItemsPastTheFirstPage(t *testing.T) {
	// 260 open items: past the 50-row default AND past the 200-row cap, so a
	// single un-paginated request of any legal size still cannot see the tail.
	// index 111 reproduces the measured production position of pf.ieops-274.
	items := ieopsFixture(260, map[int]string{
		111: "running",
		220: "running",
		37:  "blocked", // inside the first page, but a status the old query omitted
	})
	f := &fakeAihub{items: items}

	live := []string{
		"pf.ieops-1111", // index 111, running
		"pf.ieops-1220", // index 220, running
		"pf.ieops-1037", // index 37,  blocked
		"pf.ieops-1028", // index 28,  paused — the control that always worked
		"pf.ieops-1150", // index 150, paused — invisible to the old query
	}
	root := workspaceWithWorktrees(t, live...)

	res, _ := runWorktreeCheck(t, f, root, doctorOpts{})

	for _, dir := range live {
		if strings.Contains(res.Message, dir) {
			t.Errorf("%s is backed by an active work item but was reported as an orphan.\nmessage: %s", dir, res.Message)
		}
	}
	if res.Status != "ok" {
		t.Errorf("status = %q, want ok — every worktree here is live.\nmessage: %s", res.Status, res.Message)
	}

	// The mechanism, not just the outcome: more than one page had to be fetched,
	// and every page had to ask for a limit inside the server's cap.
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.listQuery) < 2 {
		t.Errorf("made %d list request(s); 260 items cannot be read in one page of at most 200", len(f.listQuery))
	}
	for i, q := range f.listQuery {
		n, err := strconv.Atoi(q.Get("limit"))
		if err != nil || n <= 0 || n > 200 {
			t.Errorf("list request %d asked for limit=%q; the server silently substitutes 50 for anything outside 1..200", i+1, q.Get("limit"))
		}
	}
}

// TestOrphanScanStillReportsTerminalWorkItems is the negative probe: the fix must
// not have been "stop reporting orphans".
func TestOrphanScanStillReportsTerminalWorkItems(t *testing.T) {
	items := ieopsFixture(260, map[int]string{111: "running"})
	// Three finished work items, one of each terminal status, all past the first
	// page so they exercise the same paginated path as the kept ones.
	items[240].Status = "wrapped"
	items[241].Status = "failed"
	items[242].Status = "cancelled"
	f := &fakeAihub{items: items}

	root := workspaceWithWorktrees(t,
		"pf.ieops-1111", // running — must be kept
		"pf.ieops-1240", // wrapped
		"pf.ieops-1241", // failed
		"pf.ieops-1242", // cancelled
	)

	res, _ := runWorktreeCheck(t, f, root, doctorOpts{})

	for _, want := range []string{"pf.ieops-1240", "pf.ieops-1241", "pf.ieops-1242"} {
		if !strings.Contains(res.Message, want) {
			t.Errorf("%s belongs to a terminal work item and must still be reported as an orphan.\nmessage: %s", want, res.Message)
		}
	}
	if strings.Contains(res.Message, "pf.ieops-1111") {
		t.Errorf("pf.ieops-1111 is running and must not be reported.\nmessage: %s", res.Message)
	}
	if res.Status != "warning" {
		t.Errorf("status = %q, want warning", res.Status)
	}
	// The status has to reach the message — that is what lets a human veto the
	// batch before running --fix.
	for _, want := range []string{"[wrapped]", "[failed]", "[cancelled]"} {
		if !strings.Contains(res.Message, want) {
			t.Errorf("message does not carry %s; each path must be reported with its work item's status.\nmessage: %s", want, res.Message)
		}
	}
}

// ─── --fix granularity ─────────────────────────────────────────────────────

func TestFixRemovesTerminalWorktreesOnly(t *testing.T) {
	// Every status is set BEFORE the fake is constructed. Setting items[111]
	// afterwards worked only because fakeAihub shares the backing array; the day
	// it copies, this test would silently degrade into "a wrapped item is kept"
	// and stop testing the thing it is named after.
	items := ieopsFixture(260, map[int]string{111: "running"})
	items[240].Status = "wrapped"
	f := &fakeAihub{
		items: items,
		// pf.ieops-1111 is running, and the listing does not mention it. That is
		// the aihub#307 shape after the pagination fix: any FUTURE way of losing
		// rows lands here, and the per-item re-check has to catch it.
		hideFromList: map[string]bool{"ieops#1111": true},
	}

	root := workspaceWithWorktrees(t, "pf.ieops-1111", "pf.ieops-1240")
	res, out := runWorktreeCheck(t, f, root, doctorOpts{fix: true})

	if _, err := os.Stat(filepath.Join(root, "pf.ieops-1111")); err != nil {
		t.Fatalf("pf.ieops-1111 backs a running work item and was deleted anyway: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "pf.ieops-1240")); !os.IsNotExist(err) {
		t.Fatalf("pf.ieops-1240 is wrapped and should have been removed, stat err = %v", err)
	}
	// Per-worktree audit trail, printed before anything is deleted.
	if !strings.Contains(out, "pf.ieops-1111") || !strings.Contains(out, "status=running") {
		t.Errorf("--fix output does not name pf.ieops-1111 with its status.\noutput:\n%s", out)
	}
	if !strings.Contains(out, "pf.ieops-1240") || !strings.Contains(out, "status=wrapped") {
		t.Errorf("--fix output does not name pf.ieops-1240 with its status.\noutput:\n%s", out)
	}
	// It must say what to do, but NOT by printing a command that removes a
	// running work item's worktree — see TestRefusalDoesNotHandOutTheBypass.
	if !strings.Contains(out, "Commit or wrap ieops#1111 first") {
		t.Errorf("--fix must say what to do about the refusal.\noutput:\n%s", out)
	}
	if strings.Contains(out, "--force-remove=pf.ieops-1111") {
		t.Errorf("--fix printed a runnable bypass for a running work item.\noutput:\n%s", out)
	}
	if res.Status != "warning" {
		t.Errorf("status = %q, want warning — something was kept", res.Status)
	}
}

// TestForceRemoveOnlyTouchesNamedDirectories: the acknowledgement is the name.
// It is never a blanket --force over whatever the scan selected, and one
// directory's acknowledgement never carries to its neighbour — even when the two
// are indistinguishable to the scan (same project, same status, adjacent seq).
//
// It is NOT "one directory per invocation": the flag takes a comma-separated list
// and may be repeated, and a previous version of this file claimed otherwise
// three lines above a usage string advertising `<dir>[,<dir>]`. What escalates
// with danger is the value, not the count — an active work item needs its status
// transcribed as well.
func TestForceRemoveOnlyTouchesNamedDirectories(t *testing.T) {
	items := ieopsFixture(20, nil)
	items[3].Status = "running"
	items[4].Status = "running"
	f := &fakeAihub{items: items, hideFromList: map[string]bool{"ieops#1003": true, "ieops#1004": true}}

	root := workspaceWithWorktrees(t, "pf.ieops-1003", "pf.ieops-1004")
	_, out := runWorktreeCheck(t, f, root, doctorOpts{
		fix:         true,
		forceRemove: map[string]string{"pf.ieops-1003": "running"},
	})

	if _, err := os.Stat(filepath.Join(root, "pf.ieops-1003")); !os.IsNotExist(err) {
		t.Errorf("pf.ieops-1003 was named by --force-remove and should be gone, stat err = %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "pf.ieops-1004")); err != nil {
		t.Errorf("--force-remove named only pf.ieops-1003; pf.ieops-1004 must survive: %v", err)
	}
	if !strings.Contains(out, "removing anyway") {
		t.Errorf("a forced removal must say so.\noutput:\n%s", out)
	}
}

// ─── failure is not emptiness ──────────────────────────────────────────────

func TestFailedListingNeverProducesOrphans(t *testing.T) {
	// 260 items, but page 2 of the walk 500s. The pre-fix code's reaction to a
	// failed listing was `continue`, i.e. compare against nothing, i.e. nominate
	// every worktree of that project for deletion.
	dirs := []string{"pf.ieops-1001", "pf.ieops-9999", "pf.deadbeef"}
	f := &fakeAihub{items: ieopsFixture(260, nil), failListPage: 2}
	root := workspaceWithWorktrees(t, dirs...)

	res, _ := runWorktreeCheck(t, f, root, doctorOpts{})

	if strings.Contains(res.Message, "orphan worktrees:") {
		t.Errorf("a failed listing must not nominate anything for deletion.\nmessage: %s", res.Message)
	}
	for _, d := range dirs {
		if !strings.Contains(res.Message, d) {
			t.Errorf("%s should be reported as unverifiable, not silently dropped.\nmessage: %s", d, res.Message)
		}
	}
	if res.Status != "warning" {
		t.Errorf("status = %q, want warning", res.Status)
	}

	// The message is not the point — nothing may actually be deleted. Asserting
	// only on the absence of a substring would pin the wording and not the
	// behaviour.
	f2 := &fakeAihub{items: ieopsFixture(260, nil), failListPage: 2}
	root2 := workspaceWithWorktrees(t, dirs...)
	if _, out := runWorktreeCheck(t, f2, root2, doctorOpts{fix: true}); strings.Contains(out, ": removed") {
		t.Errorf("--fix removed something despite a failed listing.\noutput:\n%s", out)
	}
	for _, d := range dirs {
		if _, err := os.Stat(filepath.Join(root2, d)); err != nil {
			t.Errorf("%s was deleted despite a failed listing: %v", d, err)
		}
	}
}

// TestForceRemoveReachesUnverifiableDirectories: a project the caller has lost
// access to fails its listing on every run, so without a way through, its
// directories are permanently uncleanable and the only remaining option is
// `rm -rf` — the tool gets bypassed exactly where it is being careful.
func TestForceRemoveReachesUnverifiableDirectories(t *testing.T) {
	items := ieopsFixture(260, nil)
	items[250].Status = "wrapped" // ieops#1250 -> pf.ieops-1250, resolvable, terminal
	f := &fakeAihub{items: items, failListPage: 2}
	root := workspaceWithWorktrees(t, "pf.ieops-1250", "pf.ieops-9999")

	res, out := runWorktreeCheck(t, f, root, doctorOpts{
		fix:         true,
		forceRemove: map[string]string{"pf.ieops-1250": ""},
	})

	if _, err := os.Stat(filepath.Join(root, "pf.ieops-1250")); !os.IsNotExist(err) {
		t.Errorf("--force-remove named pf.ieops-1250 and its work item is wrapped; it must be removable even though the listing failed, stat err = %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "pf.ieops-9999")); err != nil {
		t.Errorf("pf.ieops-9999 was not named and must survive: %v", err)
	}
	if !strings.Contains(out, "named by --force-remove; it was not selected because") {
		t.Errorf("output must say the directory was not selected, only named.\noutput:\n%s", out)
	}
	if strings.Contains(res.Message, "knows nothing about") {
		t.Errorf("pf.ieops-1250 was a known directory; it must not be reported as unknown.\nmessage: %s", res.Message)
	}
}

// TestForcedUnverifiableStillGetsTheSecondHop is the F1 regression: --force-remove
// on a directory whose listing failed used to call the remover DIRECTLY, with no
// verifyOrphan, no status read and no status printed. Measured on the shipped
// build: one command destroyed four live worktrees — three running, one blocked —
// with zero status queries and an [ok] report. A failed listing says nothing
// about whether GET /v1/work_items/<key> works.
func TestForcedUnverifiableStillGetsTheSecondHop(t *testing.T) {
	items := ieopsFixture(260, map[int]string{
		251: "running",
		252: "running",
		253: "blocked",
	})
	f := &fakeAihub{items: items, failListPage: 2} // every listing walk dies on page 2
	live := []string{"pf.ieops-1251", "pf.ieops-1252", "pf.ieops-1253"}
	root := workspaceWithWorktrees(t, live...)

	force := map[string]string{}
	for _, d := range live {
		force[d] = "" // named, but with no status stated — the F1 command shape
	}
	res, out := runWorktreeCheck(t, f, root, doctorOpts{fix: true, forceRemove: force})

	for _, d := range live {
		if _, err := os.Stat(filepath.Join(root, d)); err != nil {
			t.Errorf("%s backs an active work item; --force-remove alone must not delete it: %v", d, err)
		}
	}
	// The status has to have been read and printed for each one.
	for _, want := range []string{"status=running", "status=blocked"} {
		if !strings.Contains(out, want) {
			t.Errorf("output never shows %s, so no second hop happened.\noutput:\n%s", want, out)
		}
	}
	if res.Status == "ok" {
		t.Errorf("status = ok while three active worktrees were named for deletion: %s", res.Message)
	}
	if strings.Contains(out, ": removed") {
		t.Errorf("nothing should have been removed.\noutput:\n%s", out)
	}
}

// TestForcingAnActiveWorktreeNeedsTheStatusTranscribed: the graded escape hatch.
// Naming is enough when nothing could be established; when the server says the
// work item is alive, the flag has to carry that status, and it has to match.
func TestForcingAnActiveWorktreeNeedsTheStatusTranscribed(t *testing.T) {
	items := ieopsFixture(20, map[int]string{3: "running"})
	f := &fakeAihub{items: items, hideFromList: map[string]bool{"ieops#1003": true}}

	t.Run("bare name is refused", func(t *testing.T) {
		root := workspaceWithWorktrees(t, "pf.ieops-1003")
		res, out := runWorktreeCheck(t, f, root, doctorOpts{
			fix: true, forceRemove: map[string]string{"pf.ieops-1003": ""}})
		if _, err := os.Stat(filepath.Join(root, "pf.ieops-1003")); err != nil {
			t.Errorf("a bare --force-remove must not delete a running work item's worktree: %v", err)
		}
		if !strings.Contains(out, "must also carry the status") {
			t.Errorf("output must say what is missing.\noutput:\n%s", out)
		}
		if res.Status == "ok" {
			t.Errorf("status = ok: %s", res.Message)
		}
	})

	t.Run("wrong status is refused", func(t *testing.T) {
		root := workspaceWithWorktrees(t, "pf.ieops-1003")
		_, out := runWorktreeCheck(t, f, root, doctorOpts{
			fix: true, forceRemove: map[string]string{"pf.ieops-1003": "paused"}})
		if _, err := os.Stat(filepath.Join(root, "pf.ieops-1003")); err != nil {
			t.Errorf("a mismatched status must not delete: %v", err)
		}
		if !strings.Contains(out, "does not match the current status") {
			t.Errorf("output must say the status did not match.\noutput:\n%s", out)
		}
	})

	t.Run("matching status removes, loudly, and never reports ok", func(t *testing.T) {
		root := workspaceWithWorktrees(t, "pf.ieops-1003")
		res, out := runWorktreeCheck(t, f, root, doctorOpts{
			fix: true, forceRemove: map[string]string{"pf.ieops-1003": "running"}})
		if _, err := os.Stat(filepath.Join(root, "pf.ieops-1003")); !os.IsNotExist(err) {
			t.Errorf("a matching transcribed status should remove it, stat err = %v", err)
		}
		if !strings.Contains(out, "stated its current status") {
			t.Errorf("output must record that it was forced.\noutput:\n%s", out)
		}
		// F2: this is the single most dangerous outcome. It must never be green,
		// or nothing reading the exit code or the icon can see it happened.
		if res.Status != "warning" {
			t.Errorf("status = %q, want warning — a running work item's worktree was just deleted: %s", res.Status, res.Message)
		}
		if !strings.Contains(res.Message, "ONLY because --force-remove") {
			t.Errorf("the summary must record the forced removal: %s", res.Message)
		}
	})
}

// TestRefusalDoesNotHandOutTheBypass: F5. The refusal for an active work item
// used to print `--force-remove=<dir>` verbatim, so the cheapest way past the
// guard was to copy the line the guard printed.
func TestRefusalDoesNotHandOutTheBypass(t *testing.T) {
	items := ieopsFixture(20, map[int]string{3: "running"})
	f := &fakeAihub{items: items, hideFromList: map[string]bool{"ieops#1003": true}}

	// Two refusals reach an active work item, and BOTH have to be checked: the
	// one a plain --fix prints, and the one printed when --force-remove named the
	// directory but stated no status. The second is the subtler hole — the code
	// knows the status by then, so a "helpful" edit can complete the command for
	// the caller and hand over a working bypass.
	for _, tc := range []struct {
		name  string
		force map[string]string
	}{
		{"plain --fix", nil},
		{"--force-remove with no status", map[string]string{"pf.ieops-1003": ""}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := workspaceWithWorktrees(t, "pf.ieops-1003", "pf.stranger")
			_, out := runWorktreeCheck(t, f, root, doctorOpts{fix: true, forceRemove: tc.force})

			if _, err := os.Stat(filepath.Join(root, "pf.ieops-1003")); err != nil {
				t.Fatalf("pf.ieops-1003 backs a running work item and must survive: %v", err)
			}
			// A runnable bypass is one that needs nothing filled in. Naming the
			// flag shape with a `<status>` placeholder is fine; naming it with the
			// real status already substituted is the defect.
			runnable := []string{
				"--force-remove=pf.ieops-1003 ",
				"--force-remove=pf.ieops-1003\n",
				"--force-remove=pf.ieops-1003:running",
			}
			for _, bad := range runnable {
				if strings.Contains(out+"\n", bad) {
					t.Errorf("the refusal for a running work item hands out a runnable bypass (%q).\noutput:\n%s", strings.TrimSpace(bad), out)
				}
			}
			// It still has to say what to do instead.
			if !strings.Contains(out, "Commit or wrap") && !strings.Contains(out, "must also carry the status") {
				t.Errorf("the refusal says nothing actionable.\noutput:\n%s", out)
			}
			// For a name nothing is known about, printing the command IS the right
			// help: the cost of being wrong there is a stale directory.
			if !strings.Contains(out, "--force-remove=pf.stranger") {
				t.Errorf("an unidentifiable directory should come with its cleanup command.\noutput:\n%s", out)
			}
		})
	}
}

// TestFixWillNotDeleteNamesPolyforgeNeverProduced guards the widest deletion
// path: a name with no slug used to be treated as "legacy format" and removed
// with no per-item check at all. Measured before the fix: pf.ieops-1005.bak,
// pf.scratch and pf.aihub-notes were all deleted, each reported as a legacy
// worktree. "pf." is a prefix, not a licence.
func TestFixWillNotDeleteNamesPolyforgeNeverProduced(t *testing.T) {
	f := &fakeAihub{items: ieopsFixture(20, map[int]string{5: "running"})}
	strangers := []string{"pf.ieops-1005.bak", "pf.scratch", "pf.aihub-notes", "pf.ieops-1005.old",
		// 8 base62 characters, so they match the legacy SHAPE but resolve to no
		// work item. Shape alone used to be enough to delete them.
		"pf.salvage1", "pf.BACKUP01", "pf.notes123"}
	root := workspaceWithWorktrees(t, append([]string{"pf.7.aBcD1234", "pf.aBcD1234"}, strangers...)...)

	res, out := runWorktreeCheck(t, f, root, doctorOpts{fix: true})

	for _, d := range strangers {
		if _, err := os.Stat(filepath.Join(root, d)); err != nil {
			t.Errorf("%s is not a name polyforge produces and must not be deleted: %v", d, err)
		}
	}
	// The two real legacy shapes match no active work item here, so they ARE
	// orphans and must still be removed — otherwise this guard is just "delete
	// nothing".
	for _, d := range []string{"pf.7.aBcD1234", "pf.aBcD1234"} {
		if _, err := os.Stat(filepath.Join(root, d)); err != nil {
			t.Errorf("%s is a real legacy shape but resolves to no work item, so nothing is established about it and it must be KEPT without --force-remove: %v", d, err)
		}
	}
	if !strings.Contains(out, "nothing to look up, so no status could be checked") {
		t.Errorf("output must say why the unrecognised names were kept.\noutput:\n%s", out)
	}
	if res.Status != "warning" {
		t.Errorf("status = %q, want warning — four directories were kept", res.Status)
	}
}

// TestLegacyWorktreeBackedByAnActiveWorkItem is the direction no test in this
// file covered, which is why mutating `activeIDs[u]` to `false` left the suite
// green: every fixture used slug-format directories, so the legacy-id match was
// never load-bearing in any assertion.
//
// Both halves are here. The active legacy work item must be kept whether or not
// the listing saw it — via the id map when it did, and via verifyOrphan's
// wi_<ulid8> lookup when it did not — and the finished one must still go.
func TestLegacyWorktreeBackedByAnActiveWorkItem(t *testing.T) {
	// ieopsFixture ids are wi_00001000..., so pf.00001003 is a legitimate
	// pf.<ulid8> directory for wi_00001003.
	items := ieopsFixture(20, map[int]string{3: "running", 4: "paused"})
	items[9].Status = "wrapped"

	t.Run("kept when the listing sees it", func(t *testing.T) {
		f := &fakeAihub{items: items}
		root := workspaceWithWorktrees(t, "pf.00001003", "pf.7.00001004", "pf.00001009")
		runWorktreeCheck(t, f, root, doctorOpts{fix: true})
		for _, d := range []string{"pf.00001003", "pf.7.00001004"} {
			if _, err := os.Stat(filepath.Join(root, d)); err != nil {
				t.Errorf("%s backs an active work item and must be kept: %v", d, err)
			}
		}
		if _, err := os.Stat(filepath.Join(root, "pf.00001009")); !os.IsNotExist(err) {
			t.Errorf("pf.00001009 is wrapped and must still be removed, stat err = %v", err)
		}

		// The MECHANISM, not just the outcome. Both layers keep these
		// directories — the ulid8 match against the listing, and verifyOrphan's
		// wi_<ulid8> lookup — so asserting only "it survived" leaves the first
		// layer unasserted and deleting it looks free. The whole point of the
		// id map is that a directory the listing already accounted for costs no
		// extra request, so assert that no lookup happened for these two.
		f.mu.Lock()
		defer f.mu.Unlock()
		for _, id := range []string{"wi_00001003", "wi_00001004"} {
			if slices.Contains(f.getSlugs, id) {
				t.Errorf("%s was looked up individually even though the active listing already carried it; "+
					"the ulid8 match against the listing is not being used (queries: %v)", id, f.getSlugs)
			}
		}
		if !slices.Contains(f.getSlugs, "wi_00001009") {
			t.Errorf("wi_00001009 was NOT looked up before deletion; every removal needs its own hop (queries: %v)", f.getSlugs)
		}
	})

	t.Run("kept by the per-item hop when the listing misses it", func(t *testing.T) {
		// The listing does not mention them at all, so activeIDs cannot help and
		// only the wi_<ulid8> lookup can.
		f := &fakeAihub{items: items, hideFromList: map[string]bool{
			"ieops#1003": true, "ieops#1004": true}}
		root := workspaceWithWorktrees(t, "pf.00001003", "pf.7.00001004")
		res, out := runWorktreeCheck(t, f, root, doctorOpts{fix: true})
		for _, d := range []string{"pf.00001003", "pf.7.00001004"} {
			if _, err := os.Stat(filepath.Join(root, d)); err != nil {
				t.Errorf("%s: the listing missed it, but wi_ lookup resolves it as active — must be kept: %v", d, err)
			}
		}
		if !strings.Contains(out, "wi=wi_00001003") {
			t.Errorf("the legacy directory must be looked up as wi_00001003.\noutput:\n%s", out)
		}
		for _, want := range []string{"status=running", "status=paused"} {
			if !strings.Contains(out, want) {
				t.Errorf("output must carry %s.\noutput:\n%s", want, out)
			}
		}
		if res.Status == "ok" {
			t.Errorf("status = ok: %s", res.Message)
		}
	})
}

func TestWorktreeLookupKey(t *testing.T) {
	cases := map[string]string{
		"pf.aihub-307":        "aihub#307",
		"pf.global-routing-4": "global-routing#4",
		"pf.aBcD1234":         "wi_aBcD1234",
		"pf.7.aBcD1234":       "wi_aBcD1234",
		"pf.1234.00000000":    "wi_00000000",
		"pf.salvage1":         "wi_salvage1", // 8 base62: a lookup, not a licence
		"pf.scratch":          "",
		"pf.aihub-notes":      "",
		"pf.ieops-274.bak":    "",
		"pf.aBcD123":          "",
		"pf.aBcD12345":        "",
		"pf.x.aBcD1234":       "",
		"pf.":                 "",
	}
	for name, want := range cases {
		if got := worktreeLookupKey(name); got != want {
			t.Errorf("worktreeLookupKey(%q) = %q, want %q", name, got, want)
		}
	}
}

func TestWorktreeULID8Shape(t *testing.T) {
	cases := map[string]bool{
		"pf.aBcD1234":      true, // pf.<ulid8>
		"pf.7.aBcD1234":    true, // pf.<seq>.<ulid8>
		"pf.1234.00000000": true,
		"pf.aBcD123":       false, // 7 chars
		"pf.aBcD12345":     false, // 9 chars
		"pf.scratch":       false,
		"pf.aihub-notes":   false,
		"pf.ieops-274.bak": false,
		"pf.ieops-274":     false, // slug format, handled by worktreeSlug
		"pf.x.aBcD1234":    false, // seq must be digits
		"pf.aBcD_234":      false, // base62 only
		"pf.7.8.aBcD1234":  false,
		"pf.":              false,
		"notpf.aBcD1234":   false,
	}
	for name, want := range cases {
		if got := worktreeULID8(name) != ""; got != want {
			t.Errorf("worktreeULID8(%q) != \"\" = %v, want %v", name, got, want)
		}
	}
}

// TestRemovalFailureIsNotReportedAsRemoved: os.RemoveAll's error used to be
// discarded, so a directory that could not be deleted still produced
// "removed N orphan worktrees" and an [ok].
func TestRemovalFailureIsNotReportedAsRemoved(t *testing.T) {
	items := ieopsFixture(20, nil)
	items[9].Status = "wrapped"
	f := &fakeAihub{items: items}
	root := workspaceWithWorktrees(t, "pf.ieops-1009")

	// A deletion that silently does nothing. This has to be injected rather than
	// induced with a read-only parent: these tests run as root in CI, where
	// neither mode bits nor a read-only parent stop an unlink, so the guard would
	// only ever be exercised on someone's laptop.
	orig := deleteWorktreeDir
	deleteWorktreeDir = func(context.Context, string, string) {}
	t.Cleanup(func() { deleteWorktreeDir = orig })

	res, out := runWorktreeCheck(t, f, root, doctorOpts{fix: true})

	if _, err := os.Stat(filepath.Join(root, "pf.ieops-1009")); err != nil {
		t.Fatalf("fixture broken — the directory should still be there: %v", err)
	}
	if !strings.Contains(out, "REMOVAL FAILED") || !strings.Contains(res.Message, "REMOVAL FAILED") {
		t.Errorf("the directory is still on disk but nothing said the removal failed.\nmessage: %s\noutput:\n%s", res.Message, out)
	}
	if strings.Contains(out, "pf.ieops-1009: removed") {
		t.Errorf("output claims a removal that did not happen.\noutput:\n%s", out)
	}
	if res.Status == "ok" {
		t.Errorf("status = ok while a directory it claims to have removed is still there: %s", res.Message)
	}
}

// TestRemoveWorktreeDirReportsSuccessOnlyWhenGone pins the primitive itself, so
// the injected-failure test above cannot be the only thing holding the contract.
func TestRemoveWorktreeDirReportsSuccessOnlyWhenGone(t *testing.T) {
	root := workspaceWithWorktrees(t, "pf.ieops-1", "pf.ieops-2")

	if err := removeWorktreeDir(context.Background(), root, "pf.ieops-1"); err != nil {
		t.Errorf("a real removal should report success: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "pf.ieops-1")); !os.IsNotExist(err) {
		t.Errorf("pf.ieops-1 should be gone, stat err = %v", err)
	}
	// Removing something that was never there is success, not a failure: the
	// contract is "it is not on disk", not "this call did the deleting".
	if err := removeWorktreeDir(context.Background(), root, "pf.never-existed"); err != nil {
		t.Errorf("an already-absent directory should not be an error: %v", err)
	}

	orig := deleteWorktreeDir
	deleteWorktreeDir = func(context.Context, string, string) {}
	t.Cleanup(func() { deleteWorktreeDir = orig })
	if err := removeWorktreeDir(context.Background(), root, "pf.ieops-2"); err == nil {
		t.Error("the directory is still on disk; removeWorktreeDir must not report success")
	}
}

func TestFixKeepsWorktreesWhoseWorkItemCannotBeRead(t *testing.T) {
	// The whole workspace pointed at an aihub that has never heard of it — the
	// realistic cause of a 404 on every slug (a switched [server] url). Deleting
	// on 404 empties the workspace in one command.
	f := &fakeAihub{items: ieopsFixture(3, nil)}
	root := workspaceWithWorktrees(t, "pf.ieops-7001", "pf.ieops-7002")

	_, out := runWorktreeCheck(t, f, root, doctorOpts{fix: true})

	for _, d := range []string{"pf.ieops-7001", "pf.ieops-7002"} {
		if _, err := os.Stat(filepath.Join(root, d)); err != nil {
			t.Errorf("%s: its work item could not be read, so it must be kept: %v", d, err)
		}
	}
	if !strings.Contains(out, "KEPT") {
		t.Errorf("output must say the directories were kept.\noutput:\n%s", out)
	}
}

func TestForceRemoveNameThatMatchesNothingIsReported(t *testing.T) {
	// Everything on disk is live, so there is no candidate at all. A --force-remove
	// that quietly does nothing reads as "it was already cleaned up".
	items := ieopsFixture(20, map[int]string{5: "running"})
	f := &fakeAihub{items: items}
	root := workspaceWithWorktrees(t, "pf.ieops-1005")

	res, _ := runWorktreeCheck(t, f, root, doctorOpts{
		fix:         true,
		forceRemove: map[string]string{"pf.ieops-1005": "running", "pf.typo-1": ""},
	})

	if _, err := os.Stat(filepath.Join(root, "pf.ieops-1005")); err != nil {
		t.Errorf("pf.ieops-1005 is live and was never a candidate; --force-remove must not reach it: %v", err)
	}
	for _, want := range []string{"pf.ieops-1005", "pf.typo-1"} {
		if !strings.Contains(res.Message, want) {
			t.Errorf("--force-remove named %s and it matched nothing; that has to be reported.\nmessage: %s", want, res.Message)
		}
	}
	if res.Status != "warning" {
		t.Errorf("status = %q, want warning — an acknowledgement went unused", res.Status)
	}
}

func TestNoClientIsReportedAsDidNotLook(t *testing.T) {
	root := workspaceWithWorktrees(t, "pf.ieops-1001")
	var buf bytes.Buffer
	res := checkWorktrees(context.Background(), nil, ieopsCfg(), root, doctorOpts{forceRemove: map[string]string{}}, &buf)
	if res.Status == "ok" {
		t.Errorf("with no aihub client nothing was cross-referenced; reporting ok is 'did not look' dressed as 'found nothing'.\nmessage: %s", res.Message)
	}
	// The exact clean-bill phrasing, not the words "none orphaned" — the warning
	// message legitimately quotes that phrase to say it is NOT what it means.
	if strings.Contains(res.Message, "1 worktrees, none orphaned") {
		t.Errorf("message gives the clean bill without having checked: %s", res.Message)
	}
	if !strings.Contains(res.Message, "could not be cross-referenced") {
		t.Errorf("message does not say why it could not answer: %s", res.Message)
	}
}

// ─── name parsing ──────────────────────────────────────────────────────────

func TestWorktreeNameParsing(t *testing.T) {
	cases := []struct{ dir, project, slug string }{
		{"pf.aihub-307", "aihub", "aihub#307"},
		{"pf.global-routing-125", "global-routing", "global-routing#125"},
		{"pf.polyforge-scenario-4", "polyforge-scenario", "polyforge-scenario#4"},
		{"pf.ieops-274", "ieops", "ieops#274"},
		{"pf.01ks510z", "", ""},    // legacy pf.<ulid8>
		{"pf.12.01ks510z", "", ""}, // previous pf.<seq>.<ulid8>
		{"pf.aihub-", "", ""},      // malformed
		{"pf.aihub-30a", "", ""},   // seq must be digits
	}
	for _, c := range cases {
		if got := worktreeProject(c.dir); got != c.project {
			t.Errorf("worktreeProject(%q) = %q, want %q", c.dir, got, c.project)
		}
		if got := worktreeSlug(c.dir); got != c.slug {
			t.Errorf("worktreeSlug(%q) = %q, want %q", c.dir, got, c.slug)
		}
	}
}

// ─── argument parsing ──────────────────────────────────────────────────────

func TestParseDoctorArgs(t *testing.T) {
	if o, err := parseDoctorArgs(nil); err != nil || o.fix {
		t.Errorf("no args: got fix=%v err=%v", o.fix, err)
	}
	if o, err := parseDoctorArgs([]string{"--fix"}); err != nil || !o.fix {
		t.Errorf("--fix: got fix=%v err=%v", o.fix, err)
	}
	o, err := parseDoctorArgs([]string{"--fix", "--force-remove=pf.a,pf.b:running", "--force-remove", "pf.c"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := map[string]string{"pf.a": "", "pf.b": "running", "pf.c": ""}
	for d, st := range want {
		got, ok := o.forceRemove[d]
		if !ok || got != st {
			t.Errorf("--force-remove[%s] = %q,%v; want %q (got %v)", d, got, ok, st, o.forceRemove)
		}
	}
	// An unrecognised flag must not be silently ignored: the old parser only
	// looked at args[0], so `doctor --dry-run --fix` ran the read-only path and
	// `doctor --fixx` did nothing, both without a word.
	for _, args := range [][]string{
		{"--fixx"},
		{"--dry-run", "--fix"},
		{"--force-remove=pf.a"},
		{"--fix", "--force-remove"},
		// A flag consumed as a value: this used to record a directory literally
		// named "--fix" and swallow the real flag, silently.
		{"--force-remove", "--fix"},
		{"--fix", "--force-remove=--fix"},
		// An acknowledgement that names nothing: accepted silently before, which
		// is the same "it went unused" silence unmatched names are reported for.
		{"--fix", "--force-remove="},
		{"--fix", "--force-remove=,,,"},
		{"--fix", "--force-remove", "  "},
		// A status that is not a status, and a value with no directory.
		{"--fix", "--force-remove=pf.a:runnning"},
		{"--fix", "--force-remove=pf.a:"},
		{"--fix", "--force-remove=:running"},
	} {
		if _, err := parseDoctorArgs(args); err == nil {
			t.Errorf("parseDoctorArgs(%v) accepted an argument it cannot honour", args)
		}
	}
	// --help must not be an error: it used to run the read-only check, and after
	// the parser got strict it would have exited 2 on a request for usage.
	h, err := parseDoctorArgs([]string{"--help"})
	if err != nil || !h.help {
		t.Errorf("--help: got help=%v err=%v", h.help, err)
	}
	if h, err := parseDoctorArgs([]string{"-h"}); err != nil || !h.help {
		t.Errorf("-h: got help=%v err=%v", h.help, err)
	}
}

// ─── the --apply advice ────────────────────────────────────────────────────

// TestReposFixCmdDoesNotRecommendApply guards the second defect in aihub#307:
// checkRepos used to print `polyforge init --apply`, and --apply returns out of
// RunInit before the clone loop, before the repo map and before CLAUDE.md. It is
// advice that cannot work, phrased so that following it looks like success.
func TestReposFixCmdDoesNotRecommendApply(t *testing.T) {
	root := t.TempDir()
	cfg := &config.Config{Projects: map[string]config.Project{
		"ieops": {Repos: []config.Repo{{Name: "definitely-not-cloned", URL: "git@example.com:x/y.git"}}},
	}}
	res := checkRepos(root, cfg)
	if res.Status != "warning" {
		t.Fatalf("status = %q, want warning (the repo is missing)", res.Status)
	}
	if strings.Contains(res.FixCmd, "--apply") {
		t.Errorf("fix advice still names --apply, which is a no-op: %q", res.FixCmd)
	}
	if !strings.Contains(res.FixCmd, "polyforge init") {
		t.Errorf("fix advice should name `polyforge init`: %q", res.FixCmd)
	}
}

// ─── the cursor walk's own failure branches ────────────────────────────────
//
// These four branches all implement the same rule — "return an error, never the
// short list" — and three of them were dead code to this suite. The fake's cursor
// was a monotonic offset and 260 items fit inside two pages, so a repeated
// cursor, an endless cursor and a missing items array were never produced. That
// matters more than ordinary coverage: each unasserted branch is a way for
// "truncated" to be read as "complete", which is how "not in the list" becomes
// "delete this directory".

func TestCursorWalkFailuresNeverTruncate(t *testing.T) {
	// pf.ieops-1005 backs a running work item that lives on page 2+ of every
	// walk, so a truncated result would nominate it for deletion.
	items := ieopsFixture(260, map[int]string{111: "running"})
	dirs := []string{"pf.ieops-1111", "pf.ieops-1240"}

	for _, tc := range []struct {
		name string
		fake *fakeAihub
		want string
	}{
		{"repeated cursor", &fakeAihub{items: items, repeatCursor: true}, "repeated cursor"},
		{"cursor that never ends", &fakeAihub{items: items, neverEndCursor: true}, "did not terminate"},
		{"response with no items array", &fakeAihub{items: items, omitItems: true}, "carries no items array"},
		{"next_cursor of the wrong type", &fakeAihub{items: items, badCursorType: true}, "not a string or null"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			// The listing must fail, not come back short.
			if _, err := fetchActiveWorkItems(context.Background(), tc.fake.start(t), "ieops"); err == nil {
				t.Fatalf("fetchActiveWorkItems returned no error; a walk that cannot prove it finished must not return its partial result")
			} else if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("error %q does not mention %q", err, tc.want)
			}

			// And that failure must reach the check as "unverifiable", never as
			// an orphan list.
			root := workspaceWithWorktrees(t, dirs...)
			res, out := runWorktreeCheck(t, tc.fake, root, doctorOpts{fix: true})
			for _, d := range dirs {
				if _, err := os.Stat(filepath.Join(root, d)); err != nil {
					t.Errorf("%s was deleted after a listing that could not complete: %v", d, err)
				}
			}
			if strings.Contains(out, ": removed") {
				t.Errorf("something was removed after an incomplete listing.\noutput:\n%s", out)
			}
			if res.Status == "ok" {
				t.Errorf("status = ok after an incomplete listing: %s", res.Message)
			}
		})
	}
}

// ─── the git limb ──────────────────────────────────────────────────────────

// TestDeleteWorktreeDirDeregistersRealWorktrees covers the branch that had zero
// coverage anywhere in the suite, and was broken because of it.
//
// The old call was `git -C <wsRoot> worktree remove --force pf.<slug>` — wsRoot
// is not a repository and pf.<slug>/ is not a worktree, so it exited 128 every
// time and rm -rf was always the path that actually ran, leaving a prunable
// registration behind. The previous test used t.TempDir(), which is not a git
// repo, so it could not tell the difference.
func TestDeleteWorktreeDirDeregistersRealWorktrees(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Fatalf("git is required for this test and must not be skipped away: %v", err)
	}
	root := t.TempDir()
	repo := filepath.Join(root, ".repo", "demo")
	if err := os.MkdirAll(repo, 0o755); err != nil {
		t.Fatal(err)
	}
	run := func(dir string, args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t",
			"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	run(repo, "init", "-q", "-b", "main", ".")
	run(repo, "commit", "-q", "--allow-empty", "-m", "init")
	wtPath := filepath.Join(root, "pf.demo-1", "demo")
	run(repo, "worktree", "add", "-q", "-b", "task", wtPath)

	listed := func() string {
		cmd := exec.Command("git", "worktree", "list")
		cmd.Dir = repo
		out, err := cmd.Output()
		if err != nil {
			t.Fatalf("worktree list: %v", err)
		}
		return string(out)
	}
	if !strings.Contains(listed(), wtPath) {
		t.Fatalf("fixture broken: %s is not registered\n%s", wtPath, listed())
	}

	if err := removeWorktreeDir(context.Background(), root, "pf.demo-1"); err != nil {
		t.Fatalf("removeWorktreeDir: %v", err)
	}
	if strings.Contains(listed(), wtPath) {
		t.Errorf("the worktree is still registered, so only rm -rf ran:\n%s", listed())
	}
	if _, err := os.Stat(filepath.Join(root, "pf.demo-1")); !os.IsNotExist(err) {
		t.Errorf("pf.demo-1 should be gone, stat err = %v", err)
	}
	// The branch holds the work and polyforge pushes it: removing a checkout is
	// not a reason to destroy it.
	cmd := exec.Command("git", "branch", "--list", "task")
	cmd.Dir = repo
	if out, _ := cmd.Output(); !strings.Contains(string(out), "task") {
		t.Errorf("the task branch was destroyed along with the checkout: %q", out)
	}
}
