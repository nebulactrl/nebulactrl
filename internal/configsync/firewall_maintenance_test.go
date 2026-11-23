package configsync

// Pure tests of the firewall and maintenance tables of nebula.toml: how a file becomes a field,
// how the field compares with the live service, the plan-time checks and the export. The
// database-backed plan and apply tests are in plan_firewall_maintenance_test.go and
// apply_firewall_maintenance_test.go.

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/nebulactrl/nebulactrl/internal/databases/engines"
	"github.com/nebulactrl/nebulactrl/internal/db"
	"github.com/nebulactrl/nebulactrl/internal/projectconfig"
	"github.com/nebulactrl/nebulactrl/internal/services"
	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

const fmHead = "schema = 1\nserver = \"fra-1\"\n\n[services.web]\nimage = \"nginx:1.27\"\nport = 80\n"

// fmRules is a firewall file: an allow rule with an id, then a rule without one.
const fmRules = `
[[services.web.firewall.rules]]
id = "office"
name = "Allow the office"
action = "allow"

[[services.web.firewall.rules.conditions]]
field = "ip"
op = "in"
values = ["203.0.113.0/24"]

[[services.web.firewall.rules]]
name = "Block WordPress probes"
action = "block"

[[services.web.firewall.rules.conditions]]
field = "path"
op = "contains"
values = ["wp-login.php", "/wp-admin"]
`

// fmField is the field a file yields for key id of the service web.
func fmField(t *testing.T, file, id string) projectconfig.FieldValue {
	t.Helper()
	res := unitResolve(t, file)
	fv, ok := res.Services["web"].Fields[id]
	require.True(t, ok, "no %s field in %v", id, res.Services["web"].Fields)
	return fv
}

// fmLiveRules stores rules the way the platform does, in the row's jsonb column.
func fmLiveRules(t *testing.T, rules ...agentprotocol.FirewallRule) *liveService {
	t.Helper()
	raw, err := json.Marshal(agentprotocol.Firewall{Rules: rules})
	require.NoError(t, err)
	return &liveService{Row: db.Service{ID: unitWebID, Slug: "web", Firewall: raw}}
}

var (
	fmOffice = agentprotocol.FirewallRule{ID: "aaa", Name: "Allow the office", Action: agentprotocol.FirewallAllow, Conditions: []agentprotocol.FirewallCondition{
		{Field: agentprotocol.FirewallFieldIP, Op: agentprotocol.FirewallOpIn, Values: []string{"203.0.113.0/24"}},
	}}
	fmWordpress = agentprotocol.FirewallRule{ID: "bbb", Name: "Block WordPress probes", Action: agentprotocol.FirewallBlock, Conditions: []agentprotocol.FirewallCondition{
		{Field: agentprotocol.FirewallFieldPath, Op: agentprotocol.FirewallOpContains, Values: []string{"wp-login.php", "/wp-admin"}},
	}}
)

func TestUnitFirewallFileIsOneImmediateField(t *testing.T) {
	res := unitResolve(t, fmHead+fmRules)
	fields := res.Services["web"].Fields
	fv := fields["firewall"]
	require.Equal(t, "firewall", fv.ID)
	require.Equal(t, AppliesImmediate, fv.Applies)
	require.Equal(t, "services.web.firewall", fv.Path)
	v, ok := fv.Value.(projectconfig.FirewallValue)
	require.True(t, ok)
	require.Len(t, v.Rules, 2)
	require.Equal(t, "office", v.Rules[0].ID)
	require.Empty(t, v.Rules[1].ID, "a rule the file gives no id has none until the planner takes the live one")
	for id := range fields {
		require.False(t, strings.HasPrefix(id, "firewall.") || strings.HasPrefix(id, "maintenance."), "the rule list is one unit, not %s", id)
	}
	require.IsType(t, firewallOps{}, opsFor("firewall"))
	require.Equal(t, "proxy:write", scopeOfField("firewall"))
}

func TestUnitFirewallRulesWithoutIDsKeepTheLiveIDsByPosition(t *testing.T) {
	file := strings.NewReplacer(`id = "office"`+"\n", "").Replace(fmHead + fmRules)
	fv := fmField(t, file, "firewall")
	ls := fmLiveRules(t, fmOffice, fmWordpress)

	live, present := firewallOps{}.live(nil, ls, "firewall", "")
	require.True(t, present)
	want, err := firewallOps{}.desired(nil, ls, fv)
	require.NoError(t, err)
	require.Equal(t, live.FP, want.FP, "a file without ids plans as unchanged")
	require.Equal(t, `allow "Allow the office"; block "Block WordPress probes"`, want.Display)

	// A new rule has no live rule at its position: it stays without an id for the platform to make one.
	d, err := firewallOps{}.desired(nil, fmLiveRules(t, fmOffice), fv)
	require.NoError(t, err)
	require.NotEqual(t, live.FP, d.FP)

	// Another order is a change: the id follows the position, the rest does not.
	swapped := fmLiveRules(t, fmWordpress, fmOffice)
	liveSwapped, _ := firewallOps{}.live(nil, swapped, "firewall", "")
	d, err = firewallOps{}.desired(nil, swapped, fv)
	require.NoError(t, err)
	require.NotEqual(t, liveSwapped.FP, d.FP, "the first live rule is not the allow rule the file lists first")

	// A live id the file spells out for another rule is not reused.
	explicit := fmField(t, fmHead+strings.Replace(fmRules, `id = "office"`, `id = "bbb"`, 1), "firewall")
	rules := resolveFirewallIDs(explicit.Value.(projectconfig.FirewallValue), liveFirewallRules(ls.Row)).Rules
	require.Equal(t, "bbb", rules[0].ID)
	require.Empty(t, rules[1].ID, "bbb is the file's id of rule one, so rule two cannot take it from the live list")
}

func TestUnitFirewallComparesEveryPartOfARule(t *testing.T) {
	ls := fmLiveRules(t, fmOffice, fmWordpress)
	live, _ := firewallOps{}.live(nil, ls, "firewall", "")
	for name, edit := range map[string][2]string{
		"action":    {`action = "allow"`, `action = "block"`},
		"name":      {`name = "Allow the office"`, `name = "Allow the offices"`},
		"id":        {`id = "aaa"`, `id = "other"`},
		"value":     {`"203.0.113.0/24"`, `"203.0.113.0/25"`},
		"op":        {`op = "contains"`, `op = "regex"`},
		"disabled":  {`name = "Block WordPress probes"`, "name = \"Block WordPress probes\"\ndisabled = true"},
		"condition": {`values = ["wp-login.php", "/wp-admin"]`, "values = [\"wp-login.php\", \"/wp-admin\"]\nnot = true"},
		"a value":   {`"/wp-admin"`, `"/wp-admin", "/wp-includes"`},
	} {
		file := strings.Replace(fmHead+strings.Replace(fmRules, `id = "office"`, `id = "aaa"`, 1), edit[0], edit[1], 1)
		fv := fmField(t, file, "firewall")
		d, err := firewallOps{}.desired(nil, ls, fv)
		require.NoError(t, err, name)
		require.NotEqual(t, live.FP, d.FP, name)
	}

	// The platform stores countries and methods upper-case, trimmed and without duplicates; the file value is canonical too.
	canon := fmField(t, fmHead+`
[[services.web.firewall.rules]]
name = " Geo "
action = "block"

[[services.web.firewall.rules.conditions]]
field = "country"
op = "in"
values = [" de ", "DE", "fr"]
`, "firewall").Value.(projectconfig.FirewallValue)
	require.Equal(t, "Geo", canon.Rules[0].Name)
	require.Equal(t, []string{"DE", "FR"}, canon.Rules[0].Conditions[0].Values)
}

func TestUnitFirewallEmptyTableIsNoRulesAndPrunedIsEmptied(t *testing.T) {
	fv := fmField(t, fmHead+"\n[services.web.firewall]\n", "firewall")
	v := fv.Value.(projectconfig.FirewallValue)
	require.Empty(t, v.Rules)

	none := &liveService{Row: db.Service{ID: unitWebID, Slug: "web"}}
	live, present := firewallOps{}.live(nil, none, "firewall", "")
	require.True(t, present, "a service without rules has a live (empty) firewall, so a prune plans a removal only while the table is owned")
	d, err := firewallOps{}.desired(nil, none, fv)
	require.NoError(t, err)
	require.Equal(t, live.FP, d.FP, "no rules is no rules")
	require.Equal(t, "no rules", d.Display)

	_, err = firewallOps{}.desired(nil, none, projectconfig.FieldValue{ID: "firewall", Value: "x"})
	require.Error(t, err)
	require.True(t, prunable("firewall"))
	require.Equal(t, AppliesImmediate, appliesOfRemoval("firewall"))
}

func TestUnitFirewallValueConvertsToTheRulesThePlatformStores(t *testing.T) {
	fv := fmField(t, fmHead+fmRules, "firewall").Value.(projectconfig.FirewallValue)
	require.Equal(t, []agentprotocol.FirewallRule{
		{ID: "office", Name: "Allow the office", Action: "allow", Conditions: []agentprotocol.FirewallCondition{fmOffice.Conditions[0]}},
		{Name: "Block WordPress probes", Action: "block", Conditions: []agentprotocol.FirewallCondition{fmWordpress.Conditions[0]}},
	}, fv.FirewallRules())
	require.Equal(t, fv, projectconfig.FirewallValueOf(fv.FirewallRules()), "the value survives a round trip through the platform's rules")
}

// ---- maintenance ----

func fmMaintenance(on bool, title, message string, eta *time.Time) *liveService {
	return &liveService{Row: db.Service{ID: unitWebID, Slug: "web", MaintenanceEnabled: on, MaintenanceTitle: title, MaintenanceMessage: message, MaintenanceEta: eta}}
}

func TestUnitMaintenanceFileIsOneImmediateField(t *testing.T) {
	fv := fmField(t, fmHead+"\n[services.web.maintenance]\nenabled = true\ntitle = \" Back soon \"\nmessage = \"Upgrading.\"\neta = \"2030-12-01T08:00:00+02:00\"\n", "maintenance")
	require.Equal(t, AppliesImmediate, fv.Applies)
	require.Equal(t, "services.web.maintenance", fv.Path)
	require.Equal(t, projectconfig.MaintenanceValue{Enabled: true, Title: "Back soon", Message: "Upgrading.", ETA: "2030-12-01T06:00:00Z"}, fv.Value)
	require.IsType(t, maintenanceOps{}, opsFor("maintenance"))
	require.Equal(t, "deployments:write", scopeOfField("maintenance"))
	require.True(t, prunable("maintenance"))
	require.Equal(t, AppliesImmediate, appliesOfRemoval("maintenance"))
}

func TestUnitMaintenanceComparesWhatItCanSee(t *testing.T) {
	eta := time.Date(2030, 12, 1, 6, 0, 0, 123, time.UTC)
	on := fmMaintenance(true, "Back soon", "Upgrading.", &eta)
	live, present := maintenanceOps{}.live(nil, on, "maintenance", "")
	require.True(t, present)
	require.Equal(t, `on: "Back soon" until 2030-12-01T06:00:00Z`, live.Display)

	same := func(ls *liveService, file string) bool {
		t.Helper()
		l, _ := maintenanceOps{}.live(nil, ls, "maintenance", "")
		d, err := maintenanceOps{}.desired(nil, ls, fmField(t, fmHead+file, "maintenance"))
		require.NoError(t, err)
		return l.FP == d.FP
	}
	const header = "\n[services.web.maintenance]\n"
	require.True(t, same(on, header+"enabled = true\ntitle = \"Back soon\"\nmessage = \"Upgrading.\"\neta = \"2030-12-01T08:00:00+02:00\"\n"), "the same instant in another zone, sub-second part dropped")
	require.False(t, same(on, header+"enabled = true\ntitle = \"Back soon\"\nmessage = \"Upgrading.\"\n"), "no eta is a change while the live window has one")
	require.False(t, same(on, header+"enabled = true\ntitle = \"Back soon\"\neta = \"2030-12-01T06:00:00Z\"\n"), "no message is a change while the live page has one")
	require.False(t, same(on, header+"enabled = false\n"))

	// Switched off, the stored text is invisible to the file: it neither sets nor compares it.
	off := fmMaintenance(false, "Back soon", "Upgrading.", &eta)
	require.True(t, same(off, header+"enabled = false\n"))
	require.True(t, same(off, header+"enabled = false\ntitle = \"ignored while off\"\n"))
	require.False(t, same(off, header+"enabled = true\n"))
	require.True(t, same(fmMaintenance(true, "", "", nil), header+"enabled = true\n"), "empty text is the default page")
	l, _ := maintenanceOps{}.live(nil, off, "maintenance", "")
	require.Equal(t, "off", l.Display)

	_, err := maintenanceOps{}.desired(nil, off, projectconfig.FieldValue{ID: "maintenance", Value: 1})
	require.Error(t, err)
}

// ---- plan-time checks ----

func fmFirewallPlan(t *testing.T, file string, changed bool) (*planned, *svcPlan, *fieldPlan) {
	t.Helper()
	res := unitResolve(t, file)
	rs := res.Services["web"]
	p := unitPlanned(false)
	p.res = res
	sp := &svcPlan{slug: "web", rs: rs}
	var f *fieldPlan
	for _, id := range []string{"firewall", "maintenance"} {
		if fv, ok := rs.Fields[id]; ok {
			f = &fieldPlan{fv: fv, changed: changed}
		}
	}
	require.NotNil(t, f)
	return p, sp, f
}

func fmServer(caps []string, geo bool) *db.Server {
	facts, err := json.Marshal(agentprotocol.Facts{OS: "linux", Capabilities: caps})
	if err != nil {
		panic(err)
	}
	return &db.Server{Name: "fra-1", Facts: facts, GeoEnabled: geo, TrafficAnalyticsEnabled: geo}
}

func fmCodes(p *planned) []string {
	var out []string
	for _, d := range p.diags {
		out = append(out, d.Code)
	}
	return out
}

func TestUnitCheckFirewallPlacesTheRulesOnTheServer(t *testing.T) {
	country := fmHead + `
[[services.web.firewall.rules]]
name = "Block countries"
action = "block"

[[services.web.firewall.rules.conditions]]
field = "country"
op = "in"
values = ["CN"]
`
	cfg := services.Config{}
	for name, c := range map[string]struct {
		file    string
		server  *db.Server
		changed bool
		cfg     services.Config
		want    []string
	}{
		"fine":                {fmHead + fmRules, fmServer([]string{agentprotocol.CapFirewall}, false), true, cfg, nil},
		"agent too old":       {fmHead + fmRules, fmServer(nil, false), true, cfg, []string{"agent_too_old"}},
		"no geolocation":      {country, fmServer([]string{agentprotocol.CapFirewall}, false), true, cfg, []string{"geo_required"}},
		"geolocation":         {country, fmServer([]string{agentprotocol.CapFirewall}, true), true, cfg, nil},
		"unchanged":           {country, fmServer(nil, false), false, cfg, nil},
		"unknown server":      {country, nil, true, cfg, nil},
		"no rules":            {fmHead + "\n[services.web.firewall]\n", fmServer(nil, false), true, cfg, nil},
		"a tcp route":         {fmHead + fmRules, nil, true, services.Config{Route: &services.RouteConfig{Protocol: agentprotocol.ProtoTCP}}, []string{"firewall_unsupported"}},
		"an http route":       {fmHead + fmRules, nil, true, services.Config{Route: &services.RouteConfig{Protocol: agentprotocol.ProtoHTTP}}, nil},
		"a tcp route, no-op":  {fmHead + fmRules, nil, false, services.Config{Route: &services.RouteConfig{Protocol: agentprotocol.ProtoTCP}}, nil},
		"tcp beats placement": {fmHead + fmRules, fmServer(nil, false), true, services.Config{Route: &services.RouteConfig{Protocol: agentprotocol.ProtoUDP}}, []string{"firewall_unsupported"}},
	} {
		t.Run(name, func(t *testing.T) {
			p, sp, f := fmFirewallPlan(t, c.file, c.changed)
			sp.server = c.server
			p.checkFirewall(sp, f, c.cfg)
			require.Equal(t, c.want, fmCodes(p), "%v", p.diags)
			for _, d := range p.diags {
				require.Equal(t, "services.web.firewall", d.Path)
				require.Equal(t, projectconfig.SeverityError, d.Severity)
			}
		})
	}
}

func TestUnitCheckMaintenanceRefusesWhatTheServiceWould(t *testing.T) {
	future := time.Now().Add(48 * time.Hour).UTC().Truncate(time.Second).Format(time.RFC3339)
	past := time.Now().Add(-48 * time.Hour).UTC().Truncate(time.Second).Format(time.RFC3339)
	on := func(eta string) string {
		s := fmHead + "\n[services.web.maintenance]\nenabled = true\n"
		if eta != "" {
			s += "eta = \"" + eta + "\"\n"
		}
		return s
	}
	tcp := services.Config{Route: &services.RouteConfig{Protocol: agentprotocol.ProtoTCP}}
	for name, c := range map[string]struct {
		file    string
		changed bool
		cfg     services.Config
		want    []string
		path    string
	}{
		"no eta":            {on(""), true, services.Config{}, nil, ""},
		"a future eta":      {on(future), true, services.Config{}, nil, ""},
		"a past eta":        {on(past), true, services.Config{}, []string{"invalid_value"}, "services.web.maintenance.eta"},
		"a past eta, no-op": {on(past), false, services.Config{}, nil, ""},
		"off":               {fmHead + "\n[services.web.maintenance]\nenabled = false\neta = \"" + past + "\"\n", true, services.Config{}, nil, ""},
		"a tcp route":       {on(""), true, tcp, []string{"maintenance_unsupported"}, "services.web.maintenance"},
		"an http route":     {on(""), true, services.Config{Route: &services.RouteConfig{Protocol: agentprotocol.ProtoHTTP}}, nil, ""},
	} {
		t.Run(name, func(t *testing.T) {
			p, _, f := fmFirewallPlan(t, c.file, c.changed)
			p.checkMaintenance(f, c.cfg)
			require.Equal(t, c.want, fmCodes(p), "%v", p.diags)
			for _, d := range p.diags {
				require.Equal(t, c.path, d.Path)
			}
		})
	}
}

// ---- export ----

func fmExportWeb(t *testing.T, edit func(ls *liveService)) (string, *projectconfig.Service) {
	t.Helper()
	web := exImageSvc("web", services.Config{Image: "nginx:1.27", Port: 80})
	web.Row.ServerID = new(exID(1))
	edit(web)
	text, _ := exExport(t, exLiveEnv(exTestRefs(), web), false)
	_, res := exParseResolve(t, text, "")
	return text, res.Services["web"].Def
}

func TestExportFirewallWritesTheLiveRulesWithTheirIDs(t *testing.T) {
	text, def := fmExportWeb(t, func(ls *liveService) {
		raw, err := json.Marshal(agentprotocol.Firewall{Rules: []agentprotocol.FirewallRule{fmOffice, fmWordpress}})
		require.NoError(t, err)
		ls.Row.Firewall = raw
	})
	require.NotNil(t, def.Firewall)
	require.Len(t, def.Firewall.Rules, 2)
	r := def.Firewall.Rules[0]
	require.Equal(t, "aaa", *r.ID)
	require.Equal(t, "allow", *r.Action)
	require.Nil(t, r.Disabled, "defaults are omitted")
	require.Equal(t, []string{"203.0.113.0/24"}, r.Conditions[0].Values)
	require.Contains(t, text, "[[services.web.firewall.rules]]")
	require.Contains(t, text, "[[services.web.firewall.rules.conditions]]")

	// The export is the file: it plans to no difference against the live rules.
	fv := fmField(t, text, "firewall")
	ls := &liveService{Row: db.Service{Firewall: mustJSON(t, agentprotocol.Firewall{Rules: []agentprotocol.FirewallRule{fmOffice, fmWordpress}})}}
	live, _ := firewallOps{}.live(nil, ls, "firewall", "")
	d, err := firewallOps{}.desired(nil, ls, fv)
	require.NoError(t, err)
	require.Equal(t, live.FP, d.FP)
}

func mustJSON(t *testing.T, v any) json.RawMessage {
	t.Helper()
	raw, err := json.Marshal(v)
	require.NoError(t, err)
	return raw
}

func TestExportFirewallIsAnEmptyTableOnlyWhereTheFileOwnsIt(t *testing.T) {
	text, def := fmExportWeb(t, func(*liveService) {})
	require.Nil(t, def.Firewall)
	require.NotContains(t, text, "firewall")

	text, def = fmExportWeb(t, func(ls *liveService) { ls.Owned["firewall"] = "x" })
	require.NotNil(t, def.Firewall, "the file owns the firewall: it keeps saying there are no rules\n%s", text)
	require.Empty(t, def.Firewall.Rules)
}

func TestExportMaintenanceIsWrittenWhileItIsOn(t *testing.T) {
	eta := time.Date(2030, 12, 1, 6, 0, 0, 0, time.UTC)
	text, def := fmExportWeb(t, func(ls *liveService) {
		ls.Row.MaintenanceEnabled, ls.Row.MaintenanceTitle, ls.Row.MaintenanceMessage, ls.Row.MaintenanceEta = true, "Back soon", "Upgrading.\nSoon.", &eta
	})
	m := def.Maintenance
	require.NotNil(t, m, text)
	require.True(t, *m.Enabled)
	require.Equal(t, "Back soon", *m.Title)
	require.Equal(t, "Upgrading.\nSoon.", *m.Message)
	require.Equal(t, "2030-12-01T06:00:00Z", *m.ETA)

	// Off: no table, so an export never writes an outage that is not live; the stored text is not exported.
	text, def = fmExportWeb(t, func(ls *liveService) { ls.Row.MaintenanceTitle, ls.Row.MaintenanceEta = "old", &eta })
	require.Nil(t, def.Maintenance)
	require.NotContains(t, text, "maintenance")

	// Off but owned: the file keeps saying so, without the stored text.
	text, def = fmExportWeb(t, func(ls *liveService) {
		ls.Row.MaintenanceTitle, ls.Row.MaintenanceEta = "old", &eta
		ls.Owned["maintenance"] = "x"
	})
	require.NotNil(t, def.Maintenance, text)
	require.False(t, *def.Maintenance.Enabled)
	require.Nil(t, def.Maintenance.Title)
	require.Nil(t, def.Maintenance.ETA)
}

func TestExportFirewallAndMaintenanceOnlyForServicesThatServeHTTP(t *testing.T) {
	fw := mustJSON(t, agentprotocol.Firewall{Rules: []agentprotocol.FirewallRule{fmOffice}})
	eta := time.Date(2030, 12, 1, 6, 0, 0, 0, time.UTC)
	arm := func(ls *liveService) *liveService {
		ls.Row.ServerID = new(exID(1))
		ls.Row.Firewall = fw
		ls.Row.MaintenanceEnabled, ls.Row.MaintenanceEta = true, &eta
		return ls
	}
	database := arm(exSvc("pg", services.KindDatabase, services.SourceImage, services.Config{Database: &engines.Spec{Engine: "postgres"}}))
	stack := arm(exSvc("blog", services.KindStack, services.SourceCompose, services.Config{Stack: &services.StackConfig{Compose: exCompose}}))
	tcp := arm(exSvc("raw", services.KindRoute, services.SourceExternal, services.Config{Route: &services.RouteConfig{
		Protocol: "tcp", ListenPort: 2222, Upstreams: []services.RouteUpstream{{Address: "10.0.0.5:22"}}, OpenToInternet: true,
	}}))
	job := arm(exImageSvc("job", services.Config{Image: "nginx:1.27", OneShot: true}))
	http := arm(exSvc("print", services.KindRoute, services.SourceExternal, services.Config{Route: &services.RouteConfig{
		Protocol: "http", Upstreams: []services.RouteUpstream{{Address: "10.0.0.5:631"}},
	}}))

	text, _ := exExport(t, exLiveEnv(exTestRefs(), database, stack, tcp, job, http), true)
	_, res := exParseResolve(t, text, "")
	for _, slug := range []string{"pg", "blog", "raw", "job"} {
		d := res.Services[slug].Def
		require.Nil(t, d.Firewall, slug)
		require.Nil(t, d.Maintenance, slug)
	}
	require.NotNil(t, res.Services["print"].Def.Firewall)
	require.NotNil(t, res.Services["print"].Def.Maintenance)
}
