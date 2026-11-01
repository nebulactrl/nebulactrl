package deployments

import (
	"context"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"

	"github.com/nebulactrl/nebulactrl/internal/apierr"
	"github.com/nebulactrl/nebulactrl/internal/services"
)

const (
	// providerWaitPoll is how often a waiting consumer's DeployJob re-checks.
	providerWaitPoll = 3 * time.Second
	// providerWaitMax bounds how long a consumer waits for its providers.
	providerWaitMax = 10 * time.Minute
)

// OrderByReferences returns ids ordered so that a service comes after every
// service it references (edges point consumer → provider; edges touching
// services outside ids are ignored). Ties keep the input order. Services that
// take part in a reference cycle cannot be ordered and keep their input order,
// after everything that could be placed.
func OrderByReferences(ids []uuid.UUID, edges []services.RefEdge) []uuid.UUID {
	inSet := make(map[uuid.UUID]bool, len(ids))
	for _, id := range ids {
		inSet[id] = true
	}
	providers := map[uuid.UUID][]uuid.UUID{}
	for _, e := range edges {
		if inSet[e.From] && inSet[e.To] && e.From != e.To {
			providers[e.From] = append(providers[e.From], e.To)
		}
	}
	placed := make(map[uuid.UUID]bool, len(ids))
	out := make([]uuid.UUID, 0, len(ids))
	remaining := append([]uuid.UUID(nil), ids...)
	for len(remaining) > 0 {
		pick := -1
		for i, id := range remaining {
			ready := true
			for _, p := range providers[id] {
				if !placed[p] {
					ready = false
					break
				}
			}
			if ready {
				pick = i
				break
			}
		}
		if pick < 0 {
			// A cycle: keep the input order for what is left.
			out = append(out, remaining...)
			break
		}
		id := remaining[pick]
		placed[id] = true
		out = append(out, id)
		remaining = append(remaining[:pick], remaining[pick+1:]...)
	}
	return out
}

// cleanMessage trims the optional deployment message and enforces its limit.
func cleanMessage(m string) (string, error) {
	m = strings.TrimSpace(m)
	if n := utf8.RuneCountInString(m); n > MaxMessageLen {
		return "", apierr.Unprocessable("validation_failed", fmt.Sprintf("message must be at most %d characters (got %d)", MaxMessageLen, n))
	}
	return m, nil
}

func messagePtr(m string) *string {
	if m == "" {
		return nil
	}
	return &m
}

// providersPending reports whether any deployment in waitFor is still active
// and the wait has not exceeded providerWaitMax (measured from since).
func (s *Service) providersPending(ctx context.Context, waitFor []uuid.UUID, since time.Time) (bool, error) {
	if len(waitFor) == 0 || time.Since(since) >= providerWaitMax {
		return false, nil
	}
	deps, err := s.q.ListDeploymentsByIDs(ctx, waitFor)
	if err != nil {
		return false, fmt.Errorf("list provider deployments: %w", err)
	}
	for _, d := range deps {
		if Status(d.Status).IsActive() {
			return true, nil
		}
	}
	return false, nil
}
