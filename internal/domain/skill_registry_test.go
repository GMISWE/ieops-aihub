package domain

// skill_registry_test.go — the DB-free unit half of the Batch 1A registry
// tests: vocabulary parity with the migration (aihub#396 pattern), the
// single-copy structural gate on THE accessibility predicate, the predicate's
// shape per caller kind, and the request validations that must fire BEFORE
// any database is touched (proven with a nil pool — a panic there would mean
// validation was not actually in front of the query).
//
// The transactional and authorization halves that need a live PostgreSQL are
// in skill_registry_db_test.go, gated on AIHUB_TEST_DB.

import (
	"encoding/json"
	"os"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// ─── Vocabulary parity with migration 0043 (aihub#396 pattern) ──────────────

// skillRegistryStatements reads 0043_skill_registry.sql with comments
// stripped, so the CHECKs can be parsed as code rather than prose. Reuses
// stripSQLComments from work_items_status_vocab_test.go (same package).
func skillRegistryStatements(t *testing.T) string {
	t.Helper()
	b, err := os.ReadFile(migrationsDir + "/0043_skill_registry.sql")
	if err != nil {
		t.Fatalf("read 0043: %v", err)
	}
	return stripSQLComments(string(b))
}

// TestSkillRegistryVocabulariesMatchTheMigration holds the Go copies of the
// skill registries' vocabularies to the CHECK constraints the database will
// actually enforce: the name regex and the visibility values. If either side
// moves without the other, this fails.
func TestSkillRegistryVocabulariesMatchTheMigration(t *testing.T) {
	stmts := skillRegistryStatements(t)

	t.Run("name_regex", func(t *testing.T) {
		re := regexp.MustCompile(`(?is)name\s+TEXT NOT NULL CHECK \(name ~ '([^']*)'\)`)
		m := re.FindStringSubmatch(stmts)
		if m == nil {
			t.Fatalf("no name CHECK found in 0043 — either the constraint is gone (the Go regex is then the only rule and this test should say so deliberately) or this parse is broken")
		}
		if m[1] != skillNameRE.String() {
			t.Errorf("skill name regex drifted:\n  SQL CHECK: %s\n  Go regex: %s", m[1], skillNameRE.String())
		}
	})

	t.Run("visibility_values", func(t *testing.T) {
		// Reuse inClauseValues' quoted-value extraction on the whole file:
		// the only `visibility ... IN (…)` CHECK belongs to skill_versions.
		var fromSQL []string
		inRe := regexp.MustCompile(`(?is)visibility\s+TEXT NOT NULL DEFAULT 'private' CHECK \(visibility IN \(([^)]*)\)\)`)
		m := inRe.FindStringSubmatch(stmts)
		if m == nil {
			t.Fatalf("no visibility CHECK found in 0043")
		}
		for _, q := range quotedRE.FindAllStringSubmatch(m[1], -1) {
			fromSQL = append(fromSQL, q[1])
		}
		var fromGo []string
		for v, ok := range skillVisibilityValues {
			require.True(t, ok)
			fromGo = append(fromGo, v)
		}
		sortStrings(fromSQL)
		sortStrings(fromGo)
		if strings.Join(fromSQL, ",") != strings.Join(fromGo, ",") {
			t.Errorf("skill visibility vocabulary drifted:\n  SQL CHECK: %v\n  Go set:    %v", fromSQL, fromGo)
		}
		// There is no enabled flag, and there never will be one (D1). If
		// somebody adds a third value, the parity arm above catches a missing
		// Go entry; this arm catches the specific regressions the spec named.
		for _, forbidden := range []string{"enabled", "disabled"} {
			if skillVisibilityValues[forbidden] {
				t.Errorf("visibility %q exists in the Go set; the spec forbids an enabled/disabled flag (D1)", forbidden)
			}
		}
	})
}

func sortStrings(s []string) {
	for i := 1; i < len(s); i++ {
		for j := i; j > 0 && s[j] < s[j-1]; j-- {
			s[j], s[j-1] = s[j-1], s[j]
		}
	}
}

// ─── Single-copy structural gate ─────────────────────────────────────────────

// The SQL literals of THE accessibility predicate must exist in exactly ONE
// SQL copy: inside skillVersionAccessSQL / skillGrantExistsSQL in
// skill_registry.go. This mirrors TestMemoryVisibilitySQLPredicateHasOneCopy
// (aihub#379): a re-inlined, drifted copy of the predicate is the defect
// class that work item closed for memories, and the skill registry must not
// reopen it on day one.
func TestSkillVersionAccessPredicateHasOneCopy(t *testing.T) {
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatalf("read package dir: %v", err)
	}

	for clause, scope := range map[string]string{
		// The two read-predicate arms: re-inlining either anywhere in the
		// package (reader OR writer) is the drift this gate exists for.
		`visibility = 'public'`: "package",
		`p.members @>`:          "package",
		// The grants table: a READER may name it only inside the predicate
		// helper. Write paths (Share/Revoke INSERT/DELETE) legitimately name
		// the table in skill_registry_sharing.go, so the gate scopes this
		// marker to the predicate's own file.
		`skill_version_grants`: "skill_registry.go",
	} {
		found := map[string]int{}
		total := 0
		for _, e := range entries {
			name := e.Name()
			if e.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
				continue
			}
			b, rerr := os.ReadFile(name)
			if rerr != nil {
				t.Fatalf("read %s: %v", name, rerr)
			}
			if n := strings.Count(string(b), clause); n > 0 {
				found[name] = n
			}
		}
		if scope == "package" {
			for _, n := range found {
				total += n
			}
			if total != 1 || found["skill_registry.go"] != 1 {
				t.Errorf("the SQL literal %q must appear exactly once in this package, inside "+
					"skill_registry.go (skillVersionAccessSQL / skillGrantExistsSQL). Found: %v.\n"+
					"If you are adding a skill-version reader, call skillVersionAccessSQL instead of "+
					"inlining the clause (aihub#379 defect class).",
					clause, found)
			}
			continue
		}
		if n := found[scope]; n != 1 {
			t.Errorf("the SQL literal %q must appear exactly once in %s (the predicate helper). "+
				"Found there: %d, elsewhere: %v.\n"+
				"A second occurrence in the predicate's file means a reader bypassed "+
				"skillVersionAccessSQL; if the helper legitimately moved, update this gate.",
				clause, scope, n, found)
		}
	}
}

// ─── Predicate shape per caller kind ────────────────────────────────────────

func TestSkillVersionAccessSQLShapes(t *testing.T) {
	scoped := "acme"
	writer := &UserRecord{ID: "u_writer", Role: "writer"}
	admin := &UserRecord{ID: "u_admin", Role: "admin"}
	scopedWriter := &UserRecord{ID: "u_writer", Role: "writer", ProjectScope: &scoped}
	scopedAdmin := &UserRecord{ID: "u_admin", Role: "admin", ProjectScope: &scoped}

	// checkPlaceholders asserts the clause's $N references EXACTLY cover the
	// args range with no gaps, no repeats beyond the shared caller slot, and
	// nothing past the last arg. This is the assertion that would have caught
	// the real off-by-one the unscoped branch shipped on its first cut (the
	// own-skills arm consumed $idx but the grant arm's placeholders had not
	// shifted): PostgreSQL would have bound the caller's ID where the
	// membership probe belonged, and every unscoped read would have silently
	// matched the wrong user.
	checkPlaceholders := func(what string, clause string, args []any, idx, nextIdx int) {
		t.Helper()
		require.Equal(t, idx+len(args), nextIdx,
			"%s: nextIdx must equal first idx + arg count", what)
		found := map[int]bool{}
		for _, m := range regexp.MustCompile(`\$(\d+)`).FindAllStringSubmatch(clause, -1) {
			n, err := strconv.Atoi(m[1])
			require.NoError(t, err, "%s: placeholder %s", what, m[1])
			require.GreaterOrEqual(t, n, idx, "%s: placeholder $%d is below the first index %d", what, n, idx)
			require.Less(t, n, nextIdx, "%s: placeholder $%d exceeds the last arg index %d", what, n, nextIdx-1)
			found[n] = true
		}
		for n := idx; n < nextIdx; n++ {
			require.True(t, found[n], "%s: placeholder $%d has no argument bound to it", what, n)
		}
	}

	t.Run("unscoped admin sees everything with no predicate", func(t *testing.T) {
		clause, args, next := skillVersionAccessSQL("sv", admin, 1)
		require.Equal(t, " AND TRUE", clause)
		require.Empty(t, args)
		require.Equal(t, 1, next)
	})

	t.Run("unscoped writer gets public arm, own-skills arm and grant arm", func(t *testing.T) {
		clause, args, next := skillVersionAccessSQL("sv", writer, 1)
		require.Contains(t, clause, "sv.visibility = 'public'")
		require.Contains(t, clause, "s2.owner_user_id = $1")
		require.Contains(t, clause, "p.owner_user_id = $2")
		require.Contains(t, clause, "p.members @> $3::jsonb")
		require.Contains(t, clause, "EXISTS")
		require.True(t, strings.HasPrefix(clause, " AND ("),
			"the whole predicate must be one ANDed, parenthesized term — a bare OR after "+
				"the caller's key match would make the requested row readable unconditionally")
		require.True(t, strings.HasSuffix(clause, ")"), "the predicate must close its parens")
		require.Len(t, args, 3) // caller (public arm), caller (grant arm), membership probe
		for _, a := range args {
			_, isBool := a.(bool)
			require.False(t, isBool, "a writer must not carry the admin bypass flag")
		}
		// The membership probe is the caller's containment probe.
		probe, err := json.Marshal([]map[string]string{{"user_id": writer.ID}})
		require.NoError(t, err)
		require.Contains(t, args, string(probe))
		checkPlaceholders("unscoped writer", clause, args, 1, next)
	})

	t.Run("scoped writer gets ONLY the scoped grant arm", func(t *testing.T) {
		clause, args, next := skillVersionAccessSQL("sv", scopedWriter, 1)
		require.NotContains(t, clause, "visibility = 'public'",
			"a scoped key must not read public versions (rule 4)")
		require.NotContains(t, clause, "s2.owner_user_id",
			"a scoped key must not read its own private skills through the own-skills arm (rule 4)")
		require.Contains(t, clause, "EXISTS")
		require.True(t, strings.HasPrefix(clause, " AND ("),
			"the scoped arm must be ANDed and parenthesized: a predicate dissolving into "+
				"a bare OR made the REQUESTED row readable regardless of grants (found live "+
				"by TestSkillRegistryScopedKeysStayScoped)")
		require.True(t, strings.HasSuffix(clause, ")"), "the predicate must close its parens")
		require.Contains(t, clause, "g.project_name = $3")
		require.Contains(t, args, scoped)
		checkPlaceholders("scoped writer", clause, args, 1, next)
	})

	t.Run("scoped admin gets the membership bypass but stays confined", func(t *testing.T) {
		clause, args, next := skillVersionAccessSQL("sv", scopedAdmin, 1)
		require.Contains(t, clause, " OR $3", "a scoped admin carries the bypass flag like checkProjectAccess's admin level")
		require.NotContains(t, clause, "visibility = 'public'")
		require.Contains(t, args, scoped)
		require.Contains(t, args, true)
		checkPlaceholders("scoped admin", clause, args, 1, next)
	})
}

// ─── Request validation fires before the database ───────────────────────────

// skillCaller / skillPublish fixtures for the validation tests.
var (
	skillWriter = &UserRecord{ID: "u_writer", Role: "writer"}
	scopedKey   = func() *UserRecord {
		p := "acme"
		return &UserRecord{ID: "u_writer", Role: "writer", ProjectScope: &p}
	}()
)

func validBundleJSON() json.RawMessage {
	return json.RawMessage(`{"entry":"SKILL.md","files":[{"path":"SKILL.md","content":"x"}],"license":{"name":"MIT"}}`)
}

func validContractJSON() json.RawMessage {
	return json.RawMessage(`{"capabilities":["authoring"],"runtime":{"interactive":false}}`)
}

// TestPublishSkillVersionValidatesBeforeTouchingTheDatabase proves the
// request-level guards run with a NIL pool: every arm below must answer its
// 400/401/403 without panicking, and the fully valid arm must reach the "no
// database" 500 — the proof it got PAST validation and stopped at the pool
// check, not before it.
func TestPublishSkillVersionValidatesBeforeTouchingTheDatabase(t *testing.T) {
	ctx := t.Context()

	t.Run("nil caller is unauthorized", func(t *testing.T) {
		_, aerr := PublishSkillVersion(ctx, nil, nil, PublishSkillVersionRequest{SkillID: "skill_x"})
		require.NotNil(t, aerr)
		require.Equal(t, ErrUnauthorized, aerr.Code)
	})

	t.Run("scoped key cannot publish", func(t *testing.T) {
		_, aerr := PublishSkillVersion(ctx, nil, scopedKey, PublishSkillVersionRequest{SkillID: "skill_x"})
		require.NotNil(t, aerr)
		require.Equal(t, ErrForbidden, aerr.Code)
	})

	t.Run("negative expected_latest is a 400", func(t *testing.T) {
		_, aerr := PublishSkillVersion(ctx, nil, skillWriter, PublishSkillVersionRequest{
			SkillID: "skill_x", ExpectedLatest: -1,
			Bundle: validBundleJSON(), Contract: validContractJSON(),
		})
		require.NotNil(t, aerr)
		require.Equal(t, ErrBadRequest, aerr.Code)
		require.Contains(t, aerr.Message, "expected_latest")
	})

	t.Run("traversal bundle is a 400 naming the bundle", func(t *testing.T) {
		bad := json.RawMessage(`{"entry":"../SKILL.md","files":[{"path":"../SKILL.md","content":"x"}],"license":{"name":"MIT"}}`)
		_, aerr := PublishSkillVersion(ctx, nil, skillWriter, PublishSkillVersionRequest{
			SkillID: "skill_x", ExpectedLatest: 0, Bundle: bad, Contract: validContractJSON(),
		})
		require.NotNil(t, aerr)
		require.Equal(t, ErrBadRequest, aerr.Code)
		require.Contains(t, aerr.Message, "bundle")
	})

	t.Run("out-of-subset contract schema is a 400 (fail closed)", func(t *testing.T) {
		bad := json.RawMessage(`{"capabilities":["authoring"],"input_schema":{"$ref":"#/x"}}`)
		_, aerr := PublishSkillVersion(ctx, nil, skillWriter, PublishSkillVersionRequest{
			SkillID: "skill_x", ExpectedLatest: 0, Bundle: validBundleJSON(), Contract: bad,
		})
		require.NotNil(t, aerr)
		require.Equal(t, ErrBadRequest, aerr.Code)
	})

	t.Run("content digest mismatch is a 400 naming both digests", func(t *testing.T) {
		_, aerr := PublishSkillVersion(ctx, nil, skillWriter, PublishSkillVersionRequest{
			SkillID: "skill_x", ExpectedLatest: 0,
			Bundle: validBundleJSON(), Contract: validContractJSON(),
			ContentDigest: "sha256:" + strings.Repeat("0", 64),
		})
		require.NotNil(t, aerr)
		require.Equal(t, ErrBadRequest, aerr.Code)
		require.Contains(t, aerr.Message, "content_digest")
	})

	t.Run("a valid request reaches the pool check, not a panic", func(t *testing.T) {
		_, aerr := PublishSkillVersion(ctx, nil, skillWriter, PublishSkillVersionRequest{
			SkillID: "skill_x", ExpectedLatest: 0,
			Bundle: validBundleJSON(), Contract: validContractJSON(),
		})
		require.NotNil(t, aerr)
		require.Equal(t, ErrInternalError, aerr.Code)
		require.Contains(t, aerr.Message, "no database")
	})
}

func TestCreateSkillValidationFiresBeforeTheDatabase(t *testing.T) {
	ctx := t.Context()

	t.Run("nil caller is unauthorized", func(t *testing.T) {
		_, aerr := CreateSkill(ctx, nil, nil, CreateSkillRequest{Name: "grill-me"})
		require.NotNil(t, aerr)
		require.Equal(t, ErrUnauthorized, aerr.Code)
	})

	t.Run("scoped key cannot create", func(t *testing.T) {
		_, aerr := CreateSkill(ctx, nil, scopedKey, CreateSkillRequest{Name: "grill-me"})
		require.NotNil(t, aerr)
		require.Equal(t, ErrForbidden, aerr.Code)
	})

	for name, bad := range map[string]string{
		"uppercase":      "Grill-Me",
		"leading digit":  "1grill",
		"leading hyphen": "-grill",
		"underscore":     "grill_me",
		"dot":            "grill.me",
		"empty":          "",
		"too long":       strings.Repeat("a", 65),
		"whitespace":     " grill-me",
	} {
		t.Run("invalid name: "+name, func(t *testing.T) {
			_, aerr := CreateSkill(ctx, nil, skillWriter, CreateSkillRequest{Name: bad})
			require.NotNil(t, aerr)
			require.Equal(t, ErrBadRequest, aerr.Code)
		})
	}

	t.Run("a valid name reaches the pool check", func(t *testing.T) {
		_, aerr := CreateSkill(ctx, nil, skillWriter, CreateSkillRequest{Name: "grill-me"})
		require.NotNil(t, aerr)
		require.Equal(t, ErrInternalError, aerr.Code)
	})
}

func TestSetSkillVersionVisibilityRefusesTheEnableDisableFlag(t *testing.T) {
	ctx := t.Context()
	// "enabled"/"disabled" are the spec's forbidden non-state (D1: there is
	// no enabled flag); they must be a 400, not a silently accepted value.
	for _, forbidden := range []string{"enabled", "disabled", "", "project", "PROJECT"} {
		_, aerr := SetSkillVersionVisibility(ctx, nil, skillWriter, "skill_x", 1, forbidden)
		require.NotNil(t, aerr, "visibility %q was accepted", forbidden)
		require.Equal(t, ErrBadRequest, aerr.Code)
	}
	for _, ok := range []string{"private", "public"} {
		_, aerr := SetSkillVersionVisibility(ctx, nil, skillWriter, "skill_x", 1, ok)
		// Passes validation, then stops at the pool with 500 — proving the
		// value itself was accepted.
		require.NotNil(t, aerr)
		require.Equal(t, ErrInternalError, aerr.Code, "visibility %q should be valid; got %v", ok, aerr)
	}
}

func TestListSkillsLimitIsRefusedNotClamped(t *testing.T) {
	ctx := t.Context()
	for _, limit := range []int{-1, 201, 1000} {
		_, aerr := ListSkills(ctx, nil, skillWriter, ListSkillsRequest{Limit: limit})
		require.NotNil(t, aerr, "limit %d was accepted", limit)
		require.Equal(t, ErrBadRequest, aerr.Code)
		require.Contains(t, aerr.Message, "limit")
	}
}
