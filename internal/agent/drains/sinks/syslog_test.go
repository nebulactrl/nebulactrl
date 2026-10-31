package sinks

import (
	"bufio"
	"crypto/tls"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

// syslogServer reads octet-counted frames from every accepted connection.
type syslogServer struct {
	ln   net.Listener
	msgs chan string

	mu       sync.Mutex
	conns    []net.Conn
	accepted int
}

func newSyslogServer(t *testing.T, ln net.Listener) *syslogServer {
	t.Helper()
	s := &syslogServer{ln: ln, msgs: make(chan string, 1000)}
	t.Cleanup(func() { _ = ln.Close(); s.closeConns() })
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			s.mu.Lock()
			s.conns = append(s.conns, c)
			s.accepted++
			s.mu.Unlock()
			go s.serve(c)
		}
	}()
	return s
}

func (s *syslogServer) serve(c net.Conn) {
	br := bufio.NewReader(c)
	for {
		lenStr, err := br.ReadString(' ')
		if err != nil {
			return
		}
		n, err := strconv.Atoi(strings.TrimSuffix(lenStr, " "))
		if err != nil {
			s.msgs <- "BAD FRAME LENGTH " + lenStr
			return
		}
		buf := make([]byte, n)
		if _, err := io.ReadFull(br, buf); err != nil {
			return
		}
		s.msgs <- string(buf)
	}
}

func (s *syslogServer) closeConns() {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, c := range s.conns {
		_ = c.Close()
	}
	s.conns = nil
}

func (s *syslogServer) acceptedCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.accepted
}

func (s *syslogServer) next(t *testing.T) string {
	t.Helper()
	select {
	case m := <-s.msgs:
		return m
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for a syslog message")
		return ""
	}
}

func listenTCP(t *testing.T) net.Listener {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	return ln
}

func syslogEntry() Entry {
	return Entry{
		TS:   time.Date(2026, 1, 1, 12, 0, 0, 123456789, time.UTC),
		Text: "line1\nline2\r\nline3", Level: "warn", Stream: "runtime",
		ServiceID: "s1", Service: `a"b]c\d`, Project: "shop", Environment: "prod",
		Replica: 2, Server: "my srv",
	}
}

const syslogEntryWire = `1 2026-01-01T12:00:00.123456Z my_srv nebulactrl 2 - ` +
	`[nebulactrl@32473 service="a\"b\]c\\d" project="shop" environment="prod" replica="2"] ` +
	`[a"b]c\d] line1 line2  line3`

func TestSyslogTCPFormatAndFraming(t *testing.T) {
	srv := newSyslogServer(t, listenTCP(t))
	s := mustNew(t, syslogDrain(agentprotocol.SyslogDrainConfig{Address: srv.ln.Addr().String(), Protocol: agentprotocol.SyslogTCP}), nil)

	e2 := Entry{TS: base, Text: "héllo wörld ✓", Level: "error", Replica: 0}
	e3 := Entry{TS: base, Text: "dbg", Level: "debug", Server: "h", Service: "svc", Replica: -1}
	e4 := Entry{TS: base, Text: "no level", Level: "", Service: "svc"}
	if err := s.Send(t.Context(), []Entry{syslogEntry(), e2, e3, e4}); err != nil {
		t.Fatal(err)
	}
	// Default facility 1 (user): PRI = 8 + severity.
	want := []string{
		"<12>" + syslogEntryWire,
		"<11>1 2026-01-01T12:00:00.000000Z - nebulactrl 0 - [nebulactrl@32473 replica=\"0\"] héllo wörld ✓",
		"<15>1 2026-01-01T12:00:00.000000Z h nebulactrl - - [nebulactrl@32473 service=\"svc\" replica=\"-1\"] [svc] dbg",
		"<14>1 2026-01-01T12:00:00.000000Z - nebulactrl 0 - [nebulactrl@32473 service=\"svc\" replica=\"0\"] [svc] no level",
	}
	for i, w := range want {
		if got := srv.next(t); got != w {
			t.Errorf("message %d\n got %q\nwant %q", i, got, w)
		}
	}
}

func TestSyslogRawFrameBytes(t *testing.T) {
	// The batch goes out in one octet-counted write: "<len> <msg>" back to back.
	ln := listenTCP(t)
	got := make(chan []byte, 1)
	go func() {
		c, err := ln.Accept()
		if err != nil {
			return
		}
		defer c.Close()
		_ = c.SetReadDeadline(time.Now().Add(2 * time.Second))
		buf := make([]byte, 4096)
		var all []byte
		for len(all) < 2 || !strings.Contains(string(all), "second") {
			n, err := c.Read(buf)
			all = append(all, buf[:n]...)
			if err != nil {
				break
			}
		}
		got <- all
	}()
	s := mustNew(t, syslogDrain(agentprotocol.SyslogDrainConfig{Address: ln.Addr().String(), Protocol: "tcp", Facility: 16, AppName: "my app"}), nil)
	a := Entry{TS: base, Text: "first", Level: "info"}
	b := Entry{TS: base, Text: "second", Level: "info"}
	if err := s.Send(t.Context(), []Entry{a, b}); err != nil {
		t.Fatal(err)
	}
	m := func(text string) string {
		return "<134>1 2026-01-01T12:00:00.000000Z - my_app 0 - [nebulactrl@32473 replica=\"0\"] " + text
	}
	want := strconv.Itoa(len(m("first"))) + " " + m("first") + strconv.Itoa(len(m("second"))) + " " + m("second")
	select {
	case raw := <-got:
		if string(raw) != want {
			t.Fatalf("wire bytes\n got %q\nwant %q", raw, want)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("timeout")
	}
}

func TestSyslogOctetCountIsBytes(t *testing.T) {
	srv := newSyslogServer(t, listenTCP(t))
	s := mustNew(t, syslogDrain(agentprotocol.SyslogDrainConfig{Address: srv.ln.Addr().String(), Protocol: "tcp"}), nil)
	// serve() reads exactly <len> bytes, so a wrong count would desync the second message.
	if err := s.Send(t.Context(), []Entry{{TS: base, Text: strings.Repeat("é✓", 50)}, {TS: base, Text: "after"}}); err != nil {
		t.Fatal(err)
	}
	if m := srv.next(t); !strings.HasSuffix(m, strings.Repeat("é✓", 50)) {
		t.Fatalf("first message = %q", m)
	}
	if m := srv.next(t); !strings.HasSuffix(m, "] after") {
		t.Fatalf("second message = %q", m)
	}
}

func TestSyslogTCPReusesAndRedialsConnection(t *testing.T) {
	srv := newSyslogServer(t, listenTCP(t))
	s := mustNew(t, syslogDrain(agentprotocol.SyslogDrainConfig{Address: srv.ln.Addr().String(), Protocol: "tcp"}), nil)

	send := func(text string) {
		t.Helper()
		if err := s.Send(t.Context(), []Entry{{TS: base, Text: text}}); err != nil {
			t.Fatalf("Send(%q): %v", text, err)
		}
		if m := srv.next(t); !strings.HasSuffix(m, text) {
			t.Fatalf("got %q, want suffix %q", m, text)
		}
	}
	send("one")
	send("two")
	if n := srv.acceptedCount(); n != 1 {
		t.Fatalf("%d connections for two sends, want 1 (reuse)", n)
	}

	srv.closeConns()
	time.Sleep(100 * time.Millisecond) // the sink notices the close
	send("three")
	if n := srv.acceptedCount(); n != 2 {
		t.Fatalf("%d connections after the server closed, want 2 (re-dial)", n)
	}
	send("four")
	if n := srv.acceptedCount(); n != 2 {
		t.Fatalf("%d connections, want the new one reused", n)
	}
}

func TestSyslogDialFailureIsRetryableThenRecovers(t *testing.T) {
	ln := listenTCP(t)
	addr := ln.Addr().String()
	_ = ln.Close()
	s := mustNew(t, syslogDrain(agentprotocol.SyslogDrainConfig{Address: addr, Protocol: "tcp"}), nil)
	err := s.Send(t.Context(), []Entry{{TS: base, Text: "x"}})
	if se := sendErr(t, err); se.Permanent {
		t.Fatalf("dial failure must be retryable: %v", err)
	}

	ln2, err := net.Listen("tcp", addr)
	if err != nil {
		t.Skipf("cannot re-listen on %s: %v", addr, err)
	}
	srv := newSyslogServer(t, ln2)
	if err := s.Send(t.Context(), []Entry{{TS: base, Text: "back"}}); err != nil {
		t.Fatal(err)
	}
	if m := srv.next(t); !strings.HasSuffix(m, "back") {
		t.Fatalf("got %q", m)
	}
}

func TestSyslogCloseThenSend(t *testing.T) {
	srv := newSyslogServer(t, listenTCP(t))
	s := mustNew(t, syslogDrain(agentprotocol.SyslogDrainConfig{Address: srv.ln.Addr().String(), Protocol: "tcp"}), nil)
	if err := s.Send(t.Context(), []Entry{{TS: base, Text: "x"}}); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	if err := s.Send(t.Context(), []Entry{{TS: base, Text: "y"}}); err == nil {
		t.Fatal("Send after Close succeeded")
	}
	if err := s.Close(); err != nil {
		t.Fatalf("second Close: %v", err)
	}
}

// tlsListener serves the certificate of an httptest TLS server and reports the SNI it sees.
func tlsListener(t *testing.T) (net.Listener, chan string) {
	t.Helper()
	ts := httptest.NewUnstartedServer(http.NotFoundHandler())
	ts.StartTLS()
	t.Cleanup(ts.Close)
	sni := make(chan string, 8)
	cfg := &tls.Config{
		Certificates: ts.TLS.Certificates,
		GetConfigForClient: func(h *tls.ClientHelloInfo) (*tls.Config, error) {
			sni <- h.ServerName
			return nil, nil
		},
	}
	ln, err := tls.Listen("tcp", "127.0.0.1:0", cfg)
	if err != nil {
		t.Fatal(err)
	}
	return ln, sni
}

func TestSyslogTLS(t *testing.T) {
	ln, sni := tlsListener(t)
	srv := newSyslogServer(t, ln)
	s := mustNew(t, syslogDrain(agentprotocol.SyslogDrainConfig{
		Address: ln.Addr().String(), Protocol: agentprotocol.SyslogTCPTLS,
		TLSServerName: "example.com", TLSInsecureSkipVerify: true,
	}), nil)
	if err := s.Send(t.Context(), []Entry{syslogEntry()}); err != nil {
		t.Fatal(err)
	}
	if got := srv.next(t); got != "<12>"+syslogEntryWire {
		t.Fatalf("got %q", got)
	}
	if got := <-sni; got != "example.com" {
		t.Fatalf("SNI = %q, want the TLSServerName override", got)
	}
	// The TLS connection is reused too.
	if err := s.Send(t.Context(), []Entry{{TS: base, Text: "again"}}); err != nil {
		t.Fatal(err)
	}
	if m := srv.next(t); !strings.HasSuffix(m, "again") {
		t.Fatalf("got %q", m)
	}
	if n := srv.acceptedCount(); n != 1 {
		t.Fatalf("%d TLS connections, want 1", n)
	}
	// And re-dialled after the server closes it.
	srv.closeConns()
	time.Sleep(100 * time.Millisecond)
	if err := s.Send(t.Context(), []Entry{{TS: base, Text: "redialed"}}); err != nil {
		t.Fatal(err)
	}
	if m := srv.next(t); !strings.HasSuffix(m, "redialed") {
		t.Fatalf("got %q", m)
	}
	if n := srv.acceptedCount(); n != 2 {
		t.Fatalf("%d TLS connections, want 2", n)
	}
}

func TestSyslogTLSVerifiesCertificateUnlessSkipped(t *testing.T) {
	ln, _ := tlsListener(t)
	newSyslogServer(t, ln)
	s := mustNew(t, syslogDrain(agentprotocol.SyslogDrainConfig{
		Address: ln.Addr().String(), Protocol: agentprotocol.SyslogTCPTLS, TLSServerName: "example.com",
	}), nil)
	err := s.Send(t.Context(), []Entry{{TS: base, Text: "x"}})
	if se := sendErr(t, err); se.Permanent {
		t.Fatalf("a TLS failure is retryable: %v", err)
	}
	if !strings.Contains(err.Error(), "certificate") {
		t.Fatalf("expected a certificate error, got %v", err)
	}
}

func TestSyslogUDP(t *testing.T) {
	pc, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer pc.Close()
	s := mustNew(t, syslogDrain(agentprotocol.SyslogDrainConfig{Address: pc.LocalAddr().String(), Protocol: agentprotocol.SyslogUDP, Facility: 23}), nil)

	read := func() []byte {
		t.Helper()
		_ = pc.SetReadDeadline(time.Now().Add(3 * time.Second))
		buf := make([]byte, 65535)
		n, _, err := pc.ReadFrom(buf)
		if err != nil {
			t.Fatal(err)
		}
		return buf[:n]
	}

	long := Entry{TS: base, Text: strings.Repeat("é", 20000), Level: "error"}
	if err := s.Send(t.Context(), []Entry{syslogEntry(), long, {TS: base, Text: "last", Level: "debug"}}); err != nil {
		t.Fatal(err)
	}
	// Facility 23 (local7): 23*8 + severity; no octet count over UDP.
	if got := string(read()); got != "<188>"+syslogEntryWire {
		t.Fatalf("datagram 1 = %q", got)
	}
	d2 := read()
	if len(d2) > 8192 || len(d2) < 8190 {
		t.Fatalf("datagram 2 is %d bytes, want 8190..8192", len(d2))
	}
	if !utf8.Valid(d2) || !strings.HasPrefix(string(d2), "<187>1 ") {
		t.Fatalf("datagram 2 is not valid UTF-8 / wrong PRI: %.40q", d2)
	}
	if got := string(read()); !strings.HasPrefix(got, "<191>1 ") || !strings.HasSuffix(got, "] last") {
		t.Fatalf("datagram 3 = %q", got)
	}
	// The socket is reused for later batches.
	if err := s.Send(t.Context(), []Entry{{TS: base, Text: "later"}}); err != nil {
		t.Fatal(err)
	}
	if got := string(read()); !strings.HasSuffix(got, "] later") {
		t.Fatalf("datagram 4 = %q", got)
	}
}

func TestTruncateUTF8(t *testing.T) {
	b := []byte("aé✓") // 1 + 2 + 3 bytes
	for n, want := range map[int]string{6: "aé✓", 5: "aé", 4: "aé", 3: "aé", 2: "a", 1: "a", 0: ""} {
		if got := string(truncateUTF8(b, n)); got != want {
			t.Errorf("truncateUTF8(%d) = %q, want %q", n, got, want)
		}
	}
}

func TestAsciiToken(t *testing.T) {
	for _, tc := range []struct {
		in       string
		limit    int
		fallback string
		want     string
	}{
		{"my srv", 255, "-", "my_srv"},
		{"", 255, "-", "-"},
		{"ünï", 255, "-", "_n_"},
		{"abcdef", 3, "-", "abc"},
		{"ok-host.1", 255, "-", "ok-host.1"},
	} {
		if got := asciiToken(tc.in, tc.limit, tc.fallback); got != tc.want {
			t.Errorf("asciiToken(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
	if got := asciiToken(strings.Repeat("a", 300), 255, "-"); len(got) != 255 {
		t.Errorf("hostname length %d", len(got))
	}
}
