// Package dnscheck resolves a hostname for the DNS checks of status pages and
// domains.
//
// The host's own resolver (systemd-resolved, a provider's recursive resolver)
// caches negative answers: a check that ran before the record existed keeps
// saying "no records" for as long as the zone's SOA minimum, up to 30 minutes.
// So the check asks the zone's authoritative nameservers instead, with
// recursion off, and only falls back to public resolvers (and, last, the
// host's resolver) when none of them answers.
package dnscheck

import (
	"context"
	"crypto/rand"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"net/netip"
	"strings"
	"sync"
	"time"

	"golang.org/x/net/dns/dnsmessage"

	"github.com/nebulactrl/nebulactrl/internal/netguard"
)

// CacheTTL is how long an answer is kept in the process.
const CacheTTL = 60 * time.Second

const (
	attemptTimeout = 1500 * time.Millisecond
	maxAuthServers = 3
	maxCNAMEDepth  = 4
	maxCacheSize   = 2048
)

// PublicResolvers are asked when no authoritative nameserver answers.
var PublicResolvers = []string{"1.1.1.1:53", "8.8.8.8:53"}

type freshKey struct{}

// Fresh returns a context whose lookups skip the cache (a Recheck click). The
// new answer still replaces the cached one.
func Fresh(ctx context.Context) context.Context { return context.WithValue(ctx, freshKey{}, true) }

func isFresh(ctx context.Context) bool { v, _ := ctx.Value(freshKey{}).(bool); return v }

// system is the host's resolver: it finds nameservers and their addresses, and
// is the last resort for the answer itself.
type system interface {
	LookupNS(ctx context.Context, name string) ([]*net.NS, error)
	LookupIPAddr(ctx context.Context, host string) ([]net.IPAddr, error)
}

type entry struct {
	addrs    []net.IPAddr
	notFound bool
	expires  time.Time
}

// Resolver answers LookupIPAddr from the authoritative nameservers. It is safe
// for concurrent use.
type Resolver struct {
	sys    system
	public []string
	dial   func(ctx context.Context, network, addr string) (net.Conn, error)
	now    func() time.Time

	mu    sync.Mutex
	cache map[string]entry
}

// New returns a Resolver that asks the authoritative nameservers, then
// PublicResolvers, then the host's resolver.
func New() *Resolver {
	var d net.Dialer
	return &Resolver{sys: net.DefaultResolver, public: PublicResolvers, dial: d.DialContext, now: time.Now, cache: map[string]entry{}}
}

func notFound(host string) error {
	return &net.DNSError{Err: "no such host", Name: host, IsNotFound: true}
}

// LookupIPAddr returns the A and AAAA records of host. A name without
// addresses is a *net.DNSError with IsNotFound, as net.Resolver reports it. A
// CNAME is followed to its target.
func (r *Resolver) LookupIPAddr(ctx context.Context, host string) ([]net.IPAddr, error) {
	host = strings.TrimSuffix(strings.ToLower(strings.TrimSpace(host)), ".")
	if host == "" {
		return nil, notFound(host)
	}
	if !isFresh(ctx) {
		if e, ok := r.cached(host); ok {
			if e.notFound {
				return nil, notFound(host)
			}
			return append([]net.IPAddr(nil), e.addrs...), nil
		}
	}
	addrs, err := r.lookup(ctx, host, 0)
	var dnsErr *net.DNSError
	switch {
	case err == nil:
		r.store(host, entry{addrs: addrs})
	case errors.As(err, &dnsErr) && dnsErr.IsNotFound:
		r.store(host, entry{notFound: true})
	}
	return addrs, err
}

func (r *Resolver) cached(host string) (entry, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	e, ok := r.cache[host]
	if !ok || !r.now().Before(e.expires) {
		delete(r.cache, host)
		return entry{}, false
	}
	return e, true
}

func (r *Resolver) store(host string, e entry) {
	now := r.now()
	e.expires = now.Add(CacheTTL)
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.cache) >= maxCacheSize {
		for k, v := range r.cache {
			if !now.Before(v.expires) {
				delete(r.cache, k)
			}
		}
		if len(r.cache) >= maxCacheSize {
			clear(r.cache)
		}
	}
	r.cache[host] = e
}

// lookup asks the authoritative servers, then the public resolvers, then the host's.
func (r *Resolver) lookup(ctx context.Context, host string, depth int) ([]net.IPAddr, error) {
	if depth > maxCNAMEDepth {
		return nil, fmt.Errorf("lookup %s: CNAME chain too long", host)
	}
	servers := r.authServers(ctx, host)
	for _, srv := range servers {
		res, err := r.ask(ctx, srv, host, false)
		if err != nil {
			continue
		}
		return r.finish(ctx, host, res, depth)
	}
	for _, srv := range r.public {
		res, err := r.ask(ctx, srv, host, true)
		if err != nil {
			continue
		}
		return r.finish(ctx, host, res, depth)
	}
	addrs, err := r.sys.LookupIPAddr(ctx, host)
	if err != nil {
		return nil, err
	}
	return addrs, nil
}

// finish turns an answer into addresses, following a CNAME that left the zone.
func (r *Resolver) finish(ctx context.Context, host string, res answer, depth int) ([]net.IPAddr, error) {
	if len(res.ips) > 0 {
		return res.ips, nil
	}
	if res.cname != "" && res.cname != host {
		return r.lookup(ctx, res.cname, depth+1)
	}
	return nil, notFound(host)
}

// authServers returns "ip:53" for the nameservers of the zone host lives in,
// walking up the labels to the zone apex (the first name that has NS records).
// It returns nothing when they cannot be found.
func (r *Resolver) authServers(ctx context.Context, host string) []string {
	labels := strings.Split(host, ".")
	for i := 0; i+1 < len(labels); i++ {
		name := strings.Join(labels[i:], ".")
		nss, err := r.sys.LookupNS(ctx, name)
		var dnsErr *net.DNSError
		if err != nil && !(errors.As(err, &dnsErr) && dnsErr.IsNotFound) {
			return nil
		}
		if len(nss) == 0 {
			continue
		}
		var v4, v6 []string
		for _, ns := range nss {
			addrs, err := r.sys.LookupIPAddr(ctx, strings.TrimSuffix(ns.Host, "."))
			if err != nil {
				continue
			}
			for _, a := range addrs {
				// A zone's NS records are set by whoever owns the domain; one
				// pointing at an internal address would make this host send DNS
				// queries into its own network.
				if ip, ok := netip.AddrFromSlice(a.IP); !ok || netguard.Blocked(ip, netguard.Allow{}) {
					continue
				}
				if a.IP.To4() != nil {
					v4 = append(v4, net.JoinHostPort(a.IP.String(), "53"))
				} else {
					v6 = append(v6, net.JoinHostPort(a.IP.String(), "53"))
				}
			}
			if len(v4) >= maxAuthServers {
				break
			}
		}
		out := append(v4, v6...)
		if len(out) > maxAuthServers {
			out = out[:maxAuthServers]
		}
		return out
	}
	return nil
}

// answer is what one server said about a name.
type answer struct {
	ips   []net.IPAddr
	cname string // the end of the CNAME chain that starts at the name, if any
}

// ask queries A and AAAA (a CNAME comes with either answer) at one server. A
// nameserver that is not authoritative for the zone, or that refuses, is an
// error; so is a missing answer. NXDOMAIN and NODATA are answers with no
// addresses.
func (r *Resolver) ask(ctx context.Context, server, host string, recurse bool) (answer, error) {
	var out answer
	for _, qt := range []dnsmessage.Type{dnsmessage.TypeA, dnsmessage.TypeAAAA} {
		rep, err := r.exchange(ctx, server, host, qt, recurse)
		if err != nil {
			return answer{}, err
		}
		if rep.rcode != dnsmessage.RCodeSuccess && rep.rcode != dnsmessage.RCodeNameError {
			return answer{}, fmt.Errorf("%s answered %v", server, rep.rcode)
		}
		if !recurse && !rep.authoritative {
			return answer{}, fmt.Errorf("%s is not authoritative for %s", server, host)
		}
		out.ips = append(out.ips, rep.ips...)
		if rep.cname != "" {
			out.cname = rep.cname
		}
	}
	return out, nil
}

type reply struct {
	rcode         dnsmessage.RCode
	authoritative bool
	ips           []net.IPAddr
	cname         string
}

func (r *Resolver) exchange(ctx context.Context, server, host string, qt dnsmessage.Type, recurse bool) (reply, error) {
	name, err := dnsmessage.NewName(host + ".")
	if err != nil {
		return reply{}, err
	}
	var idb [2]byte
	if _, err := rand.Read(idb[:]); err != nil {
		return reply{}, err
	}
	id := binary.BigEndian.Uint16(idb[:])
	b := dnsmessage.NewBuilder(nil, dnsmessage.Header{ID: id, RecursionDesired: recurse})
	b.EnableCompression()
	if err := b.StartQuestions(); err != nil {
		return reply{}, err
	}
	if err := b.Question(dnsmessage.Question{Name: name, Type: qt, Class: dnsmessage.ClassINET}); err != nil {
		return reply{}, err
	}
	query, err := b.Finish()
	if err != nil {
		return reply{}, err
	}

	actx, cancel := context.WithTimeout(ctx, attemptTimeout)
	defer cancel()
	raw, err := r.roundTrip(actx, "udp", server, query, id)
	if err != nil {
		return reply{}, err
	}
	rep, truncated, err := parseReply(raw, id, host)
	if err != nil {
		return reply{}, err
	}
	if truncated {
		raw, err = r.roundTrip(actx, "tcp", server, query, id)
		if err != nil {
			return reply{}, err
		}
		rep, _, err = parseReply(raw, id, host)
	}
	return rep, err
}

func (r *Resolver) roundTrip(ctx context.Context, network, server string, query []byte, id uint16) ([]byte, error) {
	conn, err := r.dial(ctx, network, server)
	if err != nil {
		return nil, err
	}
	defer conn.Close()
	if dl, ok := ctx.Deadline(); ok {
		_ = conn.SetDeadline(dl)
	}
	if network == "tcp" {
		msg := binary.BigEndian.AppendUint16(nil, uint16(len(query)))
		if _, err := conn.Write(append(msg, query...)); err != nil {
			return nil, err
		}
		var n [2]byte
		if _, err := io.ReadFull(conn, n[:]); err != nil {
			return nil, err
		}
		buf := make([]byte, binary.BigEndian.Uint16(n[:]))
		if _, err := io.ReadFull(conn, buf); err != nil {
			return nil, err
		}
		return buf, nil
	}
	if _, err := conn.Write(query); err != nil {
		return nil, err
	}
	buf := make([]byte, 4096)
	for {
		n, err := conn.Read(buf)
		if err != nil {
			return nil, err
		}
		if n >= 2 && binary.BigEndian.Uint16(buf[:2]) == id {
			return buf[:n], nil
		}
		// A datagram for another query: keep waiting for ours.
	}
}

// parseReply reads a response to a query for host. truncated says the answer
// did not fit a datagram.
func parseReply(raw []byte, id uint16, host string) (rep reply, truncated bool, err error) {
	var p dnsmessage.Parser
	h, err := p.Start(raw)
	if err != nil {
		return reply{}, false, err
	}
	if !h.Response || h.ID != id {
		return reply{}, false, errors.New("unexpected DNS response")
	}
	if h.Truncated {
		return reply{}, true, nil
	}
	rep.rcode, rep.authoritative = h.RCode, h.Authoritative
	if err := p.SkipAllQuestions(); err != nil {
		return reply{}, false, err
	}
	rrs, err := p.AllAnswers()
	if err != nil {
		return reply{}, false, err
	}
	// Follow the CNAME chain that starts at host: only those owners count.
	next := map[string]string{}
	for _, rr := range rrs {
		if c, ok := rr.Body.(*dnsmessage.CNAMEResource); ok {
			next[canon(rr.Header.Name.String())] = canon(c.CNAME.String())
		}
	}
	owners := map[string]bool{host: true}
	end := host
	for range maxCNAMEDepth + 1 {
		t, ok := next[end]
		if !ok || owners[t] {
			break
		}
		owners[t], end = true, t
	}
	if end != host {
		rep.cname = end
	}
	for _, rr := range rrs {
		if !owners[canon(rr.Header.Name.String())] {
			continue
		}
		switch b := rr.Body.(type) {
		case *dnsmessage.AResource:
			rep.ips = append(rep.ips, net.IPAddr{IP: net.IP(b.A[:])})
		case *dnsmessage.AAAAResource:
			rep.ips = append(rep.ips, net.IPAddr{IP: net.IP(b.AAAA[:])})
		}
	}
	return rep, false, nil
}

func canon(name string) string { return strings.TrimSuffix(strings.ToLower(name), ".") }
