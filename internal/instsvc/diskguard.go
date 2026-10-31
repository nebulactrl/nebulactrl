package instsvc

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/shirou/gopsutil/v4/disk"

	"github.com/nebulactrl/nebulactrl/internal/db"
)

// The built-in services share one disk with the control plane's database and the
// local backups. Per-team quotas bound each team; the disk guard bounds all of
// them together: when the filesystem is nearly full, writes to the built-in S3
// and registry are refused (reads, deletes and the control plane itself keep
// working), so a full disk can never take the database down.
const (
	// DiskReserveBytes is the free space always kept (2 GiB) ...
	DiskReserveBytes uint64 = 2 << 30
	// ... or this share of the disk, whichever is more (5%).
	diskReservePercent = 5

	diskCacheTTL = 30 * time.Second

	// RegistryQuotaBytes is how many bytes of images one team may keep in the
	// built-in registry. The count is the sum of the sizes the pushed manifests
	// list, per manifest: layers shared by several images are counted for each,
	// so the real use is lower.
	RegistryQuotaBytes int64 = 50 << 30

	registryUsageTTL = 15 * time.Second
)

// ErrDiskLow means the disk that holds the instance services is nearly full.
var ErrDiskLow = errors.New("the disk holding the built-in services is almost full")

// DiskUsageFunc reports the free and total bytes of the disk to guard.
type DiskUsageFunc func() (free, total uint64, err error)

// DiskUsageOf reads the filesystem that holds path. In the control plane's
// container "/" is the host's Docker data filesystem, where the instance
// services' named volumes live by default.
func DiskUsageOf(path string) DiskUsageFunc {
	return func() (uint64, uint64, error) {
		u, err := disk.Usage(path)
		if err != nil {
			return 0, 0, err
		}
		return u.Free, u.Total, nil
	}
}

// diskGuard caches the answer of a DiskUsageFunc. The zero value guards nothing.
type diskGuard struct {
	usage DiskUsageFunc

	mu      sync.Mutex
	at      time.Time
	free    uint64
	total   uint64
	checked bool
}

// check says whether n more bytes may be written: ErrDiskLow when they would
// leave less than the reserve free. A disk that cannot be read does not block.
func (g *diskGuard) check(now time.Time, n int64) error {
	if g.usage == nil {
		return nil
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	if !g.checked || now.Sub(g.at) > diskCacheTTL || now.Before(g.at) {
		free, total, err := g.usage()
		if err != nil {
			g.checked = false
			return nil
		}
		g.free, g.total, g.at, g.checked = free, total, now, true
	}
	reserve := max(DiskReserveBytes, g.total/100*diskReservePercent)
	need := reserve
	if n > 0 {
		need += uint64(n)
	}
	if g.free < need {
		return ErrDiskLow
	}
	return nil
}

// checkDisk is the guard of the service's disk.
func (s *Service) checkDisk(n int64) error { return s.disk.check(s.now(), n) }

// CheckRegistryWrite implements forward.RegistryLedger: a team may push while
// the disk has room and its images (as far as the ledger knows) are under
// RegistryQuotaBytes. Deletes never pass through it.
func (s *Service) CheckRegistryWrite(ctx context.Context, teamID uuid.UUID) error {
	if err := s.checkDisk(0); err != nil {
		return err
	}
	used, err := s.registryBytes(ctx, teamID)
	if err != nil {
		return err
	}
	if used >= RegistryQuotaBytes {
		return ErrQuotaExceeded
	}
	return nil
}

// registryBytes is the ledger's byte count of a team, cached briefly: a push
// makes many requests.
func (s *Service) registryBytes(ctx context.Context, teamID uuid.UUID) (int64, error) {
	s.regUsage.mu.Lock()
	if c, ok := s.regUsage.byTeam[teamID]; ok && s.now().Sub(c.at) < registryUsageTTL {
		s.regUsage.mu.Unlock()
		return c.bytes, nil
	}
	s.regUsage.mu.Unlock()
	used, err := s.q.BuiltinRegistryBytes(ctx, teamID)
	if err != nil {
		return 0, fmt.Errorf("read registry usage: %w", err)
	}
	s.regUsage.mu.Lock()
	if s.regUsage.byTeam == nil {
		s.regUsage.byTeam = map[uuid.UUID]registryUsage{}
	}
	s.regUsage.byTeam[teamID] = registryUsage{bytes: used, at: s.now()}
	s.regUsage.mu.Unlock()
	return used, nil
}

// registryUsages caches registryBytes per team.
type registryUsages struct {
	mu     sync.Mutex
	byTeam map[uuid.UUID]registryUsage
}

type registryUsage struct {
	bytes int64
	at    time.Time
}

// RecordRegistryPush implements forward.RegistryLedger. The new manifest counts
// at once.
func (s *Service) RecordRegistryPush(ctx context.Context, teamID uuid.UUID, repository, digest string, size int64) error {
	if err := s.q.RecordPushedRegistryImage(ctx, db.RecordPushedRegistryImageParams{
		WorkspaceID: teamID, Repository: repository, Digest: digest, SizeBytes: size,
	}); err != nil {
		return fmt.Errorf("record registry push: %w", err)
	}
	s.regUsage.mu.Lock()
	delete(s.regUsage.byTeam, teamID)
	s.regUsage.mu.Unlock()
	return nil
}
