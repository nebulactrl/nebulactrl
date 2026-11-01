package auth

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base32"
	"strings"
)

// TokenPrefix starts every API token.
const TokenPrefix = "nct_"

var tokenEnc = base32.StdEncoding.WithPadding(base32.NoPadding)

// GenerateToken returns a new raw token ("nct_" + 52 base32 chars encoding 32
// random bytes), its SHA-256 hash and the 8-char display prefix.
func GenerateToken() (raw string, hash []byte, prefix string, err error) {
	b := make([]byte, 32)
	if _, err = rand.Read(b); err != nil {
		return "", nil, "", err
	}
	body := strings.ToLower(tokenEnc.EncodeToString(b))
	raw = TokenPrefix + body
	return raw, HashToken(raw), body[:8], nil
}

// HashToken returns the SHA-256 of a raw token.
func HashToken(raw string) []byte {
	h := sha256.Sum256([]byte(raw))
	return h[:]
}
