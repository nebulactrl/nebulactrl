package gitssh

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/pem"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"golang.org/x/crypto/ssh"
)

func TestGenerateProducesAnOpenSSHEd25519Key(t *testing.T) {
	kp, err := Generate("nebulactrl:web")
	require.NoError(t, err)

	require.True(t, strings.HasPrefix(string(kp.PrivatePEM), "-----BEGIN OPENSSH PRIVATE KEY-----\n"), "private key format")
	require.True(t, strings.HasSuffix(string(kp.PrivatePEM), "-----END OPENSSH PRIVATE KEY-----\n"))
	require.Equal(t, "ssh-ed25519", kp.Type)

	// The public line is a valid authorized_keys entry with our comment.
	require.True(t, strings.HasPrefix(kp.PublicKey, "ssh-ed25519 AAAA"), kp.PublicKey)
	pub, comment, _, rest, err := ssh.ParseAuthorizedKey([]byte(kp.PublicKey))
	require.NoError(t, err)
	require.Equal(t, "nebulactrl:web", comment)
	require.Empty(t, rest)
	require.Equal(t, ssh.FingerprintSHA256(pub), kp.Fingerprint)
	require.True(t, strings.HasPrefix(kp.Fingerprint, "SHA256:"))

	// The private half parses, matches the public half and signs.
	signer, err := ssh.ParsePrivateKey(kp.PrivatePEM)
	require.NoError(t, err)
	require.Equal(t, pub.Marshal(), signer.PublicKey().Marshal())
	sig, err := signer.Sign(rand.Reader, []byte("payload"))
	require.NoError(t, err)
	require.NoError(t, pub.Verify([]byte("payload"), sig))
}

func TestGenerateIsUnique(t *testing.T) {
	a, err := Generate("")
	require.NoError(t, err)
	b, err := Generate("")
	require.NoError(t, err)
	require.NotEqual(t, a.Fingerprint, b.Fingerprint)
	require.NotEqual(t, a.PrivatePEM, b.PrivatePEM)
	require.Len(t, strings.Fields(a.PublicKey), 2, "an empty comment adds no trailing field")
}

func TestGenerateSanitizesTheComment(t *testing.T) {
	kp, err := Generate("my key\nwith spaces")
	require.NoError(t, err)
	require.Equal(t, 3, len(strings.Fields(kp.PublicKey)), kp.PublicKey)
	require.NotContains(t, kp.PublicKey, "\n")
}

func TestParsePrivateKeyRoundTripsAGeneratedKey(t *testing.T) {
	kp, err := Generate("x")
	require.NoError(t, err)
	got, err := ParsePrivateKey(kp.PrivatePEM, "uploaded")
	require.NoError(t, err)
	require.Equal(t, kp.Fingerprint, got.Fingerprint)
	require.Equal(t, "ssh-ed25519", got.Type)
	require.True(t, strings.HasSuffix(got.PublicKey, " uploaded"))
}

func TestParsePrivateKeyNormalizesLineEndings(t *testing.T) {
	kp, err := Generate("")
	require.NoError(t, err)
	crlf := strings.ReplaceAll(strings.TrimSpace(string(kp.PrivatePEM)), "\n", "\r\n")
	got, err := ParsePrivateKey([]byte(crlf), "")
	require.NoError(t, err)
	require.Equal(t, kp.Fingerprint, got.Fingerprint)
	require.NotContains(t, string(got.PrivatePEM), "\r")
	require.True(t, strings.HasSuffix(string(got.PrivatePEM), "\n"))
}

func TestParsePrivateKeyRSA(t *testing.T) {
	k, err := rsa.GenerateKey(rand.Reader, 2048)
	require.NoError(t, err)
	pemBytes := pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(k)})
	got, err := ParsePrivateKey(pemBytes, "")
	require.NoError(t, err)
	require.Equal(t, "ssh-rsa", got.Type)

	small, err := rsa.GenerateKey(rand.Reader, 1024)
	require.NoError(t, err)
	_, err = ParsePrivateKey(pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(small)}), "")
	require.ErrorContains(t, err, "at least 2048")
}

func TestParsePrivateKeyRejects(t *testing.T) {
	_, priv := mustEd25519(t)
	block, err := ssh.MarshalPrivateKeyWithPassphrase(priv, "", []byte("hunter2"))
	require.NoError(t, err)

	cases := map[string]struct {
		in   []byte
		want string
	}{
		"passphrase":  {pem.EncodeToMemory(block), "passphrase"},
		"garbage":     {[]byte("not a key"), "not a valid"},
		"public key":  {[]byte("ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIOMqqnkVzrm0SdG6UOoqKLsabgH5C9okWi0dh2l9GKJl x"), "not a valid"},
		"empty":       {nil, "not a valid"},
		"way too big": {[]byte(strings.Repeat("A", MaxPrivateKeyBytes+1)), "larger than"},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := ParsePrivateKey(c.in, "")
			require.ErrorContains(t, err, c.want)
			// An error never echoes key material.
			require.NotContains(t, err.Error(), "hunter2")
		})
	}
}

func mustEd25519(t *testing.T) (ed25519.PublicKey, ed25519.PrivateKey) {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	return pub, priv
}
