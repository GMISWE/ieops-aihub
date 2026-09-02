package mcp

// aihub#241 defect B1: pf_update_work_item published `resources_version` as
// JSON-schema type "string", but the field it lands in is
// domain.UpdateWorkItemRequest.ResourcesVersion — an *int, backed by an INT
// column. The MCP handler forwards its arguments into the PATCH body verbatim,
// so the value arrived at echo's c.Bind as `"0"` and every request carrying it
// died as 400 BAD_REQUEST "invalid request body", two layers away from the
// mistake and with a message that named nothing.
//
// The reporter's observation that BOTH the numeric and the quoted form failed
// is consistent with a client coercing its argument to match the declared
// schema type — which is exactly why fixing the schema alone is not enough. The
// schema is a contract, but a mixed-version client (or a human calling the tool
// by hand) can still send the quoted form, so the handler coerces as well.
// These tests pin both halves, plus the wiring between them: a coercion helper
// nobody calls fixes nothing (mem_1SJ12mCz).

import (
	"encoding/json"
	"os"
	"regexp"
	"strings"
	"testing"
)

// typeOfProp reads properties.<name>.type out of a rendered tool input schema.
func typeOfProp(t *testing.T, raw json.RawMessage, propName string) string {
	t.Helper()
	var schema struct {
		Properties map[string]struct {
			Type        string `json:"type"`
			Description string `json:"description"`
		} `json:"properties"`
	}
	if err := json.Unmarshal(raw, &schema); err != nil {
		t.Fatalf("input schema is not valid JSON: %v", err)
	}
	p, ok := schema.Properties[propName]
	if !ok {
		t.Fatalf("schema has no property %q", propName)
	}
	return p.Type
}

func TestResourcesVersionPropIsInteger(t *testing.T) {
	schema := objectSchema(map[string]any{
		"resources_version": prop("integer", "probe"),
	}, nil)
	if got := typeOfProp(t, schema, "resources_version"); got != "integer" {
		t.Errorf("prop rendered type %q, want integer", got)
	}
}

// The registration itself is what ships. The go-sdk exposes no way to enumerate
// registered tools, so scan the source — scoped to the whole package, per the
// lesson that a tripwire narrower than the invariant it guards reads as
// coverage without being any (mem_I98xpPgY).
func TestResourcesVersionIsNeverRegisteredAsString(t *testing.T) {
	src := packageSource(t)
	bad := regexp.MustCompile(`"resources_version"\s*:\s*prop\(\s*"(string|number)"`)
	if m := bad.FindString(src); m != "" {
		t.Errorf("resources_version is published as a non-integer type (%s) — it binds to an *int and an INT column, so every request carrying it 400s at c.Bind (aihub#241 B1)", m)
	}
	if !regexp.MustCompile(`"resources_version"\s*:\s*prop\(\s*"integer"`).MatchString(src) {
		t.Error("no resources_version property is registered as \"integer\" — did the tool registration move? update this guard")
	}
}

// The description is the only place a caller learns that this is a CAS guard
// and what failing it looks like. aihub#238's lesson: a schema that understates
// or misstates its contract is the same defect class as one that says nothing.
func TestResourcesVersionDescriptionExplainsCAS(t *testing.T) {
	src := packageSource(t)
	m := regexp.MustCompile(`"resources_version"\s*:\s*prop\(\s*"integer",\s*"([^"]*)"`).FindStringSubmatch(src)
	if m == nil {
		t.Fatal("could not read the resources_version description — update this guard")
	}
	desc := m[1]
	for _, want := range []string{"409", "CONFLICT_CAS_FAILED"} {
		if !strings.Contains(desc, want) {
			t.Errorf("description does not tell the caller what a failed CAS returns (%q missing); got %q", want, desc)
		}
	}
	// aihub#337 replaced the previous `strings.Contains(desc, "Omit")` here. That
	// assertion demanded the very sentence the work item exists to delete: the
	// description used to end by instructing the caller to omit the field in
	// order to overwrite unconditionally, i.e. it listed the unsafe path as a
	// sanctioned option, and callers act on documented options. (The sentence is
	// not quoted verbatim anywhere in internal/ on purpose — aihub#337's
	// acceptance check is a grep for it, and a guard that trips its own criterion
	// wastes the next reader's time. `git show b4ed4f5` has the original.)
	// aihub#260 reproduced the consequence on the twin parameter over real HTTP
	// against real Postgres — one writer omitting the version silently discards
	// the other's whole list while both get a 200 — and for declared_resources
	// that list is what the server derives this work item's locks from.
	//
	// TWO assertions, because either alone is satisfied by the wrong text:
	//
	//   - the positive alone was green on the pre-fix wording, which stated the
	//     behaviour in exactly the offending imperative;
	//   - the negative alone is green if somebody simply DELETES the sentence,
	//     which is worse than the defect it removes. The behaviour is unchanged
	//     (aihub#337 is a docs change on purpose — requiring the version would be
	//     a breaking API change), so a description that hides it leaves a caller
	//     who omits the field with no warning at all.
	//
	// The negative is the discriminating half; the positive is what makes the
	// cheapest way of passing it cost as much as complying.
	if !strings.Contains(desc, "overwrites unconditionally") {
		t.Errorf("description no longer states that leaving the version out overwrites unconditionally — the behaviour did not change, and hiding it is worse than advertising it (aihub#337); got %q", desc)
	}
	if m := regexp.MustCompile(`(?i)\bomit`).FindString(desc); m != "" {
		t.Errorf("description says %q — it is offering the unsafe path as an option again. State the behaviour without the imperative, the way aihub#260 did on members_version (\"Leaving it out overwrites unconditionally: ...\"); got %q", m, desc)
	}
}

// TestNoToolDescriptionAdvertisesUnconditionalOverwrite is aihub#337's first
// acceptance criterion, as a test rather than as a grep somebody has to remember
// to run: no tool description in internal/ may instruct the caller to omit a
// compare-and-set version in order to overwrite unconditionally.
//
// Scoped to this package rather than to the one parameter above, because the
// defect travelled once already — aihub#260 fixed this exact sentence on
// pf_update_project's members_version in internal/mcp/tools_projects.go and left
// the identical one on pf_update_work_item's resources_version, which is why
// aihub#337 exists at all. The next compare-and-set parameter to be added will
// be copied from one of them.
func TestNoToolDescriptionAdvertisesUnconditionalOverwrite(t *testing.T) {
	// A PATTERN, not the one 33-character sentence the pre-fix description ended
	// with. That literal is one spelling of an unbounded class, and two cheap
	// rewrites escape it while reintroducing the defect exactly: a paraphrase
	// ("Omitting it overwrites unconditionally", "Leave it out to overwrite
	// unconditionally"), and the same sentence split across a Go `+`
	// concatenation — which is the prevailing style for long descriptions in
	// tools_lifecycle.go, so it is the likely accident rather than an exotic one.
	//
	// Both are handled: `" + "` joins are stitched back together and whitespace
	// is collapsed before matching, and the regex is the SHAPE of the offer —
	// an imperative "omit/leave out" within a short distance of "unconditional".
	src := packageSource(t)
	joined := regexp.MustCompile(`"\s*\+\s*"`).ReplaceAllString(src, "")
	joined = regexp.MustCompile(`\s+`).ReplaceAllString(joined, " ")

	offer := regexp.MustCompile(`(?i)\b(omit|leave (it |them )?out)\b[^"]{0,40}\bunconditional`)
	if m := offer.FindString(joined); m != "" {
		t.Errorf("a tool description in this package offers omitting a compare-and-set version as a way to overwrite unconditionally: %q. Every such guard in polyforge is opt-in, so the only thing a caller learns from that phrasing is that skipping it is allowed — it is not, and aihub#260 reproduced the silent data loss over real HTTP against real Postgres. State the behaviour without the imperative: \"Leaving it out overwrites unconditionally: ...\" is aihub#260's wording.", m)
	}
}

// The wiring guard. Asserting the mere presence of `normalizeIntArg(` would
// stay green if the call were neutered to `_ = normalizeIntArg(...)`, the exact
// false pass aihub#238's review found. Assert the full error-checked form.
func TestUpdateWorkItemHandlerCoercesResourcesVersion(t *testing.T) {
	b, err := os.ReadFile("tools_lifecycle.go")
	if err != nil {
		t.Fatalf("read tools_lifecycle.go: %v", err)
	}
	src := regexp.MustCompile(`\s+`).ReplaceAllString(string(b), " ")

	// Scope to the pf_update_work_item registration: a call in a neighbouring
	// handler must not satisfy this.
	start := strings.Index(src, `Name: "pf_update_work_item"`)
	if start < 0 {
		t.Fatal("could not locate the pf_update_work_item registration — update this guard")
	}
	rest := src[start+len(`Name: "pf_update_work_item"`):]
	if next := strings.Index(rest, `Name: "pf_`); next >= 0 {
		rest = rest[:next]
	}

	if !strings.Contains(rest, `if err := normalizeIntArg(args, "resources_version"); err != nil {`) {
		t.Error(`pf_update_work_item does not error-check normalizeIntArg(args, "resources_version") — a quoted version reaches the server as a JSON string and 400s at c.Bind with a message that names nothing (aihub#241 B1)`)
	}
	// The coercion must happen BEFORE the body is assembled, or it rewrites a
	// map nobody reads afterwards.
	coerceAt := strings.Index(rest, `normalizeIntArg(args, "resources_version")`)
	bodyAt := strings.Index(rest, `body := make(map[string]any)`)
	if bodyAt < 0 {
		t.Fatal("could not locate the body assembly — update this guard")
	}
	if coerceAt > bodyAt {
		t.Error("resources_version is coerced AFTER the request body is built, so the body still carries the uncoerced value (aihub#241 B1)")
	}
}

func packageSource(t *testing.T) string {
	t.Helper()
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatalf("readdir: %v", err)
	}
	var sb strings.Builder
	scanned := 0
	for _, e := range entries {
		name := e.Name()
		if !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		b, readErr := os.ReadFile(name)
		if readErr != nil {
			t.Fatalf("read %s: %v", name, readErr)
		}
		scanned++
		sb.Write(b)
		sb.WriteString("\n")
	}
	if scanned == 0 {
		t.Fatal("scanned no source files — the guard is not actually running")
	}
	return regexp.MustCompile(`\s+`).ReplaceAllString(sb.String(), " ")
}

func TestNormalizeIntArg(t *testing.T) {
	cases := []struct {
		name    string
		in      any
		present bool
		want    any
		wantErr string
	}{
		// encoding/json decodes every JSON number into float64, so this is what
		// a well-behaved client's `resources_version: 0` actually looks like.
		{name: "json number", in: float64(0), present: true, want: 0},
		{name: "json number nonzero", in: float64(12), present: true, want: 12},
		// The form that produced the 400. It must now be accepted, not merely
		// rejected with a better message: the reporter hit it from a client that
		// coerced to the declared schema type, and older clients still will.
		{name: "quoted", in: "0", present: true, want: 0},
		{name: "quoted nonzero", in: "12", present: true, want: 12},
		{name: "quoted padded", in: " 7 ", present: true, want: 7},
		{name: "already int", in: 5, present: true, want: 5},
		{name: "negative", in: float64(-1), present: true, want: -1},

		{name: "absent", present: false},
		{name: "explicit null", in: nil, present: true, want: nil},

		{name: "fractional", in: 1.5, present: true, wantErr: "must be an integer"},
		// float64 -> int conversion is implementation-defined out of range, so a
		// huge number must be rejected rather than silently bound as garbage.
		{name: "out of range", in: 1e20, present: true, wantErr: "out of range"},
		{name: "non-numeric string", in: "latest", present: true, wantErr: "must be an integer"},
		{name: "empty string", in: "", present: true, wantErr: "must be an integer"},
		{name: "bool", in: true, present: true, wantErr: "must be an integer"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			args := map[string]any{}
			if tc.present {
				args["resources_version"] = tc.in
			}
			err := normalizeIntArg(args, "resources_version")

			if tc.wantErr != "" {
				if err == nil {
					t.Fatalf("want error containing %q, got nil (value would reach the server unchanged)", tc.wantErr)
				}
				if !strings.Contains(err.Error(), tc.wantErr) {
					t.Errorf("error = %q, want it to contain %q", err, tc.wantErr)
				}
				// The message must name the field; the whole point is that the
				// old 400 named nothing.
				if !strings.Contains(err.Error(), "resources_version") {
					t.Errorf("error %q does not name the offending field", err)
				}
				return
			}

			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if !tc.present {
				if _, ok := args["resources_version"]; ok {
					t.Error("an absent key was materialised into the args map, which would send resources_version on every update and make CAS accidentally mandatory")
				}
				return
			}
			got := args["resources_version"]
			if got != tc.want {
				t.Fatalf("args[resources_version] = %#v, want %#v", got, tc.want)
			}
			if tc.want != nil {
				// Must be a Go int (or already-int), so json.Marshal emits a
				// bare number. A string here is the original bug.
				if _, isInt := got.(int); !isInt {
					t.Fatalf("value is %T, not int — it would serialize as a JSON string again", got)
				}
			}
		})
	}
}

// End-to-end on the serialization that actually matters: what the request body
// looks like on the wire.
func TestNormalizeIntArgProducesJSONNumber(t *testing.T) {
	for _, in := range []any{"0", float64(0), 0} {
		args := map[string]any{"resources_version": in}
		if err := normalizeIntArg(args, "resources_version"); err != nil {
			t.Fatalf("normalizeIntArg(%#v): %v", in, err)
		}
		b, err := json.Marshal(args)
		if err != nil {
			t.Fatalf("marshal: %v", err)
		}
		if got := string(b); got != `{"resources_version":0}` {
			t.Errorf("body for %#v is %s, want {\"resources_version\":0} — a quoted value fails c.Bind into *int with 400 (aihub#241 B1)", in, got)
		}
	}
}
