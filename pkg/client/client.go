// Package client provides the aihub HTTP API client used by all 32 MCP tools.
package client

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// seg path-escapes a single caller-supplied path segment (id, slug, name,
// memory/user/key id) before it is interpolated into a request URL. Without
// this, a human slug like "aihub#27" has its "#27" parsed by net/url as a URL
// fragment and stripped client-side, so the server only receives
// "/v1/work_items/aihub" -> 404. PathEscape leaves URL-safe ids (wi_xxx,
// mem_xxx, u_xxx) unchanged, so id-based callers see no behavior change.
func seg(s string) string { return url.PathEscape(s) }

// formatDetails renders the server error `details` object as a compact
// " details=<json>" suffix for the error string, so the conflict metadata the
// server already computes (lock holder, dedup candidates, superseded_by, …)
// reaches the caller instead of being silently dropped. Empty/null details
// yield "". The rendered JSON is capped at ~500 bytes; the server contract keeps
// secrets out of `details`, so passing it through verbatim is safe (aihub#209).
func formatDetails(raw json.RawMessage) string {
	if len(raw) == 0 || string(raw) == "null" {
		return ""
	}
	var buf bytes.Buffer
	if err := json.Compact(&buf, raw); err != nil {
		return ""
	}
	s := buf.String()
	const max = 500
	if len(s) > max {
		s = s[:max] + "...(truncated)"
	}
	return " details=" + s
}

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
	defer resp.Body.Close() //nolint:errcheck

	if resp.StatusCode >= 400 {
		var errResp struct {
			Code    string          `json:"code"`
			Message string          `json:"message"`
			Details json.RawMessage `json:"details"`
		}
		json.NewDecoder(resp.Body).Decode(&errResp) //nolint:errcheck
		if errResp.Code != "" {
			return fmt.Errorf("aihub %d %s: %s%s", resp.StatusCode, errResp.Code, errResp.Message, formatDetails(errResp.Details))
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

// doRaw executes an HTTP request and returns the response body bytes plus its
// Content-Type, bypassing the JSON decoder. Used by endpoints that return
// non-JSON payloads such as the spec/plan HTML viewer (aihub#27).
//
// Error envelope handling matches do(): if status >= 400 we still try to read
// the JSON error body and wrap it; only on 2xx do we return the raw bytes.
func (c *Client) doRaw(ctx context.Context, method, path string) ([]byte, string, error) {
	req, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, nil)
	if err != nil {
		return nil, "", fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+c.apiKey)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, "", fmt.Errorf("http %s %s: %w", method, path, err)
	}
	defer resp.Body.Close() //nolint:errcheck

	body, readErr := io.ReadAll(resp.Body)
	if readErr != nil {
		return nil, "", fmt.Errorf("read response: %w", readErr)
	}

	if resp.StatusCode >= 400 {
		var errResp struct {
			Code    string          `json:"code"`
			Message string          `json:"message"`
			Details json.RawMessage `json:"details"`
		}
		_ = json.Unmarshal(body, &errResp)
		if errResp.Code != "" {
			return nil, "", fmt.Errorf("aihub %d %s: %s%s", resp.StatusCode, errResp.Code, errResp.Message, formatDetails(errResp.Details))
		}
		return nil, "", fmt.Errorf("aihub %d: unexpected error", resp.StatusCode)
	}

	return body, resp.Header.Get("Content-Type"), nil
}

// ─── User / Auth ───────────────────────────────────────────────────────────

// WhoAmI returns the caller's identity and roles.
func (c *Client) WhoAmI(ctx context.Context) (map[string]any, error) {
	var out map[string]any
	return out, c.do(ctx, "GET", "/v1/users/me", nil, &out)
}

// Health calls GET /v1/health and decodes the response into out (may be nil).
//
// A nil error means the request completed with a 2xx — it says nothing about
// whether the server is healthy. /v1/health answers 200 in every branch by
// design (aihub#316): liveness probes read its reachability and ignore the
// body, so a 503 for an optional dependency would restart a server that is
// still serving. The verdict lives in the decoded body's "status" field
// ("ok" | "degraded"), alongside "db_ok", "embedding_enabled", "embedding_ok"
// and — only when non-empty — "embedding_error_kind". A caller that wants the
// verdict must read those; see healthVerdict in internal/cli/doctor.go.
func (c *Client) Health(ctx context.Context, out any) error {
	return c.do(ctx, "GET", "/v1/health", nil, out)
}

// Ping calls GET /v1/health, discards the body, and returns nil if the server
// answered with a 2xx. That is reachability only, NOT health: it used to claim
// "reachable and healthy", which was never true of the code and became actively
// misleading once the body started carrying a real verdict (aihub#316). Use
// Health and read "status" if you need to know whether the server is degraded.
func (c *Client) Ping(ctx context.Context) error {
	return c.do(ctx, "GET", "/v1/health", nil, nil)
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
	return out, c.do(ctx, "GET", "/v1/work_items/"+seg(id), nil, &out)
}

// UpdateWorkItem calls PATCH /v1/work_items/:id.
func (c *Client) UpdateWorkItem(ctx context.Context, id string, body any) (map[string]any, error) {
	var out map[string]any
	return out, c.do(ctx, "PATCH", "/v1/work_items/"+seg(id), body, &out)
}

// CancelWorkItem calls POST /v1/work_items/:id/cancel.
func (c *Client) CancelWorkItem(ctx context.Context, id string, body any) (map[string]any, error) {
	var out map[string]any
	return out, c.do(ctx, "POST", "/v1/work_items/"+seg(id)+"/cancel", body, &out)
}

// ClaimWorkItem calls POST /v1/work_items/:id/claim.
func (c *Client) ClaimWorkItem(ctx context.Context, id string, body any) (map[string]any, error) {
	var out map[string]any
	return out, c.do(ctx, "POST", "/v1/work_items/"+seg(id)+"/claim", body, &out)
}

// CompleteAttempt calls POST /v1/work_items/:id/complete.
// wiID is the work item id; the attempt credentials are embedded in body.
func (c *Client) CompleteAttempt(ctx context.Context, wiID string, body any) (map[string]any, error) {
	var out map[string]any
	return out, c.do(ctx, "POST", "/v1/work_items/"+seg(wiID)+"/complete", body, &out)
}

// ForceTakeover calls POST /v1/work_items/:id/force_takeover.
func (c *Client) ForceTakeover(ctx context.Context, id string, body any) (map[string]any, error) {
	var out map[string]any
	return out, c.do(ctx, "POST", "/v1/work_items/"+seg(id)+"/force_takeover", body, &out)
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

// PauseAttempt calls POST /v1/work_items/:wiID/pause.
func (c *Client) PauseAttempt(ctx context.Context, wiID string, body any) (map[string]any, error) {
	var out map[string]any
	return out, c.do(ctx, "POST", "/v1/work_items/"+seg(wiID)+"/pause", body, &out)
}

// AcquireLocks calls POST /v1/work_items/:wiID/acquire_locks.
func (c *Client) AcquireLocks(ctx context.Context, wiID string, body any) (map[string]any, error) {
	var out map[string]any
	return out, c.do(ctx, "POST", "/v1/work_items/"+seg(wiID)+"/acquire_locks", body, &out)
}

// ReconcileCommitLocks calls POST /v1/work_items/:wiID/commit_locks — the
// commit-time gate (aihub#366). body carries the attempt credentials, the repo,
// and the paths the pending commit contains.
//
// Unlike AcquireLocks it derives nothing from declared_resources: the server
// compares the paths against the file_scope locks the attempt actually holds,
// acquires the difference, and answers 409 CONFLICT_LOCK_TAKEN — with the
// holders in `details` — when the difference belongs to somebody else.
func (c *Client) ReconcileCommitLocks(ctx context.Context, wiID string, body any) (map[string]any, error) {
	var out map[string]any
	return out, c.do(ctx, "POST", "/v1/work_items/"+seg(wiID)+"/commit_locks", body, &out)
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

// GetMemory calls GET /v1/memories/:id. Unlike the list endpoint, this returns
// the memory's FULL content — the list endpoint truncates content to 800 runes
// and flags the cut with content_truncated / content_full_len (aihub#244), and
// this is the escape hatch that PR #245 declared for reading the rest
// (aihub#269).
func (c *Client) GetMemory(ctx context.Context, memoryID string) (map[string]any, error) {
	var out map[string]any
	return out, c.do(ctx, "GET", "/v1/memories/"+seg(memoryID), nil, &out)
}

// ActivateMemory calls POST /v1/memories/:id/activate.
func (c *Client) ActivateMemory(ctx context.Context, memoryID string) (map[string]any, error) {
	var out map[string]any
	return out, c.do(ctx, "POST", "/v1/memories/"+seg(memoryID)+"/activate", nil, &out)
}

// ReinforceMemory calls PATCH /v1/memories/:id/reinforce.
func (c *Client) ReinforceMemory(ctx context.Context, memoryID string, body any) (map[string]any, error) {
	var out map[string]any
	return out, c.do(ctx, "PATCH", "/v1/memories/"+seg(memoryID)+"/reinforce", body, &out)
}

// RedactMemory calls PATCH /v1/memories/:id/redact per §4.3.
func (c *Client) RedactMemory(ctx context.Context, memoryID string, body any) (map[string]any, error) {
	var out map[string]any
	return out, c.do(ctx, "PATCH", "/v1/memories/"+seg(memoryID)+"/redact", body, &out)
}

// UpdateMemory calls PATCH /v1/memories/:id/update — creates a new version
// superseding the current lineage head and advances the latest_id cursor
// (aihub#201).
func (c *Client) UpdateMemory(ctx context.Context, memoryID string, body any) (map[string]any, error) {
	var out map[string]any
	return out, c.do(ctx, "PATCH", "/v1/memories/"+seg(memoryID)+"/update", body, &out)
}

// ResolveCommit calls POST /v1/memories/:id/commit/:commit_id/resolve.
// body should be map[string]any{"reply": "..."}.
func (c *Client) ResolveCommit(ctx context.Context, memoryID, commitID string, body any) (map[string]any, error) {
	var out map[string]any
	return out, c.do(ctx, "POST", "/v1/memories/"+seg(memoryID)+"/commit/"+seg(commitID)+"/resolve", body, &out)
}

// ─── Artifacts ────────────────────────────────────────────────────────────

// GetArtifactHTML calls GET /v1/artifacts/:id/html and returns the rendered
// HTML body verbatim (aihub#27 / IEBE-1694). Returns an error when the memory
// does not exist, the caller lacks visibility, or the row has no rendered HTML
// (legacy spec/plan / non spec/plan type).
func (c *Client) GetArtifactHTML(ctx context.Context, memoryID string) (string, error) {
	body, _, err := c.doRaw(ctx, "GET", "/v1/artifacts/"+seg(memoryID)+"/html")
	if err != nil {
		return "", err
	}
	return string(body), nil
}

// ─── Conflicts ────────────────────────────────────────────────────────────

// PredictConflicts calls POST /v1/conflicts/predict.
func (c *Client) PredictConflicts(ctx context.Context, body any) (map[string]any, error) {
	var out map[string]any
	return out, c.do(ctx, "POST", "/v1/conflicts/predict", body, &out)
}

// ─── Dependencies ────────────────────────────────────────────────────────

// CreateDependency calls POST /v1/work_items/:blocked_id/dependencies.
// body must include blocked_wi_id (used in the URL path), blocking_wi_id, kind.
func (c *Client) CreateDependency(ctx context.Context, body any) (map[string]any, error) {
	// Extract blocked_wi_id for the URL path
	blockedID := ""
	if m, ok := body.(map[string]any); ok {
		if s, ok := m["blocked_wi_id"].(string); ok {
			blockedID = s
		}
	}
	if blockedID == "" {
		return nil, fmt.Errorf("CreateDependency: blocked_wi_id is required in body")
	}
	var out map[string]any
	return out, c.do(ctx, "POST", "/v1/work_items/"+seg(blockedID)+"/dependencies", body, &out)
}

// RemoveDependency calls DELETE /v1/work_items/:blocked_id/dependencies/:blocking_id/:kind.
//
// It takes the three path segments directly, and deliberately takes no body
// (aihub#324). The previous signature was `body any`: it picked the three
// segments out of the map and then called do() with a nil body, so every OTHER
// key the caller had put in that map was silently discarded — which is exactly
// what happened to the attempt_id / claim_epoch / session_secret that
// internal/mcp/tools_dependency.go used to build here. A map-shaped parameter on
// a request that carries no body makes that mistake expressible; three strings
// do not. The endpoint has no body to send: it is a DELETE addressed entirely by
// its path, authorized by project role alone.
func (c *Client) RemoveDependency(ctx context.Context, blockedID, blockingID, kind string) (map[string]any, error) {
	if blockedID == "" || blockingID == "" || kind == "" {
		return nil, fmt.Errorf("RemoveDependency: blockedID, blockingID and kind are all required")
	}
	var out map[string]any
	return out, c.do(ctx, "DELETE",
		"/v1/work_items/"+seg(blockedID)+"/dependencies/"+seg(blockingID)+"/"+seg(kind),
		nil, &out)
}

// ListDependencies calls GET /v1/work_items/:id/dependencies.
func (c *Client) ListDependencies(ctx context.Context, wiID string) (map[string]any, error) {
	var out map[string]any
	return out, c.do(ctx, "GET", "/v1/work_items/"+seg(wiID)+"/dependencies", nil, &out)
}

// ─── Steps ────────────────────────────────────────────────────────────────

// GetStep calls GET /v1/work_items/:id/step.
func (c *Client) GetStep(ctx context.Context, wiID string) (map[string]any, error) {
	var out map[string]any
	return out, c.do(ctx, "GET", "/v1/work_items/"+seg(wiID)+"/step", nil, &out)
}

// UpdateStep calls PATCH /v1/work_items/:id/step.
func (c *Client) UpdateStep(ctx context.Context, wiID string, body any) (map[string]any, error) {
	var out map[string]any
	return out, c.do(ctx, "PATCH", "/v1/work_items/"+seg(wiID)+"/step", body, &out)
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

// ─── Version ──────────────────────────────────────────────────────────────────

// GetVersion calls GET /v1/version.
func (c *Client) GetVersion(ctx context.Context) (map[string]any, error) {
	var out map[string]any
	return out, c.do(ctx, "GET", "/v1/version", nil, &out)
}

// ─── Admin ────────────────────────────────────────────────────────────────────

// UnblockWorkItem calls POST /v1/work_items/:id/unblock (admin only).
func (c *Client) UnblockWorkItem(ctx context.Context, wiID string, body any) (map[string]any, error) {
	var out map[string]any
	return out, c.do(ctx, "POST", "/v1/work_items/"+seg(wiID)+"/unblock", body, &out)
}

// CreateUser calls POST /v1/admin/users (admin only).
func (c *Client) CreateUser(ctx context.Context, body any) (map[string]any, error) {
	var out map[string]any
	return out, c.do(ctx, "POST", "/v1/admin/users", body, &out)
}

// CreateAPIKey calls POST /v1/admin/users/:id/keys (admin only).
func (c *Client) CreateAPIKey(ctx context.Context, userID string, body any) (map[string]any, error) {
	var out map[string]any
	return out, c.do(ctx, "POST", "/v1/admin/users/"+seg(userID)+"/keys", body, &out)
}

// RevokeAPIKey calls DELETE /v1/admin/users/:id/keys/:key_id (admin only).
func (c *Client) RevokeAPIKey(ctx context.Context, userID, keyID string) (map[string]any, error) {
	var out map[string]any
	return out, c.do(ctx, "DELETE", "/v1/admin/users/"+seg(userID)+"/keys/"+seg(keyID), nil, &out)
}

// ListUsers calls GET /v1/admin/users (admin only).
func (c *Client) ListUsers(ctx context.Context) (map[string]any, error) {
	var out map[string]any
	return out, c.do(ctx, "GET", "/v1/admin/users", nil, &out)
}

// UpdateUser calls PATCH /v1/admin/users/:id (admin only).
func (c *Client) UpdateUser(ctx context.Context, userID string, body any) (map[string]any, error) {
	var out map[string]any
	return out, c.do(ctx, "PATCH", "/v1/admin/users/"+seg(userID), body, &out)
}

// ─── Projects ─────────────────────────────────────────────────────────────────

// ListProjects calls GET /v1/projects.
func (c *Client) ListProjects(ctx context.Context, params url.Values) (map[string]any, error) {
	path := "/v1/projects"
	if len(params) > 0 {
		path += "?" + params.Encode()
	}
	var out map[string]any
	return out, c.do(ctx, "GET", path, nil, &out)
}

// CreateProject calls POST /v1/projects.
func (c *Client) CreateProject(ctx context.Context, body any) (map[string]any, error) {
	var out map[string]any
	return out, c.do(ctx, "POST", "/v1/projects", body, &out)
}

// GetProject calls GET /v1/projects/:name.
func (c *Client) GetProject(ctx context.Context, name string) (map[string]any, error) {
	var out map[string]any
	return out, c.do(ctx, "GET", "/v1/projects/"+seg(name), nil, &out)
}

// UpdateProject calls PATCH /v1/projects/:name.
//
// body is forwarded verbatim, so a key the caller puts in it reaches the server
// unchanged — including aihub#260's `members_version` compare-and-set
// precondition. That is load-bearing rather than incidental: this repo has
// shipped a parameter that existed in the MCP schema and was silently dropped
// one hop later, which is indistinguishable at the call site from a guard that
// passed. TestClientUpdateProjectForwardsMembersVersionOnTheWire pins it by
// reading the bytes off the wire.
//
// On a failed compare-and-set the server answers 409 CONFLICT_CAS_FAILED and
// do() folds the error envelope's details — which carry
// current_members_version — into the returned error's text, so the caller can
// retry with the right version without a second read.
func (c *Client) UpdateProject(ctx context.Context, name string, body any) (map[string]any, error) {
	var out map[string]any
	return out, c.do(ctx, "PATCH", "/v1/projects/"+seg(name), body, &out)
}

// RotateProjectIdentifier calls POST /v1/projects/:name/rotate_identifier.
// Returns {plain, prefix} — plain is shown once and must not be logged.
func (c *Client) RotateProjectIdentifier(ctx context.Context, name string) (map[string]any, error) {
	var out map[string]any
	return out, c.do(ctx, "POST", "/v1/projects/"+seg(name)+"/rotate_identifier", nil, &out)
}
