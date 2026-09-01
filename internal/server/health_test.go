package server

// aihub#316: /v1/health must report the embedding backend, not just claim "ok".
//
// These run against the healthDBPingFn seam rather than a live pool, so they
// execute on CI's plain "Unit tests" step (which deliberately leaves
// AIHUB_TEST_DB unset) instead of SKIPping there.

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/labstack/echo/v4"

	"github.com/GMISWE/ieops-aihub/internal/domain"
	"github.com/GMISWE/ieops-aihub/internal/embedding"
)

// probeSecret is the sentinel buried in the fake provider's error. /v1/health is
// unauthenticated (it is registered before the BearerAuth group in
// NewRouter), and the real error text names the embedding backend's base URL —
// so the assertion is that NOTHING of it reaches the body.
const probeSecret = "sk-live-embed-host.internal:8090"

// fakeHealthProvider counts Ping calls and returns a fixed error.
type fakeHealthProvider struct {
	pingErr error
	pings   atomic.Int64
}

func (f *fakeHealthProvider) Embed(context.Context, string) ([]float32, error) { return nil, nil }
func (f *fakeHealthProvider) EmbedBatch(_ context.Context, texts []string) ([][]float32, error) {
	return make([][]float32, len(texts)), nil
}
func (f *fakeHealthProvider) ModelID() string { return "fake-health-model" }
func (f *fakeHealthProvider) Dims() int       { return 4 }
func (f *fakeHealthProvider) Ping(context.Context) error {
	f.pings.Add(1)
	return f.pingErr
}

// installProvider swaps domain's package-level provider for the duration of the
// test and clears the probe cache on both edges, so neither the previous test's
// verdict leaks in nor this one's leaks out.
func installProvider(t *testing.T, p embedding.Provider) {
	t.Helper()
	domain.InitEmbeddingProvider(p)
	domain.ResetEmbeddingHealthCache()
	t.Cleanup(func() {
		domain.InitEmbeddingProvider(&embedding.NoopProvider{})
		domain.ResetEmbeddingHealthCache()
	})
}

// withDBPing overrides the database half of the health answer.
func withDBPing(t *testing.T, err error) {
	t.Helper()
	prev := healthDBPingFn
	healthDBPingFn = func(context.Context, *pgxpool.Pool) error { return err }
	t.Cleanup(func() { healthDBPingFn = prev })
}

// getHealth issues one GET /v1/health and returns the recorder plus the decoded
// body.
func getHealth(t *testing.T) (*httptest.ResponseRecorder, map[string]any) {
	t.Helper()
	return getHealthWithCtx(t, context.Background())
}

// getHealthWithCtx is getHealth with control over the request's context, for
// the cases where the CALLER's lifetime is the thing under test.
func getHealthWithCtx(t *testing.T, ctx context.Context) (*httptest.ResponseRecorder, map[string]any) {
	t.Helper()
	e := echo.New()
	req := httptest.NewRequest(http.MethodGet, "/v1/health", nil).WithContext(ctx)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	if err := handleHealth(nil)(c); err != nil {
		t.Fatalf("handleHealth: %v", err)
	}
	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode body %q: %v", rec.Body.String(), err)
	}
	return rec, body
}

func TestHandleHealth_HealthyProvider(t *testing.T) {
	withDBPing(t, nil)
	installProvider(t, &fakeHealthProvider{})

	rec, body := getHealth(t)

	if rec.Code != http.StatusOK {
		t.Errorf("code = %d, want 200", rec.Code)
	}
	if body["status"] != "ok" {
		t.Errorf("status = %v, want ok", body["status"])
	}
	if body["db_ok"] != true {
		t.Errorf("db_ok = %v, want true", body["db_ok"])
	}
	if body["embedding_enabled"] != true {
		t.Errorf("embedding_enabled = %v, want true", body["embedding_enabled"])
	}
	if body["embedding_ok"] != true {
		t.Errorf("embedding_ok = %v, want true", body["embedding_ok"])
	}
	if _, present := body["embedding_error_kind"]; present {
		t.Errorf("embedding_error_kind present on a healthy response: %v", body["embedding_error_kind"])
	}
}

func TestHandleHealth_FailingProviderIsDegradedAndLeaksNothing(t *testing.T) {
	withDBPing(t, nil)
	installProvider(t, &fakeHealthProvider{
		pingErr: errors.New("openai embed http: Post \"https://" + probeSecret + "/v1/embeddings\": dial tcp: connect: connection refused"),
	})

	rec, body := getHealth(t)

	// 🔴 Still 200. Orchestrator liveness probes and cli/doctor.go's
	// checkConfig read this endpoint's reachability and ignore the body; a 503
	// for an OPTIONAL dependency would restart a server that is still serving,
	// and would make `polyforge doctor` say "aihub unreachable".
	if rec.Code != http.StatusOK {
		t.Errorf("code = %d, want 200 — a degraded optional dependency must not read as dead", rec.Code)
	}
	if body["status"] != "degraded" {
		t.Errorf("status = %v, want degraded", body["status"])
	}
	if body["embedding_enabled"] != true {
		t.Errorf("embedding_enabled = %v, want true", body["embedding_enabled"])
	}
	if body["embedding_ok"] != false {
		t.Errorf("embedding_ok = %v, want false", body["embedding_ok"])
	}
	if body["embedding_error_kind"] != "unreachable" {
		t.Errorf("embedding_error_kind = %v, want unreachable", body["embedding_error_kind"])
	}

	raw := rec.Body.String()
	for _, leak := range []string{probeSecret, "dial tcp", "connection refused", "embeddings"} {
		if strings.Contains(raw, leak) {
			t.Errorf("unauthenticated /v1/health body leaked %q from the probe error: %s", leak, raw)
		}
	}
}

func TestHandleHealth_TimeoutIsItsOwnErrorKind(t *testing.T) {
	withDBPing(t, nil)
	installProvider(t, &fakeHealthProvider{
		pingErr: errors.New("openai embed http: " + probeSecret + ": " + context.DeadlineExceeded.Error()),
	})

	// A wrapped-but-not-%w error must NOT be classified as a timeout: the kind
	// has to come from the error chain, not from the text happening to contain
	// the words.
	_, body := getHealth(t)
	if body["embedding_error_kind"] != "unreachable" {
		t.Errorf("substring-only deadline text classified as %v, want unreachable", body["embedding_error_kind"])
	}

	installProvider(t, &fakeHealthProvider{
		pingErr: errors.Join(errors.New("openai embed http: "+probeSecret), context.DeadlineExceeded),
	})
	_, body = getHealth(t)
	if body["embedding_error_kind"] != "timeout" {
		t.Errorf("embedding_error_kind = %v, want timeout", body["embedding_error_kind"])
	}
	if body["status"] != "degraded" {
		t.Errorf("status = %v, want degraded", body["status"])
	}
}

func TestHandleHealth_DisabledProviderIsNotADegradation(t *testing.T) {
	withDBPing(t, nil)
	installProvider(t, &embedding.NoopProvider{})

	rec, body := getHealth(t)

	if rec.Code != http.StatusOK {
		t.Errorf("code = %d, want 200", rec.Code)
	}
	if body["status"] != "ok" {
		t.Errorf("status = %v, want ok — a dependency that was never configured is not a degradation", body["status"])
	}
	if body["embedding_enabled"] != false {
		t.Errorf("embedding_enabled = %v, want false", body["embedding_enabled"])
	}
	if body["embedding_ok"] != true {
		t.Errorf("embedding_ok = %v, want true", body["embedding_ok"])
	}
}

// TestHandleHealth_DBDownIsDegraded closes the gap the old handler had: it
// reported db_ok and still said "status": "ok".
func TestHandleHealth_DBDownIsDegraded(t *testing.T) {
	withDBPing(t, errors.New("pool ping failed"))
	installProvider(t, &fakeHealthProvider{})

	rec, body := getHealth(t)

	if rec.Code != http.StatusOK {
		t.Errorf("code = %d, want 200", rec.Code)
	}
	if body["db_ok"] != false {
		t.Errorf("db_ok = %v, want false", body["db_ok"])
	}
	if body["status"] != "degraded" {
		t.Errorf("status = %v, want degraded", body["status"])
	}
}

// TestHandleHealth_ProbeIsCached: /v1/health is polled by container runtimes, so
// an uncached probe would put steady load on the backend this wi is protecting.
func TestHandleHealth_ProbeIsCached(t *testing.T) {
	withDBPing(t, nil)
	fake := &fakeHealthProvider{}
	installProvider(t, fake)

	for i := 0; i < 3; i++ {
		if _, body := getHealth(t); body["status"] != "ok" {
			t.Fatalf("call %d: status = %v, want ok", i+1, body["status"])
		}
	}
	if got := fake.pings.Load(); got != 1 {
		t.Errorf("Ping called %d times across 3 /v1/health calls inside the TTL, want 1", got)
	}

	// And the cache is per-result, not permanent: clearing it re-probes.
	domain.ResetEmbeddingHealthCache()
	getHealth(t)
	if got := fake.pings.Load(); got != 2 {
		t.Errorf("Ping called %d times after a cache reset, want 2", got)
	}
}

// ctxRespectingProvider reports healthy unless the context handed to Ping is
// already done — it turns "which context reached the probe" into an assertable
// output.
type ctxRespectingProvider struct{ fakeHealthProvider }

func (c *ctxRespectingProvider) Ping(ctx context.Context) error {
	c.pings.Add(1)
	select {
	case <-ctx.Done():
		return ctx.Err()
	default:
		return nil
	}
}

// TestHandleHealth_CancelledRequestDoesNotPoisonTheSharedCache
//
// The probe result is SHARED: one caller probes, and everyone else is answered
// from that cache for the next TTL. So the probe must not inherit the
// requesting caller's cancellation — otherwise one impatient client (a monitor
// with a 1s timeout that hangs up mid-probe) caches OK=false and every later
// poll reads "degraded" from the cache, for a backend that is fine.
//
// Discriminating: with the probe context derived from the request context
// instead of context.WithoutCancel, the first assertion below fails.
func TestHandleHealth_CancelledRequestDoesNotPoisonTheSharedCache(t *testing.T) {
	withDBPing(t, nil)
	fake := &ctxRespectingProvider{}
	installProvider(t, fake)

	dead, cancel := context.WithCancel(context.Background())
	cancel() // the client is already gone before the handler runs

	_, body := getHealthWithCtx(t, dead)
	if body["embedding_ok"] != true {
		t.Errorf("embedding_ok = %v, want true — the caller's cancellation reached the shared probe",
			body["embedding_ok"])
	}
	if body["status"] != "ok" {
		t.Errorf("status = %v, want ok", body["status"])
	}

	// And the poisoning is what actually matters: a healthy follow-up caller
	// must not be served a "degraded" verdict cached by the one that left.
	_, body = getHealth(t)
	if body["status"] != "ok" {
		t.Errorf("follow-up status = %v, want ok — a departed caller's verdict was cached for everyone", body["status"])
	}
}

// stubWedgeBackstop is how long the wedged-pool stub pretends to be stuck
// before giving up on its own. Its value is pinned from both sides:
//
//   - ABOVE the 5s ceiling asserted below, so that when the handler is NOT
//     bounded the call returns at 8s and trips that assertion. Below 5s and the
//     unbounded case would return in time and the test would pass with the bug
//     present.
//   - low enough that the red path finishes in seconds rather than running to
//     `go test`'s 10m timeout, which is what "hangs" looks like to whoever runs
//     it next.
//
// With the bound in place the stub never reaches its backstop at all: ctx.Done
// fires first, at 2s. Written as a literal rather than derived from
// healthDBPingTimeout — a fixture computed from the constant under test moves
// with it, and then the assertion survives any change to that constant.
const stubWedgeBackstop = 8 * time.Second

// TestHandleHealth_WedgedDBDoesNotHangTheEndpoint
//
// /v1/health is what an operator hits to find out why things are slow, and the
// one handler here that does not go through contextWithTimeout. A health check
// that blocks on a wedged pool reads as "dead" to every liveness probe, which
// is strictly worse than answering "degraded".
func TestHandleHealth_WedgedDBDoesNotHangTheEndpoint(t *testing.T) {
	prev := healthDBPingFn
	healthDBPingFn = func(ctx context.Context, _ *pgxpool.Pool) error {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(stubWedgeBackstop):
			// Backstop, and it is load-bearing. A stub that ONLY waits on
			// ctx.Done() makes this test HANG when the bound is missing —
			// verified: neutralising healthDBPingTimeout ran it past a 5-minute
			// wall clock. A test that hangs instead of failing is not a gate;
			// it just looks like the harness broke. With the backstop the
			// unbounded handler returns after stubWedgeBackstop and trips the
			// elapsed-time assertion below in bounded time.
			return errors.New("stub backstop: handler never bounded the ping")
		}
	}
	t.Cleanup(func() { healthDBPingFn = prev })
	installProvider(t, &fakeHealthProvider{})

	start := time.Now()
	rec, body := getHealth(t)
	elapsed := time.Since(start)

	// healthDBPingTimeout is 2s; the literal ceiling here is deliberately not
	// written as `healthDBPingTimeout + slack`, because deriving the bound from
	// the constant under test would let a change to it move the goalposts.
	if elapsed > 5*time.Second {
		t.Errorf("handleHealth took %v against a wedged pool — it is unbounded", elapsed)
	}
	if rec.Code != http.StatusOK {
		t.Errorf("code = %d, want 200", rec.Code)
	}
	if body["db_ok"] != false {
		t.Errorf("db_ok = %v, want false", body["db_ok"])
	}
	if body["status"] != "degraded" {
		t.Errorf("status = %v, want degraded", body["status"])
	}
}

// hangingPingProvider never answers Ping until its context is done — the
// aihub#316 backend. Every other fake in this file returns immediately, which
// means nothing here exercised the probe's own timeout at all until this.
type hangingPingProvider struct{ fakeHealthProvider }

func (h *hangingPingProvider) Ping(ctx context.Context) error {
	h.pings.Add(1)
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-time.After(stubWedgeBackstop):
		// Same backstop, same reason as the wedged-DB stub below — and this is
		// the SECOND time the omission bit during aihub#316. A stub that only
		// waits on ctx.Done() turns "the probe timeout was removed" from a red
		// test into a hung one, and a hung test reads as a broken harness, so
		// nobody treats it as a finding. 8s is above the 6s ceiling asserted in
		// the test, so the unbounded case trips that assertion instead.
		return errors.New("stub backstop: the probe never bounded its Ping")
	}
}

// TestHandleHealth_HangingBackendIsBoundedByTheProbeTimeout
//
// The whole point of aihub#316 is a backend that hangs, and /v1/health is the
// endpoint that is supposed to REPORT that. If the probe inherited the hang,
// the health check would reproduce the outage it exists to describe.
//
// This also pins embeddingProbeTimeout, which no assertion previously touched:
// deleting the context.WithTimeout inside EmbeddingStatusSnapshot, or raising
// it to 60s, passed the entire file before this test existed.
func TestHandleHealth_HangingBackendIsBoundedByTheProbeTimeout(t *testing.T) {
	withDBPing(t, nil)
	installProvider(t, &hangingPingProvider{})

	start := time.Now()
	rec, body := getHealth(t)
	elapsed := time.Since(start)

	// Literal bounds around the documented 2s, written out rather than derived
	// from the constant: a fixture computed from the value under test moves
	// with it and stops being able to fail.
	if elapsed < 1500*time.Millisecond {
		t.Errorf("probe returned after %s — it did not actually wait on the hanging backend", elapsed)
	}
	if elapsed > 6*time.Second {
		t.Errorf("probe took %s — /v1/health inherited the hang it exists to report", elapsed)
	}
	if rec.Code != http.StatusOK {
		t.Errorf("code = %d, want 200", rec.Code)
	}
	if body["embedding_ok"] != false {
		t.Errorf("embedding_ok = %v, want false", body["embedding_ok"])
	}
	if body["embedding_error_kind"] != "timeout" {
		t.Errorf("embedding_error_kind = %v, want timeout", body["embedding_error_kind"])
	}
	if body["status"] != "degraded" {
		t.Errorf("status = %v, want degraded", body["status"])
	}
}

// TestHealthRouteIsUnauthenticated pins the premise the closed ErrorKind set
// rests on.
//
// Every leak assertion in this file is justified by "/v1/health is reachable
// without a token", and that is a fact about ROUTE REGISTRATION — the route is
// added before the BearerAuth group in NewRouter. Nothing asserted it, so
// moving the route into (or out of) that group would change the security
// premise with nothing going red. Every other test here calls handleHealth
// directly and would not notice.
func TestHealthRouteIsUnauthenticated(t *testing.T) {
	withDBPing(t, nil)
	installProvider(t, &embedding.NoopProvider{})

	e := NewRouter(nil, []byte("test-cookie-secret-at-least-32-bytes!!"))
	req := httptest.NewRequest(http.MethodGet, "/v1/health", nil) // no Authorization header
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("GET /v1/health without a token returned %d, want 200. If this route "+
			"moved behind BearerAuth, the closed-ErrorKind reasoning in this file is "+
			"now over-strict; if it moved out from behind it, re-check for leaks.", rec.Code)
	}
	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode %q: %v", rec.Body.String(), err)
	}
	if _, ok := body["status"]; !ok {
		t.Errorf("unauthenticated response has no status field: %v", body)
	}
}

// TestEmbeddingStatusSnapshot_BootFailureIsNotReportedAsDisabled
//
// cmd/aihub/main.go replaces a configured-but-unreachable provider with a
// NoopProvider for the whole process lifetime. Without the boot note, that is
// indistinguishable from "embedding was never configured", and /v1/health would
// answer status:"ok" for a deployment that stopped writing vectors the moment
// it booted — the precise failure this wi exists to make visible.
func TestEmbeddingStatusSnapshot_BootFailureIsNotReportedAsDisabled(t *testing.T) {
	withDBPing(t, nil)
	installProvider(t, &embedding.NoopProvider{})

	// Baseline: a genuinely unconfigured provider is not a degradation.
	if _, body := getHealth(t); body["status"] != "ok" {
		t.Fatalf("unconfigured baseline status = %v, want ok", body["status"])
	}

	domain.NoteEmbeddingUnavailableAtBoot()
	t.Cleanup(domain.ResetEmbeddingBootFailure)
	domain.ResetEmbeddingHealthCache()

	_, body := getHealth(t)
	if body["status"] != "degraded" {
		t.Errorf("status = %v, want degraded — a process that gave up on a configured "+
			"backend is reporting itself as healthy", body["status"])
	}
	if body["embedding_enabled"] != true {
		t.Errorf("embedding_enabled = %v, want true — embedding WAS configured", body["embedding_enabled"])
	}
	if body["embedding_ok"] != false {
		t.Errorf("embedding_ok = %v, want false", body["embedding_ok"])
	}
}

// TestEmbeddingStatusSnapshot_DisabledDoesNotProbe asserts the domain-level
// contract directly: a NoopProvider is answered without touching the cache, so
// switching the provider on afterwards is observed immediately rather than
// being masked by a cached "disabled" entry.
func TestEmbeddingStatusSnapshot_DisabledDoesNotProbe(t *testing.T) {
	installProvider(t, &embedding.NoopProvider{})

	st := domain.EmbeddingStatusSnapshot(context.Background())
	if st.Enabled || !st.OK || st.ErrorKind != "" {
		t.Fatalf("disabled snapshot = %+v, want {Enabled:false OK:true ErrorKind:\"\"}", st)
	}
	if time.Since(st.CheckedAt) > time.Minute {
		t.Errorf("CheckedAt = %v, want ~now", st.CheckedAt)
	}

	fake := &fakeHealthProvider{pingErr: errors.New("down: " + probeSecret)}
	domain.InitEmbeddingProvider(fake)
	st = domain.EmbeddingStatusSnapshot(context.Background())
	if !st.Enabled || st.OK {
		t.Errorf("after enabling, snapshot = %+v, want Enabled:true OK:false — a cached 'disabled' entry masked the switch", st)
	}
}
