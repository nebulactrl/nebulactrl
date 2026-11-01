// Package releasesigntest signs with a key of its own, for tests of the release
// verification (internal/releasesign) and of the installer that embeds it.
package releasesigntest

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"

	"golang.org/x/crypto/blake2b"
)

// The minisign layout of releasesign: algorithm tag and key id length.
const (
	algPrehashed = "ED"
	keyIDLen     = 8
)

// Signer makes minisign-compatible prehashed signatures. Release signing is done
// by minisign in CI.
type Signer struct {
	priv ed25519.PrivateKey
	id   [keyIDLen]byte
}

// NewSigner makes a random key. It returns the signer and the base64 minisign
// public key to verify with.
func NewSigner() (*Signer, string) {
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		panic(err)
	}
	s := &Signer{priv: priv}
	if _, err := rand.Read(s.id[:]); err != nil {
		panic(err)
	}
	raw := append(append([]byte("Ed"), s.id[:]...), pub...)
	return s, base64.StdEncoding.EncodeToString(raw)
}

// Sign returns the contents of the .minisig file for data.
func (s *Signer) Sign(data []byte, trustedComment string) []byte {
	sum := blake2b.Sum512(data)
	sig := ed25519.Sign(s.priv, sum[:])
	raw := append(append([]byte(algPrehashed), s.id[:]...), sig...)
	global := ed25519.Sign(s.priv, append(append([]byte{}, sig...), trustedComment...))
	return []byte("untrusted comment: test signature\n" +
		base64.StdEncoding.EncodeToString(raw) + "\n" +
		"trusted comment: " + trustedComment + "\n" +
		base64.StdEncoding.EncodeToString(global) + "\n")
}
