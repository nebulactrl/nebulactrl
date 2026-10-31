package dnscheck

import (
	"context"
	"io"
	"net"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"golang.org/x/net/dns/dnsmessage"
)

// zoneData is what a fake server knows: owner -> rdata.
type zoneData struct {
	a     map[string][]string
	aaaa  map[string][]string
	cname map[string]string
}

// fakeServer is a DNS server on loopback (UDP and TCP).
type fakeServer struct {
	addr    string
	queries atomic.Int32
	sawRD   atomic.Bool
	sawNoRD atomic.Bool

	mu            sync.Mutex
	data          zoneData
	authoritative bool
	rcode         dnsmessage.RCode // forced when not success
	truncateUDP   bool
}

func (f *fakeServer) update(fn func(*fakeServer)) {
	f.mu.Lock()
	defer f.mu.Unlock()
	fn(f)
}

func startFake(t *testing.T, d zoneData) *fakeServer {
	t.Helper()
	// UDP and TCP share a port; the TCP side of a UDP port can be taken.
	var pc net.PacketConn
	var tl net.Listener
	for range 20 {
		var err error
		if tl, err = net.Listen("tcp", "127.0.0.1:0"); err != nil {
			continue
		}
		if pc, err = net.ListenPacket("udp", tl.Addr().String()); err == nil {
			break
		}
		tl.Close()
		tl = nil
	}
	require.NotNil(t, tl, "no free port for the fake DNS server")
	f := &fakeServer{addr: pc.LocalAddr().String(), data: d, authoritative: true}
	t.Cleanup(func() { pc.Close(); tl.Close() })
	go func() {
		buf := make([]byte, 1500)
		for {
			n, from, err := pc.ReadFrom(buf)
			if err != nil {
				return
			}
			if resp := f.respond(buf[:n], true); resp != nil {
				_, _ = pc.WriteTo(resp, from)
			}
		}
	}()
	go func() {
		for {
			c, err := tl.Accept()
			if err != nil {
				return
			}
			go func() {
				defer c.Close()
				var l [2]byte
				if _, err := io.ReadFull(c, l[:]); err != nil {
					return
				}
				q := make([]byte, int(l[0])<<8|int(l[1]))
				if _, err := io.ReadFull(c, q); err != nil {
					return
				}
				resp := f.respond(q, false)
				_, _ = c.Write(append([]byte{byte(len(resp) >> 8), byte(len(resp))}, resp...))
			}()
		}
	}()
	return f
}

func (f *fakeServer) respond(raw []byte, udp bool) []byte {
	f.queries.Add(1)
	f.mu.Lock()
	defer f.mu.Unlock()
	var p dnsmessage.Parser
	h, err := p.Start(raw)
	if err != nil {
		return nil
	}
	if h.RecursionDesired {
		f.sawRD.Store(true)
	} else {
		f.sawNoRD.Store(true)
	}
	q, err := p.Question()
	if err != nil {
		return nil
	}
	name := q.Name.String()
	key := name[:len(name)-1]
	rh := dnsmessage.Header{ID: h.ID, Response: true, Authoritative: f.authoritative, RCode: f.rcode}
	rh.Truncated = udp && f.truncateUDP
	b := dnsmessage.NewBuilder(nil, rh)
	_ = b.StartQuestions()
	_ = b.Question(q)
	if f.rcode == dnsmessage.RCodeSuccess && !rh.Truncated {
		_ = b.StartAnswers()
		hd := dnsmessage.ResourceHeader{Name: q.Name, Class: dnsmessage.ClassINET, TTL: 60}
		switch c, isCNAME := f.data.cname[key]; {
		case isCNAME:
			hd.Type = dnsmessage.TypeCNAME
			cn, _ := dnsmessage.NewName(c + ".")
			_ = b.CNAMEResource(hd, dnsmessage.CNAMEResource{CNAME: cn})
			// A target in the same zone is served with it.
			if q.Type == dnsmessage.TypeA {
				for _, ip := range f.data.a[c] {
					th := dnsmessage.ResourceHeader{Name: cn, Class: dnsmessage.ClassINET, TTL: 60, Type: dnsmessage.TypeA}
					_ = b.AResource(th, dnsmessage.AResource{A: [4]byte(net.ParseIP(ip).To4())})
				}
			}
		case q.Type == dnsmessage.TypeA:
			hd.Type = dnsmessage.TypeA
			for _, ip := range f.data.a[key] {
				_ = b.AResource(hd, dnsmessage.AResource{A: [4]byte(net.ParseIP(ip).To4())})
			}
		case q.Type == dnsmessage.TypeAAAA:
			hd.Type = dnsmessage.TypeAAAA
			for _, ip := range f.data.aaaa[key] {
				_ = b.AAAAResource(hd, dnsmessage.AAAAResource{AAAA: [16]byte(net.ParseIP(ip).To16())})
			}
		}
	}
	out, _ := b.Finish()
	return out
}

// fakeSystem is the host's resolver: nameserver names, and a stale answer for
// everything else.
type fakeSystem struct {
	ns    map[string][]string // zone -> nameserver hosts
	hosts map[string][]string // host -> addresses
	stale error
	asked atomic.Int32
}

func (s *fakeSystem) LookupNS(_ context.Context, name string) ([]*net.NS, error) {
	var out []*net.NS
	for _, h := range s.ns[name] {
		out = append(out, &net.NS{Host: h + "."})
	}
	if len(out) == 0 {
		return nil, &net.DNSError{Err: "no such host", Name: name, IsNotFound: true}
	}
	return out, nil
}

func (s *fakeSystem) LookupIPAddr(_ context.Context, host string) ([]net.IPAddr, error) {
	if ips, ok := s.hosts[host]; ok {
		var out []net.IPAddr
		for _, ip := range ips {
			out = append(out, net.IPAddr{IP: net.ParseIP(ip)})
		}
		return out, nil
	}
	s.asked.Add(1)
	return nil, s.stale
}

var staleNoData = &net.DNSError{Err: "no such host", IsNotFound: true}

func standardSystem() *fakeSystem {
	return &fakeSystem{
		ns:    map[string][]string{"example.com": {"ns1.example.net"}},
		hosts: map[string][]string{"ns1.example.net": {"192.0.2.53"}},
		stale: staleNoData,
	}
}

// newResolver sends every query to the fake servers: the nameserver
// 192.0.2.53 is auth, the public resolver 1.1.1.1 is pub.
func newResolver(t *testing.T, sys *fakeSystem, auth, pub *fakeServer) *Resolver {
	t.Helper()
	r := New()
	r.sys = sys
	r.public = []string{"1.1.1.1:53"}
	r.dial = func(ctx context.Context, network, addr string) (net.Conn, error) {
		var target string
		switch addr {
		case "192.0.2.53:53":
			target = auth.addr
		case "1.1.1.1:53":
			target = pub.addr
		default:
			t.Errorf("unexpected server %s", addr)
			return nil, io.ErrClosedPipe
		}
		var d net.Dialer
		return d.DialContext(ctx, network, target)
	}
	return r
}

func ips(addrs []net.IPAddr) []string {
	var out []string
	for _, a := range addrs {
		out = append(out, a.IP.String())
	}
	return out
}

func TestAuthoritativeAnswerBeatsStaleResolver(t *testing.T) {
	auth := startFake(t, zoneData{a: map[string][]string{"status.example.com": {"104.21.5.9"}}, aaaa: map[string][]string{"status.example.com": {"2606:4700::6815:509"}}})
	pub := startFake(t, zoneData{})
	sys := standardSystem()
	r := newResolver(t, sys, auth, pub)

	got, err := r.LookupIPAddr(context.Background(), "status.example.com")
	require.NoError(t, err)
	require.ElementsMatch(t, []string{"104.21.5.9", "2606:4700::6815:509"}, ips(got))
	require.True(t, auth.sawNoRD.Load() && !auth.sawRD.Load(), "recursion is off towards the authoritative server")
	require.Zero(t, pub.queries.Load())
	require.Zero(t, sys.asked.Load(), "the host's caching resolver is not asked for the answer")
}

func TestAuthoritativeNoDataIsNotFound(t *testing.T) {
	auth := startFake(t, zoneData{})
	// A recursive server may still hold a record; the authoritative answer wins.
	pub := startFake(t, zoneData{a: map[string][]string{"status.example.com": {"198.51.100.1"}}})
	r := newResolver(t, standardSystem(), auth, pub)

	got, err := r.LookupIPAddr(context.Background(), "status.example.com")
	require.Empty(t, got)
	var dnsErr *net.DNSError
	require.ErrorAs(t, err, &dnsErr)
	require.True(t, dnsErr.IsNotFound)
	require.Zero(t, pub.queries.Load())
}

func TestNameserversAreFoundByWalkingUpTheLabels(t *testing.T) {
	auth := startFake(t, zoneData{a: map[string][]string{"a.b.example.com": {"203.0.113.7"}}})
	r := newResolver(t, standardSystem(), auth, startFake(t, zoneData{}))
	got, err := r.LookupIPAddr(context.Background(), "A.B.example.com.")
	require.NoError(t, err)
	require.Equal(t, []string{"203.0.113.7"}, ips(got))
}

func TestNameserversOnInternalAddressesAreNeverAsked(t *testing.T) {
	auth := startFake(t, zoneData{})
	pub := startFake(t, zoneData{a: map[string][]string{"status.example.com": {"198.51.100.1"}}})
	sys := standardSystem()
	sys.hosts["ns1.example.net"] = []string{"10.0.0.53", "127.0.0.1", "169.254.169.254"}
	r := newResolver(t, sys, auth, pub) // fails the test if an internal address is dialed

	got, err := r.LookupIPAddr(context.Background(), "status.example.com")
	require.NoError(t, err)
	require.Equal(t, []string{"198.51.100.1"}, ips(got))
	require.Zero(t, auth.queries.Load())
}

func TestFallsBackToPublicResolversWhenTheAuthoritativeServerFails(t *testing.T) {
	for name, mutate := range map[string]func(*fakeServer){
		"refused":           func(f *fakeServer) { f.rcode = dnsmessage.RCodeRefused },
		"servfail":          func(f *fakeServer) { f.rcode = dnsmessage.RCodeServerFailure },
		"not authoritative": func(f *fakeServer) { f.authoritative = false },
	} {
		t.Run(name, func(t *testing.T) {
			auth := startFake(t, zoneData{})
			auth.update(mutate)
			pub := startFake(t, zoneData{a: map[string][]string{"status.example.com": {"198.51.100.1"}}})
			r := newResolver(t, standardSystem(), auth, pub)
			got, err := r.LookupIPAddr(context.Background(), "status.example.com")
			require.NoError(t, err)
			require.Equal(t, []string{"198.51.100.1"}, ips(got))
			require.True(t, pub.sawRD.Load(), "public resolvers are asked recursively")
		})
	}
}

func TestFallsBackToTheHostResolverLast(t *testing.T) {
	auth := startFake(t, zoneData{})
	auth.update(func(f *fakeServer) { f.rcode = dnsmessage.RCodeRefused })
	pub := startFake(t, zoneData{})
	pub.update(func(f *fakeServer) { f.rcode = dnsmessage.RCodeServerFailure })
	sys := standardSystem()
	sys.stale = nil
	sys.hosts["status.example.com"] = []string{"198.51.100.2"}
	r := newResolver(t, sys, auth, pub)
	got, err := r.LookupIPAddr(context.Background(), "status.example.com")
	require.NoError(t, err)
	require.Equal(t, []string{"198.51.100.2"}, ips(got))
}

func TestCNAMEInTheZoneAndOutOfIt(t *testing.T) {
	auth := startFake(t, zoneData{
		a:     map[string][]string{"origin.example.com": {"203.0.113.9"}, "target.other.test": {"203.0.113.10"}},
		cname: map[string]string{"www.example.com": "origin.example.com", "out.example.com": "target.other.test"},
	})
	sys := standardSystem()
	sys.ns["other.test"] = []string{"ns1.example.net"}
	r := newResolver(t, sys, auth, startFake(t, zoneData{}))

	got, err := r.LookupIPAddr(context.Background(), "www.example.com")
	require.NoError(t, err)
	require.Equal(t, []string{"203.0.113.9"}, ips(got))

	got, err = r.LookupIPAddr(context.Background(), "out.example.com")
	require.NoError(t, err)
	require.Equal(t, []string{"203.0.113.10"}, ips(got), "a CNAME to another zone is followed")
}

func TestTruncatedAnswersRetryOverTCP(t *testing.T) {
	auth := startFake(t, zoneData{a: map[string][]string{"status.example.com": {"203.0.113.7"}}})
	auth.update(func(f *fakeServer) { f.truncateUDP = true })
	r := newResolver(t, standardSystem(), auth, startFake(t, zoneData{}))
	got, err := r.LookupIPAddr(context.Background(), "status.example.com")
	require.NoError(t, err)
	require.Equal(t, []string{"203.0.113.7"}, ips(got))
}

func TestAnswersAreCachedForAMinuteAndRecheckBypassesIt(t *testing.T) {
	auth := startFake(t, zoneData{})
	r := newResolver(t, standardSystem(), auth, startFake(t, zoneData{}))
	clock := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	r.now = func() time.Time { return clock }
	ctx := context.Background()
	setA := func(ip string) {
		auth.update(func(f *fakeServer) { f.data.a = map[string][]string{"status.example.com": {ip}} })
	}

	_, err := r.LookupIPAddr(ctx, "status.example.com")
	require.Error(t, err)
	asked := auth.queries.Load()
	require.NotZero(t, asked)

	// The record appears; the cached NODATA is served within the minute.
	setA("203.0.113.7")
	_, err = r.LookupIPAddr(ctx, "status.example.com")
	require.Error(t, err)
	require.Equal(t, asked, auth.queries.Load(), "no query while cached")

	// Recheck asks again, and refreshes the cache.
	got, err := r.LookupIPAddr(Fresh(ctx), "status.example.com")
	require.NoError(t, err)
	require.Equal(t, []string{"203.0.113.7"}, ips(got))
	got, err = r.LookupIPAddr(ctx, "status.example.com")
	require.NoError(t, err)
	require.Equal(t, []string{"203.0.113.7"}, ips(got))

	// After 60s the entry is gone.
	setA("203.0.113.8")
	clock = clock.Add(CacheTTL + time.Second)
	got, err = r.LookupIPAddr(ctx, "status.example.com")
	require.NoError(t, err)
	require.Equal(t, []string{"203.0.113.8"}, ips(got))
}

func TestFailuresAreNotCached(t *testing.T) {
	auth := startFake(t, zoneData{})
	auth.update(func(f *fakeServer) { f.rcode = dnsmessage.RCodeRefused })
	pub := startFake(t, zoneData{})
	pub.update(func(f *fakeServer) { f.rcode = dnsmessage.RCodeRefused })
	sys := standardSystem()
	sys.stale = &net.DNSError{Err: "i/o timeout", IsTimeout: true}
	r := newResolver(t, sys, auth, pub)
	_, err := r.LookupIPAddr(context.Background(), "status.example.com")
	require.Error(t, err)
	first := sys.asked.Load()
	_, err = r.LookupIPAddr(context.Background(), "status.example.com")
	require.Error(t, err)
	require.Greater(t, sys.asked.Load(), first)
}
