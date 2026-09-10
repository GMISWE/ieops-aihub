package domain

// aihub#543 probe wave 2 — the two halves of the render-set clause in
// `docs/mcp-cards/pf_remember.md`'s ⚠️ visibility bullet.
//
//	"…or a type in the render set, which `defaultRenderTypes` makes the
//	 `methodology.*` names this tool refuses"
//	    -> TestDefaultRenderTypesAreOnlyTypesPfRememberRefuses
//	"They are not a promise: the render set is configurable at startup
//	 (`InitRenderTypes`), so the conjunct is where an honest published claim stops"
//	    -> TestInitRenderTypesAdmitsATypePfRememberAccepts
//
// 🔴 The existing arms next door check MEMBERSHIP — "methodology.review is in
// the default set", "the three aihub#81 types are in it". Membership is the
// wrong direction for this card: the sentence's claim is that NOTHING ELSE is in
// it, i.e. that no type pf_remember accepts is renderable by default. A seventh
// default entry named `experience.report` would satisfy every existing arm and
// make the card's conjunct false the same day.
//
//	GOWORK=off go test ./internal/domain/ -run 'TestDefaultRenderTypesAreOnly|TestInitRenderTypesAdmits' -count=1

import (
	"strings"
	"testing"
)

// TestDefaultRenderTypesAreOnlyTypesPfRememberRefuses is the closure half.
//
// It quantifies over defaultRenderTypes and requires every entry to carry
// MethodologyTypePrefix — which is exactly the prefix validatePfRememberArgs
// refuses, so "no pf_remember row is renderable by default" follows from the two
// together rather than being asserted.
//
// MUTANTS:
//
//	M28 enforcement: add `experience.report` to defaultRenderTypes
//	                                          RED  names the entry
//	M29 enforcement: set defaultRenderTypes to "" (parseRenderTypes then falls
//	    back, but the raw constant is what the walk reads)
//	                                          RED  the floor — 0 entries
//	M30 publication: delete the citation from the card bullet
//	                                          RED  K12
func TestDefaultRenderTypesAreOnlyTypesPfRememberRefuses(t *testing.T) {
	// floorDefaultRenderTypes bounds the walk. An empty constant makes "every
	// entry is methodology.*" vacuously true, and vacuously true is the same
	// green as a set that is genuinely closed.
	const floorDefaultRenderTypes = 6

	entries := []string{}
	for _, e := range strings.Split(defaultRenderTypes, ",") {
		if e = strings.TrimSpace(e); e != "" {
			entries = append(entries, e)
		}
	}
	if len(entries) < floorDefaultRenderTypes {
		t.Fatalf("defaultRenderTypes parses to %d entr(ies) (%q), floor is %d — a walk over an "+
			"empty set satisfies the loop below without reading anything",
			len(entries), defaultRenderTypes, floorDefaultRenderTypes)
	}

	for _, e := range entries {
		if strings.HasPrefix(e, MethodologyTypePrefix) {
			continue
		}
		t.Errorf("defaultRenderTypes carries %q, which does not start with %q.\n\n"+
			"pf_remember refuses only the %s prefix (validatePfRememberArgs), so a default "+
			"render type outside it is a type pf_remember ACCEPTS and hasRenderableBody then "+
			"returns true for — on a default deployment, with `visibility: public`, that row is "+
			"served by GET /share/:id with no auth. The pf_remember card's ⚠️ bullet says the "+
			"two conditions cannot both hold that way; adding this entry is one of the two ways "+
			"to make that false.", e, MethodologyTypePrefix, MethodologyTypePrefix)
	}

	// The other side of the same claim, so the loop above cannot be satisfied by
	// a prefix constant that matches everything.
	if !strings.HasPrefix("experience.debug", MethodologyTypePrefix) {
		return
	}
	t.Fatalf("MethodologyTypePrefix is %q, which prefixes an experience.* type too — the loop "+
		"above then accepts any entry at all", MethodologyTypePrefix)
}

// TestInitRenderTypesAdmitsATypePfRememberAccepts is the escape hatch the card
// says is why the claim stops where it does.
//
// 🔴 Without this arm the sentence "they are not a promise" is unfalsifiable
// prose. With it, the configurability is a demonstrated fact: one call with an
// env value a deployment may really set, and a type pf_remember accepts becomes
// renderable — after which `public` plus ordinary content is enough for the
// anonymous route.
//
// The global is restored on cleanup, the way TestResolveRenderedHTML_Fallback
// does; without that the following test in this package inherits a render set
// nobody chose.
//
// MUTANTS:
//
//	M31 enforcement: make InitRenderTypes ignore its argument (assign the
//	    default set unconditionally)          RED  after-override
//	M32 enforcement: make IsRenderType always return true
//	                                          RED  the before-override control
//	M33 enforcement: drop the resolveRenderedHTML half (return nil,false for a
//	    configured type)                      RED  the deferred-render assertion
//	M34 publication: delete the citation from the card bullet
//	                                          RED  K12
func TestInitRenderTypesAdmitsATypePfRememberAccepts(t *testing.T) {
	// A type this tool accepts: a legal prefix, off the curated 13, so nothing
	// else in the tree treats it specially.
	const admitted = "experience.render_probe"

	t.Cleanup(func() { InitRenderTypes(defaultRenderTypes) })

	// The control FIRST. "Configurable" is only a fact if the value was not
	// already there — and a default set that happened to include this type
	// would make the assertion below true with InitRenderTypes doing nothing.
	InitRenderTypes(defaultRenderTypes)
	if IsRenderType(admitted) {
		t.Fatalf("%q is already in the DEFAULT render set, so this arm cannot show that "+
			"InitRenderTypes changed anything", admitted)
	}

	InitRenderTypes(defaultRenderTypes + "," + admitted)
	if !IsRenderType(admitted) {
		t.Fatalf("after-override: InitRenderTypes was handed a set naming %q and IsRenderType "+
			"still says no. The card's bullet stops short of promising that no pf_remember row "+
			"is ever renderable BECAUSE this is configurable; if it is not, the bullet is "+
			"understating what the tool guarantees.", admitted)
	}

	// And the consequence, at the function the write path actually calls: with
	// the type admitted, an ordinary pf_remember body owes a render, which is
	// what hasRenderableBody's second branch keys on.
	if stored, deferred := resolveRenderedHTML(nil, admitted, "# a body"); stored != nil || !deferred {
		t.Errorf("resolveRenderedHTML(nil, %q, body) = (%v, %v), want (nil, true) — a configured "+
			"render type must owe a deferred render, or admitting the type changed the lookup "+
			"and nothing else", admitted, stored, deferred)
	}

	// The default set must still refuse it once restored, or the override leaked
	// into every test that runs after this one.
	InitRenderTypes(defaultRenderTypes)
	if IsRenderType(admitted) {
		t.Errorf("restoring the default set left %q renderable", admitted)
	}
}
