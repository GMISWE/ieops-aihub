package mcp_test

// aihub#394 — the pf_claim_work_item parameter contract, stated as a MECHANISM.
//
// `mode` was published as `fresh|resume (default: fresh)`, forwarded onto the
// wire, and bound by domain.ClaimRequest — every hop present. What it did NOT
// have is an effect. Measured on 1ec3bdc, every read of the bound field:
//
//	internal/domain/run_attempts.go:388  if req.Mode == "" { req.Mode = "fresh" }
//	internal/domain/run_attempts.go:705  "is_resume":   req.Mode == "resume"
//	internal/domain/run_attempts.go:806  "is_resume":     req.Mode == "resume"
//
// One self-default and two audit fields. The wi_step_state upsert is byte-identical
// for both values (it resets current_step_status='idle', current_step_attempt=NULL,
// step_started_at=NULL and keeps current_step, whatever `mode` says), so "resume"
// restored exactly what "fresh" restored — while pf-work Mode C promised the caller
// that mode="resume" "Restores: prepared workspace + step state from the previous
// attempt". The owner's decision (2026-09-07) is plan B, as in aihub#387: withdraw
// the parameter rather than invent a semantics for it. Step state lives in
// wi_step_state keyed by work item and every re-claim already sees it, so the
// promise is true WITHOUT the parameter.
//
// ─── Why "is it bound?" and "does it reach the wire?" both stay GREEN here ──
//
// A claim parameter has to survive four hops to do anything:
//
//	hop 1  published MCP InputSchema     the pf_claim_work_item AddTool call
//	hop 2  MCP args -> HTTP body         the same handler's body map
//	hop 3  JSON body -> ClaimRequest     echo's c.Bind in handleClaimWorkItem
//	hop 4  ClaimRequest -> what happens  FnClaimWorkItem
//
// `mode` passed hops 1, 2 and 3. tools_step_contract_test.go's shape (published
// name ⊆ names the request struct binds) was GREEN on the defect, and so was a
// recording-server assertion on the body. **Only hop 4 discriminates**, and it
// does not discriminate on "is the field mentioned" either — `req.Mode` IS
// mentioned, three times. What has to be measured is whether a mention can
// change anything, which is why the census below classifies each read by the
// syntactic position it sits in rather than counting reads.
//
// ─── The classification, and why these two exclusions ──────────────────────
//
//	self_default   the read is the condition of an `if` whose body does nothing
//	               but assign to that same field. Its only possible effect is to
//	               replace an absent value with a default — it cannot make the
//	               claim behave differently, because after it runs the field
//	               holds a value the caller did not choose.
//
//	recorded       the read is a value inside a composite literal. The parameter
//	               is being written down, not acted on: an audit/event payload
//	               that reports the argument back to whoever sent it is not the
//	               argument doing something. `"is_resume": req.Mode == "resume"`
//	               is exactly this, and it is why a DIFFERENTIAL test against a
//	               real database would ALSO have been green — the two claims do
//	               differ, in one event field that echoes the input.
//
// Everything else counts. Note which direction each exclusion can fail in:
// over-excluding makes this test RED on correct code (a parameter genuinely
// consumed inside a composite literal), which gets the classification extended
// with a reason; under-excluding would let the next inert parameter through.
// The safe direction is the one that fails loudly, and that is the one chosen.
//
// ─── Mechanism, not a name ─────────────────────────────────────────────────
//
// Nothing here mentions `mode`. The invariant is quantified over whatever the
// tool publishes, so a parameter added tomorrow is covered the day it is added,
// and "published but deliberately not acted on" is something that has to be
// written down with a reason rather than tolerated by omission.
//
// No database needed:
//
//	go test ./internal/mcp/ -run TestClaim.*Param -v

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/GMISWE/ieops-aihub/internal/domain"
)

const (
	claimTool     = "pf_claim_work_item"
	claimDomainFn = "FnClaimWorkItem"
	// The domain package is read as SOURCE, not imported: what is being measured
	// is the syntactic position of each read, and that is a property of the code
	// rather than of any value it returns.
	domainPkgDir = "../domain"
)

// claimParamsNotInTheBody exempts a published parameter that is not a
// ClaimRequest field at all, because this process spends it somewhere else on
// the way out. Each entry needs a reason, and the reason is checked below: an
// exemption naming a field the struct DOES bind is stale and fails.
var claimParamsNotInTheBody = map[string]string{
	"work_item_id": "goes in the URL path (POST /v1/work_items/:id/claim), not in the JSON body",
}

// claimParamsNotActedOn exempts a published parameter whose ClaimRequest field
// the claim path legitimately never acts on. EMPTY today, and that is the
// correct state: after aihub#394 every parameter this tool publishes changes
// what the claim does.
var claimParamsNotActedOn = map[string]string{}

// claimFieldsDeliberatelyUnpublished covers the reverse drift: a ClaimRequest
// field the claim path honours that no MCP caller can set. Harmless only when
// deliberate — both of today's entries are values THIS process derives and
// sends on the caller's behalf — so they are written down rather than left to
// be discovered.
var claimFieldsDeliberatelyUnpublished = map[string]string{
	"session_info":  "minted by the MCP handler itself: machine_id from the environment and a session_secret generated here and persisted to the state file. Publishing it would let a caller forge another machine's credential.",
	"task_branches": "derived by claimTaskBranches from the worktrees this claim is about to create (aihub#356). The caller cannot know the branch names; the whole point is that the client reports what it will check out. Honoured in EffectiveDeclaredResource rather than in FnClaimWorkItem, which is why the intra-function census below does not see it and the staleness check for this map is the package-wide one.",
}

// ─── hop 4: how does the claim path read each ClaimRequest field? ───────────

// readKind classifies one read of a ClaimRequest field.
type readKind string

const (
	readSteer       readKind = "acted on"
	readSelfDefault readKind = "self-default"
	readRecorded    readKind = "recorded in a composite literal"
	readWrite       readKind = "assigned to"
)

// domainASTFiles parses every non-test source file of package domain.
func domainASTFiles(t *testing.T) (*token.FileSet, map[string]*ast.File) {
	t.Helper()
	entries, err := os.ReadDir(domainPkgDir)
	if err != nil {
		t.Fatalf("read %s: %v", domainPkgDir, err)
	}
	fset := token.NewFileSet()
	files := map[string]*ast.File{}
	for _, e := range entries {
		n := e.Name()
		if e.IsDir() || !strings.HasSuffix(n, ".go") || strings.HasSuffix(n, "_test.go") {
			continue
		}
		path := filepath.Join(domainPkgDir, n)
		f, err := parser.ParseFile(fset, path, nil, 0)
		if err != nil {
			t.Fatalf("parse %s: %v", path, err)
		}
		files[n] = f
	}
	if len(files) < 10 {
		// A path mistake would make every assertion below vacuous, and a vacuous
		// gate reports green forever. 10 is far below the real count and far above
		// zero, so this fails on a broken walk and not on a refactor.
		t.Fatalf("only found %d source files in %s — the walk is broken, not the package",
			len(files), domainPkgDir)
	}
	return fset, files
}

// claimRequestParamName returns FnClaimWorkItem's body and the name of its
// *ClaimRequest parameter, both derived rather than hard-coded so a rename of
// either does not silently empty the census.
func claimRequestParamName(t *testing.T) (*ast.BlockStmt, string, string) {
	t.Helper()
	fset, files := domainASTFiles(t)

	var body *ast.BlockStmt
	var reqName, where string
	for _, f := range files {
		for _, d := range f.Decls {
			fd, ok := d.(*ast.FuncDecl)
			if !ok || fd.Recv != nil || fd.Name.Name != claimDomainFn || fd.Body == nil {
				continue
			}
			if body != nil {
				t.Fatalf("found %s declared twice (%s and %s) — the census is ambiguous",
					claimDomainFn, where, fset.Position(fd.Pos()))
			}
			body, where = fd.Body, fset.Position(fd.Pos()).String()
			for _, p := range fd.Type.Params.List {
				star, ok := p.Type.(*ast.StarExpr)
				if !ok {
					continue
				}
				id, ok := star.X.(*ast.Ident)
				if !ok || id.Name != "ClaimRequest" {
					continue
				}
				if len(p.Names) != 1 {
					t.Fatalf("%s takes *ClaimRequest under %d names — the census expects one",
						claimDomainFn, len(p.Names))
				}
				reqName = p.Names[0].Name
			}
		}
	}
	if body == nil {
		t.Fatalf("no func %s found anywhere in %s — it was renamed or removed, and this gate "+
			"cannot measure hop 4 without it", claimDomainFn, domainPkgDir)
	}
	if reqName == "" {
		t.Fatalf("%s (%s) takes no *ClaimRequest parameter — the census has nothing to follow",
			claimDomainFn, where)
	}
	return body, reqName, where
}

// containsPos reports whether n spans the given position.
func containsPos(n ast.Node, p token.Pos) bool {
	return n != nil && n.Pos() <= p && p < n.End()
}

// classifyRead decides what one read of req.<field> can do, from the syntactic
// positions enclosing it. ancestors is innermost-last, EXCLUDING the read.
func classifyRead(sel *ast.SelectorExpr, ancestors []ast.Node, reqName, field string) readKind {
	for i := len(ancestors) - 1; i >= 0; i-- {
		switch node := ancestors[i].(type) {
		case *ast.AssignStmt:
			// Being on the left of `=` is a write, not a read of the caller's value.
			for _, lhs := range node.Lhs {
				if containsPos(lhs, sel.Pos()) {
					return readWrite
				}
			}
		case *ast.CompositeLit:
			return readRecorded
		case *ast.IfStmt:
			if !containsPos(node.Cond, sel.Pos()) {
				continue
			}
			if ifBodyOnlyAssignsTo(node.Body, reqName, field) {
				return readSelfDefault
			}
		}
	}
	return readSteer
}

// ifBodyOnlyAssignsTo reports whether every statement of an if-body assigns to
// req.<field> and nothing else — the shape of a defaulting guard.
//
// A body that also returns, logs, or touches any other variable is NOT a
// self-default: the read then gates something, which is exactly what a
// parameter is supposed to be able to do.
func ifBodyOnlyAssignsTo(body *ast.BlockStmt, reqName, field string) bool {
	if body == nil || len(body.List) == 0 {
		return false
	}
	for _, stmt := range body.List {
		as, ok := stmt.(*ast.AssignStmt)
		if !ok || len(as.Lhs) != 1 {
			return false
		}
		sel, ok := as.Lhs[0].(*ast.SelectorExpr)
		if !ok || sel.Sel.Name != field {
			return false
		}
		id, ok := sel.X.(*ast.Ident)
		if !ok || id.Name != reqName {
			return false
		}
	}
	return true
}

// claimFieldReads is the hop-4 census: for every ClaimRequest field, every read
// inside FnClaimWorkItem and what that read can do.
//
// Limit, stated rather than left to be discovered: the walk is intra-function
// (it does descend into nested function literals). A field acted on inside a
// helper called from here would be missed, which fails this test on correct
// code rather than passing a defect — the same trade
// ready_queue_param_wiring_test.go documents for its own census.
func claimFieldReads(t *testing.T) map[string]map[readKind]int {
	t.Helper()
	body, reqName, where := claimRequestParamName(t)

	reads := map[string]map[readKind]int{}
	var stack []ast.Node
	ast.Inspect(body, func(n ast.Node) bool {
		if n == nil {
			stack = stack[:len(stack)-1]
			return true
		}
		if sel, ok := n.(*ast.SelectorExpr); ok {
			if id, ok := sel.X.(*ast.Ident); ok && id.Name == reqName {
				field := sel.Sel.Name
				kind := classifyRead(sel, stack, reqName, field)
				if reads[field] == nil {
					reads[field] = map[readKind]int{}
				}
				reads[field][kind]++
			}
		}
		stack = append(stack, n)
		return true
	})

	if len(reads) == 0 {
		t.Fatalf("%s (%s) reads no field of its ClaimRequest at all according to the census — "+
			"that is the census being broken, not the function", claimDomainFn, where)
	}
	// Liveness. A classification that collapsed everything into one bucket would
	// make one of the two assertions below vacuous while still reporting a
	// plausible-looking census, so require that BOTH buckets are populated: the
	// claim path demonstrably acts on some fields, and this tree demonstrably
	// contains at least one read that is not an action.
	steered := 0
	nonSteer := 0
	for _, kinds := range reads {
		if kinds[readSteer] > 0 {
			steered++
		}
		nonSteer += kinds[readSelfDefault] + kinds[readRecorded] + kinds[readWrite]
	}
	if steered < 3 {
		t.Fatalf("the census classifies only %d ClaimRequest field(s) as acted on (%v) — a claim "+
			"acts on more than that, so the classifier is broken and every assertion built on it "+
			"would be vacuous", steered, formatReads(reads))
	}
	if nonSteer == 0 {
		t.Fatalf("the census found no self-default, recorded or assigned read anywhere in %s (%v) — "+
			"the classifier's exclusions never fire, so it cannot tell an inert parameter from an "+
			"active one", claimDomainFn, formatReads(reads))
	}
	t.Logf("%s (%s) reads: %v", claimDomainFn, where, formatReads(reads))
	return reads
}

func formatReads(reads map[string]map[readKind]int) []string {
	out := make([]string, 0, len(reads))
	for field, kinds := range reads {
		parts := make([]string, 0, len(kinds))
		for k, n := range kinds {
			parts = append(parts, string(k)+"×"+itoa(n))
		}
		sort.Strings(parts)
		out = append(out, field+"{"+strings.Join(parts, ",")+"}")
	}
	sort.Strings(out)
	return out
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b []byte
	for n > 0 {
		b = append([]byte{byte('0' + n%10)}, b...)
		n /= 10
	}
	return string(b)
}

// claimRequestFields maps each ClaimRequest json name to its Go field name,
// taken from the struct itself so a json-tag rename cannot desync this gate.
func claimRequestFields(t *testing.T) map[string]string {
	t.Helper()
	typ := reflect.TypeOf(domain.ClaimRequest{})
	out := map[string]string{}
	for i := 0; i < typ.NumField(); i++ {
		f := typ.Field(i)
		tag := f.Tag.Get("json")
		if tag == "-" {
			continue
		}
		name, _, _ := strings.Cut(tag, ",")
		if name == "" {
			name = f.Name
		}
		out[name] = f.Name
	}
	if len(out) == 0 {
		t.Fatal("domain.ClaimRequest has no fields — the reflection is broken")
	}
	return out
}

// TestClaimEveryPublishedParamIsActedOnByTheClaimPath is THE gate.
//
// It FAILS on the pre-fix tree, naming `mode`. That is the point: it is the
// regression test for a contract that shipped a promise nothing kept, not a
// description of code that already worked.
func TestClaimEveryPublishedParamIsActedOnByTheClaimPath(t *testing.T) {
	published := publishedSchemaProps(t, claimTool)
	fields := claimRequestFields(t)
	reads := claimFieldReads(t)

	for name := range published {
		if reason, exempt := claimParamsNotInTheBody[name]; exempt {
			// The exemption has to be TRUE, not merely claimed.
			if field, bound := fields[name]; bound {
				t.Errorf("%q is documented as not travelling in the body (%s) but "+
					"domain.ClaimRequest binds it as %s — stale exemption", name, reason, field)
			}
			continue
		}
		field, bound := fields[name]
		if !bound {
			t.Errorf("%s publishes %q and domain.ClaimRequest has no field binding it — echo's "+
				"Bind drops it in silence, so the caller is promised a parameter the server never "+
				"sees. Either bind and honour it, or withdraw it from the InputSchema; if this "+
				"process consumes it before the request goes out, record it in "+
				"claimParamsNotInTheBody with the reason.", claimTool, name)
			continue
		}
		if reason, exempt := claimParamsNotActedOn[name]; exempt {
			if reads[field][readSteer] > 0 {
				t.Errorf("%q is documented as not acted on (%s) but %s DOES act on %s — stale "+
					"exemption", name, reason, claimDomainFn, field)
			}
			continue
		}
		if reads[field][readSteer] == 0 {
			t.Errorf("%s publishes %q, %s binds it as %s, and %s never ACTS on it — every read is "+
				"%v.\n"+
				"The schema states a switch that selects nothing: the argument is accepted, "+
				"forwarded, bound, and then the only difference it makes is to an audit field that "+
				"reports the value back to the caller who sent it. A response is otherwise "+
				"identical to the one they would have got without it, and no hop says so. This is "+
				"aihub#394's exact signature, and note what it defeats — 'is it bound?' and 'does "+
				"it reach the wire?' are both GREEN here, and so is a differential test against a "+
				"real database, because the audit field really does change.\n"+
				"Either make %s act on it, or withdraw it from the InputSchema; if it is "+
				"deliberately inert, add it to claimParamsNotActedOn with the reason.",
				claimTool, name, claimDomainFn, field, claimDomainFn,
				formatReads(map[string]map[readKind]int{field: reads[field]}),
				claimDomainFn)
		}
	}

	// Stale exemptions would silently excuse a real defect, so every entry must
	// name something that is still published.
	for name, reason := range claimParamsNotInTheBody {
		if _, ok := published[name]; !ok {
			t.Errorf("claimParamsNotInTheBody exempts %q (%s), which %s no longer publishes — "+
				"stale exemption", name, reason, claimTool)
		}
	}
	for name, reason := range claimParamsNotActedOn {
		if _, ok := published[name]; !ok {
			t.Errorf("claimParamsNotActedOn exempts %q (%s), which %s no longer publishes — "+
				"stale exemption", name, reason, claimTool)
		}
	}
}

// TestClaimPathActsOnNothingUnpublished closes the reverse direction: a
// ClaimRequest field the claim path acts on that the tool does not publish is
// unreachable by any MCP caller, because the SDK drops undeclared arguments
// with no error.
func TestClaimPathActsOnNothingUnpublished(t *testing.T) {
	published := publishedSchemaProps(t, claimTool)
	fields := claimRequestFields(t)
	reads := claimFieldReads(t)

	byField := map[string]string{}
	for jsonName, field := range fields {
		byField[field] = jsonName
	}

	for field, kinds := range reads {
		if kinds[readSteer] == 0 {
			continue
		}
		jsonName, isRequestField := byField[field]
		if !isRequestField {
			continue
		}
		if reason, known := claimFieldsDeliberatelyUnpublished[jsonName]; known {
			if _, ok := published[jsonName]; ok {
				t.Errorf("%q is now published; drop it from claimFieldsDeliberatelyUnpublished "+
					"(recorded reason: %s)", jsonName, reason)
			}
			continue
		}
		if _, ok := published[jsonName]; !ok {
			t.Errorf("%s acts on ClaimRequest.%s (json %q) that %s does not publish — no MCP "+
				"caller can reach it, since the SDK drops undeclared arguments with no error. "+
				"Publish it, or record it in claimFieldsDeliberatelyUnpublished with the reason "+
				"it stays hidden.", claimDomainFn, field, jsonName, claimTool)
		}
	}

	// Staleness, checked against the PACKAGE rather than against the census
	// above. Deliberately the weaker of the two measurements, for a reason worth
	// stating: task_branches is honoured in EffectiveDeclaredResource, not in
	// FnClaimWorkItem, so the intra-function census legitimately shows no read of
	// it and requiring one here would fail on correct code. What this still
	// catches is the failure that matters — an exemption outliving the field, or
	// outliving every reader of it, which would leave a request field that is
	// both unreachable and inert while a comment claims it is load-bearing.
	readAnywhere := fieldsReadInDomainPackage(t)
	for jsonName, reason := range claimFieldsDeliberatelyUnpublished {
		field, ok := fields[jsonName]
		if !ok {
			t.Errorf("claimFieldsDeliberatelyUnpublished names %q (%s), which domain.ClaimRequest "+
				"no longer binds — stale exemption", jsonName, reason)
			continue
		}
		if !readAnywhere[field] {
			t.Errorf("claimFieldsDeliberatelyUnpublished names %q (%s), but nothing in package "+
				"domain reads .%s any more — the field is now inert AND unreachable, which is "+
				"worse than either: drop the field, or drop the exemption.", jsonName, reason, field)
		}
	}
}

// fieldsReadInDomainPackage censuses every selector `.<Name>` in package domain.
//
// ⚠️ Weaker than claimFieldReads by construction: it does not know the receiver's
// type, so a same-named field on another struct would satisfy it. That is
// acceptable for the one thing it is used for — proving an exemption is not
// stale — and it must NOT be used to answer "is this parameter honoured", which
// is the question claimFieldReads exists for.
func fieldsReadInDomainPackage(t *testing.T) map[string]bool {
	t.Helper()
	_, files := domainASTFiles(t)
	out := map[string]bool{}
	for _, f := range files {
		ast.Inspect(f, func(n ast.Node) bool {
			if sel, ok := n.(*ast.SelectorExpr); ok {
				out[sel.Sel.Name] = true
			}
			return true
		})
	}
	if len(out) == 0 {
		t.Fatal("no selectors found anywhere in package domain — the census is broken")
	}
	return out
}

// TestClaimEveryPublishedParamReachesTheWire asserts hop 2 for every published
// param, through the REAL registered tool, observed as the JSON body the server
// received.
//
// ⚠️ This test is GREEN on the pre-fix tree, `mode` and all — the forwarding was
// never the broken hop. It is here because hop 2 can equally well be where a
// parameter dies (aihub#148's shape), and it is documented as green-on-the-defect
// so nobody reads a passing run of it as evidence that the contract is honest.
func TestClaimEveryPublishedParamReachesTheWire(t *testing.T) {
	published := publishedSchemaProps(t, claimTool)
	for name, declaredType := range published {
		if _, exempt := claimParamsNotInTheBody[name]; exempt {
			continue
		}
		t.Run(name, func(t *testing.T) {
			// newClaimWorkspace repoints POLYFORGE_WORKSPACE_ROOT at a temp dir. A
			// claim WRITES a state file, and without that the write lands in the
			// live workspace's credential directory.
			newClaimWorkspace(t)
			const wiID = "wi_01JCLAIMPARAMPROBE"

			f := newFakeAihub(t)
			args := map[string]any{
				"work_item_id":    wiID,
				"idempotency_key": "idem-" + name,
			}
			args[name] = claimProbeFor(t, name, declaredType)
			_, _ = callTool(t, f, claimTool, args)
			body := lastBodyFor(t, f, "/v1/work_items/"+wiID+"/claim")
			if _, ok := body[name]; !ok {
				t.Errorf("%s publishes %q and the handler puts nothing in the claim body for it "+
					"(sent %#v, body carried %v). The argument vanishes between the model and the "+
					"server with no error at any hop — aihub#148's signature.",
					claimTool, name, args[name], sortedBodyKeys(body))
			}
		})
	}
}

// claimProbeFor returns a JSON shape a real caller might send for a param of
// the given declared type, derived from the published type so a new param is
// probed the day it is added.
func claimProbeFor(t *testing.T, name, declaredType string) any {
	t.Helper()
	switch declaredType {
	case "string":
		return "probe-" + name
	case "boolean":
		return true
	case "number", "integer":
		return float64(7)
	case "array":
		return []any{map[string]any{"resource_type": "file_scope", "resource_key": "probe"}}
	case "object":
		return map[string]any{"probe": "probe"}
	}
	t.Fatalf("%s publishes %q with declared type %q, which this probe table does not cover — "+
		"the hop-2 census would silently skip it", claimTool, name, declaredType)
	return nil
}

// lastBodyFor returns the JSON body of the last request the MCP server made to
// the given path, failing if it never called it — "the tool never reached the
// server" must not read as "the parameter was not forwarded".
func lastBodyFor(t *testing.T, f *fakeAihub, path string) map[string]any {
	t.Helper()
	var body map[string]any
	found := false
	for _, c := range f.recorded() {
		if c.Path == path {
			body, found = c.Body, true
		}
	}
	if !found {
		t.Fatalf("the MCP handler never POSTed to %s at all (paths: %v) — this census cannot "+
			"tell a dropped parameter from a call that never happened", path, f.paths())
	}
	return body
}

func sortedBodyKeys(body map[string]any) []string {
	out := make([]string, 0, len(body))
	for k := range body {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
