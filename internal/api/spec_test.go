package api

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/danielgtaylor/huma/v2"
	"github.com/danielgtaylor/huma/v2/adapters/humachi"
	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

type specInner struct {
	Name string `json:"name"`
}

type SpecEmbedded struct {
	Shared *specInner `json:"shared"`
}

type specView struct {
	SpecEmbedded
	Inner       *specInner  `json:"inner"`
	InnerOpt    *specInner  `json:"innerOpt,omitempty"`
	InnerZero   *specInner  `json:"innerZero,omitzero"`
	Owner       *uuid.UUID  `json:"owner"`
	OwnerOpt    *uuid.UUID  `json:"ownerOpt,omitempty"`
	Label       *string     `json:"label"`
	Count       *int        `json:"count"`
	At          *time.Time  `json:"at"`
	Hidden      *specInner  `json:"-"`
	NotNullable *specInner  `json:"notNullable" nullable:"false"`
	Plain       specInner   `json:"plain"`
	Items       []specInner `json:"items"`
}

type specOutput struct{ Body specView }

type specRequestOnly struct {
	Inner *specInner `json:"inner"`
}

type specInput struct{ Body specRequestOnly }

func specDoc(t *testing.T, errors ...int) map[string]any {
	t.Helper()
	r := chi.NewRouter()
	h := humachi.New(r, humaConfig())
	huma.Register(h, huma.Operation{
		OperationID: "getSpecView", Method: http.MethodGet, Path: "/view", Errors: errors,
	}, func(context.Context, *struct{}) (*specOutput, error) { return &specOutput{}, nil })
	huma.Register(h, huma.Operation{
		OperationID: "putSpecInput", Method: http.MethodPut, Path: "/input", DefaultStatus: http.StatusNoContent,
	}, func(context.Context, *specInput) (*struct{}, error) { return nil, nil })
	raw, err := json.Marshal(h.OpenAPI())
	require.NoError(t, err)
	var doc map[string]any
	require.NoError(t, json.Unmarshal(raw, &doc))
	return doc
}

func dig(t *testing.T, v any, path ...string) map[string]any {
	t.Helper()
	for _, key := range path {
		m, ok := v.(map[string]any)
		require.True(t, ok, "%s is not an object", key)
		v = m[key]
	}
	m, ok := v.(map[string]any)
	require.True(t, ok, "%v is not an object", path)
	return m
}

func TestEveryOperationDocumentsADefaultError(t *testing.T) {
	doc := specDoc(t, http.StatusNotFound, http.StatusConflict)
	responses := dig(t, doc, "paths", "/view", "get", "responses")
	require.Contains(t, responses, "404")
	require.Contains(t, responses, "409")
	def := dig(t, responses, "default")
	require.Equal(t, "Error", def["description"])
	require.Equal(t, map[string]any{"$ref": "#/components/schemas/Error"},
		dig(t, def, "content", "application/problem+json", "schema"))

	// An operation without an Errors list already had one from huma.
	require.Contains(t, dig(t, doc, "paths", "/input", "put", "responses"), "default")
}

func TestPointerFieldsOfResponsesAreNullable(t *testing.T) {
	doc := specDoc(t)
	props := dig(t, doc, "components", "schemas", "SpecView", "properties")
	required := dig(t, doc, "components", "schemas", "SpecView")["required"].([]any)

	nullObject := map[string]any{"oneOf": []any{
		map[string]any{"$ref": "#/components/schemas/SpecInner"},
		map[string]any{"type": "null"},
	}}
	require.Equal(t, nullObject, props["inner"], "a pointer to an object is `T | null`")
	require.Equal(t, nullObject, props["shared"], "also through an embedded struct")
	require.Contains(t, required, "inner", "still always present")

	for _, name := range []string{"owner", "label", "count", "at"} {
		require.Equal(t, true, nullableType(props[name]), "%s: scalar pointers are nullable: %v", name, props[name])
		require.Contains(t, required, name)
	}

	for _, name := range []string{"innerOpt", "innerZero", "ownerOpt"} {
		require.NotContains(t, required, name, "omitempty pointers are optional")
		require.NotEqual(t, nullObject, props[name], "%s is never null", name)
		require.False(t, nullableType(props[name]), "%s is never null", name)
	}
	require.NotContains(t, props, "-")
	require.Equal(t, map[string]any{"$ref": "#/components/schemas/SpecInner"}, props["notNullable"], "nullable:false wins")
	require.Equal(t, map[string]any{"$ref": "#/components/schemas/SpecInner"}, props["plain"], "a value is never null")
}

func TestRequestOnlySchemasKeepTheirDeclaredShape(t *testing.T) {
	doc := specDoc(t)
	props := dig(t, doc, "components", "schemas", "SpecRequestOnly", "properties")
	require.Equal(t, map[string]any{"$ref": "#/components/schemas/SpecInner"}, props["inner"])
}

func nullableType(prop any) bool {
	m, ok := prop.(map[string]any)
	if !ok {
		return false
	}
	types, ok := m["type"].([]any)
	return ok && len(types) == 2 && types[1] == "null"
}
