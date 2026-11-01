// Package backup is the agent side of database backups: it streams a logical
// dump out of a database container into a destination (S3-compatible bucket or
// a directory on the host) and streams stored dumps back in for restores.
//
// There is no temp file. The dump runs as an exec inside the database
// container; its stdout flows through an optional gzip, a SHA-256 tee and into
// the destination's streaming upload. A dump that exits non-zero (or an upload
// that fails) surfaces as a read error to the uploader, which then aborts: no
// truncated object is ever committed.
package backup

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"strings"

	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

// ErrNotFound is returned by Store.Get and Store.Size for a missing object.
var ErrNotFound = errors.New("backup: object not found")

// Store is a backup destination.
type Store interface {
	// Put streams r to key. The object becomes visible only when r returns
	// io.EOF and the store committed it; any other read error aborts the
	// upload and leaves nothing behind.
	Put(ctx context.Context, key string, r io.Reader) error
	// Get opens an object. The caller closes it.
	Get(ctx context.Context, key string) (io.ReadCloser, error)
	// Size returns the stored size of an object.
	Size(ctx context.Context, key string) (int64, error)
	// Delete removes an object; a missing object is not an error.
	Delete(ctx context.Context, key string) error
}

// StoreFactory builds the Store of a destination. secrets carries the S3
// credentials (agentprotocol.SecretBackup*).
type StoreFactory func(dest agentprotocol.BackupDestination, secrets map[string]string) (Store, error)

// NewStoreFactory returns the production factory; local destinations live under
// backupRoot.
func NewStoreFactory(backupRoot string) StoreFactory {
	return func(dest agentprotocol.BackupDestination, secrets map[string]string) (Store, error) {
		switch dest.Kind {
		case agentprotocol.DestLocal:
			return NewLocalStore(backupRoot, dest.LocalDir)
		case agentprotocol.DestS3:
			return NewS3Store(dest, secrets[agentprotocol.SecretBackupAccessKey], secrets[agentprotocol.SecretBackupSecretKey])
		default:
			return nil, fmt.Errorf("unknown destination kind %q", dest.Kind)
		}
	}
}

// ValidKey reports whether key is a safe relative object key; see
// agentprotocol.ValidObjectKey.
func ValidKey(key string) error { return agentprotocol.ValidObjectKey(key) }

// LocalStore stores objects as files below one directory.
type LocalStore struct{ root string }

// NewLocalStore returns a store rooted at <backupRoot>/<subDir>. subDir is
// optional and must be a safe relative path.
func NewLocalStore(backupRoot, subDir string) (*LocalStore, error) {
	if backupRoot == "" {
		return nil, errors.New("this agent has no backup directory configured")
	}
	base := filepath.Clean(backupRoot)
	root := base
	if subDir != "" {
		if err := ValidKey(subDir); err != nil {
			return nil, fmt.Errorf("invalid local directory: %w", err)
		}
		root = filepath.Join(base, filepath.FromSlash(subDir))
		if !strings.HasPrefix(root, base+string(filepath.Separator)) {
			return nil, fmt.Errorf("invalid local directory %q", subDir)
		}
	}
	return &LocalStore{root: root}, nil
}

// path maps an object key to its file below the store root. The key is a
// relative key without "." / ".." segments, and the joined path is checked to
// stay below the root as well.
func (s *LocalStore) path(key string) (string, error) {
	if err := ValidKey(key); err != nil {
		return "", err
	}
	p := filepath.Join(s.root, filepath.FromSlash(key))
	if !strings.HasPrefix(p, s.root+string(filepath.Separator)) {
		return "", fmt.Errorf("invalid object key %q", key)
	}
	return p, nil
}

// Put implements Store: it writes a hidden temp file in the destination
// directory and renames it into place once r hit EOF and the data is synced.
func (s *LocalStore) Put(_ context.Context, key string, r io.Reader) (err error) {
	p, err := s.path(key)
	if err != nil {
		return err
	}
	dir := filepath.Dir(p)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("create backup directory: %w", err)
	}
	tmp, err := os.CreateTemp(dir, ".partial-*")
	if err != nil {
		return fmt.Errorf("create temp file: %w", err)
	}
	defer func() {
		if err != nil {
			_ = tmp.Close()
			_ = os.Remove(tmp.Name())
		}
	}()
	if _, err = io.Copy(tmp, r); err != nil {
		return err
	}
	if err := tmp.Sync(); err != nil {
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Chmod(tmp.Name(), 0o600); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), p)
}

// Get implements Store.
func (s *LocalStore) Get(_ context.Context, key string) (io.ReadCloser, error) {
	p, err := s.path(key)
	if err != nil {
		return nil, err
	}
	f, err := os.Open(p)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, ErrNotFound
	}
	return f, err
}

// Size implements Store.
func (s *LocalStore) Size(_ context.Context, key string) (int64, error) {
	p, err := s.path(key)
	if err != nil {
		return 0, err
	}
	st, err := os.Stat(p)
	if errors.Is(err, fs.ErrNotExist) {
		return 0, ErrNotFound
	}
	if err != nil {
		return 0, err
	}
	return st.Size(), nil
}

// Delete implements Store and prunes directories it leaves empty (up to the
// store root).
func (s *LocalStore) Delete(_ context.Context, key string) error {
	p, err := s.path(key)
	if err != nil {
		return err
	}
	if err := os.Remove(p); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	for d := filepath.Dir(p); d != s.root && strings.HasPrefix(d, s.root+string(filepath.Separator)); d = filepath.Dir(d) {
		if os.Remove(d) != nil { // not empty, or already gone
			break
		}
	}
	return nil //nolint:nilerr // pruning emptied parent directories is best effort: the object is already deleted, and a directory that is not empty or already gone just ends the walk
}

// manifestKey is the sidecar key of a dump object.
func manifestKey(objectKey string) string { return objectKey + ".manifest.json" }

// ManifestKey is the sidecar key of a dump object.
func ManifestKey(objectKey string) string { return manifestKey(objectKey) }

// probeKey is the key used by destination tests.
func probeKey(prefix, id string) string {
	return path.Join(prefix, ".nebulactrl-probe-"+id)
}
