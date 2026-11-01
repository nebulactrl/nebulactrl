// Package gitsshtest is a tiny in-process SSH server that serves
// git-upload-pack for tests: public-key auth against a mutable key list, a
// swappable host key (to simulate a changed host) and read-only access to the
// repositories under one directory. It runs the real `git upload-pack`, so it
// needs the git binary.
package gitsshtest

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/binary"
	"errors"
	"io"
	"net"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"
)

// NewHostSigner returns a fresh Ed25519 host key.
func NewHostSigner(t testing.TB) ssh.Signer {
	t.Helper()
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	s, err := ssh.NewSignerFromKey(priv)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

// Server is a running test SSH git server.
type Server struct {
	// Addr is the listen address, 127.0.0.1:<port>.
	Addr string
	// Port is the listen port.
	Port int

	root string
	ln   net.Listener

	mu         sync.Mutex
	hostKey    ssh.Signer
	authorized []ssh.PublicKey
	authTries  int
	wg         sync.WaitGroup
}

// Start serves the repositories under root (a request for "/app.git" opens
// root/app.git) on a free loopback port. Close is registered as test cleanup.
func Start(t testing.TB, root string, hostKey ssh.Signer, authorized ...ssh.PublicKey) *Server {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	s := &Server{
		Addr: ln.Addr().String(), Port: ln.Addr().(*net.TCPAddr).Port,
		root: root, ln: ln, hostKey: hostKey, authorized: authorized,
	}
	s.wg.Add(1)
	go s.serve()
	t.Cleanup(s.Close)
	return s
}

// Close stops the server and waits for its connections.
func (s *Server) Close() {
	_ = s.ln.Close()
	s.wg.Wait()
}

// SetHostKey changes the host key presented to connections made from now on.
func (s *Server) SetHostKey(k ssh.Signer) {
	s.mu.Lock()
	s.hostKey = k
	s.mu.Unlock()
}

// Authorize adds a key that may log in.
func (s *Server) Authorize(k ssh.PublicKey) {
	s.mu.Lock()
	s.authorized = append(s.authorized, k)
	s.mu.Unlock()
}

// AuthAttempts is the number of public-key authentications attempted so far.
func (s *Server) AuthAttempts() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.authTries
}

func (s *Server) serve() {
	defer s.wg.Done()
	for {
		c, err := s.ln.Accept()
		if err != nil {
			return
		}
		s.wg.Add(1)
		go func() {
			defer s.wg.Done()
			s.handle(c)
		}()
	}
}

func (s *Server) handle(c net.Conn) {
	defer c.Close()
	s.mu.Lock()
	host := s.hostKey
	s.mu.Unlock()
	cfg := &ssh.ServerConfig{PublicKeyCallback: func(_ ssh.ConnMetadata, key ssh.PublicKey) (*ssh.Permissions, error) {
		s.mu.Lock()
		defer s.mu.Unlock()
		s.authTries++
		for _, a := range s.authorized {
			if a.Type() == key.Type() && string(a.Marshal()) == string(key.Marshal()) {
				return &ssh.Permissions{}, nil
			}
		}
		return nil, errors.New("unknown key")
	}}
	cfg.AddHostKey(host)
	conn, chans, reqs, err := ssh.NewServerConn(c, cfg)
	if err != nil {
		return
	}
	defer conn.Close()
	go ssh.DiscardRequests(reqs)
	for nc := range chans {
		if nc.ChannelType() != "session" {
			_ = nc.Reject(ssh.UnknownChannelType, "session only")
			continue
		}
		ch, creqs, err := nc.Accept()
		if err != nil {
			return
		}
		go s.session(ch, creqs)
	}
}

func (s *Server) session(ch ssh.Channel, reqs <-chan *ssh.Request) {
	defer ch.Close()
	for r := range reqs {
		switch r.Type {
		case "env":
			_ = r.Reply(true, nil)
		case "exec":
			if len(r.Payload) < 4 {
				_ = r.Reply(false, nil)
				return
			}
			n := binary.BigEndian.Uint32(r.Payload)
			if int(n) > len(r.Payload)-4 {
				_ = r.Reply(false, nil)
				return
			}
			_ = r.Reply(true, nil)
			status := s.upload(ch, string(r.Payload[4:4+n]))
			_, _ = ch.SendRequest("exit-status", false, ssh.Marshal(struct{ Status uint32 }{status}))
			return
		default:
			_ = r.Reply(false, nil)
		}
	}
}

// upload runs git-upload-pack for cmd ("git-upload-pack '/app.git'") and
// returns the exit status. Anything but upload-pack inside root is refused.
func (s *Server) upload(ch ssh.Channel, cmd string) uint32 {
	name, arg, ok := strings.Cut(strings.TrimSpace(cmd), " ")
	if !ok || (name != "git-upload-pack" && name != "git upload-pack") {
		_, _ = io.WriteString(ch.Stderr(), "only git-upload-pack is allowed\n")
		return 1
	}
	rel := strings.Trim(strings.TrimSpace(arg), `'"`)
	dir := filepath.Join(s.root, filepath.Clean("/"+rel))
	c := exec.Command("git", "upload-pack", "--strict", dir)
	c.Stdin, c.Stdout, c.Stderr = ch, ch, ch.Stderr()
	c.WaitDelay = 2 * time.Second // do not wait for a client that keeps stdin open
	if err := c.Run(); err != nil {
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			return uint32(ee.ExitCode())
		}
		_, _ = io.WriteString(ch.Stderr(), err.Error()+"\n")
		return 1
	}
	return 0
}
