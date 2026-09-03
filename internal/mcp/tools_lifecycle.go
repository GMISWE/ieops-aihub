package mcp

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/GMISWE/ieops-aihub/internal/coding"
	"github.com/GMISWE/ieops-aihub/internal/config"
	"github.com/GMISWE/ieops-aihub/internal/domain"
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
		"label", "user_id", "source", "since", "limit", "cursor",
		"sort", "order", "query",
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
		"a comma-separated string is also accepted). Makes `project` optional — an id "+
		"already names one work item, and the query is bounded to the projects you can see. "+
		"Note the asymmetry with `project`: an inaccessible project= is a 403, whereas ids "+
		"you cannot see are silently omitted, so a short result means \"not visible to you\" "+
		"as well as \"does not exist\".")
	idsProp["items"] = map[string]any{"type": "string"}
	return objectSchema(map[string]any{
		"project": prop("string", "Project name. Optional when `ids` is given, required otherwise."),
		"ids":     idsProp,
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
		"label":   prop("string", "Filter by label"),
		"user_id": prop("string", "Filter by user ID"),
		"source":  prop("string", "Filter by source"),
		"ready_only": prop("boolean", "Only return items that are ready to claim: queued, "+
			"not requiring a human session, and with no unfinished blocking dependency. "+
			"Same PREDICATE as pf_get_ready_queue's items[] (one shared SQL constant), but "+
			"not the same page: this defaults to limit=50 ordered by created_at desc, while "+
			"the ready queue defaults to 10 ordered by priority desc. With more ready items "+
			"than either limit they return different subsets."),
		"include_step_state": prop("boolean", "Attach each item's step state as `step_state` "+
			"(current_step, current_step_status, step_started_at, ...). The key is ABSENT for a work "+
			"item that has never been claimed — and also if the lookup itself failed, which is "+
			"best-effort and reported only on the server's stderr. Absent therefore means \"no step "+
			"state\", not \"definitely never claimed\"."),
		"since": prop("string", "Only items whose CREATED_AT is at or after this RFC3339 timestamp. "+
			"This is creation time, not close time: combining it with status=wrapped does NOT give "+
			"\"wrapped since T\" — an item created before T and wrapped after it is excluded. "+
			"An unparseable value is rejected rather than ignored."),
		"query": prop("string", "Semantic search over goal+content (aihub#273): "+
			"embedding cosine when the server has a provider, ILIKE fallback otherwise. "+
			"Results are similarity-ordered; not combinable with sort/order/cursor."),
		"limit": prop("string", "Max items to return. A JSON number is also accepted "+
			"(and is what most callers send); values above 200 fall back to the default of 50."),
		"cursor": prop("string", "Pagination cursor. Carries the value of the column named by `sort`, "+
			"so pass it back unchanged and do not mix cursors between different sort orders."),
		// The enums come from the server's enforced sets (aihub#224) rather than
		// being retyped here, so the published contract cannot drift from the
		// validator that rejects everything outside them.
		"sort": propEnum("string", fmt.Sprintf(
			"Sort column (default %s). %s returns ONLY closed items — a NULL close time has no position in that ordering.",
			domain.ListWorkItemsSortCreatedAt, domain.ListWorkItemsSortClosedAt),
			domain.ListWorkItemsSortValues()),
		"order": propEnum("string", fmt.Sprintf("Sort direction (default %s)", domain.ListWorkItemsOrderDesc),
			domain.ListWorkItemsOrderValues()),
	}, nil)
}

func (s *Server) registerLifecycleTools() {
	// pf_whoami
	s.mcp.AddTool(&sdkmcp.Tool{
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
		// If listing projects fails, still return whoami without projects field (best-effort)

		return jsonResult(result)
	})

	// pf_create_work_item
	s.mcp.AddTool(&sdkmcp.Tool{
		Name:        "pf_create_work_item",
		Description: "Create a work item in the specified project. To create more than one, use pf_batch_create_work_items — repeated calls here cost one round-trip each.",
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
				return errResult(fmt.Errorf("PROJECT_NOT_FOUND: project %q does not exist — create it first with pf_create_project", strArg(args, "project")))
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
	s.mcp.AddTool(&sdkmcp.Tool{
		Name: "pf_batch_create_work_items",
		Description: "Create SEVERAL work items in one call. Use this when filing more than one wi at once " +
			"(follow-ups discovered mid-execution, a backlog split into pieces) instead of calling " +
			"pf_create_work_item repeatedly — each extra call costs a whole round-trip for a confirmation " +
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
	s.mcp.AddTool(&sdkmcp.Tool{
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
		Description: "List work items with optional filters. " +
			"Item keys whose value is null are omitted: an absent key means null.",
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
	s.mcp.AddTool(&sdkmcp.Tool{
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
	s.mcp.AddTool(&sdkmcp.Tool{
		Name:        "pf_update_work_item",
		Description: "Update a work item (goal, wi_type, priority, labels, etc.)",
		InputSchema: objectSchema(map[string]any{
			"work_item_id":           prop("string", "Work item ID or slug"),
			"goal":                   prop("string", "Updated goal (status must be queued or paused)"),
			"goal_change_reason":     prop("string", "Reason for goal change (required with goal)"),
			"kind":                   prop("string", "Updated kind"),
			"priority":               prop("string", "Updated priority"),
			"milestone":              prop("string", "Updated milestone"),
			"wi_type":                prop("string", "Updated wi_type"),
			"requires_human_session": prop("boolean", "Updated requires_human_session"),
			"reclassify_reason":      prop("string", "Reason for wi_type change (min 10 chars)"),
			"labels":                 prop("array", "Updated labels"),
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
			"attrs":             prop("object", "REPLACES the whole attrs object: every key you do not resend is DELETED. Use it only when you intend to overwrite attrs wholesale (e.g. after reading the current value). To add or change keys without destroying the others, use attrs_patch. Cannot be combined with attrs_patch/attrs_unset."),
			"attrs_patch":       prop("object", "Merge these keys into attrs, leaving every other key untouched (aihub#288). Shallow: a top-level key in the patch replaces that key's stored value outright, it is NOT merged into it recursively, and null STORES a JSON null rather than deleting. To delete keys use attrs_unset. Cannot be combined with attrs."),
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
				"A wi that HAS no body is unaffected — it comes back as content: null with no content_len, so a "+
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
	s.mcp.AddTool(&sdkmcp.Tool{
		Name:        "pf_claim_work_item",
		Description: "Claim a work item — creates a new run_attempt with typed locks. Writes state file with credentials.",
		InputSchema: objectSchema(map[string]any{
			"work_item_id":    prop("string", "Work item ID or slug"),
			"idempotency_key": prop("string", "Idempotency key for DB dedup"),
			"mode":            prop("string", "fresh|resume (default: fresh)"),
			"requested_locks": requestedLocksProp("Resource locks to acquire"),
			"force_takeover":  prop("boolean", "Force takeover if already claimed"),
			"scenario_ref":    prop("string", "Git SHA of local scenario clone at claim time (optional)"),
		}, []string{"work_item_id", "idempotency_key"}),
	}, func(ctx context.Context, req *sdkmcp.CallToolRequest) (*sdkmcp.CallToolResult, error) {
		args, err := parseArgs(req.Params.Arguments)
		if err != nil {
			return errResult(err)
		}
		wiID := strArg(args, "work_item_id")
		if wiID == "" {
			return errResult(fmt.Errorf("work_item_id is required"))
		}
		idemKey := strArg(args, "idempotency_key")
		if idemKey == "" {
			return errResult(fmt.Errorf("idempotency_key is required"))
		}

		// C6-2: Generate session_secret BEFORE calling aihub
		sessionSecret, err := generateSessionSecret()
		if err != nil {
			return errResult(fmt.Errorf("generate session_secret: %w", err))
		}

		// Write partial state file first (C6-2 protocol)
		partial := &config.StateFile{
			WIID:          wiID,
			IdemKey:       idemKey,
			SessionSecret: sessionSecret,
			Claimed:       false,
		}
		if err := config.WriteStateFile(partial); err != nil {
			return errResult(fmt.Errorf("write state file: %w", err))
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
		if mode := strArg(args, "mode"); mode != "" {
			body["mode"] = mode
		}
		if v, ok := args["requested_locks"]; ok {
			body["requested_locks"] = v
		}
		if boolArg(args, "force_takeover") {
			body["force_takeover"] = true
		}
		if sr := strArg(args, "scenario_ref"); sr != "" {
			body["scenario_ref"] = sr
		}

		result, err := s.client.ClaimWorkItem(ctx, wiID, body)
		if err != nil {
			// Don't delete the partial state file — let the user retry
			return errResult(fmt.Errorf("claim work item: %w", err))
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
			// ⚠️ THE RECOVERY IS A **NEW** idempotency_key, NOT A REPLAY OF THIS
			// ONE. Read off internal/domain/run_attempts.go, not assumed: a claim
			// carrying an already-used key takes the idempotency branch, which
			// returns the EXISTING attempt and never touches session_secret_hash —
			// while this handler mints a fresh session_secret on every call, so the
			// state file it writes would hold a secret the server has never seen and
			// every later call 401s "invalid session_secret". With a new key the
			// same-user branch treats it as an implicit takeover and issues a fresh
			// attempt bound to the secret this call generated.
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
			attemptDesc := fmt.Sprintf("Attempt %s (epoch %d)", sf.AttemptID, sf.ClaimEpoch)
			if sf.AttemptID == "" {
				// An old server that did not echo attempt_id would otherwise render
				// "Attempt  (epoch 0)", which reads as data rather than as absence.
				attemptDesc = "A new attempt (the server did not echo its id)"
			}
			return errResult(fmt.Errorf("update state file: %w"+
				" — ⚠️ NOT A NO-OP: the claim ALREADY SUCCEEDED on the server."+
				" %s is running under your name and holds whatever locks this work item declares;"+
				" only this machine's local record of it failed, and without that record nothing here can authenticate as the attempt."+
				" RECOVERY: call pf_claim_work_item again with a NEW idempotency_key — replaying the same key returns this attempt without registering a new secret, leaving every later call unauthorized."+
				" The re-claim is not destructive: you already own the attempt, so it costs one epoch bump and one superseded attempt."+
				" %s",
				err, attemptDesc, stateWriteFilesystemAdvice))
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
		var worktreeProblems []string
		if sf.Project != "" {
			wsRoot := os.Getenv("POLYFORGE_WORKSPACE_ROOT")
			if wsRoot == "" {
				wsRoot = config.FindWorkspaceRoot()
			}
			if wsRoot != "" {
				effectiveCfg := resolveWorkspaceConfig(wsRoot, s.cfg)

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
					// Deliberately NOT keyed on args["mode"] (aihub#322): which branch
					// to attach to is decided by what exists in the clone, not by what
					// the caller called the claim. See resolveClaimBranch.
					branchNames := newClaimBranchNames(sf.Project, seq, wiGoal, ulid8)

					if proj, ok := effectiveCfg.Projects[sf.Project]; ok {
						worktrees := make(map[string]string)
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
										"Inspect it first — a half-finished checkout still holds whatever was written before it died. "+
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

		// Don't return session_secret to LLM (decision A)
		// Return attempt_id and claim_epoch only
		safeResult := map[string]any{
			"attempt_id":  sf.AttemptID,
			"claim_epoch": sf.ClaimEpoch,
			"ok":          true,
		}
		// Pass through other non-secret fields.
		//
		// aihub#238: `unrecognized_resources` MUST stay in this list. It is the only
		// signal that a declared resource is holding no lock, and reporting at claim
		// is the only remedy available on the stored-data path (rejecting there would
		// make historical mistyped work items unclaimable). Dropped here, the whole
		// remedy is inert and the caller sees exactly the pre-fix output.
		for _, k := range []string{"expires_at", "acquired_locks", "current_attempt_epoch", "slug", "project", "unrecognized_resources"} {
			if v, ok := result[k]; ok {
				safeResult[k] = v
			}
		}
		addWorktrees(safeResult, sf.Worktrees)
		// aihub#328: a rejected directory has to reach the caller, not just stderr.
		// The claim itself succeeded, so this is a warning on an ok:true response
		// rather than an error — but without it the agent proceeds believing it has
		// a worktree it does not have, which is the same blindness in a new place.
		if len(worktreeProblems) > 0 {
			safeResult["worktree_problems"] = worktreeProblems
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
	s.mcp.AddTool(&sdkmcp.Tool{
		Name: "pf_complete_attempt",
		Description: "Complete the current run attempt (wrapped|failed|paused). Deletes state file for terminal statuses. " +
			"Pass `note` to record the closing note in the same call instead of emitting it with a separate " +
			"pf_emit_event beforehand — which is the only order that works, since this call deletes the " +
			"credentials pf_emit_event needs. The response's note_emitted says whether it landed.",
		InputSchema: objectSchema(map[string]any{
			"work_item_id":         prop("string", "Work item ID (used to find state file)"),
			"status":               prop("string", "wrapped|failed|paused"),
			"force_terminate_step": prop("boolean", "Force terminate in-progress step"),
			"note":                 prop("string", "Closing note recorded as a `note` event before the attempt is completed (e.g. \"wrapped: <one sentence>\" / \"failed reason: <why>\"). Replaces a separate pf_emit_event call."),
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

		sf, err := config.ResolveStateFile(wiID)
		if err != nil {
			return errResult(fmt.Errorf("read state file: %w", err))
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
	s.mcp.AddTool(&sdkmcp.Tool{
		Name:        "pf_force_takeover",
		Description: "Force-take ownership of a work item from another agent",
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
		sessionSecret, err := generateSessionSecret()
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
				" — ⚠️ NOT A NO-OP: the takeover ALREADY SUCCEEDED on the server."+
				" %s has been evicted, and attempt %s (epoch %d) is running under your name holding whatever locks this work item declares;"+
				" only this machine's local record of it failed, and the session_secret it needed lived in memory alone and is now gone."+
				" RECOVERY: re-run this exact pf_force_takeover call — you now own the current attempt, so the server admits it as a self-takeover."+
				" It is not destructive: it costs one epoch bump, one superseded attempt and one timeline event."+
				" %s",
				err, prior, sf.AttemptID, sf.ClaimEpoch, stateWriteFilesystemAdvice))
		}

		// Return result without session_secret.
		// v1.21 ownership-only: no expires_at; do not surface that field.
		safeResult := map[string]any{
			"prior_attempt_id":    result["prior_attempt_id"],
			"prior_actor_display": result["prior_actor_display"],
			"new_attempt_id":      sf.AttemptID,
			"new_claim_epoch":     sf.ClaimEpoch,
			"ok":                  result["ok"],
		}
		return jsonResult(safeResult)
	})

	// pf_get_ready_queue
	s.mcp.AddTool(&sdkmcp.Tool{
		Name:        "pf_get_ready_queue",
		Description: "Get the LCRS (6-section) ready queue for a project. For Orchestrator use.",
		InputSchema: objectSchema(map[string]any{
			"project": prop("string", "Project name"),
			"max": prop("string", "Max items in ready section (default 10). A JSON number is "+
				"also accepted, and is what most callers send."),
			"non_conflicting": prop("boolean", "Only return non-conflicting items"),
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
		if boolArg(args, "non_conflicting") {
			params.Set("non_conflicting", "true")
		}
		result, err := s.client.GetReadyQueue(ctx, params)
		if err != nil {
			return errResult(err)
		}
		return jsonResult(result)
	})

	// pf_cancel_work_item
	s.mcp.AddTool(&sdkmcp.Tool{
		Name:        "pf_cancel_work_item",
		Description: "Cancel a work item",
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
	s.mcp.AddTool(&sdkmcp.Tool{
		Name:        "pf_pause_attempt",
		Description: "Pause the current attempt (releases file_scope locks acquired mid-attempt; git_branch/deploy_env locks are retained for resume; status → paused). State file is preserved for resume.",
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
			return errResult(fmt.Errorf("read state file: %w", err))
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
	s.mcp.AddTool(&sdkmcp.Tool{
		Name: "pf_acquire_locks",
		Description: "Acquire file_scope locks for the current running attempt from the work item's declared_resources (reconcile mid-attempt; blocks on conflict, never steals). " +
			"`acquired` is what THIS call took; `already_held` is every other lock the attempt holds, of every type, read from the lock table — including locks with no live declaration behind them: git_branch and deploy_env locks, locks taken from a client-supplied requested_locks, and file_scope locks predating aihub#264. The two are disjoint and together are the attempt's full lock set. " +
			"Since aihub#264, removing a path from declared_resources DOES release its file_scope lock, at the moment of the update; git_branch and deploy_env locks are not released that way and are held until the attempt ends.",
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
			return errResult(fmt.Errorf("read state file: %w", err))
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

// stateWriteFilesystemAdvice closes both aihub#323 messages.
//
// It is the third thing a caller needs and the one it is easiest to leave out:
// the two failures the state write can hit are os.MkdirAll and os.WriteFile,
// which do not fail transiently. Without this sentence the "re-run the call"
// advice above reads as a retry loop, and the caller spends its budget bumping
// epochs against a full disk.
const stateWriteFilesystemAdvice = "If the write fails again the fault is this machine's filesystem (disk full, read-only mount, wrong ownership on <workspace>/.polyforge/state) rather than the server, and no number of retries helps until that is fixed."

// verifyClaimWorktree answers whether wtPath is a usable git worktree, as
// opposed to a directory that merely exists.
//
// The claim handler used to adopt any existing directory on os.Stat alone,
// record it in the state file, and then take that early return on every later
// claim. So a `git worktree add` that died part-way through its checkout — disk
// full, SIGKILL, the machine rebooting — left a half-populated directory that
// was adopted permanently and never repaired, while pf_diff / pf_commit /
// pf_ship downstream were handed a path that is not a worktree. (aihub#328)
//
// ⚠️ `rev-parse --git-dir` ALONE IS NOT ENOUGH, and this is measured rather than
// reasoned. git searches PARENT directories, and a polyforge workspace root is
// itself commonly a git repository (the live gmi-ws workspace is one), so a
// directory with no .git at all at <wsRoot>/pf.<project>-<seq>/<repo> answers
// exit 0. Measured on git 2.43.0 against exactly that layout: `rev-parse
// --git-dir` printed <wsRoot>/.git and exited 0, i.e. the naive check calls the
// broken directory healthy. `--show-toplevel` printed <wsRoot>, which is NOT
// wtPath — comparing it against wtPath is what separates the two, because a real
// worktree's toplevel is its own path (verified: the control printed wtPath).
//
// The other half-built shape, a .git FILE whose `gitdir:` pointer names a
// missing admin directory, fails both forms with exit 128 ("fatal: not a git
// repository: ...").
//
// ⚠️ WHAT THIS DOES NOT DETECT, and the first entry is the one the work item's
// own motivation names, so read it before trusting this function:
//
//  1. A STRUCTURALLY VALID WORKTREE WHOSE CHECKOUT NEVER FINISHED. git writes
//     the .git file and the $GIT_DIR/worktrees/<id>/ admin directory BEFORE it
//     populates the working tree, so a `worktree add` stopped by SIGKILL or by
//     the machine rebooting mid-checkout leaves exactly that: an intact pointer
//     over a half-populated tree, whose --show-toplevel is its own path. It
//     passes. (The disk-full case usually does NOT reach here: git's own error
//     path removes the admin directory, which produces the dangling pointer of
//     case 1 in the paragraph above and IS caught.) Closing this needs a notion
//     of "the checkout is complete", and git has no such query — `git status`
//     reports the missing files as deletions, which is indistinguishable from a
//     developer who deleted them. That is an API-shape decision, not an
//     oversight to patch here.
//  2. A perfectly good worktree of some OTHER repository, whose toplevel is
//     itself and so passes. The wrong repo rather than a broken one; rejecting
//     it needs a notion of which origin a repo ought to have, which this path
//     does not carry.
func verifyClaimWorktree(wtPath string) error {
	cmd := exec.Command("git", "-C", wtPath, "rev-parse", "--show-toplevel")
	// ⚠️ .Output(), NOT .CombinedOutput(). This is the only place in this file
	// that PARSES git's stdout — everywhere else the combined buffer is error
	// text — and git writes diagnostics to stderr while still exiting 0.
	// Measured: with GIT_TRACE=1 in the environment, CombinedOutput returns
	// "trace: built-in: git rev-parse --show-toplevel\n<path>", TrimSpace keeps
	// the trace line, the comparison below fails, and EVERY healthy worktree is
	// rejected — with a message telling the operator to rm -rf it.
	//
	// GIT_DIR/GIT_WORK_TREE are scrubbed for the same class of reason: `-C`
	// alone does not beat them. Measured — with both set, `git -C <worktree>
	// rev-parse --show-toplevel` printed the OTHER repository's root and exited
	// 0, which would likewise reject every worktree at once.
	cmd.Env = envWithout(os.Environ(), "GIT_DIR", "GIT_WORK_TREE")
	out, err := cmd.Output()
	if err != nil {
		detail := err.Error()
		var ee *exec.ExitError
		if errors.As(err, &ee) && len(ee.Stderr) > 0 {
			detail = strings.TrimSpace(string(ee.Stderr))
		}
		return fmt.Errorf("git rev-parse --show-toplevel: %w: %s", err, detail)
	}
	top := strings.TrimSpace(string(out))
	if !sameDirPath(top, wtPath) {
		return fmt.Errorf("not the root of a git worktree: git resolves this directory to the repository at %q", top)
	}
	return nil
}

// envWithout returns env with the named variables removed.
func envWithout(env []string, drop ...string) []string {
	out := env[:0:0]
	for _, kv := range env {
		keep := true
		for _, d := range drop {
			if strings.HasPrefix(kv, d+"=") {
				keep = false
				break
			}
		}
		if keep {
			out = append(out, kv)
		}
	}
	return out
}

// sameDirPath compares two directory paths after making both absolute and
// resolving symlinks.
//
// A plain string compare would be wrong in three ways: git prints an ABSOLUTE
// PHYSICAL path, while wtPath is built by joining POLYFORGE_WORKSPACE_ROOT as
// the caller gave it — which can be relative (config.FindWorkspaceRoot returns
// "." when os.Getwd fails) and can run through a symlink. On macOS a
// t.TempDir() under /var is /private/var to git, so a string compare there
// would declare every healthy worktree broken. filepath.EvalSymlinks does not
// absolutize, which is why Abs runs first.
func sameDirPath(a, b string) bool {
	if a == b {
		return true
	}
	ra, errA := absReal(a)
	rb, errB := absReal(b)
	if errA != nil || errB != nil {
		return false
	}
	return ra == rb
}

func absReal(p string) (string, error) {
	abs, err := filepath.Abs(p)
	if err != nil {
		return "", err
	}
	return filepath.EvalSymlinks(abs)
}

// writeWorktreeExcludes adds polyforge scratch-file patterns to a worktree's
// per-worktree git exclude file, so they never get accidentally staged.
// Best-effort and non-fatal: a worktree must still be usable if this fails.
func writeWorktreeExcludes(wtPath string) {
	out, err := exec.Command("git", "-C", wtPath, "rev-parse", "--git-path", "info/exclude").Output()
	if err != nil {
		return
	}
	excludePath := strings.TrimSpace(string(out))
	if excludePath == "" {
		return
	}
	if !filepath.IsAbs(excludePath) {
		excludePath = filepath.Join(wtPath, excludePath)
	}

	patterns := []string{".superpowers/", ".pf_meta.json", ".pf_steps.json"}

	existing := ""
	if b, err := os.ReadFile(excludePath); err == nil {
		existing = string(b)
	}
	existingLines := strings.Split(existing, "\n")
	have := make(map[string]bool, len(existingLines))
	for _, l := range existingLines {
		have[strings.TrimSpace(l)] = true
	}

	var toAdd []string
	for _, p := range patterns {
		if !have[p] {
			toAdd = append(toAdd, p)
		}
	}
	if len(toAdd) == 0 {
		return
	}

	if err := os.MkdirAll(filepath.Dir(excludePath), 0o755); err != nil {
		return
	}
	f, err := os.OpenFile(excludePath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return
	}
	defer func() { _ = f.Close() }()
	if existing != "" && !strings.HasSuffix(existing, "\n") {
		_, _ = f.WriteString("\n")
	}
	for _, p := range toAdd {
		_, _ = f.WriteString(p + "\n")
	}
}

// generateSessionSecret generates a 64-hex random session secret.
func generateSessionSecret() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
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
		"goal":                   prop("string", "Single-line goal ≤500 chars"),
		"scenario":               prop("string", "Scenario (default: coding)"),
		"priority":               prop("string", "low|normal|high|urgent"),
		"wi_type":                prop("string", "Work item type (fix_bug, feature, chore, etc.)"),
		"requires_human_session": prop("boolean", "Whether this wi requires a human session"),
		"milestone":              prop("string", "Milestone name"),
		"labels":                 prop("array", "Labels"),
		"declared_resources":     declaredResourcesProp("Declared resource locks"),
		"parent_work_item_id":    prop("string", "Parent work item ID"),
		"source":                 prop("string", "Source reference"),
		"attrs":                  prop("object", "Additional attributes"),
		"blocked_by":             prop("array", "List of blocking work item IDs"),
		"content":                prop("string", contentPropDescription),
		"force_create":           prop("boolean", "Force create bypassing duplicate check"),
		"force_reason":           prop("string", "Reason for force create"),
	}
}

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
		` — entries are {"type","uri","intent"} plus an optional "repo" on path entries (aihub#261). NOTE: type takes a DECLARED type (repo/path/document/section/service/external_ref), NOT a lock type: file_scope/git_branch/worktree/tcp_port/deploy_env are resource_locks.resource_type values the server derives. A file path is type="path", uri="file:<repo-relative-path>". The path field is `+"`uri`"+`, not value/path/scope.`)
	p["items"] = map[string]any{
		"type": "object",
		"properties": map[string]any{
			"type": propEnum("string", "Declared resource type (NOT a lock type)", domain.DeclaredResourceTypeList()),
			"uri": prop("string",
				`Resource URI. Scheme by type: "file:<repo-relative-path>" for path/document/section, "repo:<repo-name>" for repo, "service:<name>" for service, a plain URL for external_ref.`),
			// Deliberately NOT an enum. The server does not validate `intent` at all;
			// only two values change behaviour ("read" suppresses the write lock and
			// downgrades path conflicts to info; "refactor" on a repo entry triggers
			// conflict rule 4). Meanwhile this repo's own fixtures use "exclusive" more
			// often than "write". Publishing a closed set would state a contract the
			// server does not keep — the exact failure this wi is about — so describe
			// the semantics instead and let unknown values through as inert. (aihub#238)
			"intent": prop("string",
				`Access intent. Not validated by the server; only two values carry behaviour: "read" (takes no write lock, and path overlaps report as info instead of soft_block) and "refactor" (on a repo entry, flags other refactors of the same repo). "write" is the conventional default; other values are accepted but inert.`),
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
			"base_branch": prop("string", "Base branch (repo entries only)"),
			"task_branch": prop("string", "Task branch (repo entries only); defaults to main for lock-key derivation"),
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
		` — usually OMIT this and let the server derive locks from the wi's declared_resources. Entries are {"resource_type","resource_key"} (NOT declared_resources' type/uri).`)
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

// resolveWorkspaceConfig returns the workspace config to build claim worktrees
// from, reading .polyforge.yaml fresh out of wsRoot rather than trusting the
// snapshot the MCP process loaded at startup.
//
// startupCfg (the server's s.cfg) is read once when the process starts, so a repo
// added to a project mid-session was silently missing from every subsequent claim
// in that session — the worktree loop iterates this config, and users saw a claim
// come back short without any error (aihub#228).
//
// startupCfg remains the fallback for two cases: the fresh read failing, and the
// server having started without POLYFORGE_WORKSPACE_ROOT (cwd with no
// .polyforge.yaml ancestor), where s.cfg is nil but the caller has since resolved
// a usable wsRoot. Returns nil when neither source yields a config, which the
// caller treats as "skip worktree creation".
func resolveWorkspaceConfig(wsRoot string, startupCfg *config.Config) *config.Config {
	if cfg, err := config.Load(wsRoot); err == nil && cfg != nil {
		return cfg
	}
	return startupCfg
}

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

// attachWorktree puts an EXISTING branch into wtPath.
//
// Where the branch lives is decided here rather than carried in from the
// resolver, because this is the last moment before the command runs and
// refs/heads is the only authority on the question. A local head is checked out
// directly; otherwise the branch is materialised from origin. The "already
// exists" retry closes the remaining race — a concurrent claim, or a ref the
// resolver could not see — for which the alternative is no worktree at all.
//
// NOT HANDLED, deliberately: `fatal: '<b>' is already used by worktree at '<p>'`,
// which is what git 2.43 says when the branch is checked out in ANOTHER
// worktree. There is no recovery — a branch cannot be in two worktrees — so the
// error is returned and the claim handler logs it and skips that repo, which is
// the correct outcome rather than a gap.
func attachWorktree(srcPath, wtPath, branch string) error {
	if gitRefExists(srcPath, "refs/heads/"+branch) {
		return runGit(srcPath, "worktree", "add", wtPath, branch)
	}
	err := runGit(srcPath, "worktree", "add", "-b", branch, wtPath, "origin/"+branch)
	if err != nil && strings.Contains(err.Error(), "already exists") {
		return runGit(srcPath, "worktree", "add", wtPath, branch)
	}
	return err
}

// claimFetchTimeout bounds the ONE network call on this path.
//
// `git fetch origin` reaches a remote and has no timeout of its own: an SSH or
// git-daemon peer that accepts the connection and then never answers leaves it
// blocked on a read forever. This runs inside an MCP request, so an unbounded
// hang spends the caller's whole request budget on a step whose failure is
// already treated as non-fatal ten lines below. Same shape as aihub#316, which
// bounded an unbounded upstream call for the same reason.
//
// A var, not a const, solely so TestAddClaimWorktree_FetchIsBounded can shorten
// it; nothing outside tests assigns to it. ⚠️ That makes it a mutated package
// global, which is safe today only because no test in internal/mcp calls
// t.Parallel() — verified, not assumed. Whoever adds the first t.Parallel() here
// owns turning this into an explicit parameter or a per-call option.
var claimFetchTimeout = 90 * time.Second

// addClaimWorktree materialises wtPath as a git worktree of srcPath.
//
// It attaches to whatever branch resolveClaimBranch finds — on a fresh claim
// just as much as on a resume, see that function for why the mode must not gate
// it — and creates a branch off origin/main only when nothing matches.
//
// ctx is the MCP request context and bounds the fetch, and ONLY the fetch. The
// local git invocations are deliberately left uncancellable.
//
// ⚠️ THE STATED REASON FOR THAT HAS MOVED — recorded rather than quietly left in
// place, because it was the premise the decision was argued from. It used to be
// that the claim handler short-circuited on os.Stat(wtPath) alone, so a
// `worktree add` killed part-way through its checkout left a half-populated
// directory that every later claim adopted and never repaired. aihub#328 made
// that early return validate the directory with verifyClaimWorktree, so a
// half-built one is now refused instead of adopted, and "adopted forever" is no
// longer a consequence of cancelling here.
//
// They stay uncancellable on the weaker reason that survives: a refused
// directory leaves that repo with NO worktree until somebody clears it by hand.
// That is a visible, reported failure rather than a silent one, but it is still
// worse than not making the mess. A local command that outlives a cancelled
// request costs a few seconds of CPU.
func addClaimWorktree(ctx context.Context, srcPath, wtPath string, n claimBranchNames) error {
	if n.Branch == "" {
		return fmt.Errorf("empty branch name")
	}
	if existing := resolveClaimBranch(srcPath, n); existing != "" {
		if err := attachWorktree(srcPath, wtPath, existing); err != nil {
			return err
		}
		return clearBaseUpstream(ctx, srcPath, existing)
	}
	// Nothing to attach to: a genuinely new work item, or one whose branch was
	// deleted. Create it rather than failing and leaving the repo with no
	// worktree at all.

	// Sync the local clone from origin so the new branch starts from the latest
	// remote state, not a stale local HEAD. Non-fatal: a stale base is worse than
	// a fresh one but far better than no worktree, which is why bounding this is
	// safe as well as necessary.
	fetchCtx, cancel := context.WithTimeout(ctx, claimFetchTimeout)
	out, fetchErr := exec.CommandContext(fetchCtx, "git", "-C", srcPath, "fetch", "origin").CombinedOutput()
	cancel()
	if fetchErr != nil {
		fmt.Fprintf(os.Stderr, "polyforge: fetch origin in %s: %v: %s\n", srcPath, fetchErr, string(out))
	}

	// --no-track is what stops the new branch from being configured to push to
	// main; see clearBaseUpstream for the measurement and the consequence.
	// It goes before -b because `worktree add`'s option parser stops at the
	// first non-option argument, and -b takes the branch name as its value.
	err := runGit(srcPath, "worktree", "add", "--no-track", "-b", n.Branch, wtPath, "origin/main")
	if err == nil {
		return clearBaseUpstream(ctx, srcPath, n.Branch)
	}
	// Branch may already exist (a racing retry of the same claim, or a ref
	// resolveClaimBranch could not see) — fall back to attach.
	//
	// ⚠️ "already checked out" is DEAD TEXT and predates aihub#322. Verified
	// against git 2.43.0: `worktree add -b <b>` on an existing branch says
	// `fatal: a branch named '<b>' already exists`, and the checked-out-elsewhere
	// case says `is already used by worktree at '<path>'` — neither contains the
	// string. Left in place rather than removed: it is harmless, it may match an
	// older or newer git, and deleting it is a behaviour change on a path this
	// work item is not about. The live matcher is "already exists".
	if strings.Contains(err.Error(), "already exists") || strings.Contains(err.Error(), "already checked out") {
		if err := runGit(srcPath, "worktree", "add", wtPath, n.Branch); err != nil {
			return err
		}
		return clearBaseUpstream(ctx, srcPath, n.Branch)
	}
	return err
}

// clearBaseUpstream drops an upstream pointing at a protected branch from the
// task branch this claim just materialised (aihub#257).
//
// It runs on all three exits of addClaimWorktree, and each one needs it for a
// different reason:
//
//   - on the CREATE path it is DEFENCE IN DEPTH and nothing more. --no-track
//     wins over branch.autoSetupMerge=always — measured on git 2.43.0, the
//     branch comes out with no upstream — so on any git that accepts the flag
//     this call has no reachable behaviour. It is kept because the invariant
//     worth holding is "the branch is not configured to push to main" rather
//     than "the flag was passed", and a git too old for --no-track would break
//     the flag and not the check. ⚠️ An earlier draft of this comment claimed
//     autoSetupMerge=always was a second live reason. It is not; it was written
//     from reasoning and disproved by measuring.
//   - the two ATTACH paths take a branch that already exists, and every task
//     branch created before this change carries upstream=origin/main. This is
//     the exit that repairs, and it is deliberately NOT a workspace-wide sweep
//     — see `polyforge doctor`'s branch-upstream check, which reports the rest
//     and repairs nothing.
//
// ⚠️ THE ATTACH EXITS ARE NOT WHERE MOST OF THE DAMAGE IS. An existing task
// worktree has a directory on disk, so the claim handler's os.Stat reuse fires
// and addClaimWorktree is never called at all. repairReusedWorktreeUpstream
// covers that path; this function covers only the case where the branch
// survived but its directory did not.
//
// ctx has its cancellation stripped: the surrounding claim path deliberately
// runs local git uncancellably (see addClaimWorktree), and a repair that failed
// with "context canceled" after `worktree add` had already succeeded would fail
// the whole claim for that repo and drop a healthy worktree from the state file.
//
// A genuine failure IS returned rather than swallowed: it means `git branch
// --unset-upstream` failed on a branch that reported having an upstream, which
// is a broken repo, not a routine outcome. Nothing here fails merely because
// the branch has no upstream — GitClearProtectedUpstream reports that as
// "nothing to clear".
func clearBaseUpstream(ctx context.Context, srcPath, branch string) error {
	cleared, err := coding.GitClearProtectedUpstream(context.WithoutCancel(ctx), srcPath, branch)
	if err != nil {
		return err
	}
	if cleared != "" {
		fmt.Fprintf(os.Stderr, "polyforge: %s tracked %s and would have pushed there; upstream cleared\n", branch, cleared)
	}
	return nil
}

// repairReusedWorktreeUpstream clears a protected upstream from the branch an
// ALREADY-EXISTING worktree has checked out (aihub#257).
//
// It reads the branch from the WORKTREE rather than using the name this claim
// computed. A reused directory is routinely on a name today's derivation does
// not produce — a pre-aihub#322 polyforge/<ulid8>, or a name from before the
// work item's goal was edited — and unsetting the upstream of the name we
// happen to have computed would either do nothing or touch an unrelated branch.
// The worktree's own HEAD is the only authority on what is checked out in it.
//
// Failure is logged, never propagated. The caller has already decided this
// worktree is healthy and is about to record it in the state file; a claim must
// not be downgraded to "no worktree for this repo" because a repair of a
// pre-existing condition did not work out.
func repairReusedWorktreeUpstream(ctx context.Context, srcPath, wtPath string) {
	out, err := exec.Command("git", "-C", wtPath, "symbolic-ref", "--quiet", "--short", "HEAD").Output()
	if err != nil {
		// Detached HEAD (exit 1) or an unreadable worktree. Neither has a branch
		// whose upstream could send anything anywhere.
		return
	}
	branch := strings.TrimSpace(string(out))
	if branch == "" {
		return
	}
	if err := clearBaseUpstream(ctx, srcPath, branch); err != nil {
		fmt.Fprintf(os.Stderr, "polyforge: could not clear %s's upstream in %s: %v\n", branch, srcPath, err)
	}
}

// runGit runs a local git command in srcPath, folding its combined output into
// the error so callers can both report it and match on it. Every invocation is
// local — `worktree add` resolves origin/<x> from a remote-tracking ref and does
// not reach the network — so there is nothing here for a timeout to protect; see
// addClaimWorktree for why these are deliberately not cancellable.
func runGit(srcPath string, args ...string) error {
	full := append([]string{"-C", srcPath}, args...)
	out, err := exec.Command("git", full...).CombinedOutput()
	if err != nil {
		return fmt.Errorf("git %s: %v: %s", strings.Join(args, " "), err, strings.TrimSpace(string(out)))
	}
	return nil
}
