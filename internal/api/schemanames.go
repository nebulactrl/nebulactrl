package api

import (
	"reflect"
	"strings"

	"github.com/danielgtaylor/huma/v2"
)

// schemaPrefix is the OpenAPI components prefix every schema reference uses.
const schemaPrefix = "#/components/schemas/"

// configPackages hold the types of the nebula.toml feature. Their short Go
// names (Plan, PlanSource, Guard, Diagnostic, …) collide with older schemas of
// other domains, so their OpenAPI names carry a "Config" prefix.
var configPackages = map[string]bool{
	"github.com/nebulactrl/nebulactrl/internal/configsync":    true,
	"github.com/nebulactrl/nebulactrl/internal/projectconfig": true,
}

// NewSchemaRegistry is the registry every API document uses: huma's map
// registry with schemaNamer.
func NewSchemaRegistry() huma.Registry { return huma.NewMapRegistry(schemaPrefix, schemaNamer) }

// schemaNamer names OpenAPI schemas: huma's default, with named types of the
// nebula.toml packages prefixed by "Config" (ConfigPlan, ConfigApplyResult,
// ConfigDiagnostic) unless the name already says Config (ServiceConfigSource).
func schemaNamer(t reflect.Type, hint string) string {
	name := huma.DefaultSchemaNamer(t, hint)
	for t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	if t.Name() != "" && configPackages[t.PkgPath()] && !strings.Contains(name, "Config") {
		return "Config" + name
	}
	return name
}
