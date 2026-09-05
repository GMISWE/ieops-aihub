package domain

// aihub#316: a liveness view of the embedding backend for /v1/health.
//
// Provider.Ping has existed since aihub#192 and is documented as "issues a
// minimal embed call to verify the backend is reachable", but it was called
// exactly once, at boot (cmd/aihub/main.go). Nothing noticed when the backend
// died afterwards — which is precisely what happened on 2026-09-01: the only
// externally visible signal was unrelated endpoints returning 500 after 30.00s.
//
// This file is separate from embedding.go (which owns the unexported
// embProvider var) because that file is held by another work item; a second
// file in the same package reads embProvider just as well.

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sync"
	"time"
)

const (
	// embeddingProbeTimeout bounds one Ping. Short on purpose: this probe runs
	// on an endpoint whose whole job is to answer quickly, and a probe that can
	// hang reproduces the failure it is meant to report.
	embeddingProbeTimeout = 2 * time.Second

	// embeddingProbeTTL is how long a probe result is reused. /v1/health is
	// polled by container runtimes and by `polyforge doctor`, so an uncached
	// probe would put a steady load on the very backend this wi exists to
	// protect.
	embeddingProbeTTL = 15 * time.Second
)

// Error kinds reported to unauthenticated callers. This is a CLOSED set: the
// raw probe error carries the embedding backend's base URL (and, on some
// providers, request echoes), and /v1/health is registered before the
// BearerAuth group in server/router.go. The full error goes to stderr; only one
// of these three literals ever reaches a response body.
const (
	embErrKindTimeout     = "timeout"
	embErrKindUnreachable = "unreachable"
)

// 🔴 What this probe CANNOT tell you, stated here because the field it feeds is
// easy to over-read: Provider.Ping embeds the 4-character string "ping". A
// backend that is up but slow answers that in milliseconds while timing out a
// real embed — production sends up to embedInputMaxRunes (embed_input.go) for
// both a work item and a memory. (Until aihub#361 memory.go's Remember sent
// req.Content with no truncation at all, so it could also fail on LENGTH
// alone; it now goes through MemoryEmbedInput like every other writer.)
// In that state OK is true and every write silently lands with a NULL vector.
// Worse for updates: refreshWorkItemEmbeddingBestEffort returns early when the
// embed fails (wi_embedding.go), so the row KEEPS ITS OLD VECTOR for the new
// text and semantic search matches on content that no longer exists.
//
// So: OK==true means "the backend answers", NOT "vectors are being written".
// Closing that gap needs a counter on dropped embeddings, which this wi does
// not add — do not read this field as though it already exists.

// EmbeddingStatus is the health view of the embedding provider.
type EmbeddingStatus struct {
	// Enabled is false when the configured provider is a NoopProvider.
	Enabled bool
	// OK reports whether the last probe succeeded. True when disabled: an
	// optional dependency that was never asked for is not a degradation.
	OK bool
	// ErrorKind is "", "timeout" or "unreachable" — never the error text.
	ErrorKind string
	// CheckedAt is when the reported result was produced.
	CheckedAt time.Time
}

var (
	embHealthMu    sync.Mutex
	embHealthCache EmbeddingStatus // zero CheckedAt ⇒ never probed
	// embGaveUpAtBoot is set when embedding was CONFIGURED but the process
	// could not reach it at startup and permanently fell back to NoopProvider.
	// Without it that state is indistinguishable from "embedding was never
	// configured", and /v1/health would answer status:"ok" for a deployment
	// that silently stopped writing vectors the moment it booted (aihub#316).
	embGaveUpAtBoot bool
)

// NoteEmbeddingUnavailableAtBoot records that embedding was asked for and could
// not be reached, so /v1/health reports the process as degraded rather than as
// "embedding not configured".
//
// Called from cmd/aihub/main.go on the paths where it replaces a configured
// provider with a NoopProvider. That replacement is permanent — there is no
// retry — so this verdict is deliberately a latch: it says "this process gave
// up", which stays true until the process restarts, and a restart is exactly
// the action the operator needs to take.
func NoteEmbeddingUnavailableAtBoot() {
	embHealthMu.Lock()
	defer embHealthMu.Unlock()
	embGaveUpAtBoot = true
}

// EmbeddingStatusSnapshot returns the current embedding backend status,
// probing at most once per embeddingProbeTTL.
//
// The mutex is held across the probe rather than only around the cache read.
// That serialises a burst of concurrent /v1/health requests onto ONE probe —
// the second caller waits for the first and then reads its result — instead of
// letting each of them open its own connection to a backend that is, by
// hypothesis, already struggling.
func EmbeddingStatusSnapshot(ctx context.Context) EmbeddingStatus {
	if isNoopProvider(embProvider) {
		embHealthMu.Lock()
		gaveUp := embGaveUpAtBoot
		embHealthMu.Unlock()
		if gaveUp {
			// Configured, unreachable at boot, permanently downgraded. Probing
			// is pointless (the provider IS the Noop now), but reporting this
			// as "not enabled, all fine" would be a lie of exactly the kind
			// this wi exists to remove.
			return EmbeddingStatus{
				Enabled: true, OK: false,
				ErrorKind: embErrKindUnreachable,
				CheckedAt: time.Now().UTC(),
			}
		}
		// No probe and no cache write: with embedding switched off there is
		// nothing to be stale about, and writing a "disabled" entry would have
		// to be invalidated by InitEmbeddingProvider.
		return EmbeddingStatus{Enabled: false, OK: true, CheckedAt: time.Now().UTC()}
	}

	embHealthMu.Lock()
	defer embHealthMu.Unlock()

	if !embHealthCache.CheckedAt.IsZero() && time.Since(embHealthCache.CheckedAt) < embeddingProbeTTL {
		return embHealthCache
	}

	// 🔴 WithoutCancel, not ctx directly. The probe result is SHARED — one
	// caller's probe is cached and answered to everyone for the next TTL — so
	// binding it to one request's lifetime lets that request poison the cache
	// for all the others. Concretely: a monitor polling /v1/health with a 1s
	// client timeout hangs up while the probe is still running, ctx is
	// cancelled, Ping returns context.Canceled, and this caches OK=false for
	// 15s. Every subsequent poll then reads "degraded" from the cache — a
	// permanent false alarm produced by the impatient client, not the backend.
	// The same applies without any disconnect: a caller arriving with less
	// than embeddingProbeTimeout left on its own deadline would silently get a
	// truncated probe. Values (tracing, etc.) are preserved; only cancellation
	// and the inherited deadline are dropped, and embeddingProbeTimeout below
	// is what actually bounds this call.
	probeCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), embeddingProbeTimeout)
	defer cancel()

	st := EmbeddingStatus{Enabled: true, OK: true}
	if err := embProvider.Ping(probeCtx); err != nil {
		st.OK = false
		st.ErrorKind = classifyEmbeddingProbeError(err)
		// Full detail to stderr only — see the closed-set comment above.
		fmt.Fprintf(os.Stderr, "embedding health: probe failed (kind=%s): %v\n", st.ErrorKind, err)
	}
	// Stamped AFTER the probe, not before: a 2s probe stamped on entry would
	// spend 2 of its 15 TTL seconds already elapsed, so the cache would really
	// last 13s and the constant would not mean what it says.
	st.CheckedAt = time.Now().UTC()
	embHealthCache = st
	return st
}

// classifyEmbeddingProbeError maps a probe error onto the closed ErrorKind set.
// The distinction that matters operationally is "the backend answered too
// slowly" (the aihub#316 failure) versus anything else; finer detail would mean
// leaking provider-supplied text to an unauthenticated caller.
func classifyEmbeddingProbeError(err error) string {
	if err == nil {
		return ""
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return embErrKindTimeout
	}
	return embErrKindUnreachable
}

// ResetEmbeddingBootFailure clears the boot-failure latch.
//
// TEST-ONLY, for the same reason as ResetEmbeddingHealthCache below: in
// production the latch is meant to survive until the process restarts, because
// a restart is the fix.
func ResetEmbeddingBootFailure() {
	embHealthMu.Lock()
	defer embHealthMu.Unlock()
	embGaveUpAtBoot = false
}

// ResetEmbeddingHealthCache drops the cached probe result.
//
// TEST-ONLY. Production code must never call it: the TTL is the whole point of
// the cache. It exists so a test can swap the provider (InitEmbeddingProvider)
// without inheriting the previous test's probe verdict.
func ResetEmbeddingHealthCache() {
	embHealthMu.Lock()
	defer embHealthMu.Unlock()
	embHealthCache = EmbeddingStatus{}
}
