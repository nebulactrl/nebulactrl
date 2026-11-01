package auth

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base32"
	"encoding/hex"
	"fmt"
	"strings"
)

// RecoveryCodeCount is the number of single-use codes issued per enrolment.
const RecoveryCodeCount = 10

// recoveryEncoding is base32 without padding; codes are shown lowercase in four
// groups of four characters (80 bits of entropy, which makes an unsalted SHA-256
// of the code unattackable offline).
var recoveryEncoding = base32.StdEncoding.WithPadding(base32.NoPadding)

// NewRecoveryCodes returns RecoveryCodeCount fresh codes formatted xxxx-xxxx-xxxx-xxxx.
func NewRecoveryCodes() ([]string, error) {
	out := make([]string, RecoveryCodeCount)
	for i := range out {
		b := make([]byte, 10) // 80 bits -> 16 base32 characters
		if _, err := rand.Read(b); err != nil {
			return nil, fmt.Errorf("recovery code: %w", err)
		}
		s := strings.ToLower(recoveryEncoding.EncodeToString(b))
		out[i] = s[0:4] + "-" + s[4:8] + "-" + s[8:12] + "-" + s[12:16]
	}
	return out, nil
}

// normalizeRecoveryCode lowercases the code and strips separators and spaces.
func normalizeRecoveryCode(c string) string {
	c = strings.ToLower(strings.TrimSpace(c))
	c = strings.NewReplacer("-", "", " ", "").Replace(c)
	return c
}

// HashRecoveryCode returns the stored form of a code.
func HashRecoveryCode(c string) string {
	sum := sha256.Sum256([]byte(normalizeRecoveryCode(c)))
	return hex.EncodeToString(sum[:])
}
