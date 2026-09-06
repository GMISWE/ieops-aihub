package server

// The CLASS gate for aihub#377: project membership is the only visibility boundary.
//
// WHAT THE DEFECT IS
// ------------------
// A caller who is not a member of a project could tell "this object does not
// exist" from "this object exists and you may not see it", because the two
// answers differed. Both refuse; only one of them confirms. Work item slugs are
// `<project>#<seq>` with seq counting from 1, so one distinguishable bit turns
// every wi-keyed endpoint into an enumerator over other teams' projects — while
// never returning a single field of any of them.
//
// aihub#357, #363, #371 and #376 were four instances of it. Fixing a fifth is not
// what closes the class.
//
// WHY THE GATE IS STRUCTURAL AND NOT BEHAVIOURAL
// ---------------------------------------------
// A behavioural test pins the endpoints someone thought of. The byte-identity
// tests do that and they are necessary — but the defect is a RELATION between two
// response paths in one handler, and the population is "every handler that
// resolves an object before it knows which project to authorize against". A new
// handler of that shape is written every few weeks and no behavioural test goes
// red when one arrives. So this file asserts on the shipped AST.
//
// 🔴 WHY R0 CENSUSES A BEHAVIOUR AND NEVER A FUNCTION NAME
// -------------------------------------------------------
// This is the load-bearing lesson of the work item, and it was learned the
// expensive way, twice in two days, by both people involved:
//
//   - The work item as filed said the defect population was the ~25 call sites of
//     domain.GetWorkItem, and told its executor to turn that upper bound into a
//     real number. It is not an upper bound. 18 of the 40 real violations never
//     touch GetWorkItem; they reach a project through loadMemoryFn,
//     commitMemoryProjectFn or GetLatestByID.
//   - The executor then censused the repo by grepping the name
//     `checkProjectAccess`, found 41 call sites, and reported a complete
//     inventory of 37. It was missing three: checkProjectAccessSoft
//     (ui_handlers_wi.go) is a second copy of the same rule under a different
//     name, with four call sites, and two more handlers open-coded
//     `u.ProjectRoles[project]` inline. GET /ui/wi/:id, the watch toggle and the
//     HTMX events partial were all enumerable and all absent from the list.
//
// Same error twice: bound the population by ONE NAME, miss nearly half. A gate
// written the same way inherits the same blind spot and reports green — which is
// worse than the grep, because it looks like proof.
//
// There was a third instance, and its TIMING is the evidence. Having written the
// two paragraphs above — an analysis of two consecutive failures to bound a
// population correctly — the same executor then enumerated the TESTS that this
// change would break by grepping `StatusForbidden`, and reported a 26-row verdict
// table. A test asserting the compliant side cannot appear in that grep by
// construction, and one did: TestUIWIDetail_403_NoProjectAccess asserted
// StatusOK. It went red off-table. Understanding the error, in writing, minutes
// earlier, did not prevent committing it a third time in a fresh disguise.
//
// 🔴 And a fourth, which should settle the argument, because the correct rule was
// already implemented in this package — for the other direction.
// TestProjectRolesHaveOneDerivation (middleware_project_roles_test.go) exists
// because the same map's WRITE side had been copied three times. Its comment,
// verbatim:
//
//	The first version of this gate inspected BearerAuth alone, because BearerAuth
//	was the function being fixed. That is the mistake this comment exists to stop
//	anyone repeating: a gate scoped to the file you are editing cannot see a defect
//	one hop away, and there WAS one. `loadUserByAPIKeyID` in ui_handlers_auth.go
//	held a third character-identical copy with both defects intact, serving every
//	/ui page load, and it stayed green through the entire BearerAuth fix. It was
//	found by grepping for writers of ProjectRoles afterwards.
//
//	So the anchor is the authorization map, not a function name: EVERY function in
//	this package that writes UserContext.ProjectRoles must obtain the role from
//	roleForUserInMembers […]
//
// "The anchor is the authorization map, not a function name" IS R0. It was
// already written down — in this package, in a test file, about this exact field,
// enforced by a passing census — and the read side was still inventoried by
// function name one work item later.
//
// So the lesson of aihub#377 is not "we missed some". It is that WRITING A LESSON
// DOWN IN THE RIGHT PLACE DOES NOT MAKE IT OPERATIVE. Four instances: two before
// the analysis was written, one minutes after, one against a prior art that
// stated the fix in one sentence. Only the census is load-bearing. Everything
// above it, this comment included, is a thing someone will read after the gate
// has already told them.
//
// So R0 does not ask "who calls checkProjectAccess". It asks "who touches
// u.ProjectRoles or u.ProjectScope at all", classifies every one of them, and
// fails on anything unclassified. Adding a fourth copy of the rule under a fifth
// name cannot pass unnoticed; it forces a line in a table.
//
// That claim was verified, not assumed: a mutation adding
// `checkProjectAccessQuiet` — a fourth copy under a fifth name, used by one
// handler — was applied to this tree and R0 went red naming it. The run is
// recorded in aihub#377's execute artifact. If you weaken R0's trigger, redo
// that probe; a census you have not seen fail is a census you have not tested.
//
// A note on the unit of exemption, borrowed from aihub#361's post-mortem
// (embed_writer_parity_test.go): its exemptions were keyed on the FILENAME, so
// appending a fifth embedding writer to an already-listed file passed the whole
// suite. Every table here is keyed on `file.go:FuncName`, never on a file.
//
// WHAT THIS GATE DOES NOT PROVE
// -----------------------------
// It proves the shape: that a load-then-authorize handler returns the shared
// response from its loader's error branch, and that no unclassified code decides
// project visibility. It does NOT prove the two responses are byte-identical at
// runtime — a message could be edited to differ. That half is behavioural and
// lives in the byte-identity suites (blocked_by_visibility_db_test.go and
// friends), which compare a real "absent" response against a real "invisible"
// one with JSONEq. Neither half is sufficient alone: the structural half catches
// the handler nobody wrote a test for, the behavioural half catches the wording
// drifting apart in handlers the structure approves of.

import (
	"errors"
	"fmt"
	"go/ast"
	"go/parser"
	"go/printer"
	"go/token"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/labstack/echo/v4"
)

// Compile-time anchor. Renaming any of these three breaks THIS FILE rather than
// quietly weakening the census below, which matches on their source text.
// A gate keyed on a string keeps passing after the string stops existing.
var _ = []any{errNotVisible, hideNotFound, notVisibleMessage}

// ─── R0: every reader of the caller's project roles, classified ──────────────

// projectRoleReaders classifies every function in package server whose body
// mentions u.ProjectRoles or u.ProjectScope. The value is why it is allowed to.
//
// The distinction that matters is whether the function ANSWERS "may this caller
// see project P". Only the first class does:
//
//	decides    — a copy of the access rule itself. Adding one of these is the
//	             thing this table exists to make visible. It must answer a
//	             non-member with notVisibleMessage, and it must also appear in
//	             accessDeciders (enforced by the R0/R1 agreement test below).
//	scope-only  — checks ONLY the api-key project_scope confinement. It can
//	             narrow, never grant, and the deciders above already apply the
//	             same check, so one of these is defence in depth rather than a
//	             gate anything relies on.
//	authorizes — a handler that calls a decider and branches on the result. It
//	             does not re-implement the rule; it may additionally read scope
//	             to BOUND a query, which is the safe direction.
//	bounds     — derives the set of projects a query may touch, via a decider.
//	populates  — fills ProjectRoles in during authentication. Not a decision.
//	forwards   — hands the map to a domain function without branching on it. The
//	             domain side owns the scoping; invariant 3 lives there.
//	presents   — shows callers their own memberships, or builds their project
//	             picker. Telling you what you are a member of is not a leak.
//	unrelated  — matched the census only because a field of the REQUEST is spelled
//	             the same as a field of the caller. Kept as a table line rather
//	             than excluded by a cleverer matcher: see mentionsProjectRoleState.
var projectRoleReaders = map[string]string{
	"middleware.go:checkProjectAccess":         "decides",
	"routes_memory.go:hasProjectAccess":        "decides",
	"ui_handlers_wi.go:checkProjectAccessSoft": "decides",

	// Subsumed by the deciders since aihub#377 (hasProjectAccess applies the same
	// ProjectScope check), and its one remaining caller — handleUIMemories —
	// calls hasProjectAccess immediately afterwards. Left in place because
	// scope_guard_test.go pins it directly and removing a guard to delete a
	// redundancy is not this work item's trade. A reasonable follow-up cleanup.
	"ui_helpers.go:uiScopeBlocks": "scope-only",

	"middleware.go:BearerAuth":               "populates",
	"ui_handlers_auth.go:loadUserByAPIKeyID": "populates",

	"middleware.go:visibleProjects": "bounds",

	"router.go:handleListWorkItems":             "authorizes",
	"router.go:handleClaimWorkItem":             "authorizes",
	"ui_handlers_wi.go:handleUIWIDetail":        "authorizes",
	"ui_handlers_wi.go:handleUIWIList":          "authorizes",
	"ui_handlers_memory.go:handleUIMemories":    "authorizes",
	"ui_handlers_queue.go:handleUIQueuePartial": "authorizes",

	"router.go:handleCreateWorkItem":        "forwards",
	"router.go:handleUpdateWorkItem":        "forwards",
	"router.go:handleCancelWorkItem":        "forwards",
	"router.go:handleForceTakeover":         "forwards",
	"router.go:handleCreateDependency":      "forwards",
	"router.go:handleListDependencies":      "forwards",
	"router.go:handlePredictConflicts":      "forwards",
	"routes_projects.go:callerToUserRecord": "forwards",

	"router.go:handleWhoami":               "presents",
	"ui_helpers.go:availableProjectsForUI": "presents",

	// req.ProjectScope, the project_scope field of the POST body — the scope being
	// GRANTED to a new key, not the caller's own. The census matches on the
	// selector name, so it cannot tell the two apart, and that is the accepted
	// cost of a matcher that cannot be wrong in the direction of silence.
	"router.go:handleCreateAPIKey": "unrelated",
}

func TestProjectVisibilityGate_R0_EveryProjectRoleReaderIsClassified(t *testing.T) {
	fset, files := parsePackageServer(t)

	var unclassified []string
	seen := map[string]bool{}
	for path, f := range files {
		base := filepath.Base(path)
		for _, d := range f.Decls {
			fn, ok := d.(*ast.FuncDecl)
			if !ok || fn.Body == nil {
				continue
			}
			if !mentionsProjectRoleState(fn.Body) {
				continue
			}
			key := base + ":" + fn.Name.Name
			seen[key] = true
			if _, ok := projectRoleReaders[key]; !ok {
				unclassified = append(unclassified, key+"  (line "+
					fset.Position(fn.Pos()).String()+")")
			}
		}
	}

	sort.Strings(unclassified)
	if len(unclassified) > 0 {
		t.Errorf("these functions read the caller's project roles/scope and are not "+
			"classified in projectRoleReaders:\n\t%s\n\n"+
			"Classify each one. If it DECIDES whether a caller may see a project, it is a "+
			"copy of the access rule and must answer a non-member with notVisibleMessage — "+
			"see checkProjectAccess. aihub#377 shipped with three violations hidden in "+
			"exactly this gap, because the inventory was taken by grepping for the name "+
			"`checkProjectAccess` and a second copy was called `checkProjectAccessSoft`.",
			strings.Join(unclassified, "\n\t"))
	}

	// The table must not rot in the other direction either: a stale entry is a
	// classification nobody has to justify any more, and it makes the count above
	// look larger than the real surface.
	var stale []string
	for key := range projectRoleReaders {
		if !seen[key] {
			stale = append(stale, key)
		}
	}
	sort.Strings(stale)
	if len(stale) > 0 {
		t.Errorf("projectRoleReaders lists functions that no longer read project "+
			"roles/scope (renamed, moved or deleted): %v — delete these entries", stale)
	}
}

// mentionsProjectRoleState reports whether a function body touches the caller's
// project membership state at all.
//
// Deliberately the broadest possible trigger — any selector named ProjectRoles
// or ProjectScope, however it is used. A narrower rule ("only an index
// expression", "only inside an if") would be a heuristic, and a heuristic is a
// thing that can be wrong in the direction of silence. Classifying a handler
// that merely forwards the map costs one table line; missing a handler that
// decides with it costs an enumerable API.
func mentionsProjectRoleState(body *ast.BlockStmt) bool {
	found := false
	ast.Inspect(body, func(n ast.Node) bool {
		sel, ok := n.(*ast.SelectorExpr)
		if !ok {
			return true
		}
		switch sel.Sel.Name {
		case "ProjectRoles", "ProjectScope":
			found = true
		}
		return true
	})
	return found
}

// ─── R1: load-then-authorize must deny with the shared response ──────────────

// 🔴 THERE IS NO LOADER WHITELIST, and the reason is a bug this gate had.
//
// R1's first version held a `resourceLoaders` map of eight names —
// domain.GetWorkItem, loadMemoryFn, commitMemoryProjectFn and so on — and
// examined a handler only if it called one of them. Its comment claimed, verbatim:
//
//	An unrecognised loader in a load-then-authorize handler is a violation, not a
//	pass: that is the whole point of a census.
//
// THAT SENTENCE WAS FALSE. An unlisted callee meant the handler was SKIPPED, not
// flagged — the code did the opposite of what the prose asserted. Mutation V4
// added a handler that loaded through a new `probeLoaderFn`, denied with its own
// `404 "probe not found"`, then called checkProjectAccess — a textbook instance
// of the defect — and R1 reported GREEN.
//
// So the gate written to stop "bound the population by a NAME" had bounded its
// own population by a list of names. That is the fourth instance of one mistake
// in this single work item, and the fourth happened inside the countermeasure
// built to prevent the first three:
//
//	1. the work item as filed bounded the defect population by the ~25 call sites
//	   of domain.GetWorkItem                          → missed 18 of 40
//	2. its executor censused the repo by grepping the name `checkProjectAccess`
//	                                                  → missed 3 (checkProjectAccessSoft)
//	3. the same executor bounded the TESTS that would break by grepping
//	   `StatusForbidden`                              → missed 1 (it asserted StatusOK)
//	4. this gate bounded the HANDLERS it inspects by a list of loader names
//	                                                  → missed V4
//
// Same shape every time. #3 was committed minutes after writing the analysis of
// #1 and #2. #4 was committed while writing R0, whose entire subject is that
// names are the wrong anchor.
//
// So R1 no longer asks what produced the object. It starts from the
// authorization call — checkProjectAccess(c, u, wi.Project, …) — takes the object
// whose project is being authorized (`wi`) BY ARGUMENT POSITION, finds the
// assignment in the same function that produced it, and requires THAT
// assignment's error guard to answer with the shared denial. Any loader works,
// including one written tomorrow under a name nobody has thought of, because the
// anchor is the value being authorized rather than the function that fetched it.
//
// By position, not by scanning the arguments: scanning also matched `u`, so
// `u := GetUser(c)` followed by `if u == nil { return redirectToLogin(c) }` was
// reported as a violation in twelve handlers. And an index can go stale when a
// signature changes, which is why TestProjectVisibilityGate_ProjectArgIndexIsCurrent
// fails if one lands on a string literal or on `u`/`c`. It exists for a specific
// reason: A CENSUS THAT SILENTLY READS THE WRONG ARGUMENT IS WORSE THAN NO
// CENSUS, because a green run from it is mistaken for proof.

// accessDeciders are the functions R0 classifies as "decides". Kept as its own
// list because R1 needs to recognise the load-then-authorize shape, and kept in
// sync with R0 by TestProjectVisibilityGate_R0AndR1AgreeOnWhoDecides below —
// two hand-maintained lists that must agree are two lists that will not.
// The value is the ZERO-BASED INDEX of the project argument, because that is
// what R1 anchors on. Taken by position rather than by scanning every argument:
// scanning also matched `u` (the *UserContext), so `u := GetUser(c)` followed by
// `if u == nil { return redirectToLogin(c) }` was reported as a load-then-
// authorize violation in twelve handlers. Position is exact.
//
//	checkProjectAccess(c, u, project, minRole)  -> 2
//	hasProjectAccess(u, project, minRole)       -> 1
//	checkProjectAccessSoft(u, project)          -> 1
//
// A signature change makes the index stale, so projectArgIsPlausible below
// rejects an index that lands on a string literal or on the context/user
// arguments, and the gate fails loudly rather than quietly inspecting the wrong
// thing. A census that silently looks at the wrong argument is worse than none.
var accessDeciders = map[string]int{
	"checkProjectAccess":     2,
	"hasProjectAccess":       1,
	"checkProjectAccessSoft": 1,
}

// r1Exemptions are load-then-authorize handlers whose loader-error branch is
// sanctioned even though it names none of the shared sentinels. Keyed
// file.go:FuncName, and every entry states what the two responses ARE — an
// exemption without that is an exemption nobody can check.
var r1Exemptions = map[string]string{
	// The backfill path exists precisely because req.Project is empty, so it has
	// no scope to authorize against yet; it carries its own bound instead
	// (ResolveVisibleWorkItemRef). Both halves answer 400 "project is required
	// (work_item_id lookup failed)" — an unresolvable reference and an invisible
	// one take the same early exit, which is what aihub#376 asked for. The 400 is
	// correct here and is NOT a visibility verdict: the request is malformed
	// because it named neither a project nor a resolvable work item.
	"routes_memory.go:handleRemember": "both halves answer the unchanged 400; scope is in ResolveVisibleWorkItemRef's WHERE clause",

	// The blocking end of DELETE /v1/dependencies has no access check by design
	// (the gate is writer on the BLOCKED item — see the model note above
	// handleCreateDependency). Both an unresolvable blocking id and a resolvable
	// one with no edge answer 404 "dependency not found", which keeps the message
	// that is useful to the authorized caller this endpoint is for.
	"router.go:handleDeleteDependency": `both halves answer 404 "dependency not found"`,

	// Unauthenticated /share/:id. Its loader error is folded into one combined
	// condition with "not public" and "no renderable body", and all of them
	// answer the same 404 "not found". Already compliant before aihub#377 and
	// deliberately left alone: it is the sanctioned capability-URL exception,
	// reached without any project membership at all.
	"routes_artifacts.go:handleSharedArtifact": `every failure answers the same 404 "not found"`,
}

func TestProjectVisibilityGate_R1_LoadThenAuthorizeDeniesWithTheSharedResponse(t *testing.T) {
	fset, files := parsePackageServer(t)

	var violations []string
	for path, f := range files {
		base := filepath.Base(path)
		for _, d := range f.Decls {
			fn, ok := d.(*ast.FuncDecl)
			if !ok || fn.Body == nil {
				continue
			}
			key := base + ":" + fn.Name.Name
			if _, ok := r1Exemptions[key]; ok {
				continue
			}
			for _, guard := range authorizedObjectGuards(fn.Body) {
				src := renderNode(fset, guard)
				if strings.Contains(src, "errNotVisible") ||
					strings.Contains(src, "hideNotFound") ||
					strings.Contains(src, "notVisibleMessage") ||
					strings.Contains(src, "http.StatusNotFound") {
					continue
				}
				violations = append(violations, key+" -> "+collapse(src))
			}
		}
	}

	sort.Strings(violations)
	if len(violations) > 0 {
		t.Errorf("these handlers resolve an object and then decide project access, but "+
			"answer a failed resolution with something other than the shared not-visible "+
			"response:\n\t%s\n\n"+
			"Return hideNotFound(err) (JSON) or the same 404 the access denial gives (HTML/"+
			"empty-body). Whatever the two branches return, they must be the SAME bytes: "+
			"identical status with a different message is still an oracle. If the pair is "+
			"legitimately some other shared answer, add a file.go:FuncName entry to "+
			"r1Exemptions stating what BOTH responses are.",
			strings.Join(violations, "\n\t"))
	}
}

// TestProjectVisibilityGate_R0AndR1AgreeOnWhoDecides keeps the two tables above
// from drifting. R1 recognises the load-then-authorize shape by looking for a
// call to an access decider, so a decider that R0 knows about and R1 does not is
// a hole: every handler that authorizes through it becomes invisible to R1.
func TestProjectVisibilityGate_R0AndR1AgreeOnWhoDecides(t *testing.T) {
	for key, why := range projectRoleReaders {
		if why != "decides" {
			continue
		}
		name := key[strings.Index(key, ":")+1:]
		if _, ok := accessDeciders[name]; !ok {
			t.Errorf("projectRoleReaders classifies %s as \"decides\" but accessDeciders "+
				"does not list %q — R1 cannot see handlers that authorize through it, so "+
				"every one of them is exempt by accident", key, name)
		}
	}
	for name := range accessDeciders {
		var found bool
		for key, why := range projectRoleReaders {
			if why == "decides" && strings.HasSuffix(key, ":"+name) {
				found = true
			}
		}
		if !found {
			t.Errorf("accessDeciders lists %q but no projectRoleReaders entry classifies "+
				"it as \"decides\" — one of the two tables is stale", name)
		}
	}
}

// ─── R2: a 403 is never a project-visibility verdict ─────────────────────────

// r2Forbidden is every construction of domain.ErrForbidden in package server,
// with the reason it is not a project-visibility answer. Keyed
// file.go:FuncName; a function may appear once however many it builds.
//
// 🔴 The first entry is the one to read before "tidying up".
var r2Forbidden = map[string]string{
	// 🔴 THE POSITIVE CONTROL. A caller who IS a member of the project and is
	// merely short of the role keeps a 403 and keeps the message naming the role
	// they have and the one they need.
	//
	// This is not an oversight and it is not a leftover. aihub#377's invariant has
	// a positive half — "a user who IS in a project can see everything about that
	// project" — and turning this into a 404 would hide a project from its own
	// members. That is not closing the leak, it is switching the feature off.
	//
	// It is also the ONLY thing in the codebase that goes red if someone
	// implements "every denial is a 404" by breaking authorization wholesale.
	// "All refusals look identical" is trivially satisfiable by refusing
	// everything, so a gate that only checks for uniformity cannot tell a fix
	// from an outage. Keep this branch, keep its wording, keep its tests
	// (TestCreateWorkItem_ViewerGets403BeforeDBWrite,
	// TestRemember_ViewerGets403BeforeDBWrite,
	// TestProjectVisibility_InsufficientRoleStillExplains).
	"middleware.go:checkProjectAccess": "member short of the required role — the positive control, see above",

	"middleware.go:RequireAdmin":                     "global admin role required; names no project",
	"router.go:handleListWorkItems":                  "\"no accessible projects; pass project= explicitly\" — names no project",
	"router.go:handleClaimWorkItem":                  "cross-user force_takeover needs maintainer/admin; caller is already a project writer",
	"router.go:handleBootstrap":                      "bootstrap key / already-bootstrapped; pre-membership",
	"routes_artifacts.go:shareRefusal":               "memory type not shareable, or visibility narrower than the project — a property of the object, not of the caller",
	"routes_artifacts.go:checkMemoryVisibility":      "private-not-author / admin-tier. INTRA-project author tiering, not membership: the caller IS a member and is entitled to know the artifact exists. Tracked as aihub#379, which will decide 403-vs-404 for it repo-wide",
	"routes_memory.go:handleRemember":                "methodology.* requires the target wi's attempt credentials",
	"routes_memory.go:enforceMethodologyAttemptGate": "methodology.* artifact not bound to a wi, or missing attempt credentials",
	"routes_memory.go:handleReinforceMemory":         "cannot reinforce a redacted memory — object state, not caller identity",
	"routes_memory.go:handleUpdateMemory":            "cannot update a redacted memory — object state, not caller identity",
}

func TestProjectVisibilityGate_R2_ForbiddenIsNeverAVisibilityVerdict(t *testing.T) {
	fset, files := parsePackageServer(t)

	var unclassified []string
	seen := map[string]bool{}
	for path, f := range files {
		base := filepath.Base(path)
		for _, d := range f.Decls {
			fn, ok := d.(*ast.FuncDecl)
			if !ok || fn.Body == nil {
				continue
			}
			if !buildsForbidden(fn.Body) {
				continue
			}
			key := base + ":" + fn.Name.Name
			seen[key] = true
			if _, ok := r2Forbidden[key]; !ok {
				unclassified = append(unclassified, key+"  ("+
					fset.Position(fn.Pos()).String()+")")
			}
		}
	}

	sort.Strings(unclassified)
	if len(unclassified) > 0 {
		t.Errorf("these functions construct domain.ErrForbidden and are not classified in "+
			"r2Forbidden:\n\t%s\n\n"+
			"A 403 must never answer \"you are not a member of this project\" — that is "+
			"errNotVisible()'s job, and a 403 there is the leak aihub#377 closed. If the "+
			"403 is about something else (a global role, the object's own state, an attempt "+
			"credential), add an entry saying which.",
			strings.Join(unclassified, "\n\t"))
	}

	var stale []string
	for key := range r2Forbidden {
		if !seen[key] {
			stale = append(stale, key)
		}
	}
	sort.Strings(stale)
	if len(stale) > 0 {
		t.Errorf("r2Forbidden lists functions that no longer build ErrForbidden: %v — "+
			"delete these entries", stale)
	}
}

func buildsForbidden(body *ast.BlockStmt) bool {
	found := false
	ast.Inspect(body, func(n ast.Node) bool {
		sel, ok := n.(*ast.SelectorExpr)
		if !ok {
			return true
		}
		if sel.Sel.Name == "ErrForbidden" {
			found = true
		}
		return true
	})
	return found
}

// ─── the behavioural statement of the positive control ───────────────────────

// TestProjectVisibility_InsufficientRoleStillExplains states, as a running test
// next to the gate, the one refusal that must NOT be a 404.
//
// It is here rather than only in router_auth_test.go because the temptation to
// "finish the job" arrives while reading THIS file — the tables above are full of
// entries saying "answer a non-member with notVisibleMessage", and a 403 sitting
// in the middle of them looks like something left half-done.
func TestProjectVisibility_InsufficientRoleStillExplains(t *testing.T) {
	e := echo.New()
	rec := httptest.NewRecorder()
	c := e.NewContext(httptest.NewRequest(http.MethodPost, "/v1/work_items", nil), rec)

	// A member: viewer on testproject. Asked for writer.
	err := checkProjectAccess(c, viewerUser(), "testproject", "writer")

	if err == nil {
		t.Fatal("a viewer must not be authorized as a writer")
	}
	if rec.Code != http.StatusForbidden {
		t.Fatalf("a MEMBER short of the required role must get 403, got %d (body %s).\n\n"+
			"If you just changed this to 404: that hides a project from its own members, "+
			"which aihub#377's invariant explicitly does not ask for — its first clause is "+
			"that a user who IS in a project can see everything about it. It also removes "+
			"the only control that distinguishes \"every denial is now uniform\" from "+
			"\"authorization is broken and everything is denied\".", rec.Code, rec.Body)
	}
	if body := rec.Body.String(); !strings.Contains(body, "writer") ||
		!strings.Contains(body, "viewer") {
		t.Errorf("the 403 must keep naming the role held and the role needed; got %s", body)
	}
	if body := rec.Body.String(); strings.Contains(body, notVisibleMessage) {
		t.Errorf("a member's role shortfall must not be reported with the not-visible "+
			"wording — that wording exists to be indistinguishable from \"does not "+
			"exist\", and this caller can see the project; got %s", body)
	}
}

// TestProjectVisibility_NonMemberGetsTheSharedNotFound is its mirror: the same
// helper, a caller with no membership, and the answer must be the shared 404.
// Without this arm the test above passes just as well against a build where
// checkProjectAccess authorizes everyone.
func TestProjectVisibility_NonMemberGetsTheSharedNotFound(t *testing.T) {
	e := echo.New()
	rec := httptest.NewRecorder()
	c := e.NewContext(httptest.NewRequest(http.MethodPost, "/v1/work_items", nil), rec)

	stranger := viewerUser()
	stranger.ProjectRoles = map[string]string{"someotherproject": "writer"}

	if err := checkProjectAccess(c, stranger, "testproject", "viewer"); err == nil {
		t.Fatal("a non-member was authorized; the sibling test proves nothing")
	}
	if rec.Code != http.StatusNotFound {
		t.Fatalf("a non-member must get 404, got %d (body %s)", rec.Code, rec.Body)
	}
	if body := rec.Body.String(); !strings.Contains(body, notVisibleMessage) {
		t.Errorf("a non-member's denial must carry notVisibleMessage verbatim; got %s", body)
	}
	if body := rec.Body.String(); strings.Contains(body, "testproject") {
		t.Errorf("the denial must not name the project — naming it is the disclosure; got %s", body)
	}
}

// ─── R3: the scope is IN the SQL, and the caller routes through it ───────────
//
// aihub#376's path — POST /v1/memories back-filling `project` from
// `work_item_id` — is the one place that resolves a reference BEFORE it knows
// which project to authorize against, so it cannot be protected by an access
// check at all. Its protection is a scoped query, and these two arms are what
// keep that true without a database.
//
// The pairing is not invented here; it is the measured lesson of aihub#371,
// which fixed the sibling defect with no test DB reachable:
//
//	M2 (scope predicate deleted from the SQL, parameter still bound): EVERY
//	behavioural arm stayed green. Only an arm asserting the query TEXT went red.
//	M3 (caller reverted to the unscoped GetWorkItem): the whole resolver suite
//	stayed green. Only an AST arm reading the caller's body went red.
//	M5 (scope applied to slugs only, canonical ids still unscoped — the plausible
//	partial fix): reddened exactly one arm.
//
// So behavioural arms alone pass a fix that does nothing, and resolver arms
// alone pass a caller that stopped calling it. Both of the following are needed,
// and both are cheap.

// expectedResolveScopeSQL is the whole WHERE clause of
// domain.ResolveVisibleWorkItemRef, whitespace-normalised.
//
// Asserted verbatim rather than by substring, and yes that means an innocuous
// reformat fails this test. That is the trade: this predicate is the only thing
// standing between a caller and every project's <project>#<seq> namespace, and
// "somebody changed the SQL and nobody looked" is precisely the failure. A
// substring check for `project = ANY($3)` would pass M5 — the mutant that adds
// `OR $1 LIKE '%#%'` — because the substring is still there.
const expectedResolveScopeSQL = `SELECT id, project FROM work_items ` +
	`WHERE (id = $1 OR slug = $1) AND ($2 OR project = ANY($3))`

func TestProjectVisibilityGate_R3_ScopeIsInTheSQL(t *testing.T) {
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "../domain/work_items.go", nil, 0)
	if err != nil {
		t.Fatalf("parse domain/work_items.go: %v", err)
	}

	var got string
	ast.Inspect(f, func(n ast.Node) bool {
		fn, ok := n.(*ast.FuncDecl)
		if !ok || fn.Name.Name != "ResolveVisibleWorkItemRef" {
			return true
		}
		ast.Inspect(fn.Body, func(m ast.Node) bool {
			lit, ok := m.(*ast.BasicLit)
			if ok && lit.Kind == token.STRING && strings.Contains(lit.Value, "SELECT") {
				got = collapse(strings.Trim(lit.Value, "`"))
			}
			return true
		})
		return false
	})

	if got == "" {
		t.Fatal("no SELECT literal found in domain.ResolveVisibleWorkItemRef — either it " +
			"was renamed or it stopped building its query inline, and this arm is now " +
			"asserting nothing. Do not delete it; re-point it.")
	}
	if got != expectedResolveScopeSQL {
		t.Errorf("the scoped resolver's SQL changed.\n  got:  %s\n  want: %s\n\n"+
			"This query is what makes an invisible work item indistinguishable from an "+
			"absent one on the aihub#376 back-fill path, which has no access check to "+
			"fall back on. If the change is deliberate, update the constant AND re-run "+
			"the mutation that applies the scope to slugs only — a substring check would "+
			"not have caught that one.", got, expectedResolveScopeSQL)
	}
}

// TestProjectVisibilityGate_R3_BackfillRoutesThroughTheScopedResolver is the M3
// arm: every assertion above calls the resolver, so all of them stay green if
// the CALLER stops using it. Reinstating the unscoped lookup compiles, passes
// the entire suite, and reads like a cleanup.
//
// Written against the AST rather than by string matching so a rename or a
// reformat cannot silently turn it into an assertion about nothing.
func TestProjectVisibilityGate_R3_BackfillRoutesThroughTheScopedResolver(t *testing.T) {
	fset, files := parsePackageServer(t)
	f, ok := files["routes_memory.go"]
	if !ok {
		t.Fatal("routes_memory.go not parsed")
	}

	var found bool
	ast.Inspect(f, func(n ast.Node) bool {
		fn, ok := n.(*ast.FuncDecl)
		if !ok || fn.Name.Name != "handleRemember" {
			return true
		}
		found = true

		scoped := calleesIn(fn.Body, map[string]bool{"domain.ResolveVisibleWorkItemRef": true})
		unscoped := calleesIn(fn.Body, map[string]bool{"domain.GetWorkItem": true})

		if len(scoped) != 1 {
			t.Errorf("handleRemember calls domain.ResolveVisibleWorkItemRef %d times, want 1",
				len(scoped))
		}
		if len(unscoped) != 0 {
			t.Errorf("handleRemember calls domain.GetWorkItem %d times, want 0 — the "+
				"unscoped lookup is the aihub#376 defect: it answers about every project, "+
				"and this path has no access check in front of it", len(unscoped))
		}

		// M4: right arity, wrong scope source. The scope must come from
		// visibleProjects(u), not from some other slice that happens to fit.
		ast.Inspect(fn.Body, func(m ast.Node) bool {
			call, ok := m.(*ast.CallExpr)
			if !ok || calleeName(call) != "domain.ResolveVisibleWorkItemRef" {
				return true
			}
			var scopeArgOK bool
			for _, arg := range call.Args {
				if inner, ok := arg.(*ast.CallExpr); ok && calleeName(inner) == "visibleProjects" {
					scopeArgOK = true
				}
			}
			if !scopeArgOK {
				t.Errorf("%s: the resolver is not given visibleProjects(u) as its scope; "+
					"a differently-derived slice may be wider than what this caller may read",
					fset.Position(call.Pos()))
			}
			return true
		})
		return false
	})

	if !found {
		t.Fatal("handleRemember not found in routes_memory.go — this arm is asserting nothing")
	}
}

// ─── the shared non-member denial assertion, with its own teeth test ─────────

// notVisibleDenial returns "" when this response is the shared not-visible
// denial, and otherwise the reason it is not. A predicate rather than a set of
// t.Errorf calls, so that it can be pointed at deliberately-wrong responses and
// checked for still being able to reject them (see assertNotVisibleDenial).
//
// Extracted during aihub#377 because six handler tests carried this assertion in
// a form that could never fail:
//
//	if err := handleActivateMemory(nil)(c); err == nil && rec.Code != http.StatusForbidden {
//	    t.Errorf("should return 403 for non-member; ...")
//	}
//
// checkProjectAccess returns a non-nil error, so `err == nil` is false, so the
// status comparison is dead code and the whole condition can never be entered.
// Those six tests passed identically whether the endpoint answered 403, 404, 200
// or 500 — they asserted on the STATUS of nothing. They were the only coverage
// those six endpoints had, and aihub#377 changes all six.
func notVisibleDenial(err error, rec *httptest.ResponseRecorder, project string) string {
	body := rec.Body.String()
	switch {
	case err == nil:
		// The handler must both refuse AND return non-nil, or the caller's
		// `if err != nil { return err }` guard lets execution continue into the
		// database after the denial has been written (the bug fixed in
		// checkProjectAccess's doc comment).
		return "handler returned a nil error, so it did not stop on the denial"
	case rec.Code != http.StatusNotFound:
		return fmt.Sprintf("status %d, want 404 (aihub#377: a non-member gets what a "+
			"nonexistent object gets)", rec.Code)
	case !strings.Contains(body, notVisibleMessage):
		return "body does not carry notVisibleMessage verbatim, so it is distinguishable " +
			"from the missing-object response: " + body
	case project != "" && strings.Contains(body, project):
		return "body names project " + project + ", which is the disclosure: " + body
	}
	return ""
}

// assertNotVisibleDenial checks a real response against notVisibleDenial AND
// proves the predicate can still reject wrong ones.
//
// 🔴 The negative controls are not optional decoration. Replacing a vacuous
// assertion with a stricter-LOOKING one that is also vacuous is the natural
// failure mode here — the six call sites all pass through
// `checkProjectAccess`, so if this predicate ever stopped inspecting what it
// thinks it inspects, all six would go quiet together and look green. Each
// control below is a response that MUST be refused; if any is accepted, the
// positive assertion above proves nothing and the test says so.
func assertNotVisibleDenial(t *testing.T, err error, rec *httptest.ResponseRecorder, project string) {
	t.Helper()

	if why := notVisibleDenial(err, rec, project); why != "" {
		t.Errorf("a non-member must receive the shared not-visible denial: %s", why)
	}

	stopped := errors.New("denied")
	for _, bad := range []struct {
		name string
		err  error
		rec  *httptest.ResponseRecorder
	}{
		// The exact pre-aihub#377 behaviour. If this is accepted, the fix is not
		// being measured at all.
		{"403 naming the project", stopped,
			recorderWith(http.StatusForbidden, `{"code":"FORBIDDEN","message":"no access to project `+project+`"}`)},
		// 🔴 The "都返回 4xx 就算过" fallacy: right status, wrong body. Same status
		// with a different message is still an oracle.
		{"404 with a per-endpoint message", stopped,
			recorderWith(http.StatusNotFound, `{"code":"NOT_FOUND","message":"memory not found"}`)},
		// A handler that writes the right denial but returns nil, letting the
		// caller fall through into the DB.
		{"correct body but nil error", nil,
			recorderWith(http.StatusNotFound, `{"code":"NOT_FOUND","message":"`+notVisibleMessage+`"}`)},
	} {
		if why := notVisibleDenial(bad.err, bad.rec, project); why == "" {
			t.Errorf("negative control %q was ACCEPTED by notVisibleDenial — the assertion "+
				"above cannot fail and this test is decoration", bad.name)
		}
	}
}

func recorderWith(code int, body string) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	rec.Code = code
	rec.Body.WriteString(body)
	return rec
}

// ─── shared AST plumbing ─────────────────────────────────────────────────────

func parsePackageServer(t *testing.T) (*token.FileSet, map[string]*ast.File) {
	t.Helper()
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatalf("read package dir: %v", err)
	}
	fset := token.NewFileSet()
	files := map[string]*ast.File{}
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		f, err := parser.ParseFile(fset, name, nil, parser.ParseComments)
		if err != nil {
			t.Fatalf("parse %s: %v", name, err)
		}
		files[name] = f
	}
	if len(files) == 0 {
		t.Fatal("parsed no non-test files in package server — the census would be " +
			"vacuously green, which is the one result it must never report")
	}
	return fset, files
}

// calleeName lives in queryparam_gate_test.go — one helper, shared. Its doc
// comment records why it must render unqualified calls for this gate's sake and
// why that cannot affect the other one.

func calleesIn(body *ast.BlockStmt, want map[string]bool) []string {
	var out []string
	ast.Inspect(body, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		if name := calleeName(call); want[name] {
			out = append(out, name)
		}
		return true
	})
	return out
}

// authorizedObjectNames returns the identifiers whose project is passed to an
// access decider: `wi` from checkProjectAccess(c, u, wi.Project, "viewer"), and
// `project` from checkProjectAccess(c, u, project, "writer").
//
// This is R1's anchor, and it is deliberately the VALUE BEING AUTHORIZED rather
// than the function that fetched it — see the note above r1Exemptions for the
// mutation that proved a name-keyed anchor blind.
func authorizedObjectNames(body *ast.BlockStmt) map[string]bool {
	out := map[string]bool{}
	ast.Inspect(body, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		idx, isDecider := accessDeciders[calleeName(call)]
		if !isDecider || idx >= len(call.Args) {
			return true
		}
		switch a := call.Args[idx].(type) {
		case *ast.SelectorExpr: // wi.Project
			if id, ok := a.X.(*ast.Ident); ok {
				out[id.Name] = true
			}
		case *ast.Ident: // a bare `project` local
			out[a.Name] = true
		}
		return true
	})
	return out
}

// TestProjectVisibilityGate_ProjectArgIndexIsCurrent fails if an accessDeciders
// index no longer points at a project. Without it, a reordered parameter list
// leaves R1 anchored on the wrong value and reporting green forever.
func TestProjectVisibilityGate_ProjectArgIndexIsCurrent(t *testing.T) {
	fset, files := parsePackageServer(t)
	seen := map[string]bool{}
	for _, f := range files {
		ast.Inspect(f, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			name := calleeName(call)
			idx, isDecider := accessDeciders[name]
			if !isDecider {
				return true
			}
			if idx >= len(call.Args) {
				t.Errorf("%s: %s has %d args but accessDeciders wants index %d",
					fset.Position(call.Pos()), name, len(call.Args), idx)
				return true
			}
			seen[name] = true
			switch a := call.Args[idx].(type) {
			case *ast.BasicLit:
				t.Errorf("%s: %s arg %d is the literal %s, not a project — the index is stale",
					fset.Position(call.Pos()), name, idx, a.Value)
			case *ast.Ident:
				if a.Name == "u" || a.Name == "c" {
					t.Errorf("%s: %s arg %d is %q, not a project — the index is stale",
						fset.Position(call.Pos()), name, idx, a.Name)
				}
			}
			return true
		})
	}
	for name := range accessDeciders {
		if !seen[name] {
			t.Errorf("accessDeciders lists %q but nothing in the package calls it; "+
				"either it is gone or the census cannot see its call sites", name)
		}
	}
}

// authorizedObjectGuards returns the `if <err> != nil { … }` guards belonging to
// the assignments that produced the objects an access decider is asked about.
//
// "Immediately following" is the right relation: that guard IS the assignment's
// error branch. Only nil-comparing guards qualify, so a `if project == ""`
// validation after `project := c.QueryParam(...)` is left alone — that is the
// caller-names-the-project family, which performs no lookup and therefore has no
// second response to be distinguishable from.
func authorizedObjectGuards(body *ast.BlockStmt) []*ast.IfStmt {
	names := authorizedObjectNames(body)
	if len(names) == 0 {
		return nil
	}
	var out []*ast.IfStmt
	ast.Inspect(body, func(n ast.Node) bool {
		block, ok := n.(*ast.BlockStmt)
		if !ok {
			return true
		}
		for i, stmt := range block.List {
			assign, ok := stmt.(*ast.AssignStmt)
			if !ok || len(assign.Rhs) != 1 {
				continue
			}
			if _, ok := assign.Rhs[0].(*ast.CallExpr); !ok {
				continue
			}
			// Does this assignment define one of the authorized objects?
			var defines bool
			for _, lhs := range assign.Lhs {
				if id, ok := lhs.(*ast.Ident); ok && names[id.Name] {
					defines = true
				}
			}
			if !defines || i+1 >= len(block.List) {
				continue
			}
			ifStmt, ok := block.List[i+1].(*ast.IfStmt)
			if !ok || !comparesToNil(ifStmt.Cond) {
				continue
			}
			out = append(out, ifStmt)
		}
		return true
	})
	return out
}

func comparesToNil(cond ast.Expr) bool {
	found := false
	ast.Inspect(cond, func(n ast.Node) bool {
		if id, ok := n.(*ast.Ident); ok && id.Name == "nil" {
			found = true
		}
		return true
	})
	return found
}

func renderNode(fset *token.FileSet, n ast.Node) string {
	var sb strings.Builder
	if err := printer.Fprint(&sb, fset, n); err != nil {
		return ""
	}
	return sb.String()
}

func collapse(s string) string {
	return strings.Join(strings.Fields(s), " ")
}
