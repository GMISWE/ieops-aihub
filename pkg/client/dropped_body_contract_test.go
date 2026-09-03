package client

// aihub#324 — the standing gate on the defect class this package shipped:
// a method that ACCEPTS a request body and then sends none.
//
// The instance was RemoveDependency. Its signature was `body any`; it picked
// three path segments out of the map and called do() with a nil body, and do()
// only marshals a non-nil one. Everything else the caller had put in that map
// went nowhere — which is how internal/mcp/tools_dependency.go came to build
// attempt_id / claim_epoch / session_secret for a request that could not carry
// them. At the call site a dropped field is indistinguishable from one that
// arrived, so nothing anywhere went red; the defect was found by reading the
// hop, not by a test.
//
// Fixing that one function does not close the class, because the next method
// written to the same shape reintroduces it silently. This test is the closure:
// it parses this package's own source and fails on the SHAPE, so a new instance
// is caught the moment it is written rather than the next time somebody reads
// carefully.
//
// It is a source-level test on purpose. A behavioural test would need a caller
// that puts an observable field in the body of every affected method, i.e. it
// could only ever cover the methods somebody remembered to enumerate — the same
// blind spot that let the original through.
//
// Run: GOWORK=off go test ./pkg/client/ -run TestNoMethodAcceptsABodyItNeverSends -v

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"strings"
	"testing"
)

// bodyParamNames are the parameter names that mean "the caller's request body".
// A method taking one of these is promising to send it.
//
// ⚠️ Two limits worth knowing before trusting a green run here. This is a
// NAME-based check, so a parameter called `reqBody`, `in` or `arg` slips past it
// — extend the set rather than assume it is covered. And the walk is scoped to
// this package, which is correct only because do() lives here; a second HTTP
// client elsewhere in the repo would need its own copy.
var bodyParamNames = map[string]bool{"body": true, "req": true, "payload": true}

// TestNoMethodAcceptsABodyItNeverSends fails when a *Client method takes a body
// parameter and every one of its do()/doRaw() calls passes a nil body.
//
// The check is deliberately narrow in one direction and broad in the other: it
// does not care WHICH body a method sends (some rebuild one from their
// arguments), only that a method advertising a body parameter puts something on
// the wire. A method that legitimately has no body must not ask for one — say so
// in its signature, as RemoveDependency now does with three plain strings.
func TestNoMethodAcceptsABodyItNeverSends(t *testing.T) {
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatalf("read ./pkg/client: %v", err)
	}
	fset := token.NewFileSet()
	checked, parsed := 0, 0
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		file, perr := parser.ParseFile(fset, name, nil, 0)
		if perr != nil {
			t.Fatalf("parse %s: %v", name, perr)
		}
		parsed++
		for _, decl := range file.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Recv == nil || fn.Body == nil {
				continue
			}
			bodyParam := bodyParamOf(fn)
			if bodyParam == "" {
				continue
			}
			checked++
			sends, sawSender := sendsABody(fn)
			if !sawSender {
				// Delegates to another method rather than calling do() itself;
				// that method is checked on its own terms.
				continue
			}
			if !sends {
				t.Errorf("%s:%d: %s takes a %q parameter but every request it sends has a nil body, "+
					"so everything the caller puts in %q is silently discarded.\n"+
					"This is aihub#324's defect: RemoveDependency did exactly this, and the "+
					"attempt credentials internal/mcp built for it never reached the wire while "+
					"both sides looked correct in isolation.\n"+
					"Either send the body, or stop asking for one — take the values the request "+
					"actually uses as ordinary typed parameters.",
					name, fset.Position(fn.Pos()).Line, fn.Name.Name, bodyParam, bodyParam)
			}
		}
	}
	if parsed == 0 {
		t.Fatalf("no non-test .go file was parsed in ./pkg/client — the walk is broken")
	}

	// A structural test that matched nothing would pass forever. This package
	// has always had body-taking methods; zero means the AST walk broke.
	if checked == 0 {
		t.Fatalf("no *Client method with a body parameter was examined — the walk is broken, " +
			"and this test is reporting success without having looked at anything")
	}
	t.Logf("examined %d *Client method(s) that accept a request body", checked)
}

// bodyParamOf returns the name of fn's request-body parameter, or "".
func bodyParamOf(fn *ast.FuncDecl) string {
	if fn.Type.Params == nil {
		return ""
	}
	for _, field := range fn.Type.Params.List {
		for _, name := range field.Names {
			if bodyParamNames[name.Name] {
				return name.Name
			}
		}
	}
	return ""
}

// sendsABody reports whether fn makes at least one do()/doRaw() call whose body
// argument is not the literal nil, and whether it makes any such call at all.
func sendsABody(fn *ast.FuncDecl) (sends, sawSender bool) {
	ast.Inspect(fn.Body, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok {
			return true
		}
		switch sel.Sel.Name {
		case "do":
			// do(ctx, method, path, body, out)
			if len(call.Args) < 4 {
				return true
			}
			sawSender = true
			if id, isIdent := call.Args[3].(*ast.Ident); !isIdent || id.Name != "nil" {
				sends = true
			}
		case "doRaw":
			// doRaw carries no body by construction, so a method that takes one
			// and only calls doRaw is discarding it.
			sawSender = true
		}
		return true
	})
	return sends, sawSender
}
