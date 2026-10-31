package api

import (
	"context"

	"github.com/nebulactrl/nebulactrl/internal/auth"
)

// inProcessKey is the unexported context key marking a request that was built
// inside this process (the MCP server dispatching a tool call to the REST
// handler). A network request can never carry it: the HTTP server builds each
// request context itself, and no middleware copies a header, cookie or query
// value into the context under this key. There is deliberately no header form
// of the marker.
type inProcessKey struct{}

// InProcess describes an in-process caller.
type InProcess struct {
	// Client is the MCP client name from `initialize` clientInfo (may be empty).
	Client string
}

// WithInProcess marks ctx as an in-process request on behalf of an MCP client.
// Only code inside this binary can call it; the auth middleware then records
// the principal as actor type `mcp` (audit rows carry actor_type=mcp and the
// client name) and nothing else about authentication changes: the bearer token
// is still verified and its scopes and project restriction still apply.
func WithInProcess(ctx context.Context, ip InProcess) context.Context {
	return context.WithValue(ctx, inProcessKey{}, ip)
}

func inProcessFrom(ctx context.Context) (InProcess, bool) {
	ip, ok := ctx.Value(inProcessKey{}).(InProcess)
	return ip, ok
}

// markInProcess returns p as seen by an in-process MCP request: a copy with
// type mcp and the client appended to the label. Other requests get p back.
func markInProcess(ctx context.Context, p *auth.Principal) *auth.Principal {
	ip, ok := inProcessFrom(ctx)
	if !ok {
		return p
	}
	cp := *p
	cp.Type = auth.TypeMCP
	if ip.Client != "" {
		cp.Label = p.Label + " via " + ip.Client
	}
	return &cp
}
