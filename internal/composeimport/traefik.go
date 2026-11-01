package composeimport

import (
	"regexp"
	"sort"
	"strconv"
	"strings"
)

var (
	traefikHostRe   = regexp.MustCompile(`Host\(([^)]*)\)`)
	traefikQuotedRe = regexp.MustCompile("[`\"']([^`\"']+)[`\"']")
	hostnameRe      = regexp.MustCompile(`^(?:[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?)(?:\.[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?)+$`)
)

// traefikInfo is what the Traefik labels of a service say.
type traefikInfo struct {
	Enabled bool
	Hosts   []string
	Port    int
}

// parseTraefikLabels reads Host(`a.example.com`) rules and the load balancer
// port. Everything else in the labels is ignored.
func parseTraefikLabels(labels map[string]string) traefikInfo {
	t := traefikInfo{Enabled: true}
	if v, ok := labels["traefik.enable"]; ok && strings.EqualFold(strings.TrimSpace(v), "false") {
		t.Enabled = false
		return t
	}
	keys := make([]string, 0, len(labels))
	for k := range labels {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	seen := map[string]bool{}
	for _, k := range keys {
		v := labels[k]
		switch {
		case strings.HasPrefix(k, "traefik.http.routers.") && strings.HasSuffix(k, ".rule"):
			for _, m := range traefikHostRe.FindAllStringSubmatch(v, -1) {
				for _, q := range traefikQuotedRe.FindAllStringSubmatch(m[1], -1) {
					h := strings.ToLower(strings.TrimSpace(q[1]))
					if hostnameRe.MatchString(h) && !seen[h] {
						seen[h] = true
						t.Hosts = append(t.Hosts, h)
					}
				}
			}
		case strings.HasPrefix(k, "traefik.http.services.") && strings.HasSuffix(k, ".loadbalancer.server.port"):
			if n, err := strconv.Atoi(strings.TrimSpace(v)); err == nil && n > 0 && n < 65536 && t.Port == 0 {
				t.Port = n
			}
		}
	}
	return t
}
