package main

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

const (
	dbSrvID   = "55555555-5555-4555-8555-555555555555"
	dbPrefix  = cfgAPI + "/services/" + fakeSvcID
	dbReplic  = dbPrefix + "/database/replicas"
	dbReplica = cfgAPI + "/services/" + fakeSvc2ID
)

func dbServers(f *cfgFake) {
	f.reply("GET", cfgAPI+"/servers", 200, map[string]any{"items": []any{
		map[string]any{"id": dbSrvID, "name": "edge-1"},
	}})
}

// ---- replicas --------------------------------------------------------------

func TestReplicasAddDryRunPrintsThePlanAndSendsDryRun(t *testing.T) {
	f := newCfgFake(t)
	dbServers(f)
	f.reply("POST", dbReplic, 200, map[string]any{
		"dryRun": true,
		"plan": map[string]any{
			"serverId": dbSrvID, "serverName": "edge-1", "sameServer": false, "devicesAdded": 2,
			"blockers": []any{map[string]any{"code": "mesh_disabled", "message": "the mesh is off"}},
			"warnings": []any{map[string]any{"code": "lag", "message": "clone may take long"}},
		},
	})

	r := f.run("databases", "replicas", "add", fakeSvcID, "--server", "EDGE-1", "--dry-run", "--name", "pg-r1", "--idempotency-key", "k9")
	require.Equal(t, 0, r.code, r.all())
	require.Equal(t, "Read replica on edge-1 over the mesh (adds 2 device(s))\nDry run only; nothing changed.\n", r.stdout)
	require.Equal(t, "blocked: the mesh is off (mesh_disabled)\nwarning: clone may take long (lag)\n", r.err)
	post := f.only("POST", dbReplic)
	require.Equal(t, map[string]any{"serverId": dbSrvID, "dryRun": true, "acceptMeshChanges": false, "name": "pg-r1"}, post.json(t))
	require.Equal(t, "k9", post.header.Get("Idempotency-Key"))
	require.Equal(t, "application/json", post.header.Get("Content-Type"))
	require.Equal(t, "Bearer nct_test", post.header.Get("Authorization"))
}

func TestReplicasAddCreatesOnTheSameServerAndAnnouncesIt(t *testing.T) {
	f := newCfgFake(t)
	dbServers(f)
	f.reply("POST", dbReplic, 202, map[string]any{
		"dryRun": false, "plan": map[string]any{"sameServer": true}, "serviceId": fakeSvc2ID, "status": "provisioning",
	})

	r := f.run("databases", "replicas", "add", fakeSvcID, "--server", dbSrvID, "--accept-mesh-changes")
	require.Equal(t, 0, r.code, r.all())
	require.Equal(t, "Read replica on the primary's server\nReplica "+fakeSvc2ID+" is being created (provisioning). Follow it with: nebulactrl databases replicas ls "+fakeSvcID+"\n", r.stdout)
	body := f.only("POST", dbReplic).json(t)
	require.Equal(t, true, body["acceptMeshChanges"])
	require.Equal(t, false, body["dryRun"])
	require.NotContains(t, body, "name", "the name is only sent when given")

	// --json prints exactly what the server answered.
	r = f.run("--json", "databases", "replicas", "add", fakeSvcID, "--server", "edge-1")
	require.Equal(t, 0, r.code, r.all())
	require.Equal(t, fakeSvc2ID, decode[map[string]any](t, r.stdout)["serviceId"])
}

func TestReplicasAddFailures(t *testing.T) {
	f := newCfgFake(t)
	before := f.total()
	r := f.run("databases", "replicas", "add", fakeSvcID)
	require.Equal(t, 2, r.code, r.all())
	require.Contains(t, r.err, "--server is required")
	require.Equal(t, before, f.total())

	dbServers(f)
	r = f.run("databases", "replicas", "add", fakeSvcID, "--server", "nowhere")
	require.Equal(t, 1, r.code)
	require.Contains(t, r.err, `server "nowhere" not found`)

	f.problem("POST", dbReplic, 409, "replica_blocked", "the primary is not a PostgreSQL database")
	r = f.run("databases", "replicas", "add", fakeSvcID, "--server", "edge-1")
	require.Equal(t, 1, r.code)
	require.Contains(t, r.err, "the primary is not a PostgreSQL database (replica_blocked, HTTP 409)")

	// A 2xx answer that is not JSON is reported, not silently ignored.
	f.replyRaw("POST", dbReplic, cfgReply{200, "application/json", "<html>"})
	r = f.run("databases", "replicas", "add", fakeSvcID, "--server", "edge-1")
	require.Equal(t, 1, r.code)
	require.Contains(t, r.err, "decode response")
}

func TestReplicasLsShowsRoleFollowsAndLag(t *testing.T) {
	f := newCfgFake(t)
	path := dbPrefix + "/database/replication"
	f.reply("GET", path, 200, map[string]any{
		"role": "primary",
		"replicas": []any{
			map[string]any{"id": "r1", "slug": "pg-replica-1", "serverName": "edge-2", "state": "streaming", "lagBytes": 3 * 1024 * 1024, "replayLagMs": 250},
			map[string]any{"id": "r2", "slug": "pg-replica-2", "state": "starting", "lagBytes": 512, "replayLagMs": 0},
			map[string]any{"id": "r3", "slug": "pg-replica-3", "state": "failed", "message": "slot missing"},
		},
		"promotion": map[string]any{"id": "p1", "status": "running", "step": "freeze", "mode": "switchover", "message": "freezing"},
	})

	r := f.run("databases", "replicas", "ls", fakeSvcID)
	require.Equal(t, 0, r.code, r.all())
	require.True(t, strings.HasPrefix(r.stdout, "role: primary\n"), r.stdout)
	require.NotContains(t, r.stdout, "follows:")
	require.Equal(t, []string{"pg-replica-1", "edge-2", "streaming", "3.0", "MiB,", "250", "ms"}, bkLine(t, r.stdout, "pg-replica-1"))
	require.Equal(t, []string{"pg-replica-2", "-", "starting", "512", "B"}, bkLine(t, r.stdout, "pg-replica-2"),
		"a zero replay lag is not printed; a missing server is a dash")
	require.Equal(t, []string{"pg-replica-3", "-", "failed", "-", "slot", "missing"}, bkLine(t, r.stdout, "pg-replica-3"), "unknown lag is a dash")
	require.Contains(t, r.stdout, "promotion: switchover running (freeze) freezing\n")

	// A replica says who it follows and prints no table when it has no replicas.
	f.reply("GET", path, 200, map[string]any{"role": "replica", "primary": map[string]any{"id": fakeSvc2ID, "slug": "pg"}})
	r = f.run("databases", "replicas", "ls", fakeSvcID)
	require.Equal(t, 0, r.code, r.all())
	require.Equal(t, "role: replica\nfollows: pg\n", r.stdout)

	r = f.run("--json", "databases", "replicas", "ls", fakeSvcID)
	require.Equal(t, 0, r.code, r.all())
	require.Equal(t, "replica", decode[map[string]any](t, r.stdout)["role"])

	f.problem("GET", path, 404, "not_found", "not a database")
	r = f.run("databases", "replicas", "ls", fakeSvcID)
	require.Equal(t, 1, r.code)
	require.Contains(t, r.err, "not a database (not_found, HTTP 404)")
}

func TestReplicasRmNeedsYesAndDeletesTheReplica(t *testing.T) {
	f := newCfgFake(t)
	path := dbPrefix + "/database/replica"
	before := f.total()
	r := f.run("databases", "replicas", "rm", fakeSvcID)
	require.Equal(t, 2, r.code, r.all())
	require.Contains(t, r.err, "re-run with --yes")
	require.Equal(t, before, f.total(), "nothing is deleted without --yes")

	f.reply("DELETE", path, 202, map[string]any{"status": "removing"})
	r = f.run("databases", "replicas", "rm", fakeSvcID, "--yes")
	require.Equal(t, 0, r.code, r.all())
	require.Equal(t, "Removing replica "+fakeSvcID+".\n", r.stdout)
	del := f.only("DELETE", path)
	require.NotEmpty(t, del.header.Get("Idempotency-Key"))
	require.Empty(t, del.body)

	r = f.run("--json", "databases", "replicas", "rm", fakeSvcID, "--yes")
	require.Equal(t, 0, r.code, r.all())
	require.Equal(t, "removing", decode[map[string]any](t, r.stdout)["status"])

	f.problem("DELETE", path, 409, "not_a_replica", "this database is a primary")
	r = f.run("databases", "replicas", "rm", fakeSvcID, "--yes")
	require.Equal(t, 1, r.code)
	require.Contains(t, r.err, "this database is a primary (not_a_replica, HTTP 409)")
}

func TestDatabasesPromotePrintsThePlanUntilYes(t *testing.T) {
	f := newCfgFake(t)
	path := dbPrefix + "/database/promote"
	f.reply("POST", path, 200, map[string]any{
		"dryRun": true,
		"plan": map[string]any{
			"mode": "switchover", "lagBytes": 2048, "lagObservedAt": "2026-01-02T03:04:05Z",
			"replica": map[string]any{"slug": "pg-r1"}, "oldPrimary": map[string]any{"slug": "pg", "serverName": ""},
			"consumers": []any{map[string]any{
				"slug": "api", "serverName": "edge-1", "keys": []any{"DATABASE_URL", "PGHOST"}, "devicesAdded": 1,
				"warnings": []any{map[string]any{"code": "mesh", "message": "adds a device"}},
			}},
			"rebuildReplicas": []any{map[string]any{"slug": "pg-r2"}},
			"schedulesMoving": 2,
			"blockers":        []any{map[string]any{"code": "frozen", "message": "primary is frozen"}},
			"warnings":        []any{map[string]any{"code": "slow", "message": "slow disks"}},
		},
	})

	r := f.run("databases", "promote", fakeSvcID)
	require.Equal(t, 0, r.code, r.all())
	require.Equal(t, strings.Join([]string{
		"switchover: promote pg-r1 over pg (-)",
		"last observed lag: 2.0 KiB 2026-01-02T03:04:05Z",
		"consumer: api on edge-1, variables DATABASE_URL, PGHOST, adds 1 device(s)",
		"rebuild needed: pg-r2",
		"backup schedules moving: 2",
		"Run again with --yes to promote.",
		"",
	}, "\n"), r.stdout)
	require.Equal(t, strings.Join([]string{
		"  warning: adds a device (mesh)",
		"blocked: primary is frozen (frozen)",
		"warning: slow disks (slow)",
		"",
	}, "\n"), r.err)
	post := f.only("POST", path)
	require.Equal(t, map[string]any{"dryRun": true}, post.json(t))
	require.Empty(t, post.header.Get("Idempotency-Key"), "a plan is read-only: no idempotency key")

	r = f.run("--json", "databases", "promote", fakeSvcID)
	require.Equal(t, 0, r.code, r.all())
	require.Equal(t, true, decode[map[string]any](t, r.stdout)["dryRun"])
}

func TestDatabasesPromoteYesConfirmsWithTheServiceSlug(t *testing.T) {
	f := newCfgFake(t)
	path := dbPrefix + "/database/promote"
	f.reply("GET", cfgAPI+"/services/"+fakeSvcID, 200, map[string]any{"id": fakeSvcID, "slug": "pg-replica"})
	f.reply("POST", path, 202, map[string]any{"promotionId": "prom-1", "status": "running", "plan": map[string]any{"mode": "forced"}})

	r := f.run("databases", "promote", fakeSvcID, "--yes", "--accept-data-loss")
	require.Equal(t, 0, r.code, r.all())
	require.Equal(t, "Promotion prom-1 started (forced). Follow it with: nebulactrl databases replicas ls "+fakeSvcID+"\n", r.stdout)
	post := f.only("POST", path)
	require.Equal(t, map[string]any{"confirm": "pg-replica", "acceptDataLoss": true}, post.json(t))
	require.NotEmpty(t, post.header.Get("Idempotency-Key"))

	r = f.run("--json", "databases", "promote", fakeSvcID, "--yes")
	require.Equal(t, 0, r.code, r.all())
	require.Equal(t, false, f.calls("POST", path)[1].json(t)["acceptDataLoss"])
	require.Equal(t, "prom-1", decode[map[string]any](t, r.stdout)["promotionId"])

	f.problem("POST", path, 409, "data_loss_not_accepted", "pass --accept-data-loss")
	r = f.run("databases", "promote", fakeSvcID, "--yes")
	require.Equal(t, 1, r.code)
	require.Contains(t, r.err, "pass --accept-data-loss (data_loss_not_accepted, HTTP 409)")

	f.problem("GET", cfgAPI+"/services/"+fakeSvcID, 404, "not_found", "no such service")
	n := len(f.calls("POST", path))
	r = f.run("databases", "promote", fakeSvcID, "--yes")
	require.Equal(t, 1, r.code)
	require.Contains(t, r.err, "no such service")
	require.Len(t, f.calls("POST", path), n, "an unreadable service slug stops before the promotion is sent")
}

func TestDatabasesPromotePlanErrorSurfaces(t *testing.T) {
	f := newCfgFake(t)
	f.problem("POST", dbPrefix+"/database/promote", 422, "not_a_replica", "only a replica can be promoted")
	r := f.run("databases", "promote", fakeSvcID)
	require.Equal(t, 1, r.code)
	require.Contains(t, r.err, "only a replica can be promoted (not_a_replica, HTTP 422)")
}

func TestDatabasesRebuildResolvesTheFollowTarget(t *testing.T) {
	f := newCfgFake(t)
	path := dbPrefix + "/database/rebuild"
	f.reply("POST", path, 202, map[string]any{"status": "rebuilding"})

	r := f.run("databases", "rebuild", fakeSvcID)
	require.Equal(t, 0, r.code, r.all())
	require.Equal(t, "Rebuilding "+fakeSvcID+". Follow it with: nebulactrl databases replicas ls "+fakeSvcID+"\n", r.stdout)
	post := f.only("POST", path)
	require.Equal(t, map[string]any{}, post.json(t))
	require.NotEmpty(t, post.header.Get("Idempotency-Key"))

	r = f.run("--json", "databases", "rebuild", fakeSvcID, "--follow", fakeSvc2ID)
	require.Equal(t, 0, r.code, r.all())
	require.Equal(t, map[string]any{"followServiceId": fakeSvc2ID}, f.calls("POST", path)[1].json(t))
	require.Equal(t, "rebuilding", decode[map[string]any](t, r.stdout)["status"])

	r = f.run("databases", "rebuild", fakeSvcID, "--follow", "bad-ref")
	require.Equal(t, 1, r.code)
	require.Contains(t, r.err, "use a service ID or project/env/service-slug")
	require.Len(t, f.calls("POST", path), 2)

	f.problem("POST", path, 409, "not_rebuildable", "the replica is healthy")
	r = f.run("databases", "rebuild", fakeSvcID)
	require.Equal(t, 1, r.code)
	require.Contains(t, r.err, "the replica is healthy (not_rebuildable, HTTP 409)")
}

func TestLagAndByteText(t *testing.T) {
	i := func(n int64) *int64 { return &n }
	for _, tc := range []struct {
		n    int64
		want string
	}{{0, "0 B"}, {1023, "1023 B"}, {1024, "1.0 KiB"}, {1536, "1.5 KiB"}, {1 << 20, "1.0 MiB"}, {5 << 30, "5.0 GiB"}, {3 << 40, "3.0 TiB"}} {
		require.Equal(t, tc.want, byteText(tc.n), tc.n)
	}
	require.Equal(t, "-", lagText(replicaMemberOut{}))
	require.Equal(t, "2.0 KiB", lagText(replicaMemberOut{LagBytes: i(2048)}))
	require.Equal(t, "2.0 KiB", lagText(replicaMemberOut{LagBytes: i(2048), ReplayLagMs: i(0)}))
	require.Equal(t, "2.0 KiB, 40 ms", lagText(replicaMemberOut{LagBytes: i(2048), ReplayLagMs: i(40)}))
}

// ---- databases ---------------------------------------------------------------

func dbEngines() map[string]any {
	return map[string]any{"engines": []any{
		map[string]any{"id": "postgres", "label": "PostgreSQL", "license": "PostgreSQL", "licenseClass": "permissive",
			"versions": []any{map[string]any{"id": "18"}, map[string]any{"id": "17", "default": true}}},
		map[string]any{"id": "mongodb", "label": "MongoDB", "license": "SSPL", "licenseClass": "source-available"},
		map[string]any{"id": "redis", "label": "Redis", "license": "RSALv2", "licenseClass": "permissive", "optIn": true},
	}}
}

func TestDatabasesEnginesListsVersionsAndLicenceFlags(t *testing.T) {
	f := newCfgFake(t)
	f.reply("GET", cfgAPI+"/database-engines", 200, dbEngines())

	r := f.run("databases", "engines")
	require.Equal(t, 0, r.code, r.all())
	require.Equal(t, []string{"postgres", "PostgreSQL", "18,", "17*", "PostgreSQL"}, bkLine(t, r.stdout, "postgres"))
	require.Regexp(t, `mongodb\s+MongoDB\s+SSPL \(source-available: --accept-license\)`, r.stdout)
	require.Regexp(t, `redis\s+Redis\s+RSALv2 \(source-available: --accept-license\)`, r.stdout, "an opt-in engine needs the acknowledgement too")

	r = f.run("--json", "databases", "engines")
	require.Equal(t, 0, r.code, r.all())
	require.Len(t, decode[map[string]any](t, r.stdout)["engines"], 3)

	require.Equal(t, 2, f.run("databases", "engines", "extra").code)
}

func TestDatabasesCreateSendsTheChosenOptionsAndPrintsNextSteps(t *testing.T) {
	f := newCfgFake(t)
	dbServers(f)
	path := cfgAPI + "/environments/" + cfgProdID + "/databases"
	f.reply("POST", path, 201, map[string]any{"id": fakeSvcID, "slug": "main"})

	before := f.total()
	r := f.run("databases", "create", "--project", "acme-shop", "--env", "production")
	require.Equal(t, 2, r.code, r.all())
	require.Contains(t, r.err, "--project, --env and --engine are required")
	require.Equal(t, before, f.total())

	r = f.run("databases", "create", "--project", "Acme Shop", "--env", "Production", "--engine", "PostgreSQL",
		"--version", "17", "--name", "Main", "--flavor", "postgis", "--database", "shop", "--server", "edge-1", "--accept-license")
	require.Equal(t, 0, r.code, r.all())
	require.Equal(t, strings.Join([]string{
		"Created database acme-shop/production/main (" + fakeSvcID + ")",
		"Deploy it with: nebulactrl services deploy acme-shop/production/main --wait",
		"Connect another service with: nebulactrl vars set DATABASE_URL='${{main.DATABASE_URL}}' acme-shop/production/<service>",
		"",
	}, "\n"), r.stdout)
	post := f.only("POST", path)
	require.Equal(t, map[string]any{
		"engine": "postgresql", "name": "Main", "version": "17", "flavor": "postgis", "databaseName": "shop",
		"serverId": dbSrvID, "acceptLicense": true,
	}, post.json(t), "the engine is lower-cased; unset options are omitted")
	require.NotEmpty(t, post.header.Get("Idempotency-Key"))

	r = f.run("--json", "databases", "create", "--project", "acme-shop", "--env", "production", "--engine", "valkey")
	require.Equal(t, 0, r.code, r.all())
	require.Equal(t, map[string]any{"engine": "valkey"}, f.calls("POST", path)[1].json(t))
	require.Equal(t, "main", decode[map[string]any](t, r.stdout)["slug"])
}

func TestDatabasesCreateFailures(t *testing.T) {
	f := newCfgFake(t)
	path := cfgAPI + "/environments/" + cfgProdID + "/databases"
	r := f.run("databases", "create", "--project", "ghost", "--env", "production", "--engine", "postgres")
	require.Equal(t, 1, r.code)
	require.Contains(t, r.err, `project "ghost" not found`)
	r = f.run("databases", "create", "--project", "acme-shop", "--env", "qa", "--engine", "postgres")
	require.Equal(t, 1, r.code)
	require.Contains(t, r.err, `environment "qa" not found in this project`)
	dbServers(f)
	r = f.run("databases", "create", "--project", "acme-shop", "--env", "production", "--engine", "postgres", "--server", "ghost")
	require.Equal(t, 1, r.code)
	require.Contains(t, r.err, `server "ghost" not found`)
	require.Empty(t, f.calls("POST", path))

	f.problem("POST", path, 422, "license_not_accepted", "MongoDB needs --accept-license")
	r = f.run("databases", "create", "--project", "acme-shop", "--env", "production", "--engine", "mongodb")
	require.Equal(t, 1, r.code)
	require.Contains(t, r.err, "MongoDB needs --accept-license (license_not_accepted, HTTP 422)")
}

func dbView() map[string]any {
	return map[string]any{
		"serviceId": fakeSvcID, "image": "postgres:17", "engine": map[string]any{"id": "postgres", "label": "PostgreSQL"},
		"spec": map[string]any{"engine": "postgres", "version": "17"},
		"exports": []any{
			map[string]any{"key": "DATABASE_URL", "reference": "${{main.DATABASE_URL}}", "secret": true, "primary": true},
			map[string]any{"key": "PGPORT", "reference": "${{main.PGPORT}}", "secret": false},
		},
	}
}

func TestDatabasesInfoPrintsEngineStateAndHistory(t *testing.T) {
	f := newCfgFake(t)
	path := dbPrefix + "/database"
	view := dbView()
	view["info"] = map[string]any{"sizeBytes": 4096, "connections": 3, "updatedAt": "2026-06-15T12:00:00Z"}
	view["rotation"] = map[string]any{"status": "succeeded"}
	view["upgrade"] = map[string]any{"fromVersion": "16", "toVersion": "17", "status": "applied"}
	f.reply("GET", path, 200, view)

	r := f.run("databases", "info", fakeSvcID)
	require.Equal(t, 0, r.code, r.all())
	require.Contains(t, r.stdout, "PostgreSQL 17 (postgres:17)\n")
	require.Contains(t, r.stdout, "size 4096 bytes, 3 connection(s), reported 2026-06-15")
	require.Equal(t, []string{"DATABASE_URL", "${{main.DATABASE_URL}}", "true"}, bkLine(t, r.stdout, "DATABASE_URL"))
	require.Equal(t, []string{"PGPORT", "${{main.PGPORT}}", "false"}, bkLine(t, r.stdout, "PGPORT"))
	require.Contains(t, r.stdout, "last rotation: succeeded\n")
	require.Contains(t, r.stdout, "last upgrade: 16 → 17 (applied)\n")

	// Without reported info, rotation or upgrade only the engine line and the exports print.
	f.reply("GET", path, 200, dbView())
	r = f.run("databases", "info", fakeSvcID)
	require.Equal(t, 0, r.code, r.all())
	require.NotContains(t, r.stdout, "connection(s)")
	require.NotContains(t, r.stdout, "last rotation")
	require.NotContains(t, r.stdout, "last upgrade")

	r = f.run("--json", "databases", "info", fakeSvcID)
	require.Equal(t, 0, r.code, r.all())
	require.Equal(t, "postgres:17", decode[map[string]any](t, r.stdout)["image"])

	f.problem("GET", path, 404, "not_found", "not a managed database")
	r = f.run("databases", "info", fakeSvcID)
	require.Equal(t, 1, r.code)
	require.Contains(t, r.err, "not a managed database (not_found, HTTP 404)")
}

func TestDatabasesRevealPicksThePrimaryExportOrTheGivenKey(t *testing.T) {
	f := newCfgFake(t)
	reveal := dbPrefix + "/database/reveal"
	f.reply("GET", dbPrefix+"/database", 200, dbView())
	f.handle("POST", reveal, func(req cfgReq) cfgReply {
		return jsonReply(200, map[string]any{"key": req.json(t)["key"], "value": "postgres://u:p@h/db"})
	})

	r := f.run("databases", "reveal", fakeSvcID)
	require.Equal(t, 0, r.code, r.all())
	require.Equal(t, "postgres://u:p@h/db\n", r.stdout)
	require.Equal(t, map[string]any{"key": "DATABASE_URL"}, f.only("POST", reveal).json(t), "the primary export is the default")

	r = f.run("databases", "reveal", fakeSvcID, "--key", "PGPORT")
	require.Equal(t, 0, r.code, r.all())
	require.Equal(t, "PGPORT", f.calls("POST", reveal)[1].json(t)["key"])
	require.Len(t, f.calls("GET", dbPrefix+"/database"), 1, "an explicit --key needs no lookup")

	r = f.run("--json", "databases", "reveal", fakeSvcID, "--key", "PGPORT")
	require.Equal(t, 0, r.code, r.all())
	require.Equal(t, "PGPORT", decode[map[string]any](t, r.stdout)["key"])

	// No primary export and no --key: a usage error, nothing revealed.
	noPrimary := dbView()
	noPrimary["exports"] = []any{map[string]any{"key": "PGPORT"}}
	f.reply("GET", dbPrefix+"/database", 200, noPrimary)
	n := len(f.calls("POST", reveal))
	r = f.run("databases", "reveal", fakeSvcID)
	require.Equal(t, 2, r.code, r.all())
	require.Contains(t, r.err, "--key is required")
	require.Len(t, f.calls("POST", reveal), n)

	f.problem("POST", reveal, 403, "forbidden", "needs the deployments:write scope")
	r = f.run("databases", "reveal", fakeSvcID, "--key", "PGPORT")
	require.Equal(t, 1, r.code)
	require.Contains(t, r.err, "needs the deployments:write scope (forbidden, HTTP 403)")
}

func TestDatabasesRotateExplainsTheOutcomeOfEachStatus(t *testing.T) {
	f := newCfgFake(t)
	path := dbPrefix + "/database/rotate-credentials"
	plan := map[string]any{
		"mode": "online", "passwordKey": "PGPASSWORD",
		"consumers": []any{map[string]any{"slug": "api"}, map[string]any{"slug": "worker"}},
		"warnings":  []any{"consumers restart"},
	}
	for _, tc := range []struct{ status, want string }{
		{"planned", "Dry run only; nothing changed."},
		{"pending", "Rotation requested; the agent is changing the password. Then run: nebulactrl databases info " + fakeSvcID},
		{"applied", "Rotation applied. Deploy the staged changes to roll the new password out."},
	} {
		status, want := tc.status, tc.want
		f.reply("POST", path, 200, map[string]any{"status": status, "plan": plan})
		r := f.run("databases", "rotate", fakeSvcID)
		require.Equal(t, 0, r.code, r.all())
		require.Equal(t, "mode: online (password variable PGPASSWORD)\nconsumer: api\nconsumer: worker\n"+want+"\n", r.stdout, status)
		require.Equal(t, "warning: consumers restart\n", r.err)
	}
	last := f.calls("POST", path)
	require.Equal(t, map[string]any{"dryRun": false}, last[0].json(t))

	r := f.run("databases", "rotate", fakeSvcID, "--dry-run")
	require.Equal(t, 0, r.code, r.all())
	require.Equal(t, map[string]any{"dryRun": true}, f.calls("POST", path)[3].json(t))

	r = f.run("--json", "databases", "rotate", fakeSvcID)
	require.Equal(t, 0, r.code, r.all())
	require.Equal(t, "applied", decode[map[string]any](t, r.stdout)["status"])

	f.problem("POST", path, 409, "rotation_in_progress", "a rotation is already running")
	r = f.run("databases", "rotate", fakeSvcID)
	require.Equal(t, 1, r.code)
	require.Contains(t, r.err, "a rotation is already running (rotation_in_progress, HTTP 409)")
}

func TestDatabasesUpgradePlansThenAppliesWithYes(t *testing.T) {
	f := newCfgFake(t)
	plan := dbPrefix + "/database/upgrade/plan"
	apply := dbPrefix + "/database/upgrade"
	f.reply("POST", plan, 200, map[string]any{
		"engine": "postgres", "from": "16", "to": "17", "method": "pg_upgrade", "downtime": "a few minutes",
		"steps": []any{"stop", "convert"}, "warnings": []any{"take a backup"}, "blockers": []any{"disk too small"},
	})

	r := f.run("databases", "upgrade", fakeSvcID, "17")
	require.Equal(t, 0, r.code, r.all())
	require.Equal(t, "postgres 16 → 17: pg_upgrade (a few minutes)\n  - stop\n  - convert\nRun again with --yes to apply.\n", r.stdout)
	require.Equal(t, "warning: take a backup\nblocked: disk too small\n", r.err)
	require.Equal(t, map[string]any{"targetVersion": "17"}, f.only("POST", plan).json(t))
	require.Empty(t, f.calls("POST", apply), "a plan never applies")

	r = f.run("--json", "databases", "upgrade", fakeSvcID, "17")
	require.Equal(t, 0, r.code, r.all())
	require.Equal(t, "pg_upgrade", decode[map[string]any](t, r.stdout)["method"])

	// --yes confirms with the slug and distinguishes a started conversion from a staged image.
	f.reply("GET", cfgAPI+"/services/"+fakeSvcID, 200, map[string]any{"id": fakeSvcID, "slug": "main"})
	f.reply("POST", apply, 200, map[string]any{"status": "pending", "plan": map[string]any{}})
	r = f.run("databases", "upgrade", fakeSvcID, "17", "--yes")
	require.Equal(t, 0, r.code, r.all())
	require.Contains(t, r.stdout, "Upgrade started: the database is stopped while its data is converted.")
	require.Equal(t, map[string]any{"targetVersion": "17", "confirm": "main"}, f.only("POST", apply).json(t))

	f.reply("POST", apply, 200, map[string]any{"status": "staged", "plan": map[string]any{}})
	r = f.run("databases", "upgrade", fakeSvcID, "17", "--yes")
	require.Equal(t, 0, r.code, r.all())
	require.Equal(t, "Upgrade staged. Deploy the database to apply it.\n", r.stdout)

	r = f.run("--json", "databases", "upgrade", fakeSvcID, "17", "--yes")
	require.Equal(t, 0, r.code, r.all())
	require.Equal(t, "staged", decode[map[string]any](t, r.stdout)["status"])

	f.problem("POST", apply, 422, "upgrade_blocked", "the target version is older")
	r = f.run("databases", "upgrade", fakeSvcID, "15", "--yes")
	require.Equal(t, 1, r.code)
	require.Contains(t, r.err, "the target version is older (upgrade_blocked, HTTP 422)")

	f.problem("POST", plan, 422, "unknown_version", "no such version")
	r = f.run("databases", "upgrade", fakeSvcID, "99")
	require.Equal(t, 1, r.code)
	require.Contains(t, r.err, "no such version (unknown_version, HTTP 422)")
	require.Equal(t, 2, f.run("databases", "upgrade", fakeSvcID).code, "needs service and version")
}
