package server

// aihub#597 — the stability-formula dedup guard.
//
// Before aihub#597, handleReinforceMemory carried an inline replica of
// domain.ComputeStabilityDays + baseStabilityForType: the three base values
// (7/180/36500) lived in two packages and were kept equal purely by hand,
// because the domain helper was unexported. aihub#597 exported the helper and
// deleted the replica, so after the dedup there is exactly ONE site that knows
// the formula. These two arms are the "drift again and it reds" guard the wi
// asked for:
//
//	Arm A  handleReinforceMemory obtains stability from the domain helper —
//	       it must CALL domain.ComputeStabilityDays, must carry no 180/36500
//	       base literal of its own, and no comment in the handler may restore
//	       the claim aihub#597 falsified ("we replicate it inline because the
//	       helper is unexported").
//	Arm B  no second replica exists ANYWHERE outside internal/domain — a
//	       repo-wide census over production .go files for the replica's
//	       fingerprint constant, calibrated against a planted replica so a
//	       blind detector cannot pass vacuously.
//
// 🔴 Why the fingerprint is 36500 (with 180 added inside the handler) and not
// the full 7/180/36500 trio: 7 collides with unrelated small literals all over
// a codebase, and a replica of baseStabilityForType that lacks its rule./
// methodology. arm is not a replica of it — 36500 is the constant no faithful
// copy can omit. Measured on the tree at aihub#597 time: the only production
// occurrences of 36500 outside internal/domain were the replica being deleted;
// test files pin it as fixture data, which is what tests are FOR, so _test.go
// is excluded from the census and internal/domain (the canonical home) is the
// one exempt directory.
//
// Mutants (verified red at aihub#597 time):
//
//	MA1  re-inline the replica in the handler (restore the switch, drop the
//	     domain call)                              RED (REPLICA_LITERAL,
//	                                                    MISSING_CALL — and MB
//	                                                    reds on 36500 too)
//	MA2  restore the old comment claim above the call, code unchanged
//	                                               RED (STALE_CLAIM)
//	MA3  rename handleReinforceMemory              RED (HANDLER_NOT_FOUND)
//	MB1  plant a second replica in a non-domain production file
//	                                               RED (SECOND_REPLICA)
//	MB2  blind the detector (look for 99999 instead of 36500)
//	                                               RED (DETECTOR_BLIND)
//
// No database: both arms read source.

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// immortalStability is the census fingerprint: the base stability_days for
// rule.*/methodology.* types. See the file header for why this constant, alone,
// identifies a replica.
const immortalStability = 36500

// stabilityLiteralValue reports whether a basic literal's numeric value equals
// v. It parses INT and FLOAT literals numerically rather than by string, so
// 36500, 36500.0, 36_500 and 3.65e4 all match — a replica does not stop being
// one by changing spelling.
func stabilityLiteralValue(lit *ast.BasicLit, v float64) bool {
	if lit.Kind != token.INT && lit.Kind != token.FLOAT {
		return false
	}
	f, err := strconv.ParseFloat(strings.ReplaceAll(lit.Value, "_", ""), 64)
	return err == nil && f == v
}

// stabilityHitsIn returns the positions of every basic literal in file whose
// value equals immortalStability.
func stabilityHitsIn(fset *token.FileSet, file *ast.File) []string {
	var hits []string
	ast.Inspect(file, func(n ast.Node) bool {
		if lit, ok := n.(*ast.BasicLit); ok && stabilityLiteralValue(lit, immortalStability) {
			hits = append(hits, fset.Position(lit.Pos()).String())
		}
		return true
	})
	return hits
}

// TestReinforceStabilityIsComputedByDomainNotInline is Arm A: the reinforce
// handler's stability value comes from the ONE site that knows the formula.
func TestReinforceStabilityIsComputedByDomainNotInline(t *testing.T) {
	const src = "routes_memory.go"
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, src, nil, parser.ParseComments)
	require.NoError(t, err, "parse %s", src)

	var fn *ast.FuncDecl
	ast.Inspect(file, func(n ast.Node) bool {
		d, ok := n.(*ast.FuncDecl)
		if ok && d.Name.Name == "handleReinforceMemory" {
			fn = d
			return false
		}
		return true
	})
	require.NotNil(t, fn,
		"HANDLER_NOT_FOUND: %s no longer declares handleReinforceMemory, so this arm walked "+
			"nothing. A rename is legitimate — point this arm at the new name in the same "+
			"diff — but a walk over no function is the same green as a correct dedup.", src)

	// The handler must CALL domain.ComputeStabilityDays. Presence of the call is
	// what makes "no literal" below mean "delegates to domain" rather than
	// "computes stability some third way".
	calls := 0
	ast.Inspect(fn.Body, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok || sel.Sel.Name != "ComputeStabilityDays" {
			return true
		}
		if pkg, ok := sel.X.(*ast.Ident); ok && pkg.Name == "domain" {
			calls++
		}
		return true
	})
	if calls == 0 {
		t.Errorf("MISSING_CALL: handleReinforceMemory never calls domain.ComputeStabilityDays. "+
			"aihub#597 made domain the one site that knows the stability formula; a handler "+
			"that stopped calling it is either replicating the formula again or writing a "+
			"stability the formula did not produce. (%s)", src)
	}

	// No stability base literal of its own. 180 is included here (unlike the
	// repo census) because inside THIS handler there is no legitimate use of
	// either constant — the fact./immortal bases belong to domain.
	ast.Inspect(fn.Body, func(n ast.Node) bool {
		lit, ok := n.(*ast.BasicLit)
		if !ok {
			return true
		}
		if stabilityLiteralValue(lit, 180) || stabilityLiteralValue(lit, immortalStability) {
			t.Errorf("REPLICA_LITERAL: handleReinforceMemory carries the stability base "+
				"literal %s at %s. That is the inline replica aihub#597 deleted growing back: "+
				"the three base values live in domain.ComputeStabilityDays and nowhere else.",
				lit.Value, fset.Position(lit.Pos()))
		}
		return true
	})

	// Publication: the claim aihub#597 falsified must not return. The old
	// comment said the replica exists "since the helper is unexported" — the
	// helper is exported now, and a comment restoring that sentence re-licenses
	// the replica to the next reader even before any code moves.
	for _, cg := range file.Comments {
		if cg.Pos() < fn.Pos() || cg.End() > fn.End() {
			continue
		}
		text := strings.ToLower(cg.Text())
		for _, stale := range []string{"replicate it inline", "helper is unexported"} {
			if strings.Contains(text, stale) {
				t.Errorf("STALE_CLAIM: a comment inside handleReinforceMemory says %q — the "+
					"claim aihub#597 falsified. The helper IS exported (domain."+
					"ComputeStabilityDays) and the handler calls it; a comment that says "+
					"otherwise is the two-sites era publishing itself back into the code. (%s)",
					stale, fset.Position(cg.Pos()))
			}
		}
	}
}

// plantedReplica is the calibration fixture for Arm B: a faithful inline
// replica of baseStabilityForType, as it would look re-created in some
// non-domain package. The detector must see THIS before its silence over the
// real tree is allowed to mean anything.
const plantedReplica = `package planted

import "strings"

func stabilityBase(memType string) float64 {
	base := 7.0
	switch {
	case strings.HasPrefix(memType, "fact."):
		base = 180.0
	case strings.HasPrefix(memType, "rule."), strings.HasPrefix(memType, "methodology."):
		base = 36500.0
	}
	return base
}
`

// TestNoSecondStabilityReplicaExistsOutsideDomain is Arm B: after aihub#597
// there is exactly one site that knows the stability formula, so a census over
// every production .go file outside internal/domain must find zero occurrences
// of the fingerprint constant.
func TestNoSecondStabilityReplicaExistsOutsideDomain(t *testing.T) {
	fset := token.NewFileSet()

	// Calibration first: a detector that cannot see a planted replica returns
	// the same empty list over the real tree as a healthy one, and the arm
	// would pass forever on blindness.
	planted, err := parser.ParseFile(fset, "planted_fixture.go", plantedReplica, 0)
	require.NoError(t, err, "parse calibration fixture")
	if len(stabilityHitsIn(fset, planted)) == 0 {
		t.Fatalf("DETECTOR_BLIND: the census detector found no stability fingerprint in the "+
			"planted replica fixture, which contains %d verbatim. Its silence over the real "+
			"tree therefore means nothing — fix the detector before trusting this arm.",
			immortalStability)
	}

	// The census root is the repo root; this test runs with the package
	// directory as cwd (the same convention the clamp census in this package
	// relies on).
	root := filepath.Join("..", "..")
	if _, err := os.Stat(filepath.Join(root, "go.mod")); err != nil {
		t.Fatalf("ROOT_NOT_FOUND: %s has no go.mod (%v). A census that walks the wrong root "+
			"visits nothing and greens on it.", root, err)
	}

	var findings []string
	parsed := 0
	walkErr := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			switch d.Name() {
			case ".git", ".codegraph", "node_modules", "testdata", "vendor":
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			// _test.go excluded deliberately: fixtures PIN the values (that is
			// what tests are for); the census hunts a second site of truth in
			// production code.
			return nil
		}
		rel, relErr := filepath.Rel(root, path)
		if relErr != nil {
			return relErr
		}
		if strings.HasPrefix(rel, filepath.Join("internal", "domain")+string(filepath.Separator)) {
			// The canonical home of the formula.
			return nil
		}
		file, parseErr := parser.ParseFile(fset, path, nil, 0)
		if parseErr != nil {
			return parseErr
		}
		parsed++
		findings = append(findings, stabilityHitsIn(fset, file)...)
		return nil
	})
	require.NoError(t, walkErr, "census walk")

	// Anti-vacuity: the tree had 82 production .go files outside internal/domain
	// when this arm landed; a walk that saw far fewer walked the wrong thing.
	if parsed < 50 {
		t.Fatalf("CENSUS_TOO_SMALL: only %d production .go files parsed outside "+
			"internal/domain — the walk is not covering the repo, so an empty findings list "+
			"proves nothing.", parsed)
	}

	if len(findings) > 0 {
		t.Errorf("SECOND_REPLICA: the stability base %d appears in production code outside "+
			"internal/domain at:\n  %s\nafter aihub#597 the formula has exactly ONE site "+
			"(domain.ComputeStabilityDays); a second copy is the hand-synced drift the dedup "+
			"deleted, growing back. Call the helper instead.",
			immortalStability, strings.Join(findings, "\n  "))
	}
}
