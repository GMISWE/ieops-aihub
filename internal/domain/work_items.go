package domain

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

// WorkItem mirrors the work_items table row.
//
// ⚠️ RequiresHumanSession is a THREE-state column — true, false and NULL — and
// this is the authoritative note on it; the request structs below point here
// rather than repeat it.
//
// NULL is not "unset pending a default". It is the state a create reaches by
// OMITTING the field, and it has its own ready-queue segment: unclassified[] in
// GetReadyQueue, which both items[] and readyOnlyPredicate exclude. So an
// unclassified work item is never offered for unattended dispatch.
//
// It is not permanent either. FnClaimWorkItem resolves a NULL to
// defaultRequiresHumanSession (true) on the FIRST claim and writes it back,
// emitting wi_classification_resolved — see its C-R9-12 comment.
//
// 🔴 THAT WRITE IS THE ONE aihub#411 T2-9 RECORDED AS UNEXPLAINED, attributing
// it to an attrs_patch-only pf_update_work_item call. aihub#447 settled it on
// two server builds: the update writes nothing here (buildWorkItemUpdate gates
// the column behind a non-nil check) and the claim writes it every time. What
// made the misattribution cheap is that an update's REPLY carries this field
// whether or not that call touched it, so a caller cannot tell a value it read
// from a value it wrote. Do not read this field's presence in a response as
// evidence that the responding call set it.
type WorkItem struct {
	ID                   string          `json:"id"`
	Seq                  int64           `json:"seq"`
	Slug                 string          `json:"slug"`
	Project              string          `json:"project"`
	Scenario             string          `json:"scenario"`
	Goal                 string          `json:"goal"`
	Source               string          `json:"source"`
	WIType               *string         `json:"wi_type"`
	Priority             string          `json:"priority"`
	RequiresHumanSession *bool           `json:"requires_human_session"`
	Milestone            *string         `json:"milestone"`
	Labels               []string        `json:"labels"`
	Status               string          `json:"status"`
	DeclaredResources    json.RawMessage `json:"declared_resources"`
	ResourcesVersion     int             `json:"resources_version"`
	ExternalShareType    *string         `json:"external_share_type"`
	ExternalShareKey     *string         `json:"external_share_key"`
	ReporterUserID       string          `json:"reporter_user_id"`
	ReporterDisplay      string          `json:"reporter_display"`
	CurrentAttemptID     *string         `json:"current_attempt_id"`
	CurrentAttemptEpoch  int64           `json:"current_attempt_epoch"`
	ParentWorkItemID     *string         `json:"parent_work_item_id"`
	Attrs                json.RawMessage `json:"attrs"`
	Content              *string         `json:"content"`
	CreatedAt            time.Time       `json:"created_at"`
	UpdatedAt            time.Time       `json:"updated_at"`
	ClosedAt             *time.Time      `json:"closed_at"`
	// Similarity is populated only on the semantic-search path
	// (GET /v1/work_items?query=..., aihub#273): cosine similarity between the
	// query embedding and this wi's embedding. omitempty keeps every non-query
	// response byte-identical to before.
	Similarity *float64 `json:"similarity,omitempty"`
	// StepState is populated only when the caller asks for it
	// (GET /v1/work_items?include_step_state=true, aihub#280). omitempty keeps
	// every response that did not ask byte-identical to before, which is also
	// what makes "did the param take effect?" answerable by comparing the
	// response key set — the row count cannot answer it.
	//
	// Absence has THREE causes and the response cannot distinguish them:
	//  1. the caller did not ask (no include_step_state)
	//  2. the caller asked, but the wi has never been claimed, so no
	//     wi_step_state row exists
	//  3. the caller asked, the row exists, and the lookup FAILED —
	//     attachStepState is best-effort and reports only to the server's stderr
	//
	// (3) is the uncomfortable one: a transient pool exhaustion makes a claimed,
	// in-progress work item read exactly like a never-claimed one, so a consumer
	// like pf-retro would conclude "no steps ran". That is a deliberate trade —
	// failing the whole list would let include_step_state break a call that works
	// without it — but it is a real ambiguity, named here rather than left for
	// someone to discover from behaviour. Callers that must be sure should read
	// the step state through pf_get_step.
	StepState *WorkItemStepState `json:"step_state,omitempty"`
}

// WorkItemStepState mirrors the wi_step_state row for one work item, as served
// under GET /v1/work_items?include_step_state=true (aihub#280).
//
// Every field is a pointer or has a natural zero because the row's columns are
// nullable: current_step is NULL before the first start_step and again after the
// last step completes, and current_step_attempt is NULL whenever the step is
// idle. Collapsing those to "" would make "between steps" indistinguishable from
// "no such column", which is the failure class this wi exists to close.
type WorkItemStepState struct {
	WIType             *string    `json:"wi_type"`
	GraphSource        string     `json:"graph_source"`
	CurrentStep        *string    `json:"current_step"`
	CurrentStepStatus  *string    `json:"current_step_status"`
	CurrentStepAttempt *string    `json:"current_step_attempt"`
	StepStartedAt      *time.Time `json:"step_started_at"`
	Version            int64      `json:"version"`
	UpdatedAt          time.Time  `json:"updated_at"`
}

// CreateWorkItemRequest is the parsed body for POST /v1/work_items.
//
// A nil RequiresHumanSession means "omitted", which stores NULL — the third
// state, not a default of false. See WorkItem.RequiresHumanSession.
type CreateWorkItemRequest struct {
	Project              string          `json:"project"`
	Goal                 string          `json:"goal"`
	Scenario             string          `json:"scenario"`
	Priority             string          `json:"priority"`
	WIType               *string         `json:"wi_type"`
	RequiresHumanSession *bool           `json:"requires_human_session"`
	Milestone            *string         `json:"milestone"`
	Labels               []string        `json:"labels"`
	DeclaredResources    json.RawMessage `json:"declared_resources"`
	ParentWorkItemID     *string         `json:"parent_work_item_id"`
	BlockedBy            []string        `json:"blocked_by"`
	Source               string          `json:"source"`
	Attrs                json.RawMessage `json:"attrs"`
	Content              *string         `json:"content"`
	ForceCreate          bool            `json:"force_create"`
	ForceReason          string          `json:"force_reason"`
}

// UpdateWorkItemRequest is the parsed body for PATCH /v1/work_items/:id.
//
// A nil RequiresHumanSession means "leave the stored value alone", so this
// request cannot put a work item BACK to NULL; an explicit JSON null binds to
// nil and is the same no-op. Measured on two server builds by aihub#447, not
// inferred from the type. See WorkItem.RequiresHumanSession.
type UpdateWorkItemRequest struct {
	Priority             *string         `json:"priority"`
	Milestone            *string         `json:"milestone"`
	WIType               *string         `json:"wi_type"`
	RequiresHumanSession *bool           `json:"requires_human_session"`
	ReclassifyReason     *string         `json:"reclassify_reason"`
	Labels               []string        `json:"labels"`
	DeclaredResources    json.RawMessage `json:"declared_resources"`
	ResourcesVersion     *int            `json:"resources_version"`
	Attrs                json.RawMessage `json:"attrs"`
	// AttrsPatch and AttrsUnset are the aihub#288 merge path. Attrs stays a
	// whole-column REPLACE; these two are the opt-in, non-destructive
	// alternative. See buildWorkItemUpdate for the exact semantics.
	AttrsPatch       json.RawMessage `json:"attrs_patch"`
	AttrsUnset       []string        `json:"attrs_unset"`
	Goal             *string         `json:"goal"`
	GoalChangeReason *string         `json:"goal_change_reason"`
	Content          *string         `json:"content"`
}

// ReadyQueue is the SEVEN-segment LCRS response for GET /v1/work_items/ready.
//
// Seven, and the published tool description says seven, and so does the design
// doc's response sketch. Until aihub#449 those three disagreed: the struct had
// seven keys with `omitempty` on the last, so a caller saw six until something
// was stale; the schema said "LCRS (6-section)"; and the design doc drew a
// third shape, six keys plus three fields no code could produce. One count in
// three places is the whole point of that wi (aihub#411 T2-20) — a segment
// added here has to be added to both of the others in the same change, which
// internal/mcp/ready_queue_section_count_test.go is what enforces.
type ReadyQueue struct {
	Items             []ReadyItem   `json:"items"`
	Running           []RunningItem `json:"running"`
	Stalled           []StalledItem `json:"stalled"`
	Paused            []PausedItem  `json:"paused"`
	NeedsHumanSession []ReadyItem   `json:"needs_human_session"`
	Unclassified      []ReadyItem   `json:"unclassified"`

	// StaleRunning lost its `omitempty` in aihub#449, and that is a WIRE change,
	// not a comment fix: the key used to be ABSENT whenever nothing was stale and
	// is now an empty list. newReadyQueue initialises it for the same reason the
	// other six are initialised — a nil slice would marshal to `null`, a third
	// spelling of "nothing" and worse than either of the two it replaces.
	//
	// The rule this now obeys is the one request_adjusted.go states and
	// ready_queue_disclosure_test.go pins: an absent key is acceptable only while
	// its absence asserts NOTHING. That holds for RequestAdjusted below — absent
	// means "nothing about your request was changed", which an empty list says no
	// better. It never held here. An absent stale_running asserted "no work item
	// has been running untouched for 24h", an ownership reminder the Orchestrator
	// is meant to act on, while ALSO meaning "this server predates the field":
	// two meanings on one absence, one of them actionable.
	//
	// The cost was paid without a caller noticing because there was no caller:
	// measured 2026-09-08, pkg/client (`GetReadyQueue`) hands back map[string]any,
	// the MCP tool applies no projection, and the only other mention of the key in
	// the tree is tests/scenarios/e2e/E2E-16-zombie-sweep.md, which asserts on a
	// NON-empty one. The `omitempty` itself arrived with the field in aihub#36
	// with no recorded reason — it was the tail field of an additive change.
	StaleRunning []RunningItem `json:"stale_running"`
	// RequestAdjusted names the caller-supplied parameters this endpoint changed
	// on the way in — today only `max`, which newReadyQueue clamps to 200 when it
	// arrives above the ceiling and replaces with 10 when it arrives non-positive.
	//
	// aihub#432 / aihub#411 T1-12: this field is why the clamp is no longer
	// silent. It was request_adjusted's one self-declared exemption — the clamp
	// existed, ReadyQueue had nowhere to report it, and `max=5000` and `max=200`
	// returned byte-identical responses. Omitted when nothing was adjusted; see
	// request_adjusted.go for why absence rather than an empty list, and for the
	// one case this cannot report.
	RequestAdjusted []RequestAdjustment `json:"request_adjusted,omitempty"`
}

// ReadyItem is a work item in the items/needs_human_session/unclassified segments.
//
// It carried an UnblockedAt until aihub#449 deleted it (aihub#411 T2-20). No
// code in this repository ever wrote it — measured 2026-09-08, the whole tree
// held exactly one mention of the name, the field declaration itself — so it was
// published on every one of these items and populated on none. It is deleted
// rather than implemented for the reason aihub#387 gave for `non_conflicting` on
// this same tool: a to-be-built feature planned on top of a field with no writer
// is how aihub#186's orchestrator design got written against a no-op.
//
// CreatedAt keeps its `omitempty` and is deliberately NOT selected for items[]:
// that asymmetry is as designed (see the Ready Queue block in
// docs/design/polyforge-v1-design.md) and was re-ratified when aihub#401 was
// cancelled. It is not the same class as UnblockedAt — it has writers, two of
// the three segments populate it, and it is absent where it was never asked for.
type ReadyItem struct {
	ID        string  `json:"id"`
	Slug      string  `json:"slug"`
	WIType    *string `json:"wi_type"`
	Priority  string  `json:"priority"`
	Goal      string  `json:"goal"`
	CreatedAt string  `json:"created_at,omitempty"`
}

// RunningItem is a work item in the running segment.
type RunningItem struct {
	ID           string `json:"id"`
	Slug         string `json:"slug"`
	Goal         string `json:"goal"`
	OwnerDisplay string `json:"owner_display"`
	LastActiveAt string `json:"last_active_at"`
}

// StalledItem is a work item in the stalled segment.
type StalledItem struct {
	ID               string `json:"id"`
	Slug             string `json:"slug"`
	StallReason      string `json:"stall_reason"`
	StalledSince     string `json:"stalled_since"`
	LastActorDisplay string `json:"last_actor_display"`
}

// PausedItem is a work item in the paused segment.
type PausedItem struct {
	ID               string  `json:"id"`
	Slug             string  `json:"slug"`
	PausedSince      string  `json:"paused_since"`
	LastActorDisplay string  `json:"last_actor_display"`
	PauseReason      *string `json:"pause_reason,omitempty"`
}

// newWorkItemID generates a new wi_ prefixed ID.
func newWorkItemID() string {
	return NewID("wi")
}

// resolveBlockedByRef turns one `blocked_by` entry — an id or a slug — into a
// canonical work_items.id, and is the security boundary for that parameter
// (aihub#357 H1).
//
// 🔴 The visibility scope is IN THE SQL, deliberately, and must stay there. The
// property this has to hold is not "refuse the caller", it is that a work item
// the caller cannot see is INDISTINGUISHABLE from one that does not exist: same
// status, same code, same message. Expressed as a WHERE clause there is exactly
// one outcome for both — no row — so no later branch can grow a second answer.
// Written as `resolve, then check, then return a different error` it would take
// one refactor to leak again, and the leak would be invisible in review.
//
// Why it matters here specifically: before aihub#357 `blocked_by` took only
// canonical ids, which are unguessable. Accepting slugs makes the identifier
// `<project>#<seq>`, a two-token namespace anyone can walk, so any per-entry
// answer that varies with existence enumerates every project on the server.
// Measured on the pre-fix tree with a caller holding no role on project B:
// `blocked_by:["<B>#2"]` answered 201 and wrote a real edge into B, while
// `blocked_by:["<B>#9999"]` answered 404 — a clean one-bit oracle for a caller
// who is 403'd on every honest read of B.
//
// Scope, matching CreateDependency's policy: the work item's own project (the
// caller must already hold writer on it to be creating anything), plus every
// project the caller holds any role in, and everything for an admin — whose
// ProjectRoles map is empty by design, hence the separate flag (aihub#227).
//
// ✅ The two are now ALIGNED, at 404 (aihub#377). This paragraph used to read:
//
//	⚠️ This DIVERGES from CreateDependency, which answers 403 and names the
//	project for the cross-project case. That is an existence leak of the same
//	kind, and it is left alone on purpose: it is reachable only with a canonical
//	id, so it is not enumerable, and changing an authorization response shape is
//	not this work item's business. Do not "align" the two by copying the 403 back
//	here — that would reinstate the oracle.
//
// Two things about that text, kept because both are instructive:
//
//   - Its closing instruction was correct and is still in force. Alignment went
//     the other way: handleCreateDependency now answers the shared 404
//     (errNotVisible) instead of 403. Copying a 403 back HERE would still
//     reinstate the oracle. Do not.
//
//   - Its stated reason for deferring was FALSE, not merely narrow. "Reachable
//     only with a canonical id, so it is not enumerable" — but the deferred path
//     resolves through GetWorkItem, which accepts a slug, so a slug worked there
//     exactly as it works here. The leak this comment called unenumerable was
//     enumerable the whole time, by the same <project>#<seq> walk. A deferral
//     argument is a claim like any other and wants the same measurement as the
//     fix it defers.
//
//     ⚠️ This bullet used to justify itself with "whose WHERE clause is
//     `id = $1 OR slug = $1`", and that was not true when it was written
//     (aihub#402): GetWorkItem dispatched on a `wi_` prefix and queried ONE
//     column. The conclusion held anyway — the else branch matched on slug, so a
//     slug did resolve — but the mechanism cited for it did not exist, which is
//     the failure mode this very bullet is about, one paragraph up, in the same
//     comment. aihub#402 made the clause true; the wording no longer leans on
//     it, so the two cannot come apart again.
//
// "Changing an authorization response shape is not this work item's business" was
// fair — it was aihub#377's business.
func resolveBlockedByRef(ctx context.Context, tx pgx.Tx, ref, ownProject string,
	callerProjectRoles map[string]string, callerRole string) (string, *AihubError) {
	return resolveVisibleRefOnTx(ctx, tx, "blocked_by", ref, ownProject, callerProjectRoles, callerRole)
}

// resolveVisibleRefOnTx is the shared body of the above and of
// parent_work_item_id's resolution (aihub#396), which is a REFERENCE FIELD ON THE
// SAME ROW with the same identifier namespace and therefore the same oracle.
//
// ⚠️ Shared deliberately, and not merely to avoid two copies of a query. The
// property being protected is the one the header above spends thirty lines on:
// scope lives in the WHERE clause, and a hidden work item is reported exactly
// like an absent one. A second hand-written copy of that query is how a field
// gets slug resolution and loses the property — which is what aihub#357 shipped.
// Whatever `field` is passed, both fields now answer through one statement, so
// they cannot come apart.
//
// `field` is a caller-supplied parameter NAME chosen at the call site, never a
// value read from the database, and it appears only in the message. So it adds
// nothing derived from the row and cannot widen what the error discloses.
func resolveVisibleRefOnTx(ctx context.Context, tx pgx.Tx, field, ref, ownProject string,
	callerProjectRoles map[string]string, callerRole string) (string, *AihubError) {

	visible := make([]string, 0, len(callerProjectRoles))
	for p, role := range callerProjectRoles {
		if role != "" {
			visible = append(visible, p)
		}
	}

	// Resolved on `tx` rather than through GetWorkItem's pool: taking a second
	// pool connection while holding one is how a small MaxConns deadlocks. This
	// is also why ResolveVisibleWorkItemRef — same query, same property — is not
	// reused here: its signature takes a pool.
	var id string
	err := tx.QueryRow(ctx, `
		SELECT id FROM work_items
		WHERE (id = $1 OR slug = $1)
		  AND (project = $2 OR $3 OR project = ANY($4))`,
		ref, ownProject, callerRole == "admin", visible,
	).Scan(&id)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			// The single answer for "no such work item" AND "not yours to see".
			// Echoing the caller's own reference back is not a leak; anything
			// derived from the row would be.
			return "", NewErr(ErrNotFound,
				fmt.Sprintf("%s references work item %q, which does not exist", field, ref))
		}
		return "", dbErrCause(err, fmt.Sprintf("failed to resolve %s entry %s", field, ref))
	}
	return id, nil
}

// CreateWorkItem inserts a new work item atomically.
// Applies classification_rules from scenario_phase_configs, runs dedup, and
// inserts wi_dependencies for blocked_by entries.
//
// callerProjectRoles / callerRole scope which work items `blocked_by` may name;
// see resolveBlockedByRef, where they are the security boundary rather than a
// convenience. They are real parameters and not fields on the request struct on
// purpose: the request is bound from the wire, so a field could be supplied by
// the caller, and the compiler cannot notice a caller that forgets to overwrite
// it. Passing nil and "" means "no project beyond req.Project is visible",
// which is the safe reading and what every non-HTTP caller wants.
func CreateWorkItem(ctx context.Context, pool *pgxpool.Pool, req *CreateWorkItemRequest, callerUserID, callerDisplay string,
	callerProjectRoles map[string]string, callerRole string) (*WorkItem, *AihubError) {
	// Validate goal. aihub#507: the required-ness check moved into
	// validateWorkItemGoalPresent — byte-identical behaviour, same code, same
	// message, same position ahead of the shape check, and deliberately so. It is
	// a move rather than a change because UpdateWorkItem now calls the same
	// function, and a refusal both doors must agree on cannot be a literal typed
	// once per door.
	if vErr := validateWorkItemGoalPresent(req.Goal); vErr != nil {
		return nil, vErr
	}
	if vErr := validateWorkItemGoalShape(req.Goal); vErr != nil {
		return nil, vErr
	}
	if req.Project == "" {
		return nil, NewErr(ErrBadRequest, "project is required")
	}

	// Defaults
	if req.Scenario == "" {
		req.Scenario = "coding"
	}
	if req.Priority == "" {
		req.Priority = "normal"
	}
	if req.Source == "" {
		req.Source = "human"
	}
	if req.Labels == nil {
		req.Labels = []string{}
	}
	if len(req.DeclaredResources) == 0 {
		req.DeclaredResources = json.RawMessage("[]")
	}
	// aihub#238: reject resource types the lock mapper cannot understand at the
	// point of entry. Before this, a mistyped entry was stored happily, showed up
	// in the UI as "resources declared", and acquired no lock — the wi looked
	// guarded and was not.
	if aihubErr := ValidateDeclaredResources(req.DeclaredResources); aihubErr != nil {
		return nil, aihubErr
	}
	// aihub#396: the vocabularies and limits the DB CHECKs enforce, checked here
	// so a caller mistake is a 400 naming the field instead of a 500 carrying a
	// SQLSTATE. See internal/domain/work_item_fields.go for the policy and for
	// why "the constraint already catches it" is not an answer.
	//
	// Placed AFTER the defaults above (so an omitted field is not read as an
	// illegal one) and BEFORE the embedding call below, which is a network
	// round-trip: rejecting a doomed request should not first spend an embedding
	// on it.
	if aihubErr := validateWorkItemPriority(req.Priority); aihubErr != nil {
		return nil, aihubErr
	}
	if aihubErr := validateWorkItemSource(req.Source); aihubErr != nil {
		return nil, aihubErr
	}
	if aihubErr := validateWorkItemLabels(req.Labels); aihubErr != nil {
		return nil, aihubErr
	}
	if aihubErr := validateWorkItemContent(req.Content); aihubErr != nil {
		return nil, aihubErr
	}
	// aihub#465: the same shape guard the PATCH path applies. Above the default
	// below, so an omitted attrs is still `{}` rather than a rejection, and
	// above the embedding call further down for the reason the aihub#396 block
	// just gave: a doomed request should not first spend a network round-trip.
	if aihubErr := validateJSONObjectParam("attrs", req.Attrs); aihubErr != nil {
		return nil, aihubErr
	}
	if len(req.Attrs) == 0 {
		req.Attrs = json.RawMessage("{}")
	}

	// Reject unimplemented scenarios
	if req.Scenario != "coding" {
		return nil, NewErr(ErrNotImplemented, fmt.Sprintf("scenario %q is not yet implemented", req.Scenario))
	}

	// aihub#273: compute the embedding before the transaction below begins —
	// it is a network call and must never run inside an open tx (same rule as
	// Remember, aihub#192). Best-effort: failure leaves the emb_* columns NULL
	// and the wi is still findable via the ILIKE text fallback.
	wiContent := ""
	if req.Content != nil {
		wiContent = *req.Content
	}
	embVecLit, embModel, embDims, embEmbeddedLen := embedWorkItemBestEffort(ctx, req.Goal, wiContent)

	// aihub#316: sampled BEFORE pool.Begin, and that ordering is the whole
	// point. Reading ctx.Err() AFTER Begin returns cannot tell "the context was
	// already dead when we called Begin" (embedding ate the budget — the
	// aihub#316 shape) from "the context died WHILE Begin was blocked waiting
	// for a free connection" (pool exhaustion — a database-side problem).
	// Blaming the embedding provider for the second case would be the same
	// mis-attribution this whole branch exists to remove, just pointing the
	// other way: measured with embedding switched off entirely and MaxConns=1
	// held busy, the after-the-fact read produced "an upstream dependency (most
	// likely the embedding provider) consumed the request budget" for a request
	// that had no upstream dependency at all.
	ctxDeadBeforeDB := ctx.Err()

	tx, err := pool.Begin(ctx)
	if err != nil {
		// The three cases get three different sentences, because each sends the
		// reader somewhere different. The original single "failed to begin
		// transaction" is how the 2026-09-01 investigation spent two passes on a
		// database that was answering in 9ms.
		switch {
		case errors.Is(ctxDeadBeforeDB, context.DeadlineExceeded):
			return nil, NewErr(ErrInternalError,
				"request deadline exhausted before reaching the database; an upstream dependency (most likely the embedding provider) consumed the request budget")
		case ctxDeadBeforeDB != nil:
			// Canceled: the caller hung up. Saying "deadline exhausted" here
			// would send the next reader hunting a slow dependency that was
			// never slow.
			return nil, NewErr(ErrInternalError,
				"request was cancelled before reaching the database; the caller disconnected upstream of any database work")
		case ctx.Err() != nil:
			// Alive on entry, dead now: the time went INSIDE Begin, i.e.
			// waiting for a pool connection. Name the pool, not the upstream.
			return nil, NewErr(ErrInternalError,
				"request deadline expired while waiting for a database connection; the connection pool is saturated, not an upstream dependency")
		default:
			return nil, NewErr(ErrInternalError, "failed to begin transaction")
		}
	}
	defer tx.Rollback(ctx) //nolint:errcheck

	// wi_type and requires_human_session are provided by the caller directly.
	// scenario_phase_configs has been removed; classification is handled client-side
	// using the local scenario clone (scenario_ref).
	wiType := req.WIType
	requiresHumanSession := req.RequiresHumanSession

	// Dedup check (skip if force_create)
	if !req.ForceCreate {
		if aihubErr := checkDedup(ctx, tx, req); aihubErr != nil {
			return nil, aihubErr
		}
	} else if req.ForceReason == "" || len(req.ForceReason) < 10 {
		return nil, NewErr(ErrBadRequest, "force_reason is required and must be at least 10 characters when force_create=true")
	}

	// aihub#396: resolve parent_work_item_id, which used to travel straight into
	// the INSERT. The column is `TEXT REFERENCES work_items(id)`, so a slug — or
	// an id that does not exist — was a 23503 foreign-key violation surfacing as
	// 500 INTERNAL_ERROR. Its immediate neighbour `blocked_by` has accepted an id
	// OR a slug, scoped to what the caller can see, with a 404 on a miss, since
	// aihub#357; the two are reference fields on the same row and there was no
	// reason for them to disagree.
	//
	// Through the SAME resolver as blocked_by, not a copy of it: slug resolution
	// makes the identifier the walkable `<project>#<seq>` namespace, so any
	// per-entry answer that varies with existence enumerates every project on the
	// server. resolveVisibleRefOnTx keeps the scope in the WHERE clause and gives
	// one answer for "absent" and "not yours".
	//
	// ⚠️ A blank value is folded to ABSENT rather than skipped. Skipping it would
	// pass "" (or "   ") through to a column that is a foreign key, which is a
	// 23503 and therefore the very 500 this block removes — an easy hole to leave
	// behind when adding a `!= ""` guard, because the guard reads as "nothing to
	// do here".
	parentID := req.ParentWorkItemID
	if parentID != nil {
		trimmed := strings.TrimSpace(*parentID)
		if trimmed == "" {
			parentID = nil
		} else {
			resolved, aihubErr := resolveVisibleRefOnTx(ctx, tx, "parent_work_item_id",
				trimmed, req.Project, callerProjectRoles, callerRole)
			if aihubErr != nil {
				return nil, aihubErr
			}
			parentID = &resolved
		}
	}

	// Get next seq from projects table (UPDATE must be last write in tx to minimize row lock duration)
	// This is deferred to after the INSERT; we do it here to fail fast on FK violation.
	var seq int64
	err = tx.QueryRow(ctx,
		`UPDATE projects SET wi_seq = wi_seq + 1 WHERE name = $1 RETURNING wi_seq`,
		req.Project,
	).Scan(&seq)
	if err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23503" {
			return nil, NewErr(ErrProjectNotFound, fmt.Sprintf("project %q not found", req.Project))
		}
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, NewErr(ErrProjectNotFound, fmt.Sprintf("project %q not found", req.Project))
		}
		return nil, dbErrCause(err, "increment wi_seq")
	}

	wiID := newWorkItemID()

	_, err = tx.Exec(ctx, `
		INSERT INTO work_items (
			id, seq, project, scenario, goal, source, wi_type, priority,
			requires_human_session, milestone, labels, status,
			declared_resources, reporter_user_id, reporter_display,
			parent_work_item_id, attrs, content,
			emb_model, emb_dims, emb_vector, embedded_len
		) VALUES (
			$1, $2, $3, $4, $5, $6, $7, $8,
			$9, $10, $11, 'queued',
			$12, $13, $14,
			$15, $16, $17,
			$18, $19, $20::vector, $21
		)`,
		wiID, seq, req.Project, req.Scenario, req.Goal, req.Source, wiType, req.Priority,
		requiresHumanSession, req.Milestone, req.Labels, req.DeclaredResources,
		callerUserID, callerDisplay, parentID, req.Attrs, req.Content,
		embModel, embDims, embVecLit, embEmbeddedLen,
	)
	if err != nil {
		return nil, dbErrCause(err, "failed to insert work_item")
	}

	// Emit work_item_filed event
	evtID := NewID("evt")
	evtPayload, _ := json.Marshal(map[string]any{
		"source":       req.Source,
		"project":      req.Project,
		"work_item_id": wiID,
		"goal":         req.Goal,
	})
	_, err = tx.Exec(ctx, `
		INSERT INTO agent_events (id, work_item_id, actor_user_id, actor_display, event_type, payload, project)
		VALUES ($1, $2, $3, $4, 'work_item_filed', $5, $6)`,
		evtID, wiID, callerUserID, callerDisplay, evtPayload, req.Project,
	)
	if err != nil {
		return nil, dbErr(err, "failed to emit work_item_filed event")
	}

	// Insert blocked_by dependencies, one 'blocks' edge and one
	// dependency_created event per entry (aihub#357).
	//
	// The event is what makes the blocking relationship machine-readable: before
	// it, the only trace a blocked_by left on the timeline was the
	// work_item_filed above, so "what is blocking this" lived nowhere but the
	// prose in wi.content. It is written inside this transaction and its failure
	// is propagated, exactly like the work_item_filed insert two statements up —
	// an edge whose creation left no trace is as unreadable as no edge.
	//
	// 🔴 Deduplicated by RESOLVED id, not by the string the caller wrote. Since
	// an entry may be an id or a slug, `[wi_x, <wi_x's slug>]` names one blocker
	// twice, and wi_dependencies' primary key is
	// (blocked_wi_id, blocking_wi_id, kind) — so an unguarded second INSERT is a
	// 23505 surfaced verbatim as `500 ... duplicate key value violates unique
	// constraint "wi_dependencies_pkey"`. Deduplicating rather than adding
	// `ON CONFLICT DO NOTHING`: the latter would silence the 500 and still emit
	// two dependency_created events for one edge, reporting a blocker twice on
	// the timeline this work item exists to make trustworthy.
	//
	// No ON CONFLICT is needed on top of it. wiID was generated moments ago in
	// this transaction, so no wi_dependencies row can already reference it and
	// the only reachable duplicate is the in-request one handled here.
	seen := make(map[string]bool, len(req.BlockedBy))
	for _, blockingRef := range req.BlockedBy {
		blockingID, aihubErr := resolveBlockedByRef(ctx, tx, blockingRef, req.Project, callerProjectRoles, callerRole)
		if aihubErr != nil {
			return nil, aihubErr
		}
		if blockingID == wiID {
			// Unreachable today (the new id cannot be named by the caller) but
			// asserted rather than assumed: wi_dependencies has a
			// blocked_wi_id != blocking_wi_id CHECK, and tripping it here would
			// surface as an INTERNAL_ERROR instead of a bad request.
			return nil, NewErr(ErrBadRequest, "a work item cannot block itself")
		}
		if seen[blockingID] {
			continue
		}
		seen[blockingID] = true

		_, err = tx.Exec(ctx, `
			INSERT INTO wi_dependencies (blocked_wi_id, blocking_wi_id, kind, created_by)
			VALUES ($1, $2, 'blocks', $3)`,
			wiID, blockingID, callerUserID,
		)
		if err != nil {
			return nil, dbErrCause(err, fmt.Sprintf("failed to create dependency for blocking_wi %s", blockingID))
		}

		if err = emitDependencyCreatedEvent(ctx, tx, wiID, blockingID, "blocks",
			req.Project, callerUserID, callerDisplay, "create_work_item.blocked_by"); err != nil {
			return nil, dbErrCause(err, fmt.Sprintf("failed to record dependency_created for blocking_wi %s", blockingID))
		}
	}

	// If blocked_by is non-empty, set status to blocked
	if len(req.BlockedBy) > 0 {
		_, err = tx.Exec(ctx, `UPDATE work_items SET status='blocked' WHERE id=$1`, wiID)
		if err != nil {
			return nil, dbErr(err, "failed to set blocked status")
		}
	}

	if err := tx.Commit(ctx); err != nil {
		if aerr := retryConflictErr(err, "failed to commit transaction"); aerr != nil { // aihub#334
			return nil, aerr
		}
		return nil, NewErr(ErrInternalError, "failed to commit transaction")
	}

	return GetWorkItem(ctx, pool, wiID)
}

// jaccardNGram computes a simple n-gram Jaccard similarity between two strings.
func jaccardNGram(a, b string, n int) float64 {
	setA := ngrams(a, n)
	setB := ngrams(b, n)
	if len(setA) == 0 && len(setB) == 0 {
		return 1.0
	}
	intersection := 0
	for g := range setA {
		if setB[g] {
			intersection++
		}
	}
	union := len(setA) + len(setB) - intersection
	if union == 0 {
		return 0
	}
	return float64(intersection) / float64(union)
}

func ngrams(s string, n int) map[string]bool {
	s = strings.ToLower(s)
	out := make(map[string]bool)
	runes := []rune(s)
	for i := 0; i+n <= len(runes); i++ {
		out[string(runes[i:i+n])] = true
	}
	return out
}

// setOverlap computes the Jaccard index |A∩B|/|A∪B| over the sets obtained by
// DE-DUPLICATING both a and b. Both sides must be deduplicated before
// intersection and union are computed, so duplicate entries in either input
// can never push the ratio above 1.0.
//
// aihub#251 defect 1: the previous version only deduplicated side a into a
// set, then counted the intersection by iterating the RAW (non-deduplicated)
// slice b and sized the union from raw slice lengths (len(a)+len(b)). A
// duplicate-laden b (e.g. an existing candidate's stored labels containing
// the same label 5+ times) could then push intersection above len(a) and the
// ratio arbitrarily far past 1.0 -- the reported ">100% similar" scores.
//
// Empty-case semantics: both-empty returns 0, not 1. "Neither side declared
// anything" is an ABSENCE of evidence, not evidence of similarity -- treating
// it as a perfect match manufactured a constant score bonus for every
// candidate that also happened to have no labels/resources, which was a
// second major driver of the false-positive collisions (aihub#251).
func setOverlap(a, b []string) float64 {
	setA := make(map[string]bool, len(a))
	for _, v := range a {
		setA[v] = true
	}
	setB := make(map[string]bool, len(b))
	for _, v := range b {
		setB[v] = true
	}
	if len(setA) == 0 && len(setB) == 0 {
		return 0
	}
	intersection := 0
	for v := range setA {
		if setB[v] {
			intersection++
		}
	}
	union := len(setA) + len(setB) - intersection
	if union == 0 {
		return 0
	}
	return float64(intersection) / float64(union)
}

// weightedComponent decides whether one non-goal dedup dimension (labels or
// resources) is APPLICABLE and, if so, what it scores.
//
// aihub#251 review follow-up (mem_veTEPhFm, WARN): a dimension is
// inapplicable ONLY when BOTH sides are empty -- neither side offers any
// evidence at all, so it should carry no weight in the composite score
// rather than being scored a hard 0 (which structurally capped the score at
// 0.6 whenever neither side declared labels/resources, even for a
// byte-identical goal). When exactly ONE side is empty and the other is not,
// that IS genuine evidence of difference -- the dimension is applicable and
// legitimately contributes a real 0, not a dropped weight.
//
// setOverlap alone cannot distinguish these two cases: it returns 0 for both
// the both-empty and the one-empty shape. That distinction has to be made
// here, from the raw (pre-overlap) inputs, before calling setOverlap.
func weightedComponent(a, b []string, weight float64) (score float64, appliedWeight float64) {
	if len(a) == 0 && len(b) == 0 {
		return 0, 0
	}
	return setOverlap(a, b), weight
}

// declaredResourceKeys parses a declared_resources JSON payload into a slice
// of canonical per-entry keys suitable for setOverlap. It handles both the
// current entry shape ({"type":"path","uri":"file:...","intent":"write"}) and
// the legacy pre-aihub#238 shape that may still be stored on old rows
// ({"type":"file_scope","value":"..."} -- see
// TestCreateWorkItem_RejectsUnknownTypeBeforeTouchingDB in
// declared_resources_wiring_test.go, which confirms new requests can no
// longer create this shape but says nothing about what is already stored).
// Entries matching neither shape are skipped rather than causing a crash or
// being silently treated as a match.
//
// ok is false ONLY when raw is non-empty and is not parseable as a JSON array
// of objects -- a genuine parse failure. It is true (with a nil/empty key
// slice) for absent, empty-array, or null input, since declaring no resources
// at all is not a parse error.
//
// aihub#251 defect 2: the previous code unmarshalled this same object-array
// JSON directly into []string, which always fails for object entries; the
// error was discarded (`_ = json.Unmarshal(...)`), leaving both sides nil.
// setOverlap(nil, nil) then hit its both-empty branch (formerly 1.0),
// silently adding a constant +0.2 to the composite score for every candidate
// regardless of whether resources actually matched. Callers here MUST treat
// ok=false as "not comparable" (contribute no similarity), never as a match.
func declaredResourceKeys(raw json.RawMessage) (keys []string, ok bool) {
	trimmed := strings.TrimSpace(string(raw))
	if trimmed == "" || trimmed == "null" || trimmed == "[]" {
		return nil, true
	}
	var items []map[string]any
	if err := json.Unmarshal(raw, &items); err != nil {
		return nil, false
	}
	keys = make([]string, 0, len(items))
	for _, item := range items {
		typ, _ := item["type"].(string)
		if typ == "" {
			continue
		}
		if uri, ok := item["uri"].(string); ok && uri != "" {
			keys = append(keys, typ+":"+uri)
			continue
		}
		if val, ok := item["value"].(string); ok && val != "" {
			keys = append(keys, typ+":"+val)
			continue
		}
		// Matches neither the current (`uri`) nor legacy (`value`) shape --
		// skip it rather than crash or let it silently vanish into a
		// both-empty comparison.
	}
	return keys, true
}

// candidateScore computes the F3 composite dedup similarity score for one
// existing candidate work item against an incoming create request. It has no
// DB access, so it is directly unit-testable without a live work_items table
// (checkDedup itself needs AIHUB_TEST_DB; the scoring math does not).
//
// The nominal weights are goal 0.6 / labels 0.2 / resources 0.2. Goal
// similarity is always applicable (Goal is a required non-empty field --
// CreateWorkItem rejects an empty goal before dedup ever runs). Labels and
// resources are each applicable UNLESS both sides are empty for that
// dimension (weightedComponent decides this from the raw inputs -- see its
// doc comment). An inapplicable dimension drops its weight entirely rather
// than contributing a hard 0, and the remaining applicable weights are
// renormalized to sum to 1.0, so an absent dimension neither manufactures a
// spurious match (the pre-aihub#251 bug) nor dilutes the score toward zero
// for the common case of a minimal work item with no labels/resources at all
// (aihub#251 review follow-up, mem_veTEPhFm WARN finding).
//
// An unparseable declared_resources payload on either side (reqOK or cOK
// false) is treated as APPLICABLE-but-zero, not inapplicable: malformed JSON
// in a stored column is abnormal/corrupt data, not mere absence of
// information, so it must never be dropped in a way that could inflate the
// score -- it stays a real, weighted 0 (matches the existing "unparseable
// never counts as a match" rule from defect 2).
//
// valid is false if the computed score falls outside [0,1] -- structurally
// this should still be impossible, since sim/labelScore/resScore are each
// bounded to [0,1] and the composite is a weighted average over weights that
// are re-scaled to sum to totalWeight (never zero: goalWeight alone is
// 0.6), but this is a defensive backstop (aihub#251 defect 3): an
// out-of-range score is a programming bug in one of the sub-scores, not
// something that should ever be compared to the 0.90/0.65 thresholds or
// formatted into a user-facing ">100% similar" string. Callers must fail
// OPEN on valid=false (skip this candidate), consistent with the existing
// "dedup is best-effort" philosophy applied when the candidate query itself
// fails.
func candidateScore(req *CreateWorkItemRequest, goal string, labels []string, resources json.RawMessage) (score float64, valid bool) {
	sim := jaccardNGram(req.Goal, goal, 3)

	labelScore, labelWeight := weightedComponent(req.Labels, labels, 0.2)

	reqRes, reqOK := declaredResourceKeys(req.DeclaredResources)
	cRes, cOK := declaredResourceKeys(resources)
	var resScore, resWeight float64
	if !reqOK || !cOK {
		resScore, resWeight = 0, 0.2
	} else {
		resScore, resWeight = weightedComponent(reqRes, cRes, 0.2)
	}

	const goalWeight = 0.6
	totalWeight := goalWeight + labelWeight + resWeight
	if totalWeight <= 0 {
		// Unreachable today: goalWeight alone keeps totalWeight >= 0.6,
		// since Goal is required non-empty. Guarded anyway rather than ever
		// dividing by zero if that invariant is ever relaxed.
		return 0, false
	}

	score = (goalWeight*sim + labelWeight*labelScore + resWeight*resScore) / totalWeight
	if score < 0 || score > 1 {
		return score, false
	}
	return score, true
}

// checkDedup performs the F3 dedup check within a transaction.
func checkDedup(ctx context.Context, tx pgx.Tx, req *CreateWorkItemRequest) *AihubError {
	// Pass req.Labels as []string so pgx serializes it as a proper PostgreSQL text[]
	// (not JSON "[]" which cannot be cast with ::text[]).
	labels := req.Labels
	if labels == nil {
		labels = []string{}
	}

	// When labels is empty we rely only on goal similarity and resource overlap.
	// Don't use labels && $2 when $2 is empty — that would give a type-cast error.
	// When declared_resources is empty [], @> $3::jsonb is trivially true for every row,
	// so we guard with a non-empty check.
	//
	// aihub#628: both queries ORDER BY before their LIMIT 50, so on a project
	// with more than 50 live work items the rows that get truncated away are
	// the least relevant by a DETERMINISTIC key, not whichever rows the heap
	// happened to return first. The composite similarity score itself (goal
	// 3-gram Jaccard, candidateScore) is computed in Go and has no SQL
	// counterpart here -- no pg_trgm in the migrations, and the pgvector
	// embeddings are written asynchronously after creation, so the incoming
	// request has nothing to compare against inside this transaction. The
	// ORDER BY therefore ranks by the closest deterministic proxies instead:
	//
	//   - label overlap count (labeled branch only): set-semantics
	//     intersection size with the request's labels -- the direct analogue
	//     of the score's 0.2-weight label component, and the predicate that
	//     admitted the row in the first place;
	//   - resource containment (labeled branch only): the other admission
	//     predicate, a coarse proxy for the 0.2-weight resource component
	//     (COALESCEd to 0 so a NULL $3 cannot sort NULLS FIRST above real
	//     matches);
	//   - seq DESC (both branches): recency. Duplicates cluster in time --
	//     the typical collision is a re-file of something recent, not of the
	//     oldest paused item in the project. seq is immutable and unique per
	//     project (slug = project || '#' || seq is UNIQUE), so it also makes
	//     the whole ordering total: same table state, same 50 rows, always.
	//
	// The trade-off, stated plainly: a goal-similar candidate with no label or
	// resource overlap and a low seq can still be truncated away on a project
	// with >50 live work items. Ranking by the real goal similarity in SQL
	// would need pg_trgm or a synchronous embedding, both out of scope here;
	// recency is the best deterministic stand-in this schema offers.
	var rows pgx.Rows
	var err error
	if len(labels) == 0 {
		// No labels: only filter by goal similarity (done in Go) + resource overlap (if any)
		rows, err = tx.Query(ctx, `
			SELECT id, slug, goal, labels, declared_resources, status
			FROM work_items
			WHERE project = $1
			  AND status IN ('queued','running','paused','blocked')
			ORDER BY seq DESC
			LIMIT 50`,
			req.Project,
		)
	} else {
		rows, err = tx.Query(ctx, `
			SELECT id, slug, goal, labels, declared_resources, status
			FROM work_items
			WHERE project = $1
			  AND status IN ('queued','running','paused','blocked')
			  AND (labels && $2::text[] OR declared_resources @> $3::jsonb)
			ORDER BY cardinality(ARRAY(SELECT UNNEST(labels) INTERSECT SELECT UNNEST($2::text[]))) DESC,
			         COALESCE((declared_resources @> $3::jsonb)::int, 0) DESC,
			         seq DESC
			LIMIT 50`,
			req.Project, labels, req.DeclaredResources,
		)
	}
	if err != nil {
		// Dedup is best-effort; if the query fails, allow creation. aihub#522
		// verified this is a DELIBERATE discard, not a missed check (the aihub#500
		// census flagged both branch assignments above as NO-CHECK because the
		// guard sits after the if/else, not inside it) — with one carve-out,
		// symmetric with the rows.Err() arm below: a class-40 rollback has
		// already killed the transaction CreateWorkItem is holding, so "allow
		// creation" is not a fallback there, it is the caller being told 500 at
		// a later statement with the SQLSTATE gone (aihub#492 / bestEffortExec).
		if aerr := retryConflictErr(err, "failed to query dedup candidates"); aerr != nil {
			return aerr
		}
		return nil
	}
	defer rows.Close()

	type candidate struct {
		ID         string
		Slug       string
		Goal       string
		Labels     []string
		Resources  json.RawMessage
		Status     string
		Similarity float64
	}

	var partials []candidate
	for rows.Next() {
		var c candidate
		var labelsRaw []string
		if scanErr := rows.Scan(&c.ID, &c.Slug, &c.Goal, &labelsRaw, &c.Resources, &c.Status); scanErr != nil {
			// aihub#608: a deliberate discard, same policy as the send-time arm
			// above — dedup is best-effort. On pgx v5 a failed Scan poisons the
			// rows, so this continue reaches Next()==false and the rows.Err()
			// arm below, which applies the documented policy: class 40 returns
			// (the transaction is dead), anything else allows creation — the
			// fail-OPEN direction this function's every failure already takes.
			// Allowlisted in internal/citest/rowserr/scan_swallow_allowlist.txt.
			continue
		}
		c.Labels = labelsRaw

		// C7: include resource overlap (resSim) per design §11 formula.
		// candidateScore is a pure helper (aihub#251) so the scoring math is
		// unit-testable without a live DB; it also carries the setOverlap and
		// declared_resources fixes for defects 1 and 2 (see their doc
		// comments above).
		score, valid := candidateScore(req, c.Goal, c.Labels, c.Resources)
		if !valid {
			// aihub#251 defect 3: an out-of-range score is a programming bug
			// in a sub-score, not a real signal. Fail OPEN — skip scoring
			// this one candidate — consistent with the existing "dedup is
			// best-effort" philosophy applied above when the query itself
			// fails, and never let a >100% (or negative) score reach a
			// threshold comparison or a user-facing message.
			fmt.Fprintf(os.Stderr,
				"aihub: checkDedup: candidate %s produced out-of-range score %.4f, skipping (aihub#251)\n",
				c.Slug, score)
			continue
		}

		if score >= 0.90 {
			return NewErrDetails(ErrConflictDuplicate,
				fmt.Sprintf("work item %q is %.0f%% similar to existing %s", req.Goal, score*100, c.Slug),
				// aihub#396 (folded from aihub#397): the real status, read off the
				// matched row.
				//
				// ⚠️ Bounded by the candidate query above, which selects only
				// queued/running/paused/blocked — so this key can never report
				// wrapped, failed or cancelled. That is not a limitation of the fix:
				// a closed work item is not a duplicate to collide with, so it is
				// never a candidate. Do not read "the real status" as "any of the
				// seven".
				//
				// The old value was the literal string "active", which is not a
				// member of the work_items.status CHECK at all
				// (queued/running/paused/blocked/wrapped/failed/cancelled) — so a
				// caller deciding what to do about the duplicate (claim it? it is
				// already running. requeue it? it is paused.) was handed a value it
				// could not compare with anything, on the one response whose entire
				// purpose is to describe the row it collided with.
				map[string]any{"existing": map[string]any{
					"id": c.ID, "slug": c.Slug, "goal": c.Goal, "status": c.Status,
				}},
			)
		}
		if score >= 0.65 {
			c.Similarity = score
			partials = append(partials, c)
		}
	}
	rows.Close()
	// aihub#334: pgx's Query is lazy, so a server-side failure while streaming
	// this result set is reachable only here. Dedup is best-effort and stays
	// best-effort for ordinary failures — but a class 40 rollback has already
	// killed the transaction CreateWorkItem is holding, so "allow creation" is
	// not a fallback, it is the caller being told 500 two statements later with
	// the SQLSTATE gone.
	if err := rows.Err(); err != nil {
		if aerr := retryConflictErr(err, "failed to scan dedup candidates"); aerr != nil {
			return aerr
		}
		return nil // Dedup is best-effort, as above.
	}

	if len(partials) > 0 {
		// aihub#628: most-similar-first, by the REAL composite score this time
		// (candidateScore ran on every scanned row, so it is available here).
		// The candidates envelope deliberately rides pkg/client's
		// DetailsRenderLimit byte cap (aihub#375, the census entry for this
		// function): when that cap cuts the rendered details, it cuts from the
		// END, so ordering the array here is what makes the second truncation
		// drop the least similar candidates rather than arbitrary ones. The
		// sort is STABLE so equal scores keep the query's deterministic
		// relevance order, keeping the whole envelope reproducible.
		sort.SliceStable(partials, func(i, j int) bool {
			return partials[i].Similarity > partials[j].Similarity
		})
		candidates := make([]map[string]any, len(partials))
		for i, p := range partials {
			candidates[i] = map[string]any{
				"id": p.ID, "slug": p.Slug, "goal": p.Goal, "similarity": p.Similarity,
			}
		}
		return NewErrDetails(ErrConflictCandidates, "similar work items found", map[string]any{"candidates": candidates})
	}
	return nil
}

// GetWorkItem fetches a work item by ID or slug.
func GetWorkItem(ctx context.Context, pool *pgxpool.Pool, idOrSlug string) (*WorkItem, *AihubError) {
	var wi WorkItem
	var labelsRaw []string

	// aihub#402: `id = $1 OR slug = $1`, the one id-or-slug rule this repo uses.
	//
	// This used to dispatch on a `wi_` prefix — id column if present, slug column
	// otherwise — which is a DIFFERENT rule from every sibling resolver
	// (resolveBlockedByRef, ResolveVisibleWorkItemRef, buildListWorkItemsWhere all
	// take the union) and it silently lost a real class of input. Project names
	// are validated by `^[a-z][a-z0-9_-]{0,39}$`, which does not reserve `wi_`,
	// and slug is `project || '#' || seq` (migration 0002), so a project named
	// `wi_lab` produces slugs `wi_lab#1` that the prefix branch looked for in the
	// ID column and never found: a 404 for a work item that exists, from every
	// caller of this function, while list / blocked_by / claim resolved it fine.
	//
	// The union cannot match two rows, which is why it is safe rather than merely
	// broader: `id` is the PRIMARY KEY, `idx_wi_slug` is UNIQUE on slug, and a
	// slug always contains '#' while an id is `wi_` + 8 base62 chars and never
	// can — so for any input at most ONE of the two predicates is satisfiable.
	const q = `SELECT id, seq, slug, project, scenario, goal, source, wi_type, priority,
		       requires_human_session, milestone, labels, status,
		       declared_resources, resources_version, external_share_type, external_share_key,
		       reporter_user_id, reporter_display, current_attempt_id, current_attempt_epoch,
		       parent_work_item_id, attrs, content, created_at, updated_at, closed_at
		FROM work_items WHERE id = $1 OR slug = $1`

	err := pool.QueryRow(ctx, q, idOrSlug).Scan(
		&wi.ID, &wi.Seq, &wi.Slug, &wi.Project, &wi.Scenario, &wi.Goal, &wi.Source,
		&wi.WIType, &wi.Priority, &wi.RequiresHumanSession, &wi.Milestone, &labelsRaw,
		&wi.Status, &wi.DeclaredResources, &wi.ResourcesVersion,
		&wi.ExternalShareType, &wi.ExternalShareKey,
		&wi.ReporterUserID, &wi.ReporterDisplay,
		&wi.CurrentAttemptID, &wi.CurrentAttemptEpoch,
		&wi.ParentWorkItemID, &wi.Attrs, &wi.Content, &wi.CreatedAt, &wi.UpdatedAt, &wi.ClosedAt,
	)
	if err != nil {
		return nil, pgxErr(err,
			fmt.Sprintf("work item %q not found", idOrSlug),
			"failed to get work item")
	}
	wi.Labels = labelsRaw
	if wi.Labels == nil {
		wi.Labels = []string{}
	}
	return &wi, nil
}

// VisibleWorkItemRef is what ResolveVisibleWorkItemRef hands back: the canonical
// id and the project, and nothing else. Deliberately not a *WorkItem — a caller
// that only needs to learn "which project does this belong to" must not be handed
// goal/content/attrs it never asked for and may not be entitled to.
type VisibleWorkItemRef struct {
	ID      string
	Project string
}

// ResolveVisibleWorkItemRef resolves an id-or-slug to its canonical id and project,
// SCOPED IN THE QUERY to the projects the caller may see (aihub#376/#377).
//
// This is for the handlers that need a work item's project in order to know which
// project to authorize against — the ones that cannot check access first because
// the project is what they are computing. GetWorkItem cannot serve them safely:
// its signature carries no caller, by design, so it answers about every project.
//
// 🔴 The scope is in the WHERE clause, not in a check after the row comes back.
// Resolve-then-reject is the same oracle wearing a different hat: whatever the
// second step returns, the first step already proved the row exists. aihub#357's
// executor ran precisely that as a mutation and left the phrase in the test name.
//
// A work item outside the scope is reported exactly as one that does not exist —
// one ErrNotFound, one message, echoing only the caller's own reference back.
// Echoing the reference is not a leak; anything derived from the row would be.
//
// Shape is resolveBlockedByRef's (above), which has run this pattern since #357;
// the difference is that this one takes a pool and has no "own project" to add,
// because the absence of a known project is the situation it exists for. isAdmin
// is a separate flag rather than a role in the slice because an admin's
// ProjectRoles map is empty by design (aihub#227).
func ResolveVisibleWorkItemRef(ctx context.Context, pool *pgxpool.Pool, ref string,
	visible []string, isAdmin bool) (*VisibleWorkItemRef, *AihubError) {

	var out VisibleWorkItemRef
	err := pool.QueryRow(ctx, `
		SELECT id, project FROM work_items
		WHERE (id = $1 OR slug = $1)
		  AND ($2 OR project = ANY($3))`,
		ref, isAdmin, visible,
	).Scan(&out.ID, &out.Project)
	if err != nil {
		return nil, pgxErr(err,
			fmt.Sprintf("work item %q not found", ref),
			"failed to resolve work item")
	}
	return &out, nil
}

// ListWorkItemsFilter holds optional filters for ListWorkItems.
type ListWorkItemsFilter struct {
	Status             []string
	WIType             *string
	Priority           *string
	Milestone          *string
	Label              *string
	UserID             *string  // reporter user_id exact match (legacy)
	ReporterDisplay    *string  // case-insensitive contains on wi.reporter_display
	OwnerDisplay       *string  // case-insensitive contains on run_attempts.actor_display (current attempt)
	AccessibleProjects []string // project allow-list for "view all" when project arg is ""
	// WatcherUserID narrows the set to work items this user watches — the
	// aihub#143 "Watching" scope. It is a MEMBERSHIP filter, never an access
	// grant: buildListWorkItemsWhere ANDs it with the project /
	// AccessibleProjects predicate rather than replacing it, because a
	// wi_watches row outlives the project access that allowed it to be created
	// (removing someone from a project does not walk that table). See the
	// header of wi_watches.go.
	WatcherUserID *string
	Source        *string
	Scenario      *string
	// ReadyOnly narrows the set to the ready-queue's items[] segment — see
	// readyOnlyPredicate for the exact definition and why it is that one.
	ReadyOnly bool
	IDs       []string
	Since     *time.Time
	// IncludeStepState attaches each item's wi_step_state row as
	// WorkItem.StepState. Not a filter: it widens each row rather than
	// narrowing the set, so its guard asserts the response key set, never the
	// row count (aihub#280).
	IncludeStepState bool
	// Query is a semantic search over goal+content (aihub#273): pgvector
	// cosine when an embedding provider is active, ILIKE fallback otherwise.
	// Not combinable with Sort/Order/Cursor — the handler rejects those.
	Query *string
	// SimilarTo names another work item (id or slug) whose STORED vector is
	// used as the query vector — document→document retrieval (aihub#277).
	// Mutually exclusive with Query, and rejected with Sort/Order/Cursor for
	// the same reason Query is. Unlike Query it needs no embedding provider
	// and has no ILIKE fallback; see wi_vector.go's header.
	SimilarTo *string
	// MinSimilarity is an OPT-IN cosine floor for the vector path, applied
	// only when > 0.
	//
	// 🔴 The default is 0 (no floor) and must stay 0. There is no globally
	// valid value: measured over 20 queries against the production corpus,
	// deliberate garbage and real queries overlap on every statistic derived
	// from similarity, frequently inverted. wi_vector.go's header carries the
	// measurement. A caller may still set this when it has calibrated a floor
	// for its own fixed query shape — which is a per-caller fact, not a
	// server default.
	MinSimilarity float64
	Limit         int
	Cursor        *string
	// Sort selects the ordering column, one of ListWorkItemsSortValues; ""
	// means created_at. Order is "desc" (default) or "asc". Both also key the
	// cursor predicate and the emitted next_cursor — see buildListWorkItemsWhere
	// and listWorkItemsNextCursor. Prefer NormalizeListWorkItemsSort to fill
	// these from caller input; unrecognised values fall back to the defaults
	// rather than reaching the query.
	Sort  string
	Order string
}

// Legal `sort` and `order` values for ListWorkItems (aihub#224).
const (
	ListWorkItemsSortCreatedAt = "created_at"
	ListWorkItemsSortClosedAt  = "closed_at"

	ListWorkItemsOrderDesc = "desc"
	ListWorkItemsOrderAsc  = "asc"
)

// listWorkItemsSortColumns maps each legal `sort` value to its qualified column.
// This map is the *enforced* set: nothing outside it can reach ORDER BY, and the
// published enum below is derived from it so contract and enforcement cannot
// drift (cf. mem_X8JDSC96).
var listWorkItemsSortColumns = map[string]string{
	ListWorkItemsSortCreatedAt: "wi.created_at",
	ListWorkItemsSortClosedAt:  "wi.closed_at",
}

// ListWorkItemsSortValues returns the legal `sort` values in a stable order, for
// callers that publish the enum (the HTTP 400 message, the MCP tool schema).
func ListWorkItemsSortValues() []string {
	return []string{ListWorkItemsSortCreatedAt, ListWorkItemsSortClosedAt}
}

// ListWorkItemsOrderValues returns the legal `order` values in a stable order.
func ListWorkItemsOrderValues() []string {
	return []string{ListWorkItemsOrderDesc, ListWorkItemsOrderAsc}
}

// WorkItemStatusValues returns the legal `status` values, in the order the schema
// declares them.
//
// 🔴 This is a SECOND COPY of a vocabulary whose authority is the database, not
// this file: work_items.status carries
//
//	CHECK (status IN ('queued','running','paused','blocked','wrapped','failed','cancelled'))
//
// from internal/db/migrations/0002_work_items.sql. Copies drift, so this one is
// not trusted — TestWorkItemStatusValuesMatchTheSchemaCheck reads the constraint
// back out of a live database with the migrations applied and fails on any
// divergence in either direction. Add a status in a migration without adding it
// here and the gate is red; the list can never silently go stale.
//
// A Go copy exists at all because the alternative is worse: rejecting an unknown
// `?status=` by asking Postgres would mean the rejection depends on a query, and
// the whole point (aihub#255) is to bound the request BEFORE it reaches the
// database.
func WorkItemStatusValues() []string {
	return []string{"queued", "running", "paused", "blocked", "wrapped", "failed", "cancelled"}
}

// NormalizeListWorkItemsSort validates caller-supplied sort/order and fills in
// the defaults (created_at / desc) for empty input, returning the lowercased
// values to store on the filter.
//
// Empty means "the caller did not ask" and must default, since every pre-aihub#224
// caller sends neither param. A *non-empty* unrecognised value is caller-supplied
// input and gets a hard reject naming the offending value and enumerating the
// legal ones, so the mistake is fixed at its source.
func NormalizeListWorkItemsSort(sort, order string) (string, string, *AihubError) {
	sort = strings.ToLower(strings.TrimSpace(sort))
	order = strings.ToLower(strings.TrimSpace(order))
	if sort == "" {
		sort = ListWorkItemsSortCreatedAt
	}
	if order == "" {
		order = ListWorkItemsOrderDesc
	}
	if _, ok := listWorkItemsSortColumns[sort]; !ok {
		return "", "", NewErr(ErrBadRequest, fmt.Sprintf(
			"invalid sort %q: must be one of %s",
			sort, strings.Join(ListWorkItemsSortValues(), ", ")))
	}
	if order != ListWorkItemsOrderDesc && order != ListWorkItemsOrderAsc {
		return "", "", NewErr(ErrBadRequest, fmt.Sprintf(
			"invalid order %q: must be one of %s",
			order, strings.Join(ListWorkItemsOrderValues(), ", ")))
	}
	return sort, order, nil
}

// listWorkItemsSort resolves a filter's Sort/Order to the qualified ORDER BY
// column, the SQL direction keyword, and the strict cursor comparison operator
// that matches that direction (DESC → `<`, ASC → `>`).
//
// Empty or unrecognised values fall back to the defaults, so the domain layer is
// safe even for a caller that skipped NormalizeListWorkItemsSort — the column
// always comes from listWorkItemsSortColumns and caller text is never
// interpolated into the query.
func listWorkItemsSort(f ListWorkItemsFilter) (col, dir, cursorOp string) {
	col, ok := listWorkItemsSortColumns[strings.ToLower(strings.TrimSpace(f.Sort))]
	if !ok {
		col = listWorkItemsSortColumns[ListWorkItemsSortCreatedAt]
	}
	if strings.EqualFold(strings.TrimSpace(f.Order), ListWorkItemsOrderAsc) {
		return col, "ASC", ">"
	}
	return col, "DESC", "<"
}

// listWorkItemsNextCursor returns the pagination cursor for the last row of a
// page: the value of the column the page was ordered by. Emitting created_at for
// a page ordered by closed_at would make page 2 an arbitrary slice of the table.
//
// A NULL sort value cannot be encoded as a cursor. buildListWorkItemsWhere
// excludes those rows so this is unreachable, but ending pagination is the
// correct degradation — a cursor read off a different column is not.
func listWorkItemsNextCursor(last *WorkItem, sortCol string) *string {
	t := last.CreatedAt
	if sortCol == listWorkItemsSortColumns[ListWorkItemsSortClosedAt] {
		if last.ClosedAt == nil {
			return nil
		}
		t = *last.ClosedAt
	}
	s := t.Format(time.RFC3339Nano)
	return &s
}

// ListWorkItemsResult holds paginated results.
type ListWorkItemsResult struct {
	Items      []*WorkItem `json:"items"`
	NextCursor *string     `json:"next_cursor"`
	// RequestAdjusted names the caller-supplied parameters this endpoint changed
	// on the way in — today only `limit`, which NormalizeListWorkItemsLimit clamps
	// to 200 when it arrives above the ceiling and replaces with 50 when it arrives
	// non-positive (aihub#267). Omitted when nothing was adjusted; see
	// request_adjusted.go for why absence rather than an empty list, and for the
	// one case this cannot report.
	RequestAdjusted []RequestAdjustment `json:"request_adjusted,omitempty"`
	// Semantic describes the semantic retrieval that produced this page, and is
	// present ONLY when the vector path served it (aihub#276). Its absence is
	// therefore meaningful: it says the ILIKE text fallback answered, and that
	// no item carries a similarity.
	Semantic *SemanticInfo `json:"semantic,omitempty"`
	// Lexical is the second retrieval section (aihub#360): verbatim-substring
	// matches over goal+content, parallel to — never merged into — Items.
	// Present exactly when the request carried a non-empty query= (similar_to
	// has no query text, so it never gets one), whichever path served Items,
	// and an empty match set is an explicit total of 0 rather than an absent
	// field. Attached by ListWorkItems around the routing, same one-exit
	// reasoning as RequestAdjusted. See lexical.go for the aihub#367
	// measurement (query= scored 0/6 at every N) and wi_lexical.go for the
	// predicate.
	Lexical *WorkItemLexicalSection `json:"lexical,omitempty"`
}

// ListWorkItems returns a paginated list of work items.
//
// Project scoping:
//   - project != ""              → single project (WHERE wi.project = $project)
//   - project == "" + AccessibleProjects set → scope to those projects
//     (WHERE wi.project = ANY(...)), used by the "view all" UI option for a
//     non-admin caller
//   - project == "" + AccessibleProjects empty → no project clause at all
//     (admin "view all" across every project)
//
// readyOnlyPredicate is *the* SQL definition of "ready", shared by the ready
// queue's items[] segment (GetReadyQueue) and by ListWorkItemsFilter.ReadyOnly.
//
// aihub#280: `ready_only` sat in the published MCP schema for a long time with
// nothing on the server consuming it, which means the decision this constant
// records had never actually been made. It is not a free choice, though — the
// seven-segment LCRS view already defines "ready" as the items[] segment: takeable
// right now, by an agent, with nobody having to unblock anything first. That is
// exactly three conditions:
//
//   - status = 'queued'              — not already running/paused/terminal
//   - requires_human_session = false — an agent may take it unattended
//   - no live 'blocks' dependency    — nothing has to land before it
//
// It is one constant rather than two copies specifically because a second copy
// is how `ready_only` would drift into meaning something other than the queue
// it is named after. Combining ready_only with an explicit status filter yields
// the intersection, so `status=running&ready_only=true` correctly returns none.
const readyOnlyPredicate = `(wi.status = 'queued'
		  AND wi.requires_human_session = false
		  AND ` + noLiveBlockerPredicate + `)`

// noLiveBlockerPredicate is the SQL for "nothing has to land before this wi":
// no 'blocks' dependency whose blocker is still open. It assumes the outer query
// aliases work_items as `wi`.
//
// One definition rather than four. Before aihub#280 this subquery was copied
// verbatim into three of GetReadyQueue's segments (items[],
// needs_human_session[], unclassified[]), and adding ready_only to the list
// endpoint would have made a fourth. Those segments differ only in their
// requires_human_session test, so a change to what "blocked" means had to be
// applied identically in every copy or the segments would start disagreeing
// about the same work item — silently, since each query is individually valid.
const noLiveBlockerPredicate = `NOT EXISTS (
		    SELECT 1 FROM wi_dependencies dep
		    JOIN work_items blocker ON dep.blocking_wi_id = blocker.id
		    WHERE dep.blocked_wi_id = wi.id
		      AND dep.kind = 'blocks'
		      AND blocker.status NOT IN ('wrapped','cancelled','failed')
		  )`

// buildListWorkItemsWhere builds the WHERE clause, JOIN clause, and ordered
// bound args for ListWorkItems from the given project scope and filter. It is
// split out from ListWorkItems so the query construction (notably placeholder
// numbering and the cursor predicate) is unit-testable without a live DB.
func buildListWorkItemsWhere(project string, f ListWorkItemsFilter) (joinClause, where string, args []any) {
	args = []any{}
	conds := []string{}
	argIdx := 1
	if project != "" {
		conds = append(conds, fmt.Sprintf("wi.project = $%d", argIdx))
		args = append(args, project)
		argIdx++
	} else if len(f.AccessibleProjects) > 0 {
		conds = append(conds, fmt.Sprintf("wi.project = ANY($%d)", argIdx))
		args = append(args, f.AccessibleProjects)
		argIdx++
	}

	// aihub#143 Watching scope. Appended to conds — i.e. ANDed with the project
	// predicate just built above — and NOT as a replacement for it. That
	// placement is the whole authorization story of this feature: a wi_watches
	// row records that someone once pressed Watch, and it survives them losing
	// access to the project, so a watch that escaped the project predicate
	// would be a standing read grant nobody issued.
	//
	// EXISTS rather than `JOIN wi_watches`: the composite primary key makes at
	// most one row match, so the two are equivalent today — but a JOIN's row
	// count is a property of the join key, and if wi_watches ever grows a
	// second row per pair the JOIN starts duplicating work items in the page
	// (and quietly consuming the LIMIT) while EXISTS cannot. Semi-join is what
	// is meant, so semi-join is what is written.
	if f.WatcherUserID != nil && *f.WatcherUserID != "" {
		conds = append(conds, fmt.Sprintf(
			"EXISTS (SELECT 1 FROM wi_watches w WHERE w.work_item_id = wi.id AND w.user_id = $%d)", argIdx))
		args = append(args, *f.WatcherUserID)
		argIdx++
	}

	if len(f.Status) > 0 {
		conds = append(conds, fmt.Sprintf("wi.status = ANY($%d)", argIdx))
		args = append(args, f.Status)
		argIdx++
	}
	if f.WIType != nil {
		conds = append(conds, fmt.Sprintf("wi.wi_type = $%d", argIdx))
		args = append(args, *f.WIType)
		argIdx++
	}
	if f.Priority != nil {
		conds = append(conds, fmt.Sprintf("wi.priority = $%d", argIdx))
		args = append(args, *f.Priority)
		argIdx++
	}
	if f.Milestone != nil {
		conds = append(conds, fmt.Sprintf("wi.milestone = $%d", argIdx))
		args = append(args, *f.Milestone)
		argIdx++
	}
	if f.Source != nil {
		conds = append(conds, fmt.Sprintf("wi.source = $%d", argIdx))
		args = append(args, *f.Source)
		argIdx++
	}
	if f.Scenario != nil {
		conds = append(conds, fmt.Sprintf("wi.scenario = $%d", argIdx))
		args = append(args, *f.Scenario)
		argIdx++
	}
	if f.Label != nil {
		conds = append(conds, fmt.Sprintf("$%d = ANY(wi.labels)", argIdx))
		args = append(args, *f.Label)
		argIdx++
	}
	// ReadyOnly binds no args, so it deliberately does not bump argIdx.
	if f.ReadyOnly {
		conds = append(conds, readyOnlyPredicate)
	}
	if f.UserID != nil {
		conds = append(conds, fmt.Sprintf("wi.reporter_user_id = $%d", argIdx))
		args = append(args, *f.UserID)
		argIdx++
	}
	if f.ReporterDisplay != nil && *f.ReporterDisplay != "" {
		conds = append(conds, fmt.Sprintf("wi.reporter_display ILIKE '%%' || $%d || '%%'", argIdx))
		args = append(args, *f.ReporterDisplay)
		argIdx++
	}
	// OwnerDisplay needs a JOIN to run_attempts. Only inject the join when
	// the filter is requested so the no-filter path stays at zero extra cost.
	if f.OwnerDisplay != nil && *f.OwnerDisplay != "" {
		joinClause = " LEFT JOIN run_attempts ra ON ra.id = wi.current_attempt_id"
		conds = append(conds, fmt.Sprintf("ra.actor_display ILIKE '%%' || $%d || '%%'", argIdx))
		args = append(args, *f.OwnerDisplay)
		argIdx++
	}
	// Slugs as well as ids, because the MCP schema publishes "IDs or slugs" and a
	// published capability the SQL does not implement is exactly this wi's defect
	// class: `ids=["aihub#280"]` returned {"items":[]} with HTTP 200 and no error
	// — indistinguishable from "no such work item". GetWorkItem has always
	// accepted either spelling; the list path had not. One bound arg referenced
	// twice, the same shape as the Query predicate below (aihub#280).
	if len(f.IDs) > 0 {
		conds = append(conds, fmt.Sprintf("(wi.id = ANY($%d) OR wi.slug = ANY($%d))", argIdx, argIdx))
		args = append(args, f.IDs)
		argIdx++
	}
	if f.Since != nil {
		conds = append(conds, fmt.Sprintf("wi.created_at >= $%d", argIdx))
		args = append(args, *f.Since)
		argIdx++
	}
	// aihub#273 text path: substring match over goal+content. The vector path
	// (listWorkItemsByVector) clears Query before calling this builder, so the
	// guard only ever applies to the fallback.
	if f.Query != nil && *f.Query != "" {
		conds = append(conds, fmt.Sprintf(
			"(wi.goal ILIKE '%%' || $%d || '%%' OR wi.content ILIKE '%%' || $%d || '%%')", argIdx, argIdx))
		args = append(args, *f.Query)
		argIdx++
	}
	sortCol, _, cursorOp := listWorkItemsSort(f)
	// sort=closed_at restricts the set to rows that HAVE a close time. Two
	// reasons, both load-bearing:
	//   1. A NULL sort key is unreachable by any cursor — `closed_at < $n` is
	//      never true for NULL — so open wis would silently vanish after page 1.
	//      Excluding them up front keeps the ordering total and the cursor exact.
	//   2. It loses nothing for the terminal statuses this sort is for: the
	//      trg_wi_closed_at trigger (migration 0002) stamps closed_at on every
	//      transition into wrapped/failed/cancelled, and INSERT always starts at
	//      'queued', so status-terminal implies closed_at IS NOT NULL. Verified
	//      against live data at aihub#224: 0 of 200 terminal aihub wis had a NULL.
	// It also makes the query eligible for the partial index idx_wi_closed.
	// Callers that want open items must sort by created_at (the default).
	if sortCol == listWorkItemsSortColumns[ListWorkItemsSortClosedAt] {
		conds = append(conds, "wi.closed_at IS NOT NULL")
	}
	// Cursor pagination: NextCursor is the last returned item's *sort column*
	// value (RFC3339Nano), so the predicate must be on that same column, with a
	// strict comparison following the sort direction (DESC → `<`, ASC → `>`).
	// Mirrors ListEvents in memory.go (strict comparison, ::timestamptz cast, no
	// secondary tie-breaker).
	if f.Cursor != nil && *f.Cursor != "" {
		conds = append(conds, fmt.Sprintf("%s %s $%d::timestamptz", sortCol, cursorOp, argIdx))
		args = append(args, *f.Cursor)
		argIdx++
	}
	// Every clause above that BINDS AN ARG bumps argIdx uniformly, so clauses can
	// be reordered or extended without re-introducing a placeholder-numbering bug
	// (cf. aihub#147). The arg-free clauses — the ReadyOnly predicate and the
	// sort=closed_at NOT NULL guard — deliberately do not bump it, which is what
	// keeps argIdx == len(args)+1. listWorkItemsByVector relies on exactly that
	// invariant to place its own placeholders after these (wi_vector.go).
	// The final bump is otherwise unread; sink it so ineffassign stays happy.
	_ = argIdx

	where = ""
	if len(conds) > 0 {
		where = "WHERE " + strings.Join(conds, " AND ")
	}
	return joinClause, where, args
}

// buildListWorkItemsQuery assembles the full SELECT for ListWorkItems, returning
// it alongside the bound args and the sort column the page is ordered by (which
// listWorkItemsNextCursor needs to emit the right cursor).
//
// Split out from ListWorkItems for the same reason buildListWorkItemsWhere was:
// the domain suite is pure-unit, so the assembled statement — clause order, the
// ORDER BY built from two interpolated fragments, the LIMIT — has no coverage
// unless it can be inspected without a live pool (cf. gc_test pinning the sweep
// SQL). Assumes f.Limit is already clamped.
func buildListWorkItemsQuery(project string, f ListWorkItemsFilter) (query string, args []any, sortCol string) {
	// The column and direction come from listWorkItemsSortColumns, never from
	// caller text, so this stays injection-safe despite the Sprintf.
	sortCol, sortDir, _ := listWorkItemsSort(f)

	joinClause, where, args := buildListWorkItemsWhere(project, f)
	query = fmt.Sprintf(`
		SELECT wi.id, wi.seq, wi.slug, wi.project, wi.scenario, wi.goal, wi.source,
			   wi.wi_type, wi.priority, wi.requires_human_session, wi.milestone, wi.labels,
			   wi.status, wi.declared_resources, wi.resources_version,
			   wi.external_share_type, wi.external_share_key,
			   wi.reporter_user_id, wi.reporter_display,
			   wi.current_attempt_id, wi.current_attempt_epoch,
			   wi.parent_work_item_id, wi.attrs, wi.created_at, wi.updated_at, wi.closed_at
		FROM work_items wi%s
		%s
		ORDER BY %s %s
		LIMIT %d`, joinClause, where, sortCol, sortDir, f.Limit+1)

	return query, args, sortCol
}

// Page-size bounds for ListWorkItems.
//
// INVARIANT: ListWorkItemsLimitCeiling >= ListWorkItemsLimitDefault, for the
// reason spelled out on recallTopKCeiling — a ceiling below the default inverts
// the endpoint, so asking for a bigger page returns fewer items.
//
// Exported only so the HTTP handler can seed filter.Limit with the same default
// the domain would apply; no test may derive an expectation from either (the
// tests spell 50 and 200 out), because a fixture read off the constant under
// test moves with the defect instead of catching it.
const (
	ListWorkItemsLimitDefault = 50
	ListWorkItemsLimitCeiling = 200
)

// NormalizeListWorkItemsLimit resolves a requested page size into the one
// ListWorkItems uses. It is the recall side's normalizeRecallTopK, deliberately
// down to its shape (aihub#267).
//
// 🔴 What changed here: a request ABOVE the ceiling now yields the CEILING, where
// it used to yield the default of 50. The two halves of one API had opposite
// answers to the same question — `top_k=300` on GET /v1/memories returned 200
// items while `limit=300` on GET /v1/work_items returned 50 — and 50 is the worse
// of the two in the direction that costs something: the caller asked for more
// than the endpoint will give and got less than it would have given.
//
// That is not hypothetical. An audit of two aihub instances on 2026-08-27
// enumerated work items with limit=5000, received a silently truncated 308, and
// concluded 11 work items existed on one instance only. Correct pagination
// showed the true figure was zero. The retraction is in aihub#267; the number was
// wrong in the direction that would have driven the opposite operational call.
//
// A NON-POSITIVE request is neither malformed nor out of range: it means the
// caller named no page size and yields the default. That is normalizeRecallTopK's
// rule, and the aihub#249 contract behind it — bad input falls back to the
// DEFAULT, never to a smaller page.
//
// This is the ONLY place a work-item page size may be bounded. A cap applied
// upstream is invisible from here, so nothing can hold it to the invariant above;
// that is exactly how aihub#309 made the recall ceiling unreachable, and why
// handleListWorkItems now forwards whatever it parsed, negatives included.
func NormalizeListWorkItemsLimit(requested int) int {
	if requested <= 0 {
		return ListWorkItemsLimitDefault
	}
	if requested > ListWorkItemsLimitCeiling {
		return ListWorkItemsLimitCeiling
	}
	return requested
}

// ListWorkItems bounds the caller's page size, runs the query, and DISCLOSES the
// bound if it fired.
//
// 🔴 The disclosure is attached HERE, wrapped around the routing, and not at the
// two `return` statements inside listWorkItemsPage. Those are the vector path and
// the text path, and annotating both is two chances to forget one — which is
// precisely how aihub#280's include_step_state worked on every query except a
// semantically-matched one. The same reasoning put handleRecall's unmatched_types
// outside domain.Recall rather than at its four exits.
func ListWorkItems(ctx context.Context, pool *pgxpool.Pool, project string, f ListWorkItemsFilter) (*ListWorkItemsResult, *AihubError) {
	requestedLimit := f.Limit
	f.Limit = NormalizeListWorkItemsLimit(f.Limit)
	res, err := listWorkItemsPage(ctx, pool, project, f)
	if err != nil || res == nil {
		return res, err
	}
	res.RequestAdjusted = appendIntAdjustment(res.RequestAdjusted, "limit", requestedLimit, f.Limit)
	// aihub#360: the lexical section, attached HERE around the routing for the
	// same one-exit reason as the disclosure above — listWorkItemsPage returns
	// from the similar_to path, the vector path, and the ILIKE text path, and
	// annotating each is three chances to forget one. Keyed on the REQUEST (a
	// non-empty query=), never on which path answered: aihub#367 measured the
	// vector path missing at 0/6 for every N precisely while returning
	// plausible full pages, and on the ILIKE fallback the section is a cheap
	// restatement rather than a wrong one. f.Limit is already normalized above,
	// so the section's page cap is the same one Items obeys.
	if f.Query != nil && strings.TrimSpace(*f.Query) != "" {
		lex, lerr := listWorkItemsLexical(ctx, pool, project, f)
		if lerr != nil {
			return nil, lerr
		}
		res.Lexical = lex
	}
	return res, nil
}

// listWorkItemsPage is ListWorkItems with the page size already bounded. Split
// out so the disclosure above covers every way this can return.
func listWorkItemsPage(ctx context.Context, pool *pgxpool.Pool, project string, f ListWorkItemsFilter) (*ListWorkItemsResult, *AihubError) {
	// aihub#277: similar_to is document→document retrieval off another row's
	// STORED vector. It is handled FIRST, and unlike query= it never falls
	// through to the ILIKE text path — see the 🔴 note in wi_vector.go's
	// header: an ILIKE fallback here would text-search for the literal string
	// "aihub#276" and answer a different question with a plausible-looking
	// page. It also does not consult embProvider at all, so it keeps working
	// with no provider configured, which is why the isNoopProvider guard the
	// query= branch carries is deliberately absent from this one.
	if f.SimilarTo != nil && strings.TrimSpace(*f.SimilarTo) != "" {
		res, vecErr := listWorkItemsByVector(ctx, pool, project, f)
		if vecErr != nil {
			return nil, vecErr
		}
		if f.IncludeStepState {
			attachStepState(ctx, pool, res.Items)
		}
		return res, nil
	}
	// aihub#273: semantic path when the caller sent query= and an embedding
	// provider is active. Any error, or an empty result the caller did not ask
	// to be empty, falls through to the ILIKE text path below (via
	// buildListWorkItemsWhere's Query guard) — the aihub#270 lesson: a
	// non-empty vector shortcut must never make unembedded rows structurally
	// unreachable, and pre-backfill "0 embedded" must never read as "no
	// matches".
	if f.Query != nil && strings.TrimSpace(*f.Query) != "" && !isNoopProvider(embProvider) {
		res, vecErr := listWorkItemsByVector(ctx, pool, project, f)
		switch {
		case vecErr != nil && f.MinSimilarity > 0:
			// 🔴 A floored request must NOT degrade to ILIKE, and must not be
			// answered with a 400 either. vecErr here is always server-side —
			// an embed timeout or outage, an empty embedding, a failed vector
			// query, a scan error — so falling through would reach the
			// MinSimilarity guard below and hand the caller
			// "pass a query= on a server with an embedding provider", which is
			// exactly what they did. Blaming the caller for a provider outage
			// is worse than the outage: the advice is impossible to follow.
			// Surface the real error instead. (Without a floor the same
			// condition still degrades to ILIKE, which is the aihub#270
			// behaviour and is deliberately kept.)
			return nil, vecErr
		case vecErr != nil:
			fmt.Fprintf(os.Stderr, "list work_items: vector path failed, falling through to text path: %v\n", vecErr)
		case len(res.Items) == 0 && f.MinSimilarity <= 0:
			// Nothing embedded matched — e.g. the corpus is not backfilled yet,
			// or every stored emb_model differs from the current provider. Fall
			// through to ILIKE so a query is never silently empty during an
			// embedding rollout (the aihub#270 lesson).
			//
			// 🔴 The MinSimilarity half of this guard is load-bearing, and it
			// mirrors RecallWithVector's identical clause verbatim
			// (memory.go: `len(r.Items) == 0 && req.SimilarityThreshold <= 0`,
			// "a caller-set SimilarityThreshold means empty is intended").
			// Without it, query= plus a floor that legitimately excludes
			// everything falls through here — which would both DISCARD the
			// floor (ILIKE cannot apply a cosine, so rows below it come back)
			// and then trip the MinSimilarity guard below, answering a
			// correctly-served request with a 400 that says the vector path was
			// never used. An empty page IS the answer when the caller set a
			// floor, so it is returned as one, carrying the semantic block.
		default:
			// The vector path returns before the text path's enrichment, so it
			// needs its own call — otherwise include_step_state would work for
			// every query except a semantically-matched one (aihub#280).
			if f.IncludeStepState {
				attachStepState(ctx, pool, res.Items)
			}
			return res, nil
		}
	}

	// Reaching here means the ILIKE text path is about to answer. A cosine
	// floor has no meaning on that path, so honouring the request is
	// impossible — and SILENTLY IGNORING it is the defect class this endpoint
	// keeps being fixed for (aihub#267/#271/#280: a dropped parameter is
	// indistinguishable from one that was never sent). Say so instead. This
	// fires for min_similarity with no query=/similar_to= at all, and for
	// min_similarity + query= when there is no embedding provider or the
	// vector half came back empty.
	if f.MinSimilarity > 0 {
		return nil, NewErr(ErrBadRequest,
			"min_similarity applies only to the vector path; this request is being served by the "+
				"text (ILIKE) path, where a cosine floor has no meaning. Pass similar_to=, or a "+
				"query= on a server with an embedding provider, or drop min_similarity")
	}

	query, args, sortCol := buildListWorkItemsQuery(project, f)

	rows, err := pool.Query(ctx, query, args...)
	if err != nil {
		return nil, dbErrCause(err, "failed to list work_items")
	}
	defer rows.Close()

	var items []*WorkItem
	for rows.Next() {
		var wi WorkItem
		var labelsRaw []string
		if scanErr := rows.Scan(
			&wi.ID, &wi.Seq, &wi.Slug, &wi.Project, &wi.Scenario, &wi.Goal, &wi.Source,
			&wi.WIType, &wi.Priority, &wi.RequiresHumanSession, &wi.Milestone, &labelsRaw,
			&wi.Status, &wi.DeclaredResources, &wi.ResourcesVersion,
			&wi.ExternalShareType, &wi.ExternalShareKey,
			&wi.ReporterUserID, &wi.ReporterDisplay,
			&wi.CurrentAttemptID, &wi.CurrentAttemptEpoch,
			&wi.ParentWorkItemID, &wi.Attrs, &wi.CreatedAt, &wi.UpdatedAt, &wi.ClosedAt,
		); scanErr != nil {
			return nil, NewErr(ErrInternalError, fmt.Sprintf("scan error: %v", scanErr))
		}
		wi.Labels = labelsRaw
		if wi.Labels == nil {
			wi.Labels = []string{}
		}
		items = append(items, &wi)
	}
	// pgx defers an error the server reports while EXECUTING the statement to
	// rows.Err(); Query above only surfaces failures met while sending it. A
	// cursor that does not cast at `$n::timestamptz`, a statement_timeout, a
	// raise inside a function — each ends the loop after zero rows, and without
	// this check they were returned as 200 {"items":[]}: an empty page the
	// caller cannot tell from "nothing matched" (aihub#382). checkDedup and
	// listWorkItemsByVector already ask; attachStepState only logs, on purpose,
	// because it decorates the page rather than being it.
	if err := rows.Err(); err != nil {
		return nil, dbErrCause(err, "failed to read work_items rows")
	}

	result := &ListWorkItemsResult{}
	if len(items) > f.Limit {
		items = items[:f.Limit]
		result.NextCursor = listWorkItemsNextCursor(items[len(items)-1], sortCol)
	}
	result.Items = items
	if result.Items == nil {
		result.Items = []*WorkItem{}
	}
	// After truncation to f.Limit, so the extra look-ahead row never costs a
	// step-state lookup.
	if f.IncludeStepState {
		attachStepState(ctx, pool, result.Items)
	}
	return result, nil
}

// attachStepState fills in WorkItem.StepState for every item that has a
// wi_step_state row, in one round trip for the whole page (aihub#280).
//
// Best-effort by design: a failure here leaves StepState nil, which is the same
// shape as "this wi was never claimed". The alternative — failing the whole list
// — would make `include_step_state=true` able to break a call that works without
// it, and the caller (pf-status, pf-retro) needs the wi fields far more than the
// step fields.
func attachStepState(ctx context.Context, pool *pgxpool.Pool, items []*WorkItem) {
	if len(items) == 0 {
		return
	}
	byID := make(map[string]*WorkItem, len(items))
	ids := make([]string, 0, len(items))
	for _, wi := range items {
		byID[wi.ID] = wi
		ids = append(ids, wi.ID)
	}
	rows, err := pool.Query(ctx, `
		SELECT work_item_id, wi_type, graph_source, current_step, current_step_status,
		       current_step_attempt, step_started_at, version, updated_at
		FROM wi_step_state
		WHERE work_item_id = ANY($1)`, ids)
	if err != nil {
		fmt.Fprintf(os.Stderr, "list work_items: include_step_state lookup failed: %v\n", err)
		return
	}
	defer rows.Close()
	for rows.Next() {
		var wiID string
		var st WorkItemStepState
		if scanErr := rows.Scan(&wiID, &st.WIType, &st.GraphSource, &st.CurrentStep,
			&st.CurrentStepStatus, &st.CurrentStepAttempt, &st.StepStartedAt,
			&st.Version, &st.UpdatedAt); scanErr != nil {
			fmt.Fprintf(os.Stderr, "list work_items: include_step_state scan failed: %v\n", scanErr)
			continue
		}
		if wi, ok := byID[wiID]; ok {
			state := st
			wi.StepState = &state
		}
	}
	// A mid-stream read failure would otherwise truncate the enrichment with no
	// signal at all — the same silence the two paths above deliberately log.
	if err := rows.Err(); err != nil {
		fmt.Fprintf(os.Stderr, "list work_items: include_step_state rows failed: %v\n", err)
	}
}

// workItemUpdate is the compiled UPDATE statement for UpdateWorkItem.
//
// It is produced by buildWorkItemUpdate, a pure function, so the two aihub#241
// invariants below have coverage that actually executes. UpdateWorkItem itself
// reads the database before it reaches this logic, so a behavioural test of it
// would have to be DB-gated — and a DB-gated test in this repo runs nowhere
// (AIHUB_TEST_DB is unset locally and on CI's "Unit tests" step), so it would
// SKIP while reading as coverage (mem_I98xpPgY). Same technique as cancelGate
// in aihub#242.
type workItemUpdate struct {
	Query string
	Args  []any
	// CAS is true when the WHERE clause carries a resources_version predicate,
	// i.e. the caller asked for compare-and-set. RowsAffected()==0 is then a
	// version conflict rather than a missing row.
	CAS bool
}

// buildWorkItemUpdate compiles the SET/WHERE clauses for UpdateWorkItem.
//
// aihub#241 fixes two defects that lived here:
//
//   - The counter never advanced. resources_version was written only when the
//     caller supplied one, as `= <caller value> + 1`; the ordinary path (no
//     version passed) left it at its old value forever. Every caller therefore
//     read 0, so even a working CAS could never detect a conflict. It is now
//     incremented in the database — `resources_version = resources_version + 1`
//     — on every write of declared_resources, independent of what the caller
//     sent, so concurrent writers cannot both compute the same next value.
//
//   - There was no compare-and-set at all. Passing resources_version changed
//     what got stored but added no predicate, so a stale writer silently
//     overwrote a fresher one. The version is now a WHERE precondition.
//
// The two are deliberately orthogonal: the increment is keyed on
// declared_resources being written, the precondition on the caller supplying a
// version. Passing resources_version alone is a plain guard ("only apply this
// patch if nobody has touched declared_resources since I read it") and is
// never wrong; omitting it keeps the historical unconditional behaviour, which
// callers depend on today.
//
// # aihub#288: the attrs merge path
//
// `attrs` was — and still is — a whole-column REPLACE: `attrs = $n` drops every
// key the caller did not resend. That is not a hypothetical. On 2026-08-30 an
// update carrying 2 keys destroyed 3 unrelated keys on aihub#284, with no
// error and no conflict, because two agents were annotating the same work item
// and the second one had only ever seen its own two keys.
//
// The fix is additive, not a change of meaning:
//
//   - `attrs` keeps REPLACE, byte for byte. Changing its default to merge would
//     silently rewrite the behaviour of every caller that exists today (the Web
//     UI, pkg/client, the MCP tool, anything scripted against the REST API). A
//     silent semantic change is a worse defect than the one being fixed. ⚠️ That
//     compatibility argument is the whole of it: this bullet also said the merge
//     default "would remove the only way to delete a key", which the third
//     bullet below contradicts in the same comment — `attrs_unset` deletes keys.
//     Corrected 2026-09-10 (aihub#543).
//   - `attrs_patch` is the opt-in merge: `attrs = attrs || $n::jsonb`. Shallow,
//     i.e. a top-level key present in the patch replaces the stored value for
//     that key outright and is NOT merged into it recursively. `null` in the
//     patch therefore STORES a JSON null; it does not delete.
//   - `attrs_unset` deletes top-level keys: `attrs = attrs - $n::text[]`.
//     Deletion has to be spelled out precisely because the merge is shallow —
//     without it the merge path would be a capability regression against
//     "resend the whole object".
//
// Shallow, not RFC 7396 deep merge: the only way to spell RFC 7396 in plain SQL
// is `jsonb_strip_nulls(attrs || $n)`, which strips nulls recursively out of the
// WHOLE merged document — so an unrelated patch would silently delete a
// pre-existing key whose value happened to be null. Explicit `attrs_unset` costs
// one parameter and has no such blast radius.
//
// Both may be sent together; the merge is applied first and the unset second
// (composed inside one SET expression), so a key named in both ends up deleted.
// `attrs` together with `attrs_patch`/`attrs_unset` is rejected as a 400 by
// UpdateWorkItem — the two are contradictory instructions for one column, and
// silently picking a winner is how this class of bug starts.
func buildWorkItemUpdate(req *UpdateWorkItemRequest, wiID string) workItemUpdate {
	setClauses := []string{"updated_at = clock_timestamp()"}
	args := []any{}
	argIdx := 1

	add := func(clause string, val any) {
		setClauses = append(setClauses, fmt.Sprintf(clause, argIdx))
		args = append(args, val)
		argIdx++
	}

	if req.Priority != nil {
		add("priority = $%d", *req.Priority)
	}
	if req.Milestone != nil {
		add("milestone = $%d", *req.Milestone)
	}
	if req.WIType != nil {
		add("wi_type = $%d", *req.WIType)
	}
	if req.RequiresHumanSession != nil {
		// Non-nil ONLY, so there is no way back to the NULL third state through
		// this function: omitting the field and sending an explicit JSON null are
		// the same no-op. That is the half of aihub#411 T2-9 this file answers —
		// the row reported an attrs_patch-only update moving a NULL to true, and
		// aihub#447 reproduced the sequence on a live-era build and on origin/main
		// and found it does not happen here. FnClaimWorkItem writes it, on the
		// first claim, from a server default. Do not "fix" that report by adding a
		// write on this path.
		add("requires_human_session = $%d", *req.RequiresHumanSession)
	}
	if req.Labels != nil {
		add("labels = $%d", req.Labels)
	}
	if req.DeclaredResources != nil {
		add("declared_resources = $%d", req.DeclaredResources)
		// Computed by Postgres from the stored value, not from anything the
		// caller sent — that is what makes it a usable CAS counter.
		setClauses = append(setClauses, "resources_version = resources_version + 1")
	}
	if req.Attrs != nil {
		add("attrs = $%d", req.Attrs)
	}
	// aihub#288: the non-destructive path, composed into ONE SET expression so
	// merge-then-delete happens in a single statement (two SET clauses for the
	// same column is a syntax error in Postgres, and two statements would
	// reintroduce a window where a concurrent reader sees the half-applied
	// state).
	//
	// The parentheses are load-bearing. Postgres assigns precedence by operator
	// SPELLING, not by operand type: binary `-` sits at the addition/subtraction
	// level, which binds TIGHTER than `||` ("any other operator"). Unparenthesised,
	// `attrs || $1::jsonb - $2::text[]` parses as `attrs || ($1::jsonb - $2::text[])`
	// — the keys would be stripped out of the incoming patch and nothing would be
	// removed from the stored value, with no error at all. Verified on PG 18.4:
	// `'{"a":1,"gone":9}'::jsonb || '{"b":2}'::jsonb - ARRAY['gone']::text[]` keeps
	// "gone". TestBuildWorkItemUpdate_MergeThenUnsetIsParenthesised locks this.
	if req.AttrsPatch != nil || req.AttrsUnset != nil {
		expr := attrsAsObject
		if req.AttrsPatch != nil {
			expr = fmt.Sprintf("(%s || $%d::jsonb)", expr, argIdx)
			args = append(args, req.AttrsPatch)
			argIdx++
		}
		if req.AttrsUnset != nil {
			expr = fmt.Sprintf("(%s - $%d::text[])", expr, argIdx)
			args = append(args, req.AttrsUnset)
			argIdx++
		}
		setClauses = append(setClauses, "attrs = "+expr)
	}
	if req.Goal != nil {
		add("goal = $%d", *req.Goal)
	}
	if req.Content != nil {
		add("content = $%d", *req.Content)
	}

	whereClauses := []string{fmt.Sprintf("id = $%d", argIdx)}
	args = append(args, wiID)
	argIdx++

	cas := req.ResourcesVersion != nil
	if cas {
		whereClauses = append(whereClauses, fmt.Sprintf("resources_version = $%d", argIdx))
		args = append(args, *req.ResourcesVersion)
	}

	return workItemUpdate{
		Query: fmt.Sprintf("UPDATE work_items SET %s WHERE %s",
			strings.Join(setClauses, ", "), strings.Join(whereClauses, " AND ")),
		Args: args,
		CAS:  cas,
	}
}

// attrsAsObject is the left operand of the aihub#288 merge: the stored attrs,
// coerced to an object if it somehow is not one.
//
// The column is `JSONB NOT NULL DEFAULT '{}'` and every reader unmarshals it
// into a map, but nothing enforces that it holds an OBJECT — `attrs` REPLACE
// takes any JSON, and JSON null is not SQL NULL, so `{"attrs":null}` stores a
// jsonb null past the NOT NULL constraint. Merging onto such a value is not a
// no-op, it is corruption, and both halves misbehave differently (verified on
// PG 18.4):
//
//	'null'::jsonb  || '{"a":1}'::jsonb        -> [null, {"a": 1}]      -- silent
//	'[1,2]'::jsonb || '{"a":1}'::jsonb        -> [1, 2, {"a": 1}]      -- silent
//	'null'::jsonb  -  ARRAY['a']::text[]      -> ERROR: cannot delete from scalar
//
// So a row that is already outside the column's intended type would either turn
// into an array nobody can read or 500 the request. Coercing to '{}' keeps the
// type invariant the rest of the codebase assumes; a caller who wants the old
// value back can still overwrite it wholesale with `attrs`.
const attrsAsObject = `CASE WHEN jsonb_typeof(attrs) = 'object' THEN attrs ELSE '{}'::jsonb END`

// normalizeAttrsPatch treats a literal JSON `null`, and an empty attrs_unset, as
// "field not supplied" (aihub#288).
//
// json.RawMessage keeps the four bytes `null` rather than staying nil, so
// `{"attrs":{…},"attrs_patch":null}` — a request that carries no patch at all —
// would otherwise trip the "cannot be combined" check and come back as a 400
// describing something the caller did not do. Clients that null-fill optional
// parameters are common enough that this must not be an error, and attrs_patch
// is a brand-new field, so nothing can be relying on it rejecting null: being
// permissive here cannot regress a caller, being strict can.
//
// An empty attrs_unset is normalised for a smaller reason — `attrs - '{}'::text[]`
// is a self-assignment, and a caller who asked for nothing should not write the
// column at all.
func normalizeAttrsPatch(req *UpdateWorkItemRequest) {
	if req.AttrsPatch != nil && bytes.Equal(bytes.TrimSpace(req.AttrsPatch), []byte("null")) {
		req.AttrsPatch = nil
	}
	if req.AttrsUnset != nil && len(req.AttrsUnset) == 0 {
		req.AttrsUnset = nil
	}
}

// normalizeDeclaredResources treats an explicit JSON `null` for
// declared_resources as "not specified" (aihub#264).
//
// A separate one-line function rather than a branch inside UpdateWorkItem for
// the reason cancelGate and isCASConflict are separate: the only behavioural
// test of a check living inline in UpdateWorkItem would be DB-gated, and a
// DB-gated test here runs only in its own scoped CI step, so deleting the check
// would leave `go test ./...` entirely green.
//
// json.RawMessage preserves `"declared_resources": null` as the four literal
// bytes, so `!= nil` is true and the field counted as part of the patch.
// ValidateDeclaredResources returns early for "null" without complaint, so the
// column was overwritten with a jsonb null: every declaration silently
// destroyed, resources_version bumped, HTTP 200.
//
// That was already wrong before this work item, and the lock release makes it
// materially worse — a null payload derives an EMPTY key set, so it would also
// drop every file_scope lock the work item holds. A caller sending null meaning
// "leave this alone" would lose its declarations AND its write protection in one
// call with nothing to notice, which is the exact silent-loss-of-protection
// shape aihub#264 is about. Measured before the fold was added: the update
// returned 200, stored `null`, and left the attempt holding no locks.
//
// Folded to "not specified", matching normalizeAttrsPatch. Clearing declarations
// still has a spelling, and it is the one the schema documents: an empty array.
func normalizeDeclaredResources(req *UpdateWorkItemRequest) {
	if req.DeclaredResources != nil && bytes.Equal(bytes.TrimSpace(req.DeclaredResources), []byte("null")) {
		req.DeclaredResources = nil
	}
}

// validateAttrsPatch rejects the three attrs payloads that must never reach
// Postgres (aihub#288, aihub#465). Run normalizeAttrsPatch first.
//
// Extracted as a pure function for the same reason isCASConflict was: the only
// behavioural test of a check living inline in UpdateWorkItem would be DB-gated,
// and a DB-gated test in this repo runs only in its own scoped CI step — deleting
// the check would leave `go test ./...` entirely green (mem_I98xpPgY).
//
//  1. An `attrs_patch` that is not a JSON object. `jsonb || jsonb` accepts
//     arrays and scalars but does something quite different with them —
//     `'{"a":1}'::jsonb || '[1]'::jsonb` yields an ARRAY, not an object — and
//     malformed JSON would surface as a 500 from the driver. A caller error
//     must be a 400. Checked BEFORE the combination rule below so a bad patch is
//     reported as a bad patch rather than as a conflict.
//
//  2. An `attrs` that is not a JSON object (aihub#465). `attrs` is a plain
//     column assignment rather than a `||` merge, so Postgres stores whatever
//     JSON arrives — including a JSON *string* — and answers 200, leaving the
//     column holding a string where every reader expects an object. Measured:
//     two live `pf_update_work_item` calls did exactly that. Checked in the
//     same place as the patch shape, and BEFORE the combination rule below,
//     for the same reason: a bad `attrs` is reported as a bad `attrs` rather
//     than as a conflict with `attrs_patch`.
//
//  3. `attrs` together with `attrs_patch`/`attrs_unset`. They are contradictory
//     instructions for one column — REPLACE everything vs. keep everything and
//     amend it. Applying both would make the result depend on clause order,
//     which is exactly the kind of silent, order-dependent outcome this work
//     item exists to remove. Fail loudly instead.
func validateAttrsPatch(req *UpdateWorkItemRequest) *AihubError {
	if req.AttrsPatch != nil {
		var probe map[string]json.RawMessage
		if err := json.Unmarshal(req.AttrsPatch, &probe); err != nil || probe == nil {
			return jsonObjectParamErr("attrs_patch", req.AttrsPatch)
		}
	}
	if aihubErr := validateJSONObjectParam("attrs", req.Attrs); aihubErr != nil {
		return aihubErr
	}
	if req.Attrs != nil && (req.AttrsPatch != nil || req.AttrsUnset != nil) {
		return NewErr(ErrBadRequest,
			"attrs cannot be combined with attrs_patch/attrs_unset: attrs REPLACES the whole object, attrs_patch/attrs_unset amend it; send one or the other")
	}
	return nil
}

// validateJSONObjectParam rejects a CALLER-SUPPLIED jsonb object parameter that
// is not a JSON object (aihub#465).
//
// The four such parameters — `attrs`, `attrs_patch`, `payload` and
// `structured_payload` — were all published as objects and all bound to a bare
// json.RawMessage, so before this guard only `attrs_patch` checked its shape.
// The other three answered 200 and stored whatever JSON arrived. The population
// that actually arrives is not hypothetical: measured over 1,170 real
// object-parameter calls in the transcript corpus (2026-09-08), 1,151 were
// objects, 19 were JSON-encoded STRINGS of an object, and none was an array, a
// number or a boolean. So the reject face is "must be a JSON object", which
// matches attrs_patch, and widening it costs no measured caller anything.
//
// Two things this deliberately does NOT do:
//
//   - It does not coerce. Decoding the string and storing the result would
//     rescue 1 of those 19 calls: 18 of them are malformed JSON, so the value
//     recovered would be a guess at what the caller meant. A caller who is
//     told gets it right on the retry; a caller who is silently "fixed" never
//     learns, and the one time the guess is wrong the wrong data is stored
//     under a 200.
//
//   - It does not touch a literal `null`, which keeps whatever meaning each
//     field already gave it (folded to "not supplied" by normalizeAttrsPatch,
//     stored as a JSON null by `attrs` and `payload`). Changing that is a
//     separate decision from the shape guard and nothing in the measured
//     population sends it.
//
// CALLER-SUPPLIED is the load-bearing word, and the split is by provenance, not
// by field (mem_X8JDSC96): the same bytes read back OUT of a jsonb column must
// never be rejected, or every path that re-remembers a stored row would start
// failing on data written before this guard existed — punishing a caller for a
// bug they did not cause. UpdateMemory is the one place in this repo that feeds
// stored attrs back into a write, and it names that provenance explicitly.
func validateJSONObjectParam(field string, raw json.RawMessage) *AihubError {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null")) {
		return nil
	}
	var probe map[string]json.RawMessage
	if err := json.Unmarshal(raw, &probe); err != nil || probe == nil {
		return jsonObjectParamErr(field, raw)
	}
	return nil
}

// jsonValueKind names the JSON type that actually arrived, for the rejection
// message. It parses rather than switching on the first byte, so a value that
// merely STARTS like an object (`{"a":`) is reported as malformed JSON instead
// of being mislabelled an object the check has just refused.
func jsonValueKind(raw json.RawMessage) string {
	var v any
	if err := json.Unmarshal(raw, &v); err != nil {
		return "malformed JSON"
	}
	switch v.(type) {
	case map[string]any:
		return "a JSON object"
	case []any:
		return "a JSON array"
	case string:
		return "a JSON string"
	case bool:
		return "a JSON boolean"
	case float64:
		return "a JSON number"
	case nil:
		// Unreachable for the OUTER value: normalizeAttrsPatch folds a literal
		// null to "not supplied" before this runs, and validateJSONObjectParam
		// folds it for the other three fields. Reachable for the value INSIDE a
		// JSON-encoded string (`"null"`), and named anyway so a future caller of
		// this helper cannot get "unknown" for a type JSON does have.
		return "a JSON null"
	}
	return "an unrecognised JSON value"
}

// jsonObjectParamSizeNote is the size sentence each guarded jsonb object
// parameter's rejection carries, keyed by the field name.
//
// A per-field table rather than one constant because the claim is not true of
// every field: `payload` IS capped, at 65,536 bytes in EmitEvent, so the
// unconditional "has no length cap" wording would have shipped a false
// statement to every pf_emit_event caller (aihub#465). A field absent from this
// table gets no size sentence at all rather than an invented one, and
// TestJSONObjectParamSizeNoteCoversEveryGuardedField fails if a guarded field
// has no entry — an empty note is a silent omission, which is the failure this
// table exists to prevent.
//
// The three "no length cap" entries are measured, not assumed (aihub#420,
// 2026-09-07): PATCH accepted an attrs_patch of 199,983 bytes and 8,016 bytes
// travelled the full MCP tool path, both HTTP 200; `attrs` and
// `structured_payload` reach the same column through the same handlers, and
// code reading finds no length check on any of the three anywhere in the
// request path.
var jsonObjectParamSizeNote = map[string]string{
	"attrs":              "attrs has no length cap",
	"attrs_patch":        "attrs_patch has no length cap",
	"structured_payload": "structured_payload has no length cap",
	"payload":            "payload's own 64KB cap is checked first, and this value is under it",
}

// JSONObjectParamSizeNote returns the size sentence one guarded field's shape
// rejection carries, or "" for a field this package does not guard.
//
// GuardedJSONObjectParams returns every field the table covers, sorted.
//
// Both exported for aihub#495. aihub#486 published "Size is never the reason for
// that 400: this field has no length cap" in six MCP parameter descriptions
// (internal/mcp, `jsonObjectPropNote`) and left the claim hanging: the only
// assertion behind it, TestSizeNoteMatchesTheCapThatActuallyExists, reads THIS
// table and never the schema text, so a published sentence that stopped being
// true — or a per-field split like `payload`'s copied onto the wrong parameter —
// would have shipped green.
//
// The table is the right thing for the MCP layer to read rather than a second
// constant of its own, precisely because that test already checks each entry
// against the behaviour it describes: a 200 KB object accepted for the three
// uncapped fields, a 70 KB `payload` refused. A hop-1 gate that reads this map
// therefore reaches the server's actual behaviour in two links, instead of
// comparing one hand-typed sentence to another hand-typed sentence.
func JSONObjectParamSizeNote(field string) string { return jsonObjectParamSizeNote[field] }

// GuardedJSONObjectParams returns the fields jsonObjectParamSizeNote covers.
func GuardedJSONObjectParams() []string {
	out := make([]string, 0, len(jsonObjectParamSizeNote))
	for field := range jsonObjectParamSizeNote {
		out = append(out, field)
	}
	sort.Strings(out)
	return out
}

// jsonObjectParamErr builds the 400 for a jsonb object parameter that is not an
// object. One builder for all four fields (aihub#465) rather than a copy per
// field: the caller mistake is identical and so is the repair, and a second
// copy would be a second message to keep true.
//
// aihub#420. The message used to be the bare sentence "attrs_patch must be a
// JSON object" — it named the expectation and never the observation, so FIVE
// materially different caller mistakes (array, string, number, boolean,
// malformed) produced one indistinguishable line. One of those five reads to
// the caller as a server bug about something else entirely: a client that
// serialises a large object argument as a JSON-encoded STRING sends a payload
// its author correctly describes as an object, reads "must be a JSON object",
// looks at their object, and concludes the real constraint is size. That is not
// hypothetical — it is how aihub#420 came to be filed, against a limit that does
// not exist.
//
// So the message names the type and the byte length received, and details
// carries both machine-readably, following vocabularyErr's split: the message
// because it is the only part some clients surface, details because an
// automated caller should not have to parse prose to retry correctly.
//
// The closing clause is stated on EVERY kind rather than only on the large or
// string-shaped ones, because a threshold for "large enough that the caller
// might blame size" is not defensible. Its wording is per-field, because for
// one of the four fields it is not unconditionally true — see
// jsonObjectParamSizeNote.
//
// aihub#465 adds the branch for the case that actually occurs. #420's
// high-signal clause fires only when the inner string decodes to an object, and
// measured over the transcript corpus that is 1 of 19 stringified payloads: the
// other 18 are malformed JSON, one closing brace short or carrying a broken
// \uXXXX escape, because a model hand-wrote the escaped text rather than a
// client wrapping a real object in quotes. So the clause #420 wrote had reached
// a caller exactly never, and the one payload it would have matched was a
// `structured_payload`, which had no guard to reach it through. The malformed
// branch therefore carries its own repair instruction plus the parse error, and
// `details.string_decodes_to` states which of the two it was machine-readably —
// a message that says only "must be a JSON object" to someone whose escaped
// text is truncated tells them nothing they can act on.
//
// The malformed branch's guidance is gated on the inner text opening with `{`,
// because the sentence claims to know what the caller was trying to do. That
// gate is measured, not guessed: 19 of 19 real inner strings start with `{`
// after trimming, so it costs nothing, while an ungated version would tell a
// caller who genuinely meant the string "nope" that they mis-escaped some JSON.
// Checking that the clause fires on the REAL population, rather than on the
// population the author imagined, is the whole lesson of #420.
func jsonObjectParamErr(field string, raw json.RawMessage) *AihubError {
	kind := jsonValueKind(raw)
	msg := fmt.Sprintf("%s must be a JSON object; got %s of %d bytes", field, kind, len(raw))
	details := map[string]any{
		"field": field,
		"got":   kind,
		"bytes": len(raw),
	}
	if kind == "a JSON string" {
		var inner string
		if json.Unmarshal(raw, &inner) == nil {
			var probe any
			switch perr := json.Unmarshal([]byte(inner), &probe); {
			case perr != nil:
				// The case that actually happens: hand-escaped JSON that came
				// out broken. Name the parse error — it is the only part that
				// says WHERE to look.
				details["string_decodes_to"] = "malformed JSON"
				details["string_decode_error"] = perr.Error()
				if bytes.HasPrefix(bytes.TrimSpace([]byte(inner)), []byte("{")) {
					msg += fmt.Sprintf(", and that string opens like a JSON object but does not parse (%v)"+
						"; do not hand-write the escaped JSON, send the object itself and let your client escape it", perr)
				}
			default:
				// The #420 case: the bytes ARE the object the caller meant,
				// wrapped in quotes. Say so, so the caller looks at their
				// serialisation instead of at their data.
				innerKind := jsonValueKind(json.RawMessage(inner))
				details["string_decodes_to"] = innerKind
				if innerKind == "a JSON object" {
					msg += ", and that string decodes to a JSON object; send the object itself, not a JSON-encoded string of it"
				} else {
					msg += fmt.Sprintf(", and that string decodes to %s, not an object"+
						"; send the object itself, not a JSON-encoded string of it", innerKind)
				}
			}
		}
	}
	if note := jsonObjectParamSizeNote[field]; note != "" {
		msg += ". Size is never the reason for this rejection: " + note
	}
	return NewErrDetails(ErrBadRequest, msg, details)
}

// casVersionUnknown is the placeholder reported when the current
// resources_version could not be re-read after a failed compare-and-set.
const casVersionUnknown = -1

// isCASConflict reports whether a completed UPDATE must be rejected as a
// compare-and-set conflict (aihub#241).
//
// Split out so the decision has coverage that actually executes: review of this
// change found that deleting the branch in UpdateWorkItem left `go test ./...`
// entirely green, because the only behavioural check of it lives in the
// DB-gated suite that SKIPs everywhere except its own scoped CI step
// (mem_I98xpPgY). The branch is a one-liner; the failure it would let through
// is not — a failed CAS silently returning 200 is exactly the silent
// unprotected overwrite this work item exists to remove.
//
// Zero rows is only meaningful when the caller asked for CAS. Without a
// version the WHERE clause is `id = $n` alone, and a work item whose row
// vanished between GetWorkItem and here is a 404, not a conflict — the caller
// of this helper distinguishes those two.
func isCASConflict(cas bool, rowsAffected int64) bool {
	return cas && rowsAffected == 0
}

// casConflictErr builds the 409 for a failed compare-and-set. Never a 400: the
// caller's payload was well-formed, someone else simply wrote
// declared_resources first. `current` may be casVersionUnknown when the re-read
// failed, which must not stop the conflict from being reported.
func casConflictErr(expected, current int) *AihubError {
	currentText := strconv.Itoa(current)
	if current == casVersionUnknown {
		currentText = "unknown"
	}
	return NewErrDetails(ErrConflictCASFailed,
		fmt.Sprintf("declared_resources CAS failed: resources_version is %s, not the expected %d; reread the work item and retry with its current resources_version", currentText, expected),
		map[string]any{
			"expected_resources_version": expected,
			"current_resources_version":  current,
		})
}

// releaseUndeclaredLocksSQL, the DELETE this function runs, lives in
// resource_events.go with every other statement that mutates resource_locks
// (aihub#343). Its doc comment moved with it.

// releaseUndeclaredFileScopeLocks releases the file_scope locks that `prior`
// justified and `next` no longer does (aihub#264).
//
// 🔴 PREVENTION, NOT CLEANUP — and the reported lock is not in scope.
//
// The candidate set is `prior − next`, so only a key present in the declaration
// this update REPLACES can be released. ieops#798's leaked lock came from a
// declaration dropped several resources_versions before the one it now stores,
// so it is in neither side and no future update of that work item will release
// it. This change stops new residue accruing; it does not sweep residue that
// already exists. The recoveries for an already-leaked lock are to re-declare
// the path and remove it again (one narrowing, now effective), to end the
// attempt, or to let the orphan sweep in gc.go take it once the owning attempt
// is no longer live.
//
// Widening the candidate set to "every file_scope lock this work item's attempts
// hold, minus next" WOULD clear that residue, and was deliberately not taken:
// it would also release locks from a client-supplied requested_locks that never
// had a declaration behind them (run_attempts.go:325-332 — the raw-API path the
// plugin never uses, but which is trusted verbatim when present). That is a
// bigger behaviour change than this item asked for, and it belongs with a
// decision about whether an undeclared lock should be able to exist at all.
//
// # Why this hangs off the update path
//
// Acquisition lives on the claim path and mutation lives here, and nothing
// connected them — which is the whole defect. It is fixed at the moment the
// declaration changes rather than when a blocked caller asks, deliberately: a
// holder can be between two declaration updates, so deciding "not in the current
// declarations, therefore free" at QUERY time would hand away a lock the holder
// is about to re-declare. Releasing at write time has no such window, and it
// keeps resources_version and the lock set moving together — this runs inside the
// same transaction as the UPDATE and after the CAS check, so a rejected update
// releases nothing and there is never a committed state where the version did not
// advance but the locks changed.
//
// # Why file_scope only
//
// Not an oversight, and not simply "that is what was reported" — it is a
// reversibility argument. FnAcquireLocks re-acquires file_scope and nothing else,
// so every lock this function can release has a documented way to be taken back
// within the same attempt, and a narrowing made by mistake costs one
// pf_acquire_locks call. git_branch and deploy_env have no such path: they are
// taken at claim and re-derived only on the next claim/resume, so releasing one
// here would leave the work item unprotected until then, and would let a second
// attempt take a branch this one still has checked out. That is the same hazard
// derivedLock's comment already refuses to open for intent=read, and it is not
// worth opening for a narrowing either. A work item that really means to give up
// a branch can pause, which already releases file_scope and re-derives the rest.
//
// # 🔴 It also REPORTS the subtraction it performed (aihub#343)
//
// The returned narrowingDiff is what wi_resources_updated is built from, and
// that is a correctness requirement rather than convenience. The first version
// of that event recomputed the diff from the derived KEYS while this function
// subtracts on the declared PATHS, and those are different answers in the flow
// the comment above calls "the ordinary polyforge flow": drop a
// {"type":"repo"} entry with the paths untouched and every key changes while
// every path stays. Measured on the repo's own aihub#261 fixture, the key-based
// record reported the two keys that were STILL HELD as removed and two keys
// held by NOBODY as added — a checkable, wrong record, which this file's own
// comments call worse than none.
//
// So there is exactly ONE subtraction in the codebase and the audit describes
// it by construction. Do not recompute it anywhere.
func releaseUndeclaredFileScopeLocks(ctx context.Context, tx pgx.Tx, wiID, project string, prior, next json.RawMessage, op lockOpCtx) (narrowingDiff, *AihubError) {
	priorLocks, ok := derivedFileScopeLocks(prior, project)
	if !ok {
		// Unparseable stored declarations: which locks they produced is unknown,
		// so releasing any of them would be a guess. Leaving them is the
		// pre-aihub#264 behaviour and is the safe direction.
		//
		// Reported as Readable=false rather than as an empty diff: "the
		// declaration justified no locks" and "we could not read the
		// declaration" must not look alike in an audit record, and the
		// unreadable case is the common one here (~14% of stored payloads).
		return narrowingDiff{}, nil
	}
	nextLocks, ok := derivedFileScopeLocks(next, project)
	if !ok {
		// Unreachable from the REST/MCP surface: UpdateWorkItem runs
		// ValidateDeclaredResources on caller input before opening the
		// transaction. Guarded rather than asserted, because "release everything
		// the old payload had" is the wrong answer to a payload we cannot read.
		return narrowingDiff{}, nil
	}

	// 🔴 Subtract on the declared PATH, not on the derived key (aihub#261).
	//
	// `keys(prior) − keys(next)` silently assumed a derived key changes only when
	// its path is dropped. Since the key gained a repo segment that is false: the
	// key also changes when the payload's repo inference changes with the paths
	// untouched, and then every still-declared path looks removed and its lock is
	// deleted with nothing to re-acquire it.
	//
	// The reachable case is the ordinary flow, not a corner: pf-plan Step 5
	// rewrites declared_resources as PATH ENTRIES ONLY, in a whole-list replace.
	// A work item claimed with a {"type":"repo"} entry holds repo-qualified locks,
	// and the next /pf-plan drops that entry. Measured on the key subtraction:
	// the attempt was left running with ZERO file_scope locks, silently — the
	// exact loss-of-protection aihub#264 exists to prevent.
	//
	// Releasing by path also makes this immune to any FUTURE change of key
	// format, which is the property that was missing rather than this particular
	// format being wrong. The keys still come from the prior derivation, so what
	// is deleted is always a key that derivation really produced.
	stillDeclared := make(map[string]bool, len(nextLocks))
	for _, path := range nextLocks {
		stillDeclared[path] = true
	}
	removedPaths := map[string]bool{}
	for _, path := range priorLocks {
		if !stillDeclared[path] {
			removedPaths[path] = true
		}
	}
	// aihub#343: the diff is assembled here, from the values this function
	// actually subtracted, and handed back for the audit record.
	diff := narrowingDiff{
		Readable:     true,
		PriorPaths:   uniqueSorted(priorLocks),
		NextPaths:    uniqueSorted(nextLocks),
		RemovedPaths: sortedSetMembers(removedPaths),
	}
	wasDeclared := make(map[string]bool, len(priorLocks))
	for _, path := range priorLocks {
		wasDeclared[path] = true
	}
	for _, path := range nextLocks {
		if !wasDeclared[path] {
			diff.AddedPaths = append(diff.AddedPaths, path)
		}
	}
	sort.Strings(diff.AddedPaths)

	if len(removedPaths) == 0 {
		return diff, nil
	}
	// Every key form each removed path could be held under, because the row was
	// written once under whatever format was current then and is never rewritten.
	// fileScopeConflictProbe with an empty repo is exactly "this path in ANY
	// repo", which is the set wanted here, so the coverage rule lives in one
	// place rather than being spelled a second time.
	removed := make([]string, 0, len(removedPaths))
	patterns := make([]string, 0, len(removedPaths))
	for path := range removedPaths {
		probe := fileScopeConflictProbe(project, "", "file:"+path)
		removed = append(removed, probe.Keys...)
		if probe.LikePattern != "" {
			patterns = append(patterns, probe.LikePattern)
		}
	}
	// Sorted so the statement's parameters are deterministic and a failure is
	// reproducible. That is the whole claim: `resource_key = ANY($2)` does not
	// scan or row-lock in array order, so this does NOT influence lock ordering,
	// and it does not need to — two different work items can never target
	// overlapping rows here, so these deletes cannot deadlock against each other.
	sort.Strings(removed)

	// dbErrCause already maps a class 40 rollback to a retryable 409 before
	// falling back to ErrInternalError (aihub#334), and ErrInternalError is
	// exactly the non-conflict outcome wanted here, so it is the whole mapping.
	sort.Strings(patterns)
	// aihub#343: through releaseLocks, so each lock this narrowing actually drops
	// gets a lock_released event carrying cause=declaration_narrowed. This is the
	// replay set in aihub#343's acceptance criterion — a wi declares a path, gets
	// the lock, removes the declaration, and a second wi claims the same path.
	// Without this event the second claimer cannot tell "the release happened"
	// from "the release never happened and the row is stale".
	//
	// No `removed` list is attached to the per-lock payloads. Each lock_released
	// already carries its own resource_key, so the list would be the same data
	// repeated once per event — O(N²) bytes against a 64KB payload cap, under a
	// field name wi_resources_updated uses for a different computation. The op_id
	// is the join; duplication is what lets two records disagree.
	released, err := releaseLocks(ctx, tx, releaseUndeclaredLocksSQL, op, wiID, removed, patterns)
	if err != nil {
		return diff, dbErrCause(err, "failed to release locks for removed declared_resources")
	}
	for _, r := range released {
		diff.ReleasedKeys = append(diff.ReleasedKeys, r.ResourceKey)
	}
	sort.Strings(diff.ReleasedKeys)
	return diff, nil
}

// narrowingDiff is the subtraction releaseUndeclaredFileScopeLocks performed,
// reported so wi_resources_updated describes THAT computation rather than a
// second one that can disagree with it (aihub#343).
//
// Paths, not derived keys, on every field except ReleasedKeys. A path is what
// the declaration says and is immune to changes in lock-key FORMAT; a key
// changes whenever the payload's repo inference changes, with the paths
// untouched. Subtracting on keys is the defect aihub#261 removed from the
// release path, and it reappeared in the audit record until this type existed.
//
// ReleasedKeys is the exception on purpose: it is not a subtraction at all, it
// is what the DELETE's RETURNING clause actually reported, so it names the rows
// that really went away rather than the rows that ought to have.
type narrowingDiff struct {
	// Readable is false when a stored declaration could not be decoded, in which
	// case no other field means anything. Reported separately so "justified no
	// locks" and "could not be read" do not look alike.
	Readable     bool
	PriorPaths   []string
	NextPaths    []string
	AddedPaths   []string
	RemovedPaths []string
	ReleasedKeys []string
}

// uniqueSorted returns the distinct values of a key→path map, sorted. Distinct
// because two declared entries (a `path` and a `document`, say) can name one
// path and would otherwise be counted twice in the audit record.
func uniqueSorted(byKey map[string]string) []string {
	seen := make(map[string]bool, len(byKey))
	out := make([]string, 0, len(byKey))
	for _, v := range byKey {
		if seen[v] {
			continue
		}
		seen[v] = true
		out = append(out, v)
	}
	sort.Strings(out)
	return out
}

// sortedSetMembers returns a set's members, sorted. Named for the set rather
// than `sortedKeys`, which declared_resources.go already uses to render a TYPE
// set into an error message — two functions with one name in one package would
// have been a compile error today and a confusing near-miss if either moved.
func sortedSetMembers(set map[string]bool) []string {
	out := make([]string, 0, len(set))
	for k := range set {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// ── The work_items editability matrix (aihub#440 / aihub#411 T2-1) ───────────
//
// ONE matrix for every field UpdateWorkItem can write, and ONE error code per
// rejection KIND. Before this, editability was per-field with three status sets
// and three codes, and the two kinds were crossed in OPPOSITE directions:
//
//	goal    — wrong caller AND wrong state both answered 409 GOAL_CHANGE_NOT_ALLOWED
//	wi_type — wrong caller AND wrong state both answered 403 WI_RECLASSIFY_FORBIDDEN
//	content — wrong state answered 409 CONFLICT_TERMINAL_STATE, on a wider status set
//	attrs, attrs_patch, attrs_unset, labels, priority, milestone,
//	requires_human_session, declared_resources — no status guard at all
//
// So a wrong CALLER on goal was reported as a state conflict, and a wrong STATE
// on wi_type was reported as a permission failure: the exact conflation
// aihub#242 removed from the cancel path, mirrored, on the most-called tool in
// the measured window (pf_update_work_item, 354 calls / 21 days per aihub#411
// §0.1; 738 in this wi's own wider re-count).
//
// ⚠️ aihub#411's T2-1 row records goal's code as a **400**. That is where the
// design doc files it (§17's HTTP 400 block), but codeToHTTPStatus has mapped it
// to 409 since before that row's own base (1ec3bdc) — the row's cell is a doc
// reading, not a measurement of the server. The defect fixed here is the KIND
// being crossed, not the number.
//
// # The matrix
//
// Three status classes and three field tiers. A status class answers "who may
// be acting on this record right now":
//
//	open   = queued, paused, blocked      — nobody is executing it
//	live   = running                      — an attempt is mid-flight against it
//	closed = wrapped, failed, cancelled   — the record is history
//
//	                                                   │ open │ live │ closed
//	 ──────────────────────────────────────────────────┼──────┼──────┼────────
//	 contract  goal, wi_type                           │  ✓*  │ 409A │  409T
//	 working   content, labels, priority, milestone,   │  ✓   │  ✓   │  409T
//	           requires_human_session,                 │      │      │
//	           declared_resources                      │      │      │
//	 record    attrs, attrs_patch, attrs_unset         │  ✓   │  ✓   │   ✓
//
//	✓*    additionally requires reporter | project maintainer | admin,
//	      else 403 FORBIDDEN — checked AFTER the state, never instead of it.
//	409A  CONFLICT_WI_ALREADY_CLAIMED   409T  CONFLICT_TERMINAL_STATE
//
// Every cell above is a subtest of TestUpdateGate (3 tiers × 7 statuses × 4
// actors, no DB required), and the row set is pinned to the struct rather than
// to this comment: TestEveryWritableUpdateFieldHasATier fails if a field
// buildWorkItemUpdate can write is missing a tier, which is what "no field
// silently exempt" has to mean to survive the next field being added.
//
// # Why these three codes and no field-specific ones
//
// The 409 names the STATE and the 403 names the CALLER, so a caller derives the
// remedy from the code alone: 403 → get a different caller; 409 → change the
// state and retry. That is cancelGate's shipped shape verbatim — it answers
// CONFLICT_WI_ALREADY_CLAIMED for running, CONFLICT_TERMINAL_STATE for terminal
// and ErrForbidden for a wrong caller — and pf_cancel_work_item's contract card
// already reads the T2-1 ruling that way ("this tool's three 409s are three
// distinct states, which is the shape that ruling asks for"). Carrying it across
// means update's state codes ARE cancel's state codes, so one rule covers both
// tools instead of one per field.
//
// The 403 half is not an invention of this change: design §4.3 already specified
// "goal 更新失败 … 403 FORBIDDEN（非 reporter/maintainer）" and the code returned
// 409 GOAL_CHANGE_NOT_ALLOWED there instead. This makes the server agree with the
// row that was already written.
//
// GOAL_CHANGE_NOT_ALLOWED and WI_RECLASSIFY_FORBIDDEN are therefore no longer
// produced anywhere. They are RETIRED rather than deleted — see errors.go for
// why, and for the gate that stops them coming back.
//
// # Why `open` includes blocked (this widens two fields)
//
// cancelGate already admits blocked for the most destructive operation there is,
// on the argument aihub#242 wrote down: a blocked wi has no live attempt to
// invalidate, and its reporter must have an exit. Editing goal or wi_type on a
// blocked wi invalidates nobody's work for exactly the same reason. Keeping
// blocked out of the contract tier would have needed a THIRD state code meaning
// "blocked is neither running nor terminal" — a code minted to describe a
// restriction with no argument behind it.
//
// # Why the record tier stays writable on a CLOSED record
//
// This is the sub-question T2-1 left open in both directions ("whether attrs
// staying writable on a terminal work item is the defect or the feature"). It is
// the FEATURE, and the evidence is traffic rather than taste. Measured over the
// 21-day transcript corpus (87 files, 738 pf_update_work_item calls, 715 whose
// response carried a status):
//
//	closed-record calls           49  (48 wrapped + 1 cancelled)
//	  … carrying attrs_patch      28
//	  … carrying attrs            20
//	  … carrying attrs_unset       1
//	  … carrying ANY other field   0
//
// So the record tier is load-bearing — post-hoc decision and merge records are
// written onto wrapped wis, including by the batch that shipped this change —
// while the working tier's new refusal on a closed record breaks zero measured
// calls. `milestone` never appears in any of the 738 calls at all, so its cell is
// unexercised by real traffic and rests on the tier argument alone.
//
// # Mixing tiers in one patch
//
// The STRICTEST tier any supplied field belongs to governs the whole patch, and
// the refusal names a field at that tier. So attrs_patch + labels against a
// wrapped wi is refused whole rather than partially applied: a PATCH that wrote
// some of its fields and refused others would need a response shape that says
// which, and there is none. Measured: no closed-record call in the corpus mixes
// tiers, so this rule costs nothing today.
//
// # What this gate deliberately does NOT do
//
// It is not re-run inside the transaction against a FOR UPDATE row, which
// cancelGate is. The difference is the blast radius of losing the race: cancel
// releases a possibly-live attempt's resource locks, while update writes only
// this work item's own columns, so a claim committing underneath yields an edit
// against a status that has since moved — stale, but not another attempt's locks
// dropped. That race predates this change and is unchanged by it.
type wiEditTier int

// The tiers are ordered by strictness so that the strictest supplied field wins
// with a plain `>` comparison. Do not reorder without re-reading
// strictestSuppliedEditTier.
const (
	wiTierRecord wiEditTier = iota
	wiTierWorking
	wiTierContract
)

func (t wiEditTier) String() string {
	switch t {
	case wiTierRecord:
		return "record"
	case wiTierWorking:
		return "working"
	case wiTierContract:
		return "contract"
	}
	return fmt.Sprintf("wiEditTier(%d)", int(t))
}

// wiStatusClass is the other axis of the matrix above.
type wiStatusClass int

const (
	wiStatusOpen wiStatusClass = iota
	wiStatusLive
	wiStatusClosed
	// wiStatusUnknown is not a class a legal status can have. It exists so that
	// adding a status to the work_items.status CHECK without adding it here
	// fails closed instead of silently landing in the most permissive class.
	// TestEveryWorkItemStatusIsClassified makes it unreachable.
	wiStatusUnknown
)

// WorkItemFieldsByEditTier returns the editability matrix's ROWS, keyed by tier
// name ("contract" | "working" | "record"), each list sorted.
//
// WorkItemStatusesByEditClass returns its COLUMNS, keyed by class name ("open" |
// "live" | "closed"), each list sorted, derived by classifying every value in
// WorkItemStatusValues().
//
// Both exported for aihub#495, whose subject is that this matrix shipped
// ENFORCED and unpublished: the tool description said "Update a work item (goal,
// wi_type, priority, labels, etc.)" while five fields had just gone from writable
// to 409 on a terminal work item. The repair is text, and text alone rots — so
// internal/mcp gates its published description against these two functions rather
// than against a list retyped there. Adding a field to the working tier without
// naming it at hop 1 is then a red test, which is the only version of this fix
// that survives the next field.
//
// They return the membership, not the verdict. updateGate remains the only thing
// that decides an outcome; a second implementation of the decision, exported for
// a test to compare against, would be two decisions.
func WorkItemFieldsByEditTier() map[string][]string {
	out := map[string][]string{}
	for field, tier := range wiEditTierByField {
		out[tier.String()] = append(out[tier.String()], field)
	}
	for _, fields := range out {
		sort.Strings(fields)
	}
	return out
}

// WorkItemStatusesByEditClass returns the matrix's status columns.
func WorkItemStatusesByEditClass() map[string][]string {
	names := map[wiStatusClass]string{
		wiStatusOpen:   "open",
		wiStatusLive:   "live",
		wiStatusClosed: "closed",
	}
	out := map[string][]string{}
	for _, status := range WorkItemStatusValues() {
		name, ok := names[wiStatusClassOf(status)]
		if !ok {
			// Unreachable while TestEveryWorkItemStatusIsClassified is green. Not
			// skipped: a status that falls out of the classification must be
			// visible to whoever reads this, not quietly absent from every column.
			name = "unknown"
		}
		out[name] = append(out[name], status)
	}
	for _, statuses := range out {
		sort.Strings(statuses)
	}
	return out
}

// wiStatusClassOf classifies one work_items.status value.
func wiStatusClassOf(status string) wiStatusClass {
	switch status {
	case "queued", "paused", "blocked":
		return wiStatusOpen
	case "running":
		return wiStatusLive
	case "wrapped", "failed", "cancelled":
		return wiStatusClosed
	}
	return wiStatusUnknown
}

// wiEditTierByField assigns a tier to every json field UpdateWorkItemRequest can
// write a column from. It is keyed by json tag because that is the name the
// caller used and therefore the name a refusal must say back.
//
// Kept in sync with buildWorkItemUpdate — that function is the definition of
// "writable", and TestEveryWritableUpdateFieldHasATier compares the two.
var wiEditTierByField = map[string]wiEditTier{
	"goal":                   wiTierContract,
	"wi_type":                wiTierContract,
	"content":                wiTierWorking,
	"labels":                 wiTierWorking,
	"priority":               wiTierWorking,
	"milestone":              wiTierWorking,
	"requires_human_session": wiTierWorking,
	"declared_resources":     wiTierWorking,
	"attrs":                  wiTierRecord,
	"attrs_patch":            wiTierRecord,
	"attrs_unset":            wiTierRecord,
}

// wiEditRiderFields are the json fields UpdateWorkItemRequest binds that write no
// column of their own: two mandatory reasons and a compare-and-set token. They
// have no tier because sending one alone changes nothing, so gating it would
// refuse a request that was going to be a no-op anyway.
//
// requires_human_session is NOT here even though the wi_type path treats it as a
// rider: it writes its own column and 45 of the 49 calls that carried it in the
// corpus carried no wi_type, so it is a field in its own right and is tiered as
// one.
var wiEditRiderFields = map[string]bool{
	"goal_change_reason": true,
	"reclassify_reason":  true,
	"resources_version":  true,
}

// suppliedEditFields lists the tiered fields this patch actually supplies, in the
// matrix's own order so that the field a refusal names is deterministic.
//
// ⚠️ Must be called AFTER normalizeDeclaredResources and normalizeAttrsPatch.
// Those fold an explicit JSON `null` down to "not supplied", and a gate that ran
// first would refuse a caller for a field it did not send.
func suppliedEditFields(req *UpdateWorkItemRequest) []string {
	var out []string
	if req.Goal != nil {
		out = append(out, "goal")
	}
	if req.WIType != nil {
		out = append(out, "wi_type")
	}
	if req.Content != nil {
		out = append(out, "content")
	}
	if req.Labels != nil {
		out = append(out, "labels")
	}
	if req.Priority != nil {
		out = append(out, "priority")
	}
	if req.Milestone != nil {
		out = append(out, "milestone")
	}
	if req.RequiresHumanSession != nil {
		out = append(out, "requires_human_session")
	}
	if req.DeclaredResources != nil {
		out = append(out, "declared_resources")
	}
	if req.Attrs != nil {
		out = append(out, "attrs")
	}
	if req.AttrsPatch != nil {
		out = append(out, "attrs_patch")
	}
	if req.AttrsUnset != nil {
		out = append(out, "attrs_unset")
	}
	return out
}

// strictestSuppliedEditTier returns the tier that governs this patch, a field at
// that tier to name in a refusal, and whether the patch touches any tiered field
// at all. A patch of riders only (or of nothing) reports supplied=false.
func strictestSuppliedEditTier(req *UpdateWorkItemRequest) (wiEditTier, string, bool) {
	tier, field, supplied := wiTierRecord, "", false
	for _, f := range suppliedEditFields(req) {
		t, ok := wiEditTierByField[f]
		if !ok {
			// Unreachable while TestEveryWritableUpdateFieldHasATier is green;
			// treated as the strictest tier rather than skipped, because the
			// failure mode of skipping is a field with no guard at all.
			t = wiTierContract
		}
		if !supplied || t > tier {
			tier, field, supplied = t, f, true
		}
	}
	return tier, field, supplied
}

// updateGate is UpdateWorkItem's pure decision function, the same shape as
// cancelGate: state is checked BEFORE permission, so a state rejection is never
// reported as a permission failure. See wiEditTierByField for the matrix this
// implements and for every argument behind it.
func updateGate(status string, tier wiEditTier, field string, isReporter bool, callerRole, projectRole string) *AihubError {
	if tier == wiTierRecord {
		// The one deliberate exemption in the matrix, and the only one: the audit
		// record of a work item stays writable for as long as the record exists.
		return nil
	}

	switch wiStatusClassOf(status) {
	case wiStatusLive:
		if tier == wiTierContract {
			return NewErr(ErrConflictWIAlreadyClaimed,
				fmt.Sprintf("cannot update %s while work item is running; pause first", field))
		}
	case wiStatusClosed:
		return NewErr(ErrConflictTerminalState,
			fmt.Sprintf("cannot update %s when work item is in terminal state: %s; only attrs, attrs_patch and attrs_unset are writable on a terminal work item", field, status))
	case wiStatusUnknown:
		return NewErr(ErrConflictTerminalState,
			fmt.Sprintf("cannot update %s: work item status %q is not in this server's editability matrix", field, status))
	}

	if tier == wiTierContract {
		canEdit := callerRole == "admin" || projectRole == "maintainer" || isReporter
		if !canEdit {
			return NewErr(ErrForbidden,
				fmt.Sprintf("insufficient permissions: only the reporter, a project maintainer, or an admin may update %s", field))
		}
	}
	return nil
}

// UpdateWorkItem applies a patch to a work item.
func UpdateWorkItem(ctx context.Context, pool *pgxpool.Pool, idOrSlug string, callerUserID, callerRole string, callerProjectRoles map[string]string, req *UpdateWorkItemRequest) (*WorkItem, *AihubError) {
	wi, aihubErr := GetWorkItem(ctx, pool, idOrSlug)
	if aihubErr != nil {
		return nil, aihubErr
	}

	// aihub#264: fold an explicit null away BEFORE anything reads the field, so
	// validation, the UPDATE and the lock release all agree it was not supplied.
	normalizeDeclaredResources(req)

	// aihub#238: same entry-point validation as CreateWorkItem — an update must
	// not be able to replace good declared_resources with silently lockless ones.
	if req.DeclaredResources != nil {
		if vErr := ValidateDeclaredResources(req.DeclaredResources); vErr != nil {
			return nil, vErr
		}
	}

	// aihub#396: the same vocabularies and limits CreateWorkItem checks, on the
	// fields this request struct actually binds.
	//
	// ⚠️ Only three of the five, and that is a property of the struct rather than
	// a gap: UpdateWorkItemRequest has no `source` and no `parent_work_item_id`
	// json tag, so neither is reachable through this path at all. Adding either
	// field here means adding its check too — TestUpdateWorkItemRequestBindsOnly
	// TheFieldsThisPathValidates fails if one appears without one.
	//
	// Each is guarded by its own supplied-ness test, because on an update
	// "absent" and "empty" are different requests: a nil Priority means the
	// caller did not mention priority, and validating "" there would reject every
	// update that only touches attrs.
	if req.Priority != nil {
		if vErr := validateWorkItemPriority(*req.Priority); vErr != nil {
			return nil, vErr
		}
	}
	if req.Labels != nil {
		if vErr := validateWorkItemLabels(req.Labels); vErr != nil {
			return nil, vErr
		}
	}
	if vErr := validateWorkItemContent(req.Content); vErr != nil {
		return nil, vErr
	}

	// aihub#288: fold null-filled optionals away, then reject contradictory or
	// malformed attrs instructions — both before any write happens.
	normalizeAttrsPatch(req)
	if vErr := validateAttrsPatch(req); vErr != nil {
		return nil, vErr
	}

	// aihub#440: ONE editability gate for every field this patch touches — state
	// first, then permission, and the code names the KIND rather than the field.
	// See wiEditTierByField for the matrix, the three codes and the measurements
	// behind each cell. Placed AFTER the two normalizers above, because they fold
	// an explicit JSON null down to "not supplied" and a gate that ran first would
	// refuse a caller for a field it did not send.
	if tier, field, supplied := strictestSuppliedEditTier(req); supplied {
		isReporter := wi.ReporterUserID == callerUserID
		projectRole := callerProjectRoles[wi.Project]
		if gateErr := updateGate(wi.Status, tier, field, isReporter, callerRole, projectRole); gateErr != nil {
			return nil, gateErr
		}
	}

	// What is left of the old goal block is about the SHAPE of the request, not
	// about state or permission, so it stays a 400 and stays BEHIND the gate: a
	// caller whose edit is refused on state should not first be told its reason
	// string is too short for an edit it is not allowed to make.
	if req.Goal != nil {
		if req.GoalChangeReason == nil || len(*req.GoalChangeReason) < 10 {
			return nil, NewErr(ErrBadRequest, "goal_change_reason is required (min 10 chars) when updating goal")
		}
		// aihub#507: an EMPTY goal is refused here too, with create's own message,
		// from the function create calls. It used to be stored: work_items.goal is
		// TEXT NOT NULL and NOT NULL admits '', so nothing downstream objected and
		// the caller got a 200 and a work item that renders blank everywhere. This
		// is the first check on this path that is about the goal's VALUE rather
		// than its shape, and it is inside the `req.Goal != nil` guard on purpose:
		// clearing a goal is now refused, LEAVING IT ALONE is untouched, and only
		// the pointer can tell those two requests apart.
		//
		// It sits AFTER goal_change_reason rather than before it, which keeps the
		// error a caller sending `{goal: ""}` with no reason already gets. Adding
		// a refusal is this change; reordering two existing 400s is not, and a
		// caller branching on the old first answer should not have to find that out
		// from a bug report.
		if vErr := validateWorkItemGoalPresent(*req.Goal); vErr != nil {
			return nil, vErr
		}
		// aihub#474: BOTH shape halves, from the one function CreateWorkItem also
		// calls. Only the newline half used to be here, so an over-cap goal that
		// `pf_create_work_item` answers with a 400 got past this path and was
		// refused by the column CHECK instead — a 500 with a SQLSTATE in it, for
		// the same input. Sharing the function is what makes the two paths agree
		// by construction rather than by two literals that happen to match today.
		if vErr := validateWorkItemGoalShape(*req.Goal); vErr != nil {
			return nil, vErr
		}
	}

	// Same split as goal: the state and permission halves moved into updateGate
	// above, and only the request-shape check is left here.
	if req.WIType != nil {
		if req.ReclassifyReason == nil || len(*req.ReclassifyReason) < 10 {
			return nil, NewErr(ErrBadRequest, "reclassify_reason is required (min 10 chars) when updating wi_type")
		}

		// scenario_phase_configs has been removed; wi_type is accepted as-is from the caller.
		// requires_human_session must be provided by the caller when reclassifying.
	}

	tx, err := pool.Begin(ctx)
	if err != nil {
		return nil, NewErr(ErrInternalError, "failed to begin transaction")
	}
	defer tx.Rollback(ctx) //nolint:errcheck

	// content's status guard used to live here, inside the transaction. It is now
	// the working tier of the matrix and is decided by updateGate before the
	// transaction opens. Nothing about isolation changed: this check read
	// wi.Status, which is the SAME pre-transaction GetWorkItem snapshot the gate
	// reads, so moving it earlier neither tightens nor loosens the race described
	// at the end of wiEditTierByField's comment.

	// aihub#264: read the declaration this update is about to replace, inside the
	// transaction and FOR UPDATE, so the diff below is computed against the value
	// the UPDATE actually overwrites.
	//
	// The GetWorkItem at the top of this function ran on the pool before the
	// transaction opened, so wi.DeclaredResources is a pre-transaction read and
	// using it would leave a window in which a concurrent narrowing's locks are
	// resurrected or a concurrent widening's are dropped. FOR UPDATE also matches
	// FnAcquireLocks, which takes the same row lock before touching
	// resource_locks — same order (work_items then resource_locks) in both, so the
	// two cannot deadlock against each other. Taken only when declared_resources
	// is part of the patch, so no other update path changes its locking.
	//
	// aihub#343: resources_version comes out of the SAME row read, not from the
	// pre-transaction GetWorkItem. The audit event's whole job is to settle
	// arguments about this number (see emitResourcesUpdated), so reading it from
	// a snapshot that a concurrent writer may already have moved would make the
	// record wrong in exactly the case somebody goes looking.
	var priorDeclared json.RawMessage
	priorResourcesVersion := casVersionUnknown
	if req.DeclaredResources != nil {
		if scanErr := tx.QueryRow(ctx,
			`SELECT declared_resources, resources_version FROM work_items WHERE id = $1 FOR UPDATE`, wi.ID,
		).Scan(&priorDeclared, &priorResourcesVersion); scanErr != nil {
			if errors.Is(scanErr, pgx.ErrNoRows) {
				return nil, NewErr(ErrNotFound, fmt.Sprintf("work item %q not found", wi.ID))
			}
			if aerr := retryConflictErr(scanErr, "failed to read declared_resources for update"); aerr != nil {
				return nil, aerr
			}
			return nil, NewErr(ErrInternalError, "failed to read declared_resources for update")
		}
	}

	upd := buildWorkItemUpdate(req, wi.ID)
	tag, err := tx.Exec(ctx, upd.Query, upd.Args...)
	if err != nil {
		return nil, dbErrCause(err, "failed to update work_item")
	}
	if isCASConflict(upd.CAS, tag.RowsAffected()) {
		// Re-read inside the same transaction to find out what the row actually
		// holds, so the caller is told what to retry with.
		//
		// Isolation matters here and is worth stating: this transaction runs at
		// pool.Begin's default READ COMMITTED, so each statement takes a fresh
		// snapshot and this SELECT sees the value the winning writer committed.
		// Under Serializable it would instead see the snapshot from before the
		// conflict and report "is 0, not the expected 0" — nonsense. If this
		// function is ever moved onto pgx.Serializable (as run_attempts.go uses),
		// this read has to move outside the transaction.
		current := casVersionUnknown
		scanErr := tx.QueryRow(ctx, `SELECT resources_version FROM work_items WHERE id = $1`, wi.ID).Scan(&current)
		if errors.Is(scanErr, pgx.ErrNoRows) {
			// Not a conflict: the row is gone. GetWorkItem above ran on the pool
			// BEFORE this transaction opened, so there is a window — narrow, and
			// nothing outside tests deletes work items today, but reporting a
			// vanished row as a version conflict would send the caller into a
			// retry loop that can never succeed.
			return nil, NewErr(ErrNotFound, fmt.Sprintf("work item %q not found", wi.ID))
		}
		return nil, casConflictErr(*req.ResourcesVersion, current)
	}

	// aihub#264: the UPDATE has been applied and the CAS (if any) has passed, so
	// the narrowing is real — release the file_scope locks it orphaned. Placed
	// after the CAS check on purpose: a rejected update returns above and rolls
	// back, so it can never leave locks released against a version that did not
	// move.
	if req.DeclaredResources != nil {
		// aihub#343: ONE lock operation for the whole declaration change, so the
		// wi_resources_updated event and every lock_released it caused share an
		// op_id. That is what lets a reader say "these three locks went away
		// BECAUSE of that declaration change" instead of inferring it from
		// adjacent timestamps.
		resOp := newLockOp(lockCauseDeclarationNarrowed,
			lockEventActor{UserID: callerUserID})
		rel, aerr := releaseUndeclaredFileScopeLocks(ctx, tx, wi.ID, wi.Project, priorDeclared, req.DeclaredResources, resOp)
		if aerr != nil {
			return nil, aerr
		}
		// Read the new version back rather than computing prior+1: the UPDATE is
		// assembled by buildWorkItemUpdate and whether it incremented is that
		// function's decision, not this one's. An audit record that reported a
		// version the row does not hold would be checkable and wrong, which is
		// worse than absent.
		newResourcesVersion := casVersionUnknown
		if scanErr := tx.QueryRow(ctx,
			`SELECT resources_version FROM work_items WHERE id = $1`, wi.ID,
		).Scan(&newResourcesVersion); scanErr != nil {
			if aerr := retryConflictErr(scanErr, "failed to re-read resources_version"); aerr != nil {
				return nil, aerr
			}
			return nil, NewErr(ErrInternalError, "failed to re-read resources_version")
		}
		// `rel` — the subtraction the release above actually performed — not a
		// second computation. See narrowingDiff for the measurement that forced
		// this.
		emitResourcesUpdated(ctx, tx, wi.ID, wi.Project,
			priorResourcesVersion, newResourcesVersion,
			declaredEntryCount(priorDeclared), declaredEntryCount(req.DeclaredResources),
			rel, lockEventActor{UserID: callerUserID}, resOp.OpID)
	}

	// Emit goal_updated event if goal changed
	if req.Goal != nil && req.GoalChangeReason != nil {
		evtID := NewID("evt")
		payload, _ := json.Marshal(map[string]any{
			"old_goal":   wi.Goal,
			"new_goal":   *req.Goal,
			"reason":     *req.GoalChangeReason,
			"changed_by": callerUserID,
		})
		_, err = tx.Exec(ctx, `
			INSERT INTO agent_events (id, work_item_id, actor_user_id, actor_display, event_type, payload, project)
			VALUES ($1, $2, $3, $4, 'wi_goal_updated', $5, $6)`,
			evtID, wi.ID, callerUserID, "", payload, wi.Project,
		)
		if err != nil {
			return nil, dbErr(err, "failed to emit wi_goal_updated event")
		}
	}

	// Fix 3: emit wi_reclassified audit event if wi_type changed
	if req.WIType != nil {
		evtID := NewID("evt")
		oldWIType := ""
		if wi.WIType != nil {
			oldWIType = *wi.WIType
		}
		var oldRHS, newRHS *bool
		oldRHS = wi.RequiresHumanSession
		newRHS = req.RequiresHumanSession
		reason := ""
		if req.ReclassifyReason != nil {
			reason = *req.ReclassifyReason
		}
		payload, _ := json.Marshal(map[string]any{
			"old_wi_type":                oldWIType,
			"new_wi_type":                *req.WIType,
			"old_requires_human_session": oldRHS,
			"new_requires_human_session": newRHS,
			"reason":                     reason,
		})
		_, err = tx.Exec(ctx, `
			INSERT INTO agent_events (id, work_item_id, actor_user_id, actor_display, event_type, payload, project)
			VALUES ($1, $2, $3, $4, 'wi_reclassified', $5, $6)`,
			evtID, wi.ID, callerUserID, "", payload, wi.Project,
		)
		if err != nil {
			return nil, dbErr(err, "failed to emit wi_reclassified event")
		}
	}

	// Emit wi_content_updated event if content changed
	if req.Content != nil {
		evtID := NewID("evt")
		oldContentHash := ""
		if wi.Content != nil {
			h := sha256.Sum256([]byte(*wi.Content))
			oldContentHash = hex.EncodeToString(h[:8])
		}
		newContentLength := len(*req.Content)
		payload, _ := json.Marshal(map[string]any{
			"old_content_hash":   oldContentHash,
			"new_content_length": newContentLength,
			"changed_by":         callerUserID,
		})
		_, err = tx.Exec(ctx, `
			INSERT INTO agent_events (id, work_item_id, actor_user_id, actor_display, event_type, payload, project)
			VALUES ($1, $2, $3, $4, 'wi_content_updated', $5, $6)`,
			evtID, wi.ID, callerUserID, "", payload, wi.Project,
		)
		if err != nil {
			return nil, dbErr(err, "failed to emit wi_content_updated event")
		}
	}

	if err := tx.Commit(ctx); err != nil {
		if aerr := retryConflictErr(err, "failed to commit update"); aerr != nil { // aihub#334
			return nil, aerr
		}
		return nil, NewErr(ErrInternalError, "failed to commit update")
	}

	// aihub#273: goal/content changed — refresh the wi embedding outside the
	// committed tx (network call), best-effort like the create path.
	if req.Goal != nil || req.Content != nil {
		refreshWorkItemEmbeddingBestEffort(ctx, pool, wi.ID)
	}

	return GetWorkItem(ctx, pool, wi.ID)
}

// cancelGate is CancelWorkItem's pure decision function (aihub#242). State is
// checked BEFORE permission so a state rejection is never reported as a
// permission failure: 409 means "wrong state", 403 means "wrong caller", and
// the two are no longer conflated.
//
// Before this fix, CancelWorkItem computed canCancel with a status check
// hard-wired to (queued|paused) and only checked wi.Status afterward. A
// blocked wi therefore always fell into the "insufficient permissions" 403
// branch — even for its own reporter — even though the real problem was
// state, not permission. Because a blocked wi (once its last dependency is
// removed) has no other exit — pf_claim_work_item also rejects status=blocked
// — that made such wis permanently stuck. Reporters may now cancel a blocked
// wi: that is the missing exit.
func cancelGate(status string, isReporter bool, callerRole, projectRole string) *AihubError {
	switch status {
	case "running":
		return NewErr(ErrConflictWIAlreadyClaimed, "work item is running; force_takeover first, then cancel")
	case "wrapped", "failed", "cancelled":
		return NewErr(ErrConflictTerminalState, fmt.Sprintf("work item is already in terminal state: %s", status))
	}

	// Remaining statuses (queued, paused, blocked): permission-only gate.
	canCancel := callerRole == "admin" || projectRole == "maintainer" || isReporter
	if !canCancel {
		return NewErr(ErrForbidden, "insufficient permissions: only the reporter, a project maintainer, or an admin may cancel this work item")
	}
	return nil
}

// CancelWorkItem sets a work item's status to cancelled if it's not running,
// and releases every resource lock still held on its behalf (aihub#355).
//
// 🔴 The release is not housekeeping — without it the locks are unreachable
// forever. cancelGate admits status='paused', and a paused attempt has
// deliberately RETAINED its git_branch / deploy_env / worktree / tcp_port locks
// so that a resume can go on holding the branch. Cancelling used to move the
// work item and leave run_attempts alone, which left those locks owned by an
// attempt that is still 'paused' — inside the retention predicate the orphan
// sweep honours, so the sweep skips them, while every API path that could
// release them (claim, force_takeover, complete_attempt) refuses a terminal
// work item. See releaseCancelledWILocksSQL for the full argument and for why
// the release is scoped by work item and covers every lock type.
//
// The gate is re-run INSIDE the transaction against a `FOR UPDATE` read of the
// status, which the single-statement version did not need. It matters now: the
// pre-transaction GetWorkItem could report 'queued' while a claim commits
// underneath, and cancelling then would release a LIVE attempt's locks — a
// worse failure than the leak being fixed. Holding the row means the claim path
// either committed before this read (so the gate sees 'running' and rejects) or
// waits behind it (and then fails its own terminal-status check).
//
// # Lock ordering, because this adds a THIRD transaction to that contention set
//
// work_items FOR UPDATE first, then resource_locks — the same order as
// FnClaimWorkItem, FnCompleteAttempt, FnAcquireLocks and UpdateWorkItem (which
// states the identical argument at :2118-2124). Two transactions in that order
// cannot deadlock against each other.
//
// ⚠️ But the set is not unanimous, and saying "same order as its sibling" would
// be true and still miss it. TWO paths take the opposite order:
//
//   - FnForceTakeover deletes the prior attempt's locks (run_attempts.go:1076,
//     :1157) and only then UPDATEs work_items (:1163).
//   - RunOrphanLockSweep (gc.go) deletes lock rows first, and then takes a
//     FOR KEY SHARE on the parent work_items row implicitly — releaseLocks'
//     agent_events insert carries an FK to work_items
//     (0006_events_memories.sql), and FOR KEY SHARE conflicts with FOR UPDATE.
//
// So a 40P01 cycle IS reachable: cancel holds work_items(W) and waits for W's
// lock rows while force_takeover holds those rows and waits for work_items(W).
// This is pre-existing rather than introduced here — FnClaimWorkItem already
// contended with both in exactly this direction — and cancel joins the majority
// order, which is the only choice that does not make it worse. What makes it
// tolerable is not the low probability: it is that every DB error on this path
// goes through dbErr, so Postgres's chosen victim surfaces as a RETRYABLE 409
// (aihub#334) rather than a 500, and the transaction rolls back whole, so no
// lock is half-released. Reordering cancel to match force_takeover instead
// would only move the cycle onto the four paths in the majority.
//
// # The alternative that was NOT taken, and the half of it that later was
//
// Setting the attempt to a terminal run_attempts.status would have let the
// EXISTING orphan sweep collect these locks with no new SQL and no new cause,
// and would also have fixed the residual oddity that a cancelled work item
// keeps an attempt marked 'paused'. It was not taken for two reasons: the sweep
// runs on a 60s tick, so the branch would stay blocked for up to a minute after
// the cancel that was supposed to free it, and none of the six legal
// run_attempts statuses meant "its work item was cancelled" — 'superseded'
// names a successor that does not exist here, and reusing it would make
// supersededByDetails offer a takeover story for a wi nobody took over.
//
// aihub#441 (aihub#411 T2-3 residue (b)) added that status, and this function
// now writes it. Read the split precisely, because only one of the two reasons
// above was answered:
//
//   - The SECOND reason is gone. 'cancelled' exists in the CHECK (migration
//     0035) and is not 'superseded', so supersededByDetails — which keys on
//     that exact literal — still returns nil and still tells no takeover story.
//   - The FIRST reason stands, which is why releaseCancelledWILocksSQL below is
//     NOT replaced by "flip the status and let the sweep have it". The status
//     write and the lock release are both here, in one transaction, so the
//     branch is free the instant the cancel commits rather than up to 60s
//     later. The sweep is now a second line of defence for these rows instead
//     of the only one: a 'cancelled' attempt no longer satisfies
//     orphanLockSweepSQL's IN ('running','paused') retention predicate.
//
// What the status write is FOR is not the locks at all — those were already
// released here since aihub#355. It is the credential path:
// verifyAttemptCredential reads run_attempts.status, and while a cancelled work
// item's attempt read 'paused' that function answered ATTEMPT_PAUSED, whose
// contract is "keep your state file, resume this" (aihub#209). Resume is
// impossible — FnClaimWorkItem refuses a terminal work item — so the one status
// a client could observe was the one that gave it the wrong instruction. It now
// reads 'cancelled' and answers ATTEMPT_MISMATCH: re-claim, and drop the
// credential that can never work again.
//
// STATEMENT ORDER: the run_attempts UPDATE goes BEFORE releaseLocks, matching
// FnCompleteAttempt (which sets status and then releases). That is deliberate
// and not cosmetic — putting it after would make this the only transaction that
// touches resource_locks before run_attempts, adding a lock-ordering edge to
// the deadlock set analysed above for no gain. releaseCancelledWILocksSQL joins
// run_attempts only on work_item_id and never reads status, so the earlier
// write cannot change which rows it deletes.
func CancelWorkItem(ctx context.Context, pool *pgxpool.Pool, idOrSlug, callerUserID, callerRole string, callerProjectRoles map[string]string) *AihubError {
	wi, aihubErr := GetWorkItem(ctx, pool, idOrSlug)
	if aihubErr != nil {
		return aihubErr
	}

	isReporter := wi.ReporterUserID == callerUserID
	projectRole := callerProjectRoles[wi.Project]
	if gateErr := cancelGate(wi.Status, isReporter, callerRole, projectRole); gateErr != nil {
		return gateErr
	}

	tx, err := pool.Begin(ctx)
	if err != nil {
		return NewErr(ErrInternalError, "failed to begin transaction")
	}
	defer tx.Rollback(ctx) //nolint:errcheck

	var status string
	if err := tx.QueryRow(ctx,
		`SELECT status FROM work_items WHERE id=$1 FOR UPDATE`, wi.ID).Scan(&status); err != nil {
		return dbErr(err, "failed to lock work_item")
	}
	if gateErr := cancelGate(status, isReporter, callerRole, projectRole); gateErr != nil {
		return gateErr
	}

	// dbErr, not NewErr(ErrInternalError, …): this statement now runs inside a
	// transaction, so the error it wraps may be SQLSTATE 40001/40P01 — a lost
	// concurrency race the caller fixes by retrying (aihub#334). The
	// single-statement version could not lose one.
	if _, err := tx.Exec(ctx,
		`UPDATE work_items SET status='cancelled' WHERE id=$1`, wi.ID); err != nil {
		return dbErr(err, "failed to cancel work item")
	}

	// aihub#441: give the work item's live attempts the terminal status the
	// cancel actually produced. See the "alternative that was NOT taken" section
	// above for why this is here, why it precedes releaseLocks, and why
	// 'superseded' could not be reused.
	//
	// The predicate is the RETENTION set, IN ('running','paused'), not just
	// 'paused'. cancelGate rejects wi.status='running', so the common case here
	// is a paused attempt — but wi.status and run_attempts.status are separate
	// columns, and 'running' is reachable. cancelGate admits 'blocked', and the
	// ESCALATED step failure puts a work item there without touching its attempt:
	// routes_step.go's `if req.Escalated` sets work_items.status='blocked' on a
	// wi whose attempt this very handler just credential-checked as 'running'.
	// Cancelling that wi must end that attempt.
	//
	// Verified rather than assumed, because the obvious candidate is the wrong
	// one: the dependency path (dependencies.go, CreateDependency) derives
	// 'blocked' under `AND status='queued'`, so it can never produce this case,
	// and CreateWorkItem's blocked_by branch runs before any attempt exists.
	// routes_step.go is the only writer of the three that can.
	//
	// Matching the retention set covers it without inventing a transition: every
	// attempt that could still be holding something is ended, and every attempt
	// that already ended (wrapped/failed/superseded) keeps the status it earned
	// rather than having its history rewritten by a later cancel.
	if _, err := tx.Exec(ctx,
		`UPDATE run_attempts SET status='cancelled', ended_at=clock_timestamp()
		 WHERE work_item_id=$1 AND status IN ('running','paused')`, wi.ID); err != nil {
		return dbErr(err, "failed to cancel work item's run attempts")
	}

	// aihub#343: through releaseLocks, so each lock the cancel drops carries a
	// lock_released event with cause=wi_cancelled. Without the event, "the
	// cancel released it" and "the cancel never touched it" are the same empty
	// stream — the exact indistinguishability aihub#355 was filed about.
	if _, relErr := releaseLocks(ctx, tx, releaseCancelledWILocksSQL,
		newLockOp(lockCauseWICancelled, lockEventActor{UserID: callerUserID}).withExtra(map[string]any{
			"prior_wi_status": status,
		}), wi.ID,
	); relErr != nil {
		return dbErr(relErr, "failed to release resource locks on cancel")
	}

	if err := tx.Commit(ctx); err != nil {
		return dbErr(err, "failed to commit work item cancel")
	}
	return nil
}

// buildReadyQueueItemsQuery assembles the SQL for the ready queue's items[]
// segment: $1 = project, $2 = max.
//
// A function rather than an inline literal so the sharing is at least visible in
// one place. Note precisely what that does and does not buy, because an earlier
// version of this comment overclaimed and was wrong:
//
// Inspecting this function's return value proves nothing about GetReadyQueue.
// Nothing forces GetReadyQueue to call it — an unused function is legal Go — so
// a divergent query inlined at the call site leaves a helper-inspecting test
// green. That was verified, not assumed: replacing the call site with an inline
// query that dropped both requires_human_session and the blocker NOT EXISTS left
// every aihub#280 test passing.
//
// The real guard is therefore behavioural and lives in
// TestGetReadyQueue_ItemsExcludesHumanSessionAndBlocked, which calls
// GetReadyQueue against a live DB and asserts what it actually returns. This
// helper's own test only pins the SQL's shape.
func buildReadyQueueItemsQuery() string {
	return `
		SELECT wi.id, wi.slug, wi.wi_type, wi.priority, wi.goal
		FROM work_items wi
		WHERE wi.project = $1
		  AND ` + readyOnlyPredicate + `
		ORDER BY
		  CASE wi.priority WHEN 'urgent' THEN 4 WHEN 'high' THEN 3
		                   WHEN 'normal' THEN 2 WHEN 'low' THEN 1 END DESC,
		  wi.created_at ASC
		LIMIT $2`
}

// readyQueueDefaultMax is the page size for a caller who named none, and
// readyQueueCeilingMax the largest this endpoint will serve. Named because
// newReadyQueue has to report them and a report of a bare literal is unreadable.
const (
	readyQueueDefaultMax = 10
	readyQueueCeilingMax = 200
)

// newReadyQueue builds the empty seven-segment response, bounds the caller's
// page size, and DISCLOSES the bound if it fired — all three in one place, so a
// response cannot exist that was bounded without saying so.
//
// That coupling is the fix, not a tidy-up. Until aihub#432 the clamp was three
// lines inside GetReadyQueue with a comment admitting it obeyed half of
// queryparam.go's Rule 2: it clamped to the CEILING rather than back to the
// default, which is right and is what aihub#267 made ListWorkItems do, but it
// told nobody, so `max=5000` and `max=200` returned byte-identical responses.
// The stated reason was that ReadyQueue had no `request_adjusted` field and a
// half-done version would be no better — true when it was written, and the
// field costs one struct member now that aihub#314 has made the shape generic.
//
// Returning the bounded value rather than mutating the caller's is what stops
// the two halves drifting: the SQL pages with exactly the number this function
// disclosed, because there is only one number.
func newReadyQueue(requestedMax int) (*ReadyQueue, int) {
	applied := requestedMax
	if applied <= 0 {
		// Neither malformed nor over a limit: "the caller named no page size",
		// which takes the endpoint default (the aihub#249 contract). A value
		// that ARRIVED non-positive is still an adjustment and is disclosed
		// below; a zero is not, because zero and absent are the same int here.
		applied = readyQueueDefaultMax
	}
	if applied > readyQueueCeilingMax {
		applied = readyQueueCeilingMax
	}
	return &ReadyQueue{
		Items:             []ReadyItem{},
		Running:           []RunningItem{},
		Stalled:           []StalledItem{},
		Paused:            []PausedItem{},
		NeedsHumanSession: []ReadyItem{},
		Unclassified:      []ReadyItem{},
		// The seventh segment is initialised like the other six as of aihub#449.
		// It used to be left nil and marked `omitempty`; see the field's own
		// comment on ReadyQueue for why an absent stale_running was the one
		// absence in this response that asserted something.
		StaleRunning:    []RunningItem{},
		RequestAdjusted: appendIntAdjustment(nil, "max", requestedMax, applied),
	}, applied
}

// GetReadyQueue returns the seven-segment LCRS view for a project.
//
// `max` pages THREE of those seven — items[], needs_human_session[] and
// unclassified[] each take it as their own LIMIT, so it is a per-section page
// size and not a budget over the response. running[], stalled[], paused[] and
// stale_running[] take no limit at all. The published description said "Max
// items in ready section" until aihub#449; that was measured wrong in aihub#401
// and is corrected in the schema rather than here, because the caller reads the
// schema.
//
// # Every segment fails the whole call (aihub#500)
//
// All seven `pool.Query` errors propagate. Until aihub#500 FIVE of them did not:
// stalled, paused, needs_human_session, unclassified and stale_running each
// wrapped their whole drain in `if err == nil { … }` with no else, so a send-time
// failure rendered that segment as an empty list and still returned 200. Only
// items[] and running[] checked `err != nil`.
//
// 🔴 aihub#500 opened naming stale_running the sole offender, "while the other six
// return dbErr". That was measured wrong — the shape was five sites, and
// stale_running only looked unique because it spelled its variable `staleErr`
// instead of shadowing `err`. Fixing the one named site would have left four
// identical defects behind while publishing "now aligned with the other six" as
// the reason, so the class is closed here rather than the instance.
//
// What decides it is not a majority vote among the segments, because there was no
// majority to appeal to. It is that each of those five already disagreed WITH
// ITSELF: the same segment that discarded the send-time error returns dbErrCause
// on rows.Err() a dozen lines below, for the same underlying condition (aihub#382
// / aihub#386 put it there). A segment cannot be best-effort on one error path
// and fatal on the other for one failure — one of the two is unfinished, and the
// fatal half is the one with a written reason. Making best-effort real would have
// meant DOWNGRADING the rows.Err() half, a larger change in the exact direction
// internal/citest/rowserr exists to prevent.
//
// Nothing recorded best-effort as intended: not the design-doc changelog, not the
// contract card, not conflictGuardExemptions — and that guard could not have
// caught this anyway, since it is scoped to transactional functions and this one
// opens no transaction. The shape arrived unremarked as the tail of an additive
// change, exactly as stale_running's `omitempty` did in aihub#36.
//
// The wire cost is narrower than it first looks. A pool that is down already fails
// at items[], the first query, so the reachable case is degradation BETWEEN
// segments — and there a partial queue is the harmful answer, because aihub#449
// made all seven keys always-present precisely so that an empty one asserts
// "nothing is here" rather than "no data reached you".
//
// # A row that cannot be scanned fails the whole call, naming its segment (aihub#608)
//
// The same contract, one row further in: every segment's per-row Scan error used
// to `continue`. What that spelled was "skip this row and publish the rest as
// the complete answer" — aihub#500's silent-empty defect at row granularity, and
// it had already fired (aihub#206: stalled rows with a NULL actor_display
// vanished from stalled[] back when nothing checked rows.Err()). What it DID on
// pgx v5 is narrower, measured during aihub#608 rather than assumed: pgx's
// Rows.Scan calls rows.fatal() on every error (rows.go, v5.9.2), so the failed
// Scan closes the rows, Next() answers false, and the segment's rows.Err() arm
// fails the call — with the READ arm's message, misattributing the site. So the
// `continue` was a lie about intent kept honest by an undocumented driver side
// effect, one arm downstream, under somebody else's error text. The Scan arms
// now return directly: the failure names its own site, and the contract stops
// depending on pgx's poisoning behaviour. Legitimate NULLs are handled by
// nullable scan targets (WIType, PauseReason, the aihub#206 locals), so a Scan
// error here means column drift or a malformed row.
// internal/citest/rowserr's swallow gate holds the shape for every drain in the
// repo — including the loops whose rows.Err() arm does NOT return (BearerAuth's
// membership drain was one), where the same `continue` really did publish
// partial results as complete.
//
// # The send-time guards classify class 40 too (aihub#548)
//
// aihub#500 gave all seven segments the same SHAPE — every Query error is
// answered — but wrote the five new guards as bare NewErr(ErrInternalError, …)
// while the rows.Err() branch a dozen lines below each of them returns
// dbErrCause. That reopened #500's own argument one axis over: the same segment
// classified a class-40 rollback as a retryable 409 on one error path and as a
// 500 on the other, for the same underlying condition. So every send-time guard
// now goes through dbErr, the documented drop-in whose non-conflict outcome is
// byte-identical to the bare NewErr it replaces (see pgx_err.go) — the caller's
// message does not move, only SQLSTATE 40001/40P01 stops being reported as "the
// server is broken". The classifier rule of TestReadyQueueAnswersEveryQueryError
// holds this for all seven and for any segment added later.
func GetReadyQueue(ctx context.Context, pool *pgxpool.Pool, project string, max int) (*ReadyQueue, *AihubError) {
	result, max := newReadyQueue(max)

	// items[]: queued + no blocker + requires_human_session=false.
	itemRows, err := pool.Query(ctx, buildReadyQueueItemsQuery(), project, max)
	if err != nil {
		return nil, dbErr(err, "failed to query ready items")
	}
	defer itemRows.Close()
	for itemRows.Next() {
		var item ReadyItem
		if err := itemRows.Scan(&item.ID, &item.Slug, &item.WIType, &item.Priority, &item.Goal); err != nil {
			return nil, dbErrCause(err, "failed to scan ready item row")
		}
		result.Items = append(result.Items, item)
	}
	// pgx defers execute-time errors to Err() (aihub#382, aihub#386).
	if err := itemRows.Err(); err != nil {
		return nil, dbErrCause(err, "failed to read ready items rows")
	}
	itemRows.Close()

	// running[]: status=running
	runRows, err := pool.Query(ctx, `
		SELECT wi.id, wi.slug, wi.goal, ra.actor_display, ra.last_active_at
		FROM work_items wi
		JOIN run_attempts ra ON ra.id = wi.current_attempt_id
		WHERE wi.project = $1 AND wi.status = 'running'
		ORDER BY ra.last_active_at DESC`,
		project,
	)
	if err != nil {
		return nil, dbErr(err, "failed to query running items")
	}
	defer runRows.Close()
	for runRows.Next() {
		var item RunningItem
		var lat time.Time
		if err := runRows.Scan(&item.ID, &item.Slug, &item.Goal, &item.OwnerDisplay, &lat); err != nil {
			return nil, dbErrCause(err, "failed to scan running item row")
		}
		item.LastActiveAt = lat.Format(time.RFC3339)
		result.Running = append(result.Running, item)
	}
	// pgx defers execute-time errors to Err() (aihub#382, aihub#386).
	if err := runRows.Err(); err != nil {
		return nil, dbErrCause(err, "failed to read running items rows")
	}
	runRows.Close()

	// stalled[]: status=blocked AND has wi_stalled event
	stalledRows, err := pool.Query(ctx, `
		SELECT wi.id, wi.slug,
		       ae.payload->>'stall_reason' as stall_reason,
		       ae.created_at,
		       ae.actor_display
		FROM work_items wi
		JOIN LATERAL (
		  SELECT ae2.payload, ae2.created_at, ae2.actor_display
		  FROM agent_events ae2
		  WHERE ae2.work_item_id = wi.id AND ae2.event_type = 'wi_stalled'
		  ORDER BY ae2.created_at DESC LIMIT 1
		) ae ON true
		WHERE wi.project = $1 AND wi.status = 'blocked'
		ORDER BY ae.created_at DESC`,
		project,
	)
	if err != nil {
		return nil, dbErr(err, "failed to query stalled items")
	}
	defer stalledRows.Close()
	for stalledRows.Next() {
		var item StalledItem
		var stalledAt time.Time
		var stall, actorDisplay *string
		// aihub#206: actor_display on the wi_stalled event can be NULL
		// (e.g. escalated-stall events emitted without a display name
		// set), which can't scan into item.LastActorDisplay's plain
		// string directly — scan through a nullable local instead.
		if err := stalledRows.Scan(&item.ID, &item.Slug, &stall, &stalledAt, &actorDisplay); err != nil {
			return nil, dbErrCause(err, "failed to scan stalled item row")
		}
		if stall != nil {
			item.StallReason = *stall
		}
		if actorDisplay != nil {
			item.LastActorDisplay = *actorDisplay
		}
		item.StalledSince = stalledAt.Format(time.RFC3339)
		result.Stalled = append(result.Stalled, item)
	}
	// pgx defers execute-time errors to Err() (aihub#382, aihub#386).
	if err := stalledRows.Err(); err != nil {
		return nil, dbErrCause(err, "failed to read stalled items rows")
	}
	stalledRows.Close()

	// paused[]: status=paused
	pausedRows, err := pool.Query(ctx, `
		SELECT wi.id, wi.slug, ra.last_active_at, ra.actor_display, ra.pause_reason
		FROM work_items wi
		LEFT JOIN run_attempts ra ON ra.id = wi.current_attempt_id
		WHERE wi.project = $1 AND wi.status = 'paused'
		ORDER BY wi.updated_at DESC`,
		project,
	)
	if err != nil {
		return nil, dbErr(err, "failed to query paused items")
	}
	defer pausedRows.Close()
	for pausedRows.Next() {
		var item PausedItem
		var lat *time.Time
		var actorDisplay *string
		if err := pausedRows.Scan(&item.ID, &item.Slug, &lat, &actorDisplay, &item.PauseReason); err != nil {
			return nil, dbErrCause(err, "failed to scan paused item row")
		}
		if lat != nil {
			item.PausedSince = lat.Format(time.RFC3339)
		}
		if actorDisplay != nil {
			item.LastActorDisplay = *actorDisplay
		}
		result.Paused = append(result.Paused, item)
	}
	// pgx defers execute-time errors to Err() (aihub#382, aihub#386).
	if err := pausedRows.Err(); err != nil {
		return nil, dbErrCause(err, "failed to read paused items rows")
	}
	pausedRows.Close()

	// needs_human_session[]: queued + no blocker + requires_human_session=true
	humanRows, err := pool.Query(ctx, `
		SELECT wi.id, wi.slug, wi.wi_type, wi.priority, wi.goal, wi.created_at
		FROM work_items wi
		WHERE wi.project = $1
		  AND wi.status = 'queued'
		  AND wi.requires_human_session = true
		  AND `+noLiveBlockerPredicate+`
		ORDER BY
		  CASE wi.priority WHEN 'urgent' THEN 4 WHEN 'high' THEN 3
		                   WHEN 'normal' THEN 2 WHEN 'low' THEN 1 END DESC,
		  wi.created_at ASC
		LIMIT $2`,
		project, max,
	)
	if err != nil {
		return nil, dbErr(err, "failed to query needs_human_session items")
	}
	defer humanRows.Close()
	for humanRows.Next() {
		var item ReadyItem
		var cat time.Time
		if err := humanRows.Scan(&item.ID, &item.Slug, &item.WIType, &item.Priority, &item.Goal, &cat); err != nil {
			return nil, dbErrCause(err, "failed to scan needs_human_session item row")
		}
		catStr := cat.Format(time.RFC3339)
		item.CreatedAt = catStr
		result.NeedsHumanSession = append(result.NeedsHumanSession, item)
	}
	// pgx defers execute-time errors to Err() (aihub#382, aihub#386).
	if err := humanRows.Err(); err != nil {
		return nil, dbErrCause(err, "failed to read needs_human_session items rows")
	}
	humanRows.Close()

	// unclassified[]: queued + no blocker + requires_human_session IS NULL.
	//
	// The third state gets a segment of its own rather than being folded into
	// either of the other two, because NULL means nobody has classified this wi —
	// it does not mean false, and items[] must not hand it to an agent.
	//
	// Note how the segment DRAINS, because it is not only by classification: a wi
	// also leaves it by being CLAIMED, since FnClaimWorkItem resolves a NULL to
	// true and writes it back. So an entry here is waiting for whichever of the
	// two comes first, and a wi that has ever been claimed cannot be in it.
	unclRows, err := pool.Query(ctx, `
		SELECT wi.id, wi.slug, wi.wi_type, wi.priority, wi.goal, wi.created_at
		FROM work_items wi
		WHERE wi.project = $1
		  AND wi.status = 'queued'
		  AND wi.requires_human_session IS NULL
		  AND `+noLiveBlockerPredicate+`
		ORDER BY wi.created_at ASC
		LIMIT $2`,
		project, max,
	)
	if err != nil {
		return nil, dbErr(err, "failed to query unclassified items")
	}
	defer unclRows.Close()
	for unclRows.Next() {
		var item ReadyItem
		var cat time.Time
		if err := unclRows.Scan(&item.ID, &item.Slug, &item.WIType, &item.Priority, &item.Goal, &cat); err != nil {
			return nil, dbErrCause(err, "failed to scan unclassified item row")
		}
		catStr := cat.Format(time.RFC3339)
		item.CreatedAt = catStr
		result.Unclassified = append(result.Unclassified, item)
	}
	// pgx defers execute-time errors to Err() (aihub#382, aihub#386).
	if err := unclRows.Err(); err != nil {
		return nil, dbErrCause(err, "failed to read unclassified items rows")
	}
	unclRows.Close()

	// stale_running[]: running wi with updated_at > 24h (ownership reminder, not forced)
	staleRows, err := pool.Query(ctx, `
		SELECT wi.id, wi.slug, wi.goal, ra.actor_display, ra.last_active_at
		FROM work_items wi
		JOIN run_attempts ra ON ra.id = wi.current_attempt_id
		WHERE wi.project = $1
		  AND wi.status = 'running'
		  AND wi.updated_at < now() - interval '24 hours'
		ORDER BY wi.updated_at ASC`,
		project,
	)
	if err != nil {
		return nil, dbErr(err, "failed to query stale_running items")
	}
	defer staleRows.Close()
	for staleRows.Next() {
		var item RunningItem
		var lat time.Time
		if err := staleRows.Scan(&item.ID, &item.Slug, &item.Goal, &item.OwnerDisplay, &lat); err != nil {
			return nil, dbErrCause(err, "failed to scan stale_running item row")
		}
		item.LastActiveAt = lat.Format(time.RFC3339)
		result.StaleRunning = append(result.StaleRunning, item)
	}
	// pgx defers execute-time errors to Err() (aihub#382, aihub#386).
	if err := staleRows.Err(); err != nil {
		return nil, dbErrCause(err, "failed to read stale_running items rows")
	}
	staleRows.Close()

	return result, nil
}
