package drain

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// TestWriteSnapshot_IsAtomicUnderAConcurrentReader pins the property `polyforge watch` depends
// on. watch re-reads this file on an interval while drain rewrites it continuously, so a plain
// truncating write hands the reader a half-written file on an unlucky interleave — which parses
// as a JSON syntax error and reads to a human as "drain has corrupted its state", at exactly the
// moment they went looking for reassurance.
//
// Mutant watched: replacing the write-temp-then-rename with a direct os.WriteFile to
// snapshot.json makes this fail with a JSON unmarshal error within a few hundred iterations.
func TestWriteSnapshot_IsAtomicUnderAConcurrentReader(t *testing.T) {
	dir := t.TempDir()
	s := &Snapshot{RunID: "r1", Project: "p", Recent: []Outcome{}}
	if err := WriteSnapshot(dir, s); err != nil {
		t.Fatalf("seed write: %v", err)
	}

	stop := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < 400; i++ {
			s.Totals.Wrapped = i
			// A growing body makes a torn read far more likely than a fixed-size one.
			s.Recent = append(s.Recent, Outcome{
				Candidate: Candidate{ID: "w", Slug: "w", Goal: strings.Repeat("x", 64)},
				Result:    ResultWrapped,
			})
			if err := WriteSnapshot(dir, s); err != nil {
				t.Errorf("write %d: %v", i, err)
				break
			}
		}
		close(stop)
	}()

	reads := 0
	for {
		select {
		case <-stop:
			wg.Wait()
			if reads == 0 {
				t.Fatal("the reader never got a read in; this proves nothing")
			}
			return
		default:
		}
		got, err := ReadSnapshot(dir)
		if err != nil {
			if os.IsNotExist(err) {
				// A rename can never make the destination briefly absent, but a reader
				// that starts before the first write legitimately sees nothing.
				continue
			}
			t.Fatalf("torn read after %d clean reads: %v", reads, err)
		}
		if got.RunID != "r1" {
			t.Fatalf("read a snapshot with run id %q", got.RunID)
		}
		reads++
	}
}

// TestWriteSnapshot_NeverEmitsNullLists guards watch's rendering. A nil slice marshals to `null`,
// which is a third spelling of "nothing" alongside an absent key and an empty list — the same
// defect aihub#449 had to fix on the ready queue's stale_running.
func TestWriteSnapshot_NeverEmitsNullLists(t *testing.T) {
	dir := t.TempDir()
	if err := WriteSnapshot(dir, &Snapshot{RunID: "r"}); err != nil {
		t.Fatalf("write: %v", err)
	}
	b, err := os.ReadFile(filepath.Join(dir, SnapshotFile))
	if err != nil {
		t.Fatal(err)
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(b, &raw); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"active", "recent"} {
		if string(raw[key]) == "null" {
			t.Errorf("%q marshalled as null; it must be []", key)
		}
	}
}

// TestWriteSnapshot_StampsVersionAndTime proves the two fields watch uses to decide whether it
// understands the file and whether the run is moving.
func TestWriteSnapshot_StampsVersionAndTime(t *testing.T) {
	dir := t.TempDir()
	s := &Snapshot{RunID: "r", Version: 0, UpdatedAt: ""}
	if err := WriteSnapshot(dir, s); err != nil {
		t.Fatal(err)
	}
	if s.Version != SnapshotVersion {
		t.Errorf("Version = %d, want %d: watch could not tell a stale schema from a current one",
			s.Version, SnapshotVersion)
	}
	if _, err := time.Parse(time.RFC3339, s.UpdatedAt); err != nil {
		t.Errorf("UpdatedAt %q is not RFC3339: %v", s.UpdatedAt, err)
	}
}

// TestNewRunID_SortsChronologically pins what ListRuns relies on: run directories are listed by
// name, so the name must order by time. The pid suffix disambiguates two runs in the same second
// without disturbing that.
func TestNewRunID_SortsChronologically(t *testing.T) {
	early := NewRunID(time.Date(2026, 9, 14, 1, 0, 0, 0, time.UTC), 999999)
	late := NewRunID(time.Date(2026, 9, 14, 2, 0, 0, 0, time.UTC), 1)
	if early >= late {
		t.Fatalf("%q does not sort before %q; ListRuns would report the wrong newest run", early, late)
	}
	a := NewRunID(time.Date(2026, 9, 14, 1, 0, 0, 0, time.UTC), 1)
	b := NewRunID(time.Date(2026, 9, 14, 1, 0, 0, 0, time.UTC), 2)
	if a == b {
		t.Fatal("two runs started in the same second collided on a run id")
	}
}

// TestLatestRoundTrips covers the `latest` pointer watch uses when given no --run.
func TestLatestRoundTrips(t *testing.T) {
	root := t.TempDir()
	if got := ReadLatest(root); got != "" {
		t.Fatalf("a fresh root reported latest run %q", got)
	}
	if err := WriteLatest(root, "20260914T010203Z-42"); err != nil {
		t.Fatal(err)
	}
	if got := ReadLatest(root); got != "20260914T010203Z-42" {
		t.Fatalf("ReadLatest = %q", got)
	}
}

// TestStepLogPath_KeepsEveryNameInsideTheRunDirectory is the security-shaped assertion in this
// file. Slugs and step ids are SERVER-supplied strings, and they are being turned into
// filesystem paths; a step id of "../../etc/cron.d/x" must not escape the run directory. aihub
// slugs already contain '#' today, so these are not hypothetical characters.
func TestStepLogPath_KeepsEveryNameInsideTheRunDirectory(t *testing.T) {
	runDir := "/tmp/run"
	cases := []struct{ slug, step string }{
		{"aihub#640", "code_change"},
		{"../../escape", "step"},
		{"ok", "../../../etc/passwd"},
		{"..", ".."},
		{".", "."},
		{"a/b", "c/d"},
		{"", ""},
	}
	for _, tc := range cases {
		p := StepLogPath(runDir, tc.slug, 1, tc.step)
		if !strings.HasPrefix(filepath.Clean(p), runDir+string(filepath.Separator)) {
			t.Errorf("StepLogPath(%q, %q) = %q, which escapes %s", tc.slug, tc.step, p, runDir)
		}
		// The real property is about path ELEMENTS, not substrings: a name like
		// "..-..-escape" contains ".." as a substring but is an ordinary directory name and
		// traverses nothing. What must never appear is an element that IS "." or "..".
		for _, elem := range strings.Split(p[len(runDir):], string(filepath.Separator)) {
			if elem == ".." || elem == "." {
				t.Errorf("StepLogPath(%q, %q) = %q contains the path element %q",
					tc.slug, tc.step, p, elem)
			}
		}
	}

	// The ordinary case must still be readable, not mangled beyond recognition: a plain `ls`
	// of the directory is how these get read.
	if got := StepLogPath("/tmp/run", "aihub#640", 3, "code_review"); got != "/tmp/run/aihub-640/03-code_review.log" {
		t.Errorf("StepLogPath = %q, want /tmp/run/aihub-640/03-code_review.log", got)
	}
	if WILogDir("/tmp/run", "aihub#640") != "/tmp/run/aihub-640" {
		t.Errorf("WILogDir = %q", WILogDir("/tmp/run", "aihub#640"))
	}
}

// TestTotalsAdd_CountsEachResultOnce is the negative control for the counters watch and the run
// report both print: each Result must land in exactly one bucket.
func TestTotalsAdd_CountsEachResultOnce(t *testing.T) {
	var tot Totals
	for _, r := range []Result{ResultWrapped, ResultFailed, ResultLockBlocked, ResultPaused, ResultClaimFailed} {
		tot.Add(r)
	}
	if tot != (Totals{Wrapped: 1, Failed: 1, LockBlocked: 1, Paused: 1, ClaimFailed: 1}) {
		t.Fatalf("totals = %+v, want one of each", tot)
	}
	// An unknown result must not be silently folded into a real bucket.
	before := tot
	tot.Add(Result("something-new"))
	if tot != before {
		t.Errorf("an unrecognised Result changed the totals: %+v -> %+v", before, tot)
	}
}

// TestListRuns_NewestFirst pins the order `polyforge watch --list` prints.
func TestListRuns_NewestFirst(t *testing.T) {
	root := t.TempDir()
	for _, id := range []string{"20260101T000000Z-1", "20260914T000000Z-2", "20260601T000000Z-3"} {
		if err := os.MkdirAll(RunDir(root, id), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	got := ListRuns(root)
	if len(got) != 3 || got[0] != "20260914T000000Z-2" || got[2] != "20260101T000000Z-1" {
		t.Fatalf("ListRuns = %v, want newest first", got)
	}
	if ListRuns(t.TempDir()) != nil {
		t.Error("a root with no runs returned a non-nil list")
	}
}

// TestWriteSnapshot_SurvivesConcurrentWriters is the regression test for a bug the
// single-writer test above could not see.
//
// The temp file used to be a FIXED path (`snapshot.json.tmp`), and Runner publishes from every
// worker goroutine in a round, so the writers raced each other rather than the reader:
// os.WriteFile truncates and then writes, a second goroutine renames that same path mid-write,
// and the reader gets a prefix. Measured with 8 publishers: 3 torn reads and 7
// "rename: no such file or directory" errors out of 400 writes — exactly the corruption
// WriteSnapshot's own doc comment promises not to produce, reintroduced one level down.
//
// Mutant watched: replacing os.CreateTemp with a fixed `SnapshotFile+".tmp"` path makes this
// fail with either a write error or a JSON syntax error.
func TestWriteSnapshot_SurvivesConcurrentWriters(t *testing.T) {
	dir := t.TempDir()
	const writers, each = 8, 40

	var wg sync.WaitGroup
	errs := make(chan error, writers*each)
	for w := 0; w < writers; w++ {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			for i := 0; i < each; i++ {
				s := &Snapshot{RunID: "r1", Project: "p", Totals: Totals{Wrapped: i}}
				// A body whose size varies makes a torn read far likelier.
				for k := 0; k <= i; k++ {
					s.Recent = append(s.Recent, Outcome{
						Candidate: Candidate{ID: "w", Slug: "w", Goal: strings.Repeat("x", 48)},
						Result:    ResultWrapped,
					})
				}
				if err := WriteSnapshot(dir, s); err != nil {
					errs <- err
					return
				}
			}
		}(w)
	}

	readsDone := make(chan struct{})
	var torn int
	go func() {
		defer close(readsDone)
		for i := 0; i < 2000; i++ {
			if _, err := ReadSnapshot(dir); err != nil && !os.IsNotExist(err) {
				torn++
			}
		}
	}()

	wg.Wait()
	<-readsDone
	close(errs)
	for err := range errs {
		t.Errorf("WriteSnapshot failed under concurrent writers: %v", err)
	}
	if torn > 0 {
		t.Errorf("watch would have seen %d corrupt snapshots", torn)
	}

	// No temp files may be left behind: the run directory is what an operator lists.
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".tmp") {
			t.Errorf("left a temp file behind: %s", e.Name())
		}
	}

	// And the survivor must be readable and complete.
	got, err := ReadSnapshot(dir)
	if err != nil {
		t.Fatalf("final read: %v", err)
	}
	if got.RunID != "r1" {
		t.Errorf("final snapshot run id = %q", got.RunID)
	}
}
