package agentprotocol

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestParseVolumeOwner(t *testing.T) {
	good := map[string]VolumeOwner{
		"0":                     {UID: 0},
		"10001":                 {UID: 10001},
		"10001:10001":           {UID: 10001, GID: 10001, HasGID: true},
		"0:0":                   {UID: 0, GID: 0, HasGID: true},
		"1000:0":                {UID: 1000, GID: 0, HasGID: true},
		"2147483647":            {UID: MaxVolumeID},
		"2147483647:2147483647": {UID: MaxVolumeID, GID: MaxVolumeID, HasGID: true},
		"007:08":                {UID: 7, GID: 8, HasGID: true},
	}
	for in, want := range good {
		got, err := ParseVolumeOwner(in)
		require.NoError(t, err, in)
		require.Equal(t, want, got, in)
	}
	for _, in := range []string{
		"", ":", "1:", ":1", "1:2:3", "-1", "+1", "1.5", " 1", "1 ", "root", "app:app", "1:app", "0x10",
		"2147483648", "1:2147483648", "99999999999", "123456789012345678901234567890",
	} {
		_, err := ParseVolumeOwner(in)
		require.Error(t, err, "%q must be refused", in)
	}
}

func TestNormalizeVolumeOwner(t *testing.T) {
	for in, want := range map[string]string{"": "", "10001": "10001", "010:0010": "10:10", "0": "0"} {
		got, err := NormalizeVolumeOwner(in)
		require.NoError(t, err, in)
		require.Equal(t, want, got, in)
	}
	_, err := NormalizeVolumeOwner("nobody")
	require.Error(t, err)
}

func TestVolumeOwnerDiffers(t *testing.T) {
	both := VolumeOwner{UID: 10001, GID: 10002, HasGID: true}
	require.False(t, both.Differs(10001, 10002))
	require.True(t, both.Differs(0, 10002))
	require.True(t, both.Differs(10001, 0))
	uid := VolumeOwner{UID: 10001}
	require.False(t, uid.Differs(10001, 0), "the uid form leaves the group alone")
	require.True(t, uid.Differs(0, 0))
}
