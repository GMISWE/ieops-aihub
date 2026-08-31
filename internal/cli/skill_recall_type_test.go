package cli

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// Recurrence gate for the `pf_recall(type=...)` contract (aihub#289).
//
// WHAT WENT WRONG
// ---------------
// Three skill templates taught `type="methodology.spec|methodology.plan|fact.*|..."`.
// The server's `type` filter is a LIST, and nothing anywhere in the chain — MCP tool,
// HTTP handler, or either SQL builder — ever split on `|`. The whole string therefore
// arrived as ONE type name, matched no row on either the exact or the LIKE branch,
// and came back as `{"items":null,"total":0}`: an empty set, no error, no warning.
//
// That empty set is indistinguishable from "this project holds no such memory", so an
// agent obeying the Memory-First rule read it as "no prior experience exists" and went
// on to redo work and re-hit pitfalls that were already recorded. Eight real calls
// took that path. The reason it was not eight hundred is that models USUALLY
// translated the template into an array on their own — i.e. the feature's correctness
// rested on the model improvising past the documentation.
//
// WHY A TEST AND NOT A COMMENT
// ----------------------------
// The templates were wrong for months with the truth plainly readable in the handler
// three directories away. A warning in prose next to the fixed line would have the
// same enforcement power as the thing it replaced: none. This fails the build instead.
//
// It is a Go test on purpose, mirroring usage_channel_test.go: `go test ./...` runs on
// every push, and this needs no new CI wiring and no PASS-count floor to be honest.
//
// SCOPE: a QUOTED `type="..."` value must not contain `|`, because that is the form
// that reaches the server verbatim. Placeholder notation is untouched —
// `type=<experience.* | fact.* | rule.*>` and pf-user's `user_type=<"human"|"machine">`
// are instructions to a reader to choose one, not strings to transmit, and the angle
// brackets are what tells them apart.
var quotedTypeWithPipeRe = regexp.MustCompile(`\btype\s*=\s*"[^"]*\|`)

func TestSkillTemplates_NoPipedTypeStrings(t *testing.T) {
	root := filepath.Join("..", "..", "plugins", "polyforge")
	if _, err := os.Stat(filepath.Join(root, "skills")); err != nil {
		// Fatal, never Skip. A gate that goes green because it could not find the
		// tree it is meant to check is the same silent success this work item exists
		// to remove. The tree is committed at a fixed path, so absence is a fault.
		t.Fatalf("polyforge plugin tree not found at %s: %v", root, err)
	}

	var offenders []string
	err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() || !strings.HasSuffix(path, ".md") {
			return nil
		}
		b, readErr := os.ReadFile(path)
		if readErr != nil {
			return readErr
		}
		for i, line := range strings.Split(string(b), "\n") {
			if quotedTypeWithPipeRe.MatchString(line) {
				offenders = append(offenders, fmt.Sprintf("%s:%d: %s", path, i+1, strings.TrimSpace(line)))
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walking %s: %v", root, err)
	}

	if len(offenders) > 0 {
		t.Errorf("a quoted type=\"...\" value contains '|', which the server rejects "+
			"(aihub#289): '|' is not a separator, so the whole string is sent as one type "+
			"name. Pass an array instead — type=[\"a.b\",\"c.*\"]. Offenders:\n%s",
			strings.Join(offenders, "\n"))
	}
}

// TestSkillTemplates_NoPipedTypeStrings_Discriminates is the negative control: without
// it, the check above passes just as happily on a tree where the pattern never matches
// anything at all — including if the regexp is broken. This proves the detector fires
// on the exact string the three templates used to carry, and stays quiet on the
// placeholder form that is deliberately still allowed.
func TestSkillTemplates_NoPipedTypeStrings_Discriminates(t *testing.T) {
	mustMatch := []string{
		`pf_recall(project=<current>, query=<wi.goal>, type="methodology.spec|methodology.plan|fact.*", top_k=8)`,
		`pf_recall(project=<current>, query=<user_intent>, type="experience.*|rule.*", top_k=5)`,
		`pf_remember(type="experience.*|fact.*|rule.*", project=<current>)`,
		`  type = "a|b"`,
	}
	for _, s := range mustMatch {
		if !quotedTypeWithPipeRe.MatchString(s) {
			t.Errorf("detector missed a broken form, so the repo scan above proves nothing: %s", s)
		}
	}

	mustNotMatch := []string{
		`pf_recall(project=<current>, type=["experience.*","rule.*"], top_k=5)`,
		`pf_remember(type=<ONE concrete type — e.g. experience.pitfall / rule.work>)`,
		`pf_remember(type=<experience.* | fact.* | rule.*>)`,
		`  user_type=<"human"|"machine", default: "human">,`,
		`| type | description |`,
		`type="experience.pitfall"`,
	}
	for _, s := range mustNotMatch {
		if quotedTypeWithPipeRe.MatchString(s) {
			t.Errorf("detector false-positived on a legitimate form, which would make the "+
				"gate unmaintainable: %s", s)
		}
	}
}
