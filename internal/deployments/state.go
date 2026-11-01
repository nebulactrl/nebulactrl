// Package deployments owns deployments, releases and their state machine.
package deployments

import "fmt"

// Status is a deployment lifecycle status.
type Status string

// Deployment statuses.
const (
	Created        Status = "created"
	Queued         Status = "queued"
	Preparing      Status = "preparing"
	Building       Status = "building"
	Releasing      Status = "releasing"
	Verifying      Status = "verifying"
	Succeeded      Status = "succeeded"
	Failed         Status = "failed"
	Canceled       Status = "canceled"
	RolledBack     Status = "rolled_back"
	RollbackFailed Status = "rollback_failed"
)

// ActiveStatuses are non-terminal statuses; the partial unique index
// deployments_one_active covers exactly this set.
var ActiveStatuses = []Status{Created, Queued, Preparing, Building, Releasing, Verifying}

// terminal is the set every active status may end in.
var terminal = []Status{Succeeded, Failed, Canceled, RolledBack, RollbackFailed}

// transitions is the allowed transition table. Image sources skip Building
// (preparing → releasing). Any active status may end in failed or canceled;
// rolled_back and rollback_failed only follow releasing/verifying.
var transitions = map[Status][]Status{
	Created:   {Queued, Failed, Canceled},
	Queued:    {Preparing, Failed, Canceled},
	Preparing: {Building, Releasing, Failed, Canceled},
	Building:  {Releasing, Failed, Canceled},
	Releasing: {Verifying, Succeeded, Failed, Canceled, RolledBack, RollbackFailed},
	Verifying: {Succeeded, Failed, Canceled, RolledBack, RollbackFailed},
}

// IsTerminal reports whether s is a final status.
func (s Status) IsTerminal() bool {
	for _, t := range terminal {
		if s == t {
			return true
		}
	}
	return false
}

// IsActive reports whether s is in the active set.
func (s Status) IsActive() bool {
	for _, a := range ActiveStatuses {
		if s == a {
			return true
		}
	}
	return false
}

// Valid reports whether s is a known status.
func (s Status) Valid() bool { return s.IsActive() || s.IsTerminal() }

// TransitionError is returned for a transition outside the table.
type TransitionError struct{ From, To Status }

func (e *TransitionError) Error() string {
	return fmt.Sprintf("invalid deployment transition %s → %s", e.From, e.To)
}

// Path returns the shortest chain of statuses from → to (exclusive of from,
// inclusive of to), used when an agent step skips intermediate statuses (e.g.
// queued → releasing via preparing). It returns nil if to is unreachable.
func Path(from, to Status) []Status {
	type node struct {
		s    Status
		prev *node
	}
	seen := map[Status]bool{from: true}
	q := []*node{{s: from}}
	for len(q) > 0 {
		n := q[0]
		q = q[1:]
		for _, nx := range transitions[n.s] {
			if seen[nx] {
				continue
			}
			seen[nx] = true
			nn := &node{s: nx, prev: n}
			if nx == to {
				var out []Status
				for p := nn; p.prev != nil; p = p.prev {
					out = append([]Status{p.s}, out...)
				}
				return out
			}
			q = append(q, nn)
		}
	}
	return nil
}
