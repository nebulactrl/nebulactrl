package mcp

import (
	"encoding/json"
	"fmt"
	"reflect"
	"strings"

	"github.com/google/jsonschema-go/jsonschema"
)

// jsonObj is a decoded JSON object (a JSON Schema, an operation).
type jsonObj = map[string]any

const componentsPrefix = "#/components/schemas/"

// schemaInliner resolves `$ref`s of an OpenAPI document's component schemas
// into self-contained JSON Schemas (MCP tools carry no shared definitions).
type schemaInliner struct {
	components map[string]jsonObj
}

// inline returns a deep copy of s with every $ref expanded. A reference that
// would recurse (a cycle) or nests deeper than maxDepth collapses to a loose
// `{"type":"object"}`.
func (in *schemaInliner) inline(s any, maxDepth int) any {
	return in.walk(s, maxDepth, nil)
}

func (in *schemaInliner) walk(v any, depth int, stack []string) any {
	switch t := v.(type) {
	case jsonObj:
		if ref, ok := t["$ref"].(string); ok {
			name := strings.TrimPrefix(ref, componentsPrefix)
			target, found := in.components[name]
			if !found || depth <= 0 {
				return jsonObj{"type": "object"}
			}
			for _, s := range stack {
				if s == name {
					return jsonObj{"type": "object"}
				}
			}
			out := in.walk(target, depth-1, append(stack, name))
			if m, ok := out.(jsonObj); ok {
				// Sibling keywords of the ref (description) win over the target's.
				for k, val := range t {
					if k != "$ref" {
						m[k] = in.walk(val, depth, stack)
					}
				}
			}
			return out
		}
		out := make(jsonObj, len(t))
		for k, val := range t {
			if k == "$schema" {
				continue
			}
			out[k] = in.walk(val, depth, stack)
		}
		return out
	case []any:
		out := make([]any, len(t))
		for i, e := range t {
			out[i] = in.walk(e, depth, stack)
		}
		return out
	}
	return v
}

// stripRequired relaxes an output schema in place: it removes every `required`
// list (but not a property named "required") and `additionalProperties: false`.
// Concise projections drop fields and tools add a few (terminal, note), so a
// strict schema would make clients that validate structured content reject
// valid results.
func stripRequired(v any) {
	switch t := v.(type) {
	case jsonObj:
		delete(t, "required")
		if ap, ok := t["additionalProperties"].(bool); ok && !ap {
			delete(t, "additionalProperties")
		}
		if props, ok := t["properties"].(jsonObj); ok {
			for _, p := range props {
				stripRequired(p)
			}
		}
		for k, val := range t {
			if k == "properties" {
				continue
			}
			stripRequired(val)
		}
	case []any:
		for _, e := range t {
			stripRequired(e)
		}
	}
}

// slim returns a trimmed copy of the schema v for tools/list, where every
// byte costs the client's context: objects and arrays nested deeper than
// maxLevel collapse to their bare type, descriptions deeper than descLevel
// (and all examples) are dropped. Validation never uses the slim form.
func slim(v any, level, maxLevel, descLevel int, opID string) any {
	t, ok := v.(jsonObj)
	if !ok {
		return v
	}
	if level > maxLevel {
		if kind, composite := compositeKind(t); composite {
			return jsonObj{"type": kind}
		}
	}
	if props, _ := t["properties"].(jsonObj); opID != "" && level == 1 && len(props) > wideObjectProps {
		desc, _ := t["description"].(string)
		return jsonObj{"type": "object", "description": strings.TrimSpace(clipDesc(desc) + " Full field reference: find_operation('" + opID + "').")}
	}
	out := make(jsonObj, len(t))
	for k, val := range t {
		switch k {
		case "examples", "example", "$comment", "readOnly", "writeOnly", "deprecated", "title", "$schema", "format":
			continue
		case "description":
			if level <= descLevel {
				if s, ok := val.(string); ok && level >= 1 {
					val = clipDesc(s)
				}
				out[k] = val
			}
		case "properties":
			props, _ := val.(jsonObj)
			np := make(jsonObj, len(props))
			for pk, pv := range props {
				np[pk] = slim(pv, level+1, maxLevel, descLevel, opID)
			}
			out[k] = np
		case "items", "additionalProperties", "not":
			out[k] = slim(val, level+1, maxLevel, descLevel, opID)
		case "anyOf", "oneOf", "allOf":
			if list, ok := val.([]any); ok {
				nl := make([]any, len(list))
				for i, e := range list {
					nl[i] = slim(e, level+1, maxLevel, descLevel, opID)
				}
				out[k] = nl
				continue
			}
			out[k] = val
		default:
			out[k] = val
		}
	}
	return out
}

// compositeKind reports whether t describes an object or an array and returns
// its `type` value (kept when collapsing).
func compositeKind(t jsonObj) (any, bool) {
	switch typ := t["type"].(type) {
	case string:
		if typ == "object" || typ == "array" {
			return typ, true
		}
	case []any:
		for _, e := range typ {
			if s, _ := e.(string); s == "object" || s == "array" {
				return typ, true
			}
		}
	}
	if t["properties"] != nil {
		return "object", true
	}
	if t["items"] != nil {
		return "array", true
	}
	return nil, false
}

const (
	// maxInputSchemaLevel and inputDescLevel shape the advertised input schema.
	maxInputSchemaLevel = 2
	inputDescLevel      = 1
	// wideObjectProps is the property count above which a top-level argument
	// object collapses to a pointer to find_operation.
	wideObjectProps = 10
	// descClip bounds a nested description (bytes).
	descClip = 100
)

// advertiseInput is the input schema as listed to clients (the full schema
// stays the validator). opID, when set, names the operation whose full field
// reference find_operation returns for collapsed wide objects.
func advertiseInput(s any, opID string) any {
	m, ok := toObj(s)
	if !ok {
		return s
	}
	out := slim(m, 0, maxInputSchemaLevel, inputDescLevel, opID)
	if schemaBytes(out) > maxAdvertisedInputBytes {
		out = slim(m, 0, 1, inputDescLevel, opID)
	}
	return out
}

// advertiseOutput is the output schema as listed to clients: shallow (top-level
// properties and their types only), relaxed (no required lists, open objects)
// and without descriptions.
func advertiseOutput(s any) any {
	m, ok := toObj(s)
	if !ok {
		return s
	}
	stripRequired(m)
	out, _ := slim(m, 0, 0, -1, "").(jsonObj)
	if schemaBytes(out) > maxAdvertisedOutputBytes {
		out = namesOnly(out)
	}
	return out
}

// clipDesc shortens a description to one short line.
func clipDesc(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	if len(s) <= descClip {
		return s
	}
	cut := descClip
	for cut > 0 && s[cut-1] != ' ' {
		cut--
	}
	if cut == 0 {
		cut = descClip
	}
	return strings.TrimRight(s[:cut], " ,;:.") + "…"
}

// toObj converts a schema (a *jsonschema.Schema or a decoded object) to a
// fresh decoded object.
func toObj(s any) (jsonObj, bool) {
	raw, err := json.Marshal(s)
	if err != nil {
		return nil, false
	}
	var m jsonObj
	if err := json.Unmarshal(raw, &m); err != nil {
		return nil, false
	}
	return m, true
}

// schemaOf infers the JSON Schema of a typed zero value (struct or pointer to
// struct), as the SDK does for its generic tools.
func schemaOf(zero any) (*jsonschema.Schema, error) {
	if zero == nil {
		return nil, nil
	}
	t := reflect.TypeOf(zero)
	for t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	return jsonschema.ForType(t, &jsonschema.ForOptions{})
}

// compiled is a resolved schema that validates tool arguments and fills
// defaults.
type compiled struct {
	resolved *jsonschema.Resolved
}

// compileSchema resolves s (a *jsonschema.Schema or a decoded JSON object).
func compileSchema(s any) (*compiled, error) {
	var sch *jsonschema.Schema
	switch t := s.(type) {
	case *jsonschema.Schema:
		sch = t
	default:
		raw, err := json.Marshal(t)
		if err != nil {
			return nil, err
		}
		sch = new(jsonschema.Schema)
		if err := json.Unmarshal(raw, sch); err != nil {
			return nil, fmt.Errorf("decode schema: %w", err)
		}
	}
	r, err := sch.Resolve(&jsonschema.ResolveOptions{})
	if err != nil {
		return nil, fmt.Errorf("resolve schema: %w", err)
	}
	return &compiled{resolved: r}, nil
}

// apply parses raw as an object, applies the schema's defaults and validates
// it. The returned bytes carry the defaults.
func (c *compiled) apply(raw json.RawMessage) (json.RawMessage, error) {
	obj := map[string]any{}
	if len(raw) > 0 && string(raw) != "null" {
		if err := json.Unmarshal(raw, &obj); err != nil {
			return nil, fmt.Errorf("arguments must be a JSON object: %w", err)
		}
	}
	var v any = obj
	if err := c.resolved.ApplyDefaults(&v); err != nil {
		return nil, err
	}
	if err := c.resolved.Validate(&v); err != nil {
		return nil, err
	}
	out, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}
	return out, nil
}

// jsonschemaEmpty is the input schema of a tool without arguments.
var jsonschemaEmpty = jsonschema.Schema{Type: "object"}

const (
	// maxAdvertisedInputBytes and maxAdvertisedOutputBytes bound one tool's
	// schemas in tools/list; beyond them nesting (input) or property types
	// (output) are dropped. find_operation and the docs carry the full shape.
	maxAdvertisedInputBytes  = 1000
	maxAdvertisedOutputBytes = 400
)

func schemaBytes(v any) int {
	raw, _ := json.Marshal(v)
	return len(raw)
}

// namesOnly reduces an object schema's properties to bare names.
func namesOnly(m jsonObj) jsonObj {
	props, _ := m["properties"].(jsonObj)
	for k := range props {
		props[k] = jsonObj{}
	}
	return m
}
