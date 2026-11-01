package secretgen

import (
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestSecretAlphabetsAndLength(t *testing.T) {
	for _, name := range PresetNames() {
		v, err := Secret(200, name)
		require.NoError(t, err)
		require.Len(t, v, 200)
		chars, _ := Preset(name)
		for _, c := range v {
			require.Contains(t, chars, string(c), name)
		}
	}
	v, err := Secret(10, "xyz")
	require.NoError(t, err)
	require.Len(t, strings.Trim(v, "xyz"), 0)
	for _, n := range []int{0, -1, MaxSecretLen + 1} {
		_, err = Secret(n, "")
		require.Error(t, err)
	}
	_, err = Secret(5, "aa")
	require.Error(t, err, "duplicate characters")
	_, err = Secret(5, "a")
	require.Error(t, err)
}

func TestSecretIsRoughlyUniform(t *testing.T) {
	// 62 does not divide 256: a modulo-biased generator would over-produce the first 8 characters.
	counts := map[rune]int{}
	v, err := Secret(MaxSecretLen, AlphaAlnum)
	require.NoError(t, err)
	for i := 0; i < 60; i++ {
		s, err := Secret(MaxSecretLen, AlphaAlnum)
		require.NoError(t, err)
		v += s
	}
	for _, c := range v {
		counts[c]++
	}
	require.Len(t, counts, 62)
	exp := float64(len(v)) / 62
	for c, n := range counts {
		require.InDelta(t, exp, float64(n), exp*0.2, string(c))
	}
}

func TestIntBytesUUID(t *testing.T) {
	for i := 0; i < 200; i++ {
		n, err := Int(-3, 3)
		require.NoError(t, err)
		require.GreaterOrEqual(t, n, int64(-3))
		require.LessOrEqual(t, n, int64(3))
	}
	_, err := Int(2, 1)
	require.Error(t, err)
	b, err := Bytes(16, "hex")
	require.NoError(t, err)
	require.Len(t, b, 32)
	_, err = Bytes(16, "rot13")
	require.Error(t, err)
	u, err := UUID()
	require.NoError(t, err)
	require.Len(t, u, 36)
}

// TestNoMathRand enforces the repository rule that internal/ code never uses
// math/rand (Dokploy generates passwords with Math.random(); we do not).
func TestNoMathRand(t *testing.T) {
	root := ".."
	fset := token.NewFileSet()
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err == nil && d.IsDir() && d.Name() == "agent" {
			return fs.SkipDir // agent backoff jitter is not secret material
		}
		if err != nil || d.IsDir() || !strings.HasSuffix(p, ".go") || strings.HasSuffix(p, "_test.go") {
			return err
		}
		f, err := parser.ParseFile(fset, p, nil, parser.ImportsOnly)
		if err != nil {
			return nil //nolint:nilerr // other units may have half-written files; compile errors are reported elsewhere
		}
		for _, imp := range f.Imports {
			if path := strings.Trim(imp.Path.Value, `"`); path == "math/rand" || path == "math/rand/v2" {
				t.Errorf("%s imports %s; use crypto/rand (internal/secretgen)", p, path)
			}
		}
		return nil
	})
	require.NoError(t, err)
}
