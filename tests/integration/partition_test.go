//go:build integration

package integration

import (
	"io"
	"net"
	"sync"
)

// NetProxy is a TCP forwarder between the agent and the control plane whose
// link tests can cut and heal, simulating a network partition: Cut closes every
// open connection and refuses new ones until Heal.
type NetProxy struct {
	l      net.Listener
	target string

	mu    sync.Mutex
	cut   bool
	conns map[net.Conn]struct{}
}

// NewNetProxy listens on a free loopback port and forwards to target.
func NewNetProxy(target string) (*NetProxy, error) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, err
	}
	p := &NetProxy{l: l, target: target, conns: map[net.Conn]struct{}{}}
	go p.serve()
	return p, nil
}

// URL is the base URL agents should use.
func (p *NetProxy) URL() string { return "http://" + p.l.Addr().String() }

// Close stops the proxy.
func (p *NetProxy) Close() {
	_ = p.l.Close()
	p.Cut()
}

// Cut drops every connection and rejects new ones.
func (p *NetProxy) Cut() {
	p.mu.Lock()
	p.cut = true
	for c := range p.conns {
		_ = c.Close()
	}
	p.conns = map[net.Conn]struct{}{}
	p.mu.Unlock()
}

// Heal accepts connections again.
func (p *NetProxy) Heal() {
	p.mu.Lock()
	p.cut = false
	p.mu.Unlock()
}

func (p *NetProxy) track(c net.Conn) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.cut {
		return false
	}
	p.conns[c] = struct{}{}
	return true
}

func (p *NetProxy) serve() {
	for {
		c, err := p.l.Accept()
		if err != nil {
			return
		}
		if !p.track(c) {
			_ = c.Close()
			continue
		}
		up, err := net.Dial("tcp", p.target)
		if err != nil || !p.track(up) {
			_ = c.Close()
			if up != nil {
				_ = up.Close()
			}
			continue
		}
		go func() {
			defer c.Close()
			defer up.Close()
			done := make(chan struct{}, 2)
			go func() { _, _ = io.Copy(up, c); done <- struct{}{} }()
			go func() { _, _ = io.Copy(c, up); done <- struct{}{} }()
			<-done
		}()
	}
}
