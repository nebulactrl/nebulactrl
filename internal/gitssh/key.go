package gitssh

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/rsa"
	"encoding/pem"
	"errors"
	"fmt"
	"strings"

	"golang.org/x/crypto/ssh"
)

// MaxPrivateKeyBytes bounds an uploaded private key.
const MaxPrivateKeyBytes = 16 << 10

// minRSABits is the smallest RSA modulus an uploaded key may have.
const minRSABits = 2048

// KeyPair is a deploy key. PrivatePEM is the OpenSSH-format private key and is
// a secret; every other field is public.
type KeyPair struct {
	PrivatePEM []byte
	// PublicKey is the authorized_keys line ("ssh-ed25519 AAAA… comment").
	PublicKey string
	// Fingerprint is the SHA256 fingerprint ("SHA256:…").
	Fingerprint string
	// Type is the key algorithm ("ssh-ed25519", "ssh-rsa", "ecdsa-sha2-nistp256").
	Type string
}

// Generate creates an Ed25519 key pair; comment ends up in the public key line.
func Generate(comment string) (KeyPair, error) {
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return KeyPair{}, fmt.Errorf("generate ed25519 key: %w", err)
	}
	block, err := ssh.MarshalPrivateKey(priv, comment)
	if err != nil {
		return KeyPair{}, fmt.Errorf("marshal private key: %w", err)
	}
	sshPub, err := ssh.NewPublicKey(pub)
	if err != nil {
		return KeyPair{}, fmt.Errorf("public key: %w", err)
	}
	return KeyPair{
		PrivatePEM:  pem.EncodeToMemory(block),
		PublicKey:   authorizedLine(sshPub, comment),
		Fingerprint: ssh.FingerprintSHA256(sshPub),
		Type:        sshPub.Type(),
	}, nil
}

// ParsePrivateKey validates an uploaded private key (OpenSSH or PEM format,
// Ed25519, ECDSA or RSA of at least 2048 bits, without passphrase) and derives
// its public half. The returned PEM is the input, normalized to end in a newline.
func ParsePrivateKey(raw []byte, comment string) (KeyPair, error) {
	if len(raw) > MaxPrivateKeyBytes {
		return KeyPair{}, fmt.Errorf("private key is larger than %d KiB", MaxPrivateKeyBytes>>10)
	}
	raw = []byte(strings.TrimSpace(strings.ReplaceAll(string(raw), "\r\n", "\n")) + "\n")
	signer, err := ssh.ParsePrivateKey(raw)
	if err != nil {
		var missing *ssh.PassphraseMissingError
		if errors.As(err, &missing) {
			return KeyPair{}, errors.New("the private key is passphrase-protected; remove the passphrase (ssh-keygen -p -N \"\" -f <key>) or generate a key here")
		}
		return KeyPair{}, errors.New("not a valid unencrypted private key (expected an OpenSSH or PEM private key)")
	}
	pub := signer.PublicKey()
	switch pub.Type() {
	case ssh.KeyAlgoED25519, ssh.KeyAlgoECDSA256, ssh.KeyAlgoECDSA384, ssh.KeyAlgoECDSA521:
	case ssh.KeyAlgoRSA:
		if cp, ok := pub.(ssh.CryptoPublicKey); ok {
			if rk, ok := cp.CryptoPublicKey().(*rsa.PublicKey); ok && rk.N.BitLen() < minRSABits {
				return KeyPair{}, fmt.Errorf("RSA keys must be at least %d bits", minRSABits)
			}
		}
	default:
		return KeyPair{}, fmt.Errorf("unsupported key type %s (use Ed25519, ECDSA or RSA)", pub.Type())
	}
	return KeyPair{
		PrivatePEM:  raw,
		PublicKey:   authorizedLine(pub, comment),
		Fingerprint: ssh.FingerprintSHA256(pub),
		Type:        pub.Type(),
	}, nil
}

func authorizedLine(pub ssh.PublicKey, comment string) string {
	line := strings.TrimSpace(string(ssh.MarshalAuthorizedKey(pub)))
	if comment = sanitizeComment(comment); comment != "" {
		line += " " + comment
	}
	return line
}

// sanitizeComment keeps a key comment on one line without spaces.
func sanitizeComment(c string) string {
	return strings.Map(func(r rune) rune {
		if r <= ' ' || r == 0x7f {
			return '-'
		}
		return r
	}, strings.TrimSpace(c))
}
