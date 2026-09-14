// Package lifecycle holds the WORK-ITEM lifecycle: everything a claim does
// besides the server-side row insert — session_secret minting, the C6-2
// pre-claim state-file write, task-branch naming, worktree creation with the
// adoption safety checks aihub#328 and aihub#257 each had to add, per-worktree
// git excludes, the canonical-keyed state file, and repo pins.
//
// WHY IT IS A PACKAGE (aihub#667). Until this move the whole of it lived in six
// unexported functions in internal/mcp/tools_lifecycle.go and was reachable only
// over MCP stdio, so `polyforge drain` (aihub#640, the headless "A" harness)
// could not claim at all: pkg/client.ClaimWorkItem performs the server-side half,
// but on its own it produces a claimed work item with NO worktree, NO state file
// and NO session_secret, so the step agent it dispatches has nowhere to work and
// cannot make an authenticated pf_* call. #640 refused to reimplement it —
// aihub#640's workflow_identity_constraint forbids A containing execution logic
// B/C lacks, and a second copy of the adoption checks is exactly the drift the
// three hardening rounds were filed about — and ended FAILED instead. This is the
// aihub#654 move (step engine -> internal/engine) applied to the work item.
//
// ⚠️ THIS WAS A MOVE, NOT A REWRITE. The function bodies and their comments are
// the ones that shipped in internal/mcp; the only edits were the package clause,
// the handful of identifiers that had to be exported, and the seams that used to
// read *mcp.Server fields (the aihub client and the startup config) and now
// arrive as parameters. When reading a comment here that says "this handler" or
// "this process", it means the claim path, which is still one path with two
// callers.
package lifecycle

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/GMISWE/ieops-aihub/internal/config"
	"github.com/GMISWE/ieops-aihub/pkg/client"
)

// Client is the aihub half of a claim: the two calls Claim makes against the
// server. *client.Client satisfies it; tests substitute a fake.
//
// Narrow on purpose. Taking *client.Client would let this package reach every
// route aihub has, and the point of the seam is that a claim is these two
// requests and no others.
type Client interface {
	ClaimWorkItem(ctx context.Context, id string, body any) (map[string]any, error)
	RecordRepoPins(ctx context.Context, wiID string, body any) (map[string]any, error)
}

// ClaimRequest is one claim's inputs — the pf_claim_work_item parameters that
// reach the server or change what this process does, and nothing else.
type ClaimRequest struct {
	// WorkItemID is the id or slug the CALLER passed. It is deliberately not
	// canonicalised before use: ResolveStateFile keys on the string the caller
	// passed, so the pre-claim stub and its replay have to agree on it.
	WorkItemID     string
	IdempotencyKey string

	// RequestedLocks is forwarded verbatim when Set is true. The flag exists
	// because the MCP path forwards on key PRESENCE (`if v, ok := args[...]`),
	// so an explicit JSON null is a value the server is meant to see, and a
	// nil-vs-absent test here would forward one fewer field than the tool does.
	RequestedLocks    any
	RequestedLocksSet bool

	ForceTakeover bool
	ScenarioRef   string
}

// ClaimResult is the observable outcome of a claim: the server's response, the
// state file as it was written, and the two things the caller has to surface.
type ClaimResult struct {
	// Response is the server's claim response, unprojected. The MCP tool runs it
	// through slimClaimResult; drain reads wi_type and scenario_url off it.
	Response map[string]any
	// State is the state file this claim persisted, canonical-keyed.
	State *config.StateFile
	// RepoPins is non-nil ONLY when the pins were computed AND the server
	// accepted them. A pin that was computed but not recorded is reported in
	// WorktreeProblems instead, never here — see claimRepoPins for why an
	// unrecorded pin must not look like a recorded one.
	RepoPins map[string]string
	// WorktreeProblems are warnings on a SUCCESSFUL claim: directories rejected
	// by verifyClaimWorktree (aihub#328) and a repo-pin write that failed for a
	// reason other than the route being absent.
	WorktreeProblems []string
}

// Claim runs the local half of a claim around the server call, and is the whole
// of what `pf_claim_work_item` does apart from parsing its arguments and
// projecting its response.
//
// startupCfg is the config the calling process loaded at startup; it is only a
// fallback, see resolveWorkspaceConfig.
//
// An error here means the claim did not complete. ⚠️ It does NOT always mean
// nothing happened on the server: the state-file failure below fires AFTER the
// claim committed, and says so in its message.
func Claim(ctx context.Context, c Client, startupCfg *config.Config, req ClaimRequest) (*ClaimResult, error) {
	wiID := req.WorkItemID
	idemKey := req.IdempotencyKey
	if wiID == "" {
		return nil, fmt.Errorf("work_item_id is required")
	}
	if idemKey == "" {
		return nil, fmt.Errorf("idempotency_key is required")
	}
	// recordedPins is what the repo-pin block below assigns on success. In the MCP
	// handler that block wrote into the projected response map; here it writes a
	// TYPED local, which is handed back on ClaimResult.RepoPins. Typed rather than
	// map[string]any on purpose: an `any` round-trip would let a future change to
	// claimRepoPins' return type turn the read into a silent nil with no compile
	// error, and a lost pin is exactly the "confidently absent" value that function
	// is careful about everywhere else.
	var recordedPins map[string]string

	// C6-2: the session_secret is chosen BEFORE calling aihub, because the
	// server binds the attempt to the secret THIS request carries.
	//
	// ⚠️ aihub#392: on a REPLAY of an idempotency_key it must be the secret
	// already recorded for that key, not a fresh one. The server's replay
	// branch returns the EXISTING attempt and never touches
	// session_secret_hash (internal/domain/run_attempts.go — the only writers
	// of that column are the two INSERTs), so minting here left the state file
	// holding S2 while the row still stored hash(S1), and every later
	// credential-checked call answered "invalid session_secret". The trigger is
	// a retry of a timed-out claim, which is the one thing an idempotency key
	// exists for.
	//
	// The invariant this restores is a property of THIS process, which is why
	// it is fixed here and not in the replay branch: persist exactly the secret
	// you sent. Reusing the recorded one makes that true on both server paths —
	// the replay returns the attempt already bound to it, and if the server
	// instead INSERTs (the row is gone) it binds the new attempt to the secret
	// this request carried, which is the same value.
	//
	// Not fixed server-side on purpose. Re-registering the received secret in
	// the replay branch would ROTATE the credential of a live attempt, so a
	// replay by a second session on the same machine would silently 403 the
	// original holder (ATTEMPT_MISMATCH since aihub#441; 401 UNAUTHORIZED before
	// it) — trading a broken retry for a broken live session, which
	// is worse than the bug. Residual, stated rather than hidden: a replay from
	// a machine that has no state file for this key (or whose file was deleted)
	// still cannot know the accepted secret, and is still left unauthenticated.
	// Closing that needs the server to say "this was a replay"; see the
	// idempotency_key description.
	sessionSecret, reusedSecret := recordedClaimSecret(wiID, idemKey)
	if !reusedSecret {
		var secretErr error
		sessionSecret, secretErr = GenerateSessionSecret()
		if secretErr != nil {
			return nil, fmt.Errorf("generate session_secret: %w", secretErr)
		}
	}

	// Write partial state file first (C6-2 protocol)
	partial := &config.StateFile{
		WIID:          wiID,
		IdemKey:       idemKey,
		SessionSecret: sessionSecret,
		Claimed:       false,
	}
	if err := config.WriteStateFile(partial); err != nil {
		return nil, fmt.Errorf("write state file: %w", err)
	}

	// Build claim body — server requires session_info.machine_id (FnClaimWorkItem 400 guard).
	machineID := os.Getenv("POLYFORGE_MACHINE_ID")
	if machineID == "" {
		h, _ := os.Hostname()
		machineID = h
	}
	body := map[string]any{
		"idempotency_key": idemKey,
		"session_info": map[string]any{
			"session_secret": sessionSecret,
			"machine_id":     machineID,
		},
	}
	// aihub#394: `mode` is deliberately NOT forwarded, and not published either.
	//
	// It was `fresh|resume (default: fresh)` from the first version of this
	// tool, and every hop existed: published here, forwarded from here, bound
	// by domain.ClaimRequest. What never existed is an effect. On 1ec3bdc the
	// complete set of reads of the bound field was one self-default
	// (`if req.Mode == "" { req.Mode = "fresh" }`) and two audit fields
	// (`"is_resume": req.Mode == "resume"`) — so `resume` restored exactly what
	// `fresh` restored, an out-of-vocabulary value was silently equal to
	// `fresh`, and the only difference the argument made was to report itself
	// back to the caller who sent it.
	//
	// Withdrawn rather than implemented (owner decision 2026-09-07, the same
	// plan B as aihub#387's `non_conflicting`): the promise pf-work Mode C made
	// for it — "restores step state from the previous attempt" — is TRUE
	// WITHOUT it. Step state lives in wi_step_state keyed by work item, and the
	// claim's upsert there keeps current_step whatever `mode` says, so every
	// re-claim already sees it. There was nothing to build, only a parameter to
	// stop pretending with.
	//
	// ⚠️ The server still binds and defaults ClaimRequest.Mode, so an older
	// plugin that still sends `mode` is unaffected. Do not re-add it here to
	// "keep the client symmetrical" — a parameter this process publishes is a
	// parameter callers will use, and claim_param_contract_test.go now fails
	// for any published parameter the claim path does not act on.
	if req.RequestedLocksSet {
		body["requested_locks"] = req.RequestedLocks
	}
	if req.ForceTakeover {
		body["force_takeover"] = true
	}
	if sr := req.ScenarioRef; sr != "" {
		body["scenario_ref"] = sr
	}

	// aihub#416: `task_branches` is deliberately NOT sent, and the whole
	// aihub#356 machine that computed it is gone (claimTaskBranches,
	// claimBranchForRepo, declaredRepoNames, keyedBranchProblems, and
	// server-side ClaimRequest.TaskBranches / EffectiveDeclaredResource).
	//
	// It existed for ONE consumer: the git_branch lock key, which a repo
	// declaration no longer derives. With no key to spell, predicting the
	// branch before the claim bought nothing and cost a whole extra
	// GetWorkItem round-trip per claim plus a worktree stat/rev-parse pass —
	// so the claim is now one request shorter than it was.
	//
	// ⚠️ Worktree CREATION never went through it and is untouched: that path
	// runs newClaimBranchNames -> resolveClaimBranch -> addClaimWorktree,
	// below, and reads no task branch from anywhere. What DID go with the
	// chain is keyedBranchProblems, whose entire subject was "the lock names
	// a branch the worktree is not on" — a sentence with no referent once the
	// lock is gone. The other worktree_problems entries (aihub#328's rejected
	// directories) are unaffected and still reported.

	result, err := c.ClaimWorkItem(ctx, wiID, body)
	if err != nil {
		// Don't delete the partial state file — let the user retry
		return nil, fmt.Errorf("claim work item: %w", err)
	}

	// Build complete state file. Key by the canonical work_items.id the server
	// returns (the input wiID may be a slug like "aihub#1"); persisting the slug
	// makes later step/event/complete calls send a slug and hit FK / lookup
	// errors. (aihub#127)
	canonicalWIID := wiID
	if v, ok := result["id"].(string); ok && v != "" {
		canonicalWIID = v
	}
	sf := &config.StateFile{
		WIID:          canonicalWIID,
		IdemKey:       idemKey,
		SessionSecret: sessionSecret,
		Claimed:       true,
		ClaimedAt:     time.Now().UTC().Format(time.RFC3339),
	}
	if v, ok := result["attempt_id"].(string); ok {
		sf.AttemptID = v
	}
	if v, ok := result["claim_epoch"]; ok {
		switch ce := v.(type) {
		case float64:
			sf.ClaimEpoch = int64(ce)
		case int64:
			sf.ClaimEpoch = ce
		}
	}
	if v, ok := result["slug"].(string); ok {
		sf.Slug = v
	}
	if v, ok := result["project"].(string); ok {
		sf.Project = v
	}
	// aihub#322: the goal only feeds the task branch name below. It is a local
	// variable rather than a StateFile field on purpose — the state file is a
	// credential file read by middleware, and the goal is mutable wi content
	// that would go stale there the moment someone edits it.
	wiGoal, _ := result["goal"].(string)

	// Persist the canonical-keyed state file and remove any orphan slug stub
	// the C6-2 pre-claim write left behind (see config.WriteClaimState). (aihub#141)
	if err := config.WriteClaimState(wiID, canonicalWIID, sf); err != nil {
		// aihub#323. Returning the error is right (aihub#319 settled that: a
		// best-effort `_ =` answers ok:true and then every later tool dies on
		// "state file not found", a whole diagnosis away from the cause). What
		// was missing is that "update state file: ..." reads as "nothing
		// happened" — while ClaimWorkItem's tx.Commit ran before this line, so
		// the attempt is live, holds this work item's locks, and if a previous
		// holder was displaced it is already gone.
		//
		// ⚠️ THE RECOVERY IS A REPLAY OF **THIS SAME** idempotency_key. It was
		// the opposite until aihub#347, and this comment said so for as long as
		// that was true — do not "restore" it. Read off internal/domain/
		// run_attempts.go (FnClaimWorkItem's idempotency branch) and
		// recordedClaimSecret below, not assumed: a
		// claim carrying an already-used key takes the idempotency branch,
		// which returns the EXISTING attempt with its claim_epoch unchanged and
		// never touches session_secret_hash. Since #347 this handler no longer
		// mints a fresh secret on that path — recordedClaimSecret reads back the
		// one the C6-2 pre-claim write persisted under this key, so the state
		// file the replay writes holds exactly the secret the server already
		// accepted. That makes the replay the cheap fix: no epoch bump, no
		// superseded attempt, and nothing left to redo but the local write that
		// failed here.
		//
		// A NEW key is the FALLBACK, and only for the case where that pre-claim
		// record cannot be READ BACK — which is the predicate, not "still
		// exists". Four ways it happens: it was deleted; the retry comes from
		// another machine; the replay addresses the work item by a different id
		// spelling than the stub is filed under (ResolveStateFile keys on the
		// string the caller passed); or THIS VERY WRITE truncated it. That last
		// one is not exotic: a claim addressed by the canonical id has
		// passedID == canonicalID, so WriteClaimState writes the stub's own
		// path, and os.WriteFile opens O_TRUNC before it writes — so a failure
		// of the write rather than of the MkdirAll (ENOSPC, EIO) leaves a
		// truncated file that no longer parses. Then recordedClaimSecret misses, a fresh
		// secret is minted, the idempotency branch never registers it, and every
		// later call answers 403 ATTEMPT_MISMATCH "invalid session_secret" (401
		// UNAUTHORIZED before aihub#441) — so with the record gone the
		// same-user branch is the way out: it treats a new key as an implicit
		// takeover and issues a fresh attempt bound to the secret this call
		// generated. Correct, but it costs one epoch bump and one superseded
		// attempt, which is why it is second.
		//
		// ⚠️ DO NOT say here that the session_secret "existed only in memory".
		// That is true of pf_force_takeover and FALSE here: the C6-2 pre-claim
		// write at the top of this handler already persisted this same secret,
		// and it must have succeeded or we would have returned there. When the
		// failure is in MkdirAll — which is the common shape, a broken or
		// read-only state directory — os.WriteFile is never reached and that
		// earlier file is still on disk. What is actually lost is the BINDING:
		// the pre-claim stub carries claimed=false and no attempt_id, so
		// ResolveStateFile skips it and no later call can authenticate as this
		// attempt. Say that instead.
		//
		// ⚠️ "Skips" there means skips it as a MATCH, and the difference is what
		// the recovery above rests on. ResolveStateFile's first two lookups both
		// require a non-empty attempt_id, so neither of them returns the stub —
		// but its last line falls through to a plain ReadStateFile and hands the
		// stub back with a NIL error (internal/config/state.go). So the secret is
		// readable while the binding is not, which is exactly why the same-key
		// replay can re-authenticate: recordedClaimSecret reads it off this very
		// stub. What is lost is the binding, not the file and not the secret.
		attemptDesc := fmt.Sprintf("Attempt %s (epoch %d)", sf.AttemptID, sf.ClaimEpoch)
		if sf.AttemptID == "" {
			// An old server that did not echo attempt_id would otherwise render
			// "Attempt  (epoch 0)", which reads as data rather than as absence.
			attemptDesc = "A new attempt (the server did not echo its id)"
		}
		return nil, fmt.Errorf("update state file: %w"+
			" (NOT A NO-OP: the claim ALREADY SUCCEEDED on the server)."+
			" %s is running under your name and holds whatever locks this work item declares;"+
			" only this machine's local record of it failed, and without that record nothing here can authenticate as the attempt."+
			" RECOVERY: call pf_claim_work_item again and REPLAY THIS SAME idempotency_key, with the same work_item_id spelling you passed here."+
			" This process persisted that key's session_secret before it called the server, so the replay reuses that exact secret and the server returns THIS attempt with its epoch unchanged (aihub#392):"+
			" it costs no epoch bump and no superseded attempt, and the only thing it has to redo is the local write that just failed."+
			" Send a NEW idempotency_key ONLY IF that record cannot be read back (deleted, truncated by the very write that just failed, or you are retrying from a different machine), since a replay that cannot read the recorded secret mints one the server never registered and every later call then answers \"invalid session_secret\"."+
			" That fallback is not destructive either, but it does cost one epoch bump and one superseded attempt."+
			" %s",
			err, attemptDesc, StateWriteFilesystemAdvice)
	}

	// Create git worktrees for each repo in the project (non-fatal).
	// Worktree path format: pf.<project>-<seq>/<repo>/
	// Branch name: polyforge/<project>-<seq>-<kebab goal> (newClaimBranchNames).
	//
	// aihub#328: declared out here so a directory rejected below can be reported
	// on the RESPONSE. The loop's existing failure mode is one line on stderr,
	// which an MCP server writes to a log the calling agent never reads — and
	// "adopted forever, noticed by nobody" is the whole defect, so moving it from
	// a silent adoption to a silent skip would only change its shape.
	//
	// ⚠️ The "nobody reads stderr" half is true of the MCP caller and NOT of the
	// drain caller, where stderr IS the operator's channel (aihub#667). That is
	// not a reason to drop this list: drainClaimer.Claim prints these entries
	// itself precisely because it has no model reading an ok:true response.
	var worktreeProblems []string
	// aihub#356: declared out here, alongside worktreeProblems and for the same
	// reason. The post-claim check below has to run whether or not the block
	// that fills this in was entered at all — every guard on the way in
	// (no workspace root, no config, no seq) is a path on which a branch may
	// already have been keyed and no worktree exists to confirm it.
	worktrees := make(map[string]string)
	if sf.Project != "" {
		wsRoot := os.Getenv("POLYFORGE_WORKSPACE_ROOT")
		if wsRoot == "" {
			wsRoot = config.FindWorkspaceRoot()
		}
		if wsRoot != "" {
			effectiveCfg := resolveWorkspaceConfig(wsRoot, startupCfg)

			// Derive seq from slug (e.g. "marketplace#42" → "42").
			seq := ""
			if sf.Slug != "" {
				if idx := strings.LastIndex(sf.Slug, "#"); idx >= 0 {
					seq = sf.Slug[idx+1:]
				}
			}

			// Derive ulid8: last 8 chars of wi_id after stripping "wi_" prefix.
			// No longer the branch name (aihub#322) — kept as the LEGACY name, which
			// resume still has to recognise for every work item claimed before this
			// change, and as the last-resort name when the slug yields no seq.
			ulid8 := claimBranchULID8(canonicalWIID)

			if effectiveCfg != nil && seq != "" && ulid8 != "" {
				// Directory name uses readable format: pf.<project>-<seq>
				// (e.g. "pf.aihub-26") so developers can identify the wi at a glance.
				wtDir := fmt.Sprintf("pf.%s-%s", sf.Project, seq)
				// Which branch to attach to is decided by what exists in the clone,
				// not by anything the caller passes. See resolveClaimBranch.
				//
				// aihub#322 established that by removing the last read of
				// args["mode"] here; aihub#394 then withdrew that parameter
				// altogether, so there is no longer an argument this could be
				// keyed on even by mistake. Kept as a note because the invariant
				// (the clone decides) is what matters, not the argument that used
				// to threaten it.
				branchNames := newClaimBranchNames(sf.Project, seq, wiGoal, ulid8)

				if proj, ok := effectiveCfg.Projects[sf.Project]; ok {
					for _, repo := range proj.Repos {
						srcPath := filepath.Join(wsRoot, ".repo", repo.Name)
						wtPath := filepath.Join(wsRoot, wtDir, repo.Name)

						// If the worktree directory already exists, reuse it — but only
						// once git agrees it IS one. aihub#328: existence is not
						// health, and adoption is permanent, because what gets adopted
						// is written to the state file and short-circuits every later
						// claim.
						if _, statErr := os.Stat(wtPath); statErr == nil {
							if vErr := verifyClaimWorktree(wtPath); vErr != nil {
								// Report, do NOT repair. The directory can hold
								// uncommitted work — a checkout killed at 90% still has
								// the other 90% — so `rm -rf` here would destroy it on a
								// guess, and `worktree add` onto a non-empty path fails
								// anyway. Skipping leaves this repo without a worktree,
								// which is the honest outcome and is what the message
								// says.
								// ⚠️ THE ORDER OF THE TWO CLEANUP COMMANDS IS
								// LOAD-BEARING and was wrong in the first draft.
								// `git worktree prune` only drops admin entries whose
								// working tree is MISSING, so running it first, while
								// the directory still exists, is a no-op; the rm then
								// leaves the registration behind and the next
								// `worktree add` on that path fails with "is a missing
								// but already registered worktree". Measured on git
								// 2.43.0: prune-then-rm made the re-add exit 128,
								// rm-then-prune made it exit 0. Advice that produces
								// the failure it was written to prevent is worse than
								// no advice.
								problem := fmt.Sprintf("%s: %s exists but is not a usable git worktree (%v), so this claim created NO worktree for that repo. "+
									"Inspect it first: a half-finished checkout still holds whatever was written before it died. "+
									"Once you are sure nothing there is worth keeping, IN THIS ORDER: `rm -rf %s && git -C %s worktree prune`, then claim again.",
									repo.Name, wtPath, vErr, wtPath, srcPath)
								fmt.Fprintf(os.Stderr, "polyforge: %s\n", problem)
								worktreeProblems = append(worktreeProblems, problem)
								continue
							}
							worktrees[repo.Name] = wtPath
							writeWorktreeExcludes(wtPath)
							// aihub#257. THIS is the branch the 199 already-hazardous
							// worktrees take, and putting the repair only inside
							// addClaimWorktree would have missed every one of them: a
							// linked worktree has a directory on disk by definition, so
							// os.Stat succeeds and the code below never runs. Same shape
							// as aihub#264, which shipped prevention that could not
							// reach the instances it was filed for.
							repairReusedWorktreeUpstream(ctx, srcPath, wtPath)
							continue
						}

						if err := addClaimWorktree(ctx, srcPath, wtPath, branchNames); err != nil {
							fmt.Fprintf(os.Stderr, "polyforge: worktree add for %s: %v\n", repo.Name, err)
							continue
						}
						worktrees[repo.Name] = wtPath
						writeWorktreeExcludes(wtPath)
					}

					if len(worktrees) > 0 {
						sf.Worktrees = worktrees
						// Best-effort: update state file with worktree paths.
						_ = config.WriteStateFile(sf)
					}
				}
			}
		}
	}

	// aihub#416 D2: record where this attempt starts from, per repo, now that
	// the worktrees exist. See domain.RecordRepoPinsRequest for why this is a
	// second request rather than a field on the claim.
	//
	// Best-effort, deliberately, and on BOTH halves. The pins are provenance:
	// a claim that cannot record them is still a claim that succeeded, and
	// failing here would turn a lost annotation into a lost session — the same
	// posture claimTaskBranches took ("EVERY failure returns nil") without its
	// downside, since nothing downstream is keyed on the value.
	//
	// Reported on the response either way. A missing pin has to be VISIBLE,
	// because the rule that comes with it (owner ruling Q-4) is that a
	// conclusion drawn in a repo with no pin must say it has no provenance —
	// and a caller cannot follow a rule about a value it was never shown.
	if pins := claimRepoPins(ctx, sf.Worktrees); len(pins) > 0 {
		body := map[string]any{
			"attempt_id":     sf.AttemptID,
			"claim_epoch":    sf.ClaimEpoch,
			"session_secret": sf.SessionSecret,
			"repo_pins":      pins,
		}
		if _, pinErr := c.RecordRepoPins(ctx, sf.WIID, body); pinErr != nil {
			fmt.Fprintf(os.Stderr, "polyforge: record repo pins for %s: %v\n", sf.WIID, pinErr)
			// 🔴 A 404 is NOT reported to the caller, and the exception is
			// deliberate. This process can be newer than the aihub it is
			// talking to, and a server that predates aihub#416 has no
			// /repo_pins route at all — so on every claim against it this
			// would append a warning about missing provenance that the agent
			// can do absolutely nothing about. A warning nobody can act on
			// trains people to skip warnings, which costs more than the one
			// it delivers.
			//
			// ⚠️ The distinction is "the capability is absent" versus "the
			// capability failed", and only the second is the caller's
			// problem. Everything else — a credential mismatch, a 409, a
			// broken connection — still reaches the response, because the
			// rule that comes with a pin (a conclusion drawn in an unpinned
			// repo must say it has no provenance) needs the caller to know
			// the pin is missing.
			if !client.IsStatus(pinErr, http.StatusNotFound) {
				worktreeProblems = append(worktreeProblems, fmt.Sprintf(
					"repo pins were not recorded (%v), so this attempt has NO server-side record of which "+
						"commit each repo started from. The claim itself succeeded and the worktrees are "+
						"usable; what is missing is provenance. Any conclusion this session publishes from "+
						"reading a repo has to say so rather than presenting itself as reproducible.", pinErr))
			}
		} else {
			recordedPins = pins
		}
	}

	return &ClaimResult{
		Response:         result,
		State:            sf,
		RepoPins:         recordedPins,
		WorktreeProblems: worktreeProblems,
	}, nil
}
