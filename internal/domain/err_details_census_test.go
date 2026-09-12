package domain

// aihub#375 — the details-writer census.
//
// pkg/client.formatDetails flattens every error's `details` into the error
// string under one shared byte cap (client.DetailsRenderLimit). That cap is a
// BACKSTOP for count-scaled lists, not a license: a producer that puts an
// unbounded value into `details` is spending every other producer's budget,
// which is exactly how the CONFLICT_SIMILAR_MEMORY refusal (a whole memory
// body in details.existing.content) got the cap pinned at 500 bytes for
// everyone. This census is what keeps that from happening again.
//
// Mechanism: every NewErrDetails call site in internal/, pkg/ and cmd/ is
// found by AST scan, keyed "<file>:<function>", and must match an entry below
// — same key, same set of details shapes — carrying a written verdict of WHY
// its values are bounded (or which key is count-scaled and what backstops it).
// A new site, a removed site, or a reshaped envelope is a red test until its
// entry is written or corrected, which forces the boundedness question at
// review time instead of after the caller's context is flooded.
//
// What a shape does and does not capture: it is the set of dotted key paths of
// the details map literal ("existing.content_excerpt", …), or the name of the
// variable/call supplying a non-literal details value ("dynamic-var:details").
// It sees a key being added, removed or renamed; it does NOT see the value
// expression behind a kept key changing. That direction is covered where the
// value has a budget worth pinning — memory_conflict_details_test.go and
// commit_locks_test.go hold theirs at runtime — and the verdicts below say,
// per site, which instrument holds it.
//
// The mutant this is built against (aihub#375's class-level one): take any
// call site here and stuff a stored blob into its map under a new key — red,
// unknown shape. Add a brand-new NewErrDetails call — red, unknown site.

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

// detailsWriter is one census entry: the shapes of every NewErrDetails call in
// one function, and the human verdict on their boundedness. The key convention
// ("<file base>:<func>") matches the other censuses in this package
// (nontx_query_errors_test.go): it survives every edit around the site.
type detailsWriter struct {
	Shapes  []string // sorted; one fingerprint per NewErrDetails call in the function
	Verdict string
}

var detailsWriterCensus = map[string]detailsWriter{
	// ── bounded by construction: fixed literals, ids, ints, enums ────────────
	"commit_locks.go:FnReconcileCommitLocks": {
		Shapes:  []string{"{hint}"},
		Verdict: "one fixed literal string",
	},
	"dependencies.go:detectCycle": {
		Shapes:  []string{"{cycle_path}"},
		Verdict: "exactly two work-item ids",
	},
	"pgx_err.go:retryConflictErr": {
		Shapes:  []string{"{retryable,sqlstate}"},
		Verdict: "a bool and a 5-char SQLSTATE",
	},
	"projects.go:membersCASConflictErr": {
		Shapes:  []string{"{current_members_version,expected_members_version}"},
		Verdict: "two ints",
	},
	"memory.go:verifyAttemptCredentialSimple": {
		Shapes:  []string{"{current_epoch}"},
		Verdict: "one int",
	},
	"work_items.go:casConflictErr": {
		Shapes:  []string{"{current_resources_version,expected_resources_version}"},
		Verdict: "two ints",
	},
	"work_item_fields.go:vocabularyErr": {
		Shapes:  []string{"{allowed,field,got}"},
		Verdict: "a fixed vocabulary list, a field name, and the caller's single enum-sized value",
	},
	"work_item_fields.go:validateWorkItemLabels": {
		Shapes:  []string{"{field,got,max}"},
		Verdict: "a field name and two ints (got is len(labels), not the labels)",
	},
	"work_item_fields.go:validateWorkItemContent": {
		Shapes:  []string{"{field,got,max,note}"},
		Verdict: "a field name, two ints, a fixed literal (got is the rune COUNT, not the content)",
	},
	"router.go:handleUpdateUser": {
		Shapes:  []string{"{field,got}"},
		Verdict: "a field name and the caller's single user_type string",
	},
	"run_attempts.go:FnClaimWorkItem": {
		Shapes:  []string{"{current_attempt.actor_display,current_attempt.claim_epoch,current_attempt.id,current_attempt.last_active_at}"},
		Verdict: "one attempt's id/display/epoch/timestamp",
	},
	"run_attempts.go:verifyAttemptCredential": {
		Shapes:  []string{"dynamic-call:supersededByDetails"},
		Verdict: "supersededByDetails returns at most {superseded_by:{actor_display,at}} — one display name and one timestamp",
	},
	"run_attempts.go:probeForeignLockHolders": {
		Shapes:  []string{"{conflict_with.actor_display,conflict_with.attempt_id,conflict_with.work_item_slug}"},
		Verdict: "one holder triple",
	},
	"run_attempts.go:lockTakenErrFor": {
		Shapes:  []string{"{conflict_with.actor_display,conflict_with.attempt_id,conflict_with.work_item_slug}"},
		Verdict: "one holder triple",
	},
	"run_attempts.go:FnAcquireLocks": {
		Shapes: []string{
			"{conflict_with.actor_display,conflict_with.attempt_id,conflict_with.work_item_slug}",
			"{conflict_with.actor_display,conflict_with.attempt_id,conflict_with.work_item_slug}",
		},
		Verdict: "one holder triple per refusal (the initial-scan arm and the reclaim-race arm)",
	},

	// ── bounded by an explicit budget a runtime test enforces ────────────────
	"memory_conflict.go:memoryConflictErr": {
		Shapes: []string{"{existing.content_excerpt,existing.content_len,existing.id,existing.similarity,existing.type}"},
		Verdict: "the aihub#375 producer: content_excerpt <= memoryConflictExcerptRunes and the WHOLE envelope " +
			"<= client.DetailsRenderLimit, both held by memory_conflict_details_test.go; content_len is an int, " +
			"the rest are id/enum/float",
	},

	// ── echoes of the caller's own request (nothing stored, nothing another
	//    actor wrote; the render cap remains the transport backstop) ──────────
	"declared_resources.go:ValidateDeclaredResources": {
		Shapes: []string{
			"{common_error,entry_shape,parse_error,valid_types}",
			"{entry_shape,got_type,hint,index,valid_types}",
			"{entry_shape,got_type,hint,index}",
			"{expected_scheme,got_type,got_uri,hint,index,schemes_by_type}",
		},
		Verdict: "fixed hints/vocabularies plus echoes of ONE offending entry from the caller's own request " +
			"(got_type, got_uri, a parse error) — a 400 that hands the caller back its own bytes, never a stored row",
	},
	"declared_resources.go:ValidateRequestedLocks": {
		Shapes: []string{
			"{entry_shape,got_resource_type,hint,index,valid_types}",
			"{entry_shape,hint,index}",
		},
		Verdict: "same class as ValidateDeclaredResources: fixed hints plus one echoed field of the caller's request",
	},
	"work_items.go:jsonObjectParamErr": {
		Shapes: []string{"dynamic-var:details"},
		Verdict: "built a few lines above the call from a kind word, a byte COUNT and a decode verdict/parse error " +
			"about the caller's own field — the field's bytes themselves are never included",
	},

	// ── count-scaled, deliberately relying on the render-cap backstop ────────
	"commit_locks.go:commitLockConflictErr": {
		Shapes: []string{"{advice,blocked_paths,conflict_with.actor_display,conflict_with.attempt_id,conflict_with.work_item_slug,conflicts}"},
		Verdict: "conflicts/blocked_paths carry one entry per blocked file, so they scale with the commit " +
			"(measured 767 bytes @ 1 conflict, 1219 @ 3): past the cap the cut reaches only keys whose content " +
			"the untruncated Message already states verbatim. advice sorts first and is budgeted against the cap " +
			"by TestCommitLockConflictErr_AdviceSurvivesTheRenderLimit (aihub#366)",
	},
	"work_items.go:checkDedup": {
		Shapes: []string{
			"{candidates}",
			"{existing.goal,existing.id,existing.slug,existing.status}",
		},
		Verdict: "existing.goal is DB-capped at 500 runes (work_items_goal_check), worst case 1582 compacted bytes — " +
			"the largest count-BOUNDED envelope, and what sized client.DetailsRenderLimit (aihub#375). candidates is " +
			"count-scaled (its query LIMITs at 50, ~16KB worst, in no particular order) and deliberately rides the " +
			"render-cap backstop: each entry names a wi the caller can look up, so the cut costs a lookup, not data",
	},
	"projects.go:undeclaredMemberRemovalErr": {
		Shapes: []string{"{expected_removals,retryable,stored_member_count,submitted_member_count,undeclared_removals}"},
		Verdict: "two int counts, a bool, and two user-id lists scaled by the project's member count / the caller's " +
			"own expected_removals — short ids, not rows; the render cap backstops a pathological membership",
	},
}

// detailsShape fingerprints the third NewErrDetails argument. A map composite
// literal becomes its sorted dotted key paths; anything else names the
// variable or call supplying the value, so a reviewer can find it.
func detailsShape(e ast.Expr) string {
	lit, ok := e.(*ast.CompositeLit)
	if !ok {
		switch v := e.(type) {
		case *ast.Ident:
			return "dynamic-var:" + v.Name
		case *ast.CallExpr:
			switch fun := v.Fun.(type) {
			case *ast.Ident:
				return "dynamic-call:" + fun.Name
			case *ast.SelectorExpr:
				return "dynamic-call:" + fun.Sel.Name
			}
		}
		return "dynamic:?"
	}
	paths := detailsKeyPaths(lit, "")
	sort.Strings(paths)
	return "{" + strings.Join(paths, ",") + "}"
}

func detailsKeyPaths(lit *ast.CompositeLit, prefix string) []string {
	var out []string
	for _, elt := range lit.Elts {
		kv, ok := elt.(*ast.KeyValueExpr)
		if !ok {
			continue
		}
		keyLit, ok := kv.Key.(*ast.BasicLit)
		if !ok || keyLit.Kind != token.STRING {
			out = append(out, prefix+"<non-literal-key>")
			continue
		}
		key, err := strconv.Unquote(keyLit.Value)
		if err != nil {
			key = keyLit.Value
		}
		if nested, ok := kv.Value.(*ast.CompositeLit); ok {
			if _, isMap := nested.Type.(*ast.MapType); isMap {
				out = append(out, detailsKeyPaths(nested, prefix+key+".")...)
				continue
			}
		}
		out = append(out, prefix+key)
	}
	return out
}

// scanDetailsWriters walks the module's Go source (tests and testdata
// excluded) and returns every NewErrDetails call site's shapes, keyed like the
// census.
func scanDetailsWriters(t *testing.T) map[string][]string {
	t.Helper()
	root := moduleRootForCensus(t)
	fset := token.NewFileSet()
	got := map[string][]string{}
	for _, top := range []string{"internal", "pkg", "cmd"} {
		if _, err := os.Stat(filepath.Join(root, top)); err != nil {
			// A tree that stops existing is a repo reshape, not a census pass:
			// the entries under it would go stale and say so.
			continue
		}
		err := filepath.WalkDir(filepath.Join(root, top), func(path string, d os.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() {
				if d.Name() == "testdata" {
					return filepath.SkipDir
				}
				return nil
			}
			if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
				return nil
			}
			f, perr := parser.ParseFile(fset, path, nil, 0)
			if perr != nil {
				return perr
			}
			for _, decl := range f.Decls {
				fd, ok := decl.(*ast.FuncDecl)
				if !ok {
					continue
				}
				ast.Inspect(fd, func(n ast.Node) bool {
					call, ok := n.(*ast.CallExpr)
					if !ok {
						return true
					}
					name := ""
					switch fun := call.Fun.(type) {
					case *ast.Ident:
						name = fun.Name
					case *ast.SelectorExpr:
						name = fun.Sel.Name
					}
					if name != "NewErrDetails" || len(call.Args) != 3 {
						return true
					}
					key := filepath.Base(path) + ":" + fd.Name.Name
					got[key] = append(got[key], detailsShape(call.Args[2]))
					return true
				})
			}
			return nil
		})
		if err != nil {
			t.Fatalf("walking %s: %v", top, err)
		}
	}
	for _, shapes := range got {
		sort.Strings(shapes)
	}
	return got
}

func moduleRootForCensus(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("go.mod not found above the test's working directory")
		}
		dir = parent
	}
}

// TestErrDetailsWritersAreClassified is the census gate. Red on an unknown
// site, an unknown shape at a known site, or a stale entry — each failure
// prints the entry to write, so classifying a new producer is copy-paste plus
// the one thing only the author can supply: the verdict.
func TestErrDetailsWritersAreClassified(t *testing.T) {
	got := scanDetailsWriters(t)

	for key, shapes := range got {
		entry, ok := detailsWriterCensus[key]
		if !ok {
			t.Errorf("unclassified details writer %s — add a census entry with a verdict on the "+
				"boundedness of every value it puts into details:\n\t%q: {Shapes: []string{%s}, Verdict: \"…\"},",
				key, key, quotedList(shapes))
			continue
		}
		if !equalStringSlices(entry.Shapes, shapes) {
			t.Errorf("details writer %s changed shape.\n  census: %v\n  source: %v\n"+
				"Update its census entry AND its verdict — a reshaped envelope is a re-asked "+
				"boundedness question, not a formality", key, entry.Shapes, shapes)
		}
	}
	for key := range detailsWriterCensus {
		if _, ok := got[key]; !ok {
			t.Errorf("stale census entry %s: no such NewErrDetails site — remove it", key)
		}
	}
}

func quotedList(ss []string) string {
	q := make([]string, len(ss))
	for i, s := range ss {
		q[i] = strconv.Quote(s)
	}
	return strings.Join(q, ", ")
}

func equalStringSlices(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
