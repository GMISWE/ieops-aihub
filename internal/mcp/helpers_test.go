package mcp

import (
	"reflect"
	"strings"
	"testing"
)

// TestPrPayload covers the defensive url/number extraction for the pf_pr
// pr_opened event (aihub#208): gh's result shape varies between a freshly
// created PR (url+number present) and an "already exists" response (neither
// present), so the payload must only carry the keys that are actually there.
func TestPrPayload(t *testing.T) {
	t.Run("fresh PR has url and number", func(t *testing.T) {
		got := prPayload("aihub", "my title", map[string]any{"url": "https://github.com/x/y/pull/1", "number": float64(1)})
		want := map[string]any{"repo": "aihub", "title": "my title", "url": "https://github.com/x/y/pull/1", "number": float64(1)}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("prPayload() = %#v, want %#v", got, want)
		}
	})

	t.Run("existing PR has neither url nor number", func(t *testing.T) {
		got := prPayload("aihub", "my title", map[string]any{"existing": true, "message": "already exists"})
		want := map[string]any{"repo": "aihub", "title": "my title"}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("prPayload() = %#v, want %#v", got, want)
		}
	})

	t.Run("nil result still produces repo/title", func(t *testing.T) {
		got := prPayload("aihub", "my title", nil)
		want := map[string]any{"repo": "aihub", "title": "my title"}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("prPayload() = %#v, want %#v", got, want)
		}
	})
}

// TestAddWorktrees covers the worktrees pass-through used by pf_wrap,
// pf_claim_work_item, and pf_complete_attempt (aihub#207 Change 2): the
// response of each of these must carry the state file's worktree map so the
// caller doesn't need to have read the state file itself first.
func TestAddWorktrees(t *testing.T) {
	t.Run("non-empty worktrees are added", func(t *testing.T) {
		result := map[string]any{"ok": true}
		addWorktrees(result, map[string]string{"aihub": "/ws/.repo/aihub"})
		got, ok := result["worktrees"].(map[string]string)
		if !ok {
			t.Fatalf("result[worktrees] = %#v, want map[string]string", result["worktrees"])
		}
		if got["aihub"] != "/ws/.repo/aihub" {
			t.Errorf("worktrees[aihub] = %q, want /ws/.repo/aihub", got["aihub"])
		}
	})

	t.Run("nil worktrees leave result untouched", func(t *testing.T) {
		result := map[string]any{"ok": true}
		addWorktrees(result, nil)
		if _, ok := result["worktrees"]; ok {
			t.Errorf("result[worktrees] should be absent for nil input, got %#v", result["worktrees"])
		}
	})

	t.Run("empty worktrees leave result untouched", func(t *testing.T) {
		result := map[string]any{"ok": true}
		addWorktrees(result, map[string]string{})
		if _, ok := result["worktrees"]; ok {
			t.Errorf("result[worktrees] should be absent for empty input, got %#v", result["worktrees"])
		}
	})
}

// aihub#333: expected_removals binds to a []string on the server, so a bare
// string must be coerced here rather than becoming an opaque 400 at c.Bind two
// layers away — the aihub#241 B1 shape, which normalizeIntArg exists to prevent
// for the integer next to it.
//
// The wrong-type cases assert an ERROR rather than a silent drop, and that is
// the discriminating half: dropping expected_removals turns a declared removal
// into an undeclared one and answers 412, so the caller would be told they did
// not declare a removal while the declaration sits in their own request.
func TestNormalizeStringSliceArg(t *testing.T) {
	t.Run("BareStringBecomesOneElementList", func(t *testing.T) {
		args := map[string]any{"expected_removals": "u_two"}
		if err := normalizeStringSliceArg(args, "expected_removals"); err != nil {
			t.Fatalf("a single user_id sent as a bare string was rejected: %v", err)
		}
		got, ok := args["expected_removals"].([]string)
		if !ok || len(got) != 1 || got[0] != "u_two" {
			t.Errorf("got %#v, want []string{\"u_two\"}", args["expected_removals"])
		}
	})

	t.Run("JSONArrayBecomesStringSlice", func(t *testing.T) {
		args := map[string]any{"expected_removals": []any{"u_two", "u_three"}}
		if err := normalizeStringSliceArg(args, "expected_removals"); err != nil {
			t.Fatalf("the ordinary array form was rejected: %v", err)
		}
		got, _ := args["expected_removals"].([]string)
		if len(got) != 2 || got[0] != "u_two" || got[1] != "u_three" {
			t.Errorf("got %#v, want []string{\"u_two\",\"u_three\"}", args["expected_removals"])
		}
	})

	t.Run("AbsentAndNullAreLeftAlone", func(t *testing.T) {
		args := map[string]any{}
		if err := normalizeStringSliceArg(args, "expected_removals"); err != nil {
			t.Errorf("an absent key must not error: %v", err)
		}
		if _, present := args["expected_removals"]; present {
			t.Error("an absent key must not be materialised; no declaration means 'removes nobody'")
		}
		args = map[string]any{"expected_removals": nil}
		if err := normalizeStringSliceArg(args, "expected_removals"); err != nil {
			t.Errorf("a null value must not error: %v", err)
		}
	})

	t.Run("NonStringElementIsNamedNotDropped", func(t *testing.T) {
		args := map[string]any{"expected_removals": []any{"u_two", 7}}
		err := normalizeStringSliceArg(args, "expected_removals")
		if err == nil {
			t.Fatal("an array with a non-string element was accepted; whatever happens next, the caller " +
				"will not learn that their declaration was malformed")
		}
		if !strings.Contains(err.Error(), "expected_removals") {
			t.Errorf("the error does not name the field: %v", err)
		}
	})

	t.Run("WrongTypeIsNamedNotDropped", func(t *testing.T) {
		args := map[string]any{"expected_removals": 7}
		err := normalizeStringSliceArg(args, "expected_removals")
		if err == nil {
			t.Fatal("a number was accepted as a removal declaration")
		}
		if !strings.Contains(err.Error(), "expected_removals") {
			t.Errorf("the error does not name the field: %v", err)
		}
	})
}
