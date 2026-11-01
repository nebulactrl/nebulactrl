// Package mcpkit is the leaf package shared by the MCP server (internal/mcp),
// the composite tools (internal/mcp/composites) and any other tool provider:
// the Tool descriptor, the structured tool Error, selector resolution
// (project/env/service, hostname), untrusted-text wrapping, detail levels and
// pagination helpers.
//
// It imports the domain services but never internal/mcp, so providers can use
// it without an import cycle. Everything here runs under the caller's
// principal: resolvers go through the services' own policy checks.
package mcpkit

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"

	"github.com/nebulactrl/nebulactrl/internal/auth"
)

// MaxResultBytes caps the serialized size of every tool result.
const MaxResultBytes = 64 * 1024

// Default and maximum page size of list tools.
const (
	DefaultPage = 50
	MaxPage     = 200
)

// Stable tool error codes that do not originate from apierr.
const (
	CodeInvalidArgument      = "invalid_argument"
	CodeNotFound             = "not_found"
	CodeAmbiguous            = "ambiguous_selector"
	CodeOutcomeUncertain     = "outcome_uncertain"
	CodeConfirmationMismatch = "confirmation_mismatch"
	CodeConfirmationRequired = "confirmation_required"
	CodeUnavailable          = "unavailable"
	CodeForbidden            = "forbidden"
	CodeTimeout              = "timeout"
	CodeInternal             = "internal"
)

// Toolsets group tools; a token or the URL (`/mcp?toolsets=core,config`)
// selects which are listed.
const (
	ToolsetCore    = "core"
	ToolsetConfig  = "config"
	ToolsetData    = "data"
	ToolsetServers = "servers"
	ToolsetObserve = "observe"
)

// Toolsets lists every toolset name.
var Toolsets = []string{ToolsetCore, ToolsetConfig, ToolsetData, ToolsetServers, ToolsetObserve}

// Error is the structured error a tool returns as `{code, message}` (plus
// optional data) in both the text and the structured content of an `isError`
// result. Providers return it (or any apierr/other error; the registry maps
// those).
type Error struct {
	Code    string `json:"code"`
	Message string `json:"message"`
	// Data carries extra context, e.g. `choices` for an ambiguous selector or
	// the last known deployment state for outcome_uncertain. Never secrets.
	Data any `json:"data,omitempty"`
}

func (e *Error) Error() string { return e.Code + ": " + e.Message }

// Errf builds an Error with a formatted message.
func Errf(code, format string, args ...any) *Error {
	return &Error{Code: code, Message: fmt.Sprintf(format, args...)}
}

// Hints are the four MCP tool annotations. Every tool sets all four
// accurately; internal/mcp enforces it with a table test.
type Hints struct {
	// ReadOnly: the tool changes nothing.
	ReadOnly bool
	// Destructive: the tool may delete or overwrite state it cannot restore
	// (only meaningful when !ReadOnly).
	Destructive bool
	// Idempotent: repeating the call with the same arguments has no further
	// effect (only meaningful when !ReadOnly).
	Idempotent bool
	// OpenWorld: the tool reaches outside the control plane (containers,
	// application text, the internet) or returns text from there.
	OpenWorld bool
}

// Tool describes one MCP tool contributed by a provider.
type Tool struct {
	Name        string
	Title       string
	Description string
	// Toolset is one of the Toolset* constants.
	Toolset string
	// Scopes are the scopes a principal must all hold for the tool to be
	// listed. The domain services still re-check them. Empty: any
	// authenticated principal.
	Scopes []string
	Hints  Hints
	// Input and Output are typed zero values (struct or pointer to struct)
	// the registry derives the input and output JSON schemas from. Output may
	// be nil for an unstructured result.
	Input  any
	Output any
	// Call runs the tool. ctx carries the principal (also passed as p) with
	// actor type mcp and a deadline. raw is the JSON arguments object (never
	// nil; `{}` when empty). Return the structured result, or an error.
	Call func(ctx context.Context, p *auth.Principal, raw json.RawMessage) (any, error)
}

// Decode parses the arguments strictly: unknown fields and type mismatches
// are an invalid_argument Error naming the problem.
func Decode[T any](raw json.RawMessage) (T, error) {
	var in T
	if len(bytes.TrimSpace(raw)) == 0 {
		return in, nil
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&in); err != nil {
		return in, Errf(CodeInvalidArgument, "invalid arguments: %s", cleanJSONError(err))
	}
	if _, err := dec.Token(); err != io.EOF {
		return in, Errf(CodeInvalidArgument, "invalid arguments: trailing data")
	}
	return in, nil
}

func cleanJSONError(err error) string {
	msg := err.Error()
	msg = strings.TrimPrefix(msg, "json: ")
	return msg
}

// Detail is the verbosity of a result.
type Detail string

// Detail levels. Concise is the default.
const (
	DetailConcise Detail = "concise"
	DetailFull    Detail = "full"
)

// ParseDetail validates a `detail` argument; "" means concise.
func ParseDetail(s string) (Detail, error) {
	switch Detail(strings.ToLower(strings.TrimSpace(s))) {
	case "", DetailConcise:
		return DetailConcise, nil
	case DetailFull:
		return DetailFull, nil
	}
	return DetailConcise, Errf(CodeInvalidArgument, "detail must be concise or full")
}

// Full reports whether the caller asked for the full result.
func (d Detail) Full() bool { return d == DetailFull }

// List is the shape of every paginated list result.
type List[T any] struct {
	Items      []T    `json:"items"`
	NextCursor string `json:"nextCursor,omitempty"`
	Truncated  bool   `json:"truncated,omitempty"`
	Note       string `json:"note,omitempty"`
}

// NewList wraps items (never nil) and the next cursor.
func NewList[T any](items []T, next string) List[T] {
	if items == nil {
		items = []T{}
	}
	return List[T]{Items: items, NextCursor: next}
}

// Fit drops items until the serialized list fits MaxResultBytes, marking it
// truncated.
func (l *List[T]) Fit() {
	for len(l.Items) > 0 && JSONSize(l) > MaxResultBytes-256 {
		l.Items = l.Items[:len(l.Items)*3/4]
		l.Truncated = true
		l.NextCursor = ""
		l.Note = "result truncated at 64 KB; request a smaller limit"
	}
}

// JSONSize is the serialized size of v (MaxResultBytes+1 when it cannot be
// serialized).
func JSONSize(v any) int {
	raw, err := json.Marshal(v)
	if err != nil {
		return MaxResultBytes + 1
	}
	return len(raw)
}

// PageLimit clamps a requested page size to [1, MaxPage], default DefaultPage.
func PageLimit(n int) int {
	switch {
	case n <= 0:
		return DefaultPage
	case n > MaxPage:
		return MaxPage
	}
	return n
}
