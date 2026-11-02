package api

import (
	"reflect"
	"strings"

	"github.com/danielgtaylor/huma/v2"
)

// nullableFieldsHook documents the pointer fields of response types as
// nullable. encoding/json writes a nil pointer without `omitempty` as `null`,
// but huma lists such a field as required and, for fields of an object type,
// cannot mark it nullable at all (the `nullable` tag panics there). The spec
// would then promise a value the server does not always send, and the
// generated clients (TypeScript `T`, Go `T`) would be wrong about it.
//
// The hook runs for every operation, follows the response schemas to the
// struct types behind them and, for every pointer field of such a type that
// has neither `omitempty` nor `omitzero` (and no `nullable:"false"`), makes the
// property nullable: `oneOf: [{$ref}, {type: null}]` for an object type (the
// generated clients read it as `T | null` and `*T`), `type: [T, null]` for a
// scalar. A pointer field with `omitempty` is left alone: it is optional and
// never null. Request-only types are not visited, so input validation stays as
// declared. The Go field type is the single source of truth: nothing to tag,
// nothing to keep in step.
func nullableFieldsHook() func(*huma.OpenAPI, *huma.Operation) {
	done := map[string]bool{}
	return func(oapi *huma.OpenAPI, op *huma.Operation) {
		v := &nullableVisitor{registry: oapi.Components.Schemas, done: done}
		for _, response := range op.Responses {
			for _, media := range response.Content {
				v.visit(media.Schema)
			}
		}
	}
}

type nullableVisitor struct {
	registry huma.Registry
	done     map[string]bool // schema refs already processed
}

// visit follows s to every named schema it reaches.
func (v *nullableVisitor) visit(s *huma.Schema) {
	if s == nil {
		return
	}
	if s.Ref != "" {
		v.visitRef(s.Ref)
		return
	}
	v.visit(s.Items)
	for _, p := range s.Properties {
		v.visit(p)
	}
	if extra, ok := s.AdditionalProperties.(*huma.Schema); ok {
		v.visit(extra)
	}
	for _, group := range [][]*huma.Schema{s.OneOf, s.AnyOf, s.AllOf} {
		for _, member := range group {
			v.visit(member)
		}
	}
}

func (v *nullableVisitor) visitRef(ref string) {
	if v.done[ref] {
		return
	}
	v.done[ref] = true
	s := v.registry.SchemaFromRef(ref)
	if s == nil {
		return
	}
	if t := v.registry.TypeFromRef(ref); t != nil && t.Kind() == reflect.Struct {
		markNullableFields(t, s)
	}
	v.visit(s)
}

// markNullableFields makes the properties of s that the pointer fields of t
// can send as null nullable.
func markNullableFields(t reflect.Type, s *huma.Schema) {
	for i := range t.NumField() {
		f := t.Field(i)
		name, options, _ := strings.Cut(f.Tag.Get("json"), ",")
		if name == "-" && options == "" {
			continue
		}
		if f.Anonymous && name == "" {
			embedded := f.Type
			for embedded.Kind() == reflect.Pointer {
				embedded = embedded.Elem()
			}
			if embedded.Kind() == reflect.Struct {
				markNullableFields(embedded, s)
				continue
			}
		}
		if !f.IsExported() || f.Type.Kind() != reflect.Pointer ||
			f.Tag.Get("nullable") == "false" ||
			hasOption(options, "omitempty") || hasOption(options, "omitzero") {
			continue
		}
		if name == "" {
			name = f.Name
		}
		if prop := s.Properties[name]; prop != nil {
			s.Properties[name] = nullable(prop)
		}
	}
}

func hasOption(options, want string) bool {
	for option := range strings.SplitSeq(options, ",") {
		if option == want {
			return true
		}
	}
	return false
}

// nullable returns prop as a schema that also accepts null.
func nullable(prop *huma.Schema) *huma.Schema {
	switch {
	case prop.Nullable || len(prop.OneOf) > 0 || len(prop.AnyOf) > 0:
		return prop
	case prop.Ref != "":
		return &huma.Schema{OneOf: []*huma.Schema{{Ref: prop.Ref}, {Type: huma.TypeNull}}}
	case prop.Type != "":
		prop.Nullable = true
	}
	return prop
}
