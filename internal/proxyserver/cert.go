package proxyserver

import (
	"bytes"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"encoding/pem"
	"fmt"
	"strings"
	"time"

	"github.com/nebulactrl/nebulactrl/internal/apierr"
)

// MaxPEMBytes bounds each uploaded PEM (certificate chain and key).
const MaxPEMBytes = 64 * 1024

// clockSkew tolerates a certificate issued moments ago on a host with a slow clock.
const clockSkew = 5 * time.Minute

// ParsedCertificate is a validated upload. KeyPEM is the normalized private key
// (one block) and must be sealed before it is stored.
type ParsedCertificate struct {
	CertPEM     string // normalized: CERTIFICATE blocks only, leaf first
	KeyPEM      string
	Domains     []string // DNS SANs then IP SANs, lower-case
	Issuer      string
	NotBefore   time.Time
	NotAfter    time.Time
	Fingerprint string // SHA-256 of the leaf DER, lower-case hex
}

func certErr(code, msg string) error { return apierr.Unprocessable(code, msg) }

// ParseCertificate validates a certificate chain and its private key. The leaf
// is the first certificate. It rejects encrypted or mismatching keys, expired
// and not-yet-valid leaves and certificates without a SAN. It never echoes key
// material in an error.
func ParseCertificate(certPEM, keyPEM string, now time.Time) (*ParsedCertificate, error) {
	if len(certPEM) > MaxPEMBytes || len(keyPEM) > MaxPEMBytes {
		return nil, certErr("pem_too_large", fmt.Sprintf("certPem and keyPem must each be at most %d KB", MaxPEMBytes/1024))
	}
	if strings.TrimSpace(certPEM) == "" || strings.TrimSpace(keyPEM) == "" {
		return nil, certErr("invalid_certificate", "certPem and keyPem are required")
	}

	var chain [][]byte
	rest := []byte(certPEM)
	for {
		var b *pem.Block
		b, rest = pem.Decode(rest)
		if b == nil {
			break
		}
		switch {
		case b.Type == "CERTIFICATE":
			chain = append(chain, b.Bytes)
		case strings.Contains(b.Type, "PRIVATE KEY"):
			return nil, certErr("invalid_certificate", "certPem contains a private key; send the key in keyPem only")
		default:
			// Other blocks (e.g. TRUSTED CERTIFICATE, comments) are not accepted: be exact.
			return nil, certErr("invalid_certificate", fmt.Sprintf("certPem contains an unsupported PEM block %q", b.Type))
		}
	}
	if len(chain) == 0 {
		return nil, certErr("invalid_certificate", "certPem contains no CERTIFICATE block")
	}
	if len(chain) > 10 {
		return nil, certErr("invalid_certificate", "certPem has more than 10 certificates")
	}
	certs := make([]*x509.Certificate, len(chain))
	for i, der := range chain {
		c, err := x509.ParseCertificate(der)
		if err != nil {
			return nil, certErr("invalid_certificate", fmt.Sprintf("certificate %d of the chain cannot be parsed", i+1))
		}
		certs[i] = c
	}
	leaf := certs[0]

	kb, _ := pem.Decode([]byte(keyPEM))
	if kb == nil {
		return nil, certErr("invalid_key", "keyPem contains no PEM block")
	}
	if kb.Type == "ENCRYPTED PRIVATE KEY" || strings.Contains(kb.Headers["Proc-Type"], "ENCRYPTED") {
		return nil, certErr("encrypted_key", "the private key is encrypted; remove the passphrase first (openssl pkey -in key.pem -out key-plain.pem)")
	}
	if !strings.HasSuffix(kb.Type, "PRIVATE KEY") {
		return nil, certErr("invalid_key", "keyPem must be a PKCS#8, PKCS#1 (RSA) or SEC1 (EC) private key")
	}
	normKey := pem.EncodeToMemory(&pem.Block{Type: kb.Type, Bytes: kb.Bytes})
	normCert := &bytes.Buffer{}
	for _, der := range chain {
		_ = pem.Encode(normCert, &pem.Block{Type: "CERTIFICATE", Bytes: der})
	}
	if _, err := tls.X509KeyPair(normCert.Bytes(), normKey); err != nil {
		if strings.Contains(err.Error(), "does not match") {
			return nil, certErr("key_mismatch", "the private key does not match the certificate")
		}
		return nil, certErr("invalid_key", "the private key cannot be parsed (supported: RSA, ECDSA, Ed25519)")
	}

	if now.After(leaf.NotAfter) {
		return nil, certErr("certificate_expired", fmt.Sprintf("the certificate expired on %s", leaf.NotAfter.UTC().Format("2006-01-02")))
	}
	if now.Add(clockSkew).Before(leaf.NotBefore) {
		return nil, certErr("certificate_not_yet_valid", fmt.Sprintf("the certificate is not valid before %s", leaf.NotBefore.UTC().Format(time.RFC3339)))
	}

	var domains []string
	seen := map[string]bool{}
	for _, d := range leaf.DNSNames {
		d = strings.ToLower(strings.TrimSuffix(d, "."))
		if d != "" && !seen[d] {
			seen[d] = true
			domains = append(domains, d)
		}
	}
	for _, ip := range leaf.IPAddresses {
		s := ip.String()
		if !seen[s] {
			seen[s] = true
			domains = append(domains, s)
		}
	}
	if len(domains) == 0 {
		return nil, certErr("certificate_no_san", "the certificate has no Subject Alternative Names (DNS or IP); browsers ignore the common name")
	}

	issuer := leaf.Issuer.CommonName
	if issuer == "" {
		issuer = leaf.Issuer.String()
	}
	sum := sha256.Sum256(leaf.Raw)
	return &ParsedCertificate{
		CertPEM: normCert.String(), KeyPEM: string(normKey), Domains: domains, Issuer: issuer,
		NotBefore: leaf.NotBefore.UTC(), NotAfter: leaf.NotAfter.UTC(), Fingerprint: hex.EncodeToString(sum[:]),
	}, nil
}
