package traffic

import (
	"net/url"
	"strings"

	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

const maxPathLen = 200

// NormalizePath replaces identifier-looking path segments (UUIDs, long hex
// strings, pure digits) with ":id" so that /users/42 and /users/43 share one
// row and ids, tokens or e-mail-like data do not end up in the database.
func NormalizePath(p string) string {
	if p == "" || p == "/" {
		return "/"
	}
	segs := strings.Split(p, "/")
	for i, s := range segs {
		if isIdentifier(s) {
			segs[i] = ":id"
		}
	}
	out := strings.Join(segs, "/")
	if len(out) > maxPathLen {
		out = out[:maxPathLen]
	}
	return out
}

func isIdentifier(s string) bool {
	if s == "" {
		return false
	}
	if len(s) == 36 && s[8] == '-' && s[13] == '-' && s[18] == '-' && s[23] == '-' {
		return isHex(s[:8]) && isHex(s[9:13]) && isHex(s[14:18]) && isHex(s[19:23]) && isHex(s[24:])
	}
	if isDigits(s) {
		return true
	}
	return len(s) >= 8 && isHex(s)
}

func isDigits(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return false
		}
	}
	return true
}

func isHex(s string) bool {
	for i := 0; i < len(s); i++ {
		c := s[i]
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f' || c >= 'A' && c <= 'F') {
			return false
		}
	}
	return true
}

// DirectReferrer labels visits without an external referrer.
const DirectReferrer = "Direct / None"

// ReferrerHost reduces a Referer header to its host. An empty or unparsable
// value is "Direct / None"; a referrer on the request's own host is internal
// navigation, not a source, and yields "".
func ReferrerHost(ref, requestHost string) string {
	if ref == "" {
		return DirectReferrer
	}
	u, err := url.Parse(ref)
	if err != nil || u.Hostname() == "" {
		return DirectReferrer
	}
	h := agentprotocol.MaskIPHost(strings.ToLower(u.Hostname()))
	if h == requestHost {
		return ""
	}
	return h
}
