package cli

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/GMISWE/ieops-aihub/internal/config"
	"github.com/GMISWE/ieops-aihub/pkg/client"
)

// Two-sided gate for aihub#335: the aihub address must not be hand-copied onto
// the newcomer path, AND the historical records that name a retired address must
// keep naming it.
//
// WHY BOTH HALVES ARE HERE, AND WHY NEITHER IS SUFFICIENT
// -------------------------------------------------------
// The obvious criterion — "git grep finds no 10.146.0.16 anywhere" — is the
// wrong one, and it is wrong in a way that makes the repo worse. Counted at
// e8fbfcb with `git grep -oE '10\.146\.0\.(16|34)'`, there were 24 occurrences
// in 14 files, and only 5 of them (all in docs/onboarding.md) were the defect:
//
//	 5  docs/onboarding.md ................. the defect — removed here
//	12  historical records, CORRECT AS WRITTEN:
//	      3  docs/deployment.md, section headed `retired`
//	      2  docs/design/polyforge-v1-design.md, a frozen design contract
//	         whose own errata header says the body is not corrected in place
//	      2  internal/domain/gc.go, a comment about an audit that happened
//	      1  internal/server/routes_memory_recall_pagination_test.go, a
//	         recorded measurement ("measured against production on …")
//	      4  v0/**, an archive of the pre-v1 deployment
//	 1  docs/deployment.md "Current production" Host row — the VM an operator
//	    SSHes into. A deploy target, not a client endpoint.
//	 5  tests/scenarios/**, awaiting an owner decision (aihub#335)
//	 1  plugins/polyforge/skills/pf-revise/SKILL.md, a real stale copy, but
//	    editing plugins/ forces a five-stamp version bump
//
// A record of what an address used to be is not a stale copy of what it is. So
// 18 of the 24 must NOT be touched, and `sed -i` over the whole tree satisfies
// a one-sided gate while destroying twelve of them — which is why the second
// half asserts those records survived: a global replace turns THIS FILE red.
//
// And the first half alone is not the criterion either: it is satisfied by
// deleting docs/onboarding.md. It is paired with the compile-time constant
// (config.AihubURLDefault) that gives the newcomer path something to work from
// without an address in the prose: TestBuiltInDefaultAnswersForAnUnconfiguredMachine
// asserts the tool can still answer the question the document stopped answering.
//
// The A-half matches ANY dotted quad rather than the two addresses seen in
// 2026-09, because the whole point of the work item is that the address changes:
// a literal-string gate would go green the moment someone pasted the NEXT
// address in. The C-half is the mirror image — there it is exactly the literal
// that has to survive, because "the retired host was 10.146.0.16" is a fact
// about a specific number.
//
// A dotted quad is still a form, though, and forms can be dodged: writing
// `url = "http://aihub-prod:8080"` restores the whole defect (a hand-made copy
// that dies on a rename) while matching no IPv4 pattern. So the A-half has a
// second assertion that keys on the MECHANISM instead of the value — the thing
// a newcomer literally pastes is a fenced config.toml block, and such a block
// must not carry a [server] section at all. Between them: the regex catches
// prose that names an address, the block rule catches a pasteable snippet that
// sets one, whatever it is spelled as.
//
// KNOWN LIMIT, stated rather than papered over: the trigger set is one file.
// Prose in onboarding.md saying "copy the Host out of docs/deployment.md into
// [server] url" would pass both halves and reproduce the defect, because
// deployment.md is deliberately not in the A-set (its Host row is an operations
// record). Closing that needs a reviewer, not a regex.

// ipv4Literal matches a dotted quad, optionally with a port. Deliberately not
// restricted to 10.x: a public IP pasted onto the newcomer path is the same
// defect, and the failure mode being gated is "somebody wrote an address here",
// not "somebody wrote this particular address here".
var ipv4Literal = regexp.MustCompile(`\b\d{1,3}\.\d{1,3}\.\d{1,3}\.\d{1,3}(:\d+)?\b`)

// newcomerPathDocs are the A-class files: what an engineer joining the team
// reads and copies from, where a stale address costs them a debugging session
// against the wrong subsystem (aihub#331 was exactly that, and #335 is the
// second occurrence of the same failure).
//
// Kept to the documents a NEWCOMER follows. docs/deployment.md is deliberately
// absent: its "Current production" table names the host an operator SSHes into,
// which is a deploy target rather than a client endpoint, and an operations
// runbook that will not say which machine it operates is not a fixed runbook.
var newcomerPathDocs = []string{
	filepath.Join("docs", "onboarding.md"),
}

// historicalAddressRecords are the C-class records and the exact substring each
// must keep. One entry per KIND of historical record, so a replace that spares
// one kind and eats another still fails:
//
//   - a doc section explicitly headed `retired`
//   - a code comment about a past audit
//   - a file under the v0/ archive tree
var historicalAddressRecords = map[string][]string{
	filepath.Join("docs", "deployment.md"): {
		// The wi's own criterion 3, verbatim: the Legacy section keeps its address.
		"### Legacy single-Compose host (`10.146.0.16`, retired)",
		"| Host | `10.146.0.16` (`PROD_HOST`) |",
	},
	filepath.Join("internal", "domain", "gc.go"): {
		"10.146.0.16 and 10.146.0.34",
	},
	filepath.Join("v0", "openapi", "aihub_openapi.yaml"): {
		"http://10.146.0.16",
	},
}

// repoRootDir returns the repository root relative to this package's directory.
// Fatal, never Skip: a gate that goes green because it could not find the files
// it checks is the silent success this work item exists to remove.
func repoRootDir(t *testing.T) string {
	t.Helper()
	root := filepath.Join("..", "..")
	if _, err := os.Stat(filepath.Join(root, "go.mod")); err != nil {
		t.Fatalf("repo root not found at %s (no go.mod): %v", root, err)
	}
	return root
}

func readRepoFile(t *testing.T, root, rel string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(root, rel))
	if err != nil {
		t.Fatalf("read %s: %v — this gate cannot pass by not finding its target", rel, err)
	}
	return string(b)
}

// TestNewcomerPathCarriesNoServerAddressLiteral is the A-half.
func TestNewcomerPathCarriesNoServerAddressLiteral(t *testing.T) {
	root := repoRootDir(t)
	for _, rel := range newcomerPathDocs {
		body := readRepoFile(t, root, rel)
		for i, line := range strings.Split(body, "\n") {
			if m := ipv4Literal.FindString(line); m != "" {
				t.Errorf("%s:%d carries the address literal %q.\n"+
					"The newcomer path must not name the aihub address: the host it names "+
					"changes (docs/deployment.md records the move from 10.146.0.16 to "+
					"10.146.0.34), and a document cannot be corrected on the machines that "+
					"already copied it. The address ships in the binary as "+
					"config.AihubURLDefault; point the reader at `polyforge doctor`, which "+
					"prints the endpoint in use.\n  line: %s",
					rel, i+1, m, strings.TrimSpace(line))
			}
		}
	}
}

// tomlServerSection matches the [server] header inside a config.toml snippet,
// commented-out forms included: a commented example is still something a reader
// uncomments and fills in.
var tomlServerSection = regexp.MustCompile(`^\s*#?\s*\[server\]\s*$`)

// TestNewcomerPathPastesNoServerSection is the A-half's second assertion: no
// fenced code block on the newcomer path may set up a [server] url, in ANY
// spelling. This is the one that survives someone using a hostname instead of an
// IP — see the file comment.
func TestNewcomerPathPastesNoServerSection(t *testing.T) {
	root := repoRootDir(t)
	for _, rel := range newcomerPathDocs {
		inFence := false
		fenceLine := 0
		for i, line := range strings.Split(readRepoFile(t, root, rel), "\n") {
			if strings.HasPrefix(strings.TrimSpace(line), "```") {
				inFence = !inFence
				if inFence {
					fenceLine = i + 1
				}
				continue
			}
			if inFence && tomlServerSection.MatchString(line) {
				t.Errorf("%s:%d — the code block opened at line %d sets up a [server] "+
					"section.\nA fenced block on the newcomer path is a thing people PASTE, "+
					"and pasting a [server] url is how a machine acquires a private copy of "+
					"an address that later moves. It does not matter whether the value is an "+
					"IP, a hostname or a placeholder: the endpoint comes from "+
					"config.AihubURLDefault, and a reader who needs to override it is "+
					"already past onboarding.", rel, i+1, fenceLine)
			}
		}
		if inFence {
			t.Errorf("%s has an unclosed code fence (opened at line %d); this test "+
				"tracks fences to decide what is pasteable and cannot do that on a file "+
				"with unbalanced ```", rel, fenceLine)
		}
	}
}

// TestHistoricalAddressRecordsAreIntact is the C-half: proof that the A-half was
// satisfied semantically and not by a tree-wide find-and-replace.
func TestHistoricalAddressRecordsAreIntact(t *testing.T) {
	root := repoRootDir(t)
	for rel, wants := range historicalAddressRecords {
		body := readRepoFile(t, root, rel)
		for _, want := range wants {
			if !strings.Contains(body, want) {
				t.Errorf("%s no longer contains the historical record %q.\n"+
					"This is a RECORD OF A RETIRED ADDRESS, not a stale copy of a live one, "+
					"and it is correct as written. If you removed it to make "+
					"TestNewcomerPathCarriesNoServerAddressLiteral pass — or ran a tree-wide "+
					"replace — that is the failure mode this test exists to catch: revert it "+
					"and fix the newcomer path instead.\n"+
					"If instead you deliberately RETIRED this record (deleting the v0/ "+
					"archive is the case to expect), then this entry is the stale one: drop "+
					"it from historicalAddressRecords in the same commit. Keep at least one "+
					"entry per kind of record, or the A-half is unguarded again.", rel, want)
			}
		}
	}
}

// TestBuiltInDefaultAnswersForAnUnconfiguredMachine is what makes removing the
// address from the docs a fix rather than a deletion: a machine with no
// [server] block and no POLYFORGE_AIHUB_URL still resolves to a usable
// endpoint, so the newcomer never has to be told one.
func TestBuiltInDefaultAnswersForAnUnconfiguredMachine(t *testing.T) {
	t.Setenv("POLYFORGE_AIHUB_URL", "")

	url, source := config.EffectiveAihubURL(&config.MachineConfig{}, "")
	if url != config.AihubURLDefault {
		t.Errorf("EffectiveAihubURL on an unconfigured machine = %q, want the built-in "+
			"default %q — with neither the docs nor the config carrying the address, this "+
			"is the only thing left that knows it", url, config.AihubURLDefault)
	}
	if source == "" {
		t.Error("EffectiveAihubURL returned no source; `polyforge doctor` prints it so a " +
			"human can tell a built-in default from an override they forgot about")
	}
	if !ipv4Literal.MatchString(config.AihubURLDefault) && !strings.Contains(config.AihubURLDefault, "://") {
		t.Errorf("config.AihubURLDefault = %q is not a usable URL", config.AihubURLDefault)
	}
}

// TestDoctorConfigLineNamesTheEndpointAndItsSource pins the ONE line a human
// now reads the address off. docs/onboarding.md quotes this line's shape and
// tells the reader to copy the base URL out of it, and having removed the
// address from the prose there is no second place to look: if the rendering
// drifts, the instruction rots silently. So the format is asserted, not just
// the fact that the URL appears somewhere.
//
// Both branches are covered because the doc claims both: that the endpoint is
// printed when the server answers, AND that it is printed when there is no API
// key at all ("is it me or is it the address?").
func TestDoctorConfigLineNamesTheEndpointAndItsSource(t *testing.T) {
	const source = "built-in default"

	t.Run("reachable", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path != "/v1/health" {
				w.WriteHeader(http.StatusNotFound)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"status":"ok","db_ok":true,` +
				`"embedding_enabled":true,"embedding_ok":true}`))
		}))
		defer srv.Close()

		got := checkConfig(context.Background(), client.New(srv.URL, "k"), srv.URL, source)
		want := srv.URL + " (" + source + ") — aihub reachable"
		if got.Status != "ok" || got.Message != want {
			t.Errorf("checkConfig() = {%s, %q}, want {ok, %q}\n"+
				"docs/onboarding.md quotes this line verbatim under "+
				"\"Which aihub am I talking to?\" — update the doc together with it",
				got.Status, got.Message, want)
		}
	})

	t.Run("no api key: still names the endpoint", func(t *testing.T) {
		got := checkConfig(context.Background(), nil, config.AihubURLDefault, source)
		if got.Status != "warning" {
			t.Errorf("status = %q, want warning", got.Status)
		}
		for _, want := range []string{config.AihubURLDefault, source} {
			if !strings.Contains(got.Message, want) {
				t.Errorf("message %q does not name %q — with no client to ask, this "+
					"line is the only thing that can tell a wrong address from a "+
					"missing key", got.Message, want)
			}
		}
		if got.FixCmd == "" {
			t.Error("no FixCmd: the cause here is the API key, and the report should say so")
		}
	})
}
