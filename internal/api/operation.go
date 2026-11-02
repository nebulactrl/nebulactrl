package api

import (
	"reflect"
	"strconv"

	"github.com/danielgtaylor/huma/v2"
)

// documentDefaultError gives every operation a `default` response carrying the
// Error problem document. An operation's Errors list names the statuses its own
// handler is meant to answer, but the domain services behind it return
// apierr.Error values of any status (a 409 from a conflict check, a 502 from
// an unreachable upstream, a 503 from a missing integration) and the
// middleware answers 401, 403, 429 and 5xx before the handler runs. Keeping an
// exhaustive per-operation list in step with the services is not maintainable,
// so the statuses the list leaves out are documented by `default`, and a
// client still reads `code` and `detail` from them. huma adds `default` itself
// only to operations without an Errors list.
func documentDefaultError(oapi *huma.OpenAPI, op *huma.Operation) {
	if _, ok := op.Responses["default"]; ok {
		return
	}
	example := huma.NewError(0, "")
	contentType := "application/json"
	if filter, ok := example.(huma.ContentTypeFilter); ok {
		contentType = filter.ContentType(contentType)
	}
	t := reflect.TypeOf(example)
	for t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	if op.Responses == nil {
		op.Responses = map[string]*huma.Response{}
	}
	op.Responses["default"] = &huma.Response{
		Description: "Error",
		Content: map[string]*huma.MediaType{
			contentType: {Schema: oapi.Components.Schemas.Schema(t, true, "Error")},
		},
	}
}

// WithResponse documents one more JSON success status of an operation whose
// output chooses its status itself (an output with a `Status int` field), next
// to the operation's DefaultStatus: a dry run that answers 200 where the real
// call answers 202, a removal that finishes at once (200) or goes on in the
// background (202). A status the spec leaves out is wrongly read as an error
// by the generated clients. T is the body type of that answer.
func WithResponse[T any](h huma.API, op huma.Operation, status int, description string) huma.Operation {
	responses := make(map[string]*huma.Response, len(op.Responses)+1)
	for key, response := range op.Responses {
		responses[key] = response
	}
	responses[strconv.Itoa(status)] = &huma.Response{
		Description: description,
		Content: map[string]*huma.MediaType{
			"application/json": {Schema: h.OpenAPI().Components.Schemas.Schema(reflect.TypeFor[T](), true, "")},
		},
	}
	op.Responses = responses
	return op
}

// Security scheme names.
const (
	SchemeBearer = "bearer"
	SchemeCookie = "cookie"
)

// IdempotentKey is the Operation.Metadata key honored by the idempotency
// middleware.
const IdempotentKey = "idempotent"

// IdempotencyHeader is the request header carrying the client's key.
const IdempotencyHeader = "Idempotency-Key"

// WithIdempotency marks op as honoring the Idempotency-Key header and documents
// the header. Replays within 24 hours return the stored status and body.
func WithIdempotency(op huma.Operation) huma.Operation {
	if op.Metadata == nil {
		op.Metadata = map[string]any{}
	}
	op.Metadata[IdempotentKey] = true
	op.Parameters = append(op.Parameters, &huma.Param{
		Name:        IdempotencyHeader,
		In:          "header",
		Description: "Client-chosen key (<=255 chars). A retry with the same key replays the original response for 24 hours.",
		Schema:      &huma.Schema{Type: "string", MaxLength: ptr(255)},
	})
	return op
}

// ScopesKey is the Operation.Metadata key holding the scopes (a []string) a
// principal must hold to call the operation.
const ScopesKey = "scopes"

// PublicKey is the Operation.Metadata key set by Public.
const PublicKey = "public"

// WithScopes declares the API-token scopes op requires and publishes them as
// the operation's OpenAPI security requirement. An empty list means any
// authenticated principal. Session users hold every scope. The domain service
// still enforces the scopes (policy.Require); this is the declaration the MCP
// catalog filters on.
func WithScopes(op huma.Operation, scopes ...string) huma.Operation {
	if scopes == nil {
		scopes = []string{}
	}
	if op.Metadata == nil {
		op.Metadata = map[string]any{}
	}
	op.Metadata[ScopesKey] = cloneScopes(scopes)
	op.Security = []map[string][]string{
		{SchemeBearer: cloneScopes(scopes)},
		{SchemeCookie: cloneScopes(scopes)},
	}
	return op
}

// ScopesOf returns the scopes declared by WithScopes: nil when op was not
// wrapped (it may still be Public), a non-nil empty slice for "any
// authenticated principal".
func ScopesOf(op *huma.Operation) []string {
	if op == nil || op.Metadata == nil {
		return nil
	}
	s, ok := op.Metadata[ScopesKey].([]string)
	if !ok {
		return nil
	}
	return cloneScopes(s)
}

func cloneScopes(s []string) []string {
	return append(make([]string, 0, len(s)), s...)
}

// Public marks op as not requiring authentication (overrides the API-wide
// security requirement).
func Public(op huma.Operation) huma.Operation {
	if op.Metadata == nil {
		op.Metadata = map[string]any{}
	}
	op.Metadata[PublicKey] = true
	op.Security = []map[string][]string{{}}
	return op
}

// IsIdempotent reports whether op was marked with WithIdempotency.
func IsIdempotent(op *huma.Operation) bool {
	if op == nil || op.Metadata == nil {
		return false
	}
	v, _ := op.Metadata[IdempotentKey].(bool)
	return v
}

func ptr[T any](v T) *T { return &v }

// Tags documented in the OpenAPI spec. Domains use these names.
var Tags = []*huma.Tag{
	{Name: "setup", Description: "First-run bootstrap"},
	{Name: "auth", Description: "Sessions"},
	{Name: "me", Description: "Current principal"},
	{Name: "tokens", Description: "API tokens"},
	{Name: "projects", Description: "Projects"},
	{Name: "environments", Description: "Environments"},
	{Name: "services", Description: "Services"},
	{Name: "variables", Description: "Service variables"},
	{Name: "domains", Description: "Service domains"},
	{Name: "volumes", Description: "Service volumes"},
	{Name: "links", Description: "Service links"},
	{Name: "deployments", Description: "Deployments and releases"},
	{Name: "canvas", Description: "Canvas layouts"},
	{Name: "servers", Description: "Servers and agents"},
	{Name: "logs", Description: "Logs"},
	{Name: "metrics", Description: "Metrics"},
	{Name: "audit", Description: "Audit log"},
	{Name: "events", Description: "Live invalidation feed"},
	{Name: "system", Description: "Control-plane info"},
	{Name: "teams", Description: "Teams, members, invites and instance settings"},
	{Name: "approvals", Description: "Production approvals for AI clients"},
}
