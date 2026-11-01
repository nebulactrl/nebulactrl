package fleet

import (
	"fmt"
	"sort"

	"github.com/google/uuid"
)

const mib = int64(1) << 20

// unknownHeadroom ranks a host without memory data below every host that has
// some.
const unknownHeadroom = int64(-1) << 62

// Host is the capacity view of one schedulable (or not) server.
type Host struct {
	ID   uuid.UUID
	Name string
	// Eligible hosts may receive services: online, not draining or drained, not
	// revoked.
	Eligible bool
	// HasData is false until the server reported its memory.
	HasData   bool
	MemTotal  int64
	MemUsed   int64
	Committed int64 // sum of the memory limits of the services placed on it

	assigned int64 // demand of moves already planned onto it
}

// Headroom is the memory that can still be handed out: the total minus the
// larger of what is used and what is promised through limits, minus moves
// already assigned. Unknown without data.
func (h *Host) Headroom() int64 {
	if !h.HasData {
		return unknownHeadroom
	}
	used := h.MemUsed
	if h.Committed > used {
		used = h.Committed
	}
	return h.MemTotal - used - h.assigned
}

// Demand is the memory a moving service needs on its target.
type Demand struct {
	// Bytes is the service's limit, or its observed usage when it has none.
	Bytes int64
	// Limited reports whether Bytes comes from a configured limit.
	Limited bool
}

// PickTarget returns the eligible host with the most headroom (ties: name, then
// ID) or nil, and reserves demand on it.
func PickTarget(hosts []*Host, d Demand) *Host {
	var best *Host
	for _, h := range hosts {
		if !h.Eligible {
			continue
		}
		if best == nil || better(h, best) {
			best = h
		}
	}
	if best != nil {
		best.assigned += d.Bytes
	}
	return best
}

func better(a, b *Host) bool {
	ha, hb := a.Headroom(), b.Headroom()
	if ha != hb {
		return ha > hb
	}
	if a.Name != b.Name {
		return a.Name < b.Name
	}
	return a.ID.String() < b.ID.String()
}

// Reserve books demand on a host chosen by the caller.
func (h *Host) Reserve(d Demand) { h.assigned += d.Bytes }

// Fit evaluates the hosts that received moves: it returns whether every
// target can hold what was assigned to it, and the reasons otherwise (or when
// a check was not possible).
func Fit(hosts []*Host) (fits bool, reasons []string) {
	fits, reasons = true, []string{}
	sorted := append([]*Host(nil), hosts...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].Name < sorted[j].Name })
	for _, h := range sorted {
		if h.assigned == 0 {
			continue
		}
		if !h.HasData {
			reasons = append(reasons, fmt.Sprintf("%s has not reported its memory yet, so the capacity check was skipped.", h.Name))
			continue
		}
		if free := h.Headroom(); free < 0 {
			fits = false
			reasons = append(reasons, fmt.Sprintf("%s would be %s over capacity: the moves need %s but only %s is free.",
				h.Name, FormatBytes(-free), FormatBytes(h.assigned), FormatBytes(h.assigned+free)))
		}
	}
	return fits, reasons
}

// Overcommitted reports whether the memory limits of a server's services add up
// to more than the host has.
func Overcommitted(committed, memTotal int64) bool {
	return memTotal > 0 && committed > memTotal
}

// FormatBytes renders a size for messages ("512 MiB", "1.5 GiB").
func FormatBytes(n int64) string {
	if n < 0 {
		n = 0
	}
	switch {
	case n >= int64(1)<<30:
		v := float64(n) / float64(int64(1)<<30)
		if v == float64(int64(v)) {
			return fmt.Sprintf("%d GiB", int64(v))
		}
		return fmt.Sprintf("%.1f GiB", v)
	case n >= mib:
		return fmt.Sprintf("%d MiB", (n+mib/2)/mib)
	default:
		return fmt.Sprintf("%d KiB", n>>10)
	}
}
