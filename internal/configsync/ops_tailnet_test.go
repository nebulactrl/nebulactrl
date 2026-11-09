package configsync

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/nebulactrl/nebulactrl/internal/databases/engines"
	"github.com/nebulactrl/nebulactrl/internal/projectconfig"
	"github.com/nebulactrl/nebulactrl/internal/services"
	"github.com/nebulactrl/nebulactrl/internal/tailnet/tailnetcfg"
)

func tnLive(t *tailnetcfg.TailnetConfig) *liveService {
	return &liveService{Cfg: services.Config{Image: "nginx:1", Port: 8641, Tailnet: t}.WithDefaults()}
}

// A file never sets Funnel: a service with Funnel on is in sync with a file that says nothing
// about it, and whatever the file changes, the live Funnel flag stays.
func TestUnitTailnetFileNeverChangesFunnel(t *testing.T) {
	pc := unitPlanCtx()
	live := tnLive(&tailnetcfg.TailnetConfig{Enabled: true, Hostname: "admin", Web: true, Funnel: true, TCP: []tailnetcfg.TCPForward{{Listen: 5432, Target: 5432}}})
	same := projectconfig.TailnetValue{
		Enabled: true, Hostname: "admin", Web: true, ExtraTags: []string{},
		TCP: []projectconfig.TailnetPortValue{{Listen: 5432, Target: 5432}},
	}

	dv, err := configOps{}.desired(pc, live, projectconfig.FieldValue{ID: "config.tailnet", Value: same})
	require.NoError(t, err)
	lv, present := configOps{}.live(pc, live, "config.tailnet", "")
	require.True(t, present)
	require.Equal(t, lv.FP, dv.FP, "Funnel is not compared")

	changed := same
	changed.Hostname = "admin-2"
	changed.HidePublic = true
	cfg := live.Cfg
	require.NoError(t, tailnetField.set(pc, &cfg, changed))
	require.NotNil(t, cfg.Tailnet)
	require.True(t, cfg.Tailnet.Funnel, "the live Funnel survives a file apply")
	require.Equal(t, "admin-2", cfg.Tailnet.Hostname)
	require.True(t, cfg.Tailnet.HidePublic)
	require.Equal(t, 8641, cfg.Port)
	require.True(t, live.Cfg.Tailnet.Funnel && live.Cfg.Tailnet.Hostname == "admin", "the live config is not mutated")

	// Funnel publishes the web exposure: a file that drops web while Funnel is on is refused,
	// not silently turned into a config the platform rejects or a Funnel that is switched off.
	noWeb := same
	noWeb.Web = false
	cfg = live.Cfg
	err = tailnetField.set(pc, &cfg, noWeb)
	var re *refError
	require.True(t, errors.As(err, &re), "%v", err)
	require.Equal(t, "invalid_value", re.Code)
	require.Contains(t, re.Msg, "Funnel")
	require.True(t, cfg.Tailnet.Funnel && cfg.Tailnet.Web, "a refused value changes nothing")

	// Switching the node off takes Funnel with it.
	cfg = live.Cfg
	require.NoError(t, tailnetField.set(pc, &cfg, projectconfig.TailnetValue{}))
	require.Nil(t, cfg.Tailnet)
}

func TestUnitTailnetOffComparesAsOff(t *testing.T) {
	pc := unitPlanCtx()
	off := projectconfig.TailnetValue{ExtraTags: []string{}, TCP: []projectconfig.TailnetPortValue{}}

	dv, err := configOps{}.desired(pc, tnLive(nil), projectconfig.FieldValue{ID: "config.tailnet", Value: off})
	require.NoError(t, err)
	lv, _ := configOps{}.live(pc, tnLive(nil), "config.tailnet", "")
	require.Equal(t, lv.FP, dv.FP, "enabled = false against a service that is not on a tailnet plans to zero")
	require.Equal(t, "off", dv.Display)

	on := projectconfig.TailnetValue{Enabled: true, Web: true, ExtraTags: []string{}, TCP: []projectconfig.TailnetPortValue{}}
	dv, err = configOps{}.desired(pc, tnLive(nil), projectconfig.FieldValue{ID: "config.tailnet", Value: on})
	require.NoError(t, err)
	require.NotEqual(t, lv.FP, dv.FP)
}

func TestUnitTailnetDisplay(t *testing.T) {
	require.Equal(t, "off", tailnetDisplay(projectconfig.TailnetValue{}))
	require.Equal(t, "on, web", tailnetDisplay(projectconfig.TailnetValue{Enabled: true, Web: true}))
	require.Equal(t, "on, hostname admin, web, tcp 2222→22, tcp 8443→8641 tls, tcp 5432, tags tag:a,tag:b, tailnet only", tailnetDisplay(projectconfig.TailnetValue{
		Enabled: true, Hostname: "admin", Web: true, ExtraTags: []string{"tag:a", "tag:b"}, HidePublic: true,
		TCP: []projectconfig.TailnetPortValue{{Listen: 2222, Target: 22}, {Listen: 8443, Target: 8641, TLS: true}, {Listen: 5432, Target: 5432}},
	}))
}

// The export writes the [tailnet] table without Funnel, and what it writes plans to zero
// against the service it came from.
func TestExportTailnetRoundTrip(t *testing.T) {
	cfg := services.Config{Image: "nginx:1", Port: 8641, Tailnet: &tailnetcfg.TailnetConfig{
		Enabled: true, Hostname: "openheroes-admin", ExtraTags: []string{"tag:admin"}, Web: true, Funnel: true, HidePublic: true,
		TCP: []tailnetcfg.TCPForward{{Listen: 8443, Target: 8641, TLS: true}, {Listen: 5432}},
	}}
	ls := exImageSvc("web", cfg)
	live := exLiveEnv(exTestRefs(), ls)

	text, v := exExport(t, live, false)
	require.Contains(t, text, "[services.web.tailnet]\n")
	require.Contains(t, text, "enabled = true")
	require.Contains(t, text, `hostname = "openheroes-admin"`)
	require.Contains(t, text, `extra_tags = ["tag:admin"]`)
	require.Contains(t, text, "web = true")
	require.Contains(t, text, "{ listen = 8443, target = 8641, tls = true }")
	require.Contains(t, text, "{ listen = 5432 }")
	require.Contains(t, text, "hide_public = true")
	require.NotContains(t, text, "funnel =", "Funnel is never written")
	require.Equal(t, []string{"tailnet.funnel (set it in the dashboard)"}, v.NotRepresentable["web"])

	_, res := exParseResolve(t, text, "")
	rs := res.Services["web"]
	require.NotNil(t, rs, text)
	fv, ok := rs.Fields["config.tailnet"]
	require.True(t, ok, text)
	require.Equal(t, "services.web.tailnet", fv.Path)

	pc := &planCtx{live: live, res: res}
	dv, err := configOps{}.desired(pc, ls, fv)
	require.NoError(t, err)
	lv, present := configOps{}.live(pc, ls, "config.tailnet", "")
	require.True(t, present)
	require.Equal(t, lv.FP, dv.FP, "a fresh export plans to no change:\n%s", text)

}

func TestExportTailnetWritesDefaultsOnlyWhenAsked(t *testing.T) {
	ls := exImageSvc("db", services.Config{Image: "postgres:17", Tailnet: &tailnetcfg.TailnetConfig{Enabled: true, TCP: []tailnetcfg.TCPForward{{Listen: 5432}}}})
	live := exLiveEnv(exTestRefs(), ls)
	text, _ := exExport(t, live, false)
	require.NotContains(t, text, "web =")
	require.NotContains(t, text, "hide_public")
	text, _ = exExport(t, live, true)
	require.Contains(t, text, "web = false")
	require.Contains(t, text, "hide_public = false")
}

func TestExportTailnetOffIsWrittenOnlyWhileTheFileOwnsIt(t *testing.T) {
	ls := exImageSvc("web", services.Config{Image: "nginx:1", Port: 80})
	text, _ := exExport(t, exLiveEnv(exTestRefs(), ls), false)
	require.NotContains(t, text, "tailnet", "a service that is not on a tailnet and was never described by the file has no table")

	ls.Owned["config.tailnet"] = "sha256:was-on"
	text, _ = exExport(t, exLiveEnv(exTestRefs(), ls), false)
	require.Contains(t, text, "[services.web.tailnet]\nenabled = false\n", "the file that switched it off keeps owning it")
	_, res := exParseResolve(t, text, "")
	require.Equal(t, projectconfig.TailnetValue{ExtraTags: []string{}, TCP: []projectconfig.TailnetPortValue{}}, res.Services["web"].Fields["config.tailnet"].Value)
}

func TestExportTailnetOfADatabase(t *testing.T) {
	cfg := services.Config{Database: &engines.Spec{Engine: "postgres"}, Tailnet: &tailnetcfg.TailnetConfig{Enabled: true, TCP: []tailnetcfg.TCPForward{{Listen: 5432}}}}
	ls := exSvc("db", services.KindDatabase, services.SourceImage, cfg)
	text, _ := exExport(t, exLiveEnv(exTestRefs(), ls), false)
	require.Contains(t, text, "[services.db.tailnet]\nenabled = true\ntcp = [{ listen = 5432 }]\n")
}
