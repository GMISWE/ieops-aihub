package domain

// skill_registry_sharing.go — the read and sharing halves of the versioned
// skill registry (aihub#708 Batch 1A). See skill_registry.go's header for the
// security posture (private default, no metadata oracle, auth recheck,
// scoped-key confinement) that every function here implements.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// ─── Read ────────────────────────────────────────────────────────────────────

// GetSkill returns a skill's identity plus the caller's latest accessible
// version. For a caller who can see no version of the skill, this answers
// NOT_FOUND — a skill's existence is disclosed only together with access to
// at least one of its versions (or ownership).
func GetSkill(ctx context.Context, pool *pgxpool.Pool, caller *UserRecord, skillID string) (*SkillDetail, *AihubError) {
	if aerr := skillRequireReadCaller(caller); aerr != nil {
		return nil, aerr
	}
	if skillID == "" {
		return nil, NewErr(ErrBadRequest, "skill id is required")
	}
	if pool == nil {
		return nil, NewErr(ErrInternalError, "get skill: no database")
	}

	pred, predArgs, _ := skillVersionAccessSQL("v", caller, 2)
	args := append([]any{skillID}, predArgs...)
	row := pool.QueryRow(ctx, fmt.Sprintf(`
		SELECT s.id, s.name, s.owner_user_id, u.display_name, s.latest_version, s.created_at, s.updated_at,
		       acc.version, acc.visibility, acc.digest, acc.author_user_id, acc.created_at
		FROM skills s
		JOIN users u ON u.id = s.owner_user_id
		LEFT JOIN LATERAL (
		    SELECT v.version, v.visibility, v.digest, v.author_user_id, v.created_at
		    FROM skill_versions v
		    WHERE v.skill_id = s.id%s
		    ORDER BY v.version DESC
		    LIMIT 1
		) acc ON TRUE
		WHERE s.id = $1`, pred), args...)

	s, err := scanSkillRow(row)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, NewErr(ErrNotFound, "skill not found")
		}
		return nil, dbErrCause(err, "get skill")
	}
	return buildSkillDetail(caller, s)
}

// scanSkillRow scans the identity columns plus the (nullable) latest
// accessible version columns of the GetSkill/ListSkills select. The acc*
// pointers are NULL exactly when the caller can access no version.
type skillScan struct {
	ident        SkillIdentity
	accVersion   *int
	accVisible   *string
	accDigest    *string
	accAuthor    *string
	accCreatedAt *time.Time
}

func scanSkillRow(row pgx.Row) (*skillScan, error) {
	var s skillScan
	err := row.Scan(
		&s.ident.ID, &s.ident.Name, &s.ident.OwnerUserID, &s.ident.OwnerDisplay,
		&s.ident.LatestVersion, &s.ident.CreatedAt, &s.ident.UpdatedAt,
		&s.accVersion, &s.accVisible, &s.accDigest, &s.accAuthor, &s.accCreatedAt)
	if err != nil {
		return nil, err
	}
	return &s, nil
}

// buildSkillDetail applies the no-oracle rules to one scanned row: a caller
// who is not the owner and not an unscoped admin must have an accessible
// version to see the skill at all, and only the owner and unscoped admins are
// told the total version count.
func buildSkillDetail(caller *UserRecord, s *skillScan) (*SkillDetail, *AihubError) {
	detail := &SkillDetail{SkillIdentity: s.ident}
	if s.accVersion != nil {
		detail.LatestAccessible = &SkillVersionSummary{
			SkillID:      s.ident.ID,
			Version:      *s.accVersion,
			Visibility:   derefOr(s.accVisible, ""),
			Digest:       derefOr(s.accDigest, ""),
			AuthorUserID: derefOr(s.accAuthor, ""),
			CreatedAt:    derefTimeOr(s.accCreatedAt),
		}
	}
	if !skillOwnerOrAdmin(caller, s.ident.OwnerUserID) {
		// No accessible version and no ownership: the skill does not exist as
		// far as this caller is told. NOTE: skillOwnerOrAdmin is false for
		// scoped callers even on their own skills (file header, rule 4), so a
		// scoped key sees a skill only through a grant on the scoped project.
		if detail.LatestAccessible == nil {
			return nil, NewErr(ErrNotFound, "skill not found")
		}
		// The total count is owner/admin metadata only.
		detail.LatestVersion = 0
		// skills.updated_at is OWNER-SENSITIVE identity metadata: it moves on
		// every publish — including private publishes this caller cannot see
		// — so serving it raw would tell a restricted reader exactly when the
		// owner published something they are not allowed to know exists (a
		// timing oracle on private activity). It is derived from the only
		// activity the caller is entitled to: the created_at of their latest
		// ACCESSIBLE version, which moves only when the caller's own view of
		// the skill moves.
		detail.UpdatedAt = detail.LatestAccessible.CreatedAt
	}
	return detail, nil
}

func derefOr(s *string, def string) string {
	if s == nil {
		return def
	}
	return *s
}

func derefTimeOr(t *time.Time) time.Time {
	if t == nil {
		return time.Time{}
	}
	return *t
}

// GetSkillVersion returns one exact version, rechecking access from the
// current rows (the auth-recheck rule: a revoked grant blocks this call).
// Inaccessible and nonexistent answer the same NOT_FOUND — no oracle.
func GetSkillVersion(ctx context.Context, pool *pgxpool.Pool, caller *UserRecord, skillID string, version int) (*SkillVersion, *AihubError) {
	if aerr := skillRequireReadCaller(caller); aerr != nil {
		return nil, aerr
	}
	if skillID == "" {
		return nil, NewErr(ErrBadRequest, "skill id is required")
	}
	if version < 1 {
		return nil, NewErr(ErrBadRequest, fmt.Sprintf("version %d is invalid: versions start at 1", version))
	}
	if pool == nil {
		return nil, NewErr(ErrInternalError, "get skill version: no database")
	}

	pred, predArgs, _ := skillVersionAccessSQL("sv", caller, 3)
	args := append([]any{skillID, version}, predArgs...)
	row := pool.QueryRow(ctx, fmt.Sprintf(`
		SELECT sv.skill_id, sv.version, sv.visibility, sv.digest, sv.author_user_id, sv.created_at,
		       sv.bundle, sv.contract
		FROM skill_versions sv
		WHERE sv.skill_id = $1 AND sv.version = $2%s`, pred), args...)

	var out SkillVersion
	var bundle, contract []byte
	err := row.Scan(
		&out.SkillID, &out.Version, &out.Visibility, &out.Digest,
		&out.AuthorUserID, &out.CreatedAt, &bundle, &contract)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			// Same code and message as a missing row: "not found", never
			// "forbidden" — the difference would be the oracle.
			return nil, NewErr(ErrNotFound, "skill version not found")
		}
		return nil, dbErrCause(err, "get skill version")
	}
	out.Bundle = json.RawMessage(bundle)
	out.Contract = json.RawMessage(contract)
	return &out, nil
}

// ListSkills pages through every skill the caller can see, its latest
// accessible version attached, ordered by skill id (the page cursor). A skill
// appears for a caller iff the caller is an unscoped admin, owns it
// (unscoped), or can access at least one of its versions.
//
// The order is BYTE order of the id, on every database: both the ORDER BY and
// the cursor comparison are pinned to COLLATE "C". Skill ids are mixed-case
// base62, which a locale collation orders differently than bytes ('a' sorts
// before 'B' under en_US.utf8; bytewise 'B' < 'a'), and the GitHub Actions
// service container initdb's its database en_US.utf8 while local databases
// are often C/POSIX — so an unpinned list has one order on CI and another
// locally. Pinning BOTH ends also keeps the cursor predicate in the same
// collation as the sort: a comparison under one collation and an ORDER BY
// under another skips or repeats ids across page boundaries.
func ListSkills(ctx context.Context, pool *pgxpool.Pool, caller *UserRecord, req ListSkillsRequest) ([]SkillDetail, *AihubError) {
	if aerr := skillRequireReadCaller(caller); aerr != nil {
		return nil, aerr
	}

	limit := req.Limit
	if limit == 0 {
		limit = skillListDefaultLimit
	}
	if limit < 1 || limit > skillListMaxLimit {
		return nil, NewErr(ErrBadRequest,
			fmt.Sprintf("limit %d is out of range; it must be 1..%d (0 means the default %d)",
				req.Limit, skillListMaxLimit, skillListDefaultLimit))
	}
	if pool == nil {
		return nil, NewErr(ErrInternalError, "list skills: no database")
	}

	pred, predArgs, nextIdx := skillVersionAccessSQL("v", caller, 1)
	args := append([]any{}, predArgs...)

	var rowVis string
	if skillSeesAllVersions(caller) {
		rowVis = "TRUE"
	} else if caller.ProjectScope != nil {
		// A scoped key sees a skill only through a grant on the scoped
		// project — exactly what the predicate already encodes into the
		// lateral join: no accessible version, no row.
		rowVis = "acc.version IS NOT NULL"
	} else {
		args = append(args, caller.ID)
		rowVis = fmt.Sprintf("(s.owner_user_id = $%d OR acc.version IS NOT NULL)", nextIdx)
		nextIdx++
	}

	var conds []string
	conds = append(conds, rowVis)
	if req.Owner != nil && *req.Owner != "" {
		args = append(args, *req.Owner)
		conds = append(conds, fmt.Sprintf("s.owner_user_id = $%d", nextIdx))
		nextIdx++
	}
	if req.Cursor != "" {
		args = append(args, req.Cursor)
		// COLLATE "C": the cursor predicate must compare ids exactly as the
		// ORDER BY sorts them (byte order), whatever LC_COLLATE the database
		// was initdb'd with — see the function comment.
		conds = append(conds, fmt.Sprintf(`(s.id COLLATE "C") > $%d`, nextIdx))
		nextIdx++
	}
	args = append(args, limit)

	rows, err := pool.Query(ctx, fmt.Sprintf(`
		SELECT s.id, s.name, s.owner_user_id, u.display_name, s.latest_version, s.created_at, s.updated_at,
		       acc.version, acc.visibility, acc.digest, acc.author_user_id, acc.created_at
		FROM skills s
		JOIN users u ON u.id = s.owner_user_id
		LEFT JOIN LATERAL (
		    SELECT v.version, v.visibility, v.digest, v.author_user_id, v.created_at
		    FROM skill_versions v
		    WHERE v.skill_id = s.id%s
		    ORDER BY v.version DESC
		    LIMIT 1
		) acc ON TRUE
		WHERE %s
		ORDER BY s.id COLLATE "C"
		LIMIT $%d`, pred, strings.Join(conds, " AND "), nextIdx), args...)
	if err != nil {
		return nil, dbErrCause(err, "list skills")
	}
	defer rows.Close()

	var out []SkillDetail
	for rows.Next() {
		s, err := scanSkillRow(rows)
		if err != nil {
			return nil, dbErrCause(err, "list skills")
		}
		detail, aerr := buildSkillDetail(caller, s)
		if aerr != nil {
			return nil, aerr
		}
		out = append(out, *detail)
	}
	if err := rows.Err(); err != nil {
		return nil, dbErrCause(err, "list skills")
	}
	return out, nil
}

// ListSkillVersions lists the versions of a skill the caller can access, in
// version order. The owner and unscoped admins see all of them; every other
// caller sees only accessible versions, and a caller who can access none of
// them gets NOT_FOUND rather than an empty list — an empty list would confirm
// the skill exists.
func ListSkillVersions(ctx context.Context, pool *pgxpool.Pool, caller *UserRecord, skillID string) ([]SkillVersionSummary, *AihubError) {
	if aerr := skillRequireReadCaller(caller); aerr != nil {
		return nil, aerr
	}
	if skillID == "" {
		return nil, NewErr(ErrBadRequest, "skill id is required")
	}
	if pool == nil {
		return nil, NewErr(ErrInternalError, "list skill versions: no database")
	}

	// Owner/admin path: they see every version of a skill they can see at
	// all, so the predicate is skipped and existence is checked directly —
	// an existing skill with zero versions is a legitimate empty list.
	// skillOwnsSkill's DB failures PROPAGATE (aihub#708 Batch 1A review
	// finding 7): swallowing them into false would demote a real owner to
	// the grant path on a transient outage, answering an access question
	// from a failed query.
	isPrivileged := skillSeesAllVersions(caller)
	if !isPrivileged && caller.ProjectScope == nil {
		owns, aerr := skillOwnsSkill(ctx, pool, caller, skillID)
		if aerr != nil {
			return nil, aerr
		}
		isPrivileged = owns
	}

	var where string
	var args []any
	if isPrivileged {
		if err := pool.QueryRow(ctx,
			`SELECT 1 FROM skills WHERE id = $1`, skillID).Scan(new(int)); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return nil, NewErr(ErrNotFound, "skill not found")
			}
			return nil, dbErrCause(err, "list skill versions")
		}
		where = "sv.skill_id = $1"
		args = []any{skillID}
	} else {
		pred, predArgs, _ := skillVersionAccessSQL("sv", caller, 2)
		where = "sv.skill_id = $1" + pred
		args = append([]any{skillID}, predArgs...)
	}

	rows, err := pool.Query(ctx, fmt.Sprintf(`
		SELECT sv.skill_id, sv.version, sv.visibility, sv.digest, sv.author_user_id, sv.created_at
		FROM skill_versions sv
		WHERE %s
		ORDER BY sv.version`, where), args...)
	if err != nil {
		return nil, dbErrCause(err, "list skill versions")
	}
	defer rows.Close()

	var out []SkillVersionSummary
	for rows.Next() {
		var s SkillVersionSummary
		if err := rows.Scan(&s.SkillID, &s.Version, &s.Visibility, &s.Digest,
			&s.AuthorUserID, &s.CreatedAt); err != nil {
			return nil, dbErrCause(err, "list skill versions")
		}
		out = append(out, s)
	}
	if err := rows.Err(); err != nil {
		return nil, dbErrCause(err, "list skill versions")
	}
	if !isPrivileged && len(out) == 0 {
		// No accessible versions: the skill is either nonexistent or entirely
		// private to somebody else. Either way, "not found" — no oracle.
		return nil, NewErr(ErrNotFound, "skill not found")
	}
	return out, nil
}

// skillOwnsSkill reports whether the caller owns the named skill (used only
// by ListSkillVersions' privileged path; the write paths read the owner row
// under their own checks). A missing skill is "not owned", not an error; every
// OTHER database failure is returned so the caller answers with the error
// instead of silently falling back to the restricted path.
func skillOwnsSkill(ctx context.Context, pool *pgxpool.Pool, caller *UserRecord, skillID string) (bool, *AihubError) {
	var owner string
	if err := pool.QueryRow(ctx,
		`SELECT owner_user_id FROM skills WHERE id = $1`, skillID).Scan(&owner); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return false, nil
		}
		return false, dbErrCause(err, "check skill ownership")
	}
	return owner == caller.ID, nil
}

// ─── Share / revoke / visibility ─────────────────────────────────────────────

// ShareSkillVersionWithProject shares one exact version with one project:
// the project's owner and every member (viewer level — any member role) gain
// read access to that version and no other. Idempotent: sharing an
// already-shared version succeeds.
//
// Authorization is three-gated, and the order is the no-oracle order:
//
//  1. the caller must be the skill's owner or an unscoped admin (otherwise
//     NOT_FOUND — the skill is not confirmed to a non-owner);
//  2. the version must exist (otherwise NOT_FOUND);
//  3. the caller must hold proper project rights on the target project:
//     its owner, one of its members, or an unscoped admin. A project the
//     caller merely happens to be able to VIEW (public visibility or the
//     bcrypt identifier) is NOT enough — a grant is addressed to a project's
//     members, and the sharer must be one (spec D3 "project sharing requires
//     proper project rights").
func ShareSkillVersionWithProject(ctx context.Context, pool *pgxpool.Pool, caller *UserRecord, req SkillVersionShareRequest) *AihubError {
	if aerr := skillRequireWriteCaller(caller); aerr != nil {
		return aerr
	}
	if aerr := skillRequireSkillVersionForWrite(ctx, pool, caller, req.SkillID, req.Version); aerr != nil {
		return aerr
	}
	if req.Project == "" {
		return NewErr(ErrBadRequest, "project is required")
	}

	// Gate 3: proper project rights.
	if aerr := requireSkillShareProjectRight(ctx, pool, caller, req.Project); aerr != nil {
		return aerr
	}

	if _, err := pool.Exec(ctx, `
		INSERT INTO skill_version_grants (skill_id, version, project_name, granted_by)
		VALUES ($1, $2, $3, $4)
		ON CONFLICT (skill_id, version, project_name) DO NOTHING`,
		req.SkillID, req.Version, req.Project, caller.ID); err != nil {
		return dbErrCause(err, "share skill version")
	}
	return nil
}

// RevokeSkillVersionFromProject removes the project grant from one exact
// version. The NEXT read by that project's members is refused (the
// auth-recheck rule); already-disclosed bytes are not recalled. Idempotent:
// revoking a grant that is not there succeeds.
func RevokeSkillVersionFromProject(ctx context.Context, pool *pgxpool.Pool, caller *UserRecord, req SkillVersionShareRequest) *AihubError {
	if aerr := skillRequireWriteCaller(caller); aerr != nil {
		return aerr
	}
	if aerr := skillRequireSkillVersionForWrite(ctx, pool, caller, req.SkillID, req.Version); aerr != nil {
		return aerr
	}
	if req.Project == "" {
		return NewErr(ErrBadRequest, "project is required")
	}
	if _, err := pool.Exec(ctx,
		`DELETE FROM skill_version_grants WHERE skill_id = $1 AND version = $2 AND project_name = $3`,
		req.SkillID, req.Version, req.Project); err != nil {
		return dbErrCause(err, "revoke skill version share")
	}
	return nil
}

// SetSkillVersionVisibility flips a version between private and public.
// Public means "any AUTHENTICATED caller may read this version" — it is not
// an anonymous-distribution promise (plan decision ledger: "Authenticated
// registry first; anonymous distribution is not implied by public flag"), and
// it never widens anything else: no future version inherits it, and no work
// item artifact is exposed by it. Flipping public→private is the revocation
// path for public sharing; the next read by a stranger is refused.
func SetSkillVersionVisibility(ctx context.Context, pool *pgxpool.Pool, caller *UserRecord, skillID string, version int, visibility string) (*SkillVersionSummary, *AihubError) {
	if aerr := skillRequireWriteCaller(caller); aerr != nil {
		return nil, aerr
	}
	if !skillVisibilityValues[visibility] {
		return nil, NewErr(ErrBadRequest,
			fmt.Sprintf("visibility %q is invalid; it must be private or public", visibility))
	}
	if aerr := skillRequireSkillVersionForWrite(ctx, pool, caller, skillID, version); aerr != nil {
		return nil, aerr
	}

	if _, err := pool.Exec(ctx,
		`UPDATE skill_versions SET visibility = $1 WHERE skill_id = $2 AND version = $3`,
		visibility, skillID, version); err != nil {
		return nil, dbErrCause(err, "set skill version visibility")
	}
	return skillVersionSummaryForOwner(ctx, pool, skillID, version)
}

// skillVersionSummaryForOwner reads a version summary WITHOUT an access
// predicate. It is deliberately UNEXPORTED (aihub#708 Batch 1A review finding
// 6): it exists only for the mutation responses above, which reach it only
// AFTER skillRequireSkillVersionForWrite has established ownership — public
// API reads go through GetSkillVersion, never here. Keeping it unexported is
// what prevents a future caller from reaching a no-auth read by accident; a
// public companion would have to carry its own caller argument and guard, and
// no consumer needs one.
func skillVersionSummaryForOwner(ctx context.Context, pool *pgxpool.Pool, skillID string, version int) (*SkillVersionSummary, *AihubError) {
	row := pool.QueryRow(ctx, `
		SELECT skill_id, version, visibility, digest, author_user_id, created_at
		FROM skill_versions WHERE skill_id = $1 AND version = $2`,
		skillID, version)
	var s SkillVersionSummary
	err := row.Scan(&s.SkillID, &s.Version, &s.Visibility, &s.Digest,
		&s.AuthorUserID, &s.CreatedAt)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			// Cannot happen on the mutation path (the version was just
			// checked to exist), but the answer stays the no-oracle 404.
			return nil, NewErr(ErrNotFound, "skill version not found")
		}
		return nil, dbErrCause(err, "get skill version summary")
	}
	return &s, nil
}

// skillRequireSkillVersionForWrite performs the write-path authorization over
// a skill and one of its versions: the skill must exist, the caller must be
// its owner or an unscoped admin, and the version must exist. Both refusals
// are NOT_FOUND, in the no-oracle order (identity first, then version).
func skillRequireSkillVersionForWrite(ctx context.Context, pool *pgxpool.Pool, caller *UserRecord, skillID string, version int) *AihubError {
	if skillID == "" {
		return NewErr(ErrBadRequest, "skill_id is required")
	}
	if version < 1 {
		return NewErr(ErrBadRequest, fmt.Sprintf("version %d is invalid: versions start at 1", version))
	}
	if pool == nil {
		return NewErr(ErrInternalError, "skill registry: no database")
	}

	var owner string
	if err := pool.QueryRow(ctx,
		`SELECT owner_user_id FROM skills WHERE id = $1`, skillID).Scan(&owner); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return NewErr(ErrNotFound, "skill not found")
		}
		return dbErrCause(err, "get skill owner")
	}
	if !skillOwnerOrAdmin(caller, owner) {
		return NewErr(ErrNotFound, "skill not found")
	}
	var exists bool
	if err := pool.QueryRow(ctx,
		`SELECT TRUE FROM skill_versions WHERE skill_id = $1 AND version = $2`,
		skillID, version).Scan(&exists); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return NewErr(ErrNotFound, "skill version not found")
		}
		return dbErrCause(err, "get skill version")
	}
	return nil
}

// requireSkillShareProjectRight checks that the caller holds proper rights on
// the project a share targets: unscoped admin, project owner, or project
// member (any role). It is deliberately stricter than checkProjectAccess's
// viewer level: bare public visibility / identifier access does not make a
// project a share target, because a grant is addressed to the project's
// members and the sharer must be one of them (spec D3).
func requireSkillShareProjectRight(ctx context.Context, pool *pgxpool.Pool, caller *UserRecord, project string) *AihubError {
	// An unscoped admin may share into any project that exists; a scoped key
	// never reaches here (skillRequireWriteCaller refused it already).
	if caller.Role == "admin" && caller.ProjectScope == nil {
		var exists bool
		if err := pool.QueryRow(ctx,
			`SELECT TRUE FROM projects WHERE name = $1`, project).Scan(&exists); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return NewErr(ErrProjectNotFound, fmt.Sprintf("project %q not found", project))
			}
			return dbErrCause(err, "get project for share")
		}
		return nil
	}

	var ownerUserID string
	var members []byte
	if err := pool.QueryRow(ctx,
		`SELECT owner_user_id, members FROM projects WHERE name = $1`, project).
		Scan(&ownerUserID, &members); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return NewErr(ErrProjectNotFound, fmt.Sprintf("project %q not found", project))
		}
		return dbErrCause(err, "get project for share")
	}
	if ownerUserID == caller.ID {
		return nil
	}
	// Member check. The decode error is DISCARDED, not acted on, matching
	// roleForUserInMembers' policy (a malformed row must not make sharing
	// impossible on exactly the rows that need a repair; a junk entry simply
	// is not this caller).
	var stored []projectMember
	if len(members) > 0 {
		_ = json.Unmarshal(members, &stored)
	}
	for _, m := range stored {
		if m.UserID == caller.ID && RoleLevel[m.Role] > 0 {
			return nil
		}
	}
	return NewErr(ErrProjectAccessDenied,
		fmt.Sprintf("sharing into project %q requires being its owner or a member", project))
}
