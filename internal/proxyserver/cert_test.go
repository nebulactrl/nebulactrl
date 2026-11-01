package proxyserver

import (
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/nebulactrl/nebulactrl/internal/apierr"
)

type certOpts struct {
	dns       []string
	ips       []net.IP
	notBefore time.Time
	notAfter  time.Time
	cn        string
}

type genResult struct {
	certPEM string
	keyPEM  string
	chain   string // leaf + issuer
	der     []byte
}

func genCert(t *testing.T, o certOpts) genResult {
	t.Helper()
	if o.notBefore.IsZero() {
		o.notBefore = time.Now().Add(-time.Hour)
	}
	if o.notAfter.IsZero() {
		o.notAfter = time.Now().Add(90 * 24 * time.Hour)
	}
	if o.cn == "" {
		o.cn = "leaf"
	}
	caKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)
	caTmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "Test Root CA"}, NotBefore: time.Now().Add(-48 * time.Hour),
		NotAfter: time.Now().Add(24 * 365 * time.Hour), IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign,
	}
	caDER, err := x509.CreateCertificate(rand.Reader, caTmpl, caTmpl, &caKey.PublicKey, caKey)
	require.NoError(t, err)
	ca, err := x509.ParseCertificate(caDER)
	require.NoError(t, err)

	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(time.Now().UnixNano()), Subject: pkix.Name{CommonName: o.cn}, DNSNames: o.dns, IPAddresses: o.ips,
		NotBefore: o.notBefore, NotAfter: o.notAfter, KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, ca, &key.PublicKey, caKey)
	require.NoError(t, err)
	kb, err := x509.MarshalPKCS8PrivateKey(key)
	require.NoError(t, err)
	leaf := string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}))
	return genResult{
		certPEM: leaf, der: der, keyPEM: string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: kb})),
		chain: leaf + string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: caDER})),
	}
}

func code(t *testing.T, err error) string {
	t.Helper()
	ae, ok := apierr.As(err)
	require.True(t, ok, "not an API error: %v", err)
	return ae.Code
}

func TestParseCertificateValidAndMetadata(t *testing.T) {
	g := genCert(t, certOpts{dns: []string{"App.Example.com", "*.apps.example.com"}, ips: []net.IP{net.ParseIP("203.0.113.5")}})
	p, err := ParseCertificate(g.certPEM, g.keyPEM, time.Now())
	require.NoError(t, err)
	require.Equal(t, []string{"app.example.com", "*.apps.example.com", "203.0.113.5"}, p.Domains)
	require.Equal(t, "Test Root CA", p.Issuer)
	require.Len(t, p.Fingerprint, 64)
	require.WithinDuration(t, time.Now().Add(90*24*time.Hour), p.NotAfter, time.Minute)
	require.Contains(t, p.KeyPEM, "PRIVATE KEY")
}

func TestParseCertificateChainLeafIsFirst(t *testing.T) {
	g := genCert(t, certOpts{dns: []string{"chain.example.com"}})
	p, err := ParseCertificate(g.chain, g.keyPEM, time.Now())
	require.NoError(t, err)
	require.Equal(t, []string{"chain.example.com"}, p.Domains)
	require.Equal(t, 2, strings.Count(p.CertPEM, "BEGIN CERTIFICATE"), "the chain is kept, leaf first")
	require.True(t, strings.HasPrefix(p.CertPEM, g.certPEM))
	single, err := ParseCertificate(g.certPEM, g.keyPEM, time.Now())
	require.NoError(t, err)
	require.Equal(t, single.Fingerprint, p.Fingerprint, "the fingerprint is the leaf's")
}

func TestParseCertificateKeyTypes(t *testing.T) {
	// RSA PKCS#1 and Ed25519 keys are accepted; the pair is verified.
	rk, err := rsa.GenerateKey(rand.Reader, 2048)
	require.NoError(t, err)
	tmpl := &x509.Certificate{SerialNumber: big.NewInt(7), Subject: pkix.Name{CommonName: "rsa"}, DNSNames: []string{"rsa.example.com"}, NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour * 24)}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &rk.PublicKey, rk)
	require.NoError(t, err)
	certPEM := string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}))
	keyPEM := string(pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(rk)}))
	_, err = ParseCertificate(certPEM, keyPEM, time.Now())
	require.NoError(t, err)

	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	tmpl.SerialNumber = big.NewInt(8)
	der, err = x509.CreateCertificate(rand.Reader, tmpl, tmpl, pub, priv)
	require.NoError(t, err)
	kb, err := x509.MarshalPKCS8PrivateKey(priv)
	require.NoError(t, err)
	_, err = ParseCertificate(string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})), string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: kb})), time.Now())
	require.NoError(t, err)
}

func TestParseCertificateRejections(t *testing.T) {
	now := time.Now()
	good := genCert(t, certOpts{dns: []string{"a.example.com"}})
	other := genCert(t, certOpts{dns: []string{"b.example.com"}})

	mk := func(o certOpts) genResult { return genCert(t, o) }
	exp := mk(certOpts{dns: []string{"a.example.com"}, notBefore: now.Add(-48 * time.Hour), notAfter: now.Add(-time.Hour)})
	nyv := mk(certOpts{dns: []string{"a.example.com"}, notBefore: now.Add(48 * time.Hour), notAfter: now.Add(96 * time.Hour)})
	nosan := mk(certOpts{cn: "legacy.example.com"})
	_, err := ParseCertificate(exp.certPEM, exp.keyPEM, now)
	require.Equal(t, "certificate_expired", code(t, err))
	_, err = ParseCertificate(nyv.certPEM, nyv.keyPEM, now)
	require.Equal(t, "certificate_not_yet_valid", code(t, err))
	_, err = ParseCertificate(nosan.certPEM, nosan.keyPEM, now)
	require.Equal(t, "certificate_no_san", code(t, err))
	_, err = ParseCertificate(good.certPEM, other.keyPEM, now)
	require.Equal(t, "key_mismatch", code(t, err))
	_, err = ParseCertificate(good.certPEM+good.keyPEM, good.keyPEM, now)
	require.Equal(t, "invalid_certificate", code(t, err))
	_, err = ParseCertificate("garbage", good.keyPEM, now)
	require.Equal(t, "invalid_certificate", code(t, err))
	_, err = ParseCertificate(good.certPEM, "garbage", now)
	require.Equal(t, "invalid_key", code(t, err))
	_, err = ParseCertificate(good.certPEM, good.certPEM, now)
	require.Equal(t, "invalid_key", code(t, err))
	_, err = ParseCertificate("", "", now)
	require.Equal(t, "invalid_certificate", code(t, err))
	_, err = ParseCertificate(strings.Repeat("x", MaxPEMBytes+1), good.keyPEM, now)
	require.Equal(t, "pem_too_large", code(t, err))
	_, err = ParseCertificate(good.certPEM, strings.Repeat("x", MaxPEMBytes+1), now)
	require.Equal(t, "pem_too_large", code(t, err))
}

func TestParseCertificateRejectsEncryptedKeys(t *testing.T) {
	g := genCert(t, certOpts{dns: []string{"a.example.com"}})
	// PKCS#8 encrypted container.
	enc8 := string(pem.EncodeToMemory(&pem.Block{Type: "ENCRYPTED PRIVATE KEY", Bytes: []byte("opaque-encrypted-der")}))
	_, err := ParseCertificate(g.certPEM, enc8, time.Now())
	require.Equal(t, "encrypted_key", code(t, err))
	// Legacy OpenSSL "Proc-Type: 4,ENCRYPTED" header.
	legacy := string(pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Headers: map[string]string{"Proc-Type": "4,ENCRYPTED", "DEK-Info": "AES-128-CBC,00112233445566778899AABBCCDDEEFF"}, Bytes: []byte("opaque")}))
	_, err = ParseCertificate(g.certPEM, legacy, time.Now())
	require.Equal(t, "encrypted_key", code(t, err))
}

func TestParseCertificateErrorsNeverEchoKeyMaterial(t *testing.T) {
	g := genCert(t, certOpts{dns: []string{"a.example.com"}})
	other := genCert(t, certOpts{dns: []string{"b.example.com"}})
	body := strings.Split(other.keyPEM, "\n")[1]
	_, err := ParseCertificate(g.certPEM, other.keyPEM, time.Now())
	require.Error(t, err)
	require.NotContains(t, err.Error(), body)
}
