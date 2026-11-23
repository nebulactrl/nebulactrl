package configsync

import (
	"context"
	"sort"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/nebulactrl/nebulactrl/internal/db"
	"github.com/nebulactrl/nebulactrl/internal/domains"
	"github.com/nebulactrl/nebulactrl/internal/projectconfig"
	"github.com/nebulactrl/nebulactrl/internal/services"
)

func planSorted(in []string) []string {
	out := append([]string(nil), in...)
	sort.Strings(out)
	return out
}

// planDiagPaths lists the paths of the diagnostics with a code.
func planDiagPaths(ds Diagnostics, code string) []string {
	var out []string
	for _, d := range ds {
		if d.Code == code {
			out = append(out, d.Path)
		}
	}
	return out
}

func TestPlanDB_StackTakesStopGraceAndEgress(t *testing.T) {
	f := newPlanFixture(t)
	f.stack(f.staging, 300)
	file := planHeadServer + "\n[services.shop]\nstop_grace_seconds = 45\nblock_egress = true\n[services.shop.stack]\n" + planComposeKey
	p := f.plan(f.staging, file)
	require.True(t, p.Valid, "%v", p.Diagnostics)
	require.Equal(t, []string{"config.blockEgress", "config.stopGraceSeconds"}, planSorted(planFieldsOf(planSvc(t, p, "shop").Changes)))
}

func TestPlanDB_StackDomainTargetAndComponentAreCreateOnly(t *testing.T) {
	f := newPlanFixture(t)
	shop := f.stack(f.staging, 300)
	_, err := f.q.InsertDomain(context.Background(), db.InsertDomainParams{
		ServiceID: shop.ID, Hostname: "shop.example.com", TargetPort: 80, Component: "web",
		Kind: "serve", RedirectCode: 308, PreservePath: true, TlsMode: "auto",
	})
	require.NoError(t, err)

	const twoComponents = "compose = '''\nservices:\n  web:\n    image: nginx:1.27\n  api:\n    image: nginx:1.27\n'''\n"
	head := planHeadServer + "\n[services.shop.stack]\n" + twoComponents + "\n[services.shop.domains.\"shop.example.com\"]\n"
	for name, c := range map[string]struct {
		rule string
		want []string
	}{
		"same values":          {"component = \"web\"\ntarget_port = 80\n", nil},
		"target port left out": {"component = \"web\"\n", nil},
		"another target port":  {"component = \"web\"\ntarget_port = 8080\n", []string{"services.shop.domains.\"shop.example.com\".target_port"}},
		"another component":    {"component = \"api\"\ntarget_port = 80\n", []string{"services.shop.domains.\"shop.example.com\".component"}},
		"both":                 {"component = \"api\"\ntarget_port = 8080\n", []string{"services.shop.domains.\"shop.example.com\".component", "services.shop.domains.\"shop.example.com\".target_port"}},
	} {
		t.Run(name, func(t *testing.T) {
			p := f.plan(f.staging, head+c.rule)
			require.ElementsMatch(t, c.want, planDiagPaths(p.Diagnostics, "immutable_field"), "%v", p.Diagnostics)
			require.Equal(t, len(c.want) == 0, p.Valid, "%v", p.Diagnostics)
			if len(c.want) > 0 {
				require.Contains(t, planSvc(t, p, "shop").Blockers, "immutable_field")
			}
		})
	}

	t.Run("a new domain may pick both", func(t *testing.T) {
		p := f.plan(f.staging, head+"component = \"web\"\ntarget_port = 80\n\n[services.shop.domains.\"other.example.com\"]\ncomponent = \"web\"\ntarget_port = 8080\n")
		require.True(t, p.Valid, "%v", p.Diagnostics)
	})
}

func TestPlanDB_AppDomainTargetPortMustBeThePortOfItsProcess(t *testing.T) {
	f := newPlanFixture(t)
	web := f.image(f.staging, "web", 8080)
	_, err := f.doms.Create(f.ctx, web.ID, "web.example.com", 0, domains.RuleInput{})
	require.NoError(t, err)

	head := planWebHead("8080") + "\n[services.web.domains.\"web.example.com\"]\n"
	p := f.plan(f.staging, head+"target_port = 8080\n")
	require.True(t, p.Valid, "%v", p.Diagnostics)

	p = f.plan(f.staging, head+"target_port = 9090\n")
	require.False(t, p.Valid)
	d := planDiag(t, p.Diagnostics, "invalid_value")
	require.Equal(t, "error", d.Severity)
	require.Equal(t, "cause: invalid_target_port", d.Hint)
	require.Contains(t, d.Path, "web.example.com")
	require.Contains(t, planSvc(t, p, "web").Blockers, "invalid_value")
}

func TestPlanDB_ScalingNeedsAStatelessServiceWithAnHTTPPort(t *testing.T) {
	f := newPlanFixture(t)
	web := f.image(f.staging, "web", 8080)
	f.image(f.staging, "queue", 0)

	const scale = "\n[services.%s.scale]\nreplicas = 2\n"
	at := func(slug string) string { return strings.ReplaceAll(scale, "%s", slug) }
	const files = "\n[services.web.files.\"/etc/a.conf\"]\ncontent = \"x\"\n"

	for name, c := range map[string]struct {
		file, slug, reason, path string
	}{
		"config files": {
			planWebHead("8080") + files + at("web"),
			"web", "config files", "services.web.scale.replicas",
		},
		"autoscaling with config files": {
			planWebHead("8080") + files + "\n[services.web.scale.autoscaling]\nenabled = true\nmax = 4\n",
			"web", "config files", "services.web.scale.autoscaling.max",
		},
		"no port in the file or live": {
			planHeadServer + "\n[services.queue]\nimage = \"nginx:1.27\"\n" + at("queue"),
			"queue", "without an HTTP port", "services.queue.scale.replicas",
		},
	} {
		t.Run(name, func(t *testing.T) {
			p := f.plan(f.staging, c.file)
			require.False(t, p.Valid, "%v", p.Diagnostics)
			d := planDiag(t, p.Diagnostics, "replicas_require_stateless")
			require.Equal(t, c.path, d.Path)
			require.Equal(t, "error", d.Severity)
			require.NotZero(t, d.Line)
			require.Contains(t, strings.ToLower(d.Message), strings.ToLower(strings.TrimPrefix(c.reason, "without an ")), d.Message)
			if len(p.Services) > 0 { // a static error of the file stops the plan before any service
				require.Contains(t, planSvc(t, p, c.slug).Blockers, "replicas_require_stateless")
			}
		})
	}

	t.Run("live config files, none in the file", func(t *testing.T) {
		docs := f.image(f.staging, "docs", 8080)
		f.editConfig(docs.ID, func(c *services.Config) {
			c.Files = []services.FileMount{{Path: "/etc/a.conf", Mode: "0644", Content: "x"}}
		})
		p := f.plan(f.staging, planHeadServer+"\n[services.docs]\nimage = \"nginx:1.27\"\nport = 8080\n"+at("docs"))
		require.False(t, p.Valid)
		d := planDiag(t, p.Diagnostics, "replicas_require_stateless")
		require.Equal(t, "services.docs.scale.replicas", d.Path)
		require.Contains(t, d.Message, "config files")
		require.Contains(t, planSvc(t, p, "docs").Blockers, "replicas_require_stateless")
	})
	t.Run("an eligible service scales", func(t *testing.T) {
		p := f.plan(f.staging, planWebHead("8080")+at("web"))
		require.True(t, p.Valid, "%v", p.Diagnostics)
	})
	t.Run("the live port counts when the file leaves it out", func(t *testing.T) {
		p := f.plan(f.staging, planHeadServer+"\n[services.web]\nimage = \"nginx:1.27\"\n"+at("web"))
		require.True(t, p.Valid, "%v", p.Diagnostics)
	})
	t.Run("one replica is always fine", func(t *testing.T) {
		p := f.plan(f.staging, planWebHead("8080")+files+"\n[services.web.scale]\nreplicas = 1\n")
		require.True(t, p.Valid, "%v", p.Diagnostics)
	})
	t.Run("a process without a port scales", func(t *testing.T) {
		f.withProcesses(web.ID, planWorker())
		file := planWebHead("8080") + planProcess("worker", planWorkerTable, "\n[services.web.processes.worker.scale]\nreplicas = 2\n")
		p := f.plan(f.staging, file)
		require.True(t, p.Valid, "%v", p.Diagnostics)
	})
}

func TestUnitScaleRequest(t *testing.T) {
	field := func(id string, v any) *fieldPlan { return &fieldPlan{fv: projectconfig.FieldValue{ID: id, Value: v}} }
	for name, c := range map[string]struct {
		f       *fieldPlan
		process string
		wants   bool
	}{
		"replicas 2":                     {field("scale.replicas", 2), "", true},
		"replicas 1":                     {field("scale.replicas", 1), "", false},
		"autoscaling on":                 {field("scale.autoscaling.enabled", true), "", true},
		"autoscaling off":                {field("scale.autoscaling.enabled", false), "", false},
		"autoscaling max is no switch":   {field("scale.autoscaling.max", 4), "", false},
		"process replicas":               {field("processes.worker.scale.replicas", 3), "worker", true},
		"process autoscaling":            {field("processes.mail-worker.scale.autoscaling.enabled", true), "mail-worker", true},
		"process replicas 1":             {field("processes.worker.scale.replicas", 1), "worker", false},
		"not a scale field":              {field("config.port", 8080), "", false},
		"a process key that is no scale": {field("config.processes.worker", nil), "", false},
	} {
		t.Run(name, func(t *testing.T) {
			process, wants := scaleRequest(c.f)
			require.Equal(t, c.wants, wants)
			if wants {
				require.Equal(t, c.process, process)
			}
		})
	}
}

func TestUnitAbsentOnKindForCron(t *testing.T) {
	require.True(t, absentOnKind("config.cron.schedule", services.Config{}))
	require.False(t, absentOnKind("config.cron.schedule", services.Config{Cron: &services.CronSpec{Schedule: "0 3 * * *"}}))
	require.False(t, absentOnKind("config.port", services.Config{}))
}
