// Package totptest computes the authenticator-app side of TOTP (RFC 6238: SHA-1,
// 6 digits, 30 s steps), independently of internal/auth, for tests that sign in
// with a second factor.
package totptest

import (
	"crypto/hmac"
	"crypto/sha1" //nolint:gosec // RFC 6238 specifies HMAC-SHA1; authenticator apps use it
	"encoding/binary"
	"fmt"
	"time"
)

// Code returns the 6-digit code an authenticator app shows for secret at t.
func Code(secret []byte, t time.Time) string {
	var msg [8]byte
	binary.BigEndian.PutUint64(msg[:], uint64(t.Unix()/30))
	m := hmac.New(sha1.New, secret)
	m.Write(msg[:])
	sum := m.Sum(nil)
	off := sum[len(sum)-1] & 0x0f
	bin := binary.BigEndian.Uint32(sum[off:off+4]) & 0x7fffffff
	return fmt.Sprintf("%06d", bin%1_000_000)
}
