package meshlinks

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestReferenceGraceOverride(t *testing.T) {
	s := NewService(nil, nil, nil, nil, nil)
	require.Equal(t, ReferenceGrace, s.grace)
	s.SetReferenceGrace(0)
	require.Equal(t, ReferenceGrace, s.grace, "zero keeps the default")
	s.SetReferenceGrace(30 * time.Second)
	require.Equal(t, 30*time.Second, s.grace)
}
