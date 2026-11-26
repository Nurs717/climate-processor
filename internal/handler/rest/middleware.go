package rest

import (
	"crypto/rand"
	"encoding/hex"
	"log/slog"
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

// noStore keeps browsers and service workers from serving a stale reading from their cache.
func noStore() gin.HandlerFunc {
	return func(c *gin.Context) {
		c.Header("Cache-Control", "no-store")
		c.Next()
	}
}
