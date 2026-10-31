package projectconfig

import (
	"slices"
	"sort"
	"strings"
	"testing"
)

var (
	testSvcPath    = []string{"services", "web"}
	testEnvSvcPath = []string{"environments", "staging", "services", "web"}
)

func TestOSADistance(t *testing.T) {
	tests := []struct {
		a, b string
		want int
	}{
		{"", "", 0},
		{"abc", "abc", 0},
		{"abc", "", 3},
		{"", "ab", 2},
		{"ab", "ba", 1},
		{"port", "prot", 1},
		{"abcd", "acbd", 1},
		{"ca", "abc", 3}, // optimal string alignment, not unrestricted Damerau-Levenshtein (2)
		{"kitten", "sitting", 3},
		{"flaw", "lawn", 2},
		{"health_check", "helth_check", 1},
		{"healthcheck_path", "health_check", 6},
		{"stop_grace_seconds", "stop_grace_secnds", 1},
		{"café", "cafe", 1},
	}
	for _, tt := range tests {
		t.Run(tt.a+"/"+tt.b, func(t *testing.T) {
			if got := osaDistance([]rune(tt.a), []rune(tt.b)); got != tt.want {
				t.Fatalf("osaDistance(%q, %q) = %d, want %d", tt.a, tt.b, got, tt.want)
			}
			if got := osaDistance([]rune(tt.b), []rune(tt.a)); got != tt.want {
				t.Fatalf("osaDistance(%q, %q) = %d, want %d (symmetry)", tt.b, tt.a, got, tt.want)
			}
		})
	}
}

func TestValidKeys(t *testing.T) {
	tests := []struct {
		name string
		path []string
		want []string
	}{
		{"root", nil, []string{"$schema", "environments", "project", "schema", "secrets", "server", "services", "variables"}},
		{"environment", []string{"environments", "staging"}, []string{"secrets", "server", "services", "variables"}},
		{"git", []string{"services", "web", "git"}, []string{"auto_deploy", "branch", "connection", "repo", "submodules", "url", "watch_paths"}},
		{"health check", []string{"services", "web", "health_check"}, []string{"command", "deadline_seconds", "interval_seconds", "path", "port", "start_period_seconds", "success_threshold", "timeout_seconds", "type"}},
		{"domain by host", []string{"services", "web", "domains", "shop.acme.com"}, []string{"add_prefix", "certificate", "cloudflare", "component", "dns_resolver", "kind", "options", "preserve_path", "process", "redirect_code", "redirect_to", "strip_prefix", "target_port", "tls", "www_redirect"}},
		{"domain with path prefix key", []string{"services", "web", "domains", "acme.com/api"}, []string{"add_prefix", "certificate", "cloudflare", "component", "dns_resolver", "kind", "options", "preserve_path", "process", "redirect_code", "redirect_to", "strip_prefix", "target_port", "tls", "www_redirect"}},
		{"redirect element", []string{"services", "web", "domains", "a.com", "options", "redirects"}, []string{"permanent", "regex", "replacement"}},
		{"ulimit by name", []string{"services", "web", "docker", "ulimits", "nofile"}, []string{"hard", "soft"}},
		{"tmpfs by path", []string{"services", "web", "docker", "tmpfs", "/tmp"}, []string{"mode", "size_mb"}},
		{"device element", []string{"services", "web", "docker", "devices"}, []string{"container_path", "host_path", "permissions"}},
		{"upstream element", []string{"services", "web", "route", "upstreams"}, []string{"address", "port", "service"}},
		{"stack resource", []string{"services", "web", "stack", "resources", "web"}, []string{"cpus", "memory_mb"}},
		{"process", []string{"services", "web", "processes", "worker"}, []string{"command", "cpus", "health_check", "memory_mb", "port", "scale", "singleton", "stop_grace_seconds"}},
		{"process in an override", []string{"environments", "staging", "services", "web", "processes", "worker"}, []string{"command", "cpus", "health_check", "memory_mb", "port", "scale", "singleton", "stop_grace_seconds"}},
		{"process health check", []string{"services", "web", "processes", "worker", "health_check"}, []string{"command", "deadline_seconds", "interval_seconds", "path", "port", "start_period_seconds", "success_threshold", "timeout_seconds", "type"}},
		{"process scale", []string{"services", "web", "processes", "worker", "scale"}, []string{"autoscaling", "replicas"}},
		{"process autoscaling", []string{"services", "web", "processes", "worker", "scale", "autoscaling"}, []string{"enabled", "max", "min", "target_cpu_percent"}},
		{"autoscaling", []string{"services", "web", "scale", "autoscaling"}, []string{"enabled", "max", "min", "target_cpu_percent"}},
		{"service secrets", []string{"services", "web", "secrets"}, []string{"keys", "scope"}},
		{"root secrets", []string{"secrets"}, []string{"keys", "scope"}},
		{"root variable entry", []string{"variables", "FOO"}, []string{"comment", "public", "rotate_every_days", "scope", "value"}},
		{"service variable entry", []string{"services", "web", "variables", "FOO"}, []string{"comment", "public", "rotate_every_days", "scope", "value"}},
		{"env variable entry", []string{"environments", "staging", "variables", "FOO"}, []string{"comment", "public", "rotate_every_days", "scope", "value"}},

		{"services table is keyed by slug", []string{"services"}, nil},
		{"variables table is keyed by name", []string{"variables"}, nil},
		{"domains table is keyed by host", []string{"services", "web", "domains"}, nil},
		{"ulimits table is keyed by name", []string{"services", "web", "docker", "ulimits"}, nil},
		{"string map entry", []string{"services", "web", "volumes", "data"}, nil},
		{"string map entry under docker", []string{"services", "web", "docker", "labels", "team"}, nil},
		{"array of strings", []string{"services", "web", "docker", "dns"}, nil},
		{"scalar field", []string{"services", "web", "image"}, nil},
		{"scalar below table", []string{"services", "web", "git", "url"}, nil},
		{"unknown root table", []string{"nope"}, nil},
		{"unknown service table", []string{"services", "web", "nope"}, nil},
		{"processes table is keyed by name", []string{"services", "web", "processes"}, nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := validKeys(tt.path)
			if !slices.Equal(got, tt.want) {
				t.Fatalf("validKeys(%v) =\n  %v\nwant\n  %v", tt.path, got, tt.want)
			}
		})
	}
}

func TestValidKeysServiceVariants(t *testing.T) {
	base := validKeys(testSvcPath)
	override := validKeys(testEnvSvcPath)

	if !slices.Contains(base, "only_in") || slices.Contains(base, "unset") {
		t.Errorf("base service must allow only_in and not unset: %v", base)
	}
	if slices.Contains(override, "only_in") || !slices.Contains(override, "unset") {
		t.Errorf("environment override must allow unset and not only_in: %v", override)
	}
	// Everything else is shared.
	strip := func(keys []string, drop string) []string {
		return slices.DeleteFunc(slices.Clone(keys), func(k string) bool { return k == drop })
	}
	if a, b := strip(base, "only_in"), strip(override, "unset"); !slices.Equal(a, b) {
		t.Errorf("base and override differ beyond only_in/unset:\n%v\n%v", a, b)
	}
	for _, k := range []string{"image", "git", "build", "docker", "scale", "health_check", "stop_grace_seconds", "memory_mb", "tasks", "backups", "files", "processes", "process_name", "singleton"} {
		if !slices.Contains(base, k) {
			t.Errorf("service keys miss %q", k)
		}
	}
}

func TestValidKeysDoesNotMutatePath(t *testing.T) {
	path := []string{"services", "web", "domains", "a.com"}
	before := slices.Clone(path)
	validKeys(path)
	Suggest(path, "kidn")
	if !slices.Equal(path, before) {
		t.Fatalf("path was modified: %v", path)
	}
}

func TestSuggest(t *testing.T) {
	git := []string{"services", "web", "git"}
	tests := []struct {
		name string
		path []string
		key  string
		want []string
	}{
		// Edit distance.
		{"typo of port", testSvcPath, "portt", []string{"port"}},
		{"typo in env override", testEnvSvcPath, "portt", []string{"port"}},
		{"distance 1 on 5 letters", git, "brnch", []string{"branch"}},
		{"distance 2 on 4 letters", git, "brch", []string{"branch"}},
		{"distance 3 on 3 letters is too far", git, "bch", nil},
		{"threshold grows with length", git, "autodeploytt", []string{"auto_deploy"}},
		{"ties are alphabetical", []string{"services", "web", "scale", "autoscaling"}, "mix", []string{"max", "min"}},
		{"transposition counts once", testSvcPath, "prot", []string{"port"}},
		{"root typo", nil, "servces", []string{"services", "server"}},
		{"nothing close", testSvcPath, "completely_unrelated_setting", nil},

		// Spellings of the same key.
		{"camelCase", testSvcPath, "stopGraceSeconds", []string{"stop_grace_seconds"}},
		{"kebab-case", testSvcPath, "stop-grace-seconds", []string{"stop_grace_seconds"}},
		{"camelCase short", testSvcPath, "memoryMb", []string{"memory_mb"}},
		{"dependsOn", testSvcPath, "dependsOn", []string{"depends_on"}},
		{"depends-on", testSvcPath, "depends-on", []string{"depends_on"}},
		{"onlyIn", testSvcPath, "onlyIn", []string{"only_in"}},
		{"upper case", testSvcPath, "PORT", []string{"port"}},
		{"camel nested table", testSvcPath, "preDeploy", []string{"pre_deploy"}},
		{"upper case at root", nil, "$SCHEMA", []string{"$schema", "schema"}},

		// Aliases.
		{"healthcheck", testSvcPath, "healthcheck", []string{"health_check"}},
		{"healthCheck", testSvcPath, "healthCheck", []string{"health_check"}},
		{"health-check", testSvcPath, "health-check", []string{"health_check"}},
		{"healthcheck_path", testSvcPath, "healthcheck_path", []string{"health_check.path"}},
		{"healthcheck_path in override", testEnvSvcPath, "healthcheck_path", []string{"health_check.path"}},
		{"env", testSvcPath, "env", []string{"variables"}},
		{"envs", testSvcPath, "envs", []string{"variables"}},
		{"env_vars", testSvcPath, "env_vars", []string{"variables"}},
		{"environment in service", testSvcPath, "environment", []string{"variables"}},
		{"env in environment block", []string{"environments", "staging"}, "env", []string{"variables"}},
		{"env at root", nil, "env", []string{"variables"}},
		{"environment at root is the environments table", nil, "environment", []string{"environments"}},
		{"start_command", testSvcPath, "start_command", []string{"command"}},
		{"startCommand", testSvcPath, "startCommand", []string{"command"}},
		{"cmd", testSvcPath, "cmd", []string{"command"}},
		{"entrypoint at service level", testSvcPath, "entrypoint", []string{"command"}},
		{"cmd in health check", []string{"services", "web", "health_check"}, "cmd", []string{"command"}},
		{"cmd in pre_deploy", []string{"services", "web", "pre_deploy"}, "cmd", []string{"command"}},
		{"entrypoint is not a command in health check", []string{"services", "web", "health_check"}, "entrypoint", nil},
		{"memory", testSvcPath, "memory", []string{"memory_mb"}},
		{"mem", testSvcPath, "mem", []string{"memory_mb"}},
		{"memory in stack resources", []string{"services", "web", "stack", "resources", "api"}, "memory", []string{"memory_mb"}},
		{"cpu", testSvcPath, "cpu", []string{"cpus"}},
		{"domain", testSvcPath, "domain", []string{"domains"}},
		{"hostname at service level", testSvcPath, "hostname", []string{"domains"}},
		{"replicas", testSvcPath, "replicas", []string{"scale.replicas"}},
		{"instances", testSvcPath, "instances", []string{"scale.replicas"}},
		{"replicas in override", testEnvSvcPath, "replicas", []string{"scale.replicas"}},
		{"replicas outside a service has no alias", []string{"services", "web", "docker"}, "replicas", nil},
		{"cron", testSvcPath, "cron", []string{"tasks.<name>"}},
		{"schedule", testSvcPath, "schedule", []string{"tasks.<name>"}},
		{"cron in a task has no alias", []string{"services", "web", "tasks", "nightly"}, "cron", nil},
		{"volume", testSvcPath, "volume", []string{"volumes"}},
		{"stop_grace", testSvcPath, "stop_grace", []string{"stop_grace_seconds"}},
		{"dockerfilePath", testSvcPath, "dockerfilePath", []string{"build.dockerfile"}},
		{"dockerfile", testSvcPath, "dockerfile", []string{"build.dockerfile"}},
		{"version at root", nil, "version", []string{"schema"}},
		{"version in a service has no alias", testSvcPath, "version", nil},
		{"workers", testSvcPath, "workers", []string{"processes"}},
		{"worker", testSvcPath, "worker", []string{"processes.<name>", "docker"}}, // docker is within edit distance 2
		{"process also matches its plural by distance", testSvcPath, "process", []string{"processes.<name>", "processes"}},
		{"worker in override", testEnvSvcPath, "worker", []string{"processes.<name>", "docker"}},
		{"worker in a process has no alias", []string{"services", "web", "processes", "sidekiq"}, "worker", nil},
		{"typo of processes", testSvcPath, "proceses", []string{"processes"}},
		{"typo of process_name", testSvcPath, "proces_name", []string{"process_name"}},
		{"camelCase processName", testSvcPath, "processName", []string{"process_name"}},
		{"cmd in a process", []string{"services", "web", "processes", "sidekiq"}, "cmd", []string{"command"}},
		{"startCommand in a process has no service alias", []string{"services", "web", "processes", "sidekiq"}, "startCommand", nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := Suggest(tt.path, tt.key)
			if !slices.Equal(got, tt.want) {
				t.Fatalf("Suggest(%v, %q) = %v, want %v", tt.path, tt.key, got, tt.want)
			}
		})
	}
}

func TestSuggestFirstChoice(t *testing.T) {
	tests := []struct {
		name  string
		path  []string
		key   string
		first string
	}{
		{"helth_check", testSvcPath, "helth_check", "health_check"},
		{"healthcheckPath", testSvcPath, "healthcheckPath", "health_check.path"},
		{"imge", testSvcPath, "imge", "image"},
		{"scal", testSvcPath, "scal", "scale"},
		{"servr", testSvcPath, "servr", "server"},
		{"hosts", testSvcPath, "hosts", "domains"},
		{"hostnames", testSvcPath, "hostnames", "domains"},
		{"redirect_cod", []string{"services", "web", "domains", "a.com"}, "redirect_cod", "redirect_code"},
		{"memry_mb", testSvcPath, "memry_mb", "memory_mb"},
		{"stop_grace_secnds", testSvcPath, "stop_grace_secnds", "stop_grace_seconds"},
		{"nested table typo", []string{"services", "web", "docker"}, "read_only_rootf", "read_only_rootfs"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := Suggest(tt.path, tt.key)
			if len(got) == 0 || got[0] != tt.first {
				t.Fatalf("Suggest(%v, %q) = %v, want first %q", tt.path, tt.key, got, tt.first)
			}
		})
	}
}

func TestSuggestCredentialKeysHaveNoSuggestion(t *testing.T) {
	keys := []string{
		"token", "password", "passwd", "secret", "registry_auth", "registry_password", "registry_token",
		"git_token", "deploy_key", "ssh_key", "private_key", "api_key", "access_token", "auth_token",
		"basic_auth_users", "webhook_url", "cloudflare_token", "credentials",
		"Token", "Registry-Auth", "GIT_TOKEN", "api-key", "deploy-key",
	}
	paths := [][]string{nil, testSvcPath, testEnvSvcPath, {"services", "web", "git"}, {"services", "web", "docker"}}
	for _, path := range paths {
		for _, key := range keys {
			if got := Suggest(path, key); got != nil {
				t.Errorf("Suggest(%v, %q) = %v, want nil", path, key, got)
			}
		}
	}
}

func TestSuggestNoValidKeys(t *testing.T) {
	for _, path := range [][]string{
		{"services"},
		{"variables"},
		{"services", "web", "domains"},
		{"services", "web", "volumes", "data"},
		{"nope"},
	} {
		if got := Suggest(path, "name"); got != nil {
			t.Errorf("Suggest(%v, name) = %v, want nil", path, got)
		}
	}
}

func TestSuggestLongKeyIsNotCompared(t *testing.T) {
	key := strings.Repeat("a", 5000)
	if got := Suggest(testSvcPath, key); got != nil {
		t.Fatalf("Suggest of a 5000-byte key = %v, want nil", got)
	}
}

func TestCredentialMessage(t *testing.T) {
	want := "credentials are never stored in nebula.toml; set the value in the dashboard or CLI, or use secrets.keys"
	if got := credentialMessage(); got != want {
		t.Fatalf("credentialMessage() = %q", got)
	}
}

// typoVariants are the mistakes an author makes with a key they know.
func typoVariants(key string) []string {
	var out []string
	if len(key) >= 3 {
		// dropped last letter
		out = append(out, key[:len(key)-1])
	}
	if len(key) >= 2 && key[0] != key[1] {
		// swapped first two letters
		out = append(out, string(key[1])+string(key[0])+key[2:])
	}
	if strings.Contains(key, "_") {
		parts := strings.Split(key, "_")
		for i := 1; i < len(parts); i++ {
			if parts[i] != "" {
				parts[i] = strings.ToUpper(parts[i][:1]) + parts[i][1:]
			}
		}
		// camelCase and kebab-case
		out = append(out, strings.Join(parts, ""), strings.ReplaceAll(key, "_", "-"))
	}
	out = append(out, strings.ToUpper(key))
	return out
}

// TestSuggestEveryKeySuggestsItself checks that, for every table of the model
// and every valid key in it, the usual typos of the key lead back to the key.
func TestSuggestEveryKeySuggestsItself(t *testing.T) {
	paths := [][]string{
		nil,
		{"environments", "staging"},
		testSvcPath,
		testEnvSvcPath,
		{"services", "web", "git"},
		{"services", "web", "build"},
		{"services", "web", "health_check"},
		{"services", "web", "pre_deploy"},
		{"services", "web", "post_deploy"},
		{"services", "web", "database"},
		{"services", "web", "route"},
		{"services", "web", "route", "upstreams"},
		{"services", "web", "stack"},
		{"services", "web", "stack", "resources", "api"},
		{"services", "web", "domains", "a.com"},
		{"services", "web", "domains", "a.com", "options"},
		{"services", "web", "domains", "a.com", "options", "cors"},
		{"services", "web", "domains", "a.com", "options", "redirects"},
		{"services", "web", "domains", "a.com", "options", "rate_limit"},
		{"services", "web", "domains", "a.com", "options", "access"},
		{"services", "web", "domains", "a.com", "cloudflare"},
		{"services", "web", "files", "/etc/app.conf"},
		{"services", "web", "scale"},
		{"services", "web", "scale", "autoscaling"},
		{"services", "web", "uptime"},
		{"services", "web", "tasks", "nightly"},
		{"services", "web", "processes", "worker"},
		{"services", "web", "processes", "worker", "health_check"},
		{"services", "web", "processes", "worker", "scale"},
		{"services", "web", "processes", "worker", "scale", "autoscaling"},
		{"services", "web", "backups", "daily"},
		{"services", "web", "docker"},
		{"services", "web", "docker", "ulimits", "nofile"},
		{"services", "web", "docker", "tmpfs", "/tmp"},
		{"services", "web", "docker", "devices"},
		{"services", "web", "docker", "gpus"},
		{"services", "web", "secrets"},
		{"services", "web", "variables", "FOO"},
	}
	for _, path := range paths {
		keys := validKeys(path)
		if len(keys) == 0 {
			t.Fatalf("validKeys(%v) is empty", path)
		}
		if !sort.StringsAreSorted(keys) {
			t.Errorf("validKeys(%v) is not sorted: %v", path, keys)
		}
		for i := 1; i < len(keys); i++ {
			if keys[i] == keys[i-1] {
				t.Errorf("validKeys(%v) has duplicate %q", path, keys[i])
			}
		}
		t.Run(strings.Join(path, "."), func(t *testing.T) {
			for _, key := range keys {
				for _, typo := range typoVariants(key) {
					if typo == key || slices.Contains(keys, typo) || credentialKey(typo) {
						continue
					}
					got := Suggest(path, typo)
					if !slices.Contains(got, key) {
						t.Errorf("Suggest(%v, %q) = %v, want it to contain %q", path, typo, got, key)
					}
					if len(got) > maxSuggestions {
						t.Errorf("Suggest(%v, %q) = %v, more than %d results", path, typo, got, maxSuggestions)
					}
					seen := map[string]bool{}
					for _, g := range got {
						if seen[g] {
							t.Errorf("Suggest(%v, %q) = %v has a duplicate", path, typo, got)
						}
						seen[g] = true
					}
				}
			}
		})
	}
}
