package config

import (
	"bytes"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func b64(b byte) string { return base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{b}, 32)) }

func env(m map[string]string) func(string) string {
	m["NEBULACTRL_DATABASE_URL"] = "postgres://x"
	return func(k string) string { return m[k] }
}

func TestSecretKeyFromEnvAndFile(t *testing.T) {
	files := map[string]string{"/run/secrets/key": b64(1) + "\n", "/run/secrets/prev": b64(2) + "\n" + b64(3) + "\n"}
	defer func(old func(string) ([]byte, error)) { readFile = old }(readFile)
	readFile = func(p string) ([]byte, error) {
		s, ok := files[p]
		if !ok {
			return nil, errors.New("no such file")
		}
		return []byte(s), nil
	}

	c, err := Load(env(map[string]string{"NEBULACTRL_SECRET_KEY": b64(1)}))
	require.NoError(t, err)
	require.Equal(t, bytes.Repeat([]byte{1}, 32), c.SecretKey)
	require.Empty(t, c.PreviousSecretKeys)

	// The key file (a Docker or systemd secret) carries a trailing newline.
	c, err = Load(env(map[string]string{"NEBULACTRL_SECRET_KEY_FILE": "/run/secrets/key", "NEBULACTRL_SECRET_KEY_PREVIOUS_FILE": "/run/secrets/prev"}))
	require.NoError(t, err)
	require.Equal(t, bytes.Repeat([]byte{1}, 32), c.SecretKey)
	require.Equal(t, [][]byte{bytes.Repeat([]byte{2}, 32), bytes.Repeat([]byte{3}, 32)}, c.PreviousSecretKeys)

	// Previous keys from the environment: a comma separated list.
	c, err = Load(env(map[string]string{"NEBULACTRL_SECRET_KEY": b64(1), "NEBULACTRL_SECRET_KEY_PREVIOUS": b64(2) + "," + b64(3)}))
	require.NoError(t, err)
	require.Len(t, c.PreviousSecretKeys, 2)
}

func TestSecretKeyErrors(t *testing.T) {
	defer func(old func(string) ([]byte, error)) { readFile = old }(readFile)
	readFile = func(string) ([]byte, error) { return nil, errors.New("permission denied") }

	_, err := Load(env(map[string]string{}))
	require.ErrorContains(t, err, "NEBULACTRL_SECRET_KEY")
	_, err = Load(env(map[string]string{"NEBULACTRL_SECRET_KEY": b64(1), "NEBULACTRL_SECRET_KEY_FILE": "/k"}))
	require.ErrorContains(t, err, "both set")
	_, err = Load(env(map[string]string{"NEBULACTRL_SECRET_KEY_FILE": "/k"}))
	require.ErrorContains(t, err, "permission denied")
	_, err = Load(env(map[string]string{"NEBULACTRL_SECRET_KEY": "short"}))
	require.ErrorContains(t, err, "NEBULACTRL_SECRET_KEY")
	_, err = Load(env(map[string]string{"NEBULACTRL_SECRET_KEY": b64(1), "NEBULACTRL_SECRET_KEY_PREVIOUS": b64(2) + ",nope!"}))
	require.ErrorContains(t, err, "NEBULACTRL_SECRET_KEY_PREVIOUS")
	// An empty key file is not a key.
	readFile = func(string) ([]byte, error) { return []byte("\n"), nil }
	_, err = Load(env(map[string]string{"NEBULACTRL_SECRET_KEY_FILE": "/k"}))
	require.ErrorContains(t, err, "NEBULACTRL_SECRET_KEY")
}

func TestDashboardAccessFromEnv(t *testing.T) {
	defer func(old func(string) ([]byte, error)) { readFile = old }(readFile)
	readFile = func(p string) ([]byte, error) {
		if p == "/run/secrets/cf" {
			return []byte("file-token\n"), nil
		}
		return nil, errors.New("no such file")
	}
	base := func(extra map[string]string) func(string) string {
		extra["NEBULACTRL_SECRET_KEY"] = b64(1)
		return env(extra)
	}

	c, err := Load(base(map[string]string{}))
	require.NoError(t, err)
	require.Empty(t, c.Installer.DashboardAllowCIDRs)
	require.Empty(t, c.Installer.DashboardDNSResolver)

	c, err = Load(base(map[string]string{
		"NEBULACTRL_DASHBOARD_ALLOW_CIDRS":  " 100.64.0.0/10, fd7a:115c:a1e0::/48 ,203.0.113.7,,100.64.0.0/10 ",
		"NEBULACTRL_DASHBOARD_DNS_RESOLVER": "Cloudflare", "NEBULACTRL_DASHBOARD_DNS_TOKEN": "tok",
	}))
	require.NoError(t, err)
	require.Equal(t, []string{"100.64.0.0/10", "fd7a:115c:a1e0::/48", "203.0.113.7"}, c.Installer.DashboardAllowCIDRs)
	require.Equal(t, "cloudflare", c.Installer.DashboardDNSResolver)
	require.Equal(t, "tok", c.Installer.DashboardDNSToken)

	// The token may come from a file (Docker or systemd secret).
	c, err = Load(base(map[string]string{"NEBULACTRL_DASHBOARD_DNS_RESOLVER": "cloudflare", "NEBULACTRL_DASHBOARD_DNS_TOKEN_FILE": "/run/secrets/cf"}))
	require.NoError(t, err)
	require.Equal(t, "file-token", c.Installer.DashboardDNSToken)
}

func TestDashboardAccessEnvErrorsFailLoad(t *testing.T) {
	base := func(extra map[string]string) func(string) string {
		extra["NEBULACTRL_SECRET_KEY"] = b64(1)
		return env(extra)
	}
	for name, tc := range map[string]struct {
		env  map[string]string
		want string
	}{
		"bad cidr":          {map[string]string{"NEBULACTRL_DASHBOARD_ALLOW_CIDRS": "100.64.0.0/10,not-a-cidr"}, "NEBULACTRL_DASHBOARD_ALLOW_CIDRS"},
		"bad prefix len":    {map[string]string{"NEBULACTRL_DASHBOARD_ALLOW_CIDRS": "10.0.0.0/33"}, "NEBULACTRL_DASHBOARD_ALLOW_CIDRS"},
		"unknown provider":  {map[string]string{"NEBULACTRL_DASHBOARD_DNS_RESOLVER": "route53", "NEBULACTRL_DASHBOARD_DNS_TOKEN": "x"}, "not supported"},
		"resolver no token": {map[string]string{"NEBULACTRL_DASHBOARD_DNS_RESOLVER": "cloudflare"}, "NEBULACTRL_DASHBOARD_DNS_TOKEN"},
		"token no resolver": {map[string]string{"NEBULACTRL_DASHBOARD_DNS_TOKEN": "secret-canary"}, "without NEBULACTRL_DASHBOARD_DNS_RESOLVER"},
		"token and file":    {map[string]string{"NEBULACTRL_DASHBOARD_DNS_RESOLVER": "cloudflare", "NEBULACTRL_DASHBOARD_DNS_TOKEN": "x", "NEBULACTRL_DASHBOARD_DNS_TOKEN_FILE": "/f"}, "both set"},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := Load(base(tc.env))
			require.ErrorContains(t, err, tc.want)
			require.NotContains(t, err.Error(), "secret-canary", "the token never appears in an error")
		})
	}
	// More than 200 entries are refused like a route's access list.
	var many []string
	for i := range 201 {
		many = append(many, fmt.Sprintf("10.%d.%d.0/24", i/250, i%250))
	}
	_, err := Load(base(map[string]string{"NEBULACTRL_DASHBOARD_ALLOW_CIDRS": strings.Join(many, ",")}))
	require.ErrorContains(t, err, "at most 200")
}

func TestStatusListenerAndUpstreamDefaultsAndParsing(t *testing.T) {
	c, err := Load(env(map[string]string{"NEBULACTRL_SECRET_KEY": b64(1)}))
	require.NoError(t, err)
	require.Equal(t, ":8081", c.StatusListen)
	require.Equal(t, "nebulactrl:8081", c.Installer.StatusUpstream)
	require.NotEqual(t, c.Listen, c.StatusListen, "the status listener is never the dashboard's")

	c, err = Load(env(map[string]string{
		"NEBULACTRL_SECRET_KEY":      b64(1),
		"NEBULACTRL_STATUS_LISTEN":   " 127.0.0.1:9091 ",
		"NEBULACTRL_STATUS_UPSTREAM": " http://status-edge:9091/ ",
	}))
	require.NoError(t, err)
	require.Equal(t, "127.0.0.1:9091", c.StatusListen)
	require.Equal(t, "status-edge:9091", c.Installer.StatusUpstream, "the scheme and trailing slash are dropped")

	for _, bad := range []string{"nebulactrl", ":8081", "nebulactrl:"} {
		_, err := Load(env(map[string]string{"NEBULACTRL_SECRET_KEY": b64(1), "NEBULACTRL_STATUS_UPSTREAM": bad}))
		require.ErrorContainsf(t, err, "NEBULACTRL_STATUS_UPSTREAM", "%q", bad)
	}
}

func TestTimersDefaultToZeroAndParse(t *testing.T) {
	c, err := Load(env(map[string]string{"NEBULACTRL_SECRET_KEY": b64(1)}))
	require.NoError(t, err)
	require.Equal(t, Timers{}, c.Timers, "unset means the built-in default of the owning package")

	c, err = Load(env(map[string]string{
		"NEBULACTRL_SECRET_KEY":           b64(1),
		"NEBULACTRL_MESH_LINK_GRACE":      "30s",
		"NEBULACTRL_REPLICA_BROKEN_AFTER": " 1m30s ",
		"NEBULACTRL_UPTIME_INTERVAL":      "2s",
	}))
	require.NoError(t, err)
	require.Equal(t, 30*time.Second, c.Timers.MeshLinkGrace)
	require.Equal(t, 90*time.Second, c.Timers.ReplicaBrokenAfter)
	require.Equal(t, 2*time.Second, c.Timers.UptimeInterval)
}

func TestTimersRejectBadValues(t *testing.T) {
	for _, bad := range []string{"10", "ten minutes", "0s", "-5m", "500ms"} {
		_, err := Load(env(map[string]string{"NEBULACTRL_SECRET_KEY": b64(1), "NEBULACTRL_MESH_LINK_GRACE": bad}))
		require.ErrorContainsf(t, err, "NEBULACTRL_MESH_LINK_GRACE", "%q", bad)
	}
}
