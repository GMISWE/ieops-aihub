package mcp_test

// aihub#543 probe wave 2, the git tail — `docs/mcp-cards/pf_push.md`.
//
// Five sentences of that card, each of which could be false while every other
// arm in the tree stayed green:
//
//   - the lease policy and the branch refusal, both published on the tool
//     description rather than in a skill;
//   - "The only HTTP call is the best-effort `push` event";
//   - "A base-moved rejection is not an error result", carrying
//     `error: "base_moved"` and advice;
//   - `base_sha_at_push` being what a later check can compare the remote
//     against; and
//   - `isBaseMoved` being SHARED with pf_ship while the two response shapes are
//     deliberately not.
//
// 🔴 WHY NOTHING HELD THEM. internal/coding/ship_test.go drives GitPush's
// protected-branch guard as a convenient way to make a push fail, and
// internal/coding/wrap_test.go pushes for real — but neither compares the
// enforced set against the PUBLISHED one, and the published one is where a
// caller learns that "sync my branch" will overwrite somebody's work. The
// base-moved path had no arm at all on the pf_push side: the marker, the advice
// and the success-shaped result are three literals in one handler, and dropping
// any of them turns a refusal into either an error string (a caller keying on
// the marker stops retrying) or a silent overwrite.
//
// No database. git is required and is skipped for explicitly by
// newResolveRepo's requireGitAndSh; the remotes are local bare repositories
// created under the test's own temp dir, never a real remote.
//
//	GOWORK=off go test ./internal/mcp/ -run 'TestPublishedPush|TestPush|TestBaseMoved' -count=1 -v

import (
	"encoding/json"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/GMISWE/ieops-aihub/internal/coding"
)

// pushCardPath is the published card carrying the sentences below.
const pushCardPath = "../../docs/mcp-cards/pf_push.md"

// gitOpsPath is where the enforced protected set is declared.
const gitOpsPath = "../coding/git_ops.go"

// pushMarkerRe reads the base-moved marker out of the CARD's own sentence
// (`error: "base_moved"`).
//
// 🔴 Read from the card rather than compared against coding.BaseMovedMarker
// alone, and the difference is a measured false green: the producer and the
// matcher SHARE that constant, so renaming it leaves isBaseMoved matching, the
// handler answering the new string, and an arm that only consulted the constant
// green — while every caller keying on the published marker has stopped working.
// Both are checked below: the card's literal is the contract, the constant is
// what the two packages agree on.
var pushMarkerRe = regexp.MustCompile("`error: \"([a-z_]+)\"`")

// cardBaseMovedMarker is the marker string the card publishes.
func cardBaseMovedMarker(t *testing.T) string {
	t.Helper()
	raw, err := os.ReadFile(pushCardPath)
	if err != nil {
		t.Fatalf("read %s: %v", pushCardPath, err)
	}
	flat := strings.Join(strings.Fields(string(raw)), " ")
	m := pushMarkerRe.FindStringSubmatch(flat)
	if m == nil {
		t.Fatalf("%s no longer publishes the base-moved marker in the form `error: \"...\"`, so "+
			"there is no published string left for this arm to hold the handler to.", pushCardPath)
	}
	return m[1]
}

// pushProtectedRe reads the refusal list out of a description or a card
// sentence: `main`/`master`/`dev`/`tot` in the card, main/master/dev/tot in the
// tool description. Anchored on the word "Refuses"/"refuses" so a list of
// branch names appearing for some other reason is not mistaken for the policy.
var pushProtectedRe = regexp.MustCompile(`(?i)refuses?[^.]*?((?:\x60?[a-z]+\x60?/){2,}\x60?[a-z]+\x60?)`)

// branchNamesIn splits the matched run into names, dropping the backticks the
// card writes them in.
func branchNamesIn(run string) []string {
	var out []string
	for _, part := range strings.Split(run, "/") {
		name := strings.TrimSpace(strings.Trim(part, "`*_ "))
		if name != "" {
			out = append(out, name)
		}
	}
	sort.Strings(out)
	return out
}

// publishedPushRefusalNames returns the branch names the LIVE tool description
// says pf_push refuses. A description that no longer states them at all is a
// failure, not a skip: the sentence this arm holds is precisely that the policy
// is published here rather than described elsewhere.
func publishedPushRefusalNames(t *testing.T) []string {
	t.Helper()
	desc := publishedTool(t, "pf_push").Description
	m := pushProtectedRe.FindStringSubmatch(desc)
	if m == nil {
		t.Fatalf("pf_push's published description no longer names the branches it refuses:\n  %s\n"+
			"The card says both facts are on the tool description rather than in a skill, because "+
			"a caller who thinks this is an ordinary push will use it to \"sync\" a branch. If the "+
			"policy moved, this arm and that sentence move with it.", desc)
	}
	return branchNamesIn(m[1])
}

// cardPushRefusalNames returns the names the CARD's own sentence states, so a
// card edited to disagree with the tool is red too.
func cardPushRefusalNames(t *testing.T) []string {
	t.Helper()
	raw, err := os.ReadFile(pushCardPath)
	if err != nil {
		t.Fatalf("read %s: %v — the card is the published side of this claim", pushCardPath, err)
	}
	flat := strings.Join(strings.Fields(string(raw)), " ")
	m := pushProtectedRe.FindStringSubmatch(flat)
	if m == nil {
		t.Fatalf("%s no longer names the branches pf_push refuses. This arm reads the claim out of "+
			"the card rather than repeating it here, so a card that stops making the claim leaves "+
			"nothing to check.", pushCardPath)
	}
	return branchNamesIn(m[1])
}

// enforcedProtectedBranches reads internal/coding's protectedBranches map keys
// out of the source with go/parser.
//
// 🔴 The map is unexported and coding.IsProtectedBranch answers one name at a
// time, so a reader can be asked but not ENUMERATED — and the direction that
// matters most is the one an enumeration is needed for: a fifth protected branch
// added to the map and not to the description is a refusal a caller is never
// told about, which is indistinguishable from a bug in their own tooling. Both
// readers are used below: the AST for the set, the exported function for what
// the code actually answers, so a map the function no longer consults is red.
func enforcedProtectedBranches(t *testing.T) []string {
	t.Helper()
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, gitOpsPath, nil, parser.SkipObjectResolution)
	if err != nil {
		t.Fatalf("parse %s: %v", gitOpsPath, err)
	}
	var out []string
	for _, decl := range f.Decls {
		gd, ok := decl.(*ast.GenDecl)
		if !ok || gd.Tok != token.VAR {
			continue
		}
		for _, spec := range gd.Specs {
			vs, ok := spec.(*ast.ValueSpec)
			if !ok || len(vs.Names) != 1 || vs.Names[0].Name != "protectedBranches" {
				continue
			}
			for _, v := range vs.Values {
				lit, ok := v.(*ast.CompositeLit)
				if !ok {
					continue
				}
				for _, elt := range lit.Elts {
					kv, ok := elt.(*ast.KeyValueExpr)
					if !ok {
						continue
					}
					k, ok := kv.Key.(*ast.BasicLit)
					if !ok || k.Kind != token.STRING {
						continue
					}
					out = append(out, strings.Trim(k.Value, `"`))
				}
			}
		}
	}
	if len(out) == 0 {
		t.Fatalf("%s declares no protectedBranches map literal this walk can read. An empty set "+
			"makes every comparison below vacuous, so this is a failure rather than a pass.", gitOpsPath)
	}
	sort.Strings(out)
	return out
}

// TestPublishedPushRefusalIsTheEnforcedSet is the publication arm for
// pf_push.md's "it **refuses `main`/`master`/`dev`/`tot`**", in three-way
// agreement: the live tool description, the card sentence, and the set
// internal/coding really refuses.
//
// Mutants, all applied to the tree and run (2026-09-10):
//
//	M1  drop "dev" from coding.protectedBranches            RED  (enforced set)
//	M2  drop `dev` from the tool description                 RED  (published set)
//	M3  drop `dev` from the card sentence                    RED  (card set)
//	M4  add "release" to protectedBranches only              RED  (unpublished refusal)
//	M5  make IsProtectedBranch always answer false           RED  (reader arm)
//	M6  green control: reword the prose around the list      GREEN
func TestPublishedPushRefusalIsTheEnforcedSet(t *testing.T) {
	published := publishedPushRefusalNames(t)
	card := cardPushRefusalNames(t)
	enforced := enforcedProtectedBranches(t)

	// The floor. Four names today; a set that shrank to one would still satisfy
	// every equality below, and "refuses main" is a materially weaker promise
	// than the one the card makes.
	const floorProtected = 4
	if len(enforced) < floorProtected {
		t.Errorf("coding.protectedBranches holds %d name(s) %v, floor is %d. Shrinking the set is a "+
			"contract change; move the floor in the same diff that argues for it.",
			len(enforced), enforced, floorProtected)
	}

	if strings.Join(published, ",") != strings.Join(enforced, ",") {
		t.Errorf("pf_push's published description refuses %v, the code refuses %v.\nA name in the "+
			"code and not the description is a refusal the caller is never told about; a name in the "+
			"description and not the code is a promise nothing keeps.", published, enforced)
	}
	if strings.Join(card, ",") != strings.Join(enforced, ",") {
		t.Errorf("%s says pf_push refuses %v, the code refuses %v. The card is read by people "+
			"deciding whether this tool is safe to point at a shared branch.",
			pushCardPath, card, enforced)
	}

	// The exported reader has to agree with the map it is the reader of —
	// otherwise the AST above describes a set nothing consults.
	for _, name := range enforced {
		if !coding.IsProtectedBranch(name) {
			t.Errorf("coding.protectedBranches contains %q but coding.IsProtectedBranch(%q) is "+
				"false, so the declared set and the enforced answer have come apart", name, name)
		}
	}
	// Negative control: a task branch must not be protected, or the positive
	// loop above is satisfied by a function that answers true to everything.
	if coding.IsProtectedBranch("polyforge/aihub-584-probe") {
		t.Errorf("coding.IsProtectedBranch answers true for a task branch, so it answers true for " +
			"everything and the loop above proves nothing")
	}
}

// pushRepo is a worktree, its local bare origin, and a second clone of the same
// bare repository used to move the remote under the worktree.
type pushRepo struct {
	root   string
	wt     string
	bare   string
	branch string
	other  string
}

// newPushRepo builds the fixture: POLYFORGE_WORKSPACE_ROOT pointed at a temp
// dir, a worktree on a task branch with a local bare origin, and the state file
// a claim would have left.
//
// ⚠️ Every remote here is a `git init --bare` directory inside the test's own
// temp dir. Nothing in this file touches a real remote, and a lease refusal is
// only observable against a remote something else can move.
func newPushRepo(t *testing.T) *pushRepo {
	t.Helper()
	root := newResolveWorkspace(t)
	r := newResolveRepo(t, root)
	writeResolveCanonical(t, map[string]string{"aihub": r.wt})
	return &pushRepo{root: root, wt: r.wt, bare: r.bare, branch: r.branch}
}

// secondClone clones the bare repository a second time, so the remote can be
// moved or the branch deleted WITHOUT updating the worktree's own
// remote-tracking ref — which is the state both lease cases are about.
func (p *pushRepo) secondClone(t *testing.T) string {
	t.Helper()
	if p.other != "" {
		return p.other
	}
	p.other = filepath.Join(t.TempDir(), "other")
	runGit(t, "", "clone", "-q", p.bare, p.other)
	runGit(t, p.other, "config", "user.email", "other@example.invalid")
	runGit(t, p.other, "config", "user.name", "other")
	runGit(t, p.other, "config", "commit.gpgsign", "false")
	return p.other
}

// commit adds one commit to the worktree and returns its sha.
func (p *pushRepo) commit(t *testing.T, name, content string) string {
	t.Helper()
	if err := os.WriteFile(filepath.Join(p.wt, name), []byte(content), 0644); err != nil {
		t.Fatalf("write %s: %v", name, err)
	}
	runGit(t, p.wt, "add", "-A")
	runGit(t, p.wt, "commit", "-q", "-m", "probe: "+name)
	return runGit(t, p.wt, "rev-parse", "HEAD")
}

// remoteSHA returns what origin holds for the branch, read from the bare
// repository rather than from any clone's tracking ref — the tracking ref is
// exactly the value under test.
func (p *pushRepo) remoteSHA(t *testing.T) string {
	t.Helper()
	out := runGit(t, p.bare, "rev-parse", "--verify", "--quiet", "refs/heads/"+p.branch)
	return strings.TrimSpace(out)
}

// remoteBranchGone reports whether origin has no such branch at all.
func (p *pushRepo) remoteBranchGone(t *testing.T) bool {
	t.Helper()
	out, _ := runGitCapture(p.bare, "rev-parse", "--verify", "--quiet", "refs/heads/"+p.branch)
	return strings.TrimSpace(out) == ""
}

// callPush drives the real pf_push tool against a fake aihub and returns the
// decoded result, whether the MCP result was an error, and the recorder.
func callPush(t *testing.T, p *pushRepo) (map[string]any, bool, *fakeAihub) {
	t.Helper()
	f := newFakeAihub(t)
	out, isErr := callToolBounded(t, f, "pf_push",
		map[string]any{"work_item_id": resolveCanonical, "repo": "aihub"}, 60*time.Second)
	return out, isErr, f
}

// TestPushRefusesAProtectedBranchAndLeavesOriginUntouched is the behaviour half
// of the refusal: an error RESULT (not a success-shaped payload), naming the
// branch, with origin unchanged and no timeline event.
//
// Mutants (2026-09-10):
//
//	M7  delete the protectedBranches guard in GitPush        RED  (origin moved)
//	M8  return the refusal through jsonResult instead of
//	    errResult                                            RED  (isError arm)
//	M9  emit the push event before the push runs             RED  (event census)
//	M10 green control: reword the refusal message            GREEN — the arm
//	                                                         reads the branch
//	                                                         name, not the wording
func TestPushRefusesAProtectedBranchAndLeavesOriginUntouched(t *testing.T) {
	p := newPushRepo(t)
	protected := enforcedProtectedBranches(t)[0]

	// Move onto a protected branch and prove we got there, so a fixture that
	// silently stayed on the task branch cannot pass this test.
	runGit(t, p.wt, "checkout", "-q", "-B", protected)
	if got := runGit(t, p.wt, "rev-parse", "--abbrev-ref", "HEAD"); got != protected {
		t.Fatalf("worktree is on %q, want the protected branch %q", got, protected)
	}
	p.branch = protected
	before := p.remoteBranchGone(t)

	out, isErr, f := callPush(t, p)
	if !isErr {
		t.Fatalf("pf_push on %q returned a NON-error result %v. The card says the refusal is "+
			"published so a caller cannot \"sync\" a shared branch with this tool; a success-shaped "+
			"answer here is the one shape that reads as \"it worked\".", protected, out)
	}
	if raw, _ := out["_raw"].(string); !strings.Contains(raw, protected) {
		t.Errorf("the refusal does not name the branch it refused: %v. A caller with several "+
			"worktrees cannot act on \"refusing to push\" alone.", out)
	}
	if !before && p.remoteBranchGone(t) || p.remoteBranchGone(t) != before {
		t.Errorf("the refused push changed what origin holds for %q; the guard runs too late to "+
			"protect anything", protected)
	}
	if len(f.recorded()) != 0 {
		t.Errorf("a refused push made %d HTTP request(s) %v. A push that never happened must leave "+
			"no push event: the timeline is what a reader reconstructs the attempt from.",
			len(f.recorded()), f.paths())
	}
}

// TestPushEmitsOnePushEventAndMakesNoOtherRequest holds two card sentences at
// once: "The push is local" / "The only HTTP call is the best-effort `push`
// event through … `POST /v1/events`", and the best-effort half of it.
//
// Mutants (2026-09-10):
//
//	M11 emit the event twice                                 RED  (count arm)
//	M12 send the event to /v1/work_items/<id>/events         RED  (path arm)
//	M13 propagate the emit failure to the caller             RED  (best-effort arm)
//	M14 drop `branch` from the payload                       RED  (payload arm)
func TestPushEmitsOnePushEventAndMakesNoOtherRequest(t *testing.T) {
	p := newPushRepo(t)
	p.commit(t, "one.txt", "first\n")

	out, isErr, f := callPush(t, p)
	if isErr {
		t.Fatalf("pf_push failed: %v", out)
	}

	calls := f.recorded()
	if len(calls) != 1 {
		t.Fatalf("pf_push made %d HTTP request(s) %v, want exactly 1. The card says this tool's ONLY "+
			"HTTP call is the push event — a second request is a hop no card describes, and zero "+
			"means the timeline loses the push.", len(calls), f.paths())
	}
	c := calls[0]
	if c.Method != "POST" || c.Path != "/v1/events" {
		t.Errorf("pf_push's one request is %s %s, want POST /v1/events (bound by handleEmitEvent)",
			c.Method, c.Path)
	}
	if c.Body["event_type"] != "push" {
		t.Errorf("the event_type is %v, want \"push\"", c.Body["event_type"])
	}
	payload, ok := c.Body["payload"].(map[string]any)
	if !ok {
		t.Fatalf("the event payload is %T, not an object", c.Body["payload"])
	}
	if payload["branch"] != p.branch || payload["repo"] != "aihub" {
		t.Errorf("the push event payload is %v, want repo=aihub branch=%s — an event that does not "+
			"name the branch cannot be read back as \"this attempt pushed that\"", payload, p.branch)
	}

	// Best-effort: the git work has already happened by the time the event is
	// emitted, so a failing aihub must not turn a delivered push into an error.
	p2 := newPushRepo(t)
	p2.commit(t, "two.txt", "second\n")
	f2 := newFakeAihub(t)
	f2.on("/v1/events", func(map[string]any) (int, any) {
		return 500, map[string]any{"error": "aihub is down"}
	})
	out2, isErr2 := callToolBounded(t, f2, "pf_push",
		map[string]any{"work_item_id": resolveCanonical, "repo": "aihub"}, 60*time.Second)
	if isErr2 {
		t.Fatalf("pf_push failed because the timeline event failed: %v. The event is best-effort "+
			"BECAUSE the push already succeeded — reporting an error here tells the caller to retry "+
			"a push that is already on origin.", out2)
	}
	if out2["ok"] != true {
		t.Errorf("pf_push answered %v with a failing event sink, want ok=true", out2)
	}
	if got := p2.remoteSHA(t); got != runGit(t, p2.wt, "rev-parse", "HEAD") {
		t.Errorf("origin/%s is %s, worktree HEAD is %s — the push this arm calls successful did not "+
			"actually deliver", p2.branch, got, runGit(t, p2.wt, "rev-parse", "HEAD"))
	}
}

// TestPushBaseShaAtPushIsTheHeadItDelivered is `base_sha_at_push` — "returned so
// a later check can tell what the remote looked like at the moment of the push".
//
// Both directions, because the value that would satisfy a one-sided arm is the
// wrong one: it must equal the head this push delivered (and therefore what
// origin holds afterwards), and it must NOT be the sha origin held before, which
// is what the name "base" invites and what an implementation reading the
// pre-push remote would return.
//
// Mutants (2026-09-10):
//
//	M15 return the pre-push remote sha                       RED  (identity arm)
//	M16 return the branch name instead                       RED
//	M17 drop the key from the response                       RED
//	M18 green control: rename the local variable             GREEN
func TestPushBaseShaAtPushIsTheHeadItDelivered(t *testing.T) {
	p := newPushRepo(t)
	first := p.commit(t, "one.txt", "first\n")
	if out, isErr, _ := callPush(t, p); isErr {
		t.Fatalf("first pf_push failed: %v", out)
	}
	if got := p.remoteSHA(t); got != first {
		t.Fatalf("fixture: origin/%s is %s after the first push, want %s", p.branch, got, first)
	}

	second := p.commit(t, "two.txt", "second\n")
	out, isErr, _ := callPush(t, p)
	if isErr {
		t.Fatalf("second pf_push failed: %v", out)
	}

	got, has := out["base_sha_at_push"].(string)
	if !has {
		t.Fatalf("pf_push's response %v carries no base_sha_at_push. A later check has nothing to "+
			"compare the remote against, which is the whole stated purpose of the key.", out)
	}
	if got != second {
		t.Errorf("base_sha_at_push = %s, the head this push delivered is %s", got, second)
	}
	if got == first {
		t.Errorf("base_sha_at_push = %s, which is what origin held BEFORE this push. A caller "+
			"comparing the remote against it would conclude nothing had been delivered.", got)
	}
	if remote := p.remoteSHA(t); remote != got {
		t.Errorf("base_sha_at_push = %s but origin/%s is %s, so the key does not describe the "+
			"remote at the moment of the push", got, p.branch, remote)
	}
}

// TestPushOfABranchDeletedOnOriginIsRecreatedWithoutALease is the OTHER half of
// the lease policy, and it is the half a flat "it is a --force-with-lease push"
// hides: when origin no longer holds the branch, the push carries no lease,
// because the lease would be taken against the stale remote-tracking ref a
// delete-branch-on-merge leaves behind and git would refuse the push as "stale
// info" (aihub#226).
//
// The fixture reproduces exactly that state: push, delete the branch through a
// SECOND clone so this worktree keeps its stale tracking ref, then push again.
//
// Mutants (2026-09-10):
//
//	M19 pass --force-with-lease unconditionally              RED  (stale info ->
//	                                                         base_moved answer)
//	M20 make GitRemoteBranchExists always answer true        RED  (same path)
//	M21 green control: reword the comment above the branch   GREEN
func TestPushOfABranchDeletedOnOriginIsRecreatedWithoutALease(t *testing.T) {
	p := newPushRepo(t)
	p.commit(t, "one.txt", "first\n")
	if out, isErr, _ := callPush(t, p); isErr {
		t.Fatalf("first pf_push failed: %v", out)
	}

	other := p.secondClone(t)
	runGit(t, other, "fetch", "-q", "origin")
	runGit(t, other, "push", "-q", "origin", "--delete", p.branch)
	if !p.remoteBranchGone(t) {
		t.Fatalf("fixture: origin still holds %s, so the deleted-branch path is not the one under "+
			"test", p.branch)
	}
	// The stale tracking ref is the whole point of the fixture: with it absent,
	// a lease would be harmless and this arm would pass against the mutant.
	if _, ok := runGitCapture(p.wt, "rev-parse", "--verify", "--quiet",
		"refs/remotes/origin/"+p.branch); !ok {
		t.Fatalf("fixture: this worktree has no stale refs/remotes/origin/%s, so --force-with-lease "+
			"would have nothing stale to take a lease against and the mutant this arm exists for "+
			"would pass", p.branch)
	}

	head := p.commit(t, "two.txt", "second\n")
	out, isErr, _ := callPush(t, p)
	if isErr {
		t.Fatalf("pf_push could not re-create a branch origin had deleted: %v\nThis is aihub#226: "+
			"a wi whose PR merged with delete-branch-on-merge could never deliver a later commit, "+
			"because the lease was taken against the tracking ref the delete left behind.", out)
	}
	if out["error"] != nil {
		t.Fatalf("pf_push answered %v; a re-created branch is not a base-moved condition and "+
			"answering with the marker sends the caller off to rebase against a branch that is gone", out)
	}
	if got := p.remoteSHA(t); got != head {
		t.Errorf("origin/%s is %q after the push, want the delivered head %s", p.branch, got, head)
	}
}

// TestPushWhoseLeaseIsStaleAnswersBaseMovedAndOverwritesNothing is the
// base-moved sentence, in all three of its parts: the result is NOT an error
// result, it carries `error: "base_moved"` plus advice to rebase and retry, and
// — the part that makes the lease worth publishing — the commit somebody else
// pushed is still there.
//
// Mutants (2026-09-10):
//
//	M22 --force instead of --force-with-lease                RED  (the push
//	                                                         succeeds and the
//	                                                         other commit is gone)
//	M23 no force at all                                      RED  (rejected as
//	                                                         non-fast-forward, so
//	                                                         isBaseMoved is false
//	                                                         and the answer is an
//	                                                         error result)
//	M24 return the base-moved case through errResult          RED  (isError arm)
//	M25 drop `advice` from the payload                       RED
//	M26 change the marker to "stale"                         RED  (both the
//	                                                         handler and
//	                                                         coding.BaseMovedMarker
//	                                                         are read)
func TestPushWhoseLeaseIsStaleAnswersBaseMovedAndOverwritesNothing(t *testing.T) {
	p := newPushRepo(t)
	p.commit(t, "one.txt", "first\n")
	if out, isErr, _ := callPush(t, p); isErr {
		t.Fatalf("first pf_push failed: %v", out)
	}

	// Somebody else moves the branch under us. Through a second clone, so this
	// worktree's remote-tracking ref stays where it was — a lease is a claim
	// about that ref, so a fixture that fetched would have nothing stale.
	other := p.secondClone(t)
	runGit(t, other, "fetch", "-q", "origin")
	runGit(t, other, "checkout", "-q", p.branch)
	if err := os.WriteFile(filepath.Join(other, "theirs.txt"), []byte("theirs\n"), 0644); err != nil {
		t.Fatalf("write theirs.txt: %v", err)
	}
	runGit(t, other, "add", "-A")
	runGit(t, other, "commit", "-q", "-m", "theirs")
	runGit(t, other, "push", "-q", "origin", p.branch)
	theirs := runGit(t, other, "rev-parse", "HEAD")
	if got := p.remoteSHA(t); got != theirs {
		t.Fatalf("fixture: origin/%s is %s, want the other clone's commit %s", p.branch, got, theirs)
	}

	mine := p.commit(t, "mine.txt", "mine\n")
	out, isErr, f := callPush(t, p)

	if isErr {
		t.Fatalf("a base-moved rejection came back as an ERROR result: %v\nThe card's sentence is "+
			"that it comes back as a JSON object carrying the marker, \"so a caller keying on the "+
			"marker keeps working\" — an error result is what makes an automated retry give up.", out)
	}
	marker := cardBaseMovedMarker(t)
	if out["error"] != marker {
		t.Errorf("the base-moved response is %v, want error=%q — the marker %s publishes. It is the "+
			"only thing a caller can branch on.", out, marker, pushCardPath)
	}
	if marker != coding.BaseMovedMarker {
		t.Errorf("%s publishes the marker %q and coding.BaseMovedMarker is %q. The constant is "+
			"shared by the producer and the matcher, so renaming it keeps this path WORKING while "+
			"every caller keying on the published string stops.",
			pushCardPath, marker, coding.BaseMovedMarker)
	}
	advice, _ := out["advice"].(string)
	if !strings.Contains(strings.ToLower(advice), "rebase") || !strings.Contains(strings.ToLower(advice), "retry") {
		t.Errorf("the base-moved advice is %q; the card promises advice to rebase and retry, which "+
			"is what makes the marker actionable rather than merely informative", advice)
	}
	if out["ok"] != nil {
		t.Errorf("the base-moved response carries ok=%v as well as the marker; a payload that says "+
			"both is one a caller can read either way", out["ok"])
	}

	// The lease's own job. A --force would have succeeded here and the other
	// clone's commit would be gone; that is the loss the published sentence
	// exists to prevent, so it is asserted rather than inferred from the marker.
	if got := p.remoteSHA(t); got != theirs {
		t.Errorf("origin/%s is %s after the refusal, want the other clone's commit %s still there "+
			"(this push's head was %s). A refusal that already overwrote the remote is the exact "+
			"failure --force-with-lease is published to prevent.", p.branch, got, theirs, mine)
	}

	// A refused push emits no push event: the timeline must not record a
	// delivery that did not happen.
	for _, c := range f.recorded() {
		if c.Path == "/v1/events" && c.Body["event_type"] == "push" {
			t.Errorf("a base-moved refusal emitted a push event %v — the timeline is the durable "+
				"record of what was delivered", c.Body)
		}
	}

	// The keys this path answers with are declared in the card's machine block,
	// which is generated from the aihub#412 corpus: `error` and `advice` are in
	// that union precisely because this path answers success-shaped.
	declared := cardResponseKeys(t, pushCardPath)
	for k := range out {
		if !declared[k] {
			t.Errorf("the base-moved response carries %q, which %s does not list in "+
				"response_keys_observed %v", k, pushCardPath, sortedKeysOf(declared))
		}
	}
}

// TestBaseMovedRecognitionIsSharedByPushAndShipWithDifferentShapes is the
// sentence about `isBaseMoved`: shared with pf_ship "so the two cannot drift on
// the fragile half — recognising the condition — while deliberately not sharing
// a response shape".
//
// One fixture, two tools, and both halves asserted: each recognises the
// condition (neither answers an error result), and the two payloads are NOT the
// same shape (pf_ship adds which stage it reached and what it already did).
//
// Mutants (2026-09-10):
//
//	M27 give pf_ship its own substring check for "base_moved" RED (drift arm:
//	                                                          the shared function
//	                                                          is read out of the
//	                                                          AST too)
//	M28 make pf_ship answer pf_push's exact payload           RED (shape arm)
//	M29 make pf_ship return errResult on base-moved           RED
func TestBaseMovedRecognitionIsSharedByPushAndShipWithDifferentShapes(t *testing.T) {
	push := baseMovedAnswerFrom(t, "pf_push", nil)
	ship := baseMovedAnswerFrom(t, "pf_ship", map[string]any{
		"message":  "probe: a commit the stale lease will refuse",
		"pr_title": "probe",
		"pr_body":  "probe",
	})

	marker := cardBaseMovedMarker(t)
	if push["error"] != marker {
		t.Errorf("pf_push did not recognise the condition: %v (published marker %q)", push, marker)
	}
	if ship["error"] != marker {
		t.Errorf("pf_ship did not recognise the condition: %v. The recogniser is shared so the two "+
			"cannot drift on it; a fused tool that reports a generic push failure leaves the caller "+
			"unable to tell a moved base from a broken remote.", ship)
	}

	// Deliberately different shapes, asserted so "they share the recogniser and
	// not the shape" cannot quietly become "they share both".
	pushKeys, shipKeys := sortedKeysOf(setOf(push)), sortedKeysOf(setOf(ship))
	if strings.Join(pushKeys, ",") == strings.Join(shipKeys, ",") {
		t.Errorf("pf_push and pf_ship answered the same key set %v on the base-moved path. The "+
			"shapes are deliberately not shared: pf_ship has to say which stage it reached and what "+
			"it already did, and a caller of the fused tool that gets pf_push's payload cannot tell "+
			"whether a commit is sitting unpushed in its worktree.", pushKeys)
	}
	for _, k := range []string{"stage", "committed"} {
		if _, has := ship[k]; !has {
			t.Errorf("pf_ship's base-moved payload %v carries no %q; that is the information fusing "+
				"took away from the caller and this key is how it is given back", ship, k)
		}
	}
}

// baseMovedAnswerFrom drives one tool into a stale-lease rejection and returns
// its decoded answer, failing rather than skipping if the fixture did not
// actually reach that state.
func baseMovedAnswerFrom(t *testing.T, tool string, extra map[string]any) map[string]any {
	t.Helper()
	p := newPushRepo(t)
	fakeGHForResolve(t, `[]`)
	p.commit(t, "one.txt", "first\n")

	f0 := newFakeAihub(t)
	if out, isErr := callToolBounded(t, f0, "pf_push",
		map[string]any{"work_item_id": resolveCanonical, "repo": "aihub"}, 60*time.Second); isErr {
		t.Fatalf("fixture: the first push failed: %v", out)
	}

	other := p.secondClone(t)
	runGit(t, other, "fetch", "-q", "origin")
	runGit(t, other, "checkout", "-q", p.branch)
	if err := os.WriteFile(filepath.Join(other, "theirs.txt"), []byte("theirs\n"), 0644); err != nil {
		t.Fatalf("write theirs.txt: %v", err)
	}
	runGit(t, other, "add", "-A")
	runGit(t, other, "commit", "-q", "-m", "theirs")
	runGit(t, other, "push", "-q", "origin", p.branch)

	// Something local to deliver. pf_ship stages and commits it itself, so the
	// file is left uncommitted for that tool and committed for pf_push.
	if tool == "pf_ship" {
		if err := os.WriteFile(filepath.Join(p.wt, "mine.txt"), []byte("mine\n"), 0644); err != nil {
			t.Fatalf("write mine.txt: %v", err)
		}
	} else {
		p.commit(t, "mine.txt", "mine\n")
	}

	f := newFakeAihub(t)
	args := map[string]any{"work_item_id": resolveCanonical, "repo": "aihub"}
	for k, v := range extra {
		args[k] = v
	}
	out, isErr := callToolBounded(t, f, tool, args, 90*time.Second)
	if isErr {
		t.Fatalf("%s answered an ERROR result on the base-moved path: %v", tool, out)
	}
	if out["error"] == nil {
		t.Fatalf("%s answered %v, which carries no error marker at all — the fixture did not reach "+
			"the stale-lease state, so the arm reading this would assert nothing", tool, out)
	}
	return out
}

// cardResponseKeys reads a card's machine-block response_keys_observed.
func cardResponseKeys(t *testing.T, cardPath string) map[string]bool {
	t.Helper()
	raw, err := os.ReadFile(cardPath)
	if err != nil {
		t.Fatalf("read %s: %v", cardPath, err)
	}
	m := regexp.MustCompile("(?s)```json\\s*(\\{.*?\\})\\s*```").FindStringSubmatch(string(raw))
	if m == nil {
		t.Fatalf("%s carries no machine block", cardPath)
	}
	var blk struct {
		Keys []string `json:"response_keys_observed"`
	}
	if err := json.Unmarshal([]byte(m[1]), &blk); err != nil {
		t.Fatalf("decode %s's machine block: %v", cardPath, err)
	}
	if len(blk.Keys) == 0 {
		t.Fatalf("%s declares no response_keys_observed, so the containment check below would "+
			"accept any key at all", cardPath)
	}
	out := map[string]bool{}
	for _, k := range blk.Keys {
		out[k] = true
	}
	return out
}

func setOf(m map[string]any) map[string]bool {
	out := map[string]bool{}
	for k := range m {
		out[k] = true
	}
	return out
}

func sortedKeysOf(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// runGitCapture runs git and returns its combined output plus whether it
// succeeded, for the places a NONZERO exit is the expected answer.
func runGitCapture(dir string, args ...string) (string, bool) {
	full := args
	if dir != "" {
		full = append([]string{"-C", dir}, args...)
	}
	out, err := exec.Command("git", full...).CombinedOutput()
	return string(out), err == nil
}
