package domain

// aihub#543 probe wave 2, band 2 (identity/authz) — the two sentences the
// pf_update_project and pf_create_user cards make about the ROLE VOCABULARIES,
// bound to the code that decides each one rather than to a list restated here.
//
//	pf_update_project.md
//	  "`owner` is the `projects.owner_user_id` column rather than a member role
//	   — the two-vocabulary split §6.2 T2-17 requires this card to name."
//	      -> TestTheRoleVocabulariesAreDistinctAndOwnerIsInNeither
//	  "So this tool's writes are the provenance of every `maintainer` row that
//	   exists…"
//	      -> TestNoMigrationCanStoreAMaintainerMemberRole
//	pf_create_user.md
//	  "the global role vocabulary here (`writer|admin`) is a third vocabulary,
//	   distinct from member roles and from the owner column"
//	      -> TestTheRoleVocabulariesAreDistinctAndOwnerIsInNeither
//
// Neither needs a database: the member vocabulary is read out of UpdateProject's
// own validation by validatedMemberRoles (projects_test.go), the global one out
// of UserGlobalRoleList, and the migration claim off the migration files.
//
//	GOWORK=off go test ./internal/domain/ -run 'TestTheRoleVocabularies|TestNoMigrationCanStore' -count=1

import (
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
)

// TestTheRoleVocabulariesAreDistinctAndOwnerIsInNeither is the T2-17 split, as a
// property of the two vocabularies rather than as a sentence about them.
//
// 🔴 Neither set is written down here. `maintainer` and `admin` appear below only
// as the two values whose MEMBERSHIP is asserted, and both are read back out of
// the live sets before being used — a third hand-copied list is the mistake
// aihub#443 already paid for once, and it would stay green in the one case that
// matters: a value migrating from one vocabulary into the other while the list
// here still agreed with the version of the code it was copied from.
//
// What would be false if this failed: a caller conflating the two vocabularies
// is the realistic mistake (`pf_whoami` reports `role: "owner"`, and `pf_recall`
// callers routinely hold a global `admin`), so the cards promise the split. If
// the sets merged, the cards' "not a project member role" would become prose
// about nothing while the tools quietly accepted each other's values.
//
// MUTANTS:
//
//	M1 enforcement: add "owner": 2 to RoleLevel and `|| m.Role == "owner"` to
//	    UpdateProject's member check          RED  owner_is_in_neither
//	M2 enforcement: add "admin" to userGlobalRoles' twin by adding it to
//	    UpdateProject's member vocabulary     RED  the_two_sets_differ /
//	                                               admin_is_global_only
//	M3 enforcement: add "maintainer": true to userGlobalRoles
//	                                          RED  maintainer_is_member_only
//	M4 publication: point the published pf_create_user.role enum at a literal
//	    list instead of UserGlobalRoleList()  RED  in
//	                                               TestCreateUserVocabulariesArePublishedAsEnums
//	                                               (internal/mcp) — this arm holds
//	                                               the vocabularies, that one holds
//	                                               the publication bound to them
//	M5a floor: replace UpdateProject's `!=` chain with `RoleLevel[m.Role] == 0`
//	                                          RED  validatedMemberRoles' own fatal —
//	                                               the vocabulary becomes unreadable,
//	                                               so this arm fails loudly rather
//	                                               than measuring an empty set
//	M5b floor: shrink UserGlobalRoleList to one value
//	                                          RED  both_vocabularies_are_populated
//
//	🟡 MEASURED GREEN, and correctly: renaming UpdateProject's members range
//	variable. The first version of this list recorded that as M5 and it does NOT
//	redden anything — validatedMemberRoles takes the loop variable's name from the
//	range statement, which its own doc comment says it does for exactly this
//	reason. Recorded rather than deleted, because "the obvious floor mutant does
//	not fire" is a fact about the helper worth knowing before trusting it.
func TestTheRoleVocabulariesAreDistinctAndOwnerIsInNeither(t *testing.T) {
	member := validatedMemberRoles(t)
	global := append([]string(nil), UserGlobalRoleList()...)
	slices.Sort(global)

	// The anti-vacuity arm, and it has to come first: every assertion below is a
	// statement about set membership, and every one of them is satisfied by an
	// empty set. An empty `member` in particular is reachable by accident —
	// validatedMemberRoles parses UpdateProject's AST, so a refactor of that
	// function is all it takes.
	t.Run("both_vocabularies_are_populated", func(t *testing.T) {
		if len(member) < 2 || len(global) < 2 {
			t.Fatalf("the member vocabulary has %d value(s) %v and the global one %d %v. "+
				"Every check below is about which set a value is in, and an empty set puts "+
				"nothing in it — so this walk is broken and the subtests would assert nothing.",
				len(member), member, len(global), global)
		}
	})

	t.Run("the_two_sets_differ", func(t *testing.T) {
		if slices.Equal(member, global) {
			t.Errorf("the member roles %v and the global roles %v are the same set. The cards "+
				"promise two vocabularies both spelled `role`, and the reason the promise is "+
				"worth making is that they disagree; one set means every card sentence drawing "+
				"the distinction is describing something that is no longer there.", member, global)
		}
	})

	// The owner column is the third vocabulary, and it is a vocabulary of one
	// value that neither of the other two may hold. RoleLevel's doc comment
	// states the invariant; this is the assertion of it.
	t.Run("owner_is_in_neither", func(t *testing.T) {
		if slices.Contains(member, "owner") {
			t.Errorf("UpdateProject accepts %q as a member role (vocabulary %v). owner is "+
				"projects.owner_user_id, a column — a members entry carrying it would be ranked "+
				"by RoleLevel, which has no rung for it, so the holder would read as a "+
				"non-member.", "owner", member)
		}
		if slices.Contains(global, "owner") {
			t.Errorf("users.role accepts %q (vocabulary %v). Project ownership is a per-project "+
				"column, so a global role of that name would be a second, disagreeing answer to "+
				"who owns a project.", "owner", global)
		}
		if _, ranked := RoleLevel["owner"]; ranked {
			t.Errorf("RoleLevel has a rung for %q. That is the aihub#443 shape exactly: the old "+
				"ladder ranked owner at 3 and maintainer at 0, so a maintainer of a public "+
				"project was answered with less than an anonymous caller.", "owner")
		}
	})

	// The two values that make the split observable, each read out of the set it
	// belongs to rather than typed in.
	t.Run("maintainer_is_member_only", func(t *testing.T) {
		const value = "maintainer"
		if !slices.Contains(member, value) {
			t.Fatalf("%q is not a member role any more (vocabulary %v), so this subtest is "+
				"asserting the absence of a value from a set it was never in — which is green "+
				"for the wrong reason. If the vocabulary genuinely changed, pick the value that "+
				"is now member-only and say so in the cards too.", value, member)
		}
		if slices.Contains(global, value) {
			t.Errorf("%q is now a legal users.role as well as a member role (global %v). It is "+
				"the value a caller conflating the two vocabularies actually sends — "+
				"internal/server/update_user_vocab_test.go uses it for exactly that reason — so "+
				"accepting it on both sides removes the answer that teaches the difference.",
				value, global)
		}
	})

	t.Run("admin_is_global_only", func(t *testing.T) {
		const value = "admin"
		if !slices.Contains(global, value) {
			t.Fatalf("%q is not a global role any more (vocabulary %v); same problem as above in "+
				"the other direction.", value, global)
		}
		if slices.Contains(member, value) {
			t.Errorf("UpdateProject now accepts %q as a member role (vocabulary %v). A global "+
				"admin already clears every project check in checkProjectAccess, so a member "+
				"role of the same name would be a second grant of the same authority ranked by "+
				"a ladder that knows nothing about it.", value, member)
		}
	})
}

// membersColumnWrite matches a SQL fragment that assigns projects.members. Same
// spelling as TestMembersUpdateHasOneWritePathAndOneRemovalCheck's, for the same
// reason: Go's regexp has no lookahead, so members_version is stripped by the
// caller before the match rather than excluded here.
var membersColumnWrite = regexp.MustCompile(`\bmembers\s*=`)

// TestNoMigrationCanStoreAMaintainerMemberRole is the half of pf_update_project's
// provenance claim that a test in this repo can hold.
//
// The card says this tool's writes are the provenance of every `maintainer` row
// that exists. Two facts carry it, and only one of them is about the API:
// TestMembersUpdateHasOneWritePathAndOneRemovalCheck (projects_members_removal_test.go)
// pins that exactly one SQL string in production Go assigns the column, and
// UpdateProject validates the vocabulary before reaching it. This arm closes the
// other door — the migrations, which write the column outside that path
// entirely and are not Go, so that census cannot see them.
//
// 🔴 0013_backfill_projects.sql is the one that matters, and it is not merely
// silent about `maintainer`: it CASEs the value DOWN to `writer` while copying
// users.project_roles into projects.members. So the backfill could not have
// produced a maintainer row even where the source data held one, which is what
// leaves this tool as the only producer. A migration that dropped the CASE would
// make the card's sentence false while every Go-side arm stayed green, and
// nothing else in the tree reads these files for this.
//
// ⚠️ The property is the MAPPING, not the absence of the word. A first version
// of this arm failed the clean tree, because 0013 mentions 'maintainer' three
// times and every one of them is a PREDICATE — the `IN ('viewer','writer',
// 'maintainer')` source filter and the CASE's own WHEN. "Does the literal appear"
// is therefore the wrong question: it cannot tell a value being stored from a
// value being tested for, and the safe shape necessarily names it.
//
// The remaining residue is stated in the card rather than asserted here: rows
// written before the validation existed are a fact about a live database, not
// about a commit, and pf_update_project.md carries the dated read for that.
//
// MUTANTS:
//
//	M6 enforcement: drop the CASE in 0013, storing rec.role verbatim
//	                                          RED  the backfill no longer downgrades
//	M7 enforcement: add a second migration assigning projects.members
//	                                          RED  one_migration_writes_members —
//	                                               a new writer is a new provenance
//	                                               and has to be read against the
//	                                               card
//	M8 publication: remove "maintainer" from RoleLevel and UpdateProject's check
//	                                          RED  the_value_is_a_member_role — the
//	                                               claim has no subject if the
//	                                               vocabulary no longer holds it
//	M9 floor: point the walk at a directory with no migrations
//	                                          RED  one_migration_writes_members
func TestNoMigrationCanStoreAMaintainerMemberRole(t *testing.T) {
	const value = "maintainer"
	const downgradeTo = "writer"

	// Bound to the vocabulary rather than asserted about a bare string: if
	// `maintainer` stopped being a member role, "no migration can store it" would
	// be true of an unbounded number of strings and would say nothing about this
	// card.
	t.Run("the_value_is_a_member_role", func(t *testing.T) {
		if !slices.Contains(validatedMemberRoles(t), value) {
			t.Fatalf("%q is not in the member-role vocabulary UpdateProject validates, so the "+
				"provenance sentence in pf_update_project.md has no subject. Reword the card "+
				"and repoint this arm at the value that replaced it.", value)
		}
	})

	dir := filepath.Join(repoRootFromDomainPackage(t), "internal", "db", "migrations")
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read %s: %v — a walk that cannot open the migrations reports no offending "+
			"migration, which is the same green as there being none", dir, err)
	}

	writers := map[string]string{}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".sql") {
			continue
		}
		raw, readErr := os.ReadFile(filepath.Join(dir, e.Name()))
		if readErr != nil {
			t.Fatalf("read %s: %v", e.Name(), readErr)
		}
		// members_version is a different column assigned by the same statements.
		probe := strings.ReplaceAll(string(raw), "members_version", "mv_column")
		if membersColumnWrite.MatchString(probe) {
			writers[e.Name()] = probe
		}
	}

	// The floor and the ratchet in one: zero writers means the walk is not
	// reading the files it thinks it is, and more than one means a second
	// provenance for the column exists that the card's sentence does not account
	// for. Either way the mapping check below stops meaning what its name says.
	t.Run("one_migration_writes_members", func(t *testing.T) {
		if len(writers) != 1 {
			t.Fatalf("%d migration(s) under %s assign projects.members: %v.\nExactly one is "+
				"expected — 0013_backfill_projects.sql. Zero means this walk found nothing and "+
				"every check below is vacuous; more than one means the column has a second "+
				"provenance outside UpdateProject's validation, and pf_update_project.md's "+
				"provenance sentence has to be read against it before this number moves.",
				len(writers), dir, sortedNames(writers))
		}
	})

	// `CASE WHEN <expr>='maintainer' THEN 'writer'`, with whitespace collapsed so
	// reformatting the migration does not read as removing the guard.
	mapping := regexp.MustCompile(`(?i)case\s+when\s+[^=]*=\s*'` + value + `'\s+then\s+'` + downgradeTo + `'`)
	for name, sql := range writers {
		collapsed := regexp.MustCompile(`\s+`).ReplaceAllString(sql, " ")
		if mapping.MatchString(collapsed) {
			continue
		}
		t.Errorf("%s assigns projects.members and does NOT map '%s' down to '%s'.\nThat is the "+
			"clause which makes pf_update_project.md's provenance sentence true: with it, the "+
			"backfill could not produce a %s row even where users.project_roles held one, so "+
			"this tool's validated writes are the only producer. Without it the card is false "+
			"while every Go-side arm stays green — TestMembersUpdateHasOneWritePathAndOneRemovalCheck "+
			"censuses Go string literals and does not read .sql files at all.",
			name, value, downgradeTo, value)
	}
}

// sortedNames is the deterministic rendering of the writer census, so a failure
// names the same files in the same order however the directory was walked.
func sortedNames(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	slices.Sort(out)
	return out
}
