package services

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestSubmodulesConfigValidationAndCanonicalAuto(t *testing.T) {
	cfg := Config{GitURL: "https://example.com/r.git", GitBranch: "main"}
	for _, ok := range []string{"", "auto", "on", "off"} {
		cfg.Submodules = ok
		require.NoError(t, cfg.WithDefaults().Validate(SourceGit), ok)
	}
	cfg.Submodules = "recursive"
	require.ErrorContains(t, cfg.WithDefaults().Validate(SourceGit), "submodules must be auto, on or off")

	// auto is the unset value, so an explicit auto never shows up as a pending change.
	cfg.Submodules = "auto"
	require.Empty(t, cfg.WithDefaults().Submodules)
	cfg.Submodules = "on"
	require.Equal(t, "on", cfg.WithDefaults().Submodules)
}
