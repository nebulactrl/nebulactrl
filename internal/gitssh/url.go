package gitssh

import (
	"errors"
	"fmt"
	"net"
	"net/url"
	"regexp"
	"strconv"
	"strings"

	"golang.org/x/crypto/ssh/knownhosts"
)

// DefaultUser is the SSH user of a git URL that names none.
const DefaultUser = "git"

// Endpoint is a parsed SSH git URL.
type Endpoint struct {
	// User is the SSH login (default "git").
	User string
	// Host is the host name or IP, without brackets.
	Host string
	// Port is the TCP port (default 22).
	Port int
	// Path is the repository path as written (leading "/" kept for ssh:// URLs).
	Path string
}

var (
	scpRe  = regexp.MustCompile(`^(?:([A-Za-z0-9._~-]+)@)?(\[[0-9A-Fa-f:.]+\]|[^\s:/@\[\]]+):(.+)$`)
	hostRe = regexp.MustCompile(`^[A-Za-z0-9]([A-Za-z0-9._-]*[A-Za-z0-9])?$`)
	userRe = regexp.MustCompile(`^[A-Za-z0-9._~-]+$`)
)

// LooksLikeSSH reports whether raw is written as an SSH URL (ssh://… or
// [user@]host:path) whether or not it is valid; ParseURL says which.
func LooksLikeSSH(raw string) bool {
	raw = strings.TrimSpace(raw)
	if strings.HasPrefix(raw, "ssh://") {
		return true
	}
	return !strings.Contains(raw, "://") && scpRe.MatchString(raw)
}

// IsSSHURL reports whether raw is a valid SSH git URL.
func IsSSHURL(raw string) bool {
	_, err := ParseURL(raw)
	return err == nil
}

// ParseURL parses `ssh://[user@]host[:port]/path` and the scp-like form
// `[user@]host:path` (git@github.com:org/repo.git). Anything else, including
// URLs that carry a password, is an error.
func ParseURL(raw string) (Endpoint, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return Endpoint{}, errors.New("empty URL")
	}
	var ep Endpoint
	switch {
	case strings.Contains(raw, "://"):
		u, err := url.Parse(raw)
		if err != nil {
			return Endpoint{}, fmt.Errorf("invalid URL: %w", err)
		}
		if u.Scheme != "ssh" {
			return Endpoint{}, fmt.Errorf("not an ssh:// URL (scheme %q)", u.Scheme)
		}
		if u.User != nil {
			if _, hasPw := u.User.Password(); hasPw {
				return Endpoint{}, errors.New("passwords in an SSH URL are not supported; use the deploy key")
			}
			ep.User = u.User.Username()
		}
		ep.Host, ep.Path = u.Hostname(), u.Path
		if ps := u.Port(); ps != "" {
			p, err := strconv.Atoi(ps)
			if err != nil || p < 1 || p > 65535 {
				return Endpoint{}, fmt.Errorf("invalid port %q", ps)
			}
			ep.Port = p
		}
	default:
		m := scpRe.FindStringSubmatch(raw)
		if m == nil {
			return Endpoint{}, errors.New("not an SSH URL (expected ssh://user@host/org/repo.git or git@host:org/repo.git)")
		}
		ep.User, ep.Host, ep.Path = m[1], strings.Trim(m[2], "[]"), m[3]
	}
	if ep.User == "" {
		ep.User = DefaultUser
	}
	if !userRe.MatchString(ep.User) {
		return Endpoint{}, fmt.Errorf("invalid SSH user %q", ep.User)
	}
	if ep.Port == 0 {
		ep.Port = 22
	}
	if ep.Host == "" || len(ep.Host) > 253 || !(hostRe.MatchString(ep.Host) || net.ParseIP(ep.Host) != nil) {
		return Endpoint{}, fmt.Errorf("invalid host %q", ep.Host)
	}
	if strings.Trim(ep.Path, "/") == "" {
		return Endpoint{}, errors.New("the URL has no repository path")
	}
	if strings.ContainsAny(ep.Path, " \t\r\n") || strings.Contains(ep.Path, "..") {
		return Endpoint{}, errors.New("invalid repository path")
	}
	return ep, nil
}

// Addr is "host:port" as dialed.
func (e Endpoint) Addr() string { return net.JoinHostPort(e.Host, strconv.Itoa(e.Port)) }

// HostName is how known_hosts names the host: "host" on port 22,
// "[host]:port" otherwise.
func (e Endpoint) HostName() string { return knownhosts.Normalize(e.Addr()) }

// Repo returns the repository as "owner/name" (any depth) without ".git".
func (e Endpoint) Repo() string {
	p := strings.Trim(e.Path, "/")
	return strings.TrimSuffix(p, ".git")
}
