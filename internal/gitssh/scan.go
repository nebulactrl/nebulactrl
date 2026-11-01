package gitssh

import (
	"context"
	"errors"
	"fmt"
	"net"
	"time"

	"golang.org/x/crypto/ssh"

	"github.com/nebulactrl/nebulactrl/internal/netguard"
)

// scanTimeout bounds a host key scan.
const scanTimeout = 10 * time.Second

var errScanned = errors.New("host key captured")

// ScanHostKey connects to the SSH server of ep, reads the host key it presents
// and aborts the handshake before any authentication. It is how the first
// fetch records a key (trust on first use) and how a changed key is shown to a
// user who has to accept it.
func ScanHostKey(ctx context.Context, ep Endpoint) (HostKey, error) {
	ctx, cancel := context.WithTimeout(ctx, scanTimeout)
	defer cancel()
	conn, err := netguard.DialContext(scanTimeout)(ctx, "tcp", ep.Addr())
	if err != nil {
		return HostKey{}, fmt.Errorf("connect to %s: %w", ep.Addr(), err)
	}
	defer conn.Close()
	if dl, ok := ctx.Deadline(); ok {
		_ = conn.SetDeadline(dl)
	}
	var got ssh.PublicKey
	cfg := &ssh.ClientConfig{
		User: ep.User,
		HostKeyCallback: func(_ string, _ net.Addr, key ssh.PublicKey) error {
			got = key
			return errScanned
		},
	}
	_, _, _, err = ssh.NewClientConn(conn, ep.Addr(), cfg)
	if got == nil {
		if err == nil {
			err = errors.New("no host key presented")
		}
		return HostKey{}, fmt.Errorf("read SSH host key of %s: %w", ep.Addr(), err)
	}
	return NewHostKey(ep.Addr(), got), nil
}
