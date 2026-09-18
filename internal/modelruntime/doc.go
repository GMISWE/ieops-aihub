// Package modelruntime is the new workflow path's model execution foundation
// (aihub#708 Batch 3): the machine-local model catalog, the uniform effort
// adapter, candidate preflight, ordered bounded fallback, and bounded
// subprocess execution.
//
// It is deliberately DISJOINT from the legacy path. internal/drain/channel.go
// (role agents, [roles.tiers] tiers, --channel/--preset) stays the legacy
// path's invocation builder, and internal/roles stays the legacy role/tier
// machinery. Nothing here selects by role or tier; a candidate is a
// workflow.ModelCandidate — an ordered {harness, model, effort} triple — and
// the trusted policy is the machine's [[models]] catalog plus the
// controller-issued workflow.StepGrant.
//
// # Public API for the next CLI integrator
//
// Authoring and config (with internal/config):
//
//	config.MachineConfig.Models       the [[models]] catalog (typed entries)
//	config.ValidateModels             strict catalog validation
//	modelruntime.NewCatalog           validated catalog view
//
// WI step authoring — turning operator names into pinned candidates:
//
//	Catalog.Candidates(names)         deterministic exact name→[]workflow.ModelCandidate
//	                                  resolution, order preserved, unknown name refuses
//
// Dispatch-time checks, in the order the controller should apply them:
//
//	Preflight                         machine gate: candidate allowed (exact
//	                                  catalog match), uses intersection with
//	                                  the trusted entry, uniform effort
//	                                  support (per-model where the harness
//	                                  exposes it locally), read_only and
//	                                  isolation representability. Returns a
//	                                  Ready carrying the exact Command.
//	NewFallback                       ordered, bounded fallback state machine
//	                                  keyed by per-attempt identity
//	ClassifyOutcome                   StepResult → next action mapping
//
// Execution:
//
//	Start                             bounded subprocess: own process group,
//	                                  /dev/null stdin, streaming writers
//	Proc.Stop                         one SIGTERM to the group, bounded wait,
//	                                  one SIGKILL backstop
//
// # Guarantees this package is built to preserve
//
//   - No credentials and no arbitrary invocation flags ever reach a command
//     line from config or database values: the Command is constructed ONLY
//     from the candidate triple, the grant, and the prompt.
//   - The uniform effort vocabulary is closed (config.ModelEfforts); a level
//     the adapter cannot express is an error, never a downgrade, and
//     per-model support is consulted where the harness exposes it locally
//     (codex's model catalog) rather than assumed.
//   - A read_only grant is enforced on EVERY candidate: fallback can never
//     widen capability, and a harness with no representable read-only
//     carrier is refused visibly instead of dispatched wide — cc, whose only
//     carrier is a tool denylist that leaves the write-capable Bash (a
//     denylist is never claimed to be read-only), and opencode, whose
//     carrier lives behind an agent selector that fails open.
//   - Fallback advances only on pre-start unavailability, or on an
//     infrastructure failure AFTER the old process tree is stopped and the
//     side effects reconciled; a review/test FAIL never advances, and each
//     candidate is used at most once per attempt identity.
//
// The package performs no lifecycle writes (no aihub calls, no git) and knows
// nothing about work-item progression; that is the controller's job.
package modelruntime
