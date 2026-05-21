// Package client provides the aihub HTTP API client used by all 32 MCP tools.
package client

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// Client is the aihub HTTP API client.
type Client struct {
	baseURL    string
	apiKey     string
	httpClient *http.Client
}

// New creates a new aihub client.
func New(baseURL, apiKey string) *Client {
	return &Client{
		baseURL: strings.TrimRight(baseURL, "/"),
		apiKey:  apiKey,
		httpClient: &http.Client{
			Timeout: 30 * time.Second,
		},
	}
}

// do executes an HTTP request and decodes the JSON response into out (if non-nil).
func (c *Client) do(ctx context.Context, method, path string, body, out any) error {
	var bodyBytes []byte
	if body != nil {
		var err error
		bodyBytes, err = json.Marshal(body)
		if err != nil {
			return fmt.Errorf("marshal request: %w", err)
		}
	}

	req, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, bytes.NewReader(bodyBytes))
	if err != nil {
		return fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+c.apiKey)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("http %s %s: %w", method, path, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		var errResp struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		}
		json.NewDecoder(resp.Body).Decode(&errResp) //nolint:errcheck
		if errResp.Code != "" {
			return fmt.Errorf("aihub %d %s: %s", resp.StatusCode, errResp.Code, errResp.Message)
		}
		return fmt.Errorf("aihub %d: unexpected error", resp.StatusCode)
	}

	if out != nil {
		if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
			return fmt.Errorf("decode response: %w", err)
		}
	}
	return nil
}

// buildQuery builds a URL query string from key-value pairs (skip empty values).
func buildQuery(pairs ...string) string {
	q := url.Values{}
	for i := 0; i+1 < len(pairs); i += 2 {
		if pairs[i+1] != "" {
			q.Set(pairs[i], pairs[i+1])
		}
	}
	if len(q) == 0 {
		return ""
	}
	return "?" + q.Encode()
}

// ─── User / Auth ───────────────────────────────────────────────────────────

// WhoAmI returns the caller's identity and roles.
func (c *Client) WhoAmI(ctx context.Context) (map[string]any, error) {
	var out map[string]any
	return out, c.do(ctx, "GET", "/v1/users/me", nil, &out)
}

// Health calls GET /health and decodes the response.
func (c *Client) Health(ctx context.Context, out any) error {
	return c.do(ctx, "GET", "/health", nil, out)
}

// ─── Work Items ────────────────────────────────────────────────────────────

// CreateWorkItem calls POST /v1/work_items.
func (c *Client) CreateWorkItem(ctx context.Context, body any) (map[string]any, error) {
	var out map[string]any
	return out, c.do(ctx, "POST", "/v1/work_items", body, &out)
}

// ListWorkItems calls GET /v1/work_items with query params.
func (c *Client) ListWorkItems(ctx context.Context, params url.Values) (map[string]any, error) {
	path := "/v1/work_items"
	if len(params) > 0 {
		path += "?" + params.Encode()
	}
	var out map[string]any
	return out, c.do(ctx, "GET", path, nil, &out)
}

// GetWorkItem calls GET /v1/work_items/:id.
func (c *Client) GetWorkItem(ctx context.Context, id string) (map[string]any, error) {
	var out map[string]any
	return out, c.do(ctx, "GET", "/v1/work_items/"+id, nil, &out)
}

// UpdateWorkItem calls PATCH /v1/work_items/:id.
func (c *Client) UpdateWorkItem(ctx context.Context, id string, body any) (map[string]any, error) {
	var out map[string]any
	return out, c.do(ctx, "PATCH", "/v1/work_items/"+id, body, &out)
}

// CancelWorkItem calls POST /v1/work_items/:id/cancel.
func (c *Client) CancelWorkItem(ctx context.Context, id string, body any) (map[string]any, error) {
	var out map[string]any
	return out, c.do(ctx, "POST", "/v1/work_items/"+id+"/cancel", body, &out)
}

// ClaimWorkItem calls POST /v1/work_items/:id/claim.
func (c *Client) ClaimWorkItem(ctx context.Context, id string, body any) (map[string]any, error) {
	var out map[string]any
	return out, c.do(ctx, "POST", "/v1/work_items/"+id+"/claim", body, &out)
}

// CompleteAttempt calls POST /v1/attempts/:id/complete.
func (c *Client) CompleteAttempt(ctx context.Context, attemptID string, body any) (map[string]any, error) {
	var out map[string]any
	return out, c.do(ctx, "POST", "/v1/attempts/"+attemptID+"/complete", body, &out)
}

// ForceTakeover calls POST /v1/work_items/:id/force_takeover.
func (c *Client) ForceTakeover(ctx context.Context, id string, body any) (map[string]any, error) {
	var out map[string]any
	return out, c.do(ctx, "POST", "/v1/work_items/"+id+"/force_takeover", body, &out)
}

// GetReadyQueue calls GET /v1/work_items/ready.
func (c *Client) GetReadyQueue(ctx context.Context, params url.Values) (map[string]any, error) {
	path := "/v1/work_items/ready"
	if len(params) > 0 {
		path += "?" + params.Encode()
	}
	var out map[string]any
	return out, c.do(ctx, "GET", path, nil, &out)
}

// RenewLease calls PATCH /v1/attempts/:id/renew.
func (c *Client) RenewLease(ctx context.Context, attemptID string, body any) (map[string]any, error) {
	var out map[string]any
	return out, c.do(ctx, "PATCH", "/v1/attempts/"+attemptID+"/renew", body, &out)
}

// PauseAttempt calls POST /v1/attempts/:id/pause.
func (c *Client) PauseAttempt(ctx context.Context, attemptID string, body any) (map[string]any, error) {
	var out map[string]any
	return out, c.do(ctx, "POST", "/v1/attempts/"+attemptID+"/pause", body, &out)
}

// ─── Events ────────────────────────────────────────────────────────────────

// EmitEvent calls POST /v1/events.
func (c *Client) EmitEvent(ctx context.Context, body any) (map[string]any, error) {
	var out map[string]any
	return out, c.do(ctx, "POST", "/v1/events", body, &out)
}

// ReadEvents calls GET /v1/events.
func (c *Client) ReadEvents(ctx context.Context, params url.Values) (map[string]any, error) {
	path := "/v1/events"
	if len(params) > 0 {
		path += "?" + params.Encode()
	}
	var out map[string]any
	return out, c.do(ctx, "GET", path, nil, &out)
}

// ─── Memory ────────────────────────────────────────────────────────────────

// Remember calls POST /v1/memories.
func (c *Client) Remember(ctx context.Context, body any) (map[string]any, error) {
	var out map[string]any
	return out, c.do(ctx, "POST", "/v1/memories", body, &out)
}

// Recall calls GET /v1/memories.
func (c *Client) Recall(ctx context.Context, params url.Values) (map[string]any, error) {
	path := "/v1/memories"
	if len(params) > 0 {
		path += "?" + params.Encode()
	}
	var out map[string]any
	return out, c.do(ctx, "GET", path, nil, &out)
}

// ActivateMemory calls PATCH /v1/memories/:id/activate.
func (c *Client) ActivateMemory(ctx context.Context, memoryID string) (map[string]any, error) {
	var out map[string]any
	return out, c.do(ctx, "PATCH", "/v1/memories/"+memoryID+"/activate", nil, &out)
}

// ReinforceMemory calls PATCH /v1/memories/:id/reinforce.
func (c *Client) ReinforceMemory(ctx context.Context, memoryID string, body any) (map[string]any, error) {
	var out map[string]any
	return out, c.do(ctx, "PATCH", "/v1/memories/"+memoryID+"/reinforce", body, &out)
}

// RedactMemory calls DELETE /v1/memories/:id.
func (c *Client) RedactMemory(ctx context.Context, memoryID string, body any) (map[string]any, error) {
	var out map[string]any
	return out, c.do(ctx, "DELETE", "/v1/memories/"+memoryID, body, &out)
}

// ─── Conflicts ────────────────────────────────────────────────────────────

// PredictConflicts calls POST /v1/conflicts/predict.
func (c *Client) PredictConflicts(ctx context.Context, body any) (map[string]any, error) {
	var out map[string]any
	return out, c.do(ctx, "POST", "/v1/conflicts/predict", body, &out)
}

// ─── Dependencies ────────────────────────────────────────────────────────

// CreateDependency calls POST /v1/dependencies.
func (c *Client) CreateDependency(ctx context.Context, body any) (map[string]any, error) {
	var out map[string]any
	return out, c.do(ctx, "POST", "/v1/dependencies", body, &out)
}

// RemoveDependency calls DELETE /v1/dependencies.
func (c *Client) RemoveDependency(ctx context.Context, body any) (map[string]any, error) {
	var out map[string]any
	return out, c.do(ctx, "DELETE", "/v1/dependencies", body, &out)
}

// ListDependencies calls GET /v1/work_items/:id/dependencies.
func (c *Client) ListDependencies(ctx context.Context, wiID string) (map[string]any, error) {
	var out map[string]any
	return out, c.do(ctx, "GET", "/v1/work_items/"+wiID+"/dependencies", nil, &out)
}

// ─── Scenario Config ──────────────────────────────────────────────────────

// GetScenarioConfig calls GET /v1/scenario_configs/:scenario.
func (c *Client) GetScenarioConfig(ctx context.Context, scenario string) (map[string]any, error) {
	var out map[string]any
	return out, c.do(ctx, "GET", "/v1/scenario_configs/"+scenario, nil, &out)
}

// UpdateScenarioConfig calls PATCH /v1/scenario_configs/:scenario.
func (c *Client) UpdateScenarioConfig(ctx context.Context, scenario string, body any) (map[string]any, error) {
	var out map[string]any
	return out, c.do(ctx, "PATCH", "/v1/scenario_configs/"+scenario, body, &out)
}

// ─── Steps ────────────────────────────────────────────────────────────────

// GetStep calls GET /v1/work_items/:id/step.
func (c *Client) GetStep(ctx context.Context, wiID string) (map[string]any, error) {
	var out map[string]any
	return out, c.do(ctx, "GET", "/v1/work_items/"+wiID+"/step", nil, &out)
}

// UpdateStep calls PATCH /v1/work_items/:id/step.
func (c *Client) UpdateStep(ctx context.Context, wiID string, body any) (map[string]any, error) {
	var out map[string]any
	return out, c.do(ctx, "PATCH", "/v1/work_items/"+wiID+"/step", body, &out)
}

// ─── Release ──────────────────────────────────────────────────────────────

// CutAlpha calls POST /v1/releases/alpha.
func (c *Client) CutAlpha(ctx context.Context, body any) (map[string]any, error) {
	var out map[string]any
	return out, c.do(ctx, "POST", "/v1/releases/alpha", body, &out)
}

// Promote calls POST /v1/releases/promote.
func (c *Client) Promote(ctx context.Context, body any) (map[string]any, error) {
	var out map[string]any
	return out, c.do(ctx, "POST", "/v1/releases/promote", body, &out)
}
