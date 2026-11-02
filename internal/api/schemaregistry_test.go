package api_test

import (
	"reflect"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/nebulactrl/nebulactrl/internal/api"
	"github.com/nebulactrl/nebulactrl/internal/configsync"
	"github.com/nebulactrl/nebulactrl/internal/projectconfig"
)

// The nebula.toml packages have short type names that collide with older
// schemas of other domains, so their OpenAPI names carry a "Config" prefix.
func TestSchemaRegistryPrefixesTheNebulaTomlPackages(t *testing.T) {
	ref := func(typ reflect.Type) string {
		return api.NewSchemaRegistry().Schema(typ, true, "").Ref
	}
	require.Equal(t, "#/components/schemas/ConfigPlan", ref(reflect.TypeFor[configsync.Plan]()))
	require.Equal(t, "#/components/schemas/ConfigApplyResult", ref(reflect.TypeFor[configsync.ApplyResult]()))
	require.Equal(t, "#/components/schemas/ConfigDiagnostic", ref(reflect.TypeFor[projectconfig.Diagnostic]()))
	require.Equal(t, "#/components/schemas/ConfigPlan", ref(reflect.TypeFor[*configsync.Plan]()), "a pointer is named after its element")
}
