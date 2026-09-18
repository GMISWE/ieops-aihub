package skillregistry

// seed.go — the canonical seed set for aihub#708 Batch 4A: the closed,
// versioned bundles a fresh aihub deployment publishes to its own skill
// registry so that NEWLY COMPOSED work-item flows can be pinned to real,
// inspectable skill versions from day one, and the idempotent plan that
// makes re-running the seed a no-op instead of a duplicate-publish storm.
//
// ─── Why this lives in the PURE package ──────────────────────────────────────
//
// The seed is DATA plus a decision function, not storage. Everything that
// touches SQL, callers or visibility lives in internal/domain
// (CreateSkill / PublishSkillVersion, both private-by-default with the
// expected-latest CAS); everything that drives them from a process (a
// bootstrap command, a migration hook) is deliberately NOT in this slice.
// What this file owns is the invariant those callers must not be able to
// get wrong:
//
//   - every seed entry is a CLOSED bundle (SkillBundle) plus a runtime
//     capability contract (SkillContract) that ValidateBundle /
//     ValidateContract accept — SeedSet() re-validates every entry on every
//     call, so a future edit that breaks the closed-bundle or subset rules
//     fails at the seed, not at the first publish request;
//   - every entry carries LICENSE and PROVENANCE. MIT provenance where it
//     applies (grill-me's interview pattern is adapted from MIT-licensed
//     upstream skill patterns) and an explicit Proprietary license on the
//     bodies derived from GMI's own plugin, because an unlabelled bundle is
//     a redistribution hazard the registry has no way to audit later;
//   - the seed's idempotency is CONTENT-addressed (PlanSeed compares the
//     VersionDigest of the seed against the digests of the versions that
//     already exist), never name- or count-addressed — "skill named spec
//     exists" is NOT evidence that the seed's content is published;
//   - the plan never emits a "pin latest" instruction. Every SeedAction
//     carries the EXACT version a newly composed flow should pin, so a
//     seeded flow is reproducible by construction (spec D4: pinning is
//     exact; "latest accessible" is a pin-time convenience for human
//     authors, resolved and frozen server-side — never a silent fallback
//     the seed relies on).
//
// ─── Names ───────────────────────────────────────────────────────────────────
//
// The task vocabulary ("grill-me, spec, plan, code_change, review,
// verification, ship, CI") is written here in the registry's legal spelling:
// a skill name matches ^[a-z][a-z0-9-]{0,63}$ (mirrored from the domain's
// CHECK, which remains the enforcement — this copy is advisory so the seed
// fails before the server has to refuse it). Underscored task names map to
// kebab-case registry names: code_change -> code-change. The mapping is
// recorded in docs/workflow-v2/02-seed-and-import.md.

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"sort"
)

// seedNameRE mirrors the skills.name CHECK in migration 0043 (the domain's
// skillNameRE is the enforced copy; this one exists so an illegal seed name
// fails inside SeedSet instead of at the server). Keep the two in sync.
var seedNameRE = regexp.MustCompile(`^[a-z][a-z0-9-]{0,63}$`)

// SeedOp is what PlanSeed says to do for one seed skill.
type SeedOp string

const (
	// SeedOpCreate: the skill identity does not exist yet. Create it
	// (CreateSkill), then publish version 1. ExpectedLatest is 0.
	SeedOpCreate SeedOp = "create"
	// SeedOpPublish: publish the seed's content as the next version under the
	// CAS token ExpectedLatest — the current latest_version for a skill with
	// published versions, or 0 for an identity that exists but has none yet
	// (CreateSkill would refuse that identity as a duplicate; publishing v1
	// is what it needs).
	SeedOpPublish SeedOp = "publish"
	// SeedOpPresent: a version with the seed's content digest already
	// exists. Do NOTHING — publishing again would duplicate content under a
	// new version number. This is the idempotent no-op.
	SeedOpPresent SeedOp = "present"
)

// SeedSkill is one seeded skill: its registry name, its closed file bundle,
// and its runtime capability contract. Contract capabilities follow the
// closed vocabulary (Capabilities()); grants are derived server-side from
// exactly those values (workflowGrantForContract), never requested here.
type SeedSkill struct {
	Name     string
	Bundle   SkillBundle
	Contract SkillContract
}

// Digest computes the content digest of this seed entry: canonical bundle
// bytes + canonical contract bytes through VersionDigest. It is the value
// PlanSeed compares the caller's ExistingVersion.Digest against, and the
// value a publish request's content_digest should carry so transport
// integrity is checked server-side too.
func (s SeedSkill) Digest() (string, error) {
	bundleJSON, err := CanonicalBundleJSON(&s.Bundle)
	if err != nil {
		return "", fmt.Errorf("seed %q: bundle: %w", s.Name, err)
	}
	contractJSON, err := CanonicalContractJSON(&s.Contract)
	if err != nil {
		return "", fmt.Errorf("seed %q: contract: %w", s.Name, err)
	}
	return VersionDigest(bundleJSON, contractJSON), nil
}

// validate checks one seed entry against the same rules the registry will
// enforce at publish time, plus the seed's own name rule.
func (s SeedSkill) validate() error {
	if !seedNameRE.MatchString(s.Name) {
		return fmt.Errorf("seed skill name %q does not match ^[a-z][a-z0-9-]{0,63}$", s.Name)
	}
	if err := ValidateBundle(&s.Bundle); err != nil {
		return fmt.Errorf("seed %q: bundle: %w", s.Name, err)
	}
	if err := ValidateContract(&s.Contract); err != nil {
		return fmt.Errorf("seed %q: contract: %w", s.Name, err)
	}
	if _, err := s.Digest(); err != nil {
		return err
	}
	return nil
}

// ExistingVersion is one version of a seed skill as the caller currently
// sees it: its number and its content digest. Version numbers are > 0.
type ExistingVersion struct {
	Version int
	Digest  string
}

// SeedAction is PlanSeed's per-skill decision. Version is ALWAYS the exact
// version a newly composed flow should pin for this skill after the plan is
// applied (never 0: the seed never teaches a silent "latest" fallback); for
// SeedOpCreate/SeedOpPublish it is the version the publish will create —
// prefer the version PublishSkillVersion actually returns if it ever
// differs, and re-plan rather than assume.
type SeedAction struct {
	Name string
	Op   SeedOp
	// ExpectedLatest is the CAS token the publish must carry: 0 when no
	// version exists yet (a skill being created, or an existing identity
	// that has never published), the current latest_version otherwise.
	// Unused for SeedOpPresent.
	ExpectedLatest int
	// Version is the exact pin (see the struct comment).
	Version int
	// Digest is the seed entry's content digest.
	Digest string
}

// PlanSeed computes the idempotent seed plan. existing maps a seed skill's
// NAME to the versions the caller can currently see for it (the owner's
// view — all versions). The key's PRESENCE is itself an input: an ABSENT key
// means the skill identity does not exist at all, while a PRESENT key with
// an empty (possibly nil) slice means the identity EXISTS but no version is
// published yet — two different situations the plan must not collapse, because
// CreateSkill refuses an existing identity (duplicate name) while publishing
// version 1 under expected_latest=0 is exactly what such an identity needs.
//
// The rules, all content-addressed:
//
//   - a version whose Digest equals the seed's digest means the content is
//     already published: SeedOpPresent, pin that version (the HIGHEST
//     matching one, so a history of identical re-publishes degrades to the
//     newest copy);
//   - a skill that exists with versions but no digest match means the seed
//     content is NEW relative to the registry: SeedOpPublish at
//     latest+1 — the CAS token is the current latest, exactly what
//     PublishSkillVersion expects;
//   - a skill identity that exists with NO versions: SeedOpPublish for
//     version 1 with expected_latest=0 (NOT SeedOpCreate — the identity is
//     already taken, so CreateSkill would be refused); the caller resolves
//     the name to its skill id itself;
//   - no skill at all (absent key): SeedOpCreate (identity + version 1);
//   - every Version in the result is > 0, so a flow composed from the plan
//     pins exact versions by construction.
//
// The plan is pure: performing it (CreateSkill + PublishSkillVersion with
// the returned ExpectedLatest and the entry's bundle/contract) is the
// caller's job — or ApplySeed's, which is that caller, written once against
// the SeedStore port — and the publish itself re-validates everything
// server-side. Re-running PlanSeed after a successful application returns
// all-present.
func PlanSeed(existing map[string][]ExistingVersion) ([]SeedAction, error) {
	seeds, err := SeedSet()
	if err != nil {
		return nil, err
	}
	// Deterministic output order: by seed name, not map iteration order.
	names := make([]string, 0, len(seeds))
	byName := make(map[string]SeedSkill, len(seeds))
	for _, s := range seeds {
		if _, dup := byName[s.Name]; dup {
			return nil, fmt.Errorf("seed set lists skill %q twice", s.Name)
		}
		byName[s.Name] = s
		names = append(names, s.Name)
	}
	sort.Strings(names)

	out := make([]SeedAction, 0, len(seeds))
	for _, name := range names {
		s := byName[name]
		digest, err := s.Digest()
		if err != nil {
			return nil, err
		}
		versions, exists := existing[name]
		// Validate the caller's rows: version numbers start at 1 and a
		// version without a digest cannot be compared — both would silently
		// turn "is the content published?" into a guess.
		latest := 0
		match := 0
		for _, v := range versions {
			if v.Version < 1 {
				return nil, fmt.Errorf("seed %q: existing version %d is invalid; versions start at 1", name, v.Version)
			}
			if v.Digest == "" {
				return nil, fmt.Errorf("seed %q: existing version %d carries no digest; the plan cannot be content-checked against it", name, v.Version)
			}
			if v.Version > latest {
				latest = v.Version
			}
			if v.Digest == digest && v.Version > match {
				match = v.Version
			}
		}
		switch {
		case !exists:
			// No identity at all: create it, then publish v1.
			out = append(out, SeedAction{Name: name, Op: SeedOpCreate, ExpectedLatest: 0, Version: 1, Digest: digest})
		case len(versions) == 0:
			// The identity EXISTS but nothing is published yet. CreateSkill
			// would refuse it as a duplicate, so the plan says publish —
			// version 1 under expected_latest=0, the CAS token a fresh
			// identity carries.
			out = append(out, SeedAction{Name: name, Op: SeedOpPublish, ExpectedLatest: 0, Version: 1, Digest: digest})
		case match > 0:
			out = append(out, SeedAction{Name: name, Op: SeedOpPresent, ExpectedLatest: 0, Version: match, Digest: digest})
		default:
			out = append(out, SeedAction{Name: name, Op: SeedOpPublish, ExpectedLatest: latest, Version: latest + 1, Digest: digest})
		}
	}
	return out, nil
}

// SeedNames returns the seed set's registry names, sorted. It is the stable
// answer to "which skills does the canonical bundle cover" for docs and for
// callers that want to fetch exactly the seeded rows.
func SeedNames() ([]string, error) {
	seeds, err := SeedSet()
	if err != nil {
		return nil, err
	}
	names := make([]string, 0, len(seeds))
	for _, s := range seeds {
		names = append(names, s.Name)
	}
	sort.Strings(names)
	return names, nil
}

// ─── Applying the plan: the callable seed operation ─────────────────────────
//
// Batch 4A shipped the plan but no caller: "no bootstrap surface calls them".
// The LOCAL, in-process callable entry lives here (aihub#708 Batch 4B review):
// one authenticated port (SeedStore) plus two operations over it, ApplySeed
// and ApplyImport. What is still NOT here — deliberately, owned by the
// server/CLI batches — is every wiring above this package: no server startup
// hook, no `polyforge` verb, no MCP tool calls them yet. A controller or CLI
// that wants to run the seed implements SeedStore over pkg/client (or over
// internal/domain directly) and calls ApplySeed; nothing else is needed.
// Reported as a gap, not faked: until that wiring lands, the operation is
// reachable only in-process and from this package's tests.

// SeedStore is the AUTHENTICATED port the seed and import operations drive.
// The controller or CLI implements it over pkg/client — CreateSkill,
// PublishSkillVersion and the skill read the versions/digests come from —
// with a caller credential that satisfies the registry's own write rules
// (authenticated, role writer or admin, NOT a project-scoped API key:
// internal/domain's skillRequireWriteCaller). This package stays pure: no
// network, no SQL, no clock. There is deliberately NO anonymous mode — a nil
// store is an error, never a degraded run — and the port has no method that
// could route around the caller's authorization: no visibility flip, no
// share, no "publish as someone else".
type SeedStore interface {
	// SeedSkill returns the named skill as the AUTHENTICATED caller sees it:
	// its id, every version visible to that caller (number and content digest
	// each — the owner's view of all versions), and whether the identity
	// exists at all. exists=false means the name is free. exists=true with
	// empty versions means the identity exists but nothing is published yet.
	SeedSkill(ctx context.Context, name string) (skillID string, versions []ExistingVersion, exists bool, err error)
	// CreateSeedSkill creates the identity and returns its id. A credential
	// that cannot write the registry must fail here; the operation never
	// retries and never falls back to an anonymous identity.
	CreateSeedSkill(ctx context.Context, name string) (skillID string, err error)
	// PublishSeedSkillVersion publishes the next immutable version under the
	// expected-latest compare-and-set and returns the version actually
	// published. expectedLatest is always the EXACT token the plan computed —
	// 0 for a fresh identity, the current latest otherwise — never a "latest"
	// wildcard. contentDigest is checked against the canonical bytes
	// server-side (the domain's PublishSkillVersion does this), and the
	// implementation must publish PRIVATE by default: sharing and visibility
	// flips are separate, explicit, revocable acts that this port cannot
	// express.
	PublishSeedSkillVersion(ctx context.Context, skillID string, expectedLatest int, bundle, contract []byte, contentDigest string) (version int, err error)
}

// SeedApplyResult is one skill's outcome after ApplySeed or ApplyImport: the
// operation that ran and the EXACT version a newly composed flow should pin
// for that skill — never 0, never "latest". Composing the recommended flow
// from these pins (which seed skill feeds which, in what order) is documented
// in docs/workflow-v2/02-seed-and-import.md, "The recommended composition".
type SeedApplyResult struct {
	Name    string
	Op      SeedOp
	SkillID string
	Version int
	Digest  string
}

// ApplySeed plans and performs the canonical seed set against store in one
// deterministic, authenticated pass: it reads the caller's current view for
// every seed name, asks PlanSeed for the content-addressed plan, then
// performs the actions in the plan's own order (sorted by name — PlanSeed's
// order, not map-iteration order). Determinism is the plan's: same registry
// state in, same actions out; the only I/O is through store, and no step
// depends on wall-clock time. Idempotency is the plan's too: re-running
// ApplySeed over an already-seeded registry performs nothing and returns
// all-present with the same exact pins.
//
// Every publish carries the plan's ExpectedLatest CAS token and the seed
// entry's content digest; every created identity is the caller's own; every
// published version is private by default. On the first store error the
// operation stops and returns the results completed so far alongside the
// error — partial progress is visible, never silently retried. A nil store
// is refused: there is no anonymous seed.
func ApplySeed(ctx context.Context, store SeedStore) ([]SeedApplyResult, error) {
	if store == nil {
		return nil, errors.New("ApplySeed: no seed store: an anonymous seed operation does not exist")
	}
	seeds, err := SeedSet()
	if err != nil {
		return nil, err
	}
	byName := make(map[string]SeedSkill, len(seeds))
	existing := make(map[string][]ExistingVersion, len(seeds))
	ids := make(map[string]string, len(seeds))
	for _, s := range seeds {
		byName[s.Name] = s
		id, versions, exists, serr := store.SeedSkill(ctx, s.Name)
		if serr != nil {
			return nil, fmt.Errorf("seed %q: read existing versions: %w", s.Name, serr)
		}
		if exists {
			// Presence in `existing` is an input (see PlanSeed): an empty
			// slice here means the identity exists with no versions.
			existing[s.Name] = versions
			ids[s.Name] = id
		}
	}
	plan, err := PlanSeed(existing)
	if err != nil {
		return nil, err
	}
	out := make([]SeedApplyResult, 0, len(plan))
	for _, action := range plan {
		res := SeedApplyResult{Name: action.Name, Op: action.Op, Version: action.Version, Digest: action.Digest}
		switch action.Op {
		case SeedOpPresent:
			res.SkillID = ids[action.Name]
			out = append(out, res)
			continue
		case SeedOpCreate:
			id, cerr := store.CreateSeedSkill(ctx, action.Name)
			if cerr != nil {
				return out, fmt.Errorf("seed %q: create identity: %w", action.Name, cerr)
			}
			res.SkillID = id
		case SeedOpPublish:
			res.SkillID = ids[action.Name]
		}
		seed := byName[action.Name]
		bundleJSON, berr := CanonicalBundleJSON(&seed.Bundle)
		if berr != nil {
			return out, fmt.Errorf("seed %q: bundle: %w", action.Name, berr)
		}
		contractJSON, cerr := CanonicalContractJSON(&seed.Contract)
		if cerr != nil {
			return out, fmt.Errorf("seed %q: contract: %w", action.Name, cerr)
		}
		version, perr := store.PublishSeedSkillVersion(ctx, res.SkillID, action.ExpectedLatest, bundleJSON, contractJSON, action.Digest)
		if perr != nil {
			return out, fmt.Errorf("seed %q: publish version %d: %w", action.Name, action.Version, perr)
		}
		// The plan's Version is the prediction; the publish's return is the
		// fact. Prefer the fact (SeedAction's own rule) so a future divergence
		// cannot leave a pin pointing at a version that was never written.
		if version > 0 {
			res.Version = version
		}
		out = append(out, res)
	}
	return out, nil
}

// ─── Licenses and provenance ────────────────────────────────────────────────

// upstreamMITNotice is the VERBATIM MIT notice of the upstream the grill-me
// pattern is adapted from: mattpocock/skills, LICENSE at commit
// 3cca18b368ae95cdbdebbff572ccafa662551015 (v1.2.3 — the copy this fleet has
// installed under ~/.claude/plugins/cache/mattpocock/mattpocock-skills/1.2.3,
// recorded in installed_plugins.json; the bytes below were read from that
// LICENSE, not transcribed from memory). The seed body is an ADAPTATION of
// that upstream — the skill NAME and the relentless-interview pattern, not a
// byte-for-byte import — and MIT's single condition is that the upstream
// copyright and permission notice is preserved in copies and substantial
// portions, so the notice rides in the bundle's license.notice untouched,
// upstream copyright line included. (An earlier revision of this seed
// misattributed the upstream to obra/superpowers — whose skill set, measured
// in the same installed-plugins cache, has no grill family at all — and
// carried a GMI copyright line instead of the upstream's.)
const upstreamMITNotice = "MIT License\n\n" +
	"Copyright (c) 2026 Matt Pocock\n\n" +
	"Permission is hereby granted, free of charge, to any person obtaining a copy\n" +
	"of this software and associated documentation files (the \"Software\"), to deal\n" +
	"in the Software without restriction, including without limitation the rights\n" +
	"to use, copy, modify, merge, publish, distribute, sublicense, and/or sell\n" +
	"copies of the Software, and to permit persons to whom the Software is\n" +
	"furnished to do so, subject to the following conditions:\n\n" +
	"The above copyright notice and this permission notice shall be included in all\n" +
	"copies or substantial portions of the Software.\n\n" +
	"THE SOFTWARE IS PROVIDED \"AS IS\", WITHOUT WARRANTY OF ANY KIND, EXPRESS OR\n" +
	"IMPLIED, INCLUDING BUT NOT LIMITED TO THE WARRANTIES OF MERCHANTABILITY,\n" +
	"FITNESS FOR A PARTICULAR PURPOSE AND NONINFRINGEMENT. IN NO EVENT SHALL THE\n" +
	"AUTHORS OR COPYRIGHT HOLDERS BE LIABLE FOR ANY CLAIM, DAMAGES OR OTHER\n" +
	"LIABILITY, WHETHER IN AN ACTION OF CONTRACT, TORT OR OTHERWISE, ARISING FROM,\n" +
	"OUT OF OR IN CONNECTION WITH THE SOFTWARE OR THE USE OR OTHER DEALINGS IN THE\n" +
	"SOFTWARE.\n"

// proprietaryNotice labels GMI-authored seed bodies. The aihub repository
// carries no open-source license file, so Proprietary is the honest SPDX
// label for content derived from the polyforge plugin's own skill bodies;
// revisiting that decision is recorded in docs/workflow-v2/04-unresolved.md.
const proprietaryNotice = "Copyright (c) 2026 GMI Engineering. Internal fleet content.\n" +
	"Distribution happens only through the aihub skill registry's access-checked\n" +
	"endpoints: every version publishes private by default, and sharing or a\n" +
	"public flip are separate, explicit, revocable acts.\n"

func mitLicense() BundleLicense {
	return BundleLicense{Name: "MIT", URL: "https://opensource.org/licenses/MIT", Notice: upstreamMITNotice}
}

func proprietaryLicense() BundleLicense {
	return BundleLicense{Name: "Proprietary", Notice: proprietaryNotice}
}

// polyforgeProvenance records where a GMI-derived seed body came from: the
// local plugin path whose content it condenses. The registry copy (not the
// plugin file) is the DB authority for newly composed flows (aihub#708 D11).
func polyforgeProvenance(source string, notes string) BundleProvenance {
	return BundleProvenance{
		Source: source,
		Notes:  notes,
	}
}

// ─── The canonical seed set ─────────────────────────────────────────────────
//
// Eight skills: the interview, the two authoring documents, the producer,
// the two gates, the shipper and the CI watcher. Capabilities follow the
// closed vocabulary; the server derives each step's grant from them
// (workflowGrantForContract):
//
//   - grill-me          authoring, interactive=true (needs a human; a
//                        requires_human_session=false flow cannot pin it)
//   - spec / plan       authoring, interactive=false (a human makes them
//                        better; they must still run unattended)
//   - code-change       authoring — the WRITE producer later gates inspect
//   - review            review — gate: read_only + independent producer
//   - verification      verification — gate: read_only + independent
//   - ship              shipping — write producer (commit/push/PR)
//   - ci                verification — the post-ship CI watcher
//
// A contract that mixed a gate capability with a producer capability is
// refused by the registry, so no seed entry can sit on both sides of a
// gate. The recommended COMPOSITION of these skills into a flow is recorded
// in docs/workflow-v2/02-seed-and-import.md, deliberately NOT here: flows
// are per-work-item generations pinned by their authors (pf_update_workflow),
// and a seed that shipped its own flow template would be a second, weaker
// place where composition rules live.

const seedRolePreamble = `You are executing ONE step of a pinned aihub workflow (aihub#708).

The invocation identity (step id, attempt, models, params, inputs) was pinned
server-side when this step started; the driver that dispatched you owns the
structured StepResult envelope that gets recorded. Your job is the WORK and an
honest report of it — never a fabricated outcome.

Honesty rules (all steps):
- Report what actually happened. blocked / incomplete / provider_error are
  honest states; a fabricated "completed" is the one unforgivable one.
- Cite real evidence: real file paths, real command results, real URLs. If you
  did not run it, do not cite it.
- Stay inside the step: do not start other steps, do not approve anything
  (approval is a separate, human-only act), do not touch work items other than
  the one you were given.
- Do not fetch remote content at runtime; everything you need is in params,
  inputs, or the worktree you run in.
`

func seedBundle(body string, license BundleLicense, provenance BundleProvenance) SkillBundle {
	return SkillBundle{
		Entry:      "SKILL.md",
		Files:      []SkillFile{{Path: "SKILL.md", Content: body, Encoding: EncodingFileUTF8}},
		License:    license,
		Provenance: provenance,
	}
}

// SeedSet returns the canonical seed skills, validated. Every call re-runs
// ValidateBundle + ValidateContract on every entry, so the set is either
// fully publishable or the error names the entry that is not.
func SeedSet() ([]SeedSkill, error) {
	seeds := []SeedSkill{
		{
			Name: "grill-me",
			Bundle: seedBundle(grillMeBody, mitLicense(), BundleProvenance{
				Source:          "aihub seed (aihub#708 Batch 4A)",
				UpstreamURL:     "https://github.com/mattpocock/skills",
				UpstreamCommit:  "3cca18b368ae95cdbdebbff572ccafa662551015",
				UpstreamLicense: "MIT",
				Notes: "Skill name and interview pattern adapted from the MIT-licensed grill family " +
					"of mattpocock/skills at the pinned commit (skills/productivity/grill-me + " +
					"skills/productivity/grilling, v1.2.3) — the upstream the local pf-spec engine " +
					"list credits as \"mattpocock's grill-with-docs + to-spec\". The body is a fresh " +
					"polyforge composition, not a byte-for-byte import; the upstream MIT notice is " +
					"preserved verbatim in license.notice, upstream copyright line included.",
			}),
			Contract: SkillContract{
				Capabilities: []Capability{CapAuthoring},
				Runtime:      RuntimeSpec{Interactive: true},
				ParamsSchema: raw(`{
					"type": "object",
					"properties": {
						"topic": {"type": "string", "minLength": 1, "description": "the goal, proposal or design being interrogated"},
						"context": {"type": "string", "description": "optional background the interview should build on"}
					},
					"required": ["topic"],
					"additionalProperties": false
				}`),
				OutputSchema: raw(`{
					"type": "object",
					"properties": {
						"record": {"type": "string", "description": "the full interview log, question by question, answer by answer"},
						"distilled_requirements": {"type": "array", "items": {"type": "string"}, "description": "the sharp, agreed statements the interrogation produced"},
						"open_questions": {"type": "array", "items": {"type": "string"}, "description": "what remains unresolved, each phrased as a question"}
					},
					"required": ["record", "distilled_requirements", "open_questions"],
					"additionalProperties": false
				}`),
			},
		},
		{
			Name: "spec",
			Bundle: seedBundle(specBody, proprietaryLicense(),
				polyforgeProvenance("plugins/polyforge/skills/pf-spec/SKILL.md",
					"Condensed from the local pf-spec skill: same OpenSpec grammar, so artifacts "+
						"stay compatible with methodology.spec artifacts, /ui annotations and "+
						"/pf-revise. The registry copy is the authority for newly composed flows.")),
			Contract: SkillContract{
				Capabilities: []Capability{CapAuthoring},
				Runtime:      RuntimeSpec{Interactive: false},
				ParamsSchema: raw(`{
					"type": "object",
					"properties": {
						"goal": {"type": "string", "minLength": 1, "description": "the work item goal the spec scopes"},
						"mode": {"type": "string", "enum": ["feature", "debug"], "description": "debug phrases the broken behavior as a violated requirement plus a fix requirement"}
					},
					"required": ["goal"],
					"additionalProperties": false
				}`),
				InputSchema: raw(`{
					"type": "object",
					"properties": {
						"interview_requirements": {"type": "array", "items": {"type": "string"}, "description": "the sharp, agreed statements the interrogation produced"},
						"interview_open_questions": {"type": "array", "items": {"type": "string"}, "description": "what remains unresolved, each phrased as a question"}
					},
					"additionalProperties": false
				}`),
				OutputSchema: raw(`{
					"type": "object",
					"properties": {
						"spec": {"type": "string", "minLength": 1, "description": "the full spec markdown, OpenSpec grammar"},
						"requirements": {"type": "array", "minItems": 1, "items": {"type": "string"}, "description": "the requirement titles, in order"}
					},
					"required": ["spec", "requirements"],
					"additionalProperties": false
				}`),
			},
		},
		{
			Name: "plan",
			Bundle: seedBundle(planBody, proprietaryLicense(),
				polyforgeProvenance("plugins/polyforge/skills/pf-plan/SKILL.md",
					"Condensed from the local pf-plan skill: same OpenSpec grammar plus ordered "+
						"steps with Touched files lines, so declared_resources derivation stays "+
						"possible from a registry-driven plan.")),
			Contract: SkillContract{
				Capabilities: []Capability{CapAuthoring},
				Runtime:      RuntimeSpec{Interactive: false},
				InputSchema: raw(`{
					"type": "object",
					"properties": {
						"spec": {"type": "string", "minLength": 1, "description": "the full spec markdown, OpenSpec grammar"}
					},
					"required": ["spec"],
					"additionalProperties": false
				}`),
				OutputSchema: raw(`{
					"type": "object",
					"properties": {
						"plan": {"type": "string", "minLength": 1, "description": "the full plan markdown"},
						"steps": {
							"type": "array",
							"minItems": 1,
							"items": {
								"type": "object",
								"properties": {
									"id": {"type": "string", "minLength": 1},
									"touched_files": {"type": "array", "items": {"type": "string"}}
								},
								"required": ["id"],
								"additionalProperties": false
							}
						}
					},
					"required": ["plan", "steps"],
					"additionalProperties": false
				}`),
			},
		},
		{
			Name: "code-change",
			Bundle: seedBundle(codeChangeBody, proprietaryLicense(),
				polyforgeProvenance("plugins/polyforge/skills/_common/lifecycle.md + scenario step graphs",
					"The WRITE producer: implements the plan inside the claimed worktree. "+
						"Ship/commit/PR are deliberately NOT here — the ship skill owns them, "+
						"and the review/verification gates must see the finished write first.")),
			Contract: SkillContract{
				Capabilities: []Capability{CapAuthoring},
				Runtime:      RuntimeSpec{Interactive: false},
				InputSchema: raw(`{
					"type": "object",
					"properties": {
						"plan": {"type": "string", "minLength": 1, "description": "the full plan markdown"}
					},
					"required": ["plan"],
					"additionalProperties": false
				}`),
				OutputSchema: raw(`{
					"type": "object",
					"properties": {
						"summary": {"type": "string", "minLength": 1, "description": "what was implemented, per plan step"},
						"changed_files": {"type": "array", "items": {"type": "string"}, "description": "repo-relative paths actually modified"}
					},
					"required": ["summary", "changed_files"],
					"additionalProperties": false
				}`),
			},
		},
		{
			Name: "review",
			Bundle: seedBundle(reviewBody, proprietaryLicense(),
				polyforgeProvenance("scenario step graphs (code_review / review_fix steps)",
					"The independent review gate. Producer isolation is enforced server-side "+
						"(read_only + independent producer): the reviewer must not be the same "+
						"producer that made the change it reviews.")),
			Contract: SkillContract{
				Capabilities: []Capability{CapReview},
				Runtime:      RuntimeSpec{Interactive: false},
				ParamsSchema: raw(`{
					"type": "object",
					"properties": {
						"scope": {"type": "string", "description": "what to review: the diff, the spec-compliance, or a named area"}
					},
					"additionalProperties": false
				}`),
				InputSchema: raw(`{
					"type": "object",
					"properties": {
						"spec": {"type": "string", "minLength": 1, "description": "the full spec markdown, OpenSpec grammar"},
						"plan": {"type": "string", "minLength": 1, "description": "the full plan markdown"},
						"changed_files": {"type": "array", "items": {"type": "string"}, "description": "repo-relative paths actually modified"}
					},
					"required": ["spec", "plan", "changed_files"],
					"additionalProperties": false
				}`),
				OutputSchema: raw(`{
					"type": "object",
					"properties": {
						"verdict": {"type": "string", "enum": ["pass", "warn", "fail"]},
						"summary": {"type": "string", "minLength": 1},
						"blockers": {"type": "array", "items": {"type": "string"}, "description": "must-fix findings; empty is legal"}
					},
					"required": ["verdict", "summary", "blockers"],
					"additionalProperties": false
				}`),
			},
		},
		{
			Name: "verification",
			Bundle: seedBundle(verificationBody, proprietaryLicense(),
				polyforgeProvenance("scenario step graphs (verification/test steps)",
					"The verification gate: runs the deterministic checks and reports honest "+
						"outcomes with evidence. A skip is reported as a skip, never as a pass.")),
			Contract: SkillContract{
				Capabilities: []Capability{CapVerification},
				Runtime:      RuntimeSpec{Interactive: false},
				ParamsSchema: raw(`{
					"type": "object",
					"properties": {
						"base_ref": {"type": "string", "minLength": 1, "description": "the explicit base ref used to inventory the complete change"},
						"commands": {
							"type": "array",
							"minItems": 1,
							"items": {
								"type": "object",
								"properties": {
									"name": {"type": "string", "minLength": 1},
									"command": {"type": "string", "minLength": 1},
									"working_directory": {"type": "string", "minLength": 1}
								},
								"required": ["name", "command", "working_directory"],
								"additionalProperties": false
							},
							"description": "the deterministic checks to run with explicit names and working directories"
						}
					},
					"required": ["base_ref", "commands"],
					"additionalProperties": false
				}`),
				InputSchema: raw(`{
					"type": "object",
					"properties": {
						"changed_files": {"type": "array", "items": {"type": "string"}, "description": "repo-relative paths actually modified"}
					},
					"required": ["changed_files"],
					"additionalProperties": false
				}`),
				OutputSchema: raw(`{
					"type": "object",
					"properties": {
						"summary": {"type": "string", "minLength": 1},
						"checks": {
							"type": "array",
							"minItems": 1,
							"items": {
								"type": "object",
								"properties": {
									"name": {"type": "string", "minLength": 1},
									"outcome": {"type": "string", "enum": ["pass", "fail", "skip"]},
									"detail": {"type": "string", "minLength": 1}
								},
								"required": ["name", "outcome", "detail"],
								"additionalProperties": false
							}
						}
					},
					"required": ["summary", "checks"],
					"additionalProperties": false
				}`),
			},
		},
		{
			Name: "ship",
			Bundle: seedBundle(shipBody, proprietaryLicense(),
				polyforgeProvenance("plugins/polyforge/skills/_common/storage.md + lifecycle.md (commit_and_pr)",
					"The shipping producer. Polyforge still owns commit/push/PR through its "+
						"own gated tools; this body drives them and never bypasses the "+
						"work-item-gated write rules (IR1).")),
			Contract: SkillContract{
				Capabilities: []Capability{CapShipping},
				Runtime:      RuntimeSpec{Interactive: false},
				ParamsSchema: raw(`{
					"type": "object",
					"properties": {
						"message": {"type": "string", "minLength": 1, "description": "the commit message"},
						"pr_title": {"type": "string", "minLength": 1},
						"pr_body": {"type": "string", "minLength": 1}
					},
					"required": ["message", "pr_title", "pr_body"],
					"additionalProperties": false
				}`),
				InputSchema: raw(`{
					"type": "object",
					"properties": {
						"changed_files": {"type": "array", "items": {"type": "string"}, "description": "repo-relative paths actually modified"},
						"review_verdict": {"type": "string", "enum": ["pass", "warn", "fail"]},
						"verification_checks": {
							"type": "array",
							"minItems": 1,
							"items": {
								"type": "object",
								"properties": {
									"name": {"type": "string", "minLength": 1},
									"outcome": {"type": "string", "enum": ["pass", "fail", "skip"]},
									"detail": {"type": "string", "minLength": 1}
								},
								"required": ["name", "outcome", "detail"],
								"additionalProperties": false
							}
						}
					},
					"required": ["changed_files", "review_verdict", "verification_checks"],
					"additionalProperties": false
				}`),
				OutputSchema: raw(`{
					"type": "object",
					"properties": {
						"pr_url": {"type": "string", "minLength": 1},
						"pr_number": {"type": "integer", "minimum": 1},
						"commits": {"type": "array", "items": {"type": "string"}, "description": "the commit shas shipped"}
					},
					"required": ["pr_url", "pr_number"],
					"additionalProperties": false
				}`),
			},
		},
		{
			Name: "ci",
			Bundle: seedBundle(ciBody, proprietaryLicense(),
				polyforgeProvenance("scenario step graphs (CI-watch steps)",
					"The post-ship CI watcher: a verification-capability step that follows the "+
						"PR's checks to a terminal state and reports honestly, including timeout.")),
			Contract: SkillContract{
				Capabilities: []Capability{CapVerification},
				Runtime:      RuntimeSpec{Interactive: false},
				InputSchema: raw(`{
					"type": "object",
					"properties": {
						"pr_url": {"type": "string", "minLength": 1}
					},
					"required": ["pr_url"],
					"additionalProperties": false
				}`),
				OutputSchema: raw(`{
					"type": "object",
					"properties": {
						"summary": {"type": "string", "minLength": 1},
						"checks": {
							"type": "array",
							"items": {
								"type": "object",
								"properties": {
									"name": {"type": "string", "minLength": 1},
									"outcome": {"type": "string", "enum": ["pass", "fail", "pending", "skip"]},
									"detail": {"type": "string"}
								},
								"required": ["name", "outcome"],
								"additionalProperties": false
							}
						}
					},
					"required": ["summary", "checks"],
					"additionalProperties": false
				}`),
			},
		},
	}
	if len(seeds) != 8 {
		return nil, errors.New("the canonical seed set must list exactly eight skills; update SeedSet and the docs together")
	}
	for _, s := range seeds {
		if err := s.validate(); err != nil {
			return nil, err
		}
	}
	return seeds, nil
}

// raw wraps a schema literal as RawMessage. The literals above are all
// inside the fail-closed subset; ValidateContract (via CompileSchema)
// refuses any keyword outside it, so a bad literal fails SeedSet, not the
// first publish.
func raw(s string) []byte { return []byte(s) }

// ─── The seed bodies ─────────────────────────────────────────────────────────

const grillMeBody = `# grill-me — interrogate the goal until it is sharp

` + seedRolePreamble + `
## Your role

You are the interviewer. A goal, proposal or design was handed to you
(params.topic, optionally params.context). Interrogate it until it is sharp
enough to author a spec from — or until the open questions that prevent that
are named precisely.

## Method

1. Restate the topic in one sentence and confirm the restatement.
2. Ask ONE question at a time. Each question must have a purpose you could
   name: an assumption to test, a boundary to find, a success criterion to
   pin, a risk to surface.
3. Drill on: the problem being solved and for whom; what "done" observably
   means; explicit non-goals; the sharpest edge cases; what breaks if the
   proposed approach is wrong.
4. Do not accept vague answers. Follow up: "you said X — does that mean A
   or B?" If the human cannot decide, record the question as open and move
   on; do not resolve it yourself by guessing.
5. Stop when new questions stop changing the requirements. Ending early is
   a failure mode; interrogating after sharpness is reached is another.

## Output

- record: the full interview log, each question and answer verbatim enough
  to audit.
- distilled_requirements: the agreed, sharp statements (each one sentence,
  testable).
- open_questions: what remains unresolved, each phrased as a question a
  decision-maker can answer.

Honesty rule for this step: never invent an answer on the human's behalf.
An unanswered question goes to open_questions, not to a plausible guess.
`

const specBody = `# spec — scope, requirements and acceptance criteria

` + seedRolePreamble + `
## Your role

Author the spec for the goal in params.goal. When the interview step is
present, ground the spec in inputs.interview_requirements and preserve
inputs.interview_open_questions as explicit unresolved decisions; when those
optional inputs are absent (the unattended composition), state unresolved
assumptions instead of inventing interview answers. The output must satisfy
the OpenSpec grammar, so it remains compatible with methodology.spec
artifacts, the /ui annotation viewer and /pf-revise:

## Contract

` + "```" + `markdown
## Requirements

### Requirement: <title>
- phrase each requirement with an RFC-2119 keyword (MUST / SHALL / SHOULD)
- at least one scenario per requirement:

#### Scenario: <title>
- GIVEN <state>
- WHEN <action>
- THEN <observable outcome>
` + "```" + `

Rules:
- Include non-goals and acceptance criteria; a requirement without a scenario
  is incomplete.
- mode=debug: phrase the broken behavior as a requirement being VIOLATED,
  with a scenario that reproduces it, plus a requirement capturing the fix.
- Scope to the goal you were given. A requirement you cannot tie to the goal
  belongs in open questions, not in the spec.

## Output

- spec: the complete markdown (a whole document, never a diff).
- requirements: the requirement titles, in document order.
`

const planBody = `# plan — ordered implementation steps

` + seedRolePreamble + `
## Your role

Author the implementation plan for the spec in inputs.spec. The plan keeps
the OpenSpec grammar from the spec and adds an ordered Steps section:

` + "```" + `markdown
## Steps

1. **<step_id>** (<size: xs|s|m|l>) - <what this step does>
   - Touched files: ` + "`<repo-relative path>` (write|read)" + `, ... - or "(no file edits)"
` + "```" + `

Rules:
- Every step carries a Touched files line (the file set a later reader can
  derive resource locks from), or the literal (no file edits).
- Ground every step in a requirement of the spec; a step with no requirement
  behind it is scope creep — flag it instead of planning it.
- Order for reviewability: small, independently checkable steps before
  large integrations; ship last.

## Output

- plan: the complete markdown.
- steps: the ordered step ids with their touched files, machine-readable.
`

const codeChangeBody = `# code-change — implement the plan

` + seedRolePreamble + `
## Your role

You are the WRITE producer: implement inputs.plan inside the claimed worktree
you run in. Later gates (review, verification) inspect what you produce, so
your output is what they will judge.

Rules:
- Work only inside the claimed work-tree. Every write happens there; no
  out-of-worktree edits, no direct pushes (the ship skill owns shipping).
- Follow the plan's steps and its Touched files; if reality disagrees with
  the plan, implement the honest smallest deviation and say so in the
  summary — never silently "fix" the plan.
- Self-check what you can (compile, quick tests) but do not claim
  verification results you did not produce; the verification gate exists to
  establish those.
- If you cannot complete the work, report incomplete/blocked with exactly
  what remains — an honest incomplete is a recoverable state.

## Output

- summary: what was implemented, per plan step, naming any deviations.
- changed_files: the repo-relative paths actually modified.
`

const reviewBody = `# review — independent gate

` + seedRolePreamble + `
## Your role

You are the REVIEW gate: inspect the work the producer made. Treat
inputs.changed_files as the producer's claimed file set, inspect the complete
worktree diff, and judge it against inputs.spec and inputs.plan (narrowed by
params.scope when supplied). You did not produce this work — that isolation is
the point of the gate; do not "help" by fixing what you review.

Method:
1. Read the change as a reviewer: correctness, spec compliance, security,
   test gaps, and the risks the producer did not name.
2. Every finding is concrete: file, line, why it matters, what would fix it.
3. Verdict:
   - pass: nothing blocking; minor notes may remain.
   - warn: real concerns that do not block shipping; name them.
   - fail: at least one blocker. List every blocker — a fail that hides its
     blockers is worse than no review.
4. Never soften a fail into a warn to be agreeable. A wrong pass is the one
   dishonest verdict this gate cannot take back.

## Output

- verdict: pass | warn | fail.
- summary: the review narrative, grounded in the actual diff.
- blockers: the must-fix findings ([] when none).
`

const verificationBody = `# verification — establish the work holds

` + seedRolePreamble + `
## Your role

You are the VERIFICATION gate. Verify the complete change represented by
inputs.changed_files and the worktree inventory against params.base_ref, then
run every check in params.commands. You establish facts, not opinions — code
review is a different gate.

## Mandatory inventory and status discipline

1. From the repository root, resolve the base explicitly. params.base_ref is
   this step's required base ref: substitute its literal value into the
   command — assign the shell variable before you use it, never expand one
   the shell has not been given. A step invoked with base_ref "origin/main"
   runs:

       base_ref='origin/main'
       base=$(git merge-base HEAD "$base_ref")

   Substitute the ref you were actually given for the example. A missing or
   empty params.base_ref, or a merge-base that does not resolve, is a failed
   inventory — never permission to compare against an implicit ref.
2. Build the complete NUL-delimited changed-path inventory from BOTH:

       git diff --name-only -z "$base" --
       git ls-files --others --exclude-standard -z

   The first covers tracked staged, unstaged, and committed-since-base paths;
   the second covers untracked paths while honoring ignore rules. Deduplicate
   without dropping paths containing spaces. Compare the result with
   inputs.changed_files; an omitted real path or a claimed path absent from
   the inventory is a FAIL that must be reported.
3. Derive affected packages/directories from every inventory entry, including
   deletions and untracked files. A targeted command is insufficient unless
   every changed package is covered. For a Go module the concrete broad check
   is "GOWORK=off go test ./..."; for a narrower Go run, enumerate every
   affected package with "GOWORK=off go test ./path/to/pkg" and report the
   coverage mapping. Use the repository's checked-in command for other stacks
   (for example "npm test -- --runInBand" only when that script exists); never
   invent a package selector from filenames and silently skip the rest.
4. Run every supplied command from its explicit working_directory through a
   shell with pipeline failure enabled. The required capture shape is:

       set +e
       bash -o pipefail -c "$command" >"$log" 2>&1
       status=$?
       set -e

   Record that status before reading or piping the log. A pipeline's last
   command must never mask an earlier failure.
5. Before trusting the harness, run non-mutating status-capture controls:
   "bash -o pipefail -c 'printf x | grep x'" MUST return 0 and
   "bash -o pipefail -c 'printf x | grep y'" MUST return non-zero. If either
   expectation is false, FAIL verification because command status is not
   trustworthy. These controls validate the harness only; they are not
   evidence that the product checks pass.

Rules:
- Record the real command, working directory, exit status, and meaningful
  output tail for every check. Status 0 is pass; non-zero is fail.
- A check you could not run is a SKIP with the reason. A skip is never a pass.
  A flaky-looking result is a fail with the transcript, not a pass.
- Do not modify the work under verification; if a check requires a fix, that
  is the producer's repair to make through the authorized repair path.
- Empty test selection, "no test files", or a command that matched no package
  is not silently green: prove the changed-package coverage or mark it fail.

## Output

- summary: the overall honest outcome in one or two sentences.
- checks: one entry per inventory/control/supplied check:
  {name, outcome: pass|fail|skip, detail}. detail always includes the command,
  working directory, captured status, coverage, and output excerpt.
`

const shipBody = `# ship — commit, push, open the PR

` + seedRolePreamble + `
## Your role

You are the SHIP step: advance the delivery lifecycle for the exact paths in
inputs.changed_files after inspecting inputs.review_verdict and
inputs.verification_checks. Shipping means commit, push, PR — through the
work-item-gated tools, never around them.

Rules:
- Ship ONLY inside the claimed work item's worktree, with the work item's
  own tools (pf_ship / pf_commit + pf_push + pf_pr). No raw git push, no
  direct-to-main, no bypass of the commit guard.
- Stage only inputs.changed_files after reconciling them with the actual
  worktree; scratch and state files stay out of the commit.
- Refuse shipping when inputs.review_verdict is "fail", or when any entry in
  inputs.verification_checks is "fail" or "skip". Do not infer gate state from
  prose or from step order.
- On partial failure (commit landed, push failed), report exactly what
  happened from the tool's own response fields (stage, side_effects), never
  a guess.

## Output

- pr_url / pr_number: the opened (or already-covering) PR.
- commits: the shas shipped.
`

const ciBody = `# ci — watch the PR's checks

` + seedRolePreamble + `
## Your role

You are the CI watcher: follow the checks of the PR in inputs.pr_url to a
terminal state and report it honestly.

Rules:
- Read check state from the PR's real CI status; name each check and its
  outcome.
- A red or missing required check is a fail — never "probably fine".
- If checks are still running when your time budget ends, report pending
  with the elapsed time: pending is an honest outcome, not a failure to
  report.
- A failing check's log tail belongs in the detail; the fix is the
  producer's job, not yours.

## Output

- summary: the CI state in one or two sentences.
- checks: one entry per check: {name, outcome: pass|fail|pending|skip,
  detail}.
`
