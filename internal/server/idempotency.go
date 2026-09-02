package server

import (
	"bytes"
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

// Tunables for the in-process idempotency cache. All three exist because of
// aihub#152 defect 2: the cache had a 24h TTL and no eviction of any kind, so
// "bounded" meant "bounded by how many distinct keys a client cares to send".
const (
	// idempotencyTTL is how long a response stays replayable.
	idempotencyTTL = 24 * time.Hour

	// maxIdempotencyEntries caps the number of live entries. Reaching it is not
	// an error: the cache purges what has expired and then evicts the entry
	// closest to expiry to make room. See storeIdempotent.
	maxIdempotencyEntries = 4096

	// maxIdempotencyBodyBytes caps the RESPONSE body a single entry may hold.
	// Without it one large response defeats the entry cap on its own — the cap
	// that matters is bytes, and entries are only a proxy for it.
	maxIdempotencyBodyBytes = 256 << 10 // 256 KiB

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

// idempotencyCache is an in-process cache for idempotent request responses,
// keyed by "<api_key_id>:<idempotency_key>".
//
// Note: This is not durable. A PostgreSQL-backed cache would survive restarts.
// TODO(M3): implement durable idempotency cache in the idempotency_cache table.
//
// A plain map under a mutex, not a sync.Map: the map has to be BOUNDED, and a
// bound needs an exact size and an eviction choice, neither of which sync.Map
// offers. Contention is not a concern — every code path below returns before
// touching the cache unless the request carries an Idempotency-Key.
var (
	idempotencyMu    sync.Mutex
	idempotencyCache = map[string]*cachedResponse{}
)

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

// loadIdempotent returns the live entry for key, dropping it if it has expired.
func loadIdempotent(key string) (*cachedResponse, bool) {
	idempotencyMu.Lock()
	defer idempotencyMu.Unlock()
	cached, ok := idempotencyCache[key]
	if !ok {
		return nil, false
	}
	if !time.Now().Before(cached.ExpiresAt) {
		delete(idempotencyCache, key)
		return nil, false
	}
	return cached, true
}

// storeIdempotent inserts an entry, making room first if the cache is full.
//
// Room is made in two stages: drop everything expired, then, if still at the
// cap, drop the entry closest to expiry. With a constant TTL that is the oldest
// entry, i.e. plain FIFO — chosen over refusing the write because refusing lets
// a flood of unique keys deny replay protection to everybody else, while FIFO
// only costs the oldest key its replay.
func storeIdempotent(key string, entry *cachedResponse) {
	idempotencyMu.Lock()
	defer idempotencyMu.Unlock()

	if _, exists := idempotencyCache[key]; !exists && len(idempotencyCache) >= maxIdempotencyEntries {
		purgeExpiredLocked(time.Now())
		for len(idempotencyCache) >= maxIdempotencyEntries {
			var oldestKey string
			var oldestAt time.Time
			for k, v := range idempotencyCache {
				if oldestKey == "" || v.ExpiresAt.Before(oldestAt) {
					oldestKey, oldestAt = k, v.ExpiresAt
				}
			}
			if oldestKey == "" {
				break
			}
			delete(idempotencyCache, oldestKey)
		}
	}
	idempotencyCache[key] = entry
}

// IdempotencyMiddleware checks the Idempotency-Key header.
// If a cached response exists for (api_key_id, idempotency_key) AND the request
// matches the one that produced it, returns it. If the request differs, returns
// 409 IDEMPOTENCY_KEY_REUSED. Otherwise calls next and caches the response.
// Only applies to POST and PATCH requests.
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
					return writeError(c, domain.NewErr(domain.ErrIdempotencyKeyReused,
						"Idempotency-Key was already used for a different request; use a new key"))
				}
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
// stops a client filling it inside one TTL window. The bound is the entry cap in
// storeIdempotent; this keeps a quiet server from holding a day of garbage.
func PurgeExpiredIdempotencyCache() {
	idempotencyMu.Lock()
	defer idempotencyMu.Unlock()
	purgeExpiredLocked(time.Now())
}

// purgeExpiredLocked drops every entry whose ExpiresAt is at or before now.
// Caller must hold idempotencyMu.
func purgeExpiredLocked(now time.Time) {
	for k, v := range idempotencyCache {
		if !now.Before(v.ExpiresAt) {
			delete(idempotencyCache, k)
		}
	}
}

// IdempotencyCacheLen reports the number of live entries. Exported for tests and
// for anything that wants to observe the cap taking effect.
func IdempotencyCacheLen() int {
	idempotencyMu.Lock()
	defer idempotencyMu.Unlock()
	return len(idempotencyCache)
}

// StartIdempotencyCachePurger runs PurgeExpiredIdempotencyCache on a ticker until
// ctx is done. It returns immediately; the loop runs in its own goroutine.
//
// Called once from cmd/aihub/main.go. The interval is the caller's to choose and
// is not derived from idempotencyTTL: with a 24h TTL any interval from minutes to
// hours reclaims the same memory, and the cost of a sweep is one pass over a map
// that the entry cap holds to maxIdempotencyEntries.
func StartIdempotencyCachePurger(ctx context.Context, interval time.Duration) {
	if interval <= 0 {
		fmt.Fprintf(os.Stderr, "idempotency: purger not started, interval %v is not positive\n", interval)
		return
	}
	go func() {
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				PurgeExpiredIdempotencyCache()
			}
		}
	}()
}
