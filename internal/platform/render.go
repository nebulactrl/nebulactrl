package platform

import (
	"fmt"
	"strings"
)

// yq quotes s as a YAML single-quoted scalar.
func yq(s string) string { return "'" + strings.ReplaceAll(s, "'", "''") + "'" }

// headscaleConfig is Headscale's configuration. The server URL is the public
// address the proxy serves it on (clients need it over HTTPS); MagicDNS uses a
// base domain under .internal, which never collides with a real name. The policy
// lives in Headscale's database (policy.mode: database): NebulaCtrl owns it and
// writes it through the API (internal/mesh), so no static policy file is mounted.
func headscaleConfig(serverURL string) string {
	return fmt.Sprintf(`server_url: %s
listen_addr: 0.0.0.0:%d
metrics_listen_addr: 127.0.0.1:9090
grpc_listen_addr: 127.0.0.1:50443
noise:
  private_key_path: /var/lib/headscale/noise_private.key
prefixes:
  v4: 100.64.0.0/10
  v6: fd7a:115c:a1e0::/48
derp:
  server:
    enabled: false
  urls:
    - https://controlplane.tailscale.com/derpmap/default
  auto_update_enabled: true
  update_frequency: 3h
disable_check_updates: true
database:
  type: sqlite
  sqlite:
    path: /var/lib/headscale/db.sqlite
    write_ahead_log: true
log:
  level: info
policy:
  mode: database
dns:
  magic_dns: true
  base_domain: tailnet.internal
  override_local_dns: false
  nameservers:
    global:
      - 1.1.1.1
      - 1.0.0.1
unix_socket: /var/run/headscale/headscale.sock
unix_socket_permission: "0770"
logtail:
  enabled: false
`, yq(strings.TrimRight(serverURL, "/")), portHeadscale)
}
