package gitproviders

import (
	"context"
	"fmt"
	"time"
)

// PushHook runs synchronously for every verified push, before the push
// deploys anything. internal/configsync registers here so that one commit
// lands code and project config together: with auto-apply on, the config the
// commit carries is staged before the deployments of the same push snapshot
// their configuration.
//
// A hook must not block the deploy for long: BeforePush gets a context that
// outlives the wait (see PushHookWait), and a push whose hook is still running
// after the wait deploys with the config as it was.
type PushHook interface {
	BeforePush(ctx context.Context, ev PushEvent)
}

const (
	// PushHookWait is how long a push waits for its hook before deploying.
	PushHookWait = 20 * time.Second
	// pushHookRun bounds one hook run, including the part after the wait.
	pushHookRun = 2 * time.Minute
)

// SetPushHook installs the hook (wire time only).
func (s *Service) SetPushHook(h PushHook) { s.pushHook = h }

// runPushHook calls the hook and waits for it at most PushHookWait. A hook that
// panics is logged and ignored; a hook still running after the wait keeps
// running detached from the request and is bounded by pushHookRun.
func (s *Service) runPushHook(ctx context.Context, ev PushEvent) {
	h := s.pushHook
	if h == nil {
		return
	}
	wait := s.pushHookWait
	if wait <= 0 {
		wait = PushHookWait
	}
	hctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), pushHookRun)
	done := make(chan struct{})
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		defer close(done)
		defer cancel()
		defer func() {
			if r := recover(); r != nil {
				s.log.Error("push hook panicked", "repo", ev.RepoFullName, "panic", fmt.Sprint(r))
			}
		}()
		h.BeforePush(hctx, ev)
	}()
	timer := time.NewTimer(wait)
	defer timer.Stop()
	select {
	case <-done:
	case <-timer.C:
		s.log.Warn("push hook still running: deploying with the current configuration", "repo", ev.RepoFullName, "branch", ev.Branch, "commit", shortSHA(ev.CommitSHA))
	case <-ctx.Done():
	}
}
