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

// scenarioURLRe validates that a project's scenario field holds a git repo URL
// (not a bare logical name like "coding"). The scenario value is consumed by
// internal/cli/init.go's scenarioRepoName()+cloneOrSync() to clone the scenario
// repo; a bare name has no host/owner so cloning is impossible and pf init silently
// skips it. We accept the two forms git remotes actually use:
//
//	SSH scp-like : git@github.com:GMISWE/polyforge-coding.git
//	URL (any scheme, e.g. https/ssh/git) : https://github.com/GMISWE/polyforge-coding.git
//
// Both require a host and at least one path segment, and an optional ".git" suffix.
var scenarioURLRe = regexp.MustCompile(`^([A-Za-z][A-Za-z0-9+.-]*://[^/\s]+/.+|[^@\s]+@[^:\s]+:.+?)(\.git)?$`)

// validateScenario checks that a project's scenario value is a git repo URL or
// empty, and returns the normalized (whitespace-trimmed) value to persist. Empty
// (unset/cleared, including whitespace-only) is always allowed and normalizes to
// ""; a non-empty value that does not look like a git URL (e.g. the bare logical
// name "coding") is rejected so the bad value can never be persisted and break
// `pf init` scenario cloning. Callers MUST store the returned value, not the raw
// input, so surrounding whitespace can't slip through and re-break cloning.
func validateScenario(scenario string) (string, *AihubError) {
	s := strings.TrimSpace(scenario)
	if s == "" {
		return "", nil
	}
	if !scenarioURLRe.MatchString(s) {
		return "", NewErr(ErrProjectScenarioInvalid,
			fmt.Sprintf("project scenario %q is invalid: must be a git URL "+
				"(e.g. git@github.com:GMISWE/polyforge-coding.git or "+
				"https://github.com/GMISWE/polyforge-coding.git) or empty", scenario))
	}
	return s, nil
}

// Project mirrors the projects table row.
type Project struct {
	Name             string          `json:"name"`
	Description      *string         `json:"description"`
	Visible          bool            `json:"visible"`
	IdentifierPrefix *string         `json:"identifier_prefix,omitempty"`
	Repos            json.RawMessage `json:"repos"`
	Members          json.RawMessage `json:"members"`
	// MembersVersion is the aihub#260 compare-and-set counter for `members`.
	// It is returned by every read (list/get) and every write so a caller that
	// has just read the project holds the token it needs to guard its own
	// read-modify-write. No omitempty: version 0 is a legitimate, common value
	// (it is where every project starts), and dropping the key at 0 would make
	// "this project has never had a members write" indistinguishable from "this
	// server does not implement the guard at all" — an absent field is exactly
	// how a caller detects an old server, so 0 must be sent as 0.
	MembersVersion int       `json:"members_version"`
	WISeq          int64     `json:"wi_seq"`
	Scenario       *string   `json:"scenario"`
	OwnerUserID    string    `json:"owner_user_id"`
	CreatedAt      time.Time `json:"created_at"`
	UpdatedAt      time.Time `json:"updated_at"`
}

// CreateProjectRequest is the body for POST /v1/projects.
type CreateProjectRequest struct {
	Name        string          `json:"name"`
	Description *string         `json:"description"`
	Visible     *bool           `json:"visible"`
	Scenario    *string         `json:"scenario"`
	Repos       json.RawMessage `json:"repos"`
}

// MemberInput is a single entry in a members update request.
type MemberInput struct {
	UserID string `json:"user_id"`
	Role   string `json:"role"` // "viewer" | "writer" | "maintainer"
}

// UpdateProjectRequest is the body for PATCH /v1/projects/:name.
type UpdateProjectRequest struct {
	Description *string         `json:"description"`
	Visible     *bool           `json:"visible"`
	Scenario    *string         `json:"scenario"`
	Repos       json.RawMessage `json:"repos"`
	// Members, when non-nil, replaces the entire members list.
	//
	// aihub#260: REPLACE is unchanged and deliberately so — changing it to a
	// delta would silently rewrite the meaning of every caller that exists
	// today and would remove the only way to REMOVE a member. What aihub#260
	// adds is MembersVersion, the guard that makes the read-modify-write this
	// forces on callers safe against a concurrent one.
	Members *[]MemberInput `json:"members"`
	// MembersVersion, when non-nil, is a compare-and-set precondition: the
	// members_version the caller last read. The whole update is applied only if
	// the stored value still matches; otherwise nothing is written and the call
	// fails with 409 CONFLICT_CAS_FAILED carrying the current version.
	//
	// Omitting it keeps the historical unconditional overwrite, which existing
	// callers depend on.
	MembersVersion *int `json:"members_version"`
}

// projectMember is a single entry in the members JSONB array.
type projectMember struct {
	UserID string `json:"user_id"`
	Role   string `json:"role"`
}

// repoModule is one entry in a repo's main_modules list: a directory path and
// the role it plays. Paths are validated for existence client-side (in /pf-init,
// which has the clones) — the server only checks structure.
type repoModule struct {
	Path string `json:"path"`
	Role string `json:"role"`
}

// repoEntry is a single entry in the repos JSONB array.
//
// The structured description block (positioning/tech_stack/main_modules/
// change_scenarios + generation metadata) is optional *as a block*: a repo may
// omit it entirely (legacy rows carry only the single-line Description, which
// /pf-init uses as a generation seed). But if any block field is set, all four
// content fields must be present and well-formed — see validateRepos. Project-
// wide completeness ("every repo described") is enforced by /pf-init's render
// gate, not here, so unrelated project updates never break mid-rollout.
type repoEntry struct {
	Name            string  `json:"name"`
	URL             string  `json:"url"`
	GithubOwnerRepo *string `json:"github_owner_repo,omitempty"`
	Description     *string `json:"description,omitempty"` // legacy single line; generation seed

	// Structured description block (all-or-nothing; English).
	Positioning     string       `json:"positioning,omitempty"`      // one line: what this repo is / its role
	TechStack       []string     `json:"tech_stack,omitempty"`       // ["Go", "PostgreSQL"]
	MainModules     []repoModule `json:"main_modules,omitempty"`     // {path, role}; paths validated client-side
	ChangeScenarios []string     `json:"change_scenarios,omitempty"` // ["add MCP tool", "schema migration"]

	// Generation metadata (set by /pf-init when it writes the block).
	GeneratedAt     *time.Time `json:"generated_at,omitempty"`     // RFC3339; age-based staleness
	GeneratedCommit string     `json:"generated_commit,omitempty"` // repo HEAD SHA at generation; content-staleness
}

// hasDescriptionBlock reports whether any structured-description field is set.
func (r *repoEntry) hasDescriptionBlock() bool {
	return r.Positioning != "" || len(r.TechStack) > 0 ||
		len(r.MainModules) > 0 || len(r.ChangeScenarios) > 0
}

// UserRecord holds caller info passed to domain project functions.
type UserRecord struct {
	ID           string
	Role         string  // "admin" | "writer"
	ProjectScope *string // nil = unscoped; else confined to this project name
}

// scanProject scans a row into a Project struct.
func scanProject(row pgx.Row) (*Project, error) {
	var p Project
	var repos, members []byte
	err := row.Scan(
		&p.Name, &p.Description, &p.Visible,
		&p.IdentifierPrefix, &repos, &members, &p.MembersVersion,
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

const projectSelectCols = `name, description, visible, identifier_prefix, repos, members, members_version,
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
	for i := range entries {
		r := &entries[i]
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
		if aerr := validateDescriptionBlock(r); aerr != nil {
			return aerr
		}
	}
	return nil
}

// validateDescriptionBlock enforces the all-or-nothing rule for a repo's
// structured description: the block may be absent, but if any field is set then
// all four content fields must be present and well-formed. It does NOT check
// main_modules path existence — the server has no repo clones; /pf-init does
// that validation against the local checkout before calling pf_update_project.
func validateDescriptionBlock(r *repoEntry) *AihubError {
	if !r.hasDescriptionBlock() {
		return nil
	}
	incomplete := func(field string) *AihubError {
		return NewErr(ErrRepoIncompleteDescription,
			fmt.Sprintf("repo %q description block is incomplete: %s required when any description field is set", r.Name, field))
	}
	if strings.TrimSpace(r.Positioning) == "" {
		return incomplete("positioning")
	}
	if len(r.TechStack) == 0 {
		return incomplete("tech_stack")
	}
	for _, t := range r.TechStack {
		if strings.TrimSpace(t) == "" {
			return incomplete("tech_stack entries must be non-empty")
		}
	}
	if len(r.MainModules) == 0 {
		return incomplete("main_modules")
	}
	for _, m := range r.MainModules {
		if strings.TrimSpace(m.Path) == "" || strings.TrimSpace(m.Role) == "" {
			return incomplete("each main_modules entry must have non-empty path and role")
		}
	}
	if len(r.ChangeScenarios) == 0 {
		return incomplete("change_scenarios")
	}
	for _, c := range r.ChangeScenarios {
		if strings.TrimSpace(c) == "" {
			return incomplete("change_scenarios entries must be non-empty")
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
	// project_scope on the api key confines the caller; an out-of-scope project
	// is reported as not-found so its existence is not revealed (applies to admin too).
	if caller.ProjectScope != nil && *caller.ProjectScope != name {
		return nil, NewErr(ErrProjectNotFound, fmt.Sprintf("project %q not found", name))
	}

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
		`SELECT name, description, visible, identifier_prefix, repos, members, members_version,
		        wi_seq, scenario, owner_user_id, created_at, updated_at, identifier_hash
		 FROM projects WHERE name = $1`, name,
	).Scan(
		&p.Name, &p.Description, &p.Visible,
		&p.IdentifierPrefix, &repos, &members, &p.MembersVersion,
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
	if owner.ProjectScope != nil && *owner.ProjectScope != req.Name {
		return nil, NewErr(ErrProjectAccessDenied, fmt.Sprintf("api key is scoped to project %q", *owner.ProjectScope))
	}

	// Default visible to true
	visible := true
	if req.Visible != nil {
		visible = *req.Visible
	}

	var scenario *string
	if req.Scenario != nil {
		norm, aerr := validateScenario(*req.Scenario)
		if aerr != nil {
			return nil, aerr
		}
		if norm != "" {
			scenario = &norm
		}
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

// applyProjectScope drops projects outside the caller's api-key project_scope.
// nil scope = unscoped (all pass). Applies to admins too.
func applyProjectScope(projects []Project, scope *string) []Project {
	if scope == nil {
		return projects
	}
	kept := make([]Project, 0, 1)
	for _, p := range projects {
		if p.Name == *scope {
			kept = append(kept, p)
		}
	}
	return kept
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
			&p.IdentifierPrefix, &repos, &members, &p.MembersVersion,
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
	projects = applyProjectScope(projects, caller.ProjectScope)
	if projects == nil {
		projects = []Project{}
	}
	return projects, nil
}

// projectUpdate is the compiled UPDATE for UpdateProject.
//
// Split out of UpdateProject as a pure function for the reason spelled out at
// buildWorkItemUpdate (work_items.go): UpdateProject reaches the database before
// it gets here, so any behavioural test of this logic would have to be DB-gated,
// and a DB-gated test in this repo runs only in its own scoped CI step — it
// SKIPs on `go test ./...` while still reading as coverage. Compiling the
// statement in a pure function gives the two aihub#260 invariants assertions
// that execute everywhere.
type projectUpdate struct {
	Query string
	Args  []any
	// CAS is true when the WHERE clause carries a members_version predicate,
	// i.e. the caller asked for compare-and-set. Zero rows returned is then a
	// version conflict rather than a vanished row.
	CAS bool
	// Empty is true when the caller supplied no field to change at all, in
	// which case Query is "" and no statement should be executed.
	Empty bool
}

// projectUpdateWritesSomething reports whether the request asks for any column
// to change. UpdateProject uses it before it touches the pool, to reject a
// members_version that guards nothing.
//
// It must agree with buildProjectUpdate's Empty exactly — if a new writable
// field is ever added to one and not the other, a request carrying only that
// field plus a version would be rejected as "changes nothing" (or, the other
// way round, a version would be silently accepted with no statement to guard).
// TestProjectUpdateWritesSomethingAgreesWithBuild pins the two together.
func projectUpdateWritesSomething(req *UpdateProjectRequest) bool {
	return req.Description != nil || req.Visible != nil || req.Scenario != nil ||
		req.Members != nil || (len(req.Repos) > 0 && string(req.Repos) != "null")
}

// buildProjectUpdate compiles the SET/WHERE clauses for UpdateProject.
//
// # aihub#260: the members lost update
//
// `members` is a whole-list REPLACE (see UpdateProjectRequest.Members), so the
// only way to add one member is read all N and send back N+1. Two admins doing
// that at once used to mean the later write silently dropped the earlier one's
// addition, and afterwards the result was indistinguishable from "that person
// was never added" — updated_at is rewritten by trg_projects_updated_at on
// every write, so even the timestamp carried no evidence. Directly observed on
// 2026-08-24 with a read-modify-write window of roughly two minutes.
//
// The guard is the same shape aihub#241 gave work_items.declared_resources, and
// it is worth restating WHY that shape, because aihub#241's doc comment records
// two earlier attempts that both failed while looking correct:
//
//   - The counter must advance in the DATABASE. `members_version =
//     members_version + 1` is computed by Postgres from the stored value, never
//     in Go from anything the caller sent or from a row this process read
//     earlier. aihub#241's first attempt wrote `= <caller value> + 1`, so on the
//     ordinary path (no version supplied) the counter never moved at all and
//     every caller read 0 forever — a working compare-and-set on top of that
//     could never detect anything. Here the increment is keyed purely on
//     `members` being written and is independent of req.MembersVersion.
//     Concretely, a Go-computed version would be read by UpdateProject's
//     pre-transaction checkProjectAccess, i.e. BEFORE the row lock is taken, so
//     two racing writers would both compute the same next value.
//
//   - Supplying the version must add a WHERE PREDICATE. aihub#241's second
//     attempt accepted the parameter and changed what got stored but added no
//     precondition, so a stale writer still won silently. Here the version is a
//     precondition and nothing else: it is never stored, and there is no Go-side
//     comparison anywhere that could keep the behaviour correct if this line
//     were deleted.
//
// The two are deliberately orthogonal, exactly as in work_items: the increment
// is keyed on `members` being written, the precondition on the caller supplying
// a version. Omitting the version keeps the historical unconditional overwrite.
//
// The increment is keyed on `members` and NOT on "any update", which is the
// whole reason this is a dedicated counter rather than a compare-and-set on
// updated_at:
//
//   - updated_at moves on every write (the trigger), so a members guard keyed on
//     it would 409 because somebody edited the description — a guard that cries
//     wolf gets passed `nil` by the next caller who hits it.
//   - a timestamp compare-and-set compares REPRESENTATIONS, not values.
//     TIMESTAMPTZ is microsecond precision and RFC3339 drops trailing zeros on
//     the way out through JSON, so `.120000` comes back as `.12` and the
//     round-trip is not guaranteed to reproduce the stored value bit for bit.
//     An integer has no such trap.
//
// A members_version with nothing to update is rejected by UpdateProject rather
// than silently returning the row: a precondition that quietly checks nothing is
// the failure this work item exists to remove.
func buildProjectUpdate(req *UpdateProjectRequest, name string) (projectUpdate, *AihubError) {
	setClauses := []string{}
	args := []any{}
	idx := 1

	add := func(clause string, val any) {
		setClauses = append(setClauses, fmt.Sprintf(clause, idx))
		args = append(args, val)
		idx++
	}

	if req.Description != nil {
		add("description=$%d", *req.Description)
	}
	if req.Visible != nil {
		add("visible=$%d", *req.Visible)
	}
	if req.Scenario != nil {
		add("scenario=$%d", *req.Scenario)
	}
	if len(req.Repos) > 0 && string(req.Repos) != "null" {
		add("repos=$%d", []byte(req.Repos))
	}
	if req.Members != nil {
		membersJSON, err := json.Marshal(*req.Members)
		if err != nil {
			return projectUpdate{}, NewErr(ErrInternalError, "failed to marshal members")
		}
		add("members=$%d", membersJSON)
		// Computed by Postgres from the stored value, not from anything the
		// caller sent and not from any row this process read — that is what
		// makes it a usable CAS counter. See the doc comment above.
		setClauses = append(setClauses, "members_version = members_version + 1")
	}

	if len(setClauses) == 0 {
		return projectUpdate{Empty: true}, nil
	}

	whereClauses := []string{fmt.Sprintf("name=$%d", idx)}
	args = append(args, name)
	idx++

	cas := req.MembersVersion != nil
	if cas {
		whereClauses = append(whereClauses, fmt.Sprintf("members_version=$%d", idx))
		args = append(args, *req.MembersVersion)
	}

	return projectUpdate{
		Query: fmt.Sprintf("UPDATE projects SET %s WHERE %s RETURNING %s",
			joinStrings(setClauses, ", "), joinStrings(whereClauses, " AND "), projectSelectCols),
		Args: args,
		CAS:  cas,
	}, nil
}

// membersCASConflictErr builds the 409 for a failed members compare-and-set
// (aihub#260). Never a 400: the caller's payload was well-formed, someone else
// simply wrote members first.
//
// `current` is always known, unlike work_items' equivalent, which needs a
// casVersionUnknown placeholder because it re-reads after the fact. Here
// UpdateProject has already read members_version under SELECT ... FOR UPDATE in
// this same transaction, so the row cannot have moved between that read and the
// failed UPDATE, and the number handed back is exactly what the caller must
// retry with.
func membersCASConflictErr(expected, current int) *AihubError {
	return NewErrDetails(ErrConflictCASFailed,
		fmt.Sprintf("project members CAS failed: members_version is %d, not the expected %d — "+
			"reread the project (pf_list_projects) and retry with its current members_version",
			current, expected),
		map[string]any{
			"expected_members_version": expected,
			"current_members_version":  current,
		})
}

// UpdateProject patches a project (owner/admin only).
func UpdateProject(ctx context.Context, conn *pgxpool.Pool, name string, caller *UserRecord, req UpdateProjectRequest) (*Project, *AihubError) {
	// aihub#260: a precondition attached to nothing is worse than no
	// precondition, because the caller reads the 200 as "the guard passed".
	// members_version is a compare-and-set on a write, so there must be a write.
	//
	// Checked here, before the pool is touched, so it has a behavioural test that
	// needs no database (TestUpdateProject_MembersVersionWithNothingToWriteIs400
	// passes a nil pool). A check living below checkProjectAccess could only be
	// covered by a DB-gated test, and a DB-gated test in this repo SKIPs on
	// `go test ./...` while still reading as coverage.
	if req.MembersVersion != nil && !projectUpdateWritesSomething(&req) {
		return nil, NewErr(ErrBadRequest,
			"members_version is a compare-and-set precondition for an update, but this request changes nothing; "+
				"send it together with the fields you want to write")
	}

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

	// Validate scenario when present. A nil pointer means "leave unchanged"; an
	// empty/whitespace string means "clear" (allowed). A non-empty value must be a
	// git URL. Normalize req.Scenario to the trimmed value so the SQL below persists
	// it (not the raw, possibly space-padded, input).
	if req.Scenario != nil {
		norm, aerr := validateScenario(*req.Scenario)
		if aerr != nil {
			return nil, aerr
		}
		req.Scenario = &norm
	}

	if req.Members != nil {
		for _, m := range *req.Members {
			if m.Role != "viewer" && m.Role != "writer" && m.Role != "maintainer" {
				return nil, NewErr(ErrBadRequest, fmt.Sprintf("invalid role %q for member %s: must be viewer, writer, or maintainer", m.Role, m.UserID))
			}
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
	//
	// members_version rides along for aihub#260. The row is locked for the rest
	// of this transaction, so this value is still what the UPDATE below compares
	// against and is safe to report in a conflict — no second read needed. Note
	// this is NOT the compare-and-set: the guard is the WHERE predicate compiled
	// by buildProjectUpdate, and deleting that predicate must break the
	// behaviour, so nothing here compares the two in Go.
	var lockedOwnerID string
	var lockedMembersVersion int
	if err := tx.QueryRow(ctx,
		`SELECT owner_user_id, members_version FROM projects WHERE name=$1 FOR UPDATE`, name,
	).Scan(&lockedOwnerID, &lockedMembersVersion); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, NewErr(ErrProjectNotFound, fmt.Sprintf("project %q not found", name))
		}
		// aihub#334: above READ COMMITTED this is where a concurrent committed
		// update turns the FOR UPDATE wait into SQLSTATE 40001. That is a
		// retryable conflict, not a broken server.
		if aerr := retryConflictErr(err, "lock project"); aerr != nil {
			return nil, aerr
		}
		return nil, NewErr(ErrInternalError, fmt.Sprintf("lock project: %v", err))
	}
	// Re-validate ownership inside the transaction to close the TOCTOU window.
	if caller.Role != "admin" && lockedOwnerID != caller.ID {
		return nil, NewErr(ErrProjectOwnerRequired, "only owner or admin can update project")
	}

	upd, aerr := buildProjectUpdate(&req, name)
	if aerr != nil {
		return nil, aerr
	}
	if upd.Empty {
		// Nothing to update — return current state
		_ = tx.Rollback(ctx)
		return existing, nil
	}

	row := tx.QueryRow(ctx, upd.Query, upd.Args...)
	p, scanErr := scanProject(row)
	if scanErr != nil {
		// The row is held by this transaction's FOR UPDATE, so it cannot have
		// vanished: with a compare-and-set requested, no rows can only mean the
		// members_version precondition did not match.
		if upd.CAS && errors.Is(scanErr, pgx.ErrNoRows) {
			return nil, membersCASConflictErr(*req.MembersVersion, lockedMembersVersion)
		}
		if aerr := retryConflictErr(scanErr, "update project"); aerr != nil {
			return nil, aerr
		}
		return nil, NewErr(ErrInternalError, fmt.Sprintf("update project: %v", scanErr))
	}

	if err := tx.Commit(ctx); err != nil {
		// aihub#334: under SERIALIZABLE, SSI reports most conflicts HERE rather
		// than at the statement that caused them, so this hop needs the same
		// mapping as the two above and not a bare INTERNAL_ERROR.
		if aerr := retryConflictErr(err, "commit update project"); aerr != nil {
			return nil, aerr
		}
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
