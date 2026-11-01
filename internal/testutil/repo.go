package testutil

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

// RepoFile returns the content of a file of the repository, by its path from
// the repository root ("deploy/install/docker-compose.yml"). It is for tests
// that check what the repository ships (the installer's compose file and
// service configuration) without the shipped package exporting it.
func RepoFile(t testing.TB, rel string) string {
	t.Helper()
	_, here, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("repo file: cannot locate the repository root")
	}
	b, err := os.ReadFile(filepath.Join(filepath.Dir(here), "..", "..", filepath.FromSlash(rel)))
	if err != nil {
		t.Fatalf("repo file: %v", err)
	}
	return string(b)
}
