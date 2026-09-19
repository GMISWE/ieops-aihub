package mcp

// tools_skills.go — the skill-registry READ tools (aihub#720 slice D).
//
// Four read-only forwarding tools over the versioned skill registry's own GET
// surface (aihub#708 Batch 1A): a composer that has to name exact skill_id +
// skill_version pairs in a create-with-steps proposal needs to be able to
// DISCOVER what exists and what it can access, without leaving the MCP surface
// for a second client. The registry's write and share surface (create, publish,
// share, revoke, visibility) is deliberately NOT published here: composition is
// a read-then-propose act, and a write tool would let a caller widen registry
// access from inside a work item's session — the exact channel spec D3 keeps
// closed.
//
// Every handler forwards its arguments VERBATIM-shaped to pkg/client's existing
// skill methods and lets the server validate — the same rule
// tools_workflows.go states: a local pre-validation would refuse the universal
// contract gate's probe shape and read as a parameter that never leaves this
// process. The one exception is the required-argument REFUSAL (an empty
// skill_id, a non-positive version), which happens before any request exactly
// like pf_get_workflow's work_item_id guard: it is this process's own
// parameter contract, not a second copy of the server's rules.
//
// Visibility is the registry's own, applied by the server on every one of
// these calls: an unknown skill and an inaccessible one answer the same
// NOT_FOUND (the no-metadata-oracle rule), a list never offers a version the
// caller cannot read, and an exact version request never falls back to
// "latest" — version is required, and a mismatch is a refusal, not a
// substitution.

import (
	"context"
	"fmt"
	"net/url"

	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"
)

// registerSkillTools registers the four read-only skill-registry tools.
func (s *Server) registerSkillTools() {
	// pf_list_skills
	s.addTool(&sdkmcp.Tool{
		Name: "pf_list_skills",
		Description: "List skills and their latest ACCESSIBLE version metadata, scoped to the caller's registry view: " +
			"owned skills, versions shared with one of the caller's projects, and public versions. A skill with no " +
			"version the caller can read is not listed. Read-only registry discovery for composing a workflow proposal; " +
			"the registry's write and share surface is not published here. Supports owner, cursor and limit; the response " +
			"carries items and a next_cursor when another page exists.",
		InputSchema: objectSchema(map[string]any{
			"owner": prop("string", "Filter to one owner's user id. Absent lists every skill the caller can see."),
			"cursor": prop("string", "Page token from a previous response's next_cursor; absent starts from the "+
				"first page."),
			"limit": prop("number", "Page size, 1..200; 0 or absent means the default 50."),
		}, nil),
	}, func(ctx context.Context, req *sdkmcp.CallToolRequest) (*sdkmcp.CallToolResult, error) {
		args, err := parseArgs(req.Params.Arguments)
		if err != nil {
			return errResult(err)
		}
		params := buildListSkillsParams(args)
		result, err := s.client.ListSkills(ctx, params)
		if err != nil {
			return errResult(err)
		}
		return jsonResult(result)
	})

	// pf_get_skill
	s.addTool(&sdkmcp.Tool{
		Name: "pf_get_skill",
		Description: "Read one skill's identity and the caller's latest ACCESSIBLE version (version number, visibility, " +
			"digest, author). A skill the caller cannot see at all answers NOT_FOUND, the same as a missing one; an " +
			"owner sees the identity even before any version exists. Read-only; the response carries no skill content: " +
			"bundles and contracts are read per exact version with pf_get_skill_version.",
		InputSchema: objectSchema(map[string]any{
			"skill_id": prop("string", "Skill id (the `skill_`-prefixed registry id, as returned by pf_list_skills)"),
		}, []string{"skill_id"}),
	}, func(ctx context.Context, req *sdkmcp.CallToolRequest) (*sdkmcp.CallToolResult, error) {
		args, err := parseArgs(req.Params.Arguments)
		if err != nil {
			return errResult(err)
		}
		skillID := strArg(args, "skill_id")
		if skillID == "" {
			return errResult(fmt.Errorf("skill_id is required"))
		}
		result, err := s.client.GetSkill(ctx, skillID)
		if err != nil {
			return errResult(err)
		}
		return jsonResult(result)
	})

	// pf_list_skill_versions
	s.addTool(&sdkmcp.Tool{
		Name: "pf_list_skill_versions",
		Description: "List the versions of one skill that are ACCESSIBLE to the caller, summaries only (version, " +
			"visibility, digest, author), never content. Inaccessible versions are omitted rather than refused, and a " +
			"skill the caller cannot see at all answers NOT_FOUND. This is the enumeration a composer walks to pick an " +
			"exact skill_version for a workflow proposal.",
		InputSchema: objectSchema(map[string]any{
			"skill_id": prop("string", "Skill id (the `skill_`-prefixed registry id, as returned by pf_list_skills)"),
		}, []string{"skill_id"}),
	}, func(ctx context.Context, req *sdkmcp.CallToolRequest) (*sdkmcp.CallToolResult, error) {
		args, err := parseArgs(req.Params.Arguments)
		if err != nil {
			return errResult(err)
		}
		skillID := strArg(args, "skill_id")
		if skillID == "" {
			return errResult(fmt.Errorf("skill_id is required"))
		}
		result, err := s.client.ListSkillVersions(ctx, skillID)
		if err != nil {
			return errResult(err)
		}
		return jsonResult(result)
	})

	// pf_get_skill_version
	s.addTool(&sdkmcp.Tool{
		Name: "pf_get_skill_version",
		Description: "Read one EXACT accessible version, including its immutable bundle and runtime contract: the " +
			"content a composer needs to judge what a step publishes and consumes. The version is required and never " +
			"falls back to latest: naming a version the caller cannot read answers NOT_FOUND, the same as a missing " +
			"one, and no other version is substituted. Read-only.",
		InputSchema: objectSchema(map[string]any{
			"skill_id": prop("string", "Skill id (the `skill_`-prefixed registry id, as returned by pf_list_skills)"),
			"version": prop("number", "Exact version number, a positive integer (never latest; use pf_get_skill for "+
				"the caller's latest accessible version)"),
		}, []string{"skill_id", "version"}),
	}, func(ctx context.Context, req *sdkmcp.CallToolRequest) (*sdkmcp.CallToolResult, error) {
		args, err := parseArgs(req.Params.Arguments)
		if err != nil {
			return errResult(err)
		}
		skillID := strArg(args, "skill_id")
		if skillID == "" {
			return errResult(fmt.Errorf("skill_id is required"))
		}
		version, err := skillVersionArg(args)
		if err != nil {
			return errResult(err)
		}
		result, err := s.client.GetSkillVersion(ctx, skillID, version)
		if err != nil {
			return errResult(err)
		}
		return jsonResult(result)
	})
}

// buildListSkillsParams forwards pf_list_skills' three optional parameters as
// query params, omitting the absent ones: the server rejects ANY unknown query
// parameter on GET /v1/skills, so forwarding an empty value would be a
// self-inflicted 400, and the distinction "absent vs empty" is not one the
// registry gives a meaning to anyway.
//
// scalarArg rather than strArg for limit, matching buildListWorkItemsParams'
// aihub#280 B6 rule: the parameter is published as a number but callers send
// strings too, and dropping a non-string silently would read as "unfiltered".
func buildListSkillsParams(args map[string]any) url.Values {
	params := url.Values{}
	for _, k := range []string{"owner", "cursor", "limit"} {
		setIfNonempty(params, k, scalarArg(args, k))
	}
	return params
}

// skillVersionArg reads and validates pf_get_skill_version's required version:
// present, numeric, integral and >= 1. Refused HERE rather than forwarded,
// because the refusal is about the published parameter contract (an absent or
// fractional version is a caller mistake the schema already says is wrong) —
// while the ACCESS half of "which versions exist" stays entirely the server's,
// which is why an unreadable version is forwarded and answered NOT_FOUND by
// the registry rather than guessed at locally.
//
// Decoding goes through helpers.go's parseIntArg (not a local strconv call):
// package mcp's numeric conversions live in one file so the queryparam gate can
// hold the refusal-naming-the-parameter policy on this hop too, and the
// string spelling a real caller sends is accepted there deliberately (the
// aihub#280 tolerance) while every non-whole spelling is refused before any
// request.
func skillVersionArg(args map[string]any) (int, error) {
	v, present, ok := parseIntArg(args, "version")
	if !present {
		return 0, fmt.Errorf("version is required")
	}
	if !ok {
		return 0, fmt.Errorf("version must be a whole number, got %#v", args["version"])
	}
	if v < 1 {
		return 0, fmt.Errorf("version must be a positive integer (>= 1), got %v", v)
	}
	return v, nil
}
