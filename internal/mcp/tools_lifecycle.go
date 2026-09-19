package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"time"

	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/GMISWE/ieops-aihub/internal/config"
	"github.com/GMISWE/ieops-aihub/internal/domain"
	"github.com/GMISWE/ieops-aihub/internal/lifecycle"
)

// pf_list_work_items forwarding tables.
//
// Publishing a param in the InputSchema while forgetting it in the forwarding
// loop makes the schema state a contract the transport does not keep — the
// caller sends the param, nothing rejects it, and it is silently dropped
// (mem_1SJ12mCz). Keeping them as named tables lets
// TestListWorkItemsEveryPublishedParamHasAWireProbe assert they agree with the
// schema, and TestListWorkItemsForwardsEveryPublishedParamByValue assert each
// decoder can actually read the shapes callers send.
//
// aihub#280: agreement between these tables and the schema is hop 2 of a
// four-hop contract, and is *not* sufficient. See the header of
// tools_list_wi_schema_test.go for the other three and where each is asserted.
var (
	// listWorkItemsStringParams are forwarded verbatim when non-empty.
	listWorkItemsStringParams = []string{
		"project", "kind", "wi_type", "priority", "milestone", "scenario",
		"label", "user_id", "claimed_by", "owner_display", "reporter_display",
		"source", "since", "limit", "cursor",
		"sort", "order", "query",
		// aihub#277 / aihub#276. Both go through scalarArg (like `limit`), so
		// a caller sending min_similarity as a JSON number — which is the
		// natural shape and what every caller will send — is forwarded rather
		// than dropped by strArg.
		"similar_to", "min_similarity",
	}
	// listWorkItemsBoolParams are forwarded as "true" when set.
	listWorkItemsBoolParams = []string{"ready_only", "include_step_state"}
	// listWorkItemsCSVParams accept EITHER a JSON string (already comma-
	// separated) or a JSON array of strings, and are forwarded as CSV — the wire
	// form handleListWorkItems' strings.Split expects.
	//
	// Both shapes are accepted because both occur. The schema publishes `ids` as
	// an array and `status` as a CSV string, but every polyforge skill that
	// filters by status wrote `status=["wrapped"]`, and strArg returns "" for a
	// non-string — so setIfNonempty dropped it and /pf-release listed the
	// project's entire backlog instead of one release's worth. Coercing here
	// fixes those callers without a plugin redeploy; the skills were corrected
	// too, so the published shape and the call sites now agree (aihub#280).
	listWorkItemsCSVParams = []string{"ids", "status"}
)

// buildListWorkItemsParams renders MCP call arguments into the HTTP query
// string for GET /v1/work_items. This is hop 2 of the four-hop parameter
// contract (aihub#280).
//
// Split out of the tool handler so hop 2 can be asserted on the value that
// actually reaches the wire, not merely on the three tables agreeing with the
// schema by name. Name agreement was green throughout the period when
// `status=["wrapped"]` was being discarded on every call: the name matched, the
// decoder could not read the shape, and nothing anywhere said so.
func buildListWorkItemsParams(args map[string]any) (url.Values, error) {
	params := url.Values{}
	// scalarArg, not strArg: `limit` is published as a string but real callers
	// send it as a JSON number, and strArg drops non-strings (aihub#280 B6).
	for _, k := range listWorkItemsStringParams {
		setIfNonempty(params, k, scalarArg(args, k))
	}
	for _, k := range listWorkItemsBoolParams {
		value, present, ok := parseBoolArg(args, k)
		if !present {
			continue
		}
		if !ok {
			// Rejected rather than defaulted to false. Defaulting is what made
			// `ready_only: "true"` return the unfiltered list, and it is
			// indistinguishable from not sending the param at all.
			return nil, fmt.Errorf("%s must be a boolean (true/false, \"true\"/\"false\", or 1/0), got %#v", k, args[k])
		}
		// Only a true is forwarded: the server reads an absent param as false,
		// so sending "false" would be redundant, and forwarding it would make
		// "explicitly false" and "unset" identical on the wire anyway.
		if value {
			params.Set(k, "true")
		}
	}
	for _, k := range listWorkItemsCSVParams {
		setIfNonempty(params, k, csvArg(args, k))
	}
	return params, nil
}

// listWorkItemsSchema is the published input schema for pf_list_work_items,
// split out so the forwarding test can read the same value the tool registers.
func listWorkItemsSchema() json.RawMessage {
	idsProp := prop("array", "Filter to these work item IDs or slugs (array of strings; "+
		"a comma-separated string is also accepted). Makes `project` optional: an id "+
		"already names one work item, and the query is bounded to the projects you can see. "+
		"An inaccessible project= answers 404 and ids you cannot see are silently omitted; "+
		"neither says whether the thing exists (aihub#377).")
	idsProp["items"] = map[string]any{"type": "string"}
	return objectSchema(map[string]any{
		"project": prop("string", "Project name. Optional when `ids` or `similar_to` is given "+
			"(each already names a work item); required otherwise. Omitting it widens the "+
			"search to every project you can see."),
		"ids": idsProp,
		"status": prop("string", "Filter by status; comma-separated for several "+
			"(e.g. \"running,paused\"). An array of strings is also accepted."),
		"wi_type":   prop("string", "Filter by work item type (e.g. fix_bug, feature)"),
		"kind":      prop("string", "DEPRECATED alias for `wi_type`; an explicit wi_type wins. There is no separate `kind` field."),
		"priority":  prop("string", "Filter by priority (urgent|high|normal|low)"),
		"milestone": prop("string", "Filter by milestone"),
		// Deliberately NOT an enum, and deliberately not advertising "release".
		// work_items.scenario is CHECKed to ('coding','writing','data') and
		// CreateWorkItem is stricter still (it rejects anything but 'coding'), so
		// 'coding' is the only value any existing row can hold. pf-release
		// filters on scenario="release", which now correctly matches nothing —
		// see the note at that call site; making release wis real is aihub#176.
		"scenario": prop("string", "Filter by scenario. In practice every work item is 'coding': "+
			"the column is constrained to coding|writing|data and creation rejects all but coding."),
		"label": prop("string", "Filter by label"),
		// aihub#383. hop 4 is `wi.reporter_user_id = $N` (domain.buildListWorkItemsWhere):
		// REPORTER only. A work item has two more user relations — the attempt
		// owner and its watchers — and neither is in that predicate, so "Filter by
		// user ID" promised a superset and the excess came back as a silent empty
		// page ("which wis are mine" returned the ones the caller had filed and none
		// they were merely working on). Description only: widening the
		// predicate changes every existing caller's result set and is not this wi.
		// Wire cost 17 B -> 146 B, inside TestListWorkItemsSchemaStaysWithinItsWireBudget.
		"user_id": prop("string", "Filter by REPORTER only: matches wi.reporter_user_id, i.e. the work items this user filed. Attempt owner and watchers are not covered (aihub#383)."),
		// aihub#652: the attempt-owner half user_id explicitly disclaims above.
		// Exact match on run_attempts.actor_user_id for the CURRENT/LATEST
		// attempt (via wi.current_attempt_id) — not a display-name search.
		//
		// review_fix (mem_dors6nNu): the description string itself used to read
		// "Filter by attempt claimant, exact match. See docs/mcp-tools.md.", which
		// never disclosed the CURRENT-attempt-only narrowing — unlike user_id's
		// own disclosure just above. "only" below is that disclosure: a superseded
		// claimant (a prior attempt on a since-reclaimed work item) does not match.
		"claimed_by": prop("string", "Filter by CURRENT attempt's claimant only, exact match."),
		// aihub#656. Unlike user_id/claimed_by above, these two are NOT exact
		// matches: case-insensitive ILIKE-contains against wi.reporter_display /
		// run_attempts.actor_display (CURRENT/LATEST attempt) respectively — a
		// display-name search, not an id filter. Said explicitly because the two
		// params right above it are both exact-match and a reader would otherwise
		// assume the same semantics carry over.
		"owner_display":    prop("string", "Filter by CURRENT attempt owner's display name, case-insensitive contains match (not exact) against run_attempts.actor_display."),
		"reporter_display": prop("string", "Filter by reporter's display name, case-insensitive contains match (not exact) against wi.reporter_display."),
		"source":           prop("string", "Filter by source"),
		"ready_only": prop("boolean", "Only return items that are ready to claim: queued, "+
			"not requiring a human session, and with no unfinished blocking dependency. "+
			"Same PREDICATE as pf_get_ready_queue's items[] (one shared SQL constant), but "+
			"not the same page: this defaults to limit=50 ordered by created_at desc, while "+
			"the ready queue defaults to 10 ordered by priority desc. With more ready items "+
			"than either limit they return different subsets."),
		"include_step_state": prop("boolean", "Attach each item's step state as `step_state` "+
			"(current_step, current_step_status, step_started_at, ...). The key is ABSENT for a work "+
			"item that has never been claimed, and also if the lookup itself failed, which is "+
			"best-effort and reported only on the server's stderr. Absent therefore means \"no step "+
			"state\", not \"definitely never claimed\"."),
		"since": prop("string", "Only items whose CREATED_AT is at or after this RFC3339 timestamp. "+
			"This is creation time, not close time: combining it with status=wrapped does NOT give "+
			"\"wrapped since T\": an item created before T and wrapped after it is excluded. "+
			"An unparseable value is rejected rather than ignored."),
		// ─── Description budget for the three semantic params ────────────────
		//
		// An InputSchema sits in the prefix of EVERY request, so prose here is a
		// standing charge, exactly like the tool Description below (whose note
		// records +70 B clearing by ~20x while +220 B did not clear at all).
		//
		// MEASURED for this change (len(listWorkItemsSchema()), which is the
		// wire content — NOT `polyforge dump-mcp-schemas`, whose contract JSON
		// carries no descriptions at all and so cannot see any of this).
		// The ceiling is enforced by TestListWorkItemsSchemaStaysWithinItsWire
		// Budget, so these numbers cannot rot silently the way they would if
		// they lived only here:
		//
		//	3,722 B  before
		//	6,000 B  first draft   (+2,278 B ≈ +570 tok/request)
		//	4,886 B  after trimming
		//	5,306 B  as shipped    (+1,584 B ≈ +396 tok/request)
		//
		// The last step back up is review fallout, and it bought correctness
		// rather than prose: `project` no longer claims it is required unless
		// `ids` is given (similar_to now also makes it optional), `similar_to`
		// no longer promises the source appears at 1.0 unconditionally (any
		// other filter can exclude it), and `min_similarity` no longer implies
		// that 0 is rejected. Each of those was a published statement the code
		// does not honour, which is worse than the bytes.
		//
		// The first draft was an order of magnitude past the +220 B already
		// judged too dear below. Two of the three params are NEW, so some of
		// what remains is structural — a published param with no description is
		// not usable. Trimmed to the sentences a caller gets WRONG without: that
		// similarity has no absolute meaning (aihub#276 acceptance criterion 2,
		// and the whole subject of that wi), that two new params exist at all,
		// and each one's error contract. The persuasion — the 20-query
		// measurement, the 81st-vs-1st comparison, the recalibration advice —
		// moved to docs/mcp-tools.md and to wi_vector.go's header, neither of
		// which is resident. Re-measure with the same one-line test before
		// adding a sentence here.
		"query": prop("string", "Semantic search over goal+content (aihub#273): "+
			"embedding cosine when the server has a provider, ILIKE fallback otherwise. "+
			"Similarity-ordered; not combinable with sort/order/cursor; mutually exclusive "+
			"with similar_to. CAUTION: `similarity` compares only WITHIN one result set; it has no "+
			"absolute meaning and there is no relevance filter, so ANY input returns a full "+
			"page. Judge by `semantic.ranked_candidates` (you got the top len(items) of that "+
			"many) and by reading the goals. For \"like this work item\" use similar_to."),
		"similar_to": prop("string", "Document-to-document recall (aihub#277): a work item id or "+
			"slug whose STORED goal+content vector becomes the query vector, far sharper "+
			"than approximating it with a one-line query=. Makes `project` optional the way "+
			"`ids` does. The source is scoped like the results: 404 outside that scope, 412 "+
			"if it has no embedding yet. It is an ordinary row in its own results (so at "+
			"similarity 1.0, unless one of your other filters excludes it); to confirm which "+
			"row was used, read `semantic.source_work_item_id`, which is unconditional. "+
			"`similarity` is still only comparable within this one result set. "+
			"Excludes query=; no sort/order/cursor."),
		"min_similarity": prop("string", "Opt-in cosine floor for the vector path; a JSON "+
			"number is also accepted. Must be in [0,1]. 0 is the default and means OFF, so "+
			"sending 0 is always accepted and always a no-op; any value above 0 requires "+
			"query= or similar_to= and is a 400 otherwise, never a silent no-op. No "+
			"globally valid value exists, so none is ever defaulted: measured, garbage and "+
			"real queries overlap on every similarity-derived statistic."),
		"limit": prop("string", "Max items to return (default 50, ceiling 200). A JSON number "+
			"is also accepted, and is what most callers send. A value above 200 is served as 200 "+
			"and reported in `request_adjusted`; a value that is not an integer is rejected with 400."),
		"cursor": prop("string", "Pagination cursor. Carries the value of the column named by `sort`, "+
			"so pass it back unchanged and do not mix cursors between different sort orders."),
		// The enums come from the server's enforced sets (aihub#224) rather than
		// being retyped here, so the published contract cannot drift from the
		// validator that rejects everything outside them.
		"sort": propEnum("string", fmt.Sprintf(
			"Sort column (default %s). %s returns ONLY closed items, since a NULL close time has no position in that ordering.",
			domain.ListWorkItemsSortCreatedAt, domain.ListWorkItemsSortClosedAt),
			domain.ListWorkItemsSortValues()),
		"order": propEnum("string", fmt.Sprintf("Sort direction (default %s)", domain.ListWorkItemsOrderDesc),
			domain.ListWorkItemsOrderValues()),
	}, nil)
}

func (s *Server) registerLifecycleTools() {
	// pf_whoami
	s.addTool(&sdkmcp.Tool{
		Name:        "pf_whoami",
		Description: "Return caller identity, project roles, and accessible projects from aihub",
		InputSchema: emptyObjectSchema(),
	}, func(ctx context.Context, req *sdkmcp.CallToolRequest) (*sdkmcp.CallToolResult, error) {
		result, err := s.client.WhoAmI(ctx)
		if err != nil {
			return errResult(err)
		}

		// Enrich with projects list: [{name, relation, role}]
		// relation: "owner" | "member" | "public"
		projectsResult, listErr := s.client.ListProjects(ctx, nil)
		if listErr == nil {
			callerID, _ := result["user_id"].(string)
			callerRole, _ := result["role"].(string)
			if itemsAny, ok := projectsResult["items"]; ok {
				if items, ok := itemsAny.([]any); ok {
					projectInfos := make([]map[string]any, 0, len(items))
					for _, item := range items {
						if proj, ok := item.(map[string]any); ok {
							name, _ := proj["name"].(string)
							ownerID, _ := proj["owner_user_id"].(string)
							membersRaw := proj["members"]

							relation := "public"
							memberRole := "viewer"

							if callerRole == "admin" || ownerID == callerID {
								relation = "owner"
								memberRole = "owner"
							} else if membersRaw != nil {
								// Parse members to find caller's role.
								//
								// aihub#312: []any is the shape that actually
								// arrives, and it used to be the one shape this
								// switch did NOT handle. The server sends members
								// as a JSON array (domain.Project.Members is a
								// json.RawMessage) and client.ListProjects decodes
								// the whole response into map[string]any
								// (pkg/client/client.go), so proj["members"] is
								// []any. With only the string and []byte cases
								// below, nothing matched, no members were ever
								// parsed, and EVERY non-admin non-owner member
								// fell through to the public/viewer defaults set
								// just above.
								//
								// string and []byte are kept, but note that
								// s.client is always the HTTP client, so the only
								// way to reach them is a project row whose members
								// JSONB holds a double-encoded JSON string instead
								// of a JSON array. They are legacy-data cases, not
								// caller-shape cases.
								//
								// ⚠️ SECOND DERIVATION — both now correct,
								// still DUPLICATED.
								// internal/server/roleForUserInMembers derives
								// the same "caller's role out of
								// projects.members" fact independently, to fill
								// project_roles. It serves BOTH server auth
								// paths — BearerAuth for /v1 and
								// loadUserByAPIKeyID for the /ui session cookie
								// — which were themselves two inline copies
								// until aihub#315 collapsed them. So the repo
								// holds three call sites and two
								// implementations: this one, and that one.
								//
								// It used to carry both of the defects fixed
								// here, and this block used to say so. It no
								// longer does: aihub#315 fixed that side on
								// 2026-09-02, the same way aihub#312 fixed this
								// one. Concretely, over there:
								//
								//   - the wholesale discard is gone. It still
								//     decodes into a TYPED slice, but keeps the
								//     partially-filled result instead of
								//     `continue`ing on error, which is sound
								//     for the reason spelled out in the []any
								//     case below — encoding/json fills the good
								//     entries in regardless. The guard was
								//     always the bug, not the decoder.
								//   - the identity compare is guarded. It
								//     returns early on an empty caller id, so
								//     the `"" == ""` match a zero-valued junk
								//     entry would otherwise allow is closed by
								//     that line rather than by an accident of
								//     its inputs.
								//
								// What is NOT fixed is the duplication itself.
								// Two implementations still derive one fact, and
								// nothing makes them agree — this comment is
								// the only thing connecting them, and a comment
								// asserting a fact about another file goes stale
								// SILENTLY. It just did: aihub#315 made the
								// paragraph above false the moment it landed and
								// nothing went red, which is why it is written
								// as a dated claim you should re-measure rather
								// than a standing one you should believe.
								//
								// Within the server package the duplication IS
								// gated now (TestProjectRolesHaveOneDerivation
								// requires every writer of ProjectRoles to go
								// through the shared parser). Nothing gates it
								// across the mcp/server boundary, so this pair
								// is still held together by prose alone.
								//
								// Measured 2026-09-02, real function against the
								// eight call sites in
								// tools_whoami_members_test.go: 8/8 agree, the
								// junk-entry fixture included. One shape OUTSIDE
								// that set still differs — a member whose `role`
								// is not a string yields ("",found) over there
								// and ("",not-found) here, so project_roles gets
								// {"aihub":""} rather than {}. checkProjectAccess
								// denies on both, so it is a payload difference,
								// not an authorization one.
								//
								// If you edit either derivation, edit the other,
								// or collapse them and delete this block.
								var members []map[string]any
								switch m := membersRaw.(type) {
								case []any:
									// Walked element by element rather than
									// re-marshalled and re-parsed in one go.
									//
									// NOT because a whole-list json.Unmarshal
									// would fail wholesale on one non-object
									// entry — it does not. encoding/json records
									// the FIRST *json.UnmarshalTypeError it hits
									// inside a slice and KEEPS DECODING the rest.
									// Measured: `[{u_a,writer}, 5, {u_b,viewer}]`
									// into []map[string]any yields a length-3
									// slice holding u_a, a nil map, and u_b —
									// entries on BOTH sides of the bad one are
									// filled — together with a non-nil error.
									//
									// The wholesale discard was the GUARD, not the
									// decoder. The pre-change code read
									// `if json.Unmarshal(...) == nil { ...use... }`,
									// so one junk entry made the error non-nil and
									// threw away a result that was in fact almost
									// entirely populated, degrading every other
									// member to public/viewer — the exact failure
									// mode aihub#312 was.
									//
									// Walking []any is still the right shape: it
									// has no error return at all, so there is
									// nothing here for a later edit to re-guard on
									// and a junk entry can only ever cost its own
									// element.
									members = make([]map[string]any, 0, len(m))
									for _, entry := range m {
										if mem, ok := entry.(map[string]any); ok {
											members = append(members, mem)
										}
									}
								// The dropped errors below are deliberate and are
								// NOT the swallow that caused aihub#312: a failed
								// json.Unmarshal still fills in every element it
								// could decode, on BOTH sides of the bad one —
								// only the bad element itself is left at its zero
								// value. Keeping that partial result therefore
								// degrades per entry exactly like the []any case
								// above, whereas discarding it on error would
								// throw away memberships that parsed cleanly. The
								// uid != "" check below is what stops the
								// zero-valued entry from matching.
								case string:
									_ = json.Unmarshal([]byte(m), &members)
								case []byte:
									_ = json.Unmarshal(m, &members)
								}
								for _, mem := range members {
									uid, _ := mem["user_id"].(string)
									// uid != "" matters now that this loop is
									// reachable at all. Both sides fall back to ""
									// when the field is absent or not a string, so
									// an entry with no user_id would match a caller
									// whose own user_id failed to decode and report
									// them as a member with that entry's role.
									// aihub#312 under-reported privilege; matching
									// on "" would over-report it, which is worse.
									if uid != "" && uid == callerID {
										relation = "member"
										if r, ok := mem["role"].(string); ok {
											memberRole = r
										}
										break
									}
								}
							}

							projectInfos = append(projectInfos, map[string]any{
								"name":     name,
								"relation": relation,
								"role":     memberRole,
							})
						}
					}
					result["projects"] = projectInfos
				}
			}
		}
		// If listing projects fails, this returns the whoami half with the
		// `projects` key the SERVER sent still in place — an array of project NAME
		// STRINGS built from project_roles by handleWhoami — rather than the
		// {name, relation, role} objects the enrichment above writes. Best-effort,
		// and the key is NEVER absent: the element TYPE is the only signal a caller
		// has that the second call failed, which is what
		// whoami_projects_shape_test.go's
		// TestWhoamiKeepsTheServersOwnProjectsWhenTheEnrichmentFails holds on both
		// branches. ⚠️ This comment said "without projects field" until 2026-09-10
		// (aihub#543) — the same falsehood the pf_whoami card corrected, and a
		// caller who believed it would read `.name` off a list of strings.

		return jsonResult(result)
	})

	// pf_create_work_item
	s.addTool(&sdkmcp.Tool{
		Name:        "pf_create_work_item",
		Description: "Create a work item in the specified project. To create more than one, use pf_batch_create_work_items; repeated calls here cost one round-trip each.",
		InputSchema: createWorkItemSchema(),
	}, func(ctx context.Context, req *sdkmcp.CallToolRequest) (*sdkmcp.CallToolResult, error) {
		args, err := parseArgs(req.Params.Arguments)
		if err != nil {
			return errResult(err)
		}
		if strArg(args, "project") == "" {
			return errResult(fmt.Errorf("project is required"))
		}
		if strArg(args, "goal") == "" {
			return errResult(fmt.Errorf("goal is required"))
		}
		applyForceReasonDefault(args)
		result, err := s.client.CreateWorkItem(ctx, args)
		if err != nil {
			// Surface PROJECT_NOT_FOUND with a clear message
			if isAihubCode(err, "PROJECT_NOT_FOUND") {
				return errResult(fmt.Errorf("PROJECT_NOT_FOUND: project %q does not exist; create it first with pf_create_project", strArg(args, "project")))
			}
			return errResult(err)
		}
		// aihub#281: the caller sent this content one line ago; the record it
		// gets back is otherwise complete. No `brief` counterpart is published
		// for create because there is nothing for it to do — a work item's
		// content at creation is whatever the caller supplied, so an unsent
		// content is an absent one and the equality gate already covers 100% of
		// the bytes. (482/482 successful creates in the sample sent content.)
		suppressContentEcho(args, result)
		return jsonResult(result)
	})

	// pf_batch_create_work_items — file several wis in ONE round-trip (aihub#290).
	//
	// 134 measured adjacent create -> create pairs, 0.171% of billed input, spent
	// filing a batch of unrelated follow-ups one call at a time. (Only pairs whose
	// goals were <0.5 similar were counted, so these are genuinely distinct items
	// being filed together, not a client retrying the same one.)
	//
	// A separate tool rather than an `items` array bolted onto pf_create_work_item,
	// for the reason aihub#286 gives for pf_ship: `project` and `goal` are in that
	// tool's flat `required` list, and objectSchema() cannot express "required
	// unless items is set". Overloading it would leave the published schema
	// misdescribing its own contract — aihub#238 / #241 again.
	s.addTool(&sdkmcp.Tool{
		Name: "pf_batch_create_work_items",
		Description: "Create SEVERAL work items in one call. Use this when filing more than one wi at once " +
			"(follow-ups discovered mid-execution, a backlog split into pieces) instead of calling " +
			"pf_create_work_item repeatedly: each extra call costs a whole round-trip for a confirmation " +
			"the next one does not read. " +
			"Items are created INDEPENDENTLY and one failure does not stop the rest: the response reports " +
			"`created` and `failed` separately, each failure carrying the item's `index` so a retry can " +
			"resend exactly the ones that did not land. Duplicate detection still runs per item, so a 409 " +
			"DUPLICATE/CANDIDATES on one item is a normal, per-item outcome. " +
			"For a single wi use pf_create_work_item.",
		InputSchema: objectSchema(map[string]any{
			"project": prop("string", "Default project for every item. An item may override it with its own \"project\"."),
			"items":   batchWorkItemsProp(),
		}, []string{"project", "items"}),
	}, func(ctx context.Context, req *sdkmcp.CallToolRequest) (*sdkmcp.CallToolResult, error) {
		args, err := parseArgs(req.Params.Arguments)
		if err != nil {
			return errResult(err)
		}
		project := strArg(args, "project")
		if project == "" {
			return errResult(fmt.Errorf("project is required"))
		}
		raw, ok := args["items"].([]any)
		if !ok || len(raw) == 0 {
			return errResult(fmt.Errorf("items must be a non-empty array of work-item objects"))
		}
		// Cap the batch. Each item is a sequential HTTP call inside this one tool
		// call, with no partial-result flush and nothing visible to the caller
		// until it returns, so an unbounded array turns a single MCP call into an
		// arbitrarily long silent stall. Refused rather than truncated: silently
		// creating a prefix of what was asked for is worse than creating nothing.
		if len(raw) > maxBatchWorkItems {
			return errResult(fmt.Errorf("items has %d entries, more than the %d-item limit; split it into several calls (nothing was created)",
				len(raw), maxBatchWorkItems))
		}

		created := make([]any, 0, len(raw))
		failed := make([]map[string]any, 0)

		for i, entry := range raw {
			item, ok := entry.(map[string]any)
			if !ok {
				failed = append(failed, map[string]any{
					"index": i,
					"error": fmt.Sprintf("item is not an object (got %T)", entry),
				})
				continue
			}
			// Copy before mutating: the caller's array is decoded from their
			// arguments and defaulting in place would edit what they sent.
			item = cloneArgs(item)
			if strArg(item, "project") == "" {
				item["project"] = project
			}
			if strArg(item, "goal") == "" {
				failed = append(failed, map[string]any{"index": i, "error": "goal is required"})
				continue
			}
			applyForceReasonDefault(item)

			res, createErr := s.client.CreateWorkItem(ctx, item)
			if createErr != nil {
				// Reported, not returned: aborting here would leave the caller
				// knowing only that "the batch failed", with no way to tell which
				// items already exist — and re-sending the whole batch would then
				// trip dedup on the ones that did land.
				failed = append(failed, map[string]any{
					"index":   i,
					"goal":    strArg(item, "goal"),
					"project": strArg(item, "project"),
					"error":   createErr.Error(),
				})
				continue
			}
			// aihub#281, and this is the tool where it matters most: `created`
			// carries one whole record per item, so a 10-item batch echoes back
			// up to 10 bodies the caller sent in the very same call. Suppressed
			// against THIS item's arguments, not the batch's — item i's content
			// is only an echo of item i.
			suppressContentEcho(item, res)
			created = append(created, res)
		}

		return jsonResult(map[string]any{
			"ok":            len(failed) == 0,
			"created_count": len(created),
			"failed_count":  len(failed),
			"created":       created,
			"failed":        failed,
		})
	})

	// pf_list_work_items
	s.addTool(&sdkmcp.Tool{
		Name: "pf_list_work_items",
		// The second sentence is the only thing that tells the caller the response
		// is projected (aihub#278), and it is nine words for two reasons.
		//
		// Cost: a tool description sits in the prefix of EVERY request, so it is
		// a standing charge against a per-call saving — the same arithmetic that
		// made a `fields` PARAMETER not worth adding. An earlier draft here
		// enumerated the seven droppable fields and measured +220 B / ~86 tokens
		// per request, which is the same order as the ~100 that killed the
		// parameter. This one measures +70 B / ~27 tokens per request — about
		// 23k tokens a day at cache-read pricing, against ~462k saved on the
		// limit=200 calls alone, so it clears by ~20x where the enumeration
		// cleared by ~5x and the parameter did not clear at all.
		//
		// Correctness: the enumeration was also the more fragile of the two. It
		// restates listWorkItemNullMeansNone in prose, in a different file, with
		// nothing to keep them in step — a checked-in list of the droppable
		// fields would rot exactly as quietly as the response shape it describes.
		// Stating the INVARIANT cannot go stale, and it is what a caller needs:
		// not which keys may vanish, but what a vanished key means.
		//
		// The field list lives in docs/mcp-tools.md and the reasoning in
		// list_wi_slim.go, neither of which is charged to anybody.
		//
		// The aihub#360 tail is the caller-facing half of that work item's
		// deliverable ①: the single-vector blind spot and the second section
		// exist nowhere a tool caller can read except this string.
		//
		// 🔴 It used to carry "0/6 at every N, measured 2026-09-06" (aihub#367),
		// on the reasoning that a dated number states which conclusions a miss
		// cannot support while a bare "search is unreliable" only invites
		// recalibration. That reasoning still holds; the NUMBER did not, and
		// aihub#677 removed it rather than refresh it. Two reasons, and the
		// second is why nothing replaced it:
		//
		//   - It was measured through the serving defect aihub#648 found, so it
		//     never isolated the index shape it was cited for.
		//   - Its replacement argues the OPPOSITE. After aihub#650 repaired the
		//     serving and re-embedded, the same six frozen work-item queries read
		//     3/6 at @1 and 6/6 at @5 (aihub#660) — this family went from the
		//     worst of the three to 100% at @10. A number that good cannot be
		//     published as evidence that excerpt queries routinely miss.
		//
		// What survives is the MECHANISM, which is what this string now states:
		// one unchunked vector per row cannot be asked for a verbatim excerpt of
		// that row. The memory side still carries a measured residue (12 of 42
		// missing at @10, aihub#660) and lexical.go holds it; the wi side has no
		// such residue today, so it claims none.
		Description: "List work items with optional filters. " +
			"Item keys whose value is null are omitted: an absent key means null. " +
			"query= returns TWO sections (aihub#360): items[] (semantic when the server has an " +
			"embedding provider, and the `semantic` block says so; ILIKE text match otherwise) plus " +
			"`lexical`, a parallel verbatim-substring section (every whitespace token of the query, " +
			"case-insensitive, must appear in goal+content; its hits carry NO similarity). The index " +
			"is ONE unchunked vector per work item over goal+content only (attrs, labels and events " +
			"are never embedded), so an EXCERPT of a stored work item can fail to retrieve it " +
			"semantically. That is a property of the index shape, not a data gap: one unchunked " +
			"vector cannot be asked for a verbatim excerpt of the row it was built from. A " +
			"semantic miss is NOT evidence of absence: judge existence by the lexical section, whose " +
			"`total: 0` is explicit, or by ids=/filters.",
		InputSchema: listWorkItemsSchema(),
	}, func(ctx context.Context, req *sdkmcp.CallToolRequest) (*sdkmcp.CallToolResult, error) {
		args, err := parseArgs(req.Params.Arguments)
		if err != nil {
			return errResult(err)
		}
		params, err := buildListWorkItemsParams(args)
		if err != nil {
			return errResult(err)
		}
		result, err := s.client.ListWorkItems(ctx, params)
		if err != nil {
			return errResult(err)
		}
		// aihub#278: drop the keys whose value is null and which say nothing the
		// key's absence does not (content, plus six that mean "none").
		// Unconditional, and lossless by a per-value check rather than by
		// assertion — see the header of list_wi_slim.go for why it is a
		// delete-list and not a keep-list like slimRecallResult, and the closing
		// note there for why `seq` and `scenario` are NOT among them despite
		// passing the same rule.
		return jsonResult(slimListWorkItemsResult(result))
	})

	// pf_get_work_item
	s.addTool(&sdkmcp.Tool{
		Name:        "pf_get_work_item",
		Description: "Get a work item by ID or slug. Pass brief=true to omit the (potentially large) content field.",
		InputSchema: objectSchema(map[string]any{
			"work_item_id": prop("string", "Work item ID or slug"),
			"brief":        prop("boolean", "Omit the content field from the response (default false)"),
		}, []string{"work_item_id"}),
	}, func(ctx context.Context, req *sdkmcp.CallToolRequest) (*sdkmcp.CallToolResult, error) {
		args, err := parseArgs(req.Params.Arguments)
		if err != nil {
			return errResult(err)
		}
		id := strArg(args, "work_item_id")
		if id == "" {
			return errResult(fmt.Errorf("work_item_id is required"))
		}
		result, err := s.client.GetWorkItem(ctx, id)
		if err != nil {
			return errResult(err)
		}
		// brief=true drops the (potentially ~20K-char) content field; default
		// false preserves the current response shape for mixed-version safety (aihub#212).
		if boolArg(args, "brief") {
			delete(result, "content")
		}
		return jsonResult(result)
	})

	// pf_update_work_item
	//
	// aihub#495: the description below is the hop-1 half of aihub#440's
	// editability matrix, which shipped ENFORCED and unpublished. The matrix is
	// domain.wiEditTierByField and domain.updateGate; the card
	// (docs/mcp-cards/pf_update_work_item.md) and docs/mcp-tools.md both carry the
	// table, and neither is on the wire — hop 1 is the only thing a tool caller
	// ever sees, so a rule stated only there is a rule a caller meets by being
	// refused.
	//
	// 🔴 The last sentence is the one that had to be here rather than only in the
	// card. Every other clause describes a refusal that predates the change:
	// `content` has answered 409 CONFLICT_TERMINAL_STATE on a terminal work item
	// since long before aihub#440, and the contract tier's two codes are
	// cancelGate's. The five WORKING-tier fields are the batch's only 200 -> 409
	// transition — they had no status guard at all and were writable on a wrapped
	// work item — so they are the only part of this text a caller could hold a
	// correct-yesterday belief about. Naming them individually costs bytes and
	// buys the one thing a matrix summary cannot: which cell MOVED.
	//
	// Cost, on the same ledger as the per-parameter notes below: the tool
	// description goes 58 -> 1,058 bytes. Measured 2026-09-09 against the
	// aihub#419 budget in tools_list_payload_budget_test.go, which is a ceiling on
	// the whole tools/list payload plus a per-tool share: this tool was already
	// the payload's largest, the change moved it 6,796 B -> 7,839 B (the wi_type
	// note below is the other +55) and its share roughly 9% -> 10%, still well
	// under the 15% ceiling. The payload TOTAL those shares were taken against
	// is deliberately not restated here: it moves on every other tool's diff,
	// and the value this sentence used to pin had been moved twice by unrelated
	// wis when aihub#550 re-measured it — the aihub#493 class, where the only
	// thing a restated number can do is be wrong. The budget test prints the
	// current total and this tool's current share on its "tools/list:" log line:
	//
	//	GOWORK=off go test ./internal/mcp/ -run TestToolsListPayload -count=1 -v
	//
	// Bought deliberately, and the alternative was worse —
	// the same disclosure spread over five parameter descriptions repeats the
	// status classes five times and still cannot state the mixed-patch rule,
	// which is about the patch rather than about any one field.
	s.addTool(&sdkmcp.Tool{
		Name: "pf_update_work_item",
		Description: "Update a work item. Editability is ONE matrix over three field tiers, and the STRICTEST " +
			"tier the patch touches governs the whole patch: a patch that mixes tiers is refused whole, never " +
			"applied in part. " +
			"contract (goal, wi_type): only while status is queued, paused or blocked, and only for the " +
			"reporter, a project maintainer or an admin. " +
			"working (content, labels, priority, milestone, requires_human_session, declared_resources): any " +
			"non-terminal status. " +
			"record (attrs, attrs_patch, attrs_unset): EVERY status, including a wrapped work item; deliberate, " +
			"and load-bearing for post-wrap records. " +
			"A refusal names the KIND, not the field: 409 CONFLICT_WI_ALREADY_CLAIMED (running) or 409 " +
			"CONFLICT_TERMINAL_STATE (wrapped, failed, cancelled) for a wrong state, 403 FORBIDDEN for a wrong " +
			"caller. " +
			"NEW since aihub#440, and the only part of this that used to be otherwise: labels, priority, " +
			"milestone, requires_human_session and declared_resources had NO status guard and succeeded on a " +
			"terminal work item; they now answer 409 CONFLICT_TERMINAL_STATE there.",
		InputSchema: objectSchema(map[string]any{
			// No `kind` in this schema (aihub#383). It was published here and
			// forwarded below, but domain.UpdateWorkItemRequest has no `kind` json
			// tag and nothing on the server reads one, so the value died at c.Bind:
			// 200, wi_type untouched, no signal. Measured live on a RUNNING wi — had
			// it bound to wi_type, the status gate in domain.UpdateWorkItem would
			// have rejected the call. Withdrawing the promise is the fix; wiring
			// `kind` to wi_type instead would have opened a bypass around
			// reclassify_reason. objectSchema sets no additionalProperties:false,
			// so a caller that still sends `kind` is forwarded and ignored exactly
			// as before: this stops ADVERTISING the parameter, it does not make
			// sending it loud. pf_list_work_items' `kind` is a different parameter
			// (a deprecated alias for its wi_type FILTER) and stays. Class gate:
			// TestUpdateWorkItemPublishesOnlyParamsTheServerBinds.
			"work_item_id": prop("string", "Work item ID or slug"),
			// aihub#440: the status set widened to include `blocked` when the
			// per-field guards became one editability matrix — a blocked wi has no
			// live attempt, which is the same argument cancelGate already accepts
			// for the far more destructive cancel. domain.wiEditTierByField holds
			// the matrix. +10 bytes of always-resident schema, budgeted against the
			// resources_version note below.
			// aihub#474: the two shape constraints now hold on BOTH write paths, so
			// this description states both — it used to state neither, while
			// pf_create_work_item's stated both, and a caller reading the pair
			// reasonably concluded the cap was create-only. The number comes from
			// domain.MaxWorkItemGoalRunes() rather than being retyped: a published
			// limit and the enforced limit that drift apart are how aihub#433 got a
			// 500 out of an in-range value. "Single-line" is the same word
			// pf_create_work_item uses for the same refusal (ErrGoalMultiline),
			// which this path has always applied and never published.
			//
			// Cost, on the same ledger as the +10 above: 55 -> 72 bytes of
			// always-resident schema, +17. Bought deliberately. The alternative
			// this tool spent a wave discovering is a caller that reads the create
			// tool's cap, sends the same string here, and gets a 500 from a
			// constraint name it has no way to map back to a field.
			//
			// aihub#507 adds "non-empty", and it is a promise this tool did not
			// keep until the same work item made it true: `goal: ""` used to be
			// STORED here — 200, work item saved, goal blank in every list — while
			// pf_create_work_item answered the identical value with 400 "goal is
			// required". Both paths now refuse it from one function
			// (domain.validateWorkItemGoalPresent). The word is the caller-facing
			// half of that: a refusal nobody is told about is discovered by being
			// hit, and this one is newly reachable, so it is the description's job
			// to arrive before the 400 does. Two words rather than a sentence
			// because "empty" and "required" are the same fact and the message
			// already says the other one.
			//
			// Cost, same ledger again: 72 -> 82 bytes, +10.
			//
			// aihub#520 settled the question this comment used to defer, which was
			// whether the create side should say it too. It does: the word is in
			// workItemFieldProps' goal description now, so all three tools that
			// publish `goal` state it. What made it a separate change is that the
			// shared string moves the input_schema_sha256 of pf_create_work_item
			// and pf_batch_create_work_items — K3 in contract_cards_gate_test.go is
			// what tells you — so those two cards had to be in its file scope.
			// The gate is no longer named on this tool:
			// TestPublishedGoalCapIsTheEnforcedOne quantifies "non-empty" over
			// every tool publishing `goal`, which is where a fourth one lands too.
			"goal": prop("string", fmt.Sprintf(
				"Single-line non-empty goal ≤%d chars (status must be queued, paused or blocked)",
				domain.MaxWorkItemGoalRunes())),
			"goal_change_reason": prop("string", "Reason for goal change (required with goal)"),
			"priority":           propEnum("string", "Updated priority", domain.WorkItemPriorityList()),
			"milestone":          prop("string", "Updated milestone"),
			// aihub#495: `wi_type` sits in the SAME tier as `goal`, under the same
			// predicate, in the same map entry pair (domain.wiEditTierByField), and
			// aihub#440 widened the status set for both at once. `goal`'s description
			// was rewritten to say so and this one was not, so the pair published two
			// different contracts for one rule — which is the aihub#474 failure mode
			// the other way round: there, a caller read pf_create_work_item's `goal`
			// and inferred the cap was create-only; here, a caller reads `goal` and
			// has no reason to think `wi_type` shares its gate.
			//
			// Same words as `goal`'s parenthetical, on purpose. The status class is
			// one fact (domain.wiStatusOpen) and two spellings of it would be two
			// things to keep in step.
			"wi_type":                prop("string", "Updated wi_type (status must be queued, paused or blocked)"),
			"requires_human_session": prop("boolean", requiresHumanSessionUpdateDescription),
			"reclassify_reason":      prop("string", "Reason for wi_type change (min 10 chars)"),
			"labels":                 prop("array", fmt.Sprintf("Updated labels (max %d)", domain.MaxWorkItemLabels())),
			"declared_resources":     declaredResourcesProp("Updated declared resources"),
			// aihub#337, mirroring aihub#260 on pf_update_project's members_version:
			// omitting it is still the behaviour, so it is still stated — but it is no
			// longer offered as an option, because callers act on the options a tool
			// description lists and this one is never the right choice. It also names
			// the tool that returns the token, for the aihub#260 reason: a guard whose
			// input nobody can find is a guard nobody passes.
			//
			// ⚠️ LENGTH IS A REAL COST — this string ships in every tools/list
			// response. Measured: 325 -> 427 characters, +102, against the +90 the
			// members_version rewrite spent. Budget any further edit against that.
			"resources_version": prop("integer", "Compare-and-set guard for declared_resources: ALWAYS send the resources_version pf_get_work_item returned. The update is applied only if it still matches, otherwise it fails with 409 CONFLICT_CAS_FAILED and reports the current version. Every write of declared_resources increments this counter. Leaving it out overwrites unconditionally: a concurrent writer's list is silently discarded, locks and all, and you still get a 200."),
			"attrs":             prop("object", "REPLACES the whole attrs object: every key you do not resend is DELETED. Use it only when you intend to overwrite attrs wholesale (e.g. after reading the current value). To add or change keys without destroying the others, use attrs_patch. Cannot be combined with attrs_patch/attrs_unset."+jsonObjectPropNote),
			"attrs_patch":       prop("object", "Merge these keys into attrs, leaving every other key untouched (aihub#288). Shallow: a top-level key in the patch replaces that key's stored value outright, it is NOT merged into it recursively, and null STORES a JSON null rather than deleting. To delete keys use attrs_unset. Cannot be combined with attrs."+jsonObjectPropNote),
			"attrs_unset":       prop("array", "Top-level attrs keys to delete (array of strings). Applied AFTER attrs_patch, so a key in both ends up deleted. Cannot be combined with attrs."),
			"content":           prop("string", contentPropDescription),
			// aihub#281. The echo suppression above needs no flag because it only
			// removes bytes the caller sent. THIS case is different and genuinely
			// lossy: an update that touches nothing but attrs or priority still
			// gets the whole body back (~80% of that response), and for a caller
			// that has not read the wi that body is new information rather than an
			// echo. So it is opt-in, default false — the same mixed-version
			// reasoning that gave pf_get_work_item its `brief` in aihub#212,
			// reused rather than reversed.
			// The wording is exact on both halves because both were wrong once. It
			// does not "omit the content field": a work item with no body keeps
			// its content: null. And it is NOT the same as pf_get_work_item's
			// brief, which deletes content and reports no length — a caller told
			// the two were equivalent would apply this tool's "no content_len
			// means no body" rule to that one's reply and conclude a work item
			// with a 4 KB body was empty.
			"brief": prop("boolean", "Replace the content body with content_len (bytes stored); default false. "+
				"A wi that HAS no body is unaffected; it comes back as content: null with no content_len, so a "+
				"missing content_len here means \"this wi has no body\", never \"the body was withheld\". "+
				"NOT the same as pf_get_work_item's brief, which deletes content outright and reports no length. "+
				"Content you send in THIS call is never echoed back regardless of this flag."),
		}, []string{"work_item_id"}),
	}, func(ctx context.Context, req *sdkmcp.CallToolRequest) (*sdkmcp.CallToolResult, error) {
		args, err := parseArgs(req.Params.Arguments)
		if err != nil {
			return errResult(err)
		}
		id := strArg(args, "work_item_id")
		if id == "" {
			return errResult(fmt.Errorf("work_item_id is required"))
		}
		// aihub#241: resources_version is an INT column and *int on the wire.
		// Coerce before building the body so a quoted "0" from a mixed-version
		// client becomes a JSON number here, instead of failing c.Bind two
		// layers away as an opaque 400 "invalid request body".
		if err := normalizeIntArg(args, "resources_version"); err != nil {
			return errResult(err)
		}
		// Remove work_item_id from body. `brief` goes with it (aihub#281): it
		// shapes THIS process's reply and means nothing to the server, and
		// forwarding a field the peer does not bind is how aihub#290's
		// expected_version became a parameter that travelled the whole way and
		// was discarded in silence.
		body := make(map[string]any)
		for k, v := range args {
			if k != "work_item_id" && k != "brief" {
				body[k] = v
			}
		}
		result, err := s.client.UpdateWorkItem(ctx, id, body)
		if err != nil {
			return errResult(err)
		}
		// aihub#281. Order matters only in that brief is the wider rule: it drops
		// the content whether or not this call sent one, so checking it first
		// keeps the two paths from having to agree about the overlap.
		if boolArg(args, "brief") {
			dropContentEcho(result)
		} else {
			suppressContentEcho(args, result)
		}
		return jsonResult(result)
	})

	// pf_claim_work_item
	s.addTool(&sdkmcp.Tool{
		Name: "pf_claim_work_item",
		Description: "Claim a work item: creates a new run_attempt with typed locks and writes the state " +
			"file every later credential-checked pf_* call authenticates with. Retry-safe: resending the " +
			"same idempotency_key returns the first call's attempt and keeps the session_secret it is " +
			"bound to (aihub#392).",
		InputSchema: objectSchema(map[string]any{
			"work_item_id": prop("string", "Work item ID or slug"),
			"idempotency_key": prop("string", "Idempotency key for DB dedup on run_attempts: a BODY "+
				"parameter, NOT the HTTP Idempotency-Key header, which the client now mints per request "+
				"on its own (aihub#436); different guarantees, and the design requires both. "+
				"Resending a key returns the "+
				"EXISTING attempt, and this process reuses the session_secret it recorded for that key so "+
				"the credential stays valid (aihub#392: it used to mint a new one, and every later call "+
				"then answered 'invalid session_secret'). CAUTION: only works where that record exists: "+
				"replaying a key from another machine, or after the state file was deleted, is still left "+
				"unauthenticated. Send a NEW key unless retrying a call whose response you never saw."),
			"requested_locks": requestedLocksProp("Resource locks to acquire"),
			// aihub#410's "…except in a narrow commit-window race" used to end this
			// string. It was inherited from a note written about the OTHER tool and
			// is not true here, and aihub#430 settled that by measurement rather
			// than by reading: claim_takeover_commit_window_db_test.go builds the
			// exact interleaving the qualifier describes — a foreign lock row
			// committed inside this claim's window, after its snapshot and while
			// its upsert is blocked on the duplicate key — and the claim comes back
			//
			//	409 CONFLICT_SERIALIZATION_FAILURE (SQLSTATE 40001), row unchanged
			//
			// because FnClaimWorkItem opens SERIALIZABLE (run_attempts.go) while
			// FnForceTakeover opens a bare pool.Begin — no isolation pinned, the
			// pool/DSN default, read committed as deployed
			// (txn_isolation_probe_test.go pins the split). At SERIALIZABLE
			// a stale read is REPORTED, so the two ways this statement could move a
			// foreign row are: the aihub#393 predicate passes, which means the owner
			// has ended and the displacement is the point, or the read was stale,
			// which is a 40001 the caller is told to retry. Neither is silent.
			//
			// ⚠️ The qualifier therefore stays on pf_force_takeover's own
			// description, whose path is the unpinned pool.Begin one, and this site
			// must NOT copy it back: the guarantee differs because the transaction
			// shape differs, and one sentence cannot be true of both. What that test
			// does NOT claim is that no interleaving whatsoever can displace a row on
			// this path — it measures the one the qualifier described. The full
			// analysis of the commit-window gap the unpinned default leaves open is
			// still in internal/domain/resource_events.go above lockUpsertSQL.
			"force_takeover": prop("boolean", "Force takeover if already claimed. NOTE: it takes over the WORK "+
				"ITEM, not other people's locks: a lock held by a running or paused attempt of a "+
				"DIFFERENT work item still answers 409 CONFLICT_LOCK_TAKEN and does not change hands "+
				"(aihub#393). It reclaims this work item's own locks, and rows whose owning attempt has "+
				"ended. No flag displaces another work item's lock."),
			"scenario_ref": prop("string", "Git SHA of local scenario clone at claim time (optional)"),
		}, []string{"work_item_id", "idempotency_key"}),
	}, func(ctx context.Context, req *sdkmcp.CallToolRequest) (*sdkmcp.CallToolResult, error) {
		args, err := parseArgs(req.Params.Arguments)
		if err != nil {
			return errResult(err)
		}

		// aihub#667: everything past the argument parsing is internal/lifecycle's.
		// It used to be ~400 lines of handler body plus six unexported functions
		// here, reachable only over MCP stdio, which is why `polyforge drain`
		// (aihub#640) could not claim at all and ended FAILED rather than
		// reimplementing it. Same code, two callers, so the two harnesses cannot
		// drift apart on session_secret replay, branch naming or worktree adoption.
		lreq := lifecycle.ClaimRequest{
			WorkItemID:     strArg(args, "work_item_id"),
			IdempotencyKey: strArg(args, "idempotency_key"),
			ForceTakeover:  boolArg(args, "force_takeover"),
			ScenarioRef:    strArg(args, "scenario_ref"),
		}
		// Presence, not emptiness: see ClaimRequest.RequestedLocksSet. An explicit
		// null is a value the server is meant to see.
		lreq.RequestedLocks, lreq.RequestedLocksSet = args["requested_locks"]

		res, claimErr := lifecycle.Claim(ctx, s.client, s.cfg, lreq)
		if claimErr != nil {
			// Don't delete the partial state file — let the user retry.
			return errResult(claimErr)
		}
		sf := res.State
		// The claim response the model sees: everything the server sent, MINUS the
		// keys claim_response_slim.go names and gives a reason for.
		//
		// ⚠️ It used to be the other way round — a fresh map plus a copy loop over
		// six named keys — and that keep-list dropped `requires_human_session`,
		// `wi_type`, `id` and `step_recovery_hint` in silence, the first being the
		// field the post-claim routing rule branches on (aihub#388). It had also
		// rotted the other way, faithfully copying `expires_at`, which v1.21
		// removed and which the pf_force_takeover handler below says not to
		// surface. aihub#238's note about `unrecognized_resources` needing to stay
		// in that list is now structural rather than remembered: nothing is copied,
		// so nothing can be forgotten. Do not reintroduce a copy loop here — see
		// that file's header for the four instances of this pattern that preceded
		// it, and TestClaimResultPassesThroughAFieldTheStructDoesNotHaveYet, which
		// no keep-list can pass however complete it is today.
		safeResult := slimClaimResult(res.Response)
		// Asserted by this handler rather than relayed: `ok` because reaching this
		// line IS the success, and the other two from the state file just written,
		// since they are what every later credential-checked pf_* call
		// authenticates with.
		safeResult["ok"] = true
		safeResult["attempt_id"] = sf.AttemptID
		safeResult["claim_epoch"] = sf.ClaimEpoch
		addWorktrees(safeResult, sf.Worktrees)
		if len(res.RepoPins) > 0 {
			safeResult["repo_pins"] = res.RepoPins
		}
		// aihub#328: a rejected directory has to reach the caller, not just stderr.
		// The claim itself succeeded, so this is a warning on an ok:true response
		// rather than an error — but without it the agent proceeds believing it has
		// a worktree it does not have, which is the same blindness in a new place.
		if len(res.WorktreeProblems) > 0 {
			safeResult["worktree_problems"] = res.WorktreeProblems
		}
		return jsonResult(safeResult)
	})

	// pf_complete_attempt
	//
	// `note` (aihub#290) exists because the closing note and the terminal call
	// were always two round-trips in a fixed order — 201 measured adjacent pairs,
	// 0.325% of billed input — and the second one read nothing out of the first.
	// The ordering was not incidental: the terminal call deletes the state file,
	// so pf_emit_event AFTER it cannot authenticate, and every skill that emits a
	// wrap note carries a warning saying so. Folding the note into this call
	// removes both the round-trip and the ordering hazard.
	s.addTool(&sdkmcp.Tool{
		Name: "pf_complete_attempt",
		Description: "Complete the current run attempt (wrapped|failed|paused). Deletes state file for terminal statuses. " +
			"Wrapping requires `derived`, the disposition list for findings this attempt noticed but did not fix; " +
			"a wrap that omits it is refused. " +
			"Pass `note` to record the closing note in the same call instead of emitting it with a separate " +
			"pf_emit_event beforehand, which is the only order that works, since this call deletes the " +
			"credentials pf_emit_event needs. The response's note_emitted says whether it landed.",
		InputSchema: objectSchema(map[string]any{
			"work_item_id":         prop("string", "Work item ID (used to find state file)"),
			"status":               prop("string", "wrapped|failed|paused"),
			"force_terminate_step": prop("boolean", "Force terminate in-progress step"),
			"note":                 prop("string", "Closing note recorded as a `note` event before the attempt is completed (e.g. \"wrapped: <one sentence>\" / \"failed reason: <why>\"). Replaces a separate pf_emit_event call."),
			"pause_reason":         prop("string", "Why the attempt is being paused. Read only when status=\"paused\" (sending one with any other status is refused, not ignored) and recorded on the attempt row and in the attempt_completed event, unlike `note`, which becomes its own timeline event whatever the status."),
			"derived": prop("array", "One disposition per finding this attempt noticed but did not fix. Required when "+
				"status=\"wrapped\" and refused if omitted; found nothing, send [] explicitly. Entries: \"folded\" or "+
				"\"folded:<text>\" (kept in this wi's record - the default, and deliberately the cheapest), "+
				"\"filed:<wi id or slug>\" (a new wi you really opened; the ref must resolve or the wrap is refused), "+
				"\"dropped:<reason>\" (judged not worth tracking; the reason is required). Recorded on the attempt row "+
				"and in the attempt_completed event; only a wrap records it. The server cannot check the list against "+
				"the note's prose, so its honesty is yours."),
			"no_steps_reason": prop("string", "Escape hatch for the no-steps-recorded gate (aihub#684): read only when "+
				"status=\"wrapped\" (sending one with any other status is refused, not ignored). Required and must be "+
				"non-empty ONLY when this work item has a commit/push/pr_opened event but never opened a single step "+
				"(wi_step_state.version==0): the wrap is otherwise refused 409 CONFLICT_NO_STEPS_RECORDED. State why no "+
				"step was opened. A whitespace-only value is treated exactly like an absent one - it will not satisfy the "+
				"gate. Recorded on the attempt row and in the attempt_completed event even when the gate never fired, same "+
				"as `derived`. A work item that produced no code at all is not gated and needs no reason."),
		}, []string{"work_item_id", "status"}),
	}, func(ctx context.Context, req *sdkmcp.CallToolRequest) (*sdkmcp.CallToolResult, error) {
		args, err := parseArgs(req.Params.Arguments)
		if err != nil {
			return errResult(err)
		}
		wiID := strArg(args, "work_item_id")
		if wiID == "" {
			return errResult(fmt.Errorf("work_item_id is required"))
		}
		status := strArg(args, "status")
		if status == "" {
			return errResult(fmt.Errorf("status is required"))
		}
		pauseReason := strArg(args, "pause_reason")
		// aihub#452: refuse the combination the schema says is not read, and refuse
		// it HERE — before the state file is resolved and, more importantly, before
		// the note is emitted below. A refusal that fires after that point has
		// already written a timeline event for a call it then rejects, which is a
		// worse contract than the one being fixed.
		//
		// The alternative — dropping the field quietly — was rejected: it is the
		// same defect wearing the opposite sign, a caller stating a reason and
		// nothing anywhere recording it or saying why. Naming both fields is the
		// point, since the caller cannot see which of the two the server objected
		// to. `note` is offered because it is the field that does what the caller
		// was reaching for.
		//
		// Narrow on purpose: only a NON-EMPTY reason on a non-paused status is
		// refused. pause_reason remains optional on paused — the converse rule
		// ("paused must carry a reason") would reject legitimate pauses, and this
		// tool is the one every executor ends its run with.
		if pauseReason != "" && status != "paused" {
			return errResult(fmt.Errorf(
				"pause_reason is read only when status=\"paused\", but status=%q was sent with one; "+
					"drop pause_reason or use note, which is recorded on every status", status))
		}

		// aihub#684: same posture as pause_reason immediately above, and for the
		// same reason — this half of the check needs no database, so refusing it
		// here spares the note emitted below. The DB-dependent half (commit
		// events + version==0 => refused) CANNOT be hop-2-guarded: it needs two
		// reads FnCompleteAttempt already does inside its transaction, so it is
		// left to the server and the note ahead of THAT specific refusal is an
		// accepted, documented cost (same shape as pf_wrap's own cost below).
		noStepsReason := strArg(args, "no_steps_reason")
		if noStepsReason != "" && status != "wrapped" {
			return errResult(fmt.Errorf(
				"no_steps_reason is read only when status=\"wrapped\", but status=%q was sent with one; "+
					"drop it or use note, which is recorded on every status", status))
		}

		// aihub#350: the derived checks a refusal must not follow a side effect
		// for, raised HERE - before the state file is resolved and before the
		// note below is emitted (the aihub#452 placement, for the same reason: a
		// refusal that fires after the note has written a timeline event for a
		// call it then rejects). Two checks, not three, and the asymmetry is
		// deliberate: a wrap with derived ABSENT or MALFORMED is refused at this
		// hop, because every pre-aihub#350 wrap call looks exactly like the first
		// and the fix is one argument away; but a non-empty list on a non-wrapped
		// status is left for the server to refuse, because a hop-2 refusal keyed
		// on the status/derived combination makes this tool's other parameters
		// unmeasurable to the aihub#419 G1 probe, whose status value is pinned to
		// "paused" (see semanticValuesByTool) while derived travels only
		// meaningfully on "wrapped". The server's guard is the authority either
		// way; what moves between hops is only which refusal spares the note.
		//
		// The shape check is domain.ValidateDerived - the server's own function,
		// imported rather than restated, so the two hops cannot drift.
		if err := normalizeStringSliceArg(args, "derived"); err != nil {
			return errResult(err)
		}
		derived, derivedPresent := args["derived"].([]string)
		if status == "wrapped" {
			if !derivedPresent {
				return errResult(fmt.Errorf(
					"derived is required to wrap: list a disposition per finding this attempt noticed " +
						"but did not fix - \"folded\" (kept in this wi's record; the default), " +
						"\"filed:<wi id or slug>\" (a new wi you really opened), or \"dropped:<reason>\". " +
						"Found nothing? Send derived: [] explicitly - an omitted list is refused because " +
						"it cannot be told apart from findings nobody dispositioned"))
			}
			if aerr := domain.ValidateDerived(derived); aerr != nil {
				return errResult(aerr)
			}
		}

		sf, err := config.ResolveStateFile(wiID)
		if err != nil {
			return errResult(config.StateFileMissingErr(wiID, err))
		}

		// Emit the note FIRST — the CompleteAttempt below deletes the state file
		// for terminal statuses, and with it the credentials this needs. Hold the
		// error and report it on the response rather than aborting: a note that
		// failed to record must not cost the caller its wrap.
		note := strArg(args, "note")
		var noteErr error
		if note != "" {
			noteErr = s.emitNote(ctx, wiID, sf, note)
		}

		body := map[string]any{
			"status":         status,
			"attempt_id":     sf.AttemptID,
			"claim_epoch":    sf.ClaimEpoch,
			"session_secret": sf.SessionSecret,
		}
		if boolArg(args, "force_terminate_step") {
			body["force_terminate_step"] = true
		}
		// Sent only when the caller supplied one (aihub#424), and by now only on
		// status=paused (aihub#452 refused the rest above). The non-emptiness half
		// still earns its keep on the paused path: FnCompleteAttempt writes whatever
		// it is given, so an unguarded assignment would send "" and turn "paused,
		// reason not given" into "paused for no stated reason".
		if pauseReason != "" {
			body["pause_reason"] = pauseReason
		}
		// aihub#350: forwarded whenever the caller supplied it, INCLUDING empty -
		// [] is the explicit "no findings" declaration and dropping it here would
		// turn every honest empty declaration into the very omission the server
		// refuses. Presence-gated so a call that never mentioned derived sends no
		// key, keeping absent and [] distinguishable at hop 3, which is the whole
		// nil-versus-empty contract of CompleteAttemptRequest.Derived.
		if derivedPresent {
			body["derived"] = derived
		}
		// aihub#684: presence-gating, same style as pause_reason — only on
		// status=wrapped by now (the guard above refused the rest), and only
		// when the caller actually sent one.
		if noStepsReason != "" {
			body["no_steps_reason"] = noStepsReason
		}

		result, err := s.client.CompleteAttempt(ctx, wiID, body)
		if err != nil {
			// Carry the note's fate into the error: the caller is about to decide
			// whether to retry, and retrying re-sends the note.
			return errResult(fmt.Errorf("%w%s", err, noteOutcomeSuffix(note != "", noteErr)))
		}
		applyNoteResult(result, note != "", noteErr)

		// Surface the worktree paths from the state file we're about to delete,
		// for all statuses, so the caller doesn't need to have read the state
		// file itself before calling pf_complete_attempt (aihub#207).
		addWorktrees(result, sf.Worktrees)

		// Delete state file for terminal statuses; keep for paused. Delete by the
		// resolved canonical key (sf.WIID), and best-effort the passed key too, so
		// a slug-addressed completion cleans any stale slug-keyed stub. (aihub#141)
		if status == "wrapped" || status == "failed" {
			_ = config.DeleteStateFile(sf.WIID)
			if wiID != sf.WIID {
				_ = config.DeleteStateFile(wiID)
			}
		}

		return jsonResult(result)
	})

	// pf_force_takeover
	s.addTool(&sdkmcp.Tool{
		Name: "pf_force_takeover",
		// aihub#410's commit-window qualifier lives HERE and only here (aihub#430).
		// It used to be on pf_claim_work_item's force_takeover prop as well, on the
		// argument that "these are two published statements of one guarantee" —
		// which was wrong: the two tools reach lockUpsertSQL through different
		// transaction shapes — one pinned SERIALIZABLE, one unpinned — so it is
		// one statement about two different guarantees. This
		// handler's path is FnForceTakeover's bare pool.Begin (no isolation
		// pinned — the pool/DSN default, read committed as deployed), where
		// the gap resource_events.go documents is reachable; the claim tool's is
		// SERIALIZABLE, where aihub#430 measured the same interleaving coming back
		// as a retryable 409 with the row untouched. Do not re-add it there.
		//
		// aihub#451 finished the job that deletion started: the qualifier that
		// STAYED here now has a test under it — force_takeover_commit_window_db_test.go
		// in internal/domain. aihub#430 had also probed READ COMMITTED and got a
		// refusal, which reads like "nothing can reach this gap"; but that probe
		// committed only the LOCK ROW inside the window. Commit the foreign
		// ATTEMPT row inside it too — which is exactly what a claim in flight
		// holds, since FnClaimWorkItem writes both in one transaction — and the
		// takeover succeeds, rewrites a live foreign holder's row, and tells
		// neither side. So this clause is not a hedge and not a leftover: delete
		// it only in the change that closes the gap.
		Description: "Force-take ownership of a work item from another agent. NOTE: it takes over the WORK " +
			"ITEM, not other people's locks: a lock held by a running or paused attempt of a DIFFERENT " +
			"work item still answers 409 CONFLICT_LOCK_TAKEN and does not change hands (aihub#393). It " +
			"reclaims this work item's own locks, and rows whose owning attempt has ended. No flag " +
			"displaces another work item's lock, except in a narrow commit-window race (aihub#410).",
		InputSchema: objectSchema(map[string]any{
			"work_item_id": prop("string", "Work item ID or slug"),
			"reason":       prop("string", "Reason for force takeover"),
		}, []string{"work_item_id", "reason"}),
	}, func(ctx context.Context, req *sdkmcp.CallToolRequest) (*sdkmcp.CallToolResult, error) {
		args, err := parseArgs(req.Params.Arguments)
		if err != nil {
			return errResult(err)
		}
		id := strArg(args, "work_item_id")
		if id == "" {
			return errResult(fmt.Errorf("work_item_id is required"))
		}

		// Generate new session_secret for the forced takeover
		sessionSecret, err := lifecycle.GenerateSessionSecret()
		if err != nil {
			return errResult(fmt.Errorf("generate session_secret: %w", err))
		}

		machineID := os.Getenv("POLYFORGE_MACHINE_ID")
		if machineID == "" {
			h, _ := os.Hostname()
			machineID = h
		}
		body := map[string]any{
			"reason": strArg(args, "reason"),
			"session_info": map[string]any{
				"session_secret": sessionSecret,
				"machine_id":     machineID,
			},
		}
		result, err := s.client.ForceTakeover(ctx, id, body)
		if err != nil {
			return errResult(err)
		}

		// Write state file with new credentials. Key by the canonical work_items.id
		// the server returns (the input id may be a slug like "aihub#1"); persisting
		// the slug would write a state file with an empty Slug that
		// ResolveStateFile's slug-scan can never match, so a later canonical-id
		// update would miss it. Populate Slug/Project too — mirror
		// pf_claim_work_item. (aihub#149)
		canonicalWIID := id
		if v, ok := result["id"].(string); ok && v != "" {
			canonicalWIID = v
		}
		sf := &config.StateFile{
			WIID:          canonicalWIID,
			SessionSecret: sessionSecret,
			Claimed:       true,
			ClaimedAt:     time.Now().UTC().Format(time.RFC3339),
		}
		if v, ok := result["new_attempt_id"].(string); ok {
			sf.AttemptID = v
		}
		if v, ok := result["new_claim_epoch"]; ok {
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
		// Carry the worktree map over from whatever state file this machine already
		// holds for the wi. Nothing in the takeover response carries it — only
		// pf_claim_work_item ever creates worktrees — and this write now REPLACES
		// the canonical-keyed file rather than sitting beside it under the slug, so
		// building sf from scratch would destroy the map a prior claim recorded.
		// The next pf_ship / pf_diff / pf_commit / pf_push / pf_pr would then find
		// no worktrees map, and with no workspace_root argument to reconstruct a
		// path from would fail outright. (aihub#319)
		//
		// Keyed on canonicalWIID rather than the caller-supplied id so it also
		// finds the map when the takeover was addressed by slug; ResolveStateFile
		// rather than ReadStateFile so it still finds it when an old server did not
		// echo `id` and canonicalWIID is therefore itself a slug.
		if prior, priorErr := config.ResolveStateFile(canonicalWIID); priorErr == nil && len(prior.Worktrees) > 0 {
			sf.Worktrees = prior.Worktrees
		}
		// Persist the canonical-keyed state file and remove any orphan slug stub a
		// prior slug-keyed write left behind, mirroring claim's WriteClaimState.
		if err := config.WriteClaimState(id, canonicalWIID, sf); err != nil {
			// aihub#323, the other half of the same defect — see the claim handler
			// above for why the error is returned rather than swallowed.
			//
			// ForceTakeover's tx.Commit ran before this line, so by now the prior
			// attempt is 'superseded', its resource_locks are deleted, a
			// force_takeover event is on the timeline, a new running attempt exists
			// carrying THIS caller's secret hash, and current_attempt_id/epoch have
			// advanced. "write state file: ..." alone reads as "the takeover failed",
			// which is the one reading under which the previous holder is dead and
			// nobody knows it.
			//
			// Re-running the same call is safe, read off internal/domain/
			// run_attempts.go rather than assumed: ForceTakeover requires
			// wi.Status == "running" (true — this takeover made it so) and admits
			// isSelf, which the caller now is because it owns the current attempt.
			// Its idempotency_key is synthesised server-side per attempt, so unlike
			// pf_claim_work_item there is no replay branch to fall into.
			prior, _ := result["prior_actor_display"].(string)
			if prior == "" {
				prior = "the previous holder"
			}
			// The "only in memory" clause IS true on this path — unlike the claim
			// handler, which persists the secret before calling the server, this one
			// generates it at :1023 and writes it nowhere until the line above.
			return errResult(fmt.Errorf("write state file: %w"+
				" (NOT A NO-OP: the takeover ALREADY SUCCEEDED on the server)."+
				" %s has been evicted, and attempt %s (epoch %d) is running under your name holding whatever locks this work item declares;"+
				" only this machine's local record of it failed, and the session_secret it needed lived in memory alone and is now gone."+
				" RECOVERY: re-run this exact pf_force_takeover call; you now own the current attempt, so the server admits it as a self-takeover."+
				" It is not destructive: it costs one epoch bump, one superseded attempt and one timeline event."+
				" %s",
				err, prior, sf.AttemptID, sf.ClaimEpoch, lifecycle.StateWriteFilesystemAdvice))
		}

		// The takeover response the model sees: everything the server sent, MINUS
		// the keys force_takeover_response_slim.go names and gives a reason for.
		//
		// ⚠️ It used to be the other way round — a fresh map holding five named keys
		// — and that keep-list dropped `id`, `slug` and `project` in silence
		// (aihub#422). Those are the three aihub#149 added to
		// domain.ForceTakeoverResponse so that a SLUG-addressed takeover could still
		// key its state file canonically; this handler reads all three itself, above,
		// and then told the model none of them — so the answer withheld the identity
		// this very call had just established, and the only surviving copy was in a
		// state file the model does not read. Do not reintroduce a copy loop here —
		// see that file's header for the five instances of this pattern that
		// preceded it, and
		// TestForceTakeoverResultPassesThroughAFieldTheStructDoesNotHaveYet, which no
		// keep-list can pass however complete it is today.
		//
		// v1.21 ownership-only: `expires_at` is not a field of ForceTakeoverResponse
		// and this handler never surfaced one. Under a delete-list that needs no
		// entry: a key the response does not carry cannot be forwarded, and naming it
		// here would be the same rot that left `expires_at` in the old CLAIM
		// keep-list — a projection faithfully maintaining a field that does not exist.
		safeResult := slimForceTakeoverResult(result)
		// Asserted by this handler rather than relayed: these two come from the state
		// file just written, and they are what every later credential-checked pf_*
		// call authenticates with. Relaying the server's values instead would report
		// a credential this machine does not hold whenever the two disagree — an
		// epoch the type switch above cannot parse leaves sf.ClaimEpoch at 0 while
		// the wire still says something else. `ok` stays relayed: on this route the
		// server's own OK field is the only statement of success.
		safeResult["new_attempt_id"] = sf.AttemptID
		safeResult["new_claim_epoch"] = sf.ClaimEpoch
		return jsonResult(safeResult)
	})

	// pf_get_ready_queue
	//
	// ⚠️ `non_conflicting` was published here, and forwarded as
	// `?non_conflicting=true`, from the day this tool was added (`50bfc35`) until
	// aihub#387 withdrew it. NOTHING ever read it: handleGetReadyQueue reads
	// `project` and `max` only, and domain.GetReadyQueue(ctx, pool, project, max)
	// has no such argument — measured 2026-09-07, `git grep non_conflicting --
	// internal/server internal/domain pkg cmd` returned 0 hits. Passing it
	// returned the ordinary ready queue with no error and no warning.
	//
	// It is GONE rather than implemented, by the owner's decision (plan B,
	// 2026-09-07): "non-conflicting" has no agreed definition here — predicted
	// from declared_resources, or read off the resource_locks actually held? —
	// and this repo has measured pf_predict_conflicts to be untrustworthy in both
	// directions (it reports an attempt's OWN locks as conflicts after a claim,
	// and false-negatives on read intent), so building on it would have produced
	// a second untrustworthy predicate. Whether the capability is wanted at all
	// is aihub#186's call; that design was written on top of this switch while it
	// did nothing, which is the cost that made this a bug and not a gap.
	//
	// 🔴 Do not re-add the parameter without a hop-3 reader to go with it:
	// TestReadyQueueEveryPublishedParamIsReadByTheHandler
	// (ready_queue_param_wiring_test.go) fails on any param this schema publishes
	// that handleGetReadyQueue does not read.
	//
	// 🔴 The section COUNT in the description below is one of three copies of the
	// same fact — the others are domain.ReadyQueue's field list and the Ready
	// Queue block in docs/design/polyforge-v1-design.md — and all three said
	// something different until aihub#449 (aihub#411 T2-20): this string said
	// "6-section", the struct had seven keys with `omitempty` on the last, and the
	// doc drew six plus three fields no code could produce. Adding a segment means
	// editing all three in one change; TestReadyQueueSectionCountIsOneNumber
	// (ready_queue_section_count_test.go) fails when this string and the struct
	// disagree.
	s.addTool(&sdkmcp.Tool{
		Name: "pf_get_ready_queue",
		Description: "Get the LCRS (7-section) ready queue for a project. Every section is " +
			"always present; an empty one is an empty list, never a missing key. " +
			"For Orchestrator use.",
		InputSchema: objectSchema(map[string]any{
			"project": prop("string", "Project name"),
			"max": prop("string", "Max items in EACH queued section: items, "+
				"needs_human_session and unclassified take it as their own LIMIT (default "+
				"10); running, stalled, paused and stale_running are unbounded. A JSON "+
				"number is also accepted, and is what most callers send."),
		}, []string{"project"}),
	}, func(ctx context.Context, req *sdkmcp.CallToolRequest) (*sdkmcp.CallToolResult, error) {
		args, err := parseArgs(req.Params.Arguments)
		if err != nil {
			return errResult(err)
		}
		project := strArg(args, "project")
		if project == "" {
			return errResult(fmt.Errorf("project is required"))
		}
		params := url.Values{}
		params.Set("project", project)
		// scalarArg, not strArg: `max` is published as a string, but "max items:
		// 5" is most naturally written as a JSON number, and strArg returns "" for
		// a non-string — so setIfNonempty dropped it and handleGetReadyQueue fell
		// back to its own default of 10, with no error at any hop. Same defect and
		// same fix as `limit` above (aihub#280 B6 / aihub#148).
		setIfNonempty(params, "max", scalarArg(args, "max"))
		result, err := s.client.GetReadyQueue(ctx, params)
		if err != nil {
			return errResult(err)
		}
		return jsonResult(result)
	})

	// pf_cancel_work_item
	//
	// aihub#355 changed both halves of what this tool does and what it can
	// return, so the description says so rather than leaving a caller to find
	// out. Cancelling now RELEASES every resource lock held on the work item's
	// behalf, and it now has two 409s where it used to answer 200 or 500.
	s.addTool(&sdkmcp.Tool{
		Name: "pf_cancel_work_item",
		Description: "Cancel a work item, and release every resource lock still held on its behalf. " +
			"Legal from queued, paused or blocked; a RUNNING work item is refused with 409 " +
			"CONFLICT_WI_ALREADY_CLAIMED (force_takeover first, then cancel) and an already-terminal one " +
			"with 409 CONFLICT_TERMINAL_STATE. " +
			"The lock release matters for a PAUSED work item: pausing releases only file_scope locks and " +
			"keeps every other type, and before aihub#355 cancelling left those held forever: the work " +
			"item was terminal, so no claim, force_takeover or complete_attempt could ever release them, " +
			"and the orphan sweep skips a paused attempt's rows by design. Since aihub#416 the retained " +
			"set is normally empty, because the only lock the server derives is file_scope; it is " +
			"non-empty for an attempt that supplied requested_locks explicitly, or that predates that " +
			"change. Every release emits a lock_released event with cause=wi_cancelled, so " +
			"pf_read_events can confirm it. " +
			"RETRYABLE: the status check is now re-run inside the transaction against a locked row, so " +
			"a cancel racing a claim can return 409 CONFLICT_WI_ALREADY_CLAIMED (correctly: the previous " +
			"200 was a lie, and it released a live attempt's locks), and a lost concurrency race returns " +
			"409 CONFLICT_SERIALIZATION_FAILURE with retryable=true. Both mean retry or re-read, not " +
			"\"the server is broken\".",
		InputSchema: objectSchema(map[string]any{
			"work_item_id": prop("string", "Work item ID or slug"),
			"reason":       prop("string", "Cancellation reason"),
		}, []string{"work_item_id"}),
	}, func(ctx context.Context, req *sdkmcp.CallToolRequest) (*sdkmcp.CallToolResult, error) {
		args, err := parseArgs(req.Params.Arguments)
		if err != nil {
			return errResult(err)
		}
		id := strArg(args, "work_item_id")
		if id == "" {
			return errResult(fmt.Errorf("work_item_id is required"))
		}
		body := map[string]any{}
		if reason := strArg(args, "reason"); reason != "" {
			body["reason"] = reason
		}
		result, err := s.client.CancelWorkItem(ctx, id, body)
		if err != nil {
			return errResult(err)
		}
		return jsonResult(result)
	})

	// pf_pause_attempt
	s.addTool(&sdkmcp.Tool{
		Name:        "pf_pause_attempt",
		Description: "Pause the current attempt (releases file_scope locks acquired mid-attempt; any other lock type is retained for resume, though since aihub#416 that set is normally empty, because file_scope is the only lock the server derives; status becomes paused). State file is preserved for resume.",
		InputSchema: objectSchema(map[string]any{
			"work_item_id": prop("string", "Work item ID (used to find state file)"),
			"pause_reason": prop("string", "Optional reason for pausing"),
		}, []string{"work_item_id"}),
	}, func(ctx context.Context, req *sdkmcp.CallToolRequest) (*sdkmcp.CallToolResult, error) {
		args, err := parseArgs(req.Params.Arguments)
		if err != nil {
			return errResult(err)
		}
		wiID := strArg(args, "work_item_id")
		if wiID == "" {
			return errResult(fmt.Errorf("work_item_id is required"))
		}
		sf, err := config.ResolveStateFile(wiID)
		if err != nil {
			return errResult(config.StateFileMissingErr(wiID, err))
		}
		body := map[string]any{
			"attempt_id":     sf.AttemptID,
			"claim_epoch":    sf.ClaimEpoch,
			"session_secret": sf.SessionSecret,
		}
		if reason := strArg(args, "pause_reason"); reason != "" {
			body["pause_reason"] = reason
		}
		result, err := s.client.PauseAttempt(ctx, sf.WIID, body)
		if err != nil {
			return errResult(err)
		}
		// State file is kept for paused status (C5-3: resume needs credentials)
		return jsonResult(result)
	})

	// pf_acquire_locks
	//
	// aihub#345: the already_held sentence in the description below is the fix,
	// not decoration. This tool used to report only the locks it had just
	// re-derived from declared_resources, so `already_held: []` was read as
	// "this attempt holds no locks" while the server went on enforcing locks it
	// had not mentioned — and an execute agent published exactly that conclusion
	// as a correction to a premise that had been right.
	s.addTool(&sdkmcp.Tool{
		Name: "pf_acquire_locks",
		Description: "Acquire file_scope locks for the current running attempt from the work item's declared_resources (reconcile mid-attempt; blocks on conflict, never steals). " +
			"`acquired` is what THIS call took; `already_held` is every other lock the attempt holds, of every type, read from the lock table, including locks with no live declaration behind them: locks taken from a client-supplied requested_locks, file_scope locks predating aihub#264, and (on a database with rows older than aihub#416) git_branch/deploy_env rows, which nothing derives any more. The two are disjoint and together are the attempt's full lock set. " +
			"Since aihub#264, removing a path from declared_resources DOES release its file_scope lock, at the moment of the update. Any surviving non-file_scope row is not released that way and is held until the attempt ends.",
		InputSchema: objectSchema(map[string]any{
			"work_item_id": prop("string", "Work item ID (used to find state file)"),
		}, []string{"work_item_id"}),
	}, func(ctx context.Context, req *sdkmcp.CallToolRequest) (*sdkmcp.CallToolResult, error) {
		args, err := parseArgs(req.Params.Arguments)
		if err != nil {
			return errResult(err)
		}
		wiID := strArg(args, "work_item_id")
		if wiID == "" {
			return errResult(fmt.Errorf("work_item_id is required"))
		}
		sf, err := config.ResolveStateFile(wiID)
		if err != nil {
			return errResult(config.StateFileMissingErr(wiID, err))
		}
		body := map[string]any{
			"attempt_id":     sf.AttemptID,
			"claim_epoch":    sf.ClaimEpoch,
			"session_secret": sf.SessionSecret,
		}
		result, err := s.client.AcquireLocks(ctx, sf.WIID, body)
		if err != nil {
			return errResult(err)
		}
		return jsonResult(result)
	})
}

// emptyObjectSchema returns a JSON schema for an empty object (no required fields).
func emptyObjectSchema() json.RawMessage {
	return json.RawMessage(`{"type":"object","properties":{}}`)
}

// objectSchema returns a JSON schema for an object with the given properties.
func objectSchema(props map[string]any, required []string) json.RawMessage {
	schema := map[string]any{
		"type":       "object",
		"properties": props,
	}
	if len(required) > 0 {
		schema["required"] = required
	}
	b, _ := json.Marshal(schema)
	return json.RawMessage(b)
}

// prop returns a simple property definition.
func prop(typ, description string) map[string]any {
	return map[string]any{
		"type":        typ,
		"description": description,
	}
}

// propEnum returns a property definition with an enum constraint (aihub#70).
func propEnum(typ, description string, enum []string) map[string]any {
	p := prop(typ, description)
	p["enum"] = enum
	return p
}

// jsonObjectPropNote is the hop-1 half of the aihub#465 shape guard, appended to
// the description of every caller-supplied jsonb object parameter that has NO
// length cap: `attrs` (pf_create_work_item, pf_batch_create_work_items,
// pf_update_work_item, pf_remember), `attrs_patch` (pf_update_work_item) and
// `structured_payload` (pf_save_artifact).
//
// aihub#486. The guard shipped enforced and unpublished, which is the mirror of
// aihub#238's published-but-unenforced enum: the InputSchema said
// `"type": "object"` and stopped there, so nothing told a caller what a
// violation looks like or how to repair it — and hop 1 is the only thing an LLM
// caller sees. The claim is not new, only newly stated: `internal/domain`'s
// validateJSONObjectParam has rejected these with a 400 since aihub#465.
//
// The middle sentence is the one a caller cannot guess, and it is why this text
// is worth its resident bytes: 18 of the 19 stringified values measured over the
// transcript corpus were a model hand-writing escaped JSON that came out
// malformed, not a client wrapping a good object. "Send an object" alone tells
// those 18 nothing about what to change.
//
// The closing sentence is here because its absence is what filed aihub#420: a
// caller who reads only "must be a JSON object", looks at the object they meant
// to send, and concludes the real limit is size.
//
// ⚠️ `payload` does NOT take this string. It is the one guarded field with a
// real size cap, so the closing sentence would be false there — see
// emitEventPayloadPropDescription in internal/mcp/tools_events.go, and
// jsonObjectParamSizeNote in internal/domain/work_items.go, which makes the same
// split on the error-message side.
const jsonObjectPropNote = " Must be a JSON object: a string (including a JSON-encoded string of the object you meant), " +
	"an array, a number or a boolean is rejected with 400 naming the type received, and nothing is written. " +
	"Do not hand-write the escaped JSON; send the object and let your client serialise it. " +
	"Size is never the reason for that 400: this field has no length cap."

// maxBatchWorkItems bounds pf_batch_create_work_items. Generous relative to the
// measured behaviour it replaces — the adjacent-create runs this fuses were a
// handful of follow-ups long, not dozens — while still keeping one MCP call from
// becoming an unbounded sequence of HTTP calls.
const maxBatchWorkItems = 50

// workItemFieldProps returns the per-work-item create fields, minus `project`.
//
// Single definition shared by pf_create_work_item's schema and the `items` entry
// schema of pf_batch_create_work_items (aihub#290). Two hand-maintained copies
// would drift, and a field present on one tool but not the other is the same
// silent-drop failure the batch tool exists downstream of.
func workItemFieldProps() map[string]any {
	return map[string]any{
		// aihub#474: the number is sourced from the constant that enforces it
		// rather than typed here, so it cannot move in the validator while two
		// hand-typed descriptions keep quoting the old one. Both work-item write
		// paths publish the cap from one place.
		//
		// aihub#520 adds "non-empty", the word aihub#507 put on
		// pf_update_work_item's own `goal` string and deliberately left off this
		// shared one. The refusal is not new on these two paths, only unpublished:
		// both handlers above reject `goal: ""` in this package before the request
		// leaves it (`goal is required` — whole-call on pf_create_work_item,
		// per-item and reported by index on pf_batch_create_work_items), and
		// domain.validateWorkItemGoalPresent answers 400 with the same text on
		// every path that writes the column. What was missing is the caller-facing
		// half: JSON-Schema `required` means "must be PRESENT", not "must be
		// non-empty", so a caller reading the published schema could satisfy it
		// with the empty string and be refused anyway. aihub#507 stopped short
		// because editing this string moves the input_schema_sha256 of BOTH tools
		// that share it and stales their contract cards; those two cards are in
		// this change's file scope and are regenerated in the same commit.
		"goal": prop("string", fmt.Sprintf("Single-line non-empty goal ≤%d chars",
			domain.MaxWorkItemGoalRunes())),
		"scenario": prop("string", "Scenario (default: coding)"),
		// aihub#396: a real enum, not a pipe-separated string in a description.
		// The values come from domain, which is where the check that refuses them
		// lives, so the published set and the accepted set are one value. propEnum
		// existed and was used four lines below for another field; this one was
		// written as prose.
		//
		// ⚠️ aihub#396 also recorded a REASON that is false, and it is corrected
		// here rather than repeated (aihub#496, 2026-09-09): "the SDK validates an
		// enum before the handler runs". Measured on go-sdk v1.6.0 by aihub#463
		// (2026-09-08): applySchema -> resolved.Validate is wired only into the
		// GENERIC AddTool[In, Out]; polyforge registers through the untyped method
		// (*mcp.Server).AddTool (see addTool in server.go), and Server.callTool
		// hands that path straight to the handler with NO schema step. A test drove
		// an out-of-vocabulary value through a real client session into the POST
		// body to establish it.
		//
		// So the enum constrains the CLIENT — an LLM reading tools/list, and any
		// client that validates before sending — and nothing in this process. The
		// hard refusal is the Go validator in domain, which answers 400 naming the
		// field; the enum is how a caller learns the set before spending a round
		// trip. Publishing an enum without the Go check would move a 500 nowhere,
		// which is why both halves are always done together.
		"priority":               propEnum("string", "Work item priority", domain.WorkItemPriorityList()),
		"wi_type":                prop("string", "Work item type (fix_bug, feature, chore, etc.)"),
		"requires_human_session": prop("boolean", requiresHumanSessionCreateDescription),
		"milestone":              prop("string", "Milestone name"),
		"labels":                 prop("array", fmt.Sprintf("Labels (max %d)", domain.MaxWorkItemLabels())),
		"declared_resources":     declaredResourcesProp("Declared resource locks"),
		"parent_work_item_id":    prop("string", "Parent work item ID"),
		// aihub#396: "Source reference" read as free text, and it is a closed
		// vocabulary — sending "jira" instead of "sync_jira" was a 500. Published
		// as an enum from the same list the validator uses.
		"source":       propEnum("string", "How this work item was filed", domain.WorkItemSourceList()),
		"attrs":        prop("object", "Additional attributes."+jsonObjectPropNote),
		"blocked_by":   prop("array", blockedByPropDescription),
		"content":      prop("string", contentPropDescription),
		"force_create": prop("boolean", "Force create bypassing duplicate check"),
		"force_reason": prop("string", "Reason for force create"),
		// aihub#720 slice C: the EXPLICIT composition-mode selector, published on
		// both create tools from this one shared map (slice B's G4 exemption for
		// handleCreateWorkItem.workflow_mode is deleted in the same change — the
		// parameter this row publishes is what makes the name reachable).
		//
		// The enum is domain.WorkflowModeValues — the same list
		// resolveCreateWorkflowMode refuses outside of, so the offered set and
		// the enforced set are one value (the aihub#396 rule: publish the enum
		// from the validator's own list, never hand-typed).
		//
		// The description states the CONSEQUENCE of a wrong choice rather than
		// naming the field's type: the selector's whole point (slice A/B) is that
		// a contradictory combination fails CLOSED with 400 COMPOSE_FAILED and a
		// machine-readable details.reason instead of silently walking the legacy
		// graph. 'db' is reachable-but-refused here on purpose: it requires
		// `steps`, which the create tools do not publish — the published path to a
		// db-mode flow is create (pending/absent) then pin via
		// pf_update_workflow, exactly what the aihub#708 exemption for
		// handleCreateWorkItem.steps records next door. Kept terse: this string
		// is resident on every request that lists either create tool.
		"workflow_mode": propEnum("string",
			"EXPLICIT composition-mode selector: 'legacy' = scenario step graph (the default when steps are omitted and this is absent); "+
				"'pending' = create uncomposed, pin the flow later via pf_update_workflow; 'db' pins generation 1 in this create and "+
				"REQUIRES steps (not published on the create tools: create, then pin via pf_update_workflow). A bad or contradictory "+
				"combination is 400 COMPOSE_FAILED with details.reason, never a silent legacy fall-back; claiming a 'pending' wi "+
				"answers 409 COMPOSE_PENDING.",
			domain.WorkflowModeValues()),
	}
}

// requiresHumanSessionCreateDescription is the published description of
// `requires_human_session` on the two create paths.
//
// It names the THIRD state, which is the aihub#411 T2-9 ruling. The column is a
// *bool — true / false / NULL — and NULL is not "unset pending a default": it is
// its own ready-queue segment (`unclassified[]`, domain.GetReadyQueue), which
// items[] and readyOnlyPredicate both exclude. Published as a bare boolean with
// no mention of omission, the one state a caller reached BY DOING NOTHING was the
// state the contract did not name. aihub#397 measured exactly that and was
// cancelled as description-only, which the adjudication ruled is not a
// cancellation reason.
//
// The claim-path sentence is contract, not background. aihub#411 itself was
// created with requires_human_session: null, did not stay null, and the write was
// attributed to an unrelated attrs_patch-only pf_update_work_item call. aihub#447
// reproduced the whole sequence on two server builds and found the update writes
// nothing here; the FIRST claim does, from a server default. A caller that cannot
// see the claim-path write in any published text has no way to reach that
// conclusion, which is how one line of silence bought an unexplained-behaviour
// row in the audit.
const requiresHumanSessionCreateDescription = "Whether a human has to be in the session for this wi. " +
	"THREE states, not two: true, false, and OMITTED. Omitting it stores NULL, which is NOT a default of " +
	"false: the wi goes to the ready queue's unclassified[] segment instead of items[], the segment that " +
	"means \"takeable now by an agent\", and pf_list_work_items' ready_only filter does not return it. NULL is " +
	"not permanent: the FIRST pf_claim_work_item on such a wi resolves it to true from a server default, " +
	"writes that back and records a wi_classification_resolved event. Send false explicitly for a wi an " +
	"agent may take unattended."

// requiresHumanSessionUpdateDescription is the counterpart of the constant above
// on pf_update_work_item, which can reach only TWO of the three states.
//
// Nil binds to "leave the column alone" (domain.buildWorkItemUpdate), so an
// explicit null is the same no-op as omitting the field. That is measured rather
// than read off the type: aihub#447 sent both against a scratch wi on a live-era
// build and on origin/main, and the stored value did not move either time.
//
// The last sentence is the one worth the bytes. The value a caller finds in this
// field is very often one the CLAIM wrote, and this tool's reply carries the
// field whether or not the call touched it — which is exactly how aihub#411 T2-9
// came to record an attrs_patch-only update as the writer of a NULL -> true
// transition that FnClaimWorkItem had made a minute earlier.
//
// Declared here rather than inline so the map literal above keeps its alignment
// groups; the neighbouring aihub#337 note explains why length is a real cost on
// this tool in particular.
const requiresHumanSessionUpdateDescription = "Set the human-session classification to true or false. " +
	"It cannot reach the third state: there is no way back to NULL (unclassified) through this tool, because " +
	"omitting this field and sending an explicit null BOTH mean \"leave the stored value alone\". A wi that was " +
	"still NULL when it was first claimed is already true (the claim resolves NULL from a server default), so " +
	"this field is how you CORRECT that value, not how you undo it."

// blockedByPropDescription is the published description of `blocked_by`.
//
// It states what the parameter DOES, not merely what it is a list of, because
// aihub#357 was filed on the belief that it only flipped `status` — the
// consequence of an unrelated read-side defect, but a belief the old one-line
// description ("List of blocking work item IDs") did nothing to correct. A
// schema that describes the argument's type and stays silent about its effect
// leaves every caller to find out by measuring.
const blockedByPropDescription = "Work items that block this one; each entry may be an id or a slug. " +
	"Each creates a real 'blocks' dependency edge and records a dependency_created event on the new wi's " +
	"timeline, and a non-empty list makes the new wi status=blocked. Removing the last unfinished blocker " +
	"requeues it. An entry naming no work item is rejected and the wi is NOT created."

// contentPropDescription is the published description of the `content`
// parameter, written once and shared by pf_create_work_item,
// pf_batch_create_work_items and pf_update_work_item so the three cannot drift.
//
// The second sentence is contract, not decoration. aihub#281 stops echoing this
// field back, and a schema that kept quiet about it would be describing a
// response the tool no longer returns — the same drift between published shape
// and real behaviour that aihub#238 and aihub#241 are about.
const contentPropDescription = "Background context for this wi (markdown, max 20000 chars). " +
	"Not echoed back: the response reports content_len (bytes stored) in its place."

// createWorkItemSchema is pf_create_work_item's InputSchema: the shared per-item
// fields plus the project this one is filed under.
func createWorkItemSchema() json.RawMessage {
	props := workItemFieldProps()
	props["project"] = prop("string", "Project name")
	return objectSchema(props, []string{"project", "goal"})
}

// batchWorkItemsProp describes the `items` array *including its entry shape*,
// following declaredResourcesProp (aihub#238): an array whose element shape is
// undocumented is a contract the caller has to guess at.
func batchWorkItemsProp() map[string]any {
	props := workItemFieldProps()
	props["project"] = prop("string", "Project for THIS item; defaults to the call's top-level project.")
	p := prop("array", fmt.Sprintf("Work items to create (1-%d). Each entry takes the same fields as pf_create_work_item; `project` is optional per item and falls back to the top-level one.", maxBatchWorkItems))
	p["items"] = map[string]any{
		"type":       "object",
		"properties": props,
		"required":   []string{"goal"},
	}
	return p
}

// applyForceReasonDefault supplies a force_reason when force_create is set,
// because the server requires >=10 chars and rejects the request without one.
// Shared by the single and batch create paths so a batch item does not fail a
// validation the single-item path quietly satisfies for you.
func applyForceReasonDefault(args map[string]any) {
	if boolArg(args, "force_create") && strArg(args, "force_reason") == "" {
		args["force_reason"] = "force_create=true via MCP (admin bypass dedup check)"
	}
}

// cloneArgs returns a shallow copy, so defaulting a batch item's fields does not
// mutate the arguments map the caller handed us.
func cloneArgs(in map[string]any) map[string]any {
	out := make(map[string]any, len(in)+2)
	for k, v := range in {
		out[k] = v
	}
	return out
}

// declaredResourcesProp describes the declared_resources array *including its
// entry shape* (aihub#238).
//
// Before this, all three declared_resources schemas were a bare
// prop("array", ...) and the only written record of the real shape was
// pf-plan/SKILL.md Step 5 — invisible to every caller that does not go through
// pf-plan, even though the MCP schema is their sole contract. Combined with a
// server that silently skipped unrecognized types, a wrong guess cost nothing at
// the call and everything later.
//
// The enum is taken from domain.DeclaredResourceTypeList() rather than written
// out here, so the published contract cannot drift from the validator that
// enforces it.
func declaredResourcesProp(description string) map[string]any {
	p := prop("array", description+
		`. Entries are {"type","uri","intent"} plus an optional "repo" on path entries (aihub#261). NOTE: type takes a DECLARED type (repo/path/document/section/service/external_ref), NOT a lock type: file_scope/git_branch/worktree/tcp_port/deploy_env are resource_locks.resource_type values. Since aihub#416 the server derives exactly ONE of them: file_scope, from path/document/section entries. git_branch and deploy_env are no longer derived from anything (a repo or service entry takes no lock), and worktree/tcp_port never were; all four remain legal only in an explicit requested_locks. A file path is type="path", uri="file:<repo-relative-path>". The path field is `+"`uri`"+`, not value/path/scope.`)
	p["items"] = map[string]any{
		"type": "object",
		"properties": map[string]any{
			// aihub#395 part 3 said "external_ref takes NO lock ...; every other
			// type derives a lock". aihub#416 made the second half false: repo and
			// service now derive nothing either, so THREE of the six types take no
			// lock and only path/document/section do.
			//
			// The sentence is rewritten rather than dropped because #395's reason
			// survives the change and gets bigger: a caller reads this under a
			// field whose own description says "Declared resource locks", so an
			// entry that is accepted and produces no lock has to be named as such
			// or the caller measures it the hard way. What DID change is that the
			// no-lock set is no longer a curiosity of one annotation type — it is
			// now the majority — and that repo/service still produce a signal
			// (predict rules 2, 4, 6), which external_ref's own warning-exemption
			// means it does not.
			"type": propEnum("string", "Declared resource type (NOT a lock type). Only path/document/section "+
				"take a lock (file_scope). repo and service take NONE since aihub#416; they are advisory: they "+
				"feed pf_predict_conflicts, the timeline and deploy preflight, and derive no resource_locks row. "+
				"external_ref takes no lock and no warning either, an annotation only.",
				domain.DeclaredResourceTypeList()),
			// aihub#395 part 4. Generated from domain.declaredResourceURISchemes,
			// which is the table ValidateDeclaredResources enforces — so the
			// sentence a caller reads and the rule the server applies are one
			// value. It used to be prose here and enforced nowhere, and a wrong
			// scheme was a 200 that keyed the lock off the unstripped uri.
			// Kept deliberately terse: this string is always-resident in every
			// session that lists any of the four tools publishing this prop, so the
			// REASON a wrong scheme is dangerous (TrimPrefix is a no-op on a prefix
			// that is not there, so the lock lands on a nonsense key) lives in the
			// comment above and in ValidateDeclaredResources, not here. What the
			// caller needs at the call site is the rule and its consequence.
			"uri": prop("string",
				"Resource URI; the scheme is validated per type (wrong scheme = 400 naming the entry): "+
					domain.DeclaredResourceURISchemeDoc()+"."),
			// Deliberately NOT an enum. The server does not validate `intent` at all;
			// only two values change behaviour ("read" suppresses the write lock and
			// downgrades path conflicts to info; "refactor" on a repo entry triggers
			// conflict rule 4). Meanwhile this repo's own fixtures use "exclusive" more
			// often than "write". Publishing a closed set would state a contract the
			// server does not keep — the exact failure this wi is about — so describe
			// the semantics instead and let unknown values through as inert. (aihub#238)
			"intent": prop("string",
				`Access intent. Not validated by the server; only two values carry behaviour: "read" and `+
					`"refactor" (on a repo entry, flags other refactors of the same repo). "write" is the `+
					`conventional default; other values are accepted but inert. "read" is honoured on `+
					`path/document/section ONLY: no write lock, and a path overlap reports info instead `+
					`of soft_block. On repo and service entries "read" is inert for a different reason `+
					`since aihub#416: those two take no lock under ANY intent, so there is nothing for `+
					`"read" to suppress.`),
			// aihub#261. The uri of a path/document/section entry is REPO-relative,
			// and until this field existed nothing in the payload said which repo —
			// so in a multi-repo project every repo's go.mod / Makefile / README.md
			// derived one lock key and hard-blocked each other (measured: 409
			// CONFLICT_LOCK_TAKEN between two work items editing two different files).
			//
			// Optional, and omitting it is not an error: the key then keeps its
			// pre-aihub#261 "<project>:<path>" form and conflicts with every repo's
			// copy of that path, exactly as before. Saying "unspecified means all
			// repos" in the description is the point — a caller who reads it as
			// "means no repo" would expect isolation the server does not give.
			"repo": prop("string",
				`Repo the uri is relative to (path/document/section entries only), e.g. "ieops-core". Optional but recommended in a multi-repo project: without it the lock key cannot tell one repo's go.mod/Makefile/README.md from another's, so unrelated work items block each other. Omitted means "unspecified repo", which still conflicts with every repo's copy of that path. Defaults to the repo named by this payload's own {"type":"repo"} entry when it names exactly one.`),
			// aihub#395 part 2: `base_branch` is deliberately NOT published.
			//
			// It was `prop("string", "Base branch (repo entries only)")` and read by
			// nothing. Measured on a8ad8c0, the complete set of non-test occurrences
			// was the struct field (DeclaredResourceItem.BaseBranch) and the decoder
			// that fills it (decodeDeclaredResources) — no lock key, conflict rule,
			// query or worktree base. The worktree base is origin/main, hard-coded in
			// addClaimWorktree, so a caller who set base_branch got a claim off
			// origin/main with no error and no warning.
			//
			// Withdrawn from the schema rather than implemented, the same plan B as
			// aihub#387 and aihub#394. The struct field and decoder STAY: stored
			// declared_resources are JSON and a value already recorded on an existing
			// work item must keep round-tripping. Deleting the field would silently
			// drop it from every stored payload that carries one, which is a data
			// loss to fix a documentation defect.
			// aihub#416: `task_branch` is deliberately NOT published, the same plan
			// B as `base_branch` two comments up and for the same reason — it is
			// read by nothing.
			//
			// Its ONLY consumer was the git_branch lock key
			// ("<repo>/<task_branch>", domain.resourceToLock), and the de-locking
			// ruling retired that derivation: a repo entry now takes no lock under
			// any intent, so there is no key left for this value to spell. The
			// aihub#356 machine that overrode it at claim time went with it
			// (claimTaskBranches, ClaimRequest.TaskBranches,
			// EffectiveDeclaredResource), so this field is not merely overridden
			// now — it is read by nothing at all.
			//
			// Publishing it would be the aihub#395 defect exactly: a parameter a
			// caller can set, that is accepted with a 200, and that changes
			// nothing. Withdrawn rather than deleted, again per #395: the struct
			// field domain.DeclaredResourceItem.TaskBranch and its decode in
			// decodeDeclaredResources STAY, because stored declared_resources are
			// JSON and dropping the field would silently discard the value from
			// every work item that already carries one — a data loss to fix a
			// documentation defect.
			//
			// ⚠️ Do not re-publish it as "documentation of which branch the work
			// happens on". Nothing reads it, so it would document nothing; the
			// branch a work item is really on is `git rev-parse --abbrev-ref HEAD`
			// in its worktree, which is what pf_ship / pf_pr / pf_push / pf_wrap
			// already ask, and the commit a claim starts from is recorded server-
			// side as run_attempts.repo_pins.
		},
		"required": []string{"type", "uri"},
	}
	return p
}

// requestedLocksProp describes the requested_locks array including its entry
// shape (aihub#238).
//
// This array had no item schema and was passed through to the server verbatim, so
// a caller had to guess domain.ResourceLockReq's {resource_type, resource_key}.
// Guessing the neighbouring declared_resources {type, value} shape produced an
// empty resource_type, tripped the resource_locks CHECK constraint, and returned
// 500 INTERNAL_ERROR with a bare SQLSTATE.
//
// Normal polyforge flow leaves this unset and lets the server derive locks from
// the work item's declared_resources.
func requestedLocksProp(description string) map[string]any {
	p := prop("array", description+
		`. Usually OMIT this and let the server derive locks from the wi's declared_resources. Entries are {"resource_type","resource_key"} (NOT declared_resources' type/uri).`)
	p["items"] = map[string]any{
		"type": "object",
		"properties": map[string]any{
			"resource_type": propEnum("string", "Lock type (NOT a declared_resources type)", domain.ResourceLockTypeList()),
			"resource_key": prop("string",
				`Lock key. file_scope is "<project>:<repo>:<repo-relative-path>", or "<project>:<repo-relative-path>" when the declaration names no repo (aihub#222, aihub#261); git_branch is "<repo>/<branch>"; deploy_env is the bare service name.`),
		},
		"required": []string{"resource_type", "resource_key"},
	}
	return p
}
