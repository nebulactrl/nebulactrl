// Package apierr defines the API error type. Every error response is an
// RFC 9457 problem+json document carrying an extra machine-readable `code`.
package apierr

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strings"

	"github.com/danielgtaylor/huma/v2"
)

// Error is an API error. It implements error and huma.StatusError.
type Error struct {
	Status  int            `json:"status" doc:"HTTP status code" example:"404"`
	Code    string         `json:"code" doc:"Stable machine-readable error code" example:"not_found"`
	Message string         `json:"detail,omitempty" doc:"Human-readable explanation"`
	Details map[string]any `json:"details,omitempty" doc:"Additional structured context"`
	// Type is the RFC 9457 problem type URI; "about:blank" unless overridden.
	Type string `json:"type,omitempty" doc:"A URI reference identifying the problem type" example:"about:blank"`
	// Title is the HTTP status text.
	Title string `json:"title,omitempty" doc:"Short summary of the problem type" example:"Not Found"`
	// Errors lists field-level validation failures.
	Errors []*huma.ErrorDetail `json:"errors,omitempty" doc:"Field-level validation errors"`
}

// New returns an Error.
func New(status int, code, msg string) *Error {
	return &Error{Status: status, Code: code, Message: msg, Type: "about:blank", Title: http.StatusText(status)}
}

// WithDetails attaches structured details and returns e.
func (e *Error) WithDetails(d map[string]any) *Error {
	e.Details = d
	return e
}

// Error implements error.
func (e *Error) Error() string {
	if e.Message == "" {
		return e.Code
	}
	return e.Message
}

// GetStatus implements huma.StatusError.
func (e *Error) GetStatus() int { return e.Status }

// ContentType makes huma respond with application/problem+json.
func (e *Error) ContentType(ct string) string {
	if ct == "application/json" {
		return "application/problem+json"
	}
	if ct == "application/cbor" {
		return "application/problem+cbor"
	}
	return ct
}

// NotFound is a 404 for the named resource.
func NotFound(what string) *Error {
	if what == "" {
		what = "resource"
	}
	return New(http.StatusNotFound, "not_found", what+" not found")
}

// Forbidden is a 403.
func Forbidden() *Error {
	return New(http.StatusForbidden, "forbidden", "you do not have permission to perform this action")
}

// Unauthorized is a 401.
func Unauthorized() *Error {
	return New(http.StatusUnauthorized, "unauthorized", "authentication required")
}

// Conflict is a 409.
func Conflict(code, msg string) *Error { return New(http.StatusConflict, code, msg) }

// Unprocessable is a 422.
func Unprocessable(code, msg string) *Error { return New(http.StatusUnprocessableEntity, code, msg) }

// PreconditionFailed is a 412.
func PreconditionFailed(code, msg string) *Error {
	return New(http.StatusPreconditionFailed, code, msg)
}

// BadRequest is a 400.
func BadRequest(code, msg string) *Error { return New(http.StatusBadRequest, code, msg) }

// TooManyRequests is a 429.
func TooManyRequests(msg string) *Error {
	return New(http.StatusTooManyRequests, "rate_limited", msg)
}

// Internal is a 500 that does not leak its cause.
func Internal() *Error {
	return New(http.StatusInternalServerError, "internal_error", "unexpected error occurred")
}

// As extracts an *Error from err's chain.
func As(err error) (*Error, bool) {
	var e *Error
	if errors.As(err, &e) {
		return e, true
	}
	return nil, false
}

// codeForStatus maps framework-generated statuses to stable codes.
func codeForStatus(status int) string {
	switch status {
	case http.StatusBadRequest:
		return "bad_request"
	case http.StatusUnauthorized:
		return "unauthorized"
	case http.StatusForbidden:
		return "forbidden"
	case http.StatusNotFound:
		return "not_found"
	case http.StatusMethodNotAllowed:
		return "method_not_allowed"
	case http.StatusNotAcceptable:
		return "not_acceptable"
	case http.StatusConflict:
		return "conflict"
	case http.StatusPreconditionFailed:
		return "precondition_failed"
	case http.StatusPreconditionRequired:
		return "precondition_required"
	case http.StatusRequestEntityTooLarge:
		return "payload_too_large"
	case http.StatusUnsupportedMediaType:
		return "unsupported_media_type"
	case http.StatusUnprocessableEntity:
		return "validation_failed"
	case http.StatusTooManyRequests:
		return "rate_limited"
	}
	if status >= 500 {
		return "internal_error"
	}
	return strings.ReplaceAll(strings.ToLower(http.StatusText(status)), " ", "_")
}

// FromHuma converts huma's framework errors (validation, parsing, unexpected
// handler errors) into an *Error. It is installed as huma.NewError.
func FromHuma(status int, msg string, errs ...error) huma.StatusError {
	e := New(status, codeForStatus(status), msg)
	var causes []error
	for _, err := range errs {
		if err == nil {
			continue
		}
		// A handler returned an *Error (or wrapped one): preserve it verbatim.
		if ae, ok := As(err); ok {
			return ae
		}
		if d, ok := err.(huma.ErrorDetailer); ok {
			e.Errors = append(e.Errors, d.ErrorDetail())
			continue
		}
		causes = append(causes, err)
	}
	if status >= 500 {
		e.Message = "unexpected error occurred"
		if len(causes) > 0 {
			slog.Error("unhandled error in API handler", "status", status, "error", errors.Join(causes...))
		}
		return e
	}
	for _, c := range causes {
		e.Errors = append(e.Errors, &huma.ErrorDetail{Message: c.Error()})
	}
	return e
}

func init() {
	huma.NewError = FromHuma
}

// Write renders e as application/problem+json on a plain http.ResponseWriter,
// for code that runs outside huma (middleware, raw routes).
func Write(w http.ResponseWriter, e *Error) {
	w.Header().Set("Content-Type", "application/problem+json")
	w.WriteHeader(e.Status)
	_ = json.NewEncoder(w).Encode(e)
}
