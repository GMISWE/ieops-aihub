// Package client provides the aihub HTTP API client used by all 32 MCP tools.
package client

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
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

// DetailsRenderLimit is the byte cap formatDetails applies to the compacted
// `details` JSON.
//
// Exported so that a PRODUCER of `details` can hold itself to the budget its
// consumer actually enforces, instead of copying the number and drifting.
// internal/domain.CommitLockRefusalAdvice is the first such producer: its
// refusal advice is useless if it arrives cut in half, and the test that pins
// that has to know where the cut falls. Go marshals map keys alphabetically, so
// what a given key's budget is depends on where its name sorts — a producer
// cannot work that out from the total alone, but it can from this number.
// internal/domain.memoryConflictErr is the second (aihub#375), pinned by
// memory_conflict_details_test.go the same way.
//
// 2048, was 500 (aihub#375). The old number was in practice a patch for ONE
// producer — the CONFLICT_SIMILAR_MEMORY refusal used to carry the colliding
// memory's ENTIRE body in details.existing.content — and every other error paid
// for it: keys past the cut were lost from envelopes that held nothing
// unbounded at all. With that producer bounded at the source, the cap's job
// shrinks to backstopping COUNT-scaled lists, and its size follows from the
// measured envelopes (internal/domain, 2026-09-12, compacted bytes):
//
//	memory conflict, 120-rune excerpt      491 CJK / 634 worst-case 4-byte runes
//	commit-lock refusal                    767 @ 1 conflict, 1219 @ 3
//	duplicate wi, goal at its 500-rune cap 1582  ← largest count-bounded envelope
//	dedup candidates at their LIMIT 50     16166 — still truncated, by design
//
// So 2048 passes every count-bounded envelope whole (largest measured 1582) and
// a commit-lock refusal up to ~6 conflicting files (beyond that the cut only
// reaches keys whose content the untruncated Message already states verbatim),
// while candidates/conflicts lists at scale still hit the backstop. The census
// in internal/domain/err_details_census_test.go is what keeps "count-bounded"
// true: a new details writer must classify itself there before it ships.
const DetailsRenderLimit = 2048

// formatDetails renders the server error `details` object as a compact
// " details=<json>" suffix for the error string, so the conflict metadata the
// server already computes (lock holder, dedup candidates, superseded_by, …)
// reaches the caller instead of being silently dropped. Empty/null details
// yield "". The rendered JSON is capped at DetailsRenderLimit bytes; the server
// contract keeps secrets out of `details`, so passing it through verbatim is
// safe (aihub#209).
func formatDetails(raw json.RawMessage) string {
	if len(raw) == 0 || string(raw) == "null" {
		return ""
	}
	var buf bytes.Buffer
	if err := json.Compact(&buf, raw); err != nil {
		return ""
	}
	s := buf.String()
	if len(s) > DetailsRenderLimit {
		s = s[:DetailsRenderLimit] + "...(truncated)"
	}
	return " details=" + s
}

// APIError is a structured aihub error response: the fields the server
// actually sent, kept apart instead of flattened into one string.
//
// WHY THIS TYPE EXISTS (aihub#414). do() used to return only
// fmt.Errorf("aihub %d %s: %s%s", status, code, message, details), so the CODE
// and the MESSAGE became one piece of text — and callers classified on it with
// strings.Contains. Message and details carry observed values (step names, ids,
// file paths), so any value containing a code-shaped token could flip a
// downstream decision. The MCP layer's classifyStepUpdateErr deletes the
// caller's local credential file on its mismatch arm, so a step named
// "ATTEMPT_MISMATCH" was enough to destroy state (aihub#398 found this and
// routed around it by picking a code with no dangerous substring, which is a
// server-side error vocabulary constrained by a client-side parsing bug).
//
// 🔴 Error() IS BYTE-IDENTICAL to the string do() built before, on purpose. Any
// caller still reading the text — logs, tests, an older consumer of pkg/client —
// sees exactly what it saw. This change ADDS a structured path; it removes no
// information and reformats nothing. Compare by Code and the data cannot reach
// the comparison at all.
type APIError struct {
	StatusCode int
	Code       string
	Message    string
	Details    json.RawMessage
}

func (e *APIError) Error() string {
	// 🔴 The no-Code branch renders the string the unstructured fallback used to
	// return, BYTE-IDENTICALLY (aihub#416). An error body with no `code` is not
	// an aihub error at all — it is the router answering for a route the server
	// does not have, or a proxy — and before this it degraded to a plain
	// fmt.Errorf, which threw the STATUS away. Callers that need to tell "this
	// server has no such route" from "this call was refused" could not: both were
	// opaque strings.
	//
	// Kept byte-identical rather than improved, because the text is what several
	// years of logs and one or two error-matching sites have seen. What changes
	// is that the value is now inspectable; what it PRINTS does not change.
	if e.Code == "" {
		return fmt.Sprintf("aihub %d: unexpected error", e.StatusCode)
	}
	return fmt.Sprintf("aihub %d %s: %s%s", e.StatusCode, e.Code, e.Message, formatDetails(e.Details))
}

// IsStatus reports whether err is an aihub APIError carrying the given HTTP
// status, whether or not the response body was a structured aihub error.
//
// It exists for the one question a CODE cannot answer: "does this server have
// this route at all". A 404 from an unregistered route is produced by the
// router, so it carries no aihub code — the caller has to key on the status.
// aihub#416's repo-pin hop is the first such caller: it must stay silent against
// an aihub older than that change and loud about every other failure.
func IsStatus(err error, status int) bool {
	var apiErr *APIError
	if !errors.As(err, &apiErr) {
		return false
	}
	return apiErr.StatusCode == status
}

// IsCode reports whether err is an aihub APIError whose code is EXACTLY code.
//
// Exact, not a substring: that is the whole point of the type. It unwraps, so a
// call site that wrapped the error with %w for context still classifies.
func IsCode(err error, code string) bool {
	var apiErr *APIError
	if !errors.As(err, &apiErr) {
		return false
	}
	return apiErr.Code == code
}

// idempotencyKeyPrefix namespaces the client-generated Idempotency-Key, per the
// design's `idem_...` form (§4.1 "认证").
const idempotencyKeyPrefix = "idem_"

// newIdempotencyKey mints a key for ONE outbound request.
//
// crypto/rand.Text returns at least 128 bits over the RFC 4648 base32 alphabet
// and, unlike rand.Read, has no error to drop on the floor — which matters for a
// value whose whole job is to be unrepeatable.
func newIdempotencyKey() string { return idempotencyKeyPrefix + rand.Text() }

// setStandardHeaders applies the headers that are a property of the PROTOCOL
// rather than of any one endpoint, so that both request builders in this package
// get them from one place and a third one cannot quietly skip them.
//
// # Idempotency-Key (aihub#436)
//
// The design has required `Idempotency-Key: idem_...` on every mutating request
// since §4.1, restated as H-R3-8 ("header only; all POST/PATCH must carry it"),
// and the server holds up its half — aihub#152 is where its fingerprint check
// was made correct. IdempotencyMiddleware sits on the whole /v1 group, caches
// the response under
// <api_key_id>:<idempotency_key> for 24h, replays it for a later request whose
// fingerprint (method + request target + body) matches, and answers 409
// IDEMPOTENCY_KEY_REUSED when it does not. No client sent the header, so the
// mandatory half of that contract had a compliance rate of zero and the
// middleware was exercised by nothing but its own unit tests.
//
// A FRESH key per outbound request, and deliberately not per logical operation.
// The cache replays on a key hit, so a key reused across two genuinely different
// creates would serve the first one's response for the second — the header would
// stop being a safety net and become a correctness bug. It also gives §11 C9 for
// free: a force_create=true retry after a 409 must not inherit the previous key,
// and here it structurally cannot.
//
// What that cost, and how it is paid (aihub#472). Setting this header is also
// what told net/http it may re-send a mutating request: as of Go 1.26 the
// unexported (*http.Request).isReplayable admits a non-GET method whenever the
// body is nil, NoBody or rewindable AND an Idempotency-Key (or
// X-Idempotency-Key) is present, and (*persistConn).shouldRetryRequest then
// re-sends a request it has already written IN FULL if the reused connection
// fails before the first response byte. Reproduced: the server ran the mutation
// twice and the caller was handed err=nil. The server-side cache does not close
// that — it stores a response only for a finished 2xx under 1 MiB in the same
// process, and a restart is one of the likeliest producers of the very read
// error that triggers the retry.
//
// So this package sends mutating requests on a transport that keeps no idle
// connections (see mutatingClient). shouldRetryRequest returns false for
// !pc.isReused() before it reaches any replay clause, so a request that never
// rides a reused connection is never re-sent AFTER BEING WRITTEN, whatever this
// header says. The
// header therefore keeps its protocol meaning and its server-side replay
// semantics for a CALLER that retries with the same key, while a silent
// transport re-execution is unreachable. A transport retry also cannot raise
// the 409 — not because the fingerprint would match, but because there is no
// transport retry.
//
// A CALLER-level retry (an agent re-invoking pf_ship, pf_claim_work_item, …) is
// a new call into this package and gets a new key. It is a different mechanism
// from the `idempotency_key` BODY parameter on claim, which dedups on
// run_attempts in the database and returns the existing attempt; the two keep
// different guarantees and the design requires both (H-R3-8).
func (c *Client) setStandardHeaders(req *http.Request) {
	req.Header.Set("Authorization", "Bearer "+c.apiKey)
	if isMutatingMethod(req.Method) {
		req.Header.Set("Idempotency-Key", newIdempotencyKey())
	}
}

// isMutatingMethod reports whether a method is one the design calls mutating —
// the set that must carry an Idempotency-Key (§4.1, H-R3-8) and, since
// aihub#472, the set that must not travel on a reusable connection.
//
// It exists so those two answers come from one predicate. If a header rule and
// a connection rule for the same concept were written twice, a method could be
// added to one and not the other, and the result — a mutation that carries the
// header and still travels on a pooled connection — is precisely aihub#472 with
// nothing to announce it. DELETE is the live illustration of the coupling: it
// gets no header, so isReplayable does not admit it and the pooled transport is
// safe for it; the day it gets one it must move clients in the same edit.
func isMutatingMethod(method string) bool {
	return method == http.MethodPost || method == http.MethodPatch
}

// requestTimeout bounds every outbound request, on both clients below.
const requestTimeout = 30 * time.Second

// Client is the aihub HTTP API client.
type Client struct {
	baseURL string
	apiKey  string

	// httpClient carries the safe methods and pools connections as usual.
	httpClient *http.Client
	// mutatingClient carries POST/PATCH and keeps no idle connections.
	mutatingClient *http.Client
}

// New creates a new aihub client.
func New(baseURL, apiKey string) *Client {
	return &Client{
		baseURL:        strings.TrimRight(baseURL, "/"),
		apiKey:         apiKey,
		httpClient:     &http.Client{Timeout: requestTimeout},
		mutatingClient: &http.Client{Timeout: requestTimeout, Transport: nonPoolingTransport()},
	}
}

// nonPoolingTransport returns a transport that uses each connection for exactly
// one request.
//
// This is aihub#472's fix. net/http re-sends a request it has already written
// in full — transportReadFromServerError, errServerClosedIdle — but only on a
// connection it took from the idle pool: shouldRetryRequest returns false on
// !pc.isReused() before it consults isReplayable at all. Taking mutating
// requests out of the pool therefore makes a silent second execution structurally
// unreachable, for every mutating request rather than for a subset.
//
// The narrower-looking fix, clearing req.GetBody on POST/PATCH so isReplayable
// stops admitting them, was measured and rejected. Three reasons, in order of
// weight:
//
//  1. It misses the no-body mutations entirely. NewRequestWithContext turns a
//     zero-length *bytes.Reader into http.NoBody, so isReplayable's first
//     disjunct is satisfied by `r.Body == NoBody` no matter what GetBody holds,
//     and ActivateMemory, RotateProjectIdentifier and any doRaw POST stay exactly
//     as double-executable as before. TestMutatingRequestIsNotRetriedByTransport
//     carries that arm; under a GetBody-only fix it still reports two executions
//     and err=nil.
//  2. It removes a retry that was always safe. The nothingWrittenError clause
//     is `req.outgoingLength() == 0 || req.GetBody != nil`; a bodied POST
//     satisfied it through GetBody long before aihub#436, and nothing was
//     written, so re-sending could not double-execute. Clearing GetBody
//     surrenders it and lands BELOW the pre-#436 behaviour. Not pooling the
//     connection loses nothing instead: the stale-idle-connection race that
//     retry compensates for cannot happen on a connection that was just dialled.
//  3. GetBody has other readers. HTTP/2 uses it to replay a stream the server
//     answered REFUSED_STREAM or GOAWAY — provably not processed, so provably
//     safe — and returns a hard error without it; Client.do uses it to follow a
//     307/308 with the body intact, and silently stops following the redirect
//     when it is nil. Neither is reachable against aihub today, and neither has
//     anything to do with this bug.
//
// Scope, stated rather than implied: shouldRetryRequest is the HTTP/1 path, and
// that is the only one aihub is reached over (an http:// base URL never
// negotiates h2). Over HTTP/2 the decision belongs to http2shouldRetryRequest
// instead, which never consults isReplayable and so was never affected by
// aihub#436 in either direction; DisableKeepAlives makes those connections
// single-use but does not change that function. Nothing here makes the h2 path
// worse, and nothing here should be read as a proof about it.
//
// The price is one connection setup per mutating request. That is the honest
// cost of the guarantee, and it is paid on calls that already carry a database
// write behind a 30s timeout.
func nonPoolingTransport() http.RoundTripper { return nonPoolingFrom(http.DefaultTransport) }

// nonPoolingFrom is nonPoolingTransport with its one input made an argument, so
// that the fallback below is reachable from a test. A branch whose whole job is
// to hold the guarantee in the case nobody expects is the last one that should
// be unexecuted.
func nonPoolingFrom(base0 http.RoundTripper) http.RoundTripper {
	base, ok := base0.(*http.Transport)
	if !ok {
		// A program that replaced http.DefaultTransport with something that is
		// not an *http.Transport leaves nothing to clone. Build the guarantee
		// from scratch rather than fall back to the shared default: what this
		// transport must NOT do is the whole reason it exists.
		return &http.Transport{Proxy: http.ProxyFromEnvironment, DisableKeepAlives: true}
	}
	tr := base.Clone() // keeps proxy, dial and TLS settings; drops the shared pool
	tr.DisableKeepAlives = true
	return tr
}

// clientFor returns the *http.Client that must carry a request with this
// method. Both request builders go through it, so a third one cannot quietly
// put a mutation back on a pooled connection.
func (c *Client) clientFor(method string) *http.Client {
	if isMutatingMethod(method) {
		return c.mutatingClient
	}
	return c.httpClient
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
	c.setStandardHeaders(req)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	resp, err := c.clientFor(method).Do(req)
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
			// aihub#414: the structured error, not a rendered string. Its Error()
			// reproduces the previous text exactly, so this is additive.
			return &APIError{
				StatusCode: resp.StatusCode,
				Code:       errResp.Code,
				Message:    errResp.Message,
				Details:    errResp.Details,
			}
		}
		// Structured even with no code, so the STATUS survives for IsStatus.
		// Error() renders exactly the string this used to return.
		return &APIError{StatusCode: resp.StatusCode, Message: errResp.Message}
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
	c.setStandardHeaders(req)

	resp, err := c.clientFor(method).Do(req)
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
			// aihub#414: same envelope, same type — a caller must not have to know
			// which of the two transports produced its error to classify it.
			return nil, "", &APIError{
				StatusCode: resp.StatusCode,
				Code:       errResp.Code,
				Message:    errResp.Message,
				Details:    errResp.Details,
			}
		}
		// Structured even with no code, for the reason the sibling site above
		// gives: the status is the only thing that answers "does this server have
		// this route", and a plain fmt.Errorf discards it.
		return nil, "", &APIError{StatusCode: resp.StatusCode, Message: errResp.Message}
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

// RecordRepoPins calls POST /v1/work_items/:wiID/repo_pins — the second hop of a
// claim (aihub#416). body carries the attempt credentials and
// {"<repo>": "<40-char sha>"} read out of the worktrees the claim just created.
//
// Separate from ClaimWorkItem because the worktrees do not exist yet when the
// claim goes out; see domain.RecordRepoPinsRequest for why predicting them
// instead was the option that was rejected.
func (c *Client) RecordRepoPins(ctx context.Context, wiID string, body any) (map[string]any, error) {
	var out map[string]any
	return out, c.do(ctx, "POST", "/v1/work_items/"+seg(wiID)+"/repo_pins", body, &out)
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
