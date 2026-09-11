package mcp

// aihub#591 built this file's original arm; aihub#598 repurposed it.
//
// The #591 arm (TestJSONResultAndCompactAreByteIdentical) held pf_get_memory's
// corrected hop-5 line on its live half: jsonResult and jsonResultCompact
// produced BYTE-IDENTICAL text, because 34df071 moved marshalJSON from
// json.MarshalIndent to json.Marshal for every tool in the server. aihub#598
// then folded jsonResultCompact into jsonResult — the identity was the proof the
// second function was dead weight — and with it the byte-identity arm lost its
// subject: there is no pair left to compare.
//
// The card sentence moved with it. pf_get_memory's hop 5 now says the server
// has exactly ONE JSON result serializer, and that is a census claim, so the
// arm below is a census: every function in this package's production source
// that both marshals JSON and constructs a CallToolResult must be jsonResult
// itself, and there must be exactly one. A re-added jsonResultCompact — or any
// new sibling serializer, whatever its name — is red, which is strictly more
// than the old arm held (a re-added byte-identical twin kept the old arm green
// while splitting the marshal point back into two sites of truth).
//
// Calibrated like internal/server's replica census (aihub#597): the detector
// must first see a planted second serializer in a fixture, so its silence over
// the real tree cannot be blindness.
//
// Mutants (verified red at aihub#598 time):
//
//	MC1  re-add jsonResultCompact verbatim to recall_slim.go   RED (SECOND_SERIALIZER)
//	MC2  blind the detector (require MarshalIndent only)       RED (DETECTOR_BLIND)
//	MC3  rename jsonResult everywhere but here                 RED (CANONICAL_NOT_FOUND)
//	MP1  restore the card's pre-fold two-function paragraph    RED (the contract-cards
//	     gate: it cites this file's retired arm name and a symbol recall_slim.go
//	     no longer declares)
//
//	GOWORK=off go test ./internal/mcp/ -run TestJSONResultIsTheOnlyJSONResultSerializer -count=1

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"strings"
	"testing"
)

// jsonSerializerFuncsIn returns the names of every function declared in file
// that BOTH marshals JSON — a call to json.Marshal / json.MarshalIndent or to
// the package's own marshalJSON — AND constructs an sdkmcp.CallToolResult
// composite literal. That pair is what "JSON result serializer" means here:
// errResult builds a result without marshalling, handlers marshal nothing
// themselves, and only a serializer does both.
func jsonSerializerFuncsIn(file *ast.File) []string {
	var out []string
	for _, decl := range file.Decls {
		fd, ok := decl.(*ast.FuncDecl)
		if !ok || fd.Body == nil {
			continue
		}
		marshals, buildsResult := false, false
		ast.Inspect(fd.Body, func(n ast.Node) bool {
			switch v := n.(type) {
			case *ast.CallExpr:
				switch f := v.Fun.(type) {
				case *ast.Ident:
					if f.Name == "marshalJSON" {
						marshals = true
					}
				case *ast.SelectorExpr:
					if pkg, ok := f.X.(*ast.Ident); ok && pkg.Name == "json" &&
						(f.Sel.Name == "Marshal" || f.Sel.Name == "MarshalIndent") {
						marshals = true
					}
				}
			case *ast.CompositeLit:
				if sel, ok := v.Type.(*ast.SelectorExpr); ok && sel.Sel.Name == "CallToolResult" {
					buildsResult = true
				}
			}
			return true
		})
		if marshals && buildsResult {
			out = append(out, fd.Name.Name)
		}
	}
	return out
}

// plantedSerializer is the calibration fixture: a second JSON result serializer
// as it would look re-created in this package — jsonResultCompact's exact shape.
const plantedSerializer = `package mcp

import (
	"encoding/json"

	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"
)

func jsonResultPlanted(v any) (*sdkmcp.CallToolResult, error) {
	b, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}
	return &sdkmcp.CallToolResult{
		Content: []sdkmcp.Content{&sdkmcp.TextContent{Text: string(b)}},
	}, nil
}
`

// TestJSONResultIsTheOnlyJSONResultSerializer holds pf_get_memory's post-fold
// hop-5 sentence: exactly one function turns a value into a JSON tool result.
func TestJSONResultIsTheOnlyJSONResultSerializer(t *testing.T) {
	fset := token.NewFileSet()

	// Calibration first: a detector that cannot see the planted serializer
	// returns the same answer over the real tree as a healthy one, and this
	// arm would green on blindness forever.
	planted, err := parser.ParseFile(fset, "planted_fixture.go", plantedSerializer, parser.SkipObjectResolution)
	if err != nil {
		t.Fatalf("parse calibration fixture: %v", err)
	}
	if got := jsonSerializerFuncsIn(planted); len(got) != 1 || got[0] != "jsonResultPlanted" {
		t.Fatalf("DETECTOR_BLIND: the census detector reported %v over a fixture that declares "+
			"one verbatim second serializer (jsonResultPlanted). Its answer over the real tree "+
			"therefore means nothing — fix the detector before trusting this arm.", got)
	}

	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatalf("read package dir: %v", err)
	}
	var serializers []string
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		file, perr := parser.ParseFile(fset, name, nil, parser.SkipObjectResolution)
		if perr != nil {
			t.Fatalf("parse %s: %v", name, perr)
		}
		for _, fn := range jsonSerializerFuncsIn(file) {
			serializers = append(serializers, name+":"+fn)
		}
	}

	// Anti-vacuity and the census in one assertion: the canonical serializer
	// must be found (a walk that sees nothing is a broken walk, and a renamed
	// jsonResult must point this arm at the new name in the same diff), and
	// nothing else may qualify.
	if len(serializers) != 1 || !strings.HasSuffix(serializers[0], ":jsonResult") {
		if len(serializers) == 0 {
			t.Fatalf("CANONICAL_NOT_FOUND: no function in this package both marshals JSON and " +
				"builds a CallToolResult, so either jsonResult was renamed (point this arm at " +
				"the new name in the same diff) or the walk saw nothing — both are red, because " +
				"a census over no serializers is the same green as a correct fold.")
		}
		t.Errorf("SECOND_SERIALIZER: found %d JSON result serializer(s) %v, want exactly one "+
			"(jsonResult). aihub#598 folded jsonResultCompact into jsonResult after aihub#592 "+
			"measured them byte-identical since 34df071; a second serializer — whatever its "+
			"name, and even if byte-identical today — splits the server's single marshal point "+
			"back into two sites of truth, which is the drift the fold deleted. "+
			"pf_get_memory's hop 5 says there is exactly one; make that sentence true again.",
			len(serializers), serializers)
	}
}
