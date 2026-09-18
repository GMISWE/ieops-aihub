package domain

// skill_registry_db_test.go — the live-PostgreSQL half of the Batch 1A
// registry tests: publication CAS (including the concurrent-publish race),
// per-version sharing and privacy (the spec's "Publishing and sharing"
// Requirement), revocation and auth-recheck, scoped-key confinement, and the
// no-metadata-oracle rules.
//
// Gated exactly like the repo's other DB tests (setupLatestTestDB pattern):
// skipped unless AIHUB_TEST_DB is set, so `go test ./...` stays green with no
// database. Migration 0043's Up section is applied by the setup helper — it
// is idempotent (CREATE ... IF NOT EXISTS, constraints inline), so replaying
// it against a database goose already migrated is a no-op (the aihub#444
// replay hazard is why it MUST stay idempotent).
//
//	AIHUB_TEST_DB=postgres://postgres:testpass@localhost:5433/aihub_test?sslmode=disable \
//	GOWORK=off go test ./internal/domain/ -run TestSkillRegistry -count=1 -v

import (
	"context"
	"fmt"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/require"

	"github.com/GMISWE/ieops-aihub/internal/citest/testname"
	"github.com/GMISWE/ieops-aihub/internal/skillregistry"
)

// setupSkillRegistryDB connects to AIHUB_TEST_DB and ensures migration 0043 is
// applied.
func setupSkillRegistryDB(t *testing.T) *pgxpool.Pool {
	t.Helper()
	pool := setupLatestTestDB(t)
	runMigration(t, pool, "0043_skill_registry.sql")
	return pool
}

// skillUser seeds one user with a t.Name()-derived id plus a suffix, so one
// test can hold an owner, a member, a stranger and an admin without any two
// colliding across runs.
func skillUser(t *testing.T, pool *pgxpool.Pool, suffix string) string {
	t.Helper()
	uid := "u_" + testname.Sanitize(t.Name()) + "_" + suffix
	mustExec(t, pool, `INSERT INTO users(id,email,display_name) VALUES('`+uid+`','`+uid+`@test.local','`+uid+`') ON CONFLICT (id) DO NOTHING`)
	return uid
}

// skillCleanup removes every skill row owned by the given users. Grants and
// versions cascade from skills, so one delete clears the whole registry
// footprint of the test; the re-run-safety reasoning is the same as
// resetTestProject's (memory_latest_test.go).
func skillCleanup(t *testing.T, pool *pgxpool.Pool, userIDs ...string) {
	t.Helper()
	list := ""
	for i, u := range userIDs {
		if i > 0 {
			list += ","
		}
		list += "'" + u + "'"
	}
	mustExec(t, pool, `DELETE FROM skills WHERE owner_user_id IN (`+list+`)`)
}

// skillBundleFor returns a canonical closed bundle whose entry file carries the
// given content, so versions are distinguishable by content and digest.
func skillBundleFor(content string) []byte {
	return []byte(fmt.Sprintf(`{"entry":"SKILL.md","files":[{"path":"SKILL.md","content":%q}],"license":{"name":"MIT"}}`, content))
}

func skillContractFor() []byte {
	return []byte(`{"capabilities":["authoring"],"runtime":{"interactive":false}}`)
}

// createSkillFor creates a skill and returns its id.
func createSkillFor(t *testing.T, ctx context.Context, pool *pgxpool.Pool, caller *UserRecord, name string) string {
	t.Helper()
	detail, aerr := CreateSkill(ctx, pool, caller, CreateSkillRequest{Name: name})
	require.Nil(t, aerr)
	return detail.ID
}

// publishFor publishes the next version with the given expected-latest and
// content, requiring success.
func publishFor(t *testing.T, ctx context.Context, pool *pgxpool.Pool, caller *UserRecord, skillID string, expected int, content string) *SkillVersionSummary {
	t.Helper()
	s, aerr := PublishSkillVersion(ctx, pool, caller, PublishSkillVersionRequest{
		SkillID:        skillID,
		ExpectedLatest: expected,
		Bundle:         skillBundleFor(content),
		Contract:       skillContractFor(),
	})
	require.Nil(t, aerr)
	return s
}

// TestSkillRegistryPublishCASAndRetention covers the publication contract:
// contiguous integer versions under the expected-latest CAS, the 409 on a
// stale expected value (with the current latest disclosed in details, so the
// caller can retry), and retention — a pinned old version's bytes never
// change when later versions publish.
func TestSkillRegistryPublishCASAndRetention(t *testing.T) {
	pool := setupSkillRegistryDB(t)
	ctx := context.Background()
	owner := skillUser(t, pool, "owner")
	skillCleanup(t, pool, owner)
	defer skillCleanup(t, pool, owner)
	caller := &UserRecord{ID: owner, Role: "writer"}

	skill := createSkillFor(t, ctx, pool, caller, "grill-me")

	// A fresh skill has zero versions and the owner sees it (with no latest
	// accessible version yet).
	detail, aerr := GetSkill(ctx, pool, caller, skill)
	require.Nil(t, aerr)
	require.Equal(t, 0, detail.LatestVersion)
	require.Nil(t, detail.LatestAccessible, "a skill with no versions has no accessible version")

	v1 := publishFor(t, ctx, pool, caller, skill, 0, "v1 body")
	require.Equal(t, 1, v1.Version)
	require.Equal(t, "private", v1.Visibility, "every new version publishes private (D1)")

	// Stale expected_latest: 409, with the current value disclosed in details.
	_, aerr = PublishSkillVersion(ctx, pool, caller, PublishSkillVersionRequest{
		SkillID: skill, ExpectedLatest: 0,
		Bundle: skillBundleFor("v1 again"), Contract: skillContractFor(),
	})
	require.NotNil(t, aerr)
	require.Equal(t, ErrConflictCASFailed, aerr.Code)
	require.Equal(t, map[string]any{"expected_latest": 0, "current_latest": 1}, aerr.Details)

	v2 := publishFor(t, ctx, pool, caller, skill, 1, "v2 body")
	require.Equal(t, 2, v2.Version)

	// RETENTION + immutability: the pinned v1 is still readable and its
	// bytes and digest are unchanged by v2's publication.
	got1, aerr := GetSkillVersion(ctx, pool, caller, skill, 1)
	require.Nil(t, aerr)
	require.Equal(t, v1.Digest, got1.Digest)
	require.Contains(t, string(got1.Bundle), "v1 body")
	require.Equal(t, 1, got1.Version)

	// latest_version advanced, and the owner is told the total.
	detail, aerr = GetSkill(ctx, pool, caller, skill)
	require.Nil(t, aerr)
	require.Equal(t, 2, detail.LatestVersion)
	require.NotNil(t, detail.LatestAccessible)
	require.Equal(t, 2, detail.LatestAccessible.Version)

	// Duplicate names for one owner are refused; another owner's same name
	// is fine (the key is per owner).
	_, aerr = CreateSkill(ctx, pool, caller, CreateSkillRequest{Name: "grill-me"})
	require.NotNil(t, aerr)
	require.Equal(t, ErrConflictDuplicate, aerr.Code)
	stranger := &UserRecord{ID: skillUser(t, pool, "stranger"), Role: "writer"}
	skillCleanup(t, pool, stranger.ID)
	defer skillCleanup(t, pool, stranger.ID)
	other, aerr := CreateSkill(ctx, pool, stranger, CreateSkillRequest{Name: "grill-me"})
	require.Nil(t, aerr)
	require.NotEqual(t, skill, other.ID)
}

// TestSkillRegistryConcurrentPublishExactlyOneWins drives the Requirement's
// "two publishers read latest=N … at most one publish succeeds": N concurrent
// PublishSkillVersion calls, all with the same expected_latest, against one
// skills row. The FOR UPDATE row lock serializes them; exactly one wins and
// the rest answer 409 CONFLICT_CAS_FAILED.
func TestSkillRegistryConcurrentPublishExactlyOneWins(t *testing.T) {
	pool := setupSkillRegistryDB(t)
	ctx := context.Background()
	owner := skillUser(t, pool, "owner")
	skillCleanup(t, pool, owner)
	defer skillCleanup(t, pool, owner)
	caller := &UserRecord{ID: owner, Role: "writer"}

	skill := createSkillFor(t, ctx, pool, caller, "racer")
	publishFor(t, ctx, pool, caller, skill, 0, "v1")

	const n = 6
	var wg sync.WaitGroup
	results := make([]*SkillVersionSummary, n)
	errs := make([]*AihubError, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			results[idx], errs[idx] = PublishSkillVersion(ctx, pool, caller, PublishSkillVersionRequest{
				SkillID:        skill,
				ExpectedLatest: 1, // every goroutine read "latest is 1"
				Bundle:         skillBundleFor(fmt.Sprintf("racer %d", idx)),
				Contract:       skillContractFor(),
			})
		}(i)
	}
	wg.Wait()

	wins, conflicts := 0, 0
	for i, aerr := range errs {
		switch {
		case aerr == nil:
			wins++
			require.Equal(t, 2, results[i].Version, "the single winner must have created version 2")
		case aerr.Code == ErrConflictCASFailed:
			conflicts++
		default:
			t.Fatalf("goroutine %d: unexpected error %v", i, aerr)
		}
	}
	require.Equal(t, 1, wins, "exactly one publish may win the CAS")
	require.Equal(t, n-1, conflicts, "every other publisher must answer 409 CONFLICT_CAS_FAILED")

	// And the row advanced exactly once.
	detail, aerr := GetSkill(ctx, pool, caller, skill)
	require.Nil(t, aerr)
	require.Equal(t, 2, detail.LatestVersion)
}

// TestSkillRegistrySharingAndLatestAccessible is the spec's own
// "Publishing and sharing" scenario: v1 is shared with a project and v2 is
// published private afterwards; a project member's latest-ACCESSIBLE query
// still selects v1, v2 is not disclosed to them at all, and an existing
// pinned v1 reference is unchanged.
func TestSkillRegistrySharingAndLatestAccessible(t *testing.T) {
	pool := setupSkillRegistryDB(t)
	ctx := context.Background()
	ownerID := skillUser(t, pool, "owner")
	memberID := skillUser(t, pool, "member")
	owner := &UserRecord{ID: ownerID, Role: "writer"}
	member := &UserRecord{ID: memberID, Role: "writer"}
	skillCleanup(t, pool, ownerID, memberID)
	defer skillCleanup(t, pool, ownerID, memberID)

	proj := testProject(t, pool, ownerID)
	mustExec(t, pool, `UPDATE projects SET members='[{"user_id":"`+memberID+`","role":"viewer"}]'::jsonb WHERE name='`+proj+`'`)

	skill := createSkillFor(t, ctx, pool, owner, "spec-flow")
	publishFor(t, ctx, pool, owner, skill, 0, "shared v1")

	// Share v1 with the project.
	aerr := ShareSkillVersionWithProject(ctx, pool, owner, SkillVersionShareRequest{
		SkillID: skill, Version: 1, Project: proj,
	})
	require.Nil(t, aerr)

	// The member can read v1 and only v1.
	got, aerr := GetSkillVersion(ctx, pool, member, skill, 1)
	require.Nil(t, aerr)
	require.Contains(t, string(got.Bundle), "shared v1")

	// NOW v2 is published privately. It must not leak to the member in any
	// shape: not through the latest-accessible query, not by version fetch.
	publishFor(t, ctx, pool, owner, skill, 1, "private v2")

	detail, aerr := GetSkill(ctx, pool, member, skill)
	require.Nil(t, aerr)
	require.NotNil(t, detail.LatestAccessible, "the member still sees v1")
	require.Equal(t, 1, detail.LatestAccessible.Version,
		"the member's latest ACCESSIBLE version is v1; v2 must not be selected or disclosed")
	require.Equal(t, 0, detail.LatestVersion,
		"the member must not be told the total version count (no metadata oracle)")

	_, aerr = GetSkillVersion(ctx, pool, member, skill, 2)
	require.NotNil(t, aerr)
	require.Equal(t, ErrNotFound, aerr.Code, "v2 must answer NOT_FOUND, never FORBIDDEN (no oracle)")

	versions, aerr := ListSkillVersions(ctx, pool, member, skill)
	require.Nil(t, aerr)
	require.Len(t, versions, 1, "the member's version list is exactly [v1]")
	require.Equal(t, 1, versions[0].Version)

	// The pinned v1 reference is unchanged: same digest, same bytes.
	pinned, aerr := GetSkillVersion(ctx, pool, member, skill, 1)
	require.Nil(t, aerr)
	require.Equal(t, got.Digest, pinned.Digest)
	require.Contains(t, string(pinned.Bundle), "shared v1")

	// The owner sees both, and their latest-accessible is the true latest.
	ownerDetail, aerr := GetSkill(ctx, pool, owner, skill)
	require.Nil(t, aerr)
	require.Equal(t, 2, ownerDetail.LatestVersion)
	require.Equal(t, 2, ownerDetail.LatestAccessible.Version)

	// Sharing v2 explicitly is what makes it visible — and it changes
	// nothing retroactively for v1.
	require.Nil(t, ShareSkillVersionWithProject(ctx, pool, owner, SkillVersionShareRequest{
		SkillID: skill, Version: 2, Project: proj,
	}))
	detail, aerr = GetSkill(ctx, pool, member, skill)
	require.Nil(t, aerr)
	require.Equal(t, 2, detail.LatestAccessible.Version, "after sharing v2 the member's latest accessible is v2")
	versions, aerr = ListSkillVersions(ctx, pool, member, skill)
	require.Nil(t, aerr)
	require.Len(t, versions, 2)
}

// TestSkillRegistryRevocationAndAuthRecheck covers the spec's "revoked access"
// half: a revoked project grant and a public→private flip each block the very
// NEXT fetch (auth recheck on every read), while idempotent re-shares and
// re-revokes succeed.
func TestSkillRegistryRevocationAndAuthRecheck(t *testing.T) {
	pool := setupSkillRegistryDB(t)
	ctx := context.Background()
	ownerID := skillUser(t, pool, "owner")
	memberID := skillUser(t, pool, "member")
	strangerID := skillUser(t, pool, "stranger")
	owner := &UserRecord{ID: ownerID, Role: "writer"}
	member := &UserRecord{ID: memberID, Role: "writer"}
	stranger := &UserRecord{ID: strangerID, Role: "writer"}
	skillCleanup(t, pool, ownerID, memberID, strangerID)
	defer skillCleanup(t, pool, ownerID, memberID, strangerID)

	proj := testProject(t, pool, ownerID)
	mustExec(t, pool, `UPDATE projects SET members='[{"user_id":"`+memberID+`","role":"viewer"}]'::jsonb WHERE name='`+proj+`'`)

	skill := createSkillFor(t, ctx, pool, owner, "revoker")
	publishFor(t, ctx, pool, owner, skill, 0, "v1")
	share := SkillVersionShareRequest{SkillID: skill, Version: 1, Project: proj}
	require.Nil(t, ShareSkillVersionWithProject(ctx, pool, owner, share))
	// Re-sharing is idempotent.
	require.Nil(t, ShareSkillVersionWithProject(ctx, pool, owner, share))

	_, aerr := GetSkillVersion(ctx, pool, member, skill, 1)
	require.Nil(t, aerr)

	// Revoke: the member's NEXT fetch is refused.
	require.Nil(t, RevokeSkillVersionFromProject(ctx, pool, owner, share))
	// Re-revoking is idempotent.
	require.Nil(t, RevokeSkillVersionFromProject(ctx, pool, owner, share))
	_, aerr = GetSkillVersion(ctx, pool, member, skill, 1)
	require.NotNil(t, aerr)
	require.Equal(t, ErrNotFound, aerr.Code)

	// Public sharing: a stranger can read; flipping back to private refuses
	// the next read. (Public is authenticated distribution — the stranger
	// here is an authenticated writer.)
	_, aerr = SetSkillVersionVisibility(ctx, pool, owner, skill, 1, "public")
	require.Nil(t, aerr)
	_, aerr = GetSkillVersion(ctx, pool, stranger, skill, 1)
	require.Nil(t, aerr, "public means any authenticated caller may read")

	_, aerr = SetSkillVersionVisibility(ctx, pool, owner, skill, 1, "private")
	require.Nil(t, aerr)
	_, aerr = GetSkillVersion(ctx, pool, stranger, skill, 1)
	require.NotNil(t, aerr)
	require.Equal(t, ErrNotFound, aerr.Code, "the private flip must refuse the NEXT read (auth recheck)")

	// A second project's grant is independent: revoking proj's grant never
	// touched it (each version is shared per project). The name is derived
	// through testname.Sanitize with a distinct input so the hash keeps it
	// from colliding with proj while staying inside the 40-char CHECK.
	proj2 := "p_" + testname.Sanitize(t.Name()+" Two")
	mustExec(t, pool, `INSERT INTO projects(name,owner_user_id) VALUES('`+proj2+`','`+ownerID+`') ON CONFLICT (name) DO NOTHING`)
	require.Nil(t, ShareSkillVersionWithProject(ctx, pool, owner, SkillVersionShareRequest{
		SkillID: skill, Version: 1, Project: proj2,
	}))
	_, aerr = GetSkillVersion(ctx, pool, owner, skill, 1)
	require.Nil(t, aerr)
}

// TestSkillRegistryScopedKeysStayScoped pins rule 4: a project-scoped API key
// reads only versions granted to the scoped project (where it holds rights),
// never public versions, never its own user's private skills, and it cannot
// write anything. The scope must not be escaped through the registry.
func TestSkillRegistryScopedKeysStayScoped(t *testing.T) {
	pool := setupSkillRegistryDB(t)
	ctx := context.Background()
	ownerID := skillUser(t, pool, "owner")
	memberID := skillUser(t, pool, "member")
	skillCleanup(t, pool, ownerID, memberID)
	defer skillCleanup(t, pool, ownerID, memberID)
	owner := &UserRecord{ID: ownerID, Role: "writer"}
	unscopedMember := &UserRecord{ID: memberID, Role: "writer"}

	proj := testProject(t, pool, ownerID)
	mustExec(t, pool, `UPDATE projects SET members='[{"user_id":"`+memberID+`","role":"viewer"}]'::jsonb WHERE name='`+proj+`'`)
	scopedMember := &UserRecord{ID: memberID, Role: "writer", ProjectScope: &proj}
	scopedOwner := &UserRecord{ID: ownerID, Role: "writer", ProjectScope: &proj}

	skill := createSkillFor(t, ctx, pool, owner, "scoped-probe")
	publishFor(t, ctx, pool, owner, skill, 0, "granted v1")
	publishFor(t, ctx, pool, owner, skill, 1, "private v2")
	require.Nil(t, ShareSkillVersionWithProject(ctx, pool, owner, SkillVersionShareRequest{
		SkillID: skill, Version: 1, Project: proj,
	}))
	// Make v3 PUBLIC: a scoped key still must not read it.
	publishFor(t, ctx, pool, owner, skill, 2, "public v3")
	_, aerr := SetSkillVersionVisibility(ctx, pool, owner, skill, 3, "public")
	require.Nil(t, aerr)

	// The scoped MEMBER sees exactly the granted v1 through the project.
	got, aerr := GetSkillVersion(ctx, pool, scopedMember, skill, 1)
	require.Nil(t, aerr)
	require.Contains(t, string(got.Bundle), "granted v1")
	for _, v := range []int{2, 3} {
		_, aerr = GetSkillVersion(ctx, pool, scopedMember, skill, v)
		require.NotNil(t, aerr, "scoped key read v%d", v)
		require.Equal(t, ErrNotFound, aerr.Code)
	}

	// The scoped OWNER of the skill: the own-skills arm is CLOSED to scoped
	// keys, so their private v2 and even the public v3 are out of reach —
	// but they DO see the granted v1, because they are the target project's
	// owner and that is the grant path, not the own-skills path. The skill
	// identity is therefore visible to them exactly as to any other project
	// member, and never through ownership.
	ownerViaScope, aerr := GetSkill(ctx, pool, scopedOwner, skill)
	require.Nil(t, aerr)
	require.NotNil(t, ownerViaScope.LatestAccessible)
	require.Equal(t, 1, ownerViaScope.LatestAccessible.Version,
		"the scoped owner sees only the granted version")
	require.Equal(t, 0, ownerViaScope.LatestVersion,
		"no total-count disclosure through a scoped key")
	for _, v := range []int{2, 3} {
		_, aerr = GetSkillVersion(ctx, pool, scopedOwner, skill, v)
		require.NotNil(t, aerr, "scoped owner read own private/public v%d — rule 4 broken", v)
		require.Equal(t, ErrNotFound, aerr.Code)
	}

	// The scoped key's list contains only what the scoped project's grants
	// reach.
	list, aerr := ListSkills(ctx, pool, scopedMember, ListSkillsRequest{})
	require.Nil(t, aerr)
	require.Len(t, list, 1, "the scoped member list holds only the skill granted to the scoped project")
	require.Equal(t, skill, list[0].ID)
	require.Equal(t, 1, list[0].LatestAccessible.Version)
	require.Equal(t, 0, list[0].LatestVersion, "no version-count disclosure through a scoped key")

	// Writes: every one is a 403, before anything is read or written.
	_, aerr = CreateSkill(ctx, pool, scopedMember, CreateSkillRequest{Name: "escape-attempt"})
	require.Equal(t, ErrForbidden, aerr.Code)
	_, aerr = PublishSkillVersion(ctx, pool, scopedMember, PublishSkillVersionRequest{
		SkillID: skill, ExpectedLatest: 2, Bundle: skillBundleFor("x"), Contract: skillContractFor(),
	})
	require.Equal(t, ErrForbidden, aerr.Code)
	aerr = ShareSkillVersionWithProject(ctx, pool, scopedMember, SkillVersionShareRequest{
		SkillID: skill, Version: 1, Project: proj,
	})
	require.Equal(t, ErrForbidden, aerr.Code)
	aerr = RevokeSkillVersionFromProject(ctx, pool, scopedMember, SkillVersionShareRequest{
		SkillID: skill, Version: 1, Project: proj,
	})
	require.Equal(t, ErrForbidden, aerr.Code)
	_, aerr = SetSkillVersionVisibility(ctx, pool, scopedMember, skill, 1, "public")
	require.Equal(t, ErrForbidden, aerr.Code)

	// The unscoped member sees the public v3 too — the confinement above is
	// the SCOPE's doing, not the caller's.
	got, aerr = GetSkillVersion(ctx, pool, unscopedMember, skill, 3)
	require.Nil(t, aerr)
	require.Contains(t, string(got.Bundle), "public v3")
}

// TestSkillRegistryAuthorizationAndAdmin covers the write-path authorization
// matrix: only the owner or an unscoped admin may publish/share; a stranger
// gets NOT_FOUND (never FORBIDDEN — no oracle); sharing into a project
// requires real project rights; and the admin path is complete.
func TestSkillRegistryAuthorizationAndAdmin(t *testing.T) {
	pool := setupSkillRegistryDB(t)
	ctx := context.Background()
	ownerID := skillUser(t, pool, "owner")
	strangerID := skillUser(t, pool, "stranger")
	adminID := skillUser(t, pool, "admin")
	viewerID := skillUser(t, pool, "viewer")
	skillCleanup(t, pool, ownerID, strangerID, adminID, viewerID)
	defer skillCleanup(t, pool, ownerID, strangerID, adminID, viewerID)
	owner := &UserRecord{ID: ownerID, Role: "writer"}
	stranger := &UserRecord{ID: strangerID, Role: "writer"}
	admin := &UserRecord{ID: adminID, Role: "admin"}

	proj := testProject(t, pool, ownerID)
	// The stranger is NOT a member of proj.

	skill := createSkillFor(t, ctx, pool, owner, "authz")
	publishFor(t, ctx, pool, owner, skill, 0, "v1")

	// A stranger cannot publish on the owner's skill — and the answer is
	// NOT_FOUND, the no-oracle code (the skill's existence stays undisclosed).
	_, aerr := PublishSkillVersion(ctx, pool, stranger, PublishSkillVersionRequest{
		SkillID: skill, ExpectedLatest: 0, Bundle: skillBundleFor("hijack"), Contract: skillContractFor(),
	})
	require.NotNil(t, aerr)
	require.Equal(t, ErrNotFound, aerr.Code)

	// A stranger cannot share, revoke or flip visibility either.
	aerr = ShareSkillVersionWithProject(ctx, pool, stranger, SkillVersionShareRequest{
		SkillID: skill, Version: 1, Project: proj,
	})
	require.Equal(t, ErrNotFound, aerr.Code)
	aerr = RevokeSkillVersionFromProject(ctx, pool, stranger, SkillVersionShareRequest{
		SkillID: skill, Version: 1, Project: proj,
	})
	require.Equal(t, ErrNotFound, aerr.Code)
	_, aerr = SetSkillVersionVisibility(ctx, pool, stranger, skill, 1, "public")
	require.Equal(t, ErrNotFound, aerr.Code)

	// Sharing into a project that does not exist is the project gate's 404.
	aerr = ShareSkillVersionWithProject(ctx, pool, owner, SkillVersionShareRequest{
		SkillID: skill, Version: 1, Project: "p_does_not_exist_" + testname.Sanitize(t.Name()),
	})
	require.NotNil(t, aerr)
	require.Equal(t, ErrProjectNotFound, aerr.Code)

	// The skill owner IS the project owner here, so sharing into proj works.
	require.Nil(t, ShareSkillVersionWithProject(ctx, pool, owner, SkillVersionShareRequest{
		SkillID: skill, Version: 1, Project: proj,
	}))

	// An UNRELATED writer who is a viewer MEMBER of the project can share
	// their own skills into it.
	mustExec(t, pool, `UPDATE projects SET members='[{"user_id":"`+viewerID+`","role":"viewer"}]'::jsonb WHERE name='`+proj+`'`)
	viewer := &UserRecord{ID: viewerID, Role: "writer"}
	memberSkill := createSkillFor(t, ctx, pool, viewer, "member-skill")
	publishFor(t, ctx, pool, viewer, memberSkill, 0, "v1")
	require.Nil(t, ShareSkillVersionWithProject(ctx, pool, viewer, SkillVersionShareRequest{
		SkillID: memberSkill, Version: 1, Project: proj,
	}), "a viewer member holds proper project rights for sharing into the project")

	// A writer who is NOT a member of the project cannot share into it, even
	// though the project is visible=true (public): bare view rights are not
	// "proper project rights" for a share target (spec D3).
	mustExec(t, pool, `UPDATE projects SET visible=true, members='[]'::jsonb WHERE name='`+proj+`'`)
	strangerSkill := createSkillFor(t, ctx, pool, stranger, "stranger-skill")
	publishFor(t, ctx, pool, stranger, strangerSkill, 0, "v1")
	aerr = ShareSkillVersionWithProject(ctx, pool, stranger, SkillVersionShareRequest{
		SkillID: strangerSkill, Version: 1, Project: proj,
	})
	require.NotNil(t, aerr)
	require.Equal(t, ErrProjectAccessDenied, aerr.Code,
		"a non-member of a visible project must not share into it; membership is the requirement")

	// The GLOBAL ADMIN: sees every skill, is told the total count, may
	// publish on the owner's skill, and the admin's publishes respect the
	// same CAS.
	adminDetail, aerr := GetSkill(ctx, pool, admin, skill)
	require.Nil(t, aerr)
	require.Equal(t, 1, adminDetail.LatestVersion, "the admin is told the total count")
	adminV, aerr := PublishSkillVersion(ctx, pool, admin, PublishSkillVersionRequest{
		SkillID: skill, ExpectedLatest: 1, Bundle: skillBundleFor("admin v2"), Contract: skillContractFor(),
	})
	require.Nil(t, aerr)
	require.Equal(t, 2, adminV.Version)

	// NIL caller is refused everywhere.
	_, aerr = GetSkill(ctx, pool, nil, skill)
	require.Equal(t, ErrUnauthorized, aerr.Code)
	_, aerr = ListSkills(ctx, pool, nil, ListSkillsRequest{})
	require.Equal(t, ErrUnauthorized, aerr.Code)
	aerr = ShareSkillVersionWithProject(ctx, pool, nil, SkillVersionShareRequest{})
	require.Equal(t, ErrUnauthorized, aerr.Code)
}

// TestSkillRegistryNoMetadataOracle pins rule 2 end to end against the live
// predicate: a wholly-private skill is indistinguishable from a missing one
// for a stranger (same code, same message), the list omits it, and the only
// count disclosure is to the owner and admins.
func TestSkillRegistryNoMetadataOracle(t *testing.T) {
	pool := setupSkillRegistryDB(t)
	ctx := context.Background()
	ownerID := skillUser(t, pool, "owner")
	strangerID := skillUser(t, pool, "stranger")
	adminID := skillUser(t, pool, "admin")
	skillCleanup(t, pool, ownerID, strangerID, adminID)
	defer skillCleanup(t, pool, ownerID, strangerID, adminID)
	owner := &UserRecord{ID: ownerID, Role: "writer"}
	stranger := &UserRecord{ID: strangerID, Role: "writer"}
	admin := &UserRecord{ID: adminID, Role: "admin"}

	private := createSkillFor(t, ctx, pool, owner, "private-skill")
	publishFor(t, ctx, pool, owner, private, 0, "secret v1")
	publishFor(t, ctx, pool, owner, private, 1, "secret v2")

	// The stranger's view of the private skill equals their view of a
	// nonexistent skill: same code, same message.
	_, hidden := GetSkill(ctx, pool, stranger, private)
	_, missing := GetSkill(ctx, pool, stranger, "skill_missing_")
	require.NotNil(t, hidden)
	require.NotNil(t, missing)
	require.Equal(t, missing.Code, hidden.Code)
	require.Equal(t, missing.Message, hidden.Message)
	require.Equal(t, ErrNotFound, hidden.Code)

	_, aerr := GetSkillVersion(ctx, pool, stranger, private, 1)
	require.Equal(t, ErrNotFound, aerr.Code)
	_, aerr = ListSkillVersions(ctx, pool, stranger, private)
	require.Equal(t, ErrNotFound, aerr.Code,
		"an empty version list would confirm the skill exists; it must be NOT_FOUND")

	// The stranger's list contains no trace of the private skill.
	list, aerr := ListSkills(ctx, pool, stranger, ListSkillsRequest{})
	require.Nil(t, aerr)
	for _, item := range list {
		require.NotEqual(t, private, item.ID, "the private skill leaked into the stranger's list")
	}

	// The owner and the admin ARE told the count (2); that is the whole
	// disclosure and it is theirs by right.
	ownerDetail, aerr := GetSkill(ctx, pool, owner, private)
	require.Nil(t, aerr)
	require.Equal(t, 2, ownerDetail.LatestVersion)
	adminDetail, aerr := GetSkill(ctx, pool, admin, private)
	require.Nil(t, aerr)
	require.Equal(t, 2, adminDetail.LatestVersion)
}

// TestSkillRegistryListPaginationAndOwnerFilter exercises the list contract:
// ordered pages by skill id, the owner filter, and the refused (not clamped)
// out-of-range limit.
func TestSkillRegistryListPaginationAndOwnerFilter(t *testing.T) {
	pool := setupSkillRegistryDB(t)
	ctx := context.Background()
	ownerID := skillUser(t, pool, "owner")
	skillCleanup(t, pool, ownerID)
	defer skillCleanup(t, pool, ownerID)
	owner := &UserRecord{ID: ownerID, Role: "writer"}

	// Three skills; ids are generated, so capture them and sort by what the
	// query orders on (s.id) to know the page boundaries.
	var ids []string
	for _, name := range []string{"page-a", "page-b", "page-c"} {
		ids = append(ids, createSkillFor(t, ctx, pool, owner, name))
	}
	sortStrings(ids)

	page1, aerr := ListSkills(ctx, pool, owner, ListSkillsRequest{Limit: 2})
	require.Nil(t, aerr)
	require.Len(t, page1, 2)
	require.Equal(t, ids[0], page1[0].ID)
	require.Equal(t, ids[1], page1[1].ID)

	page2, aerr := ListSkills(ctx, pool, owner, ListSkillsRequest{Limit: 2, Cursor: page1[len(page1)-1].ID})
	require.Nil(t, aerr)
	require.Len(t, page2, 1)
	require.Equal(t, ids[2], page2[0].ID)

	// The owner filter intersects with visibility: filtering to the owner
	// returns all three; filtering to a user with nothing visible returns an
	// empty page, not an error and not a disclosure.
	owned, aerr := ListSkills(ctx, pool, owner, ListSkillsRequest{Owner: &ownerID})
	require.Nil(t, aerr)
	require.Len(t, owned, 3)
	missing := "u_nobody_" + testname.Sanitize(t.Name())
	filtered, aerr := ListSkills(ctx, pool, owner, ListSkillsRequest{Owner: &missing})
	require.Nil(t, aerr)
	require.Empty(t, filtered)
}

// ─── aihub#708 Batch 1A repair: digest reproducibility over a real JSONB roundtrip ──

// TestSkillRegistryDigestReproducesAfterJSONBRoundtrip is finding 1's control:
// publish stores the CANONICAL bundle/contract bytes plus the digest over them;
// PostgreSQL jsonb then re-serializes those bytes its own way (key order by
// length-then-bytes, spacing, numbers via numeric). A fetch returns the
// re-serialized bytes, and re-computing the digest over what CAME BACK must
// reproduce the digest stored at publish time — the recomputation runs the
// real decode+canonicalize path on the fetched bytes, not on the request.
//
// The contract deliberately carries every spelling the canonicalizer folds:
// reordered keys, 1.0 vs 1, 0.50, 1e2, a big exact integer, an object enum
// member written out of key order.
func TestSkillRegistryDigestReproducesAfterJSONBRoundtrip(t *testing.T) {
	pool := setupSkillRegistryDB(t)
	ctx := context.Background()
	owner := skillUser(t, pool, "owner")
	skillCleanup(t, pool, owner)
	defer skillCleanup(t, pool, owner)
	caller := &UserRecord{ID: owner, Role: "writer"}

	contract := []byte(`{
		"capabilities": ["deterministic_operation"],
		"input_schema": {
			"type": "object",
			"properties": {
				"query":  {"type": "string", "minLength": 1},
				"weight": {"type": "number", "minimum": 0.50, "maximum": 1e2},
				"mode":   {"enum": [1.0, "fast", {"b": 2, "a": 1.50}]},
				"ref":    {"enum": [123456789012345678901234567890, 123456789012345678901234567891]}
			},
			"required": ["query", "mode"],
			"additionalProperties": false
		},
		"runtime": {"interactive": false}
	}`)
	bundle := skillBundleFor("digest roundtrip body")

	skill := createSkillFor(t, ctx, pool, caller, "digest-roundtrip")
	published, aerr := PublishSkillVersion(ctx, pool, caller, PublishSkillVersionRequest{
		SkillID: skill, ExpectedLatest: 0, Bundle: bundle, Contract: contract,
	})
	require.Nil(t, aerr)

	// What we would have stored: the canonical bytes computed from the
	// request (the exact input PublishSkillVersion canonicalized and wrote).
	storedContract, err := skillregistry.DecodeContract(contract)
	require.Nil(t, err)
	storedContractJSON, err := skillregistry.CanonicalContractJSON(storedContract)
	require.Nil(t, err)
	storedBundle, err := skillregistry.DecodeBundle(bundle)
	require.Nil(t, err)
	storedBundleJSON, err := skillregistry.CanonicalBundleJSON(storedBundle)
	require.Nil(t, err)
	require.Equal(t, published.Digest, skillregistry.VersionDigest(storedBundleJSON, storedContractJSON),
		"the published digest must be over the canonical bytes that were stored")

	// Fetch through the real column: pgx hands back PostgreSQL's jsonb text,
	// which is NOT the canonical form (different key order, added spaces).
	// If the fetched bytes were identical to the canonical bytes, this test
	// would be vacuous — it would never have exercised the roundtrip.
	fetched, aerr := GetSkillVersion(ctx, pool, caller, skill, published.Version)
	require.Nil(t, aerr)
	require.NotEqual(t, string(storedContractJSON), string(fetched.Contract),
		"the fetched contract should carry jsonb's own serialization, not ours — otherwise this control proves nothing")

	// THE ASSERTION: recompute the digest over the fetched bytes via the same
	// decode+canonicalize path any later audit or Batch 4A seed/import runs,
	// and it must reproduce the digest stored at publish time.
	fetchedBundle, err := skillregistry.DecodeBundle(fetched.Bundle)
	require.Nil(t, err)
	fetchedBundleJSON, err := skillregistry.CanonicalBundleJSON(fetchedBundle)
	require.Nil(t, err)
	fetchedContract, err := skillregistry.DecodeContract(fetched.Contract)
	require.Nil(t, err)
	fetchedContractJSON, err := skillregistry.CanonicalContractJSON(fetchedContract)
	require.Nil(t, err)
	require.Equal(t, published.Digest, skillregistry.VersionDigest(fetchedBundleJSON, fetchedContractJSON),
		"the digest must reproduce from the bytes PostgreSQL returns: canonicalization must be a function of the JSON value, not of the spelling")

	// And the exact numbers survived the numeric column: the big integer
	// enum members round-trip without float64 loss (their distinctness is
	// the proof — a float64 hop would have collapsed or re-spelled them).
	require.Contains(t, string(fetchedContractJSON), "123456789012345678901234567890")
	require.Contains(t, string(fetchedContractJSON), "123456789012345678901234567891")
	require.Equal(t, string(storedContractJSON), string(fetchedContractJSON))
}

// TestSkillRegistryNearMaxBundleSurvivesJSONBWhitespaceExpansion publishes a
// bundle just below MaxBundleBytes using several files (each remains beneath
// MaxFileBytes), then proves PostgreSQL's larger whitespace-bearing JSONB
// rendering can still be decoded, canonicalized, and digested identically.
func TestSkillRegistryNearMaxBundleSurvivesJSONBWhitespaceExpansion(t *testing.T) {
	pool := setupSkillRegistryDB(t)
	ctx := context.Background()
	owner := skillUser(t, pool, "owner")
	skillCleanup(t, pool, owner)
	defer skillCleanup(t, pool, owner)
	caller := &UserRecord{ID: owner, Role: "writer"}

	bundle := skillregistry.SkillBundle{
		Entry: "SKILL.md",
		Files: []skillregistry.SkillFile{
			{Path: "SKILL.md", Content: strings.Repeat("x", skillregistry.MaxFileBytes-1024)},
			{Path: "docs/one.md", Content: strings.Repeat("x", skillregistry.MaxFileBytes-1024)},
			{Path: "docs/two.md", Content: strings.Repeat("x", skillregistry.MaxFileBytes-1024)},
			{Path: "docs/three.md", Content: strings.Repeat("x", skillregistry.MaxFileBytes-1024)},
		},
		License: skillregistry.BundleLicense{Name: "MIT"},
	}
	const headroom = 1
	for {
		canonical, err := skillregistry.CanonicalBundleJSON(&bundle)
		require.NoError(t, err)
		remaining := skillregistry.MaxBundleBytes - headroom - len(canonical)
		if remaining == 0 {
			break
		}
		require.Positive(t, remaining, "initial bundle unexpectedly exceeds near-limit target")
		for i := range bundle.Files {
			room := skillregistry.MaxFileBytes - len(bundle.Files[i].Content)
			if room == 0 {
				continue
			}
			add := remaining
			if add > room {
				add = room
			}
			bundle.Files[i].Content += strings.Repeat("x", add)
			remaining -= add
			if remaining == 0 {
				break
			}
		}
	}
	bundleJSON, err := skillregistry.CanonicalBundleJSON(&bundle)
	require.NoError(t, err)
	require.Equal(t, skillregistry.MaxBundleBytes-headroom, len(bundleJSON))

	skill := createSkillFor(t, ctx, pool, caller, "near-max-jsonb")
	published, aerr := PublishSkillVersion(ctx, pool, caller, PublishSkillVersionRequest{
		SkillID: skill, ExpectedLatest: 0, Bundle: bundleJSON, Contract: skillContractFor(),
	})
	require.Nil(t, aerr)

	fetched, aerr := GetSkillVersion(ctx, pool, caller, skill, published.Version)
	require.Nil(t, aerr)
	require.Greater(t, len(fetched.Bundle), skillregistry.MaxBundleBytes,
		"negative control: fetched JSONB must cross the old raw-size limit")
	fetchedBundle, err := skillregistry.DecodeBundle(fetched.Bundle)
	require.NoError(t, err)
	fetchedBundleJSON, err := skillregistry.CanonicalBundleJSON(fetchedBundle)
	require.NoError(t, err)
	fetchedContract, err := skillregistry.DecodeContract(fetched.Contract)
	require.NoError(t, err)
	fetchedContractJSON, err := skillregistry.CanonicalContractJSON(fetchedContract)
	require.NoError(t, err)
	require.Equal(t, string(bundleJSON), string(fetchedBundleJSON))
	require.Equal(t, published.Digest, skillregistry.VersionDigest(fetchedBundleJSON, fetchedContractJSON))
}

// TestSkillRegistryPrivatePublishDoesNotLeakIdentityMetadata is finding 4's
// control: skills.updated_at moves on every publish, including private ones.
// A project member who can read shared v1 must not be able to observe the
// owner publishing a private v2 through any field of the skill identity the
// member can see — updated_at must be derived from the accessible version.
//
// The raw DB row is the control for the leak itself: it DOES move, and the
// member's view must not.
func TestSkillRegistryPrivatePublishDoesNotLeakIdentityMetadata(t *testing.T) {
	pool := setupSkillRegistryDB(t)
	ctx := context.Background()
	ownerID := skillUser(t, pool, "owner")
	memberID := skillUser(t, pool, "member")
	owner := &UserRecord{ID: ownerID, Role: "writer"}
	member := &UserRecord{ID: memberID, Role: "writer"}
	skillCleanup(t, pool, ownerID, memberID)
	defer skillCleanup(t, pool, ownerID, memberID)

	proj := testProject(t, pool, ownerID)
	mustExec(t, pool, `UPDATE projects SET members='[{"user_id":"`+memberID+`","role":"viewer"}]'::jsonb WHERE name='`+proj+`'`)

	skill := createSkillFor(t, ctx, pool, owner, "timing-leak")
	publishFor(t, ctx, pool, owner, skill, 0, "shared v1")
	require.Nil(t, ShareSkillVersionWithProject(ctx, pool, owner, SkillVersionShareRequest{
		SkillID: skill, Version: 1, Project: proj,
	}))

	// The member's view of the identity BEFORE the private publish.
	before, aerr := GetSkill(ctx, pool, member, skill)
	require.Nil(t, aerr)
	require.NotNil(t, before.LatestAccessible)
	require.Equal(t, 1, before.LatestAccessible.Version)
	// updated_at is DERIVED for the member: it is the accessible version's
	// created_at, never the skills row's own clock.
	require.True(t, before.UpdatedAt.Equal(before.LatestAccessible.CreatedAt),
		"a restricted reader's updated_at must be derived from the accessible version, got %v vs %v",
		before.UpdatedAt, before.LatestAccessible.CreatedAt)

	var rawBefore time.Time
	require.Nil(t, pool.QueryRow(ctx, `SELECT updated_at FROM skills WHERE id = $1`, skill).Scan(&rawBefore))

	// The owner publishes a PRIVATE v2. Nothing is shared with the member.
	publishFor(t, ctx, pool, owner, skill, 1, "private v2")

	// Control: the underlying identity row DID move — the leak this test
	// guards against is real at the storage layer.
	var rawAfter time.Time
	require.Nil(t, pool.QueryRow(ctx, `SELECT updated_at FROM skills WHERE id = $1`, skill).Scan(&rawAfter))
	require.False(t, rawAfter.Equal(rawBefore),
		"control failed: skills.updated_at did not move on the private publish, so this test is not exercising the leak")

	// The member's view is byte-identical in every identity field, and v2 is
	// still nowhere in it.
	after, aerr := GetSkill(ctx, pool, member, skill)
	require.Nil(t, aerr)
	require.Equal(t, before.UpdatedAt.UnixNano(), after.UpdatedAt.UnixNano(),
		"the member's visible updated_at moved when a private v2 was published — a timing oracle on private activity")
	require.True(t, after.UpdatedAt.Equal(before.LatestAccessible.CreatedAt),
		"the member's updated_at must still be derived from v1's created_at")
	require.Equal(t, before.CreatedAt.UnixNano(), after.CreatedAt.UnixNano())
	require.Equal(t, 0, after.LatestVersion)
	require.NotNil(t, after.LatestAccessible)
	require.Equal(t, 1, after.LatestAccessible.Version)
	require.Equal(t, before.LatestAccessible.Digest, after.LatestAccessible.Digest)

	// The list path (same buildSkillDetail) is equally closed.
	list, aerr := ListSkills(ctx, pool, member, ListSkillsRequest{})
	require.Nil(t, aerr)
	var listed *SkillDetail
	for i := range list {
		if list[i].ID == skill {
			listed = &list[i]
			break
		}
	}
	require.NotNil(t, listed, "the granted skill must be in the member's list")
	require.Equal(t, before.UpdatedAt.UnixNano(), listed.UpdatedAt.UnixNano(),
		"the list must derive updated_at exactly like the detail path")

	// The OWNER still sees the true clock: their own activity is theirs.
	ownerDetail, aerr := GetSkill(ctx, pool, owner, skill)
	require.Nil(t, aerr)
	require.True(t, ownerDetail.UpdatedAt.Equal(rawAfter),
		"the owner's updated_at is the real identity row; deriving it would hide the owner's own state from them")
	require.Equal(t, 2, ownerDetail.LatestVersion)
}

// TestSkillRegistryOwnershipProbeFailuresPropagate is finding 7's control:
// skillOwnsSkill used to swallow every DB failure into false, silently
// demoting a real owner to the grant-only path — an access answer computed
// from a FAILED query. The failure must reach the caller as the error it is.
// The probe is forced with a closed pool: a real connection layer failure,
// no schema surgery, no cross-test effects (Close is idempotent, and this
// pool is not the one the setup/cleanup helpers own).
func TestSkillRegistryOwnershipProbeFailuresPropagate(t *testing.T) {
	dsn := os.Getenv("AIHUB_TEST_DB")
	if dsn == "" {
		t.Skip("set AIHUB_TEST_DB to run this integration test")
	}
	setupSkillRegistryDB(t) // ensures migrations applied; this test needs no rows
	ctx := context.Background()
	owner := &UserRecord{ID: "u_" + testname.Sanitize(t.Name()) + "_owner", Role: "writer"}

	dead, err := pgxpool.New(ctx, dsn)
	require.Nil(t, err)
	dead.Close()

	_, aerr := ListSkillVersions(ctx, dead, owner, "skill_probe")
	require.NotNil(t, aerr, "a failed ownership probe must not answer as a successful access decision")
	require.Equal(t, ErrInternalError, aerr.Code,
		"the DB failure must surface as the classified error, not be swallowed into the non-privileged path")
	require.Contains(t, aerr.Message, "ownership",
		"the error should name the probe that failed, so the operator can tell it from the version-list query itself")
}
