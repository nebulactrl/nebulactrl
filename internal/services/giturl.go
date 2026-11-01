package services

import (
	"net/url"
	"regexp"
	"strings"
)

// gitURLCredentialsHint is the message of a git URL that embeds credentials.
const gitURLCredentialsHint = "gitUrl must not contain credentials (user or token before the host): connect the repository with a git connection, or put the access token in the token field"

// gitURLUserinfoRe matches the userinfo of a URL inside free text.
var gitURLUserinfoRe = regexp.MustCompile(`(?i)\b([a-z][a-z0-9+.-]*://)[^/\s@]+@`)

// RedactGitURL removes the userinfo (user, password, token) from every
// http(s):// and git:// URL in text: a clone URL, a log line or an error
// message. The login of an SSH URL (git@host) is not a secret and stays.
func RedactGitURL(text string) string {
	return gitURLUserinfoRe.ReplaceAllStringFunc(text, func(m string) string {
		if i := strings.Index(m, "://"); i >= 0 && strings.EqualFold(m[:i], "ssh") {
			return m
		}
		return m[:strings.Index(m, "://")+3]
	})
}

// gitURLHasCredentials reports whether an http(s):// or git:// clone URL
// carries userinfo. SSH URLs name the SSH login and are checked by
// gitssh.ParseURL (no password).
func gitURLHasCredentials(raw string) bool {
	u, err := url.Parse(raw)
	if err != nil {
		return false
	}
	switch strings.ToLower(u.Scheme) {
	case "http", "https", "git":
		return u.User != nil
	}
	return false
}

// SameGitRepo reports whether two clone URLs name the same repository on the
// same host: scheme, userinfo, a trailing ".git" and letter case do not
// matter. An URL that cannot be parsed matches nothing.
func SameGitRepo(a, b string) bool {
	ha, pa, ok := gitRepoKey(a)
	if !ok {
		return false
	}
	hb, pb, ok := gitRepoKey(b)
	return ok && ha == hb && pa == pb
}

// gitRepoKey is the lower-cased host[:port] and path of a clone URL without
// ".git" and surrounding slashes.
func gitRepoKey(raw string) (host, path string, ok bool) {
	s := strings.TrimSpace(raw)
	if s == "" {
		return "", "", false
	}
	if strings.Contains(s, "://") {
		u, err := url.Parse(s)
		if err != nil || u.Hostname() == "" {
			return "", "", false
		}
		host, path = strings.ToLower(u.Hostname()), u.Path
		if p := u.Port(); p != "" && !isDefaultGitPort(u.Scheme, p) {
			host += ":" + p
		}
	} else { // scp-like: [user@]host:path
		hp, rest, found := strings.Cut(s, ":")
		if !found {
			return "", "", false
		}
		if i := strings.LastIndex(hp, "@"); i >= 0 {
			hp = hp[i+1:]
		}
		host, path = strings.ToLower(hp), rest
	}
	path = strings.TrimSuffix(strings.Trim(path, "/"), ".git")
	path = strings.ToLower(strings.Trim(path, "/"))
	if host == "" || path == "" {
		return "", "", false
	}
	return host, path, true
}

func isDefaultGitPort(scheme, port string) bool {
	switch strings.ToLower(scheme) {
	case "https":
		return port == "443"
	case "http":
		return port == "80"
	case "ssh":
		return port == "22"
	}
	return false
}
