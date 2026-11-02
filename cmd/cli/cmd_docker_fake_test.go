package main

import (
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

const (
	dkSvc   = cfgAPI + "/services/" + fakeSvcID
	dkSrv   = cfgAPI + "/servers/" + dbSrvID + "/unrestricted"
	dkMaint = dkSvc + "/maintenance"
)

func dkService(kind string, docker map[string]any) map[string]any {
	cfg := map[string]any{"image": "traefik/whoami:latest", "port": 80}
	if docker != nil {
		cfg["docker"] = docker
	}
	return map[string]any{"id": fakeSvcID, "name": "web", "slug": "web", "kind": kind, "generation": 5, "config": cfg}
}

func TestServicesDockerWithoutFlagsPrintsTheOptionsSorted(t *testing.T) {
	f := newCfgFake(t)
	f.reply("GET", dkSvc, 200, dkService("git", map[string]any{"user": "1000:1000", "capDrop": []string{"ALL"}, "pidsLimit": 512}))

	r := f.run("services", "docker", fakeSvcID)
	require.Equal(t, 0, r.code, r.all())
	lines := strings.Split(strings.TrimSpace(r.stdout), "\n")
	require.Len(t, lines, 3)
	require.Equal(t, []string{"capDrop", `["ALL"]`}, strings.Fields(lines[0]))
	require.Equal(t, []string{"pidsLimit", "512"}, strings.Fields(lines[1]))
	require.Equal(t, []string{"user", `"1000:1000"`}, strings.Fields(lines[2]))
	require.Empty(t, f.calls("PATCH", dkSvc), "showing the options changes nothing")

	f.reply("GET", dkSvc, 200, dkService("git", nil))
	r = f.run("services", "docker", fakeSvcID)
	require.Equal(t, "web has no Docker options (its hostname is its slug).\n", r.stdout)
}

func TestServicesDockerGlobalFlagsAreNotChanges(t *testing.T) {
	f := newCfgFake(t)
	f.reply("GET", dkSvc, 200, dkService("git", map[string]any{"pidsLimit": 512}))

	r := f.run("--json", "services", "docker", fakeSvcID)
	require.Equal(t, 0, r.code, r.all())
	require.Equal(t, map[string]any{"pidsLimit": float64(512)}, decode[map[string]any](t, r.stdout))
	require.Empty(t, f.calls("PATCH", dkSvc), "--json only shows the options")
}

func TestServicesDockerWithFlagsPatchesTheConfigGuardedByTheGeneration(t *testing.T) {
	f := newCfgFake(t)
	f.reply("GET", dkSvc, 200, dkService("git", map[string]any{"hostname": "old", "capDrop": []string{"NET_RAW"}}))
	f.reply("PATCH", dkSvc, 200, map[string]any{"id": fakeSvcID, "name": "web"})

	r := f.run("services", "docker", fakeSvcID, "--cap-drop", "ALL", "--no-new-privileges", "--hostname", "default", "--pids-limit", "256")
	require.Equal(t, 0, r.code, r.all())
	require.Equal(t, "Updated the Docker options of web ("+fakeSvcID+"); staged until the next deployment\n", r.stdout)
	patch := f.only("PATCH", dkSvc)
	require.Equal(t, `"5"`, patch.header.Get("If-Match"))
	cfg := patch.json(t)["config"].(map[string]any)
	require.Equal(t, map[string]any{"capDrop": []any{"ALL"}, "noNewPrivileges": true, "pidsLimit": float64(256)}, cfg["docker"],
		"hostname default removed the old name; --cap-drop replaced the old list")
	require.Equal(t, "traefik/whoami:latest", cfg["image"], "the rest of the config round-trips")
	require.Equal(t, float64(80), cfg["port"])

	// --clear drops the docker section entirely.
	r = f.run("--json", "services", "docker", fakeSvcID, "--clear")
	require.Equal(t, 0, r.code, r.all())
	require.Equal(t, fakeSvcID, decode[map[string]any](t, r.stdout)["id"])
	cleared := f.calls("PATCH", dkSvc)[1].json(t)["config"].(map[string]any)
	require.NotContains(t, cleared, "docker")
	require.Equal(t, "traefik/whoami:latest", cleared["image"])
}

func TestServicesDockerRefusesWhatItCannotApply(t *testing.T) {
	f := newCfgFake(t)
	f.reply("GET", dkSvc, 200, dkService("stack", nil))
	r := f.run("services", "docker", fakeSvcID, "--read-only")
	require.Equal(t, 2, r.code, r.all())
	require.Contains(t, r.err, "is a Compose stack; its containers follow the compose file")
	require.Empty(t, f.calls("PATCH", dkSvc))

	f.reply("GET", dkSvc, 200, dkService("git", nil))
	r = f.run("services", "docker", fakeSvcID, "--sysctl", "novalue")
	require.Equal(t, 2, r.code, r.all())
	require.Contains(t, r.err, `--sysctl "novalue" must be key=value`)
	r = f.run("services", "docker", fakeSvcID, "--gpus", "lots")
	require.Equal(t, 2, r.code, r.all())
	require.Contains(t, r.err, "--gpus must be all, a number of GPUs or none")
	require.Empty(t, f.calls("PATCH", dkSvc), "malformed flags never reach the API")

	f.problem("PATCH", dkSvc, 422, "requires_unrestricted_server", "--privileged needs an unrestricted server")
	r = f.run("services", "docker", fakeSvcID, "--privileged")
	require.Equal(t, 1, r.code, r.all())
	require.Contains(t, r.err, "--privileged needs an unrestricted server (requires_unrestricted_server, HTTP 422)")

	f.problem("PATCH", dkSvc, 412, "precondition_failed", "the service changed")
	r = f.run("services", "docker", fakeSvcID, "--read-only")
	require.Equal(t, 1, r.code, r.all())
	require.Contains(t, r.err, "the service changed (precondition_failed, HTTP 412)")
}

func TestServersUnrestrictedShowsEnablesAndDisables(t *testing.T) {
	f := newCfgFake(t)
	dbServers(f)
	f.reply("GET", dkSrv, 200, map[string]any{"unrestricted": true, "stacks": 2, "dockerServices": 3})
	f.handle("PUT", dkSrv, func(req cfgReq) cfgReply {
		return jsonReply(200, map[string]any{"unrestricted": req.json(t)["unrestricted"]})
	})

	r := f.run("servers", "unrestricted", "EDGE-1")
	require.Equal(t, 0, r.code, r.all())
	require.Equal(t, "Server edge-1: unrestricted=true (2 stacks, 3 services use options that need it)\n", r.stdout)
	require.Empty(t, f.calls("PUT", dkSrv))

	r = f.run("--json", "servers", "unrestricted", dbSrvID)
	require.Equal(t, float64(3), decode[map[string]any](t, r.stdout)["dockerServices"])

	r = f.run("servers", "unrestricted", "edge-1", "--on", "--confirm", "edge-1")
	require.Equal(t, 0, r.code, r.all())
	require.Equal(t, "Server edge-1: unrestricted=true\n", r.stdout)
	require.Equal(t, map[string]any{"unrestricted": true, "confirm": "edge-1"}, f.only("PUT", dkSrv).json(t))

	r = f.run("servers", "unrestricted", "edge-1", "--off")
	require.Equal(t, 0, r.code, r.all())
	require.Equal(t, "Server edge-1: unrestricted=false\n", r.stdout)
	require.Equal(t, map[string]any{"unrestricted": false}, f.calls("PUT", dkSrv)[1].json(t), "disabling sends no confirmation")

	r = f.run("--json", "servers", "unrestricted", "edge-1", "--off")
	require.Equal(t, false, decode[map[string]any](t, r.stdout)["unrestricted"])
}

func TestServersUnrestrictedValidatesBeforeAskingTheServer(t *testing.T) {
	f := newCfgFake(t)
	dbServers(f)
	before := f.total()
	r := f.run("servers", "unrestricted", "edge-1", "--on", "--off")
	require.Equal(t, 2, r.code)
	require.Contains(t, r.err, "give --on or --off, not both")
	r = f.run("servers", "unrestricted", "edge-1", "--on")
	require.Equal(t, 2, r.code)
	require.Contains(t, r.err, "--on needs --confirm <server name>")
	require.Equal(t, before, f.total())

	r = f.run("servers", "unrestricted", "ghost")
	require.Equal(t, 1, r.code)
	require.Contains(t, r.err, `server "ghost" not found`)

	f.problem("PUT", dkSrv, 422, "confirm_mismatch", "the confirmation does not match the server name")
	r = f.run("servers", "unrestricted", "edge-1", "--on", "--confirm", "other")
	require.Equal(t, 1, r.code)
	require.Contains(t, r.err, "the confirmation does not match the server name (confirm_mismatch, HTTP 422)")

	f.problem("GET", dkSrv, 403, "forbidden", "instance admins only")
	r = f.run("servers", "unrestricted", "edge-1")
	require.Equal(t, 1, r.code)
	require.Contains(t, r.err, "instance admins only (forbidden, HTTP 403)")
}

func TestServicesMaintenanceTurnsTheModeOnWithPageTextAndETA(t *testing.T) {
	f := newCfgFake(t)
	eta := "2030-11-02T09:30:00Z"
	f.handle("PUT", dkMaint, func(req cfgReq) cfgReply {
		on := req.json(t)["enabled"] == true
		view := map[string]any{"enabled": on}
		if e, ok := req.json(t)["eta"]; ok {
			view["eta"] = e
		}
		return jsonReply(200, map[string]any{"id": fakeSvcID, "maintenance": view})
	})

	r := f.run("--idempotency-key", "m-key", "services", "maintenance", fakeSvcID, "ON", "--title", "Back soon", "--message", "Upgrading", "--eta", eta)
	require.Equal(t, 0, r.code, r.all())
	want, err := time.Parse(time.RFC3339, eta)
	require.NoError(t, err)
	require.Equal(t, "Maintenance mode is on, expected back "+want.Local().Format(time.RFC1123)+"\n", r.stdout)
	put := f.only("PUT", dkMaint)
	require.Equal(t, map[string]any{"enabled": true, "title": "Back soon", "message": "Upgrading", "eta": eta}, put.json(t))
	require.Equal(t, "m-key", put.header.Get("Idempotency-Key"))

	r = f.run("services", "maintenance", fakeSvcID, "on")
	require.Equal(t, 0, r.code, r.all())
	require.Equal(t, "Maintenance mode is on\n", r.stdout)
	require.Equal(t, map[string]any{"enabled": true}, f.calls("PUT", dkMaint)[1].json(t))

	r = f.run("services", "maintenance", fakeSvcID, "on", "--eta", "45m")
	require.Equal(t, 0, r.code, r.all())
	sent := f.calls("PUT", dkMaint)[2].json(t)["eta"].(string)
	got, err := time.Parse(time.RFC3339, sent)
	require.NoError(t, err)
	require.WithinDuration(t, time.Now().Add(45*time.Minute), got, time.Minute, "a duration counts from now")
}

func TestServicesMaintenanceOffAndValidation(t *testing.T) {
	f := newCfgFake(t)
	f.reply("PUT", dkMaint, 200, map[string]any{"id": fakeSvcID, "maintenance": map[string]any{"enabled": false}})

	r := f.run("services", "maintenance", fakeSvcID, "off")
	require.Equal(t, 0, r.code, r.all())
	require.Equal(t, "Maintenance mode is off\n", r.stdout)
	require.Equal(t, map[string]any{"enabled": false}, f.only("PUT", dkMaint).json(t))

	r = f.run("--json", "services", "maintenance", fakeSvcID, "off")
	require.Equal(t, false, decode[map[string]any](t, r.stdout)["enabled"])

	before := f.total()
	for args, want := range map[string]string{
		"maybe":             "the second argument is on or off",
		"off --title x":     "--title, --message and --eta only apply to `on`",
		"off --eta 1h":      "--title, --message and --eta only apply to `on`",
		"on --eta tomorrow": "is neither an RFC 3339 time nor a positive duration",
		"on --eta -5m":      "is neither an RFC 3339 time nor a positive duration",
	} {
		r = f.run(append([]string{"services", "maintenance", fakeSvcID}, strings.Fields(args)...)...)
		require.Equal(t, 2, r.code, "%s: %s", args, r.all())
		require.Contains(t, r.err, want, args)
	}
	require.Equal(t, before, f.total(), "invalid input never reaches the API")

	f.problem("PUT", dkMaint, 403, "forbidden", "needs proxy:write")
	r = f.run("services", "maintenance", fakeSvcID, "on")
	require.Equal(t, 1, r.code)
	require.Contains(t, r.err, "needs proxy:write (forbidden, HTTP 403)")
}
