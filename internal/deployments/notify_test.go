package deployments

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/nebulactrl/nebulactrl/internal/db"
	"github.com/nebulactrl/nebulactrl/internal/notifications"
	"github.com/nebulactrl/nebulactrl/internal/notifications/notificationstest"
)

func finishedCtx() depCtx {
	return depCtx{
		Dep: db.Deployment{
			ID: uuid.MustParse("11111111-2222-4333-8444-555555555555"), ServiceID: uuid.New(), Trigger: "git_push",
			CommitSha: "0123456789abcdef", CommitBranch: "main",
		},
		ProjectID: uuid.New(), ServiceName: "api", ServiceSlug: "api",
	}
}

func TestNotifyFinishedMapsTerminalStatuses(t *testing.T) {
	cases := []struct {
		status   Status
		msg      string
		typ      string
		severity string // "" = the type's default
		title    string
	}{
		{Succeeded, "", notifications.TypeDeploySucceeded, "", "api deployed"},
		{Failed, "Health check failed\nlast 50 log lines: SECRET=hunter2", notifications.TypeDeployFailed, "", "api failed to deploy"},
		{RollbackFailed, "restore failed", notifications.TypeDeployFailed, "", "api failed to deploy and could not be restored"},
		{RolledBack, "verification failed", notifications.TypeDeployRolledBack, "", "api was rolled back"},
	}
	for _, tc := range cases {
		rec := &notificationstest.Recorder{}
		s := &Service{Deps: Deps{Notifier: rec}}
		dc := finishedCtx()
		s.notifyFinished(context.Background(), dc, tc.status, tc.msg)
		evs := rec.Events()
		require.Len(t, evs, 1, string(tc.status))
		ev := evs[0]
		require.Equal(t, tc.typ, ev.Type)
		require.Equal(t, tc.title, ev.Title)
		require.Equal(t, dc.ProjectID, *ev.ProjectID)
		require.Equal(t, dc.Dep.ServiceID, *ev.ServiceID)
		require.Equal(t, "?tab=deployments", ev.URL, "opens the inspector on the deployments tab")
		require.Equal(t, "git_push", ev.Fields["Trigger"])
		require.Equal(t, "0123456", ev.Fields["Commit"])
		require.Equal(t, "main", ev.Fields["Branch"])
		require.Equal(t, "11111111", ev.Fields["Deployment"])
		// Only the first line of the status message goes to third parties: the
		// rest can be container output.
		require.NotContains(t, ev.Body, "hunter2")
		if tc.status != Succeeded {
			require.Contains(t, ev.Body, tc.msg[:5])
		}
	}
}

func TestNotifyFinishedIgnoresCanceledAndActive(t *testing.T) {
	rec := &notificationstest.Recorder{}
	s := &Service{Deps: Deps{Notifier: rec}}
	for _, st := range []Status{Canceled, Queued, Building, Verifying} {
		s.notifyFinished(context.Background(), finishedCtx(), st, "x")
	}
	require.Empty(t, rec.Events())
	// And without a notifier it is a no-op.
	(&Service{}).notifyFinished(context.Background(), finishedCtx(), Failed, "x")
}
