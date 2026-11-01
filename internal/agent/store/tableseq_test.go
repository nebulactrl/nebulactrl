package store

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestTableSeqPersistsAcrossReopen(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(dir)
	require.NoError(t, err)
	n, err := s.LoadTableSeq()
	require.NoError(t, err)
	require.Zero(t, n, "nothing saved yet")
	require.NoError(t, s.SaveTableSeq(1<<40+7))
	require.NoError(t, s.Close())

	s, err = Open(dir)
	require.NoError(t, err)
	defer s.Close()
	n, err = s.LoadTableSeq()
	require.NoError(t, err)
	require.Equal(t, uint64(1<<40+7), n)
}
