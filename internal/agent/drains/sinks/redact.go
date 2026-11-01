package sinks

import (
	"encoding/base64"
	"encoding/json"
	"regexp"
	"sort"
	"strings"

	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

const (
	redactMask      = "••••••"
	redactMinSecret = 6
)

var urlRe = regexp.MustCompile("[A-Za-z][A-Za-z0-9+.\\-]*://[^\\s\"'<>`]+")

// Redact removes secret values from an error message before it is stored or
// reported: URLs are cut to scheme://host[:port] (path, query string, fragment
// and userinfo are dropped) and every secret value of at least 6 characters
// (including the values of the headers secret) is replaced by ••••••.
func Redact(msg string, secrets map[string]string) string {
	msg = urlRe.ReplaceAllStringFunc(msg, stripURL)
	for _, v := range secretValues(secrets) {
		msg = strings.ReplaceAll(msg, v, redactMask)
	}
	return msg
}

// stripURL reduces one URL to scheme://host[:port]: userinfo, query and
// fragment are dropped, and a path is replaced by a placeholder because
// webhook-style endpoints carry their secret in it.
func stripURL(s string) string {
	i := strings.Index(s, "://")
	scheme, rest := s[:i+3], s[i+3:]
	end := strings.IndexAny(rest, "/?#")
	if end < 0 {
		end = len(rest)
	}
	authority, tail := rest[:end], rest[end:]
	if at := strings.LastIndex(authority, "@"); at >= 0 {
		authority = authority[at+1:]
	}
	if q := strings.IndexAny(tail, "?#"); q >= 0 {
		tail = tail[:q]
	}
	if tail != "" && tail != "/" {
		tail = "/…"
	}
	return scheme + authority + tail
}

// secretValues lists the strings to mask, longest first.
func secretValues(secrets map[string]string) []string {
	var vals []string
	add := func(v string) {
		if len(v) >= redactMinSecret {
			vals = append(vals, v)
		}
	}
	for name, v := range secrets {
		add(v)
		if name == agentprotocol.LogDrainSecretHeaders && v != "" {
			var h map[string]string
			if json.Unmarshal([]byte(v), &h) == nil {
				for _, hv := range h {
					add(hv)
				}
			}
		}
	}
	// A server echoing the request may show the base64 basic credentials.
	if u, p := secrets[agentprotocol.LogDrainSecretUsername], secrets[agentprotocol.LogDrainSecretPassword]; u != "" || p != "" {
		add(base64.StdEncoding.EncodeToString([]byte(u + ":" + p)))
	}
	sort.Slice(vals, func(i, j int) bool {
		if len(vals[i]) != len(vals[j]) {
			return len(vals[i]) > len(vals[j])
		}
		return vals[i] < vals[j]
	})
	return vals
}
