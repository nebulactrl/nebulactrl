package secretbox

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"testing"

	"github.com/stretchr/testify/require"
)

func randKey(t *testing.T) []byte {
	t.Helper()
	key := make([]byte, KeySize)
	_, err := rand.Read(key)
	require.NoError(t, err)
	return key
}

func newBox(t *testing.T) *Box {
	t.Helper()
	b, err := New(randKey(t))
	require.NoError(t, err)
	return b
}

// sealV1 writes a pre-upgrade ciphertext: version 1, no key id, AAD = [1].
func sealV1(t *testing.T, key, plain []byte) []byte {
	t.Helper()
	block, err := aes.NewCipher(key)
	require.NoError(t, err)
	aead, err := cipher.NewGCM(block)
	require.NoError(t, err)
	nonce := make([]byte, aead.NonceSize())
	_, err = rand.Read(nonce)
	require.NoError(t, err)
	return aead.Seal(append([]byte{FormatV1}, nonce...), nonce, plain, []byte{FormatV1})
}

func TestRoundTrip(t *testing.T) {
	b := newBox(t)
	for _, plain := range []string{"", "x", "sekret-canary-12345", string(bytes.Repeat([]byte("é"), 4096))} {
		ct, err := b.SealString(ServiceVariablesValue, "svc/KEY", plain)
		require.NoError(t, err)
		require.Equal(t, FormatV2, ct[0])
		require.Equal(t, b.Header(), ct[:HeaderSize])
		got, err := b.OpenString(ServiceVariablesValue, "svc/KEY", ct)
		require.NoError(t, err)
		require.Equal(t, plain, got)
	}
}

func TestNonceUnique(t *testing.T) {
	b := newBox(t)
	a, _ := b.Seal(ReleasesEnv, "r", []byte("same"))
	c, _ := b.Seal(ReleasesEnv, "r", []byte("same"))
	require.NotEqual(t, a, c)
}

func TestTamperedCiphertextFails(t *testing.T) {
	b := newBox(t)
	ct, err := b.Seal(ReleasesEnv, "r1", []byte("top secret"))
	require.NoError(t, err)
	for i := range ct {
		bad := bytes.Clone(ct)
		bad[i] ^= 0x01
		_, err := b.Open(ReleasesEnv, "r1", bad)
		require.ErrorIs(t, err, ErrDecrypt, "flip byte %d", i)
	}
	_, err = b.Open(ReleasesEnv, "r1", ct[:5])
	require.ErrorIs(t, err, ErrDecrypt)
	_, err = b.Open(ReleasesEnv, "r1", nil)
	require.ErrorIs(t, err, ErrDecrypt)
}

func TestWrongKeyFails(t *testing.T) {
	ct, _ := newBox(t).Seal(ReleasesEnv, "r", []byte("hello"))
	_, err := newBox(t).Open(ReleasesEnv, "r", ct)
	require.ErrorIs(t, err, ErrUnknownKey)
	require.ErrorIs(t, err, ErrDecrypt)
}

func TestContextIsBoundIntoTheCiphertext(t *testing.T) {
	b := newBox(t)
	ct, err := b.SealString(ServiceVariablesValue, RowID("svc-a", "DB_PASSWORD"), "hunter2")
	require.NoError(t, err)
	// Another row, another column of the same row, another table: all fail.
	for name, tc := range map[string]struct {
		col Column
		row string
	}{
		"other row":    {ServiceVariablesValue, RowID("svc-b", "DB_PASSWORD")},
		"other key":    {ServiceVariablesValue, RowID("svc-a", "API_KEY")},
		"other column": {ServiceVariablesTemplate, RowID("svc-a", "DB_PASSWORD")},
		"other table":  {SharedVariablesValue, RowID("svc-a", "DB_PASSWORD")},
		"empty row":    {ServiceVariablesValue, ""},
	} {
		_, err := b.OpenString(tc.col, tc.row, ct)
		require.ErrorIs(t, err, ErrDecrypt, name)
	}
	got, err := b.OpenString(ServiceVariablesValue, RowID("svc-a", "DB_PASSWORD"), ct)
	require.NoError(t, err)
	require.Equal(t, "hunter2", got)
}

func TestSealNeedsContext(t *testing.T) {
	b := newBox(t)
	_, err := b.Seal(Column{}, "r", []byte("x"))
	require.Error(t, err)
	_, err = b.Seal(ReleasesEnv, "", []byte("x"))
	require.Error(t, err)
}

func TestKeyRing(t *testing.T) {
	oldKey, newKey := randKey(t), randKey(t)
	oldBox, err := New(oldKey)
	require.NoError(t, err)
	ring, err := NewRing(newKey, oldKey)
	require.NoError(t, err)

	require.NotEqual(t, oldBox.KeyID(), ring.KeyID())
	require.Equal(t, []string{ring.KeyID(), oldBox.KeyID()}, ring.KeyIDs())
	require.Len(t, ring.KeyID(), 8)

	// A value sealed under the old key opens through the ring and needs a rekey.
	ct, err := oldBox.SealString(CertificatesKey, "cert-1", "PEM")
	require.NoError(t, err)
	require.True(t, ring.NeedsRekey(ct))
	got, err := ring.OpenString(CertificatesKey, "cert-1", ct)
	require.NoError(t, err)
	require.Equal(t, "PEM", got)

	// New values use the current key and open with that key alone.
	fresh, err := ring.SealString(CertificatesKey, "cert-1", "PEM")
	require.NoError(t, err)
	require.False(t, ring.NeedsRekey(fresh))
	only, err := New(newKey)
	require.NoError(t, err)
	got, err = only.OpenString(CertificatesKey, "cert-1", fresh)
	require.NoError(t, err)
	require.Equal(t, "PEM", got)
	// ... and the old key alone no longer does.
	_, err = oldBox.Open(CertificatesKey, "cert-1", fresh)
	require.ErrorIs(t, err, ErrUnknownKey)

	// Removing the old key from the ring orphans values still on it.
	_, err = only.Open(CertificatesKey, "cert-1", ct)
	require.ErrorIs(t, err, ErrUnknownKey)
}

func TestRingRejectsDuplicatesAndBadKeys(t *testing.T) {
	k := randKey(t)
	_, err := NewRing(k, k)
	require.ErrorContains(t, err, "twice")
	_, err = NewRing(randKey(t), make([]byte, 5))
	require.ErrorContains(t, err, "previous key 1")
	_, err = New(make([]byte, 16))
	require.Error(t, err)
}

func TestLegacyV1StaysReadable(t *testing.T) {
	oldKey, newKey := randKey(t), randKey(t)
	v1 := sealV1(t, oldKey, []byte("legacy secret"))

	b, err := New(oldKey)
	require.NoError(t, err)
	got, err := b.OpenString(ServicesGitToken, "anything", v1)
	require.NoError(t, err)
	require.Equal(t, "legacy secret", got, "v1 ignores the row context")
	require.True(t, b.NeedsRekey(v1))

	// After a rotation the v1 value opens through the previous key.
	ring, err := NewRing(newKey, oldKey)
	require.NoError(t, err)
	got, err = ring.OpenString(ServicesGitToken, "other-row", v1)
	require.NoError(t, err)
	require.Equal(t, "legacy secret", got)

	// Without the key it fails like any other value.
	only, err := New(newKey)
	require.NoError(t, err)
	_, err = only.Open(ServicesGitToken, "x", v1)
	require.ErrorIs(t, err, ErrDecrypt)
	// A v1 value with a flipped bit fails.
	bad := bytes.Clone(v1)
	bad[len(bad)-1] ^= 1
	_, err = b.Open(ServicesGitToken, "x", bad)
	require.ErrorIs(t, err, ErrDecrypt)
}

func TestKeyValidation(t *testing.T) {
	_, err := New(make([]byte, 16))
	require.Error(t, err)
	k, err := GenerateKey()
	require.NoError(t, err)
	b, err := ParseKey(k)
	require.NoError(t, err)
	require.Len(t, b, KeySize)
	_, err = ParseKey("not base64 !!")
	require.Error(t, err)
}

func TestParseKeyList(t *testing.T) {
	a, b := randKey(t), randKey(t)
	enc := base64.StdEncoding.EncodeToString
	keys, err := ParseKeyList(enc(a) + ", " + enc(b) + "\n")
	require.NoError(t, err)
	require.Equal(t, [][]byte{a, b}, keys)
	keys, err = ParseKeyList("  \n")
	require.NoError(t, err)
	require.Empty(t, keys)
	_, err = ParseKeyList(enc(a) + ",nope!")
	require.ErrorContains(t, err, "key 2")
}

func TestDigestIsStableKeyedAndFull(t *testing.T) {
	a, b := newBox(t), newBox(t)
	d := a.Digest("sekret-canary-12345")
	require.Len(t, d, 64)
	require.Equal(t, d, a.Digest("sekret-canary-12345"))
	require.NotEqual(t, d, a.Digest("sekret-canary-12346"))
	require.NotEqual(t, d, b.Digest("sekret-canary-12345"), "keyed: another instance key gives another digest")
	require.NotContains(t, d, "sekret")
}

func TestFingerprintMatchesAcrossRotation(t *testing.T) {
	oldKey, newKey := randKey(t), randKey(t)
	oldBox, _ := New(oldKey)
	ring, err := NewRing(newKey, oldKey)
	require.NoError(t, err)
	stored := oldBox.Fingerprint("value")
	require.NotEqual(t, stored, ring.Fingerprint("value"), "the current key gives another fingerprint")
	require.True(t, ring.FingerprintMatches(stored, "value"))
	require.True(t, ring.FingerprintMatches(ring.Fingerprint("value"), "value"))
	require.False(t, ring.FingerprintMatches(stored, "other"))
}

func TestOpenStringWipesTheBuffer(t *testing.T) {
	b := newBox(t)
	ct, err := b.SealString(ReleasesEnv, "r", "abc")
	require.NoError(t, err)
	p, err := b.Open(ReleasesEnv, "r", ct)
	require.NoError(t, err)
	Wipe(p)
	require.Equal(t, []byte{0, 0, 0}, p)
}

func TestRegistryIsConsistent(t *testing.T) {
	seen := map[string]bool{}
	for _, c := range All() {
		require.NotEmpty(t, c.Table)
		require.NotEmpty(t, c.Column)
		require.NotEmpty(t, c.RowKey, c.String())
		require.False(t, seen[c.String()], "duplicate %s", c)
		seen[c.String()] = true
	}
	require.Len(t, All(), 34)
}
