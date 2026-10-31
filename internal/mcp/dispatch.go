package mcp

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"strings"
	"sync/atomic"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/nebulactrl/nebulactrl/internal/api"
	"github.com/nebulactrl/nebulactrl/internal/apierr"
	"github.com/nebulactrl/nebulactrl/internal/mcp/mcpkit"
	"github.com/nebulactrl/nebulactrl/internal/prodguard"
)

// dispatcher runs REST operations inside this process: it calls the root
// http.Handler (the same router, middleware, auth, idempotency and services
// the network hits) with a request carrying the caller's own bearer token.
//
// The request context carries api.InProcess, which the auth middleware turns
// into actor type `mcp` (audit rows record actor_type=mcp and the client
// name). That marker is a context value, never a header, so no network
// request can claim it; see api.WithInProcess.
type dispatcher struct {
	root atomic.Pointer[http.Handler]
}

// attach sets the handler dispatch calls.
func (d *dispatcher) attach(root http.Handler) { d.root.Store(&root) }

// dispatchReq is one REST call.
type dispatchReq struct {
	method  string
	path    string // under /api/v1, already expanded
	query   url.Values
	body    []byte
	ifMatch string // already quoted
}

// caller identifies who the call is made for.
type caller struct {
	token      string // the caller's raw bearer token
	client     string // MCP clientInfo name
	remoteAddr string
}

// dispatchResp is the buffered REST response.
type dispatchResp struct {
	status int
	header http.Header
	body   []byte
}

// recorder is a minimal buffering http.ResponseWriter.
type recorder struct {
	status int
	header http.Header
	body   bytes.Buffer
}

func (r *recorder) Header() http.Header { return r.header }
func (r *recorder) WriteHeader(c int) {
	if r.status == 0 {
		r.status = c
	}
}
func (r *recorder) Write(b []byte) (int, error) {
	if r.status == 0 {
		r.status = http.StatusOK
	}
	return r.body.Write(b)
}
func (r *recorder) Flush() {}

// maxDispatchBody bounds what a dispatched response may buffer.
const maxDispatchBody = 8 << 20

// do performs the request in-process.
func (d *dispatcher) do(ctx context.Context, who caller, r dispatchReq) (dispatchResp, error) {
	root := d.root.Load()
	if root == nil {
		return dispatchResp{}, mcpkit.Errf(mcpkit.CodeUnavailable, "the API is not attached to the MCP server")
	}
	target := api.BasePath + r.path
	if len(r.query) > 0 {
		target += "?" + r.query.Encode()
	}
	rd := bytes.NewReader(r.body)
	// The MCP request itself was routed by chi: its route context must not leak
	// into the inner request, or chi would treat the call as a sub-route of /mcp.
	ctx = context.WithValue(ctx, chi.RouteCtxKey, (*chi.Context)(nil))
	ctx = api.WithInProcess(ctx, api.InProcess{Client: who.client})
	ctx = prodguard.WithClient(ctx, who.client)
	req, err := http.NewRequestWithContext(ctx, r.method, "http://in-process"+target, rd)
	if err != nil {
		return dispatchResp{}, mcpkit.Errf(mcpkit.CodeInvalidArgument, "invalid request path")
	}
	req.RemoteAddr = who.remoteAddr
	req.Header.Set("Authorization", "Bearer "+who.token)
	req.Header.Set("Accept", "application/json")
	if len(r.body) > 0 {
		req.Header.Set("Content-Type", "application/json")
	}
	if r.ifMatch != "" {
		req.Header.Set("If-Match", r.ifMatch)
	}
	if r.method != http.MethodGet && r.method != http.MethodHead {
		// Every attempt carries a fresh key: an MCP client retry is a new call,
		// and a stored response must never replace an approval retry's.
		req.Header.Set(api.IdempotencyHeader, uuid.NewString())
	}

	rec := &recorder{header: http.Header{}}
	(*root).ServeHTTP(rec, req)
	if rec.status == 0 {
		rec.status = http.StatusOK
	}
	body := rec.body.Bytes()
	if len(body) > maxDispatchBody {
		return dispatchResp{}, mcpkit.Errf(mcpkit.CodeInternal, "the response is too large to return over MCP")
	}
	return dispatchResp{status: rec.status, header: rec.header, body: body}, nil
}

// problemError maps a problem+json response to the error a tool returns:
// approval_required becomes *prodguard.ErrApprovalRequired (the registry runs
// the approval flow), everything else a *mcpkit.Error with the API's code.
func problemError(resp dispatchResp) error {
	if ae, ok := prodguard.FromProblem(resp.status, resp.body); ok {
		return ae
	}
	var p struct {
		Code    string         `json:"code"`
		Title   string         `json:"title"`
		Detail  string         `json:"detail"`
		Details map[string]any `json:"details"`
		Errors  []struct {
			Message  string `json:"message"`
			Location string `json:"location"`
		} `json:"errors"`
	}
	_ = json.Unmarshal(resp.body, &p)
	if resp.status >= 500 && p.Code != "server_offline" {
		return apierr.Internal()
	}
	code := p.Code
	if code == "" {
		code = defaultCode(resp.status)
	}
	msg := p.Detail
	if msg == "" {
		msg = p.Title
	}
	if msg == "" {
		msg = http.StatusText(resp.status)
	}
	te := &mcpkit.Error{Code: code, Message: msg}
	data := map[string]any{}
	for k, v := range p.Details {
		data[k] = v
	}
	if len(p.Errors) > 0 {
		fields := make([]string, 0, len(p.Errors))
		for _, e := range p.Errors {
			fields = append(fields, strings.TrimSpace(e.Location+": "+e.Message))
		}
		data["fields"] = fields
	}
	if len(data) > 0 {
		te.Data = data
	}
	return te
}

func defaultCode(status int) string {
	switch status {
	case http.StatusUnauthorized:
		return "unauthorized"
	case http.StatusForbidden:
		return "forbidden"
	case http.StatusNotFound:
		return "not_found"
	case http.StatusConflict:
		return "conflict"
	case http.StatusPreconditionFailed:
		return "generation_conflict"
	case http.StatusUnprocessableEntity, http.StatusBadRequest:
		return mcpkit.CodeInvalidArgument
	case http.StatusTooManyRequests:
		return "rate_limited"
	}
	return "error"
}
