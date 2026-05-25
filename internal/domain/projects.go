package domain

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"golang.org/x/crypto/bcrypt"
)

// projectNameRe validates project names: ^[a-z][a-z0-9_-]{0,39}$
var projectNameRe = regexp.MustCompile(`^[a-z][a-z0-9_-]{0,39}$`)

// Project mirrors the projects table row.
type Project struct {
	Name             string          `json:"name"`
	Description      *string         `json:"description"`
	Visible          bool            `json:"visible"`
	IdentifierPrefix *string         `json:"identifier_prefix,omitempty"`
	Repos            json.RawMessage `json:"repos"`
	Members          json.RawMessage `json:"members"`
	WISeq            int64           `json:"wi_seq"`
	Scenario         *string         `json:"scenario"`
	OwnerUserID      string          `json:"owner_user_id"`
	CreatedAt        time.Time       `json:"created_at"`
	UpdatedAt        time.Time       `json:"updated_at"`
}

// CreateProjectRequest is the body for POST /v1/projects.
type CreateProjectRequest struct {
	Name        string          `json:"name"`
	Description *string         `json:"description"`
	Visible     *bool           `json:"visible"`
	Scenario    *string         `json:"scenario"`
	Repos       json.RawMessage `json:"repos"`
}

// UpdateProjectRequest is the body for PATCH /v1/projects/:name.
type UpdateProjectRequest struct {
	Description *string         `json:"description"`
	Visible     *bool           `json:"visible"`
	Scenario    *string         `json:"scenario"`
	Repos       json.RawMessage `json:"repos"`
}

// projectMember is a single entry in the members JSONB array.
type projectMember struct {
	UserID string `json:"user_id"`
	Role   string `json:"role"`
}

// repoEntry is a single entry in the repos JSONB array.
type repoEntry struct {
	Name             string  `json:"name"`
	URL              string  `json:"url"`
	GithubOwnerRepo  *string `json:"github_owner_repo,omitempty"`
	Description      *string `json:"description,omitempty"`
}

// UserRecord holds caller info passed to domain project functions.
type UserRecord struct {
	ID   string
	Role string // "admin" | "writer"
}

// scanProject scans a row into a Project struct.
func scanProject(row pgx.Row) (*Project, error) {
	var p Project
	var repos, members []byte
	err := row.Scan(
		&p.Name, &p.Description, &p.Visible,
		&p.IdentifierPrefix, &repos, &members,
		&p.WISeq, &p.Scenario, &p.OwnerUserID,
		&p.CreatedAt, &p.UpdatedAt,
	)
	if err != nil {
		return nil, err
	}
	if repos != nil {
		p.Repos = json.RawMessage(repos)
	} else {
		p.Repos = json.RawMessage("[]")
	}
	if members != nil {
		p.Members = json.RawMessage(members)
	} else {
		p.Members = json.RawMessage("[]")
	}
	return &p, nil
}

const projectSelectCols = `name, description, visible, identifier_prefix, repos, members,
       wi_seq, scenario, owner_user_id, created_at, updated_at`

// validateRepos checks that repo names and URLs are unique within the list.
func validateRepos(repos json.RawMessage) *AihubError {
	if len(repos) == 0 {
		return nil
	}
	var entries []repoEntry
	if err := json.Unmarshal(repos, &entries); err != nil {
		return NewErr(ErrBadRequest, "repos must be a valid JSON array")
	}
	names := make(map[string]bool, len(entries))
	urls := make(map[string]bool, len(entries))
	for _, r := range entries {
		if r.Name != "" {
			if names[r.Name] {
				return NewErr(ErrRepoDuplicateName, fmt.Sprintf("duplicate repo name: %q", r.Name))
			}
			names[r.Name] = true
		}
		if r.URL != "" {
			if urls[r.URL] {
				return NewErr(ErrRepoDuplicateURL, fmt.Sprintf("duplicate repo URL: %q", r.URL))
			}
			urls[r.URL] = true
		}
	}
	return nil
}

// checkProjectAccess enforces the 5-level permission chain:
//  1. admin → pass
//  2. owner_user_id == caller → pass (all permissions)
//  3. member with role >= minRole → pass
//  4. visible == true → viewer level
//  5. identifier bcrypt check → viewer level
//
// minRole: "viewer" or "writer" or "owner"
func checkProjectAccess(ctx context.Context, conn *pgxpool.Pool, name string, caller *UserRecord, identifier string, minRole string) (*Project, *AihubError) {
	// Level 1: admin bypasses all checks
	if caller.Role == "admin" {
		p, err := getProjectByName(ctx, conn, name)
		if err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return nil, NewErr(ErrProjectNotFound, fmt.Sprintf("project %q not found", name))
			}
			return nil, NewErr(ErrInternalError, fmt.Sprintf("get project: %v", err))
		}
		return p, nil
	}

	p, err := getProjectByNameWithHash(ctx, conn, name)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, NewErr(ErrProjectNotFound, fmt.Sprintf("project %q not found", name))
		}
		return nil, NewErr(ErrInternalError, fmt.Sprintf("get project: %v", err))
	}

	// Level 2: owner has all permissions
	if p.OwnerUserID == caller.ID {
		return &p.Project, nil
	}

	// Level 3: member role check
	var members []projectMember
	if len(p.Members) > 0 {
		_ = json.Unmarshal(p.Members, &members)
	}
	for _, m := range members {
		if m.UserID == caller.ID {
			// member found — check role level
			if minRole == "owner" {
				// only owner/admin can do owner-level ops
				break
			}
			if roleLevel(m.Role) >= roleLevel(minRole) {
				return &p.Project, nil
			}
			// member exists but insufficient role
			return nil, NewErr(ErrProjectAccessDenied,
				fmt.Sprintf("project %q requires %s role, you have %s", name, minRole, m.Role))
		}
	}

	// Levels 4 & 5 only grant viewer access
	if minRole == "writer" || minRole == "owner" {
		// public/identifier access only grants viewer; not enough
	} else {
		// Level 4: visible == true → viewer
		if p.Visible {
			return &p.Project, nil
		}

		// Level 5: bcrypt identifier check
		if identifier != "" && p.identifierHash != nil {
			if bcrypt.CompareHashAndPassword([]byte(*p.identifierHash), []byte(identifier)) == nil {
				return &p.Project, nil
			}
			return nil, NewErr(ErrProjectAccessDenied, "invalid project identifier")
		}
	}

	return nil, NewErr(ErrProjectAccessDenied,
		fmt.Sprintf("access denied to project %q", name))
}

// roleLevel converts a role name to an integer for comparison.
func roleLevel(role string) int {
	switch role {
	case "viewer":
		return 1
	case "writer":
		return 2
	case "owner":
		return 3
	}
	return 0
}

// projectWithHash is an internal type that includes the identifier_hash field.
type projectWithHash struct {
	Project
	identifierHash *string
}

// getProjectByName fetches a project without the identifier_hash field.
func getProjectByName(ctx context.Context, conn *pgxpool.Pool, name string) (*Project, error) {
	row := conn.QueryRow(ctx,
		`SELECT `+projectSelectCols+` FROM projects WHERE name = $1`, name)
	return scanProject(row)
}

// getProjectByNameWithHash fetches a project including the identifier_hash field.
func getProjectByNameWithHash(ctx context.Context, conn *pgxpool.Pool, name string) (*projectWithHash, error) {
	var p projectWithHash
	var repos, members []byte
	err := conn.QueryRow(ctx,
		`SELECT name, description, visible, identifier_prefix, repos, members,
		        wi_seq, scenario, owner_user_id, created_at, updated_at, identifier_hash
		 FROM projects WHERE name = $1`, name,
	).Scan(
		&p.Name, &p.Description, &p.Visible,
		&p.IdentifierPrefix, &repos, &members,
		&p.WISeq, &p.Scenario, &p.OwnerUserID,
		&p.CreatedAt, &p.UpdatedAt, &p.identifierHash,
	)
	if err != nil {
		return nil, err
	}
	if repos != nil {
		p.Repos = json.RawMessage(repos)
	} else {
		p.Repos = json.RawMessage("[]")
	}
	if members != nil {
		p.Members = json.RawMessage(members)
	} else {
		p.Members = json.RawMessage("[]")
	}
	return &p, nil
}

// CreateProject inserts a new project.
func CreateProject(ctx context.Context, conn *pgxpool.Pool, owner *UserRecord, req CreateProjectRequest) (*Project, *AihubError) {
	if !projectNameRe.MatchString(req.Name) {
		return nil, NewErr(ErrProjectNameInvalid,
			fmt.Sprintf("project name %q is invalid: must match ^[a-z][a-z0-9_-]{0,39}$", req.Name))
	}

	// Default visible to true
	visible := true
	if req.Visible != nil {
		visible = *req.Visible
	}

	var scenario *string
	if req.Scenario != nil && *req.Scenario != "" {
		scenario = req.Scenario
	}

	// Validate and default repos
	repos := json.RawMessage("[]")
	if len(req.Repos) > 0 && string(req.Repos) != "null" {
		if aerr := validateRepos(req.Repos); aerr != nil {
			return nil, aerr
		}
		repos = req.Repos
	}

	row := conn.QueryRow(ctx,
		`INSERT INTO projects (name, description, visible, repos, scenario, owner_user_id)
		 VALUES ($1, $2, $3, $4, $5, $6)
		 RETURNING `+projectSelectCols,
		req.Name, req.Description, visible, []byte(repos), scenario, owner.ID,
	)
	p, err := scanProject(row)
	if err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23505" {
			return nil, NewErr(ErrProjectAlreadyExists,
				fmt.Sprintf("project %q already exists", req.Name))
		}
		return nil, NewErr(ErrInternalError, fmt.Sprintf("create project: %v", err))
	}
	return p, nil
}

// GetProject fetches a project after enforcing the 5-level permission chain.
func GetProject(ctx context.Context, conn *pgxpool.Pool, name string, caller *UserRecord, identifier string) (*Project, *AihubError) {
	return checkProjectAccess(ctx, conn, name, caller, identifier, "viewer")
}

// ListProjects returns all projects visible to the caller.
func ListProjects(ctx context.Context, conn *pgxpool.Pool, caller *UserRecord) ([]Project, *AihubError) {
	var rows pgx.Rows
	var err error

	if caller.Role == "admin" {
		rows, err = conn.Query(ctx,
			`SELECT `+projectSelectCols+` FROM projects ORDER BY name`)
	} else {
		rows, err = conn.Query(ctx,
			`SELECT `+projectSelectCols+`
			 FROM projects
			 WHERE visible = true
			    OR owner_user_id = $1
			    OR members @> jsonb_build_array(jsonb_build_object('user_id', $1::text))
			 ORDER BY name`,
			caller.ID)
	}
	if err != nil {
		return nil, NewErr(ErrInternalError, fmt.Sprintf("list projects: %v", err))
	}
	defer rows.Close()

	var projects []Project
	for rows.Next() {
		var p Project
		var repos, members []byte
		if scanErr := rows.Scan(
			&p.Name, &p.Description, &p.Visible,
			&p.IdentifierPrefix, &repos, &members,
			&p.WISeq, &p.Scenario, &p.OwnerUserID,
			&p.CreatedAt, &p.UpdatedAt,
		); scanErr != nil {
			return nil, NewErr(ErrInternalError, fmt.Sprintf("scan project: %v", scanErr))
		}
		if repos != nil {
			p.Repos = json.RawMessage(repos)
		} else {
			p.Repos = json.RawMessage("[]")
		}
		if members != nil {
			p.Members = json.RawMessage(members)
		} else {
			p.Members = json.RawMessage("[]")
		}
		projects = append(projects, p)
	}
	if projects == nil {
		projects = []Project{}
	}
	return projects, nil
}

// UpdateProject patches a project (owner/admin only).
func UpdateProject(ctx context.Context, conn *pgxpool.Pool, name string, caller *UserRecord, req UpdateProjectRequest) (*Project, *AihubError) {
	// Check owner/admin access
	existing, aerr := checkProjectAccess(ctx, conn, name, caller, "", "owner")
	if aerr != nil {
		return nil, aerr
	}
	// Non-admin must be owner
	if caller.Role != "admin" && existing.OwnerUserID != caller.ID {
		return nil, NewErr(ErrProjectAccessDenied, "only owner or admin can update project")
	}

	if len(req.Repos) > 0 && string(req.Repos) != "null" {
		if aerr := validateRepos(req.Repos); aerr != nil {
			return nil, aerr
		}
	}

	tx, err := conn.Begin(ctx)
	if err != nil {
		return nil, NewErr(ErrInternalError, "begin transaction")
	}
	defer tx.Rollback(ctx) //nolint:errcheck

	// SELECT FOR UPDATE to prevent concurrent members/repos writes.
	// Also fetch owner_user_id so we can re-validate after acquiring the lock
	// (owner may have been transferred between the pre-transaction access check and here).
	var lockedOwnerID string
	if err := tx.QueryRow(ctx,
		`SELECT owner_user_id FROM projects WHERE name=$1 FOR UPDATE`, name,
	).Scan(&lockedOwnerID); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, NewErr(ErrProjectNotFound, fmt.Sprintf("project %q not found", name))
		}
		return nil, NewErr(ErrInternalError, fmt.Sprintf("lock project: %v", err))
	}
	// Re-validate ownership inside the transaction to close the TOCTOU window.
	if caller.Role != "admin" && lockedOwnerID != caller.ID {
		return nil, NewErr(ErrProjectOwnerRequired, "only owner or admin can update project")
	}

	setClauses := []string{}
	args := []any{}
	idx := 1

	if req.Description != nil {
		setClauses = append(setClauses, fmt.Sprintf("description=$%d", idx))
		args = append(args, *req.Description)
		idx++
	}
	if req.Visible != nil {
		setClauses = append(setClauses, fmt.Sprintf("visible=$%d", idx))
		args = append(args, *req.Visible)
		idx++
	}
	if req.Scenario != nil {
		setClauses = append(setClauses, fmt.Sprintf("scenario=$%d", idx))
		args = append(args, *req.Scenario)
		idx++
	}
	if len(req.Repos) > 0 && string(req.Repos) != "null" {
		setClauses = append(setClauses, fmt.Sprintf("repos=$%d", idx))
		args = append(args, []byte(req.Repos))
		idx++
	}

	if len(setClauses) == 0 {
		// Nothing to update — return current state
		if err := tx.Rollback(ctx); err != nil && err != pgx.ErrTxClosed {
			// best effort
		}
		return existing, nil
	}

	args = append(args, name)
	query := fmt.Sprintf("UPDATE projects SET %s WHERE name=$%d RETURNING "+projectSelectCols,
		joinStrings(setClauses, ", "), idx)

	row := tx.QueryRow(ctx, query, args...)
	p, scanErr := scanProject(row)
	if scanErr != nil {
		return nil, NewErr(ErrInternalError, fmt.Sprintf("update project: %v", scanErr))
	}

	if err := tx.Commit(ctx); err != nil {
		return nil, NewErr(ErrInternalError, "commit update project")
	}
	return p, nil
}

// RotateIdentifier generates a new identifier for a project (owner/admin only).
// Returns the plain token (shown once), the prefix (stored), and any error.
// The plain token is NEVER stored in the database.
func RotateIdentifier(ctx context.Context, conn *pgxpool.Pool, name string, caller *UserRecord) (plain, prefix string, aerr *AihubError) {
	// Must be owner or admin
	existing, aerr := checkProjectAccess(ctx, conn, name, caller, "", "owner")
	if aerr != nil {
		return "", "", aerr
	}
	if caller.Role != "admin" && existing.OwnerUserID != caller.ID {
		return "", "", NewErr(ErrProjectAccessDenied, "only owner or admin can rotate identifier")
	}

	// Generate random token: "pi_" + 16 bytes hex (35 chars total)
	rawBytes := make([]byte, 16)
	if _, err := rand.Read(rawBytes); err != nil {
		return "", "", NewErr(ErrInternalError, "generate random bytes")
	}
	hexToken := hex.EncodeToString(rawBytes)
	plain = "pi_" + hexToken

	// identifier_prefix: first 4 bytes of hex = 8 hex chars
	prefix = "pi_" + hexToken[:8]

	// bcrypt hash at cost=12 — NOTE: plain never stored
	hashBytes, err := bcrypt.GenerateFromPassword([]byte(plain), 12)
	if err != nil {
		return "", "", NewErr(ErrInternalError, "hash identifier")
	}
	hashStr := string(hashBytes)

	// Update: store hash + prefix; identifier_hash is write-only (never returned)
	_, execErr := conn.Exec(ctx,
		`UPDATE projects SET identifier_hash=$1, identifier_prefix=$2 WHERE name=$3`,
		hashStr, prefix, name,
	)
	if execErr != nil {
		return "", "", NewErr(ErrInternalError, fmt.Sprintf("update identifier: %v", execErr))
	}

	return plain, prefix, nil
}

// TransferOwner changes the owner of a project (current owner/admin only).
func TransferOwner(ctx context.Context, conn *pgxpool.Pool, name, newOwnerID string, caller *UserRecord) *AihubError {
	// Must be owner or admin
	existing, aerr := checkProjectAccess(ctx, conn, name, caller, "", "owner")
	if aerr != nil {
		return aerr
	}
	if caller.Role != "admin" && existing.OwnerUserID != caller.ID {
		return NewErr(ErrProjectAccessDenied, "only owner or admin can transfer ownership")
	}

	// Verify new owner exists
	var check string
	if err := conn.QueryRow(ctx, `SELECT id FROM users WHERE id=$1`, newOwnerID).Scan(&check); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return NewErr(ErrNotFound, fmt.Sprintf("user %q not found", newOwnerID))
		}
		return NewErr(ErrInternalError, fmt.Sprintf("check new owner: %v", err))
	}

	_, execErr := conn.Exec(ctx,
		`UPDATE projects SET owner_user_id=$1 WHERE name=$2`,
		newOwnerID, name,
	)
	if execErr != nil {
		return NewErr(ErrInternalError, fmt.Sprintf("transfer owner: %v", execErr))
	}
	return nil
}

// joinStrings joins a string slice with a separator.
func joinStrings(ss []string, sep string) string {
	if len(ss) == 0 {
		return ""
	}
	var sb strings.Builder
	sb.WriteString(ss[0])
	for _, s := range ss[1:] {
		sb.WriteString(sep)
		sb.WriteString(s)
	}
	return sb.String()
}
