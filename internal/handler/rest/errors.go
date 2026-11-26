package rest

import (
	"context"
	"errors"
	"net/http"

	"climate-processor/internal/apperr"

	"github.com/gin-gonic/gin"
)

// statusClientClosedRequest is the non-standard status (used by nginx) for a request the client abandoned.
const statusClientClosedRequest = 499

// errorBody is the JSON shape of every error response.
type errorBody struct {
	Error errorDetail `json:"error"`
}

type errorDetail struct {
	Code      string `json:"code"`                 // machine-readable, see apperr.Code
	Message   string `json:"message"`              // safe to show to a user
	RequestID string `json:"request_id,omitempty"` // quote it when reporting a problem: it finds the log lines
}

// statusOf maps an application error code to an HTTP status.
func statusOf(code apperr.Code) int {
	switch code {
	case apperr.CodeNotFound:
		return http.StatusNotFound
	case apperr.CodeBadRequest:
		return http.StatusBadRequest
	case apperr.CodeUnauthorized:
		return http.StatusUnauthorized
	case apperr.CodeForbidden:
		return http.StatusForbidden
	case apperr.CodeConflict:
		return http.StatusConflict
	case apperr.CodeTooManyRequests:
		return http.StatusTooManyRequests
	case apperr.CodeUnavailable:
		return http.StatusServiceUnavailable
	default:
		return http.StatusInternalServerError
	}
}

// handlerFunc is a handler that reports a failure by returning an error; wrap turns it into a gin handler.
type handlerFunc func(c *gin.Context) error

func wrap(h handlerFunc) gin.HandlerFunc {
	return func(c *gin.Context) {
		if err := h(c); err != nil {
			renderError(c, err)
		}
	}
}

// renderError writes err as the JSON error response. The error is also attached to the gin context: the
// access log is the one place that logs it (with the cause), while the client only sees the public reason.
func renderError(c *gin.Context, err error) {
	_ = c.Error(err)
	if errors.Is(err, context.Canceled) {
		c.AbortWithStatus(statusClientClosedRequest) // the client left, nobody reads the body
		return
	}
	code := apperr.CodeOf(err)
	c.AbortWithStatusJSON(statusOf(code), errorBody{Error: errorDetail{
		Code:      code.String(),
		Message:   apperr.ReasonOf(err),
		RequestID: requestIDOf(c),
	}})
}

func badRequest(op, reason string) error {
	return apperr.New(apperr.CodeBadRequest, op, reason)
}
