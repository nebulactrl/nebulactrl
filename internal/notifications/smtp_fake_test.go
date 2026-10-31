package notifications

import (
	"bufio"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"fmt"
	"math/big"
	"net"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// fakeSMTP is a minimal in-process SMTP server: EHLO, STARTTLS, AUTH PLAIN and
// LOGIN, MAIL, RCPT, DATA, QUIT. It records what a client sent.
type fakeSMTP struct {
	t          *testing.T
	ln         net.Listener
	cert       tls.Certificate
	implicit   bool     // TLS from the first byte
	starttls   bool     // advertise STARTTLS
	auth       []string // advertised mechanisms ("PLAIN", "LOGIN"); empty: no AUTH
	user       string
	pass       string
	rejectRcpt map[string]string // address -> full reply line, e.g. "550 no such user"
	failData   string            // reply to the end of DATA, e.g. "451 try later"

	mu       sync.Mutex
	messages []fakeMail
	authed   []string // "user:pass" of successful logins
	sawTLS   []bool   // per session: was the DATA phase encrypted?
	hellos   []string
}

type fakeMail struct {
	From  string
	Rcpts []string
	Data  string
}

func selfSignedCert(t *testing.T) tls.Certificate {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)
	tpl := &x509.Certificate{
		SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "fake-smtp"},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour),
		IPAddresses: []net.IP{net.ParseIP("127.0.0.1")}, DNSNames: []string{"localhost"},
		KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, tpl, tpl, &key.PublicKey, key)
	require.NoError(t, err)
	return tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key}
}

func newFakeSMTP(t *testing.T, mut func(*fakeSMTP)) *fakeSMTP {
	t.Helper()
	f := &fakeSMTP{t: t, cert: selfSignedCert(t), rejectRcpt: map[string]string{}}
	if mut != nil {
		mut(f)
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	if f.implicit {
		ln = tls.NewListener(ln, &tls.Config{Certificates: []tls.Certificate{f.cert}})
	}
	f.ln = ln
	t.Cleanup(func() { _ = ln.Close() })
	go f.serve()
	return f
}

func (f *fakeSMTP) port() int { return f.ln.Addr().(*net.TCPAddr).Port }

func (f *fakeSMTP) serve() {
	for {
		c, err := f.ln.Accept()
		if err != nil {
			return
		}
		go f.session(c)
	}
}

func (f *fakeSMTP) session(c net.Conn) {
	defer c.Close()
	_ = c.SetDeadline(time.Now().Add(10 * time.Second))
	encrypted := f.implicit
	r := bufio.NewReader(c)
	w := func(format string, a ...any) { fmt.Fprintf(c, format+"\r\n", a...) }
	w("220 fake.example ESMTP ready")
	var from string
	var rcpts []string
	authed := f.user == "" && len(f.auth) == 0
	for {
		line, err := r.ReadString('\n')
		if err != nil {
			return
		}
		line = strings.TrimRight(line, "\r\n")
		cmd, arg, _ := strings.Cut(line, " ")
		switch strings.ToUpper(cmd) {
		case "EHLO", "HELO":
			f.mu.Lock()
			f.hellos = append(f.hellos, arg)
			f.mu.Unlock()
			lines := []string{"fake.example"}
			if f.starttls && !encrypted {
				lines = append(lines, "STARTTLS")
			}
			if len(f.auth) > 0 {
				lines = append(lines, "AUTH "+strings.Join(f.auth, " "))
			}
			lines = append(lines, "8BITMIME")
			for i, l := range lines {
				if i < len(lines)-1 {
					w("250-%s", l)
				} else {
					w("250 %s", l)
				}
			}
		case "STARTTLS":
			w("220 go ahead")
			tc := tls.Server(c, &tls.Config{Certificates: []tls.Certificate{f.cert}})
			if err := tc.Handshake(); err != nil {
				return
			}
			c, encrypted = tc, true
			r = bufio.NewReader(c)
			w = func(format string, a ...any) { fmt.Fprintf(c, format+"\r\n", a...) }
		case "AUTH":
			mech, initial, _ := strings.Cut(arg, " ")
			var user, pass string
			switch strings.ToUpper(mech) {
			case "PLAIN":
				if initial == "" {
					w("334 ")
					initial, _ = r.ReadString('\n')
					initial = strings.TrimSpace(initial)
				}
				raw, _ := base64.StdEncoding.DecodeString(initial)
				parts := strings.Split(string(raw), "\x00")
				if len(parts) == 3 {
					user, pass = parts[1], parts[2]
				}
			case "LOGIN":
				w("334 %s", base64.StdEncoding.EncodeToString([]byte("Username:")))
				u, _ := r.ReadString('\n')
				w("334 %s", base64.StdEncoding.EncodeToString([]byte("Password:")))
				p, _ := r.ReadString('\n')
				ub, _ := base64.StdEncoding.DecodeString(strings.TrimSpace(u))
				pb, _ := base64.StdEncoding.DecodeString(strings.TrimSpace(p))
				user, pass = string(ub), string(pb)
			default:
				w("504 unsupported mechanism")
				continue
			}
			if user == f.user && pass == f.pass {
				authed = true
				f.mu.Lock()
				f.authed = append(f.authed, user+":"+pass)
				f.mu.Unlock()
				w("235 2.7.0 authenticated")
			} else {
				w("535 5.7.8 authentication failed")
			}
		case "MAIL":
			if !authed {
				w("530 5.7.0 authentication required")
				continue
			}
			from = angle(arg)
			w("250 ok")
		case "RCPT":
			addr := angle(arg)
			if reply, ok := f.rejectRcpt[addr]; ok {
				w("%s", reply)
				continue
			}
			rcpts = append(rcpts, addr)
			w("250 ok")
		case "DATA":
			w("354 end with <CRLF>.<CRLF>")
			var data strings.Builder
			for {
				l, err := r.ReadString('\n')
				if err != nil {
					return
				}
				if l == ".\r\n" {
					break
				}
				data.WriteString(strings.TrimPrefix(l, "."))
			}
			if f.failData != "" {
				w("%s", f.failData)
				continue
			}
			f.mu.Lock()
			f.messages = append(f.messages, fakeMail{From: from, Rcpts: rcpts, Data: data.String()})
			f.sawTLS = append(f.sawTLS, encrypted)
			f.mu.Unlock()
			from, rcpts = "", nil
			w("250 2.0.0 queued")
		case "RSET":
			from, rcpts = "", nil
			w("250 ok")
		case "NOOP":
			w("250 ok")
		case "QUIT":
			w("221 bye")
			return
		default:
			w("502 command not implemented")
		}
	}
}

func (f *fakeSMTP) mails() []fakeMail {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]fakeMail(nil), f.messages...)
}

// emailConfig points an email channel at the server.
func (f *fakeSMTP) emailConfig(tlsMode string) ChannelConfig {
	return ChannelConfig{
		Host: "127.0.0.1", Port: f.port(), TLS: tlsMode, From: "NebulaCtrl <alerts@example.com>",
		To: []string{"ops@example.com", "dev@example.com"}, InsecureSkipVerify: true,
	}
}

// angle returns the address between < and > of "FROM:<a@b> BODY=8BITMIME".
func angle(arg string) string {
	i, j := strings.Index(arg, "<"), strings.Index(arg, ">")
	if i < 0 || j < i {
		return arg
	}
	return arg[i+1 : j]
}
