package configsync_test

// DB-backed tests of the [services.<s>.tailnet] table: the plan shows it as one staged
// config.tailnet change, apply stages it through the services use case (which asks the team's
// tailnet integration), a live Funnel survives, and a fresh export plans to nothing.

import (
	"context"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/stretchr/testify/require"

	"github.com/nebulactrl/nebulactrl/internal/apierr"
	"github.com/nebulactrl/nebulactrl/internal/configsync"
	"github.com/nebulactrl/nebulactrl/internal/services"
	"github.com/nebulactrl/nebulactrl/internal/tailnet/tailnetcfg"
)

// tailnetHookFake stands in for the tailnet domain: it records what the services use case asks
// it, and can refuse like a team without the integration.
type tailnetHookFake struct {
	validated []tailnetcfg.TailnetConfig
	changes   [][2]*tailnetcfg.TailnetConfig
	err       error
}

func (h *tailnetHookFake) ValidateConfig(_ context.Context, _ services.Scope, cfg tailnetcfg.TailnetConfig) error {
	h.validated = append(h.validated, cfg)
	return h.err
}

func (h *tailnetHookFake) ConfigChanged(_ context.Context, _ pgx.Tx, _ services.Scope, from, to *tailnetcfg.TailnetConfig) error {
	h.changes = append(h.changes, [2]*tailnetcfg.TailnetConfig{from, to})
	return nil
}

func (h *tailnetHookFake) ServiceDeleted(context.Context, pgx.Tx, services.Scope) error { return nil }

func (f *applyFixture) tailnetHook() *tailnetHookFake {
	h := &tailnetHookFake{}
	f.svcs.SetTailnetHook(h)
	return h
}

// applyTailnetTable is the admin UI of a deployment that must be tailnet-only: TCP 443 to the
// service port with TLS, and no public router.
const applyTailnetTable = `
[services.web.tailnet]
enabled = true
hostname = "web-admin"
extra_tags = ["tag:admin"]
tcp = [{ listen = 443, target = 8641, tls = true }]
hide_public = true
`

func applyChange(t *testing.T, ps configsync.PlanService, field string) configsync.PlanChange {
	t.Helper()
	for _, c := range ps.Changes {
		if c.Field == field {
			return c
		}
	}
	t.Fatalf("no change of %s in %s", field, applyJSON(ps.Changes))
	return configsync.PlanChange{}
}

func TestApplyStagesTheTailnetTable(t *testing.T) {
	f := newApplyFixture(t)
	hook := f.tailnetHook()
	web := f.uiService(f.staging, "web", services.SourceImage, services.Config{Image: "nginx:1.27", Port: 8641})
	file := applyWebFile(8641, applyTailnetTable)

	pl := f.plan("staging", file)
	require.True(t, pl.Valid, applyJSON(pl.Diagnostics))
	ps := applyPlanService(t, pl, "web")
	require.Equal(t, configsync.ActionUpdate, ps.Action)
	c := applyChange(t, ps, "config.tailnet")
	require.Equal(t, "services.web.tailnet", c.Path)
	require.Equal(t, configsync.OpSet, c.Op)
	require.Equal(t, configsync.AppliesStaged, c.Applies, "a tailnet change is staged like every config change")
	require.Equal(t, "off", c.From)
	require.Equal(t, "on, hostname web-admin, tcp 443→8641 tls, tags tag:admin, tailnet only", c.To)

	before := f.row(web.ID)
	res := f.mustApply(file, applyWithPlanID(pl.PlanID))
	require.Equal(t, configsync.StatusStaged, res.Status, applyJSON(res.Steps))
	after := f.row(web.ID)
	require.Equal(t, before.Generation+1, after.Generation)

	cfg := f.cfgOf(web.ID)
	require.Equal(t, &tailnetcfg.TailnetConfig{
		Enabled: true, Hostname: "web-admin", ExtraTags: []string{"tag:admin"}, HidePublic: true,
		TCP: []tailnetcfg.TCPForward{{Listen: 443, Target: 8641, TLS: true}},
	}, cfg.Tailnet)
	require.Equal(t, 8641, cfg.Port)
	require.Contains(t, applyOwnedKeys(f.owned(web.ID)), "config.tailnet")

	require.Len(t, hook.validated, 1, "the integration is asked, as for PATCH /services/{id}")
	require.Equal(t, [][2]*tailnetcfg.TailnetConfig{{nil, cfg.Tailnet}}, hook.changes, "and the change is audited")

	// The same file again changes nothing.
	again := f.plan("staging", file)
	require.Empty(t, applyPlanService(t, again, "web").Changes, applyJSON(again.Services))

	// A fresh export of the environment plans to no change.
	ex, err := f.cfg.Export(f.admin, f.project.ID, "staging", "", false, false)
	require.NoError(t, err)
	require.Contains(t, ex.File, "[services.web.tailnet]")
	fresh := f.plan("staging", ex.File)
	require.True(t, fresh.Valid, applyJSON(fresh.Diagnostics)+ex.File)
	require.Empty(t, applyPlanService(t, fresh, "web").Changes, applyJSON(fresh.Services)+ex.File)
}

func TestApplyTailnetKeepsALiveFunnel(t *testing.T) {
	f := newApplyFixture(t)
	hook := f.tailnetHook()
	web := f.uiService(f.staging, "web", services.SourceImage, services.Config{
		Image: "nginx:1.27", Port: 8641,
		Tailnet: &tailnetcfg.TailnetConfig{Enabled: true, Hostname: "web-ts", Web: true, Funnel: true},
	})

	// A file that says what is live, without Funnel, has nothing to change.
	same := applyWebFile(8641, "\n[services.web.tailnet]\nenabled = true\nhostname = \"web-ts\"\nweb = true\n")
	pl := f.plan("staging", same)
	require.True(t, pl.Valid, applyJSON(pl.Diagnostics))
	require.Empty(t, applyPlanService(t, pl, "web").Changes, applyJSON(pl.Services))

	// A file that changes the table keeps the live Funnel.
	renamed := applyWebFile(8641, "\n[services.web.tailnet]\nenabled = true\nhostname = \"web-ts2\"\nweb = true\n")
	pl = f.plan("staging", renamed)
	require.True(t, pl.Valid, applyJSON(pl.Diagnostics))
	require.Equal(t, []string{"config.tailnet"}, applyChangeFields(applyPlanService(t, pl, "web")))
	res := f.mustApply(renamed, applyWithPlanID(pl.PlanID))
	require.Equal(t, configsync.StatusStaged, res.Status, applyJSON(res.Steps))
	cfg := f.cfgOf(web.ID)
	require.NotNil(t, cfg.Tailnet)
	require.Equal(t, "web-ts2", cfg.Tailnet.Hostname)
	require.True(t, cfg.Tailnet.Funnel, "a file apply never changes Funnel")
	require.True(t, hook.validated[len(hook.validated)-1].Funnel)

	// Dropping the web exposure under a live Funnel is refused at plan time.
	noWeb := applyWebFile(8641, "\n[services.web.tailnet]\nenabled = true\ntcp = [{ listen = 5432 }]\n")
	pl = f.plan("staging", noWeb)
	require.False(t, pl.Valid)
	var found bool
	for _, d := range pl.Diagnostics {
		if d.Code == "invalid_value" && d.Path == "services.web.tailnet" {
			found = true
			require.Contains(t, d.Message, "Funnel")
		}
	}
	require.True(t, found, applyJSON(pl.Diagnostics))
	require.Equal(t, configsync.ActionBlocked, applyPlanService(t, pl, "web").Action)
}

func TestApplyTailnetReleaseAndSwitchOff(t *testing.T) {
	f := newApplyFixture(t)
	hook := f.tailnetHook()
	web := f.uiService(f.staging, "web", services.SourceImage, services.Config{Image: "nginx:1.27", Port: 8641})
	f.mustApply(applyWebFile(8641, applyTailnetTable))
	require.NotNil(t, f.cfgOf(web.ID).Tailnet)
	require.Contains(t, applyOwnedKeys(f.owned(web.ID)), "config.tailnet")

	// Removing the table releases it: the node stays as it is.
	bare := applyWebFile(8641, "")
	pl := f.plan("staging", bare)
	require.True(t, pl.Valid, applyJSON(pl.Diagnostics))
	require.Empty(t, applyChangeFields(applyPlanService(t, pl, "web")), "an omitted table is not a change")
	require.Equal(t, []string{"services.web.tailnet"}, applyPlanService(t, pl, "web").Released)
	f.mustApply(bare)
	require.NotNil(t, f.cfgOf(web.ID).Tailnet, "removing the table does not disable the node")
	require.NotContains(t, applyOwnedKeys(f.owned(web.ID)), "config.tailnet")

	// enabled = false switches it off.
	off := applyWebFile(8641, "\n[services.web.tailnet]\nenabled = false\n")
	pl = f.plan("staging", off)
	require.True(t, pl.Valid, applyJSON(pl.Diagnostics))
	c := applyChange(t, applyPlanService(t, pl, "web"), "config.tailnet")
	require.Equal(t, "off", c.To)
	f.mustApply(off, applyWithPlanID(pl.PlanID))
	require.Nil(t, f.cfgOf(web.ID).Tailnet)
	last := hook.changes[len(hook.changes)-1]
	require.NotNil(t, last[0])
	require.Nil(t, last[1])
}

func TestApplyCreatesAServiceOnATailnetThroughAnUpdate(t *testing.T) {
	f := newApplyFixture(t)
	hook := f.tailnetHook()
	file := applyWebFile(8641, applyTailnetTable)

	pl := f.plan("staging", file)
	require.True(t, pl.Valid, applyJSON(pl.Diagnostics))
	require.Equal(t, configsync.ActionCreate, applyPlanService(t, pl, "web").Action)
	res := f.mustApply(file, applyWithPlanID(pl.PlanID))
	require.Equal(t, configsync.StatusStaged, res.Status, applyJSON(res.Steps))

	web := f.svcRow(f.staging, "web")
	cfg := f.cfgOf(web.ID)
	require.NotNil(t, cfg.Tailnet)
	require.Equal(t, "web-admin", cfg.Tailnet.Hostname)
	require.Equal(t, 8641, cfg.Port)
	require.Len(t, hook.validated, 1, "a new service is checked against the integration like an edited one")
	require.Len(t, hook.changes, 1)
	require.Nil(t, hook.changes[0][0])
	require.Contains(t, applyOwnedKeys(f.owned(web.ID)), "config.tailnet")
}

// The plan does not read the team's integration: what depends on it fails when the change is
// applied, with the code the API answers, and nothing is written.
func TestApplyTailnetNeedsTheIntegration(t *testing.T) {
	f := newApplyFixture(t)
	hook := f.tailnetHook()
	hook.err = apierr.Unprocessable("tailnet_not_configured", "connect Tailscale or Headscale in Team settings → Integrations first")

	// A new service.
	file := applyWebFile(8641, applyTailnetTable)
	pl := f.plan("staging", file)
	require.True(t, pl.Valid, "the plan cannot know: %s", applyJSON(pl.Diagnostics))
	_, err := f.apply(f.admin, "staging", file)
	ae, ok := apierr.As(err)
	require.True(t, ok, "%v", err)
	require.Equal(t, "tailnet_not_configured", ae.Code)
	require.False(t, f.hasSvc(f.staging, "web"), "the apply is one transaction")

	// An existing service.
	web := f.uiService(f.staging, "web", services.SourceImage, services.Config{Image: "nginx:1.27", Port: 8641})
	before := f.row(web.ID)
	_, err = f.apply(f.admin, "staging", file)
	ae, ok = apierr.As(err)
	require.True(t, ok, "%v", err)
	require.Equal(t, "tailnet_not_configured", ae.Code)
	require.Equal(t, before.Generation, f.row(web.ID).Generation)
	require.Nil(t, f.cfgOf(web.ID).Tailnet)

	hook.err = apierr.Unprocessable("tailnet_tag_not_allowed", "tag tag:admin is not in the integration's allowed tags")
	_, err = f.apply(f.admin, "staging", file)
	ae, ok = apierr.As(err)
	require.True(t, ok, "%v", err)
	require.Equal(t, "tailnet_tag_not_allowed", ae.Code)
}

func TestPlanRejectsATailnetThePlatformRejects(t *testing.T) {
	f := newApplyFixture(t)
	for name, tc := range map[string]struct{ table, code, path string }{
		"web without a port":   {"[services.web.tailnet]\nenabled = true\nweb = true\n", "invalid_value", "services.web.tailnet"},
		"duplicate tcp listen": {"[services.web.tailnet]\nenabled = true\ntcp = [{ listen = 22 }, { listen = 22, target = 2222 }]\n", "invalid_value", "services.web.tailnet"},
		"bad hostname":         {"[services.web.tailnet]\nenabled = true\nweb = true\nhostname = \"not a host\"\n", "invalid_value", "services.web.tailnet.hostname"},
		"funnel key":           {"[services.web.tailnet]\nenabled = true\nweb = true\nfunnel = true\n", "unknown_key", "services.web.tailnet.funnel"},
	} {
		t.Run(name, func(t *testing.T) {
			file := "schema = 1\nserver = \"agent1\"\n\n[services.web]\nimage = \"nginx:1.27\"\nport = 8641\n\n" + tc.table
			if name == "web without a port" {
				file = "schema = 1\nserver = \"agent1\"\n\n[services.web]\nimage = \"nginx:1.27\"\n\n" + tc.table
			}
			pl := f.plan("staging", file)
			require.False(t, pl.Valid, applyJSON(pl.Diagnostics))
			var found bool
			for _, d := range pl.Diagnostics {
				found = found || (d.Code == tc.code && d.Path == tc.path)
			}
			require.True(t, found, "want %s at %s: %s", tc.code, tc.path, applyJSON(pl.Diagnostics))
		})
	}
	// The file of a rejected plan writes nothing.
	require.False(t, f.hasSvc(f.staging, "web"))
}
