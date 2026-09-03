package server

// The second instance of aihub#248 W1, closed as part of aihub#253.
//
// routes_artifacts.go filters the artifact viewer's version rail through
// versionRefVisibleTo. fetchArtifactLinks did not: it handed the whole lineage
// to wi_detail.html.tmpl, which renders each member's id (inside the "View"
// link), date and status. So /ui/wi/:id disclosed the existence, timestamp and
// status of lineage members the caller may not see.
//
// This is a caller-scoped authorization test, so the assertion is that the
// hidden member's IDENTIFIERS are absent — not merely that the row count is
// right. A count-only assertion stays green for an implementation that drops
// the wrong member, and it stays green for one that keeps the hidden member and
// drops a visible one.
//
// Both seams (recallFn, wiVersionChainFn) are stubbed and the pool is never
// dereferenced, so this runs without a database.

import (
	"context"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/require"

	"github.com/GMISWE/ieops-aihub/internal/domain"
)

// hiddenPrivateID / hiddenProjectID are the two members a plain viewer of projA
// must not learn about, and hiddenDate is rendered next to them by the template.
const (
	hiddenPrivateID = "mem_hidden_private_QQQ"
	hiddenProjectID = "mem_hidden_project_ZZZ"
	hiddenDate      = "2001-09-11T00:00:00Z"
)

func withArtifactLinkSeams(t *testing.T, chain []domain.MemoryVersionRef, head domain.Memory) {
	t.Helper()
	origRecall, origChain := recallFn, wiVersionChainFn
	t.Cleanup(func() { recallFn, wiVersionChainFn = origRecall, origChain })

	recallFn = func(_ context.Context, _ *pgxpool.Pool, _ *domain.RecallRequest) (*domain.RecallResponse, error) {
		return &domain.RecallResponse{Items: []domain.MemoryWithStrength{{Memory: head}}}, nil
	}
	wiVersionChainFn = func(_ context.Context, _ *pgxpool.Pool, _ string) ([]domain.MemoryVersionRef, error) {
		return chain, nil
	}
}

// lineage returns a 4-member chain whose head is visible to a projA viewer and
// whose middle two members are not, for two independent reasons.
func lineage() []domain.MemoryVersionRef {
	return []domain.MemoryVersionRef{
		{ID: "mem_v1", CreatedAt: "2026-01-01T00:00:00Z", Status: "archived",
			Project: "projA", Visibility: "project", AuthorUserID: "u_other"},
		{ID: hiddenPrivateID, CreatedAt: hiddenDate, Status: "archived",
			Project: "projA", Visibility: "private", AuthorUserID: "u_other"},
		{ID: hiddenProjectID, CreatedAt: hiddenDate, Status: "archived",
			Project: "projB", Visibility: "project", AuthorUserID: "u_other"},
		{ID: "mem_head", CreatedAt: "2026-03-01T00:00:00Z", Status: "active", IsCurrent: true,
			Project: "projA", Visibility: "project", AuthorUserID: "u_other"},
	}
}

func headMemory() domain.Memory {
	return domain.Memory{
		ID: "mem_head", Type: "methodology.spec", Content: "body",
		Project: "projA", Visibility: "project", AuthorUserID: "u_other",
	}
}

func TestWIDetailVersionRail_OmitsLineageMembersTheCallerCannotSee(t *testing.T) {
	withArtifactLinkSeams(t, lineage(), headMemory())

	viewer := &UserContext{
		UserID: "u_viewer", Role: "writer",
		ProjectRoles: map[string]string{"projA": "viewer"},
	}
	links := fetchArtifactLinks(context.Background(), &pgxpool.Pool{}, viewer,
		&domain.WorkItem{ID: "wi_x", Project: "projA"})

	require.Len(t, links, 1, "expected exactly one collapsed artifact link")
	got := links[0].Versions

	var ids, dates []string
	for _, v := range got {
		ids = append(ids, v.ID)
		dates = append(dates, v.CreatedAt)
	}
	joinedIDs, joinedDates := strings.Join(ids, ","), strings.Join(dates, ",")

	// THE criterion: the identifiers must be gone, not just the count reduced.
	require.NotContains(t, joinedIDs, hiddenPrivateID,
		"/ui/wi/:id leaked the id of another author's private lineage member (aihub#248 W1)")
	require.NotContains(t, joinedIDs, hiddenProjectID,
		"/ui/wi/:id leaked the id of a lineage member in a project this caller is not in")
	require.NotContains(t, joinedDates, hiddenDate,
		"/ui/wi/:id leaked the timestamp of a lineage member the caller may not see")

	// And the visible ones are still there — a filter that drops everything is
	// not a fix, it is an outage for a legitimate reader.
	require.Equal(t, []string{"mem_head", "mem_v1"}, ids,
		"expected exactly the two visible members, newest-first")
}

func TestWIDetailVersionRail_AdminSeesTheWholeLineage(t *testing.T) {
	withArtifactLinkSeams(t, lineage(), headMemory())

	admin := &UserContext{UserID: "u_admin", Role: "admin"}
	links := fetchArtifactLinks(context.Background(), &pgxpool.Pool{}, admin,
		&domain.WorkItem{ID: "wi_x", Project: "projA"})

	require.Len(t, links, 1)
	require.Len(t, links[0].Versions, 4,
		"an admin must still see every lineage member; the filter must not over-reach")
}

func TestWIDetailVersionRail_PrivateMembersOwnAuthorSeesIt(t *testing.T) {
	withArtifactLinkSeams(t, lineage(), headMemory())

	// The author of the private member, holding only a viewer role on projA.
	// `private` means "visible to its author", so this caller sees 3 of 4 —
	// everything except the member that lives in a project they are not in.
	author := &UserContext{
		UserID: "u_other", Role: "writer",
		ProjectRoles: map[string]string{"projA": "viewer"},
	}
	links := fetchArtifactLinks(context.Background(), &pgxpool.Pool{}, author,
		&domain.WorkItem{ID: "wi_x", Project: "projA"})

	require.Len(t, links, 1)
	var ids []string
	for _, v := range links[0].Versions {
		ids = append(ids, v.ID)
	}
	require.Contains(t, strings.Join(ids, ","), hiddenPrivateID,
		"the private member's own author must still see it")
	require.NotContains(t, strings.Join(ids, ","), hiddenProjectID,
		"but not a member in a project they are not in")
}
