package databases

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestBrokenAfterOverride(t *testing.T) {
	s := NewService(nil, nil, nil, nil, nil, nil)
	require.Equal(t, BrokenAfter, s.brokenAfter)
	s.SetBrokenAfter(-time.Second)
	require.Equal(t, BrokenAfter, s.brokenAfter, "a non-positive value keeps the default")
	s.SetBrokenAfter(time.Minute)
	require.Equal(t, time.Minute, s.brokenAfter)
}
