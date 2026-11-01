// Package secretbox encrypts small secrets at rest with AES-256-GCM.
//
// Ciphertext formats (docs/security/secrets-at-rest.md):
//
//	v2: 0x02 || keyID (4) || nonce (12) || sealed
//	    AAD = 0x02 || keyID || "<table>:<column>:<rowID>"
//	v1: 0x01 || nonce (12) || sealed        (legacy; AAD = 0x01; no key id)
//
// The key id selects a key of the ring; the AAD binds the value to the column
// and row it lives in, so a ciphertext copied elsewhere by someone who can
// write to the database does not open. v1 values stay readable (any key of the
// ring, no context check) until `nebulactrl-server rekey` rewrites them.
package secretbox

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"crypto/hkdf"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
)

// Ciphertext format versions (the first byte of every ciphertext).
const (
	FormatV1 byte = 1
	FormatV2 byte = 2
)

// KeySize is the required key length in bytes.
const KeySize = 32

// KeyIDSize is the length of the key id in a v2 ciphertext header.
const KeyIDSize = 4

// HeaderSize is the length of the v2 header (version byte + key id).
const HeaderSize = 1 + KeyIDSize

// MaxPlaintextSize is the largest value Seal accepts. Secrets are small; the
// bound keeps the ciphertext buffer size computation far from overflow.
const MaxPlaintextSize = 64 << 20

// ErrDecrypt is returned for every failed Open so callers cannot distinguish
// failure causes.
var ErrDecrypt = errors.New("secretbox: decryption failed")

// ErrUnknownKey is returned by Open when a v2 ciphertext names a key id that is
// not in the ring. It matches ErrDecrypt with errors.Is; it carries no secret
// and is for operators (the key was removed too early).
var ErrUnknownKey = fmt.Errorf("%w: key id is not in the key ring", ErrDecrypt)

type ringKey struct {
	id    [KeyIDSize]byte
	aead  cipher.AEAD
	fpKey []byte
}

// Box seals values with the current key of its ring and opens values sealed
// with any key of the ring.
type Box struct {
	keys []ringKey // keys[0] is the current key
}

// New returns a Box with a single 32-byte key.
func New(key []byte) (*Box, error) { return NewRing(key) }

// NewRing returns a Box that seals with current and opens with current or any
// of previous. Key ids (a fingerprint of each key) must be distinct.
func NewRing(current []byte, previous ...[]byte) (*Box, error) {
	b := &Box{keys: make([]ringKey, 0, 1+len(previous))}
	for i, key := range append([][]byte{current}, previous...) {
		k, err := newRingKey(key)
		if err != nil {
			if i == 0 {
				return nil, err
			}
			return nil, fmt.Errorf("previous key %d: %w", i, err)
		}
		for _, o := range b.keys {
			if o.id == k.id {
				return nil, errors.New("secretbox: the key ring contains the same key twice")
			}
		}
		b.keys = append(b.keys, k)
	}
	return b, nil
}

func newRingKey(key []byte) (ringKey, error) {
	if len(key) != KeySize {
		return ringKey{}, fmt.Errorf("secretbox: key must be %d bytes, got %d", KeySize, len(key))
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return ringKey{}, fmt.Errorf("secretbox: %w", err)
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return ringKey{}, fmt.Errorf("secretbox: %w", err)
	}
	fp, err := hkdf.Key(sha256.New, key, nil, "nebulactrl variable-fingerprint", 32)
	if err != nil {
		return ringKey{}, fmt.Errorf("secretbox: %w", err)
	}
	id, err := hkdf.Key(sha256.New, key, nil, "nebulactrl secret-key-id", KeyIDSize)
	if err != nil {
		return ringKey{}, fmt.Errorf("secretbox: %w", err)
	}
	k := ringKey{aead: aead, fpKey: fp}
	copy(k.id[:], id)
	return k, nil
}

// KeyID returns the id of the current key (8 hex characters), the value shown
// in logs and by `rekey`. It is derived one way from the key.
func (b *Box) KeyID() string { return hex.EncodeToString(b.keys[0].id[:]) }

// KeyIDs returns the ids of every key of the ring, current first.
func (b *Box) KeyIDs() []string {
	out := make([]string, len(b.keys))
	for i, k := range b.keys {
		out[i] = hex.EncodeToString(k.id[:])
	}
	return out
}

// Header returns the v2 header of the current key: version byte and key id.
// A stored ciphertext starts with these bytes iff it is already on the current
// key.
func (b *Box) Header() []byte {
	h := make([]byte, 0, HeaderSize)
	h = append(h, FormatV2)
	return append(h, b.keys[0].id[:]...)
}

// NeedsRekey reports whether ct is not a v2 value sealed with the current key.
func (b *Box) NeedsRekey(ct []byte) bool {
	return len(ct) < HeaderSize || !bytes.Equal(ct[:HeaderSize], b.Header())
}

// Fingerprint returns 8 hex characters of HMAC-SHA256 over s under a key
// derived from the current key with HKDF. It answers "did the secret change,
// and is it the same as an earlier one?" without keeping the value; a database
// dump alone cannot be used to brute-force low-entropy values. After a key
// rotation compare stored fingerprints with FingerprintMatches.
func (b *Box) Fingerprint(s string) string { return fingerprint(b.keys[0].fpKey, s) }

// FingerprintMatches reports whether stored is the fingerprint of s under the
// current key or any previous key of the ring.
func (b *Box) FingerprintMatches(stored, s string) bool {
	for _, k := range b.keys {
		if hmac.Equal([]byte(stored), []byte(fingerprint(k.fpKey, s))) {
			return true
		}
	}
	return false
}

func fingerprint(key []byte, s string) string {
	m := hmac.New(sha256.New, key)
	m.Write([]byte(s))
	return hex.EncodeToString(m.Sum(nil)[:4])
}

// Digest returns the full HMAC-SHA256 of s (64 hex characters) under the same
// derived key as Fingerprint, for identity checks that must not collide (the
// build reuse hash). Like Fingerprint it cannot be reversed from a database
// dump. It changes when the current key changes (one cache miss).
func (b *Box) Digest(s string) string {
	m := hmac.New(sha256.New, b.keys[0].fpKey)
	m.Write([]byte(s))
	return hex.EncodeToString(m.Sum(nil))
}

// ParseKey decodes a base64 (std or raw, url or std alphabet) 32-byte key.
func ParseKey(s string) ([]byte, error) {
	s = strings.TrimSpace(s)
	for _, enc := range []*base64.Encoding{base64.StdEncoding, base64.RawStdEncoding, base64.URLEncoding, base64.RawURLEncoding} {
		if b, err := enc.DecodeString(s); err == nil {
			if len(b) != KeySize {
				return nil, fmt.Errorf("secretbox: key must decode to %d bytes, got %d", KeySize, len(b))
			}
			return b, nil
		}
	}
	return nil, errors.New("secretbox: key is not valid base64")
}

// ParseKeyList decodes a list of keys separated by commas, spaces or newlines.
// An empty list is valid.
func ParseKeyList(s string) ([][]byte, error) {
	fields := strings.FieldsFunc(s, func(r rune) bool { return r == ',' || r == ' ' || r == '\t' || r == '\n' || r == '\r' })
	out := make([][]byte, 0, len(fields))
	for i, f := range fields {
		k, err := ParseKey(f)
		if err != nil {
			return nil, fmt.Errorf("key %d: %w", i+1, err)
		}
		out = append(out, k)
	}
	return out, nil
}

// GenerateKey returns a fresh random key encoded as std base64.
func GenerateKey() (string, error) {
	key := make([]byte, KeySize)
	if _, err := rand.Read(key); err != nil {
		return "", err
	}
	defer Wipe(key)
	return base64.StdEncoding.EncodeToString(key), nil
}

// Wipe zeroes b. Use it on plaintext buffers that are no longer needed; Go
// strings cannot be wiped, so this only helps where a value stays a []byte.
func Wipe(b []byte) { clear(b) }

// aad builds the additional data of a v2 value: header || "table:column:rowID".
func aad(header []byte, col Column, rowID string) []byte {
	out := make([]byte, 0, len(header)+len(col.Table)+len(col.Column)+len(rowID)+2)
	out = append(out, header...)
	out = append(out, col.Table...)
	out = append(out, ':')
	out = append(out, col.Column...)
	out = append(out, ':')
	return append(out, rowID...)
}

// Seal encrypts plain with the current key, bound to column col and row rowID.
func (b *Box) Seal(col Column, rowID string, plain []byte) ([]byte, error) {
	if col.Table == "" || col.Column == "" || rowID == "" {
		return nil, errors.New("secretbox: seal needs a column and a row id")
	}
	if len(plain) > MaxPlaintextSize {
		return nil, fmt.Errorf("secretbox: value is larger than %d bytes", MaxPlaintextSize)
	}
	k := b.keys[0]
	ns := k.aead.NonceSize()
	out := make([]byte, HeaderSize+ns, HeaderSize+ns+len(plain)+k.aead.Overhead())
	out[0] = FormatV2
	copy(out[1:], k.id[:])
	nonce := out[HeaderSize:]
	if _, err := rand.Read(nonce); err != nil {
		return nil, fmt.Errorf("secretbox: nonce: %w", err)
	}
	return k.aead.Seal(out, nonce, plain, aad(out[:HeaderSize], col, rowID)), nil //nolint:gosec // the nonce is filled from crypto/rand above
}

// Open decrypts a value produced by Seal for the same column and row. v1
// values (sealed before context binding) open under any key of the ring and
// ignore col and rowID.
func (b *Box) Open(col Column, rowID string, ct []byte) ([]byte, error) {
	if len(ct) == 0 {
		return nil, ErrDecrypt
	}
	switch ct[0] {
	case FormatV2:
		if len(ct) < HeaderSize {
			return nil, ErrDecrypt
		}
		for _, k := range b.keys {
			if !bytes.Equal(k.id[:], ct[1:HeaderSize]) {
				continue
			}
			ns := k.aead.NonceSize()
			if len(ct) < HeaderSize+ns+k.aead.Overhead() {
				return nil, ErrDecrypt
			}
			plain, err := k.aead.Open(nil, ct[HeaderSize:HeaderSize+ns], ct[HeaderSize+ns:], aad(ct[:HeaderSize], col, rowID))
			if err != nil {
				return nil, ErrDecrypt
			}
			return plain, nil
		}
		return nil, ErrUnknownKey
	case FormatV1:
		for _, k := range b.keys {
			ns := k.aead.NonceSize()
			if len(ct) < 1+ns+k.aead.Overhead() {
				return nil, ErrDecrypt
			}
			if plain, err := k.aead.Open(nil, ct[1:1+ns], ct[1+ns:], ct[:1]); err == nil {
				return plain, nil
			}
		}
		return nil, ErrDecrypt
	}
	return nil, ErrDecrypt
}

// SealString encrypts s.
func (b *Box) SealString(col Column, rowID string, s string) ([]byte, error) {
	buf := []byte(s)
	defer Wipe(buf)
	return b.Seal(col, rowID, buf)
}

// OpenString decrypts ct to a string. The intermediate plaintext buffer is
// zeroed.
func (b *Box) OpenString(col Column, rowID string, ct []byte) (string, error) {
	p, err := b.Open(col, rowID, ct)
	if err != nil {
		return "", err
	}
	s := string(p)
	Wipe(p)
	return s, nil
}
