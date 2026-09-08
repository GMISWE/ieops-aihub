package mcp

// aihub#444 (aihub#411 decision table §6.2 T2-5 and T2-18), the hop-1 half.
//
// Three things have to be true on the wire and none implies another:
//
//	the event vocabulary is PUBLISHED on pf_emit_event.event_type, and not
//	  under the `enum` key, which nothing enforces;
//	pf_read_events.types stops calling itself a whitelist, because it filters
//	  and does not validate;
//	pf_read_events.user_id says WHICH identity it filters.
//
//	go test ./internal/mcp/ -run TestEmitEventType -v
//	go test ./internal/mcp/ -run TestReadEvents -v

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/GMISWE/ieops-aihub/internal/domain"
)

// eventToolProp decodes one property out of one of the two published schemas, so
// a MISSING key can be told apart from an empty one — the distinction the `enum`
// arm below turns on.
func eventToolProp(t *testing.T, schema json.RawMessage, tool, name string) map[string]any {
	t.Helper()
	var decoded struct {
		Properties map[string]map[string]any `json:"properties"`
	}
	if err := json.Unmarshal(schema, &decoded); err != nil {
		t.Fatalf("%s InputSchema is not valid JSON: %v", tool, err)
	}
	p, ok := decoded.Properties[name]
	if !ok {
		t.Fatalf("%s publishes no `%s` property at all", tool, name)
	}
	return p
}

func eventToolPropDesc(t *testing.T, schema json.RawMessage, tool, name string) string {
	t.Helper()
	desc, _ := eventToolProp(t, schema, tool, name)["description"].(string)
	if desc == "" {
		t.Fatalf("%s.%s publishes an empty description", tool, name)
	}
	return desc
}

// TestEmitEventTypeIsNotPublishedAsAClosedEnum is the deliberate deviation from
// the ruling's literal wording, pinned so it is a decision rather than a
// shortcut.
//
// §6.2 T2-5's ruling reads "publish the event vocabulary as an enum on
// pf_emit_event". The CAUTION attached to the same ruling is why the `enum` KEY
// is not used: an MCP enum is advisory. polyforge registers through the untyped
// (*mcp.Server).AddTool method, whose callTool path runs the handler with no
// schema step (measured on go-sdk v1.6.0), and EmitEvent accepts any string, so
// `enum` here would state a closed contract that neither end keeps.
//
// aihub#445 WITHDREW exactly such an enum from pf_remember in commit f128b69, the
// base of this change. Publishing a new one here would leave the repo asserting
// both halves of one question. The substance of the ruling is
// delivered — the vocabulary IS on the wire, checked below — and only the word
// that would make it false is dropped.
//
// If event_type is ever genuinely closed (a DB CHECK, or a Go reject-list), this
// test is the thing to delete, and deleting it should be the same change that
// closes it.
func TestEmitEventTypeIsNotPublishedAsAClosedEnum(t *testing.T) {
	if raw, ok := eventToolProp(t, emitEventSchema(), "pf_emit_event", "event_type")["enum"]; ok {
		t.Errorf("pf_emit_event.event_type publishes enum %v. agent_events.event_type has no CHECK "+
			"and EmitEvent accepts any string, so this states a closed contract nothing keeps — the "+
			"aihub#238 rule, and the thing aihub#445 just removed from pf_remember. Publish the "+
			"vocabulary in the description as an open list, or close the set for real first.", raw)
	}
}

// TestEmitEventTypeDescriptionPublishesTheVocabulary is the other half: dropping
// the enum without publishing anything would leave the caller exactly where
// §6.2 T2-5 found them, guessing.
func TestEmitEventTypeDescriptionPublishesTheVocabulary(t *testing.T) {
	desc := eventToolPropDesc(t, emitEventSchema(), "pf_emit_event", "event_type")

	for _, typ := range domain.EventVocabulary {
		if !strings.Contains(desc, typ) {
			t.Errorf("pf_emit_event.event_type does not publish %q, which domain.EventVocabulary "+
				"carries. A name the server can write and the schema does not name is a name a "+
				"caller can only learn by accident.", typ)
		}
	}

	// The vocabulary alone would be read as closed — the failure this whole row
	// is about, arriving from the other direction — so the openness has to be
	// stated, and so does each rule that really is enforced.
	for _, must := range []string{"NOT a closed set", "no CHECK", "403", "400"} {
		if !strings.Contains(desc, must) {
			t.Errorf("pf_emit_event.event_type never says %q. The published list is not enforced and "+
				"the three admin / work_item_id rules are; a description that states neither leaves a "+
				"caller unable to tell which refusals are real.", must)
		}
	}

	// The enforced sets are published by NAME, not merely referred to, because a
	// 403 a caller cannot predict is a 403 they will retry.
	for _, typ := range domain.AdminOnlyEventTypes {
		if !strings.Contains(desc, typ) {
			t.Errorf("pf_emit_event.event_type does not name admin-only type %q", typ)
		}
	}
	for _, typ := range domain.AdminEventWhitelist {
		if !strings.Contains(desc, typ) {
			t.Errorf("pf_emit_event.event_type does not name admin-whitelisted type %q", typ)
		}
	}
}

// TestEmitEventTypeDescriptionIsDerivedNotRetyped guards the drift that a
// hand-typed copy would reintroduce.
//
// The whole defect being repaired is three lists of one vocabulary that stopped
// agreeing. A fourth copy, typed into a string literal, would be the same defect
// with the wire added to it — and it would be the copy nobody re-reads, because
// it is the only one that is not code.
func TestEmitEventTypeDescriptionIsDerivedNotRetyped(t *testing.T) {
	desc := emitEventTypePropDescription()

	// A literal list would not move when the domain lists move. Rather than
	// parsing the builder, this asserts the observable consequence: every
	// vocabulary entry appears, in the order sort.Strings puts them, as one run
	// of comma-separated text.
	sorted := append([]string(nil), domain.EventVocabulary...)
	for i := 1; i < len(sorted); i++ {
		for j := i; j > 0 && sorted[j] < sorted[j-1]; j-- {
			sorted[j], sorted[j-1] = sorted[j-1], sorted[j]
		}
	}
	joined := strings.Join(sorted, ", ")
	if !strings.Contains(desc, joined) {
		t.Errorf("pf_emit_event.event_type does not contain domain.EventVocabulary as one sorted run, "+
			"so it is not built from it. Wanted the substring:\n%s", joined)
	}
}

// TestReadEventsTypesIsDescribedAsAFilterNotAWhitelist is the string §6.2 T2-5
// names directly.
//
// "Whitelist" and "filter" differ in the only way that matters: a whitelist
// REJECTS what is not on it. This rejects nothing — an unknown value becomes
// `event_type IN ('typo')`, matches no row, and returns 200 with an empty list.
// Measured over 2,244 transcripts, callers passed 34 distinct values here and
// about half name nothing any code path emits; every one of those calls got that
// empty 200 back. This is aihub#259's failure mode surviving its own fix.
func TestReadEventsTypesIsDescribedAsAFilterNotAWhitelist(t *testing.T) {
	desc := eventToolPropDesc(t, readEventsSchema(), "pf_read_events", "types")

	if strings.Contains(strings.ToLower(desc), "(whitelist)") {
		t.Errorf("pf_read_events.types still calls itself a whitelist:\n%s", desc)
	}
	if !strings.Contains(desc, "FILTER") {
		t.Errorf("pf_read_events.types does not say it is a filter:\n%s", desc)
	}
	for _, must := range []string{"NOT rejected", "empty list"} {
		if !strings.Contains(desc, must) {
			t.Errorf("pf_read_events.types never says %q — the consequence, not the label, is what a "+
				"caller needs: an unmatched name and an event that did not happen look identical.", must)
		}
	}

	// The lock-churn caveat is aihub#343's and is load-bearing for a different
	// reason (an unfiltered read can be filled by one claim's lock_acquired
	// events). Rewording `types` must not drop it.
	for _, keep := range []string{"lock_acquired", "lock_released", "wi_resources_updated", "limit 50"} {
		if !strings.Contains(desc, keep) {
			t.Errorf("pf_read_events.types lost the aihub#343 clause about %q", keep)
		}
	}
}

// TestReadEventsUserIDNamesTheIdentityItFilters is §6.2 T2-18 on this tool.
//
// The row asks every user_id-shaped parameter to say which of three identities
// it filters — reporter, attempt owner, watcher. The honest answer here is NONE
// OF THEM: ListEvents compares agent_events.actor_user_id, the emitter. Naming
// one of the three would have been the row satisfied and the caller misled.
func TestReadEventsUserIDNamesTheIdentityItFilters(t *testing.T) {
	desc := eventToolPropDesc(t, readEventsSchema(), "pf_read_events", "user_id")

	if desc == "Filter by user" {
		t.Fatal("pf_read_events.user_id still says only \"Filter by user\" — the §6.2 T2-18 instance")
	}
	for _, must := range []string{"ACTOR", "actor_user_id", "NOT the work item's reporter", "watcher"} {
		if !strings.Contains(desc, must) {
			t.Errorf("pf_read_events.user_id never says %q. It filters the EMITTER, which is none of "+
				"the three identities T2-18 enumerates, and a caller who assumes reporter gets a "+
				"confident wrong answer rather than an error.", must)
		}
	}
}

// TestListWorkItemsUserIDStillNamesReporter is the cross-check T2-18 implies but
// does not restate: the two user_id filters mean DIFFERENT things, so making one
// honest is only half the row. aihub#383 made this one honest; this asserts it
// stayed that way while its sibling was being rewritten, because "both say
// something specific" and "both say the same thing" are indistinguishable to a
// reader who checks only one.
func TestListWorkItemsUserIDStillNamesReporter(t *testing.T) {
	desc := eventToolPropDesc(t, listWorkItemsSchema(), "pf_list_work_items", "user_id")

	for _, must := range []string{"REPORTER", "reporter_user_id"} {
		if !strings.Contains(desc, must) {
			t.Errorf("pf_list_work_items.user_id no longer names %q (aihub#383)", must)
		}
	}
	if strings.Contains(desc, "actor_user_id") {
		t.Error("pf_list_work_items.user_id now mentions actor_user_id — it filters the REPORTER; " +
			"the actor filter is pf_read_events.user_id, and conflating them is the T2-18 defect")
	}
}
