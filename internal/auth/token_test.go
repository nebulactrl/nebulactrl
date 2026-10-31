package auth

import (
	"regexp"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestGenerateToken(t *testing.T) {
	raw, hash, prefix, err := GenerateToken()
	require.NoError(t, err)
	require.Regexp(t, regexp.MustCompile(`^nct_[a-z2-7]{52}$`), raw)
	require.Equal(t, raw[4:12], prefix)
	require.Equal(t, HashToken(raw), hash)
	require.Len(t, hash, 32)
	raw2, _, _, _ := GenerateToken()
	require.NotEqual(t, raw, raw2)
}
