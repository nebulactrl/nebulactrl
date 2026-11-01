package mcp

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"slices"
	"sort"
	"strconv"
	"strings"

	"github.com/danielgtaylor/huma/v2"
	"github.com/google/uuid"

	"github.com/nebulactrl/nebulactrl/internal/api"
	"github.com/nebulactrl/nebulactrl/internal/mcp/mcpkit"
	"github.com/nebulactrl/nebulactrl/internal/policy"
)

// opParam is one path or query parameter surfaced as a tool argument.
type opParam struct {
	name     string
	in       string // path | query
	required bool
	selector string // "", "project", "environment" or "service": accepts a selector
}

// opModel is one REST operation as a tool: its flat input schema (path +
// query + body merged into one object), the mapping back to the request, the
// scopes it needs and the schema of its result.
type opModel struct {
	id          string
	method      string
	path        string
	summary     string
	description string
	tags        []string
	scopes      []string

	params []opParam
	// bodyFields maps tool argument -> body property for an object body.
	bodyFields map[string]string
	// bodyArg is the argument carrying a non-object body verbatim ("" if none).
	bodyArg     string
	ifMatch     bool
	ifMatchReq  bool
	streaming   bool // the success response can be text/event-stream
	hasJSONBody bool // the success response also has a JSON form
	binary      bool // the success response is not JSON
	logs        bool // the operation returns log text (wrapped as untrusted)
	forceQuery  map[string]string
	hook        *opHook
	input       jsonObj
	output      jsonObj // object schema; nil when the response has no body
	outputKey   string  // non-empty when the response is wrapped as {outputKey: value}
	validator   *compiled
}

func (m *opModel) mutating() bool {
	return m.method != http.MethodGet && m.method != http.MethodHead
}

// selectorParam maps path/query parameter names that accept human selectors.
var selectorParam = map[string]string{
	"projectId":     "project",
	"environmentId": "environment",
	"serviceId":     "service",
}

const selectorHelp = "Accepts a UUID or a selector: " +
	"project: slug or name; environment: project/env; service: project/env/service or a hostname."

// buildOps derives an opModel for every operation of the huma API.
func buildOps(h huma.API) (map[string]*opModel, error) {
	doc := h.OpenAPI()
	raw, err := json.Marshal(doc)
	if err != nil {
		return nil, fmt.Errorf("marshal openapi: %w", err)
	}
	var spec struct {
		Paths      map[string]map[string]jsonObj `json:"paths"`
		Components struct {
			Schemas map[string]jsonObj `json:"schemas"`
		} `json:"components"`
	}
	if err := json.Unmarshal(raw, &spec); err != nil {
		return nil, fmt.Errorf("decode openapi: %w", err)
	}
	inl := &schemaInliner{components: spec.Components.Schemas}

	scopes := map[string][]string{}
	for _, item := range doc.Paths {
		for _, op := range []*huma.Operation{item.Get, item.Put, item.Post, item.Delete, item.Patch, item.Head} {
			if op != nil {
				scopes[op.OperationID] = api.ScopesOf(op)
			}
		}
	}

	out := map[string]*opModel{}
	for path, methods := range spec.Paths {
		for method, op := range methods {
			id, _ := op["operationId"].(string)
			if id == "" {
				continue
			}
			m, err := newOpModel(inl, strings.ToUpper(method), path, op)
			if err != nil {
				return nil, fmt.Errorf("operation %s: %w", id, err)
			}
			m.scopes = scopes[id]
			if m.scopes == nil {
				m.scopes = []string{}
			}
			if slices.Contains(m.scopes, policy.ScopeLogsRead) {
				m.logs = true
				m.outputKey = ""
				m.output = jsonObj{
					"type":                 "object",
					"description":          "Log entries are returned as untrusted text blocks ({untrusted, nonce, source, notice, text, lines}); other fields are structured.",
					"additionalProperties": true,
				}
			}
			out[id] = m
		}
	}
	return out, nil
}

func newOpModel(inl *schemaInliner, method, path string, op jsonObj) (*opModel, error) {
	m := &opModel{method: method, path: path, bodyFields: map[string]string{}}
	m.id, _ = op["operationId"].(string)
	m.summary, _ = op["summary"].(string)
	m.description, _ = op["description"].(string)
	if tags, ok := op["tags"].([]any); ok {
		for _, t := range tags {
			if s, ok := t.(string); ok {
				m.tags = append(m.tags, s)
			}
		}
	}

	props := jsonObj{}
	var required []string

	if params, ok := op["parameters"].([]any); ok {
		for _, p := range params {
			pm, _ := p.(jsonObj)
			name, _ := pm["name"].(string)
			in, _ := pm["in"].(string)
			req, _ := pm["required"].(bool)
			desc, _ := pm["description"].(string)
			schema, _ := inl.inline(pm["schema"], 8).(jsonObj)
			if schema == nil {
				schema = jsonObj{"type": "string"}
			}
			switch {
			case in == "header" && strings.EqualFold(name, "If-Match"):
				m.ifMatch, m.ifMatchReq = true, req
				props["ifMatch"] = jsonObj{
					"type":        "integer",
					"description": "The generation (or version) from the last read of the resource; a stale value is refused with generation_conflict.",
				}
				if req {
					required = append(required, "ifMatch")
				}
			case in == "query" && name == "follow":
				// Follow modes stream; the tool always reads a bounded snapshot.
				m.forceQuery = map[string]string{"follow": "false"}
			case in == "path" || in == "query":
				p := opParam{name: name, in: in, required: req || in == "path"}
				if sel, ok := selectorParam[name]; ok {
					p.selector = sel
					schema = jsonObj{"type": "string"}
					if desc == "" {
						desc = name
					}
					desc += ". " + selectorHelp
				}
				if desc != "" {
					schema["description"] = desc
				}
				props[name] = schema
				if p.required {
					required = append(required, name)
				}
				m.params = append(m.params, p)
			}
		}
	}

	if rb, ok := op["requestBody"].(jsonObj); ok {
		if content, ok := rb["content"].(jsonObj); ok {
			if mt, ok := content["application/json"].(jsonObj); ok {
				body, _ := inl.inline(mt["schema"], 8).(jsonObj)
				bodyReq, _ := rb["required"].(bool)
				bprops, _ := body["properties"].(jsonObj)
				if typ, _ := body["type"].(string); typ == "object" && len(bprops) > 0 && body["oneOf"] == nil && body["anyOf"] == nil {
					for pk, pv := range bprops {
						name := pk
						if _, clash := props[name]; clash || name == "ifMatch" || name == "detail" {
							name = "body_" + pk
						}
						props[name] = pv
						m.bodyFields[name] = pk
					}
					if reqs, ok := body["required"].([]any); ok {
						for _, r := range reqs {
							if rs, ok := r.(string); ok {
								for arg, real := range m.bodyFields {
									if real == rs {
										required = append(required, arg)
									}
								}
							}
						}
					}
				} else if body != nil {
					m.bodyArg = "body"
					if desc, _ := body["description"].(string); desc == "" {
						body["description"] = "The request body."
					}
					props["body"] = body
					if bodyReq {
						required = append(required, "body")
					}
				}
			}
		}
	}

	sort.Strings(required)
	input := jsonObj{"type": "object", "properties": props, "additionalProperties": false}
	if len(required) > 0 {
		input["required"] = toAnySlice(dedupe(required))
	}
	m.input = input
	m.hook = opHooks[m.id]
	if m.hook != nil && m.hook.schema != nil {
		m.hook.schema(m.input)
	}

	// Response.
	if responses, ok := op["responses"].(jsonObj); ok {
		for _, code := range []string{"200", "201", "202", "203"} {
			resp, ok := responses[code].(jsonObj)
			if !ok {
				continue
			}
			content, _ := resp["content"].(jsonObj)
			if _, ok := content["text/event-stream"]; ok {
				m.streaming = true
			}
			if mt, ok := content["application/json"].(jsonObj); ok {
				m.hasJSONBody = true
				if sch, ok := inl.inline(mt["schema"], 6).(jsonObj); ok {
					m.setOutput(sch)
				}
			} else if len(content) > 0 && !m.streaming {
				m.binary = true
			}
			break
		}
	}
	if m.output == nil {
		m.output = jsonObj{"type": "object", "properties": jsonObj{"ok": jsonObj{"type": "boolean"}}}
		m.outputKey = ""
	}
	v, err := compileSchema(m.input)
	if err != nil {
		return nil, err
	}
	m.validator = v
	return m, nil
}

// setOutput stores the advertised (slim, requirement-free) result schema,
// wrapping a non-object result under "result".
func (m *opModel) setOutput(sch jsonObj) {
	if typ, _ := sch["type"].(string); typ == "object" || sch["properties"] != nil {
		m.output, _ = advertiseOutput(sch).(jsonObj)
		return
	}
	m.outputKey = "result"
	m.output, _ = advertiseOutput(jsonObj{"type": "object", "properties": jsonObj{"result": sch}}).(jsonObj)
}

func dedupe(in []string) []string {
	out := in[:0:0]
	for i, s := range in {
		if i == 0 || s != in[i-1] {
			out = append(out, s)
		}
	}
	return out
}

func toAnySlice(in []string) []any {
	out := make([]any, len(in))
	for i, s := range in {
		out[i] = s
	}
	return out
}

// decodeArgs decodes validated arguments keeping integers exact.
func decodeArgs(raw json.RawMessage) (map[string]any, error) {
	args := map[string]any{}
	if len(bytes.TrimSpace(raw)) == 0 {
		return args, nil
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	if err := dec.Decode(&args); err != nil {
		return nil, mcpkit.Errf(mcpkit.CodeInvalidArgument, "arguments must be a JSON object")
	}
	return args, nil
}

func scalarString(v any) (string, bool) {
	switch t := v.(type) {
	case string:
		return t, true
	case json.Number:
		return t.String(), true
	case float64:
		return strconv.FormatFloat(t, 'f', -1, 64), true
	case bool:
		return strconv.FormatBool(t), true
	}
	return "", false
}

// request turns validated arguments into the REST request of the operation.
// Selector arguments are resolved to ids first (under the caller's principal).
func (m *opModel) request(ctx context.Context, res *mcpkit.Resolver, args map[string]any) (dispatchReq, error) {
	req := dispatchReq{method: m.method, query: url.Values{}}
	for k, v := range m.forceQuery {
		req.query.Set(k, v)
	}
	path := m.path
	for _, p := range m.params {
		v, present := args[p.name]
		if !present {
			continue
		}
		if p.selector != "" {
			s, _ := v.(string)
			id, err := resolveSelector(ctx, res, p.selector, s)
			if err != nil {
				return req, err
			}
			v = id
		}
		switch p.in {
		case "path":
			s, ok := scalarString(v)
			if !ok || s == "" {
				return req, mcpkit.Errf(mcpkit.CodeInvalidArgument, "%s must be a non-empty string", p.name)
			}
			path = strings.ReplaceAll(path, "{"+p.name+"}", url.PathEscape(s))
		case "query":
			if list, ok := v.([]any); ok {
				for _, e := range list {
					if s, ok := scalarString(e); ok {
						req.query.Add(p.name, s)
					}
				}
			} else if s, ok := scalarString(v); ok {
				req.query.Set(p.name, s)
			}
		}
	}
	req.path = path
	if m.ifMatch {
		if v, ok := args["ifMatch"]; ok {
			if s, ok := scalarString(v); ok {
				req.ifMatch = strconv.Quote(s)
			}
		}
	}
	switch {
	case m.bodyArg != "":
		if v, ok := args[m.bodyArg]; ok {
			b, err := json.Marshal(v)
			if err != nil {
				return req, mcpkit.Errf(mcpkit.CodeInvalidArgument, "body is not valid JSON")
			}
			req.body = b
		}
	case len(m.bodyFields) > 0:
		body := map[string]any{}
		for arg, real := range m.bodyFields {
			if v, ok := args[arg]; ok {
				body[real] = v
			}
		}
		if m.hook != nil && m.hook.body != nil {
			if err := m.hook.body(body); err != nil {
				return req, err
			}
		}
		b, err := json.Marshal(body)
		if err != nil {
			return req, mcpkit.Errf(mcpkit.CodeInvalidArgument, "body is not valid JSON")
		}
		req.body = b
	}
	return req, nil
}

// resolveSelector turns a selector into the id string the REST path wants.
func resolveSelector(ctx context.Context, res *mcpkit.Resolver, kind, sel string) (string, error) {
	sel = strings.TrimSpace(sel)
	if _, err := uuid.Parse(sel); err == nil {
		return sel, nil
	}
	if res == nil {
		return "", mcpkit.Errf(mcpkit.CodeUnavailable, "selectors are not available on this control plane; pass an id")
	}
	switch kind {
	case "project":
		p, err := res.Project(ctx, sel)
		if err != nil {
			return "", err
		}
		return p.ID.String(), nil
	case "environment":
		e, err := res.Environment(ctx, sel)
		if err != nil {
			return "", err
		}
		return e.ID.String(), nil
	default:
		s, err := res.Service(ctx, sel)
		if err != nil {
			return "", err
		}
		return s.ID.String(), nil
	}
}
