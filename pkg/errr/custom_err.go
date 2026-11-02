package errr

import (
	"errors"
	"sort"
	"strings"
)

// Domain-level sentinel errors.
// Handlers map these to HTTP status codes.
var (
	ErrNotFound      = errors.New("resource not found")
	ErrAlreadyExists = errors.New("resource already exists")
	ErrConflict      = errors.New("conflict with current state")
	ErrUnauthorized  = errors.New("unauthorized")
	ErrForbidden     = errors.New("forbidden")
	ErrInvalidInput  = errors.New("invalid input")
	ErrUnavailable   = errors.New("service unavailable")
	ErrInternal      = errors.New("internal error")
)

// PublicError wraps a sentinel with a message that is safe to show to the client.
// errors.Is(err, sentinel) keeps working, so handlers still map it by sentinel.
type PublicError struct {
	Sentinel error
	Message  string
}

func (e *PublicError) Error() string { return e.Message }
func (e *PublicError) Unwrap() error { return e.Sentinel }

// WithMessage returns sentinel annotated with a client-safe message.
func WithMessage(sentinel error, message string) error {
	return &PublicError{Sentinel: sentinel, Message: message}
}

// ValidationError carries per-field validation messages. It unwraps to ErrInvalidInput.
type ValidationError struct {
	Fields map[string]string
}

func (e *ValidationError) Error() string {
	keys := make([]string, 0, len(e.Fields))
	for k := range e.Fields {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		parts = append(parts, k+": "+e.Fields[k])
	}
	return "invalid input: " + strings.Join(parts, "; ")
}

func (e *ValidationError) Unwrap() error { return ErrInvalidInput }

// Validation collects field errors. Use Err() to get nil when nothing failed.
type Validation map[string]string

// Check records msg for field when cond is false. The first message per field wins.
func (v Validation) Check(cond bool, field, msg string) {
	if cond {
		return
	}
	if _, exists := v[field]; !exists {
		v[field] = msg
	}
}

// Err returns a *ValidationError, or nil when no field failed.
func (v Validation) Err() error {
	if len(v) == 0 {
		return nil
	}
	return &ValidationError{Fields: v}
}

// IsNotFound reports whether err is (or wraps) ErrNotFound.
func IsNotFound(err error) bool { return errors.Is(err, ErrNotFound) }
