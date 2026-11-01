package gitssh

import (
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/go-git/go-git/v5/plumbing/transport"
	gitssh "github.com/go-git/go-git/v5/plumbing/transport/ssh"
	"golang.org/x/crypto/ssh"
)

// ErrNoKey is returned when a service clones over SSH but has no deploy key.
var ErrNoKey = errors.New("this service clones over SSH but has no deploy key; add one in the service's source settings")

// connectTimeout bounds the TCP connect and SSH handshake of a clone.
const connectTimeout = 20 * time.Second

// Credentials are what an SSH clone needs: the deploy key and the pinned host
// keys. The private key is a secret and is never logged.
type Credentials struct {
	PrivateKeyPEM []byte
	// KnownHosts are the pinned host keys as known_hosts lines.
	KnownHosts []string
}

// authMethod is go-git's public-key auth with a connect timeout (go-git sets
// none) and the pinned host-key callback. It never falls back to ssh-agent or
// ~/.ssh/known_hosts.
type authMethod struct{ *gitssh.PublicKeys }

func (a authMethod) ClientConfig() (*ssh.ClientConfig, error) {
	cfg, err := a.PublicKeys.ClientConfig()
	if err != nil {
		return nil, err
	}
	cfg.Timeout = connectTimeout
	return cfg, nil
}

// Auth returns the go-git auth method for ep and the Pins it enforces. A host
// without a pinned key fails the handshake (UnpinnedError).
func (c Credentials) Auth(ep Endpoint) (transport.AuthMethod, *Pins, error) {
	if len(c.PrivateKeyPEM) == 0 {
		return nil, nil, errors.New("an SSH URL needs a deploy key")
	}
	pins, err := NewPins(c.KnownHosts)
	if err != nil {
		return nil, nil, err
	}
	pk, err := gitssh.NewPublicKeys(ep.User, c.PrivateKeyPEM, "")
	if err != nil {
		return nil, nil, fmt.Errorf("load deploy key: %w", err)
	}
	pk.HostKeyCallback = pins.Callback()
	pk.HostKeyAlgorithms = pins.Algorithms(ep.Addr())
	return authMethod{pk}, pins, nil
}

// Explain turns a transport error from a clone or ls-remote made with Auth into
// one a user can act on: a host-key mismatch becomes *MismatchError, a rejected
// key says so. Other errors are returned unchanged.
func Explain(err error, ep Endpoint, pins *Pins) error {
	if err == nil {
		return nil
	}
	var mm *MismatchError
	if errors.As(err, &mm) {
		return mm
	}
	if pins != nil {
		if m := pins.Mismatch(); m != nil {
			return m
		}
		if strings.Contains(err.Error(), "no common algorithm for host key") && pins.Has(ep.Addr()) {
			return &MismatchError{Host: ep.HostName(), Offered: HostKey{}, Pinned: pins.pinnedKeys(ep.Addr())}
		}
	}
	var un *UnpinnedError
	if errors.As(err, &un) {
		return un
	}
	msg := err.Error()
	switch {
	case strings.Contains(msg, "unable to authenticate"):
		return fmt.Errorf("SSH authentication to %s failed: the deploy key was rejected; add the public key as a deploy key on the repository (%w)", ep.Addr(), err)
	}
	return err
}
