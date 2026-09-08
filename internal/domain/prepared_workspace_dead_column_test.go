package domain

// Structural gate for aihub#487: `run_attempts.prepared_workspace` is a dead
// column, and this file is what keeps it dead.
//
// ─── Why the gate is the deliverable, and why it is GREEN on its own tree ────
//
// aihub#487 came out of aihub#416's spec Q-6, owner-ruled 2026-09-08 (choice A
// in aihub#416.attrs.owner_ruling_2026_09_08_q2_q6): follow the aihub#395 part 2
// `base_branch` precedent — withdraw the field from the published contract, keep
// the DB column.
//
// The wi body asserted the field was bound by ClaimRequest and silently dropped
// on the way in. Measured on 6cd8229, that was FALSE. The field is on
// RunAttempt, the table-mirror struct, and nothing published it: no MCP tool
// InputSchema, no contract card under docs/mcp-cards/, no docs/mcp-tools.md
// entry. The only contract document naming it is v0/openapi/aihub_openapi.yaml,
// and v0/ is archived — the same file still advertises `base_branch`, withdrawn
// a dozen wi's ago, and still lists `lease_until` as required although leases
// were deleted in v1.21. The precedent did not touch it either. So the
// precedent's one-line schema deletion had no target here and aihub#487 deleted
// nothing.
//
// That absence is precisely what needed gating. A dead surface whose only record
// of being dead is one wi's prose is what produced the wrong premise in the
// first place: the next reader re-derived it, got it backwards, and the mistake
// reached an owner ruling. This file makes the claim checkable instead of
// remembered.
//
// ⚠️ Consequence: being green on the tree that introduced it means this gate
// cannot be validated by running it here. Its discriminating power was
// established with mutants — adding the column to an `INSERT INTO run_attempts`
// column list, binding it on ClaimRequest, and deleting the mirror field each
// turn it red — plus a green control proving the prose comment that documents
// the column does not trip it. If you change the scanner, redo that.
//
// No database needed, so it runs in CI's "Unit tests" step and on a laptop:
//
//	go test ./internal/domain/ -run TestPreparedWorkspaceStaysADeadColumn -v

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const (
	// preparedWorkspaceColumn is the DDL column name (0004_run_attempts.sql).
	preparedWorkspaceColumn = "prepared_workspace"
	// preparedWorkspaceField is the Go field mirroring it.
	preparedWorkspaceField = "PreparedWorkspace"
	// preparedWorkspaceMirrorFile is the ONE non-test file allowed to name
	// either of the above: the struct that mirrors the table.
	preparedWorkspaceMirrorFile = "internal/domain/run_attempts.go"
)

// TestPreparedWorkspaceStaysADeadColumn fails when anything in non-test code
// starts writing, reading or binding `prepared_workspace`.
//
// 🔴 It asks "has this surface come back", not "does RunAttempt still compile".
// A guard written as "domain declares the field" would be green either way,
// which is the mistake the base_branch precedent's own gate calls out: what has
// to be asserted is that no caller is invited to set it and no query touches it,
// which is a property of the published and written surface and of nothing else.
//
// The three arms are deliberately different questions:
//
//   - a SQL string naming the column means something writes or reads it;
//   - a second struct tag means a request or response shape now binds it;
//   - a second identifier means a reader exists.
//
// Two of the arms double as the anti-vacuity check: the mirror field MUST still
// be found. A scanner that silently stopped matching would otherwise satisfy
// every "must be absent" assertion at once.
func TestPreparedWorkspaceStaysADeadColumn(t *testing.T) {
	root := repoRootForPolicy(t)
	files := nonTestGoFiles(t, root)
	if len(files) < 50 {
		t.Fatalf("found only %d non-test .go files under %s; this gate is not looking at the "+
			"repo it claims to police", len(files), root)
	}

	// The scan set is non-test files only, which is the sole reason this gate can
	// spell the column name at all. Asserted rather than assumed: if
	// nonTestGoFiles ever starts returning test files, every arm below would
	// match this very file and could never fail.
	for _, path := range files {
		if strings.HasSuffix(path, "_test.go") {
			t.Fatalf("nonTestGoFiles returned a test file (%s); this gate would match its own "+
				"source and could never fail", path)
		}
	}

	var tags, literals, idents []string

	for _, path := range files {
		rel, err := filepath.Rel(root, path)
		if err != nil {
			t.Fatal(err)
		}
		rel = filepath.ToSlash(rel)

		src, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read %s: %v", rel, err)
		}

		fset := token.NewFileSet()
		// A mention in prose is not a reader, so comments must not count: the mirror
		// field carries a long comment naming this column, and a gate that counted
		// comments would be red on its own documentation — where the cheapest way
		// to green it would be to delete the explanation.
		//
		// ⚠️ What EXCLUDES comments is the node-type switch below, which matches
		// only *ast.Field, *ast.BasicLit and *ast.Ident. `mode 0` is
		// belt-and-braces, NOT the mechanism. Measured 2026-09-08 (aihub#487
		// review): flipping it to parser.ParseComments leaves this gate green,
		// because ast.Walk does not reach *ast.Comment from *ast.File in the first
		// place ("don't walk n.Comments", go/ast/walk.go). So do not read a green
		// run after flipping that flag as evidence the prose exclusion still
		// works — it never depended on the flag.
		f, err := parser.ParseFile(fset, path, src, 0)
		if err != nil {
			t.Fatalf("parse %s: %v", rel, err)
		}

		tagPos := map[token.Pos]bool{}
		at := func(p token.Pos) string {
			return fmt.Sprintf("%s:%d", rel, fset.Position(p).Line)
		}

		ast.Inspect(f, func(n ast.Node) bool {
			switch x := n.(type) {
			case *ast.Field:
				if x.Tag == nil {
					return true
				}
				tagPos[x.Tag.Pos()] = true
				if strings.Contains(x.Tag.Value, preparedWorkspaceColumn) {
					tags = append(tags, at(x.Tag.Pos()))
				}
			case *ast.BasicLit:
				// A struct tag is a BasicLit too, and was classified above.
				if tagPos[x.Pos()] || x.Kind != token.STRING {
					return true
				}
				if strings.Contains(x.Value, preparedWorkspaceColumn) {
					literals = append(literals, at(x.Pos()))
				}
			case *ast.Ident:
				if x.Name == preparedWorkspaceField {
					idents = append(idents, at(x.Pos()))
				}
			}
			return true
		})
	}

	// ── Arm 1: nothing writes or reads the column ───────────────────────────
	if len(literals) > 0 {
		t.Errorf("the column name %q appears in %d string literal(s) in non-test code (%s). "+
			"On 6cd8229 there were none: both `INSERT INTO run_attempts` column lists "+
			"(FnClaimWorkItem and the force-takeover path) are explicit and omit it, no UPDATE "+
			"sets it and no SELECT reads it, and the archived v0 writer omitted it too — so no "+
			"generation of this codebase ever wrote it. A SQL string naming it means something "+
			"now writes or reads a column "+
			"aihub#487 recorded as dead. Either revive it on purpose — and retire this gate in "+
			"the same diff, so the record moves with the behaviour — or drop the reference.",
			preparedWorkspaceColumn, len(literals), strings.Join(literals, ", "))
	}

	// ── Arm 2: exactly one struct binds it, and it is the table mirror ──────
	switch {
	case len(tags) == 0:
		t.Fatalf("no struct field in non-test code carries a %q json tag. This arm is the "+
			"anti-vacuity check — it proves the scanner can still SEE the field it polices. "+
			"Either RunAttempt.%s was deleted, which is a legitimate cleanup but then this "+
			"whole gate should go in the same diff rather than stand green against nothing, "+
			"or the scanner stopped matching. (aihub#487)",
			preparedWorkspaceColumn, preparedWorkspaceField)
	case len(tags) == 1 && !strings.HasPrefix(tags[0], preparedWorkspaceMirrorFile+":"):
		// Nothing was revived; the mirror MOVED. Kept as its own case so the
		// message cannot explain the ">1" case while reporting one field — a
		// plain file rename lands here, and run_attempts.go is over 1400 lines,
		// so a future split is a plausible refactor rather than a defect.
		t.Errorf("the only %q binding is at %s, but this gate expects the run_attempts table "+
			"mirror in %s. That is a MOVE, not a revival: update "+
			"preparedWorkspaceMirrorFile to the new path. (aihub#487)",
			preparedWorkspaceColumn, tags[0], preparedWorkspaceMirrorFile)
	case len(tags) > 1:
		t.Errorf("%q is bound by %d struct field(s) (%s); exactly one is allowed, the "+
			"run_attempts table mirror in %s. A second binding is a request or response shape "+
			"publishing a column nothing writes — the aihub#395 part 2 defect class, where a "+
			"caller was invited to set a field that no hop honoured. (aihub#487)",
			preparedWorkspaceColumn, len(tags), strings.Join(tags, ", "),
			preparedWorkspaceMirrorFile)
	}

	// ── Arm 3: exactly one identifier, the mirror field's own declaration ───
	switch {
	case len(idents) == 0:
		t.Fatalf("the identifier %s does not occur in non-test code at all; same anti-vacuity "+
			"reasoning as the arm above. (aihub#487)", preparedWorkspaceField)
	case len(idents) == 1 && !strings.HasPrefix(idents[0], preparedWorkspaceMirrorFile+":"):
		// Same MOVE-not-revival case as the arm above.
		t.Errorf("the only declaration of %s is at %s, but this gate expects it in %s. That is "+
			"a MOVE, not a new reader: update preparedWorkspaceMirrorFile to the new path. "+
			"(aihub#487)",
			preparedWorkspaceField, idents[0], preparedWorkspaceMirrorFile)
	case len(idents) > 1:
		t.Errorf("the identifier %s occurs %d time(s) in non-test code (%s); exactly one is "+
			"allowed, its own declaration in %s. Any further occurrence is a READER of a column "+
			"no code path writes, so whatever it computes is a constant dressed as data. "+
			"(aihub#487)",
			preparedWorkspaceField, len(idents), strings.Join(idents, ", "),
			preparedWorkspaceMirrorFile)
	}
}
