package dnscheck

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"golang.org/x/net/dns/dnsmessage"
)

func mustName(s string) dnsmessage.Name { return dnsmessage.MustNewName(s + ".") }

func addA(t *testing.T, b *dnsmessage.Builder, owner, ip string) {
	t.Helper()
	h := dnsmessage.ResourceHeader{Name: mustName(owner), Class: dnsmessage.ClassINET, TTL: 60}
	require.NoError(t, b.AResource(h, dnsmessage.AResource{A: [4]byte(net.ParseIP(ip).To4())}))
}

func addAAAA(t *testing.T, b *dnsmessage.Builder, owner, ip string) {
	t.Helper()
	h := dnsmessage.ResourceHeader{Name: mustName(owner), Class: dnsmessage.ClassINET, TTL: 60}
	require.NoError(t, b.AAAAResource(h, dnsmessage.AAAAResource{AAAA: [16]byte(net.ParseIP(ip).To16())}))
}

func addCNAME(t *testing.T, b *dnsmessage.Builder, owner, target string) {
	t.Helper()
	h := dnsmessage.ResourceHeader{Name: mustName(owner), Class: dnsmessage.ClassINET, TTL: 60}
	require.NoError(t, b.CNAMEResource(h, dnsmessage.CNAMEResource{CNAME: mustName(target)}))
}

func buildReply(t *testing.T, h dnsmessage.Header, host string, answers func(*dnsmessage.Builder)) []byte {
	t.Helper()
	b := dnsmessage.NewBuilder(nil, h)
	require.NoError(t, b.StartQuestions())
	require.NoError(t, b.Question(dnsmessage.Question{Name: mustName(host), Type: dnsmessage.TypeA, Class: dnsmessage.ClassINET}))
	require.NoError(t, b.StartAnswers())
	if answers != nil {
		answers(&b)
	}
	out, err := b.Finish()
	require.NoError(t, err)
	return out
}

func TestParseReplyKeepsOnlyTheAddressesOfTheNameAndItsCNAMEChain(t *testing.T) {
	raw := buildReply(t, dnsmessage.Header{ID: 7, Response: true, Authoritative: true}, "www.example.com", func(b *dnsmessage.Builder) {
		addCNAME(t, b, "www.example.com", "origin.example.com")
		addA(t, b, "origin.example.com", "203.0.113.9")
		addAAAA(t, b, "origin.example.com", "2606:4700::1")
		addA(t, b, "evil.example", "6.6.6.6") // an owner that is not part of the chain
	})
	rep, truncated, err := parseReply(raw, 7, "www.example.com")
	require.NoError(t, err)
	require.False(t, truncated)
	require.True(t, rep.authoritative)
	require.Equal(t, "origin.example.com", rep.cname)
	require.Equal(t, []string{"203.0.113.9", "2606:4700::1"}, ips(rep.ips), "the unrelated owner's record is dropped")
}

func TestParseReplyStopsAtACNAMELoop(t *testing.T) {
	raw := buildReply(t, dnsmessage.Header{ID: 7, Response: true}, "a.example.com", func(b *dnsmessage.Builder) {
		addCNAME(t, b, "a.example.com", "b.example.com")
		addCNAME(t, b, "b.example.com", "a.example.com")
	})
	rep, _, err := parseReply(raw, 7, "a.example.com")
	require.NoError(t, err)
	require.Equal(t, "b.example.com", rep.cname)
	require.Empty(t, rep.ips)
}

func TestParseReplyRejectsWhatIsNotTheAnswerToOurQuery(t *testing.T) {
	good := buildReply(t, dnsmessage.Header{ID: 9, Response: true}, "a.example.com", func(b *dnsmessage.Builder) {
		addA(t, b, "a.example.com", "203.0.113.1")
	})

	_, _, err := parseReply(good, 10, "a.example.com")
	require.ErrorContains(t, err, "unexpected DNS response", "another query's ID")

	query := buildReply(t, dnsmessage.Header{ID: 9, Response: false}, "a.example.com", nil)
	_, _, err = parseReply(query, 9, "a.example.com")
	require.ErrorContains(t, err, "unexpected DNS response", "a query, not a response")

	_, _, err = parseReply([]byte{1, 2, 3}, 9, "a.example.com")
	require.Error(t, err, "not a DNS message")

	_, _, err = parseReply(good[:len(good)-3], 9, "a.example.com")
	require.Error(t, err, "an answer section cut short")

	truncated := buildReply(t, dnsmessage.Header{ID: 9, Response: true, Truncated: true}, "a.example.com", nil)
	rep, trunc, err := parseReply(truncated, 9, "a.example.com")
	require.NoError(t, err)
	require.True(t, trunc)
	require.Empty(t, rep.ips)
}

func TestExchangeRejectsANameThatCannotBeEncoded(t *testing.T) {
	r := &Resolver{dial: func(context.Context, string, string) (net.Conn, error) {
		t.Error("nothing is sent for a name that cannot be encoded")
		return nil, io.ErrClosedPipe
	}}
	long := strings.Repeat("a", 300)
	_, err := r.exchange(context.Background(), "192.0.2.53:53", long, dnsmessage.TypeA, false)
	require.Error(t, err)
}

func TestRoundTripSkipsDatagramsOfOtherQueries(t *testing.T) {
	client, server := net.Pipe()
	r := &Resolver{dial: func(context.Context, string, string) (net.Conn, error) { return client, nil }}
	go func() {
		buf := make([]byte, 64)
		_, _ = server.Read(buf)
		_, _ = server.Write([]byte{0x12})             // too short to carry an ID
		_, _ = server.Write([]byte{0x00, 0x01, 0x02}) // another query's ID
		_, _ = server.Write([]byte{0x12, 0x34, 0xAA})
	}()
	got, err := r.roundTrip(context.Background(), "udp", "192.0.2.53:53", []byte{0x12, 0x34, 0x01}, 0x1234)
	require.NoError(t, err)
	require.Equal(t, []byte{0x12, 0x34, 0xAA}, got)
}

func TestRoundTripErrors(t *testing.T) {
	dialTo := func(c net.Conn) *Resolver {
		return &Resolver{dial: func(context.Context, string, string) (net.Conn, error) { return c, nil }}
	}
	query := []byte{0, 1, 2, 3}

	t.Run("dial fails", func(t *testing.T) {
		boom := errors.New("network unreachable")
		r := &Resolver{dial: func(context.Context, string, string) (net.Conn, error) { return nil, boom }}
		_, err := r.roundTrip(context.Background(), "udp", "192.0.2.53:53", query, 1)
		require.ErrorIs(t, err, boom)
	})
	t.Run("udp write fails", func(t *testing.T) {
		client, server := net.Pipe()
		require.NoError(t, server.Close())
		_, err := dialTo(client).roundTrip(context.Background(), "udp", "x", query, 1)
		require.Error(t, err)
	})
	t.Run("udp read fails", func(t *testing.T) {
		client, server := net.Pipe()
		go func() { _, _ = server.Read(make([]byte, 16)); _ = server.Close() }()
		_, err := dialTo(client).roundTrip(context.Background(), "udp", "x", query, 1)
		require.Error(t, err)
	})
	t.Run("tcp write fails", func(t *testing.T) {
		client, server := net.Pipe()
		require.NoError(t, server.Close())
		_, err := dialTo(client).roundTrip(context.Background(), "tcp", "x", query, 1)
		require.Error(t, err)
	})
	t.Run("tcp closes before the length", func(t *testing.T) {
		client, server := net.Pipe()
		go func() { _, _ = io.ReadFull(server, make([]byte, 2+len(query))); _ = server.Close() }()
		_, err := dialTo(client).roundTrip(context.Background(), "tcp", "x", query, 1)
		require.ErrorIs(t, err, io.EOF)
	})
	t.Run("tcp closes mid message", func(t *testing.T) {
		client, server := net.Pipe()
		go func() {
			_, _ = io.ReadFull(server, make([]byte, 2+len(query)))
			_, _ = server.Write([]byte{0, 10, 1, 2}) // announces 10 bytes, sends 2
			_ = server.Close()
		}()
		_, err := dialTo(client).roundTrip(context.Background(), "tcp", "x", query, 1)
		require.Error(t, err)
	})
	t.Run("tcp success", func(t *testing.T) {
		client, server := net.Pipe()
		go func() {
			_, _ = io.ReadFull(server, make([]byte, 2+len(query)))
			_, _ = server.Write([]byte{0, 3, 7, 8, 9})
		}()
		got, err := dialTo(client).roundTrip(context.Background(), "tcp", "x", query, 1)
		require.NoError(t, err)
		require.Equal(t, []byte{7, 8, 9}, got)
	})
}

func TestUnreachableServersFallThroughToTheHostResolver(t *testing.T) {
	sys := standardSystem()
	sys.stale = nil
	sys.hosts["status.example.com"] = []string{"198.51.100.2"}
	r := New()
	r.sys = sys
	r.public = []string{"1.1.1.1:53"}
	var dialed []string
	r.dial = func(_ context.Context, network, addr string) (net.Conn, error) {
		dialed = append(dialed, network+" "+addr)
		return nil, errors.New("unreachable")
	}

	got, err := r.LookupIPAddr(context.Background(), "status.example.com")
	require.NoError(t, err)
	require.Equal(t, []string{"198.51.100.2"}, ips(got))
	require.Equal(t, []string{"udp 192.0.2.53:53", "udp 1.1.1.1:53"}, dialed, "the nameserver, then the public resolver, were tried once each")
}

func TestEmptyHostIsNotFound(t *testing.T) {
	r := New()
	for _, h := range []string{"", "  ", "."} {
		got, err := r.LookupIPAddr(context.Background(), h)
		require.Empty(t, got)
		var dnsErr *net.DNSError
		require.ErrorAs(t, err, &dnsErr, "%q", h)
		require.True(t, dnsErr.IsNotFound)
	}
}

func TestACNAMEChainThatNeverEndsFails(t *testing.T) {
	cnames := map[string]string{}
	for i := range 8 {
		cnames[fmt.Sprintf("n%d.example.com", i)] = fmt.Sprintf("n%d.example.com", i+1)
	}
	auth := startFake(t, zoneData{cname: cnames})
	r := newResolver(t, standardSystem(), auth, startFake(t, zoneData{}))

	_, err := r.LookupIPAddr(context.Background(), "n0.example.com")
	require.ErrorContains(t, err, "CNAME chain too long")
	var dnsErr *net.DNSError
	require.False(t, errors.As(err, &dnsErr), "an overlong chain is not 'no such host'")
}

func TestCacheDropsExpiredEntriesBeforeItClearsEverything(t *testing.T) {
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	newResolver := func() *Resolver {
		r := New()
		r.now = func() time.Time { return now }
		return r
	}

	t.Run("a full cache of fresh entries starts over", func(t *testing.T) {
		r := newResolver()
		for i := range maxCacheSize {
			r.store(fmt.Sprintf("h%d.example.com", i), entry{notFound: true})
		}
		require.Len(t, r.cache, maxCacheSize)
		r.store("one-more.example.com", entry{notFound: true})
		require.Len(t, r.cache, 1)
		require.Contains(t, r.cache, "one-more.example.com")
	})

	t.Run("only expired entries are dropped when that makes room", func(t *testing.T) {
		r := newResolver()
		for i := range maxCacheSize - 1 {
			r.store(fmt.Sprintf("old%d.example.com", i), entry{notFound: true}) // expire at now+TTL
		}
		now = now.Add(CacheTTL / 2)
		r.store("late.example.com", entry{notFound: true}) // the cache is full now; expires at now+TTL
		require.Len(t, r.cache, maxCacheSize)

		now = now.Add(CacheTTL/2 + time.Second) // the old batch has expired, "late" has not
		r.store("newest.example.com", entry{notFound: true})
		require.Len(t, r.cache, 2)
		require.Contains(t, r.cache, "late.example.com")
		require.Contains(t, r.cache, "newest.example.com")
	})
}

type nsFailSystem struct{ *fakeSystem }

func (nsFailSystem) LookupNS(context.Context, string) ([]*net.NS, error) {
	return nil, &net.DNSError{Err: "server misbehaving", IsTemporary: true}
}

func TestAuthServers(t *testing.T) {
	ctx := context.Background()
	newSys := func(ns []string, hosts map[string][]string) *fakeSystem {
		return &fakeSystem{ns: map[string][]string{"example.com": ns}, hosts: hosts, stale: staleNoData}
	}

	t.Run("at most three, in nameserver order", func(t *testing.T) {
		sys := newSys([]string{"ns1.example.net", "ns2.example.net", "ns3.example.net", "ns4.example.net"}, map[string][]string{
			"ns1.example.net": {"192.0.2.1"}, "ns2.example.net": {"192.0.2.2"}, "ns3.example.net": {"192.0.2.3"}, "ns4.example.net": {"192.0.2.4"},
		})
		r := &Resolver{sys: sys}
		require.Equal(t, []string{"192.0.2.1:53", "192.0.2.2:53", "192.0.2.3:53"}, r.authServers(ctx, "status.example.com"))
	})
	t.Run("IPv4 first, IPv6 after", func(t *testing.T) {
		sys := newSys([]string{"ns1.example.net", "ns2.example.net"}, map[string][]string{
			"ns1.example.net": {"2606:4700::1111"}, "ns2.example.net": {"192.0.2.2"},
		})
		r := &Resolver{sys: sys}
		require.Equal(t, []string{"192.0.2.2:53", "[2606:4700::1111]:53"}, r.authServers(ctx, "example.com"))
	})
	t.Run("IPv6 addresses count towards the cap", func(t *testing.T) {
		sys := newSys([]string{"ns1.example.net"}, map[string][]string{
			"ns1.example.net": {"2606:4700::1", "2606:4700::2", "2606:4700::3", "2606:4700::4"},
		})
		r := &Resolver{sys: sys}
		require.Len(t, r.authServers(ctx, "example.com"), 3)
	})
	t.Run("a nameserver that does not resolve is skipped", func(t *testing.T) {
		sys := newSys([]string{"gone.example.net", "ns2.example.net"}, map[string][]string{"ns2.example.net": {"192.0.2.2"}})
		r := &Resolver{sys: sys}
		require.Equal(t, []string{"192.0.2.2:53"}, r.authServers(ctx, "example.com"))
	})
	t.Run("no nameserver resolves", func(t *testing.T) {
		r := &Resolver{sys: newSys([]string{"gone.example.net"}, map[string][]string{})}
		require.Empty(t, r.authServers(ctx, "example.com"))
	})
	t.Run("a failing NS lookup gives up", func(t *testing.T) {
		r := &Resolver{sys: nsFailSystem{newSys([]string{"ns1.example.net"}, map[string][]string{"ns1.example.net": {"192.0.2.1"}})}}
		require.Empty(t, r.authServers(ctx, "status.example.com"))
	})
	t.Run("a single label has no zone", func(t *testing.T) {
		r := &Resolver{sys: newSys(nil, nil)}
		require.Empty(t, r.authServers(ctx, "localhost"))
	})
}
