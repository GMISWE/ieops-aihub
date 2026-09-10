package domain

// aihub#543 probe wave 2, lane L5 — `docs/mcp-cards/pf_save_artifact.md`'s hop
// 4, the REASON `structured_payload` has no stored-data exemption:
//
//	"Unlike `pf_remember`'s `attrs`, this field has no stored-data exemption,
//	 because no path in the repo feeds a stored `structured_payload` back into a
//	 write — `UpdateMemory` carries the whole merged `attrs` object instead and
//	 leaves this field unset"
//	    -> TestNoStoredStructuredPayloadIsFedBackIntoAWrite
//
// ─── What was already held, and what was not ───────────────────────────────
//
// TestRememberAttrsProvenanceSplit holds the CONSEQUENCE — attrsFromStoredRow
// exempts `attrs` only, and a structured_payload on the same request stays
// checked — and TestResolveUpdateMemoryAttrsReportsProvenance holds that
// UpdateMemory sets that flag. Neither can see the premise the card gives for
// the asymmetry, which is a NEGATIVE about the whole repo: that no path re-feeds
// a stored structured_payload. That premise is what makes the missing exemption
// safe rather than an oversight, and it is the sentence somebody would delete
// the guard on the strength of.
//
// No database: it reads the tree and the struct.
//
//	go test ./internal/domain/ -run TestNoStoredStructuredPayload -count=1 -v

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestNoStoredStructuredPayloadIsFedBackIntoAWrite is the card's premise as a
// census over the tree.
//
// Two halves, and the first is what makes the second complete: the update path
// cannot carry this field because UpdateMemoryRequest does not declare it, and
// no other path writes it because no Go statement anywhere sets it. Only the
// request binding does, from bytes a caller sent — which is exactly the
// provenance validateRememberJSONParams assumes when it refuses without an
// exemption.
//
// MUTANTS (applied to this tree; the verdict is what ran):
//
//	M40 enforcement: add `StructuredPayload json.RawMessage` to
//	    UpdateMemoryRequest                     RED  the declared-field arm
//	M41 enforcement: add `rr.StructuredPayload = head.StructuredPayload` to
//	    UpdateMemory                            RED  the no-writer arm, naming the
//	                                                 function — which is the event
//	                                                 the card's "because" rules out
//	M42 enforcement: break the WALK — make goFieldWriteSites skip every .go file
//	                                            RED  the Attrs control, which is
//	                                                 what proves the walk can see a
//	                                                 field write at all
//	M43 publication: delete every citation from the card sentence this arm helps
//	    retire                                  RED  K12 — the sentence lands back
//	                                                 in the debt column and the
//	                                                 pf_save_artifact ledger row
//	                                                 stops matching
//
//	── recorded GREEN ──
//	M42a enforcement: change only the `field` constant to a name nothing writes
//	                                            GREEN the control is hard-coded to
//	                                                  Attrs, deliberately: it is a
//	                                                  claim about the WALK and not
//	                                                  about the field under test, so
//	                                                  it does not move with it
//	M43a publication: delete only THIS arm's citation, leaving
//	     TestRememberAttrsProvenanceSplit named in the same sentence
//	                                            GREEN the sentence stays cited
func TestNoStoredStructuredPayloadIsFedBackIntoAWrite(t *testing.T) {
	const field = "StructuredPayload"

	// Half one: the update path has nowhere to put it. Read off the type rather
	// than off the source, so a field added through an embedded struct is caught
	// too.
	updateFields := map[string]bool{}
	ty := reflect.TypeOf(UpdateMemoryRequest{})
	for i := 0; i < ty.NumField(); i++ {
		updateFields[ty.Field(i).Name] = true
	}
	require.NotEmpty(t, updateFields,
		"UpdateMemoryRequest declares no fields at all; the absence below would then be a fact "+
			"about an empty struct")
	require.Contains(t, updateFields, "Attrs",
		"UpdateMemoryRequest no longer declares Attrs, so the card's contrast — this path carries "+
			"the whole merged attrs object INSTEAD — has lost the thing it contrasts with")
	require.NotContains(t, updateFields, field,
		"UpdateMemoryRequest now declares %s. The card says this path leaves the field unset and "+
			"that no stored value is ever re-fed into a write; with the field declared, a caller's "+
			"edit can carry one back and validateRememberJSONParams will refuse it with no "+
			"exemption to fall back on — the pf_update_memory 400 that attrsFromStoredRow exists "+
			"to prevent, arriving on the other field.", field)

	// Half two: nothing in the tree assigns it. The control is Attrs, which is
	// written in exactly the place the card names.
	writes := goFieldWriteSites(t, "..", field)
	controls := goFieldWriteSites(t, "..", "Attrs")

	require.NotEmpty(t, controls,
		"the census found no write to `Attrs` anywhere under internal/, which this tree certainly "+
			"has (UpdateMemory assigns it from resolveUpdateMemoryAttrs) — the WALK is what broke, "+
			"and its silence about %s would mean nothing", field)

	sort.Strings(writes)
	require.Empty(t, writes,
		"these sites write `%s` in Go: %v\nThe card's hop 4 says no path in the repo feeds a "+
			"stored structured_payload back into a write, and that premise is why the field has no "+
			"attrsFromStoredRow-style exemption. If one of these carries a value read out of the "+
			"column, the exemption is now needed and the sentence is now false — fix whichever is "+
			"wrong, in the same change.", field, writes)
}

// goFieldWriteSites returns the "<file>/<func>" of every Go statement under root
// that writes the named struct field — as a composite-literal key or as the
// left-hand side of an assignment.
//
// go/parser rather than a scan of the bytes, because this field's name appears
// in several long comments in memory.go (including the one the card quotes) and
// a text scan would report every one of them as a writer.
func goFieldWriteSites(t *testing.T, root, field string) []string {
	t.Helper()
	var out []string
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			switch d.Name() {
			case ".git", "node_modules", "vendor", "testdata":
				return fs.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(d.Name(), ".go") || strings.HasSuffix(d.Name(), "_test.go") {
			return nil
		}
		fset := token.NewFileSet()
		f, perr := parser.ParseFile(fset, path, nil, parser.SkipObjectResolution)
		if perr != nil {
			return perr
		}
		rel, relErr := filepath.Rel(root, path)
		if relErr != nil {
			rel = path
		}
		rel = filepath.ToSlash(rel)

		record := func(where string) {
			for _, existing := range out {
				if existing == where {
					return
				}
			}
			out = append(out, where)
		}

		var inFunc string
		ast.Inspect(f, func(n ast.Node) bool {
			switch node := n.(type) {
			case *ast.FuncDecl:
				inFunc = node.Name.Name
			case *ast.KeyValueExpr:
				if id, ok := node.Key.(*ast.Ident); ok && id.Name == field {
					record(rel + "/" + inFunc)
				}
			case *ast.AssignStmt:
				for _, lhs := range node.Lhs {
					sel, ok := lhs.(*ast.SelectorExpr)
					if ok && sel.Sel.Name == field {
						record(rel + "/" + inFunc)
					}
				}
			}
			return true
		})
		return nil
	})
	require.NoError(t, err, "walking %s for writes to %s", root, field)
	return out
}
