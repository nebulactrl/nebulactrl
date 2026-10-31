package composestack

import (
	"sort"
	"strings"
	"testing"

	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

func appStack(extra string) string {
	return "services:\n  app:\n    image: busybox\n    " + strings.ReplaceAll(extra, "\n", "\n    ") + "\n"
}

// The options that work on every server pass the allow-list as they are.
func TestTierSafeOptionsPassOnRestrictedServers(t *testing.T) {
	cases := map[string]string{
		"cap_drop":               "cap_drop: [ALL]",
		"cap_drop with add":      "cap_drop: [ALL]\ncap_add: [NET_BIND_SERVICE]",
		"no-new-privileges":      "security_opt: ['no-new-privileges:true']",
		"no-new-privileges bare": "security_opt: [no-new-privileges]",
		"read_only":              "read_only: true",
		"pids_limit":             "pids_limit: 200",
		"hostname":               "hostname: web",
		"dns":                    "dns: [1.1.1.1, 9.9.9.9]",
		"sysctls map":            "sysctls: {net.core.somaxconn: 1024, net.ipv4.ip_unprivileged_port_start: 0}",
		"sysctls list":           "sysctls: ['net.ipv4.tcp_fin_timeout=30', 'kernel.shmmax=268435456']",
		"user":                   "user: '1000:1000'",
		"group_add":              "group_add: ['44', video]",
	}
	for name, yml := range cases {
		t.Run(name, func(t *testing.T) {
			if vs := Check(mustParse(t, appStack(yml)), Policy{}); len(vs) != 0 {
				t.Fatalf("refused on a restricted server: %s", Summary(vs))
			}
		})
	}
}

// Everything that needs an unrestricted server is refused on a restricted one
// with that reason, and lifted by an unrestricted server (never unsupported).
func TestTierUnrestrictedOnlyOptions(t *testing.T) {
	cases := []struct{ name, yaml, key string }{
		{"privileged", "privileged: true", "privileged"},
		{"devices", "devices: ['/dev/dri:/dev/dri']", "devices"},
		{"gpus all", "gpus: all", "gpus"},
		{"gpus list", "gpus: [{driver: nvidia, count: 1}]", "gpus"},
		{"gpu reservation", "deploy:\n  resources:\n    reservations:\n      devices:\n        - {driver: nvidia, count: all, capabilities: [gpu]}", "deploy.resources.reservations.devices[0]"},
		{"runtime", "runtime: nvidia", "runtime"},
		{"pid host", "pid: host", "pid"},
		{"seccomp =", "security_opt: ['seccomp=unconfined']", "security_opt"},
		{"seccomp :", "security_opt: ['seccomp:unconfined']", "security_opt"},
		{"apparmor =", "security_opt: ['apparmor=unconfined']", "security_opt"},
		{"apparmor :", "security_opt: ['apparmor:unconfined']", "security_opt"},
		{"cap NET_ADMIN", "cap_add: [NET_ADMIN]", "cap_add"},
		{"cap CAP_SYS_ADMIN", "cap_add: [CAP_SYS_ADMIN]", "cap_add"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p := mustParse(t, appStack(tc.yaml))
			vs := Check(p, Policy{})
			if !hasViolation(vs, "app", tc.key) {
				t.Fatalf("not refused: %s", Summary(vs))
			}
			for _, v := range vs {
				if v.Kind != KindRestricted {
					t.Fatalf("expected a restricted violation, got %+v", v)
				}
				if !strings.Contains(v.Message, "unrestricted") {
					t.Fatalf("the reason does not name the unrestricted server: %q", v.Message)
				}
			}
			if un := Check(p, Policy{Unrestricted: true}); len(un) != 0 {
				t.Fatalf("an unrestricted server still refuses: %s", Summary(un))
			}
		})
	}
}

// The runtime runc is Docker's default, not an option.
func TestTierRuncRuntimeIsDefault(t *testing.T) {
	if vs := Check(mustParse(t, appStack("runtime: runc")), Policy{}); len(vs) != 0 {
		t.Fatalf("runc refused: %s", Summary(vs))
	}
}

// Only the curated sysctls pass; host-wide parameters never reach a restricted server.
func TestTierSysctls(t *testing.T) {
	for _, k := range agentprotocol.AllowedSysctls {
		if vs := Check(mustParse(t, appStack("sysctls: {"+k+": 1}")), Policy{}); len(vs) != 0 {
			t.Errorf("curated sysctl %s refused: %s", k, Summary(vs))
		}
	}
	for name, yml := range map[string]string{
		"vm map":       "sysctls: {vm.max_map_count: 262144}",
		"core pattern": "sysctls: ['kernel.core_pattern=|/bin/sh']",
		"mixed":        "sysctls: {net.core.somaxconn: 1024, net.core.rmem_max: 1000000}",
	} {
		t.Run(name, func(t *testing.T) {
			p := mustParse(t, appStack(yml))
			vs := Check(p, Policy{})
			if !hasViolation(vs, "app", "sysctls") {
				t.Fatalf("not refused: %s", Summary(vs))
			}
			if len(vs) != 1 {
				t.Fatalf("only the uncurated key is a violation: %s", Summary(vs))
			}
			if un := Check(p, Policy{Unrestricted: true}); len(un) != 0 {
				t.Fatalf("an unrestricted server still refuses: %s", Summary(un))
			}
		})
	}
}

// Other security options (profiles, labels) stay refused on a restricted server.
func TestTierOtherSecurityOptions(t *testing.T) {
	for _, o := range []string{"seccomp=/etc/profile.json", "apparmor=custom", "label=disable", "systempaths=unconfined"} {
		p := mustParse(t, appStack("security_opt: ['"+o+"']"))
		if !hasViolation(Check(p, Policy{}), "app", "security_opt") {
			t.Errorf("%s not refused", o)
		}
	}
}

// The stack allow-list and the typed Docker options must agree on what needs an
// unrestricted server: every field agentprotocol lists has a compose spelling
// that a restricted stack refuses.
func TestTierMatchesTypedDockerOptions(t *testing.T) {
	all := agentprotocol.DockerOptions{
		Devices:            []agentprotocol.DeviceMapping{{HostPath: "/dev/dri"}},
		GPUs:               &agentprotocol.GPURequest{Count: -1, Driver: "nvidia"},
		Runtime:            "nvidia",
		Privileged:         true,
		PidHost:            true,
		SeccompUnconfined:  true,
		ApparmorUnconfined: true,
		CapAdd:             []string{"NET_ADMIN", "SYS_ADMIN"},
	}
	compose := map[string]string{
		"devices":            "devices: ['/dev/dri:/dev/dri']",
		"gpus":               "gpus: all",
		"runtime":            "runtime: nvidia",
		"privileged":         "privileged: true",
		"pidHost":            "pid: host",
		"seccompUnconfined":  "security_opt: ['seccomp=unconfined']",
		"apparmorUnconfined": "security_opt: ['apparmor=unconfined']",
		"capAdd NET_ADMIN":   "cap_add: [NET_ADMIN]",
		"capAdd SYS_ADMIN":   "cap_add: [SYS_ADMIN]",
	}
	fields := all.UnrestrictedFields()
	sort.Strings(fields)
	var known []string
	for k := range compose {
		known = append(known, k)
	}
	sort.Strings(known)
	if strings.Join(fields, ",") != strings.Join(known, ",") {
		t.Fatalf("the typed options list %v, this test knows %v: add the compose spelling of a new field", fields, known)
	}
	for field, yml := range compose {
		vs := Check(mustParse(t, appStack(yml)), Policy{})
		if len(vs) == 0 {
			t.Errorf("%s (%s) is not refused on a restricted server", field, yml)
		}
		if un := Check(mustParse(t, appStack(yml)), Policy{Unrestricted: true}); len(un) != 0 {
			t.Errorf("%s still refused on an unrestricted server: %s", field, Summary(un))
		}
	}
}
