// Package apperr defines the application error: a machine-readable Code that transports map to
// an HTTP or gRPC status, a Reason that is safe to show to clients, and the operation and
// underlying error that are only written to logs.
package apperr

import (
	"errors"
	"fmt"
)

// Code classifies an error. Transports (REST, bot, ...) translate it into their own status.
type Code uint8

const (
	CodeUnknown         Code = iota // unexpected failure, shown to clients as "internal error"
	CodeNotFound                    // the requested thing does not exist
	CodeBadRequest                  // malformed or invalid input
	CodeUnauthorized                // no or invalid credentials
	CodeForbidden                   // authenticated, but not allowed
	CodeConflict                    // the request clashes with the current state
	CodeTooManyRequests             // rate limit or budget exceeded
	CodeUnavailable                 // a dependency (database, LLM provider) is down
)

func (c Code) String() string {
	switch c {
	case CodeNotFound:
		return "not_found"
	case CodeBadRequest:
		return "bad_request"
	case CodeUnauthorized:
		return "unauthorized"
	case CodeForbidden:
		return "forbidden"
	case CodeConflict:
		return "conflict"
	case CodeTooManyRequests:
		return "too_many_requests"
	case CodeUnavailable:
		return "unavailable"
	default:
		return "unknown"
	}
}

// internalReason is what clients see for errors that are not an *Error.
const internalReason = "internal error"

// Error is an application error.
type Error struct {
	Code   Code
	Reason string // public text: never put internal details here
	Op     string // where it happened, e.g. "service.Stats"; logs only
	Err    error  // underlying cause; logs only
}

// New creates an error without an underlying cause.
func New(code Code, op, reason string) *Error {
	return &Error{Code: code, Op: op, Reason: reason}
}

// Wrap creates an error that keeps err as its cause (errors.Is and errors.As see through it).
func Wrap(err error, code Code, op, reason string) *Error {
	return &Error{Code: code, Op: op, Reason: reason, Err: err}
}

// Error returns the full text for logs: "op: reason: cause".
func (e *Error) Error() string {
	s := e.Reason
	if e.Op != "" {
		s = e.Op + ": " + s
	}
	if e.Err != nil {
		s = fmt.Sprintf("%s: %v", s, e.Err)
	}
	return s
}

func (e *Error) Unwrap() error { return e.Err }

// CodeOf returns the code of the first *Error in err's chain, or CodeUnknown.
func CodeOf(err error) Code {
	var e *Error
	if errors.As(err, &e) {
		return e.Code
	}
	return CodeUnknown
}

// ReasonOf returns the client-safe text of the first *Error in err's chain.
// Any other error yields "internal error", so internal details never reach a client.
func ReasonOf(err error) string {
	var e *Error
	if errors.As(err, &e) && e.Reason != "" {
		return e.Reason
	}
	return internalReason
}
