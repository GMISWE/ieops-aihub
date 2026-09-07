package mcp_test

// aihub#387 — the pf_get_ready_queue parameter contract, stated as a MECHANISM.
//
// `non_conflicting` was published in this tool's InputSchema and forwarded onto
// the wire as `?non_conflicting=true` from the day the tool was added
// (`50bfc35`), and NOTHING on the server side has ever read it:
// `handleGetReadyQueue` reads `project` and `max` only, and
// `domain.GetReadyQueue(ctx, pool, project, max)` has no such argument.
// Measured on 4a0e6e0:
//
//	git grep -n 'non_conflicting|NonConflicting' -- internal/server internal/domain pkg cmd
//	  -> exit 1, 0 hits
//
// Passing `non_conflicting=true` therefore returned the ordinary ready queue,
// with no error and no warning at any hop. The cost was not a missing feature:
// aihub#186's design — an orchestrator fanning out non-conflicting wi's — was
// written ON TOP of that switch, so a schema that lied misled the PLANNING of
// work that had not been written yet. The owner's decision (2026-09-07) is plan
// B: withdraw the parameter from the schema rather than implement it, because
// "non-conflicting" has no agreed definition here (predicted from
// declared_resources, or from the locks actually held?) and this repo has
// measured `pf_predict_conflicts` to be untrustworthy in BOTH directions, so an
// implementation would have produced a second untrustworthy predicate.
//
// ─── Why the gate has to reach hop 3, and what that rules out ───────────────
//
// A pf_get_ready_queue parameter has to survive three hops to do anything:
//
//	hop 1  published MCP InputSchema      the pf_get_ready_queue AddTool call
//	                                     in registerLifecycleTools
//	hop 2  MCP args -> HTTP query string  the same handler's url.Values
//	hop 3  query param -> filter/SQL      handleGetReadyQueue (internal/server)
//
// **Hop 2 was never the defect.** The forwarding was present and correct, so a
// recording-server assertion on the query string — the shape
// recall_wire_query_test.go uses, and the shape this file's second test uses —
// was and would remain GREEN with the bug in place. The only assertion with
// discriminating power here compares hop 1 against hop 3, which is why the
// census below reads the server package's AST rather than replaying a request:
// what has to be measured is whether the handler READS the name, and a request
// against the real handler cannot tell "read and had no effect" from "never
// read at all" (that is precisely why the live probe in aihub#385 §7.3 found the
// two response bodies byte-identical).
//
// ─── Mechanism, not a name ─────────────────────────────────────────────────
//
// Nothing here mentions `non_conflicting`. The invariant is quantified over the
// published schema, so a parameter added tomorrow is covered the day it is
// added, and the exemption maps make "published but deliberately not read"
// something that has to be written down with a reason rather than tolerated by
// omission.
//
// No database needed:
//
//	go test ./internal/mcp/ -run TestReadyQueue -v

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"
)

const (
	readyQueueTool    = "pf_get_ready_queue"
	readyQueueHandler = "handleGetReadyQueue"
	// The server package is read as SOURCE, not imported: what is being measured
	// is which parameter names appear in the handler's reads, and that is a
	// property of the code, not of any value it returns.
	serverPkgDir = "../server"
)

// readyQueueParamsNotReadByTheHandler exempts a param that pf_get_ready_queue
// publishes and the server handler legitimately never reads — one this process
// consumes itself before the request goes out (the `fields`-style projection
// aihub#313 added to pf_recall is the canonical example).
//
// Each entry needs a reason, because "published and not read" is otherwise
// indistinguishable from the defect this file exists to catch. It is EMPTY
// today, and that is the correct state: every parameter this tool publishes is
// one the server acts on.
var readyQueueParamsNotReadByTheHandler = map[string]string{}

// readyQueueParamsReadButNotPublished is the reverse drift: a query parameter
// the handler honours that no MCP caller can discover or reach. Harmless only
// when deliberate, so it is written down rather than left to be found.
var readyQueueParamsReadButNotPublished = map[string]string{}

// echoRequestReaderMethods are the method names that hand back caller-supplied
// request text. Kept identical in spirit to internal/server's own
// queryparam_gate_test.go, including the deliberately unqualified `Get`, which
// covers `c.Request().URL.Query().Get("x")`.
//
// Over-collecting here would be the dangerous direction — an unrelated
// `.Get("project")` inside the handler could mask a genuinely unread param — so
// note what makes that acceptable: the census is scoped to ONE function body,
// and a coincidence has to occur inside it. Under-collecting is the safe
// direction: a reader the census does not know makes this test RED on correct
// code, which gets the census extended rather than the defect shipped.
var echoRequestReaderMethods = map[string]bool{
	"QueryParam":  true,
	"QueryParams": true,
	"FormValue":   true,
	"FormParams":  true,
	"Get":         true,
}

// ─── hop 3: which query parameters does the handler actually read? ──────────

// serverASTFiles parses every non-test source file of package server.
func serverASTFiles(t *testing.T) (*token.FileSet, map[string]*ast.File) {
	t.Helper()
	entries, err := os.ReadDir(serverPkgDir)
	if err != nil {
		t.Fatalf("read %s: %v", serverPkgDir, err)
	}
	fset := token.NewFileSet()
	files := map[string]*ast.File{}
	for _, e := range entries {
		n := e.Name()
		if e.IsDir() || !strings.HasSuffix(n, ".go") || strings.HasSuffix(n, "_test.go") {
			continue
		}
		path := filepath.Join(serverPkgDir, n)
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
			len(files), serverPkgDir)
	}
	return fset, files
}

// firstStringLit returns the first DIRECT argument that is a string literal.
// It deliberately does not descend into composite literals, so
// `queryEnumCSV(c, "status", []string{"queued","wrapped"})` yields "status" and
// not one of the allowed values.
func firstStringLit(args []ast.Expr) (string, bool) {
	for _, a := range args {
		lit, ok := a.(*ast.BasicLit)
		if !ok || lit.Kind != token.STRING {
			continue
		}
		if s, err := strconv.Unquote(lit.Value); err == nil {
			return s, true
		}
	}
	return "", false
}

// readerCallParam reports the parameter name a request-reading call names.
func readerCallParam(call *ast.CallExpr, localReaders map[string]bool) (string, bool) {
	switch fn := call.Fun.(type) {
	case *ast.Ident:
		if localReaders[fn.Name] {
			return firstStringLit(call.Args)
		}
	case *ast.SelectorExpr:
		if echoRequestReaderMethods[fn.Sel.Name] {
			return firstStringLit(call.Args)
		}
	}
	return "", false
}

// bodyReadsRequest reports whether a function body reaches any known reader.
func bodyReadsRequest(body ast.Node, localReaders map[string]bool) bool {
	found := false
	ast.Inspect(body, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		switch fn := call.Fun.(type) {
		case *ast.Ident:
			if localReaders[fn.Name] {
				found = true
			}
		case *ast.SelectorExpr:
			if echoRequestReaderMethods[fn.Sel.Name] {
				found = true
			}
		}
		return true
	})
	return found
}

// takesContextAndParamName reports whether fd has the shape of a parameter
// reader: `func f(c echo.Context, name string, …)`.
func takesContextAndParamName(fd *ast.FuncDecl) bool {
	if fd.Recv != nil || fd.Type.Params == nil || len(fd.Type.Params.List) < 2 {
		return false
	}
	sel, ok := fd.Type.Params.List[0].Type.(*ast.SelectorExpr)
	if !ok || sel.Sel.Name != "Context" {
		return false
	}
	pkg, ok := sel.X.(*ast.Ident)
	if !ok || pkg.Name != "echo" {
		return false
	}
	id, ok := fd.Type.Params.List[1].Type.(*ast.Ident)
	return ok && id.Name == "string"
}

// requestReaderFuncs DERIVES package server's own parameter readers instead of
// listing them: any `func f(c echo.Context, name string, …)` whose body itself
// reaches a reader is one. Iterated to a fixpoint so a wrapper around a wrapper
// is found too.
//
// Derived rather than hard-coded because a hard-coded list rots in the
// false-RED direction — a new reader in queryparam.go would make the census
// miss real reads and fail this test on correct code. `queryInt`, `queryBool`,
// `queryCSV`, `trimmedParam` and the /ui lenient readers all match this shape.
func requestReaderFuncs(t *testing.T, files map[string]*ast.File) map[string]bool {
	t.Helper()
	readers := map[string]bool{}
	for round := 0; round < 8; round++ {
		added := 0
		for _, f := range files {
			for _, d := range f.Decls {
				fd, ok := d.(*ast.FuncDecl)
				if !ok || fd.Body == nil || readers[fd.Name.Name] {
					continue
				}
				if !takesContextAndParamName(fd) || !bodyReadsRequest(fd.Body, readers) {
					continue
				}
				readers[fd.Name.Name] = true
				added++
			}
		}
		if added == 0 {
			break
		}
	}
	if len(readers) == 0 {
		t.Fatal("derived no query-parameter readers at all in package server — the derivation is " +
			"broken, and every census built on it would be silently empty")
	}
	return readers
}

// handlerQueryParams is the hop-3 census: the parameter names
// handleGetReadyQueue reads.
//
// The handler is found by NAME across the whole package rather than at a pinned
// file path, so moving it between files does not turn this gate off.
//
// Limit, stated rather than left to be discovered: the walk is intra-function
// (it does cover nested function literals, which matters — the handler is a
// closure returned by a constructor). A parameter read inside a NEW helper of
// your own would be missed, which fails this test on correct code rather than
// passing a defect; extend `requestReaderFuncs` when that happens.
func handlerQueryParams(t *testing.T) map[string]bool {
	t.Helper()
	fset, files := serverASTFiles(t)
	readers := requestReaderFuncs(t, files)

	var body *ast.BlockStmt
	var where string
	for _, f := range files {
		for _, d := range f.Decls {
			fd, ok := d.(*ast.FuncDecl)
			if !ok || fd.Recv != nil || fd.Name.Name != readyQueueHandler || fd.Body == nil {
				continue
			}
			if body != nil {
				// Two declarations cannot both be the handler, and censusing the
				// wrong one silently would be worse than stopping here.
				t.Fatalf("found %s declared twice (%s and %s) — the census is ambiguous",
					readyQueueHandler, where, fset.Position(fd.Pos()))
			}
			body, where = fd.Body, fset.Position(fd.Pos()).String()
		}
	}
	if body == nil {
		t.Fatalf("no func %s found anywhere in %s — it was renamed or removed, and this gate cannot "+
			"measure hop 3 without it", readyQueueHandler, serverPkgDir)
	}

	params := map[string]bool{}
	ast.Inspect(body, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		if name, ok := readerCallParam(call, readers); ok {
			params[name] = true
		}
		return true
	})
	if len(params) == 0 {
		t.Fatalf("%s (%s) reads no query parameter at all according to the census — that is the "+
			"census being broken, not the handler: the endpoint has a required parameter",
			readyQueueHandler, where)
	}
	t.Logf("%s (%s) reads %v; derived readers: %v", readyQueueHandler, where,
		sortedKeys(params), sortedKeys(readers))
	return params
}

func sortedKeys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// TestReadyQueueEveryPublishedParamIsReadByTheHandler is THE gate.
//
// It FAILS on the pre-fix tree, naming `non_conflicting`. That is the point: it
// is the regression test for a contract that shipped a lie, not a description of
// code that already worked.
func TestReadyQueueEveryPublishedParamIsReadByTheHandler(t *testing.T) {
	published := publishedSchemaProps(t, readyQueueTool)
	read := handlerQueryParams(t)

	for name := range published {
		if reason, exempt := readyQueueParamsNotReadByTheHandler[name]; exempt {
			// The exemption has to be TRUE, not merely claimed: a param that is in
			// fact read must not sit behind a "consumed locally" note.
			if read[name] {
				t.Errorf("%q is documented as consumed in-process (%s) but %s DOES read it — "+
					"stale exemption", name, reason, readyQueueHandler)
			}
			continue
		}
		if !read[name] {
			t.Errorf("%s publishes %q and %s never reads it.\n"+
				"The schema states a contract no hop keeps: the caller's argument is accepted, "+
				"forwarded, and then ignored — no error, no warning, and a response identical to "+
				"the one they would have got without it. This is aihub#387's exact signature, and "+
				"its cost was not a missing feature but a DESIGN (aihub#186) written on top of a "+
				"switch that did nothing. Either make %s honour it, or withdraw it from the "+
				"InputSchema; if it is genuinely consumed in this process, add it to "+
				"readyQueueParamsNotReadByTheHandler with the reason.",
				readyQueueTool, name, readyQueueHandler, readyQueueHandler)
		}
	}

	// A stale exemption would silently excuse a real drop, so every entry must
	// name something that is still published.
	for name, reason := range readyQueueParamsNotReadByTheHandler {
		if _, ok := published[name]; !ok {
			t.Errorf("readyQueueParamsNotReadByTheHandler exempts %q (%s), which %s no longer "+
				"publishes — stale exemption", name, reason, readyQueueTool)
		}
	}
}

// TestReadyQueueHandlerReadsNothingUnpublished closes the reverse direction: a
// query parameter the server honours but the tool does not publish is reachable
// by nobody through MCP, because the SDK drops undeclared arguments silently.
func TestReadyQueueHandlerReadsNothingUnpublished(t *testing.T) {
	published := publishedSchemaProps(t, readyQueueTool)
	read := handlerQueryParams(t)

	for name := range read {
		if reason, known := readyQueueParamsReadButNotPublished[name]; known {
			if _, ok := published[name]; ok {
				t.Errorf("%q is now published; drop it from readyQueueParamsReadButNotPublished "+
					"(recorded reason: %s)", name, reason)
			}
			continue
		}
		if _, ok := published[name]; !ok {
			t.Errorf("%s reads query parameter %q that %s does not publish — no MCP caller can "+
				"reach it, since the SDK drops undeclared arguments with no error. Publish it, or "+
				"record it in readyQueueParamsReadButNotPublished with the reason it stays hidden.",
				readyQueueHandler, name, readyQueueTool)
		}
	}
}

// ─── hop 2, for completeness — and a note on what it cannot prove ───────────

// readyQueueProbeFor returns a JSON shape a real caller might send for a param
// of the given declared type. Derived from the published type so a new param is
// probed the day it is added.
func readyQueueProbeFor(t *testing.T, name, declaredType string) any {
	t.Helper()
	switch declaredType {
	case "string":
		return "probe-" + name
	case "boolean":
		return true
	case "number", "integer":
		return float64(7)
	case "array":
		return []any{"probe"}
	case "object":
		return map[string]any{"probe": "probe"}
	}
	t.Fatalf("%s publishes %q with declared type %q, which this probe table does not cover — "+
		"the hop-2 census would silently skip it", readyQueueTool, name, declaredType)
	return nil
}

// TestReadyQueueEveryPublishedParamReachesTheWire asserts hop 2 for every
// published param, through the REAL registered tool and the REAL pkg/client,
// observed as the query string the server received.
//
// ⚠️ This test is GREEN on the pre-fix tree, `non_conflicting` and all — the
// forwarding was never the broken hop. It is here because hop 2 had no
// assertion for this tool at all and a parameter can equally well die there
// (aihub#148's shape), and it is documented as green-on-the-defect so nobody
// reads a passing run of it as evidence that the contract is honest.
func TestReadyQueueEveryPublishedParamReachesTheWire(t *testing.T) {
	published := publishedSchemaProps(t, readyQueueTool)
	for name, declaredType := range published {
		if reason, exempt := readyQueueParamsNotReadByTheHandler[name]; exempt {
			t.Logf("%s: consumed in-process, not expected on the wire — %s", name, reason)
			continue
		}
		t.Run(name, func(t *testing.T) {
			q := newQueryRecorder(t)
			args := map[string]any{"project": "aihub"}
			args[name] = readyQueueProbeFor(t, name, declaredType)
			callToolAgainstRecorder(t, q, readyQueueTool, args)
			if got := q.last(t); !got.Has(name) {
				t.Errorf("%s publishes %q and the handler puts nothing on the wire for it "+
					"(%#v -> query %v). The argument vanishes between the model and the server "+
					"with no error at any hop — aihub#148's signature.",
					readyQueueTool, name, args[name], got)
			}
		})
	}
}
