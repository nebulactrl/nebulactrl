package services

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/nebulactrl/nebulactrl/internal/apierr"
	"github.com/nebulactrl/nebulactrl/internal/db"
	"github.com/nebulactrl/nebulactrl/internal/policy"
	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

// MaxTail bounds the `tail` parameter of log endpoints.
const MaxTail = 5000

// LogEntry is one runtime log line, already redacted.
type LogEntry struct {
	TS          time.Time `json:"ts"`
	Line        string    `json:"line"`
	ServiceID   uuid.UUID `json:"serviceId"`
	ServiceName string    `json:"serviceName,omitempty"`
}

// runningServer returns the server that runs the service's current deployment.
func (s *Service) runningServer(ctx context.Context, svc db.Service) (uuid.UUID, error) {
	if svc.CurrentDeploymentID == nil {
		return uuid.Nil, apierr.Conflict("not_deployed", "the service has no running deployment")
	}
	deps, err := s.q.ListDeploymentsByIDs(ctx, []uuid.UUID{*svc.CurrentDeploymentID})
	if err != nil {
		return uuid.Nil, fmt.Errorf("current deployment: %w", err)
	}
	if len(deps) != 1 || deps[0].ServerID == nil {
		return uuid.Nil, apierr.Conflict("not_deployed", "the service has no running deployment")
	}
	return *deps[0].ServerID, nil
}

func clampTail(tail int) int {
	if tail < 0 {
		return 0
	}
	if tail > MaxTail {
		return MaxTail
	}
	return tail
}

// CheckLogFilter validates the `component` and `process` filters of the log
// endpoints together: they select by different things (a stack's components, a
// service's processes), so only one can be given.
func CheckLogFilter(sc Scope, component, process string) error {
	if component != "" && process != "" {
		return apierr.Unprocessable("validation_failed", "component and process cannot be combined: component is for Compose stacks, process for services with several processes")
	}
	if err := CheckLogComponent(sc, component); err != nil {
		return err
	}
	return CheckLogProcess(sc, process)
}

func (s *Service) openStream(ctx context.Context, svc db.Service, tail int, follow, withName bool, component, process string) (<-chan LogEntry, func(), error) {
	serverID, err := s.runningServer(ctx, svc)
	if err != nil {
		return nil, nil, err
	}
	redact, err := s.RedactorFor(ctx, svc.ID)
	if err != nil {
		return nil, nil, fmt.Errorf("redactor: %w", err)
	}
	process, processDefault, _, err := ResolveServiceProcessFilter(ctx, s.q, svc, process)
	if err != nil {
		return nil, nil, fmt.Errorf("resolve process: %w", err)
	}
	frames, cancel, err := s.disp.SubscribeLogs(ctx, serverID, agentprotocol.LogsSubscribe{
		StreamID: uuid.NewString(), ServiceID: svc.ID.String(), Tail: clampTail(tail), Follow: follow, Component: component,
		Process: process, Default: processDefault,
	})
	if err != nil {
		return nil, nil, err
	}
	name := ""
	if withName {
		name = svc.Name
	}
	out := make(chan LogEntry, 64)
	go func() {
		defer close(out)
		for f := range frames {
			for _, l := range f.Lines {
				select {
				case out <- LogEntry{TS: l.TS, Line: redact(l.Text), ServiceID: svc.ID, ServiceName: name}:
				case <-ctx.Done():
					return
				}
			}
		}
	}()
	return out, cancel, nil
}

// SubscribeServiceLogs streams a service's runtime logs. The channel closes at
// the end of the stream (non-follow) or when cancel is called.
func (s *Service) SubscribeServiceLogs(ctx context.Context, id uuid.UUID, tail int, follow bool) (<-chan LogEntry, func(), error) {
	return s.SubscribeServiceComponentLogs(ctx, id, tail, follow, "")
}

// SubscribeServiceComponentLogs is SubscribeServiceLogs for one component of a
// Compose stack ("" = every component, each line prefixed `<component> | `).
// A component is only valid for stacks and must be one of their components.
func (s *Service) SubscribeServiceComponentLogs(ctx context.Context, id uuid.UUID, tail int, follow bool, component string) (<-chan LogEntry, func(), error) {
	return s.SubscribeServiceProcessLogs(ctx, id, tail, follow, component, "")
}

// SubscribeServiceProcessLogs is SubscribeServiceLogs for one component of a
// Compose stack or one process of a service with processes ("" = all of them,
// each line prefixed `<name> | `). A component is only valid for stacks, a
// process only for services with processes, and the two cannot be combined.
func (s *Service) SubscribeServiceProcessLogs(ctx context.Context, id uuid.UUID, tail int, follow bool, component, process string) (<-chan LogEntry, func(), error) {
	sc, err := LoadScope(ctx, s.q, policy.ScopeLogsRead, id)
	if err != nil {
		return nil, nil, err
	}
	if err := RequireContainer(sc, "runtime logs"); err != nil {
		return nil, nil, err
	}
	if err := CheckLogFilter(sc, component, process); err != nil {
		return nil, nil, err
	}
	return s.openStream(ctx, sc.Service, tail, follow, false, component, process)
}

// SubscribeEnvironmentLogs merges the runtime logs of every deployed service
// of an environment. Services whose server is unreachable are skipped; an
// error is returned only when no service could be subscribed.
func (s *Service) SubscribeEnvironmentLogs(ctx context.Context, envID uuid.UUID, tail int, follow bool) (<-chan LogEntry, func(), error) {
	env, err := LoadEnvironment(ctx, s.q, policy.ScopeLogsRead, envID)
	if err != nil {
		return nil, nil, err
	}
	svcs, err := s.q.ListAllEnvironmentServices(ctx, env.Env.ID)
	if err != nil {
		return nil, nil, fmt.Errorf("list services: %w", err)
	}
	var chans []<-chan LogEntry
	var cancels []func()
	var lastErr error
	for _, svc := range svcs {
		if svc.CurrentDeploymentID == nil {
			continue
		}
		ch, cancel, err := s.openStream(ctx, svc, tail, follow, true, "", "")
		if err != nil {
			lastErr = err
			s.logger.DebugContext(ctx, "environment logs: skipping service", "service", svc.Slug, "err", err)
			continue
		}
		chans = append(chans, ch)
		cancels = append(cancels, cancel)
	}
	if len(chans) == 0 {
		if lastErr != nil {
			return nil, nil, lastErr
		}
		return nil, nil, apierr.Conflict("not_deployed", "no service in this environment is running")
	}
	merged := make(chan LogEntry, 64)
	var wg sync.WaitGroup
	for _, ch := range chans {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for e := range ch {
				select {
				case merged <- e:
				case <-ctx.Done():
					return
				}
			}
		}()
	}
	go func() { wg.Wait(); close(merged) }()
	return merged, func() {
		for _, c := range cancels {
			c()
		}
	}, nil
}
