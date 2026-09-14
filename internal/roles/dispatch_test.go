package roles

import (
	"fmt"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// aihub#670. dispatch.go's table is read by two very different consumers: the four renderers in
// this package (which build real file names from it) and
// internal/cli/engine_native_dispatch_model_test.go (which pins two markdown documents against
// it). Neither of those checks the table's own SHAPE, and the shape is what both assume:
//
//   - a format string with anything other than exactly one %s silently produces a wrong name
//     rather than an error — fmt appends %!(EXTRA ...) or leaves a literal, and either way the
//     renderers write a file under that name and the gate demands the document match it, so the
//     defect propagates consistently and looks correct everywhere;
//   - mustAgentName panics on a harness the table does not cover. Today every renderer is
//     exercised by a test (generateRoles for pi/codex/opencode in internal/cli, RenderCC via the
//     staleness gate here), so a deleted row surfaces AS A PANIC. A panic mid-suite names a line
//     number, not the cause; the assertions below name the cause.
//
// Deliberately NOT asserted here: what any harness's dispatch call actually is. That is a
// runtime fact about somebody else's CLI, checkable only against that CLI, and each row's Note
// carries the evidence and the version it was measured on.

// dispatchRenderers pairs each harness with the renderer that writes its agent files and the
// extension it writes them under, so the filename agreement below is checked against the real
// renderer rather than against a restatement of it.
func dispatchRenderers() []struct {
	harness string
	ext     string
	render  func([]Role, map[string]string) (map[string]string, error)
} {
	return []struct {
		harness string
		ext     string
		render  func([]Role, map[string]string) (map[string]string, error)
	}{
		{"pi", ".md", RenderPiAgentFiles},
		{"codex", ".toml", RenderCodexAgentFiles},
		// RenderCodexProfiles is the ACTUALLY-LOADABLE codex path (RenderCodexAgentFiles writes
		// into a directory codex never scans), and before aihub#670 it had no test of any kind:
		// `grep -rn RenderCodexProfiles --include='*_test.go'` was empty. It shares
		// renderCodexTOML's filename construction with its sibling, which this change edited, so
		// leaving the one that matters unpinned would mean the covered path is the dead one.
		{"codex", ".config.toml", RenderCodexProfiles},
		{"opencode", ".md", RenderOpencodeAgentFiles},
	}
}

func TestDispatchTableShape(t *testing.T) {
	table := Dispatches()
	if len(table) == 0 {
		t.Fatal("Dispatches() is empty; every assertion below iterates it")
	}

	// Exactly one %s, and no other verb. `%%` is a literal percent and must not count.
	verbRe := regexp.MustCompile(`%[^%]`)
	seen := map[string]bool{}
	for _, d := range table {
		if seen[d.Harness] {
			t.Errorf("harness %q appears twice in the table. DispatchFor returns the FIRST "+
				"match, so the second row would be dead and its evidence unreachable.", d.Harness)
		}
		seen[d.Harness] = true

		for label, format := range map[string]string{
			"AgentNameFormat": d.AgentNameFormat,
			"AgentIDFormat":   d.AgentIDFormat,
		} {
			verbs := verbRe.FindAllString(strings.ReplaceAll(format, "%%", ""), -1)
			if len(verbs) != 1 || verbs[0] != "%s" {
				t.Errorf("%s.%s = %q has verbs %v, want exactly one %%s. Any other shape still "+
					"produces a string, so the renderers would write files under the wrong name "+
					"and the gate would demand the documents match that wrong name.",
					d.Harness, label, format, verbs)
			}
		}
		if strings.TrimSpace(d.Note) == "" {
			t.Errorf("harness %q has a blank Note. The field's contract is that a row nobody "+
				"can check is a row nobody can correct — and for an empty Call the Note is the "+
				"only place the reason lives.", d.Harness)
		}
	}

	// Sorted, as Dispatches() documents: callers iterate it and must not each re-sort.
	names := make([]string, 0, len(table))
	for _, d := range table {
		names = append(names, d.Harness)
	}
	if !sort.StringsAreSorted(names) {
		t.Errorf("Dispatches() returned %v, which is not sorted", names)
	}
}

func TestDispatchUnknownHarnessIsAnErrorNotAZeroRow(t *testing.T) {
	// The whole point of DispatchFor's ok return. A zero HarnessDispatch formats every role to
	// the empty string, which would make AgentIDFor("typo", "reviewer") == "" — a name the
	// renderers would happily write a file under and the gate would happily match a blank
	// document cell against.
	if _, ok := DispatchFor("claude-code"); ok {
		t.Error(`DispatchFor("claude-code") reported a row; the key is "cc" and a near-miss ` +
			`must not resolve`)
	}
	for _, fn := range []struct {
		name string
		call func(string, string) (string, error)
	}{{"AgentIDFor", AgentIDFor}, {"AgentNameFor", AgentNameFor}} {
		got, err := fn.call("copilot", "reviewer")
		if err == nil {
			t.Errorf("%s(\"copilot\", \"reviewer\") = %q with no error. copilot is a hook target, "+
				"not a role harness; answering for it at all is the failure.", fn.name, got)
		}
		if got != "" {
			t.Errorf("%s returned %q alongside its error; a caller that ignores the error must "+
				"not get a plausible-looking name", fn.name, got)
		}
	}
}

func TestDispatchTableCoversEveryRendererAndNamesItsFiles(t *testing.T) {
	roleList, err := LoadRoles()
	if err != nil {
		t.Fatalf("LoadRoles: %v", err)
	}
	if len(roleList) == 0 {
		t.Fatal("the role catalog is empty; the filename comparisons below would check nothing")
	}

	// cc first: its renderer takes an alias map, so it does not fit the shared signature.
	for _, r := range roleList {
		if _, err := AgentNameFor("cc", r.Name); err != nil {
			t.Fatalf("AgentNameFor(\"cc\", %q): %v — RenderCCAgentFiles calls mustAgentName, "+
				"which PANICS on this, so removing cc's row breaks the staleness gate with a "+
				"stack trace instead of a reason", r.Name, err)
		}
	}

	for _, rr := range dispatchRenderers() {
		if _, ok := DispatchFor(rr.harness); !ok {
			t.Errorf("harness %q has a renderer in this package but no dispatch row. Its "+
				"renderer calls mustAgentName and will PANIC.", rr.harness)
			continue
		}
		rendered, err := rr.render(roleList, nil)
		if err != nil {
			t.Fatalf("render %s: %v", rr.harness, err)
		}
		want := map[string]bool{}
		for _, r := range roleList {
			name, nerr := AgentNameFor(rr.harness, r.Name)
			if nerr != nil {
				t.Fatalf("AgentNameFor(%q, %q): %v", rr.harness, r.Name, nerr)
			}
			want[name+rr.ext] = true
		}
		if len(rendered) != len(want) {
			t.Errorf("render %s produced %d file(s), catalog has %d role(s)",
				rr.harness, len(rendered), len(want))
		}
		for got := range rendered {
			if !want[got] {
				var expected []string
				for k := range want {
					expected = append(expected, k)
				}
				sort.Strings(expected)
				t.Errorf("render %s wrote %q, which AgentNameFor does not produce (expected one "+
					"of %v). The documented dispatch names the AgentNameFor spelling, so this "+
					"file is one nobody would dispatch.", rr.harness, got, expected)
			}
		}
	}
}

func TestDispatchCallPlaceholdersAreTheDocumentedOnes(t *testing.T) {
	// Call is pinned into the markdown by exact containment (see
	// internal/cli/engine_native_dispatch_model_test.go), so its placeholder spellings are part
	// of the contract, not cosmetic. A row that says <prompt> where the document says <§0b>
	// turns that gate red for a reason nobody can act on.
	for _, d := range Dispatches() {
		if d.Call == "" {
			continue
		}
		if !strings.Contains(d.Call, "<id>") {
			t.Errorf("%s's Call %q does not mention <id>. The agent id is the only part of the "+
				"call that varies per step; a call that hardcodes one is wrong for four roles "+
				"out of five.", d.Harness, d.Call)
		}
		if !strings.Contains(d.Call, "<§0b>") {
			t.Errorf("%s's Call %q does not mention <§0b>, the prompt-template placeholder the "+
				"documents use. Any other spelling breaks the exact-containment pin in "+
				"internal/cli with a message that points at the document rather than here.",
				d.Harness, d.Call)
		}
		if strings.Contains(d.Call, fmt.Sprintf(d.AgentIDFormat, "")) && d.AgentIDFormat != "%s" {
			t.Errorf("%s's Call %q embeds its own agent-id prefix instead of <id>; the id must "+
				"come from AgentIDFormat so the two cannot disagree", d.Harness, d.Call)
		}
	}
}
