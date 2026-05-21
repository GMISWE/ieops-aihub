//go:build integration

// Package integration_test contains end-to-end integration tests that run
// against a live aihub server and PostgreSQL database.
// Run with: cd tests/integration && make test
// Or against an already-running server: make test-local
package integration_test

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"net/url"
	"os"
	"testing"
	"time"

	"github.com/GMISWE/ieops-aihub/pkg/client"
)

const (
	defaultAihubURL = "http://localhost:8081"
	// testAPIKey must be seeded in the test database with writer access to "aihub" project.
	// See tests/integration/seed_test_data.sql for the seed data.
	testAPIKey      = "test-api-key-writer"
	testAdminKey    = "test-api-key-admin"
	testProject     = "aihub"
)

// newTestClient creates a client pointed at the test aihub server.
// AIHUB_URL and AIHUB_API_KEY env vars override the defaults.
func newTestClient(t *testing.T) *client.Client {
	t.Helper()
	aihubURL := os.Getenv("AIHUB_URL")
	if aihubURL == "" {
		aihubURL = defaultAihubURL
	}
	apiKey := os.Getenv("AIHUB_API_KEY")
	if apiKey == "" {
		apiKey = testAPIKey
	}
	return client.New(aihubURL, apiKey)
}

// newAdminClient creates a client with admin-level access.
func newAdminClient(t *testing.T) *client.Client {
	t.Helper()
	aihubURL := os.Getenv("AIHUB_URL")
	if aihubURL == "" {
		aihubURL = defaultAihubURL
	}
	adminKey := os.Getenv("AIHUB_ADMIN_KEY")
	if adminKey == "" {
		adminKey = testAdminKey
	}
	return client.New(aihubURL, adminKey)
}

// waitForHealth polls GET /v1/health until it returns 200 or timeout elapses.
func waitForHealth(t *testing.T, c *client.Client, timeout time.Duration) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	for {
		if err := c.Health(ctx, nil); err == nil {
			return
		}
		select {
		case <-ctx.Done():
			t.Fatal("aihub did not become healthy in time")
		case <-time.After(500 * time.Millisecond):
		}
	}
}

// newSessionInfo generates a fresh session_info for a claim request.
func newSessionInfo() map[string]any {
	secret := make([]byte, 32)
	rand.Read(secret) //nolint:errcheck
	return map[string]any{
		"machine_id":     "test-machine-001",
		"session_secret": hex.EncodeToString(secret),
	}
}

// mustCreateWorkItem creates a work item or fails the test.
func mustCreateWorkItem(t *testing.T, c *client.Client, ctx context.Context, body map[string]any) string {
	t.Helper()
	result, err := c.CreateWorkItem(ctx, body)
	if err != nil {
		t.Fatalf("CreateWorkItem: %v", err)
	}
	wiID, ok := result["id"].(string)
	if !ok || wiID == "" {
		t.Fatalf("CreateWorkItem: missing id in response: %v", result)
	}
	return wiID
}

// mustClaimWorkItem claims a work item or fails the test.
// Returns the full claim response map.
func mustClaimWorkItem(t *testing.T, c *client.Client, ctx context.Context, wiID, idemKey string) map[string]any {
	t.Helper()
	si := newSessionInfo()
	result, err := c.ClaimWorkItem(ctx, wiID, map[string]any{
		"idempotency_key": idemKey,
		"session_info":    si,
		"mode":            "fresh",
	})
	if err != nil {
		t.Fatalf("ClaimWorkItem(%s): %v", wiID, err)
	}
	return result
}

// mustCompleteAttempt wraps/fails/pauses a run_attempt or fails the test.
func mustCompleteAttempt(t *testing.T, c *client.Client, ctx context.Context, wiID string, claim map[string]any, status string) {
	t.Helper()
	attemptID := claim["attempt_id"].(string)
	claimEpoch := int64(claim["claim_epoch"].(float64))
	sessionSecret := claim["session_secret"].(string)
	_, err := c.CompleteAttempt(ctx, attemptID, map[string]any{
		"attempt_id":     attemptID,
		"claim_epoch":    claimEpoch,
		"session_secret": sessionSecret,
		"status":         status,
	})
	if err != nil {
		t.Fatalf("CompleteAttempt(%s → %s): %v", wiID, status, err)
	}
}

// mustGetWorkItem fetches a work item or fails the test.
func mustGetWorkItem(t *testing.T, c *client.Client, ctx context.Context, wiID string) map[string]any {
	t.Helper()
	wi, err := c.GetWorkItem(ctx, wiID)
	if err != nil {
		t.Fatalf("GetWorkItem(%s): %v", wiID, err)
	}
	return wi
}

// mustGetReadyQueue fetches the ready queue for a project or fails the test.
func mustGetReadyQueue(t *testing.T, c *client.Client, ctx context.Context, project string) map[string]any {
	t.Helper()
	params := url.Values{}
	params.Set("project", project)
	q, err := c.GetReadyQueue(ctx, params)
	if err != nil {
		t.Fatalf("GetReadyQueue(%s): %v", project, err)
	}
	return q
}

// cancelWorkItem cancels a work item (best-effort; doesn't fail the test).
func cancelWorkItem(t *testing.T, c *client.Client, ctx context.Context, wiID string) {
	t.Helper()
	c.CancelWorkItem(ctx, wiID, nil) //nolint:errcheck
}
