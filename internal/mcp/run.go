package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/nebulactrl/nebulactrl/internal/audit"
	"github.com/nebulactrl/nebulactrl/internal/auth"
	"github.com/nebulactrl/nebulactrl/internal/db"
	"github.com/nebulactrl/nebulactrl/internal/mcp/catalog"
	"github.com/nebulactrl/nebulactrl/internal/mcp/mcpkit"
	"github.com/nebulactrl/nebulactrl/internal/prodguard"
)

// AuditAction is recorded for every tool call.
const AuditAction = "mcp.tool_call"

// env binds one authenticated request to the tool implementations: the
// principal, its raw bearer token (for in-process dispatch), the connection
// facts and the toolsets the URL selected.
type env struct {
	h      *Handler
	p      *auth.Principal
	token  string
	remote string
	ua     string
	// toolsets is the explicit ?toolsets= selection (nil = none given): it
	// limits listing and find/call_operation.
	toolsets map[string]bool
	// listed is what tools/list shows: the selection, or the default set.
	listed map[string]bool
}

type clientKey struct{}

func clientFrom(ctx context.Context, fallback string) string {
	if s, ok := ctx.Value(clientKey{}).(string); ok && s != "" {
		return s
	}
	if fallback != "" {
		return clip(fallback, 80)
	}
	return "mcp"
}

// clientName is the MCP client's name from clientInfo, else its User-Agent.
func (e *env) clientName(req *mcp.CallToolRequest) string {
	if req != nil {
		if ci := req.ClientInfo(); ci != nil && ci.Name != "" {
			return clip(ci.Name, 80)
		}
	}
	if e.ua != "" {
		return clip(e.ua, 80)
	}
	return "mcp"
}

func clip(s string, n int) string {
	s = strings.Map(func(r rune) rune {
		if r < 0x20 {
			return ' '
		}
		return r
	}, s)
	if len(s) > n {
		return s[:n]
	}
	return s
}

// handle executes one tool call: it validates the arguments against the tool's
// schema, binds the principal (actor type `mcp`), runs the production approval
// flow for mutating tools, maps errors to structured ToolErrors, caps the
// result and audits the call.
func (e *env) handle(ctx context.Context, req *mcp.CallToolRequest, rt *regTool) (res *mcp.CallToolResult) {
	ctx = auth.WithPrincipal(ctx, e.p)
	ctx = audit.WithActorType(ctx, audit.ActorMCP)
	client := e.clientName(req)
	ctx = prodguard.WithClient(ctx, client)
	ctx = context.WithValue(ctx, clientKey{}, client)
	ctx, cancel := context.WithTimeout(ctx, toolTimeout)
	defer cancel()

	var raw json.RawMessage
	if req != nil && req.Params != nil {
		raw = req.Params.Arguments
	}
	outcome := "ok"
	var auditArgs map[string]any
	defer func() {
		if r := recover(); r != nil {
			e.h.log.ErrorContext(ctx, "mcp tool panicked", "tool", rt.name, "panic", fmt.Sprint(r))
			outcome = CodeInternal
			res = errorResult(toolErr(CodeInternal, "internal error"))
		}
		e.audit(ctx, rt.name, auditArgs, outcome)
	}()

	validated, err := rt.val.apply(raw)
	if err != nil {
		outcome = CodeInvalidArgument
		return errorResult(toolErr(CodeInvalidArgument, "invalid arguments for %s: %s", rt.name, cleanValidation(err)))
	}
	auditArgs = redactArgs(validated)

	out, pre, err := e.invoke(ctx, req, rt, validated)
	switch {
	case pre != nil:
		outcome = "approval_required"
		return pre
	case err != nil:
		te := toToolError(ctx, e.h.log, rt.name, err)
		outcome = te.Code
		return errorResult(te)
	}
	return okResult(out)
}

// invoke runs the tool. When a domain service or the central production gate
// answers approval_required, the result carries the plan and the dashboard
// page where a person approves it (confirmation_required); the client never
// approves anything itself.
func (e *env) invoke(ctx context.Context, req *mcp.CallToolRequest, rt *regTool, args json.RawMessage) (any, *mcp.CallToolResult, error) {
	out, err := rt.call(ctx, e, req, args)
	if err == nil {
		return out, nil, nil
	}
	if ae, ok := prodguard.AsRequired(err); ok {
		return nil, confirmationRequired(ae), nil
	}
	return nil, nil, err
}

// callOp dispatches an operation in-process under the caller's token and
// shapes the response: curated projection, untrusted log wrapping, the
// {"ok":true} stand-in for an empty body.
func (e *env) callOp(ctx context.Context, m *opModel, proj *catalog.Projection, detail mcpkit.Detail, args map[string]any) (any, error) {
	if m.streaming && !m.hasJSONBody {
		return nil, mcpkit.Errf(mcpkit.CodeUnavailable, "%s streams events and cannot be called over MCP", m.id)
	}
	if m.hook != nil && m.hook.pre != nil {
		if err := m.hook.pre(ctx, e, args); err != nil {
			return nil, err
		}
	}
	dreq, err := m.request(ctx, e.h.resolver, args)
	if err != nil {
		return nil, err
	}
	// Central production guard: after the arguments are valid, before the
	// call is dispatched. An approval it consumed covers the domain service's
	// own check of the same call.
	ctx, err = e.gate(ctx, m, args)
	if err != nil {
		return nil, err
	}
	resp, err := e.h.disp.do(ctx, caller{token: e.token, client: clientFrom(ctx, e.ua), remoteAddr: e.remote}, dreq)
	if err != nil {
		return nil, err
	}
	if resp.status >= http.StatusBadRequest {
		return nil, problemError(resp)
	}
	var v any
	if len(strings.TrimSpace(string(resp.body))) == 0 {
		v = map[string]any{"ok": true}
	} else {
		dec := json.NewDecoder(strings.NewReader(string(resp.body)))
		dec.UseNumber()
		if err := dec.Decode(&v); err != nil {
			return nil, mcpkit.Errf(mcpkit.CodeInternal, "the API returned a response that is not JSON")
		}
	}
	if m.logs {
		v = wrapLogResponse(m.id, args, v)
	} else if proj != nil {
		v = proj.Apply(catalog.Detail(detail), v)
	}
	if m.hook != nil && m.hook.result != nil {
		if v, err = m.hook.result(ctx, e, v); err != nil {
			return nil, err
		}
	}
	v = wrapThirdParty(m.id, v)
	if m.outputKey != "" {
		v = map[string]any{m.outputKey: v}
	}
	return v, nil
}

// get reads one API resource in-process under the caller's token and returns
// its JSON object.
func (e *env) get(ctx context.Context, path string) (map[string]any, error) {
	resp, err := e.h.disp.do(ctx, caller{token: e.token, client: clientFrom(ctx, e.ua), remoteAddr: e.remote}, dispatchReq{method: http.MethodGet, path: path})
	if err != nil {
		return nil, err
	}
	if resp.status >= http.StatusBadRequest {
		return nil, problemError(resp)
	}
	dec := json.NewDecoder(strings.NewReader(string(resp.body)))
	dec.UseNumber()
	var out map[string]any
	if err := dec.Decode(&out); err != nil {
		return nil, mcpkit.Errf(mcpkit.CodeInternal, "the API returned a response that is not JSON")
	}
	return out, nil
}

// wrapLogResponse replaces every array of log entries in a log operation's
// response by an untrusted block (per-call nonce); the other fields (cursors,
// capture state, unavailable servers) stay structured.
func wrapLogResponse(opID string, args map[string]any, v any) any {
	source := opID
	for _, k := range []string{"serviceId", "deploymentId", "runId", "environmentId"} {
		if s, ok := args[k].(string); ok && s != "" {
			source += ":" + s
			break
		}
	}
	var walk func(any) any
	walk = func(x any) any {
		switch t := x.(type) {
		case map[string]any:
			for k, val := range t {
				if lines, ok := logLines(val); ok {
					t[k] = mcpkit.WrapLines(source, lines)
					continue
				}
				t[k] = walk(val)
			}
			return t
		case []any:
			if lines, ok := logLines(t); ok {
				return mcpkit.WrapLines(source, lines)
			}
			for i, e := range t {
				t[i] = walk(e)
			}
		}
		return x
	}
	return walk(v)
}

// logLines recognises an array of log entries (objects with a string `line`)
// and renders each as one text line: "<ts> [<stream>] <line>".
func logLines(v any) ([]string, bool) {
	arr, ok := v.([]any)
	if !ok || len(arr) == 0 {
		return nil, false
	}
	out := make([]string, 0, len(arr))
	for _, e := range arr {
		m, ok := e.(map[string]any)
		if !ok {
			return nil, false
		}
		line, ok := m["line"].(string)
		if !ok {
			return nil, false
		}
		var prefix []string
		for _, k := range []string{"ts", "createdAt"} {
			if s, ok := m[k].(string); ok && s != "" {
				prefix = append(prefix, s)
				break
			}
		}
		for _, k := range []string{"serviceName", "replica", "stream"} {
			if s, ok := m[k].(string); ok && s != "" {
				prefix = append(prefix, "["+s+"]")
			}
		}
		out = append(out, strings.TrimSpace(strings.Join(prefix, " ")+" "+line))
	}
	return out, true
}

func cleanValidation(err error) string {
	msg := err.Error()
	msg = strings.ReplaceAll(msg, "\n", "; ")
	if len(msg) > 400 {
		msg = msg[:400] + "…"
	}
	return msg
}

// sensitiveArg matches argument names that may carry secrets or free text.
var sensitiveArg = regexp.MustCompile(`(?i)(secret|password|passwd|token|credential|private|apikey|api_key|authorization|^(values?|command|body|env|variables|inputs?|data|content|text|generate|payload|config|script|args)$)`)

// redactArgs keeps tool arguments that are safe to audit: ids, selectors,
// counts and enums. Anything whose name suggests a secret or free text is
// replaced, long strings are clipped and nested values are summarized.
func redactArgs(raw json.RawMessage) map[string]any {
	var args map[string]any
	if err := json.Unmarshal(raw, &args); err != nil {
		return nil
	}
	out := make(map[string]any, len(args))
	for k, v := range args {
		if sensitiveArg.MatchString(k) {
			out[k] = "[redacted]"
			continue
		}
		out[k] = auditValue(v, 0)
	}
	return out
}

func auditValue(v any, depth int) any {
	switch t := v.(type) {
	case string:
		if len(t) > 200 {
			return t[:200] + "…"
		}
		return t
	case map[string]any:
		if depth >= 1 {
			return fmt.Sprintf("[object, %d keys]", len(t))
		}
		out := make(map[string]any, len(t))
		for k, e := range t {
			if sensitiveArg.MatchString(k) {
				out[k] = "[redacted]"
				continue
			}
			out[k] = auditValue(e, depth+1)
		}
		return out
	case []any:
		if depth >= 1 || len(t) > 20 {
			return fmt.Sprintf("[%d items]", len(t))
		}
		out := make([]any, len(t))
		for i, e := range t {
			out[i] = auditValue(e, depth+1)
		}
		return out
	}
	return v
}

// audit records the call in its own transaction, independent of the request
// context so a client disconnect cannot drop the record.
func (e *env) audit(ctx context.Context, tool string, args map[string]any, outcome string) {
	pool := e.h.deps.Pool
	if pool == nil {
		return
	}
	actx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	err := db.InTx(actx, pool, func(tx pgx.Tx) error {
		return audit.Record(actx, tx, audit.Entry{
			Action: AuditAction, ResourceType: "mcp_tool", ResourceID: tool,
			Metadata: map[string]any{"tool": tool, "outcome": outcome, "args": args, "client": clientFrom(ctx, e.ua)},
		})
	})
	if err != nil {
		e.h.log.ErrorContext(actx, "audit mcp tool call", "tool", tool, "error", err)
	}
}
