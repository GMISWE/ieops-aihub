//go:build integration

package integration_test

import (
	"context"
	"encoding/json"
	"os"
	"testing"
	"time"

	"github.com/GMISWE/ieops-aihub/pkg/client"
)

// TestScenarioConfigCAS verifies the optimistic-locking (CAS) mechanism on
// scenario_phase_configs:
//  1. GET current config + version
//  2. Add a new wi_type with correct version → should succeed, version increments
//  3. Retry with the now-stale version → should get 409 CONFLICT_VERSION_MISMATCH
//  4. Verify the new wi_type is present in the config
//
// Requires maintainer or admin access (uses AIHUB_ADMIN_KEY / test-api-key-admin).
func TestScenarioConfigCAS(t *testing.T) {
	ctx := context.Background()

	// Use admin client for scenario config updates (requires maintainer/admin)
	adminURL := os.Getenv("AIHUB_URL")
	if adminURL == "" {
		adminURL = defaultAihubURL
	}
	adminKey := os.Getenv("AIHUB_ADMIN_KEY")
	if adminKey == "" {
		adminKey = testAdminKey
	}
	ac := client.New(adminURL, adminKey)
	waitForHealth(t, ac, 30*time.Second)

	// 1. GET current scenario config
	config, err := ac.GetScenarioConfig(ctx, "coding")
	if err != nil {
		t.Skipf("GetScenarioConfig: %v (scenario config may not be seeded)", err)
	}

	currentVersion, ok := config["version"].(float64)
	if !ok {
		t.Fatalf("GetScenarioConfig: missing 'version' field in response: %v", config)
	}
	version := int(currentVersion)
	t.Logf("current version: %d", version)

	// 2. Parse current content and add a new wi_type "debug_session"
	contentRaw, ok := config["content"].(map[string]any)
	if !ok {
		t.Fatalf("GetScenarioConfig: 'content' is not a map: %T", config["content"])
	}
	wiTypes, _ := contentRaw["wi_types"].(map[string]any)
	if wiTypes == nil {
		wiTypes = make(map[string]any)
	}
	// Add new wi_type (debug_session — requires_human_session=false for auto routing)
	wiTypes["debug_session"] = map[string]any{
		"requires_human_session": false,
		"steps":                  []string{"prepare_context", "code_change", "commit_and_pr"},
	}
	contentRaw["wi_types"] = wiTypes

	newContent, err := json.Marshal(contentRaw)
	if err != nil {
		t.Fatalf("marshal updated content: %v", err)
	}

	// 3. PUT with correct version → CAS should succeed
	updated, err := ac.UpdateScenarioConfig(ctx, "coding", map[string]any{
		"content": json.RawMessage(newContent),
		"version": version,
	})
	if err != nil {
		t.Fatalf("CAS update with correct version=%d failed: %v", version, err)
	}
	newVersion, _ := updated["version"].(float64)
	if int(newVersion) != version+1 {
		t.Errorf("expected updated version=%d, got %.0f", version+1, newVersion)
	}
	t.Logf("CAS update succeeded: version %d → %.0f", version, newVersion)

	// 4. PUT with stale version (original version) → should get 409
	_, err = ac.UpdateScenarioConfig(ctx, "coding", map[string]any{
		"content": json.RawMessage(newContent),
		"version": version, // stale
	})
	if err == nil {
		t.Error("expected CAS conflict error with stale version, got nil")
	} else {
		t.Logf("CAS stale version correctly rejected: %v", err)
	}

	// 5. GET the config again and verify debug_session wi_type is present
	config2, err := ac.GetScenarioConfig(ctx, "coding")
	if err != nil {
		t.Fatalf("second GetScenarioConfig: %v", err)
	}
	content2, _ := config2["content"].(map[string]any)
	wiTypes2, _ := content2["wi_types"].(map[string]any)
	if _, exists := wiTypes2["debug_session"]; !exists {
		t.Error("debug_session wi_type not present after CAS update")
	} else {
		t.Logf("debug_session wi_type confirmed present in config")
	}
}
