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
	"path/filepath"
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
// an idealised list endpoint: the limit is capped at 200 and the cap FAILS OPEN,
// so limit<=0 (i.e. "no limit param") and limit>200 both silently become 50
// (aihub#267, internal/domain/work_items.go).
//
// Measured against production on 2026-08-31, project ieops, 127 open items:
//
//	limit absent -> 50 rows + next_cursor
//	limit=50     -> 50 rows + next_cursor
//	limit=200    -> 127 rows, no next_cursor
//	limit=500    -> 50 rows + next_cursor   <- the "just ask for a big number" fix
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
	if end < len(matched) {
		body["next_cursor"] = strconv.Itoa(end)
	} else {
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
		opts.forceRemove = map[string]bool{}
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
	items := ieopsFixture(260, nil)
	items[240].Status = "wrapped"
	f := &fakeAihub{
		items: items,
		// pf.ieops-1111 is running, and the listing does not mention it. That is
		// the aihub#307 shape after the pagination fix: any FUTURE way of losing
		// rows lands here, and the per-item re-check has to catch it.
		hideFromList: map[string]bool{"ieops#1111": true},
	}
	items[111].Status = "running"

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
	if !strings.Contains(out, "--force-remove") {
		t.Errorf("--fix must tell the caller how to override a refusal.\noutput:\n%s", out)
	}
	if res.Status != "warning" {
		t.Errorf("status = %q, want warning — something was kept", res.Status)
	}
}

func TestForceRemoveIsPerDirectory(t *testing.T) {
	items := ieopsFixture(20, nil)
	items[3].Status = "running"
	items[4].Status = "running"
	f := &fakeAihub{items: items, hideFromList: map[string]bool{"ieops#1003": true, "ieops#1004": true}}

	root := workspaceWithWorktrees(t, "pf.ieops-1003", "pf.ieops-1004")
	_, out := runWorktreeCheck(t, f, root, doctorOpts{
		fix:         true,
		forceRemove: map[string]bool{"pf.ieops-1003": true},
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
	f := &fakeAihub{items: ieopsFixture(260, nil), failListPage: 2}
	root := workspaceWithWorktrees(t, "pf.ieops-1001", "pf.ieops-9999", "pf.deadbeef")

	res, _ := runWorktreeCheck(t, f, root, doctorOpts{})

	if strings.Contains(res.Message, "orphan worktrees:") {
		t.Errorf("a failed listing must not nominate anything for deletion.\nmessage: %s", res.Message)
	}
	for _, d := range []string{"pf.ieops-1001", "pf.ieops-9999", "pf.deadbeef"} {
		if !strings.Contains(res.Message, d) {
			t.Errorf("%s should be reported as unverifiable, not silently dropped.\nmessage: %s", d, res.Message)
		}
	}
	if res.Status != "warning" {
		t.Errorf("status = %q, want warning", res.Status)
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
		forceRemove: map[string]bool{"pf.ieops-1005": true, "pf.typo-1": true},
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
	res := checkWorktrees(context.Background(), nil, ieopsCfg(), root, doctorOpts{forceRemove: map[string]bool{}}, &buf)
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
	o, err := parseDoctorArgs([]string{"--fix", "--force-remove=pf.a,pf.b", "--force-remove", "pf.c"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	for _, d := range []string{"pf.a", "pf.b", "pf.c"} {
		if !o.forceRemove[d] {
			t.Errorf("--force-remove did not record %s (got %v)", d, o.forceRemove)
		}
	}
	// An unrecognised flag must not be silently ignored: the old parser only
	// looked at args[0], so `doctor --dry-run --fix` ran the read-only path and
	// `doctor --fixx` did nothing, both without a word.
	for _, args := range [][]string{{"--fixx"}, {"--dry-run", "--fix"}, {"--force-remove=pf.a"}, {"--fix", "--force-remove"}} {
		if _, err := parseDoctorArgs(args); err == nil {
			t.Errorf("parseDoctorArgs(%v) accepted an argument it cannot honour", args)
		}
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
