package cloudflare

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strings"
	"time"
)

// Cloudflare Tunnel (cfd_tunnel) API. Every call needs the token to carry
// "Account · Cloudflare Tunnel · Edit" in addition to the zone permissions of the
// DNS integration; a missing permission surfaces as ErrAuth.

var (
	accountIDRe = regexp.MustCompile(`^[0-9a-f]{32}$`)
	tunnelIDRe  = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)
)

// ErrNoAccount is returned when a tunnel call has no valid account ID.
var ErrNoAccount = errors.New("cloudflare: no account selected for tunnels")

// TunnelCNAME is the DNS target of a tunnel: <tunnelId>.cfargotunnel.com.
func TunnelCNAME(tunnelID string) string { return tunnelID + ".cfargotunnel.com" }

// Tunnel is a Cloudflare Tunnel.
type Tunnel struct {
	ID        string     `json:"id"`
	Name      string     `json:"name"`
	Status    string     `json:"status"` // inactive, degraded, healthy, down
	ConfigSrc string     `json:"config_src"`
	CreatedAt time.Time  `json:"created_at"`
	DeletedAt *time.Time `json:"deleted_at"`
}

// TunnelConnection is one connection of a connector to Cloudflare's edge.
type TunnelConnection struct {
	ID                 string `json:"id"`
	ColoName           string `json:"colo_name"`
	OpenedAt           string `json:"opened_at"`
	IsPendingReconnect bool   `json:"is_pending_reconnect"`
}

// TunnelClient is a connector (cloudflared instance) and its connections.
type TunnelClient struct {
	ID      string             `json:"id"`
	Version string             `json:"version"`
	Conns   []TunnelConnection `json:"conns"`
}

// OriginRequest is the per-rule connection setting of an ingress rule. Only the
// fields NebulaCtrl sets are modelled.
type OriginRequest struct {
	// HTTPHostHeader sets the Host header sent to the origin.
	HTTPHostHeader string `json:"httpHostHeader,omitempty"`
}

// IngressRule is one ingress rule of a remotely-managed tunnel. The last rule
// of a configuration must have neither hostname nor path.
type IngressRule struct {
	Hostname      string         `json:"hostname,omitempty"`
	Path          string         `json:"path,omitempty"`
	Service       string         `json:"service"`
	OriginRequest *OriginRequest `json:"originRequest,omitempty"`
}

// CatchAllService answers every hostname that matches no other rule.
const CatchAllService = "http_status:404"

// IngressForHosts renders the ingress rules of a tunnel that forwards every
// given hostname to upstream and answers anything else with 404. The hosts are
// sorted and de-duplicated; the Host header of a rule is the hostname itself (a
// wildcard hostname keeps the visitor's Host header, which is the match).
func IngressForHosts(hosts []string, upstream string) []IngressRule {
	seen := make(map[string]bool, len(hosts))
	uniq := make([]string, 0, len(hosts))
	for _, h := range hosts {
		h = strings.ToLower(strings.TrimSpace(h))
		if h == "" || seen[h] {
			continue
		}
		seen[h] = true
		uniq = append(uniq, h)
	}
	sort.Strings(uniq)
	rules := make([]IngressRule, 0, len(uniq)+1)
	for _, h := range uniq {
		r := IngressRule{Hostname: h, Service: upstream}
		if !strings.Contains(h, "*") {
			r.OriginRequest = &OriginRequest{HTTPHostHeader: h}
		}
		rules = append(rules, r)
	}
	return append(rules, IngressRule{Service: CatchAllService})
}

// IngressHash fingerprints ingress rules, to skip a push of an unchanged config.
func IngressHash(rules []IngressRule) string {
	var b strings.Builder
	for _, r := range rules {
		hh := ""
		if r.OriginRequest != nil {
			hh = r.OriginRequest.HTTPHostHeader
		}
		b.WriteString(r.Hostname + "\x00" + r.Path + "\x00" + r.Service + "\x00" + hh + "\n")
	}
	sum := sha256.Sum256([]byte(b.String()))
	return hex.EncodeToString(sum[:8])
}

func tunnelPath(accountID, tunnelID string, rest ...string) (string, error) {
	if !accountIDRe.MatchString(accountID) {
		return "", ErrNoAccount
	}
	p := "/accounts/" + url.PathEscape(accountID) + "/cfd_tunnel"
	if tunnelID != "" {
		if !tunnelIDRe.MatchString(tunnelID) {
			return "", fmt.Errorf("cloudflare: invalid tunnel id %q", tunnelID)
		}
		p += "/" + tunnelID
	}
	for _, r := range rest {
		p += "/" + r
	}
	return p, nil
}

// CreateTunnel creates a remotely-managed tunnel (its ingress rules live at
// Cloudflare and are set with PutTunnelConfig).
func (c *Client) CreateTunnel(ctx context.Context, accountID, name string) (Tunnel, error) {
	p, err := tunnelPath(accountID, "")
	if err != nil {
		return Tunnel{}, err
	}
	var t Tunnel
	body := map[string]string{"name": name, "config_src": "cloudflare"}
	if err := c.do(ctx, http.MethodPost, p, nil, body, &t); err != nil {
		return Tunnel{}, err
	}
	if !tunnelIDRe.MatchString(t.ID) {
		return Tunnel{}, fmt.Errorf("cloudflare: create tunnel returned an invalid id %q", t.ID)
	}
	return t, nil
}

// GetTunnel returns a tunnel (ErrNotFound when it is gone).
func (c *Client) GetTunnel(ctx context.Context, accountID, tunnelID string) (Tunnel, error) {
	p, err := tunnelPath(accountID, tunnelID)
	if err != nil {
		return Tunnel{}, err
	}
	var t Tunnel
	if err := c.do(ctx, http.MethodGet, p, nil, nil, &t); err != nil {
		return Tunnel{}, err
	}
	return t, nil
}

// PutTunnelConfig replaces the ingress configuration of a remotely-managed
// tunnel.
func (c *Client) PutTunnelConfig(ctx context.Context, accountID, tunnelID string, ingress []IngressRule) error {
	p, err := tunnelPath(accountID, tunnelID, "configurations")
	if err != nil {
		return err
	}
	if len(ingress) == 0 || ingress[len(ingress)-1].Hostname != "" || ingress[len(ingress)-1].Path != "" {
		return errors.New("cloudflare: the last ingress rule must be a catch-all")
	}
	body := map[string]any{"config": map[string]any{"ingress": ingress}}
	return c.do(ctx, http.MethodPut, p, nil, body, nil)
}

// TunnelToken returns the connector token of a tunnel (what cloudflared runs
// with). It is a credential: never log or persist it.
func (c *Client) TunnelToken(ctx context.Context, accountID, tunnelID string) (string, error) {
	p, err := tunnelPath(accountID, tunnelID, "token")
	if err != nil {
		return "", err
	}
	var tok string
	if err := c.do(ctx, http.MethodGet, p, nil, nil, &tok); err != nil {
		return "", err
	}
	if tok == "" {
		return "", errors.New("cloudflare: the API returned an empty tunnel token")
	}
	return tok, nil
}

// TunnelConnections lists the connectors of a tunnel and their connections.
func (c *Client) TunnelConnections(ctx context.Context, accountID, tunnelID string) ([]TunnelClient, error) {
	p, err := tunnelPath(accountID, tunnelID, "connections")
	if err != nil {
		return nil, err
	}
	var out []TunnelClient
	if err := c.do(ctx, http.MethodGet, p, nil, nil, &out); err != nil {
		return nil, err
	}
	return out, nil
}

// DeleteTunnel deletes a tunnel. Lingering connections (Cloudflare keeps
// tracking a connector for a few minutes after it stops) are cleaned up first,
// because a tunnel with connections cannot be deleted. A tunnel that is already
// gone is not an error.
func (c *Client) DeleteTunnel(ctx context.Context, accountID, tunnelID string) error {
	conns, err := tunnelPath(accountID, tunnelID, "connections")
	if err != nil {
		return err
	}
	if err := c.do(ctx, http.MethodDelete, conns, nil, nil, nil); err != nil {
		if errors.Is(err, ErrNotFound) {
			return nil
		}
		return err
	}
	p, _ := tunnelPath(accountID, tunnelID)
	if err := c.do(ctx, http.MethodDelete, p, nil, nil, nil); err != nil && !errors.Is(err, ErrNotFound) {
		return err
	}
	return nil
}
