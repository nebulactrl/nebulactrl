package gitproviders_test

import (
	"bytes"
	"context"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/nebulactrl/nebulactrl/internal/gitproviders"
)

// hookFunc adapts a function to gitproviders.PushHook.
type hookFunc func(ctx context.Context, ev gitproviders.PushEvent)

func (h hookFunc) BeforePush(ctx context.Context, ev gitproviders.PushEvent) { h(ctx, ev) }

func TestPushHookRunsBeforeAnyDeploymentOfThePush(t *testing.T) {
	f := newFixture(t)
	conn := f.giteaConnection()
	f.giteaRepo("main")
	f.link(f.gitService("api").ID, conn.Connection.ID, gitproviders.LinkInput{})

	var seen []gitproviders.PushEvent
	var deploymentsAtHook = -1
	f.svc.SetPushHook(hookFunc(func(_ context.Context, ev gitproviders.PushEvent) {
		seen = append(seen, ev)
		deploymentsAtHook = len(f.dep.snapshot())
		time.Sleep(20 * time.Millisecond) // the deploy waits for a hook that is still working
	}))

	body := readTestdata(t, "gitea_push.json")
	code, _ := f.post("gitea", conn.Connection.ID, giteaHeaders(conn.WebhookSecret, body, "h-1"), body)
	require.Equal(t, 200, code)
	f.svc.Wait()

	require.Equal(t, 0, deploymentsAtHook, "the hook finished before the push created its first deployment")
	require.Len(t, f.dep.snapshot(), 1)
	require.Len(t, seen, 1)
	require.Equal(t, conn.Connection.ID, seen[0].ConnectionID)
	require.Equal(t, "acme/shop", seen[0].RepoFullName)
	require.Equal(t, "main", seen[0].Branch)
	require.Equal(t, "bffeb74224043ba2feb48e12f2ac4a0a9de3e04c", seen[0].CommitSHA)
	require.True(t, seen[0].PathsComplete)
	require.NotEmpty(t, seen[0].ChangedPaths)
}

func TestPushHookRunsForPushesNoServiceBuilds(t *testing.T) {
	// An environment can be bound to a repository without any git service in it.
	f := newFixture(t)
	conn := f.giteaConnection()
	f.giteaRepo("main")
	var calls int
	f.svc.SetPushHook(hookFunc(func(context.Context, gitproviders.PushEvent) { calls++ }))

	body := readTestdata(t, "gitea_push.json")
	code, reply := f.post("gitea", conn.Connection.ID, giteaHeaders(conn.WebhookSecret, body, "h-2"), body)
	require.Equal(t, 200, code)
	require.Contains(t, reply["summary"], "no service uses this repository")
	f.svc.Wait()
	require.Equal(t, 1, calls)
}

func TestPushHookRunsEvenWhenTheCommitSkipsDeploys(t *testing.T) {
	f := newFixture(t)
	conn := f.giteaConnection()
	f.giteaRepo("main")
	f.link(f.gitService("api").ID, conn.Connection.ID, gitproviders.LinkInput{})
	var calls int
	f.svc.SetPushHook(hookFunc(func(context.Context, gitproviders.PushEvent) { calls++ }))

	body := bytes.Replace(readTestdata(t, "gitea_push.json"), []byte("Add services folder"), []byte("config only [skip ci]"), -1)
	code, reply := f.post("gitea", conn.Connection.ID, giteaHeaders(conn.WebhookSecret, body, "h-3"), body)
	require.Equal(t, 200, code)
	require.Contains(t, reply["summary"], "[skip ci]")
	f.svc.Wait()
	require.Equal(t, 1, calls, "the config of a [skip ci] commit is still checked")
	require.Empty(t, f.dep.snapshot())
}

func TestSlowPushHookDoesNotBlockTheDeployBeyondTheWait(t *testing.T) {
	f := newFixture(t, func(d *gitproviders.Deps) { d.PushHookWait = 50 * time.Millisecond })
	conn := f.giteaConnection()
	f.giteaRepo("main")
	f.link(f.gitService("api").ID, conn.Connection.ID, gitproviders.LinkInput{})

	release := make(chan struct{})
	var mu sync.Mutex
	var finished bool
	var ctxErr error
	f.svc.SetPushHook(hookFunc(func(ctx context.Context, _ gitproviders.PushEvent) {
		<-release
		mu.Lock()
		defer mu.Unlock()
		finished, ctxErr = true, ctx.Err()
	}))

	body := readTestdata(t, "gitea_push.json")
	start := time.Now()
	code, _ := f.post("gitea", conn.Connection.ID, giteaHeaders(conn.WebhookSecret, body, "h-4"), body)
	require.Equal(t, 200, code)
	require.Less(t, time.Since(start), 5*time.Second)
	require.Len(t, f.dep.snapshot(), 1, "the deploy proceeded with the config as it was")
	mu.Lock()
	require.False(t, finished, "the hook is still running")
	mu.Unlock()

	close(release)
	f.svc.Wait()
	mu.Lock()
	defer mu.Unlock()
	require.True(t, finished, "the hook completes after the push returned")
	require.NoError(t, ctxErr, "its context outlives the webhook request")
}

func TestPanickingPushHookDoesNotStopTheDeploy(t *testing.T) {
	f := newFixture(t)
	conn := f.giteaConnection()
	f.giteaRepo("main")
	f.link(f.gitService("api").ID, conn.Connection.ID, gitproviders.LinkInput{})
	f.svc.SetPushHook(hookFunc(func(context.Context, gitproviders.PushEvent) { panic("boom") }))

	body := readTestdata(t, "gitea_push.json")
	code, _ := f.post("gitea", conn.Connection.ID, giteaHeaders(conn.WebhookSecret, body, "h-5"), body)
	require.Equal(t, 200, code)
	require.Len(t, f.dep.snapshot(), 1)
}
