package main

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/nebulactrl/nebulactrl/internal/apiclient"
)

func TestParseGitPollFlags(t *testing.T) {
	tests := []struct {
		name                    string
		every                   string
		deploy, notifyOnly, off bool
		want                    gitPollChange
		wantErr                 string
	}{
		{name: "no flags only shows"},
		{name: "every", every: "15m", want: gitPollChange{interval: 15}},
		{name: "every hour", every: "60m", want: gitPollChange{interval: 60}},
		{name: "every 1h", every: "1h", want: gitPollChange{interval: 60}},
		{name: "deploy", deploy: true, want: gitPollChange{deploy: new(true)}},
		{name: "notify only", notifyOnly: true, want: gitPollChange{deploy: new(false)}},
		{name: "every and notify only", every: "1m", notifyOnly: true, want: gitPollChange{interval: 1, deploy: new(false)}},
		{name: "off", off: true, want: gitPollChange{off: true}},
		{name: "unknown interval", every: "2m", wantErr: "use 1m, 5m, 15m or 60m"},
		{name: "deploy and notify only", deploy: true, notifyOnly: true, wantErr: "exclude each other"},
		{name: "off with every", off: true, every: "5m", wantErr: "--off cannot be combined"},
		{name: "off with deploy", off: true, deploy: true, wantErr: "--off cannot be combined"},
		{name: "off with notify only", off: true, notifyOnly: true, wantErr: "--off cannot be combined"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := parseGitPollFlags(tc.every, tc.deploy, tc.notifyOnly, tc.off)
			if tc.wantErr != "" {
				require.ErrorContains(t, err, tc.wantErr)
				return
			}
			require.NoError(t, err)
			require.Equal(t, tc.want, got)
			require.Equal(t, tc.want != gitPollChange{}, got.set())
		})
	}
}

func TestGitPollChangeBodyKeepsWhatTheFlagsLeaveOut(t *testing.T) {
	on15NoDeploy := apiclient.GitPollView{Enabled: true, IntervalMinutes: 15, Deploy: false}
	offView := apiclient.GitPollView{Enabled: false, IntervalMinutes: 0, Deploy: true}
	tests := []struct {
		name         string
		change       gitPollChange
		cur          apiclient.GitPollView
		wantEnabled  bool
		wantInterval int64
		wantDeploy   bool
	}{
		{"every keeps the deploy setting", gitPollChange{interval: 1}, on15NoDeploy, true, 1, false},
		{"deploy keeps the interval", gitPollChange{deploy: new(true)}, on15NoDeploy, true, 15, true},
		{"notify only while off enables at 5m", gitPollChange{deploy: new(false)}, offView, true, 5, false},
		{"every while off defaults to deploying", gitPollChange{interval: 60}, offView, true, 60, true},
		{"every ignores a stale deploy while off", gitPollChange{interval: 5}, apiclient.GitPollView{Deploy: false}, true, 5, true},
		{"off keeps the stored values", gitPollChange{off: true}, on15NoDeploy, false, 15, false},
		{"off from never set", gitPollChange{off: true}, offView, false, 5, true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			b := tc.change.body(tc.cur)
			require.Equal(t, tc.wantEnabled, b.Enabled)
			require.NotNil(t, b.IntervalMinutes)
			require.Equal(t, tc.wantInterval, int64(*b.IntervalMinutes))
			require.NotNil(t, b.Deploy)
			require.Equal(t, tc.wantDeploy, *b.Deploy)
		})
	}
}

func TestGitPollLine(t *testing.T) {
	now := time.Date(2026, 11, 3, 12, 0, 0, 0, time.UTC)
	checked := now.Add(-2 * time.Minute)
	tests := []struct {
		name string
		view apiclient.GitPollView
		want string
	}{
		{"off", apiclient.GitPollView{}, "Not checking for new commits."},
		{"on, up to date", apiclient.GitPollView{Enabled: true, IntervalMinutes: 5, Deploy: true, CheckedAt: &checked, HeadSha: new("abc1234def"), HeadBranch: new("main")},
			"Checking main every 5 min · deploys new commits · last checked 2m ago · head abc1234 (running)"},
		{"on, new commit", apiclient.GitPollView{Enabled: true, IntervalMinutes: 15, Deploy: true, CheckedAt: &checked, HeadSha: new("abc1234def"), HeadBranch: new("main"), UpdateAvailable: true},
			"Checking main every 15 min · deploys new commits · last checked 2m ago · new commit abc1234 on main"},
		{"notify only every minute", apiclient.GitPollView{Enabled: true, IntervalMinutes: 1, CheckedAt: &checked, HeadSha: new("abc1234def"), HeadBranch: new("dev"), UpdateAvailable: true},
			"Checking dev every minute · shows new commits (no deploy) · last checked 2m ago · new commit abc1234 on dev"},
		{"every hour, not checked yet", apiclient.GitPollView{Enabled: true, IntervalMinutes: 60, Deploy: true},
			"Checking the branch every hour · deploys new commits · not checked yet"},
		{"check failed", apiclient.GitPollView{Enabled: true, IntervalMinutes: 5, Deploy: true, CheckedAt: &checked, HeadBranch: new("main"), Error: new("repository not found")},
			"Checking main every 5 min · deploys new commits · last checked 2m ago · last check failed: repository not found"},
		{"off after a manual check", apiclient.GitPollView{HeadSha: new("abc1234def"), HeadBranch: new("main"), UpdateAvailable: true},
			"Not checking for new commits · new commit abc1234 on main"},
		{"off, manual check failed", apiclient.GitPollView{Error: new("timeout")},
			"Not checking for new commits · last check failed: timeout"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			require.Equal(t, tc.want, gitPollLine(tc.view, now))
		})
	}
}

func TestAgoText(t *testing.T) {
	now := time.Date(2026, 11, 3, 12, 0, 0, 0, time.UTC)
	for d, want := range map[time.Duration]string{
		3 * time.Second:  "just now",
		30 * time.Second: "30s ago",
		5 * time.Minute:  "5m ago",
		3 * time.Hour:    "3h ago",
		72 * time.Hour:   "3d ago",
	} {
		require.Equal(t, want, agoText(now.Add(-d), now))
	}
}

func TestServicesGitPoll(t *testing.T) {
	poll := svSvc + "/git-poll"
	f := newCfgFake(t)
	f.reply("GET", poll, 200, map[string]any{"enabled": false, "intervalMinutes": 5, "deploy": true, "updateAvailable": false})
	f.reply("PUT", poll, 200, map[string]any{"enabled": true, "intervalMinutes": 15, "deploy": false, "updateAvailable": false, "headBranch": "main"})
	f.reply("POST", poll+"/check", 200, map[string]any{"enabled": true, "intervalMinutes": 15, "deploy": false, "updateAvailable": true,
		"headBranch": "main", "headSha": "abcdef0123", "checkedAt": time.Now().UTC().Format(time.RFC3339)})

	r := f.run("services", "git-poll", fakeSvcID)
	require.Equal(t, 0, r.code, r.all())
	require.Equal(t, "Not checking for new commits.\n", r.stdout)
	require.Empty(t, f.calls("PUT", poll), "no flags only reads")

	r = f.run("services", "git-poll", fakeSvcID, "--every", "15m", "--notify-only")
	require.Equal(t, 0, r.code, r.all())
	require.Equal(t, "Checking main every 15 min · shows new commits (no deploy) · not checked yet\n", r.stdout)
	body := f.only("PUT", poll).json(t)
	require.Equal(t, map[string]any{"enabled": true, "intervalMinutes": float64(15), "deploy": false}, body, "all three fields are sent")

	r = f.run("services", "git-poll", fakeSvcID, "--check")
	require.Equal(t, 0, r.code, r.all())
	require.Contains(t, r.stdout, "new commit abcdef0 on main")
	require.Len(t, f.calls("PUT", poll), 1, "--check alone changes nothing")

	r = f.run("services", "git-poll", fakeSvcID, "--off", "--check")
	require.Equal(t, 0, r.code, r.all())
	require.Len(t, f.calls("PUT", poll), 2, "the setter is applied before the check")
	require.Len(t, f.calls("POST", poll+"/check"), 2)
	require.Equal(t, false, f.calls("PUT", poll)[1].json(t)["enabled"])

	r = f.run("--json", "services", "git-poll", fakeSvcID)
	require.Equal(t, 0, r.code, r.all())
	require.Equal(t, false, decode[map[string]any](t, r.stdout)["enabled"])
}

func TestServicesGitPollRejectsBadFlagsAndRefusals(t *testing.T) {
	poll := svSvc + "/git-poll"
	f := newCfgFake(t)

	r := f.run("services", "git-poll", fakeSvcID, "--off", "--every", "5m")
	require.Equal(t, 2, r.code)
	require.Contains(t, r.err, "--off cannot be combined")
	r = f.run("services", "git-poll", fakeSvcID, "--deploy", "--notify-only")
	require.Equal(t, 2, r.code)
	require.Contains(t, r.err, "exclude each other")
	r = f.run("services", "git-poll", fakeSvcID, "--every", "3m")
	require.Equal(t, 2, r.code)
	require.Contains(t, r.err, "use 1m, 5m, 15m or 60m")
	require.Empty(t, f.calls("GET", poll), "flag errors are reported before any request")

	f.problem("GET", poll, 422, "git_poll_not_applicable", "the service has no git source")
	r = f.run("services", "git-poll", fakeSvcID)
	require.Equal(t, 1, r.code)
	require.Contains(t, r.err, "the service has no git source (git_poll_not_applicable, HTTP 422)")
}
