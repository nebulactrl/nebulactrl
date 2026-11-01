package instsvc

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// QuotaGate decides whether a team may write n more bytes to its built-in S3
// slice. The forwarder calls it for PutObject and UploadPart with the request's
// Content-Length (or x-amz-decoded-content-length): it admits when
//
//	used + in-flight + n <= effective quota
//
// and otherwise answers an error satisfying errors.Is(err, ErrQuotaExceeded)
// (any other error means "cannot decide": the forwarder answers 503). release
// frees the in-flight bytes and must be called once the request is over, whether
// it succeeded or not. Reads and deletes never pass the gate, and nothing is
// ever deleted to make room.
type QuotaGate interface {
	Admit(ctx context.Context, teamID uuid.UUID, n int64) (release func(), err error)
}

var _ QuotaGate = (*Service)(nil)

// gate holds the bytes admitted but not finished, per team, and the debounced
// usage re-reads that follow uploads.
type gate struct {
	mu       sync.Mutex
	inflight map[uuid.UUID]int64
	refresh  map[uuid.UUID]*time.Timer
}

func newGate() gate {
	return gate{inflight: map[uuid.UUID]int64{}, refresh: map[uuid.UUID]*time.Timer{}}
}

// Admit implements QuotaGate.
func (s *Service) Admit(ctx context.Context, teamID uuid.UUID, n int64) (func(), error) {
	if n < 0 {
		n = 0
	}
	// One disk serves every team and the control plane's own database: when it
	// is nearly full nobody writes, whatever their quota says.
	if err := s.checkDisk(n); err != nil {
		return nil, err
	}
	st, err := s.q.GetS3QuotaState(ctx, teamID)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNoSlice
	}
	if err != nil {
		return nil, fmt.Errorf("read quota state: %w", err)
	}
	if st.Deleted {
		return nil, ErrNoSlice
	}
	if st.State != StateReady && st.State != StateFull {
		return nil, ErrNotReady
	}
	quota := effectiveQuota(st.QuotaBytes, st.DefaultQuotaBytes)

	s.gate.mu.Lock()
	if st.UsedBytes+s.gate.inflight[teamID]+n > quota {
		s.gate.mu.Unlock()
		if st.State != StateFull {
			// The slice is full for new writes from now on; the usage job lifts
			// the flag when space is free again.
			if err := s.q.SetSliceState(ctx, setState(st.ID, StateFull, "Built-in backup storage is full.")); err != nil {
				s.log.WarnContext(ctx, "mark slice full", "team_id", teamID, "err", err)
			}
		}
		return nil, ErrQuotaExceeded
	}
	s.gate.inflight[teamID] += n
	s.gate.mu.Unlock()

	var once sync.Once
	return func() {
		once.Do(func() {
			s.gate.mu.Lock()
			if left := s.gate.inflight[teamID] - n; left > 0 {
				s.gate.inflight[teamID] = left
			} else {
				delete(s.gate.inflight, teamID)
			}
			s.gate.mu.Unlock()
			if n > 0 {
				s.scheduleUsageRefresh(teamID)
			}
		})
	}, nil
}

// inflightBytes is what the gate holds for a team right now (tests, metrics).
func (s *Service) inflightBytes(teamID uuid.UUID) int64 {
	s.gate.mu.Lock()
	defer s.gate.mu.Unlock()
	return s.gate.inflight[teamID]
}

// scheduleUsageRefresh re-reads the team's usage shortly after an upload, so the
// bytes it added count as used rather than disappearing between the release and
// the next periodic usage job. One pending read per team: a busy backup does not
// list the bucket per object.
func (s *Service) scheduleUsageRefresh(teamID uuid.UUID) {
	if s.backends[KindS3] == nil || s.refreshDelay <= 0 {
		return
	}
	s.gate.mu.Lock()
	defer s.gate.mu.Unlock()
	if s.gate.refresh[teamID] != nil {
		return
	}
	s.gate.refresh[teamID] = time.AfterFunc(s.refreshDelay, func() {
		s.gate.mu.Lock()
		delete(s.gate.refresh, teamID)
		s.gate.mu.Unlock()
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
		defer cancel()
		if err := s.RefreshTeamUsage(ctx, teamID); err != nil {
			s.log.WarnContext(ctx, "refresh usage after upload", "team_id", teamID, "err", err)
		}
	})
}
