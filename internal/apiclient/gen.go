// Package apiclient is the typed REST client of the control plane, generated
// with oapi-codegen from openapi/openapi.json (OpenAPI 3.1, accepted as is by
// oapi-codegen v2.8.0). Never edit client.gen.go by hand; run `make generate`.
package apiclient

// oapi-codegen resolves `output` in the config relative to the working
// directory, so the directive runs from the repository root, like the Makefile.
//go:generate sh -c "cd ../.. && go run github.com/oapi-codegen/oapi-codegen/v2/cmd/oapi-codegen@v2.8.0 -config internal/apiclient/oapi-codegen.yaml openapi/openapi.json"
