package config_test

// aihub#428 — the single minting point for the state-file-missing refusal.
//
// config.StateFileMissingErr is the one place this message is built. This file
// tests the message itself; the repo-wide assertion that nothing else builds one
// lives in internal/mcp/state_file_missing_test.go, next to the aihub#421 suite
// that pins the per-tool behaviour.
//
//	go test ./internal/config/ -run TestStateFileMissingErr -v

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/GMISWE/ieops-aihub/internal/config"
)

// TestStateFileMissingErr_CarriesAllFourThings pins the four properties the
// unified wording had to satisfy at once. They are asserted separately, not as
// one golden string, so a failure says WHICH one was dropped — and three of the
// four are exactly what one of the three old prefixes had and the others lacked.
func TestStateFileMissingErr_CarriesAllFourThings(t *testing.T) {
	underlying := errors.New("state file not found for aihub#428: open /tmp/x.json: no such file or directory")
	err := config.StateFileMissingErr("aihub#428", underlying)
	msg := err.Error()

	// 1. The CODE. New in aihub#428: the family filed under CLIENT_UNCLASSIFIED
	//    (31.15% of all recorded failures) because errResult emits a bare string
	//    with nothing to classify on.
	if !strings.HasPrefix(msg, "STATE_FILE_MISSING: ") {
		t.Errorf("message = %q, want the STATE_FILE_MISSING: prefix — without a code this family "+
			"stays the largest and least analysable one in the corpus", msg)
	}
	// 2. The WORK ITEM. This is what internal/coding/scenario.go's prefix had and
	//    the other two did not, and it is the commonest prefix by call volume.
	if !strings.Contains(msg, "aihub#428") {
		t.Errorf("message = %q, want it to name the work item", msg)
	}
	// 3. The ACTION. This is what pf_emit_event alone had — 1 tool out of 12.
	if !strings.Contains(msg, "/pf-work") {
		t.Errorf("message = %q, want the recovery command. Eleven of twelve tools used to say what "+
			"failed and nothing about what to do", msg)
	}
	// 4. The UNDERLYING error, preserved verbatim. It carries the PATH, which is
	//    how an operator discovers their workspace root is wrong.
	if !strings.Contains(msg, underlying.Error()) {
		t.Errorf("message = %q, want it to contain the wrapped error %q", msg, underlying.Error())
	}
}

// TestStateFileMissingErr_UnwrapsToTheCause is the half a string comparison
// cannot see, and it is load-bearing rather than decorative.
//
// The advice the message gives ("claim it first") is right for BOTH failure modes
// this wrapper covers — a state file that was never written, and one that exists
// but will not parse — which is why the wording says "no local credential" rather
// than asserting the work item is unclaimed. The two are only distinguishable
// through the wrapped cause, so %w has to survive: a caller or an operator
// separating "never claimed" from "claimed but corrupt" has nothing else to go
// on. Building the message with %v instead of %w would leave every assertion in
// the test above green.
func TestStateFileMissingErr_UnwrapsToTheCause(t *testing.T) {
	root := t.TempDir()
	t.Setenv("POLYFORGE_WORKSPACE_ROOT", root)
	if dir := config.StateDir(); !strings.HasPrefix(dir, root+string(os.PathSeparator)) {
		t.Fatalf("config.StateDir() = %q, outside the temp root %q — refusing to run", dir, root)
	}

	// The real cause, produced the way production produces it, rather than a
	// hand-made sentinel that could not tell us whether the real one survives.
	_, readErr := config.ReadStateFile("wi_aihub428absent")
	if readErr == nil {
		t.Fatalf("precondition: reading %q succeeded", filepath.Join(config.StateDir(), "wi_aihub428absent.json"))
	}

	wrapped := config.StateFileMissingErr("wi_aihub428absent", readErr)

	if !errors.Is(wrapped, fs.ErrNotExist) {
		t.Errorf("errors.Is(err, fs.ErrNotExist) = false; the refusal must WRAP its cause with %%w, " +
			"not render it with %%v. Everything a caller uses to tell \"never claimed\" from " +
			"\"claimed but the file is corrupt\" is in the cause, and a %%v build looks identical " +
			"in every string assertion (aihub#428)")
	}
	if !errors.Is(wrapped, readErr) {
		t.Errorf("errors.Is(err, readErr) = false, want the exact cause reachable")
	}
}

// TestStateFileMissingErr_DoesNotAssertACauseItHasNotEstablished is the negative
// control on the WORDING.
//
// The tempting message is "wi X is not claimed". It is wrong, and wrong in the
// direction that misdiagnoses: config.ResolveStateFile also fails when the file
// is present but unparseable, and telling that operator their work item is
// unclaimed sends them to re-claim without ever learning their state file is
// corrupt. The advice happens to be right either way; the DIAGNOSIS is not this
// function's to make. This arm exists so a later "clearer" rewording has to
// argue with a test rather than with a comment.
func TestStateFileMissingErr_DoesNotAssertACauseItHasNotEstablished(t *testing.T) {
	msg := config.StateFileMissingErr("aihub#428", errors.New("parse state file for aihub#428: unexpected end of JSON input")).Error()

	for _, forbidden := range []string{"is not claimed", "was never claimed", "has not been claimed"} {
		if strings.Contains(msg, forbidden) {
			t.Errorf("message = %q contains %q — but this error is also returned for a state file "+
				"that EXISTS and fails to parse, so that phrasing states a cause the function has "+
				"not established (aihub#428)", msg, forbidden)
		}
	}
}
