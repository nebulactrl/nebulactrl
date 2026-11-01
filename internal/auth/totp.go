package auth

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha1" //nolint:gosec // RFC 6238 default; authenticator apps expect HMAC-SHA1.
	"crypto/subtle"
	"encoding/base32"
	"encoding/binary"
	"fmt"
	"net/url"
	"strings"
	"time"
)

// TOTP parameters (RFC 6238 defaults, understood by every authenticator app).
const (
	TOTPPeriod = 30 // seconds
	TOTPDigits = 6
	// totpSkew is the number of time steps accepted on each side of "now" to
	// tolerate clock drift.
	totpSkew = 1
	// totpSecretBytes is 160 bits, the RFC 4226 recommended length.
	totpSecretBytes = 20
)

var totpEncoding = base32.StdEncoding.WithPadding(base32.NoPadding)

// GenerateTOTPSecret returns a fresh random secret.
func GenerateTOTPSecret() ([]byte, error) {
	b := make([]byte, totpSecretBytes)
	if _, err := rand.Read(b); err != nil {
		return nil, fmt.Errorf("totp secret: %w", err)
	}
	return b, nil
}

// EncodeTOTPSecret renders a secret as the unpadded base32 string authenticator
// apps accept for manual entry.
func EncodeTOTPSecret(secret []byte) string { return totpEncoding.EncodeToString(secret) }

// TOTPProvisioningURI returns the otpauth:// URI that authenticator apps import.
func TOTPProvisioningURI(issuer, account string, secret []byte) string {
	label := url.PathEscape(issuer + ":" + account)
	q := url.Values{}
	q.Set("secret", EncodeTOTPSecret(secret))
	q.Set("issuer", issuer)
	q.Set("algorithm", "SHA1")
	q.Set("digits", fmt.Sprint(TOTPDigits))
	q.Set("period", fmt.Sprint(TOTPPeriod))
	return "otpauth://totp/" + label + "?" + q.Encode()
}

// hotp is RFC 4226 with dynamic truncation, returning digits decimal digits.
func hotp(secret []byte, counter uint64, digits int) string {
	var msg [8]byte
	binary.BigEndian.PutUint64(msg[:], counter)
	m := hmac.New(sha1.New, secret)
	m.Write(msg[:])
	sum := m.Sum(nil)
	off := sum[len(sum)-1] & 0x0f
	bin := binary.BigEndian.Uint32(sum[off:off+4]) & 0x7fffffff
	mod := uint32(1)
	for range digits {
		mod *= 10
	}
	return fmt.Sprintf("%0*d", digits, bin%mod)
}

// TOTPStep returns the 30 s time step containing t.
func TOTPStep(t time.Time) int64 { return t.Unix() / TOTPPeriod }

// VerifyTOTP checks code against the steps around now and returns the matching
// step. Steps at or before lastStep are rejected, so an observed code cannot be
// replayed. The comparison is constant-time.
func VerifyTOTP(secret []byte, code string, now time.Time, lastStep int64) (int64, bool) {
	code = strings.ReplaceAll(strings.TrimSpace(code), " ", "")
	if len(code) != TOTPDigits {
		return 0, false
	}
	cur := TOTPStep(now)
	matched := int64(-1)
	// Walk every candidate (no early exit) so timing does not reveal which step hit.
	for d := int64(-totpSkew); d <= totpSkew; d++ {
		step := cur + d
		if step <= lastStep || step < 0 {
			continue
		}
		want := hotp(secret, uint64(step), TOTPDigits)
		if subtle.ConstantTimeCompare([]byte(want), []byte(code)) == 1 {
			matched = step
		}
	}
	if matched < 0 {
		return 0, false
	}
	return matched, true
}

// looksLikeTOTP reports whether s has the shape of a TOTP code (as opposed to a
// recovery code).
func looksLikeTOTP(s string) bool {
	s = strings.ReplaceAll(strings.TrimSpace(s), " ", "")
	if len(s) != TOTPDigits {
		return false
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}
