package engine

import (
	"context"
	"sort"
	"sync"
	"time"

	"github.com/nebulactrl/nebulactrl/internal/databases/engines"
	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

const (
	// ReplPollTick is how often the poller looks for members that are due.
	ReplPollTick = 5 * time.Second
	// ReplPollEvery is the default period of one member (ReplicationDesired.PollSeconds).
	ReplPollEvery = 30 * time.Second
	// replPollTimeout bounds one status query.
	replPollTimeout = 20 * time.Second
	replPollWorkers = 4
)

// pollReplication queries every replication member that is due, over the
// in-container socket (no password, no agent_commands traffic), and stores the
// result for observe(). A failed query is recorded in ReplicationStatus.Error and
// never fails anything else.
func (e *Engine) pollReplication(ctx context.Context) {
	now := time.Now()
	e.repl.mu.Lock()
	var due []string
	user := map[string]string{}
	for id, d := range e.repl.desired {
		every := ReplPollEvery
		if d.PollSeconds > 0 {
			every = time.Duration(d.PollSeconds) * time.Second
		}
		if last, ok := e.repl.polled[id]; ok && now.Sub(last) < every {
			continue
		}
		due = append(due, id)
		user[id] = d.User
	}
	e.repl.mu.Unlock()
	if len(due) == 0 {
		return
	}
	sort.Strings(due)

	var wg sync.WaitGroup
	sem := make(chan struct{}, replPollWorkers)
	for _, id := range due {
		wg.Add(1)
		sem <- struct{}{}
		go func() {
			defer wg.Done()
			defer func() { <-sem }()
			e.pollMember(ctx, id, user[id])
		}()
	}
	wg.Wait()
}

// pollMember runs the status query of one service.
func (e *Engine) pollMember(ctx context.Context, serviceID, user string) {
	e.repl.mu.Lock()
	if e.repl.polled == nil {
		e.repl.polled = map[string]time.Time{}
	}
	e.repl.polled[serviceID] = time.Now()
	e.repl.mu.Unlock()

	if e.isInflight(serviceID) {
		return // a command (deploy, promote, ...) works on it; the next poll sees the result
	}
	store := func(st agentprotocol.ReplicationStatus) {
		st.CheckedAt = time.Now().UTC()
		e.repl.mu.Lock()
		defer e.repl.mu.Unlock()
		if _, still := e.repl.desired[serviceID]; !still {
			return
		}
		if e.repl.status == nil {
			e.repl.status = map[string]agentprotocol.ReplicationStatus{}
		}
		e.repl.status[serviceID] = st
	}
	unknown := func(msg string) {
		store(agentprotocol.ReplicationStatus{Role: agentprotocol.ReplRoleUnknown, Error: msg})
	}

	target := e.runningDB(ctx, serviceID)
	if target == nil {
		unknown("the database container is not running")
		return
	}
	plan, err := engines.ReplPlan(engines.Spec{Engine: engines.Postgres, Username: user}, agentprotocol.DBActionReplStatus, engines.ReplArgs{})
	if err != nil {
		unknown(err.Error())
		return
	}
	out, err := e.execPlanIn(ctx, target.ID, plan, newRedactor(nil, nil), replPollTimeout)
	if err != nil {
		unknown(err.Error())
		return
	}
	st, err := engines.ParseReplStatus(out)
	if err != nil {
		unknown(err.Error())
		return
	}
	store(st)
}
