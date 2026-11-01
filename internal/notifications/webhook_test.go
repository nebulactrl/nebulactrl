package notifications

import (
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestSignMatchesDocumentedScheme(t *testing.T) {
	body := []byte(`{"id":"1"}`)
	at := time.Unix(1_700_000_000, 0)
	// The documented recipe, written out independently of Sign.
	mac := hmac.New(sha256.New, []byte("whsec_test_secret_value"))
	mac.Write([]byte("1700000000." + string(body)))
	want := "t=1700000000,v1=" + hex.EncodeToString(mac.Sum(nil))
	require.Equal(t, want, Sign("whsec_test_secret_value", body, at))
}

func TestVerifySignature(t *testing.T) {
	const secret = "whsec_test_secret_value"
	body := []byte(`{"title":"api failed"}`)
	now := time.Unix(1_700_000_100, 0)
	header := Sign(secret, body, time.Unix(1_700_000_100, 0))

	require.NoError(t, VerifySignature(secret, header, body, now, 0))

	t.Run("tampered body", func(t *testing.T) {
		require.ErrorContains(t, VerifySignature(secret, header, []byte(`{"title":"api failed!"}`), now, 0), "mismatch")
	})
	t.Run("wrong secret", func(t *testing.T) {
		require.ErrorContains(t, VerifySignature("whsec_other_secret_value", header, body, now, 0), "mismatch")
	})
	t.Run("tampered timestamp", func(t *testing.T) {
		// Moving t forward to dodge the replay window invalidates the MAC.
		forged := strings.Replace(header, "t=1700000100", "t=1700000200", 1)
		require.ErrorContains(t, VerifySignature(secret, forged, body, time.Unix(1_700_000_200, 0), 0), "mismatch")
	})
	t.Run("replay outside tolerance", func(t *testing.T) {
		require.ErrorContains(t, VerifySignature(secret, header, body, now.Add(6*time.Minute), 0), "tolerance")
		require.NoError(t, VerifySignature(secret, header, body, now.Add(6*time.Minute), 10*time.Minute))
		require.ErrorContains(t, VerifySignature(secret, header, body, now.Add(-6*time.Minute), 0), "tolerance")
	})
	t.Run("malformed", func(t *testing.T) {
		for _, h := range []string{"", "garbage", "t=abc,v1=00", "t=1700000100", "v1=00"} {
			require.Error(t, VerifySignature(secret, h, body, now, 0), h)
		}
	})
	t.Run("rotation: any v1 may match", func(t *testing.T) {
		ts := strconv.FormatInt(now.Unix(), 10)
		multi := "t=" + ts + ",v1=" + strings.Repeat("0", 64) + ",v1=" + signHex(secret, ts, body)
		require.NoError(t, VerifySignature(secret, multi, body, now, 0))
	})
}

func TestGenerateSigningSecret(t *testing.T) {
	a, err := GenerateSigningSecret()
	require.NoError(t, err)
	b, err := GenerateSigningSecret()
	require.NoError(t, err)
	require.NotEqual(t, a, b)
	require.True(t, strings.HasPrefix(a, "whsec_"))
	require.GreaterOrEqual(t, len(a), 40)
}

// Reference receiver of the documented scheme (docs/operations/notifications.md).
// VerifySignature checks a signature header against the raw body: the timestamp
// must be within tolerance of now (0: DefaultSignatureTolerance) and at least
// one v1 entry must equal the expected HMAC (compared in constant time).
// Receivers must verify the raw bytes, not a re-serialized JSON value.
func VerifySignature(secret, header string, body []byte, now time.Time, tolerance time.Duration) error {
	if tolerance <= 0 {
		tolerance = DefaultSignatureTolerance
	}
	var ts string
	var sigs []string
	for _, part := range strings.Split(header, ",") {
		k, v, ok := strings.Cut(strings.TrimSpace(part), "=")
		if !ok {
			continue
		}
		switch k {
		case "t":
			ts = v
		case "v1":
			sigs = append(sigs, v)
		}
	}
	if ts == "" || len(sigs) == 0 {
		return errors.New("malformed signature header")
	}
	sec, err := strconv.ParseInt(ts, 10, 64)
	if err != nil {
		return errors.New("malformed signature timestamp")
	}
	if d := now.Sub(time.Unix(sec, 0)); d > tolerance || d < -tolerance {
		return fmt.Errorf("signature timestamp outside the %s tolerance", tolerance)
	}
	want := []byte(signHex(secret, ts, body))
	for _, s := range sigs {
		if subtle.ConstantTimeCompare(want, []byte(s)) == 1 {
			return nil
		}
	}
	return errors.New("signature mismatch")
}
