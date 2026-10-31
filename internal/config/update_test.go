package config

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestLoadUpdateDefaults(t *testing.T) {
	u, err := loadUpdate(func(string) string { return "" })
	require.NoError(t, err)
	require.True(t, u.Check)
	require.Equal(t, DefaultUpdateFeedURL, u.FeedURL)
}

func TestLoadUpdateOverrides(t *testing.T) {
	env := map[string]string{"NEBULACTRL_UPDATE_CHECK": "false", "NEBULACTRL_UPDATE_FEED_URL": " https://mirror.internal/releases/latest.json "}
	u, err := loadUpdate(func(k string) string { return env[k] })
	require.NoError(t, err)
	require.False(t, u.Check)
	require.Equal(t, "https://mirror.internal/releases/latest.json", u.FeedURL)
}

func TestLoadUpdateRejectsBadValues(t *testing.T) {
	env := map[string]string{"NEBULACTRL_UPDATE_CHECK": "maybe", "NEBULACTRL_UPDATE_FEED_URL": "ftp://x"}
	_, err := loadUpdate(func(k string) string { return env[k] })
	require.ErrorContains(t, err, "NEBULACTRL_UPDATE_CHECK")
	require.ErrorContains(t, err, "NEBULACTRL_UPDATE_FEED_URL")
}

func TestLoadUpdateRefusesAPlainHTTPFeed(t *testing.T) {
	env := map[string]string{"NEBULACTRL_UPDATE_FEED_URL": "http://mirror.internal/releases/latest.json"}
	_, err := loadUpdate(func(k string) string { return env[k] })
	require.ErrorContains(t, err, "https://")
}
