package slugres

// This file is the census ledger: every way a value may prove it is a
// canonical work_items(id), written down where a reviewer can disagree with
// it. The scanner (slugres.go) is the mechanism; this file is the policy.
//
// Two lessons from aihub#361's first cut are structural here:
//
//   - An exemption is granted to a CALL SITE (file + function + the exact
//     argument text + the column), never to a file. A file-granular exemption
//     is a pre-approved hole: the next call added to the file inherits it
//     silently.
//   - A recognised name is not enough — the aihub#361 reviewer defeated a
//     name-keyed allowlist by wrapping the argument. classifyExpr therefore
//     rejects any call wrapping a raw value (only registered resolvers and
//     minters pass), and the calibration fixtures pin that direction.

// wiResolvers are the functions that resolve an id-or-slug reference and
// return a struct whose ID field is canonical (first return value). Their own
// SQL carries the `id = $N OR slug = $N` shape, which the scanner verifies
// independently (it classifies those literals as self-resolving).
var wiResolvers = map[string]bool{
	"GetWorkItem":               true, // internal/domain/work_items.go -> *WorkItem
	"ResolveVisibleWorkItemRef": true, // internal/domain/work_items.go -> *VisibleWorkItemRef
	"getWorkItemFn":             true, // internal/server/ui_handlers_wi.go test seam over GetWorkItem
}

// idResolvers resolve an id-or-slug reference and return the canonical id
// STRING directly.
var idResolvers = map[string]bool{
	"resolveBlockedByRef":        true, // internal/domain/work_items.go (tx side)
	"resolveVisibleRefOnTx":      true, // internal/domain/work_items.go (tx side, shared body)
	"resolveRecallWorkItemRef":   true, // internal/domain/memory.go (aihub#363)
	"resolveRememberWorkItemRef": true, // internal/domain/memory.go (resolve-and-scope, Remember)
}

// idMinters return a freshly minted canonical id. A minted id has never been
// a slug.
var idMinters = map[string]bool{
	"NewID":         true, // internal/domain/ids.go
	"newWorkItemID": true, // internal/domain/work_items.go — NewID("wi") under its own name
}

// sqlWrapperFuncs are this repo's helpers with the (…, sql string, args ...any)
// shape: the placeholder $N binds to the argument N positions after the SQL,
// exactly like Query/QueryRow/Exec. Registering a wrapper is what keeps its
// call sites position-mapped instead of falling into the dynamic-SQL net.
var sqlWrapperFuncs = map[string]bool{
	"bestEffortExec": true, // internal/domain/pgx_err.go
}

// ParamContract declares that a function PARAMETER (or a field of a
// struct-typed parameter, written "param.Field") receives only canonical
// work-item ids. Registering a contract does not discharge the obligation —
// it moves it to every caller, which the gate checks (scanContractCallers).
type ParamContract struct {
	// File anchors the contract to the declaring file; a contract whose
	// function has moved or been renamed is reported, not silently ignored.
	File string
	// Func is the bare function name.
	Func string
	// Param is the parameter name, or "param.Field" for a struct field.
	Param string
	// Reason records why the contract holds — usually where its callers get
	// their canonical id from.
	Reason string
}

var canonicalParams = []ParamContract{
	// ── dependency graph (the aihub#357 surface) ──────────────────────────
	{File: "internal/domain/dependencies.go", Func: "ListDependencies", Param: "wiID",
		Reason: "compares wiID to blocked_wi_id/blocking_wi_id; handleListDependencies resolves and passes wi.ID (aihub#357), the UI detail page passes wi.ID through listDependenciesFn"},
	{File: "internal/domain/dependencies.go", Func: "CreateDependency", Param: "req.BlockedWIID",
		Reason: "handleCreateDependency overwrites both request ends with resolved canonical ids before the call (aihub#357); the existence probe and the wi_dependencies INSERT both key on it"},
	{File: "internal/domain/dependencies.go", Func: "CreateDependency", Param: "req.BlockingWIID",
		Reason: "same as req.BlockedWIID: resolved at the handler, FK column at the INSERT"},
	{File: "internal/domain/dependencies.go", Func: "DeleteDependency", Param: "blockedWIID",
		Reason: "handleDeleteDependency resolves both path ends and passes .ID (aihub#357)"},
	{File: "internal/domain/dependencies.go", Func: "DeleteDependency", Param: "blockingWIID",
		Reason: "same handler, same resolution, other end of the edge"},
	{File: "internal/domain/dependencies.go", Func: "GetParentRef", Param: "childWiID",
		Reason: "the UI detail view passes wi.ID via the getParentRefFn seam"},
	{File: "internal/domain/dependencies.go", Func: "ListChildren", Param: "parentWiID",
		Reason: "the UI detail view passes wi.ID via the listChildrenFn seam"},
	{File: "internal/domain/dependencies.go", Func: "detectCycle", Param: "blockingWIID",
		Reason: "internal helper of CreateDependency; receives CreateDependency's contract-covered request ends"},
	{File: "internal/domain/dependencies.go", Func: "emitDependencyCreatedEvent", Param: "blockedWIID",
		Reason: "called by CreateDependency (contract-covered ends) and by CreateWorkItem's blocked_by loop (freshly minted wiID + resolveBlockedByRef output)"},
	{File: "internal/domain/dependencies.go", Func: "emitWIUnblockedEvent", Param: "wiID",
		Reason: "called by requeueIfUnblocked and the complete_attempt requeue sweep with ids read out of wi_dependencies rows — FK values, canonical from the DB"},
	{File: "internal/domain/dependencies.go", Func: "requeueIfUnblocked", Param: "blockedWIID",
		Reason: "DeleteDependency passes its resolved blocked end"},
	{File: "internal/domain/dependencies.go", Func: "requeueIfUnblocked", Param: "excludeBlockerWIID",
		Reason: "DeleteDependency passes its resolved blocking end"},

	// ── run-attempt lifecycle ─────────────────────────────────────────────
	{File: "internal/domain/run_attempts.go", Func: "FnCompleteAttempt", Param: "wiID",
		Reason: "the complete_attempt HTTP handlers resolve id-or-slug via GetWorkItem and pass wi.ID; the row lock inside re-reads work_items by this id"},
	{File: "internal/domain/run_attempts.go", Func: "FnRecordRepoPins", Param: "wiID",
		Reason: "the repo-pins route resolves via GetWorkItem and passes wi.ID"},
	{File: "internal/domain/run_attempts.go", Func: "fnForceTerminateStep", Param: "wiID",
		Reason: "called inside FnCompleteAttempt (its contract param) and the takeover path (wi.ID scanned from the FOR UPDATE row)"},
	{File: "internal/domain/run_attempts.go", Func: "probeForeignLockHolders", Param: "wiID",
		Reason: "called with wi.ID scanned from the claim/takeover self-resolving FOR UPDATE row"},
	{File: "internal/domain/run_attempts.go", Func: "unblockDependentWI", Param: "wiID",
		Reason: "called inside FnCompleteAttempt with the row-scanned wi.ID"},

	// ── lock machinery ────────────────────────────────────────────────────
	{File: "internal/domain/resource_events.go", Func: "acquireLockUpsert", Param: "workItemID",
		Reason: "lock plumbing below the claim/acquire paths; every caller passes the row-scanned wi.ID"},
	{File: "internal/domain/resource_events.go", Func: "lockRefusalFor", Param: "workItemID",
		Reason: "called by acquireLockUpsert with its own contract-covered workItemID"},

	// ── misc domain ───────────────────────────────────────────────────────
	{File: "internal/domain/wi_embedding.go", Func: "refreshWorkItemEmbeddingBestEffort", Param: "wiID",
		Reason: "called after create/update flows with wi.ID from the row just written or loaded"},
	{File: "internal/domain/wi_watches.go", Func: "WatchWorkItem", Param: "wiID",
		Reason: "the UI watch toggle resolves the work item and passes wi.ID via the watchWorkItemFn seam"},
	{File: "internal/domain/wi_watches.go", Func: "UnwatchWorkItem", Param: "wiID",
		Reason: "same toggle, opposite direction, same resolved wi.ID"},
	{File: "internal/domain/wi_watches.go", Func: "IsWatchingWorkItem", Param: "wiID",
		Reason: "read through fetchWatching, whose own parameter carries the contract"},

	// ── step routes (internal/server) ─────────────────────────────────────
	{File: "internal/server/routes_step.go", Func: "loadCompletedSteps", Param: "wiID",
		Reason: "its doc comment states the canonical-id precondition; handleGetStep resolves and overwrites wiID with wi.ID before calling"},
	{File: "internal/server/routes_step.go", Func: "startStep", Param: "wiID",
		Reason: "handleUpdateStep resolves id-or-slug via GetWorkItem and overwrites wiID with wi.ID before any step mutation"},
	{File: "internal/server/routes_step.go", Func: "insertStepCompletion", Param: "wiID",
		Reason: "called by handleUpdateStep after it overwrites wiID with the resolved wi.ID"},
	{File: "internal/server/routes_step.go", Func: "insertStepEvent", Param: "wiID",
		Reason: "step-event emission inside the resolved-handler flow"},
	{File: "internal/server/ui_handlers_wi.go", Func: "fetchWatching", Param: "wiID",
		Reason: "the detail-page side-load; its caller passes the resolved work item's wi.ID"},
}

// SiteExemption clears ONE argument expression at ONE call site. Col is the
// column the position lands in ("work_items.id", "work_item_id", ...) or
// "call:<Func>.<Param>" for a contract-call exemption, so an exemption for one
// sink in a function does not silently cover another sink over the same
// variable.
type SiteExemption struct {
	File   string
	Func   string // display name, methods as "(Recv).Name"
	Arg    string // exact source text of the argument expression
	Col    string
	Reason string
}

func (e *SiteExemption) key() string { return e.File + ":" + e.Func + ":" + e.Arg + ":" + e.Col }

var siteExemptions = []SiteExemption{
	{File: "internal/domain/conflicts.go", Func: "PredictConflicts", Arg: "p.WIID", Col: "work_items.id",
		Reason: "p ranges over will_unlock candidates scanned out of wi_dependencies.blocked_wi_id — " +
			"an FK into work_items(id), so the value is canonical from the DB; the scanner cannot " +
			"trace a range variable over a locally-built slice"},
	{File: "internal/server/routes_memory.go", Func: "handleUpdateMemory", Arg: "*head.WorkItemID", Col: "work_item_id",
		Reason: "head is the memory row loaded from the DB before the update; memories.work_item_id " +
			"FK-references work_items(id), so the pointer either is nil (guarded) or holds a canonical id"},

	// pkg/client's HTTP methods share names with the domain functions they
	// front, and the MCP tools call the CLIENT: the value goes over HTTP to the
	// handler, which is where resolution belongs (and where the contract layer
	// checks it). These clear the name collision, keyed on the call's arity so
	// they stop matching if the call changes shape.
	{File: "internal/mcp/tools_dependency.go", Func: "(*Server).registerDependencyTools",
		Arg: "<call with 2 args>", Col: "call:CreateDependency.req.BlockedWIID",
		Reason: "s.client.CreateDependency (pkg/client), not domain.CreateDependency: it POSTs to " +
			"handleCreateDependency, which resolves both ends (aihub#357). Sending a slug here is correct."},
	{File: "internal/mcp/tools_dependency.go", Func: "(*Server).registerDependencyTools",
		Arg: "<call with 2 args>", Col: "call:CreateDependency.req.BlockingWIID",
		Reason: "same call, other contract end — one HTTP client call clears both."},
	{File: "internal/mcp/tools_dependency.go", Func: "(*Server).registerDependencyTools",
		Arg: "<call with 2 args>", Col: "call:ListDependencies.wiID",
		Reason: "s.client.ListDependencies (pkg/client): it GETs handleListDependencies, which " +
			"resolves id-or-slug and passes wi.ID down (aihub#357)."},
}

// DynamicExemption covers one string literal carrying a work-item-id
// comparison that the position-mapper cannot see (dynamic SQL). Fragment must
// be a substring of the literal; Reason must say where the bound value is
// resolved.
type DynamicExemption struct {
	File     string
	Func     string // enclosing function display name, or "<file scope>"
	Fragment string
	Reason   string
}

func (e *DynamicExemption) key() string { return e.File + ":" + e.Func + ":" + e.Fragment }

var dynamicSQLExemptions = []DynamicExemption{
	{File: "internal/domain/memory.go", Func: "ListEvents", Fragment: "e.work_item_id = $%d",
		Reason: "Sprintf where-builder. The bound value is f.WorkItemID, resolved by the callers: " +
			"handleListEvents resolves id-or-slug via GetWorkItem and passes wi.ID (aihub#343), and " +
			"MCP pf_read_events reaches this through that handler."},
	{File: "internal/domain/memory.go", Func: "recallText", Fragment: "AND work_item_id = $%d",
		Reason: "Sprintf where-builder. Recall resolves req.WorkItemID via resolveRecallWorkItemRef " +
			"before building the filter and overwrites the request field with the canonical id (aihub#363)."},
	{File: "internal/domain/memory_lexical.go", Func: "recallLexical", Fragment: "AND work_item_id = $%d",
		Reason: "Sprintf where-builder, same request object: recallLexical's only caller is Recall, " +
			"which has already overwritten req.WorkItemID with resolveRecallWorkItemRef's canonical id " +
			"at its single entry (aihub#363) before the lexical section runs (aihub#360)."},

	// The two lock-release statements are file-scope consts EXECUTED through
	// releaseLocks(ctx, tx, stmt, op, args...) — the SQL travels as a
	// parameter, so no call site can be position-mapped. Order matters below:
	// the cancelled statement is a strict prefix of the undeclared one, so the
	// entry with the discriminating fragment must come first or it never
	// matches anything and reads as stale.
	{File: "internal/domain/resource_events.go", Func: "<file scope>",
		Fragment: "rl.resource_key = ANY($2::text[])",
		Reason: "releaseUndeclaredLocksSQL: $1 is the wiID of the declared-resources narrowing " +
			"inside UpdateWorkItem's transaction, whose row lock resolves id-or-slug " +
			"(`id = $1 OR slug = $1` in internal/domain/work_items.go) before this runs."},
	{File: "internal/domain/resource_events.go", Func: "<file scope>",
		Fragment: "AND ra.work_item_id = $1",
		Reason: "releaseCancelledWILocksSQL: $1 is wi.ID in CancelWorkItem, read from the work_items " +
			"row it locked by the self-resolving `id = $1 OR slug = $1` predicate."},
}

// AliasContract declares a variable that holds a contract function as a value
// (test seams and the like). Calls through the alias are checked against the
// aliased function's contracts.
type AliasContract struct {
	File    string
	Alias   string // the variable name
	Aliases string // the contract function it holds
	Reason  string
}

func (a *AliasContract) key() string { return a.File + ":" + a.Alias + "->" + a.Aliases }

var aliasContracts = []AliasContract{
	// internal/server/ui_handlers_wi.go stubs its domain reads through
	// package-level function variables so tests can inject failures. Each one
	// is declared here so calls through the seam stay inside the contract
	// check.
	{File: "internal/server/ui_handlers_wi.go", Alias: "listDependenciesFn", Aliases: "ListDependencies",
		Reason: "test seam; the UI detail page calls it with the resolved wi.ID"},
	{File: "internal/server/ui_handlers_wi.go", Alias: "getParentRefFn", Aliases: "GetParentRef",
		Reason: "test seam; called with the resolved wi.ID"},
	{File: "internal/server/ui_handlers_wi.go", Alias: "listChildrenFn", Aliases: "ListChildren",
		Reason: "test seam; called with the resolved wi.ID"},
	{File: "internal/server/ui_handlers_wi.go", Alias: "watchWorkItemFn", Aliases: "WatchWorkItem",
		Reason: "test seam; the watch toggle calls it with the resolved wi.ID"},
	{File: "internal/server/ui_handlers_wi.go", Alias: "unwatchWorkItemFn", Aliases: "UnwatchWorkItem",
		Reason: "test seam; the watch toggle calls it with the same resolved wi.ID as the watch arm"},
	{File: "internal/server/ui_handlers_wi.go", Alias: "isWatchingFn", Aliases: "IsWatchingWorkItem",
		Reason: "test seam; fetchWatching calls it with its contract-covered wiID"},
}
