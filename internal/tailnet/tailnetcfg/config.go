// Package tailnetcfg is the desired per-service tailnet configuration stored in
// services.config. It is a leaf package (standard library only) so the services
// package can embed it without importing the tailnet domain.
package tailnetcfg

import (
	"cmp"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"regexp"
	"slices"
	"strings"
)

// Limits.
const (
	MaxTCPForwards = 16
	MaxExtraTags   = 10
	maxLabel       = 63
)

var (
	labelRe = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]*[a-z0-9])?$`)
	tagRe   = regexp.MustCompile(`^tag:[a-zA-Z][a-zA-Z0-9-]{0,62}$`)
)

// TCPForward exposes a tailnet port that forwards to a port of the service.
type TCPForward struct {
	// Listen is the tailnet port (1-65535).
	Listen int `json:"listen"`
	// Target is the service port; 0 means the same as Listen.
	Target int `json:"target,omitempty"`
	// TLS terminates TLS with the node certificate (Tailscale with HTTPS
	// certificates enabled only).
	TLS bool `json:"tls,omitempty"`
}

// TailnetConfig is the desired tailnet node of a service. Nil (or Enabled=false after
// normalisation, which collapses to nil) means the service is not on a tailnet.
// (Named TailnetConfig, not Config: huma names schemas by bare type name.)
type TailnetConfig struct {
	Enabled bool `json:"enabled"`
	// Hostname is the machine name; empty derives <service>-<environment>.
	Hostname string `json:"hostname,omitempty"`
	// ExtraTags are added to the integration's default tags and must be in the
	// integration's allow-list (checked by the tailnet service).
	ExtraTags []string `json:"extraTags,omitempty"`
	// Web serves the service's HTTP port on the tailnet: HTTPS on 443 with
	// Tailscale, plain HTTP on 80 with Headscale (no Serve HTTPS there).
	Web bool `json:"web,omitempty"`
	// TCP are extra raw TCP ports (databases, game servers).
	TCP []TCPForward `json:"tcp,omitempty"`
	// Funnel publishes the web exposure on the public internet through
	// Tailscale's relays. Tailscale only; needs Web.
	Funnel bool `json:"funnel,omitempty"`
	// HidePublic ("Tailnet only") removes the service's public domain routers.
	HidePublic bool `json:"hidePublic,omitempty"`
}

// Normalize returns the canonical form: trimmed, lowercased, sorted and
// deduplicated. A disabled config collapses to nil.
func Normalize(c *TailnetConfig) *TailnetConfig {
	if c == nil || !c.Enabled {
		return nil
	}
	n := *c
	n.Hostname = strings.ToLower(strings.TrimSpace(n.Hostname))
	tags := make([]string, 0, len(n.ExtraTags))
	for _, t := range n.ExtraTags {
		if t = strings.TrimSpace(t); t != "" {
			tags = append(tags, t)
		}
	}
	slices.Sort(tags)
	n.ExtraTags = slices.Compact(tags)
	if len(n.ExtraTags) == 0 {
		n.ExtraTags = nil
	}
	fw := make([]TCPForward, 0, len(n.TCP))
	for _, f := range n.TCP {
		if f.Target == 0 {
			f.Target = f.Listen
		}
		fw = append(fw, f)
	}
	slices.SortFunc(fw, func(a, b TCPForward) int { return cmp.Compare(a.Listen, b.Listen) })
	n.TCP = fw
	if len(n.TCP) == 0 {
		n.TCP = nil
	}
	return &n
}

// Validate checks a normalised, enabled config. servicePort is the service's
// HTTP port (0 = none).
func (c TailnetConfig) Validate(servicePort int) error {
	if c.Hostname != "" {
		if err := ValidHostname(c.Hostname); err != nil {
			return err
		}
	}
	if len(c.ExtraTags) > MaxExtraTags {
		return fmt.Errorf("at most %d extra tags", MaxExtraTags)
	}
	for _, t := range c.ExtraTags {
		if !tagRe.MatchString(t) {
			return fmt.Errorf("%q is not a valid tag (use tag:name with letters, digits and hyphens)", t)
		}
	}
	if !c.Web && len(c.TCP) == 0 {
		return fmt.Errorf("enable the web exposure or add at least one TCP port")
	}
	if c.Web && servicePort <= 0 {
		return fmt.Errorf("the web exposure needs the service to have an HTTP port")
	}
	if c.Funnel && !c.Web {
		return fmt.Errorf("funnel publishes the web exposure; enable web first")
	}
	if len(c.TCP) > MaxTCPForwards {
		return fmt.Errorf("at most %d TCP ports", MaxTCPForwards)
	}
	seen := map[int]bool{}
	for _, f := range c.TCP {
		if f.Listen < 1 || f.Listen > 65535 {
			return fmt.Errorf("tcp listen port %d is out of range", f.Listen)
		}
		if f.Target < 1 || f.Target > 65535 {
			return fmt.Errorf("tcp target port %d is out of range", f.Target)
		}
		if seen[f.Listen] {
			return fmt.Errorf("tcp listen port %d is used twice", f.Listen)
		}
		seen[f.Listen] = true
	}
	if c.Web && (seen[443] || seen[80]) {
		return fmt.Errorf("tcp ports 80 and 443 are used by the web exposure")
	}
	return nil
}

// ValidHostname checks an RFC 1123 label (lowercase, at most 63 characters).
func ValidHostname(h string) error {
	if len(h) == 0 || len(h) > maxLabel || !labelRe.MatchString(h) {
		return fmt.Errorf("hostname %q must be a lowercase RFC 1123 label (letters, digits, hyphens; at most %d characters)", h, maxLabel)
	}
	return nil
}

// DefaultHostname derives <service>-<environment>, sanitised to one label.
func DefaultHostname(serviceSlug, envSlug string) string {
	h := sanitizeLabel(serviceSlug)
	if e := sanitizeLabel(envSlug); e != "" && e != h {
		if h == "" {
			h = e
		} else {
			h += "-" + e
		}
	}
	if h == "" {
		h = "service"
	}
	if len(h) > maxLabel {
		h = strings.TrimRight(h[:maxLabel], "-")
	}
	return h
}

func sanitizeLabel(s string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(s) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			b.WriteRune(r)
		case b.Len() > 0:
			b.WriteByte('-')
		}
	}
	return strings.Trim(b.String(), "-")
}

// EffectiveHostname is the configured hostname or the derived default.
func (c TailnetConfig) EffectiveHostname(serviceSlug, envSlug string) string {
	if c.Hostname != "" {
		return c.Hostname
	}
	return DefaultHostname(serviceSlug, envSlug)
}

// Tags returns the sorted union of the integration's default tags and the
// service's extra tags.
func (c TailnetConfig) Tags(defaults []string) []string {
	out := append(slices.Clone(defaults), c.ExtraTags...)
	slices.Sort(out)
	return slices.Compact(out)
}

// IdentityHash fingerprints what makes the node's identity on the tailnet: when
// it changes the device must be re-created (the hostname and tags are fixed at
// login). Exposures are not part of it.
func IdentityHash(provider, loginServer, hostname string, tags []string) string {
	t := slices.Clone(tags)
	slices.Sort(t)
	sum := sha256.Sum256([]byte(strings.Join([]string{provider, loginServer, hostname, strings.Join(t, ",")}, "\x00")))
	return hex.EncodeToString(sum[:8])
}
