package server

import (
	"bytes"
	"net/http"
	"sync"
	"time"

	"github.com/labstack/echo/v4"
)

// cachedResponse holds a cached HTTP response body and metadata.
type cachedResponse struct {
	StatusCode int
	Body       []byte
	ExpiresAt  time.Time
}

// idempotencyCache is an in-process cache for idempotent request responses.
// Key format: "<api_key_id>:<idempotency_key>"
// Note: This is not durable. A PostgreSQL-backed cache would survive restarts.
// TODO(M3): implement durable idempotency cache in the idempotency_cache table.
var idempotencyCache sync.Map

// IdempotencyMiddleware checks the Idempotency-Key header.
// If a cached response exists for (api_key_id, idempotency_key), returns it.
// Otherwise, calls next and caches the response for 24 hours.
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

			cacheKey := u.APIKeyID + ":" + idemKey

			// Check cache
			if v, ok := idempotencyCache.Load(cacheKey); ok {
				cached := v.(*cachedResponse)
				if time.Now().Before(cached.ExpiresAt) {
					c.Response().Header().Set("X-Idempotency-Replayed", "true")
					return c.JSONBlob(cached.StatusCode, cached.Body)
				}
				// Expired; remove
				idempotencyCache.Delete(cacheKey)
			}

			// Intercept the response body
			resWriter := &responseWriter{ResponseWriter: c.Response().Writer, body: &bytes.Buffer{}}
			c.Response().Writer = resWriter

			err := next(c)

			// Cache successful-or-idempotent responses
			if resWriter.status >= 200 && resWriter.status < 300 {
				idempotencyCache.Store(cacheKey, &cachedResponse{
					StatusCode: resWriter.status,
					Body:       resWriter.body.Bytes(),
					ExpiresAt:  time.Now().Add(24 * time.Hour),
				})
			}

			return err
		}
	}
}

// responseWriter wraps http.ResponseWriter to capture the status and body.
type responseWriter struct {
	http.ResponseWriter
	status int
	body   *bytes.Buffer
}

func (rw *responseWriter) WriteHeader(code int) {
	rw.status = code
	rw.ResponseWriter.WriteHeader(code)
}

func (rw *responseWriter) Write(b []byte) (int, error) {
	rw.body.Write(b)
	return rw.ResponseWriter.Write(b)
}

// PurgeExpiredIdempotencyCache removes entries older than 24 hours from the in-memory cache.
// Should be called periodically (e.g. from a background goroutine or GC job).
func PurgeExpiredIdempotencyCache() {
	now := time.Now()
	idempotencyCache.Range(func(k, v any) bool {
		cached := v.(*cachedResponse)
		if now.After(cached.ExpiresAt) {
			idempotencyCache.Delete(k)
		}
		return true
	})
}
