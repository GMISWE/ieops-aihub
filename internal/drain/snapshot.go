package drain

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"time"
)

// SnapshotVersion is bumped whenever Snapshot's shape changes incompatibly, so a `polyforge
// watch` from a different binary says so instead of rendering a half-understood file. An
// unattended scheduler and its observer are upgraded at different moments by construction —
// drain may have been running for hours when the binary on disk is replaced — so the two really
// do meet across versions, and silence there would mean watch confidently reporting stale field
// semantics.
const SnapshotVersion = 1

// SnapshotFile is the snapshot's filename inside a run directory.
const SnapshotFile = "snapshot.json"

// Snapshot is the entire data source for `polyforge watch`.
//
// aihub#640 `watch_is_light_because_of_datasource` is explicit that watch is light because of
// WHERE it reads, not how much it prints: `/pf-status` calls the aihub API and shows the
// repository's view; `polyforge watch` reads only this file and shows the PROCESS's view, with
// ZERO network, so it can never hang because the server is slow — which is exactly when you
// most want to know what the scheduler is doing.
//
// That constraint decides the field list. Anything watch shows must be written here by drain at
// the moment drain learned it. The one piece that is otherwise server-only — "who is blocking
// me" — is resolved by drain when it hits the block and stored in Blocker, because drain has to
// make that query anyway and the ruling puts the cost on the side that already pays it.
//
// Scope caveat, and watch prints it: this is one process's record of one run on one machine. It
// is not the project's state. A second person draining the same project appears here only
// through the blocks they caused.
type Snapshot struct {
	Version int    `json:"version"`
	RunID   string `json:"run_id"`
	Project string `json:"project"`
	PID     int    `json:"pid"`

	StartedAt string `json:"started_at"`
	UpdatedAt string `json:"updated_at"`

	// Scope records how "my work items" was resolved, so a reader can tell a COMPLETED that
	// means "my queue is empty" from one that means "the project is empty".
	ScopeAll    bool   `json:"scope_all"`
	ScopeUserID string `json:"scope_user_id,omitempty"`

	// Channel is the harness/model route this run settled on at preflight.
	Channel Channel `json:"channel"`
	// PreflightRejected records the candidates that failed preflight and why. Kept rather than
	// discarded: "drain is using pi" is a much less useful thing to read at 3am than "drain is
	// using pi because claude's credential expired".
	PreflightRejected []PreflightVerdict `json:"preflight_rejected,omitempty"`

	Round int `json:"round"`

	// Active is what is running right now, keyed by work item id.
	Active []ActiveWI `json:"active"`
	// Recent is the finished outcomes of this run, newest last.
	Recent []Outcome `json:"recent"`

	Totals Totals `json:"totals"`
	// Queue is the last observed queue state, refreshed at each round boundary.
	Queue QueueState `json:"queue"`

	// Finished, Terminal and StopReason are written once, when the run ends. Their absence is
	// how watch knows the run is still going — and a `Finished:false` snapshot whose PID is
	// gone is how it knows the run DIED rather than ended, which is a distinction an
	// unattended scheduler has to be able to make.
	Finished   bool       `json:"finished"`
	Terminal   Terminal   `json:"terminal,omitempty"`
	StopReason StopReason `json:"stop_reason,omitempty"`
	ExitCode   int        `json:"exit_code"`
}

// ActiveWI is one work item currently executing, with enough detail to answer "what is it doing
// and has it been doing it for too long".
type ActiveWI struct {
	Candidate   Candidate `json:"candidate"`
	StepID      string    `json:"step_id"`
	StepIndex   int       `json:"step_index"`
	StepCount   int       `json:"step_count"`
	Role        string    `json:"role,omitempty"`
	Channel     Channel   `json:"channel"`
	StepStarted string    `json:"step_started_at"`
	LogDir      string    `json:"log_dir,omitempty"`
}

// Totals are the run's cumulative counters.
type Totals struct {
	Wrapped     int `json:"wrapped"`
	Failed      int `json:"failed"`
	LockBlocked int `json:"lock_blocked"`
	Paused      int `json:"paused"`
	ClaimFailed int `json:"claim_failed"`
	Cancelled   int `json:"cancelled"`
	Created     int `json:"created"`
	Rounds      int `json:"rounds"`
}

// Add folds one outcome into the totals.
func (t *Totals) Add(r Result) {
	switch r {
	case ResultWrapped:
		t.Wrapped++
	case ResultFailed:
		t.Failed++
	case ResultLockBlocked:
		t.LockBlocked++
	case ResultPaused:
		t.Paused++
	case ResultClaimFailed:
		t.ClaimFailed++
	case ResultCancelled:
		t.Cancelled++
	}
}

// RunDir is the directory holding one run's snapshot and per-step logs:
// <root>/drain/<run-id>/. Callers pass the polyforge home (~/.polyforge) as root.
func RunDir(root, runID string) string { return filepath.Join(root, "drain", runID) }

// LatestLink is the path of the pointer file naming the most recent run, which is how `polyforge
// watch` with no --run finds one.
//
// It is a small FILE containing the run id, not a symlink, on purpose: a symlink to a directory
// that has since been deleted resolves to an error that reads like a bug ("no such file or
// directory" naming a path the user never typed), whereas a stale id in a file lets watch say
// "run <id> is gone" and name it.
func LatestLink(root string) string { return filepath.Join(root, "drain", "latest") }

// WriteSnapshot atomically replaces the run's snapshot file.
//
// Atomic because watch reads it concurrently and on an unlucky interleave a plain truncating
// write hands the reader a half-written file, which parses as a JSON syntax error and reads to a
// human as "drain has corrupted its state" at exactly the moment they went looking for
// reassurance. Rename within one directory is atomic on every platform this ships to.
//
// The temp file gets a UNIQUE name, and that is not tidiness. Every worker in a round publishes,
// so several goroutines call this concurrently; with one shared `snapshot.json.tmp` the writers
// race each other rather than the reader. Measured with 8 publishers: os.WriteFile truncates
// and then writes, a second goroutine renames that same path mid-write, and the reader gets a
// prefix — 3 torn reads and 7 "rename: no such file or directory" errors out of 400 writes. That
// is precisely the corruption the paragraph above promises not to produce, reintroduced one
// level down. os.CreateTemp gives each writer its own file, so the only shared step is the
// rename, which is the atomic one.
func WriteSnapshot(dir string, s *Snapshot) error {
	s.Version = SnapshotVersion
	s.UpdatedAt = time.Now().UTC().Format(time.RFC3339)
	if s.Active == nil {
		s.Active = []ActiveWI{}
	}
	if s.Recent == nil {
		s.Recent = []Outcome{}
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("create run dir %s: %w", dir, err)
	}
	b, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal snapshot: %w", err)
	}
	f, err := os.CreateTemp(dir, SnapshotFile+".*.tmp")
	if err != nil {
		return fmt.Errorf("create temp snapshot in %s: %w", dir, err)
	}
	tmp := f.Name()
	if _, err := f.Write(b); err != nil {
		_ = f.Close()
		_ = os.Remove(tmp)
		return fmt.Errorf("write %s: %w", tmp, err)
	}
	if err := f.Close(); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("close %s: %w", tmp, err)
	}
	// CreateTemp makes the file 0600; the snapshot is meant to be readable by whoever runs
	// `polyforge watch`, which need not be the same uid in a shared container.
	if err := os.Chmod(tmp, 0o644); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("chmod %s: %w", tmp, err)
	}
	final := filepath.Join(dir, SnapshotFile)
	if err := os.Rename(tmp, final); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("rename %s -> %s: %w", tmp, final, err)
	}
	return nil
}

// ReadSnapshot loads a run's snapshot.
func ReadSnapshot(dir string) (*Snapshot, error) {
	b, err := os.ReadFile(filepath.Join(dir, SnapshotFile))
	if err != nil {
		return nil, err
	}
	var s Snapshot
	if err := json.Unmarshal(b, &s); err != nil {
		return nil, fmt.Errorf("parse %s: %w", filepath.Join(dir, SnapshotFile), err)
	}
	return &s, nil
}

// WriteLatest records runID as the most recent run.
func WriteLatest(root, runID string) error {
	p := LatestLink(root)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return err
	}
	return os.WriteFile(p, []byte(runID+"\n"), 0o644)
}

// ReadLatest returns the most recent run id, or "" when no run has been recorded.
func ReadLatest(root string) string {
	b, err := os.ReadFile(LatestLink(root))
	if err != nil {
		return ""
	}
	return trimLine(string(b))
}

// ListRuns returns every run id under root, newest first by directory name. Run ids are
// timestamp-prefixed (NewRunID), so lexical order is chronological order.
func ListRuns(root string) []string {
	entries, err := os.ReadDir(filepath.Join(root, "drain"))
	if err != nil {
		return nil
	}
	var out []string
	for _, e := range entries {
		if e.IsDir() {
			out = append(out, e.Name())
		}
	}
	sort.Sort(sort.Reverse(sort.StringSlice(out)))
	return out
}

// NewRunID builds a run id from a timestamp and the pid. The timestamp leads so that lexical
// ordering of run directories is chronological (ListRuns relies on it); the pid disambiguates
// two runs started in the same second.
func NewRunID(now time.Time, pid int) string {
	return fmt.Sprintf("%s-%d", now.UTC().Format("20060102T150405Z"), pid)
}

func trimLine(s string) string {
	for len(s) > 0 && (s[len(s)-1] == '\n' || s[len(s)-1] == '\r' || s[len(s)-1] == ' ') {
		s = s[:len(s)-1]
	}
	return s
}

// WILogDir is the directory holding one work item's per-step logs: <run-dir>/<wi-slug>/.
func WILogDir(runDir, slug string) string { return filepath.Join(runDir, safeName(slug)) }

// StepLogPath is where one step's combined output is written:
// <run-dir>/<wi-slug>/NN-<step-id>.log.
//
// This is the concrete answer to `three_ops_problems` ③ ("step 输出不进任何 LLM 上下文 …… 所以必须
// 落盘或落 aihub artifact，否则出问题无从查起"). Disk, not an aihub artifact, for three reasons,
// and the wi leaves the choice to implementation:
//
//  1. The failures most worth reading are the ones where the server or the credential is the
//     problem. An artifact write needs a working server and a valid API key, so it is least
//     available exactly when it is most needed.
//  2. `watch` is specified as zero-network. Logs it cannot reach are logs it cannot offer.
//  3. Step output is unbounded — a full test run, a lint sweep — and artifacts are a curated
//     record meant to be read by people and embedded for recall. Dumping build logs into that
//     store would degrade it.
//
// The step index prefix keeps the directory in execution order for a plain `ls`, which is how
// this will actually be read.
func StepLogPath(runDir, slug string, index int, stepID string) string {
	return filepath.Join(runDir, safeName(slug), fmt.Sprintf("%02d-%s.log", index, safeName(stepID)))
}

// safeName makes a slug or step id safe as a single path element. aihub slugs contain '#'
// (aihub#640) and '/' appears in neither today, but a scheduler that builds paths from
// server-supplied strings should not be the reason a future id format escapes its directory.
func safeName(s string) string {
	if s == "" {
		return "_"
	}
	out := make([]rune, 0, len(s))
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
			out = append(out, r)
		case r == '-', r == '_', r == '.':
			out = append(out, r)
		default:
			out = append(out, '-')
		}
	}
	// A name of all dots would still be "." or ".."; neither survives this loop as such
	// because the first branch never fires and every dot is kept, so guard it explicitly.
	s2 := string(out)
	if s2 == "." || s2 == ".." {
		return "_"
	}
	return s2
}
