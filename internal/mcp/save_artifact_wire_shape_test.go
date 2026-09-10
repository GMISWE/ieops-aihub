package mcp_test

// aihub#543 probe wave 2, lane L5 — `docs/mcp-cards/pf_save_artifact.md`'s hop
// 2-3 and the hop 0-1 uniqueness claim about `path`:
//
//	"`visibility`, `structured_payload`, `supersedes_memory_id` and `html` are
//	 forwarded only when non-empty"   ← MEASURED FALSE for one of the four;
//	                                    the card now says what the wire does
//	    -> TestSaveArtifactForwardsOptionalKeysOnlyWhenSet
//	"sends the body of `POST /v1/memories` … the same endpoint `pf_remember`
//	 uses. The difference is credentials"
//	    -> TestSaveArtifactAndRememberPostOneEndpointAndDifferByCredentials
//	"`path` is the one parameter … consumed by the local filesystem"
//	                                  ← MEASURED FALSE as a quantifier over
//	                                    "the local filesystem"; corrected to
//	                                    the file this process READS
//	    -> TestOnlyOnePublishedParameterNamesAFileThisProcessReads
//
// ─── What was already covered, stated precisely ────────────────────────────
//
// memory_tools_wire_test.go holds every published pf_save_artifact property to
// the VALUE it lands on, including the two renames (`path` -> `content`,
// `html` -> `rendered_html`). What it cannot see is the OMISSION direction: each
// probe sets its property to a truthy fixture and asserts the value arrives, so
// a builder that forwarded all four unconditionally satisfies it exactly as well
// as this one does. It also asserts the request PATH only for `landPath` probes,
// and every pf_save_artifact property is `landBody` — so no arm in the tree read
// the endpoint this tool posts to.
//
// No database:
//
//	GOWORK=off go test ./internal/mcp/ -run 'TestSaveArtifact|TestOnlyOnePublished' -count=1

import (
	"encoding/json"
	"go/ast"
	"go/parser"
	"go/token"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"
)

// memoriesWirePath is the route both memory doors POST, and therefore the only
// one the body assertions below may read.
const memoriesWirePath = "/v1/memories"

// driveMemoryTool runs one real tool call against a fake aihub in an isolated
// workspace and returns the body the server received.
//
// The FLOOR lives here rather than in each test, because most assertions below
// are about keys that must be ABSENT and an absent key cannot be told apart from
// a body that carried nothing at all. So the body is first required to carry the
// two keys every call on this endpoint renders — `type` and `content` — and a
// body missing either fails here instead of silently satisfying an absence
// assertion.
func driveMemoryTool(t *testing.T, tool, wiID string, args map[string]any) map[string]any {
	t.Helper()

	f := newFakeAihub(t)
	f.on(memoriesWirePath, func(map[string]any) (int, any) {
		return http.StatusOK, map[string]any{"memory_id": "mem_probe543", "is_new": true}
	})

	result, isErr := callToolBounded(t, f, tool, args, 20*time.Second)
	if isErr {
		t.Fatalf("%s failed: %v — a refused call reaches the server with a body nobody should "+
			"draw conclusions from", tool, result)
	}

	body := lastBodyFor(t, f, memoriesWirePath)
	for _, always := range []string{"type", "content"} {
		if s, _ := body[always].(string); s == "" {
			t.Fatalf("the %s body carries no %s (keys: %v) — the walk is broken and the absence "+
				"assertions below would pass against a body like this", tool, always, sortedBodyKeys(body))
		}
	}
	return body
}

// saveArtifactArgs is a minimal valid pf_save_artifact call.
func saveArtifactArgs(wiID string, extra map[string]any) map[string]any {
	args := map[string]any{
		"work_item_id": wiID,
		"type":         "methodology.spec",
		"content":      "the artifact body",
	}
	for k, v := range extra {
		args[k] = v
	}
	return args
}

// TestSaveArtifactForwardsOptionalKeysOnlyWhenSet pins hop 2-3's rule about the
// four optional keys — and it is the arm that MEASURED the card's old wording
// false.
//
// 🔴 The card said all four are "forwarded only when non-empty". Three of them
// are: `visibility`, `supersedes_memory_id` and `html` go through
// strArg(...) != "", so an explicit "" is byte-identical to an omission (the
// pf_update_step finding, one tool along). `structured_payload` is not: its
// guard is `if v, ok := args["structured_payload"]; ok`, which tests for the KEY
// and not for the value, so an explicit JSON `null` and an explicit `{}` both
// reach the wire — measured here, both arms below. No caller is harmed by it
// today, because validateJSONObjectParam folds a literal null back to "not
// supplied" server-side; what was wrong was the card, and the card was corrected
// rather than the builder, since changing the builder is a wire change nobody
// asked for.
//
// What the rule really buys for the three is what the pf_update_step card now
// says: a key that ARRIVES always carries a value the caller meant.
//
// MUTANTS (applied to this tree; the verdict is what ran):
//
//	M20 enforcement: drop the `!= ""` guard on `visibility` in
//	    buildSaveArtifactBody, forwarding it always
//	                                            RED  the omitted and empty-string
//	                                                 arms
//	M21 enforcement: change structured_payload's `if v, ok :=` guard to
//	    `if v := args[...]; v != nil` — i.e. make the card's OLD sentence true
//	                                            RED  the explicit-null arm, which
//	                                                 is the arm that exists to
//	                                                 record what the wire does
//	M23 publication: delete this arm's citation from both clauses of the card
//	    sentence                                RED  K12 — the sentence names no
//	                                                 other arm, so it lands back in
//	                                                 the debt column and the
//	                                                 pf_save_artifact ledger row
//	                                                 stops matching
//
//	── recorded GREEN, because it is this family's stated limit ──
//	M22 publication: restore the card's old "all four … only when non-empty"
//	    wording with the citation left in place GREEN in K12 and GREEN here. NO arm
//	                                                  in this repo reads that
//	                                                  sentence, so a card sentence
//	                                                  can be made false again
//	                                                  without going red. K12 checks
//	                                                  that a claim is POINTED at an
//	                                                  arm, never that it is true —
//	                                                  its stated limit, and
//	                                                  polyforge-scenario#20's
//	                                                  reviewer is the other half
func TestSaveArtifactForwardsOptionalKeysOnlyWhenSet(t *testing.T) {
	const wiID = "wi_artifact_optional"

	// The keys this hop renders on every call, whatever the optional arguments
	// say. Named once so the absence arms compare a whole body rather than
	// probing for the key they already expect to be missing — a probe for one
	// name stays green when a fifth optional key starts riding along.
	always := []string{"attempt_id", "claim_epoch", "content", "session_secret", "type", "work_item_id"}

	t.Run("all four omitted", func(t *testing.T) {
		seedStateFile(t, wiID)
		body := driveMemoryTool(t, "pf_save_artifact", wiID, saveArtifactArgs(wiID, nil))
		requireExactKeys(t, body, always)
	})

	// The three string-valued keys: present when non-empty, absent when omitted,
	// and absent when explicitly "". The third arm is the one the sentence is
	// about, and the one no existing probe could see.
	for _, tc := range []struct {
		arg     string
		lands   string
		nonZero string
	}{
		{arg: "visibility", lands: "visibility", nonZero: "team"},
		{arg: "supersedes_memory_id", lands: "supersedes_memory_id", nonZero: "mem_old543"},
		{arg: "html", lands: "rendered_html", nonZero: "<p>rendered</p>"},
	} {
		t.Run(tc.arg+" set", func(t *testing.T) {
			seedStateFile(t, wiID)
			body := driveMemoryTool(t, "pf_save_artifact", wiID,
				saveArtifactArgs(wiID, map[string]any{tc.arg: tc.nonZero}))
			if got := body[tc.lands]; got != tc.nonZero {
				t.Errorf("%s=%q arrived at body.%s as %#v", tc.arg, tc.nonZero, tc.lands, got)
			}
		})
		t.Run(tc.arg+" explicitly empty", func(t *testing.T) {
			seedStateFile(t, wiID)
			body := driveMemoryTool(t, "pf_save_artifact", wiID,
				saveArtifactArgs(wiID, map[string]any{tc.arg: ""}))
			requireExactKeys(t, body, always)
		})
	}

	// structured_payload, both directions, and the two that make the card's old
	// sentence false.
	t.Run("structured_payload set", func(t *testing.T) {
		seedStateFile(t, wiID)
		payload := map[string]any{"criteria": []any{"a", "b"}}
		body := driveMemoryTool(t, "pf_save_artifact", wiID,
			saveArtifactArgs(wiID, map[string]any{"structured_payload": payload}))
		want, err := json.Marshal(payload)
		if err != nil {
			t.Fatalf("marshal the fixture: %v", err)
		}
		got, err := json.Marshal(body["structured_payload"])
		if err != nil {
			t.Fatalf("marshal what the server received: %v", err)
		}
		if string(got) != string(want) {
			t.Errorf("structured_payload arrived as %s, want %s", got, want)
		}
	})
	for _, tc := range []struct {
		name  string
		value any
		want  string
	}{
		{name: "explicit null", value: nil, want: "null"},
		{name: "explicit empty object", value: map[string]any{}, want: "{}"},
	} {
		t.Run("structured_payload "+tc.name, func(t *testing.T) {
			seedStateFile(t, wiID)
			body := driveMemoryTool(t, "pf_save_artifact", wiID,
				saveArtifactArgs(wiID, map[string]any{"structured_payload": tc.value}))
			raw, ok := body["structured_payload"]
			if !ok {
				t.Fatalf("structured_payload=%v did NOT reach the wire (keys %v). That would make "+
					"the card's old \"forwarded only when non-empty\" wording true for this key, "+
					"and the ⚠️ the card now carries about it wrong — correct the card in the "+
					"same change", tc.value, sortedBodyKeys(body))
			}
			got, err := json.Marshal(raw)
			if err != nil {
				t.Fatalf("marshal what the server received: %v", err)
			}
			if string(got) != tc.want {
				t.Errorf("structured_payload=%v arrived as %s, want %s", tc.value, got, tc.want)
			}
		})
	}
}

// TestSaveArtifactAndRememberPostOneEndpointAndDifferByCredentials pins hop
// 2-3's "the same endpoint `pf_remember` uses. The difference is credentials".
//
// Two claims in one sentence and each one is what makes the other worth stating:
// if the endpoints differed, the credential difference would be a detail of two
// separate contracts; if the bodies were the same, "the same endpoint" would
// make the two tools interchangeable, which is the thing the server's
// methodology branch exists to prevent. So both are asserted from two real
// requests recorded in the same run.
//
// ⚠️ The credential ASYMMETRY is what is asserted, not that pf_remember can
// never carry credentials — it can, when a caller supplies work_item_id and the
// state file exists. What holds is that this tool injects them without being
// asked and pf_remember does not, which is what "selects the server's
// methodology branch" rests on.
//
// MUTANTS (applied to this tree; the verdict is what ran):
//
//	M24 enforcement: point pf_save_artifact at POST /v1/artifacts in
//	    pkg/client                              RED  the shared-endpoint arm
//	M25 enforcement: drop the credential injection from buildSaveArtifactBody
//	                                            RED  the asymmetry arm
//	M26 publication: remove this arm's citation from the two card sentences
//	                                            RED  K12 POPULATION_MOVED ×2
func TestSaveArtifactAndRememberPostOneEndpointAndDifferByCredentials(t *testing.T) {
	const wiID = "wi_artifact_endpoint"
	credentials := []string{"attempt_id", "claim_epoch", "session_secret"}

	seedStateFile(t, wiID)
	artifact := driveMemoryTool(t, "pf_save_artifact", wiID, saveArtifactArgs(wiID, nil))

	seedStateFile(t, wiID)
	remembered := driveMemoryTool(t, "pf_remember", wiID, map[string]any{
		"project": "p_probe543", "type": "experience.debug", "content": "a memory body",
		"visibility": "project",
	})

	// The shared endpoint. driveMemoryTool reads the body off memoriesWirePath, so
	// a tool posting anywhere else fails its floor rather than arriving here — but
	// that failure would read as "the walk is broken", so the fact is stated.
	for _, tool := range []string{"pf_save_artifact", "pf_remember"} {
		if len(artifact) == 0 || len(remembered) == 0 {
			t.Fatalf("%s recorded no body on %s", tool, memoriesWirePath)
		}
	}

	for _, key := range credentials {
		if _, ok := artifact[key]; !ok {
			t.Errorf("pf_save_artifact's body carries no %s (keys %v). The card says this body's "+
				"credentials are what selects the server's methodology branch; without them the "+
				"branch enforceMethodologyAttemptGate takes is the other one", key, sortedBodyKeys(artifact))
		}
		if _, ok := remembered[key]; ok {
			t.Errorf("pf_remember's body carries %s (keys %v) — the card's \"the difference is "+
				"credentials\" is then false, and the two tools reach the same endpoint with the "+
				"same authority", key, sortedBodyKeys(remembered))
		}
	}
}

// requireExactKeys fails unless body's key set is exactly want.
func requireExactKeys(t *testing.T, body map[string]any, want []string) {
	t.Helper()
	got := sortedBodyKeys(body)
	expect := append([]string(nil), want...)
	sort.Strings(expect)
	if len(got) != len(expect) {
		t.Fatalf("the body carries keys %v, want exactly %v — every extra key is a value the "+
			"caller did not set and the server will act on", got, expect)
	}
	for i := range expect {
		if got[i] != expect[i] {
			t.Fatalf("the body carries keys %v, want exactly %v", got, expect)
		}
	}
}

// fileContentReaders are the standard-library calls that read a file's CONTENT.
// os.Stat and its friends are deliberately absent: the card's claim is about a
// parameter whose value is read as the artifact body, and a worktree path that
// gets stat'ed to decide whether a directory exists is not that.
var fileContentReaders = map[string]bool{"ReadFile": true, "Open": true, "OpenFile": true}

// fileReadSitesNotFromACallerNamedPath are the content-read call sites in this
// package whose path this process CONSTRUCTS rather than receives.
//
// An entry is a claim a reader can check by opening the file, in the shape
// cursor_validation_test.go's rawCursorReadExemptions uses, and the arm below
// checks it non-stale in both directions: a site missing from here fails, and an
// entry naming a site that no longer reads a file fails too.
var fileReadSitesNotFromACallerNamedPath = map[string]string{
	"tools_lifecycle.go/writeWorktreeExcludes": "appends three patterns to " +
		".git/info/exclude. The path is filepath.Join(worktree, \".git/info/exclude\") — a fixed " +
		"relative name under a directory this process derived, so no caller ever names the file " +
		"that is read.",
}

// TestOnlyOnePublishedParameterNamesAFileThisProcessReads is hop 0-1's
// uniqueness claim about `path`, in the form that is TRUE.
//
// 🔴 The card said `path` is "the one parameter in the whole toolset whose value
// is consumed by the local filesystem", and measured against this package that
// quantifier is FALSE: `pf_commit.workspace_root`, `pf_push.workspace_root` and
// `pf_ship.workspace_root` are published parameters whose values locate a
// worktree on this machine, which the universal gate's own local-consumption
// table records in as many words ("passed to coding.WorktreePath to locate the
// worktree this commit runs in"). They are consumed by the local filesystem too.
//
// What is unique to `path` is narrower and is the property the sentence is
// really about: it is the only published parameter whose value NAMES A FILE THIS
// PROCESS READS. The card now says that, and this arm is what holds it — as a
// census over the content-read call sites in the package, because the way the
// claim becomes false is a second reader arriving, not this one changing.
//
// MUTANTS (applied to this tree; the verdict is what ran):
//
//	M27 enforcement: add `os.ReadFile(strArg(args, "pr_body_file"))` to
//	    tools_coding.go                         RED  names the new site
//	M28 enforcement: delete the exemption entry for the exclude-file writer
//	                                            RED  the unexplained-site arm,
//	                                                 which is what proves the walk
//	                                                 sees a real site
//	M29 enforcement: add a second call to resolveArtifactContent
//	                                            RED  the single-caller arm
//	M31 publication: delete this arm's citation clause from the card sentence
//	                                            RED  K12 — the sentence names no
//	                                                 other arm
//
//	── recorded GREEN ──
//	M30 publication: restore the card's old "the local filesystem" quantifier with
//	    the citation left in place              GREEN nothing reads that sentence;
//	                                                  see M22 above for why that is
//	                                                  K12's stated limit rather than
//	                                                  a hole in this arm
func TestOnlyOnePublishedParameterNamesAFileThisProcessReads(t *testing.T) {
	sites, resolverCalls := mcpFileReadSites(t)

	// FLOOR: the walk found the site this claim is ABOUT. A walk that found
	// nothing satisfies every assertion below by having read no source.
	found := false
	for _, site := range sites {
		if strings.HasPrefix(site, "artifact_path.go/") {
			found = true
		}
	}
	if !found {
		t.Fatalf("the walk found no content-read call site in artifact_path.go, which certainly "+
			"has one — the AST walk is what broke, and its silence would read as \"nothing else "+
			"reads a file\". Found: %v", sites)
	}
	if len(sites) < 2 {
		t.Fatalf("the walk found %d content-read call site(s) in this package (%v); with fewer "+
			"than two, the exemption half below is exercised against nothing", len(sites), sites)
	}

	var unexplained []string
	for _, site := range sites {
		if strings.HasPrefix(site, "artifact_path.go/") {
			continue // the `path` parameter's own reader, which is the claim
		}
		reason, exempt := fileReadSitesNotFromACallerNamedPath[site]
		if !exempt || reason == "" {
			unexplained = append(unexplained, site)
		}
	}
	sort.Strings(unexplained)
	if len(unexplained) > 0 {
		t.Errorf("these call sites read a file's content in internal/mcp and are not accounted "+
			"for: %v\nIf the path comes from a published tool parameter, pf_save_artifact.path is "+
			"no longer the only one and docs/mcp-cards/pf_save_artifact.md's hop 0-1 must say so. "+
			"If this process constructs the path, add:\n\n\t%q: \"<why no caller names this file>\",\n\n"+
			"to fileReadSitesNotFromACallerNamedPath.", unexplained, unexplained[0])
	}

	// The mirror: an exemption for a site that no longer reads a file is a
	// comment pretending to be a check, and it is how such a list grows past
	// what anybody has read.
	have := map[string]bool{}
	for _, site := range sites {
		have[site] = true
	}
	for site := range fileReadSitesNotFromACallerNamedPath {
		if !have[site] {
			t.Errorf("fileReadSitesNotFromACallerNamedPath still exempts %s, which no longer "+
				"reads a file's content — delete the entry", site)
		}
	}

	// And the other half of "the one parameter": the reader has exactly one
	// caller, so no second tool can be feeding it a path of its own.
	if len(resolverCalls) != 1 {
		t.Errorf("resolveArtifactContent is called from %v, want exactly one call site. It reads "+
			"the file named by a published parameter, so a second caller is a second parameter "+
			"whose value names a file this process reads", resolverCalls)
	}
}

// mcpFileReadSites returns the "<file>/<func>" of every content-read call in the
// non-test source of this package, plus the call sites of
// resolveArtifactContent.
//
// go/parser rather than a scan of the bytes, for the reason BuildArmIndex gives
// for the same choice: this package's source discusses `os.Stat` and
// `resolveArtifactContent` in several long comments, and a text scan reports
// every one of those sentences as a call site.
func mcpFileReadSites(t *testing.T) (sites, resolverCalls []string) {
	t.Helper()
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatalf("list the package directory: %v", err)
	}
	scanned := 0
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		fset := token.NewFileSet()
		f, perr := parser.ParseFile(fset, filepath.Join(".", name), nil, parser.SkipObjectResolution)
		if perr != nil {
			t.Fatalf("parse %s: %v", name, perr)
		}
		scanned++
		for _, decl := range f.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Body == nil {
				continue
			}
			where := name + "/" + fn.Name.Name
			ast.Inspect(fn.Body, func(n ast.Node) bool {
				call, isCall := n.(*ast.CallExpr)
				if !isCall {
					return true
				}
				sel, isSel := call.Fun.(*ast.SelectorExpr)
				if !isSel {
					if id, isID := call.Fun.(*ast.Ident); isID && id.Name == "resolveArtifactContent" {
						resolverCalls = append(resolverCalls, where)
					}
					return true
				}
				pkg, isPkg := sel.X.(*ast.Ident)
				if isPkg && pkg.Name == "os" && fileContentReaders[sel.Sel.Name] {
					sites = appendUnique(sites, where)
				}
				return true
			})
		}
	}
	if scanned == 0 {
		t.Fatal("parsed no production source — the directory listing is relative to the package " +
			"directory and must match")
	}
	sort.Strings(sites)
	sort.Strings(resolverCalls)
	return sites, resolverCalls
}

func appendUnique(xs []string, x string) []string {
	for _, existing := range xs {
		if existing == x {
			return xs
		}
	}
	return append(xs, x)
}
