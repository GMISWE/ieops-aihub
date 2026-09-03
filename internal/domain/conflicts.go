package domain

import (
	"context"
	"encoding/json"
	"strings"

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
	effectiveProject := req.Project
	if req.WorkItemID != nil && *req.WorkItemID != "" {
		var p string
		if lookupErr := pool.QueryRow(ctx,
			`SELECT project FROM work_items WHERE id=$1 OR slug=$1`, *req.WorkItemID,
		).Scan(&p); lookupErr == nil && p != "" {
			effectiveProject = p
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

	// Rule 2: same git_branch conflict (soft_block)
	for _, res := range resources {
		if res.Type != "repo" {
			continue
		}
		repoName := strings.TrimPrefix(res.URI, "repo:")
		rows, err := pool.Query(ctx, `
			SELECT rl.owner_attempt_id, ra.actor_display, wi.slug, wi.id
			FROM resource_locks rl
			JOIN run_attempts ra ON ra.id = rl.owner_attempt_id
			JOIN work_items wi ON wi.id = ra.work_item_id
			WHERE rl.resource_type='git_branch'
			  AND rl.resource_key LIKE $1 || '/%'
			  AND ra.status='running'`,
			repoName,
		)
		if err == nil {
			for rows.Next() {
				var ownerAttemptID, actorDisplay, wiSlug, wiID string
				if err := rows.Scan(&ownerAttemptID, &actorDisplay, &wiSlug, &wiID); err != nil {
					continue
				}
				result.Predictions = append(result.Predictions, ConflictPrediction{
					Rule:         2,
					Severity:     SeveritySoftBlock,
					Description:  "Another attempt is working on the same repo branch",
					ResourceType: "git_branch",
					AttemptID:    ownerAttemptID,
					ActorDisplay: actorDisplay,
					WISlug:       wiSlug,
					WIID:         wiID,
				})
				if result.Severity != SeverityHardBlock {
					result.Severity = SeveritySoftBlock
				}
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
			SELECT ra.actor_display, wi.slug, wi.id
			FROM work_items wi
			JOIN run_attempts ra ON ra.id = wi.current_attempt_id
			WHERE wi.status='running'
			  AND wi.declared_resources @> $1::jsonb`,
			`[{"type":"repo","uri":"repo:`+repoName+`","intent":"refactor"}]`,
		)
		if err == nil {
			for rows.Next() {
				var actorDisplay, wiSlug, wiID string
				if err := rows.Scan(&actorDisplay, &wiSlug, &wiID); err != nil {
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
				})
				if result.Severity != SeverityHardBlock {
					result.Severity = SeveritySoftBlock
				}
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
			  AND wi.declared_resources @> $1::jsonb`,
			`[{"type":"external_ref","uri":"`+res.URI+`"}]`,
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
			rows.Close()
		}
	}

	// Compute will_unlock: work items that would be unblocked if this wi completes
	if req.WorkItemID != nil && *req.WorkItemID != "" {
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
			*req.WorkItemID,
		)
		if err == nil {
			for rows.Next() {
				var item WillUnlockItem
				if err := rows.Scan(&item.ID, &item.Slug, &item.Goal); err != nil {
					continue
				}
				result.WillUnlock = append(result.WillUnlock, item)
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
//   - git_branch and deploy_env are NOT per-file exclusions that a reader can
//     harmlessly share. Dropping them for intent=read would let a second
//     attempt take a branch another attempt is on — and because both takeover
//     paths DELETE the prior attempt's locks before re-deriving, an existing
//     branch lock would be silently released on the next takeover rather than
//     merely not taken.
//   - PredictConflicts rule 2 (same-repo git_branch) has no intent check
//     either, so leaving repo alone keeps derivation and prediction in
//     agreement for repo entries. Applying the rule here and not there would
//     recreate, on `repo`, exactly the rule-1-vs-rule-3 contradiction this
//     change exists to remove. Whether intent=read should mean anything at all
//     for repo/service is genuinely undecided; deciding it needs rule 2 changed
//     in the same breath.
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
// 🔴 Requiring EXACTLY one is the safety property, not a simplification. With
// two or more declared repos the payload does not say which tree any given path
// belongs to, and guessing would produce a key naming a repo the work item may
// not be editing — a wrong key is a missed conflict, whereas returning "" falls
// back to the unqualified key, which conflicts with everything it used to.
// Ambiguity therefore resolves toward over-blocking, in the same direction as
// every other choice in lockConflictProbe.
func declaredRepoDefault(items []DeclaredResourceItem) string {
	repo := ""
	for _, it := range items {
		if it.Type != "repo" {
			continue
		}
		name := strings.TrimPrefix(it.URI, "repo:")
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
// An explicit per-entry `repo` always wins: a payload that declares repo:A but
// marks one path as belonging to repo:B means it.
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

// unmarshalDeclaredResources decodes a stored declared_resources payload into
// the shared item type and applies the repo pre-pass.
//
// 🔴 It exists to delete three hand-written anonymous structs. FnClaimWorkItem,
// FnForceTakeover and FnAcquireLocks each used to unmarshal into their own local
// struct with a hardcoded field list, and aihub#342's post-mortem named that as
// "the quietest form of this defect": a field absent from one of those lists
// never reaches the mapper, there is nothing to grep for, and every check
// downstream reads the zero value and passes. aihub#261 adds exactly such a
// field, so the lists are removed rather than extended — the failure mode is
// designed out instead of being re-tested for.
//
// Stored data is never rejected here: a payload that will not parse yields no
// entries, matching the pre-existing behaviour at all three sites, because
// failing would make historical work items unclaimable (~14% of stored entries
// are malformed — see ValidateDeclaredResources).
func unmarshalDeclaredResources(raw json.RawMessage) []DeclaredResourceItem {
	if len(raw) == 0 {
		return nil
	}
	var items []DeclaredResourceItem
	if err := json.Unmarshal(raw, &items); err != nil {
		return nil
	}
	return resolveDeclaredRepos(items)
}

// derivedFileScopeLockKeys returns the set of file_scope lock keys a stored
// declared_resources payload justifies, and whether it could be read at all.
//
// aihub#264. Deriving through derivedLock rather than re-implementing the
// mapping is the point: this set is compared against the CURRENT lock rows to
// decide which ones a narrowing has orphaned, so if it disagreed with the
// derivation used at acquisition by even one entry, the difference would show up
// as a lock silently released or silently kept. derivedLock's own doc comment
// lists the four sites that must go through it; this is the fifth caller and the
// only one asking the question in reverse ("which locks does this payload still
// justify") rather than forward ("what should I take").
//
// Inheriting the intent rule is a behavioural consequence, not an accident: an
// entry flipped from write to read maps to no lock here exactly as it maps to no
// lock at claim, so the write lock it already holds is released. Anything else
// would let intent=read enforce like intent=write for the lifetime of the
// attempt, which is the contradiction aihub#342 exists to remove.
//
// ok=false means the payload is not a JSON array of objects at all. Callers must
// then release NOTHING: an unreadable declaration says nothing about which locks
// it produced, and guessing in either direction is worse than leaving the
// pre-existing rows alone. ValidateDeclaredResources' own doc comment reports
// that roughly 14% of stored entries would fail it, so the stored side must
// never be assumed well-formed. (That figure is quoted from there, not
// re-measured here.)
//
// 🔴 The decode is deliberately as permissive as ValidateDeclaredResources', and
// that is a bug fix, not a style choice. Unmarshalling straight into
// []DeclaredResourceItem is STRICTER than the validator: the validator decodes
// into []map[string]any and only type-asserts `type` and `uri`, so an entry like
//
//	{"type":"path","uri":"file:a.go","intent":true}
//
// passes validation, while a typed unmarshal fails on `intent` with an
// UnmarshalTypeError and takes the WHOLE array down with it. Measured: with the
// strict decode, such an update narrowed declared_resources, bumped
// resources_version, returned 200 — and released nothing, which is precisely the
// aihub#264 defect this function exists to prevent, reachable straight from
// caller input. Ignoring a wrong-typed optional field instead means the entry
// still yields its key.
//
// Erring toward MORE keys is the safe direction here: a key that turns out not
// to be held makes the release a no-op, whereas a missing key leaks a lock.
func derivedFileScopeLockKeys(raw json.RawMessage, project string) (keys map[string]bool, ok bool) {
	keys = map[string]bool{}
	trimmed := strings.TrimSpace(string(raw))
	if trimmed == "" || trimmed == "null" {
		return keys, true
	}
	var items []map[string]any
	if err := json.Unmarshal(raw, &items); err != nil {
		return nil, false
	}
	// Convert first, THEN resolve repos, then derive: the repo an entry inherits
	// is carried by a different entry, so the pre-pass cannot run per item
	// (aihub#261). Deriving inside the conversion loop would have produced
	// unqualified keys here while the acquire path produced qualified ones —
	// which, for a function whose whole job is to diff derived keys against held
	// rows, means releasing nothing and orphaning the lock instead.
	converted := make([]DeclaredResourceItem, 0, len(items))
	for _, item := range items {
		str := func(key string) string {
			s, _ := item[key].(string)
			return s
		}
		converted = append(converted, DeclaredResourceItem{
			Type:       str("type"),
			URI:        str("uri"),
			Repo:       str("repo"),
			Intent:     str("intent"),
			BaseBranch: str("base_branch"),
			TaskBranch: str("task_branch"),
		})
	}
	for _, res := range resolveDeclaredRepos(converted) {
		lockType, lockKey := derivedLock(res, project)
		if lockType == "file_scope" && lockKey != "" {
			keys[lockKey] = true
		}
	}
	return keys, true
}

// resourceToLock converts a DeclaredResourceItem to a (resource_type, resource_key) pair per §25 mapping.
// project namespaces file_scope keys (aihub#222); it is ignored for git_branch/deploy_env.
//
// ⚠️ This mapper answers "which lock KEY does this resource correspond to", not
// "does this resource take a lock". It deliberately ignores Intent. If you are
// about to insert a resource_locks row or check one for a conflict, call
// derivedLock instead (aihub#342).
func resourceToLock(res DeclaredResourceItem, project string) (lockType, lockKey string) {
	switch res.Type {
	case "repo":
		repoName := strings.TrimPrefix(res.URI, "repo:")
		branch := res.TaskBranch
		if branch == "" {
			branch = "main"
		}
		return "git_branch", repoName + "/" + branch
	case "path", "document", "section":
		return "file_scope", fileScopeLockKey(project, res.Repo, res.URI)
	case "service":
		svc := strings.TrimPrefix(res.URI, "service:")
		return "deploy_env", svc
	case "external_ref":
		return "", "" // no lock for external_ref
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
// 🔴 The failure mode being ACCEPTED, stated plainly: an attempt that is already
// running across the binary swap AND declares exactly one repo holds an
// unqualified row, while a later pf_acquire_locks on the same attempt derives
// the qualified key and inserts a second row. For that attempt, and only until
// it ends or pauses, one path is covered by two rows, and a narrowing would
// release only the qualified one. Both rows are released together by
// owner_attempt_id at pause/complete/takeover, and the extra row can only
// over-block, never under-block. This is bounded, self-healing, and in the
// conservative direction; the alternative — rewriting live rows in a migration —
// would have to guess the repo for every existing declaration, which is the one
// thing that could produce a wrong key and therefore a missed conflict.
//
// Only file_scope keys are namespaced: git_branch keys are already
// repo-qualified (repo/branch), and deploy_env keys (service) are intentionally
// global so cross-project deploys to one environment still conflict.
func fileScopeLockKey(project, repo, uri string) string {
	if repo == "" {
		return project + ":" + fileURIToLockKey(uri)
	}
	return project + ":" + repo + ":" + fileURIToLockKey(uri)
}

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
// structure (git_branch, deploy_env): one key, exact equality, as before.
func exactProbe(key string) lockConflictProbe {
	return lockConflictProbe{Keys: []string{key}}
}

// fileScopeConflictProbe builds the probe for a file_scope declaration.
func fileScopeConflictProbe(project, repo, uri string) lockConflictProbe {
	path := fileURIToLockKey(uri)
	if repo == "" {
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
