package domain

import (
	"encoding/json"
	"fmt"
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
// external_ref is deliberately present: it is a KNOWN type that maps to NO lock,
// which is why "resourceToLock returned an empty lock type" can never itself be
// used as the error signal.
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

// ValidateDeclaredResources rejects a declared_resources payload the lock mapper
// cannot understand, so a mistake costs a 400 at the call that made it instead
// of a silently lockless work item.
//
// Use this ONLY on caller-supplied input — CreateWorkItem, UpdateWorkItem and
// PredictConflicts. Do NOT use it on paths that read already-stored
// declared_resources (claim, force_takeover, acquire_locks): roughly 14% of
// existing entries would fail it, and those work items must stay claimable.
//
// Of those stored-data paths, only FnClaimWorkItem currently reports what it
// could not map — it calls UnrecognizedDeclaredResources and returns the result
// as ClaimResponse.unrecognized_resources. FnForceTakeover and FnAcquireLocks
// still skip unmappable entries without reporting, because neither response
// carries a field for it; force_takeover is at least always followed by a fresh
// claim, which does report. Do not read this comment as "all three report".
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
					"hint":        "`file_scope`, `git_branch`, `worktree`, `tcp_port` and `deploy_env` are resource_locks.resource_type values (what the server DERIVES); declared_resources.type is the input vocabulary above. A file path is type=\"path\".",
				})
		}
		if uri, _ := item["uri"].(string); strings.TrimSpace(uri) == "" {
			return NewErrDetails(ErrBadRequest,
				fmt.Sprintf("declared_resources[%d]: `uri` is required and must be non-empty", i),
				map[string]any{
					"index":       i,
					"got_type":    typ,
					"entry_shape": `{"type":"path","uri":"file:<repo-relative-path>","intent":"write"}`,
					"hint":        "the field is `uri` — `value`, `path`, `scope` and `resource_key` are silently ignored; expected schemes are file: for path/document/section, repo: for repo, service: for service",
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
// unclaimable, but they must stop being silent about them. Callers surface the
// result (claim returns it as unrecognized_resources) so the operator sees that
// a resource they declared is holding no lock.
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
					"hint":        "file_scope keys are project-namespaced as \"<project>:<repo-relative-path>\" (aihub#222); git_branch keys are \"<repo>/<branch>\"",
				})
		}
	}
	return nil
}
