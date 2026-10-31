package composites_test

import (
	"encoding/json"
	"reflect"
	"testing"

	"github.com/google/jsonschema-go/jsonschema"
	"github.com/stretchr/testify/require"

	"github.com/nebulactrl/nebulactrl/internal/mcp/composites"
	"github.com/nebulactrl/nebulactrl/internal/mcp/mcpkit"
	"github.com/nebulactrl/nebulactrl/internal/policy"
)

// The 16 composites of the contract, with the scopes a token needs to see each
// and the hint that must hold. Tools needing no database to list.
var wantComposites = map[string]struct {
	scopes      []string
	readOnly    bool
	destructive bool
}{
	"get_overview":              {[]string{policy.ScopeProjectsRead}, true, false},
	"find_issues":               {[]string{policy.ScopeProjectsRead}, true, false},
	"diagnose_service":          {[]string{policy.ScopeProjectsRead}, true, false},
	"explain_failed_deployment": {[]string{policy.ScopeProjectsRead}, true, false},
	"diagnose_server":           {[]string{policy.ScopeServersRead}, true, false},
	"tail_logs":                 {[]string{policy.ScopeLogsRead}, true, false},
	"search_resources":          {[]string{policy.ScopeProjectsRead}, true, false},
	"review_changes":            {[]string{policy.ScopeProjectsRead}, true, false},
	"stage_changes":             {[]string{policy.ScopeProjectsWrite}, false, false},
	"apply_changes":             {[]string{policy.ScopeDeploymentsWrite}, false, false},
	"rollback_service":          {[]string{policy.ScopeDeploymentsRollback}, false, true},
	"add_database":              {[]string{policy.ScopeProjectsWrite}, false, false},
	"setup_domain":              {[]string{policy.ScopeProjectsWrite}, false, false},
	"verify_service":            {[]string{policy.ScopeProjectsRead}, true, false},
	"get_recent_changes":        {[]string{policy.ScopeProjectsRead}, true, false},
	// The project file: export and plan read (projects:read lists it), apply
	// stages and is refused without projects:write when called.
	"sync_config": {[]string{policy.ScopeProjectsRead}, false, false},
}

func TestComposeToolContract(t *testing.T) {
	tools := composites.Tools(composites.Deps{})
	require.Len(t, tools, len(wantComposites))
	seen := map[string]bool{}
	for _, tl := range tools {
		want, ok := wantComposites[tl.Name]
		require.True(t, ok, "unexpected composite %q", tl.Name)
		require.False(t, seen[tl.Name], "duplicate %q", tl.Name)
		seen[tl.Name] = true

		// The project file tool is the one composite outside core: it is for an
		// agent that edits configuration (?toolsets=core,config).
		wantToolset := mcpkit.ToolsetCore
		if tl.Name == "sync_config" {
			wantToolset = mcpkit.ToolsetConfig
		}
		require.Equal(t, wantToolset, tl.Toolset, tl.Name)
		require.ElementsMatch(t, want.scopes, tl.Scopes, tl.Name)
		require.Equal(t, want.readOnly, tl.Hints.ReadOnly, tl.Name)
		require.Equal(t, want.destructive, tl.Hints.Destructive, tl.Name)
		if tl.Hints.ReadOnly {
			require.True(t, tl.Hints.Idempotent, "%s: read-only tools are idempotent", tl.Name)
			require.False(t, tl.Hints.Destructive, tl.Name)
		}
		require.NotEmpty(t, tl.Title, tl.Name)
		require.GreaterOrEqual(t, len(tl.Description), 80, "%s needs an agent-facing description", tl.Name)
		require.NotNil(t, tl.Input, tl.Name)
		require.NotNil(t, tl.Output, tl.Name)
		require.NotNil(t, tl.Call, tl.Name)

		// Both schemas derive, and the output type has the envelope fields.
		in, err := jsonschema.ForType(reflect.TypeOf(tl.Input), nil)
		require.NoError(t, err, "%s input schema", tl.Name)
		out, err := jsonschema.ForType(reflect.TypeOf(tl.Output), nil)
		require.NoError(t, err, "%s output schema", tl.Name)
		raw, _ := json.Marshal(out)
		for _, f := range []string{`"summary"`, `"errors"`, `"nextTools"`} {
			require.Contains(t, string(raw), f, "%s output lacks %s", tl.Name, f)
		}
		// No uuid.UUID arrays ([16]integer) and no raw JSON in outputs.
		require.NotContains(t, string(raw), `"minItems":16`, tl.Name)
		_ = in
	}
	require.Len(t, seen, len(wantComposites))
}

// Every tool except the pure-selector ones takes a `detail` argument or is
// documented as having none; arguments are decoded strictly.
func TestComposeToolsRejectUnknownArguments(t *testing.T) {
	for _, tl := range composites.Tools(composites.Deps{}) {
		_, err := tl.Call(t.Context(), nil, json.RawMessage(`{"definitely_not_an_argument":1}`))
		require.Error(t, err, tl.Name)
		var te *mcpkit.Error
		require.ErrorAs(t, err, &te, tl.Name)
		require.Equal(t, mcpkit.CodeInvalidArgument, te.Code, tl.Name)
	}
}
