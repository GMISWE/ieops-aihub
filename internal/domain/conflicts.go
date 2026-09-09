package domain

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// ConflictSeverity is soft_block or hard_block.
type ConflictSeverity string

const (
	SeveritySoftBlock ConflictSeverity = "soft_block"
	SeverityHardBlock ConflictSeverity = "hard_block"
	SeverityInfo      ConflictSeverity = "info"
)

// ConflictPrediction is a single prediction result.
type ConflictPrediction struct {
	Rule         int              `json:"rule"`
	Severity     ConflictSeverity `json:"severity"`
	Description  string           `json:"description"`
	ResourceType string           `json:"resource_type,omitempty"`
	ResourceKey  string           `json:"resource_key,omitempty"`
	AttemptID    string           `json:"attempt_id,omitempty"`
	ActorDisplay string           `json:"actor_display,omitempty"`
	WIID         string           `json:"work_item_id,omitempty"`
	WISlug       string           `json:"work_item_slug,omitempty"`
	// LastActiveAgeSeconds is how long ago the conflicting attempt last reported
	// activity (run_attempts.last_active_at), on the declaration-join rules that
	// have an attempt row to read it from: 2 (repo), 4 (repo refactor) and
	// 6 (service). aihub#416.
	//
	// 🔴 It is what makes the de-locking ruling's deploy preflight a call that
	// already exists. mechanics (3) asks for "running attempts with service
	// declarations + heartbeat age", and with this field
	// pf_predict_conflicts(declared_resources=[{"type":"service", ...}]) IS that
	// list — no new endpoint and no new tool.
	//
	// 🔴 A POINTER, and that is not style. With `omitempty` on a bare int64 the
	// value 0 disappears, and 0 is a real and important answer here: an attempt
	// that reported activity within the last second is the MOST live holder
	// there is, and rendering it as "no age recorded" would invert the reading.
	// nil means the rule had no attempt row; 0 means "just now".
	//
	// ⚠️ It is an AGE, not a lease. Nothing in this system expires on it (design
	// v1.21 removed expires_at outright and handleRenewLease answers 410 Gone);
	// it is published so a HUMAN can judge wait-versus-takeover, and no code path
	// may branch on it.
	LastActiveAgeSeconds *int64 `json:"last_active_age_seconds,omitempty"`
}

// lastActiveAgeSeconds converts a run_attempts.last_active_at reading into the
// age carried on a prediction. Clamped at zero because clock_timestamp() on the
// server and time.Now() here are two clocks: a row written microseconds ago can
// read as very slightly in the future, and a negative age would be reported as
// though the attempt were active in the future rather than now.
func lastActiveAgeSeconds(lastActive time.Time) *int64 {
	age := int64(time.Since(lastActive).Seconds())
	if age < 0 {
		age = 0
	}
	return &age
}

// PredictConflictsRequest is the body for POST /v1/conflicts/predict.
type PredictConflictsRequest struct {
	WorkItemID *string `json:"work_item_id"`
	// Project namespaces file_scope lock keys (aihub#222). When WorkItemID is set
	// the wi's own project takes precedence; Project is the fallback for a
	// create-preview predict issued before the wi exists. Empty yields a bare key,
	// which only matches legacy pre-migration rows.
	Project           string          `json:"project"`
	DeclaredResources json.RawMessage `json:"declared_resources"`
	DryRun            bool            `json:"dry_run"`
}

// DeclaredResourceItem is a single declared resource entry.
type DeclaredResourceItem struct {
	Type string `json:"type"`
	URI  string `json:"uri"`
	// Repo names the repository a path/document/section URI is relative to
	// (aihub#261). declared_resources paths are REPO-relative, and until this
	// field existed the lock key had no way to say which repo, so every repo's
	// go.mod / Makefile / README.md in one project shared a single key.
	//
	// Optional, and the empty value is not a defect — it means "some repo in
	// this project, unspecified", which is exactly what every pre-aihub#261
	// declaration meant. See fileScopeLockKey for what the empty value does to
	// the key, and lockConflictProbe for why it still conflicts with everything
	// it used to.
	//
	// It is deliberately a separate field rather than a prefix baked into `uri`:
	// a repo-qualified uri would be indistinguishable from a path whose first
	// segment happens to be a directory of that name.
	Repo       string `json:"repo,omitempty"`
	Intent     string `json:"intent"`
	BaseBranch string `json:"base_branch,omitempty"`
	TaskBranch string `json:"task_branch,omitempty"`
}

// WillUnlockItem describes a blocked wi that will be unblocked by a successful claim.
type WillUnlockItem struct {
	ID   string `json:"id"`
	Slug string `json:"slug"`
	Goal string `json:"goal"`
}

// PredictConflictsResponse is returned by POST /v1/conflicts/predict.
type PredictConflictsResponse struct {
	Severity    ConflictSeverity     `json:"severity"`
	Predictions []ConflictPrediction `json:"predictions"`
	WillUnlock  []WillUnlockItem     `json:"will_unlock"`
}

// declaresContainmentSQL is the WHERE fragment PredictConflicts rules 2, 5 and 6
// use to ask "does this RUNNING work item DECLARE this entry", and
// declaresIntentContainmentSQL is the same question with the intent pinned as
// well (rule 4).
//
// 🔴 THE OPERAND IS BUILT BY POSTGRES OUT OF BOUND PARAMETERS AND IS NEVER
// ASSEMBLED AS JSON TEXT IN GO (aihub#511). All four rules used to paste the
// declared name into a JSON literal — `[{"type":"repo","uri":"repo:`+name+`"}]`
// — and nothing on the way in constrains the characters of a name:
// ValidateDeclaredResources checks the uri SCHEME and stops there. That broke in
// two ways, and the second is why "reject odd names at the door" would not have
// been a fix:
//
//   - a name holding `"` closed the string early, Postgres refused the operand
//     with 22P02, and the error went to stderr while the CALLER received
//     {"predictions":[],"severity":"info"} — byte-identical to a genuine
//     all-clear, which is exactly the aihub#238 failure mode rule 2's own header
//     below says it exists to avoid;
//   - a name holding `\b` produced VALID json for a DIFFERENT string, so there
//     was no error to log at all: the query matched nothing, silently, and no
//     caller could tell that from "nobody else declared it".
//
// This is a regression rather than an oversight, which is why the rule is
// written here instead of left in a commit message: rule 2 bound its repo name
// as $1 (`resource_key LIKE $1 || '/%'`) until the aihub#416 rewrite replaced
// that query with a concatenated containment literal, and rules 4 and 5 had
// carried the concatenated shape since before that. Anything added later that
// asks the same question must reuse these two constants.
//
// ⚠️ The ::text casts are load-bearing, not decoration: jsonb_build_object takes
// "any", so without them Postgres cannot infer the parameter types and refuses
// to prepare the statement.
const declaresContainmentSQL = `wi.declared_resources @> jsonb_build_array(jsonb_build_object('type',$1::text,'uri',$2::text))`

const declaresIntentContainmentSQL = `wi.declared_resources @> jsonb_build_array(jsonb_build_object('type',$1::text,'uri',$2::text,'intent',$3::text))`

// PredictConflicts applies the 5 conflict rules and returns predictions.
// Implements §23 of the design doc.
func PredictConflicts(ctx context.Context, pool *pgxpool.Pool, req *PredictConflictsRequest, callerProjectRoles map[string]string) (*PredictConflictsResponse, *AihubError) {
	// aihub#238: validate BEFORE any database access. This is the call pf-work
	// uses as its pre-claim gate, and an unrecognized type used to fall through
	// resourceToLock into `continue`, so the response was
	// {"predictions":[],"severity":"info"} — a fake all-clear that reads exactly
	// like a genuine one. Refusing is the only safe answer for input we cannot
	// map to locks. Ordering matters: the test for this passes a nil pool, so if
	// validation ever moves below the first query the test panics instead of
	// passing.
	if aihubErr := ValidateDeclaredResources(req.DeclaredResources); aihubErr != nil {
		return nil, aihubErr
	}

	var resources []DeclaredResourceItem
	if len(req.DeclaredResources) > 0 && string(req.DeclaredResources) != "null" {
		if err := json.Unmarshal(req.DeclaredResources, &resources); err != nil {
			return nil, NewErr(ErrBadRequest, "failed to parse declared_resources")
		}
	}
	// aihub#261: a path entry inherits the repo from the payload's own `repo:`
	// declaration. Run it here, once, so rules 1 and 3 below see the same
	// resolved entries the claim path will — predict exists to answer "what will
	// claim do", so a difference in the pre-pass is a difference in the answer.
	resources = resolveDeclaredRepos(resources)

	result := &PredictConflictsResponse{
		Severity:    SeverityInfo,
		Predictions: []ConflictPrediction{},
		WillUnlock:  []WillUnlockItem{},
	}

	// Resolve the project whose namespace the declared file_scope resources live
	// in. file_scope lock keys are "<project>:<path>" (aihub#222) so byte-identical
	// relative paths in different projects (e.g. a fork repo and its parent) do not
	// collide. Prefer the wi's own project (authoritative); fall back to req.Project
	// for a create-preview issued before the wi exists.
	//
	// The same lookup also resolves the CANONICAL id (aihub#357). work_item_id
	// arrives as an id or a slug — this query already said so — but will_unlock
	// below compares it to wi_dependencies.blocking_wi_id, a column that
	// FK-references work_items(id) and never holds a slug. So half this function
	// accepted a slug and the other half silently answered `"will_unlock": []`
	// for it, which is also what a work item that unblocks nothing returns.
	// Resolving once, here, is what keeps the two halves from disagreeing again.
	effectiveProject := req.Project
	canonicalWIID := ""
	if req.WorkItemID != nil && *req.WorkItemID != "" {
		canonicalWIID = *req.WorkItemID
		var id, p string
		if lookupErr := pool.QueryRow(ctx,
			`SELECT id, project FROM work_items WHERE id=$1 OR slug=$1`, *req.WorkItemID,
		).Scan(&id, &p); lookupErr == nil {
			if p != "" {
				effectiveProject = p
			}
			if id != "" {
				canonicalWIID = id
			}
		}
	}

	// Rule 1: resource_lock conflict (hard_block)
	// Skip if dry_run=true (advisory only)
	//
	// derivedLock, not resourceToLock (aihub#342): rule 1 answers "would taking
	// this resource's lock collide", and a declaration that takes no lock cannot
	// collide with one. Before this, an intent=read path over a held lock got
	// hard_block from rule 1 and info from rule 3 — two rules of one function
	// contradicting each other on a single input, with only dry_run deciding
	// which one the caller saw.
	if !req.DryRun {
		for _, res := range resources {
			lockType, lockKey, probe := derivedLockProbe(res, effectiveProject)
			if lockType == "" {
				continue
			}
			var ownerAttemptID, actorDisplay, wiSlug, wiID string
			err := pool.QueryRow(ctx, `
				SELECT rl.owner_attempt_id, ra.actor_display, wi.slug, wi.id
				FROM resource_locks rl
				JOIN run_attempts ra ON ra.id = rl.owner_attempt_id
				JOIN work_items wi ON wi.id = ra.work_item_id
				WHERE `+lockConflictWhereClause+` AND ra.status='running'`,
				lockType, probe.Keys, probe.LikePattern,
			).Scan(&ownerAttemptID, &actorDisplay, &wiSlug, &wiID)
			if err == nil {
				result.Predictions = append(result.Predictions, ConflictPrediction{
					Rule:         1,
					Severity:     SeverityHardBlock,
					Description:  "Resource lock is already held by another attempt",
					ResourceType: lockType,
					ResourceKey:  lockKey,
					AttemptID:    ownerAttemptID,
					ActorDisplay: actorDisplay,
					WISlug:       wiSlug,
					WIID:         wiID,
				})
				result.Severity = SeverityHardBlock
				return result, nil // hard_block: stop processing further rules
			}
		}
	}

	// Rule 2: another running work item DECLARES the same repo (soft_block)
	//
	// 🔴 THIS RULE READS DECLARATIONS, NOT LOCKS, and the rewrite was mandatory
	// rather than tidy-up (aihub#416 §5.3). It used to hardcode
	// `rl.resource_type='git_branch'` in SQL, which BYPASSED resourceToLock —
	// so retiring the git_branch derivation without touching this query would
	// have left it matching zero rows forever, and zero rows is byte-identical
	// to "no conflict". That is the fake all-clear aihub#238 was filed to
	// remove, and it would have come back inside the same function.
	//
	// The shape is rule 4's and rule 5's, deliberately: `declared_resources @>`
	// against a running work item. Those two were already the "advisory
	// declaration" pattern the de-locking ruling generalises, so this rule now
	// joins them instead of being the odd one out.
	//
	// ⚠️ The wording moved from "is working on the same repo BRANCH" to
	// "declares the same repo" because the old sentence became FALSE, not merely
	// stale: no branch name participates in the judgement any more. Two attempts
	// on two different branches of one repo match this rule, and they should —
	// what it reports is shared blast radius, which is why it is soft_block and
	// not hard_block. Callers keying on the description text are the reason this
	// is called out rather than silently changed.
	for _, res := range resources {
		if res.Type != "repo" {
			continue
		}
		repoName := strings.TrimPrefix(res.URI, "repo:")
		rows, err := pool.Query(ctx, `
			SELECT ra.id, ra.actor_display, wi.slug, wi.id, ra.last_active_at
			FROM work_items wi
			JOIN run_attempts ra ON ra.id = wi.current_attempt_id
			WHERE wi.status='running'
			  AND `+declaresContainmentSQL,
			"repo", "repo:"+repoName,
		)
		if err == nil {
			for rows.Next() {
				var ownerAttemptID, actorDisplay, wiSlug, wiID string
				var lastActive time.Time
				if err := rows.Scan(&ownerAttemptID, &actorDisplay, &wiSlug, &wiID, &lastActive); err != nil {
					continue
				}
				result.Predictions = append(result.Predictions, ConflictPrediction{
					Rule:                 2,
					Severity:             SeveritySoftBlock,
					Description:          "Another attempt declares the same repo",
					ResourceType:         "repo",
					ResourceKey:          repoName,
					AttemptID:            ownerAttemptID,
					ActorDisplay:         actorDisplay,
					WISlug:               wiSlug,
					WIID:                 wiID,
					LastActiveAgeSeconds: lastActiveAgeSeconds(lastActive),
				})
				if result.Severity != SeverityHardBlock {
					result.Severity = SeveritySoftBlock
				}
			}
			// pgx defers execute-time errors to Err() (aihub#382, aihub#386).
			if err := rows.Err(); err != nil {
				fmt.Fprintf(os.Stderr, "predict_conflicts: rule 2 repo declaration rows: %v\n", err)
			}
			rows.Close()
		}
	}

	// Rule 3: path glob overlap (soft_block or info based on intent)
	for _, res := range resources {
		if res.Type != "path" && res.Type != "document" && res.Type != "section" {
			continue
		}
		// aihub#261: rule 3 compares through the same probe rule 1 uses, so the
		// advisory answer stays a superset of the hard one. Building a bare key
		// here and prefix-matching it would make rule 3 MISS the legacy/qualified
		// cross-cases that rule 1 blocks on — the two-rules-one-input
		// contradiction aihub#342 exists to prevent, rediscovered one segment down.
		probe := fileScopeConflictProbe(effectiveProject, res.Repo, res.URI)
		rows, err := pool.Query(ctx, `
			SELECT rl.resource_key, ra.actor_display, wi.slug, wi.id
			FROM resource_locks rl
			JOIN run_attempts ra ON ra.id = rl.owner_attempt_id
			JOIN work_items wi ON wi.id = ra.work_item_id
			WHERE rl.resource_type='file_scope'
			  AND ra.status='running'`,
		)
		if err == nil {
			for rows.Next() {
				var existingKey, actorDisplay, wiSlug, wiID string
				if err := rows.Scan(&existingKey, &actorDisplay, &wiSlug, &wiID); err != nil {
					continue
				}
				if probe.Overlaps(existingKey) {
					severity := SeveritySoftBlock
					if res.Intent == "read" {
						severity = SeverityInfo
					}
					result.Predictions = append(result.Predictions, ConflictPrediction{
						Rule:         3,
						Severity:     severity,
						Description:  "File path overlaps with another running attempt",
						ResourceType: "file_scope",
						ResourceKey:  existingKey,
						ActorDisplay: actorDisplay,
						WISlug:       wiSlug,
						WIID:         wiID,
					})
					if result.Severity != SeverityHardBlock && severity == SeveritySoftBlock {
						result.Severity = SeveritySoftBlock
					}
				}
			}
			// pgx defers execute-time errors to Err() (aihub#382, aihub#386).
			if err := rows.Err(); err != nil {
				fmt.Fprintf(os.Stderr, "predict_conflicts: rule 3 file_scope rows: %v\n", err)
			}
			rows.Close()
		}
	}

	// Rule 4: same repo refactor (soft_block)
	for _, res := range resources {
		if res.Type != "repo" || res.Intent != "refactor" {
			continue
		}
		repoName := strings.TrimPrefix(res.URI, "repo:")
		rows, err := pool.Query(ctx, `
			SELECT ra.actor_display, wi.slug, wi.id, ra.last_active_at
			FROM work_items wi
			JOIN run_attempts ra ON ra.id = wi.current_attempt_id
			WHERE wi.status='running'
			  AND `+declaresIntentContainmentSQL,
			"repo", "repo:"+repoName, "refactor",
		)
		if err == nil {
			for rows.Next() {
				var actorDisplay, wiSlug, wiID string
				var lastActive time.Time
				if err := rows.Scan(&actorDisplay, &wiSlug, &wiID, &lastActive); err != nil {
					continue
				}
				result.Predictions = append(result.Predictions, ConflictPrediction{
					Rule:         4,
					Severity:     SeveritySoftBlock,
					Description:  "Another attempt is refactoring the same repo",
					ResourceType: "repo",
					ResourceKey:  repoName,
					ActorDisplay: actorDisplay,
					WISlug:       wiSlug,
					// aihub#416: the same age rules 2 and 6 carry. Rule 4 is a repo
					// prediction too, and a caller that got an age from rule 2 and
					// none from rule 4 for one repo would read the gap as "that one
					// is not live".
					LastActiveAgeSeconds: lastActiveAgeSeconds(lastActive),
				})
				if result.Severity != SeverityHardBlock {
					result.Severity = SeveritySoftBlock
				}
			}
			// pgx defers execute-time errors to Err() (aihub#382, aihub#386).
			if err := rows.Err(); err != nil {
				fmt.Fprintf(os.Stderr, "predict_conflicts: rule 4 refactor rows: %v\n", err)
			}
			rows.Close()
		}
	}

	// Rule 5: external_ref overlap (info)
	for _, res := range resources {
		if res.Type != "external_ref" {
			continue
		}
		rows, err := pool.Query(ctx, `
			SELECT ra.actor_display, wi.slug, wi.id
			FROM work_items wi
			JOIN run_attempts ra ON ra.id = wi.current_attempt_id
			WHERE wi.status='running'
			  AND `+declaresContainmentSQL,
			"external_ref", res.URI,
		)
		if err == nil {
			for rows.Next() {
				var actorDisplay, wiSlug, wiID string
				if err := rows.Scan(&actorDisplay, &wiSlug, &wiID); err != nil {
					continue
				}
				result.Predictions = append(result.Predictions, ConflictPrediction{
					Rule:         5,
					Severity:     SeverityInfo,
					Description:  "Another attempt references the same external resource",
					ResourceType: "external_ref",
					ResourceKey:  res.URI,
					ActorDisplay: actorDisplay,
					WISlug:       wiSlug,
				})
			}
			// pgx defers execute-time errors to Err() (aihub#382, aihub#386).
			if err := rows.Err(); err != nil {
				fmt.Fprintf(os.Stderr, "predict_conflicts: rule 5 external_ref rows: %v\n", err)
			}
			rows.Close()
		}
	}

	// Rule 6: another running work item declares the same service (info)
	//
	// 🔴 NEW IN aihub#416, and it exists because retiring the deploy_env
	// derivation would otherwise have left `service` with NO rule at all. Rule 1
	// was its only reader, and rule 1 goes through derivedLockProbe, which now
	// returns ("","") for a service — so a service declaration would have
	// produced a silent empty result. The de-locking ruling says advisory
	// entries "feed pf_predict_conflicts"; before this, the rule they were
	// supposed to feed did not exist.
	//
	// 🔴 `info`, NOT soft_block, and the difference is the whole design. Two
	// observers sharing one service is no longer a problem to be prevented: each
	// records the service generation at the start of its observation and re-reads
	// it at the end, and a mismatch invalidates that observation. So what a
	// caller needs here is a FACT ("somebody else is looking at this service, and
	// here is how recently they were active"), not an obstacle. Deploy-versus-
	// observation exclusion is the runbook's job, not this predicate's.
	//
	// Outside the `if !req.DryRun` guard, like rules 2-5: only rule 1 is gated on
	// dry_run, because only rule 1 answers "would an insert collide". A rule that
	// reads declarations has nothing to be advisory about — it already is.
	for _, res := range resources {
		if res.Type != "service" {
			continue
		}
		svc := strings.TrimPrefix(res.URI, "service:")
		rows, err := pool.Query(ctx, `
			SELECT ra.id, ra.actor_display, wi.slug, wi.id, ra.last_active_at
			FROM work_items wi
			JOIN run_attempts ra ON ra.id = wi.current_attempt_id
			WHERE wi.status='running'
			  AND `+declaresContainmentSQL,
			"service", "service:"+svc,
		)
		if err == nil {
			for rows.Next() {
				var ownerAttemptID, actorDisplay, wiSlug, wiID string
				var lastActive time.Time
				if err := rows.Scan(&ownerAttemptID, &actorDisplay, &wiSlug, &wiID, &lastActive); err != nil {
					continue
				}
				result.Predictions = append(result.Predictions, ConflictPrediction{
					Rule:                 6,
					Severity:             SeverityInfo,
					Description:          "Another attempt declares the same service",
					ResourceType:         "service",
					ResourceKey:          svc,
					AttemptID:            ownerAttemptID,
					ActorDisplay:         actorDisplay,
					WISlug:               wiSlug,
					WIID:                 wiID,
					LastActiveAgeSeconds: lastActiveAgeSeconds(lastActive),
				})
				// Deliberately NO write to result.Severity. An info prediction that
				// raised the top-level severity would be a soft_block wearing
				// another name, and pf-work's pre-claim gate reads that field.
			}
			// pgx defers execute-time errors to Err() (aihub#382, aihub#386).
			if err := rows.Err(); err != nil {
				fmt.Fprintf(os.Stderr, "predict_conflicts: rule 6 service declaration rows: %v\n", err)
			}
			rows.Close()
		}
	}

	// Compute will_unlock: work items that would be unblocked if this wi completes.
	// Keyed on canonicalWIID, never on the caller's raw reference (aihub#357).
	if canonicalWIID != "" {
		rows, err := pool.Query(ctx, `
			SELECT DISTINCT wi.id, wi.slug, wi.goal
			FROM wi_dependencies dep
			JOIN work_items wi ON wi.id = dep.blocked_wi_id
			WHERE dep.blocking_wi_id = $1
			  AND dep.kind = 'blocks'
			  AND wi.status = 'blocked'
			  AND NOT EXISTS (
			    SELECT 1 FROM wi_dependencies dep2
			    JOIN work_items blocker ON dep2.blocking_wi_id = blocker.id
			    WHERE dep2.blocked_wi_id = wi.id
			      AND dep2.kind = 'blocks'
			      AND dep2.blocking_wi_id != $1
			      AND blocker.status NOT IN ('wrapped','cancelled','failed')
			  )`,
			canonicalWIID,
		)
		if err == nil {
			for rows.Next() {
				var item WillUnlockItem
				if err := rows.Scan(&item.ID, &item.Slug, &item.Goal); err != nil {
					continue
				}
				result.WillUnlock = append(result.WillUnlock, item)
			}
			// pgx defers execute-time errors to Err() (aihub#382, aihub#386).
			if err := rows.Err(); err != nil {
				fmt.Fprintf(os.Stderr, "predict_conflicts: will_unlock rows: %v\n", err)
			}
			rows.Close()
		}
	}

	// H7: cross-project folding — redact actor_display/work_item_slug for projects caller can't view
	foldedPredictions := make([]ConflictPrediction, 0, len(result.Predictions))
	for _, p := range result.Predictions {
		if p.WIID != "" {
			// Look up the project of the conflicting wi
			var wiProject string
			pool.QueryRow(ctx, `SELECT project FROM work_items WHERE id=$1`, p.WIID).Scan(&wiProject) //nolint:errcheck
			if wiProject != "" {
				callerRole := callerProjectRoles[wiProject]
				if callerRole == "" && wiProject != "" {
					// No access — redact identifying info
					p.ActorDisplay = ""
					p.WIID = ""
					p.WISlug = ""
					p.AttemptID = ""
					p.Description = "[conflict in project " + wiProject + " — no visibility]"
				}
			}
		}
		foldedPredictions = append(foldedPredictions, p)
	}
	result.Predictions = foldedPredictions

	return result, nil
}

// derivedLock returns the write lock a declared resource takes, or ("", "") if
// it takes none. It is the ONE place the intent rule lives, and every path that
// turns declared_resources into resource_locks rows must go through it:
// FnClaimWorkItem, FnForceTakeover, FnAcquireLocks, and PredictConflicts rule 1.
//
// aihub#342. The rule is not new — it is the contract carried by
// declaredResourcesProp in internal/mcp ("read ... takes no write lock, and path
// overlaps report as info instead of soft_block"), advertised on
// pf_predict_conflicts, pf_update_work_item, pf_create_work_item and
// pf_batch_create_work_items — and it was already implemented, once, inside
// FnAcquireLocks. The other three derivation sites each re-implemented the
// mapping without it, so a work item whose sole declared resource was
// {"type":"path","uri":"file:.gitignore","intent":"read"} had a file_scope write
// lock taken for it at claim, then 409'd somebody else, while
// pf_predict_conflicts — the pre-claim gate — reported the same input as `info`.
// Two tools, one input, opposite answers.
//
// 🔴 The read rule is deliberately scoped to file_scope, NOT applied to every
// lock type. That is a decision, not an oversight, and it is written as a
// condition on lockType rather than on res.Type so that widening it cannot
// happen by accident:
//
//   - Both halves of the contract sentence are about paths, every recorded
//     instance is a path, and pf-plan's guidance only ever teaches intent=read
//     on a `path` entry.
//   - 🔴 The open question this list used to end on — "whether intent=read
//     should mean anything at all for repo/service is genuinely undecided;
//     deciding it needs rule 2 changed in the same breath" — was DISSOLVED
//     rather than answered (aihub#416, aihub#411 row T1-8). Since repo and
//     service derive no lock at all (resourceToLock), intent cannot make a
//     difference to them under any value, so there is no longer a rule for it
//     to be inconsistent with. Rule 2 did change in the same breath, and in the
//     other direction: it stopped reading the lock table entirely.
//   - The condition therefore stays written on lockType even though file_scope
//     is now the only type it can ever see. That is deliberate: a future lock
//     type must state its own intent rule rather than inherit this one by
//     falling through, which is precisely what writing it on res.Type would
//     allow.
//
// Kept as a separate function from resourceToLock, and NOT folded into it. Note
// what that split does and does not buy: rule 3 does not call resourceToLock at
// all (it builds its comparison key straight from fileScopeLockKey), so as of
// today resourceToLock has exactly one caller and folding the check in would be
// behaviour-identical. The split is about which QUESTION each name answers —
// "what key does this map to" versus "does this take a lock" — so that the next
// caller that wants only a key does not silently inherit the lock decision, and
// so this comment has somewhere to live.
func derivedLock(res DeclaredResourceItem, project string) (lockType, lockKey string) {
	lockType, lockKey = resourceToLock(res, project)
	if lockType == "file_scope" && res.Intent == "read" {
		return "", ""
	}
	return lockType, lockKey
}

// derivedLockProbe returns the lock a declared resource takes AND the set of
// existing keys that block it. Every site that checks for a conflict before
// inserting must use this, not derivedLock plus a hand-written `resource_key=$2`:
// since aihub#261 the key a declaration WRITES and the keys that BLOCK it are
// different sets, and a site that kept the old equality test would silently stop
// seeing legacy unqualified holders.
func derivedLockProbe(res DeclaredResourceItem, project string) (lockType, lockKey string, probe lockConflictProbe) {
	lockType, lockKey = derivedLock(res, project)
	if lockType == "" {
		return "", "", lockConflictProbe{}
	}
	if lockType == "file_scope" {
		return lockType, lockKey, fileScopeConflictProbe(project, res.Repo, res.URI)
	}
	return lockType, lockKey, exactProbe(lockKey)
}

// declaredRepoDefault returns the repo that a payload's unqualified path entries
// belong to: the single repo the payload itself names, or "" when it names none
// or more than one.
//
// This is what makes aihub#261 do anything at all without a client change. No
// polyforge skill emits a `repo` field on a path entry (pf-plan Step 5 derives
// only type/uri/intent), so an explicit repo would be inert until the plugin
// ships one — which is a separate, release-gated change. A work item that
// already declares {"type":"repo","uri":"repo:X"} alongside its paths has
// unambiguously said which tree those repo-relative paths are in, and
// ieops#571 — one half of the reported collision — is exactly that shape.
//
// 🔴 Requiring EXACTLY one bounds the guess; it does not make the guess correct.
// With two or more declared repos the payload does not say which tree any given
// path belongs to, so returning "" falls back to the unqualified key, which
// conflicts with everything it used to — ambiguity resolves toward over-blocking,
// in the same direction as every other choice in lockConflictProbe.
//
// What it does NOT establish, and no code here does: that a path actually LIVES
// in the declared repo. Nothing validates that, and a `repo:` entry's real job is
// to name the branch/worktree the work item is on, not to scope every path it
// touches. A work item declaring repo:ieops-core while editing a path that lives
// in ieops-v2 gets a key naming ieops-core, which conflicts with nothing naming
// ieops-v2 — a missed conflict from a MIS-declaration, reachable with exactly one
// declared repo. The explicit per-entry `repo` field is the escape hatch for a
// payload whose paths span repos; the inference is a convenience for the common
// single-repo case, not a proof of correctness.
func declaredRepoDefault(items []DeclaredResourceItem) string {
	repo := ""
	for _, it := range items {
		if it.Type != "repo" {
			continue
		}
		name := normalizeRepo(strings.TrimPrefix(it.URI, "repo:"))
		if name == "" {
			continue
		}
		if repo != "" && repo != name {
			return "" // ambiguous: more than one repo declared
		}
		repo = name
	}
	return repo
}

// resolveDeclaredRepos fills in the implied repo on entries that did not state
// one, and is THE pre-pass every derivation site runs before mapping entries to
// locks. It is a whole-payload operation because the repo of one entry is
// carried by a different entry, which is why it cannot live inside derivedLock.
//
// An explicit per-entry `repo` wins whenever it is non-empty: a payload that
// declares repo:A but marks one path as belonging to repo:B means it. An
// explicitly empty `"repo": ""` is indistinguishable from an absent field and so
// inherits the default — Go has no way to tell the two apart here, and
// "unspecified" is the conservative reading of both.
func resolveDeclaredRepos(items []DeclaredResourceItem) []DeclaredResourceItem {
	def := declaredRepoDefault(items)
	if def == "" {
		return items
	}
	out := make([]DeclaredResourceItem, len(items))
	copy(out, items)
	for i := range out {
		if out[i].Repo == "" {
			out[i].Repo = def
		}
	}
	return out
}

// decodeDeclaredResources is the ONE decoder for a stored declared_resources
// payload: it converts to the shared item type, applies the repo pre-pass, and
// reports whether the payload could be read at all (ok=false means it is not a
// JSON array of objects).
//
// 🔴 It exists to delete two hand-written anonymous structs. FnClaimWorkItem and
// FnForceTakeover each unmarshalled into their own local struct with a hardcoded
// field list, and aihub#342's post-mortem named that as "the quietest form of
// this defect": a field absent from one of those lists never reaches the mapper,
// there is nothing to grep for, and every check downstream reads the zero value
// and passes. aihub#261 adds exactly such a field (`repo`), so the lists are
// removed rather than extended — the failure mode is designed out instead of
// being re-tested for. (FnAcquireLocks never had such a struct; it already
// decoded into DeclaredResourceItem. It uses this decoder for the repo pre-pass
// and for the permissiveness below, not to lose a field list.)
//
// 🔴 The decode is deliberately PERMISSIVE — as permissive as
// ValidateDeclaredResources — and that is a bug fix, not a style choice.
// Unmarshalling straight into []DeclaredResourceItem is STRICTER than the
// validator: the validator decodes into []map[string]any and only type-asserts
// `type` and `uri`, so an entry like
//
//	{"type":"path","uri":"file:a.go","intent":true}
//	{"type":"path","uri":"file:a.go","repo":123}
//
// passes validation and is stored with a 200, while a typed unmarshal fails on
// that one field with an UnmarshalTypeError and takes the WHOLE ARRAY down with
// it. Measured on the strict decode: a work item storing the second entry then
// claimed with NO locks at all and no error anywhere — the "fake all-clear"
// aihub#238 exists to remove, reachable straight from caller input.
//
// Every new optional field is another way to reach that state, which is why the
// permissiveness lives in the decoder and not in a rule each caller remembers:
// a wrong-typed optional field degrades to its zero value, and for `repo` the
// zero value means "unspecified", which over-blocks. Erring toward MORE keys is
// the safe direction — a key that turns out not to be held makes a release a
// no-op, whereas a missing key leaks a lock.
func decodeDeclaredResources(raw json.RawMessage) ([]DeclaredResourceItem, bool) {
	trimmed := strings.TrimSpace(string(raw))
	if trimmed == "" || trimmed == "null" {
		return nil, true
	}
	var items []map[string]any
	if err := json.Unmarshal(raw, &items); err != nil {
		return nil, false
	}
	// Convert first, THEN resolve repos: the repo an entry inherits is carried by
	// a DIFFERENT entry, so the pre-pass cannot run per item (aihub#261).
	out := make([]DeclaredResourceItem, 0, len(items))
	for _, item := range items {
		str := func(key string) string {
			s, _ := item[key].(string)
			return s
		}
		out = append(out, DeclaredResourceItem{
			Type:       str("type"),
			URI:        str("uri"),
			Repo:       str("repo"),
			Intent:     str("intent"),
			BaseBranch: str("base_branch"),
			TaskBranch: str("task_branch"),
		})
	}
	return resolveDeclaredRepos(out), true
}

// unmarshalDeclaredResources is decodeDeclaredResources for the two callers that
// must never fail on stored data: an unreadable payload yields no entries rather
// than an error, because failing would make historical work items unclaimable
// (~14% of stored entries are malformed — see ValidateDeclaredResources).
func unmarshalDeclaredResources(raw json.RawMessage) []DeclaredResourceItem {
	items, _ := decodeDeclaredResources(raw)
	return items
}

// derivedFileScopeLocks returns, for a stored declared_resources payload, a map
// from each file_scope lock key the payload justifies to the DECLARED PATH that
// key protects — and whether the payload could be read at all.
//
// aihub#264. Deriving through derivedLock rather than re-implementing the
// mapping is the point: this is compared against the CURRENT lock rows to decide
// which ones a narrowing has orphaned, so if it disagreed with the derivation
// used at acquisition by even one entry, the difference would show up as a lock
// silently released or silently kept.
//
// Inheriting the intent rule is a behavioural consequence, not an accident: an
// entry flipped from write to read maps to no lock here exactly as it maps to no
// lock at claim, so it is absent from this map and the write lock it already
// holds is released. Anything else would let intent=read enforce like
// intent=write for the lifetime of the attempt, which is the contradiction
// aihub#342 exists to remove.
//
// 🔴 The PATH in the value, not just the key, is what aihub#261 forced. The
// caller used to release `keys(prior) − keys(next)`, which silently assumed a
// derived key changes only when its path is dropped. Once the key carries a
// repo, it ALSO changes when the payload's repo inference changes with the paths
// untouched — and then every still-declared path looks removed, its lock is
// deleted, and nothing re-acquires it.
//
// That is not a corner case, it is the ordinary polyforge flow: pf-plan Step 5
// derives declared_resources from the plan's `Touched files:` lines as PATH
// ENTRIES ONLY and writes them as a whole-list replace. A work item claimed with
// a {"type":"repo"} entry holds repo-qualified locks, and the very next /pf-plan
// drops that entry. Measured on the key-subtraction version: the attempt was
// left running with ZERO file_scope locks, silently. Subtracting on the path
// makes the release immune to any future change of key FORMAT, which is the
// property that was missing rather than any particular format being wrong.
//
// ok=false means the payload is not a JSON array of objects at all. Callers must
// then release NOTHING: an unreadable declaration says nothing about which locks
// it produced, and guessing in either direction is worse than leaving the
// pre-existing rows alone.
func derivedFileScopeLocks(raw json.RawMessage, project string) (byKey map[string]string, ok bool) {
	byKey = map[string]string{}
	items, ok := decodeDeclaredResources(raw)
	if !ok {
		return nil, false
	}
	for _, res := range items {
		lockType, lockKey := derivedLock(res, project)
		if lockType == "file_scope" && lockKey != "" {
			byKey[lockKey] = fileURIToLockKey(res.URI)
		}
	}
	return byKey, true
}

// resourceToLock converts a DeclaredResourceItem to a (resource_type, resource_key) pair per §25 mapping.
// project namespaces file_scope keys (aihub#222).
//
// ⚠️ This mapper answers "which lock KEY does this resource correspond to", not
// "does this resource take a lock". It deliberately ignores Intent. If you are
// about to insert a resource_locks row or check one for a conflict, call
// derivedLock instead (aihub#342).
//
// 🔴 THREE of the six declared types map to NO lock, and repo/service joined
// external_ref there by owner ruling, not by oversight (aihub#416, the ruling in
// its attrs.owner_ruling_2026_09_07, verbatim: 「对于仓库/服务问题，我的建议是不要
// 上锁，会引入更多的问题，我们应该从逻辑层面去解决这个问题」). What each of them
// used to derive, and what replaces it:
//
//   - repo -> ("git_branch", "<repo>/<branch>"). One repo declaration took the
//     branch for the whole attempt, which serialised parallel batches that
//     touched no common file. Replaced by git itself: every work item works its
//     own task branch, and a genuine write-write collision surfaces as a
//     rejected push or a merge conflict — a stronger detector than a lock keyed
//     on a branch NAME, which is all this ever was.
//   - service -> ("deploy_env", "<service>"). One service declaration took the
//     environment until the attempt ENDED, and pause does not release it
//     (acquireLocksReleasePausedSQL deletes file_scope only), so a paused
//     observer blocked every deploy to that environment indefinitely with no
//     automatic exit — the orphan sweep does not reach a paused attempt.
//     Replaced by detection: an observer records the service generation before
//     and after, and a mismatch invalidates the OBSERVATION instead of excluding
//     everybody else. The lock could not have prevented the thing it named
//     anyway — it stopped another polyforge work item, never the person running
//     `docker run` on the same host.
//
// 🔴 WHAT DID NOT CHANGE, so a reader does not over-generalise this. `repo` and
// `service` are still LEGAL declared types (declaredResourceTypes), still
// validated for their uri scheme, and still feed PredictConflicts (rules 2, 4
// and 6, all of which now join on work_items.declared_resources rather than on
// the lock table), the timeline and deploy preflight. They are advisory, not
// ignored. And the resource_locks CHECK constraint is UNCHANGED: 'git_branch'
// and 'deploy_env' remain legal lock types, reachable through an explicit
// client-supplied requested_locks, exactly as 'worktree' and 'tcp_port' already
// were — retiring a DERIVATION is not retiring a vocabulary.
//
// ⚠️ res.TaskBranch is consequently read by nothing here. The field and its
// decode stay (DeclaredResourceItem.TaskBranch) because stored declared_resources
// are JSON and deleting a field silently drops it from every payload that
// carries one; it is withdrawn from the published schema instead, which is the
// aihub#395 base_branch precedent.
func resourceToLock(res DeclaredResourceItem, project string) (lockType, lockKey string) {
	switch res.Type {
	case "path", "document", "section":
		return "file_scope", fileScopeLockKey(project, res.Repo, res.URI)
	case "repo", "service", "external_ref":
		return "", "" // advisory declarations: no lock is derived (aihub#416)
	}
	return "", ""
}

// fileScopeLockKey builds a file_scope lock key namespaced by the owning wi's
// project and, when the declaration says so, by the repo the path is relative to:
//
//	repo == ""  ->  "<project>:<path>"
//	repo != ""  ->  "<project>:<repo>:<path>"
//
// The bare relative path alone is unsafe as a lock key — a fork repo whose paths
// are byte-identical to its parent (a different project) would share keys and
// hard-block the parent even though they are physically distinct repositories
// (aihub#222). Namespacing by project isolates them, while two wi's in the SAME
// project touching the same file still share a key and still conflict.
//
// aihub#261 adds the repo segment, because project alone has the identical
// defect one level down: declared_resources paths are REPO-relative, so in a
// multi-repo project every repo's go.mod / go.sum / Makefile / README.md /
// Dockerfile / .github/workflows/*.yml derived one key. Measured on the acquire
// path, that is a hard block — 409 CONFLICT_LOCK_TAKEN between two work items
// editing two different files (aihub#256's ieops#606 vs ieops#571).
//
// 🔴 The empty repo keeps the OLD key byte-for-byte, and that is the whole
// migration story: every row written before aihub#261 was written by a
// declaration with no repo, so the new code re-derives exactly the key already
// in the table. No resource_locks row is stranded, and no DB migration is
// required — unlike aihub#222, which had to rewrite every row in
// 0028_file_scope_project_key.sql because it changed the key unconditionally.
// See lockConflictProbe for the other half — why a finer key does not buy that
// compatibility at the cost of a missed conflict.
//
// The stranded-lock question was checked rather than assumed, because a live
// lock whose key no longer matches what the code derives is a lock nobody can
// release, which would be worse than the collision being fixed. Every release
// path keys on owner_attempt_id, not on resource_key:
//
//	run_attempts.go  claim-takeover, FnCompleteAttempt, FnForceTakeover,
//	                 acquireLocksReleasePausedSQL   -> WHERE owner_attempt_id=$1
//	gc.go            orphanLockSweepSQL             -> WHERE the owner attempt
//	                                                   is not running/paused
//
// so no key format can produce an unreleasable row. The ONE key-matching delete
// is releaseUndeclaredLocksSQL (work_items.go), the aihub#264 narrowing release,
// and it is scoped to a single work item.
//
// 🔴 The failure mode being ACCEPTED, stated plainly: an existing row is never
// UPGRADED. A lock taken as "<project>:<path>" keeps that key for the life of the
// attempt even after the same work item starts deriving a repo-qualified key.
//
// What that does and does not cost, each checked rather than assumed:
//
//   - It still blocks correctly. Every qualified probe carries the unqualified
//     key and every unqualified probe carries the any-repo LIKE, so the stale row
//     is found from both directions (lockConflictProbe).
//   - It is still released. Every terminal path deletes by owner_attempt_id, and
//     the narrowing release matches every key FORM of a dropped path
//     (releaseUndeclaredLocksSQL), so the old format is covered there too.
//   - No duplicate row appears. pf_acquire_locks probes before inserting, finds
//     the stale row is owned by this very attempt, and no-ops. An earlier version
//     of this comment claimed a second row was inserted; that was wrong, and the
//     probe is why.
//
// So the residue is a key that names less than it protects until the attempt
// ends — conservative in every direction. The alternative, rewriting live rows in
// a migration, would have to GUESS the repo for every existing declaration, and a
// wrong repo segment is a missed conflict: the one outcome worse than the bug.
//
// Only file_scope keys are namespaced, and since aihub#416 that is the whole
// story rather than one case of several: file_scope is the only lock type
// resourceToLock derives at all. A key supplied verbatim in requested_locks is
// namespaced by whoever wrote it and by nothing here.
func fileScopeLockKey(project, repo, uri string) string {
	repo = normalizeRepo(repo)
	if repo == "" {
		return project + ":" + fileURIToLockKey(uri)
	}
	return project + ":" + repo + ":" + fileURIToLockKey(uri)
}

// normalizeRepo trims a repo name before it is spliced into a lock key.
//
// Without it "repo-a " and "repo-a" key differently, so two work items editing
// the same physical file would NOT conflict — a missed conflict produced purely
// by whitespace, and the qualified probe is exact so nothing else catches it.
// A whitespace-only value must mean "unspecified" rather than a repo literally
// named " ", because "unspecified" is the conservative reading: it conflicts
// with every repo's copy of the path instead of with none of them.
func normalizeRepo(repo string) string { return strings.TrimSpace(repo) }

// lockConflictProbe is the set of EXISTING resource_locks keys that a candidate
// declaration conflicts with. It exists because "which key do I insert" and
// "which keys block me" stopped being the same question at aihub#261.
//
// Before the repo segment they were the same question: one declaration, one key,
// exact equality. Adding a segment makes keys finer, and a finer key is the
// direction that can MISS a conflict — which is worse than the false conflict
// being fixed, because a false conflict is noisy and a missed one is silent.
// Measured pre-fix (see file_scope_repo_key_db_test.go): the old coarse key
// over-blocks and never under-blocks, since resource_locks is keyed
// PRIMARY KEY (resource_type, resource_key) and one key admits one holder. So
// the fix must not introduce an under-block anywhere.
//
// A declaration with no repo means "some repo in this project, unspecified" —
// NOT "no repo". It therefore has to conflict with every repo-qualified variant
// of the same path, and a qualified declaration has to conflict with the
// unqualified (legacy) form of its own path. That yields exactly four cases, and
// only the first one changes behaviour:
//
//	qualified vs qualified, different repo -> no conflict   (the aihub#261 fix)
//	qualified vs qualified, same repo      -> conflict      (unchanged)
//	qualified vs unqualified (either order)-> conflict      (unchanged, conservative)
//	unqualified vs unqualified             -> conflict      (unchanged)
//
// so the change is a strict removal of false conflicts with no new missed ones,
// at every adoption ratio. That matters more than it looks: mixed adoption is
// the STEADY state, not a transition window. No polyforge skill emits a repo on
// a path entry today (pf-plan's Step 5 derives only
// {"type":"path","uri":...,"intent":...}), so unqualified declarations keep
// arriving indefinitely and the "transition" never ends on its own.
//
// LikePattern is the SQL half of the "any repo" case and is empty when unused.
// Matches is its Go equivalent and MUST agree with it —
// TestFileScopeRepoKey_ProbeSQLAndGoAgree runs both against the same rows for
// that reason, including keys carrying LIKE metacharacters (`_` is in almost
// every Go path in this repo, and unescaped it silently widens the pattern).
type lockConflictProbe struct {
	// Keys are exact resource_key values to test with `= ANY(...)`.
	Keys []string
	// LikePattern is an additional SQL LIKE pattern, already escaped, or "".
	LikePattern string

	anyRepoPrefix string // "<project>:" — the Go mirror of LikePattern
	anyRepoSuffix string // ":<path>"
}

// exactProbe is the whole probe for a lock type that has no qualification
// structure: one key, exact equality, as before.
//
// Since aihub#416 no DERIVED lock reaches it — file_scope is the only type
// resourceToLock produces and it has its own probe — but it stays reachable, and
// is not dead: deriveClaimLocks builds one for every client-supplied
// requested_locks entry, which is the only remaining way to take a git_branch,
// deploy_env, worktree or tcp_port row.
func exactProbe(key string) lockConflictProbe {
	return lockConflictProbe{Keys: []string{key}}
}

// fileScopeConflictProbe builds the probe for a file_scope declaration.
func fileScopeConflictProbe(project, repo, uri string) lockConflictProbe {
	path := fileURIToLockKey(uri)
	if normalizeRepo(repo) == "" {
		// "Some repo, unspecified": conflict with the legacy/unqualified key and
		// with every repo-qualified variant of the same path.
		return lockConflictProbe{
			Keys:          []string{fileScopeLockKey(project, "", uri)},
			LikePattern:   likeEscape(project) + ":%:" + likeEscape(path),
			anyRepoPrefix: project + ":",
			anyRepoSuffix: ":" + path,
		}
	}
	// A named repo: conflict with the same repo's key, and with the unqualified
	// form, which may name this very file.
	return exactProbeMulti(
		fileScopeLockKey(project, repo, uri),
		fileScopeLockKey(project, "", uri),
	)
}

func exactProbeMulti(keys ...string) lockConflictProbe {
	return lockConflictProbe{Keys: keys}
}

// Matches reports whether an existing lock key conflicts with this probe. It is
// the Go mirror of the SQL predicate in lockConflictWhereClause.
func (p lockConflictProbe) Matches(existing string) bool {
	for _, k := range p.Keys {
		if k == existing {
			return true
		}
	}
	if p.LikePattern == "" {
		return false
	}
	// Mirrors LIKE '<project>:%:<path>'. `%` matches zero or more characters, so
	// the length test is >=, matching SQL rather than requiring a non-empty repo.
	return len(existing) >= len(p.anyRepoPrefix)+len(p.anyRepoSuffix) &&
		strings.HasPrefix(existing, p.anyRepoPrefix) &&
		strings.HasSuffix(existing, p.anyRepoSuffix)
}

// Overlaps is PredictConflicts rule 3's matcher: everything Matches accepts,
// plus rule 3's looser glob/prefix semantics over the candidate's own key forms.
//
// Rule 3 is deliberately a SUPERSET of rule 1 (Matches). aihub#342 was caused by
// rule 1 and rule 3 answering a single input differently with only dry_run
// deciding which the caller saw; keeping rule 3 ⊇ rule 1 by construction means
// the advisory answer can be broader than the hard one but never contradict it.
func (p lockConflictProbe) Overlaps(existing string) bool {
	if p.Matches(existing) {
		return true
	}
	for _, k := range p.Keys {
		if globOverlap(k, existing) {
			return true
		}
	}
	return false
}

// likeEscape neutralises the LIKE metacharacters in a literal so a project name
// or path is matched as itself. `_` matters far more than it looks: it appears
// in most Go filenames in this repo (run_attempts.go, work_items.go), and
// unescaped it matches ANY single character — widening the pattern into other
// files instead of merely being untidy. Backslash is Postgres' default LIKE
// escape character and the values are passed as bind parameters, so no
// string-literal escaping is involved on top of this.
func likeEscape(s string) string {
	return strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(s)
}

// lockConflictWhereClause is the ONE SQL predicate every conflict probe uses, so
// the three call sites (PredictConflicts rule 1, FnClaimWorkItem, FnAcquireLocks)
// cannot drift apart. $1 is the resource_type, $2 the exact-key array, $3 the
// LIKE pattern, empty when unused.
const lockConflictWhereClause = `rl.resource_type = $1
	  AND (rl.resource_key = ANY($2::text[]) OR ($3 <> '' AND rl.resource_key LIKE $3))`

// fileURIToLockKey strips the "file:" scheme, yielding the bare relative path
// embedded inside a file_scope lock key. Callers namespace it via fileScopeLockKey.
func fileURIToLockKey(uri string) string {
	return strings.TrimPrefix(uri, "file:")
}

// globOverlap checks if two glob patterns (or paths) overlap.
// Simple heuristic: prefix match or exact match.
func globOverlap(a, b string) bool {
	if a == b {
		return true
	}
	if strings.HasPrefix(a, b) || strings.HasPrefix(b, a) {
		return true
	}
	// Strip ** glob suffix and check prefix
	aBase := strings.TrimSuffix(a, "/**")
	bBase := strings.TrimSuffix(b, "/**")
	return strings.HasPrefix(aBase, bBase) || strings.HasPrefix(bBase, aBase)
}
