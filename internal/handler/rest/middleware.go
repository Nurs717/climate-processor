package rest

import (
	"crypto/rand"
	"encoding/hex"
	"log/slog"
	"math"
	"net/http"
	"regexp"
	"runtime/debug"
	"strconv"
	"time"

	"climate-processor/internal/apperr"
	"climate-processor/internal/logger"

	"github.com/gin-gonic/gin"
)

const (
	requestIDHeader = "X-Request-ID"
	requestIDKey    = "request_id" // key in the gin context
)

// requestIDPattern limits what is accepted from the caller: anything else could forge log lines.
var requestIDPattern = regexp.MustCompile(`^[A-Za-z0-9._-]{1,64}$`)

// requestID gives every request an id: the caller's X-Request-ID when it looks sane (a reverse proxy
// can set it), otherwise a random one. The id is returned in the response header, shown in error bodies
// and added to every log record written with the request context.
func requestID() gin.HandlerFunc {
	return func(c *gin.Context) {
		id := c.GetHeader(requestIDHeader)
		if !requestIDPattern.MatchString(id) {
			id = newRequestID()
		}
		c.Set(requestIDKey, id)
		c.Header(requestIDHeader, id)
		c.Request = c.Request.WithContext(logger.With(c.Request.Context(), slog.String("request_id", id)))
		c.Next()
	}
}

func requestIDOf(c *gin.Context) string { return c.GetString(requestIDKey) }

func newRequestID() string {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		// crypto/rand does not fail on supported platforms; a time-based id is still better than none.
		return strconv.FormatInt(time.Now().UnixNano(), 16)
	}
	return hex.EncodeToString(b[:])
}

// accessLog writes one record per request, after it is handled: "request done" with the status, the
// duration and, for a failed request, the cause. Server errors are logged at ERROR, the rest at INFO.
func accessLog(log *slog.Logger) gin.HandlerFunc {
	return func(c *gin.Context) {
		start := time.Now()
		c.Next()

		status := c.Writer.Status()
		attrs := []any{
			"method", c.Request.Method,
			"path", c.Request.URL.Path,
			"status", status,
			"duration_ms", time.Since(start).Milliseconds(),
			"bytes", max(c.Writer.Size(), 0), // gin reports -1 when nothing was written
			"client_ip", c.ClientIP(),
		}
		if err := c.Errors.Last(); err != nil {
			attrs = append(attrs, "error", err.Err)
		}

		level := slog.LevelInfo
		if status >= http.StatusInternalServerError {
			level = slog.LevelError
		}
		log.Log(c.Request.Context(), level, "request done", attrs...)
	}
}

// recovery turns a panic in a handler into a logged error with the stack and a clean 500 response.
func recovery(log *slog.Logger) gin.HandlerFunc {
	return gin.CustomRecoveryWithWriter(nil, func(c *gin.Context, recovered any) {
		log.ErrorContext(c.Request.Context(), "panic in a request handler",
			"panic", recovered, "stack", string(debug.Stack()))
		renderError(c, apperr.New(apperr.CodeUnknown, "rest.recovery", "internal error"))
	})
}

// Costs of a request for the rate limiter. The latest value is cheap; a query that aggregates the stored
// history keeps the database busy for a while, so it counts as several requests.
const (
	costCheap   = 1
	costHistory = 5
)

// rateLimit answers 429 with a Retry-After header when the client of the request has used up its tokens.
// The client is c.ClientIP(), which honours X-Forwarded-For only from trusted proxies. A nil limiter lets
// every request through.
func rateLimit(l Limiter, now func() time.Time, cost int) gin.HandlerFunc {
	if l == nil {
		return func(*gin.Context) {}
	}
	return func(c *gin.Context) {
		ok, wait := l.Allow(c.ClientIP(), cost, now())
		if ok {
			return
		}
		c.Header("Retry-After", strconv.Itoa(retryAfterSeconds(wait)))
		renderError(c, apperr.New(apperr.CodeTooManyRequests, "rest.rateLimit", "too many requests, retry later"))
	}
}

// retryAfterSeconds rounds the wait up to whole seconds, the unit of the Retry-After header, and never gives 0.
func retryAfterSeconds(d time.Duration) int {
	return max(1, int(math.Ceil(d.Seconds())))
}

// noStore keeps browsers and service workers from serving a stale reading from their cache.
func noStore() gin.HandlerFunc {
	return func(c *gin.Context) {
		c.Header("Cache-Control", "no-store")
		c.Next()
	}
}
