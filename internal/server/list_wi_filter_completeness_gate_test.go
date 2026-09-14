package server

// The RECURRENCE gate for aihub#656.
//
// aihub#656 was filed on a measured gap: domain.ListWorkItemsFilter had three
// exported fields — OwnerDisplay, ReporterDisplay, WatcherUserID — with full
// SQL support in buildListWorkItemsWhere, and ZERO entries in
// handleListWorkItems' hop-3 binding table. The raw HTTP endpoint
// (/v1/work_items) silently dropped all three query params: 200 OK, an
// unfiltered result set, no error anywhere. router_list_wi_params_test.go
// closes THOSE three sites, but three fixed sites is not a closed class —
// aihub#280's own binding table already existed when this gap was introduced,
// so "add the entry when you add the field" was already the convention and it
// was not enough. The next field added to ListWorkItemsFilter can repeat this
// exactly, and nothing would go red.
//
// So this gate is not "are those three fields wired" — it is a STRUCTURAL
// invariant, in the same spirit as queryparam_gate_test.go's class gate for
// aihub#255/#267/#340:
//
//	every exported field of domain.ListWorkItemsFilter must be a write target
//	somewhere in handleListWorkItems, or be named in the exemption map below
//	with a stated reason.
//
// Written against reflect + go/ast rather than a hand-maintained list of field
// names, for the same reason the query-param gate is AST-based: a textual
// convention ("remember to check this list") is exactly the discipline that
// already failed once here.
//
// ⚠️ What this gate does NOT prove, stated so nobody reads more into a green
// run: it proves a field is wired to SOMETHING inside handleListWorkItems, not
// that it is wired under the CORRECT query-param name or with the CORRECT
// semantics. `{"owner_display", &filter.ReporterDisplay}` (swapped dest) would
// satisfy this gate just as well as the correct wiring — both fields would
// show up in the write-set, on the WRONG binding. That half is
// router_list_wi_params_test.go's job (it asserts the specific query param
// reaches the specific field with the specific value) and
// internal/domain/work_items_list_filters_test.go's (it asserts the field
// reaches the specific SQL predicate). This gate only answers "is every field
// reachable at all", which is the shape aihub#656's actual defect had: not a
// wrong name, an ABSENT one.
import (
	"go/ast"
	"go/parser"
	"go/token"
	"reflect"
	"testing"

	"github.com/GMISWE/ieops-aihub/internal/domain"
)

// listWIFilterFieldExemptions are ListWorkItemsFilter fields deliberately
// absent from handleListWorkItems' query-param wiring, with the reason it is
// legitimate rather than an oversight.
//
// Keep this map SMALL. Every entry is a field this gate stops checking, and a
// convenient exemption is how the gate becomes decoration — the same warning
// queryparam_gate_test.go's lenientReaders carries.
var listWIFilterFieldExemptions = map[string]string{
	"AccessibleProjects": "computed from the authenticated caller's project " +
		"memberships (checkProjectAccess / u.ProjectRoles), never from a query " +
		"param — there is no `?accessible_projects=` to bind, by design.",
}

// listWorkItemsHandlerFuncName is the function this gate inspects. Named as a
// constant, not inlined, so a rename shows up as a one-line diff here instead
// of a silently-vacuous walk that finds no matching FuncDecl.
const listWorkItemsHandlerFuncName = "handleListWorkItems"

// listWorkItemsFilterLocalName is the identifier handleListWorkItems binds its
// domain.ListWorkItemsFilter value to. Selector expressions are matched on
// this name rather than on type (no type-checking pass here, matching
// queryparam_gate_test.go's own choice), which is why a rename of the local
// would silently blind this gate — the activity floor in the test below is
// what catches that.
const listWorkItemsFilterLocalName = "filter"

// findFuncDecl returns the *ast.FuncDecl named name at the top level of file,
// or nil.
func findFuncDecl(file *ast.File, name string) *ast.FuncDecl {
	for _, decl := range file.Decls {
		if fn, ok := decl.(*ast.FuncDecl); ok && fn.Name.Name == name {
			return fn
		}
	}
	return nil
}

// isFilterSelector reports whether e is `filter.<Name>` — a SelectorExpr on
// the exact local this gate tracks — and returns the field name when it is.
func isFilterSelector(e ast.Expr) (string, bool) {
	sel, ok := e.(*ast.SelectorExpr)
	if !ok {
		return "", false
	}
	id, ok := sel.X.(*ast.Ident)
	if !ok || id.Name != listWorkItemsFilterLocalName {
		return "", false
	}
	return sel.Sel.Name, true
}

// harvestFilterWrites walks body and returns every ListWorkItemsFilter field
// name reached as a write target, in either of the two idioms
// handleListWorkItems uses:
//
//   - direct assignment      filter.WIType = &wiType
//     filter.Status = statuses
//   - the table's dest idiom {"priority", &filter.Priority}, later
//     dereferenced as *p.dest = &value. The composite literal never writes
//     through `filter.Priority` directly — it takes its ADDRESS — so the
//     write target here is the operand of `&filter.X`, wherever that
//     expression occurs (a struct-literal value, same as a plain assignment's
//     RHS would be). Restricting this to AssignStmt alone is exactly the gap
//     that would leave every table-driven field invisible to the walk, which
//     is nine of the eleven fields the real handler wires this way.
func harvestFilterWrites(body ast.Node) map[string]bool {
	written := map[string]bool{}
	ast.Inspect(body, func(n ast.Node) bool {
		switch v := n.(type) {
		case *ast.AssignStmt:
			for _, lhs := range v.Lhs {
				if name, ok := isFilterSelector(lhs); ok {
					written[name] = true
				}
			}
		case *ast.UnaryExpr:
			if v.Op == token.AND {
				if name, ok := isFilterSelector(v.X); ok {
					written[name] = true
				}
			}
		}
		return true
	})
	return written
}

// exportedFieldNames returns the exported field names of a struct type via
// reflection, so the census tracks the real type rather than a second,
// hand-copied list of its own that could drift from it exactly as the wiring
// table drifted from the struct aihub#656 found.
func exportedFieldNames(t reflect.Type) []string {
	var out []string
	for i := 0; i < t.NumField(); i++ {
		f := t.Field(i)
		if f.PkgPath != "" { // unexported
			continue
		}
		out = append(out, f.Name)
	}
	return out
}

// TestEveryListWorkItemsFilterFieldIsWiredSomewhere is the recurrence gate.
//
// It reflects domain.ListWorkItemsFilter's exported field names, AST-parses
// router.go, finds handleListWorkItems, and harvests every `filter.<Field>`
// write target inside it. Every field must appear in that set or in
// listWIFilterFieldExemptions above; otherwise it is exactly the aihub#656
// shape — a field the domain layer supports and the HTTP layer never reaches.
func TestEveryListWorkItemsFilterFieldIsWiredSomewhere(t *testing.T) {
	fields := exportedFieldNames(reflect.TypeOf(domain.ListWorkItemsFilter{}))
	if len(fields) < 10 {
		// domain.ListWorkItemsFilter had 24 exported fields when this gate was
		// written. A count far below that means reflection stopped seeing the
		// real type (wrong type, package not built, …), and this gate must not
		// report green for that reason.
		t.Fatalf("reflect.TypeOf(domain.ListWorkItemsFilter{}) reports only %d exported "+
			"fields — the walk is broken, not the struct", len(fields))
	}

	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "router.go", nil, 0)
	if err != nil {
		t.Fatalf("parse router.go: %v", err)
	}
	fn := findFuncDecl(file, listWorkItemsHandlerFuncName)
	if fn == nil || fn.Body == nil {
		t.Fatalf("%s not found (or has no body) in router.go — this gate's target function "+
			"was renamed or moved, and it must be updated rather than left silently vacuous",
			listWorkItemsHandlerFuncName)
	}

	written := harvestFilterWrites(fn.Body)
	if len(written) < 10 {
		// The real handler writes to well over half of the struct's fields.
		// A number this low means listWorkItemsFilterLocalName stopped
		// matching (a rename of the `filter` local) rather than that the
		// handler genuinely wires almost nothing, so this floor is what
		// catches a walk that is quietly finding nothing rather than a
		// handler that has shrunk.
		t.Fatalf("only %d ListWorkItemsFilter fields found written in %s — the local-name "+
			"match (%q) has likely stopped finding the filter variable, and this gate is "+
			"passing vacuously rather than checking anything",
			len(written), listWorkItemsHandlerFuncName, listWorkItemsFilterLocalName)
	}

	// consultedExemptions counts only exemptions actually FALLEN BACK ON below —
	// a field absent from `written` but present in listWIFilterFieldExemptions —
	// not len(listWIFilterFieldExemptions). AccessibleProjects is already a
	// direct assignment in handleListWorkItems, so it is already in `written`
	// and its exemption-map entry is never consulted; a static map-length count
	// would report "(1 exempted)" regardless, which is misleading — it credits
	// the exemption map with explaining a gap it never had to explain.
	consultedExemptions := 0
	for _, name := range fields {
		if written[name] {
			continue
		}
		if reason, exempt := listWIFilterFieldExemptions[name]; exempt {
			consultedExemptions++
			t.Logf("domain.ListWorkItemsFilter.%s: exempt — %s", name, reason)
			continue
		}
		t.Errorf("domain.ListWorkItemsFilter.%s is never written to in %s. "+
			"This is the exact shape of aihub#656: buildListWorkItemsWhere may already support "+
			"this field in SQL while the HTTP layer silently never reaches it (200 OK, "+
			"unfiltered, no error). Either wire it in the hop-3 binding table (or a direct "+
			"assignment) or add it to listWIFilterFieldExemptions above with a stated reason.",
			name, listWorkItemsHandlerFuncName)
	}
	t.Logf("%d/%d domain.ListWorkItemsFilter fields wired in %s (%d exempted)",
		len(written), len(fields), listWorkItemsHandlerFuncName, consultedExemptions)
}
