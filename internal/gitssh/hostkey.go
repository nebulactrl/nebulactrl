package gitssh

import (
	"bytes"
	"encoding/base64"
	"errors"
	"fmt"
	"net"
	"strings"
	"sync"

	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/knownhosts"
)

// HostKey is one SSH host key of a host, in the shape known_hosts stores it.
type HostKey struct {
	// Host is the known_hosts name: "host" on port 22, "[host]:port" otherwise.
	Host string `json:"host"`
	// Type is the key algorithm ("ssh-ed25519", ...).
	Type string `json:"type"`
	// Key is the base64 wire-format public key.
	Key string `json:"key"`
	// Fingerprint is the SHA256 fingerprint ("SHA256:…").
	Fingerprint string `json:"fingerprint"`
}

// NewHostKey describes pub as the key of host (a host or "host:port").
func NewHostKey(host string, pub ssh.PublicKey) HostKey {
	return HostKey{
		Host: knownhosts.Normalize(host), Type: pub.Type(),
		Key: base64.StdEncoding.EncodeToString(pub.Marshal()), Fingerprint: ssh.FingerprintSHA256(pub),
	}
}

// Authorized is the key as "type base64", the form stored in the database.
func (h HostKey) Authorized() string { return h.Type + " " + h.Key }

// Line is the known_hosts line ("host type base64").
func (h HostKey) Line() string { return h.Host + " " + h.Authorized() }

// Public decodes the key.
func (h HostKey) Public() (ssh.PublicKey, error) {
	raw, err := base64.StdEncoding.DecodeString(h.Key)
	if err != nil {
		return nil, fmt.Errorf("host key base64: %w", err)
	}
	pub, err := ssh.ParsePublicKey(raw)
	if err != nil {
		return nil, fmt.Errorf("host key: %w", err)
	}
	return pub, nil
}

// HostKeyFromAuthorized rebuilds a HostKey from the stored "type base64" form.
func HostKeyFromAuthorized(host, authorized string) (HostKey, error) {
	typ, b64, ok := strings.Cut(strings.TrimSpace(authorized), " ")
	if !ok {
		return HostKey{}, errors.New("host key: expected \"type base64\"")
	}
	h := HostKey{Host: host, Type: typ, Key: strings.TrimSpace(b64)}
	pub, err := h.Public()
	if err != nil {
		return HostKey{}, err
	}
	if pub.Type() != typ {
		return HostKey{}, fmt.Errorf("host key: type %q does not match the key (%s)", typ, pub.Type())
	}
	h.Fingerprint = ssh.FingerprintSHA256(pub)
	return h, nil
}

// ParseKnownHostsLine parses "host type base64" (one plain host, no hashing,
// markers or wildcards: the control plane writes these lines itself).
func ParseKnownHostsLine(line string) (HostKey, error) {
	f := strings.Fields(line)
	if len(f) < 3 {
		return HostKey{}, fmt.Errorf("invalid known_hosts line %q", line)
	}
	if strings.ContainsAny(f[0], ",*?!|@") {
		return HostKey{}, fmt.Errorf("unsupported known_hosts host pattern %q", f[0])
	}
	return HostKeyFromAuthorized(f[0], f[1]+" "+f[2])
}

// MismatchError is a host presenting a key other than the pinned one.
type MismatchError struct {
	Host string
	// Pinned are the keys on record for Host.
	Pinned []HostKey
	// Offered is the key the server presented; its Key is empty when the server
	// no longer offers the pinned key type at all.
	Offered HostKey
}

func (e *MismatchError) Error() string {
	pinned := make([]string, len(e.Pinned))
	for i, p := range e.Pinned {
		pinned[i] = p.Fingerprint
	}
	offered := "a key of a different type"
	if e.Offered.Fingerprint != "" {
		offered = e.Offered.Fingerprint
	}
	return fmt.Sprintf("SSH host key for %s changed: pinned %s, the server presented %s. "+
		"If the change is expected, review the new key and use \"Accept new host key\"; otherwise someone may be intercepting the connection",
		e.Host, strings.Join(pinned, ", "), offered)
}

// UnpinnedError is a host with no pinned key: the clone fails closed.
type UnpinnedError struct{ Host string }

func (e *UnpinnedError) Error() string {
	return "no SSH host key is pinned for " + e.Host + "; the first fetch from the control plane records it"
}

// Pins verifies host keys against the pinned set. It is safe for concurrent use.
type Pins struct {
	mu       sync.Mutex
	byHost   map[string][]pin
	mismatch *MismatchError
}

type pin struct {
	key HostKey
	pub ssh.PublicKey
}

// NewPins parses known_hosts lines ("host type base64").
func NewPins(lines []string) (*Pins, error) {
	p := &Pins{byHost: map[string][]pin{}}
	for _, l := range lines {
		if strings.TrimSpace(l) == "" {
			continue
		}
		k, err := ParseKnownHostsLine(l)
		if err != nil {
			return nil, err
		}
		pub, err := k.Public()
		if err != nil {
			return nil, err
		}
		k.Host = knownhosts.Normalize(k.Host)
		p.byHost[k.Host] = append(p.byHost[k.Host], pin{k, pub})
	}
	return p, nil
}

// Has reports whether host has a pinned key.
func (p *Pins) Has(host string) bool { return len(p.byHost[knownhosts.Normalize(host)]) > 0 }

// Callback returns the ssh host-key callback: a key equal to a pinned one
// passes; anything else fails, and a mismatch is remembered for Mismatch.
func (p *Pins) Callback() ssh.HostKeyCallback {
	return func(hostname string, _ net.Addr, key ssh.PublicKey) error {
		host := knownhosts.Normalize(hostname)
		pinned := p.byHost[host]
		if len(pinned) == 0 {
			return &UnpinnedError{Host: host}
		}
		for _, c := range pinned {
			if c.pub.Type() == key.Type() && bytes.Equal(c.pub.Marshal(), key.Marshal()) {
				return nil
			}
		}
		keys := make([]HostKey, len(pinned))
		for i, c := range pinned {
			keys[i] = c.key
		}
		mm := &MismatchError{Host: host, Pinned: keys, Offered: NewHostKey(host, key)}
		p.mu.Lock()
		p.mismatch = mm
		p.mu.Unlock()
		return mm
	}
}

// Mismatch returns the last mismatch the callback saw, or nil.
func (p *Pins) Mismatch() *MismatchError {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.mismatch
}

// Algorithms returns the host key algorithms to negotiate for host: those of
// the pinned key types, so a server that offers several key types is not
// mistaken for a changed one. Nil when host has no pin.
func (p *Pins) Algorithms(host string) []string {
	var out []string
	seen := map[string]bool{}
	for _, c := range p.byHost[knownhosts.Normalize(host)] {
		algs := []string{c.key.Type}
		if c.key.Type == ssh.KeyAlgoRSA {
			algs = []string{ssh.KeyAlgoRSASHA512, ssh.KeyAlgoRSASHA256}
		}
		for _, a := range algs {
			if !seen[a] {
				seen[a] = true
				out = append(out, a)
			}
		}
	}
	return out
}

func (p *Pins) pinnedKeys(host string) []HostKey {
	var out []HostKey
	for _, c := range p.byHost[knownhosts.Normalize(host)] {
		out = append(out, c.key)
	}
	return out
}
