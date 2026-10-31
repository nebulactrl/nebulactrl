package composestack

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"go.yaml.in/yaml/v3"

	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

const shop = `
services:
  web:
    image: nginx:1.27
    ports: ["8080:80", "127.0.0.1:9090:9090/tcp"]
    expose: ["8443"]
    depends_on: [db]
    mem_limit: 256m
    labels: ["team=shop"]
  db:
    image: postgres:17
    volumes: [data:/var/lib/postgresql/data]
    networks: [back]
networks:
  back: {}
volumes:
  data: {}
`

func mustParse(t *testing.T, s string) *Project {
	t.Helper()
	p, err := Parse([]byte(s))
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func TestParseComponents(t *testing.T) {
	p := mustParse(t, shop)
	got := p.Components()
	if len(got) != 2 || got[0].Name != "db" || got[1].Name != "web" || got[0].Index != 0 || got[1].Index != 1 {
		t.Fatalf("components: %+v", got)
	}
	if want := []int{80, 8443, 9090}; len(got[1].Ports) != 3 || got[1].Ports[0] != want[0] || got[1].Ports[1] != want[1] || got[1].Ports[2] != want[2] {
		t.Fatalf("web ports: %v", got[1].Ports)
	}
	if got[1].Image != "nginx:1.27" {
		t.Fatalf("image: %q", got[1].Image)
	}
}

func TestParseErrors(t *testing.T) {
	for name, in := range map[string]string{
		"empty":       "",
		"not yaml":    "services: [",
		"no services": "version: '3'\n",
		"not a map":   "- a\n- b\n",
	} {
		if _, err := Parse([]byte(in)); err == nil {
			t.Errorf("%s: expected an error", name)
		}
	}
}

func TestPortsShortSyntax(t *testing.T) {
	for in, want := range map[string][]int{
		"80":                     {80},
		"8080:80":                {80},
		"127.0.0.1:8080:80/tcp":  {80},
		"[::1]:8080:80":          {80},
		"8000-8002:9000-9002":    {9000, 9001, 9002},
		"${PORT}:80":             {80},
		"${HOST_PORT}:${C_PORT}": nil,
	} {
		got := containerPortsOfShort(in)
		if len(got) != len(want) {
			t.Errorf("%q: got %v, want %v", in, got, want)
			continue
		}
		for i := range got {
			if got[i] != want[i] {
				t.Errorf("%q: got %v, want %v", in, got, want)
			}
		}
	}
}

func TestCheckAcceptsPlainStack(t *testing.T) {
	if vs := Check(mustParse(t, shop), Policy{}); len(vs) != 0 {
		t.Fatalf("unexpected violations: %s", Summary(vs))
	}
}

// Every restricted construct is refused under the default policy, and an
// Unrestricted policy lets exactly the restricted ones through.
func TestCheckAllowList(t *testing.T) {
	cases := []struct {
		name, yaml, svc, key string
	}{
		{"privileged", "privileged: true", "app", "privileged"},
		{"host network", "network_mode: host", "app", "network_mode"},
		{"bridge network", "network_mode: bridge", "app", "network_mode"},
		{"container network", "network_mode: container:other", "app", "network_mode"},
		{"other service network", "network_mode: service:ghost", "app", "network_mode"},
		{"host pid", "pid: host", "app", "pid"},
		{"host ipc", "ipc: host", "app", "ipc"},
		{"host uts", "uts: host", "app", "uts"},
		{"host userns", "userns_mode: host", "app", "userns_mode"},
		{"devices", "devices: ['/dev/fuse:/dev/fuse']", "app", "devices"},
		{"gpus", "gpus: all", "app", "gpus"},
		{"runtime", "runtime: nvidia", "app", "runtime"},
		{"cap ALL", "cap_add: [ALL]", "app", "cap_add"},
		{"cap SYS_MODULE", "cap_add: [CAP_SYS_MODULE]", "app", "cap_add"},
		{"cap SYS_ADMIN", "cap_add: [SYS_ADMIN]", "app", "cap_add"},
		{"seccomp", "security_opt: ['seccomp=unconfined']", "app", "security_opt"},
		{"apparmor", "security_opt: ['apparmor:unconfined']", "app", "security_opt"},
		{"docker socket", "volumes: ['/var/run/docker.sock:/var/run/docker.sock']", "app", "volumes[0]"},
		{"host path", "volumes: ['/etc:/host-etc:ro']", "app", "volumes[0]"},
		{"relative bind", "volumes: ['./data:/data']", "app", "volumes[0]"},
		{"home bind", "volumes: ['~/data:/data']", "app", "volumes[0]"},
		{"long bind", "volumes: [{type: bind, source: /srv, target: /srv}]", "app", "volumes[0]"},
		{"undeclared volume", "volumes: ['ghost:/data']", "app", "volumes[0]"},
		{"volumes_from container", "volumes_from: ['container:db']", "app", "volumes_from[0]"},
		{"reserved label", "labels: {traefik.enable: 'true'}", "app", "labels"},
		{"compose label", "labels: ['com.docker.compose.project=other']", "app", "labels"},
		{"nebula label", "labels: {nebulactrl.service: x}", "app", "labels"},
		{"cgroup parent", "cgroup_parent: /host", "app", "cgroup_parent"},
		{"blkio device", "blkio_config: {device_read_bps: [{path: /dev/sda, rate: 1mb}]}", "app", "blkio_config.device_read_bps"},
		{"privileged hook", "post_start: [{command: id, privileged: true}]", "app", "post_start"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			src := "services:\n  app:\n    image: busybox\n    " + strings.ReplaceAll(tc.yaml, "\n", "\n    ") + "\n"
			p := mustParse(t, src)
			vs := Check(p, Policy{})
			if !hasViolation(vs, tc.svc, tc.key) {
				t.Fatalf("not refused: %s", Summary(vs))
			}
			for _, v := range vs {
				if v.Kind != KindRestricted {
					t.Fatalf("expected a restricted violation, got %+v", v)
				}
			}
			if un := Check(p, Policy{Unrestricted: true}); len(un) != 0 {
				t.Fatalf("Unrestricted still refuses: %s", Summary(un))
			}
		})
	}
}

func TestCheckUnrestrictedCaps(t *testing.T) {
	p := mustParse(t, "services:\n  app:\n    image: busybox\n    cap_add: [NET_ADMIN, NET_BIND_SERVICE]\n")
	if vs := Check(p, Policy{}); !hasViolation(vs, "app", "cap_add") {
		t.Fatalf("NET_ADMIN must be refused by default: %v", vs)
	}
	if vs := Check(p, Policy{Unrestricted: true}); len(vs) != 0 {
		t.Fatalf("NET_ADMIN is allowed on an unrestricted server: %s", Summary(vs))
	}
}

func TestCheckAllowsSafeOptions(t *testing.T) {
	p := mustParse(t, `
services:
  app:
    image: busybox
    cap_add: [NET_BIND_SERVICE, CHOWN]
    security_opt: ["no-new-privileges:true"]
    network_mode: "none"
    volumes:
      - data:/data
      - type: tmpfs
        target: /cache
    tmpfs: [/run]
    sysctls: {net.core.somaxconn: 1024}
    ulimits: {nofile: 4096}
    shm_size: 256m
    labels: {team: shop}
    healthcheck: {test: ["CMD", "true"]}
  side:
    image: busybox
    network_mode: service:app
    pid: service:app
volumes:
  data: {}
`)
	if vs := Check(p, Policy{}); len(vs) != 0 {
		t.Fatalf("safe options refused: %s", Summary(vs))
	}
}

func TestCheckUnsupportedSurvivesUnrestricted(t *testing.T) {
	cases := map[string]string{
		"build":    "build: .",
		"provider": "image: busybox\n    provider: {type: foo}",
		"env_file": "image: busybox\n    env_file: .env",
		"scale":    "image: busybox\n    scale: 3",
		"replicas": "image: busybox\n    deploy: {replicas: 2}",
		"no image": "command: sleep 1",
		"extends":  "extends: {file: other.yml, service: x}",
	}
	for name, body := range cases {
		src := "services:\n  app:\n    " + body + "\n"
		vs := Check(mustParse(t, src), Policy{Unrestricted: true})
		if len(vs) == 0 {
			t.Errorf("%s: an unsupported construct passed under Unrestricted", name)
			continue
		}
		for _, v := range vs {
			if v.Kind != KindUnsupported {
				t.Errorf("%s: %+v should be unsupported", name, v)
			}
		}
	}
}

func TestCheckBadNames(t *testing.T) {
	p := mustParse(t, "services:\n  My.App:\n    image: busybox\n")
	if vs := Check(p, Policy{Unrestricted: true}); !hasViolation(vs, "My.App", "name") {
		t.Fatalf("expected a name violation: %v", vs)
	}
}

func TestCheckVolumesAndNetworks(t *testing.T) {
	src := `
services:
  app:
    image: busybox
    volumes: [data:/data, shared:/shared]
volumes:
  data:
    name: nc-1234abcd_data
  shared:
    external: true
    name: nc-vol-0123abcd-secrets
  nfs:
    driver_opts: {type: nfs, o: "addr=10.0.0.1", device: ":/export"}
  bindish:
    driver: local
    driver_opts: {type: none, o: bind, device: /etc}
networks:
  default:
    name: nc-1234abcd_default
  edge:
    external: true
    name: nebulactrl-edge
  macv:
    driver: macvlan
secrets:
  hostfile: {file: /etc/shadow}
  inline: {content: "x"}
configs:
  hostconf: {file: ./c}
`
	vs := Check(mustParse(t, src), Policy{Project: "nc-1234abcd"})
	for _, key := range []string{"volumes.shared", "volumes.nfs", "volumes.bindish", "networks.edge", "networks.macv", "secrets.hostfile", "configs.hostconf"} {
		if !hasViolation(vs, "", key) {
			t.Errorf("%s not refused: %s", key, Summary(vs))
		}
	}
	for _, key := range []string{"volumes.data", "networks.default", "secrets.inline"} {
		if hasViolation(vs, "", key) {
			t.Errorf("%s wrongly refused: %s", key, Summary(vs))
		}
	}
	if un := Check(mustParse(t, src), Policy{Project: "nc-1234abcd", Unrestricted: true}); len(un) != 0 {
		t.Fatalf("Unrestricted: %s", Summary(un))
	}
}

func hasViolation(vs []Violation, svc, key string) bool {
	for _, v := range vs {
		if v.Service == svc && v.Key == key {
			return true
		}
	}
	return false
}

func overrideOf(t *testing.T, p *Project, in OverrideInput) (Override, map[string]any) {
	t.Helper()
	o, err := BuildOverride(p, in)
	if err != nil {
		t.Fatal(err)
	}
	var doc map[string]any
	if err := yaml.Unmarshal(o.YAML, &doc); err != nil {
		t.Fatalf("override is not YAML: %v\n%s", err, o.YAML)
	}
	return o, doc
}

func baseInput() OverrideInput {
	return OverrideInput{
		ServiceID: "11111111-2222-3333-4444-555555555555", DeploymentID: "dep-1", ReleaseID: "rel-1", EnvironmentID: "env-1",
		Attachments: []Attachment{{Name: "nebulactrl-env-env-1"}},
	}
}

func TestOverrideInjectsLabelsNetworksAndDropsPorts(t *testing.T) {
	p := mustParse(t, shop)
	o, doc := overrideOf(t, p, baseInput())
	yamlText := string(o.YAML)

	svcs := doc["services"].(map[string]any)
	web := svcs["web"].(map[string]any)
	db := svcs["db"].(map[string]any)

	if web["container_name"] != "nc-11111111-web" || db["container_name"] != "nc-11111111-db" {
		t.Fatalf("container names: %v %v", web["container_name"], db["container_name"])
	}
	labels := web["labels"].(map[string]any)
	for k, want := range map[string]string{
		agentprotocol.LabelManaged: "true", agentprotocol.LabelService: "11111111-2222-3333-4444-555555555555",
		agentprotocol.LabelDeployment: "dep-1", agentprotocol.LabelRelease: "rel-1", agentprotocol.LabelEnvironment: "env-1",
		agentprotocol.LabelComponent: "web", agentprotocol.LabelReplica: "1",
	} {
		if labels[k] != want {
			t.Errorf("label %s = %v, want %s", k, labels[k], want)
		}
	}
	if db["labels"].(map[string]any)[agentprotocol.LabelReplica] != "0" {
		t.Errorf("db replica index: %v", db["labels"])
	}
	// Ports are removed with !reset, only for the service that has some.
	if !strings.Contains(yamlText, "ports: !reset []") {
		t.Errorf("web ports not reset:\n%s", yamlText)
	}
	if _, ok := db["ports"]; ok {
		t.Errorf("db has no ports to reset")
	}
	if web["restart"] != "unless-stopped" {
		t.Errorf("restart default: %v", web["restart"])
	}
	// web had no explicit networks: it keeps the project's default one.
	wn := web["networks"].(map[string]any)
	if _, ok := wn["default"]; !ok {
		t.Errorf("web lost the default network: %v", wn)
	}
	if alias := wn["nc_net_0"].(map[string]any)["aliases"].([]any); len(alias) != 1 || alias[0] != "web" {
		t.Errorf("web alias: %v", alias)
	}
	// db has explicit networks (back): the default is not added.
	if _, ok := db["networks"].(map[string]any)["default"]; ok {
		t.Errorf("db wrongly gained the default network")
	}
	// The environment network is declared external under its real name.
	ext := doc["networks"].(map[string]any)["nc_net_0"].(map[string]any)
	if ext["name"] != "nebulactrl-env-env-1" || ext["external"] != true {
		t.Errorf("external network: %v", ext)
	}
	if len(o.Notes) == 0 || !strings.Contains(o.Notes[0], "web: ports are not published") {
		t.Errorf("notes: %v", o.Notes)
	}
}

func TestOverrideIsolateAndSharedAliases(t *testing.T) {
	p := mustParse(t, shop)
	in := baseInput()
	in.Attachments = []Attachment{{Name: "nebulactrl-env-env-1-int"}, {Name: "nebulactrl-shared-abcd1234", AliasSuffix: "prod.shop"}}
	in.Isolate = true
	o, doc := overrideOf(t, p, in)
	if !strings.Contains(string(o.YAML), "networks: !override") {
		t.Errorf("isolated networks must replace the compose networks:\n%s", o.YAML)
	}
	web := doc["services"].(map[string]any)["web"].(map[string]any)
	wn := web["networks"].(map[string]any)
	if _, ok := wn["default"]; ok {
		t.Errorf("isolated service kept the default network")
	}
	if a := wn["nc_net_1"].(map[string]any)["aliases"].([]any); a[0] != "web.prod.shop" {
		t.Errorf("shared alias: %v", a)
	}
}

func TestOverrideResourcesAndPinnedImages(t *testing.T) {
	p := mustParse(t, shop)
	in := baseInput()
	in.Resources = map[string]agentprotocol.StackResources{"web": {MemoryMB: 512, CPUs: 1.5}}
	in.Images = map[string]string{"db": "postgres@sha256:abc"}
	o, doc := overrideOf(t, p, in)
	web := doc["services"].(map[string]any)["web"].(map[string]any)
	limits := web["deploy"].(map[string]any)["resources"].(map[string]any)["limits"].(map[string]any)
	if limits["memory"] != "512M" || limits["cpus"] != "1.5" {
		t.Errorf("limits: %v", limits)
	}
	// The compose file's mem_limit conflicts with deploy limits and is reset.
	if !strings.Contains(string(o.YAML), "mem_limit: !reset null") {
		t.Errorf("mem_limit not reset:\n%s", o.YAML)
	}
	if strings.Contains(string(o.YAML), "cpus: !reset") {
		t.Errorf("cpus reset without a base cpus:\n%s", o.YAML)
	}
	db := doc["services"].(map[string]any)["db"].(map[string]any)
	if db["image"] != "postgres@sha256:abc" || db["pull_policy"] != "missing" {
		t.Errorf("pinned image: %v %v", db["image"], db["pull_policy"])
	}
	if _, ok := web["image"]; ok {
		t.Errorf("web image must not be overridden")
	}
}

func TestOverrideNetworkModeAndProfilesAreNoted(t *testing.T) {
	p := mustParse(t, `
services:
  vpn: {image: busybox}
  app: {image: busybox, network_mode: "service:vpn"}
  tools: {image: busybox, profiles: [debug]}
`)
	o, doc := overrideOf(t, p, baseInput())
	app := doc["services"].(map[string]any)["app"].(map[string]any)
	if _, ok := app["networks"]; ok {
		t.Errorf("a service with network_mode must not get networks")
	}
	joined := strings.Join(o.Notes, "\n")
	if !strings.Contains(joined, "app: network_mode service:vpn") || !strings.Contains(joined, "tools: belongs to the profiles debug") {
		t.Errorf("notes: %v", o.Notes)
	}
}

// The override must never carry keys the allow-list refuses.
func TestOverrideTripsNoAllowListRule(t *testing.T) {
	p := mustParse(t, shop)
	o, _ := overrideOf(t, p, baseInput())
	// Merge-checking the override as a model of its own: it has no image
	// (and no network_mode, privileged, ...), so only the image rule fires.
	op, err := Parse(o.YAML)
	if err != nil {
		t.Fatal(err)
	}
	for _, v := range Check(op, Policy{Project: "x"}) {
		// The reserved labels are ours; the external network is ours too.
		if v.Key != "image" && v.Key != "labels" && v.Key != "networks.nc_net_0" {
			t.Errorf("override violates the allow-list: %s", v)
		}
	}
}

// With a real `docker compose` the generated override merges into the base
// and yields what the agent will run. Skipped without the compose plugin (the
// command needs no daemon).
func TestOverrideMergesWithDockerCompose(t *testing.T) {
	if err := exec.Command("docker", "compose", "version").Run(); err != nil {
		t.Skip("docker compose is not installed")
	}
	dir := t.TempDir()
	p := mustParse(t, shop)
	in := baseInput()
	in.Resources = map[string]agentprotocol.StackResources{"web": {MemoryMB: 128}}
	in.Images = map[string]string{"db": "postgres:17"}
	o, _ := overrideOf(t, p, in)
	for name, data := range map[string][]byte{"compose.yaml": []byte(shop), "override.yaml": o.YAML} {
		if err := os.WriteFile(filepath.Join(dir, name), data, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	for _, isolate := range []bool{false, true} {
		in := in
		in.Isolate = isolate
		o2, _ := overrideOf(t, p, in)
		if err := os.WriteFile(filepath.Join(dir, "override.yaml"), o2.YAML, 0o600); err != nil {
			t.Fatal(err)
		}
		cmd := exec.Command("docker", "compose", "-p", "nc-11111111", "-f", "compose.yaml", "-f", "override.yaml", "config", "--format", "json")
		cmd.Dir = dir
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("compose config (isolate=%v): %v\n%s\n%s", isolate, err, out, o2.YAML)
		}
		var merged struct {
			Services map[string]struct {
				ContainerName string                     `json:"container_name"`
				Ports         []any                      `json:"ports"`
				Labels        map[string]string          `json:"labels"`
				Networks      map[string]json.RawMessage `json:"networks"`
				MemLimit      string                     `json:"mem_limit"`
				Deploy        struct {
					Resources struct {
						Limits struct {
							Memory string `json:"memory"`
						} `json:"limits"`
					} `json:"resources"`
				} `json:"deploy"`
			} `json:"services"`
		}
		if err := json.Unmarshal(out, &merged); err != nil {
			t.Fatal(err)
		}
		web := merged.Services["web"]
		if web.ContainerName != "nc-11111111-web" || len(web.Ports) != 0 || web.MemLimit != "" || web.Deploy.Resources.Limits.Memory != "134217728" {
			t.Errorf("isolate=%v web: %+v", isolate, web)
		}
		if web.Labels["team"] != "shop" || web.Labels[agentprotocol.LabelComponent] != "web" {
			t.Errorf("labels were not merged: %v", web.Labels)
		}
		_, hasDefault := web.Networks["default"]
		_, hasBack := merged.Services["db"].Networks["back"]
		_, hasEnv := web.Networks["nc_net_0"]
		if !hasEnv || hasDefault == isolate || hasBack == isolate {
			t.Errorf("isolate=%v networks: web=%v db=%v", isolate, web.Networks, merged.Services["db"].Networks)
		}
	}
}
