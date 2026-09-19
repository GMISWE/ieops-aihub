package mcp_test

// tools_skills_test.go — the aihub#720 slice D arms: the four read-only
// skill-registry tools' published schema, their wire behaviour, and the
// required-argument refusals that happen before any request is issued.
//
// What is deliberately NOT re-proved here: that every published parameter
// leaves this process and lands on a route the server registers is the
// universal contract gate's job (universal_contract_gate_test.go, G1/G2), which
// quantifies over every tool the day it is added; and that unknown arguments
// are disclosed is unknown_params_test.go's. These arms hold the claims that
// are specific to the skill surface: the minimal schema, the read-only wire
// (GET and only GET), the required-refusal boundary, and the exact-version
// request that never falls back to latest.
//
// No database:
//
//	GOWORK=off go test ./internal/mcp/ -run 'TestSkill|TestGetSkillVersion|TestListSkills' -count=1 -v

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

// skillReadTools is the slice-D surface: tool name → the exact property set its
// published schema may carry, and the required subset. A property outside this
// map (or a missing one) is a contract change this arm reports, because the
// cards pin the same set word for word.
var skillReadTools = map[string]struct {
	props    []string
	required []string
}{
	"pf_list_skills":         {props: []string{"owner", "cursor", "limit"}, required: nil},
	"pf_get_skill":           {props: []string{"skill_id"}, required: []string{"skill_id"}},
	"pf_list_skill_versions": {props: []string{"skill_id"}, required: []string{"skill_id"}},
	"pf_get_skill_version":   {props: []string{"skill_id", "version"}, required: []string{"skill_id", "version"}},
}

// TestSkillToolsPublishExactlyTheReadSchema reads each tool's schema off a real
// tools/list (the same instrument the contract cards hash) and pins the
// property set AND the required set: skill_id is required wherever it is
// published, version is required beside it, and pf_list_skills publishes three
// OPTIONAL parameters and nothing else.
func TestSkillToolsPublishExactlyTheReadSchema(t *testing.T) {
	for name, want := range skillReadTools {
		t.Run(name, func(t *testing.T) {
			tool := publishedTool(t, name)
			props := publishedSchemaProps(t, name)
			if len(props) != len(want.props) {
				t.Errorf("%s publishes %d property/properties (%v), want exactly %d (%v) — the "+
					"schema is minimal by design, and the cards pin this set verbatim",
					name, len(props), mapKeys(props), len(want.props), want.props)
			}
			for _, p := range want.props {
				if _, ok := props[p]; !ok {
					t.Errorf("%s does not publish %q (published: %v)", name, p, mapKeys(props))
				}
			}
			raw, err := json.Marshal(tool.InputSchema)
			if err != nil {
				t.Fatalf("marshal %s InputSchema: %v", name, err)
			}
			var schema struct {
				Required []string `json:"required"`
			}
			if err := json.Unmarshal(raw, &schema); err != nil {
				t.Fatalf("decode %s InputSchema: %v", name, err)
			}
			if strings.Join(schema.Required, ",") != strings.Join(want.required, ",") {
				t.Errorf("%s required = %v, want %v", name, schema.Required, want.required)
			}
		})
	}
}

// mapKeys renders a publishedSchemaProps map for a failure message.
func mapKeys(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

// TestSkillToolsIssueOnlyGETRequests drives all four tools against a fake aihub
// and pins the read-only claim on the WIRE: every request this family makes is
// a GET. The registry's write and share surface (POST/PATCH/DELETE) is
// deliberately not published, and this arm is what makes that a behavioural
// fact rather than a promise in a description.
func TestSkillToolsIssueOnlyGETRequests(t *testing.T) {
	f := newFakeAihub(t)
	calls := []struct {
		tool string
		args map[string]any
	}{
		{"pf_list_skills", nil},
		{"pf_get_skill", map[string]any{"skill_id": "skill_a"}},
		{"pf_list_skill_versions", map[string]any{"skill_id": "skill_a"}},
		{"pf_get_skill_version", map[string]any{"skill_id": "skill_a", "version": 2}},
	}
	for _, c := range calls {
		if _, isErr := callTool(t, f, c.tool, c.args); isErr {
			t.Fatalf("%s refused a well-formed call against a fake aihub answering {\"ok\":true}", c.tool)
		}
	}
	for _, rec := range f.recorded() {
		if rec.Method != http.MethodGet {
			t.Errorf("the skill read tools issued %s %s; the family is read-only and every "+
				"request must be a GET", rec.Method, rec.Path)
		}
	}
	if n := len(f.recorded()); n != len(calls) {
		t.Errorf("%d request(s) recorded for %d calls; one request per call, nothing more", n, len(calls))
	}
}

// TestSkillReadToolsRefuseMissingRequiredArgsBeforeAnyRequest pins the
// required-refusal boundary: an absent or empty skill_id (and an absent
// version) is refused IN THIS PROCESS, before any HTTP request is issued — the
// same shape as pf_get_workflow's work_item_id guard, and the load-bearing half
// of "skill_id/version 必填校验": a composer that forgot half its reference
// learns it immediately rather than as a server 404 naming a nonexistent skill.
func TestSkillReadToolsRefuseMissingRequiredArgsBeforeAnyRequest(t *testing.T) {
	cases := []struct {
		name string
		tool string
		args map[string]any
	}{
		{"pf_get_skill without skill_id", "pf_get_skill", nil},
		{"pf_get_skill with an empty skill_id", "pf_get_skill", map[string]any{"skill_id": ""}},
		{"pf_list_skill_versions without skill_id", "pf_list_skill_versions", nil},
		{"pf_list_skill_versions with an empty skill_id", "pf_list_skill_versions", map[string]any{"skill_id": ""}},
		{"pf_get_skill_version without skill_id", "pf_get_skill_version", map[string]any{"version": 1}},
		{"pf_get_skill_version without version", "pf_get_skill_version", map[string]any{"skill_id": "skill_a"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newFakeAihub(t)
			if _, isErr := callTool(t, f, tc.tool, tc.args); !isErr {
				t.Fatalf("%s accepted %v — the required argument is the parameter contract", tc.tool, tc.args)
			}
			if n := len(f.recorded()); n != 0 {
				t.Errorf("the refusal still issued %d HTTP request(s); a required-argument refusal "+
					"must happen before any request", n)
			}
		})
	}
}

// TestGetSkillVersionRefusesANonIntegralOrNonPositiveVersion pins the version
// guard's negative half: zero, negative, fractional and non-numeric versions are
// refused locally — an exact-version read has no fallback spelling, and
// forwarding a malformed one would hand the server a 400 this process could
// have answered without the round trip. A whole-number STRING is accepted
// (the aihub#280 tolerance), and that acceptance is pinned positively by the
// last arm of TestGetSkillVersionRequestsTheExactVersionSegment.
func TestGetSkillVersionRefusesANonIntegralOrNonPositiveVersion(t *testing.T) {
	// Astra review SF2 regression arms: the pre-fix parseIntArg parsed every
	// string through float64, so "1.0" and "1e0" were silently COERCED into
	// version 1. An exact-version argument must be an exact integer literal —
	// "2.0" is a refusal, not a different spelling of 2. (Whitespace padding
	// stays tolerated: the accept arms and the dedicated whitespace test pin
	// that no caller regresses on " 2".)
	for _, v := range []any{"2.0", "1e2", "0x1", int64(1) << 62} {
		t.Run("rejects "+jsonArgName(v), func(t *testing.T) {
			f := newFakeAihub(t)
			if _, isErr := callTool(t, f, "pf_get_skill_version", map[string]any{"skill_id": "skill_a", "version": v}); !isErr {
				t.Fatalf("pf_get_skill_version coerced version %#v; versions must be exact integers", v)
			}
			if n := len(f.recorded()); n != 0 {
				t.Errorf("the coerced version still issued %d HTTP request(s)", n)
			}
		})
	}
	for _, v := range []any{" 2", float64(2), int(2), int64(2)} {
		t.Run("accepts "+jsonArgName(v), func(t *testing.T) {
			f := newFakeAihub(t)
			if _, isErr := callTool(t, f, "pf_get_skill_version", map[string]any{"skill_id": "skill_a", "version": v}); isErr {
				t.Fatalf("pf_get_skill_version refused an exact integer %#v", v)
			}
			reqs := f.recorded()
			if len(reqs) != 1 || !strings.HasSuffix(reqs[0].Path, "/versions/2") {
				t.Fatalf("accepted version did not request /versions/2: %+v", reqs)
			}
		})
	}
}

func TestGetSkillVersionAcceptsWhitespacePaddedInteger(t *testing.T) {
	// " 2" travels with the trim the old float parser gave it; the exact-int
	// parser keeps the same tolerance so no caller regresses, but only for
	// literal whitespace — "2.0" must NOT pass (arm above).
	f := newFakeAihub(t)
	if _, isErr := callTool(t, f, "pf_get_skill_version", map[string]any{"skill_id": "skill_a", "version": " 2"}); isErr {
		t.Fatal("pf_get_skill_version refused a whitespace-padded exact integer")
	}
}

func TestGetSkillVersionRefusesANonIntegralOrNonPositiveVersionOrig(t *testing.T) {
	for _, v := range []any{0, -1, 0.5, 7.25, "2.5", true, nil} {
		t.Run(jsonArgName(v), func(t *testing.T) {
			f := newFakeAihub(t)
			args := map[string]any{"skill_id": "skill_a", "version": v}
			if v == nil {
				delete(args, "version")
			}
			if _, isErr := callTool(t, f, "pf_get_skill_version", args); !isErr {
				t.Fatalf("pf_get_skill_version accepted version %#v; only whole numbers >= 1 are valid", v)
			}
			if n := len(f.recorded()); n != 0 {
				t.Errorf("the refusal still issued %d HTTP request(s)", n)
			}
		})
	}
}

// TestSkillToolsPassTheServerAnswerThroughUnprojected pins the hop-5 claim
// for the whole family: a key the server sends that nothing in this process
// has heard of still reaches the model, which is the same bar
// TestListUsersPassesTheServerAnswerThroughUnprojected holds for that tool —
// a keep-list here would silently strip registry keys the cards did not name.
func TestSkillToolsPassTheServerAnswerThroughUnprojected(t *testing.T) {
	payload := map[string]any{
		"items": []any{}, "skill_id": "skill_a", "version": 3,
		"a_key_no_struct_knows": "keep-me",
	}
	for _, tool := range []string{"pf_list_skills", "pf_get_skill", "pf_list_skill_versions", "pf_get_skill_version"} {
		t.Run(tool, func(t *testing.T) {
			f := newFakeAihub(t)
			f.on("/v1/skills", func(map[string]any) (int, any) { return http.StatusOK, payload })
			f.on("/v1/skills/skill_a", func(map[string]any) (int, any) { return http.StatusOK, payload })
			f.on("/v1/skills/skill_a/versions", func(map[string]any) (int, any) { return http.StatusOK, payload })
			f.on("/v1/skills/skill_a/versions/3", func(map[string]any) (int, any) { return http.StatusOK, payload })
			args := map[string]any{"skill_id": "skill_a", "version": 3}
			if tool == "pf_list_skills" {
				args = nil
			}
			got, isErr := callTool(t, f, tool, args)
			if isErr {
				t.Fatalf("%s refused a well-formed call against a cooperating fake", tool)
			}
			if got["a_key_no_struct_knows"] != "keep-me" {
				t.Errorf("%s dropped a key no struct in this process knows about; the answer must "+
					"reach the model exactly as the server sent it (got %v)", tool, got)
			}
		})
	}
}

// jsonArgName renders a version literal for a subtest name.
func jsonArgName(v any) string {
	raw, err := json.Marshal(v)
	if err != nil {
		return "invalid"
	}
	if v == nil {
		return "absent"
	}
	return string(raw)
}

// TestGetSkillVersionRequestsTheExactVersionSegment pins the never-latest
// claim on the wire: version 7 produces a request for
// /v1/skills/<id>/versions/7 — one exact segment, no substitution, no
// latest-resolution. A skill id containing a space round-trips through the
// same path: the client escapes it (pkg/client seg), the fake records the
// DECODED path, and a broken escape would have split the segment and missed
// the route. The second arm pins the string spelling's acceptance: "9" as a
// JSON string reaches the same exact segment as the number does.
func TestGetSkillVersionRequestsTheExactVersionSegment(t *testing.T) {
	f := newFakeAihub(t)
	if _, isErr := callTool(t, f, "pf_get_skill_version",
		map[string]any{"skill_id": "skill a 1", "version": 7}); isErr {
		t.Fatal("pf_get_skill_version refused a well-formed exact-version call")
	}
	if _, isErr := callTool(t, f, "pf_get_skill_version",
		map[string]any{"skill_id": "skill_b", "version": "9"}); isErr {
		t.Fatal("pf_get_skill_version refused the whole-number string spelling (aihub#280 tolerance)")
	}
	paths := f.paths()
	if len(paths) != 2 {
		t.Fatalf("expected exactly two requests, got %v", paths)
	}
	if want := "/v1/skills/skill a 1/versions/7"; paths[0] != want {
		t.Errorf("request path = %q, want %q — the exact version must name its own segment, "+
			"never latest and never a neighbouring version", paths[0], want)
	}
	if want := "/v1/skills/skill_b/versions/9"; paths[1] != want {
		t.Errorf("string-spelled version produced path %q, want %q", paths[1], want)
	}
}

// TestListSkillsForwardsItsThreeOptionalParams pins the query half of
// pf_list_skills: owner, cursor and limit leave this process as query
// parameters, and absent parameters are OMITTED rather than sent empty — the
// server rejects any unknown query parameter on GET /v1/skills, and an empty
// value would be a self-inflicted refusal.
func TestListSkillsForwardsItsThreeOptionalParams(t *testing.T) {
	q := newQueryRecorder(t)
	callToolAgainstRecorder(t, q, "pf_list_skills", map[string]any{
		"owner": "u_probe", "cursor": "c_probe", "limit": 25,
	})
	got := q.last(t)
	for key, want := range map[string]string{"owner": "u_probe", "cursor": "c_probe", "limit": "25"} {
		if got.Get(key) != want {
			t.Errorf("%s = %q, want %q", key, got.Get(key), want)
		}
	}

	// The absent-parameter half: nothing is sent, nothing is invented.
	callToolAgainstRecorder(t, q, "pf_list_skills", nil)
	got = q.last(t)
	for _, key := range []string{"owner", "cursor", "limit"} {
		if got.Get(key) != "" {
			t.Errorf("an omitted %s still arrived as %q; absent parameters must be omitted, not sent empty", key, got.Get(key))
		}
	}
}
