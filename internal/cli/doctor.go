package cli

import (
	"context"
	"fmt"
	"io"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"sort"
	"strconv"
	"strings"

	"github.com/GMISWE/ieops-aihub/internal/coding"
	"github.com/GMISWE/ieops-aihub/internal/config"
	"github.com/GMISWE/ieops-aihub/pkg/client"
)

type checkResult struct {
	Name    string
	Status  string // "ok", "warning", "error"
	Message string
	FixCmd  string
}

// doctorOpts is the parsed argument list. Unknown flags are rejected rather than
// ignored: the previous parser was `args[0] == "--fix"`, so `doctor --dry-run --fix`
// silently ran the read-only path and `doctor --fixx` silently did nothing. In a
// command whose --fix branch deletes directories, "the flag you typed was not the
// flag that ran" is not a cosmetic problem.
type doctorOpts struct {
	fix  bool
	help bool
	// forceRemove maps a worktree directory the caller has explicitly named to
	// the work-item status they claim to have seen ("" when they stated none).
	//
	// The acknowledgement is the NAME, not the count: this is never a blanket
	// --force over whatever the scan happened to select. An earlier version of
	// this comment claimed the flag took one directory at a time, three lines
	// under a usage string advertising `<dir>[,<dir>]` — the claim was false and
	// a guard documented as something it is not is worse than no guard.
	//
	// What actually escalates with danger is the VALUE. A directory whose work
	// item is still active can only be forced as `<dir>:<status>`, and the status
	// has to match what the server reports at that moment. That cannot be typed
	// from the refusal message alone; it has to be looked up, and if the work
	// item moves on in the meantime the removal stops.
	forceRemove map[string]string
}

const doctorUsage = `polyforge doctor [--fix] [--force-remove=<dir>[:<status>][,...]]

  --fix                    Remove orphan worktrees whose work item is provably
                           terminal (wrapped/failed/cancelled). Every other
                           directory is printed with its status and KEPT.

  --force-remove=<dir>     Remove <dir> even though --fix would not: its work
                           item could not be read, does not exist, or the name is
                           not one polyforge produces. Requires --fix.

  --force-remove=<dir>:<status>
                           Required instead when <dir>'s work item is still
                           active (running/paused/queued/blocked): <status> must
                           equal the status the server reports for it right now,
                           or the removal is refused. A running work item's
                           worktree holds uncommitted work by definition, so this
                           asks you to have looked it up rather than to have
                           copied a command out of a warning.

  --help, -h               Print this.`

func parseDoctorArgs(args []string) (doctorOpts, error) {
	opts := doctorOpts{forceRemove: map[string]string{}}
	addDirs := func(v string) error {
		if strings.TrimSpace(v) == "" || strings.Trim(v, " ,") == "" {
			// An acknowledgement that names nothing is the same "it went unused"
			// silence unmatched names are reported for.
			return fmt.Errorf("--force-remove was given no worktree directory name")
		}
		for _, item := range strings.Split(v, ",") {
			item = strings.TrimSpace(item)
			if item == "" {
				continue
			}
			// A value that looks like a flag is a missing argument, not a
			// directory: `--force-remove --fix` used to record a directory
			// literally named "--fix" and swallow the real flag.
			if strings.HasPrefix(item, "-") {
				return fmt.Errorf("--force-remove got %q, which looks like a flag rather than a worktree directory", item)
			}
			dir, status, hadColon := strings.Cut(item, ":")
			dir, status = strings.TrimSpace(dir), strings.TrimSpace(status)
			if dir == "" {
				return fmt.Errorf("--force-remove got %q, which has no directory name before the ':'", item)
			}
			if hadColon && status == "" {
				return fmt.Errorf("--force-remove=%s has a ':' but no status after it", item)
			}
			if status != "" && !isKnownWIStatus(status) {
				return fmt.Errorf("--force-remove=%s: %q is not a work item status (one of %s)",
					item, status, strings.Join(allWIStatuses, ", "))
			}
			opts.forceRemove[dir] = status
		}
		return nil
	}
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch {
		case a == "--fix":
			opts.fix = true
		case a == "--help" || a == "-h":
			opts.help = true
		case strings.HasPrefix(a, "--force-remove="):
			if err := addDirs(strings.TrimPrefix(a, "--force-remove=")); err != nil {
				return opts, err
			}
		case a == "--force-remove":
			if i+1 >= len(args) {
				return opts, fmt.Errorf("--force-remove needs a worktree directory name")
			}
			i++
			if err := addDirs(args[i]); err != nil {
				return opts, err
			}
		default:
			return opts, fmt.Errorf("unknown argument %q\n\n%s", a, doctorUsage)
		}
	}
	if opts.help {
		return opts, nil
	}
	if len(opts.forceRemove) > 0 && !opts.fix {
		return opts, fmt.Errorf("--force-remove only means anything together with --fix")
	}
	return opts, nil
}

// RunDoctor runs 8 diagnostic checks and reports their status.
// With --fix: attempts to auto-repair fixable issues.
//
// Checks (§12.1):
//  1. workspace       – can locate .polyforge.yaml from wsRoot
//  2. config          – ~/.polyforge/config not required in v1, checks aihub reachability
//  3. repos           – .repo/<name>/ exist and match .polyforge.yaml remotes
//  4. worktrees       – pf.<project>-<seq>/ list vs server wi list; flag orphans
//  5. branch-upstream – task worktrees whose branch tracks main/master/dev/tot
//  6. version         – GET /v1/version; compare min_client_version vs local binary
//  7. claude_md       – CLAUDE.md managed block format + .polyforge/repo-map/ presence
//  8. usage_md        – .polyforge/usage.md still carrying rules using-polyforge owns
//
// ⚠️ --fix does NOT act on check 5. Its FixCmd is a command for a human to run
// per worktree; see checkBranchUpstreams for why a sweep is the wrong shape.
func RunDoctor(ctx context.Context, c *client.Client, cfg *config.Config, wsRoot string, args []string) {
	opts, err := parseDoctorArgs(args)
	if err != nil {
		fmt.Fprintf(os.Stderr, "polyforge doctor: %v\n", err)
		os.Exit(2)
	}
	if opts.help {
		fmt.Println(doctorUsage)
		return
	}

	// Each check is run and printed before the next one starts, so the
	// per-worktree lines --fix streams land next to the [ok]/[warn] worktrees
	// line they belong to. Building the whole slice first put them at the very
	// top of the report, above the first check, formatted like continuations of
	// something several lines away.
	checks := []func() checkResult{
		func() checkResult { return checkWorkspace(wsRoot, cfg) },
		func() checkResult { return checkConfig(ctx, c) },
		func() checkResult { return checkRepos(wsRoot, cfg) },
		func() checkResult { return checkWorktrees(ctx, c, cfg, wsRoot, opts, os.Stdout) },
		func() checkResult { return checkBranchUpstreams(ctx, cfg, wsRoot) },
		func() checkResult { return checkVersion(ctx, c) },
		func() checkResult { return checkClaudeMd(wsRoot) },
		func() checkResult { return checkUsageMd(wsRoot) },
	}

	allOk := true
	for _, run := range checks {
		ch := run()
		icon := "ok"
		if ch.Status == "warning" {
			icon = "warn"
		}
		if ch.Status == "error" {
			icon = "FAIL"
			allOk = false
		}
		fmt.Printf("[%s] %s: %s\n", icon, ch.Name, ch.Message)
		if ch.FixCmd != "" && !opts.fix {
			fmt.Printf("       fix: %s\n", ch.FixCmd)
		}
	}
	if !allOk {
		os.Exit(1)
	}
}

// checkWorkspace verifies that .polyforge.yaml was found.
func checkWorkspace(wsRoot string, cfg *config.Config) checkResult {
	if cfg == nil {
		return checkResult{
			Name:    "workspace",
			Status:  "error",
			Message: fmt.Sprintf(".polyforge.yaml not found in %s", wsRoot),
			FixCmd:  "polyforge init",
		}
	}
	return checkResult{
		Name:    "workspace",
		Status:  "ok",
		Message: ".polyforge.yaml found",
	}
}

// checkConfig verifies aihub reachability via GET /v1/health — and, since
// aihub#316, reads what that endpoint's BODY says about the server's
// dependencies.
//
// It used to decode the response into a map and then read nothing out of it,
// so the one first-party diagnostic printed "[ok] config: aihub reachable"
// against a server whose database ping was failing and whose embedding backend
// had been dead for hours. That silence is what aihub#316 exists to end, and
// the server-side half of it — a "degraded" status plus per-dependency fields —
// is only worth emitting if something reads it.
//
// There is deliberately no non-2xx to notice: /v1/health answers 200 in every
// branch (see handleHealth in internal/server/router.go), because container
// liveness probes and this very check treat its reachability as liveness. The
// verdict is in the body or nowhere.
func checkConfig(ctx context.Context, c *client.Client) checkResult {
	if c == nil {
		return checkResult{
			Name:    "config",
			Status:  "warning",
			Message: "aihub client not configured (POLYFORGE_API_KEY / POLYFORGE_AIHUB_URL not set)",
		}
	}
	var health map[string]any
	if err := c.Health(ctx, &health); err != nil {
		return checkResult{
			Name:    "config",
			Status:  "error",
			Message: fmt.Sprintf("aihub unreachable: %v", err),
		}
	}
	return healthVerdict(health)
}

// healthBoolField reads a /v1/health boolean that MAY NOT BE THERE, and that
// distinction is the whole backward-compatibility problem in one function.
//
// A server older than aihub#316 answers with only {status, version, db_ok} — no
// embedding fields at all — and in Go an absent JSON key and an explicit false
// both land on the same zero value, so decoding into a plain bool would read
// every older server as "embedding backend down" and warn on all of them.
//
// nil therefore means "this server does not report it", which is not a finding.
// A key that is present but not a JSON boolean is also nil, and is reported
// separately as unreadable rather than as false: "could not tell" and "fine"
// have to stay different answers here, same as everywhere else in this file.
func healthBoolField(m map[string]any, key string) (val *bool, malformed bool) {
	v, present := m[key]
	if !present || v == nil {
		return nil, false
	}
	b, ok := v.(bool)
	if !ok {
		return nil, true
	}
	return &b, false
}

// healthVerdict turns a decoded /v1/health body into the config check result.
//
// Split out of checkConfig as a pure function on purpose: every interesting
// input is a shape of JSON — new/degraded, new/ok, old server with no embedding
// fields, old server whose hardcoded status="ok" contradicts its own
// db_ok=false — and none of them need a socket or a database to construct.
//
// `status` alone is NOT sufficient evidence, in either direction. Before
// aihub#316 it was the literal "ok" regardless of db_ok, so an old server with a
// failing database says "ok"; hence db_ok is checked on its own. And a future
// server may degrade on a dependency this client has no field for, so a
// "degraded" that names nothing recognised is still reported rather than
// swallowed.
//
// No FixCmd: nothing `doctor --fix` can run repairs a dead database or a dead
// embedding backend, and printing a command that cannot fix what it is offered
// for is the defect checkRepos already had to unlearn (aihub#307).
func healthVerdict(health map[string]any) checkResult {
	const name = "config"

	status, _ := health["status"].(string)
	dbOK, dbBad := healthBoolField(health, "db_ok")
	embEnabled, embEnabledBad := healthBoolField(health, "embedding_enabled")
	embOK, embOKBad := healthBoolField(health, "embedding_ok")
	errKind, _ := health["embedding_error_kind"].(string)

	var problems []string
	if dbOK != nil && !*dbOK {
		problems = append(problems, "database: ping failed (db_ok=false)")
	}
	if embEnabled != nil && *embEnabled && embOK != nil && !*embOK {
		p := "embedding backend: probe failed"
		if errKind != "" {
			p += " (" + errKind + ")"
		}
		problems = append(problems, p)
	}
	if len(problems) == 0 && status == "degraded" {
		problems = append(problems, "server reports status=degraded but names no dependency "+
			"this client knows how to read — check the server log")
	}

	var unreadable []string
	for _, f := range []struct {
		key string
		bad bool
	}{{"db_ok", dbBad}, {"embedding_enabled", embEnabledBad}, {"embedding_ok", embOKBad}} {
		if f.bad {
			unreadable = append(unreadable, f.key)
		}
	}

	// Context that is not a finding but is worth printing, because "the server
	// never told me" and "the server told me it is fine" look identical in a
	// one-line green report otherwise.
	var notes []string
	switch {
	case embEnabled == nil:
		notes = append(notes, "server does not report embedding health (predates aihub#316)")
	case !*embEnabled:
		notes = append(notes, "embedding disabled")
	}

	suffix := ""
	if len(notes) > 0 {
		suffix = " (" + strings.Join(notes, "; ") + ")"
	}

	switch {
	case len(problems) > 0:
		msg := "aihub reachable but degraded — " + strings.Join(problems, "; ") + suffix
		if len(unreadable) > 0 {
			msg += fmt.Sprintf("; also could not read %s from the health body", strings.Join(unreadable, ", "))
		}
		return checkResult{Name: name, Status: "warning", Message: msg}
	case len(unreadable) > 0:
		return checkResult{Name: name, Status: "warning",
			Message: fmt.Sprintf("aihub reachable, but %s came back as a non-boolean, so its health "+
				"could not be read — this is 'did not look', not 'looked and found nothing'%s",
				strings.Join(unreadable, ", "), suffix)}
	}
	return checkResult{Name: name, Status: "ok", Message: "aihub reachable" + suffix}
}

// upstreamOffender is one task worktree configured to push to a protected
// branch.
type upstreamOffender struct {
	Repo     string // the .repo/<name> the worktree belongs to
	Worktree string // absolute path of the linked worktree
	Branch   string // the branch it has checked out
	Upstream string // the protected upstream, as `<remote>/<branch>`
}

// checkBranchUpstreams reports task worktrees whose checked-out branch tracks a
// protected branch — the state in which a bare `git push` can land on main.
//
// WHY IT IS A SEPARATE CHECK FROM `worktrees`. That one asks whether a
// directory should still exist. This one asks whether the branch inside it is
// aimed somewhere dangerous, which is orthogonal: a perfectly live worktree of
// a running work item is exactly the case that matters most here.
//
// WHAT IT COUNTS, stated because three different numbers were available and two
// of them are the wrong instrument (measured 2026-09-03 in this workspace):
//
//   - 5,580 LOCAL BRANCHES across the 45 clones track origin/main. Most belong
//     to no worktree at all. A branch that is not checked out anywhere cannot
//     receive a bare `git push`, so counting these reports a hazard four times
//     larger than the one that exists, and buries the live cases in it.
//   - 273 worktrees exist counting each clone's own main worktree. Those are
//     legitimately on main tracking origin/main and must never be flagged,
//     which is what the branch != remoteBranch guard in
//     GitClearProtectedUpstream is for.
//   - 227 LINKED worktrees are on a branch, and ~198 of those branches track
//     origin/main. That is the hazard surface, and it is what this counts.
//
// ⚠️ THAT LAST NUMBER IS DELIBERATELY APPROXIMATE. Two measurements an hour
// apart on the same day gave 199 and then 198 — a worktree's branch acquired
// its own upstream in between, because the workspace is live and other people
// are working in it. A comment that pinned an exact figure would be wrong by
// the afternoon; the whole reason this is a check and not a docs table is that
// the count has to be taken, not remembered.
//
// IT REPAIRS NOTHING, including under --fix. Changing an upstream rewrites the
// meaning of `git push` in a directory somebody else may be working in right
// now, and the fleet-wide version of that is a one-command edit to 199
// checkouts belonging to other people. The remedy is printed per branch and
// left to a human. The claim path repairs the ONE branch it is materialising
// (clearBaseUpstream), which is the only branch polyforge is the actor on.
func checkBranchUpstreams(ctx context.Context, cfg *config.Config, wsRoot string) checkResult {
	const name = "branch-upstream"
	if cfg == nil {
		return checkResult{Name: name, Status: "warning", Message: "skipped (no config)"}
	}

	seen := map[string]bool{}
	var offenders []upstreamOffender
	var unreadable []string
	scanned := 0
	for _, proj := range cfg.Projects {
		for _, r := range proj.Repos {
			repoPath := filepath.Join(wsRoot, ".repo", r.Name)
			if seen[repoPath] {
				continue // a repo shared by two projects is one clone
			}
			seen[repoPath] = true
			if _, err := os.Stat(repoPath); err != nil {
				continue // checkRepos owns "the clone is missing"
			}
			wts, listErr := linkedWorktreeBranches(ctx, repoPath)
			if listErr != nil {
				unreadable = append(unreadable, r.Name)
				continue
			}
			for wt, branch := range wts {
				scanned++
				remote, remoteBranch, err := coding.GitUpstream(ctx, repoPath, branch)
				if err != nil {
					unreadable = append(unreadable, r.Name+"/"+branch)
					continue
				}
				if remote == "" || remote == "." {
					continue
				}
				if remoteBranch == branch || !coding.IsProtectedBranch(remoteBranch) {
					continue
				}
				offenders = append(offenders, upstreamOffender{
					Repo: r.Name, Worktree: wt, Branch: branch, Upstream: remote + "/" + remoteBranch,
				})
			}
		}
	}

	// "Nothing found" and "nothing looked" must not print the same line. Without
	// this, a git that is missing or broken for every repo produces scanned == 0
	// and an [ok] that reads exactly like a clean workspace.
	sort.Strings(unreadable)
	suffix := ""
	if len(unreadable) > 0 {
		suffix = fmt.Sprintf("; %d could not be read: %s", len(unreadable), strings.Join(unreadable, ", "))
	}
	unreadableStatus := "ok"
	if len(unreadable) > 0 {
		unreadableStatus = "warning"
	}

	if scanned == 0 {
		return checkResult{Name: name, Status: unreadableStatus,
			Message: "no task worktrees to check" + suffix}
	}
	if len(offenders) == 0 {
		return checkResult{Name: name, Status: unreadableStatus,
			Message: fmt.Sprintf("%d task worktrees, none tracking a protected branch%s", scanned, suffix)}
	}

	sort.Slice(offenders, func(i, j int) bool { return offenders[i].Worktree < offenders[j].Worktree })
	const showAtMost = 5
	shown := make([]string, 0, showAtMost)
	for _, o := range offenders[:min(len(offenders), showAtMost)] {
		// The pf.<project>-<seq> directory AND the repo: one work item can have a
		// worktree per repo, and labelling by directory alone renders those as
		// several identical entries.
		shown = append(shown, fmt.Sprintf("%s/%s -> %s",
			filepath.Base(filepath.Dir(o.Worktree)), o.Repo, o.Upstream))
	}
	msg := fmt.Sprintf("%d of %d task worktrees track a protected branch, so `git push` with "+
		"push.default=upstream would push them there: %s",
		len(offenders), scanned, strings.Join(shown, ", "))
	if len(offenders) > showAtMost {
		msg += fmt.Sprintf(" (+%d more)", len(offenders)-showAtMost)
	}
	return checkResult{Name: name, Status: "warning", Message: msg + suffix,
		FixCmd: "either `git config --global push.default current`, which makes a bare push " +
			"target the branch's own name everywhere and touches no branch config; or, per " +
			"worktree and only where you know nobody else is working: git branch --unset-upstream"}
}

// linkedWorktreeBranches maps each LINKED worktree of repoPath that is on a
// branch AND still exists on disk to that branch. Three kinds of entry are
// excluded, each for a different reason:
//
//   - the clone's own main worktree — tracking origin/main there is correct.
//   - a detached HEAD — no branch, so no upstream to aim anywhere.
//   - a PRUNABLE entry — a worktree whose directory was deleted but whose
//     registration survives. Git still prints its `branch refs/heads/<b>` line,
//     so without this it inflates the denominator and can be reported as an
//     offender with a remedy that says "run this in a directory" that is gone.
//     It also cannot receive a bare push, which is the whole criterion.
//
// `worktree list --porcelain` emits the main worktree first, which is the only
// documented ordering guarantee, so it is skipped by comparing paths rather
// than by position. ⚠️ filepath.Abs comes before EvalSymlinks because
// EvalSymlinks on a relative path returns a relative path while git always
// prints an absolute one; POLYFORGE_WORKSPACE_ROOT accepts a relative value, and
// the two would then never compare equal — the exclusion would silently stop
// working and every clone's main worktree would re-enter the count.
//
// An error is returned rather than folded into an empty map: "git could not be
// run here" and "this repo has no task worktrees" are the same value otherwise,
// and the caller reports them differently on purpose.
func linkedWorktreeBranches(ctx context.Context, repoPath string) (map[string]string, error) {
	out, err := exec.CommandContext(ctx, "git", "-C", repoPath, "worktree", "list", "--porcelain").Output()
	if err != nil {
		return nil, fmt.Errorf("git -C %s worktree list: %w", repoPath, err)
	}
	main := repoPath
	if abs, absErr := filepath.Abs(repoPath); absErr == nil {
		main = abs
	}
	if resolved, symErr := filepath.EvalSymlinks(main); symErr == nil {
		main = resolved
	}

	res := map[string]string{}
	// Entries are blank-line separated blocks, so a block's flags can follow its
	// branch line; the branch is therefore recorded per block and committed only
	// once the block ends.
	cur, branch, prunable := "", "", false
	flush := func() {
		if cur != "" && branch != "" && !prunable {
			resolved := cur
			if r, symErr := filepath.EvalSymlinks(cur); symErr == nil {
				resolved = r
			}
			if resolved != main {
				res[cur] = branch
			}
		}
		cur, branch, prunable = "", "", false
	}
	for _, line := range strings.Split(string(out), "\n") {
		line = strings.TrimRight(line, "\r")
		switch {
		case line == "":
			flush()
		case strings.HasPrefix(line, "worktree "):
			flush()
			cur = strings.TrimPrefix(line, "worktree ")
		case strings.HasPrefix(line, "branch refs/heads/"):
			branch = strings.TrimPrefix(line, "branch refs/heads/")
		case line == "prunable" || strings.HasPrefix(line, "prunable "):
			prunable = true
		}
	}
	flush()
	return res, nil
}

// checkRepos verifies that .repo/<name>/ directories exist and their remote
// URLs match .polyforge.yaml.
func checkRepos(wsRoot string, cfg *config.Config) checkResult {
	if cfg == nil {
		return checkResult{Name: "repos", Status: "warning", Message: "skipped (no config)"}
	}
	repoBase := filepath.Join(wsRoot, ".repo")
	var missing, mismatch []string

	for _, proj := range cfg.Projects {
		for _, r := range proj.Repos {
			repoPath := filepath.Join(repoBase, r.Name)
			if _, err := os.Stat(repoPath); os.IsNotExist(err) {
				missing = append(missing, r.Name)
				continue
			}
			// Check remote URL.
			out, err := exec.Command("git", "-C", repoPath, "remote", "get-url", "origin").Output()
			if err != nil {
				mismatch = append(mismatch, r.Name+"(remote-err)")
				continue
			}
			actual := strings.TrimSpace(string(out))
			if r.URL != "" && actual != r.URL {
				mismatch = append(mismatch, fmt.Sprintf("%s(want %s got %s)", r.Name, r.URL, actual))
			}
		}
	}

	if len(missing) == 0 && len(mismatch) == 0 {
		return checkResult{Name: "repos", Status: "ok", Message: "all repos present and remotes match"}
	}
	var msgs []string
	if len(missing) > 0 {
		msgs = append(msgs, fmt.Sprintf("missing: %s", strings.Join(missing, ", ")))
	}
	if len(mismatch) > 0 {
		msgs = append(msgs, fmt.Sprintf("remote mismatch: %s", strings.Join(mismatch, ", ")))
	}
	// `polyforge init`, NOT `polyforge init --apply`. --apply has been a hard
	// no-op since it was deprecated: RunInit prints a deprecation line and
	// returns before the clone loop, before the repo map, before CLAUDE.md. So
	// the advice this check used to print did nothing at all, and — worse — it
	// did nothing *quietly enough to look like success*, which is the failure
	// mode a doctor exists to remove rather than create (aihub#307).
	fixCmd := "polyforge init"
	if len(mismatch) > 0 {
		// And `init` does not fix a remote mismatch either: cloneOrSync only
		// fetches and resets an existing checkout, it has no `git remote set-url`
		// path. Naming a command that cannot repair the thing it is offered for
		// is the same defect as --apply, one step further down.
		fixCmd = "polyforge init (clones what is missing; a remote mismatch needs " +
			"`git -C .repo/<name> remote set-url origin <url>` by hand — init only fetches and resets)"
	}
	return checkResult{
		Name:    "repos",
		Status:  "warning",
		Message: strings.Join(msgs, "; "),
		FixCmd:  fixCmd,
	}
}

// listPageLimit is the page size checkWorktrees asks for, and it is exactly 200
// rather than "a big number" on purpose.
//
// The server's cap fails OPEN. domain.ListWorkItems treats `limit <= 0 || limit
// > 200` as "unset" and substitutes 50 (aihub#267), so the two intuitive ways to
// write this — omit limit, or ask for 1000 — are the same wrong answer, and both
// return a full-looking page. Measured against production 2026-08-31 on a
// project with 127 open items: limit absent → 50, limit=50 → 50, limit=200 →
// 127, limit=500 → 50.
const listPageLimit = 200

// listPageBudget bounds the cursor walk so a server that keeps handing back a
// cursor cannot spin forever. Hitting it is reported as a FAILED listing, never
// as a complete one — see fetchActiveWorkItems.
const listPageBudget = 100

// activeWIStatuses are the statuses whose work item still owns its worktree.
// `blocked` belongs here and used not to be: a step failure moves a claimed wi
// from running to blocked (internal/domain/dependencies.go) without touching its
// worktree, so leaving it out made every escalated wi's uncommitted work look
// like garbage. Terminal statuses are the complement — wrapped, failed,
// cancelled — and only those release a worktree.
var activeWIStatuses = []string{"running", "paused", "queued", "blocked"}

// terminalWIStatuses is the complement. Together with activeWIStatuses it
// partitions the status set the DB CHECK constraint allows
// (internal/db/migrations/0002_work_items.sql) — no gap, no overlap. A gap here
// is a status whose worktree nothing can classify.
var terminalWIStatuses = []string{"wrapped", "failed", "cancelled"}

var allWIStatuses = append(append([]string{}, activeWIStatuses...), terminalWIStatuses...)

func isTerminalWIStatus(s string) bool {
	return slices.Contains(terminalWIStatuses, s)
}

func isKnownWIStatus(s string) bool {
	return slices.Contains(allWIStatuses, s)
}

// fetchActiveWorkItems returns every non-terminal work item of one project,
// walking the cursor to the end.
//
// It returns an error on every failure it can see — a failed page, a response
// with no items array, a repeated cursor, an exhausted page budget — rather than
// the short list it has so far. Its caller turns "this work item was not in the
// list" into "delete this directory", so a truncated list is not a smaller
// answer, it is a wrong one that destroys uncommitted work. The original code
// had no cursor loop and no limit at all, so it silently compared against the
// server's first 50 rows; a project with 130 open items had 80 of them
// invisible, and five live worktrees (one of them a running wi) were listed for
// deletion (aihub#307).
//
// One gap it CANNOT see: the server's cursor is the last row's created_at with a
// strict comparison and no secondary tie-breaker (buildListWorkItemsWhere), so
// rows sharing that exact timestamp are skipped and the walk still ends
// normally. created_at defaults to clock_timestamp() so ties are unlikely, but
// "unlikely" is not "detected" — which is why verifyOrphan re-reads each
// candidate rather than trusting this result. A row lost here costs a refusal,
// not a deletion.
func fetchActiveWorkItems(ctx context.Context, c *client.Client, projectName string) ([]map[string]any, error) {
	var out []map[string]any
	cursor := ""
	for page := 1; page <= listPageBudget; page++ {
		params := url.Values{
			"project": []string{projectName},
			"status":  []string{strings.Join(activeWIStatuses, ",")}, // server splits on ","
			"limit":   []string{strconv.Itoa(listPageLimit)},
		}
		if cursor != "" {
			params.Set("cursor", cursor)
		}
		result, err := c.ListWorkItems(ctx, params)
		if err != nil {
			return nil, fmt.Errorf("page %d: %w", page, err)
		}
		items, ok := result["items"].([]any)
		if !ok {
			// A response without an items array is not an empty result. Reading
			// it as one would mean "every worktree of this project is an orphan".
			return nil, fmt.Errorf("page %d: response carries no items array", page)
		}
		for _, item := range items {
			if m, ok := item.(map[string]any); ok {
				out = append(out, m)
			}
		}
		// "Done" and "unreadable" have to be different answers. A one-line
		// `next, _ := result["next_cursor"].(string)` made every unexpected type
		// end the walk as if it had completed — the one branch in this function
		// where "error, never truncate" was not actually implemented.
		var next string
		switch v := result["next_cursor"].(type) {
		case nil:
			return out, nil // the server emits JSON null on the last page
		case string:
			next = v
		default:
			return nil, fmt.Errorf("page %d: next_cursor is %T, not a string or null", page, v)
		}
		if next == "" {
			return out, nil
		}
		if next == cursor {
			return nil, fmt.Errorf("page %d: server repeated cursor %q", page, next)
		}
		cursor = next
	}
	return nil, fmt.Errorf("listing did not terminate within %d pages of %d", listPageBudget, listPageLimit)
}

// worktreeProject returns the project segment of a `pf.<project>-<seq>` directory
// name, or "" for the two legacy formats (pf.<ulid8>, pf.<seq>.<ulid8>) that
// carry no project. Project names may contain "-" (global-routing,
// polyforge-scenario), so the split is on the LAST "-" and the tail must be all
// digits.
func worktreeProject(name string) string {
	rest := strings.TrimPrefix(name, "pf.")
	i := strings.LastIndex(rest, "-")
	if i <= 0 || i == len(rest)-1 {
		return ""
	}
	for _, r := range rest[i+1:] {
		if r < '0' || r > '9' {
			return ""
		}
	}
	return rest[:i]
}

// worktreeSlug converts `pf.<project>-<seq>` back into the work item slug
// `<project>#<seq>`, or "" when the name carries no slug.
func worktreeSlug(name string) string {
	p := worktreeProject(name)
	if p == "" {
		return ""
	}
	return p + "#" + strings.TrimPrefix(name, "pf."+p+"-")
}

// worktreeULID8 returns the 8-character id tail of the two pre-slug worktree
// formats — `pf.<ulid8>` and `pf.<seq>.<ulid8>` — or "" for anything else. ulid8
// is exactly the 8 base62 characters domain.NewID appends after the `wi_`
// prefix, which is what makes it a lookup key and not just a label.
//
// The precision matters twice over. Treating "anything that is not
// pf.<project>-<seq>" as a legacy name handed a blanket permission to names that
// are not worktrees at all — measured: `pf.ieops-274.bak`, `pf.scratch` and
// `pf.aihub-notes` were all deleted, each reported as a legacy worktree. And
// requiring exactly 8 base62 characters is not enough by itself: `pf.salvage1`
// and `pf.BACKUP01` fit that shape too, which is why matching the shape now buys
// a lookup rather than a deletion.
func worktreeULID8(name string) string {
	rest, ok := strings.CutPrefix(name, "pf.")
	if !ok {
		return ""
	}
	if seq, tail, found := strings.Cut(rest, "."); found {
		if !allDigits(seq) {
			return ""
		}
		rest = tail
	}
	if len(rest) != 8 || !allBase62(rest) {
		return ""
	}
	return rest
}

// worktreeLookupKey returns what to ask the server about for a directory, and
// whether there is anything to ask at all.
//
// For `pf.<project>-<seq>` that is the slug. For the two legacy formats it is
// `wi_<ulid8>` — and THAT is the correction. The previous code returned "no slug
// to ask about, so the active listing is the only evidence there is", which was
// simply false: the tail in the directory name IS the work item id minus its
// `wi_` prefix (the same value this file already indexes as `activeIDs[id[3:]]`),
// and GET /v1/work_items/:id resolves the `wi_` spelling through
// domain.FormatIDOrSlug. Verified live: wi_VGg1VR2x -> aihub#307, running.
//
// So a running `wi_aBcD1234` with worktree `pf.aBcD1234` used to be deletable on
// any listing gap — the exact class of failure the per-item re-check exists for —
// under a message asserting the check "was not possible" when it was.
func worktreeLookupKey(dir string) string {
	if slug := worktreeSlug(dir); slug != "" {
		return slug
	}
	if u := worktreeULID8(dir); u != "" {
		return "wi_" + u
	}
	return ""
}

func allDigits(s string) bool {
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return s != ""
}

func allBase62(s string) bool {
	for _, r := range s {
		switch {
		case r >= '0' && r <= '9', r >= 'A' && r <= 'Z', r >= 'a' && r <= 'z':
		default:
			return false
		}
	}
	return s != ""
}

// verdictKind says what it would take to delete a directory, and it exists
// because "removable / not removable" was too coarse. It collapsed two very
// different refusals — "its work item is running" and "nobody can tell what this
// directory is" — into one, and then a single --force-remove flag was enough to
// override both. Deleting a directory nobody can identify costs a stale
// directory; deleting a running work item's costs uncommitted work.
type verdictKind int

const (
	// verdictTerminal — the work item exists and is finished. --fix removes it.
	verdictTerminal verdictKind = iota
	// verdictActive — the work item exists and is running/paused/queued/blocked.
	// Its worktree holds uncommitted work by definition. Only forceable by
	// transcribing the current status.
	verdictActive
	// verdictUnknown — unreadable, absent, or a name polyforge never produced.
	// Forceable by name: the cost of being wrong is a stale directory.
	verdictUnknown
)

// orphanVerdict is the per-worktree answer --fix acts on. Each field is here
// because the caller must be able to print WHY before it deletes anything.
type orphanVerdict struct {
	Dir    string
	Key    string // slug or wi_<ulid8> that was asked about; "" if unidentifiable
	Status string // work item status as the server reports it, "" if unknown
	Kind   verdictKind
	Note   string
}

func (v orphanVerdict) statusText() string {
	if v.Status == "" {
		return "unknown"
	}
	return v.Status
}

// verifyOrphan re-asks the server about ONE directory before it can be deleted.
//
// This is deliberately a second, independent hop rather than a reuse of the list
// result: the list is what was wrong in aihub#307, and a check that answers "is
// this safe to delete?" out of the same data that proposed the deletion cannot
// catch the next version of that bug. It is also what makes it possible to print
// each path together with its work item's status, which is what a human needs to
// veto the batch.
//
// It must be reached from EVERY path that deletes, including the forced ones and
// including a directory whose project listing failed. A failed listing says
// nothing about whether GET /v1/work_items/<key> works; skipping this hop
// because the listing was unusable reintroduces exactly the bug above, in one
// flag.
//
// A 404 lands on verdictUnknown, not on "delete it". The far likelier cause than
// a deleted work item is ~/.polyforge/config.toml pointing at a different aihub
// than the workspace was built against — in which case EVERY key 404s, and a
// delete-on-404 rule empties the workspace in one command. It stays forceable by
// name so a genuinely dead directory does not become permanently uncleanable.
func verifyOrphan(ctx context.Context, c *client.Client, dir string) orphanVerdict {
	key := worktreeLookupKey(dir)
	if key == "" {
		// "pf." is a prefix, not a licence: a directory nobody can identify is
		// the last thing that should be deleted without being asked about.
		return orphanVerdict{Dir: dir, Kind: verdictUnknown,
			Note: "not a name polyforge produces (expected pf.<project>-<seq>, pf.<ulid8> or " +
				"pf.<seq>.<ulid8>) — nothing to look up, so no status could be checked"}
	}
	wi, err := c.GetWorkItem(ctx, key)
	if err != nil {
		return orphanVerdict{Dir: dir, Key: key, Kind: verdictUnknown,
			Note: fmt.Sprintf("could not read %s: %v — refusing to delete on an unread work item", key, err)}
	}
	status, _ := wi["status"].(string)
	if status == "" {
		return orphanVerdict{Dir: dir, Key: key, Kind: verdictUnknown,
			Note: fmt.Sprintf("%s came back without a status field — refusing to delete", key)}
	}
	if !isTerminalWIStatus(status) {
		return orphanVerdict{Dir: dir, Key: key, Status: status, Kind: verdictActive,
			Note: fmt.Sprintf("%s is %s, not a terminal state — its worktree may hold uncommitted work. "+
				"That it was selected at all means the active listing missed it; please report that", key, status)}
	}
	return orphanVerdict{Dir: dir, Key: key, Status: status, Kind: verdictTerminal,
		Note: fmt.Sprintf("%s is %s (terminal)", key, status)}
}

// checkWorktrees cross-references pf.* directories with active work items
// from aihub; flags directories with no matching active wi.
//
// Everything here is arranged around one asymmetry: a false negative costs a
// stale directory, a false positive costs a developer's uncommitted work. So
// every way of not knowing — a failed page, an unparseable response, an
// unreadable work item — has to land on "keep", and has to say so out loud
// instead of being folded into the orphan list.
func checkWorktrees(ctx context.Context, c *client.Client, cfg *config.Config, wsRoot string, opts doctorOpts, out io.Writer) checkResult {
	entries, err := os.ReadDir(wsRoot)
	if err != nil {
		return checkResult{Name: "worktrees", Status: "error", Message: fmt.Sprintf("readdir: %v", err)}
	}

	var wt []string
	for _, e := range entries {
		if e.IsDir() && strings.HasPrefix(e.Name(), "pf.") {
			wt = append(wt, e.Name())
		}
	}
	if len(wt) == 0 {
		return checkResult{Name: "worktrees", Status: "ok", Message: "no worktrees found"}
	}
	sort.Strings(wt)

	// Fetch active work items from aihub to cross-reference.
	// The server requires a "project" query parameter, so we query each project
	// separately and merge the results. If cfg is nil we skip the cross-reference
	// (cannot determine projects) and list pf.* dirs as a warning only.
	activeIDs := map[string]bool{}   // ulid8 suffix → true (legacy format match)
	activeSlugs := map[string]bool{} // "pf.<project>-<seq>" → true (new format match)
	failed := map[string]string{}    // project → why its listing is not usable
	if c != nil && cfg != nil {
		projects := make([]string, 0, len(cfg.Projects))
		for projectName := range cfg.Projects {
			projects = append(projects, projectName)
		}
		sort.Strings(projects)
		for _, projectName := range projects {
			items, listErr := fetchActiveWorkItems(ctx, c, projectName)
			if listErr != nil {
				// Was `continue`, which let the orphan scan run against a set
				// this project contributed nothing to — i.e. one failed request
				// nominated every one of its worktrees for deletion.
				failed[projectName] = listErr.Error()
				continue
			}
			for _, m := range items {
				if id, ok := m["id"].(string); ok && len(id) > 3 {
					activeIDs[id[3:]] = true // strip "wi_" prefix → 01ks510z
				}
				// New slug-based format: "aihub#30" → "pf.aihub-30"
				if slug, ok := m["slug"].(string); ok && slug != "" {
					activeSlugs["pf."+strings.ReplaceAll(slug, "#", "-")] = true
				}
			}
		}
	}

	// Identify orphan candidates: worktree that does not match any active wi.
	// Supports three formats:
	//   pf.<project>-<seq>   — new readable format (matched via activeSlugs)
	//   pf.<seq>.<ulid8>     — previous format (matched via activeIDs ulid8 suffix)
	//   pf.<ulid8>           — legacy format (matched via activeIDs ulid8 suffix)
	// With no client or no config there is nothing to cross-reference against, so
	// nothing can be called an orphan. Report that as "did not look" rather than
	// as "looked and found nothing" — a green line here is the same silent
	// failure the rest of this function exists to remove.
	if c == nil || cfg == nil {
		return checkResult{Name: "worktrees", Status: "warning",
			Message: fmt.Sprintf("%d worktrees found, but they could not be cross-referenced "+
				"(no aihub client or no .polyforge.yaml) — this is 'did not look', not 'none orphaned'", len(wt))}
	}

	var candidates []string
	var unverifiable []unverifiableDir
	for _, name := range wt {
		if activeSlugs[name] {
			continue
		}
		// Legacy formats: the 8-char tail IS the work item id minus its `wi_`
		// prefix, which is what activeIDs is keyed on, so this is an exact lookup
		// and not a suffix scan. Running it on an unrecognised name would compare
		// an arbitrary tail ("bak") against the id set and could match by
		// accident, so worktreeULID8 has to have said yes first.
		if u := worktreeULID8(name); u != "" && activeIDs[u] {
			continue
		}
		if why, gap := listingGapFor(name, failed); gap {
			unverifiable = append(unverifiable, unverifiableDir{Dir: name, Why: why})
			continue
		}
		candidates = append(candidates, name)
	}

	if opts.fix {
		return removeOrphans(ctx, c, wsRoot, len(wt), candidates, unverifiable, opts, out)
	}

	// Read-only report. Each candidate is still re-checked individually so the
	// message carries the work item status a human needs to sanity-check the
	// list — the old message was bare directory names, which is exactly what let
	// a running wi's worktree sit in it unnoticed.
	var listed []string
	blockers := 0
	for _, dir := range candidates {
		v := verifyOrphan(ctx, c, dir)
		listed = append(listed, fmt.Sprintf("%s [%s]", v.Dir, v.statusText()))
		if v.Kind != verdictTerminal {
			blockers++
		}
	}

	switch {
	case len(listed) == 0 && len(unverifiable) == 0:
		return checkResult{Name: "worktrees", Status: "ok",
			Message: fmt.Sprintf("%d worktrees, none orphaned", len(wt))}
	case len(listed) == 0:
		return checkResult{Name: "worktrees", Status: "warning",
			Message: fmt.Sprintf("%d worktrees, none orphaned; %d could not be verified: %s",
				len(wt), len(unverifiable), describeUnverifiable(unverifiable))}
	}

	msg := fmt.Sprintf("%d orphan worktrees: %s", len(listed), strings.Join(listed, ", "))
	if blockers > 0 {
		msg += fmt.Sprintf("; %d of them are NOT safe to remove (their work item is not in a terminal state, "+
			"or is not a name polyforge produces) — --fix will refuse", blockers)
	}
	if len(unverifiable) > 0 {
		msg += fmt.Sprintf("; %d more could not be verified and are left alone: %s",
			len(unverifiable), describeUnverifiable(unverifiable))
	}
	return checkResult{Name: "worktrees", Status: "warning", Message: msg, FixCmd: "polyforge doctor --fix"}
}

// unverifiableDir is a pf.* directory whose classification could not be trusted
// because the listing it would have been compared against failed. It keeps the
// bare name as well as the reason so --force-remove can still reach it: a
// project the caller has lost access to fails its listing on every run, and
// without a way through, the only remaining cleanup is `rm -rf` — i.e. the tool
// gets bypassed exactly where it is trying to be careful.
type unverifiableDir struct {
	Dir string
	Why string
}

func describeUnverifiable(in []unverifiableDir) string {
	out := make([]string, 0, len(in))
	for _, u := range in {
		out = append(out, fmt.Sprintf("%s (%s)", u.Dir, u.Why))
	}
	return strings.Join(out, ", ")
}

// listingGapFor reports whether `name` was compared against an incomplete set,
// in which case "not in the active list" means nothing.
func listingGapFor(name string, failed map[string]string) (string, bool) {
	if len(failed) == 0 {
		return "", false
	}
	if p := worktreeProject(name); p != "" {
		if why, bad := failed[p]; bad {
			return "project " + p + " could not be listed: " + why, true
		}
		return "", false
	}
	// A legacy name is matched against the ulid8 map merged across ALL projects,
	// so any failed listing can be the reason it looks inactive.
	names := make([]string, 0, len(failed))
	for p := range failed {
		names = append(names, p)
	}
	sort.Strings(names)
	return "legacy directory name, and these projects could not be listed: " + strings.Join(names, ", "), true
}

// removeOrphans is the --fix path. Every directory it can delete goes through
// verifyOrphan first, and the verdict is printed before that directory is
// touched.
//
// "Every" is load-bearing and was learned twice. The first version deleted the
// whole candidate list in one loop with no per-worktree step. The second added
// the per-item check on the candidate path but let --force-remove delete an
// unverifiable directory by calling the remover directly — so one flag
// reproduced the original data-loss bug (four live worktrees, three of them
// running, destroyed with zero status queries and an [ok] report), and did it in
// the branch whose whole premise was that the listing could not be trusted. A
// failed listing says nothing about whether GET /v1/work_items/<key> works.
//
// The escape hatch is graded by what being wrong costs. verdictUnknown — nothing
// could be read, or the name is not one polyforge produces — is forceable by
// name; the cost is a stale directory. verdictActive needs the work item's
// current status transcribed into the flag, because the cost there is somebody's
// uncommitted work.
func removeOrphans(ctx context.Context, c *client.Client, wsRoot string, total int, candidates []string, unverifiable []unverifiableDir, opts doctorOpts, out io.Writer) checkResult {
	// An unverifiable directory is reachable only when named: its listing failed,
	// so nothing selected it. But once named it is verified exactly like a
	// candidate — the naming buys it a look, not a deletion. Without any way
	// through, a project the caller has lost access to would leave permanently
	// uncleanable directories and the tool would simply be bypassed with rm -rf.
	var acting []string
	acting = append(acting, candidates...)
	unverifiableWhy := map[string]string{}
	kept := unverifiable[:0:0]
	for _, u := range unverifiable {
		if _, named := opts.forceRemove[u.Dir]; named {
			acting = append(acting, u.Dir)
			unverifiableWhy[u.Dir] = u.Why
			continue
		}
		kept = append(kept, u)
	}
	unverifiable = kept
	sort.Strings(acting)

	// A --force-remove name that matches nothing at all must be said out loud.
	// Left silent, a typo and a directory that was already gone look identical,
	// and the caller walks away believing an acknowledgement was acted on.
	known := make(map[string]bool, len(acting))
	for _, d := range acting {
		known[d] = true
	}
	var unmatched []string
	for d := range opts.forceRemove {
		if !known[d] {
			unmatched = append(unmatched, d)
		}
	}
	sort.Strings(unmatched)

	if len(acting) == 0 && len(unmatched) == 0 && len(unverifiable) == 0 {
		return checkResult{Name: "worktrees", Status: "ok",
			Message: fmt.Sprintf("%d worktrees, none orphaned", total)}
	}

	var removed, refused, forced, failedRemoval []string
	for _, dir := range acting {
		v := verifyOrphan(ctx, c, dir)
		stated, named := opts.forceRemove[dir]

		if why, ok := unverifiableWhy[dir]; ok {
			_, _ = fmt.Fprintf(out, "       worktree %s: named by --force-remove; it was not selected because %s\n", dir, why)
		}
		_, _ = fmt.Fprintf(out, "       worktree %s: wi=%s status=%s — %s\n",
			v.Dir, keyText(v.Key), v.statusText(), v.Note)

		allowed, why := forceAllows(v, stated, named)
		if !allowed {
			_, _ = fmt.Fprintf(out, "       worktree %s: KEPT — %s\n", v.Dir, why)
			refused = append(refused, fmt.Sprintf("%s [%s]", v.Dir, v.statusText()))
			continue
		}
		if v.Kind != verdictTerminal {
			_, _ = fmt.Fprintf(out, "       worktree %s: removing anyway — %s\n", v.Dir, why)
			forced = append(forced, fmt.Sprintf("%s [%s]", v.Dir, v.statusText()))
		}

		if err := removeWorktreeDir(ctx, wsRoot, dir); err != nil {
			_, _ = fmt.Fprintf(out, "       worktree %s: REMOVAL FAILED: %v\n", v.Dir, err)
			failedRemoval = append(failedRemoval, fmt.Sprintf("%s (%v)", v.Dir, err))
			continue
		}
		_, _ = fmt.Fprintf(out, "       worktree %s: removed\n", v.Dir)
		removed = append(removed, v.Dir)
	}

	// Phrased so that "removed <n> orphan" stays a substring: tests/scenarios/e2e
	// asserts on it. The denominator counts every directory this run acted on,
	// forced ones included — it previously counted only the selected candidates,
	// so forcing four unverifiable directories reported "removed 4 of 0".
	msg := fmt.Sprintf("removed %d orphan worktree(s) of %d considered", len(removed), len(acting))
	if len(removed) > 0 {
		msg += ": " + strings.Join(removed, ", ")
	}
	if len(forced) > 0 {
		msg += fmt.Sprintf("; %d removed ONLY because --force-remove named them: %s", len(forced), strings.Join(forced, ", "))
	}
	if len(refused) > 0 {
		msg += fmt.Sprintf("; KEPT %d that are not safe to remove: %s", len(refused), strings.Join(refused, ", "))
	}
	if len(failedRemoval) > 0 {
		msg += fmt.Sprintf("; REMOVAL FAILED for %d: %s", len(failedRemoval), strings.Join(failedRemoval, ", "))
	}
	if len(unverifiable) > 0 {
		msg += fmt.Sprintf("; %d could not be verified and were left alone: %s",
			len(unverifiable), describeUnverifiable(unverifiable))
	}
	if len(unmatched) > 0 {
		msg += fmt.Sprintf("; --force-remove named %d director(y/ies) that this run knows nothing about, "+
			"so nothing was done with them: %s", len(unmatched), strings.Join(unmatched, ", "))
	}
	// `forced` belongs in this condition. Without it, the single most dangerous
	// thing this command can do — deleting the worktree of a work item the server
	// says is running — was the one outcome reported fully green, invisible to
	// anything reading the exit code or the [ok]/[warn] icon.
	status := "ok"
	if len(refused) > 0 || len(forced) > 0 || len(unverifiable) > 0 || len(unmatched) > 0 || len(failedRemoval) > 0 {
		status = "warning"
	}
	return checkResult{Name: "worktrees", Status: status, Message: msg}
}

// forceAllows decides whether one directory may be deleted, and returns the
// sentence explaining the decision either way.
//
// The refusal for an ACTIVE work item deliberately does not print a ready-made
// bypass. It used to print `--force-remove=<dir>` verbatim, which made the
// cheapest way past the guard to copy the line the guard printed — an agent that
// hit the refusal was handed the command that defeats it. The status has to be
// looked up instead, and it is checked against the server's current value, so a
// work item that moves on between the lookup and the run stops the removal.
func forceAllows(v orphanVerdict, stated string, named bool) (bool, string) {
	switch v.Kind {
	case verdictTerminal:
		return true, "work item is terminal"
	case verdictUnknown:
		if !named {
			return false, fmt.Sprintf("nothing could be established about it. If it is genuinely dead: "+
				"polyforge doctor --fix --force-remove=%s", v.Dir)
		}
		return true, "--force-remove named it and nothing could be established about it"
	default: // verdictActive
		if !named {
			return false, fmt.Sprintf("its work item is %s and its worktree may hold uncommitted work. "+
				"Commit or wrap %s first; see `polyforge doctor --help` if it really has to go", v.Status, v.Key)
		}
		if stated == "" {
			return false, fmt.Sprintf("--force-remove named it, but its work item is %s: naming an active "+
				"work item's worktree is not enough, the flag must also carry the status "+
				"(--force-remove=%s:<status>) so it is clear that was looked up", v.Status, v.Dir)
		}
		if stated != v.Status {
			return false, fmt.Sprintf("--force-remove=%s:%s does not match the current status %q — "+
				"the work item moved since that was read, so the removal stops", v.Dir, stated, v.Status)
		}
		return true, fmt.Sprintf("--force-remove named it AND stated its current status (%s)", v.Status)
	}
}

func keyText(s string) string {
	if s == "" {
		return "(unidentifiable)"
	}
	return s
}

// deleteWorktreeDir is the deletion primitive, split out as a package-level var
// purely so removeWorktreeDir's verification can be tested: on a box running as
// root neither a read-only parent nor a stripped mode blocks unlink, so "the
// deletion failed" cannot otherwise be induced, and a guard that can only be
// exercised by not being root is a guard that never runs.
var deleteWorktreeDir = func(ctx context.Context, wsRoot, dir string) {
	// Deregister every linked worktree under pf.<slug>/ before deleting the
	// directory, so git does not keep a prunable registration pointing at a path
	// that is gone.
	//
	// This used to be `git -C <wsRoot> worktree remove --force <dir>`, which can
	// never succeed: wsRoot is not a repository and pf.<slug>/ is not a worktree.
	// The registered worktrees are the pf.<slug>/<repo>/ subdirectories, and each
	// belongs to .repo/<repo>. Measured before the fix — `fatal: 'pf.demo-1' is
	// not a working tree`, exit 128 — so the fallback was in fact the only path
	// that ever ran, under a comment calling the git call "safest".
	//
	// Branches are deliberately left alone: the branch is where the work is, and
	// polyforge pushes it. Removing the checkout is not a reason to destroy it.
	for _, sub := range gitWorktreesUnder(ctx, filepath.Join(wsRoot, dir)) {
		mainRoot, err := gitMainWorktreeOf(ctx, sub)
		if err != nil {
			continue // not a linked worktree, or git unavailable: rm handles it
		}
		_ = exec.CommandContext(ctx, "git", "-C", mainRoot, "worktree", "remove", "--force", sub).Run()
	}
	// rm -rf finishes the job: it clears anything git refused or never knew
	// about, plus the pf.<slug>/ parent itself. Its error is deliberately
	// unhandled here — whether the directory actually went away is settled by
	// removeWorktreeDir looking, not by any exit status.
	_ = os.RemoveAll(filepath.Join(wsRoot, dir))
}

// gitWorktreesUnder returns the immediate subdirectories of a pf.<slug>/
// directory, which is where the per-repo checkouts live.
func gitWorktreesUnder(ctx context.Context, parent string) []string {
	entries, err := os.ReadDir(parent)
	if err != nil {
		return nil
	}
	var out []string
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		sub := filepath.Join(parent, e.Name())
		if _, statErr := os.Stat(filepath.Join(sub, ".git")); statErr == nil {
			out = append(out, sub)
		}
	}
	return out
}

// gitMainWorktreeOf returns the main worktree root that owns a linked worktree,
// derived from its --git-common-dir (".../.repo/<repo>/.git" → ".../.repo/<repo>").
func gitMainWorktreeOf(ctx context.Context, worktree string) (string, error) {
	out, err := exec.CommandContext(ctx, "git", "-C", worktree,
		"rev-parse", "--path-format=absolute", "--git-common-dir").Output()
	if err != nil {
		return "", err
	}
	commonDir := strings.TrimSpace(string(out))
	if commonDir == "" {
		return "", fmt.Errorf("git reported no common dir for %s", worktree)
	}
	return filepath.Dir(commonDir), nil
}

// removeWorktreeDir deletes one worktree directory and confirms it is gone.
//
// The confirmation is the point. os.RemoveAll's error was previously discarded,
// so an EBUSY, a read-only parent, or a mount point produced a green
// "removed N orphan worktrees" over a directory still sitting on disk — a
// success message derived from having reached the line rather than from the
// outcome it claims.
func removeWorktreeDir(ctx context.Context, wsRoot, dir string) error {
	path := filepath.Join(wsRoot, dir)
	deleteWorktreeDir(ctx, wsRoot, dir)
	switch _, err := os.Stat(path); {
	case err == nil:
		return fmt.Errorf("%s is still on disk after the removal", path)
	case os.IsNotExist(err):
		return nil
	default:
		return fmt.Errorf("could not confirm %s is gone: %w", path, err)
	}
}

// checkUsageMd reports a .polyforge/usage.md that still carries a rule section the
// plugin-versioned using-polyforge skill owns (aihub#294).
//
// This check has to exist because writeUsageMd cannot fix the problem by itself. That
// function refuses to overwrite an existing usage.md — deliberately; the file is the
// user's — so dropping the rule sections from its template only helps workspaces created
// after this release. Every workspace that already ran init keeps its frozen copy, a
// session then receives the rules twice, and the two copies are under no obligation to
// agree. They already did not: IR1's worktree path was wrong in one of them for three
// months and nothing anywhere reported it.
//
// REPORT-ONLY, ON PURPOSE — and this is the second time that conclusion was reached the
// hard way. The first cut of this check also removed the sections under `--fix`, deciding
// each section's extent from markdown structure. Review found six input classes where
// that destroyed content the user owned, three of them leaving the file structurally
// broken (an unterminated fence or HTML comment swallows the rest of the document). The
// live check against a real frozen workspace missed all six, because a pristine generated
// template is the one input that cannot exhibit any of them.
//
// Inferring the extent of a generated section from the shape of a file a user has since
// edited is the wrong primitive. The right one is to delete a span only when it is
// byte-identical to something a known template version emitted, and otherwise say so and
// stop. That is a real piece of work — it needs the historical template bodies — and it
// is NOT what this work item is about, which is that a rule was on a channel that could
// never be corrected. Detection is what makes that stop being silent; removal was a
// convenience that cost a data-loss path.
//
// `--fix` deliberately does not reach here. It is also not the operator's considered
// choice as often as it looks: plugins/polyforge/skills/pf-stop/SKILL.md tells agents to
// run `polyforge doctor --fix` to clean up worktrees, so the caller asking for it has
// very likely never read this file.
func checkUsageMd(wsRoot string) checkResult {
	const name = "usage_md"
	path := filepath.Join(wsRoot, ".polyforge", "usage.md")

	b, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		// Absent is not a finding here: usage.md is written by init, and a workspace
		// that never ran init has a louder problem that checkWorkspace already reports.
		return checkResult{Name: name, Status: "ok", Message: "no .polyforge/usage.md to check"}
	}
	if err != nil {
		// Anything else — a permission or IO fault on a file that IS there — must not
		// report green. "Could not look" and "looked and found nothing" are different.
		return checkResult{Name: name, Status: "warning",
			Message: fmt.Sprintf(".polyforge/usage.md not readable: %v", err)}
	}

	found, wellFormed := scanUsageSections(string(b))
	if !wellFormed {
		// An unterminated fence or HTML comment means everything after it was skipped,
		// so a clean result here would mean "stopped looking", not "looked and found
		// nothing". In a check whose entire subject is silent failure, that green would
		// be the very defect it exists to report.
		return checkResult{Name: name, Status: "warning",
			Message: ".polyforge/usage.md has an unterminated code fence or HTML comment — " +
				"the scan could not read past it, so this is 'did not look', not 'found nothing'"}
	}
	if len(found) == 0 {
		return checkResult{Name: name, Status: "ok",
			Message: ".polyforge/usage.md carries no rule section owned by using-polyforge"}
	}
	return checkResult{Name: name, Status: "warning",
		Message: fmt.Sprintf(".polyforge/usage.md still carries %d rule section(s) that using-polyforge "+
			"owns (%s) — that file is never regenerated, so this copy cannot be corrected and a "+
			"session sees both",
			len(found), strings.Join(found, ", ")),
		FixCmd: "edit .polyforge/usage.md and delete those sections by hand — the maintained " +
			"copy ships with the using-polyforge skill (not automated: see checkUsageMd)"}
}

// fenceDelim reports the fence character and run length that opens or closes a fenced
// code block, or (0, 0) for any other line. `line` must already have leading space
// stripped. Character AND length both matter: per CommonMark a fence is closed only by a
// run of the SAME character at least as long as the one that opened it, so a ``` inside a
// ```` block, or a ~~~ inside a ``` block, is content rather than a terminator. Treating
// every ```/~~~ prefix as a toggle got both of those backwards.
func fenceDelim(line string) (byte, int) {
	if line == "" {
		return 0, 0
	}
	c := line[0]
	if c != '`' && c != '~' {
		return 0, 0
	}
	n := 0
	for n < len(line) && line[n] == c {
		n++
	}
	if n < 3 {
		return 0, 0
	}
	return c, n
}

// scanUsageSections walks a usage.md body once and reports which skillOwnedUsageSections
// headings it really carries, in template order, plus whether the document parsed cleanly.
//
// "Really" is doing work here. A line that merely looks like an owned heading does not
// count when it is inside a fenced block, inside an indented code block (4+ spaces is a
// code block, and CommonMark allows an ATX heading at most 3), or inside an HTML comment.
// Each of those is an example or a note ABOUT the heading, not the heading.
//
// wellFormed is false when the walk ends inside a fence or a comment, i.e. when the tail
// of the file was never examined. The caller must not report a clean bill in that case.
func scanUsageSections(body string) (found []string, wellFormed bool) {
	owned := make(map[string]bool, len(skillOwnedUsageSections))
	for _, h := range skillOwnedUsageSections {
		owned[h] = true
	}
	seen := make(map[string]bool, len(skillOwnedUsageSections))

	var fenceChar byte
	fenceLen := 0
	inComment := false

	for _, line := range strings.Split(body, "\n") {
		stripped := strings.TrimLeft(line, " \t")
		indent := len(line) - len(stripped)

		if inComment {
			if strings.Contains(line, "-->") {
				inComment = false
			}
			continue
		}
		if fenceLen > 0 {
			if c, n := fenceDelim(stripped); c == fenceChar && n >= fenceLen && indent <= 3 {
				fenceLen = 0
			}
			continue
		}
		if c, n := fenceDelim(stripped); n > 0 && indent <= 3 {
			fenceChar, fenceLen = c, n
			continue
		}
		if i := strings.Index(line, "<!--"); i >= 0 {
			if !strings.Contains(line[i:], "-->") {
				inComment = true
			}
			continue
		}
		if indent >= 4 {
			continue // indented code block, not a heading
		}
		if s := strings.TrimSpace(line); owned[s] {
			seen[s] = true
		}
	}

	for _, h := range skillOwnedUsageSections {
		if seen[h] {
			found = append(found, strings.TrimPrefix(h, "## "))
		}
	}
	return found, fenceLen == 0 && !inComment
}

// checkClaudeMd inspects the CLAUDE.md managed block that `polyforge init`
// writes. Two failure modes, both reported as warnings — a stale block is not a
// broken workspace, so this check must never fail the run:
//
//   - The block still inlines the per-repo detail (the pre-aihub#291 format).
//     That detail sits at context position 0, is re-read on every request, and
//     compaction cannot drop it; re-running `polyforge init` moves it to
//     .polyforge/repo-map/. This is the whole reason the check exists: the
//     saving only lands once a workspace re-runs init, and workspaces go months
//     without doing so.
//   - The block is slim but the repo map it points at is missing, so routing
//     has nothing but the one-line positioning. Say so, rather than leaving an
//     agent to quietly guess which repo a task belongs to.
//
// The expected set of maps comes from the block's own `### <project>` headings,
// not from .polyforge.yaml — see managedBlockProjects.
func checkClaudeMd(wsRoot string) checkResult {
	const name = "claude_md"
	const fix = "polyforge init"

	b, err := os.ReadFile(filepath.Join(wsRoot, "CLAUDE.md"))
	if err != nil {
		return checkResult{Name: name, Status: "warning",
			Message: fmt.Sprintf("CLAUDE.md not readable: %v", err), FixCmd: fix}
	}
	block, ok := managedBlockOf(string(b))
	if !ok {
		return checkResult{Name: name, Status: "warning",
			Message: "CLAUDE.md has no polyforge managed block", FixCmd: fix}
	}
	if blockIsLegacyFormat(block) {
		return checkResult{Name: name, Status: "warning",
			Message: fmt.Sprintf("managed block is the legacy inline format (%d B, re-read on every request) — "+
				"re-running init moves per-repo detail to .polyforge/repo-map/", len(block)),
			FixCmd: fix}
	}

	mapDir := filepath.Join(wsRoot, ".polyforge", repoMapDirName)
	present := map[string]bool{}
	entries, _ := os.ReadDir(mapDir) // absent dir → no entries, handled below
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".md") {
			present[e.Name()] = true
		}
	}
	if len(present) == 0 {
		return checkResult{Name: name, Status: "warning",
			Message: fmt.Sprintf("repo map missing: no %s/*.md — the block carries only one-line positioning, "+
				"so routing has no main_modules / change_scenarios / tech_stack to read",
				filepath.Join(".polyforge", repoMapDirName)),
			FixCmd: fix}
	}
	var missing []string
	for _, project := range managedBlockProjects(block) {
		if fn := repoMapFileName(project); fn != "" && !present[fn] {
			missing = append(missing, project)
		}
	}
	if len(missing) > 0 {
		sort.Strings(missing)
		return checkResult{Name: name, Status: "warning",
			Message: fmt.Sprintf("repo map missing for project(s): %s", strings.Join(missing, ", ")),
			FixCmd:  fix}
	}
	return checkResult{Name: name, Status: "ok",
		Message: fmt.Sprintf("managed block slim (%d B), %d repo map(s) present", len(block), len(present))}
}

// checkVersion fetches GET /v1/version and compares min_client_version with
// the locally compiled version.
func checkVersion(ctx context.Context, c *client.Client) checkResult {
	if c == nil {
		return checkResult{Name: "version", Status: "warning", Message: "skipped (no aihub client)"}
	}
	ver, err := c.GetVersion(ctx)
	if err != nil {
		// Non-fatal: server may not expose this endpoint in v1.
		return checkResult{Name: "version", Status: "ok", Message: "version endpoint not available (non-fatal)"}
	}
	minVer, _ := ver["min_client_version"].(string)
	if minVer == "" {
		return checkResult{Name: "version", Status: "ok", Message: "server did not set min_client_version"}
	}
	return checkResult{Name: "version", Status: "ok",
		Message: fmt.Sprintf("server min_client_version=%s (local=dev)", minVer)}
}
