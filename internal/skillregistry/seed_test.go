package skillregistry

// seed_test.go — the seed set, the idempotent plan and the callable operation
// (aihub#708 Batch 4A code, Batch 4B review tests: the plan was pure but
// untested, and nothing anywhere called it). Every test here drives the REAL
// SeedSet/PlanSeed/ApplySeed against a fake SeedStore that mirrors the
// domain's own contract (expected-latest CAS, digest verification, private
// publish, duplicate-name refusal), so the operation's invariants are pinned
// where they live rather than in a future CLI test.

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
)

// ─── The canonical set and its provenance ─────────────────────────────────────

func TestSeedSetIsTheCanonicalEightInSortedPlanOrder(t *testing.T) {
	seeds, err := SeedSet()
	if err != nil {
		t.Fatalf("SeedSet: %v", err)
	}
	if len(seeds) != 8 {
		t.Fatalf("SeedSet returned %d skills, want 8", len(seeds))
	}
	names, err := SeedNames()
	if err != nil {
		t.Fatalf("SeedNames: %v", err)
	}
	want := []string{"ci", "code-change", "grill-me", "plan", "review", "ship", "spec", "verification"}
	if strings.Join(names, ",") != strings.Join(want, ",") {
		t.Fatalf("SeedNames = %v, want %v", names, want)
	}
}

// The MIT claim must name the ACTUAL upstream, pinned: URL, exact commit, and
// the upstream's own notice — an adapted MIT work preserves the upstream
// copyright line, it does not replace it with the adapter's.
func TestSeedGrillMeCarriesTheActualUpstreamMITProvenance(t *testing.T) {
	seeds, err := SeedSet()
	if err != nil {
		t.Fatalf("SeedSet: %v", err)
	}
	var grill *SeedSkill
	for i := range seeds {
		if seeds[i].Name == "grill-me" {
			grill = &seeds[i]
		}
	}
	if grill == nil {
		t.Fatal("grill-me missing from the seed set")
	}
	p := grill.Bundle.Provenance
	if p.UpstreamURL != "https://github.com/mattpocock/skills" {
		t.Errorf("UpstreamURL = %q, want the actual upstream (mattpocock/skills)", p.UpstreamURL)
	}
	// The pinned commit of the installed upstream copy (v1.2.3), not "latest".
	if p.UpstreamCommit != "3cca18b368ae95cdbdebbff572ccafa662551015" {
		t.Errorf("UpstreamCommit = %q, want the exact pinned commit", p.UpstreamCommit)
	}
	if p.UpstreamLicense != "MIT" {
		t.Errorf("UpstreamLicense = %q, want MIT", p.UpstreamLicense)
	}
	if grill.Bundle.License.Name != "MIT" {
		t.Fatalf("grill-me license = %q, want MIT", grill.Bundle.License.Name)
	}
	notice := grill.Bundle.License.Notice
	if !strings.Contains(notice, "Copyright (c) 2026 Matt Pocock") {
		t.Errorf("notice does not preserve the upstream copyright line:\n%s", notice)
	}
	if !strings.HasPrefix(notice, "MIT License\n\nCopyright (c) 2026 Matt Pocock") {
		t.Errorf("notice is not the upstream MIT notice verbatim:\n%s", notice)
	}
	for _, phrase := range []string{
		"Permission is hereby granted, free of charge",
		"The above copyright notice and this permission notice shall be included in all",
		"THE SOFTWARE IS PROVIDED \"AS IS\"",
	} {
		if !strings.Contains(notice, phrase) {
			t.Errorf("notice is missing the upstream MIT phrase %q", phrase)
		}
	}
}

// The Astra regression, by name (aihub#708): the verification body's
// merge-base command expanded ${base_ref} — a shell variable nothing in the
// instructions ever assigned, so a literal run compared the change against
// an empty-string ref. The body must substitute the concrete params.base_ref
// value, assigning the variable BEFORE the merge-base command, and must not
// expand a variable the shell has not been given.
func TestSeedVerificationBodyAssignsBaseRefBeforeMergeBase(t *testing.T) {
	seeds, err := SeedSet()
	if err != nil {
		t.Fatalf("SeedSet: %v", err)
	}
	var verification *SeedSkill
	for i := range seeds {
		if seeds[i].Name == "verification" {
			verification = &seeds[i]
		}
	}
	if verification == nil {
		t.Fatal("verification missing from the seed set")
	}
	body := verification.Bundle.Files[0].Content
	if strings.Contains(body, "${base_ref}") {
		t.Errorf("verification body still expands unassigned ${base_ref}")
	}
	assign := strings.Index(body, "base_ref='origin/main'")
	merge := strings.Index(body, `base=$(git merge-base HEAD "$base_ref")`)
	if assign < 0 || merge < 0 {
		t.Fatalf("verification body lost the concrete base assignment (offset %d) or the merge-base command (offset %d)", assign, merge)
	}
	if assign > merge {
		t.Errorf("the base_ref assignment (offset %d) must come before the merge-base command (offset %d)", assign, merge)
	}
	if !strings.Contains(body, "params.base_ref") {
		t.Error("verification body must name params.base_ref as the source of the base ref")
	}
}

// ─── PlanSeed ────────────────────────────────────────────────────────────────

func TestPlanSeedCreatePublishAndPresentAreContentAddressed(t *testing.T) {
	seeds, err := SeedSet()
	if err != nil {
		t.Fatalf("SeedSet: %v", err)
	}
	var specSeed, planSeed SeedSkill
	for _, s := range seeds {
		switch s.Name {
		case "spec":
			specSeed = s
		case "plan":
			planSeed = s
		}
	}
	specDigest, err := specSeed.Digest()
	if err != nil {
		t.Fatal(err)
	}
	planDigest, err := planSeed.Digest()
	if err != nil {
		t.Fatal(err)
	}
	plan, err := PlanSeed(map[string][]ExistingVersion{
		// spec exists with a DIFFERENT version published: publish latest+1.
		"spec": {{Version: 1, Digest: "sha256:old"}, {Version: 3, Digest: "sha256:older"}},
		// plan exists and version 2 already carries the seed digest: present.
		"plan": {{Version: 1, Digest: "sha256:old"}, {Version: 2, Digest: planDigest}},
	})
	if err != nil {
		t.Fatalf("PlanSeed: %v", err)
	}
	var specAction, planAction *SeedAction
	for i := range plan {
		switch plan[i].Name {
		case "spec":
			specAction = &plan[i]
		case "plan":
			planAction = &plan[i]
		}
	}
	if specAction == nil || planAction == nil {
		t.Fatalf("plan missing spec/plan entries: %v", plan)
	}
	if specAction.Op != SeedOpPublish || specAction.ExpectedLatest != 3 || specAction.Version != 4 || specAction.Digest != specDigest {
		t.Errorf("spec action = %+v, want publish at expected 3 -> version 4", specAction)
	}
	if planAction.Op != SeedOpPresent || planAction.Version != 2 || planAction.Digest != planDigest {
		t.Errorf("plan action = %+v, want present pinning version 2", planAction)
	}
	// Every entry pins an exact version > 0 — never a silent "latest".
	for _, a := range plan {
		if a.Version < 1 {
			t.Errorf("action %+v pins version %d; a composed flow cannot pin latest", a, a.Version)
		}
	}
}

func TestPlanSeedPinsTheNewestIdenticalRepublish(t *testing.T) {
	seeds, err := SeedSet()
	if err != nil {
		t.Fatalf("SeedSet: %v", err)
	}
	var ci SeedSkill
	for _, s := range seeds {
		if s.Name == "ci" {
			ci = s
		}
	}
	digest, err := ci.Digest()
	if err != nil {
		t.Fatal(err)
	}
	plan, err := PlanSeed(map[string][]ExistingVersion{
		"ci": {{Version: 2, Digest: digest}, {Version: 5, Digest: digest}, {Version: 4, Digest: "sha256:other"}},
	})
	if err != nil {
		t.Fatalf("PlanSeed: %v", err)
	}
	var ciAction *SeedAction
	for i := range plan {
		if plan[i].Name == "ci" {
			ciAction = &plan[i]
		}
	}
	if ciAction == nil {
		t.Fatal("plan missing the ci row")
	}
	if ciAction.Op != SeedOpPresent || ciAction.Version != 5 {
		t.Errorf("ci action = %+v, want present at the HIGHEST matching version 5", ciAction)
	}
}

// The regression the Batch 4B review asked for by name: an identity that
// EXISTS but has no versions yet must plan as PUBLISH (CreateSkill would
// refuse the duplicate name; publishing v1 under expected_latest=0 is what
// such an identity actually needs), never as create.
func TestPlanSeedExistingIdentityWithNoVersionsPlansPublish(t *testing.T) {
	plan, err := PlanSeed(map[string][]ExistingVersion{
		"spec": {}, // present, empty: identity exists, nothing published
	})
	if err != nil {
		t.Fatalf("PlanSeed: %v", err)
	}
	var spec *SeedAction
	for i := range plan {
		if plan[i].Name == "spec" {
			spec = &plan[i]
		}
	}
	if spec == nil {
		t.Fatal("plan missing spec")
	}
	if spec.Op != SeedOpPublish || spec.ExpectedLatest != 0 || spec.Version != 1 {
		t.Errorf("spec action = %+v, want publish at expected_latest 0 -> version 1", spec)
	}
	// A nil slice stored under the key means the same thing: presence is the input.
	nilPlan, err := PlanSeed(map[string][]ExistingVersion{
		"spec": nil,
	})
	if err != nil {
		t.Fatalf("PlanSeed(nil slice): %v", err)
	}
	for i := range nilPlan {
		if nilPlan[i].Name == "spec" && nilPlan[i].Op != SeedOpPublish {
			t.Errorf("nil slice: spec action = %+v, want publish", nilPlan[i])
		}
	}
}

func TestPlanSeedAbsentKeyStillMeansCreate(t *testing.T) {
	plan, err := PlanSeed(map[string][]ExistingVersion{})
	if err != nil {
		t.Fatalf("PlanSeed: %v", err)
	}
	if len(plan) != 8 {
		t.Fatalf("plan has %d actions, want 8", len(plan))
	}
	for _, a := range plan {
		if a.Op != SeedOpCreate || a.ExpectedLatest != 0 || a.Version != 1 {
			t.Errorf("action %+v, want create at version 1", a)
		}
	}
}

func TestPlanSeedRefusesUnusableExistingRows(t *testing.T) {
	for name, existing := range map[string]map[string][]ExistingVersion{
		"version below one": {"spec": {{Version: 0, Digest: "sha256:x"}}},
		"missing digest":    {"spec": {{Version: 1}}},
	} {
		if _, err := PlanSeed(existing); err == nil {
			t.Errorf("%s: PlanSeed accepted unusable rows", name)
		}
	}
}

// ─── The callable operation ──────────────────────────────────────────────────

// fakeSeedStore mirrors the domain contract the real implementation must
// satisfy: duplicate names are refused, publishes are CAS-checked against the
// current latest, digests are verified against the canonical bytes, and
// versions are private by construction (there is simply no visibility knob).
type fakeSeedStore struct {
	skills    map[string]*fakeSeed
	publishOf map[string]error // skill id -> publish failure
	createOf  map[string]error // name -> create failure
	log       []string
}

type fakeSeed struct {
	id       string
	versions []ExistingVersion
}

func newFakeSeedStore() *fakeSeedStore {
	return &fakeSeedStore{skills: map[string]*fakeSeed{}, publishOf: map[string]error{}, createOf: map[string]error{}}
}

func (s *fakeSeedStore) SeedSkill(_ context.Context, name string) (string, []ExistingVersion, bool, error) {
	s.log = append(s.log, "read:"+name)
	sk, ok := s.skills[name]
	if !ok {
		return "", nil, false, nil
	}
	return sk.id, sk.versions, true, nil
}

func (s *fakeSeedStore) CreateSeedSkill(_ context.Context, name string) (string, error) {
	s.log = append(s.log, "create:"+name)
	if err := s.createOf[name]; err != nil {
		return "", err
	}
	if _, ok := s.skills[name]; ok {
		// The domain's own refusal shape (CONFLICT_DUPLICATE).
		return "", fmt.Errorf("you already have a skill named %q", name)
	}
	sk := &fakeSeed{id: "skill_" + name + "_id"}
	s.skills[name] = sk
	return sk.id, nil
}

func (s *fakeSeedStore) PublishSeedSkillVersion(_ context.Context, skillID string, expectedLatest int, bundle, contract []byte, contentDigest string) (int, error) {
	s.log = append(s.log, fmt.Sprintf("publish:%s@%d", skillID, expectedLatest))
	if contentDigest == "" {
		return 0, errors.New("content digest is required")
	}
	// Digest verification, exactly where the domain does it.
	if got := VersionDigest(bundle, contract); got != contentDigest {
		return 0, fmt.Errorf("content_digest %q does not match the computed digest %s", contentDigest, got)
	}
	var owner *fakeSeed
	for _, sk := range s.skills {
		if sk.id == skillID {
			owner = sk
		}
	}
	if owner == nil {
		return 0, errors.New("skill not found")
	}
	if err := s.publishOf[skillID]; err != nil {
		return 0, err
	}
	latest := 0
	for _, v := range owner.versions {
		if v.Version > latest {
			latest = v.Version
		}
	}
	if expectedLatest != latest {
		return 0, fmt.Errorf("expected_latest %d but the current latest_version is %d", expectedLatest, latest)
	}
	owner.versions = append(owner.versions, ExistingVersion{Version: latest + 1, Digest: contentDigest})
	return latest + 1, nil
}

func TestApplySeedSeedsAFreshRegistryThenIsIdempotent(t *testing.T) {
	store := newFakeSeedStore()
	first, err := ApplySeed(context.Background(), store)
	if err != nil {
		t.Fatalf("ApplySeed: %v", err)
	}
	if len(first) != 8 {
		t.Fatalf("first run produced %d results, want 8", len(first))
	}
	for _, r := range first {
		if r.Op != SeedOpCreate || r.Version != 1 || r.SkillID == "" {
			t.Errorf("first run result %+v, want create publishing version 1", r)
		}
	}
	// The digests published are the seed entries' own content digests.
	seeds, err := SeedSet()
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range seeds {
		d, err := s.Digest()
		if err != nil {
			t.Fatal(err)
		}
		found := false
		for _, r := range first {
			if r.Name == s.Name {
				found = true
				if r.Digest != d {
					t.Errorf("%s published digest %q, want its seed digest %q", s.Name, r.Digest, d)
				}
			}
		}
		if !found {
			t.Errorf("first run missing %s", s.Name)
		}
	}
	// Deterministic order: reads follow SeedSet's literal order; the performs
	// follow the plan's sorted order (ci first, verification last).
	if store.log[0] != "read:grill-me" {
		t.Errorf("first operation = %q, want the first seed read", store.log[0])
	}
	if store.log[len(store.log)-1] != "publish:skill_verification_id@0" {
		t.Errorf("last operation = %q, want the plan's final publish in sorted order: %v", store.log[len(store.log)-1], store.log)
	}
	mark := len(store.log)

	second, err := ApplySeed(context.Background(), store)
	if err != nil {
		t.Fatalf("re-run ApplySeed: %v", err)
	}
	if len(second) != 8 {
		t.Fatalf("second run produced %d results, want 8", len(second))
	}
	for _, r := range second {
		if r.Op != SeedOpPresent || r.Version != 1 {
			t.Errorf("re-run result %+v, want present pinning version 1 (idempotency)", r)
		}
	}
	// A no-op run must not have attempted a single publish or create.
	for _, entry := range store.log[mark:] {
		if strings.HasPrefix(entry, "publish:") || strings.HasPrefix(entry, "create:") {
			t.Errorf("idempotent re-run performed %q — a present plan must publish nothing", entry)
		}
	}
}

// The operation-level twin of the PlanSeed regression: an identity that
// exists with zero versions must publish v1 (expected_latest=0), and must NOT
// attempt the create the domain would refuse as a duplicate.
func TestApplySeedExistingIdentityNoVersionsPublishesVersionOne(t *testing.T) {
	store := newFakeSeedStore()
	// The identity exists (a previous CreateSkill), but nothing was published.
	if _, err := store.CreateSeedSkill(context.Background(), "spec"); err != nil {
		t.Fatal(err)
	}
	store.log = nil // only the operation's own actions are asserted below
	res, err := ApplySeed(context.Background(), store)
	if err != nil {
		t.Fatalf("ApplySeed: %v", err)
	}
	for _, r := range res {
		if r.Name != "spec" {
			continue
		}
		if r.Op != SeedOpPublish || r.Version != 1 {
			t.Errorf("spec result %+v, want publish of version 1 against the empty identity", r)
		}
		if r.SkillID != "skill_spec_id" {
			t.Errorf("spec result published skill id %q, want the existing identity", r.SkillID)
		}
	}
	for _, entry := range store.log {
		if entry == "create:spec" {
			t.Errorf("the operation attempted a create for the existing identity: %v", store.log)
		}
	}
}

func TestApplySeedPublishesNewContentAtLatestPlusOne(t *testing.T) {
	store := newFakeSeedStore()
	seeds, err := SeedSet()
	if err != nil {
		t.Fatal(err)
	}
	var spec SeedSkill
	for _, s := range seeds {
		if s.Name == "spec" {
			spec = s
		}
	}
	digest, err := spec.Digest()
	if err != nil {
		t.Fatal(err)
	}
	// spec exists with two unrelated versions: the seed is content-new.
	store.skills["spec"] = &fakeSeed{
		id:       "skill_spec_id",
		versions: []ExistingVersion{{Version: 1, Digest: "sha256:old"}, {Version: 2, Digest: "sha256:older"}},
	}
	res, err := ApplySeed(context.Background(), store)
	if err != nil {
		t.Fatalf("ApplySeed: %v", err)
	}
	for _, r := range res {
		if r.Name == "spec" && (r.Op != SeedOpPublish || r.Version != 3 || r.Digest != digest) {
			t.Errorf("spec result %+v, want publish of the seed digest as version 3", r)
		}
	}
	if !containsString(store.log, "publish:skill_spec_id@2") {
		t.Errorf("publish did not carry the current latest 2 as the CAS token: %v", store.log)
	}
}

func TestApplySeedRefusesNilStoreAndStopsAtStoreErrors(t *testing.T) {
	if _, err := ApplySeed(context.Background(), nil); err == nil {
		t.Fatal("ApplySeed(nil) must be refused: there is no anonymous seed operation")
	}
	store := newFakeSeedStore()
	store.publishOf["skill_ci_id"] = errors.New("boom")
	res, err := ApplySeed(context.Background(), store)
	if err == nil {
		t.Fatal("a store error must surface, not be swallowed")
	}
	if !strings.Contains(err.Error(), "ci") {
		t.Errorf("error %q must name the skill that failed", err)
	}
	// Partial results carry only the skills that COMPLETED; the failing one is
	// in the error, not dressed up as a pinned version.
	if len(res) != 0 {
		t.Fatalf("partial results = %+v, want none: the first planned action failed", res)
	}
}

func containsString(list []string, want string) bool {
	for _, v := range list {
		if v == want {
			return true
		}
	}
	return false
}
