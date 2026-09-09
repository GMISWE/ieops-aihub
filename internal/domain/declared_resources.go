package domain

import (
	"encoding/json"
	"fmt"
	"net/url"
	"sort"
	"strings"
)

// aihub#238. Two distinct vocabularies meet on the wi surface and are easy to
// confuse, because one value (`file_scope`) is legal in exactly one of them:
//
//	declared_resources[].type : repo | path | document | section | service | external_ref
//	resource_locks.resource_type : git_branch | worktree | file_scope | tcp_port | deploy_env
//
// A declared type outside the first set used to be skipped in silence by every
// resourceToLock call site, so the wi acquired no locks, PredictConflicts
// answered {"predictions":[]} — a fake all-clear — and nothing anywhere logged a
// word. The two sets and their validators live here so the mapping has one home.

// declaredResourceTypes is the closed set of declared_resources `type` values
// resourceToLock understands (§25 mapping). Keep in lock-step with
// resourceToLock in conflicts.go.
//
// 🔴 THREE of these six are KNOWN types that map to NO lock — `external_ref`,
// and since aihub#416 also `repo` and `service` — which is why "resourceToLock
// returned an empty lock type" can never itself be used as the error signal.
// That sentence used to name external_ref alone, and the count is the only part
// of it that changed: the reasoning was always that an empty lock type is
// ambiguous between "unknown type" and "known type that locks nothing", and it
// is now ambiguous three ways instead of one. UnrecognizedDeclaredResources
// answers the question this map exists for, and it consults this map rather than
// the mapper's return value for exactly that reason.
//
// A type is in this set because it is a legal thing to DECLARE, not because it
// takes a lock. repo and service are still validated, still stored, and still
// read by PredictConflicts rules 2, 4 and 6; they simply derive no
// resource_locks row.
var declaredResourceTypes = map[string]bool{
	"repo":         true,
	"path":         true,
	"document":     true,
	"section":      true,
	"service":      true,
	"external_ref": true,
}

// resourceLockTypes mirrors the resource_locks.resource_type CHECK constraint in
// internal/db/migrations/0004_run_attempts.sql. The two copies are one
// invariant; TestResourceLockTypesMatchMigrationCheckConstraint parses the
// migration and fails if they drift.
var resourceLockTypes = map[string]bool{
	"git_branch": true,
	"worktree":   true,
	"file_scope": true,
	"tcp_port":   true,
	"deploy_env": true,
}

// sortedKeys renders a type set for an error message, so callers learn the legal
// values from the rejection itself rather than from a skill doc they may never
// read.
func sortedKeys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// DeclaredResourceTypeList returns the legal declared_resources `type` values.
// Exported for the MCP layer, which embeds them in its JSON Schema enum so the
// contract is visible before a call is made, not after it fails.
func DeclaredResourceTypeList() []string { return sortedKeys(declaredResourceTypes) }

// ResourceLockTypeList returns the legal resource_locks.resource_type values.
func ResourceLockTypeList() []string { return sortedKeys(resourceLockTypes) }

// declaredResourceURISchemes is the required `uri` scheme for each declared
// type — the same mapping resourceToLock relies on when it strips the prefix.
//
// ⚠️ THIS IS THE ONLY COPY OF THE RULE. The published JSON-Schema sentence is
// GENERATED from it by DeclaredResourceURISchemeDoc, so the contract a caller
// reads and the rule the server enforces cannot drift apart. Do not restate the
// schemes as a rule in the MCP layer, in a skill, or in a doc table: aihub#395
// exists because they were published in prose and enforced nowhere, and a second
// hand-written copy is how that comes back. (The `entry_shape` strings in the
// errors below are illustrations of the whole entry, not statements of the rule
// — they live in this file so drift is visible in one diff.)
//
// An empty value means "no fixed prefix": the type takes an absolute URL, which
// is the branch of uriSchemeProblem below that does not test a prefix.
// external_ref is the only such type today, deliberately — it names something
// outside this system entirely, so there is no polyforge-side namespace for a
// prefix to select.
//
// ⚠️ That reason used to be stated as "it is the one declared type that maps to
// no lock at all". Since aihub#416 that is false — repo and service map to no
// lock either — and it was never the load-bearing half anyway: repo: and
// service: keep their prefixes precisely because those names still MEAN
// something here (rules 2, 4 and 6 join on the whole uri, so the prefix is part
// of the value that is matched).
var declaredResourceURISchemes = map[string]string{
	"repo":         "repo:",
	"path":         "file:",
	"document":     "file:",
	"section":      "file:",
	"service":      "service:",
	"external_ref": "",
}

// DeclaredResourceURISchemeDoc renders the published per-type scheme sentence
// from declaredResourceURISchemes.
//
// Grouped by scheme rather than by type so the text stays short as types are
// added: three of today's six share `file:`. Types within a group and the groups
// themselves are sorted, so the output is stable and a diff of the rendered
// schema is caused by a real change rather than by map iteration order.
func DeclaredResourceURISchemeDoc() string {
	byScheme := map[string][]string{}
	for typ, scheme := range declaredResourceURISchemes {
		byScheme[scheme] = append(byScheme[scheme], typ)
	}
	schemes := make([]string, 0, len(byScheme))
	for scheme := range byScheme {
		schemes = append(schemes, scheme)
	}
	// Prefixed schemes first, in order; the no-fixed-prefix group last, because
	// "an absolute URL for external_ref" reads as the exception it is. Sorting
	// "" to the end rather than letting sort.Strings put it first is the only
	// reason this is not a bare sort.
	sort.Slice(schemes, func(i, j int) bool {
		if (schemes[i] == "") != (schemes[j] == "") {
			return schemes[j] == ""
		}
		return schemes[i] < schemes[j]
	})

	parts := make([]string, 0, len(byScheme))
	for _, scheme := range schemes {
		types := byScheme[scheme]
		sort.Strings(types)
		shape := "an absolute URL"
		if scheme != "" {
			shape = scheme + declaredResourceURIPlaceholder(scheme)
		}
		parts = append(parts, shape+" for "+strings.Join(types, "/"))
	}
	return strings.Join(parts, ", ")
}

// declaredResourceURIPlaceholders is presentation only: what to show after each
// scheme in the rendered sentence.
//
// Separate from declaredResourceURISchemes because it is not enforced — nothing
// checks the part after the scheme (see uriSchemeProblem) — and a table that
// mixes an enforced rule with a cosmetic one invites a reader to believe the
// cosmetic half is also checked. A scheme with no entry here renders as
// `<name>`, so adding a scheme without a placeholder produces a vaguer sentence
// rather than a wrong one.
var declaredResourceURIPlaceholders = map[string]string{
	"file:":    "<repo-relative-path>",
	"repo:":    "<repo-name>",
	"service:": "<name>",
}

// declaredResourceURIPlaceholder returns the placeholder for a scheme.
func declaredResourceURIPlaceholder(scheme string) string {
	if p, ok := declaredResourceURIPlaceholders[scheme]; ok {
		return p
	}
	return "<name>"
}

// declaredResourceSchemePrefixes returns the set of non-empty scheme prefixes
// the vocabulary reserves, derived rather than listed so a new type's scheme is
// reserved the moment its rule is written.
func declaredResourceSchemePrefixes() map[string]bool {
	out := map[string]bool{}
	for _, scheme := range declaredResourceURISchemes {
		if scheme != "" {
			out[scheme] = true
		}
	}
	return out
}

// uriSchemeProblem reports why uri is wrong for typ, or "" when it is fine.
//
// Only the SCHEME is checked, and that boundary is deliberate. What comes after
// it — a path that exists, a repo the project actually contains, a reachable URL
// — is not knowable here, and a check that guessed at it would reject legal
// input, which is the one failure mode worse than the one being fixed: the cheap
// way out of a validator that refuses good payloads is to delete the validator.
// Note in particular that a bare `"file:"` with nothing after it still passes
// this; that entry derives an empty lock key and is reported by the separate
// no-uri path in UnrecognizedDeclaredResources, which is where the existing
// remedy for it lives.
func uriSchemeProblem(typ, uri string) (problem, expected string) {
	scheme, known := declaredResourceURISchemes[typ]
	if !known {
		// Unreachable through ValidateDeclaredResources, which rejects an unknown
		// type first. Returning "" rather than an error keeps this function honest
		// about what it measured: it has no rule for this type, so it found no
		// scheme problem.
		return "", ""
	}
	if scheme != "" {
		if strings.HasPrefix(uri, scheme) {
			return "", scheme
		}
		return fmt.Sprintf("expected the %q scheme", scheme), scheme
	}
	// The no-fixed-prefix case: an absolute URL, and specifically not one of the
	// prefixes that name a DIFFERENT declared type — reaching for the wrong one
	// of two adjacent vocabularies is the actual mistake this catches.
	for reserved := range declaredResourceSchemePrefixes() {
		if strings.HasPrefix(uri, reserved) {
			return fmt.Sprintf("%q names another declared type, not a URL", reserved),
				"an absolute URL"
		}
	}
	u, err := url.Parse(uri)
	if err != nil || u.Scheme == "" || (u.Host == "" && u.Opaque == "") {
		return "expected an absolute URL with a scheme", "an absolute URL"
	}
	return "", "an absolute URL"
}

// ValidateDeclaredResources rejects a declared_resources payload the lock mapper
// cannot understand, so a mistake costs a 400 at the call that made it instead
// of a silently lockless work item.
//
// Use this ONLY on caller-supplied input — CreateWorkItem, UpdateWorkItem and
// PredictConflicts. Do NOT use it on paths that read already-stored
// declared_resources (claim, force_takeover, acquire_locks): roughly 14% of
// existing entries would fail it, and those work items must stay claimable.
//
// All THREE of those stored-data paths report what they could not map: each
// calls UnrecognizedDeclaredResources and returns the result under the same
// `unrecognized_resources` key — on ClaimResponse, on ForceTakeoverResponse and
// on AcquireLocksResponse (aihub#509, closing the surviving half of aihub#411
// T2-12).
//
// ⚠️ This paragraph used to say the opposite, and closed with "Do not read this
// comment as 'all three report'". Both takeover and acquire_locks skipped
// unmappable entries in silence because neither response had a field for it, and
// the standing excuse was that a takeover is always followed by a fresh claim
// which does report. That is a property of the polyforge skill flow, not of the
// HTTP surface: each of these is its own endpoint, and acquire_locks in
// particular exists to answer "which locks do I hold" — a question a skipped
// declaration silently changes the answer to. Kept as history rather than
// deleted, because "the next call reports it" is the shape of the argument, and
// it will be reachable for whatever field is added next.
func ValidateDeclaredResources(raw json.RawMessage) *AihubError {
	trimmed := strings.TrimSpace(string(raw))
	if len(trimmed) == 0 || trimmed == "null" {
		return nil
	}

	var items []map[string]any
	if err := json.Unmarshal(raw, &items); err != nil {
		return NewErrDetails(ErrBadRequest,
			"declared_resources must be a JSON array of resource objects",
			map[string]any{
				"parse_error":  err.Error(),
				"valid_types":  DeclaredResourceTypeList(),
				"entry_shape":  `{"type":"path","uri":"file:<repo-relative-path>","intent":"write"}`,
				"common_error": "`type` takes a DECLARED type, not a lock type; the path field is `uri`, not `value`/`path`/`scope`",
			})
	}

	for i, item := range items {
		typ, _ := item["type"].(string)
		if !declaredResourceTypes[typ] {
			return NewErrDetails(ErrBadRequest,
				fmt.Sprintf("declared_resources[%d]: unrecognized type %q — it would acquire no lock at all", i, typ),
				map[string]any{
					"index":       i,
					"got_type":    typ,
					"valid_types": DeclaredResourceTypeList(),
					"entry_shape": `{"type":"path","uri":"file:<repo-relative-path>","intent":"write"}`,
					"hint":        "`file_scope`, `git_branch`, `worktree`, `tcp_port` and `deploy_env` are resource_locks.resource_type values; declared_resources.type is the input vocabulary above. Since aihub#416 the server DERIVES exactly one of them — `file_scope`, from path/document/section entries; the other four are legal only in an explicit requested_locks. A file path is type=\"path\".",
				})
		}
		uri, _ := item["uri"].(string)
		if strings.TrimSpace(uri) == "" {
			return NewErrDetails(ErrBadRequest,
				fmt.Sprintf("declared_resources[%d]: `uri` is required and must be non-empty", i),
				map[string]any{
					"index":       i,
					"got_type":    typ,
					"entry_shape": `{"type":"path","uri":"file:<repo-relative-path>","intent":"write"}`,
					"hint":        "the field is `uri` — `value`, `path`, `scope` and `resource_key` are silently ignored; expected schemes are " + DeclaredResourceURISchemeDoc(),
				})
		}
		// aihub#395 part 4. The scheme has been published per type since
		// aihub#238 and was validated nowhere, while resourceToLock derives the
		// lock key with strings.TrimPrefix — a NO-OP on a wrong prefix. So
		// {"type":"repo","uri":"file:x"} was a 200 that keyed its git_branch lock
		// as "file:x/main": a well-formed lock on a key nothing else will ever
		// collide with, listed in the claim response's acquired_locks, on a work
		// item whose author believes the repo is guarded.
		//
		// That is why this has to be a REJECTION and not a warning. The three
		// signals an existing guard looks at — an error, an empty lock type, an
		// empty key — are all absent in this shape; the only evidence the caller
		// ever sees says it worked.
		if problem, expected := uriSchemeProblem(typ, uri); problem != "" {
			return NewErrDetails(ErrBadRequest,
				fmt.Sprintf("declared_resources[%d]: uri %q is not valid for type %q — %s",
					i, uri, typ, problem),
				map[string]any{
					"index":           i,
					"got_type":        typ,
					"got_uri":         uri,
					"expected_scheme": expected,
					"schemes_by_type": DeclaredResourceURISchemeDoc(),
					"hint":            "the lock key is derived by stripping the scheme, and stripping a prefix that is not there silently succeeds — so a wrong scheme does not fail, it produces a lock on a key nobody will collide with",
				})
		}
	}
	return nil
}

// UnrecognizedDeclaredResources returns one human-readable descriptor per entry
// whose type the lock mapper cannot understand, and never fails.
//
// This is the stored-data counterpart of ValidateDeclaredResources: the lock
// derivation paths cannot reject historical rows without making those work items
// unclaimable, but they must stop being silent about them. All three surface the
// result under the same `unrecognized_resources` key — FnClaimWorkItem at both
// its exits, FnForceTakeover and FnAcquireLocks since aihub#509 — so the operator
// sees that a resource they declared is holding no lock.
func UnrecognizedDeclaredResources(raw json.RawMessage) []string {
	trimmed := strings.TrimSpace(string(raw))
	if len(trimmed) == 0 || trimmed == "null" {
		return nil
	}
	var items []map[string]any
	if err := json.Unmarshal(raw, &items); err != nil {
		return []string{"declared_resources is not a JSON array of objects — no locks could be derived from it"}
	}

	var out []string
	for i, item := range items {
		typ, _ := item["type"].(string)
		uri, _ := item["uri"].(string)
		hasURI := strings.TrimSpace(uri) != ""

		switch {
		case !declaredResourceTypes[typ]:
			shown := typ
			if shown == "" {
				shown = "(absent)"
			}
			desc := fmt.Sprintf("declared_resources[%d]: type %s acquires no lock", i, shown)
			if hasURI {
				desc += fmt.Sprintf(" (uri %q)", uri)
			}
			out = append(out, desc)

		case typ != "external_ref" && !hasURI:
			// A recognized type with no uri is the second silent shape: it maps to a
			// well-typed lock with an EMPTY key (e.g. service -> ("deploy_env","")),
			// which the claim path now refuses to insert. Report it, or skipping it
			// would be just as quiet as the bug this change fixes.
			// external_ref is exempt: it takes no lock either way.
			out = append(out, fmt.Sprintf(
				"declared_resources[%d]: type %s has no `uri`, so it acquires no lock (the field is `uri`, not value/path/scope)", i, typ))
		}
	}
	return out
}

// ValidateRequestedLocks rejects a client-supplied requested_locks slice before
// it reaches Postgres.
//
// Without this, guessing the neighbouring declared_resources {type, value} shape
// leaves resource_type empty, the row trips the resource_locks CHECK constraint,
// and the caller gets `500 INTERNAL_ERROR: failed to acquire lock :: ERROR: new
// row for relation "resource_locks" violates check constraint
// "resource_locks_resource_type_check" (SQLSTATE 23514)`. That message is worse
// than opaque, it is misleading: it reads as "file_scope is not allowed" when the
// constraint does list file_scope and the empty column was resource_type.
func ValidateRequestedLocks(locks []ResourceLockReq) *AihubError {
	for i, l := range locks {
		if !resourceLockTypes[l.ResourceType] {
			return NewErrDetails(ErrBadRequest,
				fmt.Sprintf("requested_locks[%d]: unrecognized resource_type %q", i, l.ResourceType),
				map[string]any{
					"index":             i,
					"got_resource_type": l.ResourceType,
					"valid_types":       ResourceLockTypeList(),
					"entry_shape":       `{"resource_type":"file_scope","resource_key":"<project>:<path>"}`,
					"hint":              "requested_locks uses resource_type/resource_key — NOT declared_resources' type/uri. Normally leave requested_locks unset and let the server derive locks from the work item's declared_resources.",
				})
		}
		if strings.TrimSpace(l.ResourceKey) == "" {
			return NewErrDetails(ErrBadRequest,
				fmt.Sprintf("requested_locks[%d]: `resource_key` is required and must be non-empty", i),
				map[string]any{
					"index":       i,
					"entry_shape": `{"resource_type":"file_scope","resource_key":"<project>:<path>"}`,
					"hint":        "file_scope keys are \"<project>:<repo>:<repo-relative-path>\", or \"<project>:<repo-relative-path>\" when the declaration names no repo (aihub#222, aihub#261); git_branch keys are \"<repo>/<branch>\"",
				})
		}
	}
	return nil
}
