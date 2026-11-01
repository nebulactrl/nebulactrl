package composeimport

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

// loadFixture reads testdata/<dir>: docker-compose.yml, an optional .env, and
// every other file as a referenced file (by its relative path).
func loadFixture(t *testing.T, dir string) ComposeSource {
	t.Helper()
	root := filepath.Join("testdata", dir)
	src := ComposeSource{Files: map[string]string{}}
	err := filepath.WalkDir(root, func(p string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		rel, _ := filepath.Rel(root, p)
		raw, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		switch rel {
		case "docker-compose.yml":
			src.Compose = string(raw)
		case ".env":
			src.Env = string(raw)
		default:
			src.Files[filepath.ToSlash(rel)] = string(raw)
		}
		return nil
	})
	require.NoError(t, err)
	require.NotEmpty(t, src.Compose)
	return src
}

// svc returns the planned service with the Compose key.
func svc(t *testing.T, p ComposePlan, key string) ComposePlanService {
	t.Helper()
	for _, s := range p.Services {
		if s.Key == key {
			return s
		}
	}
	t.Fatalf("service %q not in plan", key)
	return ComposePlanService{}
}

func envOf(s ComposePlanService, key string) (ComposePlanEnv, bool) {
	for _, e := range s.Env {
		if e.Key == key {
			return e, true
		}
	}
	return ComposePlanEnv{}, false
}
