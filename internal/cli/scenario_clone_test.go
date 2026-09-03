package cli

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/GMISWE/ieops-aihub/internal/config"
)

// aihub#327 — scenario clones are keyed on the repo NAME alone, so two scenario
// repos with the same name under different owners share one directory. The
// second project's clone is never made; it silently reads the first project's
// step graph instead, and no check anywhere goes red.
//
// WHY THE FIXTURE IS CONSTRUCTED RATHER THAN OBSERVED
// ---------------------------------------------------
// The live instance of this collision (GMISWE/polyforge-coding vs
// yingfang-ai/polyforge-coding) currently has BYTE-IDENTICAL templates, so a
// test driven from the real repos would pass in both states and prove nothing.
// These fixtures therefore make the two same-named repos differ, which is the
// only arrangement in which "the wrong one was used" is observable at all.
//
// The pair is deliberate: TestScenarioClonesDoNotCollideAcrossOwners is the
// collision, and TestScenarioCloneSingleOwnerStillWorks is the control. A "fix"
// that refused every scenario, or cloned nothing, would satisfy the first alone.

// scenarioRepoBasename is the name BOTH fixture origins carry. It is the whole
// point of the fixture: the two URLs differ only in their owner segment.
const scenarioRepoBasename = "polyforge-coding"

func mustGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	full := append([]string{"-C", dir}, args...)
	out, err := exec.Command("git", full...).CombinedOutput()
	if err != nil {
		t.Fatalf("git -C %s %s: %v\n%s", dir, strings.Join(args, " "), err, out)
	}
	return strings.TrimSpace(string(out))
}

// newScenarioOrigin creates a bare repo at <root>/<owner>/<repo>.git holding a
// single feature.md whose body is `body`, and returns its file:// URL.
//
// A file:// URL keeps the suite hermetic (no network, no gh token) while still
// exercising the real `git clone` / `git fetch` / `git reset` path that init
// uses — the defect lives in the destination PATH, not in the transport.
func newScenarioOrigin(t *testing.T, root, owner, repo, body string) string {
	t.Helper()
	bare := filepath.Join(root, owner, repo+".git")
	if err := os.MkdirAll(filepath.Dir(bare), 0o755); err != nil {
		t.Fatalf("mkdir origin parent: %v", err)
	}
	if out, err := exec.Command("git", "init", "--bare", "-q", "-b", "main", bare).CombinedOutput(); err != nil {
		t.Fatalf("git init --bare: %v\n%s", err, out)
	}

	work := filepath.Join(t.TempDir(), owner+"-seed")
	if out, err := exec.Command("git", "clone", "-q", bare, work).CombinedOutput(); err != nil {
		t.Fatalf("git clone seed: %v\n%s", err, out)
	}
	mustGit(t, work, "config", "user.email", "t@t.test")
	mustGit(t, work, "config", "user.name", "t")
	if err := os.WriteFile(filepath.Join(work, "feature.md"), []byte(body), 0o644); err != nil {
		t.Fatalf("write feature.md: %v", err)
	}
	mustGit(t, work, "add", "feature.md")
	mustGit(t, work, "commit", "-q", "-m", "seed "+owner)
	mustGit(t, work, "push", "-q", "-u", "origin", "main")
	return "file://" + bare
}

const fixtureUID = "u_test"

func scenarioProject(name, url string) serverProject {
	u := url
	return serverProject{
		Name:        name,
		Visible:     true,
		OwnerUserID: fixtureUID,
		Scenario:    &u,
	}
}

// readTemplate returns the feature.md the workspace would serve out of `dir`.
func readTemplate(t *testing.T, repoDir, dir string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(repoDir, dir, "feature.md"))
	if err != nil {
		t.Fatalf("scenario clone %s has no feature.md: %v", dir, err)
	}
	return strings.TrimSpace(string(b))
}

func mustReadDirNames(t *testing.T, dir string) []string {
	t.Helper()
	ents, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read %s: %v", dir, err)
	}
	var names []string
	for _, e := range ents {
		names = append(names, e.Name())
	}
	return names
}

// TestScenarioClonesDoNotCollideAcrossOwners is the constructed collision.
//
// Two projects, two scenario repos that share a basename and differ in owner,
// and templates that DIFFER. Each project must end up reading its own.
func TestScenarioClonesDoNotCollideAcrossOwners(t *testing.T) {
	root := t.TempDir()
	origins := filepath.Join(root, "origins")

	alphaURL := newScenarioOrigin(t, origins, "alpha-org", scenarioRepoBasename, "ALPHA STEP GRAPH")
	betaURL := newScenarioOrigin(t, origins, "beta-org", scenarioRepoBasename, "BETA STEP GRAPH")

	repoDir := filepath.Join(root, "ws", ".repo")
	if err := os.MkdirAll(repoDir, 0o755); err != nil {
		t.Fatalf("mkdir .repo: %v", err)
	}

	projects := []serverProject{
		scenarioProject("alpha", alphaURL),
		scenarioProject("beta", betaURL),
	}
	outcomes := cloneScenarioRepos(repoDir, projects, fixtureUID)

	byProject := map[string]scenarioCloneOutcome{}
	for _, o := range outcomes {
		byProject[o.Project] = o
	}
	for _, p := range []string{"alpha", "beta"} {
		o, ok := byProject[p]
		if !ok {
			t.Fatalf("project %s produced no scenario clone outcome at all; got %+v", p, outcomes)
		}
		if o.Status != scenarioCloneOK {
			t.Fatalf("project %s: scenario clone status = %q (%s), want %q",
				p, o.Status, o.Detail, scenarioCloneOK)
		}
	}

	if byProject["alpha"].Dir == byProject["beta"].Dir {
		t.Fatalf("both scenario repos landed in the same directory %q — this is the collision: "+
			"the path is keyed on the repo name %q and drops the owner, so %q never gets cloned "+
			"and silently reads the other org's step graph",
			byProject["alpha"].Dir, scenarioRepoBasename, betaURL)
	}

	// The load-bearing half: the right CONTENT, not merely two directories.
	if got := readTemplate(t, repoDir, byProject["alpha"].Dir); got != "ALPHA STEP GRAPH" {
		t.Errorf("project alpha reads %q from .repo/%s, want %q",
			got, byProject["alpha"].Dir, "ALPHA STEP GRAPH")
	}
	if got := readTemplate(t, repoDir, byProject["beta"].Dir); got != "BETA STEP GRAPH" {
		t.Errorf("project beta reads %q from .repo/%s, want %q — beta is being served alpha's "+
			"step graph, which is aihub#327 exactly",
			got, byProject["beta"].Dir, "BETA STEP GRAPH")
	}

	// And each clone must actually point at its own origin, so that a later
	// fetch keeps serving the same org's templates.
	for _, p := range []string{"alpha", "beta"} {
		want := *projects[map[string]int{"alpha": 0, "beta": 1}[p]].Scenario
		got := mustGit(t, filepath.Join(repoDir, byProject[p].Dir), "remote", "get-url", "origin")
		if got != want {
			t.Errorf("project %s: .repo/%s origin = %q, want %q",
				p, byProject[p].Dir, got, want)
		}
	}
}

// TestScenarioCloneSingleOwnerStillWorks is the control half of the pair: the
// ordinary one-scenario workspace must keep working, and a second init pass over
// it must be a no-op rather than a re-clone or an error.
func TestScenarioCloneSingleOwnerStillWorks(t *testing.T) {
	root := t.TempDir()
	origins := filepath.Join(root, "origins")
	url := newScenarioOrigin(t, origins, "GMISWE", scenarioRepoBasename, "ONLY STEP GRAPH")

	repoDir := filepath.Join(root, "ws", ".repo")
	if err := os.MkdirAll(repoDir, 0o755); err != nil {
		t.Fatalf("mkdir .repo: %v", err)
	}
	projects := []serverProject{scenarioProject("solo", url)}

	first := cloneScenarioRepos(repoDir, projects, fixtureUID)
	if len(first) != 1 || first[0].Status != scenarioCloneOK {
		t.Fatalf("first pass: %+v, want one %q outcome", first, scenarioCloneOK)
	}
	dir := first[0].Dir
	if got := readTemplate(t, repoDir, dir); got != "ONLY STEP GRAPH" {
		t.Fatalf("first pass: .repo/%s/feature.md = %q, want %q", dir, got, "ONLY STEP GRAPH")
	}
	firstNames := mustReadDirNames(t, repoDir)

	// Idempotence: same inputs, same directory, same content, no new litter.
	second := cloneScenarioRepos(repoDir, projects, fixtureUID)
	if len(second) != 1 || second[0].Status != scenarioCloneOK {
		t.Fatalf("second pass: %+v, want one %q outcome", second, scenarioCloneOK)
	}
	if second[0].Dir != dir {
		t.Errorf("second pass landed in .repo/%s, first pass in .repo/%s — init is not idempotent",
			second[0].Dir, dir)
	}
	if got := readTemplate(t, repoDir, dir); got != "ONLY STEP GRAPH" {
		t.Errorf("second pass: .repo/%s/feature.md = %q, want %q", dir, got, "ONLY STEP GRAPH")
	}
	if got := mustReadDirNames(t, repoDir); len(got) != len(firstNames) {
		t.Errorf("second pass changed .repo/ from %v to %v — a second init must not add a directory",
			firstNames, got)
	}
}

// TestScenarioCloneRefusesADirectoryHoldingAnotherRemote is the residual-collision
// guard. Owner-qualifying the path removes the collision this work item is about,
// but it cannot remove every one (two hosts, same owner/repo; a hand-made or
// pre-existing directory). Whatever is left must FAIL LOUDLY instead of silently
// fetch+resetting someone else's clone, which is what cloneOrSync does today.
func TestScenarioCloneRefusesADirectoryHoldingAnotherRemote(t *testing.T) {
	root := t.TempDir()
	origins := filepath.Join(root, "origins")
	alphaURL := newScenarioOrigin(t, origins, "alpha-org", scenarioRepoBasename, "ALPHA STEP GRAPH")
	betaURL := newScenarioOrigin(t, origins, "beta-org", scenarioRepoBasename, "BETA STEP GRAPH")

	repoDir := filepath.Join(root, "ws", ".repo")
	if err := os.MkdirAll(repoDir, 0o755); err != nil {
		t.Fatalf("mkdir .repo: %v", err)
	}

	// Learn the directory beta WOULD use, then plant alpha's clone there.
	planned := cloneScenarioRepos(repoDir, []serverProject{scenarioProject("beta", betaURL)}, fixtureUID)
	if len(planned) != 1 || planned[0].Dir == "" {
		t.Fatalf("could not learn beta's scenario directory: %+v", planned)
	}
	dir := planned[0].Dir
	if err := os.RemoveAll(filepath.Join(repoDir, dir)); err != nil {
		t.Fatalf("clear planted dir: %v", err)
	}
	if out, err := exec.Command("git", "clone", "-q", alphaURL, filepath.Join(repoDir, dir)).CombinedOutput(); err != nil {
		t.Fatalf("plant alpha clone: %v\n%s", err, out)
	}

	got := cloneScenarioRepos(repoDir, []serverProject{scenarioProject("beta", betaURL)}, fixtureUID)
	if len(got) != 1 {
		t.Fatalf("outcomes = %+v, want exactly one", got)
	}
	if got[0].Status != scenarioCloneMismatch {
		t.Errorf("status = %q (%s), want %q: .repo/%s holds %s, not the declared %s",
			got[0].Status, got[0].Detail, scenarioCloneMismatch, dir, alphaURL, betaURL)
	}
	if body := readTemplate(t, repoDir, dir); body != "ALPHA STEP GRAPH" {
		t.Errorf(".repo/%s/feature.md = %q — the mismatched directory was written to; "+
			"a refusal must leave it exactly as it was", dir, body)
	}
}

// TestScenarioCloneRecoversFromAnEmptyLeftoverDirectory covers the state the
// refusal above would otherwise make permanent. An interrupted clone can leave
// an empty directory at the destination; an unconditional refusal would mean
// `polyforge init` could never recover from its own half-finished work.
//
// Paired with the negative: a directory that is NOT empty is someone's, and must
// be declined rather than deleted.
func TestScenarioCloneRecoversFromAnEmptyLeftoverDirectory(t *testing.T) {
	root := t.TempDir()
	origins := filepath.Join(root, "origins")
	url := newScenarioOrigin(t, origins, "GMISWE", scenarioRepoBasename, "STEP GRAPH")
	dir := scenarioDirName(url)

	repoDir := filepath.Join(root, "ws", ".repo")
	if err := os.MkdirAll(filepath.Join(repoDir, dir), 0o755); err != nil {
		t.Fatalf("plant empty leftover: %v", err)
	}
	got := cloneScenarioRepos(repoDir, []serverProject{scenarioProject("solo", url)}, fixtureUID)
	if len(got) != 1 || got[0].Status != scenarioCloneOK {
		t.Fatalf("empty leftover: %+v, want %q — init cannot recover from its own "+
			"interrupted clone", got, scenarioCloneOK)
	}
	if body := readTemplate(t, repoDir, dir); body != "STEP GRAPH" {
		t.Errorf(".repo/%s/feature.md = %q, want %q", dir, body, "STEP GRAPH")
	}

	// Negative: not empty => decline, and leave every byte where it was.
	root2 := t.TempDir()
	repoDir2 := filepath.Join(root2, "ws", ".repo")
	if err := os.MkdirAll(filepath.Join(repoDir2, dir), 0o755); err != nil {
		t.Fatalf("plant occupied leftover: %v", err)
	}
	keep := filepath.Join(repoDir2, dir, "someones-file.txt")
	if err := os.WriteFile(keep, []byte("do not delete me"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	got2 := cloneScenarioRepos(repoDir2, []serverProject{scenarioProject("solo", url)}, fixtureUID)
	if len(got2) != 1 || got2[0].Status != scenarioCloneMismatch {
		t.Errorf("occupied leftover: %+v, want %q", got2, scenarioCloneMismatch)
	}
	if b, err := os.ReadFile(keep); err != nil || string(b) != "do not delete me" {
		t.Errorf("occupied leftover: %s = %q (err %v) — init deleted or overwrote a file "+
			"that was not its own", keep, string(b), err)
	}
}

// TestCloneOrSyncKeepsLocalModifications pins the other half of "idempotent":
// syncing an existing clone must not silently throw away tracked-file edits.
// cloneOrSync's `git reset --hard` did exactly that, with no message.
//
// Untracked files are deliberately NOT covered — `git reset --hard` does not
// remove them, so making them block the reset would strand clones as stale over
// build output that was never at risk.
func TestCloneOrSyncKeepsLocalModifications(t *testing.T) {
	root := t.TempDir()
	origins := filepath.Join(root, "origins")
	url := newScenarioOrigin(t, origins, "GMISWE", scenarioRepoBasename, "UPSTREAM V1")

	repoDir := filepath.Join(root, "ws", ".repo")
	if err := os.MkdirAll(repoDir, 0o755); err != nil {
		t.Fatalf("mkdir .repo: %v", err)
	}
	cloneOrSync(repoDir, "demo", url)
	local := filepath.Join(repoDir, "demo")
	if _, err := os.Stat(local); err != nil {
		t.Fatalf("clone did not land: %v", err)
	}

	// Local edit to a tracked file, and an unrelated upstream advance so the
	// sync path has a real reset to perform.
	if err := os.WriteFile(filepath.Join(local, "feature.md"), []byte("LOCAL EDIT"), 0o644); err != nil {
		t.Fatalf("local edit: %v", err)
	}
	seed := filepath.Join(t.TempDir(), "advance")
	if out, err := exec.Command("git", "clone", "-q", url, seed).CombinedOutput(); err != nil {
		t.Fatalf("clone for advance: %v\n%s", err, out)
	}
	mustGit(t, seed, "config", "user.email", "t@t.test")
	mustGit(t, seed, "config", "user.name", "t")
	if err := os.WriteFile(filepath.Join(seed, "other.md"), []byte("v2"), 0o644); err != nil {
		t.Fatalf("advance write: %v", err)
	}
	mustGit(t, seed, "add", "other.md")
	mustGit(t, seed, "commit", "-q", "-m", "advance")
	mustGit(t, seed, "push", "-q", "origin", "main")

	cloneOrSync(repoDir, "demo", url)

	b, err := os.ReadFile(filepath.Join(local, "feature.md"))
	if err != nil {
		t.Fatalf("read after sync: %v", err)
	}
	if string(b) != "LOCAL EDIT" {
		t.Errorf(".repo/demo/feature.md = %q after sync, want %q — `polyforge init` discarded a "+
			"tracked local modification with no message", string(b), "LOCAL EDIT")
	}
}

// TestScenarioCloneReportsAFailedClone keeps the outcome list honest. Every other
// case in this file asserts on Status, so a cloneScenarioRepos that returned
// scenarioCloneOK unconditionally would still satisfy most of them — and callers
// (RunInit, and anything that grows out of it) would print nothing when a
// scenario never landed.
func TestScenarioCloneReportsAFailedClone(t *testing.T) {
	root := t.TempDir()
	repoDir := filepath.Join(root, "ws", ".repo")
	if err := os.MkdirAll(repoDir, 0o755); err != nil {
		t.Fatalf("mkdir .repo: %v", err)
	}
	missing := "file://" + filepath.Join(root, "origins", "nobody", "not-a-repo.git")

	got := cloneScenarioRepos(repoDir, []serverProject{scenarioProject("p", missing)}, fixtureUID)
	if len(got) != 1 {
		t.Fatalf("outcomes = %+v, want exactly one", got)
	}
	if got[0].Status != scenarioCloneFailed {
		t.Errorf("status = %q (%s), want %q for a URL that cannot be cloned",
			got[0].Status, got[0].Detail, scenarioCloneFailed)
	}
}

// TestScenarioCloneSkipsAValueThatIsNotAURL covers the other non-OK exit: a
// project whose scenario is a bare logical name has no directory to derive, and
// must be reported rather than turned into a directory named after itself.
func TestScenarioCloneSkipsAValueThatIsNotAURL(t *testing.T) {
	repoDir := filepath.Join(t.TempDir(), ".repo")
	if err := os.MkdirAll(repoDir, 0o755); err != nil {
		t.Fatalf("mkdir .repo: %v", err)
	}
	got := cloneScenarioRepos(repoDir, []serverProject{scenarioProject("p", "coding")}, fixtureUID)
	if len(got) != 1 || got[0].Status != scenarioCloneUnparseable {
		t.Fatalf("outcomes = %+v, want one %q", got, scenarioCloneUnparseable)
	}
	if names := mustReadDirNames(t, repoDir); len(names) != 0 {
		t.Errorf(".repo/ = %v, want empty — a non-URL scenario must not create a directory", names)
	}
}

// TestScenarioDirNameAndRemoteMatchAgree pins the derivation itself, including
// the shapes the end-to-end cases above cannot reach with a file:// fixture.
//
// The two functions are asserted TOGETHER on purpose. The directory a URL clones
// into and the comparison that decides whether an existing directory belongs to
// that URL have to accept the same set of inputs: if they disagreed, a URL would
// get a directory on the first init and be called a mismatch on the second, and
// `polyforge init` would stop being idempotent for that URL alone.
func TestScenarioDirNameAndRemoteMatchAgree(t *testing.T) {
	for _, tc := range []struct {
		name string
		url  string
		dir  string
	}{
		{"scp-style github", "git@github.com:GMISWE/polyforge-coding.git", "GMISWE__polyforge-coding"},
		{"https github", "https://github.com/GMISWE/polyforge-coding.git", "GMISWE__polyforge-coding"},
		{"https no .git", "https://github.com/yingfang-ai/polyforge-coding", "yingfang-ai__polyforge-coding"},
		{"ssh scheme", "ssh://git@github.com/GMISWE/polyforge-coding.git", "GMISWE__polyforge-coding"},
		{"token in url", "https://ghp_tok@github.com/GMISWE/polyforge-coding.git", "GMISWE__polyforge-coding"},
		{"host with port", "https://git.example.com:8443/org/repo.git", "org__repo"},
		{"gitlab subgroup keeps the last group", "https://gitlab.com/grp/sub/repo.git", "sub__repo"},
		{"trailing slash", "https://github.com/GMISWE/polyforge-coding/", "GMISWE__polyforge-coding"},
		{"single path segment keeps the bare name", "ssh://git@host/repo.git", "repo"},
		{"file url", "file:///srv/mirrors/acme/repo.git", "acme__repo"},
		{"not a url", "coding", ""},
		{"empty", "", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := scenarioDirName(tc.url); got != tc.dir {
				t.Errorf("scenarioDirName(%q) = %q, want %q", tc.url, got, tc.dir)
			}
			// Every URL that gets a directory must also match itself, or the
			// second init would refuse the clone the first one made.
			if tc.dir != "" && !sameGitRemote(tc.url, tc.url) {
				t.Errorf("sameGitRemote(%q, %q) = false — init would refuse its own clone", tc.url, tc.url)
			}
		})
	}
}

// TestSameGitRemoteDiscriminates is the negative half: the check that guards the
// refusal must not be a function that says "yes" to everything. Equivalent
// spellings of one repo match (they have to — runClone rewrites SSH to
// token-bearing HTTPS on fallback, so a clone's origin legitimately differs from
// the declared URL) and different repos do not.
func TestSameGitRemoteDiscriminates(t *testing.T) {
	same := [][2]string{
		{"git@github.com:GMISWE/polyforge-coding.git", "https://github.com/GMISWE/polyforge-coding.git"},
		{"git@github.com:GMISWE/polyforge-coding.git", "https://ghp_token@github.com/GMISWE/polyforge-coding.git"},
		{"https://github.com/GMISWE/polyforge-coding", "https://GitHub.com/GMISWE/polyforge-coding.git/"},
		{"ssh://git@github.com/GMISWE/x.git", "git@github.com:GMISWE/x"},
	}
	for _, p := range same {
		if !sameGitRemote(p[0], p[1]) {
			t.Errorf("sameGitRemote(%q, %q) = false, want true — a token-cloned or "+
				"scheme-rewritten origin would be reported as someone else's repo", p[0], p[1])
		}
	}
	differ := [][2]string{
		{"git@github.com:GMISWE/polyforge-coding.git", "git@github.com:yingfang-ai/polyforge-coding.git"},
		{"git@github.com:GMISWE/polyforge-coding.git", "git@gitlab.com:GMISWE/polyforge-coding.git"},
		{"git@github.com:GMISWE/a.git", "git@github.com:GMISWE/b.git"},
		{"coding", "coding"}, // not a URL at all: must not match, not even itself
	}
	for _, p := range differ {
		if sameGitRemote(p[0], p[1]) {
			t.Errorf("sameGitRemote(%q, %q) = true, want false — the mismatch guard would "+
				"fetch+reset one project's scenario clone for another's URL", p[0], p[1])
		}
	}
}

// TestDoctorChecksScenarioClones covers the third surface. `polyforge doctor`
// walked proj.Repos only, so the scenario clone — the one directory whose
// contents decide which step graph every wi in the project runs — was the one
// thing under .repo/ nothing verified. Two states, because a check that only
// fired on the broken layout could be a check that always fires.
func TestDoctorChecksScenarioClones(t *testing.T) {
	root := t.TempDir()
	origins := filepath.Join(root, "origins")
	url := newScenarioOrigin(t, origins, "GMISWE", scenarioRepoBasename, "STEP GRAPH")

	ws := filepath.Join(root, "ws")
	repoDir := filepath.Join(ws, ".repo")
	if err := os.MkdirAll(repoDir, 0o755); err != nil {
		t.Fatalf("mkdir .repo: %v", err)
	}
	cfg := &config.Config{Projects: map[string]config.Project{
		"solo": {Scenario: url},
	}}

	// State 1 — the pre-upgrade layout: the clone exists, but under the bare
	// repo name. doctor must say so, because that is the workspace where a
	// second org's scenario would silently resolve to this one.
	if out, err := exec.Command("git", "clone", "-q", url,
		filepath.Join(repoDir, scenarioRepoBasename)).CombinedOutput(); err != nil {
		t.Fatalf("plant legacy clone: %v\n%s", err, out)
	}
	got := checkRepos(ws, cfg)
	if got.Status != "warning" || !strings.Contains(got.Message, scenarioDirName(url)) {
		t.Errorf("legacy layout: checkRepos = %+v, want a warning naming %q — a clone under "+
			"the bare repo name is exactly the state this layout change leaves behind",
			got, scenarioDirName(url))
	}

	// State 2 — after `polyforge init` on the new binary. Same workspace, same
	// leftover directory, plus the owner-qualified clone: healthy.
	if got := cloneScenarioRepos(repoDir, []serverProject{scenarioProject("solo", url)}, fixtureUID); got[0].Status != scenarioCloneOK {
		t.Fatalf("clone to the owner-qualified path: %+v", got)
	}
	if got := checkRepos(ws, cfg); got.Status != "ok" {
		t.Errorf("after init: checkRepos = %+v, want ok — the leftover .repo/%s must not keep "+
			"the workspace permanently in warning", got, scenarioRepoBasename)
	}
}

// --- the read side ------------------------------------------------------------
//
// Nothing in Go reads the scenario clone: pf-work and the native pf-execute
// engine derive the path themselves, from prose, in the agent's head. So the
// writer moving on its own would not fix the bug, it would relocate it — the
// agent would keep opening the old colliding directory. These are the documents
// that carry the derivation, and they must agree with the producer.

// scenarioPathDocs are the delivered surfaces that derive the scenario clone path.
// Each carries an anchor that must genuinely be present, so a renamed, moved or
// emptied file fails loudly instead of satisfying the bans for free.
var scenarioPathDocs = []struct {
	rel    string
	anchor string
}{
	{"skills/pf-work/SKILL.md", "### Mode A — New wi"},
	{"skills/pf-execute/engine.native.md", "## Execute (rhs=false, auto mode)"},
	{"skills/pf-execute/references/engine-native-details.md", "--- step instructions ---"},
}

// legacyScenarioSpellings are the derivations that drop the owner. Any one of
// them tells a session to open the colliding directory.
var legacyScenarioSpellings = []string{
	".repo/<scenario_name>",
	"<last path segment of URL, strip .git>",
	".repo/polyforge-coding", // an owner hardcoded into prose is the same bug, pre-collided
}

func pluginRoot(t *testing.T) string {
	t.Helper()
	root := filepath.Join("..", "..", "plugins", "polyforge")
	if _, err := os.Stat(root); err != nil {
		t.Fatalf("plugin root %s not found: %v", root, err)
	}
	return root
}

// TestScenarioPathSpellingIsOwnerQualified gates the read side against the
// producer. The producer is the oracle: internal/cli/init.go joins owner and repo
// with scenarioDirSep, so if that layout ever changes this test fails and names
// the prose that has to change with it — rather than several hand-written doc
// strings quietly agreeing with each other while all of them are wrong.
func TestScenarioPathSpellingIsOwnerQualified(t *testing.T) {
	producer, err := os.ReadFile("init.go")
	if err != nil {
		t.Fatalf("read scenario path producer: %v", err)
	}
	if !strings.Contains(string(producer), `scenarioDirSep = "__"`) {
		t.Fatalf("internal/cli/init.go no longer joins owner and repo with a `__` separator. " +
			"The documented spelling <owner>__<repo> is now unverified — re-derive it from the " +
			"new producer and update every delivered surface.")
	}
	canonical := "<owner>" + scenarioDirSep + "<repo>"

	root := pluginRoot(t)
	documented := 0
	for _, d := range scenarioPathDocs {
		b, rerr := os.ReadFile(filepath.Join(root, d.rel))
		if rerr != nil {
			t.Fatalf("read %s: %v", d.rel, rerr)
		}
		body := string(b)
		if !strings.Contains(body, d.anchor) {
			t.Fatalf("%s no longer contains its anchor %q — this file was renamed, moved or "+
				"rewritten, so the bans below would pass while asserting nothing", d.rel, d.anchor)
		}
		for _, legacy := range legacyScenarioSpellings {
			if strings.Contains(body, legacy) {
				t.Errorf("%s still derives the scenario path as %q, but the producer writes "+
					"%s. A session told the legacy form opens the directory two owners share, "+
					"which is the defect (aihub#327), not a cosmetic difference.",
					d.rel, legacy, canonical)
			}
		}
		documented += strings.Count(body, canonical)
	}
	if documented == 0 {
		t.Errorf("no delivered surface documents the scenario layout %s at all — this test "+
			"would pass vacuously, so the absence is itself the failure", canonical)
	}
}

// TestScenarioReadFallbackIsRemoteChecked covers the one thing the layout change
// cannot do on its own: the plugin and the CLI binary ship on separate channels,
// so a session can run new skills against a workspace whose last `polyforge init`
// predates the owner-qualified layout. The documented fallback to the legacy
// directory is what keeps that workspace working — and it is only safe if it
// verifies the legacy clone's origin first. A bare "if the new path is missing,
// use the old one" fallback re-opens the exact silent mix-up in every workspace
// that has not re-inited.
func TestScenarioReadFallbackIsRemoteChecked(t *testing.T) {
	root := pluginRoot(t)
	// BOTH resolvers, not just one. pf-work resolves the scenario when a wi is
	// CREATED and the native engine resolves it when a wi is EXECUTED; a session
	// can reach the clone through either, so guarding one and leaving the other
	// bare would leave the silent mix-up reachable by the other half of the flow.
	for _, rel := range []string{
		"skills/pf-execute/references/engine-native-details.md",
		"skills/pf-work/SKILL.md",
	} {
		b, err := os.ReadFile(filepath.Join(root, rel))
		if err != nil {
			t.Fatalf("read %s: %v", rel, err)
		}
		body := string(b)
		for _, want := range []string{
			"remote get-url origin", // the check itself
			"aihub#327",             // why it is there, findable from the text
		} {
			if !strings.Contains(body, want) {
				t.Errorf("%s does not mention %q — the legacy-path fallback is either absent or "+
					"unguarded, and an unguarded fallback serves another owner's step graph "+
					"silently in every workspace that has not re-run polyforge init", rel, want)
			}
		}
	}
}
