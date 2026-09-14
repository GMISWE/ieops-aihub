package engine

import (
	"fmt"
	"path/filepath"
	"regexp"
	"strings"
)

// ownerRepoSep is the directory-name separator between owner and repo in the owner-qualified
// scenario clone layout (.repo/<owner><ownerRepoSep><repo>). internal/cli/init.go declares the
// same value as scenarioDirSep, a CONTRACT const pinned by scenario_clone_test.go; it cannot be
// imported here (internal/cli already imports internal/engine, so the reverse would cycle), so
// this is a deliberate duplicate. Keep the two literal values equal by hand if either changes.
const ownerRepoSep = "__"

// ResolveScenarioPath resolves the scenario clone directory for scenarioURL under
// workspaceRoot.
//
// The primary layout is owner-qualified: <workspaceRoot>/.repo/<owner>__<repo>/, where owner
// and repo are the last two path segments of scenarioURL with a trailing ".git" stripped (a
// URL with only one path segment has no owner and keeps the bare repo name, so the directory
// name is just <repo> with no separator prefix). Keying on the repo name alone once gave two
// orgs' same-named scenario repos ONE directory, silently running the wrong org's step graph
// (aihub#327); the owner-qualified layout exists to make that collision impossible.
//
// The legacy layout, <workspaceRoot>/.repo/<repo>/, is used ONLY when the owner-qualified path
// is absent AND sameRemote reports that `git -C <legacy> remote get-url origin` names the same
// repo as scenarioURL: host compared case-insensitively, full path compared exactly (not just
// the last two path segments), scheme/credentials/".git" ignored. If the legacy directory is
// absent, has no origin remote, or that origin names a different host or path,
// ResolveScenarioPath returns an error rather than silently falling back; an unguarded fallback
// re-opens the exact mix-up the owner-qualified layout exists to prevent.
//
// Presence of the owner-qualified path is decided by the injected gitDirExists closure, not by
// asking git: `git -C <dir> rev-parse --git-dir` succeeds (and reports an ENCLOSING repo's
// .git) for an existing-but-not-a-repo directory nested inside any git work tree, which is
// exactly this workspace's own shape (.repo/ sits inside a git work tree), so it cannot be used
// as a presence probe. Production wires gitDirExists to a filesystem check of <dir>/.git,
// matching internal/cli/init.go's syncScenarioClone presence probe.
func ResolveScenarioPath(git GitRunner, gitDirExists func(path string) bool, workspaceRoot, scenarioURL string) (path string, legacy bool, err error) {
	owner, repo, err := parseOwnerRepo(scenarioURL)
	if err != nil {
		return "", false, fmt.Errorf("engine: ResolveScenarioPath: scenario URL %q: %w", scenarioURL, err)
	}

	repo = sanitizePathSegment(repo)
	if repo == "" {
		return "", false, fmt.Errorf(
			"engine: ResolveScenarioPath: scenario URL %q has an empty or traversal repo path segment",
			scenarioURL)
	}
	if owner != "" {
		owner = sanitizePathSegment(owner)
		if owner == "" {
			return "", false, fmt.Errorf(
				"engine: ResolveScenarioPath: scenario URL %q has an empty or traversal owner path segment",
				scenarioURL)
		}
	}

	ownerQualifiedDir := repo
	if owner != "" {
		ownerQualifiedDir = owner + ownerRepoSep + repo
	}
	ownerQualifiedPath := filepath.Join(workspaceRoot, ".repo", ownerQualifiedDir)
	if gitDirExists(ownerQualifiedPath) {
		return ownerQualifiedPath, false, nil
	}

	legacyPath := filepath.Join(workspaceRoot, ".repo", repo)
	origin, gerr := git(legacyPath, "remote", "get-url", "origin")
	if gerr != nil {
		return "", false, fmt.Errorf(
			"engine: ResolveScenarioPath: owner-qualified clone %q not found, and legacy clone "+
				"%q is absent or has no origin remote (%w); run polyforge init with a binary that "+
				"knows the owner-qualified .repo/<owner>__<repo> layout",
			ownerQualifiedPath, legacyPath, gerr)
	}
	if !sameRemote(origin, scenarioURL) {
		return "", false, fmt.Errorf(
			"engine: ResolveScenarioPath: legacy clone %q has origin %q, which does not match "+
				"project.scenario %q (compared on host and full path, not just the last two "+
				"path segments); this workspace has not been `polyforge init`-ed with a binary "+
				"that knows the owner-qualified layout, and trusting the mismatched legacy "+
				"clone would silently run the wrong org's step graph",
			legacyPath, origin, scenarioURL)
	}
	return legacyPath, true, nil
}

// normalizeRemote and sameRemote are a deliberate duplicate of internal/cli/init.go's
// normalizeGitRemote and sameGitRemote (see ownerRepoSep's comment for why duplicated rather
// than imported: internal/cli already imports internal/engine). Keep this pair's behavior
// identical to the internal/cli original by hand.
func normalizeRemote(url string) (host, path string) {
	s := strings.TrimSpace(url)
	s = strings.TrimSuffix(s, "/")
	s = strings.TrimSuffix(s, ".git")
	var authority, rest string
	if i := strings.Index(s, "://"); i >= 0 {
		s = s[i+3:]
		if j := strings.Index(s, "/"); j >= 0 {
			authority, rest = s[:j], s[j+1:]
		} else {
			authority, rest = s, ""
		}
	} else if i := strings.Index(s, ":"); i >= 0 {
		authority, rest = s[:i], s[i+1:]
	} else {
		return "", ""
	}
	if at := strings.LastIndex(authority, "@"); at >= 0 {
		authority = authority[at+1:]
	}
	if c := strings.LastIndex(authority, ":"); c >= 0 && isAllDigitsRemote(authority[c+1:]) {
		authority = authority[:c]
	}
	return authority, strings.Trim(rest, "/")
}

// sameRemote reports whether a and b name the same git remote: host compared
// case-insensitively, full path compared exactly. An empty parsed path on either side is never
// a match (an unparseable URL is never "the same" as anything), matching
// internal/cli/init.go's sameGitRemote.
func sameRemote(a, b string) bool {
	ha, pa := normalizeRemote(a)
	hb, pb := normalizeRemote(b)
	if pa == "" || pb == "" {
		return false
	}
	return strings.EqualFold(ha, hb) && pa == pb
}

// isAllDigitsRemote reports whether s is non-empty and every rune is an ASCII digit; used by
// normalizeRemote to recognize a ":<port>" suffix on the host.
func isAllDigitsRemote(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

// sanitizePathSegment is a deliberate duplicate of internal/cli/init.go's helper of the same
// name (see ownerRepoSep's comment for why duplicated rather than imported). It maps a path
// segment that is empty, ".", or ".." to "" (both signal "do not trust this as a path
// component"), and maps '/', '\', and NUL to '_', so an owner or repo name derived from a
// scenario URL cannot escape workspaceRoot/.repo/ via filepath.Join.
func sanitizePathSegment(s string) string {
	switch s {
	case "", ".", "..":
		return ""
	}
	return strings.Map(func(r rune) rune {
		switch r {
		case '/', '\\', 0:
			return '_'
		}
		return r
	}, s)
}

// parseOwnerRepo extracts the last two path segments of a git remote URL as (owner, repo),
// tolerating every scheme this codebase's scenario URLs use: scp-like
// ("git@github.com:OWNER/REPO.git"), https ("https://github.com/OWNER/REPO.git", optionally
// with embedded credentials), ssh ("ssh://git@github.com/OWNER/REPO.git"), and a bare repo
// name with no owner segment at all. A trailing ".git" is stripped first; the remainder is
// split on runs of '/' and ':' (both act as path separators across these schemes, and treating
// a run of either as one boundary is what keeps "https://" and "user:token@host" from
// producing spurious empty segments); the last two non-empty tokens are owner and repo. A
// single-token result has no owner: owner is "".
func parseOwnerRepo(rawURL string) (owner, repo string, err error) {
	trimmed := strings.TrimSpace(rawURL)
	if trimmed == "" {
		return "", "", fmt.Errorf("empty URL")
	}
	trimmed = strings.TrimSuffix(trimmed, ".git")
	parts := strings.FieldsFunc(trimmed, func(r rune) bool {
		return r == '/' || r == ':'
	})
	switch len(parts) {
	case 0:
		return "", "", fmt.Errorf("%q has no path segments", rawURL)
	case 1:
		return "", parts[0], nil
	default:
		return parts[len(parts)-2], parts[len(parts)-1], nil
	}
}

// PinScenarioSHA resolves HEAD of the scenario clone at scenarioPath. Writing it into
// <worktree_root>/.pf_meta.json (alongside ScenarioMeta.StartedAt) is the CALLER's job — a
// worktree-root file write is outside this package's scope — so this returns only the sha.
func PinScenarioSHA(git GitRunner, scenarioPath string) (sha string, err error) {
	out, gerr := git(scenarioPath, "rev-parse", "HEAD")
	if gerr != nil {
		return "", fmt.Errorf("engine: PinScenarioSHA: git rev-parse HEAD in %q: %w", scenarioPath, gerr)
	}
	sha = strings.TrimSpace(out)
	if sha == "" {
		return "", fmt.Errorf("engine: PinScenarioSHA: git rev-parse HEAD in %q returned empty output", scenarioPath)
	}
	return sha, nil
}

// ScenarioMeta mirrors .pf_meta.json's two keys verbatim, so the caller that writes that file
// doesn't invent its own field names.
type ScenarioMeta struct {
	ScenarioSHA string `json:"scenario_sha"`
	StartedAt   string `json:"started_at"`
}

// ResolveTemplate walks the two-rung wi_type template fallback chain at the pinned sha:
// {wiType}.{project}.md (project-specific, tried only when project != "") falling back to
// {wiType}.md (generic). source reports which rung matched, "project" or "generic". When
// project == "" only the generic rung is tried. If neither exists, ResolveTemplate returns an
// error — deciding to call pf_complete_attempt(failed) and report the available .md files is
// the CALLER's job, not this function's.
func ResolveTemplate(git GitRunner, scenarioPath, sha, wiType, project string) (content, source string, err error) {
	if project != "" {
		projectPath := fmt.Sprintf("%s.%s.md", wiType, project)
		if out, gerr := git(scenarioPath, "show", sha+":"+projectPath); gerr == nil {
			return out, "project", nil
		}
	}

	genericPath := fmt.Sprintf("%s.md", wiType)
	out, gerr := git(scenarioPath, "show", sha+":"+genericPath)
	if gerr != nil {
		if project != "" {
			return "", "", fmt.Errorf(
				"engine: ResolveTemplate: neither %s.%s.md nor %s.md exists at %s in %s",
				wiType, project, wiType, sha, scenarioPath)
		}
		return "", "", fmt.Errorf(
			"engine: ResolveTemplate: %s.md does not exist at %s in %s: %w",
			wiType, sha, scenarioPath, gerr)
	}
	return out, "generic", nil
}

// Step is one "## Step: <id>" section of a template, in document order.
type Step struct {
	ID      string
	Content string
}

// stepHeadingRe matches a step heading at strict line start: "## Step: <id>", where <id> is
// one or more word characters, optionally followed by trailing whitespace and nothing else.
var stepHeadingRe = regexp.MustCompile(`^## Step: (\w+)\s*$`)

// fenceLineRe matches a fenced-code-block delimiter line (``` optionally followed by a
// language tag), used only to toggle in/out of "inside a fence" — both the opening and
// closing delimiter match this same pattern.
var fenceLineRe = regexp.MustCompile("^```")

// ScanSteps splits templateContent into its "## Step: <id>" sections, in document order,
// ignoring any such heading-shaped line that falls inside a fenced code block (``` ... ```) —
// a template that shows a literal "## Step: foo" as an EXAMPLE inside a fence must not be
// mistaken for a real section boundary. Returns an error if no step heading is found at all.
func ScanSteps(templateContent string) ([]Step, error) {
	lines := strings.Split(templateContent, "\n")

	var steps []Step
	var currentID string
	var currentLines []string
	started := false
	inFence := false

	flush := func() {
		if started {
			steps = append(steps, Step{
				ID:      currentID,
				Content: strings.TrimSpace(strings.Join(currentLines, "\n")),
			})
		}
	}

	for _, line := range lines {
		trimmedRight := strings.TrimRight(line, "\r")
		if fenceLineRe.MatchString(trimmedRight) {
			inFence = !inFence
			if started {
				currentLines = append(currentLines, line)
			}
			continue
		}
		if !inFence {
			if m := stepHeadingRe.FindStringSubmatch(trimmedRight); m != nil {
				flush()
				currentID = m[1]
				currentLines = nil
				started = true
				continue
			}
		}
		if started {
			currentLines = append(currentLines, line)
		}
	}
	flush()

	if len(steps) == 0 {
		return nil, fmt.Errorf(`engine: ScanSteps: no "## Step: <id>" sections found`)
	}
	return steps, nil
}

// includeLineRe matches an "@include: <path>" line (leading/trailing whitespace on the line
// tolerated, mirroring ParseDeclaredRole's leniency).
var includeLineRe = regexp.MustCompile(`^@include:\s*(.+?)\s*$`)

// levelLineRe matches a "level: <value>" line, recognised ONLY when it is fed the line
// immediately following an @include: line — see ExpandIncludes.
var levelLineRe = regexp.MustCompile(`^level:\s*(.+?)\s*$`)

// ExpandIncludes expands every "@include: <path>" line in content, fetching each include's
// content via fetch (production: a closure over a GitRunner doing `git show <sha>:<path>`,
// built in internal/cli). "level:" is recognised ONLY on the line immediately after
// "@include:" (the pair-scan rule) — its scope is that one include, and a "level:" line
// anywhere else is ordinary content, left untouched. When a level is present, a
// "Review level: <value>" line is inserted immediately before the expanded content.
//
// A fetch failure (a missing include) is wrapped with the include's own path (fetch's error
// alone does not name it, and neither did the caller: internal/cli/engine.go only adds the
// step id) and remains errors.Is-compatible with the original via %w, matching
// engine-native-details.md §0 step 6 ("if missing, call pf_complete_attempt(failed) and stop");
// deciding to fail the attempt is the caller's job, this function only reports what fetch told
// it, with the path attached.
func ExpandIncludes(content string, fetch func(path string) (string, error)) (string, error) {
	lines := strings.Split(content, "\n")
	out := make([]string, 0, len(lines))

	for i := 0; i < len(lines); i++ {
		m := includeLineRe.FindStringSubmatch(strings.TrimSpace(lines[i]))
		if m == nil {
			out = append(out, lines[i])
			continue
		}

		path := m[1]
		var level string
		consumedNext := false
		if i+1 < len(lines) {
			if lm := levelLineRe.FindStringSubmatch(strings.TrimSpace(lines[i+1])); lm != nil {
				level = lm[1]
				consumedNext = true
			}
		}

		expanded, err := fetch(path)
		if err != nil {
			return "", fmt.Errorf("engine: ExpandIncludes: fetch %q: %w", path, err)
		}
		if level != "" {
			out = append(out, "Review level: "+level)
		}
		out = append(out, expanded)

		if consumedNext {
			i++
		}
	}

	return strings.Join(out, "\n"), nil
}
