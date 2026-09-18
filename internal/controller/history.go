package controller

// history.go — the invocation/result history the DB workflow path runs the
// pure policy over (aihub#708 Batch 2B).
//
// workflow.Decide is all-or-nothing: it needs every expected invocation
// paired with its recorded result, the human approvals, and the repair
// authorizations, in event order. Nothing the controller does in one run is
// enough on a resumed flow — results recorded by earlier attempts persist in
// the CURRENT generation (wi_workflow_results is keyed by work item +
// generation, not by attempt), so the driver that takes over mid-flow inherits
// a history it never saw.
//
// The events API is the one existing surface that carries that history:
//
//	workflow_invocation_started  steps_version, step_id, step_attempt_id,
//	                            producer_id, run_attempt_id, claim_epoch,
//	                            repair_episode_id
//	step_completed / step_failed  steps_version, step_id, step_attempt_id,
//	                            status, review_verdict, artifact
//	workflow_approval            steps_version, step_id, artifact, decision
//	workflow_repair_authorized   repair_id, kind, failed_step_id,
//	                            failed_step_attempt_id, reason
//
// # The one thing it does not carry, and what that costs
//
// The result events do not carry the recorded EVIDENCE of gate results. The
// server validated that evidence when the result was recorded
// (validateWorkflowResultShape applies the same shape rules the pure policy
// does), so a result that exists server-side necessarily had valid evidence —
// but a history rebuilt from events cannot re-present it to workflow.Decide,
// whose validateResult refuses a completed review/verification gate with no
// evidence. No endpoint this batch may touch exposes wi_workflow_results
// rows directly, so the gap is structural.
//
// The History therefore tracks REPRESENTABILITY rather than pretending: when
// a history cannot be faithfully expressed as a workflow.PolicyInput (missing
// reconstructed evidence; more than one retry authorization; a retry inside
// an episode lineage, which the pure PolicyInput vocabulary cannot name; an
// episode whose bounded role set is not three completed results), PolicyInput
// reports ok=false and the driver falls back to the SERVER's own fences —
// every start was ordering/approval/repair-fenced under the work item lock,
// every result was shape-validated at record time, and every episode close
// ran the pure episode predicate with the true data. That fallback is always
// disclosed, never silent: it lands in the wrap note and the run log.

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"

	"github.com/GMISWE/ieops-aihub/internal/workflow"
)

// EventsReader is the events half of the client API. *client.Client satisfies
// it; tests can fake it.
type EventsReader interface {
	ReadEvents(ctx context.Context, params url.Values) (map[string]any, error)
}

// eventRow is the slice of domain.EventRow the reconstruction needs.
type eventRow struct {
	ActorUserID *string         `json:"actor_user_id"`
	EventType   string          `json:"event_type"`
	Payload     json.RawMessage `json:"payload"`
}

type invocationEvent struct {
	StepsVersion    int    `json:"steps_version"`
	StepID          string `json:"step_id"`
	StepAttemptID   string `json:"step_attempt_id"`
	ProducerID      string `json:"producer_id"`
	RunAttemptID    string `json:"run_attempt_id"`
	ClaimEpoch      int64  `json:"claim_epoch"`
	RepairEpisodeID string `json:"repair_episode_id"`
}

type resultEvent struct {
	Step          string               `json:"step"`
	StepsVersion  int                  `json:"steps_version"`
	StepAttemptID string               `json:"step_attempt_id"`
	Status        string               `json:"status"`
	ReviewVerdict string               `json:"review_verdict"`
	Artifact      workflow.ArtifactRef `json:"artifact"`
}

type approvalEvent struct {
	StepsVersion int                  `json:"steps_version"`
	StepID       string               `json:"step_id"`
	Artifact     workflow.ArtifactRef `json:"artifact"`
	Decision     string               `json:"decision"`
}

type repairEvent struct {
	RepairID          string `json:"repair_id"`
	Kind              string `json:"kind"`
	FailedStepID      string `json:"failed_step_id"`
	FailedStepAttempt string `json:"failed_step_attempt_id"`
	StepsVersion      int    `json:"steps_version"`
	Reason            string `json:"reason"`
	ParentEpisodeID   string `json:"parent_episode_id"`
}

// workflowEventTypes is the closed type filter the reconstruction reads. The
// step_completed/step_failed types are shared with the legacy scenario path;
// the payload's steps_version field discriminates (legacy step events carry
// none, so they decode to 0 and never match a generation).
var workflowEventTypes = []string{
	"workflow_invocation_started",
	"step_completed",
	"step_failed",
	"workflow_approval",
	"workflow_repair_authorized",
}

// History is the ordered invocation/result history of one generation, plus
// everything the pure policy needs around it.
type History struct {
	Expected  []workflow.ExpectedInvocation
	Results   []workflow.StepResult
	Approvals []workflow.HumanApproval

	// inRun marks result indexes this run recorded itself (full fidelity,
	// evidence included).
	inRun map[int]bool

	// repairAuths are the authorization ids whose bound invocations recorded
	// results, in event order, with their expected invocations.
	repairAuths []repairBinding

	// unrepresentableReason is non-empty when the history cannot be expressed
	// as a workflow.PolicyInput (see the file comment).
	unrepresentableReason string
}

type repairBinding struct {
	ID          string
	Kind        string
	FailedSA    string
	Reason      string
	Bound       []workflow.ExpectedInvocation
	RecordedIdx []int
}

func newHistory() *History {
	return &History{inRun: map[int]bool{}}
}

// Record appends one result THIS run recorded, with full fidelity.
func (h *History) Record(expected workflow.ExpectedInvocation, result workflow.StepResult) {
	h.Expected = append(h.Expected, expected)
	h.Results = append(h.Results, result)
	h.inRun[len(h.Results)-1] = true
}

// NoteRepair records that this run opened and drove a repair authorization,
// so PolicyInput can express it.
func (h *History) NoteRepair(id, kind, failedStepAttemptID, reason string, bound workflow.ExpectedInvocation, resultIdx int) {
	h.repairAuths = append(h.repairAuths, repairBinding{
		ID: id, Kind: kind, FailedSA: failedStepAttemptID, Reason: reason,
		Bound: []workflow.ExpectedInvocation{bound}, RecordedIdx: []int{resultIdx},
	})
}

// LoadHistory reconstructs the recorded history of the current generation
// from the events API. A read failure is an error, never an empty history:
// an unreadable history and an absent one are different facts.
func LoadHistory(ctx context.Context, api EventsReader, wiID string, stepsVersion int) (*History, error) {
	h := newHistory()
	rows, err := readWorkflowEvents(ctx, api, wiID)
	if err != nil {
		return nil, err
	}

	// Pass 1: invocations of this generation, keyed by step attempt.
	invocations := map[string]invocationEvent{}
	for _, row := range rows {
		if row.EventType != "workflow_invocation_started" {
			continue
		}
		var ev invocationEvent
		if err := json.Unmarshal(row.Payload, &ev); err != nil {
			return nil, fmt.Errorf("reconstruct workflow history: unreadable invocation event: %w", err)
		}
		if ev.StepsVersion != stepsVersion || ev.StepAttemptID == "" {
			continue
		}
		invocations[ev.StepAttemptID] = ev
	}

	// Pass 2: results, joined to their invocations in event order.
	for _, row := range rows {
		if row.EventType != "step_completed" && row.EventType != "step_failed" {
			continue
		}
		var ev resultEvent
		if err := json.Unmarshal(row.Payload, &ev); err != nil {
			return nil, fmt.Errorf("reconstruct workflow history: unreadable result event: %w", err)
		}
		if ev.StepsVersion != stepsVersion || ev.StepAttemptID == "" {
			continue
		}
		inv, ok := invocations[ev.StepAttemptID]
		if !ok {
			// A recorded result whose minting event is missing cannot be
			// verified — mark the history unrepresentable rather than
			// guessing its producer and epoch.
			h.unrepresentable("result %s has no invocation event to join", ev.StepAttemptID)
			continue
		}
		h.Expected = append(h.Expected, workflow.ExpectedInvocation{
			WorkItemID:    wiID,
			FlowVersion:   ev.StepsVersion,
			StepID:        inv.StepID,
			StepAttemptID: inv.StepAttemptID,
			Epoch:         int(inv.ClaimEpoch),
			ProducerID:    inv.ProducerID,
		})
		// Evidence is deliberately absent: the events API does not expose the
		// recorded evidence, and fabricating a placeholder would be the fake
		// this whole path refuses to produce. The gap is accounted for in
		// PolicyInput, never papered over.
		h.Results = append(h.Results, workflow.StepResult{
			Status:        workflow.ResultStatus(ev.Status),
			ReviewVerdict: workflow.ReviewVerdict(ev.ReviewVerdict),
			WorkItemID:    wiID,
			FlowVersion:   ev.StepsVersion,
			StepID:        inv.StepID,
			StepAttemptID: ev.StepAttemptID,
			Epoch:         int(inv.ClaimEpoch),
			ProducerID:    inv.ProducerID,
			Artifact:      ev.Artifact,
		})
	}

	// Pass 3: approvals. Only granted decisions satisfy the policy's
	// hasApproval; a rejection is simply not an approval.
	for _, row := range rows {
		if row.EventType != "workflow_approval" {
			continue
		}
		var ev approvalEvent
		if err := json.Unmarshal(row.Payload, &ev); err != nil {
			return nil, fmt.Errorf("reconstruct workflow history: unreadable approval event: %w", err)
		}
		if ev.StepsVersion != stepsVersion || ev.Decision != string(workflow.ApprovalGranted) {
			continue
		}
		actor := ""
		if row.ActorUserID != nil {
			actor = *row.ActorUserID
		}
		h.Approvals = append(h.Approvals, workflow.HumanApproval{
			WorkItemID:  wiID,
			FlowVersion: ev.StepsVersion,
			StepID:      ev.StepID,
			Artifact:    ev.Artifact,
			ActorID:     actor,
			Decision:    workflow.ApprovalGranted,
		})
	}

	// Pass 4: repair authorizations, with the results their bound invocations
	// recorded, in event order. The pure policy binds episode roles by event
	// order, so the reconstruction must not source that order from map
	// iteration — the bound-invocation list is built from the chronological
	// event scan.
	type repairMeta struct{ kind, failedSA, reason string }
	repairs := map[string]repairMeta{}
	for _, row := range rows {
		if row.EventType != "workflow_repair_authorized" {
			continue
		}
		var ev repairEvent
		if err := json.Unmarshal(row.Payload, &ev); err != nil {
			return nil, fmt.Errorf("reconstruct workflow history: unreadable repair event: %w", err)
		}
		if ev.StepsVersion != stepsVersion || ev.RepairID == "" {
			continue
		}
		repairs[ev.RepairID] = repairMeta{kind: ev.Kind, failedSA: ev.FailedStepAttempt, reason: ev.Reason}
	}
	boundOrder := map[string][]string{} // repair id -> step attempt ids, event order
	for _, row := range rows {
		if row.EventType != "workflow_invocation_started" {
			continue
		}
		var ev invocationEvent
		if err := json.Unmarshal(row.Payload, &ev); err != nil || ev.StepsVersion != stepsVersion || ev.RepairEpisodeID == "" {
			continue
		}
		boundOrder[ev.RepairEpisodeID] = append(boundOrder[ev.RepairEpisodeID], ev.StepAttemptID)
	}
	resultIdxBySA := map[string]int{}
	for i, r := range h.Results {
		resultIdxBySA[r.StepAttemptID] = i
	}
	for repairID, sas := range boundOrder {
		meta, known := repairs[repairID]
		if !known {
			h.unrepresentable("repair authorization %s has no authorization event to read its kind from", repairID)
			continue
		}
		binding := repairBinding{ID: repairID, Kind: meta.kind, FailedSA: meta.failedSA, Reason: meta.reason}
		for _, sa := range sas {
			idx, recorded := resultIdxBySA[sa]
			if !recorded {
				continue
			}
			inv := invocations[sa]
			binding.Bound = append(binding.Bound, workflow.ExpectedInvocation{
				WorkItemID:    wiID,
				FlowVersion:   stepsVersion,
				StepID:        inv.StepID,
				StepAttemptID: sa,
				Epoch:         int(inv.ClaimEpoch),
				ProducerID:    inv.ProducerID,
			})
			binding.RecordedIdx = append(binding.RecordedIdx, idx)
		}
		if len(binding.Bound) == 0 {
			// An authorization whose bound invocations never recorded carries
			// nothing the policy consumes; the loop's hold handling drives it.
			continue
		}
		h.repairAuths = append(h.repairAuths, binding)
	}
	return h, nil
}

func (h *History) unrepresentable(format string, args ...any) {
	if h.unrepresentableReason == "" {
		h.unrepresentableReason = fmt.Sprintf(format, args...)
	}
}

// readWorkflowEvents pages through the work item's workflow events in
// chronological order.
func readWorkflowEvents(ctx context.Context, api EventsReader, wiID string) ([]eventRow, error) {
	var collected []eventRow
	cursor := ""
	pages := 0
	for {
		p := url.Values{}
		p.Set("work_item_id", wiID)
		p.Set("types", joinCSV(workflowEventTypes))
		p.Set("limit", "500")
		if cursor != "" {
			p.Set("cursor", cursor)
		}
		res, err := api.ReadEvents(ctx, p)
		if err != nil {
			return nil, fmt.Errorf("read workflow events: %w", err)
		}
		raw, _ := res["events"].([]any)
		for _, it := range raw {
			m, ok := it.(map[string]any)
			if !ok {
				continue
			}
			b, err := json.Marshal(m)
			if err != nil {
				return nil, err
			}
			var row eventRow
			if err := json.Unmarshal(b, &row); err != nil {
				return nil, fmt.Errorf("read workflow events: unreadable event row: %w", err)
			}
			collected = append(collected, row)
		}
		next, _ := res["next_cursor"].(string)
		if next == "" || len(raw) == 0 {
			break
		}
		cursor = next
		pages++
		if pages > 100 {
			return nil, fmt.Errorf("read workflow events: exceeded 100 pages for %s; refusing an unbounded read", wiID)
		}
	}
	// The API returns newest-first; the history is oldest-first.
	for i, j := 0, len(collected)-1; i < j; i, j = i+1, j-1 {
		collected[i], collected[j] = collected[j], collected[i]
	}
	return collected, nil
}

func joinCSV(parts []string) string {
	out := ""
	for i, p := range parts {
		if i > 0 {
			out += ","
		}
		out += p
	}
	return out
}

// PolicyInput assembles the pure policy's input from the history. ok is false
// when the history cannot be faithfully represented — the caller must then
// rely on the server's own fences and say so, never on a silently degraded
// policy run.
func (h *History) PolicyInput(flow *workflow.ValidatedFlow, requiresHumanSession bool) (workflow.PolicyInput, bool) {
	in := workflow.PolicyInput{
		RequiresHumanSession: requiresHumanSession,
		Expected:             append([]workflow.ExpectedInvocation(nil), h.Expected...),
		Approvals:            append([]workflow.HumanApproval(nil), h.Approvals...),
	}

	// The evidence gap: a reconstructed (not in-run) completed gate result
	// carries no evidence because the events API does not expose it.
	if h.unrepresentableReason != "" {
		return in, false
	}
	for i, r := range h.Results {
		if h.inRun[i] {
			continue
		}
		if isCompletedGateWithoutEvidence(flow, r) {
			return in, false
		}
	}

	// Repair authorizations. The pure PolicyInput vocabulary names ONE
	// outstanding retry and any number of COMPLETE episodes; anything richer
	// (two retries; a retry whose lineage descends from an episode; an
	// episode whose bounded set is not three recorded results) is not
	// expressible, and pretending otherwise would feed the policy a lie.
	var retries, episodes []repairBinding
	for _, b := range h.repairAuths {
		switch b.Kind {
		case "retry":
			if len(b.Bound) != 1 {
				return in, false
			}
			retries = append(retries, b)
		case "episode":
			if len(b.Bound) != 3 {
				return in, false
			}
			episodes = append(episodes, b)
		default:
			return in, false
		}
	}
	if len(retries) > 1 {
		return in, false
	}
	if len(episodes) > 0 && len(retries) > 0 {
		// A retry replacement is not episode-contained in the pure model, so
		// an episode-mode Decide would refuse it as an unbound retry.
		return in, false
	}
	for _, b := range retries {
		in.Repair = &workflow.RepairAuthorization{
			FailedStepAttemptID: b.FailedSA,
			Retry:               b.Bound[0],
			Authorized:          true,
			Reason:              b.Reason,
		}
	}
	for _, b := range episodes {
		in.RepairEpisodes = append(in.RepairEpisodes, workflow.RepairEpisode{
			FailedReviewStepAttemptID: b.FailedSA,
			RepairedProducer:          b.Bound[0],
			Verification:              b.Bound[1],
			Review:                    b.Bound[2],
			Authorized:                true,
			Reason:                    b.Reason,
		})
	}
	return in, true
}

func isCompletedGateWithoutEvidence(flow *workflow.ValidatedFlow, r workflow.StepResult) bool {
	if r.Status != workflow.StatusCompleted || len(r.Evidence) != 0 {
		return false
	}
	for _, s := range flow.BoundSteps() {
		if s.Step.ID != r.StepID {
			continue
		}
		for _, c := range s.Contract.Capabilities {
			if c == "review" || c == "verification" {
				return true
			}
		}
		return false
	}
	return false
}
