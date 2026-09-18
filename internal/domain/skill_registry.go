package domain

// skill_registry.go — the versioned skill registry (aihub#708 Batch 1A):
// vocabularies, result/request types, caller validation, THE accessibility
// predicate, and the create/publish entrypoints.
//
// The storage shape is migration 0043: `skills` holds the stable owner/key
// identity plus the expected-latest CAS token, `skill_versions` holds the
// immutable versions (closed bundle + runtime contract + content digest), and
// the per-version project-sharing grants table holds who a version is shared
// with. The pure
// validation halves live in internal/skillregistry; this file and
// skill_registry_sharing.go are the authorized, transactional half.
//
// ─── The security posture, in four sentences ─────────────────────────────────
//
//  1. PRIVATE BY DEFAULT: a published version is visible to its owner and to
//     unscoped global admins until it is explicitly shared — per version, so
//     no sharing ever reaches a future version (spec D1).
//  2. NO METADATA ORACLE: an inaccessible skill or version answers the same
//     NOT_FOUND as a nonexistent one, list results never include skills the
//     caller cannot see any version of, and the total version count is
//     disclosed only to the owner and admins — a stranger must not be able to
//     learn that a v2 exists at all (the spec's "v2 content is not disclosed"
//     includes its metadata).
//  3. AUTH RECHECK ON EVERY READ: accessibility is recomputed from the current
//     rows on every call — there is no cached grant, so a revoked grant or a
//     public→private flip blocks the very next fetch (spec D3). Already
//     disclosed bytes cannot be recalled; access going forward can.
//  4. SCOPED API KEYS STAY SCOPED: a key with ProjectScope may read only
//     versions granted to that one project (where it must still hold project
//     rights) and may not create, publish or share anything. Not even public
//     versions: checkProjectAccess answers an out-of-scope project NOT_FOUND
//     regardless of its visibility, and a scoped key gets the same
//     confinement here — public registry data is global data, and a scoped
//     key is not a global reader.
//
// ─── Publication is a compare-and-set under a row lock ─────────────────────
//
// PublishSkillVersion opens a transaction, takes `SELECT ... FOR UPDATE` on
// the skills row, and refuses when the caller's expected_latest does not
// match the locked row's latest_version (409 CONFLICT_CAS_FAILED, details
// carry the current value). Two publishers who both read latest=N therefore
// serialize: the first commits N+1, the second wakes under the lock, sees N+1,
// and is refused — at most one publish per expected value (the Requirement's
// "at most one publish succeeds").
//
// ─── Version retention ───────────────────────────────────────────────────────
//
// No code path deletes a skill version, so a version pinned by a work-item
// flow (Batch 2 stores exact (skill_id, version) refs) stays readable by
// construction (spec D1 "referenced versions are retained"). Grant rows may
// be revoked and visibility may flip; content rows are forever.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/GMISWE/ieops-aihub/internal/skillregistry"
)

// ─── Vocabularies (mirrored by CHECKs in migration 0043) ────────────────────
//
// aihub#396 policy: a vocabulary a DB CHECK enforces is ALSO validated in Go
// and answered with a 400 naming the field, and a test holds the Go copy to
// the migration (TestSkillRegistryVocabulariesMatchTheMigration parses
// 0043_skill_registry.sql and fails if either copy moves).

var skillNameRE = regexp.MustCompile(`^[a-z][a-z0-9-]{0,63}$`)

// Skill visibility values, mirroring the skill_versions CHECK. There is no
// "project" value: project sharing is grant ROWS, not a third visibility
// tier, so the two mechanisms cannot disagree about what "shared" means.
var skillVisibilityValues = map[string]bool{
	"private": true,
	"public":  true,
}

// SkillVisibilityList returns the legal skill_versions.visibility values,
// sorted. Exported for the same reason the other vocabulary lists are: the
// published API descriptions quote it, and dbCheckPolicies holds this list
// to the migration's CHECK (aihub#396/#434 — the policy registry parses
// 0043_skill_registry.sql and asserts the Go set and the SQL IN-list are
// equal).
func SkillVisibilityList() []string { return sortedKeys(skillVisibilityValues) }

// List page sizes. Out-of-range is REFUSED with a 400 rather than clamped: a
// clamp would need the request_adjusted disclosure to be honest about it
// (aihub#314), and this API has no caller yet — refusing is the simpler,
// honest contract.
const (
	skillListDefaultLimit = 50
	skillListMaxLimit     = 200
)

// pgUniqueViolation is Postgres SQLSTATE 23505, matched on the code (not the
// constraint name, which a migration may rename — the same reasoning as
// pgForeignKeyViolation in wi_watches.go).
const pgUniqueViolation = "23505"

func isUniqueViolation(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == pgUniqueViolation
}

// ─── Result types ────────────────────────────────────────────────────────────

// SkillIdentity is the stable owner/key identity of a skill.
type SkillIdentity struct {
	ID           string `json:"id"`
	Name         string `json:"name"`
	OwnerUserID  string `json:"owner_user_id"`
	OwnerDisplay string `json:"owner_display"`
	// LatestVersion is the total number of published versions. It is populated
	// ONLY for the owner and unscoped global admins; for every other caller it
	// stays zero and is omitted — the count is itself skill metadata a
	// restricted reader must not be told (file header, rule 2).
	LatestVersion int       `json:"latest_version,omitempty"`
	CreatedAt     time.Time `json:"created_at"`
	// UpdatedAt is when the identity last changed — publication included.
	// It is OWNER-SENSITIVE: it moves on private publishes a restricted
	// reader must not learn about, so for every caller who is not the owner
	// or an unscoped admin, buildSkillDetail overwrites it with the
	// created_at of the caller's latest ACCESSIBLE version (see
	// skill_registry_sharing.go).
	UpdatedAt time.Time `json:"updated_at"`
}

// SkillVersionSummary names a version without its content.
type SkillVersionSummary struct {
	SkillID      string    `json:"skill_id"`
	Version      int       `json:"version"`
	Visibility   string    `json:"visibility"`
	Digest       string    `json:"digest"`
	AuthorUserID string    `json:"author_user_id"`
	CreatedAt    time.Time `json:"created_at"`
}

// SkillVersion is a full version: summary plus the immutable content.
type SkillVersion struct {
	SkillVersionSummary
	Bundle   json.RawMessage `json:"bundle"`
	Contract json.RawMessage `json:"contract"`
}

// SkillDetail is an identity plus the caller's latest ACCESSIBLE version —
// never the skill's true latest unless the caller can see it (file header,
// rule 2).
type SkillDetail struct {
	SkillIdentity
	// LatestAccessible is nil only for an owner/admin viewing a skill that has
	// no versions yet; for every other caller a nil here never leaves this
	// package (the skill answers NOT_FOUND instead).
	LatestAccessible *SkillVersionSummary `json:"latest_accessible,omitempty"`
}

// ─── Request types ───────────────────────────────────────────────────────────

// CreateSkillRequest creates the stable identity. A skill starts with no
// versions; content arrives via PublishSkillVersion.
type CreateSkillRequest struct {
	Name string `json:"name"`
}

// PublishSkillVersionRequest publishes the next immutable version under the
// expected-latest compare-and-set. ExpectedLatest is the latest_version the
// caller last saw: 0 for a freshly created skill, N for "I have seen up to N".
// Bundle and Contract are raw JSON, decoded and validated by
// internal/skillregistry (closed bundle, closed contract, fail-closed schema
// subset) before anything is written.
//
// The new version is always published PRIVATE. Sharing it is a separate,
// authorized action (ShareSkillVersionWithProject /
// SetSkillVersionVisibility), so a publish can never leak a future version
// through an old grant (spec D1).
type PublishSkillVersionRequest struct {
	SkillID        string          `json:"skill_id"`
	ExpectedLatest int             `json:"expected_latest"`
	Bundle         json.RawMessage `json:"bundle"`
	Contract       json.RawMessage `json:"contract"`
	// ContentDigest, when non-empty, must equal the server-computed digest of
	// (bundle, contract). It lets a caller verify transport integrity and
	// makes Batch 4A's seed/import content-checked.
	ContentDigest string `json:"content_digest,omitempty"`
}

// SkillVersionShareRequest names a version and a project for
// ShareSkillVersionWithProject / RevokeSkillVersionFromProject.
type SkillVersionShareRequest struct {
	SkillID string `json:"skill_id"`
	Version int    `json:"version"`
	Project string `json:"project"`
}

// ListSkillsRequest pages through the skills visible to the caller.
type ListSkillsRequest struct {
	// Owner, when set, filters to one owner's skills (intersected with
	// visibility, so this is not an oracle for other owners' skills).
	Owner *string `json:"owner,omitempty"`
	// Limit: 0 means the default (50); 1..200 or it is refused.
	Limit int `json:"limit,omitempty"`
	// Cursor is the last skill ID of the previous page.
	Cursor string `json:"cursor,omitempty"`
}

// ─── Caller validation ────────────────────────────────────────────────────────

// skillRequireWriteCaller validates the caller for a registry write:
// authenticated, a known global role, and NOT a project-scoped API key.
// Writes are confined to unscoped keys because a skill is a personal
// resource outside every project — creating, publishing or sharing one from
// a project-scoped key would be exactly the scope escape the task forbids.
func skillRequireWriteCaller(caller *UserRecord) *AihubError {
	if caller == nil || caller.ID == "" {
		return NewErr(ErrUnauthorized, "not authenticated")
	}
	if caller.Role != "writer" && caller.Role != "admin" {
		return NewErr(ErrForbidden, "skill registry writes require role writer or admin")
	}
	if caller.ProjectScope != nil {
		return NewErr(ErrForbidden,
			fmt.Sprintf("api key is scoped to project %q; a scoped key cannot write the skill registry", *caller.ProjectScope))
	}
	return nil
}

// skillRequireReadCaller validates the caller for a registry read. Scoped keys
// may read (confined to their project's grants by the accessibility
// predicate); admins are distinguished from writers by the predicate too.
func skillRequireReadCaller(caller *UserRecord) *AihubError {
	if caller == nil || caller.ID == "" {
		return NewErr(ErrUnauthorized, "not authenticated")
	}
	if caller.Role != "writer" && caller.Role != "admin" {
		return NewErr(ErrForbidden, "skill registry reads require role writer or admin")
	}
	return nil
}

// skillOwnerOrAdmin reports whether the caller may act on a skill they do not
// own: unscoped global admins may; nobody else. A scoped key is NEVER an
// owner for this purpose — even on its own user's skill, a scoped key gets
// read access only through grants on the scoped project (file header, rule 4).
func skillOwnerOrAdmin(caller *UserRecord, ownerUserID string) bool {
	if caller.Role == "admin" && caller.ProjectScope == nil {
		return true
	}
	return caller.ID == ownerUserID && caller.ProjectScope == nil
}

// skillSeesAllVersions reports whether the caller sees every version of every
// skill: unscoped admins only. (Owners are covered by the predicate's
// own-skills arm instead.)
func skillSeesAllVersions(caller *UserRecord) bool {
	return caller.Role == "admin" && caller.ProjectScope == nil
}

// ─── THE accessibility predicate (one SQL copy) ───────────────────────────────
//
// skillVersionAccessSQL renders the caller-scoped "can this caller read this
// skill_versions row" predicate. Every SQL reader of skill versions MUST
// build its WHERE clause through this function (skill_registry_singlecopy_test.go
// is the structural gate); a re-inlined copy is the aihub#379 defect class.
//
// The rule it encodes, in full:
//
//	unscoped admin        → every version (no predicate at all)
//	unscoped caller       → public versions, own skills' versions, and
//	                        versions granted to a project the caller owns or
//	                        is a member of
//	project-scoped caller → ONLY versions granted to the scoped project,
//	                        where the caller is that project's owner or
//	                        member (a scoped admin passes the membership half,
//	                        the same way checkProjectAccess lets an admin pass
//	                        once the scope gate is behind them)
//
// Membership is checked against projects.members with jsonb containment, so
// a role change or removal in the project takes effect on the next read — no
// cached grant (the auth-recheck rule). Membership is role-agnostic: a grant
// shares at viewer level, and every member role outranks viewer.
//
// alias is the SQL alias of the skill_versions row (e.g. "sv" or "v"); idx is
// the first unused placeholder index; the returned clause starts with " AND "
// (or is " AND FALSE") so call sites append it to a WHERE body.
func skillVersionAccessSQL(alias string, caller *UserRecord, idx int) (clause string, args []any, nextIdx int) {
	nextIdx = idx

	// Unscoped admin sees everything: no predicate at all.
	if caller.Role == "admin" && caller.ProjectScope == nil {
		return " AND TRUE", nil, nextIdx
	}

	// The membership probe for the jsonb containment arm. It is built from
	// the caller's ID, and a marshal of one map of one string cannot fail;
	// if it ever did, fail closed rather than skip the membership check.
	memberProbe, err := json.Marshal([]map[string]string{{"user_id": caller.ID}})
	if err != nil {
		return " AND FALSE", nil, nextIdx
	}

	if caller.ProjectScope != nil {
		// Scoped: the grant must name the scoped project. Public versions and
		// the caller's own private versions are deliberately NOT reachable
		// through a scoped key (file header, rule 4). The arm is wrapped in
		// AND (…) so it can never dissolve into the caller's WHERE as a bare
		// OR — a predicate that starts with OR after "key = $1 AND key = $2"
		// matches THE REQUESTED ROW UNCONDITIONALLY, which is precisely the
		// disclosure this function exists to prevent (caught live by
		// TestSkillRegistryScopedKeysStayScoped).
		grantClause, grantArgs, next := skillGrantExistsSQL(alias, idx, true, caller, memberProbe)
		return " AND (" + grantClause + ")", grantArgs, next
	}

	// Unscoped: the own-skills arm consumes $idx, so the grant arm's
	// placeholders start at $idx+1 and its args ride AFTER the caller's.
	// firstIdx (not idx) is what keeps the two aligned: the public arm and
	// the grant arm share one bind value for the caller, but the probe
	// must not be renumbered under it.
	grantClause, grantArgs, nextIdx := skillGrantExistsSQL(alias, idx+1, false, caller, memberProbe)
	clause = fmt.Sprintf(
		" AND (%[1]s.visibility = 'public' OR %[1]s.skill_id IN (SELECT id FROM skills s2 WHERE s2.owner_user_id = $%[2]d) OR %[3]s)",
		alias, idx, grantClause)
	args = append([]any{caller.ID}, grantArgs...)
	return clause, args, nextIdx
}

// skillGrantExistsSQL renders the EXISTS arm: a grant row on this version,
// naming a project the caller holds rights in. The arm is emitted as bare
// "EXISTS (…)" — no leading OR, no AND: the callers compose it inside their
// own parens, which is what keeps the whole predicate one ANDed term.
// firstIdx is the placeholder index for the arm's OWN first bind value (the
// caller), because the arm is embedded after other clauses that may already
// occupy earlier placeholders — skillVersionAccessSQL's unscoped branch passes
// idx+1 for exactly that reason. withScope additionally pins the grant to the
// caller's scoped project and lets a scoped admin pass the membership check
// (mirroring checkProjectAccess's admin level). The grants table is named in
// exactly this one function — the single-copy gate greps for it, so do not
// restate the table name in SQL anywhere else in this package.
func skillGrantExistsSQL(alias string, firstIdx int, withScope bool, caller *UserRecord, memberProbe []byte) (clause string, args []any, nextIdx int) {
	// args order: $firstIdx = caller ID (project-owner arm / membership arm),
	//            $firstIdx+1 = membership probe,
	//            then optionally the scoped-admin flag, then the scope pin.
	args = append(args, caller.ID, string(memberProbe))
	clause = fmt.Sprintf(`
	    EXISTS (
	        SELECT 1 FROM skill_version_grants g
	        JOIN projects p ON p.name = g.project_name
	        WHERE g.skill_id = %[1]s.skill_id
	          AND g.version  = %[1]s.version
	          AND (p.owner_user_id = $%[2]d OR p.members @> $%[3]d::jsonb`,
		alias, firstIdx, firstIdx+1)
	nextIdx = firstIdx + 2
	if caller.Role == "admin" {
		args = append(args, true)
		clause += fmt.Sprintf(" OR $%d", nextIdx)
		nextIdx++
	}
	clause += ")"
	if withScope {
		if caller.ProjectScope == nil {
			// Defensive: withScope is only set by the scoped branch, which
			// runs only with a scope. If this ever runs without one, fail
			// closed.
			return " AND FALSE", nil, nextIdx
		}
		args = append(args, *caller.ProjectScope)
		clause += fmt.Sprintf(" AND g.project_name = $%d", nextIdx)
		nextIdx++
	}
	clause += ")"
	return clause, args, nextIdx
}

// ─── Create ──────────────────────────────────────────────────────────────────

// CreateSkill creates the stable owner/key identity: an owner plus a name,
// unique per owner, with zero versions. The name is the stable key callers
// refer to the skill by alongside its id.
func CreateSkill(ctx context.Context, pool *pgxpool.Pool, caller *UserRecord, req CreateSkillRequest) (*SkillDetail, *AihubError) {
	if aerr := skillRequireWriteCaller(caller); aerr != nil {
		return nil, aerr
	}
	if !skillNameRE.MatchString(req.Name) {
		return nil, NewErr(ErrBadRequest,
			fmt.Sprintf("skill name %q is invalid: must match ^[a-z][a-z0-9-]{0,63}$", req.Name))
	}
	if pool == nil {
		return nil, NewErr(ErrInternalError, "create skill: no database")
	}

	id := NewID("skill")
	_, err := pool.Exec(ctx, `
		INSERT INTO skills (id, owner_user_id, name) VALUES ($1, $2, $3)`,
		id, caller.ID, req.Name)
	if err != nil {
		// A (owner, name) unique violation is the caller's own duplicate key.
		if isUniqueViolation(err) {
			return nil, NewErr(ErrConflictDuplicate,
				fmt.Sprintf("you already have a skill named %q", req.Name))
		}
		return nil, dbErrCause(err, "create skill")
	}
	return GetSkill(ctx, pool, caller, id)
}

// ─── Publish (expected-latest CAS under a row lock) ──────────────────────────

// PublishSkillVersion publishes the next immutable version of a skill.
//
// The whole operation is one transaction around `SELECT ... FOR UPDATE` on
// the skills row:
//
//  1. lock the identity row (the publication row lock);
//  2. authorize inside the lock — the ownership check reads the locked row,
//     so nothing can change between the check and the write (the same TOCTOU
//     reasoning as UpdateProject's re-validation);
//  3. refuse when expected_latest != the locked row's latest_version (409
//     CONFLICT_CAS_FAILED, details carry current_latest so the caller can
//     re-read and retry);
//  4. insert version = latest+1, always private, with the canonical bundle,
//     contract and the server-computed digest;
//  5. write latest_version = latest+1 back, commit.
//
// Two concurrent publishers who both expected latest=N serialize on the
// lock: exactly one wins — the Requirement's "at most one publish succeeds".
func PublishSkillVersion(ctx context.Context, pool *pgxpool.Pool, caller *UserRecord, req PublishSkillVersionRequest) (*SkillVersionSummary, *AihubError) {
	if aerr := skillRequireWriteCaller(caller); aerr != nil {
		return nil, aerr
	}
	if req.SkillID == "" {
		return nil, NewErr(ErrBadRequest, "skill_id is required")
	}
	if req.ExpectedLatest < 0 {
		return nil, NewErr(ErrBadRequest,
			fmt.Sprintf("expected_latest %d is invalid: it must be >= 0", req.ExpectedLatest))
	}

	// Decode + validate everything BEFORE touching the database: the closed
	// bundle, the closed contract, the fail-closed schema subset and the
	// optional content-digest check are all pure, and a request that cannot
	// be published must fail with a 400 naming the reason rather than a
	// database error — or worse, a written row.
	bundle, aerr := decodeSkillBundle(req.Bundle)
	if aerr != nil {
		return nil, aerr
	}
	contract, aerr := decodeSkillContract(req.Contract)
	if aerr != nil {
		return nil, aerr
	}
	bundleJSON, bundleErr := skillregistry.CanonicalBundleJSON(bundle)
	if bundleErr != nil {
		return nil, NewErr(ErrBadRequest, fmt.Sprintf("bundle does not serialize: %v", bundleErr))
	}
	contractJSON, contractErr := skillregistry.CanonicalContractJSON(contract)
	if contractErr != nil {
		return nil, NewErr(ErrBadRequest, fmt.Sprintf("contract does not serialize: %v", contractErr))
	}
	digest := skillregistry.VersionDigest(bundleJSON, contractJSON)
	if req.ContentDigest != "" && req.ContentDigest != digest {
		return nil, NewErr(ErrBadRequest, fmt.Sprintf(
			"content_digest %q does not match the computed digest %s", req.ContentDigest, digest))
	}

	if pool == nil {
		return nil, NewErr(ErrInternalError, "publish skill version: no database")
	}

	tx, err := pool.Begin(ctx)
	if err != nil {
		return nil, NewErr(ErrInternalError, "begin transaction")
	}
	defer tx.Rollback(ctx) //nolint:errcheck

	// The publication row lock. Under it latest_version cannot change, so the
	// comparison below IS the compare-and-set: deleting it must break the CAS
	// tests (skill_registry_db_test.go).
	var lockedLatest int
	var lockedOwner string
	if err := tx.QueryRow(ctx,
		`SELECT latest_version, owner_user_id FROM skills WHERE id = $1 FOR UPDATE`,
		req.SkillID,
	).Scan(&lockedLatest, &lockedOwner); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, NewErr(ErrNotFound, "skill not found")
		}
		if aerr := retryConflictErr(err, "lock skill"); aerr != nil {
			return nil, aerr
		}
		return nil, NewErr(ErrInternalError, fmt.Sprintf("lock skill: %v", err))
	}
	// Authorization inside the lock. A non-owner is answered NOT_FOUND — the
	// no-oracle rule: the skill's existence must not be confirmed to a caller
	// who cannot write it.
	if !skillOwnerOrAdmin(caller, lockedOwner) {
		return nil, NewErr(ErrNotFound, "skill not found")
	}
	if req.ExpectedLatest != lockedLatest {
		return nil, NewErrDetails(ErrConflictCASFailed,
			fmt.Sprintf("skill was published concurrently: expected_latest %d but the current latest_version is %d",
				req.ExpectedLatest, lockedLatest),
			map[string]any{"expected_latest": req.ExpectedLatest, "current_latest": lockedLatest})
	}

	newVersion := lockedLatest + 1
	if _, err := tx.Exec(ctx, `
		INSERT INTO skill_versions (skill_id, version, bundle, contract, digest, visibility, author_user_id)
		VALUES ($1, $2, $3, $4, $5, 'private', $6)`,
		req.SkillID, newVersion, bundleJSON, contractJSON, digest, caller.ID); err != nil {
		// The PK (skill_id, version) cannot collide under the lock (the row
		// is held and version = lockedLatest+1), so a unique violation here
		// is an integrity surprise, not a caller error.
		return nil, dbErrCause(err, "insert skill version")
	}
	if _, err := tx.Exec(ctx,
		`UPDATE skills SET latest_version = $1, updated_at = clock_timestamp() WHERE id = $2`,
		newVersion, req.SkillID); err != nil {
		return nil, dbErrCause(err, "advance latest_version")
	}

	if err := tx.Commit(ctx); err != nil {
		// This path is READ COMMITTED + FOR UPDATE, but the commit hop still
		// needs the class-40 mapping (aihub#334's rule applies to every
		// commit site).
		if aerr := retryConflictErr(err, "commit publish skill version"); aerr != nil {
			return nil, aerr
		}
		return nil, NewErr(ErrInternalError, "commit publish skill version")
	}

	return &SkillVersionSummary{
		SkillID:      req.SkillID,
		Version:      newVersion,
		Visibility:   "private",
		Digest:       digest,
		AuthorUserID: caller.ID,
		CreatedAt:    time.Now().UTC(),
	}, nil
}

// decodeSkillBundle decodes and validates a request bundle, mapping the pure
// package's error to a 400.
func decodeSkillBundle(raw json.RawMessage) (*skillregistry.SkillBundle, *AihubError) {
	bundle, err := skillregistry.DecodeBundle(raw)
	if err != nil {
		return nil, NewErr(ErrBadRequest, fmt.Sprintf("bundle: %v", err))
	}
	return bundle, nil
}

// decodeSkillContract decodes and validates a request contract, mapping the
// pure package's error to a 400.
func decodeSkillContract(raw json.RawMessage) (*skillregistry.SkillContract, *AihubError) {
	contract, err := skillregistry.DecodeContract(raw)
	if err != nil {
		return nil, NewErr(ErrBadRequest, fmt.Sprintf("contract: %v", err))
	}
	return contract, nil
}
