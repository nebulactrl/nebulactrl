package projectconfig

import (
	"fmt"
	"slices"
	"sort"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

var t2allKinds = []string{KindImage, KindGit, KindDatabase, KindRoute, KindStack}

func TestCatalogSanity(t *testing.T) {
	cat := Catalog()
	require.NotEmpty(t, cat)
	seen := map[string]bool{}
	for _, f := range cat {
		require.NotEmpty(t, f.ID)
		require.False(t, seen[f.ID], "duplicate field id %q", f.ID)
		seen[f.ID] = true
		require.NotEmpty(t, f.Key, f.ID)
		require.NotEmpty(t, f.GoField, f.ID)
		require.NotEmpty(t, f.ValueType, f.ID)
		require.NotEmpty(t, f.Doc, f.ID)
		require.Contains(t, []string{AppliesStaged, AppliesImmediate}, f.Applies, f.ID)
		require.Equal(t, strings.Contains(f.ID, "*"), f.Collection, "%s: Collection marks the ids of collection members", f.ID)
		require.LessOrEqual(t, strings.Count(f.ID, "*"), 1, f.ID)
		for _, k := range f.Kinds {
			require.Contains(t, t2allKinds, k, "%s lists an unknown kind", f.ID)
		}
	}
	// every field id owns its own file key
	byKey := map[string]string{}
	for _, f := range cat {
		if prev, dup := byKey[f.Key]; dup {
			t.Errorf("file key %q is owned by both %q and %q", f.Key, prev, f.ID)
		}
		byKey[f.Key] = f.ID
	}

	t.Run("the immediate fields are the documented ones", func(t *testing.T) {
		var got []string
		for _, f := range cat {
			if f.Applies == AppliesImmediate {
				got = append(got, f.ID)
			}
		}
		sort.Strings(got)
		want := []string{
			"backups.*", "domains.*.cloudflare", "git.auto_deploy", "git.link", "git.watch_paths", "name",
			"processes.*.scale.autoscaling.enabled", "processes.*.scale.autoscaling.max", "processes.*.scale.autoscaling.min",
			"processes.*.scale.autoscaling.targetCpuPercent", "processes.*.scale.replicas",
			"scale.autoscaling.enabled", "scale.autoscaling.max", "scale.autoscaling.min", "scale.autoscaling.targetCpuPercent",
			"scale.replicas", "tasks.*", "uptime.alerts", "uptime.enabled", "uptime.failureThreshold",
			"uptime.recoveryThreshold", "uptime.reminderMinutes",
		}
		require.Equal(t, want, got)
	})
	t.Run("the catalog is a copy", func(t *testing.T) {
		a := Catalog()
		a[0].ID = "mutated"
		a[0].Kinds = []string{"mutated"}
		require.NotEqual(t, "mutated", Catalog()[0].ID)
	})
	t.Run("units follow the key name", func(t *testing.T) {
		for _, f := range cat {
			switch {
			case strings.HasSuffix(f.ID, "Seconds") || strings.HasSuffix(f.ID, "seconds"):
				require.Equal(t, "seconds", f.Unit, f.ID)
			case strings.HasSuffix(f.ID, "Mb"):
				require.Equal(t, "mb", f.Unit, f.ID)
			case strings.HasSuffix(f.ID, "Minutes"):
				require.Equal(t, "minutes", f.Unit, f.ID)
			case strings.HasSuffix(f.ID, "Percent"):
				require.Equal(t, "percent", f.Unit, f.ID)
			}
		}
	})
}

func TestLookupField(t *testing.T) {
	samples := []string{"web", "NODE_ENV", "shop.acme.com", "shop.acme.com/api", "a.b", "x"}
	for _, f := range Catalog() {
		for _, sample := range samples {
			id := strings.Replace(f.ID, "*", sample, 1)
			if !strings.Contains(f.ID, "*") && sample != samples[0] {
				continue
			}
			got, ok := LookupField(id)
			require.True(t, ok, "LookupField(%q)", id)
			require.Equal(t, f.ID, got.ID, "LookupField(%q) must resolve to the most specific pattern", id)
		}
	}

	longest := []struct{ id, want string }{
		{"domains.a.b/x.cloudflare", "domains.*.cloudflare"},
		{"domains.shop.acme.com.cloudflare", "domains.*.cloudflare"},
		{"domains.shop.acme.com", "domains.*"},
		{"domains.shop.acme.com/api", "domains.*"},
		{"variables.K#secret", "variables.*#secret"},
		{"variables.K", "variables.*"},
		{"variables.cloudflare", "variables.*"},
		{"config.stack.resources.web", "config.stack.resources.*"},
		{"config.stack.compose", "config.stack.compose"},
		{"config.docker.capAdd", "config.docker.capAdd"},
		{"links.db", "links.*"},
		{"networks.backplane", "networks.*"},
		{"volumes.uploads", "volumes.*"},
		{"tasks.digest", "tasks.*"},
		{"backups.nightly", "backups.*"},
		{"config.processes.worker", "config.processes.*"},
		{"config.processes.a.b", "config.processes.*"},
		{"processes.worker.scale.replicas", "processes.*.scale.replicas"},
		{"processes.scale.scale.replicas", "processes.*.scale.replicas"},
		{"processes.worker.scale.autoscaling.targetCpuPercent", "processes.*.scale.autoscaling.targetCpuPercent"},
		{"scale.replicas", "scale.replicas"},
		{"config.processName", "config.processName"},
		{"config.singleton", "config.singleton"},
	}
	for _, tc := range longest {
		got, ok := LookupField(tc.id)
		require.True(t, ok, tc.id)
		require.Equal(t, tc.want, got.ID, tc.id)
	}

	for _, id := range []string{"", "nope", "config.nothing", "variables.", "tasks.", "config.docker", "Config.port"} {
		_, ok := LookupField(id)
		require.False(t, ok, "LookupField(%q) must not resolve", id)
	}
}

func TestDockerCatalogEntries(t *testing.T) {
	var catalogDocker []string
	for _, f := range Catalog() {
		if rest, ok := strings.CutPrefix(f.ID, "config.docker."); ok {
			catalogDocker = append(catalogDocker, rest)
			require.Equal(t, AppliesStaged, f.Applies, f.ID)
			require.Equal(t, []string{KindImage, KindGit, KindDatabase}, f.Kinds, f.ID)
		}
	}
	var fromTable []string
	for _, df := range dockerFields {
		fromTable = append(fromTable, df.json)
		spec, ok := LookupField("config.docker." + df.json)
		require.True(t, ok, df.json)
		require.Equal(t, "docker."+df.key, spec.Key, df.json)
		require.Equal(t, df.typ, spec.ValueType, df.json)
	}
	sort.Strings(catalogDocker)
	sort.Strings(fromTable)
	require.Equal(t, fromTable, catalogDocker, "the catalog lists exactly the docker options of the field table")
	require.Len(t, dockerFields, 26)

	t.Run("DockerOptionValue returns the documented Go type", func(t *testing.T) {
		for _, d := range []*Docker{nil, {}} {
			for _, df := range dockerFields {
				v, ok := DockerOptionValue(d, df.json)
				require.True(t, ok, df.json)
				require.Equal(t, df.typ, fmt.Sprintf("%T", v), df.json)
			}
		}
	})
	t.Run("unknown option", func(t *testing.T) {
		v, ok := DockerOptionValue(&Docker{}, "nope")
		require.False(t, ok)
		require.Nil(t, v)
	})
	t.Run("capabilities are normalised", func(t *testing.T) {
		v, _ := DockerOptionValue(&Docker{CapAdd: []string{"cap_net_bind_service", "ipc_lock", "IPC_LOCK"}, CapDrop: []string{"all", "CAP_NET_RAW"}}, "capAdd")
		require.Equal(t, []string{"IPC_LOCK", "NET_BIND_SERVICE"}, v)
		v, _ = DockerOptionValue(&Docker{CapDrop: []string{"all", "CAP_NET_RAW"}}, "capDrop")
		require.Equal(t, []string{"ALL", "NET_RAW"}, v)
	})
	t.Run("lists and maps are sorted", func(t *testing.T) {
		d := &Docker{
			Ulimits: map[string]*Ulimit{"nofile": {Soft: new(int64(1)), Hard: new(int64(2))}, "memlock": {Soft: new(int64(-1)), Hard: new(int64(-1))}},
			Tmpfs:   map[string]*Tmpfs{"/tmp": {SizeMB: new(8)}, "/run": {Mode: new("1777")}},
		}
		v, _ := DockerOptionValue(d, "ulimits")
		require.Equal(t, []agentprotocol.Ulimit{{Name: "memlock", Soft: -1, Hard: -1}, {Name: "nofile", Soft: 1, Hard: 2}}, v)
		v, _ = DockerOptionValue(d, "tmpfs")
		require.Equal(t, []agentprotocol.TmpfsMount{{Path: "/run", Mode: "1777"}, {Path: "/tmp", SizeMB: 8}}, v)
	})
	t.Run("the unrestricted options", func(t *testing.T) {
		for _, df := range dockerFields {
			want := slices.Contains([]string{"devices", "gpus", "runtime", "privileged", "pidHost", "seccompUnconfined", "apparmorUnconfined"}, df.json)
			require.Equal(t, want, DockerFieldUnrestricted(df.json), df.json)
		}
		require.False(t, DockerFieldUnrestricted("nope"))
	})
}

// the services of t2KindsDoc, one per kind plus extras that reach every key.
const t2KindsDoc = `schema = 1
server = "srv"

# ---- image: every key an image service owns ----
[services.img]
name = "Image service"
server = "srv-2"
generate_domain = true
depends_on = ["dbs"]
networks = ["backplane"]
image = "ghcr.io/acme/img:1.2"
port = 8080
command = ["run", "--fast"]
one_shot = false
stop_grace_seconds = 45
memory_mb = 512
cpus = 1.5
image_retention = 7
block_egress = false

[services.img.health_check]
type = "http"
path = "/up"
port = 8080
interval_seconds = 3
timeout_seconds = 4
start_period_seconds = 5
deadline_seconds = 60
success_threshold = 3

[services.img.pre_deploy]
command = ["migrate"]
timeout_seconds = 120

[services.img.post_deploy]
command = ["notify"]
timeout_seconds = 60

[services.img.docker]
cap_add = ["cap_net_bind_service", "IPC_LOCK"]
cap_drop = ["net_raw"]
no_new_privileges = true
read_only_rootfs = true
pids_limit = 512
hostname = "img"
dns = ["1.1.1.1"]
user = "1000:1000"
group_add = ["44"]
shm_size_mb = 256
extra_hosts = ["db.local:10.0.0.5"]
init = true
stop_signal = "SIGQUIT"
entrypoint = ["/entrypoint.sh"]
runtime = "runsc"
privileged = true
pid_host = true
seccomp_unconfined = true
apparmor_unconfined = true

[services.img.docker.sysctls]
"net.core.somaxconn" = "1024"

[services.img.docker.host_sysctls]
"vm.max_map_count" = 262144

[services.img.docker.ulimits.nofile]
soft = 65536
hard = 65536

[services.img.docker.labels]
team = "shop"

[services.img.docker.tmpfs."/tmp"]
size_mb = 64
mode = "1777"

[[services.img.docker.devices]]
host_path = "/dev/dri/renderD128"
permissions = "rw"

[services.img.docker.gpus]
count = 1
driver = "nvidia"
capabilities = ["utility", "compute"]

[services.img.variables]
PLAIN = "p"
FANCY = { value = "f", comment = "documented", rotate_every_days = 30, public = true }
REF = "${{dbs.DATABASE_URL}}"
GEN = "${{secret(32)}}"

[services.img.secrets]
keys = ["API_TOKEN", "OTHER_SECRET"]
scope = "runtime"

[services.img.domains."shop.acme.com"]
target_port = 8080
strip_prefix = false
add_prefix = "/v1"
kind = "serve"
preserve_path = true
tls = "auto"
dns_resolver = "cloudflare"
www_redirect = true

[services.img.domains."shop.acme.com".cloudflare]
proxied = true
origin_lock = false

[services.img.domains."shop.acme.com".options]
force_https = true
compress = true
noindex = false
security_headers = "strict"
basic_auth = true
large_uploads = false

[services.img.domains."shop.acme.com".options.request_headers]
X-B = "2"
X-A = "1"

[services.img.domains."shop.acme.com".options.cors]
origins = ["https://app.acme.com"]
methods = ["GET"]
max_age_seconds = 600

[[services.img.domains."shop.acme.com".options.redirects]]
regex = "^/old/(.*)"
replacement = "/new/${1}"
permanent = true

[services.img.domains."shop.acme.com".options.rate_limit]
average = 100
period_seconds = 1
burst = 200

[services.img.domains."shop.acme.com".options.access]
allow_cidrs = ["203.0.113.0/24"]
reject_status = 403

[services.img.domains."shop.acme.com/api"]
kind = "redirect"
redirect_to = "www.acme.com"
redirect_code = 301

[services.img.domains."plain.acme.com"]

[services.img.volumes]
data = "/data"

[services.img.files."/data/app.conf"]
mode = "0600"
content = "listen = 8080\n"

[services.img.files."/data/b.conf"]
content = "b"

[services.img.scale]
replicas = 1

[services.img.scale.autoscaling]
enabled = false
min = 1
max = 3
target_cpu_percent = 70

[services.img.uptime]
enabled = true
alerts = false
failure_threshold = 4
recovery_threshold = 3
reminder_minutes = 30

[services.img.tasks.digest]
schedule = "0 7 * * 1-5"
timezone = "Europe/Berlin"
command = ["bun", "run", "digest"]
enabled = false
timeout_seconds = 900
concurrency = "allow"

[services.img.tasks.minimal]
schedule = "@daily"
command = ["run"]

# ---- image with an exec health check ----
[services.ex]
image = "busybox:1"

[services.ex.health_check]
type = "exec"
command = ["CMD", "true"]

# ---- git, connected ----
[services.gt]
name = "Git service"
port = 3000
command = ["bun", "start"]
process_name = "web"
singleton = false
depends_on = ["img"]

[services.gt.git]
connection = "gh"
repo = "acme/gt"
branch = "main"
submodules = "on"
auto_deploy = true
watch_paths = ["apps/gt/**"]

[services.gt.build]
type = "dockerfile"
dockerfile = "apps/gt/Dockerfile"
context = "."
location = "build_server"
server = "builder-1"
registry = "ghcr"

[services.gt.variables]
BUILD_FLAG = { value = "1", scope = "build" }

[services.gt.domains."gt.acme.com"]

[services.gt.domains."api.gt.acme.com"]
process = "worker"

[services.gt.processes.worker]
command = ["bun", "run", "worker"]
port = 3001
stop_grace_seconds = 120
singleton = false
memory_mb = 256
cpus = 0.5

[services.gt.processes.worker.health_check]
type = "tcp"
port = 3001
interval_seconds = 5
timeout_seconds = 2
start_period_seconds = 10
deadline_seconds = 60
success_threshold = 1

[services.gt.processes.worker.scale]
replicas = 2

[services.gt.processes.worker.scale.autoscaling]
enabled = false
min = 1
max = 4
target_cpu_percent = 60

[services.gt.processes.clock]
command = ["bun", "run", "clock"]
singleton = true

# ---- git, by URL ----
[services.gu]

[services.gu.git]
url = "https://github.com/acme/gu.git"
branch = "trunk"
submodules = "off"

# ---- database ----
[services.dbs]
name = "Database"
networks = ["backplane"]
stop_grace_seconds = 60
memory_mb = 1024
cpus = 2.0
image_retention = 3
block_egress = false

[services.dbs.database]
engine = "postgres"
version = "17"
flavor = "postgis"
name = "shop"
username = "shop"

[services.dbs.docker]
no_new_privileges = true
pids_limit = 256

[services.dbs.variables]
EXTRA = "1"

[services.dbs.secrets]
keys = ["DB_ROOT_PASSWORD"]

[services.dbs.uptime]
reminder_minutes = 15

[services.dbs.backups.nightly]
destination = "offsite"
cron = "0 2 * * *"
timezone = "Europe/Berlin"
enabled = true
retention_count = 14
retention_days = 30

[services.dbs.backups.minimal]
destination = "offsite"
cron = "@daily"

# ---- route: http ----
[services.rt]
name = "Route"
server = "srv-2"
depends_on = ["img"]

[services.rt.route]
protocol = "http"
upstreams = [{ address = "10.0.0.1:80" }, { service = "img", port = 8080 }]
scheme = "https"
insecure_skip_verify = true
server_name = "printer.lan"
pass_host = false
health_path = "/"
verify_skip = true

[services.rt.domains."rt.acme.com"]

[services.rt.uptime]
enabled = false

# ---- route: tcp ----
[services.tcp]

[services.tcp.route]
protocol = "tcp"
listen_port = 25565
upstreams = [{ address = "10.0.0.1:25565" }]
allow_cidrs = ["203.0.113.0/24"]
deny_cidrs = ["203.0.113.128/25"]
open_to_internet = false
proxy_protocol = 1

# ---- stack ----
[services.stk]
name = "Stack"
depends_on = ["img"]
networks = ["backplane"]

[services.stk.stack]
wait_seconds = 120
compose = '''
services:
  web:
    image: nginx:1.27-alpine
    environment:
      GREETING: ${GREETING}
  cache:
    image: valkey/valkey:8
'''

[services.stk.stack.resources.web]
memory_mb = 256
cpus = 0.5

[services.stk.variables]
GREETING = "hello"

[services.stk.secrets]
keys = ["STK_TOKEN"]

[services.stk.domains."stk.acme.com"]
component = "web"
target_port = 80

[services.stk.uptime]
reminder_minutes = 5
`

func t2KindsResolved(t *testing.T) *Resolved {
	t.Helper()
	f, idx, ds := Parse([]byte(t2KindsDoc), nil)
	require.NotNil(t, f)
	require.False(t, ds.HasErrors(), "the kinds document must be valid:\n%s", ds.RenderText(""))
	r, rds := f.Resolve("", idx)
	require.Empty(t, rds)
	return r
}

func TestFieldsPerKind(t *testing.T) {
	r := t2KindsResolved(t)
	kinds := map[string]string{"img": KindImage, "ex": KindImage, "gt": KindGit, "gu": KindGit, "dbs": KindDatabase, "rt": KindRoute, "tcp": KindRoute, "stk": KindStack}
	require.Len(t, r.Services, len(kinds))
	for slug, kind := range kinds {
		require.Equal(t, kind, r.Services[slug].Kind, slug)
	}

	docker := func(ids ...string) []string {
		var out []string
		for _, df := range dockerFields {
			out = append(out, "config.docker."+df.json)
		}
		return append(out, ids...)
	}
	want := map[string][]string{
		"img": docker(
			"name", "server", "generate_domain", "links.dbs", "networks.backplane",
			"config.image", "config.port", "config.startCommand", "config.oneshot", "config.stopGraceSeconds", "config.memoryMb",
			"config.cpus", "config.imageRetention", "config.blockEgress",
			"config.healthcheck.type", "config.healthcheck.path", "config.healthcheck.port", "config.healthcheck.intervalSeconds",
			"config.healthcheck.timeoutSeconds", "config.healthcheck.startPeriodSeconds", "config.healthcheck.deadlineSeconds",
			"config.healthcheck.successThreshold",
			"config.preDeployCommand", "config.preDeployTimeoutSeconds", "config.postDeployCommand", "config.postDeployTimeoutSeconds",
			"config.files", "volumes.data",
			"variables.PLAIN", "variables.FANCY", "variables.REF", "variables.GEN", "variables.API_TOKEN#secret", "variables.OTHER_SECRET#secret",
			"domains.shop.acme.com", "domains.shop.acme.com.cloudflare", "domains.shop.acme.com/api", "domains.plain.acme.com",
			"scale.replicas", "scale.autoscaling.enabled", "scale.autoscaling.min", "scale.autoscaling.max", "scale.autoscaling.targetCpuPercent",
			"uptime.enabled", "uptime.alerts", "uptime.failureThreshold", "uptime.recoveryThreshold", "uptime.reminderMinutes",
			"tasks.digest", "tasks.minimal",
		),
		"ex": {"server", "config.image", "config.healthcheck.type", "config.healthcheck.command"},
		"gt": {
			"name", "server", "links.img", "config.port", "config.startCommand",
			"config.gitBranch", "config.submodules", "git.link", "git.auto_deploy", "git.watch_paths",
			"config.buildType", "config.dockerfilePath", "config.buildContext", "config.buildLocation", "config.buildServerId", "config.registryId",
			"variables.BUILD_FLAG",
			"config.processName", "config.singleton", "config.processes.worker", "config.processes.clock",
			"processes.worker.scale.replicas", "processes.worker.scale.autoscaling.enabled", "processes.worker.scale.autoscaling.min",
			"processes.worker.scale.autoscaling.max", "processes.worker.scale.autoscaling.targetCpuPercent",
			"domains.gt.acme.com", "domains.api.gt.acme.com",
		},
		"gu": {"server", "config.gitUrl", "config.gitBranch", "config.submodules"},
		"dbs": {
			"name", "server", "networks.backplane", "config.stopGraceSeconds", "config.memoryMb", "config.cpus", "config.imageRetention", "config.blockEgress",
			"config.database.engine", "config.database.version", "config.database.flavor", "config.database.databaseName", "config.database.username",
			"config.docker.noNewPrivileges", "config.docker.pidsLimit",
			"variables.EXTRA", "variables.DB_ROOT_PASSWORD#secret",
			"uptime.reminderMinutes", "backups.nightly", "backups.minimal",
		},
		"rt": {
			"name", "server", "links.img",
			"config.route.protocol", "config.route.upstreams", "config.route.scheme", "config.route.insecureSkipVerify", "config.route.serverName",
			"config.route.passHost", "config.route.healthPath", "config.route.verifySkip",
			"domains.rt.acme.com", "uptime.enabled",
		},
		"tcp": {
			"server", "config.route.protocol", "config.route.upstreams", "config.route.listenPort", "config.route.allowCidrs",
			"config.route.denyCidrs", "config.route.openToInternet", "config.route.proxyProtocol",
		},
		"stk": {
			"name", "server", "links.img", "networks.backplane",
			"config.stack.compose", "config.stack.waitSeconds", "config.stack.resources.web",
			"variables.GREETING", "variables.STK_TOKEN#secret", "domains.stk.acme.com", "uptime.reminderMinutes",
		},
	}
	for slug, ids := range want {
		t.Run("ids of "+slug, func(t *testing.T) {
			sort.Strings(ids)
			require.Equal(t, ids, r.Services[slug].FieldIDs())
		})
	}

	t.Run("every catalog entry is exercised", func(t *testing.T) {
		hit := map[string]bool{}
		for _, rs := range r.Services {
			for id := range rs.Fields {
				spec, ok := LookupField(id)
				require.True(t, ok, id)
				hit[spec.ID] = true
			}
		}
		for _, f := range Catalog() {
			require.True(t, hit[f.ID], "no service in the kinds document produces %q", f.ID)
		}
	})

	t.Run("applies, kinds, ids and paths", func(t *testing.T) {
		for slug, rs := range r.Services {
			for id, fv := range rs.Fields {
				spec, ok := LookupField(id)
				require.True(t, ok, id)
				require.Equal(t, id, fv.ID)
				require.Equal(t, spec.Applies, fv.Applies, "%s/%s", slug, id)
				if len(spec.Kinds) > 0 {
					require.Contains(t, spec.Kinds, rs.Kind, "%s/%s: the catalog does not list this kind", slug, id)
				}
				require.True(t, strings.HasPrefix(fv.Path, rs.Path) || fv.Path == "server", "%s/%s path %q", slug, id, fv.Path)
				require.NotZero(t, fv.Pos.Line, "%s/%s", slug, id)
			}
		}
		immediate := []string{"name", "scale.replicas", "processes.worker.scale.replicas", "git.link", "git.auto_deploy", "git.watch_paths", "uptime.enabled", "tasks.digest", "backups.nightly", "domains.shop.acme.com.cloudflare"}
		for slug, id := range map[string]string{"img": "name", "gt": "git.link"} {
			require.Equal(t, AppliesImmediate, r.Services[slug].Fields[id].Applies)
		}
		for _, id := range immediate {
			spec, ok := LookupField(id)
			require.True(t, ok)
			require.Equal(t, AppliesImmediate, spec.Applies, id)
		}
		for _, id := range []string{"server", "config.port", "variables.X", "variables.X#secret", "domains.a.com", "volumes.v", "links.x", "networks.n", "config.files", "generate_domain"} {
			spec, ok := LookupField(id)
			require.True(t, ok, id)
			require.Equal(t, AppliesStaged, spec.Applies, id)
		}
	})
}

func TestFieldValuesOfTheImageService(t *testing.T) {
	r := t2KindsResolved(t)
	img := r.Services["img"].Fields
	val := func(id string) any {
		fv, ok := img[id]
		require.True(t, ok, id)
		return fv.Value
	}

	require.Equal(t, "Image service", val("name"))
	require.Equal(t, "srv-2", val("server"))
	require.Equal(t, "services.img.server", img["server"].Path)
	require.Equal(t, true, val("generate_domain"))
	require.Equal(t, "dbs", val("links.dbs"))
	require.Equal(t, "backplane", val("networks.backplane"))
	require.Equal(t, 8080, val("config.port"))
	require.Equal(t, 1.5, val("config.cpus"))
	require.Equal(t, false, val("config.oneshot"))
	require.Equal(t, []string{"run", "--fast"}, val("config.startCommand"))
	require.Equal(t, "http", val("config.healthcheck.type"))
	require.Equal(t, 3, val("config.healthcheck.successThreshold"))
	require.Equal(t, []string{"migrate"}, val("config.preDeployCommand"))
	require.Equal(t, 120, val("config.preDeployTimeoutSeconds"))

	t.Run("docker values", func(t *testing.T) {
		require.Equal(t, []string{"IPC_LOCK", "NET_BIND_SERVICE"}, val("config.docker.capAdd"), "upper-case, sorted, no CAP_ prefix")
		require.Equal(t, []string{"NET_RAW"}, val("config.docker.capDrop"))
		require.Equal(t, true, val("config.docker.noNewPrivileges"))
		require.Equal(t, int64(512), val("config.docker.pidsLimit"))
		require.Equal(t, 256, val("config.docker.shmSizeMb"))
		require.Equal(t, "img", val("config.docker.hostname"))
		require.Equal(t, map[string]string{"net.core.somaxconn": "1024"}, val("config.docker.sysctls"))
		require.Equal(t, map[string]int64{"vm.max_map_count": 262144}, val("config.docker.hostSysctls"))
		require.Equal(t, []agentprotocol.Ulimit{{Name: "nofile", Soft: 65536, Hard: 65536}}, val("config.docker.ulimits"))
		require.Equal(t, map[string]string{"team": "shop"}, val("config.docker.labels"))
		require.Equal(t, []agentprotocol.TmpfsMount{{Path: "/tmp", SizeMB: 64, Mode: "1777"}}, val("config.docker.tmpfs"))
		require.Equal(t, []agentprotocol.DeviceMapping{{HostPath: "/dev/dri/renderD128", Permissions: "rw"}}, val("config.docker.devices"))
		require.Equal(t, agentprotocol.GPURequest{Count: 1, Driver: "nvidia", Capabilities: []string{"compute", "utility"}}, val("config.docker.gpus"))
		require.Equal(t, "runsc", val("config.docker.runtime"))
		require.Equal(t, true, val("config.docker.privileged"))
		require.Equal(t, "services.img.docker.cap_add", img["config.docker.capAdd"].Path)
		for _, df := range dockerFields {
			require.Equal(t, df.typ, fmt.Sprintf("%T", val("config.docker."+df.json)), df.json)
		}
	})

	t.Run("files are sorted and default to 0644", func(t *testing.T) {
		require.Equal(t, []FileMount{
			{Path: "/data/app.conf", Mode: "0600", Content: "listen = 8080\n"},
			{Path: "/data/b.conf", Mode: "0644", Content: "b"},
		}, val("config.files"))
	})

	t.Run("variables", func(t *testing.T) {
		require.Equal(t, Variable{Value: "p"}, val("variables.PLAIN"))
		require.Equal(t, Variable{Value: "f", Comment: "documented", RotateEveryDays: new(30), Public: true}, val("variables.FANCY"))
		require.Equal(t, Variable{Value: "${{dbs.DATABASE_URL}}"}, val("variables.REF"))
		require.Equal(t, SecretDecl{Name: "API_TOKEN", Scope: "runtime"}, val("variables.API_TOKEN#secret"))
		require.Equal(t, "services.img.secrets.keys[0]", img["variables.API_TOKEN#secret"].Path)
		require.Equal(t, "services.img.secrets.keys[1]", img["variables.OTHER_SECRET#secret"].Path)
	})

	t.Run("domain rules declare only what is written", func(t *testing.T) {
		shop, ok := val("domains.shop.acme.com").(DomainRule)
		require.True(t, ok)
		require.Equal(t, "shop.acme.com", shop.Host)
		require.Equal(t, "", shop.PathPrefix)
		require.Equal(t, new(8080), shop.TargetPort)
		require.Equal(t, new(false), shop.StripPrefix, "a written false is declared")
		require.Equal(t, new("/v1"), shop.AddPrefix)
		require.Equal(t, new("serve"), shop.Kind)
		require.Equal(t, new(true), shop.PreservePath)
		require.Equal(t, new("auto"), shop.TLS)
		require.Equal(t, new("cloudflare"), shop.DNSResolver)
		require.Equal(t, new(true), shop.WWWRedirect)
		require.Nil(t, shop.Component)
		require.Nil(t, shop.RedirectTo)
		require.Nil(t, shop.RedirectCode)
		require.Nil(t, shop.Certificate)
		require.NotNil(t, shop.Options)
		require.Equal(t, new(true), shop.Options.ForceHTTPS)
		require.True(t, shop.Options.Compress)
		require.False(t, shop.Options.Noindex)
		require.Equal(t, "strict", shop.Options.SecurityHeaders)
		require.True(t, shop.Options.BasicAuth)
		require.Equal(t, []agentprotocol.Header{{Name: "X-A", Value: "1"}, {Name: "X-B", Value: "2"}}, shop.Options.RequestHeaders, "headers are sorted by name")
		require.Empty(t, shop.Options.ResponseHeaders)
		require.NotNil(t, shop.Options.CORS)
		require.Equal(t, []string{"https://app.acme.com"}, shop.Options.CORS.Origins)
		require.Equal(t, 600, shop.Options.CORS.MaxAgeSeconds)
		require.Equal(t, []agentprotocol.Redirect{{Regex: "^/old/(.*)", Replacement: "/new/${1}", Permanent: true}}, shop.Options.Redirects)
		require.Equal(t, 100, shop.Options.RateLimit.Average)
		require.Equal(t, []string{"203.0.113.0/24"}, shop.Options.Access.AllowCIDRs)
		require.Equal(t, 403, shop.Options.Access.RejectStatus)

		api := val("domains.shop.acme.com/api").(DomainRule)
		require.Equal(t, "shop.acme.com", api.Host)
		require.Equal(t, "/api", api.PathPrefix)
		require.Equal(t, new("redirect"), api.Kind)
		require.Equal(t, new("www.acme.com"), api.RedirectTo)
		require.Equal(t, new(301), api.RedirectCode)
		require.Nil(t, api.Options, "options that are not written are not declared")
		require.Nil(t, api.TargetPort)

		plain := val("domains.plain.acme.com").(DomainRule)
		require.Equal(t, DomainRule{Host: "plain.acme.com"}, plain, "an empty table declares nothing")

		cf := val("domains.shop.acme.com.cloudflare").(CloudflareRule)
		require.Equal(t, CloudflareRule{Proxied: new(true), OriginLock: new(false)}, cf)
		require.Equal(t, AppliesImmediate, img["domains.shop.acme.com.cloudflare"].Applies)
		require.Equal(t, `services.img.domains."shop.acme.com".cloudflare`, img["domains.shop.acme.com.cloudflare"].Path)
		require.Equal(t, `services.img.domains."shop.acme.com/api"`, img["domains.shop.acme.com/api"].Path)
	})

	t.Run("volumes, scale, uptime", func(t *testing.T) {
		require.Equal(t, Volume{Name: "data", MountPath: "/data"}, val("volumes.data"))
		require.Equal(t, 1, val("scale.replicas"))
		require.Equal(t, false, val("scale.autoscaling.enabled"))
		require.Equal(t, 3, val("scale.autoscaling.max"))
		require.Equal(t, 70, val("scale.autoscaling.targetCpuPercent"))
		require.Equal(t, false, val("uptime.alerts"))
		require.Equal(t, 4, val("uptime.failureThreshold"))
		require.Equal(t, 30, val("uptime.reminderMinutes"))
	})

	t.Run("tasks have their defaults filled", func(t *testing.T) {
		require.Equal(t, TaskValue{Name: "digest", Schedule: "0 7 * * 1-5", Timezone: "Europe/Berlin", Command: []string{"bun", "run", "digest"}, Enabled: false, TimeoutSeconds: 900, Concurrency: "allow"}, val("tasks.digest"))
		require.Equal(t, TaskValue{Name: "minimal", Schedule: "@daily", Timezone: "UTC", Command: []string{"run"}, Enabled: true, TimeoutSeconds: 1800, Concurrency: "forbid"}, val("tasks.minimal"))
	})
}

func TestFieldValuesOfTheOtherKinds(t *testing.T) {
	r := t2KindsResolved(t)
	get := func(slug, id string) FieldValue { return t2field(t, r, slug, id) }

	t.Run("server falls back to the root server", func(t *testing.T) {
		fv := get("dbs", "server")
		require.Equal(t, "srv", fv.Value)
		require.Equal(t, "server", fv.Path)
		require.Equal(t, 2, fv.Pos.Line)
	})

	t.Run("git", func(t *testing.T) {
		require.Equal(t, GitLink{ConnectionName: "gh", Repo: "acme/gt"}, get("gt", "git.link").Value)
		require.Equal(t, "services.gt.git.connection", get("gt", "git.link").Path)
		require.Equal(t, AppliesImmediate, get("gt", "git.link").Applies)
		require.Equal(t, true, get("gt", "git.auto_deploy").Value)
		require.Equal(t, []string{"apps/gt/**"}, get("gt", "git.watch_paths").Value)
		require.Equal(t, "on", get("gt", "config.submodules").Value)
		require.Equal(t, "dockerfile", get("gt", "config.buildType").Value)
		require.Equal(t, "build_server", get("gt", "config.buildLocation").Value)
		require.Equal(t, "builder-1", get("gt", "config.buildServerId").Value)
		require.Equal(t, "ghcr", get("gt", "config.registryId").Value)
		require.Equal(t, Variable{Value: "1", Scope: "build"}, get("gt", "variables.BUILD_FLAG").Value)
		require.Equal(t, "https://github.com/acme/gu.git", get("gu", "config.gitUrl").Value)
		t2noField(t, r, "gu", "git.link")
		t2noField(t, r, "gt", "config.gitUrl")
	})

	t.Run("processes", func(t *testing.T) {
		require.Equal(t, "web", get("gt", "config.processName").Value)
		require.Equal(t, false, get("gt", "config.singleton").Value)
		require.Equal(t, AppliesStaged, get("gt", "config.processes.worker").Applies)
		require.Equal(t, "services.gt.processes.worker", get("gt", "config.processes.worker").Path)
		require.Equal(t, ProcessValue{
			Name: "worker", Command: []string{"bun", "run", "worker"}, Port: 3001, StopGraceSeconds: 120, MemoryMB: 256, CPUs: 0.5,
			HealthCheck: ProcessHealth{Type: "tcp", Port: 3001, IntervalSeconds: 5, TimeoutSeconds: 2, StartPeriodSeconds: 10, DeadlineSeconds: 60, SuccessThreshold: 1},
		}, get("gt", "config.processes.worker").Value)
		require.Equal(t, ProcessValue{Name: "clock", Command: []string{"bun", "run", "clock"}, Singleton: true}, get("gt", "config.processes.clock").Value,
			"unwritten keys stay zero: they inherit or take defaults")
		require.Equal(t, 2, get("gt", "processes.worker.scale.replicas").Value)
		require.Equal(t, AppliesImmediate, get("gt", "processes.worker.scale.replicas").Applies)
		require.Equal(t, "services.gt.processes.worker.scale.replicas", get("gt", "processes.worker.scale.replicas").Path)
		require.Equal(t, 4, get("gt", "processes.worker.scale.autoscaling.max").Value)
		require.Equal(t, 60, get("gt", "processes.worker.scale.autoscaling.targetCpuPercent").Value)
		t2noField(t, r, "gt", "processes.clock.scale.replicas")
		require.Equal(t, new("worker"), get("gt", "domains.api.gt.acme.com").Value.(DomainRule).Process)
		require.Nil(t, get("gt", "domains.gt.acme.com").Value.(DomainRule).Process)
	})

	t.Run("exec health check", func(t *testing.T) {
		require.Equal(t, []string{"CMD", "true"}, get("ex", "config.healthcheck.command").Value)
	})

	t.Run("database", func(t *testing.T) {
		require.Equal(t, "postgres", get("dbs", "config.database.engine").Value)
		require.Equal(t, "17", get("dbs", "config.database.version").Value)
		require.Equal(t, "postgis", get("dbs", "config.database.flavor").Value)
		require.Equal(t, "shop", get("dbs", "config.database.databaseName").Value)
		require.Equal(t, "shop", get("dbs", "config.database.username").Value)
		require.Equal(t, BackupValue{Key: "nightly", DestinationName: "offsite", Cron: "0 2 * * *", Timezone: "Europe/Berlin", Enabled: true, RetentionCount: 14, RetentionDays: 30}, get("dbs", "backups.nightly").Value)
		require.Equal(t, BackupValue{Key: "minimal", DestinationName: "offsite", Cron: "@daily", Timezone: "UTC", Enabled: true, RetentionCount: 7, RetentionDays: 0}, get("dbs", "backups.minimal").Value, "backup defaults are filled")
		require.Equal(t, AppliesImmediate, get("dbs", "backups.nightly").Applies)
		require.Equal(t, SecretDecl{Name: "DB_ROOT_PASSWORD", Scope: "runtime"}, get("dbs", "variables.DB_ROOT_PASSWORD#secret").Value)
	})

	t.Run("route", func(t *testing.T) {
		require.Equal(t, []UpstreamValue{{Address: "10.0.0.1:80"}, {Service: "img", Port: 8080}}, get("rt", "config.route.upstreams").Value)
		require.Equal(t, "https", get("rt", "config.route.scheme").Value)
		require.Equal(t, false, get("rt", "config.route.passHost").Value)
		require.Equal(t, true, get("rt", "config.route.insecureSkipVerify").Value)
		require.Equal(t, 25565, get("tcp", "config.route.listenPort").Value)
		require.Equal(t, []string{"203.0.113.0/24"}, get("tcp", "config.route.allowCidrs").Value)
		require.Equal(t, []string{"203.0.113.128/25"}, get("tcp", "config.route.denyCidrs").Value)
		require.Equal(t, false, get("tcp", "config.route.openToInternet").Value)
		require.Equal(t, 1, get("tcp", "config.route.proxyProtocol").Value)
	})

	t.Run("stack", func(t *testing.T) {
		compose, ok := get("stk", "config.stack.compose").Value.(string)
		require.True(t, ok)
		require.Contains(t, compose, "valkey/valkey:8")
		require.Equal(t, 120, get("stk", "config.stack.waitSeconds").Value)
		require.Equal(t, StackResourceValue{Component: "web", MemoryMB: 256, CPUs: 0.5}, get("stk", "config.stack.resources.web").Value)
		rule := get("stk", "domains.stk.acme.com").Value.(DomainRule)
		require.Equal(t, new("web"), rule.Component)
		require.Equal(t, new(80), rule.TargetPort)
	})
}

// Keys that are not written are not owned: no default leaks into Fields.
func TestFieldsOnlyHoldWrittenKeys(t *testing.T) {
	r := t2resolve(t, "schema = 1\n\n[services.a]\nimage = \"nginx:1\"\n\n[services.b]\nimage = \"nginx:1\"\nport = 80\n", "")
	require.Equal(t, []string{"config.image"}, r.Services["a"].FieldIDs())
	require.Equal(t, []string{"config.image", "config.port"}, r.Services["b"].FieldIDs())

	empty := t2resolve(t, "schema = 1\n\n[services.a]\nimage = \"nginx:1\"\ncommand = []\nmemory_mb = 0\nport = 0\n", "")
	require.Equal(t, []string{"config.image", "config.memoryMb", "config.port", "config.startCommand"}, empty.Services["a"].FieldIDs(), "an explicit empty value is owned")
	require.Equal(t, []string{}, empty.Services["a"].Fields["config.startCommand"].Value)
	require.Equal(t, 0, empty.Services["a"].Fields["config.memoryMb"].Value)
}

// An omitted proxied is not owned: the file then neither sets nor diffs the proxy setting, so an
// existing DNS-only name is left alone and the server applies its default (proxied, with the
// documented exceptions) only to a name it starts managing. A written value, true or false, is owned.
func TestCloudflareOmittedProxiedIsNotOwned(t *testing.T) {
	const head = "schema = 1\n\n[services.web]\nimage = \"nginx:1\"\nport = 80\n\n[services.web.domains.\"a.acme.com\"]\n\n"
	cf := func(body string) CloudflareRule {
		t.Helper()
		r := t2resolve(t, head+"[services.web.domains.\"a.acme.com\".cloudflare]\n"+body, "")
		v, ok := t2field(t, r, "web", "domains.a.acme.com.cloudflare").Value.(CloudflareRule)
		require.True(t, ok)
		return v
	}
	require.Equal(t, CloudflareRule{OriginLock: new(true)}, cf("origin_lock = true\n"), "omitted proxied stays nil")
	require.Equal(t, CloudflareRule{}, cf(""), "an empty table declares nothing")
	require.Equal(t, CloudflareRule{Proxied: new(false)}, cf("proxied = false\n"))
	require.Equal(t, CloudflareRule{Proxied: new(true)}, cf("proxied = true\n"))

	// No cloudflare table at all: no cloudflare field.
	r := t2resolve(t, head, "")
	require.NotContains(t, r.Services["web"].FieldIDs(), "domains.a.acme.com.cloudflare")
}
