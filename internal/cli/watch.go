package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"syscall"
	"time"

	"github.com/GMISWE/ieops-aihub/internal/drain"
)

// WatchUsage is `polyforge watch`'s help text.
const WatchUsage = `usage: polyforge watch [options]

Show what a running ` + "`polyforge drain`" + ` is doing. Reads only that run's local
state file: ZERO network, so it cannot hang when aihub is slow.

Options:
  --run=<id>     Which run to show (default: the most recent).
  --list         List the runs on this machine and exit.
  --follow, -f   Redraw until the run finishes.
  --interval=<d> Redraw interval for --follow (default 2s).
  --json         Print the raw snapshot.

This is the PROCESS's view, not the project's: one run, on this machine. For the
repository's view of the queue, use /pf-status.`

// RunWatch implements `polyforge watch`.
//
// It never constructs an aihub client and never takes a context deadline for I/O, because it
// performs no I/O beyond reading one JSON file. aihub#640 `watch_is_light_because_of_datasource`
// makes that the defining property: "/pf-status 要打 aihub API（仓库视角）；pf watch 只读 drain
// 自己写的本地状态快照（进程视角）…… 零网络，永不因服务端卡顿而卡". The moment you most want to know
// what the scheduler is doing is the moment the server is misbehaving, so an observer that shares
// the server's failure modes is an observer that is absent exactly when it is needed.
func RunWatch(ctx context.Context, args []string) {
	var (
		runID    string
		follow   bool
		asJSON   bool
		list     bool
		interval = 2 * time.Second
	)
	for _, a := range args {
		switch {
		case hasPrefix(a, "--run="):
			runID = a[len("--run="):]
		case a == "--follow", a == "-f":
			follow = true
		case a == "--json":
			asJSON = true
		case a == "--list":
			list = true
		case hasPrefix(a, "--interval="):
			d, err := time.ParseDuration(a[len("--interval="):])
			if err != nil || d <= 0 {
				fmt.Fprintf(os.Stderr, "watch: --interval: %q is not a positive duration\n", a[len("--interval="):])
				os.Exit(1)
			}
			interval = d
		case a == "--help", a == "-h":
			fmt.Println(WatchUsage)
			return
		default:
			fmt.Fprintf(os.Stderr, "watch: unknown flag %q\n\n%s\n", a, WatchUsage)
			os.Exit(1)
		}
	}

	home, err := polyforgeHome()
	if err != nil {
		fmt.Fprintf(os.Stderr, "watch: %v\n", err)
		os.Exit(2)
	}

	if list {
		runs := drain.ListRuns(home)
		if len(runs) == 0 {
			fmt.Println("no drain runs recorded on this machine")
			return
		}
		latest := drain.ReadLatest(home)
		for _, id := range runs {
			marker := "  "
			if id == latest {
				marker = "* "
			}
			line := marker + id
			if s, err := drain.ReadSnapshot(drain.RunDir(home, id)); err == nil {
				line += fmt.Sprintf("  %-10s %s", s.Project, runStateLabel(s))
			}
			fmt.Println(line)
		}
		return
	}

	if runID == "" {
		runID = drain.ReadLatest(home)
		if runID == "" {
			fmt.Fprintln(os.Stderr, "watch: no drain run recorded on this machine yet. "+
				"Start one with `polyforge drain --project=<name>`.")
			os.Exit(1)
		}
	}
	dir := drain.RunDir(home, runID)

	for {
		s, err := drain.ReadSnapshot(dir)
		if err != nil {
			// Name the run and the reason. A stale id in the `latest` pointer is the common
			// case, and "no such file or directory" naming a path the user never typed reads
			// like a bug in watch rather than a run that has been cleaned up.
			fmt.Fprintf(os.Stderr, "watch: cannot read run %s (%v)\n", runID, err)
			os.Exit(1)
		}
		if asJSON {
			b, _ := json.MarshalIndent(s, "", "  ")
			fmt.Println(string(b))
			return
		}
		if follow {
			// Clear and home the cursor, so a redraw replaces the frame instead of
			// scrolling. Plain ANSI, no dependency, and harmless in a pipe.
			fmt.Print("\033[H\033[2J")
		}
		printSnapshot(s, dir)
		if !follow || s.Finished {
			return
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(interval):
		}
	}
}

func printSnapshot(s *drain.Snapshot, dir string) {
	if s.Version != drain.SnapshotVersion {
		// An unattended run can outlive the binary that started it, so a version mismatch is
		// a real scenario rather than a theoretical one. Say so instead of rendering fields
		// whose meaning may have changed underneath.
		fmt.Printf("warning: snapshot version %d, this binary understands %d; fields below may be "+
			"stale or mean something different.\n", s.Version, drain.SnapshotVersion)
	}

	fmt.Printf("drain %s  project=%s  scope=%s  channel=%s\n",
		s.RunID, s.Project, snapshotScope(s), s.Channel)
	fmt.Printf("  %s   round %d   started %s\n", runStateLabel(s), s.Round, s.StartedAt)
	fmt.Printf("  wrapped=%d failed=%d lock-blocked=%d paused=%d claim-failed=%d created=%d\n",
		s.Totals.Wrapped, s.Totals.Failed, s.Totals.LockBlocked, s.Totals.Paused,
		s.Totals.ClaimFailed, s.Totals.Created)
	fmt.Printf("  queue: executable=%d blocked-by-mine=%d blocked-by-others=%d running=%d paused=%d\n",
		s.Queue.Executable, s.Queue.BlockedByMine, s.Queue.BlockedByOthers,
		s.Queue.Running, s.Queue.Paused)

	for _, v := range s.PreflightRejected {
		fmt.Printf("  channel %s unavailable: %s\n", v.Channel, v.Reason)
	}

	if len(s.Active) > 0 {
		fmt.Println("\n  in flight:")
		for _, a := range s.Active {
			fmt.Printf("    %-14s step %d/%d %-18s %-9s %-10s %s\n",
				a.Candidate.Slug, a.StepIndex, a.StepCount, a.StepID, a.Role,
				a.Channel, since(a.StepStarted))
		}
	}

	if n := len(s.Recent); n > 0 {
		fmt.Println("\n  finished:")
		start := 0
		if n > 10 {
			start = n - 10
		}
		for _, o := range s.Recent[start:] {
			line := fmt.Sprintf("    %-14s %-12s %d step(s)", o.Candidate.Slug, o.Result, o.Steps)
			if o.Blocker != nil {
				// "Who is blocking me" is server-only knowledge, resolved by drain when it
				// hit the block and stored in the snapshot so watch can show it without a
				// network call of its own.
				line += "  blocked by " + blockerLabel(o.Blocker)
			}
			if o.Err != "" {
				line += ": " + truncate(o.Err, 70)
			}
			fmt.Println(line)
		}
	}

	if s.Finished {
		fmt.Printf("\n  ended: %s (stopped: %s, exit %d)\n", s.Terminal, s.StopReason, s.ExitCode)
		if s.StopReason == drain.StopDivergence {
			fmt.Println("  DIVERGENCE: the round created at least as many work items as it " +
				"completed. The queue is growing; look before draining again.")
		}
	}
	fmt.Printf("\n  step output: %s\n", dir)
	fmt.Println("  (this machine, this run, not the project's state; use /pf-status for that)")
}

func blockerLabel(b *drain.Blocker) string {
	switch {
	case b.WorkItem != "" && b.Actor != "":
		return fmt.Sprintf("%s (%s)", b.WorkItem, b.Actor)
	case b.WorkItem != "":
		return b.WorkItem
	case b.Actor != "":
		return b.Actor
	default:
		return "another attempt"
	}
}

func snapshotScope(s *drain.Snapshot) string {
	if s.ScopeAll {
		return "--all"
	}
	if s.ScopeUserID != "" {
		return "mine(" + s.ScopeUserID + ")"
	}
	return "mine"
}

// runStateLabel describes whether the run is going, done, or dead.
//
// The dead case is why this is not a boolean. An unattended scheduler can be OOM-killed or lose
// its terminal, and the file it leaves behind is byte-identical to the file of a run that is
// still working — Finished is false in both. The pid is the only thing that separates them, and
// on a container with no supervisor nothing else will ever notice.
func runStateLabel(s *drain.Snapshot) string {
	if s.Finished {
		return fmt.Sprintf("finished: %s", s.Terminal)
	}
	if s.PID > 0 && !processAlive(s.PID) {
		return "DIED (process gone, run never finished)"
	}
	return "running"
}

// processAlive reports whether pid exists. Signal 0 performs the permission and existence checks
// without delivering anything, which is the portable way to ask.
func processAlive(pid int) bool {
	p, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	return p.Signal(syscall.Signal(0)) == nil
}

func since(ts string) string {
	t, err := time.Parse(time.RFC3339, ts)
	if err != nil {
		return ""
	}
	d := time.Since(t).Truncate(time.Second)
	if d < 0 {
		return "0s"
	}
	return d.String()
}

func hasPrefix(s, p string) bool { return len(s) >= len(p) && s[:len(p)] == p }
