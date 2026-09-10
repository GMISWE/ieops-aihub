package mcp_test

// aihub#543 probe wave 2, band 1 — the `docs/mcp-cards/pf_update_work_item.md`
// sentences about what the caller is TOLD and what the reply then contains.
//
//	"The token comes from `pf_get_work_item`, which is named in the description
//	 for the reason `aihub#260` gives about `members_version` — a guard whose
//	 input nobody can find is a guard nobody passes."
//	    -> TestPublishedCASGuardNamesTheToolThatReturnsItsToken
//	"`brief` is the wider rule and is checked first."
//	    -> TestBriefDropsTheBodyWhereTheEqualityGateWouldKeepIt
//	"`brief` here is **not** `pf_get_work_item`'s `brief`: this one reports
//	 `content_len`, that one reports nothing."
//	    -> TestPublishedBriefDifferenceFromGetIsTheEnforcedOne
//	"this tool's reply carries `requires_human_session` whether or not the call
//	 wrote it, so a caller cannot tell a value it read from a value it wrote."
//	    -> TestTheUpdateEchoCarriesRequiresHumanSessionEitherWay
//
// 🔴 WHY THE EXISTING GATES CANNOT HOLD ANY OF THEM.
//
// TestResourcesVersionDescriptionExplainsCAS reads the same description and
// asserts three things about it — the 409, the code, and that the unconditional
// path is stated without an imperative. It says nothing about where the token
// comes from, so a description that dropped the only pointer to the tool that
// returns it stays green there while the guard becomes one a caller cannot pass.
//
// The two `brief` claims are ORDER and CROSS-TOOL, and every existing arm in
// wi_echo_test.go drives one tool with one flag: TestUpdateBriefDropsContentTheCallerNeverSent
// covers brief with nothing sent, TestUpdateKeepsContentWhenTheStoredValueDiffers
// covers a divergence with no brief, and nothing sends BOTH — which is the only
// input at which the two rules disagree. Nothing anywhere drives
// pf_get_work_item's brief at all: the universal gate records its parameter as
// "consumed here" and that is a claim about the wire, not about the reply's
// shape.
//
// The `requires_human_session` claim is the one the aihub#447 misattribution
// turned on, and requireIdentityFieldsSurvive — the closest thing to it — names
// id, slug, seq, goal, attrs and declared_resources, not this field.
//
// No database:
//
//	GOWORK=off go test ./internal/mcp/ -run 'TestPublishedCASGuardNames|TestBriefDropsTheBody|TestPublishedBriefDifference|TestTheUpdateEchoCarries' -count=1

import (
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/GMISWE/ieops-aihub/internal/domain"
)

// casTokenSourceTool is the tool whose reply carries the version this guard
// compares against. Named once: it is both the string the description must
// contain and the tool whose response really has to carry the key.
const casTokenSourceTool = "pf_get_work_item"

// casGuardParam is the guarded parameter, and casGuardedColumn the field it
// guards — the pair the description has to connect for a caller to use it.
const (
	casGuardParam    = "resources_version"
	casGuardedColumn = "declared_resources"
)

// TestPublishedCASGuardNamesTheToolThatReturnsItsToken is the aihub#260 property
// as an assertion: a compare-and-set guard is unusable unless its input is
// findable, so the description names the tool, and that tool's reply really
// carries the key.
//
// 🔴 The enforcement half is read off domain.WorkItem's json tags rather than
// from a list retyped here. That is what makes it fail in the direction that
// matters: rename the field on the struct and the description keeps pointing at
// a reply that no longer has it, which is the state aihub#260 describes for
// members_version.
//
// MUTANTS (applied to this tree and run; the verdict is what happened, not what
// was expected):
//
//	M18 publication: reword the description to name the parameter but not the tool
//	    ("ALWAYS send the resources_version you last read")
//	                                             RED  names_the_source. This is the
//	                                                  PLAUSIBLE rewrite rather than a
//	                                                  deletion, and the one a
//	                                                  substring search for "version"
//	                                                  would wave through
//	M19 enforcement: rename domain.WorkItem's `resources_version` json tag to
//	    `resources_v`                            RED  the reply arm, naming the tag
//	                                                  set it found
//	M20 enforcement: publish the named tool under a different name
//	    (pf_get_work_item_v2)                    RED  the floor — a description
//	                                                  naming a tool that does not
//	                                                  exist is worse than one naming
//	                                                  none
//	M21 publication: drop the citation clause from the card sentence
//	                                             RED  K12 DEBT_GROWTH
func TestPublishedCASGuardNamesTheToolThatReturnsItsToken(t *testing.T) {
	props := schemaProps(t, publishedTool(t, "pf_update_work_item"))
	guard, ok := props[casGuardParam]
	if !ok {
		t.Fatalf("pf_update_work_item publishes no %q (it publishes %v) — the guard is gone, and "+
			"every assertion below would be about a parameter callers cannot send",
			casGuardParam, sortedPropNames(propTypes(props)))
	}
	desc := guard.Description

	t.Run("names_the_source", func(t *testing.T) {
		if !strings.Contains(desc, casTokenSourceTool) {
			t.Errorf("%s is published as %q, which never names %s. The value is a token the "+
				"caller has to have READ from somewhere, and this description is the only place "+
				"a caller learns where — aihub#260's finding on members_version: a guard whose "+
				"input nobody can find is a guard nobody passes, and every such guard in "+
				"polyforge is opt-in.", casGuardParam, desc, casTokenSourceTool)
		}
		// The guard must also say what it guards, or "send this token" is advice
		// with no subject.
		if !strings.Contains(desc, casGuardedColumn) {
			t.Errorf("%s is published as %q, which does not say it guards %s",
				casGuardParam, desc, casGuardedColumn)
		}
	})

	t.Run("the_named_tool_is_published", func(t *testing.T) {
		// The floor for the arm below: a description may name any string, and the
		// assertion "its reply carries the key" is answered by an unpublished tool
		// just as well as by a correct one.
		if tool := publishedTool(t, casTokenSourceTool); tool == nil {
			t.Fatalf("%s is not published, so the description points at a call a caller cannot "+
				"make", casTokenSourceTool)
		}
	})

	t.Run("the_named_tools_reply_carries_the_token", func(t *testing.T) {
		tags := workItemJSONTags(t)
		if len(tags) < 10 {
			t.Fatalf("domain.WorkItem exposes only %d json field(s) (%v) — the record this walk "+
				"reads is not the one both tools answer with, and the assertion below would be "+
				"about nothing", len(tags), sortedNames(tags))
		}
		if !tags[casGuardParam] {
			t.Errorf("domain.WorkItem has no json:%q field, so %s's reply does not carry the "+
				"token %s's description tells the caller to read from it. Its fields are %v. "+
				"That is the members_version shape exactly: the guard survives, the input "+
				"stops being reachable, and the caller finds out by getting a 409 it cannot "+
				"resolve.", casGuardParam, casTokenSourceTool, casGuardParam, sortedNames(tags))
		}
	})
}

// workItemJSONTags is the wire key set of the record both pf_get_work_item and
// pf_update_work_item answer with, read off the struct rather than written down.
//
// Both handlers end in c.JSON(..., wi) on the same *domain.WorkItem — router.go
// handleGetWorkItem and handleUpdateWorkItem — so one tag set answers for both
// replies, which is what lets the description's pointer be checked against the
// tool it points AT rather than against the tool that publishes it.
func workItemJSONTags(t *testing.T) map[string]bool {
	t.Helper()
	typ := reflect.TypeOf(domain.WorkItem{})
	out := make(map[string]bool, typ.NumField())
	for i := 0; i < typ.NumField(); i++ {
		name, _, _ := strings.Cut(typ.Field(i).Tag.Get("json"), ",")
		if name == "" || name == "-" {
			continue
		}
		out[name] = true
	}
	return out
}

// briefRecordID is the work item every brief case below is served for.
const briefRecordID = "wi_brief_probe"

// TestBriefDropsTheBodyWhereTheEqualityGateWouldKeepIt is the ORDER claim, at
// the one input where the two rules disagree.
//
// The stored content differs from what the caller sent — the concurrent-writer
// window the equality gate exists for — and `brief` is set. The equality gate
// alone would KEEP the body (that is
// TestUpdateKeepsContentWhenTheStoredValueDiffers); brief alone drops it and
// reports the length. So the answer names which of the two ran, and the
// in-arm control re-sends the identical call without brief so the case cannot
// pass because the fake stopped serving a body.
//
// MUTANTS (applied to this tree and run):
//
//	M22a enforcement: check the equality gate first and brief second, as
//	     `if !suppressContentEcho(…) && brief { drop }`
//	                                             GREEN — and this is the measurement
//	                                                  that shapes the arm. Branch
//	                                                  ORDER alone has no observable
//	                                                  consequence: whichever runs
//	                                                  first, a set `brief` ends in
//	                                                  dropContentEcho and the reply
//	                                                  is the same bytes. So what this
//	                                                  arm holds is the WIDTH — brief
//	                                                  applies whether or not the
//	                                                  caller sent a body and whether
//	                                                  or not it matched — which is
//	                                                  the half a caller can act on
//	M22b enforcement: let the equality gate's PRECONDITION subsume brief, as
//	     `if content was sent { suppress } else if brief { drop }`
//	                                             RED  both assertions: the body
//	                                                  survives and no content_len is
//	                                                  reported. This is the shape the
//	                                                  handler's own comment argues
//	                                                  against, and every existing arm
//	                                                  in wi_echo_test.go passes it
//	M23  enforcement: make dropContentEcho stop reporting content_len
//	                                             RED  the length assertion
//	M24  instrument: make the fake serve no body at all
//	                                             RED  the control, which requires the
//	                                                  body back OUT when brief is
//	                                                  absent
//	M25  publication: drop the citation clause from the card sentence
//	                                             RED  K12 DEBT_GROWTH
func TestBriefDropsTheBodyWhereTheEqualityGateWouldKeepIt(t *testing.T) {
	// Same byte length, different bytes: the fixture wi_echo_test.go established
	// for this window, so a length-only comparison cannot satisfy either arm.
	const sent = "status: queued\nowner: nobody\n"
	const stored = "status: paused\nowner: nobody\n"
	if stored == sent || len(stored) != len(sent) {
		t.Fatalf("fixture cannot discriminate: stored and sent must differ in bytes and agree "+
			"in length (%q vs %q)", stored, sent)
	}

	t.Run("brief wins", func(t *testing.T) {
		f := newFakeAihub(t)
		servePatch(f, briefRecordID, workItemRecord(briefRecordID, stored))

		_, got := callToolText(t, f, "pf_update_work_item", map[string]any{
			"work_item_id": briefRecordID, "content": sent, "brief": true,
		})

		if _, present := got["content"]; present {
			t.Errorf("brief=true left the body in the reply. The equality gate would keep it " +
				"here — stored differs from sent — so this is the equality gate answering a " +
				"call that set brief, i.e. brief has stopped being the WIDER rule and now " +
				"applies only where the gate would have dropped the body anyway. (Branch ORDER " +
				"is not the claim: M22a swapped the two and stayed green.)")
		}
		if got["content_len"] != float64(len(stored)) {
			t.Errorf("content_len = %v, want %d. Under brief the reply must always carry a "+
				"positive signal: the length of what was left out, or `content: null` when "+
				"there is no body.", got["content_len"], len(stored))
		}
	})

	t.Run("control: without brief the same call keeps the body", func(t *testing.T) {
		f := newFakeAihub(t)
		servePatch(f, briefRecordID, workItemRecord(briefRecordID, stored))

		_, got := callToolText(t, f, "pf_update_work_item", map[string]any{
			"work_item_id": briefRecordID, "content": sent,
		})

		if got["content"] != stored {
			t.Errorf("content = %#v, want %#v — without this the case above is satisfied by a "+
				"fake that serves no body and by a handler that deletes content unconditionally",
				got["content"], stored)
		}
		if _, present := got["content_len"]; present {
			t.Errorf("content_len = %v on a reply that kept the body", got["content_len"])
		}
	})
}

// TestPublishedBriefDifferenceFromGetIsTheEnforcedOne pins the cross-tool claim
// in both of its halves: the description says the two flags differ, and the two
// tools really answer differently.
//
// 🔴 One word, two tools — the same shape as the idempotency-key arm next door.
// The published text is the only place a caller learns that a flag it already
// knows from pf_get_work_item means something else here, and the difference is
// observable in exactly one key.
//
// MUTANTS (applied to this tree and run):
//
//	M26 publication: delete the "NOT the same as pf_get_work_item's brief" clause
//	                                             RED  names_the_other_tool
//	M27 enforcement: make pf_get_work_item's brief call dropContentEcho too
//	                                             RED  the get arm — content_len = 52
//	                                                  appears where the description
//	                                                  says nothing is reported
//	M28 enforcement: make pf_update_work_item's brief `delete(result,"content")`
//	    without the length                       RED  the update arm
//	M29 publication: drop the citation clause from the card sentence
//	                                             RED  K12 DEBT_GROWTH
func TestPublishedBriefDifferenceFromGetIsTheEnforcedOne(t *testing.T) {
	const body = "## Body\n\nthe part a caller may not want echoed back\n"

	t.Run("names_the_other_tool", func(t *testing.T) {
		props := schemaProps(t, publishedTool(t, "pf_update_work_item"))
		brief, ok := props["brief"]
		if !ok {
			t.Fatalf("pf_update_work_item publishes no `brief` (it publishes %v)",
				sortedPropNames(propTypes(props)))
		}
		if !strings.Contains(brief.Description, casTokenSourceTool) {
			t.Errorf("pf_update_work_item publishes brief as %q, which never names %s. A caller "+
				"meets this flag on that tool first, where it deletes the body and reports "+
				"nothing; here it replaces the body with a length. A flag whose meaning "+
				"changes between tools and says so nowhere is one a caller reads once.",
				brief.Description, casTokenSourceTool)
		}
		if !strings.Contains(brief.Description, "content_len") {
			t.Errorf("pf_update_work_item publishes brief as %q, which does not name the key it "+
				"substitutes for the body (content_len)", brief.Description)
		}
	})

	t.Run("update reports the length", func(t *testing.T) {
		f := newFakeAihub(t)
		servePatch(f, briefRecordID, workItemRecord(briefRecordID, body))

		_, got := callToolText(t, f, "pf_update_work_item", map[string]any{
			"work_item_id": briefRecordID, "priority": "urgent", "brief": true,
		})
		if _, present := got["content"]; present {
			t.Errorf("brief=true kept the body on the update path")
		}
		if got["content_len"] != float64(len(body)) {
			t.Errorf("content_len = %v, want %d", got["content_len"], len(body))
		}
	})

	t.Run("get reports nothing", func(t *testing.T) {
		f := newFakeAihub(t)
		// The same path serves both methods, so this is the SAME record the update
		// case above reads — the difference measured below is the tool's, not the
		// fixture's.
		servePatch(f, briefRecordID, workItemRecord(briefRecordID, body))

		_, got := callToolText(t, f, "pf_get_work_item", map[string]any{
			"work_item_id": briefRecordID, "brief": true,
		})
		if _, present := got["content"]; present {
			t.Errorf("pf_get_work_item's brief kept the body, so this arm is not exercising the " +
				"flag at all and the absence below says nothing")
		}
		if v, present := got["content_len"]; present {
			t.Errorf("pf_get_work_item reported content_len = %v under brief. The update tool's "+
				"published description tells callers this tool reports nothing, and a caller "+
				"who reads a length here cannot tell the two flags apart — which is the "+
				"distinction that sentence exists to make.", v)
		}
		// The floor: the reply must be the record, or "no content_len" is the
		// answer an error result would give too.
		if got["id"] != briefRecordID {
			t.Errorf("pf_get_work_item answered %#v, not the served record — the absence "+
				"assertion above would then be about an empty reply", got["id"])
		}
	})
}

// TestTheUpdateEchoCarriesRequiresHumanSessionEitherWay is the aihub#447
// misattribution's own explanation, asserted.
//
// Two calls, one writing the field and one not, and the reply's key set must be
// the SAME. That is what makes a value a caller read indistinguishable from one
// it wrote — the property that made "an attrs_patch-only update moved
// requires_human_session" a reading somebody could arrive at honestly.
//
// ⚠️ Asserted as equality over the whole key set rather than presence of one
// key, because the cheap way to make the two calls distinguishable is to ADD
// something to one of them (a `written_fields` list, a `requires_human_session_source`)
// rather than to remove the field.
//
// MUTANTS (applied to this tree and run):
//
//	M30 enforcement: delete requires_human_session from the reply when the caller
//	    did not send it                          RED  the FLOOR, naming the field and
//	                                                  the key set it did find
//	M31 enforcement: add a `requires_human_session_written` bool to the reply
//	                                             GREEN on the first version of this
//	                                                  arm, which compared key SETS —
//	                                                  the sets matched and the VALUES
//	                                                  carried the answer. RED after
//	                                                  the comparison became per-key,
//	                                                  naming the key and both values.
//	                                                  ⚠️ The arm was FIXED by this
//	                                                  mutant; it is recorded rather
//	                                                  than quietly repaired because
//	                                                  the weaker version is the one a
//	                                                  reader would write again
//	M32 instrument: make the fake omit the field from the served record
//	                                             RED  the floor, from the other side
//	M33 publication: drop the citation clause from the card sentence
//	                                             RED  K12 DEBT_GROWTH
func TestTheUpdateEchoCarriesRequiresHumanSessionEitherWay(t *testing.T) {
	const field = "requires_human_session"

	reply := func(t *testing.T, args map[string]any) map[string]any {
		t.Helper()
		f := newFakeAihub(t)
		servePatch(f, briefRecordID, workItemRecord(briefRecordID, "a body"))
		args["work_item_id"] = briefRecordID
		_, got := callToolText(t, f, "pf_update_work_item", args)
		return got
	}

	wrote := reply(t, map[string]any{field: true})
	readOnly := reply(t, map[string]any{"attrs_patch": map[string]any{"probe": "x"}})

	// FLOOR first: both replies must actually carry the field. Absent from both,
	// the equality below is satisfied by a record that never had it.
	for name, got := range map[string]map[string]any{"wrote it": wrote, "did not write it": readOnly} {
		if _, present := got[field]; !present {
			t.Fatalf("the reply to a call that %s carries no %q at all (keys=%v) — this walk is "+
				"not reading the work-item record, and the comparison below would pass on two "+
				"equally empty answers", name, field, sortedReplyKeys(got))
		}
	}

	// 🔴 The comparison is the WHOLE reply, key by key, not the presence of one
	// field — and that is a correction the mutants forced. A key-set equality was
	// GREEN against a handler that added `requires_human_session_written: true/false`
	// to both replies: the sets matched and the VALUES carried the answer. The
	// served record is identical for both calls, so anything that differs between
	// them was written by this process, which is exactly what "a caller cannot
	// tell" forbids.
	if a, b := sortedReplyKeys(wrote), sortedReplyKeys(readOnly); strings.Join(a, ",") != strings.Join(b, ",") {
		t.Errorf("the two replies carry different keys:\n  wrote the field:     %v\n"+
			"  did not write it:    %v\nThe card explains the aihub#447 misattribution by this "+
			"tool's reply being identical either way; a caller that CAN tell the two apart is a "+
			"better contract, and the card's account of why the audit reached the wrong "+
			"conclusion has to change with it.", a, b)
	}
	for _, k := range sortedReplyKeys(wrote) {
		if !reflect.DeepEqual(wrote[k], readOnly[k]) {
			t.Errorf("%q = %#v in the reply to the call that wrote %s and %#v in the reply to the "+
				"one that did not, from the SAME served record. Whatever the difference is, this "+
				"process put it there, and a caller can read it — which is the distinction the "+
				"card says does not exist and the aihub#447 audit needed.",
				k, wrote[k], field, readOnly[k])
		}
	}
}

// sortedKeys is the reply's key set, ordered so a failure message is stable.
func sortedReplyKeys(m map[string]any) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// sortedNames is the same for a set.
func sortedNames(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// propTypes reduces a decoded property map to name -> type, which is what
// sortedPropNames (claim_published_word_test.go) takes.
func propTypes(props map[string]struct {
	Type        string `json:"type"`
	Description string `json:"description"`
}) map[string]string {
	out := make(map[string]string, len(props))
	for k, v := range props {
		out[k] = v.Type
	}
	return out
}
