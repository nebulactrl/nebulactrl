package projects

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/nebulactrl/nebulactrl/internal/apierr"
)

func TestSlugify(t *testing.T) {
	require.Equal(t, "my-cool-app", Slugify("My Cool App!", "x"))
	require.Equal(t, "caf-au-lait", Slugify("  Café   au lait ", "x"))
	require.Equal(t, "x", Slugify("!!!", "x"))
	require.Equal(t, "a-b", Slugify("a___b", "x"))
	long := Slugify(strings.Repeat("ab ", 50), "x")
	require.LessOrEqual(t, len(long), maxSlugLen)
	require.False(t, strings.HasSuffix(long, "-"))
}

func TestUniqueSlug(t *testing.T) {
	require.Equal(t, "app", UniqueSlug("app", nil))
	require.Equal(t, "app-2", UniqueSlug("app", []string{"app"}))
	require.Equal(t, "app-3", UniqueSlug("app", []string{"app", "app-2", "other"}))
}

func TestCheckEnvironmentSlug(t *testing.T) {
	require.NoError(t, CheckEnvironmentSlug("staging"))
	require.NoError(t, CheckEnvironmentSlug("settings-2"))
	for _, name := range []string{"settings", "Settings", " SETTINGS!! "} {
		err := CheckEnvironmentSlug(Slugify(name, "environment"))
		ae, ok := apierr.As(err)
		require.True(t, ok, name)
		require.Equal(t, "slug_reserved", ae.Code)
		require.Equal(t, 422, ae.Status)
	}
}
