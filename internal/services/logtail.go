package services

import (
	"context"
	"sort"
	"time"

	"github.com/google/uuid"
)

// internalTailTimeout bounds how long TailRuntimeLogsInternal waits for the agent.
const internalTailTimeout = 8 * time.Second

// TailRuntimeLogsInternal returns the last n runtime log lines of a service,
// oldest first, redacted for the service's secrets. It is for system callers
// without a principal (health alerts): it performs no policy check, so its
// result must only go to places the service's project already trusts.
func (s *Service) TailRuntimeLogsInternal(ctx context.Context, id uuid.UUID, n int) ([]string, error) {
	sc, err := LoadScopeInternal(ctx, s.q, id)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(ctx, internalTailTimeout)
	defer cancel()
	ch, stop, err := s.openStream(ctx, sc.Service, n, false, false, "", "")
	if err != nil {
		return nil, err
	}
	defer stop()
	var entries []LogEntry
loop:
	for {
		select {
		case <-ctx.Done():
			break loop
		case e, ok := <-ch:
			if !ok {
				break loop
			}
			entries = append(entries, e)
		}
	}
	sort.SliceStable(entries, func(i, j int) bool { return entries[i].TS.Before(entries[j].TS) })
	if len(entries) > n {
		entries = entries[len(entries)-n:]
	}
	out := make([]string, len(entries))
	for i, e := range entries {
		out[i] = e.Line
	}
	return out, nil
}
