package mcp

// Tests for the pf_ship response contract (aihub#286).
//
// The contract has two hops — coding.Ship must produce a ShipResult that
// describes a partial failure (covered by internal/coding/ship_test.go against a
// real git repo), and that ShipResult must survive the trip into the MCP JSON
// the caller actually reads. Two hops, two assertions: a correct ShipResult that
// the payload builder drops on the floor is exactly as useless as no ShipResult
// at all, and nothing in the coding package can see it happen.

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/GMISWE/ieops-aihub/internal/coding"
)

// TestShipPayload_PushFailureReportsTheLocalCommit is the MCP half of the key
// negative criterion: when the push stage fails, what the caller reads must say
// a commit exists and give its sha.
func TestShipPayload_PushFailureReportsTheLocalCommit(t *testing.T) {
	res := &coding.ShipResult{
		Stage:     coding.StagePush,
		Committed: true,
		CommitSHA: "abc123def456",
		HeadSHA:   "abc123def456",
		Branch:    "polyforge/xyz",
		Wrap:      &coding.WrapResult{Branch: "polyforge/xyz", Stage: coding.StagePush},
	}
	err := errors.New("push: refusing to push to main branch; use a task branch")

	got := shipPayload("aihub", res, err)

	if ok, _ := got["ok"].(bool); ok {
		t.Error("ok = true for a failed ship")
	}
	if got["stage"] != coding.StagePush {
		t.Errorf("stage = %v, want %q", got["stage"], coding.StagePush)
	}
	if committed, _ := got["committed"].(bool); !committed {
		t.Error("committed = false; the caller cannot tell a commit was made")
	}
	if got["commit_sha"] != "abc123def456" {
		t.Errorf("commit_sha = %v, want the sha of the commit that was made", got["commit_sha"])
	}
	if got["error"] != err.Error() {
		t.Errorf("error = %v, want the underlying failure verbatim", got["error"])
	}

	// The side effects have to be spelled out, not left to be inferred from
	// committed=true by a reader who may not know what the field means.
	effects, _ := got["side_effects"].([]string)
	if len(effects) == 0 {
		t.Fatal("side_effects is empty; a fused call must state what it already did")
	}
	if !containsSubstring(effects, "abc123def456") {
		t.Errorf("side_effects %q never names the commit sha", effects)
	}
	if advice, _ := got["advice"].(string); advice == "" {
		t.Error("advice is empty")
	}

	// And it must survive marshalling — this is what actually reaches the wire.
	b, mErr := marshalJSON(got)
	if mErr != nil {
		t.Fatalf("marshal: %v", mErr)
	}
	var round map[string]any
	if uErr := json.Unmarshal(b, &round); uErr != nil {
		t.Fatalf("unmarshal: %v", uErr)
	}
	if round["commit_sha"] != "abc123def456" {
		t.Errorf("commit_sha did not survive marshalling: %v", round["commit_sha"])
	}
}

// TestShipPayload_RetryStillReportsTheUnpushedCommit is the shape the first
// draft got wrong, and it is the most likely failure sequence there is: push
// fails, the agent retries, the push fails again. On that second call Ship
// correctly makes no commit (Committed=false, CommitSHA="") because the first
// one already did — so a side-effect report keyed only on "did THIS call commit"
// answers "none", flatly denying the unpushed work at the moment the caller is
// deciding whether to redo it. The undelivered work is identified by HeadSHA.
func TestShipPayload_RetryStillReportsTheUnpushedCommit(t *testing.T) {
	res := &coding.ShipResult{
		Stage:     coding.StagePush,
		Committed: false, // nothing to commit — an earlier attempt already did it
		CommitSHA: "",
		HeadSHA:   "aaaa1111bbbb2222",
		Branch:    "polyforge/xyz",
		Wrap:      &coding.WrapResult{Branch: "polyforge/xyz", Stage: coding.StagePush},
	}
	got := shipPayload("aihub", res, errors.New("push: refusing to push to main branch; use a task branch"))

	effects, _ := got["side_effects"].([]string)
	if containsSubstring(effects, "none") {
		t.Fatalf("side_effects = %q — it denies the unpushed commit that HeadSHA proves exists", effects)
	}
	if !containsSubstring(effects, "aaaa1111bbbb2222") {
		t.Errorf("side_effects = %q never names HEAD, the only identifier of the undelivered work", effects)
	}
	if !containsSubstring(effects, "NOT on origin") {
		t.Errorf("side_effects = %q never says the work is undelivered", effects)
	}
	// The advice must not point at a commit this response does not report.
	advice, _ := got["advice"].(string)
	if strings.Contains(advice, "reported above") {
		t.Errorf("advice %q points at a commit above; on a retry there is none", advice)
	}
	if !strings.Contains(advice, "head_sha") {
		t.Errorf("advice %q does not tell the caller which field identifies the local work", advice)
	}
}

// TestShipPayload_BaseMovedMarkerPassesThrough: pf_push answers a moved base
// with error == "base_moved", and callers key on that exact string. Fusing must
// not rename it.
func TestShipPayload_BaseMovedMarkerPassesThrough(t *testing.T) {
	res := &coding.ShipResult{
		Stage: coding.StagePush, Committed: true, CommitSHA: "deadbeef",
	}
	err := errors.New("push: base_moved: ! [rejected] main -> main (stale info)")

	got := shipPayload("aihub", res, err)

	if got["error"] != "base_moved" {
		t.Errorf("error = %v, want the verbatim base_moved marker pf_push returns", got["error"])
	}
	if detail, _ := got["error_detail"].(string); detail != err.Error() {
		t.Errorf("error_detail = %q, want the full git output kept alongside the marker", detail)
	}
	if got["commit_sha"] != "deadbeef" {
		t.Errorf("commit_sha = %v; a moved base must not hide the local commit", got["commit_sha"])
	}
}

// TestShipPayload_PRStageFailureSaysTheCommitsAreSafe: once the push landed, the
// commits are on origin. Saying so is the difference between "retry" and "panic".
func TestShipPayload_PRStageFailureSaysTheCommitsAreSafe(t *testing.T) {
	res := &coding.ShipResult{
		Stage: coding.StagePR, Committed: true, CommitSHA: "cafe01",
		Branch: "polyforge/x",
		Wrap: &coding.WrapResult{
			Branch: "polyforge/x", Stage: coding.StagePR, Pushed: true, PushedSHA: "cafe01",
		},
	}
	got := shipPayload("aihub", res, errors.New("create PR: gh pr create: exit 1"))

	if pushed, _ := got["pushed"].(bool); !pushed {
		t.Error("pushed = false although the push stage completed")
	}
	effects, _ := got["side_effects"].([]string)
	if !containsSubstring(effects, "force-pushed") {
		t.Errorf("side_effects %q does not say the commits reached origin", effects)
	}
}

// TestShipPayload_SuccessCarriesNoFailureFields keeps the success response from
// growing failure noise a caller would have to filter.
func TestShipPayload_SuccessCarriesNoFailureFields(t *testing.T) {
	res := &coding.ShipResult{
		Stage: coding.StageDone, Committed: true, CommitSHA: "f00d", HeadSHA: "f00d",
		Branch: "polyforge/x",
		Wrap: &coding.WrapResult{
			Branch: "polyforge/x", Stage: coding.StageDone, Pushed: true, PushedSHA: "f00d",
			Action: coding.WrapActionPushedAndCreatedPR,
			PR:     map[string]any{"url": "https://example.com/pr/1", "number": 1},
		},
	}
	got := shipPayload("aihub", res, nil)

	if ok, _ := got["ok"].(bool); !ok {
		t.Error("ok = false for a successful ship")
	}
	for _, key := range []string{"error", "error_detail", "side_effects", "advice"} {
		if _, present := got[key]; present {
			t.Errorf("success response carries %q", key)
		}
	}
	if got["pr_action"] != coding.WrapActionPushedAndCreatedPR {
		t.Errorf("pr_action = %v", got["pr_action"])
	}
	if got["pr"] == nil {
		t.Error("pr is absent; the PR url is the one thing the caller actually wanted")
	}
}

// TestCodingToolsStillRegistered: pf_ship fuses three tools but does not replace
// them. Callers step through pf_commit / pf_push / pf_pr when debugging, and
// pf_wrap shares the same underlying chain. Enumerating through the real
// in-memory MCP transport is what the CI contract dump does, so this fails the
// same way a dropped tool would break the published schema.
func TestCodingToolsStillRegistered(t *testing.T) {
	ctx := context.Background()
	names := registeredToolNames(t, ctx)

	for _, want := range []string{"pf_diff", "pf_commit", "pf_push", "pf_pr", "pf_wrap", "pf_ship"} {
		if !names[want] {
			t.Errorf("tool %q is not registered", want)
		}
	}
}

// TestShipToolSchemaWarnsAboutTheForcePush: fusing must make the force-push MORE
// visible, not less. The tool's description is the only place a caller learns
// that "ship" rewrites a remote branch.
func TestShipToolSchemaWarnsAboutTheForcePush(t *testing.T) {
	ctx := context.Background()
	tool := registeredTool(t, ctx, "pf_ship")

	for _, want := range []string{"FORCE-PUSH", "stage", "side_effects"} {
		if !strings.Contains(tool.Description, want) {
			t.Errorf("pf_ship description never mentions %q: %s", want, tool.Description)
		}
	}

	// The enumerated tool carries its schema as a decoded value, so round-trip
	// it back through JSON to read the published `required` list.
	raw, err := json.Marshal(tool.InputSchema)
	if err != nil {
		t.Fatalf("re-marshal input schema: %v", err)
	}
	var schema struct {
		Required []string `json:"required"`
	}
	if err := json.Unmarshal(raw, &schema); err != nil {
		t.Fatalf("input schema is not valid JSON: %v", err)
	}
	req := map[string]bool{}
	for _, r := range schema.Required {
		req[r] = true
	}
	// pr_title / pr_body are unconditionally required — that honesty is the
	// reason this is a separate tool rather than flags on pf_commit.
	for _, want := range []string{"work_item_id", "repo", "message", "pr_title", "pr_body"} {
		if !req[want] {
			t.Errorf("pf_ship does not declare %q required; the published schema understates its contract", want)
		}
	}
}

// TestCommitToolsDeclareThatTheyAcquireLocks is aihub#366's stated cost, made
// into a gate.
//
// The owner accepted one price for the commit-time lock check: "commit becomes
// an operation that ACQUIRES LOCKS — a heavier semantic than it has today, and
// it must be stated in the tool description, not left implicit." A behaviour
// this heavy that reaches no caller-visible text is precisely the failure the
// change exists to prevent, one level up: an action that quietly changed what it
// does. The description is the ONLY place an agent can learn it, because an
// agent has no changelog and reads no docs directory.
//
// Substrings rather than prose matching, and each one is a distinct promise:
// that locks are acquired, that a conflict REFUSES the call, that the refusal
// names the holder, and that the outcome is reported in the response.
func TestCommitToolsDeclareThatTheyAcquireLocks(t *testing.T) {
	ctx := context.Background()

	for _, name := range []string{"pf_commit", "pf_ship"} {
		t.Run(name, func(t *testing.T) {
			desc := registeredTool(t, ctx, name).Description
			for _, want := range []string{
				"ACQUIRE",             // it takes locks
				"CONFLICT_LOCK_TAKEN", // and refuses on a real conflict
				"holder",              // naming who has it
				"lock_gate",           // and reporting the outcome in the response
			} {
				if !strings.Contains(desc, want) {
					t.Errorf("%s's description never mentions %q, so a caller cannot learn "+
						"from the tool itself that committing now takes locks: %s", name, want, desc)
				}
			}
		})
	}
}

// TestCommitLockGateReport_TellsTheNotCommittedFactsApart pins the whole of
// report()'s classification in one table, including the outcomes no wire test
// reaches cheaply.
//
// The defect it replaces: the switch keyed on a single !ran flag, which is true
// for "the gate was never invoked", "the gate could not run" and "the gate ran
// and refused" alike — so all three answered "no staged changes, so no files
// needed locking". Two of the three had files staged at the moment they said it,
// and in a pf_ship response that sentence sits next to an `error` naming
// CONFLICT_LOCK_TAKEN and a `side_effects` entry saying the files WERE staged.
//
// 🔴 AND THE FIRST FIX FOR THAT SHIPPED THE SAME DEFECT AGAIN, which is the
// reason for the sixth row and for the rewritten invariant at the bottom. That
// version split the failure side three ways on invoked / err / ran — all three
// of which live INSIDE run(), so every failure upstream of the gate still landed
// on `!invoked` and still answered "no staged changes". An earlier version of
// this very comment then claimed "Five facts, five names", and the uniqueness
// check below enforced it. There are six facts. Counting them from the switch's
// own branches is what made the sixth invisible: the enumeration and the thing
// enumerated came from the same place, so nothing could disagree.
//
// So the invariant is no longer "one name per row". Two rows now legitimately
// share `could_not_run` — nothing was checked and nothing was committed is the
// same instruction to the caller in both — and what must stay distinct is the
// DETAIL, because that is the field a human reads to find out which happened.
//
// Both success values are in the table as positive controls: a "fix" that
// reported every outcome as a distinct kind of failure would satisfy the
// failure rows on its own.
func TestCommitLockGateReport_TellsTheNotCommittedFactsApart(t *testing.T) {
	conflictErr := errors.New("commit refused: CONFLICT_LOCK_TAKEN: this commit changes 1 file(s) locked by another attempt")
	checkErr := errors.New("the commit-time lock check could not be completed, so nothing was committed: 502 Bad Gateway")
	stageErr := errors.New("git add -A: exit status 128\nfatal: Unable to create '.git/index.lock': File exists.")

	cases := []struct {
		name      string
		gate      *commitLockGate
		stageErr  error
		want      string
		wantStage string // a phrase the detail must contain
		why       string
	}{
		{
			name:      "never reached: the commit stage failed upstream of the gate",
			gate:      &commitLockGate{},
			stageErr:  stageErr,
			want:      "could_not_run",
			wantStage: "before the lock check was reached",
			why: "GitStage / GitHasStagedChanges / GitStagedPaths all fail before runCommitGate " +
				"calls the gate, so invoked, err and ran are ALL zero — and the gate has no idea " +
				"what is in the index. Claiming nothing was staged here is a positive falsehood, " +
				"and it is the one this row exists to catch",
		},
		{
			name:      "never invoked: nothing was staged",
			gate:      &commitLockGate{},
			want:      "not_run",
			wantStage: "no staged changes",
			why: "the commit stage COMPLETED and the index matched HEAD, so runCommitGate " +
				"short-circuited and there was genuinely no change set to protect",
		},
		{
			name:      "invoked, refused by another attempt's lock",
			gate:      &commitLockGate{invoked: true, checked: 3, err: conflictErr},
			want:      "refused",
			wantStage: "held by another live attempt",
			why:       "the gate ran, and what it found was somebody else's lock — the caller waits",
		},
		{
			name:      "invoked, the check itself failed",
			gate:      &commitLockGate{invoked: true, checked: 3, err: checkErr},
			want:      "could_not_run",
			wantStage: "did not complete",
			why: "nothing is known about those files, which is not the same fact as knowing " +
				"they are clear — and unlike a refusal, the caller just retries",
		},
		{
			name:      "ran, locks acquired",
			gate:      &commitLockGate{invoked: true, ran: true, checked: 3, acquired: []string{"a.go"}},
			want:      "acquired",
			wantStage: "now locked by it",
			why:       "positive control: a widened lock set has to stay visible where it was widened",
		},
		{
			name:      "ran, everything already covered",
			gate:      &commitLockGate{invoked: true, ran: true, checked: 3},
			want:      "covered",
			wantStage: "already covered",
			why:       "positive control: the quiet success must not be dressed up as a failure",
		},
	}

	details := map[string]string{}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			out := tc.gate.report(map[string]any{}, tc.stageErr)
			got, _ := out["lock_gate"].(string)
			if got != tc.want {
				t.Fatalf("lock_gate = %q, want %q — %s", got, tc.want, tc.why)
			}
			detail, _ := out["lock_gate_detail"].(string)
			if detail == "" {
				t.Fatal("lock_gate_detail is empty; the bare value does not say what to do next")
			}
			if !strings.Contains(detail, tc.wantStage) {
				t.Errorf("lock_gate_detail = %q, want it to say %q — %s", detail, tc.wantStage, tc.why)
			}
			// "no staged changes" is a claim about the index, and it is only
			// knowable on the one row where the commit stage got far enough to
			// look. Anywhere else it contradicts the response it sits in.
			if tc.want != "not_run" && strings.Contains(detail, "no staged changes") {
				t.Errorf("lock_gate_detail = %q asserts an empty index, but this row either had "+
					"%d file(s) staged or never found out — %s", detail, tc.gate.checked, tc.why)
			}
			// Six facts, six DETAILS. Names may repeat where the caller's next
			// move is the same; the sentence they read must not.
			if prev, dup := details[detail]; dup {
				t.Errorf("this row is indistinguishable from %q: both say %q. A reader cannot "+
					"tell which of the two happened", prev, detail)
			}
			details[detail] = tc.name
		})
	}
}

// TestStrSliceArg characterizes the `paths` decoding that pf_commit and pf_ship
// now share.
func TestStrSliceArg(t *testing.T) {
	cases := []struct {
		name string
		args map[string]any
		want []string
	}{
		{"absent", map[string]any{}, nil},
		{"null", map[string]any{"paths": nil}, nil},
		{"not an array", map[string]any{"paths": "a.go"}, nil},
		{"strings", map[string]any{"paths": []any{"a.go", "b.go"}}, []string{"a.go", "b.go"}},
		{"skips non-strings", map[string]any{"paths": []any{"a.go", 3, true, "b.go"}}, []string{"a.go", "b.go"}},
		{"empty array", map[string]any{"paths": []any{}}, nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := strSliceArg(tc.args, "paths")
			if len(got) != len(tc.want) {
				t.Fatalf("got %q, want %q", got, tc.want)
			}
			for i := range got {
				if got[i] != tc.want[i] {
					t.Fatalf("got %q, want %q", got, tc.want)
				}
			}
		})
	}
}

// --- helpers ---------------------------------------------------------------

func containsSubstring(haystack []string, needle string) bool {
	for _, h := range haystack {
		if strings.Contains(h, needle) {
			return true
		}
	}
	return false
}

// registeredTools enumerates the server's tools over an in-memory transport pair,
// the same way `polyforge dump-mcp-schemas` builds the CI contract.
func registeredTools(t *testing.T, ctx context.Context) []*sdkmcp.Tool {
	t.Helper()
	// nil client and config: registration only calls AddTool, never a handler.
	server := New(nil, nil)

	cTransport, sTransport := sdkmcp.NewInMemoryTransports()
	serverCtx, cancel := context.WithCancel(ctx)
	t.Cleanup(cancel)

	go func() {
		session, err := server.Connect(serverCtx, sTransport)
		if err != nil {
			return
		}
		_ = session.Wait()
	}()

	client := sdkmcp.NewClient(&sdkmcp.Implementation{Name: "tools-test", Version: "1.0.0"}, nil)
	session, err := client.Connect(ctx, cTransport, nil)
	if err != nil {
		t.Fatalf("client connect: %v", err)
	}
	t.Cleanup(func() { _ = session.Close() })

	var tools []*sdkmcp.Tool
	for tool, iterErr := range session.Tools(ctx, nil) {
		if iterErr != nil {
			t.Fatalf("tools iteration: %v", iterErr)
		}
		tools = append(tools, tool)
	}
	if len(tools) == 0 {
		t.Fatal("no tools enumerated; the harness is not exercising registration")
	}
	return tools
}

func registeredToolNames(t *testing.T, ctx context.Context) map[string]bool {
	t.Helper()
	names := map[string]bool{}
	for _, tool := range registeredTools(t, ctx) {
		names[tool.Name] = true
	}
	return names
}

func registeredTool(t *testing.T, ctx context.Context, name string) *sdkmcp.Tool {
	t.Helper()
	for _, tool := range registeredTools(t, ctx) {
		if tool.Name == name {
			return tool
		}
	}
	t.Fatalf("tool %q is not registered", name)
	return nil
}
