package auth

import (
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func TestRelyingPartyFromPublicURL(t *testing.T) {
	s := NewService(nil, nil)
	cases := []struct {
		base        string
		rpID        string
		origin      string
		unavailable bool
	}{
		{"https://nebula.example.com", "nebula.example.com", "https://nebula.example.com", false},
		{"https://Nebula.Example.com:8443/", "nebula.example.com", "https://Nebula.Example.com:8443", false},
		{"http://localhost:5173", "localhost", "http://localhost:5173", false},
		{"http://app.localhost", "app.localhost", "http://app.localhost", false},
		{"http://nebula.example.com", "", "", true}, // plain HTTP on a real domain
		{"http://203.0.113.7:8000", "", "", true},   // an IP address is not a valid RP ID
		{"https://[2001:db8::1]", "", "", true},     // nor an IPv6 one
		{"", "", "", true},                          // no public URL
		{"not a url", "", "", true},
	}
	for _, c := range cases {
		wa, err := s.relyingParty(c.base)
		if c.unavailable {
			require.Error(t, err, c.base)
			require.NotEmpty(t, s.PasskeyUnavailable(c.base), c.base)
			continue
		}
		require.NoError(t, err, c.base)
		require.Equal(t, c.rpID, wa.Config.RPID, c.base)
		require.Equal(t, []string{c.origin}, wa.Config.RPOrigins, c.base)
		require.Empty(t, s.PasskeyUnavailable(c.base), c.base)
	}
}

func TestUserHandleIsTheRawUserID(t *testing.T) {
	id := uuid.New()
	h := userHandle(id)
	require.Len(t, h, 16)
	back, err := uuid.FromBytes(h)
	require.NoError(t, err)
	require.Equal(t, id, back)
}
