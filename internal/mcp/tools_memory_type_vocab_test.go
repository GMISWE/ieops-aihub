package mcp

// aihub#445 (aihub#411 decision table §6.2 T2-6), the hop-1 half: pf_remember's
// `type` must NOT be published as a closed enum, and the description that
// replaced the enum must say what the server really enforces AND still carry the
// curated vocabulary.
//
// Both halves are needed and neither implies the other. Deleting the enum and
// saying nothing would leave a caller with no vocabulary at all, which invents
// type names instead of reusing them — the same unrenderable-row outcome by the
// other road. Keeping a suggestion list without marking it open would be the
// enum again in prose.
//
//	go test ./internal/mcp/ -run TestRememberType -v

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/GMISWE/ieops-aihub/internal/domain"
)

// rememberTypeProp returns pf_remember's `type` property as a decoded map, so a
// missing `enum` key can be told apart from an empty one.
func rememberTypeProp(t *testing.T) map[string]any {
	t.Helper()
	var schema struct {
		Properties map[string]map[string]any `json:"properties"`
	}
	if err := json.Unmarshal(rememberSchema(), &schema); err != nil {
		t.Fatalf("pf_remember InputSchema is not valid JSON: %v", err)
	}
	p, ok := schema.Properties["type"]
	if !ok {
		t.Fatal("pf_remember publishes no `type` property at all")
	}
	return p
}

// TestRememberTypeIsNotPublishedAsAClosedEnum is the withdrawal itself.
//
// The 13-value propEnum stated a closed contract that nothing kept, twice over:
// the server accepts any legal prefix and stores it, and this process would not
// have refused an off-enum value either — polyforge registers through the
// untyped (*mcp.Server).AddTool method, whose callTool path invokes the handler
// with no schema step (measured on go-sdk v1.6.0; written out in
// internal/domain/user_fields.go).
//
// This is the aihub#238 rule a second time — a value the server does not
// validate is not published as a closed enum, exactly as
// TestDeclaredResourcesProp_DescribesURIAndIntent requires of `intent` — and the
// inverse of aihub#463, where the vocabulary really was closed so the repair was
// to make the server enforce it. The principle is the same in both: the
// published set and the enforced set are one set.
func TestRememberTypeIsNotPublishedAsAClosedEnum(t *testing.T) {
	if raw, ok := rememberTypeProp(t)["enum"]; ok {
		t.Errorf("pf_remember.type publishes enum %v. The accepted set is a PREFIX rule "+
			"(domain.MemoryTypePrefixes, and since aihub#445 the memories_type_check CHECK), so it is "+
			"infinite and no list of names can state it. If the list is worth publishing, publish it "+
			"as a suggestion in the description — which is what memoryTypeParamDesc does.", raw)
	}
	if got, _ := rememberTypeProp(t)["type"].(string); got != "string" {
		t.Errorf("pf_remember.type is published as %q, want \"string\"", got)
	}
}

// TestRememberTypeDescriptionStatesWhatIsEnforced covers the other half: hop 1
// is the only thing an LLM caller sees, so withdrawing the enum without stating
// the rule that replaced it would leave the tool publishing LESS about `type`
// than before.
func TestRememberTypeDescriptionStatesWhatIsEnforced(t *testing.T) {
	desc, _ := rememberTypeProp(t)["description"].(string)
	if desc == "" {
		t.Fatal("pf_remember.type has no description; with the enum gone it now publishes nothing at all")
	}

	// Anti-vacuity: an empty domain vocabulary would make every Contains below
	// trivially true.
	if len(domain.MemoryTypePrefixes) == 0 || len(domain.PfRememberTypeEnum) == 0 {
		t.Fatalf("domain publishes %d prefixes and %d suggested types — one vocabulary is empty",
			len(domain.MemoryTypePrefixes), len(domain.PfRememberTypeEnum))
	}

	// Every prefix this TOOL accepts must be named. methodology. is legal for the
	// column and refused one layer up by validatePfRememberArgs, so it is the one
	// prefix that must NOT be offered — and must still be mentioned, or a caller
	// holding a spec learns nothing about where it goes.
	for _, p := range domain.MemoryTypePrefixes {
		if p == "methodology." {
			continue
		}
		if !strings.Contains(desc, p+"*") {
			t.Errorf("the description does not name the accepted prefix %q; got %q", p+"*", desc)
		}
	}
	if !strings.Contains(desc, "methodology.* is refused here") {
		t.Errorf("the description does not say methodology.* is refused by this tool; got %q", desc)
	}
	if !strings.Contains(desc, "pf_save_artifact") {
		t.Errorf("the description refuses methodology.* without naming where those go; got %q", desc)
	}

	// The '|' ban (aihub#289) is enforced on both the write and the read path and
	// was published on neither. A type containing one passes the prefix check, so
	// a caller cannot deduce the rule from the prefixes alone.
	if !strings.Contains(desc, "'|'") {
		t.Errorf("the description does not publish the '|' ban, which the prefix rule does not "+
			"imply — \"experience.*|rule.*\" starts with a legal prefix; got %q", desc)
	}

	// And the suggestion list, marked as open. "not a closed set" is asserted
	// verbatim because it is the entire difference between this description and
	// the enum it replaced.
	if !strings.Contains(desc, "not a closed set") {
		t.Errorf("the description lists suggested types without saying the list is open, which is "+
			"the enum again in prose; got %q", desc)
	}
	for _, v := range domain.PfRememberTypeEnum {
		if !strings.Contains(desc, v) {
			t.Errorf("the curated type %q was dropped from the description; withdrawing the enum must "+
				"not cost the caller the vocabulary", v)
		}
	}
}

// TestRememberTypeDescriptionIsDerivedNotRetyped guards the reason the two lists
// above are read from domain rather than written here.
//
// A description with the values typed out would be a fourth copy of a vocabulary
// that already exists three times, and it would stay green while all four
// drifted. Adding a name to MemoryTypeEnum must change what pf_remember
// publishes, with no second edit — so the test perturbs nothing and instead
// asserts the identity of the published text and the text the domain lists
// generate.
func TestRememberTypeDescriptionIsDerivedNotRetyped(t *testing.T) {
	desc, _ := rememberTypeProp(t)["description"].(string)
	if want := memoryTypeParamDesc(); desc != want {
		t.Fatalf("the published description is not the one the builder produces:\n got %q\nwant %q", desc, want)
	}
	if !strings.Contains(desc, strings.Join(domain.PfRememberTypeEnum, ", ")) {
		t.Errorf("the suggested list is not rendered from domain.PfRememberTypeEnum in order, so it "+
			"can drift from MemoryTypeEnum without this test noticing; got %q", desc)
	}
	// The 19/13 relationship, stated where a reader of the schema will look for
	// it: pf_remember publishes the UI select list minus the six methodology.*
	// entries. domain's TestPfRememberTypeEnum_NoMethodology owns the derivation
	// itself; this only pins that pf_remember is the tool it applies to.
	if len(domain.PfRememberTypeEnum) != len(domain.MemoryTypeEnum)-6 {
		t.Errorf("pf_remember suggests %d types and the UI select list holds %d; the published "+
			"suggestion is meant to be that list minus the six methodology.* entries",
			len(domain.PfRememberTypeEnum), len(domain.MemoryTypeEnum))
	}
}
