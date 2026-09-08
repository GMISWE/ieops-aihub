package domain

// aihub#463 — the closed vocabularies on the `users` table, in the package that
// refuses a value outside them, exported so the MCP layer publishes the same set
// it will be judged against.
//
// This is the second application of the policy work_item_fields.go states in
// full: *a vocabulary a DB CHECK enforces is ALSO validated in Go and answered
// with a 400 naming the field; the CHECK is the last line of defence, never the
// caller-facing one.* aihub#411 §6.1 T1-4 is the reason it is applied per-CHECK
// rather than per-field — "a per-field fix produces a fourth instance".
//
// ─── What the gap actually cost here ───────────────────────────────────────
//
// Worse than the work-item case, not merely the same. handleCreateUser answers a
// failed INSERT with internalError(c, "failed to create user"), which DISCARDS
// the pgx error, so `role: "maintainer"` came back as:
//
//	500 INTERNAL_ERROR  failed to create user
//
// The work-item 500 at least carried the constraint name and SQLSTATE forward
// through dbErrCause; this one names neither the field, nor the value, nor the
// legal set. A caller — human or LLM — cannot tell an illegal role from a
// duplicate email or a dead database, and 500 tells it to retry either way.
//
// ─── The MCP enum is NOT the guard, and that is the whole point ─────────────
//
// aihub#396 recorded that "the go-sdk validates an enum before the handler runs
// (mcp/tool.go, resolved.Validate)". Read on go-sdk v1.6.0, that is true only of
// the GENERIC registration path: applySchema -> resolved.Validate is wired in
// toolForErr, reached from the top-level AddTool[In, Out]. polyforge registers
// through the untyped METHOD (*mcp.Server).AddTool(t, h) (internal/mcp/
// server.go, addTool), and Server.callTool for that path invokes st.handler
// directly with no schema step. So a published enum here constrains the CLIENT —
// an LLM reading tools/list, and any client that validates before sending — and
// nothing in this process refuses the value.
//
// ⇒ The enum is how a caller learns the set; THIS file is what enforces it.
// Publishing the enum without the Go check would move a 500 nowhere.

// userTypes mirrors the user_type CHECK in
// internal/db/migrations/0001_initial.sql.
var userTypes = map[string]bool{
	"human":   true,
	"machine": true,
}

// userGlobalRoles mirrors the role CHECK in
// internal/db/migrations/0001_initial.sql.
//
// ⚠️ "Global" is load-bearing, not decoration. aihub#411 §6.2 T2-17 counts three
// distinct role vocabularies in this system, and two of them are spelled `role`:
// this column (writer|admin), and a project MEMBER's role
// (viewer|writer|maintainer), validated separately in projects.go. They are not
// supersets of one another — `admin` is not a member role and `maintainer` is
// not a global one — so a name that did not say which one this is would be an
// invitation to validate a member role against it.
var userGlobalRoles = map[string]bool{
	"writer": true,
	"admin":  true,
}

// UserTypeList returns the legal `user_type` values, sorted for a stable
// rendered schema.
func UserTypeList() []string { return sortedKeys(userTypes) }

// UserGlobalRoleList returns the legal users.role values — the GLOBAL role, not
// a project member role.
func UserGlobalRoleList() []string { return sortedKeys(userGlobalRoles) }

// ValidateUserType checks a user_type value that was supplied.
//
// Exported because the caller is internal/server (handleCreateUser), unlike the
// work-item validators whose caller is in this package. The empty string is not
// special-cased: the handler defaults it before calling, so this function only
// ever sees a value somebody chose, and "" reaching here would be a real
// caller-visible mistake rather than an omission.
func ValidateUserType(userType string) *AihubError {
	if !userTypes[userType] {
		return vocabularyErr("user_type", userType, UserTypeList())
	}
	return nil
}

// ValidateUserGlobalRole checks a users.role value that was supplied.
func ValidateUserGlobalRole(role string) *AihubError {
	if !userGlobalRoles[role] {
		return vocabularyErr("role", role, UserGlobalRoleList())
	}
	return nil
}
