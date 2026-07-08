//go:build integration

package integration_test

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/GMISWE/ieops-aihub/pkg/client"
)

// TestProjectScopeIsolation verifies that a key with project_scope=B confines
// its caller to B, even though the underlying user is a member of A and B.
// Layer 1 (content): a work item in A must be denied. Layer 2 (existence):
// A must not appear in ListProjects and GetProject(A) must be denied.
func TestProjectScopeIsolation(t *testing.T) {
	ctx := context.Background()
	admin := newAdminClient(t)
	waitForHealth(t, admin, 30*time.Second)

	baseURL := os.Getenv("AIHUB_URL")
	if baseURL == "" {
		baseURL = defaultAihubURL
	}
	const writerUserID = "u_test_writer_001" // seeded writer user

	sfx := scopeRandSuffix(t)
	projA := "sca" + sfx
	projB := "scb" + sfx

	for _, p := range []string{projA, projB} {
		if _, err := admin.CreateProject(ctx, map[string]any{"name": p, "visible": false}); err != nil {
			t.Fatalf("CreateProject(%s): %v", p, err)
		}
		if _, err := admin.UpdateProject(ctx, p, map[string]any{
			"members": []map[string]any{{"user_id": writerUserID, "role": "writer"}},
		}); err != nil {
			t.Fatalf("UpdateProject(%s) members: %v", p, err)
		}
	}

	wiA := mustCreateWorkItem(t, admin, ctx, map[string]any{"project": projA, "goal": "scope isolation A"})
	wiB := mustCreateWorkItem(t, admin, ctx, map[string]any{"project": projB, "goal": "scope isolation B"})

	scopedKey := scopeMustCreateKey(t, admin, ctx, writerUserID, "scoped-to-B", &projB)
	openKey := scopeMustCreateKey(t, admin, ctx, writerUserID, "no-scope", nil)
	scoped := client.New(baseURL, scopedKey)
	open := client.New(baseURL, openKey)

	t.Run("control_unscoped_key_sees_both", func(t *testing.T) {
		if _, err := open.GetWorkItem(ctx, wiA); err != nil {
			t.Errorf("unscoped key denied wiA: %v", err)
		}
		if _, err := open.GetWorkItem(ctx, wiB); err != nil {
			t.Errorf("unscoped key denied wiB: %v", err)
		}
	})

	t.Run("scoped_key_content_isolation", func(t *testing.T) {
		if _, err := scoped.GetWorkItem(ctx, wiB); err != nil {
			t.Errorf("B-scoped key denied its OWN work item wiB: %v", err)
		}
		if _, err := scoped.GetWorkItem(ctx, wiA); err == nil {
			t.Errorf("LEAK: B-scoped key read work item wiA in project A")
		} else {
			t.Logf("ok: blocked from wiA: %v", err)
		}
	})

	t.Run("scoped_key_project_existence_hidden", func(t *testing.T) {
		list, err := scoped.ListProjects(ctx, url.Values{})
		if err != nil {
			t.Fatalf("ListProjects scoped: %v", err)
		}
		if scopeContains(scopeProjectNames(list), projA) {
			t.Errorf("LEAK: B-scoped key sees project A in ListProjects")
		}
		if _, err := scoped.GetProject(ctx, projA); err == nil {
			t.Errorf("LEAK: B-scoped key fetched project A via GetProject")
		} else {
			t.Logf("ok: blocked from GetProject(A): %v", err)
			// Existence must be hidden via 404, not 403 — a 403 would confirm
			// project A exists to a caller who shouldn't even know that.
			msg := err.Error()
			if !strings.Contains(msg, "404") && !strings.Contains(msg, "PROJECT_NOT_FOUND") {
				t.Errorf("expected 404/PROJECT_NOT_FOUND hiding existence, got: %v", err)
			}
		}
	})

	t.Run("scoped_key_dependency_isolation", func(t *testing.T) {
		// wiA lives in project A; the B-scoped key must not read its dependency edges.
		if _, err := scoped.ListDependencies(ctx, wiA); err == nil {
			t.Errorf("LEAK: B-scoped key listed dependencies of wiA in project A")
		} else {
			t.Logf("ok: dependencies of wiA blocked: %v", err)
		}
	})

	t.Run("scoped_key_memory_content_isolation", func(t *testing.T) {
		// admin stores a project-A memory; the B-scoped key must not recall it.
		if _, err := admin.Remember(ctx, map[string]any{
			"project": projA, "type": "fact.note", "content": "secret A note", "visibility": "project",
		}); err != nil {
			t.Fatalf("admin Remember in A: %v", err)
		}
		q := url.Values{}
		q.Set("project", projA)
		if _, err := scoped.Recall(ctx, q); err == nil {
			t.Errorf("LEAK: B-scoped key recalled memories of project A")
		} else {
			t.Logf("ok: recall in A blocked: %v", err)
		}
	})

	t.Run("scoped_admin_key_is_confined", func(t *testing.T) {
		const adminUserID = "u_test_admin_001"
		adminScoped := client.New(baseURL, scopeMustCreateKey(t, admin, ctx, adminUserID, "admin-scoped-B", &projB))
		if scopeContains(scopeProjectNames(mustListProjects(t, adminScoped, ctx)), projA) {
			t.Errorf("LEAK: scoped admin key sees project A in ListProjects")
		}
		if _, err := adminScoped.GetProject(ctx, projA); err == nil {
			t.Errorf("LEAK: scoped admin key fetched project A")
		}
	})
}

func scopeRandSuffix(t *testing.T) string {
	t.Helper()
	b := make([]byte, 4)
	if _, err := rand.Read(b); err != nil {
		t.Fatalf("rand: %v", err)
	}
	return hex.EncodeToString(b)
}

func scopeMustCreateKey(t *testing.T, c *client.Client, ctx context.Context, userID, name string, scope *string) string {
	t.Helper()
	body := map[string]any{"name": name}
	if scope != nil {
		body["project_scope"] = *scope
	}
	r, err := c.CreateAPIKey(ctx, userID, body)
	if err != nil {
		t.Fatalf("CreateAPIKey(%s): %v", name, err)
	}
	k, _ := r["raw_key"].(string)
	if k == "" {
		t.Fatalf("CreateAPIKey(%s): no raw_key in %v", name, r)
	}
	return k
}

func scopeProjectNames(list map[string]any) []string {
	var out []string
	items, _ := list["items"].([]any)
	for _, it := range items {
		if m, ok := it.(map[string]any); ok {
			if n, ok := m["name"].(string); ok {
				out = append(out, n)
			}
		}
	}
	return out
}

func scopeContains(ss []string, s string) bool {
	for _, x := range ss {
		if x == s {
			return true
		}
	}
	return false
}

func mustListProjects(t *testing.T, c *client.Client, ctx context.Context) map[string]any {
	t.Helper()
	l, err := c.ListProjects(ctx, url.Values{})
	if err != nil {
		t.Fatalf("ListProjects: %v", err)
	}
	return l
}
