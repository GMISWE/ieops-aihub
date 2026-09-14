package lifecycle

import (
	"os/exec"
	"strings"
)

// claimBranchULID8 derives the 8-char branch suffix (polyforge/<ulid8>) from a
// work item's canonical id. Callers must pass the canonical wi id (wi_<ulid>),
// not a raw slug such as "aihub#225": for a slug the "wi_" prefix is absent, so
// the last-8-chars slice would leak slug characters into the branch name (e.g.
// "ihub#225"). Returns "" when the canonical id is shorter than 8 chars, which
// the caller treats as "skip worktree creation". (aihub#225)
func claimBranchULID8(canonicalWIID string) string {
	bare := strings.TrimPrefix(canonicalWIID, "wi_")
	if len(bare) < 8 {
		return ""
	}
	return bare[len(bare)-8:]
}

// ---------------------------------------------------------------------------
// Readable task branch names (aihub#322)
// ---------------------------------------------------------------------------
//
// Until aihub#322 the claim branch was polyforge/<ulid8> — eight random chars
// carrying no information at all. `git branch -r` on ieops-ctlchain showed 58 of
// them, 44 unmerged, and nothing in the name says which work item any of them
// belongs to or whether it is abandoned. The name is COMPUTED at claim time and
// stored nowhere, so it can be changed without a migration; existing branches
// keep the names they were created with, which is why resume must still know the
// old shape (see resolveClaimBranch).
//
// Shape: polyforge/<project>-<seq>-<kebab goal>, e.g.
// polyforge/aihub-322-readable-task-branch-names.
//
// WHY <project> IS IN THERE, given the hand-made precedents in ieops-datachain
// are seq-only (polyforge/528-stagesconfig-wiring): <seq> is unique per PROJECT,
// not per repo. config.Config is map[project]Project and each Project carries its
// own []Repo with no cross-project uniqueness constraint anywhere in Load(), so
// one repo may legally be listed under two projects — at which point two work
// items, aihub#42 and ieops#42, resolve to the same branch in the same clone.
// The fresh-claim path treats "branch already exists" as "attach to it", so the
// collision would not error; it would silently put two work items on one branch.
// (The live workspace has 28 repos across 7 projects and currently no such
// sharing — this guards the structure, not an observed instance.) A seq-only
// scheme would also collide most easily in its DEGRADED form, where the goal
// contributes nothing and the whole name is just polyforge/<seq>. The worktree
// DIRECTORY already spells pf.<project>-<seq> for exactly this reason, so a
// branch that matches it is the consistent choice as well as the safe one.
const (
	claimBranchPrefix = "polyforge/"
	// Total ref length cap, prefix included. Git imposes no limit of its own; the
	// filesystem does (loose refs are files), and a name nobody can read on a
	// `git branch` line has defeated the point of the change.
	claimBranchMaxTotal = 72
	claimBranchProjMax  = 24
	claimBranchSeqMax   = 16
	claimBranchDescMax  = 40
	// Below this a truncated description is noise rather than a hint, so drop it
	// and keep the bare polyforge/<project>-<seq>.
	claimBranchMinDesc = 4
)

// kebabToken reduces free-form text to lowercase [a-z0-9-], collapsing every run
// of rejected characters to a single "-" and trimming the ends. maxLen <= 0 means
// no cap; otherwise the result is cut back to maxLen and, when that lands
// mid-word, back again to the last "-" so the tail is a whole word.
//
// Everything outside [a-z0-9] is rejected, not transliterated — goals in this
// repo are routinely Chinese and routinely contain "#", "/", ":", backticks,
// quotes and emoji. That is deliberately lossy: the result is a hint, and a hint
// that is always a legal git ref beats a faithful one that sometimes is not. The
// two ref rules that bite here — a path component may not contain ".." and may
// not end in ".lock" — are unreachable by construction, because "." is not in the
// accepted set at all.
func kebabToken(s string, maxLen int) string {
	var b strings.Builder
	b.Grow(len(s))
	pendingDash := false
	for _, r := range strings.ToLower(s) {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			if pendingDash && b.Len() > 0 {
				b.WriteByte('-')
			}
			pendingDash = false
			b.WriteRune(r)
			continue
		}
		pendingDash = true
	}
	out := b.String()
	if maxLen > 0 && len(out) > maxLen {
		out = out[:maxLen]
		if i := strings.LastIndex(out, "-"); i > 0 {
			out = out[:i]
		}
		out = strings.TrimRight(out, "-")
	}
	return out
}

// claimBranchNames are the three names the worktree code needs for one claim.
// Only Branch is used to CREATE; the other two exist so a resume can recognise a
// branch an earlier claim created under a different name.
type claimBranchNames struct {
	// Branch is what a claim made today uses:
	// polyforge/<project>-<seq>-<desc>, degrading to polyforge/<project>-<seq>
	// when the goal reduces to nothing, then to polyforge/<ulid8> when project
	// and seq both do, then to "" when there is no ulid8 either — which the
	// caller already treats as "skip worktree creation".
	Branch string
	// Legacy is the pre-aihub#322 name, polyforge/<ulid8>. Empty when no ulid8.
	Legacy string
	// Stem is polyforge/<project>-<seq>. It has TWO uses in resolveClaimBranch:
	// as an exact candidate in its own right (it is a name this scheme really
	// produces — degradation row 2, "the goal reduced to nothing"), and as the
	// prefix of the glob <Stem>-*. Those are different lookups with different
	// hazards, and only the second one is a set.
	//
	// ⚠️ It is populated only when BOTH components survived kebabToken, and that
	// is a correctness requirement, not tidiness. A stem is a claim that the
	// string identifies ONE work item; drop either component and the GLOB
	// identifies a SET. With no seq, "polyforge/aihub-*" matches every branch in
	// the project and a claim silently attaches to somebody else's work item
	// (reproduced: it landed on polyforge/aihub-999-someone-elses-work-item).
	// With no project, "polyforge/528-*" matches the hand-made
	// polyforge/528-stagesconfig-wiring that really exists in ieops-datachain.
	// The invariant is enforced HERE, where the stem is built, rather than left
	// to every use site to remember. Branch still degrades to whichever component
	// survived — that is a NAME, matched exactly, and an exact match cannot
	// over-match.
	Stem string
}

// newClaimBranchNames derives all three names. It never returns a Branch that is
// not a legal git ref: everything outside [a-z0-9-] is dropped by kebabToken, so
// the two ref rules that would otherwise bite ("..", a ".lock" suffix) are
// unreachable, and each degradation step is itself a legal ref.
func newClaimBranchNames(project, seq, goal, ulid8 string) claimBranchNames {
	n := claimBranchNames{}
	if ulid8 != "" {
		n.Legacy = claimBranchPrefix + ulid8
	}

	proj := kebabToken(project, claimBranchProjMax)
	sq := kebabToken(seq, claimBranchSeqMax)
	if proj != "" && sq != "" {
		n.Stem = claimBranchPrefix + proj + "-" + sq
	}

	base := strings.Trim(proj+"-"+sq, "-")
	if base == "" {
		n.Branch = n.Legacy
		return n
	}
	base = claimBranchPrefix + base

	budget := claimBranchMaxTotal - len(base) - 1 // -1 for the joining "-"
	if budget > claimBranchDescMax {
		budget = claimBranchDescMax
	}
	n.Branch = base
	if budget >= claimBranchMinDesc {
		if desc := kebabToken(goal, budget); desc != "" {
			n.Branch = base + "-" + desc
		}
	}
	return n
}

// gitRefExists reports whether ref (a full ref path such as
// "refs/heads/polyforge/x") resolves in the repo at srcPath.
func gitRefExists(srcPath, ref string) bool {
	return exec.Command("git", "-C", srcPath, "show-ref", "--verify", "--quiet", ref).Run() == nil
}

// gitUniqueBranchMatch returns the single branch under refPrefix whose name
// matches pattern, or "". Anything but exactly one match returns "": zero means
// nothing to attach to, and two or more mean picking one would be a guess.
//
// Split on "\n" and not strings.Fields: Fields splits on unicode.IsSpace, which
// includes U+00A0, and git permits a UTF-8 NBSP inside a refname while
// forbidding every ASCII control character and the ASCII space. One such ref
// would be miscounted as two matches. Line-splitting is exact for
// --format=%(refname), which emits one ref per line and cannot emit a refname
// containing one.
func gitUniqueBranchMatch(srcPath, refPrefix, pattern string) string {
	out, err := exec.Command("git", "-C", srcPath, "for-each-ref",
		"--format=%(refname)", refPrefix+pattern).Output()
	if err != nil {
		return ""
	}
	matches := []string{}
	for _, line := range strings.Split(string(out), "\n") {
		if line = strings.TrimRight(line, "\r"); line != "" {
			matches = append(matches, line)
		}
	}
	if len(matches) != 1 {
		return ""
	}
	return strings.TrimPrefix(matches[0], refPrefix)
}

// resolveClaimBranch finds the branch this claim should attach to, or "" when
// there is none — which the caller turns into "create it".
//
// It deliberately does NOT report where the branch was found. An earlier version
// returned a fromRemote bool alongside the name, and that flag could be stale by
// the time it was used: with the local glob ambiguous (two matches, correctly
// declined) but the remote glob unique, the glob tier returned fromRemote=true for a
// branch that also existed locally, `worktree add -b` failed with "already
// exists", and the repo got NO worktree at all. Whether a local head exists is
// now decided inside attachWorktree, immediately before the command that cares,
// from the only authority on the question.
//
// ⚠️ It runs on EVERY claim, fresh and resume alike, and the mode is
// deliberately not an input. The pre-aihub#322 code was NAME-STABLE — every
// claim of a work item computed the same polyforge/<ulid8> — so on a fresh
// claim `worktree add -b` failed with "already exists" and the fallback
// attached to the existing work. That attach-if-exists behaviour was load
// bearing on both paths, and the claims that most need it declare "fresh":
// force_takeover (Mode D) is by definition applied to a work item another agent
// already has a branch for, `/pf-work <slug>` without --resume sends "fresh",
// and `mode` is optional, so an omitted one arrives as "". Gating the lookup on
// mode=="resume" therefore let those claims compute a name no pre-1.1.18 work
// item has, succeed at `-b`, and land on a virgin branch off origin/main while
// the real work sat on the legacy branch. Deciding from what EXISTS rather than
// from what the caller called the claim removes the whole class.
//
// EXACT names are tried first and exhaustively, then the one glob. An exact
// name cannot over-match, so there is no reason to reach the heuristic while an
// exact candidate is still untried.
//
//  1. n.Branch — the name this claim would compute today.
//  2. n.Legacy — polyforge/<ulid8>, the pre-aihub#322 name.
//  3. n.Stem   — the bare polyforge/<project>-<seq>.
//  4. a unique n.Stem+"-*" match.
//
// Why n.Stem is an EXACT candidate and not only a glob prefix: the bare stem is
// a name this scheme really produces — degradation row 2, "the goal reduces to
// nothing", and goals here are routinely Chinese, so it is common rather than
// exotic. The glob "<Stem>-*" cannot match the bare "<Stem>": add any latin word
// to such a work item's goal and tier 1 misses the new name, tier 4 misses the
// old one, and the claim silently starts over on origin/main with the previous
// commits abandoned. The mirror direction (desc → bare stem) always worked,
// which is why only this one direction was broken and nothing noticed.
//
// ⚠️ WHY LEGACY OUTRANKS STEM. An earlier version had these the other way round,
// on the reasoning that "a bare stem can only have been created by a post-322
// claim, so it is the more recent". THAT REASONING IS FALSE, and was measured to
// be false across all 45 repos in .repo/: eleven bare-stem-shaped branches
// exist, and every one PREDATES this scheme — polyforge/aihub-21 (2026-05-23),
// -47 (05-25), -58 (05-26), -29, -55, polyforge/ieops-210, -390, -549, -577 —
// while the commit that introduced this naming is dated 2026-09-01 and plugin
// 1.1.18 is unreleased. The May-era code named branches polyforge/<ulid8>.
//
// There is also a second, still-live producer that has nothing to do with this
// function: declared_resources[].task_branch is human-settable, and ieops#549
// and ieops#577 carry exactly "polyforge/ieops-549" / "polyforge/ieops-577" in
// it. So a stem-shaped branch may be FOREIGN to the claim that finds it, whereas
// polyforge/<ulid8> can only ever have been produced by this system for this
// work item. The safer candidate goes first.
//
// Inverting costs nothing, which is what makes it free to be careful: when a
// bare stem IS legitimately this claim's, the goal reduced to nothing, so
// Branch == Stem and tier 1 already returns it. Tier 3 is reached DISTINCTLY
// only when the goal has since gained latin text — exactly the case where a
// stem-shaped branch is more likely to be the old or foreign one.
//
// The rest of the order: Branch first because it is the most specific name and
// the one a healthy claim wants. Duplicates are skipped rather than probed twice
// (the Branch == Stem case above). The glob stays last, after every exact
// candidate, and runs only when Stem carries both components — see the field
// comment for why a half stem globs a set rather than an identity.
//
// Each candidate is looked for locally first and then as origin/<name>: a local
// head deleted while the remote branch survives (a cleanup pass, a fresh clone)
// must not be re-created from origin/main, which would orphan the pushed work.
//
// KNOWN, NOT FIXED HERE: the name embeds two mutable fields, so a project rename
// orphans a branch the same way a goal edit would if tier 4 did not exist, and
// two repos of one project can end up on differently-named branches for the same
// claim. Both are inherent to deriving a name from mutable data; neither loses
// commits, because the branch that holds them still exists under its old name.
func resolveClaimBranch(srcPath string, n claimBranchNames) string {
	const localRefs, remoteRefs = "refs/heads/", "refs/remotes/origin/"

	tried := map[string]bool{"": true}
	for _, cand := range []string{n.Branch, n.Legacy, n.Stem} {
		if tried[cand] {
			continue
		}
		tried[cand] = true
		if gitRefExists(srcPath, localRefs+cand) || gitRefExists(srcPath, remoteRefs+cand) {
			return cand
		}
	}

	// Last tier: the goal changed since the claim that created the branch.
	if n.Stem == "" {
		return ""
	}
	if m := gitUniqueBranchMatch(srcPath, localRefs, n.Stem+"-*"); m != "" {
		return m
	}
	return gitUniqueBranchMatch(srcPath, remoteRefs, n.Stem+"-*")
}
