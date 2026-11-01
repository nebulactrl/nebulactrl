package auth

import (
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// RFC 6238 appendix B: SHA-1 secret "12345678901234567890", 8-digit codes. Our
// 6-digit codes are the same truncation modulo 10^6.
var rfc6238Vectors = []struct {
	unix  int64
	code8 string
}{
	{59, "94287082"},
	{1111111109, "07081804"},
	{1111111111, "14050471"},
	{1234567890, "89005924"},
	{2000000000, "69279037"},
	{20000000000, "65353130"},
}

func TestTOTPRFC6238Vectors(t *testing.T) {
	secret := []byte("12345678901234567890")
	for _, v := range rfc6238Vectors {
		at := time.Unix(v.unix, 0)
		require.Equal(t, v.code8, hotp(secret, uint64(TOTPStep(at)), 8), "8 digits at %d", v.unix)
		require.Equal(t, v.code8[2:], TOTPCode(secret, at), "6 digits at %d", v.unix)
	}
}

func TestRFC4226HOTPVectors(t *testing.T) {
	secret := []byte("12345678901234567890")
	want := []string{"755224", "287082", "359152", "969429", "338314", "254676", "287922", "162583", "399871", "520489"}
	for i, w := range want {
		require.Equal(t, w, hotp(secret, uint64(i), 6), "counter %d", i)
	}
}

func TestVerifyTOTPSkewAndReplay(t *testing.T) {
	secret := []byte("12345678901234567890")
	now := time.Unix(1111111109, 0)
	cur := TOTPStep(now)

	code := TOTPCode(secret, now)
	step, ok := VerifyTOTP(secret, code, now, 0)
	require.True(t, ok)
	require.Equal(t, cur, step)

	// A code from the previous and the next step is tolerated (clock drift) ...
	_, ok = VerifyTOTP(secret, TOTPCode(secret, now.Add(-30*time.Second)), now, 0)
	require.True(t, ok)
	_, ok = VerifyTOTP(secret, TOTPCode(secret, now.Add(30*time.Second)), now, 0)
	require.True(t, ok)
	// ... two steps away is not.
	_, ok = VerifyTOTP(secret, TOTPCode(secret, now.Add(-90*time.Second)), now, 0)
	require.False(t, ok)
	_, ok = VerifyTOTP(secret, TOTPCode(secret, now.Add(90*time.Second)), now, 0)
	require.False(t, ok)

	// A step that was already accepted can never be accepted again.
	_, ok = VerifyTOTP(secret, code, now, step)
	require.False(t, ok)
	_, ok = VerifyTOTP(secret, TOTPCode(secret, now.Add(-30*time.Second)), now, step)
	require.False(t, ok)
}

func TestVerifyTOTPRejectsMalformed(t *testing.T) {
	secret := []byte("12345678901234567890")
	now := time.Unix(59, 0)
	for _, bad := range []string{"", "12345", "1234567", "abcdef", "287 08", "28708"} {
		_, ok := VerifyTOTP(secret, bad, now, 0)
		require.False(t, ok, bad)
	}
	// Whitespace around a correct code is fine (users paste with spaces).
	_, ok := VerifyTOTP(secret, " 287 082 ", now, 0)
	require.True(t, ok)
}

func TestTOTPProvisioningURI(t *testing.T) {
	secret := []byte("12345678901234567890")
	uri := TOTPProvisioningURI("NebulaCtrl", "ada@example.com", secret)
	require.True(t, strings.HasPrefix(uri, "otpauth://totp/NebulaCtrl:ada@example.com?"), uri)
	require.Contains(t, uri, "secret=GEZDGNBVGY3TQOJQGEZDGNBVGY3TQOJQ")
	require.Contains(t, uri, "issuer=NebulaCtrl")
	require.Contains(t, uri, "digits=6")
	require.Contains(t, uri, "period=30")
}

func TestRecoveryCodes(t *testing.T) {
	codes, err := NewRecoveryCodes()
	require.NoError(t, err)
	require.Len(t, codes, RecoveryCodeCount)
	seen := map[string]bool{}
	for _, c := range codes {
		require.Regexp(t, `^[a-z2-7]{4}(-[a-z2-7]{4}){3}$`, c)
		require.False(t, seen[c], "duplicate code")
		seen[c] = true
		require.False(t, looksLikeTOTP(c))
	}
	c := codes[0]
	// Hash ignores case, dashes and spaces, so a retyped code still matches.
	require.Equal(t, HashRecoveryCode(c), HashRecoveryCode(strings.ToUpper(c)))
	require.Equal(t, HashRecoveryCode(c), HashRecoveryCode(strings.ReplaceAll(c, "-", " ")))
	require.NotEqual(t, HashRecoveryCode(c), HashRecoveryCode(codes[1]))
	require.NotContains(t, HashRecoveryCode(c), c[:4], "the stored form must not contain the code")
	require.Len(t, normalizeRecoveryCode(c), 16)
}

// TOTPCode returns the 6-digit code for the time step containing t.
func TOTPCode(secret []byte, t time.Time) string {
	return hotp(secret, uint64(TOTPStep(t)), TOTPDigits)
}
