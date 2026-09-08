package server

import (
	"bytes"
	"container/list"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"os"
	"sync"
	"time"

	"github.com/labstack/echo/v4"

	"github.com/GMISWE/ieops-aihub/internal/domain"
)

// Tunables for the in-process idempotency cache.
//
// Four of them exist because of aihub#152 defect 2: the cache had a 24h TTL and
// no eviction of any kind, so "bounded" meant "bounded by how many distinct keys
// a client cares to send".
//
// maxIdempotencyTotalBytes and idempotencyEntryOverhead were added by aihub#461,
// which is the first work item that could MEASURE this cache rather than reason
// about it: no client sent the header until aihub#436, so every earlier number
// here was sized against a map that was permanently empty in production.
const (
	// idempotencyTTL is how long a response stays replayable.
	//
	// 24h is what the design states (§4.1: "server 缓存 24h"), and aihub#461 kept
	// it deliberately rather than shortening it. The measurement is on
	// maxIdempotencyTotalBytes below: a full day of entries at the busiest 24h
	// yet measured is ~1.3 MiB, so the TTL costs no memory worth reclaiming and
	// the byte budget, not the TTL, is what bounds this cache.
	//
	// What the TTL DOES still govern is the window in which one key presented
	// for a DIFFERENT request is caught and answered 409 (see
	// IdempotencyMiddleware). Shortening it narrows that guard in exchange for
	// memory nothing needs, which is why the arithmetic points at leaving it
	// alone.
	idempotencyTTL = 24 * time.Hour

	// maxIdempotencyEntries caps the number of live entries. Reaching it is not
	// an error: the least recently used entry is dropped to make room. See
	// storeIdempotent.
	//
	// Kept at 4096 by aihub#461, now against a measured target instead of none.
	// The busiest 24h measured produced ~1.8e3 mutating requests server-wide
	// (counted as maxIdempotencyTotalBytes describes), so one TTL window sits at
	// ~44% of this cap: the TTL retires entries, the cap does not. It is the cap
	// that binds in the regime this API actually runs in — at the measured mean
	// accounted entry size of ~770 B, 4096 entries is ~3.0 MiB, an order of
	// magnitude under the byte budget. The two caps bind in different regimes
	// and neither is redundant; see maxIdempotencyTotalBytes for the crossover.
	maxIdempotencyEntries = 4096

	// maxIdempotencyBodyBytes caps the RESPONSE body a single entry may hold.
	// Without it one large response defeats the entry cap on its own.
	//
	// ⚠️ This is a real narrowing of the guarantee, and it is deliberate: a
	// response over the cap is served to the client in full but is NOT stored, so
	// a retry of it re-executes rather than replays.
	//
	// 1 MiB, measured against the mutating endpoints rather than assumed
	// (aihub#461). Two of them carry user text and everything else answers with
	// a handful of scalars: the work item (POST /v1/work_items and PATCH
	// /v1/work_items/:id both return the full domain.WorkItem, whose `content`
	// is held to 20,000 characters by a CHECK in migration 0011) and the memory
	// (PATCH /v1/memories/:id/update returns the new lineage head).
	//
	// Measured on the live server, 2026-09-08, over the most recent 200 work
	// items in each of the ten projects — 1,102 in total, six projects having
	// fewer than 200: p50 1,076 B, p99 12,013 B, max 37,078 B. The largest work
	// item MEASURED is therefore 3.5% of this cap. The memory shape has no
	// server-side length cap at all (only the MCP layer's 20,000 characters), so
	// it is the one that can exceed 1 MiB — and when it does, the response is
	// served in full and simply not cached, which is this cap working.
	//
	// 🔴 The shape this comment used to name as the largest — "an artifact with
	// its rendered_html" — cannot reach the cache AT ALL, and that was wrong
	// rather than merely stale:
	//
	//   - POST /v1/memories, the artifact write path and the only endpoint that
	//     accepts an explicit `html=`, answers with a fixed ten-field projection
	//     (id, memory_id, is_new, type, project, visibility, activation_count,
	//     stability_days, base_strength, created_at) that carries neither
	//     `content` nor `rendered_html`.
	//   - PATCH /v1/memories/:id/update does return a whole memory, but
	//     domain.UpdateMemoryRequest has no rendered-HTML field, so the new head
	//     is written through resolveRenderedHTML with no explicit value: NULL
	//     for every type, deferred to the background renderer for methodology.*.
	//     Memory.RenderedHTML is `omitempty`, so it is absent from the JSON.
	//
	// No POST or PATCH under /v1 can therefore produce a rendered_html byte.
	// Measuring len(rendered_html) across the artifacts in the database — which
	// is what this work item was filed asking for — would have answered a
	// question about a shape this cache cannot hold.
	maxIdempotencyBodyBytes = 1 << 20 // 1 MiB

	// maxIdempotencyTotalBytes caps the memory the whole cache may hold. It is
	// the bound that was missing: before it, maxIdempotencyEntries ×
	// maxIdempotencyBodyBytes = 4 GiB was the only ceiling, which is not a bound
	// an operator of a containerised service can act on.
	//
	// # Derivation (aihub#461, measured 2026-09-08 — re-derive, do not trust)
	//
	// Mutating requests per 24h, at the busiest 24h in the record
	// (2026-09-07T12:46Z → 2026-09-08T12:46Z): ~1.8e3 server-wide, against
	// ~3e2 on a normal day (the prior six days ran 89–996 events/day).
	//
	// How that was counted, since neither available source counts requests and
	// the two have opposite biases:
	//
	//   - agent_events for all ten projects, 3,596 rows in the window, mapped to
	//     endpoints per emitter. The mapping is NOT one row per request in
	//     either direction: CreateWorkItem writes exactly one work_item_filed, a
	//     fused step update writes both step_completed and step_started, and
	//     lock_acquired/lock_released are one row PER LOCK from claim,
	//     acquire_locks, the commit gate and attempt termination alike — which
	//     is 46% of the rows in this window. Some requests write nothing at all
	//     (predict_conflicts, an acquire_locks that takes no lock).
	//   - 1,163 client-side MCP tool calls over the same window, deduplicated by
	//     tool_use id, from one machine's transcripts. This one undercounts: a
	//     pf_ship is several requests, and other machines are invisible to it.
	//
	// The two bracket the same quantity from opposite sides and agree to ~1.5×.
	// The per-endpoint table in the test below is what the mix estimate rests
	// on, and re-deriving it is the point of the stats line the purger writes.
	//
	// Bytes per entry, over that request mix: ~770 B accounted (~415 B of
	// response body plus key, fingerprint and idempotencyEntryOverhead). The mix
	// is dominated by tiny responses — PATCH …/step is ~82 B of body and ~23% of
	// all mutating calls — while the work-item shapes above supply most of the
	// volume. TestIdempotency_MeasuredSteadyStateFitsTheBudget re-measures every
	// shape from the real Go response types and fails if this stops holding.
	//
	// Steady state = 1,756 entries × ~770 B = 1,351,897 B ≈ 1.3 MiB, on the
	// busiest day, at a 24h TTL. That total is measured rather than multiplied
	// out here: it is what the test above prints.
	//
	// 32 MiB is 25× that, and the multiple is the point: the cache must absorb a
	// traffic burst or a shift in the response mix without evicting, while still
	// being a number an operator can reason about. It also stays above
	// 16 × maxIdempotencyBodyBytes, so one maximal entry can never dominate the
	// budget (the gate for that is
	// TestIdempotencyCaps_AreMutuallyConsistent).
	//
	// Where the two caps cross over: the byte budget starts binding before the
	// entry cap once the mean entry exceeds maxIdempotencyTotalBytes /
	// maxIdempotencyEntries = 8 KiB. Measured mean is 770 B, so today the entry
	// cap binds first and this one is the guard against the mix changing — a day
	// of 4096 entries the size of the largest work item measured would be
	// 145 MiB, and is held to 32 MiB.
	maxIdempotencyTotalBytes = 32 << 20 // 32 MiB

	// idempotencyEntryOverhead is what one entry costs BESIDES its body, key and
	// fingerprint: the cachedResponse header (72 B), the list.Element (40 B) and
	// the idempotencyEntry it points at (32 B), the map bucket slot, the string
	// headers, and allocator rounding on all of them. It is a deliberate
	// over-estimate — accounting has to be cheap
	// (O(1), no reflection) and a budget is only useful if it errs toward
	// reserving too much. An entry with an 82 B body is accounted at ~430 B.
	idempotencyEntryOverhead = 256

	// idempotencyPurgeInterval is how often the sweep runs. It is not derived
	// from idempotencyTTL: with a 24h TTL any interval from minutes to hours
	// reclaims the same memory, and a sweep is one pass over a map the entry cap
	// holds to maxIdempotencyEntries.
	idempotencyPurgeInterval = 10 * time.Minute

	// maxIdempotencyRequestBytes caps the REQUEST body this middleware is
	// willing to buffer in order to fingerprint it. A request above the cap is
	// passed through with idempotency disabled rather than buffered whole: the
	// middleware must not become the thing that holds an arbitrarily large body
	// in memory.
	maxIdempotencyRequestBytes = 4 << 20 // 4 MiB
)

// cachedResponse holds a cached HTTP response body and metadata.
type cachedResponse struct {
	StatusCode int
	Body       []byte
	// Fingerprint identifies the request this response answered — method, target
	// and body. A second request presenting the same Idempotency-Key with a
	// different fingerprint is a client bug, and answering it with this body was
	// aihub#152 defect 1.
	Fingerprint string
	ExpiresAt   time.Time
}

// idempotencyEntry is one cache entry plus the two things eviction needs: the
// key (so an element found from the recency list can delete its own map slot in
// O(1)) and the accounted size (so the byte total can be maintained
// incrementally rather than recomputed).
type idempotencyEntry struct {
	key   string
	resp  *cachedResponse
	bytes int
}

// idempotencyCache is an in-process cache for idempotent request responses,
// keyed by "<api_key_id>:<idempotency_key>".
//
// A map plus a container/list of the same entries under one mutex — the map
// answers lookups, the list orders them by recency, front = most recently used.
// Not a sync.Map: the cache has to be BOUNDED, and a bound needs an exact size
// and an eviction choice, neither of which sync.Map offers. Contention is not a
// concern — every code path below returns before touching the cache unless the
// request carries an Idempotency-Key.
//
// # Not durable, and the TODO below is worth less than it looks
//
// TODO(M3): implement a durable idempotency cache in the idempotency_cache
// table (design §4.1 offers "PG 独立表 … 或共享 Redis").
//
// ⚠️ Read that TODO against the measurement in IdempotencyMiddleware's comment
// before acting on it. A durable cache buys replay across a restart and across
// replicas — of entries that, as of aihub#472, NOTHING can present the key for.
// Its premise is a caller that deliberately reuses a key; until one exists,
// making this cache durable adds a write to the hot path of every mutating
// request in exchange for hits that are structurally impossible. The order is:
// a reusing caller first, then durability.
var (
	idempotencyMu    sync.Mutex
	idempotencyCache = map[string]*list.Element{}
	idempotencyLRU   = list.New()
	idempotencyBytes int
	idempotencyStats IdempotencyStats
)

// IdempotencyStats is a snapshot of the cache's size and of every decision it
// has made since the process started. Exported because it is the calibration
// instrument for the sizing above: the constants are derived from a traffic
// measurement taken outside this process, and a pinned measurement rots.
//
// 🔴 Hits and Replays are the two numbers that matter most, because they are
// what can FALSIFY the claim this cache is currently sized against — that its
// hit rate is structurally zero (IdempotencyMiddleware explains why). A nonzero
// Hits means a caller reuses keys, which changes what the TTL, the caps and the
// TODO(M3) durable cache are all worth. Nothing else in the process can tell
// anyone that.
type IdempotencyStats struct {
	// Entries and Bytes are live values; the rest are cumulative counters.
	Entries int
	Bytes   int
	// PeakEntries and PeakBytes are the high-water marks, which is what a
	// sizing decision needs — the live value at the moment an operator happens
	// to look says nothing about whether a cap was approached.
	//
	// Sampled AFTER eviction, so they are the largest the cache ever legally
	// HELD, not the transient overshoot inside one store. A cache that is
	// repeatedly at its limit therefore reads as PeakEntries exactly
	// maxIdempotencyEntries with EvictedForEntryCap climbing, which is the pair
	// to read together: the peak alone cannot distinguish "just reached the cap
	// once" from "has been evicting all day".
	PeakEntries int
	PeakBytes   int
	Stores      uint64
	// Hits counts lookups that found a LIVE entry, whether or not the
	// fingerprint then matched; Replays counts the subset that were served back
	// to the client, and ReuseRejected the subset answered 409.
	Hits          uint64
	Replays       uint64
	ReuseRejected uint64
	// EvictedForEntryCap and EvictedForByteCap separate the two caps, so
	// "which limit is actually binding" is answerable without re-deriving it
	// from the entry sizes. An eviction that satisfies both at once is
	// attributed to the entry cap; the counters are a diagnosis of which
	// regime the cache is in, not an audit that has to sum to something.
	EvictedForEntryCap uint64
	EvictedForByteCap  uint64
	PurgedExpired      uint64
	// EvictionScanned counts list elements examined while making room. It is
	// the O(1) claim in makeRoomLocked expressed as an observable: LRU eviction
	// examines exactly one element per entry dropped, and the two-pass scan
	// this replaced examined the whole cache per eviction. See
	// TestIdempotencyEviction_IsConstantWork, which asserts on this rather than
	// on wall-clock time.
	EvictionScanned uint64
}

// requestFingerprint hashes the parts of a request that make it that request:
// method, request target (path AND query), and body. Two requests that differ in
// any of them are different operations even under one Idempotency-Key.
//
// Hashed rather than stored so an entry's size does not grow with the request,
// and length-prefixed so that no reshuffling of the three components can produce
// the same input string (method "POST" + target "/a" must not collide with
// method "POS" + target "T/a").
func requestFingerprint(method, target string, body []byte) string {
	h := sha256.New()
	// hash.Hash documents that Write never returns an error, which is also why
	// h.Write below is unchecked.
	fmt.Fprintf(h, "%d:%s%d:%s%d:", len(method), method, len(target), target, len(body)) //nolint:errcheck // sha256 writes cannot fail
	h.Write(body)
	return hex.EncodeToString(h.Sum(nil))
}

// idempotencyEntrySize is the accounted cost of one entry. See
// idempotencyEntryOverhead for what the constant covers and why the accounting
// deliberately over-estimates.
func idempotencyEntrySize(key string, entry *cachedResponse) int {
	return idempotencyEntryOverhead + len(key) + len(entry.Body) + len(entry.Fingerprint)
}

// loadIdempotent returns the live entry for key, dropping it if it has expired.
// A hit promotes the entry to most-recently-used.
func loadIdempotent(key string) (*cachedResponse, bool) {
	idempotencyMu.Lock()
	defer idempotencyMu.Unlock()
	el, ok := idempotencyCache[key]
	if !ok {
		return nil, false
	}
	entry := el.Value.(*idempotencyEntry)
	if !time.Now().Before(entry.resp.ExpiresAt) {
		removeElementLocked(el)
		return nil, false
	}
	idempotencyLRU.MoveToFront(el)
	idempotencyStats.Hits++
	return entry.resp, true
}

// storeIdempotent inserts an entry as most-recently-used, then makes room.
//
// Both caps are enforced AFTER the insert rather than before it, so the entry
// just written is subject to them like any other. That ordering is what lets one
// pass of makeRoomLocked satisfy the entry cap and the byte cap together.
func storeIdempotent(key string, entry *cachedResponse) {
	idempotencyMu.Lock()
	defer idempotencyMu.Unlock()

	size := idempotencyEntrySize(key, entry)
	if el, exists := idempotencyCache[key]; exists {
		// A repeated key with a fresh response: replace in place and promote,
		// rather than delete-then-insert, so the byte total stays exact.
		prev := el.Value.(*idempotencyEntry)
		idempotencyBytes += size - prev.bytes
		prev.resp, prev.bytes = entry, size
		idempotencyLRU.MoveToFront(el)
	} else {
		idempotencyCache[key] = idempotencyLRU.PushFront(&idempotencyEntry{key: key, resp: entry, bytes: size})
		idempotencyBytes += size
	}
	idempotencyStats.Stores++
	makeRoomLocked()
	recordPeaksLocked()
}

// makeRoomLocked drops least-recently-used entries until both caps hold.
// Caller must hold idempotencyMu.
//
// O(1) per eviction, and that is the whole reason this function exists.
// aihub#152 made room in two stages — sweep every entry for expiry, then scan
// every entry for the one closest to expiry — so an insert into a full cache was
// two O(n) passes over 4096 entries, on the request path, under the global
// mutex, on behalf of a request that in production can never be replayed
// (aihub#461). The recency list makes the victim the element at the back, and
// idempotencyEntry.key lets it delete its own map slot without a search.
//
// Dropping the expiry sweep from this path loses nothing: with a constant TTL,
// insertion order IS expiry order, so the least recently used entry is also the
// one closest to expiry — the same victim the sweep-then-scan picked — and
// entries that expire while the cache is under both caps are reclaimed by
// StartIdempotencyCachePurger, which is off the request path.
func makeRoomLocked() {
	for len(idempotencyCache) > maxIdempotencyEntries || idempotencyBytes > maxIdempotencyTotalBytes {
		victim := idempotencyLRU.Back()
		idempotencyStats.EvictionScanned++
		if victim == nil {
			// Unreachable while maxIdempotencyTotalBytes stays well above one
			// maximal entry (asserted by TestIdempotencyCaps_AreMutuallyConsistent):
			// an empty cache holds 0 entries and 0 bytes, so neither condition
			// above can still be true. Guarded anyway rather than indexed into,
			// because the alternative to a wrong constant here is a nil panic in
			// the middleware.
			return
		}
		if len(idempotencyCache) > maxIdempotencyEntries {
			idempotencyStats.EvictedForEntryCap++
		} else {
			idempotencyStats.EvictedForByteCap++
		}
		removeElementLocked(victim)
	}
}

// removeElementLocked drops one element from both containers and from the byte
// total. Caller must hold idempotencyMu.
//
// Every removal path goes through here — expiry-on-read, eviction and the purge
// sweep — because the byte total is only trustworthy if it is maintained in
// exactly one place. A delete that forgets to decrement it makes the cache
// shrink and its accounted size grow, and the budget then evicts a healthy cache
// down to nothing.
func removeElementLocked(el *list.Element) {
	entry := el.Value.(*idempotencyEntry)
	idempotencyLRU.Remove(el)
	delete(idempotencyCache, entry.key)
	idempotencyBytes -= entry.bytes
}

// recordPeaksLocked updates the high-water marks. Caller must hold idempotencyMu.
func recordPeaksLocked() {
	if n := len(idempotencyCache); n > idempotencyStats.PeakEntries {
		idempotencyStats.PeakEntries = n
	}
	if idempotencyBytes > idempotencyStats.PeakBytes {
		idempotencyStats.PeakBytes = idempotencyBytes
	}
}

// IdempotencyMiddleware checks the Idempotency-Key header.
// If a cached response exists for (api_key_id, idempotency_key) AND the request
// matches the one that produced it, returns it. If the request differs, returns
// 409 IDEMPOTENCY_KEY_REUSED. Otherwise calls next and caches the response.
// Only applies to POST and PATCH requests.
//
// # What this cache is for, as of aihub#472 (measured, not inferred)
//
// Its hit rate in production is structurally zero, and that is a property of two
// changes rather than an observation that might drift:
//
//  1. aihub#436 made pkg/client mint a FRESH key per outbound request
//     (setStandardHeaders → newIdempotencyKey, crypto/rand.Text). Two requests
//     from the in-tree client therefore cannot present the same key. Before
//     that, no client sent the header at all and this cache was permanently
//     empty in production.
//  2. aihub#472 then routed POST and PATCH onto a transport that keeps no idle
//     connections, because sending the header is also what told net/http a
//     mutating request may be re-sent. shouldRetryRequest returns false for a
//     connection that was never reused, so there is no transport retry to
//     replay for POST/PATCH.
//
// The design's stated purpose for this header — §4.1, "防网络重试双写" — is
// therefore now delivered by that transport, not by this cache. What is left
// here is two things, and both are worth keeping:
//
//   - the 409 below, which catches a client reusing one key for a genuinely
//     different request (aihub#152 defect 1) inside the TTL window; and
//   - insurance for a FUTURE caller that deliberately reuses a key — an
//     out-of-tree client, a script, or a caller-level retry that chooses to
//     re-present its key. The middleware is mounted on the whole /v1 group and
//     serves every such caller, so this is a real audience, just not one that
//     exists today.
//
// A caller-level retry from the in-tree client (an agent re-invoking pf_ship,
// pf_claim_work_item, …) mints a new key and is NOT such a caller; it is
// deduplicated, where it is deduplicated at all, by the `idempotency_key` BODY
// parameter on claim, which is a different mechanism against a different table.
//
// 🔴 Do not restate any of this as "either replays or executes exactly once".
// Nothing in production replays. If that ever changes, IdempotencyCacheStats
// will say so — Hits is nonzero — and the sizing on the constants above has to
// be redone rather than patched.
func IdempotencyMiddleware() echo.MiddlewareFunc {
	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error {
			idemKey := c.Request().Header.Get("Idempotency-Key")
			if idemKey == "" {
				return next(c)
			}

			// Only idempotency-key for POST/PATCH
			method := c.Request().Method
			if method != http.MethodPost && method != http.MethodPatch {
				return next(c)
			}

			u := GetUser(c)
			if u == nil {
				// Not authenticated yet; let auth middleware handle it
				return next(c)
			}

			body, ok := bufferRequestBody(c)
			if !ok {
				// Too large to fingerprint. Idempotency is best-effort (it is not
				// durable either), and running the request is strictly better than
				// replaying a response we cannot prove belongs to it.
				return next(c)
			}

			cacheKey := u.APIKeyID + ":" + idemKey
			fingerprint := requestFingerprint(method, c.Request().URL.RequestURI(), body)

			if cached, hit := loadIdempotent(cacheKey); hit {
				if cached.Fingerprint != fingerprint {
					// aihub#152 defect 1: this used to replay cached.Body. The key
					// ignored method, target and body, so reusing one key across two
					// different calls served the FIRST call's response for the second,
					// stamped X-Idempotency-Replayed: true.
					countReuseRejected()
					return writeError(c, domain.NewErr(domain.ErrIdempotencyKeyReused,
						"Idempotency-Key was already used for a different request; use a new key"))
				}
				countReplay()
				c.Response().Header().Set("X-Idempotency-Replayed", "true")
				return c.JSONBlob(cached.StatusCode, cached.Body)
			}

			// Intercept the response body
			resWriter := &responseWriter{ResponseWriter: c.Response().Writer, body: &bytes.Buffer{}}
			c.Response().Writer = resWriter

			err := next(c)

			// Cache successful-or-idempotent responses
			if resWriter.status >= 200 && resWriter.status < 300 && !resWriter.tooLarge {
				storeIdempotent(cacheKey, &cachedResponse{
					StatusCode:  resWriter.status,
					Body:        resWriter.body.Bytes(),
					Fingerprint: fingerprint,
					ExpiresAt:   time.Now().Add(idempotencyTTL),
				})
			}

			return err
		}
	}
}

// countReplay and countReuseRejected split what a HIT turned into. Separate
// tiny functions rather than inline locking so the middleware keeps one
// lock/unlock per counter and no defer on the request path.
func countReplay() {
	idempotencyMu.Lock()
	idempotencyStats.Replays++
	idempotencyMu.Unlock()
}

func countReuseRejected() {
	idempotencyMu.Lock()
	idempotencyStats.ReuseRejected++
	idempotencyMu.Unlock()
}

// bufferRequestBody reads the request body so it can be fingerprinted and puts it
// back for the handler. It reports false when the body exceeds
// maxIdempotencyRequestBytes, in which case the body is still fully restored (the
// buffered prefix followed by the unread remainder) and the caller must skip
// idempotency rather than fail the request.
func bufferRequestBody(c echo.Context) ([]byte, bool) {
	req := c.Request()
	if req.Body == nil || req.Body == http.NoBody {
		return nil, true
	}
	buf, err := io.ReadAll(io.LimitReader(req.Body, maxIdempotencyRequestBytes+1))
	if err != nil {
		// Restore what was read; the handler will surface the read error itself.
		req.Body = io.NopCloser(io.MultiReader(bytes.NewReader(buf), req.Body))
		return nil, false
	}
	if len(buf) > maxIdempotencyRequestBytes {
		req.Body = io.NopCloser(io.MultiReader(bytes.NewReader(buf), req.Body))
		return nil, false
	}
	req.Body = io.NopCloser(bytes.NewReader(buf))
	return buf, true
}

// responseWriter wraps http.ResponseWriter to capture the status and body.
// It stops buffering past maxIdempotencyBodyBytes and records that it did, so an
// oversized response is passed through to the client but never cached.
type responseWriter struct {
	http.ResponseWriter
	status   int
	body     *bytes.Buffer
	tooLarge bool
}

func (rw *responseWriter) WriteHeader(code int) {
	rw.status = code
	rw.ResponseWriter.WriteHeader(code)
}

func (rw *responseWriter) Write(b []byte) (int, error) {
	if rw.status == 0 {
		// net/http implies 200 for a Write with no preceding WriteHeader; without
		// this the entry would be dropped by the 2xx test below for the wrong
		// reason.
		rw.status = http.StatusOK
	}
	if !rw.tooLarge {
		if rw.body.Len()+len(b) > maxIdempotencyBodyBytes {
			rw.tooLarge = true
			rw.body.Reset()
		} else {
			rw.body.Write(b)
		}
	}
	return rw.ResponseWriter.Write(b)
}

// PurgeExpiredIdempotencyCache removes entries whose TTL has passed.
//
// aihub#152 defect 2: this was defined and never called from anywhere, so the
// only eviction that ever happened was the lazy one on a cache HIT after expiry
// — which never fires for the keys that actually accumulate, since a key sent
// once is never looked up again. StartIdempotencyCachePurger is what calls it in
// production; it remains exported because it is also the whole of the cleanup an
// operator or a test needs.
//
// Note what this does and does not buy, because the two are easy to conflate:
// purging reclaims entries after the TTL, it does not BOUND the cache. Nothing
// stops a client filling it inside one TTL window. The bounds are the entry cap
// and the byte budget, both applied in storeIdempotent; this keeps a quiet
// server from holding a day of garbage.
func PurgeExpiredIdempotencyCache() {
	idempotencyMu.Lock()
	defer idempotencyMu.Unlock()
	purgeExpiredLocked(time.Now())
}

// purgeExpiredLocked drops every entry whose ExpiresAt is at or before now.
// Caller must hold idempotencyMu.
//
// O(n) by design, and that is fine: it runs on idempotencyPurgeInterval in its
// own goroutine, never on a request. The eviction path is the one that had to
// become O(1) (makeRoomLocked).
func purgeExpiredLocked(now time.Time) {
	for el := idempotencyLRU.Front(); el != nil; {
		next := el.Next()
		if !now.Before(el.Value.(*idempotencyEntry).resp.ExpiresAt) {
			removeElementLocked(el)
			idempotencyStats.PurgedExpired++
		}
		el = next
	}
}

// IdempotencyCacheLen reports the number of live entries. Exported for tests and
// for anything that wants to observe the cap taking effect.
func IdempotencyCacheLen() int {
	idempotencyMu.Lock()
	defer idempotencyMu.Unlock()
	return len(idempotencyCache)
}

// IdempotencyCacheBytes reports the accounted footprint of the live entries, in
// bytes — the value maxIdempotencyTotalBytes bounds. The counterpart of
// IdempotencyCacheLen for the cap that entries are only a proxy for.
func IdempotencyCacheBytes() int {
	idempotencyMu.Lock()
	defer idempotencyMu.Unlock()
	return idempotencyBytes
}

// IdempotencyCacheStats returns a snapshot of the counters. A snapshot by value:
// nothing outside this file may hold a pointer into state guarded by
// idempotencyMu.
func IdempotencyCacheStats() IdempotencyStats {
	idempotencyMu.Lock()
	defer idempotencyMu.Unlock()
	s := idempotencyStats
	s.Entries = len(idempotencyCache)
	s.Bytes = idempotencyBytes
	return s
}

// idempotencyLogWriter is where the periodic cache line goes. A variable, not
// os.Stderr inline, so a test can assert that the purger actually emits it —
// the line is the calibration instrument, and "the formatter is correct" is not
// the same claim as "something calls it".
//
// Behind its own mutex, and not for a hypothetical reason: the purger runs in a
// goroutine that outlives the test which started it, so a plain variable is
// read by one goroutine while the next test's cleanup writes it. `go test
// -race` reported exactly that, intermittently — the two tests that swap this
// writer do not have to overlap for the previous test's ticker to still be
// mid-tick.
var (
	idempotencyLogMu     sync.Mutex
	idempotencyLogWriter io.Writer = os.Stderr
)

// idempotencyLogTarget reads the current writer.
func idempotencyLogTarget() io.Writer {
	idempotencyLogMu.Lock()
	defer idempotencyLogMu.Unlock()
	return idempotencyLogWriter
}

// setIdempotencyLogWriter swaps the writer and returns the previous one, so a
// caller can restore it. Only tests call it; production never changes the
// target from os.Stderr.
func setIdempotencyLogWriter(w io.Writer) io.Writer {
	idempotencyLogMu.Lock()
	defer idempotencyLogMu.Unlock()
	prev := idempotencyLogWriter
	idempotencyLogWriter = w
	return prev
}

// logIdempotencyCacheStats writes one line per purge tick, in the same
// prefix-then-key=value shape as main.go's gc lines. Deliberately the cheapest
// mechanism available: this repo has no levelled logger, and a periodic line on
// stderr is readable with `docker logs … | grep -F 'idempotency:'` without an
// endpoint, a scrape target or a new dependency.
//
// Silent on a cache that has never been touched, so a quiet server does not
// accumulate 144 uninformative lines a day.
func logIdempotencyCacheStats() {
	s := IdempotencyCacheStats()
	if s.Stores == 0 && s.Entries == 0 {
		return
	}
	// Discarded deliberately: a diagnostic line that cannot be written has
	// nowhere left to report that fact, and failing the purge tick over it would
	// trade the cache's expiry sweep for a log write. errcheck's default
	// exclusion covers fmt.Fprintf to os.Stderr specifically, and this writer is
	// an io.Writer so the exclusion does not apply.
	_, _ = fmt.Fprintf(idempotencyLogTarget(),
		"idempotency: entries=%d/%d bytes=%d/%d peak_entries=%d peak_bytes=%d stores=%d hits=%d replays=%d reuse_rejected=%d evicted_entry_cap=%d evicted_byte_cap=%d purged=%d\n",
		s.Entries, maxIdempotencyEntries, s.Bytes, maxIdempotencyTotalBytes,
		s.PeakEntries, s.PeakBytes, s.Stores, s.Hits, s.Replays, s.ReuseRejected,
		s.EvictedForEntryCap, s.EvictedForByteCap, s.PurgedExpired)
}

// StartIdempotencyCachePurger runs PurgeExpiredIdempotencyCache on
// idempotencyPurgeInterval until ctx is done, and logs one stats line per tick.
// It returns immediately; the loop runs in its own goroutine. Called once from
// cmd/aihub/main.go.
//
// It takes NO interval parameter, and that is deliberate. The first version did,
// and an independent review measured the consequence: passing 0 made the function
// log and return without starting anything — the production purger silently never
// ran, which is exactly the aihub#152 defect — and the test asserting main()
// schedules it stayed GREEN, because an AST check on the call site counts
// arguments and cannot evaluate them. A parameter whose bad values must be caught
// by a test is a hole; a package constant has no bad values to catch.
func StartIdempotencyCachePurger(ctx context.Context) {
	startIdempotencyCachePurger(ctx, idempotencyPurgeInterval)
}

// startIdempotencyCachePurger is the parameterised form, unexported so only tests
// can choose a tick fast enough to observe.
func startIdempotencyCachePurger(ctx context.Context, interval time.Duration) {
	go func() {
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				PurgeExpiredIdempotencyCache()
				logIdempotencyCacheStats()
			}
		}
	}()
}
