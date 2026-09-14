// Package engine is a behavior-preserving Go port of the step-execution logic that today
// lives in markdown pseudocode, read and executed by an LLM inside a Claude Code / pi session
// ("B/C" in aihub#640's A/B/C harness identity terminology): the §0 startup sequence
// (plugins/polyforge/skills/pf-execute/references/engine-native-details.md), the step-bracket
// pf_update_step sequencing, review-result marker parsing, the `role:` catalog/heuristic
// fallback, and wrap-time worktree cleanup (plugins/polyforge/skills/_common/references/
// lifecycle-details.md).
//
// This package exists (aihub#654) because a future headless CLI orchestrator with no LLM
// turning the crank ("A") cannot execute markdown pseudocode the way B/C does — the
// workflow_identity_constraint (aihub#640) requires A to contain no execution logic B/C lacks,
// so both must eventually call the same Go code. Reachable today via `polyforge engine <verb>`
// (internal/cli/engine.go); consumed directly by B/C's Go-side tests, and destined to be the
// thing A calls when it is built (out of this wi's scope).
//
// Design rule (aihub#654 plan, Design Decision B): every exported function here operates on
// plain data (strings/structs) with no MCP/HTTP calls and no shelling out from inside this
// package itself — every I/O boundary is an injected function type (GitRunner below, plus a few
// narrower closures on individual functions). The real, os/exec-backed implementations live in
// internal/cli/engine.go. This keeps the package unit-testable without a live aihub server or a
// real git checkout, and keeps its outputs suitable for a future same-input-same-output
// cross-implementation contract test (aihub#657).
//
// This package consumes internal/roles's already-exported LoadRoles / RoleByName /
// RoleForStepID / Role.SortedStepIDs / ValidTiers verbatim. It introduces no parallel
// role/tier/capability data model — see role.go.
package engine

// GitRunner runs `git -C dir <args...>` and returns its trimmed stdout. The production
// implementation (os/exec-backed) lives in internal/cli/engine.go; tests throughout this
// package inject a fake, so no code under internal/engine shells out itself.
type GitRunner func(dir string, args ...string) (stdout string, err error)
