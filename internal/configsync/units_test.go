package configsync

// Pure unit tests: no database, no network. Everything here runs on values
// built in the test (hand-built plans, parsed TOML snippets, sealed rows).

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/nebulactrl/nebulactrl/internal/auth"
	"github.com/nebulactrl/nebulactrl/internal/databases/engines"
	"github.com/nebulactrl/nebulactrl/internal/db"
	"github.com/nebulactrl/nebulactrl/internal/policy"
	"github.com/nebulactrl/nebulactrl/internal/projectconfig"
	"github.com/nebulactrl/nebulactrl/internal/secretbox"
	"github.com/nebulactrl/nebulactrl/internal/services"
	"github.com/nebulactrl/nebulactrl/internal/varexpr"
	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

// ---------------------------------------------------------------------------
// shared helpers (all prefixed unit)

func unitUUID(n int) uuid.UUID {
	return uuid.MustParse(fmt.Sprintf("00000000-0000-4000-8000-%012d", n))
}

var (
	unitWebID     = unitUUID(101)
	unitBuilderID = unitUUID(102)
	unitHubID     = unitUUID(103)
)

// unitPlanCtx is a plan context with one live service (web), one service that
// only exists in the file (queue) and a few named servers and registries.
func unitPlanCtx() *planCtx {
	return &planCtx{
		live: &liveEnv{
			Services: map[string]*liveService{"web": {Row: db.Service{ID: unitWebID, Slug: "web"}}},
			Refs: &refs{
				Servers:    []db.Server{{ID: unitBuilderID, Name: "Builder-1"}, {ID: unitUUID(104), Name: "Dup"}, {ID: unitUUID(105), Name: "dup"}},
				Registries: []db.Registry{{ID: unitHubID, Name: "Hub"}, {ID: unitUUID(106), Name: "Twin"}, {ID: unitUUID(107), Name: "twin"}},
			},
		},
		res: &projectconfig.Resolved{Services: map[string]*projectconfig.ResolvedService{
			"web":   {Slug: "web"},
			"queue": {Slug: "queue"},
		}},
	}
}

// unitResolve parses a nebula.toml snippet and resolves its base view.
func unitResolve(t *testing.T, text string) *projectconfig.Resolved {
	t.Helper()
	pr := parseFor(Input{File: text}, "")
	require.NotNil(t, pr.res, "diagnostics: %v", pr.diags)
	for slug, rs := range pr.res.Services {
		require.NotNil(t, rs.Def, "service %s has no definition; diagnostics: %v", slug, pr.diags)
	}
	return pr.res
}

// ---------------------------------------------------------------------------
// drift rule

func TestUnitClassifyTruthTable(t *testing.T) {
	const a, b, c = "sha256:a", "sha256:b", "sha256:c"
	for _, tc := range []struct {
		name    string
		base    string
		hasBase bool
		live    string
		present bool
		file    string
		want    string
	}{
		{"equal after an apply", a, true, b, true, b, StateInSync},
		{"equal and untouched since the apply", a, true, a, true, a, StateInSync},
		{"equal on a field never applied", "", false, b, true, b, StateInSync},
		{"absent live with a base", a, true, "", false, b, StateNew},
		{"absent live without a base", "", false, "", false, b, StateNew},
		{"absent live is new even when the stale live fingerprint equals the file", a, true, b, false, b, StateNew},
		{"never applied and different", "", false, a, true, b, StateAdopt},
		{"newer file waiting", a, true, a, true, b, StateFileChanged},
		{"edited in the dashboard", a, true, b, true, a, StateDrift},
		{"both changed", a, true, b, true, c, StateConflict},
	} {
		t.Run(tc.name, func(t *testing.T) {
			require.Equal(t, tc.want, classify(tc.base, tc.hasBase, tc.live, tc.present, tc.file))
		})
	}
}

func TestUnitClassifyMatchesTheSpecTableEverywhere(t *testing.T) {
	fps := []string{"sha256:a", "sha256:b", "sha256:c"}
	for _, base := range fps {
		for _, hasBase := range []bool{true, false} {
			for _, live := range fps {
				for _, present := range []bool{true, false} {
					for _, file := range fps {
						var want string
						switch {
						case !present:
							want = StateNew
						case live == file:
							want = StateInSync
						case !hasBase:
							want = StateAdopt
						case live == base && file != base:
							want = StateFileChanged
						case live != base && file == base:
							want = StateDrift
						case live != base && file != base:
							want = StateConflict
						default:
							t.Fatalf("unreachable row base=%s live=%s file=%s", base, live, file)
						}
						require.Equal(t, want, classify(base, hasBase, live, present, file),
							"base=%s hasBase=%v live=%s present=%v file=%s", base, hasBase, live, present, file)
					}
				}
			}
		}
	}
}

func TestUnitWorstState(t *testing.T) {
	chain := []string{StateInSync, StateFileChanged, StateDrift, StateConflict}
	for i, a := range chain {
		for j, b := range chain {
			require.Equal(t, chain[max(i, j)], worstState(a, b), "%s vs %s", a, b)
		}
	}
	// First-apply states are worse than in sync and better than a real divergence.
	for _, s := range []string{StateNew, StateAdopt} {
		require.Equal(t, s, worstState(StateInSync, s))
		require.Equal(t, s, worstState(s, StateInSync))
		require.Equal(t, StateDrift, worstState(s, StateDrift))
		require.Equal(t, StateConflict, worstState(StateConflict, s))
		require.Equal(t, StateFileChanged, worstState(s, StateFileChanged))
	}
	// An empty accumulator reads as in sync.
	require.Equal(t, StateInSync, worstState("", ""))
	require.Equal(t, StateInSync, worstState("", StateInSync))
	require.Equal(t, StateDrift, worstState("", StateDrift))
	require.Equal(t, StateDrift, worstState(StateDrift, ""))
}

func TestUnitServiceStateOf(t *testing.T) {
	for _, tc := range []struct {
		name   string
		fields []string
		want   string
	}{
		{"no fields", nil, StateInSync},
		{"empty list", []string{}, StateInSync},
		{"all in sync", []string{StateInSync, StateInSync}, StateInSync},
		{"a new field keeps its name", []string{StateNew}, StateNew},
		{"an adopted field keeps its name", []string{StateAdopt, StateInSync}, StateAdopt},
		{"adopt outranks new", []string{StateNew, StateAdopt}, StateAdopt},
		{"drift beats first-apply states", []string{StateNew, StateDrift}, StateDrift},
		{"file_changed beats adopt", []string{StateAdopt, StateFileChanged}, StateFileChanged},
		{"conflict beats everything", []string{StateFileChanged, StateConflict, StateDrift, StateNew}, StateConflict},
		{"drift beats in sync", []string{StateInSync, StateDrift, StateInSync}, StateDrift},
		{"unknown states do not count", []string{"", "bogus"}, StateInSync},
	} {
		t.Run(tc.name, func(t *testing.T) {
			require.Equal(t, tc.want, serviceStateOf(tc.fields))
		})
	}
}

func TestUnitPrunable(t *testing.T) {
	for id, want := range map[string]bool{
		"variables.NODE_ENV":                   true,
		"variables.NODE_ENV#secret":            false,
		"domains.shop.acme.com":                true,
		"domains.shop.acme.com/api":            true,
		"domains.shop.acme.com.cloudflare":     false,
		"domains.shop.acme.com/api.cloudflare": false,
		"volumes.data":                         true,
		"networks.shared":                      true,
		"links.db":                             true,
		"tasks.digest":                         true,
		"backups.nightly":                      true,
		"config.processes.worker":              true,
		"config.processes.mail-worker":         true,
		"config.processName":                   false,
		"config.singleton":                     false,
		"processes.worker.scale.replicas":      false,
		"uptime":                               true,
		"uptime.enabled":                       true,
		"uptime.reminderMinutes":               true,
		"config.port":                          false,
		"config.files":                         false,
		"config.docker.privileged":             false,
		"scale.replicas":                       false,
		"name":                                 false,
		"server":                               false,
		"git.link":                             false,
		"tasks":                                false,
		"uptimeish":                            false,
		"variablesX":                           false,
		"":                                     false,
	} {
		require.Equal(t, want, prunable(id), "id %q", id)
	}
}

func TestUnitReleasedFields(t *testing.T) {
	owned := map[string]string{"variables.B": "x", "config.port": "x", "backups.nightly": "x", "variables.A": "x"}
	present := func(ids ...string) func(string) bool {
		return func(id string) bool {
			for _, p := range ids {
				if p == id {
					return true
				}
			}
			return false
		}
	}
	require.Equal(t, []string{"backups.nightly", "variables.A"}, releasedFields(owned, present("config.port", "variables.B")))
	require.Empty(t, releasedFields(owned, present("config.port", "variables.B", "backups.nightly", "variables.A")))
	require.Equal(t, []string{"backups.nightly", "config.port", "variables.A", "variables.B"}, releasedFields(owned, present()))
	require.Empty(t, releasedFields(nil, present()))
	require.Empty(t, releasedFields(map[string]string{}, present("x")))
}

func TestUnitFieldPath(t *testing.T) {
	const base = "services.web"
	for _, tc := range []struct{ id, want string }{
		{"variables.NODE_ENV", "services.web.variables.NODE_ENV"},
		{"variables.NODE_ENV#secret", "services.web.variables.NODE_ENV"},
		{"volumes.data", "services.web.volumes.data"},
		{"tasks.digest", "services.web.tasks.digest"},
		{"backups.nightly", "services.web.backups.nightly"},
		{"networks.shared", "services.web.networks.shared"},
		{"links.db", "services.web.links.db"},
		{"domains.shop.acme.com", `services.web.domains."shop.acme.com"`},
		{"domains.shop.acme.com/api", `services.web.domains."shop.acme.com/api"`},
		{"config.processes.worker", "services.web.processes.worker"},
		{"config.processes.mail-worker", "services.web.processes.mail-worker"},
		{"processes.worker.scale.replicas", "services.web.processes.worker.scale.replicas"},
		{"processes.worker.scale.autoscaling.enabled", "services.web.processes.worker.scale.autoscaling.enabled"},
		{"processes.worker.scale.autoscaling.targetCpuPercent", "services.web.processes.worker.scale.autoscaling.target_cpu_percent"},
		{"config.processName", "services.web.process_name"},
		{"config.singleton", "services.web.singleton"},
	} {
		require.Equal(t, tc.want, fieldPath(base, tc.id), tc.id)
	}
}

func TestUnitOnlyMatches(t *testing.T) {
	for _, tc := range []struct {
		name    string
		only    []string
		path    string
		fieldID string
		want    bool
	}{
		{"no filter selects everything", nil, "services.web.port", "config.port", true},
		{"empty filter selects everything", []string{}, "x", "y", true},
		{"service prefix", []string{"services.web"}, "services.web.port", "config.port", true},
		{"the path itself", []string{"services.web"}, "services.web", "", true},
		{"a longer slug is another service", []string{"services.web"}, "services.webapp.port", "config.port", false},
		{"field id", []string{"config.port"}, "services.web.port", "config.port", true},
		{"field id is exact, not a prefix", []string{"config.por"}, "services.web.port", "config.port", false},
		{"a field id prefix does not select the group", []string{"config"}, "services.web.port", "config.port", false},
		{"quoted key below a table", []string{"services.web.domains"}, `services.web.domains."shop.acme.com"`, "domains.shop.acme.com", true},
		{"array element", []string{"services.web.route.upstreams"}, "services.web.route.upstreams[0].address", "config.route.upstreams", true},
		{"spaces around an entry are ignored", []string{"  services.web "}, "services.web.port", "config.port", true},
		{"blank entries never match", []string{"", "   "}, "services.web.port", "config.port", false},
		{"any entry may match", []string{"services.api", "services.web"}, "services.web.port", "config.port", true},
		{"a longer key is not selected by its prefix", []string{"services.web.port"}, "services.web.port_x", "x", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			require.Equal(t, tc.want, onlyMatches(tc.only, tc.path, tc.fieldID))
		})
	}
}

func TestUnitServiceSelected(t *testing.T) {
	rs := &projectconfig.ResolvedService{
		Slug: "web", Path: "services.web",
		Fields: map[string]projectconfig.FieldValue{"config.port": {ID: "config.port", Path: "services.web.port"}},
	}
	require.True(t, serviceSelected(nil, rs))
	require.True(t, serviceSelected([]string{"services.web"}, rs))
	require.True(t, serviceSelected([]string{"services.web.port"}, rs), "a key path of one of its fields")
	require.True(t, serviceSelected([]string{"config.port"}, rs), "a field id")
	require.False(t, serviceSelected([]string{"services.worker"}, rs))
	require.False(t, serviceSelected([]string{"services.webapp"}, rs))
	require.False(t, serviceSelected([]string{"config.memoryMb"}, rs))
}

func TestUnitAppliesOfRemoval(t *testing.T) {
	for id, want := range map[string]string{
		"tasks.digest":    AppliesImmediate,
		"backups.nightly": AppliesImmediate,
		"uptime":          AppliesImmediate,
		"uptime.enabled":  AppliesImmediate,
		"variables.OLD":   AppliesStaged,
		"domains.a.com":   AppliesStaged,
		"volumes.data":    AppliesStaged,
		"networks.n":      AppliesStaged,
		"links.db":        AppliesStaged,
	} {
		require.Equal(t, want, appliesOfRemoval(id), id)
	}
	require.Equal(t, AppliesStaged, appliesOfRemoval("config.processes.worker"), "a pruned process is a staged removal")
}

func TestUnitBlockerCodes(t *testing.T) {
	ds := Diagnostics{
		{Severity: projectconfig.SeverityError, Code: "unknown_server", Path: "services.web.server"},
		{Severity: projectconfig.SeverityError, Code: "invalid_value", Path: "services.web"},
		{Severity: projectconfig.SeverityError, Code: "unknown_server", Path: "services.web.server"},
		{Severity: projectconfig.SeverityWarning, Code: "a_warning", Path: "services.web.port"},
		{Severity: projectconfig.SeverityError, Code: "other_service", Path: "services.web2.port"},
		{Severity: projectconfig.SeverityError, Code: "no_path", Path: ""},
	}
	require.Equal(t, []string{"invalid_value", "unknown_server"}, blockerCodes(ds, "services.web"))
	require.Equal(t, []string{"other_service"}, blockerCodes(ds, "services.web2"))
	require.Empty(t, blockerCodes(ds, "services.worker"))
}

func TestUnitLiveKindOf(t *testing.T) {
	for _, tc := range []struct {
		row  db.Service
		want string
	}{
		{db.Service{Kind: services.KindDatabase, SourceType: services.SourceImage}, projectconfig.KindDatabase},
		{db.Service{Kind: services.KindRoute, SourceType: services.SourceExternal}, projectconfig.KindRoute},
		{db.Service{Kind: services.KindStack, SourceType: services.SourceCompose}, projectconfig.KindStack},
		{db.Service{Kind: services.KindApp, SourceType: services.SourceGit}, projectconfig.KindGit},
		{db.Service{Kind: services.KindWorker, SourceType: services.SourceGit}, projectconfig.KindGit},
		{db.Service{Kind: services.KindApp, SourceType: services.SourceImage}, projectconfig.KindImage},
		{db.Service{Kind: services.KindWorker, SourceType: services.SourceImage}, projectconfig.KindImage},
	} {
		require.Equal(t, tc.want, liveKindOf(tc.row), "%s/%s", tc.row.Kind, tc.row.SourceType)
	}
}

func TestUnitBackupKeysAndUnmanagedCounts(t *testing.T) {
	nightly, taken := "nightly", "schedule-1"
	ls := &liveService{
		Vars:    map[string]liveVar{"A": {}, "B": {}, "C": {}},
		Domains: []db.Domain{{Hostname: "a.com"}, {Hostname: "b.com", PathPrefix: "/api"}},
		Tasks:   map[string]db.ServiceTask{"t1": {}, "t2": {}},
		Volumes: []db.Volume{{Name: "data"}, {Name: "cache"}},
		Backups: []db.BackupSchedule{
			{ID: unitUUID(1), ConfigKey: &nightly},
			{ID: unitUUID(2)},
			{ID: unitUUID(3), ConfigKey: &taken},
			{ID: unitUUID(4)},
		},
	}
	keys := ls.backupKeys()
	require.Len(t, keys, 4)
	require.Equal(t, unitUUID(1), keys["nightly"].ID)
	require.Equal(t, unitUUID(3), keys["schedule-1"].ID, "an explicit key wins the synthesised name")
	require.Equal(t, unitUUID(2), keys["schedule-2"].ID, "schedules without a key take the next free names in order")
	require.Equal(t, unitUUID(4), keys["schedule-3"].ID)

	rs := &projectconfig.ResolvedService{Fields: map[string]projectconfig.FieldValue{
		"variables.A":        {},
		"variables.B#secret": {},
		"domains.a.com":      {},
		"tasks.t1":           {},
		"backups.nightly":    {},
		"backups.schedule-1": {},
		"backups.schedule-2": {},
		"volumes.data":       {},
		"config.port":        {},
	}}
	require.Equal(t, map[string]int{"variables": 1, "domains": 1, "tasks": 1, "backups": 1, "volumes": 1}, unmanagedCounts(ls, rs))

	for _, id := range []string{"variables.C", "domains.b.com/api", "tasks.t2", "backups.schedule-3", "volumes.cache"} {
		rs.Fields[id] = projectconfig.FieldValue{}
	}
	require.Nil(t, unmanagedCounts(ls, rs), "a file that mentions every member leaves nothing unmanaged")
}

func TestUnitFindDomain(t *testing.T) {
	ls := &liveService{Domains: []db.Domain{
		{ID: unitUUID(1), Hostname: "Shop.Acme.com"},
		{ID: unitUUID(2), Hostname: "shop.acme.com", PathPrefix: "/api"},
	}}
	d, ok := ls.findDomain("shop.acme.com", "")
	require.True(t, ok)
	require.Equal(t, unitUUID(1), d.ID)
	d, ok = ls.findDomain("SHOP.ACME.COM", "/api")
	require.True(t, ok)
	require.Equal(t, unitUUID(2), d.ID)
	_, ok = ls.findDomain("shop.acme.com", "/other")
	require.False(t, ok)
	_, ok = ls.findDomain("other.acme.com", "")
	require.False(t, ok)
}

func TestUnitAppliedByLabel(t *testing.T) {
	require.Equal(t, "nebula.toml @3f9c2a1", appliedByLabel(db.ConfigSource{Origin: OriginGit, CommitSha: "3f9c2a1b9d0e", AppliedByLabel: "system"}))
	require.Equal(t, "nebula.toml @abc", appliedByLabel(db.ConfigSource{Origin: OriginGit, CommitSha: "abc", AppliedByLabel: "system"}))
	require.Equal(t, "git push", appliedByLabel(db.ConfigSource{Origin: OriginGit, CommitSha: "", AppliedByLabel: "git push"}))
	require.Equal(t, "alice", appliedByLabel(db.ConfigSource{Origin: OriginCLI, CommitSha: "3f9c2a1b9d0e", AppliedByLabel: "alice"}))
}

// ---------------------------------------------------------------------------
// origin and actor

func TestUnitOriginOf(t *testing.T) {
	uid, tok := unitUUID(1), unitUUID(2)
	user := &auth.Principal{Type: auth.TypeUser, UserID: uid}
	mcp := &auth.Principal{Type: auth.TypeMCP, UserID: uid, TokenID: &tok}
	token := &auth.Principal{Type: auth.TypeToken, UserID: uid, TokenID: &tok}
	system := auth.System(unitUUID(3), unitUUID(4), "git push")
	odd := &auth.Principal{Type: "something-else"}

	for _, tc := range []struct {
		name string
		src  SourceInfo
		p    *auth.Principal
		want string
	}{
		{"explicit git", SourceInfo{Origin: OriginGit}, user, OriginGit},
		{"explicit cli", SourceInfo{Origin: OriginCLI}, system, OriginCLI},
		{"explicit api", SourceInfo{Origin: OriginAPI}, mcp, OriginAPI},
		{"explicit mcp", SourceInfo{Origin: OriginMCP}, user, OriginMCP},
		{"explicit ui", SourceInfo{Origin: OriginUI}, token, OriginUI},
		{"unknown explicit value is ignored", SourceInfo{Origin: "bogus"}, user, OriginUI},
		{"no principal", SourceInfo{}, nil, OriginAPI},
		{"system principal", SourceInfo{}, system, OriginGit},
		{"agent", SourceInfo{}, mcp, OriginMCP},
		{"browser user", SourceInfo{}, user, OriginUI},
		{"api token", SourceInfo{}, token, OriginAPI},
		{"unknown principal type", SourceInfo{}, odd, OriginAPI},
	} {
		t.Run(tc.name, func(t *testing.T) {
			require.Equal(t, tc.want, originOf(tc.src, tc.p))
		})
	}
}

func TestUnitActorOf(t *testing.T) {
	uid, tok := unitUUID(1), unitUUID(2)
	typ, id, label := actorOf(nil)
	require.Equal(t, []string{"system", "", "system"}, []string{typ, id, label})

	typ, id, label = actorOf(&auth.Principal{Type: auth.TypeUser, UserID: uid, Label: "Alice"})
	require.Equal(t, []string{"user", uid.String(), "Alice"}, []string{typ, id, label})

	typ, id, label = actorOf(&auth.Principal{Type: auth.TypeMCP, UserID: uid, TokenID: &tok, Label: "claude"})
	require.Equal(t, []string{"mcp", tok.String(), "claude"}, []string{typ, id, label}, "an agent is identified by its token")

	typ, id, label = actorOf(&auth.Principal{Type: auth.TypeToken, UserID: uid, TokenID: &tok, Label: "ci"})
	require.Equal(t, []string{"token", tok.String(), "ci"}, []string{typ, id, label})

	typ, id, label = actorOf(auth.System(unitUUID(3), unitUUID(4), "git push"))
	require.Equal(t, []string{"system", "", "git push"}, []string{typ, id, label})

	typ, id, label = actorOf(&auth.Principal{Type: "other", UserID: uid, Label: "x"})
	require.Equal(t, []string{"token", uid.String(), "x"}, []string{typ, id, label})
}

// ---------------------------------------------------------------------------
// errors

func TestUnitErrorConstructors(t *testing.T) {
	p := Plan{PlanID: "pl_0123", Valid: false, Services: []PlanService{{Slug: "web", Action: ActionBlocked}}}

	blocked := ErrConfigBlocked(p)
	require.Equal(t, http.StatusUnprocessableEntity, blocked.Status)
	require.Equal(t, "config_blocked", blocked.Code)
	require.Equal(t, CodeConfigBlocked, blocked.Code)
	require.Equal(t, p, blocked.Details["plan"], "the plan travels in details.plan")
	require.NotEmpty(t, blocked.Error())

	stale := ErrPlanStale(p)
	require.Equal(t, http.StatusConflict, stale.Status)
	require.Equal(t, "plan_stale", stale.Code)
	require.Equal(t, CodePlanStale, stale.Code)
	require.Equal(t, p, stale.Details["plan"])
	require.NotEmpty(t, stale.Error())

	missing := []string{"proxy:write", "admin", "deployments:write"}
	scope := ErrInsufficientScope(missing)
	require.Equal(t, http.StatusForbidden, scope.Status)
	require.Equal(t, "insufficient_scope", scope.Code)
	require.Equal(t, CodeInsufficientScope, scope.Code)
	require.Equal(t, []string{"admin", "deployments:write", "proxy:write"}, scope.Details["scopes"], "details.scopes is sorted")
	require.Equal(t, []string{"proxy:write", "admin", "deployments:write"}, missing, "the caller's slice is not reordered")
	require.NotEmpty(t, scope.Error())

	var asErr error = scope
	require.Error(t, asErr)
}

func TestUnitNewLastPlan(t *testing.T) {
	p := Plan{Services: []PlanService{
		{Slug: "web", Action: ActionUpdate, State: StateDrift, Changes: []PlanChange{
			{Field: "config.port", To: "8080", desiredFP: "sha256:port"},
			{Field: "variables.TOKEN", To: "super-secret-text", Masked: true, desiredFP: "sha256:token"},
		}},
		{Slug: "legacy", Action: ActionNotInFile, Changes: []PlanChange{}},
		{Slug: "fresh", Action: ActionCreate, State: StateNew},
	}}
	lp := NewLastPlan(p)
	require.Equal(t, LastPlan{Services: map[string]LastPlanService{
		"web":    {Action: ActionUpdate, State: StateDrift, Changes: 2, Fields: map[string]string{"config.port": "sha256:port", "variables.TOKEN": "sha256:token"}},
		"legacy": {Action: ActionNotInFile, Changes: 0, Fields: map[string]string{}},
		"fresh":  {Action: ActionCreate, State: StateNew, Changes: 0, Fields: map[string]string{}},
	}}, lp)

	raw, err := json.Marshal(lp)
	require.NoError(t, err)
	require.NotContains(t, string(raw), "super-secret-text", "the stored plan holds fingerprints, never values")
	require.NotContains(t, string(raw), "8080")
	require.NotContains(t, string(raw), "generations", "empty generations are omitted")

	empty := NewLastPlan(Plan{})
	require.NotNil(t, empty.Services)
	require.Empty(t, empty.Services)
}

// ---------------------------------------------------------------------------
// orderServices

func TestUnitOrderServices(t *testing.T) {
	t.Run("depends_on and variable references", func(t *testing.T) {
		res := unitResolve(t, `
schema = 1

[services.a-web]
image = "nginx"
depends_on = ["z-db", "ghost"]

[services.a-web.variables]
CACHE_URL = "${{m-cache.URL}}"
CACHE_AGAIN = "${{m-cache.OTHER}}"
SELF = "${{a-web.OTHER}}"
GLOBAL = "${{shared.NAME}}"
TEAMED = "${{team.KEY}}"

[services.m-cache]
image = "redis"

[services.z-db]
image = "postgres"
`)
		// sorted order is a-web, m-cache, z-db; a-web must follow both of its providers.
		require.Equal(t, []string{"m-cache", "z-db", "a-web"}, orderServices(res))
	})

	t.Run("route upstream edge", func(t *testing.T) {
		res := unitResolve(t, `
schema = 1
server = "fra-1"

[services.a-route.route]
protocol = "http"
upstreams = [{ service = "z-app", port = 3000 }]

[services.z-app]
image = "nginx"
port = 3000
`)
		require.Equal(t, []string{"z-app", "a-route"}, orderServices(res))
	})

	t.Run("transitive chain", func(t *testing.T) {
		res := unitResolve(t, `
schema = 1

[services.a]
image = "x"
depends_on = ["b"]

[services.b]
image = "x"
depends_on = ["c"]

[services.c]
image = "x"
`)
		require.Equal(t, []string{"c", "b", "a"}, orderServices(res))
	})

	t.Run("no edges keeps the sorted order", func(t *testing.T) {
		res := unitResolve(t, `
schema = 1

[services.b]
image = "x"

[services.a]
image = "x"
depends_on = ["ghost"]
`)
		require.Equal(t, []string{"a", "b"}, orderServices(res))
	})

	t.Run("a cycle falls back to the sorted order for what is left", func(t *testing.T) {
		res := unitResolve(t, `
schema = 1

[services.a]
image = "x"
depends_on = ["b"]

[services.b]
image = "x"
depends_on = ["a"]

[services.c]
image = "x"
`)
		got := orderServices(res)
		require.Equal(t, []string{"c", "a", "b"}, got, "the independent service is placed, the cycle follows in sorted order")
	})

	t.Run("every service appears exactly once", func(t *testing.T) {
		res := unitResolve(t, `
schema = 1

[services.a]
image = "x"
depends_on = ["b", "b"]

[services.a.variables]
V = "${{b.X}}${{b.Y}}"

[services.b]
image = "x"
`)
		require.Equal(t, []string{"b", "a"}, orderServices(res))
	})
}

// ---------------------------------------------------------------------------
// variables: templates, fingerprints, stored rows

func TestUnitTemplateSecret(t *testing.T) {
	for text, want := range map[string]bool{
		"${{secret(8)}}":            true,
		"${{secret(48)}}-suffix":    true,
		"${{randomBytes(16, hex)}}": true,
		"${{uuid()}}":               false,
		"${{randomInt(1, 5)}}":      false,
		"${{db.URL}}":               false,
		"literal":                   false,
		"":                          false,
	} {
		tpl, err := varexpr.Parse(text)
		require.NoError(t, err, text)
		require.Equal(t, want, templateSecret(tpl), text)
	}
}

func TestUnitDescribeTemplateNeverShowsLiteralsAroundGenerators(t *testing.T) {
	for _, tc := range []struct {
		text       string
		wantText   string
		wantMasked bool
		hidden     string
	}{
		{"production", "production", false, ""},
		{"", "", false, ""},
		{"${{db.URL}}", "${{db.URL}}", false, ""},
		{"postgres://${{db.HOST}}:5432", "postgres://${{db.HOST}}:5432", false, ""},
		{"${{secret(48)}}", "generated(secret(48))", true, ""},
		{"sk_live_abcdef${{secret(32)}}", "generated(secret(32))", true, "sk_live_abcdef"},
		{"${{secret(16)}}-${{uuid()}}", "generated(secret(16), uuid())", true, ""},
		{"${{randomBytes(16, hex)}}", "generated(randomBytes(16, hex))", true, ""},
		{`${{secret(10, "abcdefxyz")}}`, "generated(secret(10, 9-char alphabet))", true, "abcdefxyz"},
		{"keep-${{uuid()}}-me", "generated(uuid())", true, "keep-"},
		{"${{secret(", "${{secret(", false, ""}, // unparsable text is shown as it is: the validator rejects it
	} {
		got, masked := describeTemplate(tc.text)
		require.Equal(t, tc.wantText, got, tc.text)
		require.Equal(t, tc.wantMasked, masked, tc.text)
		if tc.hidden != "" {
			require.NotContains(t, got, tc.hidden)
		}
	}
}

func TestUnitVarFP(t *testing.T) {
	n, zero := 90, 0
	base := varFP("value", false, "runtime", "", nil, "")
	require.Regexp(t, `^sha256:[0-9a-f]{64}$`, base)

	require.Equal(t, base, varFP("value", false, "", "", nil, ""), "an empty scope is runtime")
	require.Equal(t, base, varFP("value", false, "runtime", "a comment", &n, ""), "metadata the file does not declare is not compared")

	for name, other := range map[string]string{
		"text":   varFP("other", false, "runtime", "", nil, ""),
		"secret": varFP("value", true, "runtime", "", nil, ""),
		"scope":  varFP("value", false, "build", "", nil, ""),
	} {
		require.NotEqual(t, base, other, name)
	}

	withComment := varFP("value", false, "runtime", "a", nil, "comment")
	require.NotEqual(t, base, withComment)
	require.NotEqual(t, withComment, varFP("value", false, "runtime", "b", nil, "comment"))
	require.Equal(t, withComment, varFP("value", false, "runtime", "a", &n, "comment"), "rotate is not part of a comment-only projection")

	rotNil := varFP("value", false, "runtime", "", nil, "rotate")
	rotZero := varFP("value", false, "runtime", "", &zero, "rotate")
	rotN := varFP("value", false, "runtime", "", &n, "rotate")
	require.NotEqual(t, rotNil, rotZero, "no reminder is not the same as a reminder of 0 days")
	require.NotEqual(t, rotZero, rotN)
	require.NotEqual(t, rotNil, rotN)
	require.Equal(t, rotN, varFP("value", false, "runtime", "any comment", &n, "rotate"))

	both := varFP("value", false, "runtime", "a", &n, "comment,rotate")
	require.NotEqual(t, withComment, both)
	require.NotEqual(t, rotN, both)

	require.NotContains(t, varFP("hunter2-planted", true, "runtime", "", nil, ""), "hunter2", "a fingerprint never reveals the text")
}

func TestUnitReferenceOnly(t *testing.T) {
	for text, want := range map[string]bool{
		"${{db.URL}}":                 true,
		"${{a.X}}${{b.Y}}":            true,
		"${{urlencode(db.PASSWORD)}}": true,
		"${{KEY}}":                    true,
		"plain":                       false,
		"":                            false,
		"x${{db.URL}}":                false,
		"${{db.URL}}x":                false,
		" ${{db.URL}}":                false, //nolint:gocritic // the leading space is deliberate: leading whitespace makes it not reference-only
		"${{a.X}}/${{b.Y}}":           false,
		"${{secret(8)}}":              false,
		"${{db.URL}":                  false,
		"$${{db.URL}}":                false,
	} {
		require.Equal(t, want, referenceOnly(text), "%q", text)
	}
}

func unitBox(t *testing.T) *secretbox.Box {
	t.Helper()
	box, err := secretbox.New(bytes.Repeat([]byte{0x42}, secretbox.KeySize))
	require.NoError(t, err)
	return box
}

func TestUnitDecodeVarNeverKeepsASecretValue(t *testing.T) {
	box := unitBox(t)
	rid := secretbox.RowID(unitUUID(5).String(), "KEY")
	vctx, tctx := secretbox.ServiceVariablesValue, secretbox.ServiceVariablesTemplate
	const planted = "hunter2-planted-secret-value"
	seal := func(col secretbox.Column, s string) []byte {
		t.Helper()
		b, err := box.SealString(col, rid, s)
		require.NoError(t, err)
		return b
	}
	rot := int32(90)

	for _, tc := range []struct {
		name         string
		value        string
		template     string
		secret       bool
		wantTemplate string
		wantOpaque   bool
		wantDisplay  string
		wantMasked   bool
	}{
		{"plain literal is its own template", "production", "", false, "production", false, "production", false},
		{"secret literal is opaque", planted, "", true, "", true, Mask, true},
		{"secret reference alone is its own template", "${{db.PASSWORD}}", "", true, "${{db.PASSWORD}}", false, "${{db.PASSWORD}}", false},
		{"secret reference with literal text is opaque", "pw-${{db.PASSWORD}}", "", true, "", true, Mask, true},
		{"generator template stored beside the generated value", planted, "${{secret(48)}}", true, "${{secret(48)}}", false, "generated(secret(48))", true},
		{"plain template stored beside its resolved value", "postgres://u:p@h/db", "${{db.URL}}", false, "${{db.URL}}", false, "${{db.URL}}", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var templateEnc []byte
			if tc.template != "" {
				templateEnc = seal(tctx, tc.template)
			}
			lv, err := decodeVar(box, rid, "KEY", seal(vctx, tc.value), templateEnc, tc.secret, "build", "a comment", &rot, "manual", vctx, tctx)
			require.NoError(t, err)
			require.Equal(t, "KEY", lv.Key)
			require.Equal(t, tc.secret, lv.Secret)
			require.Equal(t, "build", lv.Scope)
			require.Equal(t, "a comment", lv.Comment)
			require.Equal(t, "manual", lv.Source)
			require.NotNil(t, lv.Rotate)
			require.Equal(t, 90, *lv.Rotate)
			require.Equal(t, tc.wantTemplate, lv.Template)
			require.Equal(t, tc.wantOpaque, lv.Opaque)

			v := liveVarView(lv, "")
			require.Equal(t, tc.wantDisplay, v.Display)
			require.Equal(t, tc.wantMasked, v.Masked)
			require.Regexp(t, `^sha256:[0-9a-f]{64}$`, v.FP)
			if tc.secret {
				require.NotContains(t, fmt.Sprintf("%+v", lv), planted)
				require.NotContains(t, v.Display+v.FP, planted)
			}
		})
	}

	t.Run("no reminder stays nil", func(t *testing.T) {
		lv, err := decodeVar(box, rid, "KEY", seal(vctx, "v"), nil, false, "runtime", "", nil, "", vctx, tctx)
		require.NoError(t, err)
		require.Nil(t, lv.Rotate)
	})

	t.Run("a value sealed for another row does not open", func(t *testing.T) {
		_, err := decodeVar(box, secretbox.RowID(unitUUID(6).String(), "KEY"), "KEY", seal(vctx, planted), nil, true, "runtime", "", nil, "", vctx, tctx)
		require.ErrorContains(t, err, "decrypt variable KEY")
		require.NotContains(t, err.Error(), planted)
	})

	t.Run("a template sealed for another row does not open", func(t *testing.T) {
		other := secretbox.RowID(unitUUID(6).String(), "KEY")
		_, err := decodeVar(box, other, "KEY", seal(vctx, "v"), seal(tctx, "${{secret(48)}}"), true, "runtime", "", nil, "", vctx, tctx)
		require.ErrorContains(t, err, "decrypt template of KEY")
	})

	t.Run("an empty ciphertext is an error, not an empty value", func(t *testing.T) {
		_, err := decodeVar(box, rid, "KEY", nil, nil, false, "runtime", "", nil, "", vctx, tctx)
		require.ErrorContains(t, err, "decrypt variable KEY")
	})
}

func TestUnitVariableDesiredAndLiveViewsAgree(t *testing.T) {
	fv := func(key string, v projectconfig.Variable) projectconfig.FieldValue {
		return projectconfig.FieldValue{ID: "variables." + key, Path: "services.web.variables." + key, Value: v}
	}
	plain := &planCtx{}

	t.Run("plain literal", func(t *testing.T) {
		d, err := varOps{}.desired(plain, nil, fv("NODE_ENV", projectconfig.Variable{Value: "production"}))
		require.NoError(t, err)
		require.Equal(t, "production", d.Display)
		require.False(t, d.Masked)
		require.Empty(t, d.Proj)
		live := liveVarView(liveVar{Key: "NODE_ENV", Template: "production"}, d.Proj)
		require.Equal(t, d.FP, live.FP, "a stored scope of \"\" is runtime")
		require.Equal(t, d.Display, live.Display)

		build, err := varOps{}.desired(plain, nil, fv("NODE_ENV", projectconfig.Variable{Value: "production", Scope: "build"}))
		require.NoError(t, err)
		require.NotEqual(t, d.FP, build.FP)
		require.Equal(t, build.FP, liveVarView(liveVar{Key: "NODE_ENV", Template: "production", Scope: "build"}, "").FP)
	})

	t.Run("generator", func(t *testing.T) {
		d, err := varOps{}.desired(plain, nil, fv("SESSION", projectconfig.Variable{Value: "${{secret(48)}}"}))
		require.NoError(t, err)
		require.Equal(t, "generated(secret(48))", d.Display)
		require.True(t, d.Masked)
		require.Equal(t, varFP("${{secret(48)}}", true, "runtime", "", nil, ""), d.FP, "a secret-class generator makes the variable secret")
		live := liveVarView(liveVar{Key: "SESSION", Secret: true, Template: "${{secret(48)}}"}, d.Proj)
		require.Equal(t, d.FP, live.FP)
		require.Equal(t, d.Display, live.Display)
		require.True(t, live.Masked)

		id, err := varOps{}.desired(plain, nil, fv("REQUEST_ID", projectconfig.Variable{Value: "${{uuid()}}"}))
		require.NoError(t, err)
		require.Equal(t, varFP("${{uuid()}}", false, "runtime", "", nil, ""), id.FP, "uuid() is not secret-class")
		require.Equal(t, "generated(uuid())", id.Display)
	})

	t.Run("declared secret name", func(t *testing.T) {
		declared := &planCtx{rs: &projectconfig.ResolvedService{Fields: map[string]projectconfig.FieldValue{"variables.DB_URL#secret": {}}}}
		d, err := varOps{}.desired(declared, nil, fv("DB_URL", projectconfig.Variable{Value: "${{db.URL}}"}))
		require.NoError(t, err)
		require.Equal(t, varFP("${{db.URL}}", true, "runtime", "", nil, ""), d.FP)
		notDeclared, err := varOps{}.desired(plain, nil, fv("DB_URL", projectconfig.Variable{Value: "${{db.URL}}"}))
		require.NoError(t, err)
		require.NotEqual(t, notDeclared.FP, d.FP)

		// The same declaration holds when the service exists but does not have the variable yet.
		ls := &liveService{Vars: map[string]liveVar{"OTHER": {}}}
		d2, err := varOps{}.desired(declared, ls, fv("DB_URL", projectconfig.Variable{Value: "${{db.URL}}"}))
		require.NoError(t, err)
		require.Equal(t, d.FP, d2.FP)
	})

	t.Run("writing a value never unsets a stored secret", func(t *testing.T) {
		ls := &liveService{Vars: map[string]liveVar{"DB_URL": {Secret: true}}}
		d, err := varOps{}.desired(plain, ls, fv("DB_URL", projectconfig.Variable{Value: "${{db.URL}}"}))
		require.NoError(t, err)
		require.Equal(t, varFP("${{db.URL}}", true, "runtime", "", nil, ""), d.FP)
	})

	t.Run("declared metadata is compared, undeclared is not", func(t *testing.T) {
		days := 30
		d, err := varOps{}.desired(plain, nil, fv("KEY", projectconfig.Variable{Value: "x", Comment: "note", RotateEveryDays: &days}))
		require.NoError(t, err)
		require.Equal(t, "comment,rotate", d.Proj)
		require.Equal(t, varFP("x", false, "runtime", "note", &days, "comment,rotate"), d.FP)

		same := liveVar{Key: "KEY", Template: "x", Comment: "note", Rotate: &days}
		require.Equal(t, d.FP, liveVarView(same, d.Proj).FP)
		other := liveVar{Key: "KEY", Template: "x", Comment: "another note", Rotate: &days}
		require.NotEqual(t, d.FP, liveVarView(other, d.Proj).FP)

		// A file that declares only the value ignores the live comment.
		bare, err := varOps{}.desired(plain, nil, fv("KEY", projectconfig.Variable{Value: "x"}))
		require.NoError(t, err)
		require.Empty(t, bare.Proj)
		require.Equal(t, bare.FP, liveVarView(other, bare.Proj).FP)
	})

	t.Run("errors", func(t *testing.T) {
		var re *refError
		_, err := varOps{}.desired(plain, nil, fv("KEY", projectconfig.Variable{Value: "${{secret("}))
		require.True(t, errors.As(err, &re), "%v", err)
		require.Equal(t, "invalid_template", re.Code)

		_, err = varOps{}.desired(plain, nil, projectconfig.FieldValue{ID: "variables.KEY", Value: "not a variable"})
		require.True(t, errors.As(err, &re), "%v", err)
		require.Equal(t, "wrong_type", re.Code)
	})

	t.Run("secret declaration", func(t *testing.T) {
		d, err := varOps{}.desired(plain, nil, projectconfig.FieldValue{ID: "variables.K#secret", Value: projectconfig.SecretDecl{Name: "K"}})
		require.NoError(t, err)
		require.Equal(t, "secret", d.Display)
		require.Equal(t, projectconfig.Fingerprint(map[string]any{"secret": true, "scope": "runtime"}), d.FP)

		ls := &liveService{Vars: map[string]liveVar{"K": {Secret: true}, "PLAIN": {Secret: false}, "B": {Secret: true, Scope: "build"}}}
		live, ok := varOps{}.live(plain, ls, "variables.K#secret", "")
		require.True(t, ok)
		require.Equal(t, d.FP, live.FP)
		require.Equal(t, "secret", live.Display)

		notSecret, ok := varOps{}.live(plain, ls, "variables.PLAIN#secret", "")
		require.True(t, ok)
		require.Equal(t, "not secret", notSecret.Display)
		require.NotEqual(t, d.FP, notSecret.FP)

		build, err := varOps{}.desired(plain, nil, projectconfig.FieldValue{ID: "variables.B#secret", Value: projectconfig.SecretDecl{Name: "B", Scope: "build"}})
		require.NoError(t, err)
		liveBuild, ok := varOps{}.live(plain, ls, "variables.B#secret", "")
		require.True(t, ok)
		require.Equal(t, build.FP, liveBuild.FP)

		_, ok = varOps{}.live(plain, ls, "variables.MISSING#secret", "")
		require.False(t, ok)
		_, ok = varOps{}.live(plain, ls, "variables.MISSING", "")
		require.False(t, ok)
	})
}

func TestUnitLiveVarViewMasking(t *testing.T) {
	for _, tc := range []struct {
		name        string
		lv          liveVar
		wantDisplay string
		wantMasked  bool
	}{
		{"opaque secret", liveVar{Secret: true, Opaque: true}, Mask, true},
		{"opaque wins over a template", liveVar{Secret: true, Opaque: true, Template: "leaked-text"}, Mask, true},
		{"secret generator template", liveVar{Secret: true, Template: "${{secret(32)}}"}, "generated(secret(32))", true},
		{"secret reference", liveVar{Secret: true, Template: "${{db.PASSWORD}}"}, "${{db.PASSWORD}}", false},
		{"secret without any template text", liveVar{Secret: true, Template: ""}, "", true},
		{"plain value", liveVar{Template: "production"}, "production", false},
		{"plain generator", liveVar{Template: "${{uuid()}}"}, "generated(uuid())", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			v := liveVarView(tc.lv, "")
			require.Equal(t, tc.wantDisplay, v.Display)
			require.Equal(t, tc.wantMasked, v.Masked)
		})
	}
}

// ---------------------------------------------------------------------------
// display / as* / convert helpers

func TestUnitDisplay(t *testing.T) {
	ch := make(chan int)
	for _, tc := range []struct {
		name string
		v    any
		want string
	}{
		{"nil", nil, ""},
		{"string", "x y", "x y"},
		{"true", true, "true"},
		{"false", false, "false"},
		{"int", 42, "42"},
		{"negative int64", int64(-7), "-7"},
		{"int32 goes through json", int32(5), "5"},
		{"fraction", 1.5, "1.5"},
		{"whole float has no fraction", 2.0, "2"},
		{"string list", []string{"a", "b"}, `["a","b"]`},
		{"empty string list", []string{}, `[]`},
		{"map", map[string]int{"a": 1}, `{"a":1}`},
		{"struct", projectconfig.Volume{Name: "d", MountPath: "/d"}, `{"name":"d","mountPath":"/d"}`},
		{"what json cannot encode", ch, fmt.Sprint(ch)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			require.Equal(t, tc.want, display(tc.v))
		})
	}
}

func TestUnitAsHelpers(t *testing.T) {
	s := "abc"
	var nilStr *string
	require.Equal(t, "x", asString("x"))
	require.Equal(t, "abc", asString(&s))
	require.Equal(t, "", asString(nilStr))
	require.Equal(t, "", asString(nil))
	require.Equal(t, "", asString(5))
	require.Equal(t, unitUUID(9).String(), asString(unitUUID(9)), "a Stringer is read through String()")

	n := 7
	var nilInt *int
	for _, tc := range []struct {
		v    any
		want int
		ok   bool
	}{
		{5, 5, true}, {int32(6), 6, true}, {int64(7), 7, true}, {8.0, 8, true}, {&n, 7, true},
		{nilInt, 0, false}, {"5", 0, false}, {nil, 0, false}, {true, 0, false},
	} {
		got, ok := asInt(tc.v)
		require.Equal(t, tc.ok, ok, "%#v", tc.v)
		require.Equal(t, tc.want, got, "%#v", tc.v)
	}

	bt := true
	var nilBool *bool
	for _, tc := range []struct {
		v    any
		want bool
		ok   bool
	}{
		{true, true, true}, {false, false, true}, {&bt, true, true}, {nilBool, false, false}, {"true", false, false}, {1, false, false}, {nil, false, false},
	} {
		got, ok := asBool(tc.v)
		require.Equal(t, tc.ok, ok, "%#v", tc.v)
		require.Equal(t, tc.want, got, "%#v", tc.v)
	}

	f := 2.5
	var nilFloat *float64
	for _, tc := range []struct {
		v    any
		want float64
		ok   bool
	}{
		{1.5, 1.5, true}, {float32(0.5), 0.5, true}, {3, 3, true}, {&f, 2.5, true}, {nilFloat, 0, false}, {"1.5", 0, false}, {nil, 0, false},
	} {
		got, ok := asFloat(tc.v)
		require.Equal(t, tc.ok, ok, "%#v", tc.v)
		require.Equal(t, tc.want, got, "%#v", tc.v)
	}

	got, ok := asStrings([]string{"a"})
	require.True(t, ok)
	require.Equal(t, []string{"a"}, got)
	got, ok = asStrings(nil)
	require.True(t, ok, "nil is an empty list")
	require.Nil(t, got)
	_, ok = asStrings("a")
	require.False(t, ok)
	_, ok = asStrings([]any{"a"})
	require.False(t, ok)

	require.Equal(t, []string{}, nonNil(nil))
	require.NotNil(t, nonNil(nil))
	in := []string{"a"}
	require.Equal(t, in, nonNil(in))
}

func TestUnitConvert(t *testing.T) {
	i, ok := convert[int](int32(5))
	require.True(t, ok)
	require.Equal(t, 5, i)
	i, ok = convert[int](7.0)
	require.True(t, ok)
	require.Equal(t, 7, i)
	i, ok = convert[int](new(9))
	require.True(t, ok)
	require.Equal(t, 9, i)
	_, ok = convert[int]("5")
	require.False(t, ok)
	_, ok = convert[int](nil)
	require.False(t, ok)

	s, ok := convert[string]("x")
	require.True(t, ok)
	require.Equal(t, "x", s)
	_, ok = convert[string](5)
	require.False(t, ok)
	_, ok = convert[string](nil)
	require.False(t, ok)

	b, ok := convert[bool](true)
	require.True(t, ok)
	require.True(t, b)
	b, ok = convert[bool](new(false))
	require.True(t, ok)
	require.False(t, b)
	_, ok = convert[bool]("true")
	require.False(t, ok)

	f, ok := convert[float64](2)
	require.True(t, ok)
	require.Equal(t, 2.0, f)
	f, ok = convert[float64](float32(1.5))
	require.True(t, ok)
	require.Equal(t, 1.5, f)
	_, ok = convert[float64]("1.5")
	require.False(t, ok)

	src := []string{"a", "b"}
	l, ok := convert[[]string](src)
	require.True(t, ok)
	require.Equal(t, []string{"a", "b"}, l)
	l[0] = "changed"
	require.Equal(t, "a", src[0], "the converted list is a copy")
	l, ok = convert[[]string](nil)
	require.True(t, ok)
	require.Empty(t, l)
	_, ok = convert[[]string]([]any{"a"})
	require.False(t, ok)

	_, ok = convert[int64](5)
	require.False(t, ok, "only the five scalar kinds of Config are converted")
}

// ---------------------------------------------------------------------------
// refs name lookups

func TestUnitRefsNameLookups(t *testing.T) {
	alpha, dup1, dup2 := unitUUID(11), unitUUID(12), unitUUID(13)
	r := &refs{
		Servers:      []db.Server{{ID: alpha, Name: "Alpha"}, {ID: dup1, Name: "Dup"}, {ID: dup2, Name: "dup"}},
		Connections:  []db.GitConnection{{ID: alpha, Name: "Alpha"}, {ID: dup1, Name: "Dup"}, {ID: dup2, Name: "dup"}},
		Registries:   []db.Registry{{ID: alpha, Name: "Alpha"}, {ID: dup1, Name: "Dup"}, {ID: dup2, Name: "dup"}},
		Destinations: []db.BackupDestination{{ID: alpha, Name: "Alpha"}, {ID: dup1, Name: "Dup"}, {ID: dup2, Name: "dup"}},
		Networks:     []db.SharedNetwork{{ID: alpha, Name: "Alpha"}, {ID: dup1, Name: "Dup"}, {ID: dup2, Name: "dup"}},
	}
	lookups := map[string]func(string) (uuid.UUID, int){
		"server":      func(n string) (uuid.UUID, int) { x, res := r.server(n); return x.ID, res },
		"connection":  func(n string) (uuid.UUID, int) { x, res := r.connection(n); return x.ID, res },
		"registry":    func(n string) (uuid.UUID, int) { x, res := r.registry(n); return x.ID, res },
		"destination": func(n string) (uuid.UUID, int) { x, res := r.destination(n); return x.ID, res },
		"network":     func(n string) (uuid.UUID, int) { x, res := r.network(n); return x.ID, res },
	}
	for kind, lookup := range lookups {
		t.Run(kind, func(t *testing.T) {
			for _, name := range []string{"Alpha", "alpha", "ALPHA"} {
				id, res := lookup(name)
				require.Equal(t, refOK, res, name)
				require.Equal(t, alpha, id, name)
			}
			for _, name := range []string{"Dup", "dup", "DUP"} {
				id, res := lookup(name)
				require.Equal(t, refAmbiguous, res, name)
				require.Equal(t, uuid.Nil, id, "an ambiguous name resolves to nothing: %s", name)
			}
			for _, name := range []string{"beta", "", "alph", "alpha "} {
				id, res := lookup(name)
				require.Equal(t, refNone, res, "%q", name)
				require.Equal(t, uuid.Nil, id)
			}
		})
	}

	srv, ok := r.serverByID(dup2)
	require.True(t, ok)
	require.Equal(t, "dup", srv.Name)
	_, ok = r.serverByID(unitUUID(99))
	require.False(t, ok)
	_, ok = (&refs{}).serverByID(dup1)
	require.False(t, ok)
	require.Equal(t, refNone, func() int { _, res := (&refs{}).server("x"); return res }(), "no servers at all")

	for _, ok := range []bool{
		func() bool { _, ok := r.connectionByID(alpha); return ok }(),
		func() bool { _, ok := r.registryByID(alpha); return ok }(),
		func() bool { _, ok := r.destinationByID(alpha); return ok }(),
	} {
		require.True(t, ok)
	}
	for _, ok := range []bool{
		func() bool { _, ok := r.connectionByID(unitUUID(99)); return ok }(),
		func() bool { _, ok := r.registryByID(unitUUID(99)); return ok }(),
		func() bool { _, ok := r.destinationByID(unitUUID(99)); return ok }(),
	} {
		require.False(t, ok)
	}
}

func TestUnitRefsCertificatesAreScopedToTheServer(t *testing.T) {
	s1, s2 := unitUUID(21), unitUUID(22)
	r := &refs{Certs: []db.Certificate{
		{ID: unitUUID(31), ServerID: s1, Name: "Wild"},
		{ID: unitUUID(32), ServerID: s2, Name: "wild"},
		{ID: unitUUID(33), ServerID: s1, Name: "Other"},
		{ID: unitUUID(34), ServerID: s2, Name: "Twin"},
		{ID: unitUUID(35), ServerID: s2, Name: "twin"},
	}}
	c, res := r.certificate("WILD", s1)
	require.Equal(t, refOK, res)
	require.Equal(t, unitUUID(31), c.ID)
	c, res = r.certificate("wild", s2)
	require.Equal(t, refOK, res)
	require.Equal(t, unitUUID(32), c.ID)
	_, res = r.certificate("other", s2)
	require.Equal(t, refNone, res, "the certificate lives on the other server")
	_, res = r.certificate("twin", s2)
	require.Equal(t, refAmbiguous, res)
	_, res = r.certificate("twin", s1)
	require.Equal(t, refNone, res)
	_, res = r.certificate("wild", unitUUID(99))
	require.Equal(t, refNone, res)

	got, ok := r.certificateByID(unitUUID(33))
	require.True(t, ok)
	require.Equal(t, "Other", got.Name)
	_, ok = r.certificateByID(unitUUID(99))
	require.False(t, ok)
}

// ---------------------------------------------------------------------------
// the config field table

type unitCfgSample struct {
	value any // what the file resolver hands over (the Go type the catalog names)
	want  any // what get returns after set and WithDefaults
}

func unitSame(v any) unitCfgSample { return unitCfgSample{value: v, want: v} }

// unitCfgSamples holds one non-default value for every config.* field id of the
// catalog (dynamic ids expanded with the sample component "api").
var unitCfgSamples = map[string]unitCfgSample{
	"config.image":          unitSame("nginx:1.27"),
	"config.gitUrl":         unitSame("https://example.com/acme/shop.git"),
	"config.gitBranch":      unitSame("develop"),
	"config.submodules":     unitSame("on"),
	"config.dockerfilePath": unitSame("build/Dockerfile"),
	"config.buildContext":   unitSame("apps/web"),
	"config.buildType":      unitSame("dockerfile"),
	"config.buildLocation":  unitSame("target"),
	"config.buildServerId":  {value: "Builder-1", want: "builder-1"},
	"config.registryId":     {value: "Hub", want: "hub"},

	"config.port":             unitSame(8080),
	"config.startCommand":     unitSame([]string{"bun", "run", "start"}),
	"config.oneshot":          unitSame(true),
	"config.processName":      unitSame("jobs"),
	"config.singleton":        unitSame(true),
	"config.stopGraceSeconds": unitSame(45),
	"config.memoryMb":         unitSame(256),
	"config.cpus":             unitSame(1.5),
	"config.imageRetention":   unitSame(9),
	"config.blockEgress":      unitSame(true),

	"config.healthcheck.type":               unitSame("tcp"),
	"config.healthcheck.path":               unitSame("/healthz"),
	"config.healthcheck.port":               unitSame(9000),
	"config.healthcheck.intervalSeconds":    unitSame(5),
	"config.healthcheck.timeoutSeconds":     unitSame(7),
	"config.healthcheck.startPeriodSeconds": unitSame(11),
	"config.healthcheck.deadlineSeconds":    unitSame(90),
	"config.healthcheck.successThreshold":   unitSame(4),
	"config.healthcheck.command":            unitSame([]string{"CMD", "true"}),

	"config.preDeployCommand":         unitSame([]string{"bun", "run", "db:migrate"}),
	"config.preDeployTimeoutSeconds":  unitSame(120),
	"config.postDeployCommand":        unitSame([]string{"bun", "run", "warm"}),
	"config.postDeployTimeoutSeconds": unitSame(240),

	"config.files": {
		value: []projectconfig.FileMount{{Path: "b.conf", Mode: "0600", Content: "b"}, {Path: "a.conf", Content: "a"}},
		want:  []projectconfig.FileMount{{Path: "a.conf", Mode: "0644", Content: "a"}, {Path: "b.conf", Mode: "0600", Content: "b"}},
	},
	"config.processes.api": {
		value: projectconfig.ProcessValue{
			Name: "api", Command: []string{"bin/api", "--serve"}, Port: 9100, StopGraceSeconds: 90, Singleton: true, MemoryMB: 256, CPUs: 0.5,
		},
		// The platform fills the health check of the process from its port.
		want: projectconfig.ProcessValue{
			Name: "api", Command: []string{"bin/api", "--serve"}, Port: 9100, StopGraceSeconds: 90, Singleton: true, MemoryMB: 256, CPUs: 0.5,
			HealthCheck: projectconfig.ProcessHealth{Type: "http", Path: "/", Port: 9100, IntervalSeconds: 2, TimeoutSeconds: 3, DeadlineSeconds: 120, SuccessThreshold: 2},
		},
	},

	"config.database.engine":       unitSame("postgres"),
	"config.database.version":      unitSame("16"),
	"config.database.flavor":       unitSame("timescale"),
	"config.database.databaseName": unitSame("shop"),
	"config.database.username":     unitSame("shop_user"),

	"config.route.protocol":           unitSame("tcp"),
	"config.route.scheme":             unitSame("https"),
	"config.route.serverName":         unitSame("api.internal"),
	"config.route.healthPath":         unitSame("/ping"),
	"config.route.insecureSkipVerify": unitSame(true),
	"config.route.verifySkip":         unitSame(true),
	"config.route.openToInternet":     unitSame(true),
	"config.route.passHost":           unitSame(false),
	"config.route.listenPort":         unitSame(5432),
	"config.route.proxyProtocol":      unitSame(2),
	"config.route.allowCidrs":         unitSame([]string{"10.0.0.0/8"}),
	"config.route.denyCidrs":          unitSame([]string{"10.1.0.0/16"}),
	"config.route.upstreams": {
		value: []projectconfig.UpstreamValue{{Address: " 10.0.0.5:80 "}, {Service: "web", Port: 3000}},
		want:  []projectconfig.UpstreamValue{{Address: "10.0.0.5:80"}, {Service: "web", Port: 3000}},
	},

	"config.stack.compose":       unitSame("services:\n  web:\n    image: nginx\n"),
	"config.stack.waitSeconds":   unitSame(120),
	"config.stack.resources.api": unitSame(projectconfig.StackResourceValue{Component: "api", MemoryMB: 256, CPUs: 0.5}),

	"config.docker.capAdd":             {value: []string{"net_bind_service", "CAP_CHOWN"}, want: []string{"CHOWN", "NET_BIND_SERVICE"}},
	"config.docker.capDrop":            unitSame([]string{"ALL"}),
	"config.docker.noNewPrivileges":    unitSame(true),
	"config.docker.readOnlyRootfs":     unitSame(true),
	"config.docker.pidsLimit":          unitSame(int64(512)),
	"config.docker.hostname":           {value: "Web-1", want: "web-1"},
	"config.docker.dns":                unitSame([]string{"1.1.1.1", "8.8.8.8"}),
	"config.docker.sysctls":            unitSame(map[string]string{"net.core.somaxconn": "1024"}),
	"config.docker.hostSysctls":        unitSame(map[string]int64{"vm.max_map_count": 262144}),
	"config.docker.user":               unitSame("1000:1000"),
	"config.docker.groupAdd":           unitSame([]string{"44"}),
	"config.docker.shmSizeMb":          unitSame(256),
	"config.docker.extraHosts":         unitSame([]string{"db:10.0.0.1"}),
	"config.docker.labels":             unitSame(map[string]string{"team": "core"}),
	"config.docker.init":               unitSame(true),
	"config.docker.stopSignal":         {value: "sigquit", want: "SIGQUIT"},
	"config.docker.entrypoint":         unitSame([]string{"/entry.sh", "--serve"}),
	"config.docker.runtime":            unitSame("runsc"),
	"config.docker.privileged":         unitSame(true),
	"config.docker.pidHost":            unitSame(true),
	"config.docker.seccompUnconfined":  unitSame(true),
	"config.docker.apparmorUnconfined": unitSame(true),
	"config.docker.ulimits": {
		value: []agentprotocol.Ulimit{{Name: "nofile", Soft: 1024, Hard: 2048}, {Name: "memlock", Soft: -1, Hard: -1}},
		want:  []agentprotocol.Ulimit{{Name: "memlock", Soft: -1, Hard: -1}, {Name: "nofile", Soft: 1024, Hard: 2048}},
	},
	"config.docker.tmpfs": {
		value: []agentprotocol.TmpfsMount{{Path: "/tmp", SizeMB: 128, Mode: "1777"}, {Path: "/cache", SizeMB: 64}},
		want:  []agentprotocol.TmpfsMount{{Path: "/cache", SizeMB: 64}, {Path: "/tmp", SizeMB: 128, Mode: "1777"}},
	},
	"config.docker.devices": unitSame([]agentprotocol.DeviceMapping{{HostPath: "/dev/dri/renderD128", ContainerPath: "/dev/dri/renderD128", Permissions: "rw"}}),
	"config.docker.gpus": {
		value: agentprotocol.GPURequest{Count: 1, Driver: "nvidia", Capabilities: []string{"Utility", "compute"}},
		want:  agentprotocol.GPURequest{Count: 1, Driver: "nvidia", Capabilities: []string{"compute", "utility"}},
	},
}

// unitTypeName normalises a Go type name for comparison with a catalog ValueType.
func unitTypeName(s string) string {
	s, _, _ = strings.Cut(s, " (")
	return strings.ReplaceAll(s, "projectconfig.", "")
}

func TestUnitConfigFieldsRoundTripEveryCatalogID(t *testing.T) {
	pc := unitPlanCtx()
	var noAccessor, noSample []string
	seen := map[string]bool{}

	for _, spec := range projectconfig.Fields {
		if !strings.HasPrefix(spec.ID, "config.") {
			continue
		}
		id := strings.ReplaceAll(spec.ID, "*", "api")
		seen[id] = true
		f, ok := lookupCfgField(id)
		if !ok {
			noAccessor = append(noAccessor, id)
			continue
		}
		sample, ok := unitCfgSamples[id]
		if !ok {
			noSample = append(noSample, id)
			continue
		}
		t.Run(id, func(t *testing.T) {
			require.NotNil(t, f.get, "get")
			require.NotNil(t, f.set, "set")
			require.Equal(t, unitTypeName(spec.ValueType), unitTypeName(fmt.Sprintf("%T", sample.value)),
				"the sample must have the Go type the catalog names for %s", id)

			base := services.Config{}.WithDefaults()
			def := f.get(pc, base)

			cfg := base
			require.NoError(t, f.set(pc, &cfg, sample.value))
			got := f.get(pc, cfg.WithDefaults())
			require.Equal(t, sample.want, got)
			require.NotEqual(t, projectconfig.Fingerprint(def), projectconfig.Fingerprint(got),
				"the sample must differ from the default %v, or the round trip proves nothing", def)

			// Normalisation is idempotent: writing back what was read changes nothing.
			again := services.Config{}.WithDefaults()
			require.NoError(t, f.set(pc, &again, got))
			require.Equal(t, got, f.get(pc, again.WithDefaults()))

			// The planner compares the file value with the live value by fingerprint.
			dv, err := configOps{}.desired(pc, nil, projectconfig.FieldValue{ID: id, Value: sample.value})
			require.NoError(t, err)
			lv, present := configOps{}.live(pc, &liveService{Cfg: cfg.WithDefaults()}, id, "")
			require.True(t, present)
			require.Equal(t, lv.FP, dv.FP)
			require.Equal(t, display(sample.want), dv.Display)
			require.Equal(t, display(sample.want), lv.Display)

			// Every other service is not in sync with a field that has a different value.
			dlv, _ := configOps{}.live(pc, &liveService{Cfg: base}, id, "")
			require.NotEqual(t, dlv.FP, dv.FP)
		})
	}
	require.Empty(t, noAccessor, "catalog ids without a config field accessor (lookupCfgField)")
	require.Empty(t, noSample, "catalog ids without a sample in unitCfgSamples (add one with a non-default value)")
	for id := range unitCfgSamples {
		require.True(t, seen[id], "unitCfgSamples has %s but the catalog has no such config id", id)
	}
	require.GreaterOrEqual(t, len(seen), 60, "the catalog should expose the whole config table")
}

func TestUnitConfigFieldLookupMisses(t *testing.T) {
	for _, id := range []string{"", "port", "config.nope", "config.docker.nope", "config.route.nope", "config.database.nope", "config.stack.nope", "config.healthcheck.nope"} {
		_, ok := lookupCfgField(id)
		require.False(t, ok, "%q", id)
	}
	pc := unitPlanCtx()
	var re *refError
	_, err := configOps{}.desired(pc, nil, projectconfig.FieldValue{ID: "config.nope", Value: 1})
	require.True(t, errors.As(err, &re), "%v", err)
	require.Equal(t, "invalid_value", re.Code)
	_, present := configOps{}.live(pc, &liveService{Cfg: services.Config{}.WithDefaults()}, "config.nope", "")
	require.False(t, present)
}

func TestUnitConfigFieldSetRejectsWrongTypes(t *testing.T) {
	pc := unitPlanCtx()
	for _, tc := range []struct {
		id string
		v  any
	}{
		{"config.port", "8080"},
		{"config.image", 5},
		{"config.oneshot", "yes"},
		{"config.cpus", "1.5"},
		{"config.startCommand", "bun"},
		{"config.files", "x"},
		{"config.route.protocol", 5},
		{"config.route.passHost", "no"},
		{"config.route.listenPort", "1"},
		{"config.route.allowCidrs", "10.0.0.0/8"},
		{"config.route.upstreams", "x"},
		{"config.stack.waitSeconds", "x"},
		{"config.stack.resources.api", "x"},
		{"config.database.engine", 5},
		{"config.docker.capAdd", 5},
		{"config.docker.pidsLimit", "x"},
		{"config.docker.privileged", "x"},
		{"config.processName", 5},
		{"config.singleton", "yes"},
		{"config.processes.worker", "x"},
	} {
		f, ok := lookupCfgField(tc.id)
		require.True(t, ok, tc.id)
		cfg := services.Config{}.WithDefaults()
		err := f.set(pc, &cfg, tc.v)
		var re *refError
		require.True(t, errors.As(err, &re), "%s: %v", tc.id, err)
		require.Equal(t, "wrong_type", re.Code, tc.id)
		require.Equal(t, services.Config{}.WithDefaults(), cfg, "%s: a rejected value changes nothing", tc.id)
	}
}

func TestUnitConfigFieldNamedReferences(t *testing.T) {
	pc := unitPlanCtx()
	for _, tc := range []struct {
		id, name, code string
	}{
		{"config.buildServerId", "ghost", "unknown_server"},
		{"config.buildServerId", "dup", "ambiguous_reference"},
		{"config.registryId", "ghost", "unknown_registry"},
		{"config.registryId", "twin", "ambiguous_reference"},
	} {
		f, _ := lookupCfgField(tc.id)
		cfg := services.Config{}.WithDefaults()
		err := f.set(pc, &cfg, tc.name)
		var re *refError
		require.True(t, errors.As(err, &re), "%s %s: %v", tc.id, tc.name, err)
		require.Equal(t, tc.code, re.Code, "%s %s", tc.id, tc.name)
	}

	// An empty name clears the reference; an unknown id prints as the id.
	f, _ := lookupCfgField("config.buildServerId")
	cfg := services.Config{}.WithDefaults()
	require.NoError(t, f.set(pc, &cfg, "builder-1"))
	require.Equal(t, &unitBuilderID, cfg.BuildServerID)
	require.Equal(t, "builder-1", f.get(pc, cfg))
	require.NoError(t, f.set(pc, &cfg, ""))
	require.Nil(t, cfg.BuildServerID)
	require.Equal(t, "", f.get(pc, cfg))
	stray := unitUUID(555)
	cfg.BuildServerID = &stray
	require.Equal(t, stray.String(), f.get(pc, cfg))

	g, _ := lookupCfgField("config.registryId")
	require.NoError(t, g.set(pc, &cfg, "HUB"))
	require.Equal(t, &unitHubID, cfg.RegistryID)
	require.NoError(t, g.set(pc, &cfg, ""))
	require.Nil(t, cfg.RegistryID)
	cfg.RegistryID = &stray
	require.Equal(t, stray.String(), g.get(pc, cfg))
}

func TestUnitConfigDesiredEqualsLiveWhenAFileWritesADefault(t *testing.T) {
	pc := unitPlanCtx()
	base := services.Config{}.WithDefaults()
	ls := &liveService{Cfg: base}
	for _, tc := range []struct {
		id string
		v  any
	}{
		{"config.stopGraceSeconds", 30},
		{"config.imageRetention", 5},
		{"config.submodules", "auto"},
		{"config.dockerfilePath", "Dockerfile"},
		{"config.buildContext", "."},
		{"config.buildType", "auto"},
		{"config.buildLocation", "inherit"},
		{"config.port", 0},
		{"config.memoryMb", 0},
		{"config.cpus", 0.0},
		{"config.oneshot", false},
		{"config.startCommand", []string{}},
		{"config.files", []projectconfig.FileMount(nil)},
		{"config.docker.capAdd", []string{}},
		{"config.docker.labels", map[string]string{}},
		{"config.docker.gpus", agentprotocol.GPURequest{}},
		{"config.docker.privileged", false},
		{"config.healthcheck.intervalSeconds", 2},
		{"config.healthcheck.timeoutSeconds", 3},
		{"config.healthcheck.deadlineSeconds", 120},
		{"config.healthcheck.successThreshold", 2},
		{"config.healthcheck.type", "none"},
	} {
		dv, err := configOps{}.desired(pc, ls, projectconfig.FieldValue{ID: tc.id, Value: tc.v})
		require.NoError(t, err, tc.id)
		lv, present := configOps{}.live(pc, ls, tc.id, "")
		require.True(t, present, tc.id)
		require.Equal(t, lv.FP, dv.FP, "%s: writing the default explicitly must plan to zero", tc.id)
		require.Equal(t, StateInSync, classify("sha256:old", true, lv.FP, present, dv.FP), tc.id)
	}
}

func TestUnitConfigKindFieldsAreAbsentOnOtherKinds(t *testing.T) {
	pc := unitPlanCtx()
	image := &liveService{Cfg: services.Config{Image: "nginx"}.WithDefaults()}
	for _, id := range []string{"config.route.protocol", "config.route.upstreams", "config.stack.compose", "config.stack.waitSeconds", "config.stack.resources.api", "config.database.engine"} {
		_, present := configOps{}.live(pc, image, id, "")
		require.False(t, present, "%s is not a field of an image service", id)
	}
	for _, id := range []string{"config.port", "config.docker.user", "config.files"} {
		_, present := configOps{}.live(pc, image, id, "")
		require.True(t, present, id)
	}
	require.True(t, absentOnKind("config.route.scheme", services.Config{}))
	require.False(t, absentOnKind("config.route.scheme", services.Config{Route: &services.RouteConfig{}}))
	require.True(t, absentOnKind("config.stack.compose", services.Config{}))
	require.False(t, absentOnKind("config.stack.compose", services.Config{Stack: &services.StackConfig{}}))
	require.True(t, absentOnKind("config.database.engine", services.Config{}))
	require.False(t, absentOnKind("config.database.engine", services.Config{Database: new(engines.Spec)}))
	require.False(t, absentOnKind("config.port", services.Config{}))
}

func TestUnitConfigScalarSliceGetIsNeverNil(t *testing.T) {
	get := cfgFields["config.startCommand"].get
	require.Equal(t, []string{}, get(nil, services.Config{}))
	require.Equal(t, []string{"a"}, get(nil, services.Config{StartCommand: []string{"a"}}))
}

func TestUnitConfigSettersDoNotMutateTheirSource(t *testing.T) {
	pc := unitPlanCtx()
	set := func(cfg *services.Config, id string, v any) {
		t.Helper()
		f, ok := lookupCfgField(id)
		require.True(t, ok, id)
		require.NoError(t, f.set(pc, cfg, v))
	}

	t.Run("route", func(t *testing.T) {
		src := services.Config{}.WithDefaults()
		set(&src, "config.route.protocol", "http")
		set(&src, "config.route.scheme", "http")
		snapshot := *src.Route
		next := src
		set(&next, "config.route.scheme", "https")
		set(&next, "config.route.allowCidrs", []string{"10.0.0.0/8"})
		require.Equal(t, snapshot, *src.Route, "the source route is untouched")
		require.Equal(t, "https", next.Route.Scheme)
		require.Equal(t, "http", next.Route.Protocol, "a route key leaves the others alone")
	})

	t.Run("stack", func(t *testing.T) {
		src := services.Config{}.WithDefaults()
		set(&src, "config.stack.compose", "services:\n  web:\n    image: nginx\n")
		set(&src, "config.stack.resources.api", projectconfig.StackResourceValue{Component: "api", MemoryMB: 128, CPUs: 1})
		snapshot := *src.Stack
		next := src
		set(&next, "config.stack.waitSeconds", 99)
		set(&next, "config.stack.resources.worker", projectconfig.StackResourceValue{Component: "worker", MemoryMB: 64})
		require.Equal(t, snapshot.WaitSeconds, src.Stack.WaitSeconds)
		require.Len(t, src.Stack.Resources, 1, "the source resources map is not written to")
		require.Equal(t, 99, next.Stack.WaitSeconds)
		require.Len(t, next.Stack.Resources, 2)
		require.Equal(t, agentprotocol.StackResources{MemoryMB: 128, CPUs: 1}, next.Stack.Resources["api"], "other components stay")
		require.Equal(t, snapshot.Compose, next.Stack.Compose)

		f, _ := lookupCfgField("config.stack.resources.db")
		require.Equal(t, projectconfig.StackResourceValue{Component: "db"}, f.get(pc, next), "a component without limits reads as zero")
		require.Equal(t, projectconfig.StackResourceValue{Component: "db"}, f.get(pc, services.Config{}), "so does a service without a stack")
	})

	t.Run("database", func(t *testing.T) {
		src := services.Config{}.WithDefaults()
		set(&src, "config.database.engine", "postgres")
		set(&src, "config.database.databaseName", "shop")
		snapshot := *src.Database
		next := src
		set(&next, "config.database.username", "app")
		require.Equal(t, snapshot, *src.Database)
		require.Equal(t, "app", next.Database.Username)
		require.Equal(t, "shop", next.Database.DatabaseName)
		require.Equal(t, "postgres", next.Database.Engine)
	})

	t.Run("docker", func(t *testing.T) {
		src := services.Config{}.WithDefaults()
		set(&src, "config.docker.user", "1000")
		snapshot := *src.Docker
		next := src
		set(&next, "config.docker.hostname", "web")
		require.Equal(t, snapshot, *src.Docker)
		require.Equal(t, "web", next.Docker.Hostname)
		require.Equal(t, "1000", next.Docker.User)
	})
}

func TestUnitDockerCollectionFieldsKeepTheRestOfTheOptions(t *testing.T) {
	pc := unitPlanCtx()
	set := func(cfg *services.Config, id string, v any) {
		t.Helper()
		f, ok := lookupCfgField(id)
		require.True(t, ok, id)
		require.NoError(t, f.set(pc, cfg, v))
		*cfg = cfg.WithDefaults()
	}
	get := func(cfg services.Config, id string) any {
		t.Helper()
		f, ok := lookupCfgField(id)
		require.True(t, ok, id)
		return f.get(pc, cfg)
	}

	ulimits := []agentprotocol.Ulimit{{Name: "nofile", Soft: 1024, Hard: 2048}}
	tmpfs := []agentprotocol.TmpfsMount{{Path: "/tmp", SizeMB: 64, Mode: "1777"}}
	devices := []agentprotocol.DeviceMapping{{HostPath: "/dev/kvm"}}
	gpus := agentprotocol.GPURequest{Count: -1, Driver: "nvidia"}

	cfg := services.Config{}.WithDefaults()
	set(&cfg, "config.docker.ulimits", ulimits)
	set(&cfg, "config.docker.tmpfs", tmpfs)
	set(&cfg, "config.docker.devices", devices)
	set(&cfg, "config.docker.gpus", gpus)
	require.Equal(t, ulimits, get(cfg, "config.docker.ulimits"))
	require.Equal(t, tmpfs, get(cfg, "config.docker.tmpfs"))
	require.Equal(t, devices, get(cfg, "config.docker.devices"))
	require.Equal(t, gpus, get(cfg, "config.docker.gpus"))
	require.Equal(t, "", get(cfg, "config.docker.user"), "a field that was not written stays at its default")

	// Clearing one collection leaves the others.
	set(&cfg, "config.docker.ulimits", nil)
	require.Empty(t, get(cfg, "config.docker.ulimits"))
	require.Equal(t, tmpfs, get(cfg, "config.docker.tmpfs"))
	require.Equal(t, devices, get(cfg, "config.docker.devices"))
	require.Equal(t, gpus, get(cfg, "config.docker.gpus"))

	set(&cfg, "config.docker.gpus", agentprotocol.GPURequest{})
	require.Equal(t, agentprotocol.GPURequest{}, get(cfg, "config.docker.gpus"), "a zero GPU request means no GPUs")
	require.NotNil(t, cfg.Docker)
	require.Nil(t, cfg.Docker.GPUs)

	set(&cfg, "config.docker.tmpfs", []agentprotocol.TmpfsMount{})
	set(&cfg, "config.docker.devices", []agentprotocol.DeviceMapping(nil))
	require.Nil(t, cfg.Docker, "an option set with nothing left is no option set")

	// ulimits and tmpfs compare in a canonical order, so a reordered file is no change.
	a := services.Config{}.WithDefaults()
	b := services.Config{}.WithDefaults()
	set(&a, "config.docker.ulimits", []agentprotocol.Ulimit{{Name: "nofile", Soft: 1, Hard: 2}, {Name: "memlock", Soft: -1, Hard: -1}})
	set(&b, "config.docker.ulimits", []agentprotocol.Ulimit{{Name: "memlock", Soft: -1, Hard: -1}, {Name: "nofile", Soft: 1, Hard: 2}})
	require.Equal(t, projectconfig.Fingerprint(get(a, "config.docker.ulimits")), projectconfig.Fingerprint(get(b, "config.docker.ulimits")))
	set(&a, "config.docker.tmpfs", []agentprotocol.TmpfsMount{{Path: "/b"}, {Path: "/a"}})
	set(&b, "config.docker.tmpfs", []agentprotocol.TmpfsMount{{Path: "/a"}, {Path: "/b"}})
	require.Equal(t, projectconfig.Fingerprint(get(a, "config.docker.tmpfs")), projectconfig.Fingerprint(get(b, "config.docker.tmpfs")))
}

// ---------------------------------------------------------------------------
// route upstream placeholder ids

func TestUnitPlaceholderAndSlugIDs(t *testing.T) {
	require.Equal(t, placeholderID("queue"), placeholderID("queue"))
	require.NotEqual(t, placeholderID("queue"), placeholderID("web"))
	require.NotEqual(t, uuid.Nil, placeholderID("queue"))
	require.Equal(t, uuid.Version(5), placeholderID("queue").Version(), "a name-based id")

	pc := unitPlanCtx()
	require.Equal(t, unitWebID, pc.slugID("web"), "a live service keeps its own id")
	require.Equal(t, placeholderID("queue"), pc.slugID("queue"), "a planned service gets its placeholder")
	require.Equal(t, placeholderID("ghost"), pc.slugID("ghost"))

	// slugOf inverts slugID for every service the plan knows.
	for _, slug := range []string{"web", "queue"} {
		require.Equal(t, slug, pc.slugOf(pc.slugID(slug)), slug)
	}
	require.Equal(t, placeholderID("ghost").String(), pc.slugOf(placeholderID("ghost")), "a placeholder of a slug outside the file prints as its id")
	stray := unitUUID(777)
	require.Equal(t, stray.String(), pc.slugOf(stray))

	noRes := unitPlanCtx()
	noRes.res = nil
	require.Equal(t, "web", noRes.slugOf(unitWebID))
	require.Equal(t, placeholderID("queue").String(), noRes.slugOf(placeholderID("queue")))

	// During an apply the created ids win over live ids and placeholders.
	created := unitUUID(888)
	pc.createdID = func(slug string) (uuid.UUID, bool) {
		if slug == "queue" {
			return created, true
		}
		return uuid.Nil, false
	}
	require.Equal(t, created, pc.slugID("queue"))
	require.Equal(t, unitWebID, pc.slugID("web"))
	require.Equal(t, "queue", pc.slugOf(created))
	require.Equal(t, "queue", pc.slugOf(placeholderID("queue")), "the plan-time id still resolves")
	pc.createdID = func(slug string) (uuid.UUID, bool) { return created, slug == "web" }
	require.Equal(t, created, pc.slugID("web"), "a created id is preferred over the live one")
}

func TestUnitRouteUpstreamsAcrossLiveAndPlannedServices(t *testing.T) {
	pc := unitPlanCtx()
	f, ok := lookupCfgField("config.route.upstreams")
	require.True(t, ok)

	cfg := services.Config{}.WithDefaults()
	ups := []projectconfig.UpstreamValue{{Service: "queue", Port: 5672}, {Service: "web", Port: 3000}, {Address: "10.0.0.5:80"}}
	require.NoError(t, f.set(pc, &cfg, ups))
	require.Len(t, cfg.Route.Upstreams, 3)
	require.Equal(t, placeholderID("queue"), *cfg.Route.Upstreams[0].ServiceID, "a service created by the same file is a placeholder")
	require.Equal(t, unitWebID, *cfg.Route.Upstreams[1].ServiceID)
	require.Nil(t, cfg.Route.Upstreams[2].ServiceID)
	require.Equal(t, ups, f.get(pc, cfg), "reading the plan-time config gives the slugs back")

	var re *refError
	err := f.set(pc, &cfg, []projectconfig.UpstreamValue{{Service: "ghost", Port: 1}})
	require.True(t, errors.As(err, &re), "%v", err)
	require.Equal(t, "invalid_upstream", re.Code)

	// An upstream whose service id is unknown prints as the id.
	stray := unitUUID(999)
	cfg.Route.Upstreams = []services.RouteUpstream{{ServiceID: &stray, Port: 80}}
	require.Equal(t, []projectconfig.UpstreamValue{{Service: stray.String(), Port: 80}}, f.get(pc, cfg))
}

// ---------------------------------------------------------------------------
// opsFor

func TestUnitOpsForEveryCatalogID(t *testing.T) {
	want := map[string]string{
		"name": "metaOps", "server": "metaOps", "generate_domain": "metaOps",
		"links.*": "linkOps", "networks.*": "networkOps",
		"variables.*": "varOps", "variables.*#secret": "varOps",
		"domains.*": "domainOps", "domains.*.cloudflare": "cloudflareOps",
		"volumes.*":      "volumeOps",
		"scale.replicas": "scaleOps", "scale.autoscaling.enabled": "scaleOps", "scale.autoscaling.min": "scaleOps",
		"scale.autoscaling.max": "scaleOps", "scale.autoscaling.targetCpuPercent": "scaleOps",
		"git.link": "gitOps", "git.auto_deploy": "gitOps", "git.watch_paths": "gitOps",
		"uptime.enabled": "uptimeOps", "uptime.alerts": "uptimeOps", "uptime.failureThreshold": "uptimeOps",
		"uptime.recoveryThreshold": "uptimeOps", "uptime.reminderMinutes": "uptimeOps",
		"tasks.*": "taskOps", "backups.*": "backupOps",
		"processes.*.scale.replicas": "processScaleOps", "processes.*.scale.autoscaling.enabled": "processScaleOps",
		"processes.*.scale.autoscaling.min": "processScaleOps", "processes.*.scale.autoscaling.max": "processScaleOps",
		"processes.*.scale.autoscaling.targetCpuPercent": "processScaleOps",
	}
	for _, spec := range projectconfig.Fields {
		if strings.HasPrefix(spec.ID, "config.") {
			concrete := strings.ReplaceAll(spec.ID, "*", "api")
			require.Equal(t, "configOps", strings.TrimPrefix(fmt.Sprintf("%T", opsFor(concrete)), "configsync."), concrete)
			continue
		}
		name, ok := want[spec.ID]
		require.True(t, ok, "catalog id %s has no expected handler in this test: add it", spec.ID)
		concrete := strings.ReplaceAll(spec.ID, "*", "sample")
		ops := opsFor(concrete)
		require.NotNil(t, ops, "opsFor(%s)", concrete)
		require.Equal(t, name, strings.TrimPrefix(fmt.Sprintf("%T", ops), "configsync."), concrete)

		found, ok := projectconfig.LookupField(concrete)
		require.True(t, ok, concrete)
		require.Equal(t, spec.ID, found.ID, "the catalog resolves %s to its own pattern", concrete)
	}
	for id := range want {
		var found bool
		for _, spec := range projectconfig.Fields {
			found = found || spec.ID == id
		}
		require.True(t, found, "the expectation table names %s but the catalog does not", id)
	}

	// Hostnames with dots and paths, and ids that are no field.
	require.Equal(t, "domainOps", strings.TrimPrefix(fmt.Sprintf("%T", opsFor("domains.shop.acme.com/api")), "configsync."))
	require.Equal(t, "cloudflareOps", strings.TrimPrefix(fmt.Sprintf("%T", opsFor("domains.shop.acme.com/api.cloudflare")), "configsync."))
	require.Equal(t, "varOps", strings.TrimPrefix(fmt.Sprintf("%T", opsFor("variables.A#secret")), "configsync."))
	for _, id := range []string{"", "bogus", "port", "scales.replicas", "taskss.x", "generate_domain2"} {
		require.Nil(t, opsFor(id), "%q", id)
	}
}

// ---------------------------------------------------------------------------
// domains: ids, rules and the proxy scope

func TestUnitSplitDomainID(t *testing.T) {
	for _, tc := range []struct{ id, host, path string }{
		{"domains.shop.acme.com", "shop.acme.com", ""},
		{"domains.shop.acme.com/api", "shop.acme.com", "/api"},
		{"domains.shop.acme.com/api/v1", "shop.acme.com", "/api/v1"},
		{"domains.shop.acme.com.cloudflare", "shop.acme.com", ""},
		{"domains.shop.acme.com/api.cloudflare", "shop.acme.com", "/api"},
		{"domains.*.acme.com", "*.acme.com", ""},
	} {
		host, path := splitDomainID(tc.id)
		require.Equal(t, tc.host, host, tc.id)
		require.Equal(t, tc.path, path, tc.id)
	}
}

func TestUnitRuleParts(t *testing.T) {
	require.Empty(t, ruleParts(projectconfig.DomainRule{Host: "shop.acme.com", PathPrefix: "/api"}), "the host and path are the identity, not parts")

	s, i, b := "x", 1, true
	full := projectconfig.DomainRule{
		Host: "h", TargetPort: &i, Component: &s, StripPrefix: &b, AddPrefix: &s, Kind: &s, RedirectTo: &s, RedirectCode: &i,
		PreservePath: &b, TLS: &s, Certificate: &s, DNSResolver: &s, WWWRedirect: &b, Options: &agentprotocol.RouteOptions{},
	}
	require.Equal(t, []string{
		"targetPort", "component", "stripPrefix", "addPrefix", "kind", "redirectTo", "redirectCode",
		"preservePath", "tls", "certificate", "dnsResolver", "wwwRedirect", "options",
	}, ruleParts(full))

	zeroPorts := 0
	f := false
	empty := ""
	zero := projectconfig.DomainRule{TargetPort: &zeroPorts, StripPrefix: &f, AddPrefix: &empty}
	require.Equal(t, []string{"targetPort", "stripPrefix", "addPrefix"}, ruleParts(zero), "a declared zero value is still declared")
	require.Equal(t, []string{"options"}, ruleParts(projectconfig.DomainRule{Options: &agentprotocol.RouteOptions{Compress: true}}))
}

func TestUnitProxyScopeNeeded(t *testing.T) {
	port := 8080
	comp := "web"
	proc := "metrics"
	strip := true
	kind := "redirect"
	const path = `services.web.domains."shop.acme.com"`
	opts := &agentprotocol.RouteOptions{Compress: true}
	for _, tc := range []struct {
		name  string
		id    string
		value any
		want  bool
	}{
		{"target port only", "domains.shop.acme.com", projectconfig.DomainRule{Host: "shop.acme.com", TargetPort: &port}, false},
		{"component only", "domains.shop.acme.com", projectconfig.DomainRule{Host: "shop.acme.com", Component: &comp}, false},
		{"target port and component", "domains.shop.acme.com", projectconfig.DomainRule{Host: "shop.acme.com", TargetPort: &port, Component: &comp}, false},
		{"process only", "domains.shop.acme.com", projectconfig.DomainRule{Host: "shop.acme.com", Process: &proc}, false},
		{"target port and process", "domains.shop.acme.com", projectconfig.DomainRule{Host: "shop.acme.com", TargetPort: &port, Process: &proc}, false},
		{"process plus options", "domains.shop.acme.com", projectconfig.DomainRule{Host: "shop.acme.com", Process: &proc, Options: opts}, true},
		{"no declared keys", "domains.shop.acme.com", projectconfig.DomainRule{Host: "shop.acme.com"}, false},
		{"strip prefix", "domains.shop.acme.com", projectconfig.DomainRule{Host: "shop.acme.com", StripPrefix: &strip}, true},
		{"redirect kind", "domains.shop.acme.com", projectconfig.DomainRule{Host: "shop.acme.com", Kind: &kind}, true},
		{"options", "domains.shop.acme.com", projectconfig.DomainRule{Host: "shop.acme.com", Options: opts}, true},
		{"target port plus options", "domains.shop.acme.com", projectconfig.DomainRule{Host: "shop.acme.com", TargetPort: &port, Options: opts}, true},
		{"wildcard host without keys", "domains.*.acme.com", projectconfig.DomainRule{Host: "*.acme.com"}, true},
		{"wildcard host with only a target port", "domains.*.acme.com", projectconfig.DomainRule{Host: "*.acme.com", TargetPort: &port}, true},
		{"wildcard host with an unexpected value", "domains.*.acme.com", nil, true},
		{"path prefix with only a target port", "domains.shop.acme.com/api", projectconfig.DomainRule{Host: "shop.acme.com", PathPrefix: "/api", TargetPort: &port}, true},
		{"path prefix without keys", "domains.shop.acme.com/api", projectconfig.DomainRule{Host: "shop.acme.com", PathPrefix: "/api"}, true},
		{"cloudflare settings", "domains.shop.acme.com.cloudflare", projectconfig.CloudflareRule{}, false},
		{"cloudflare id with a rule value", "domains.shop.acme.com.cloudflare", projectconfig.DomainRule{Host: "shop.acme.com", Options: opts}, false},
		{"not a domain field", "config.port", 8080, false},
		{"a value of another type", "domains.shop.acme.com", "x", false},
		{"no value", "domains.shop.acme.com", nil, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := &fieldPlan{fv: projectconfig.FieldValue{ID: tc.id, Path: path, Value: tc.value}}
			require.Equal(t, tc.want, proxyScopeNeeded(f))
		})
	}
}

// ---------------------------------------------------------------------------
// PlanChange rendering

func TestUnitChangeRendering(t *testing.T) {
	p := &planned{}

	t.Run("set copies position, ids and fingerprints", func(t *testing.T) {
		f := &fieldPlan{
			fv:      projectconfig.FieldValue{ID: "config.port", Path: "services.web.port", Pos: projectconfig.Pos{Line: 14, Column: 1}, Applies: AppliesStaged},
			desired: fieldView{FP: "sha256:d", Display: "8080"}, live: fieldView{FP: "sha256:l", Display: "3000"},
			present: true, base: "sha256:b", hasBase: true, state: StateFileChanged, changed: true,
		}
		c := p.change(f)
		require.Equal(t, PlanChange{
			Path: "services.web.port", Line: 14, Field: "config.port", Op: OpSet, Applies: AppliesStaged, Reason: ReasonFileChanged,
			From: "3000", To: "8080", baseFP: "sha256:b", liveFP: "sha256:l", desiredFP: "sha256:d",
		}, c)
	})

	t.Run("reasons follow the state", func(t *testing.T) {
		for state, want := range map[string]string{
			StateNew: ReasonNew, StateAdopt: ReasonAdopt, StateFileChanged: ReasonFileChanged, StateDrift: ReasonDrift, StateConflict: ReasonConflict,
		} {
			c := p.change(&fieldPlan{fv: projectconfig.FieldValue{ID: "config.port", Applies: AppliesStaged}, present: true, state: state, changed: true})
			require.Equal(t, OpSet, c.Op)
			require.Equal(t, want, c.Reason, state)
		}
	})

	t.Run("absent live is an add", func(t *testing.T) {
		c := p.change(&fieldPlan{
			fv:      projectconfig.FieldValue{ID: "scale.replicas", Applies: AppliesImmediate},
			desired: fieldView{Display: "2"}, state: StateNew, changed: true,
		})
		require.Equal(t, OpAdd, c.Op)
		require.Equal(t, ReasonNew, c.Reason)
		require.Equal(t, AppliesImmediate, c.Applies)
		require.Equal(t, "", c.From)
		require.Equal(t, "2", c.To)
		require.False(t, c.Masked)
	})

	t.Run("a removal shows the live value and no desired value", func(t *testing.T) {
		c := p.change(&fieldPlan{
			fv:      projectconfig.FieldValue{ID: "tasks.old", Path: "services.web.tasks.old", Applies: AppliesImmediate},
			desired: fieldView{FP: "ignored"}, live: fieldView{FP: "sha256:l", Display: "0 7 * * *"},
			present: true, base: "sha256:b", hasBase: true, changed: true, remove: true,
		})
		require.Equal(t, OpRemove, c.Op)
		require.Equal(t, ReasonFileChanged, c.Reason)
		require.Equal(t, "0 7 * * *", c.From)
		require.Equal(t, "", c.To)
		require.Equal(t, "", c.desiredFP)
		require.Equal(t, "sha256:l", c.liveFP)
		require.Equal(t, "sha256:b", c.baseFP)
	})

	t.Run("generated values keep their descriptor, nothing else is shown", func(t *testing.T) {
		c := p.change(&fieldPlan{
			fv:      projectconfig.FieldValue{ID: "variables.SESSION", Applies: AppliesStaged},
			desired: fieldView{Display: "generated(secret(48))", Masked: true}, state: StateNew, changed: true,
		})
		require.True(t, c.Masked)
		require.Equal(t, "generated(secret(48))", c.To)
		require.Equal(t, "", c.From)
	})

	t.Run("masked values are replaced by the mask on both sides", func(t *testing.T) {
		c := p.change(&fieldPlan{
			fv:      projectconfig.FieldValue{ID: "variables.DB_URL", Applies: AppliesStaged},
			desired: fieldView{Display: "${{db.URL}}", Masked: true}, live: fieldView{Display: "old-text", Masked: true},
			present: true, state: StateFileChanged, changed: true,
		})
		require.True(t, c.Masked)
		require.Equal(t, Mask, c.From)
		require.Equal(t, Mask, c.To)
		require.NotContains(t, c.From+c.To, "old-text")
		require.NotContains(t, c.From+c.To, "db.URL")
	})

	t.Run("a masked live value hides itself next to a plain desired value", func(t *testing.T) {
		c := p.change(&fieldPlan{
			fv:      projectconfig.FieldValue{ID: "variables.TOKEN", Applies: AppliesStaged},
			desired: fieldView{Display: "plain"}, live: fieldView{Display: "stored-secret-descriptor", Masked: true},
			present: true, state: StateDrift, changed: true,
		})
		require.True(t, c.Masked)
		require.Equal(t, Mask, c.From)
		require.Equal(t, "plain", c.To)
	})

	t.Run("a masked removal hides the live value", func(t *testing.T) {
		c := p.change(&fieldPlan{
			fv:   projectconfig.FieldValue{ID: "variables.OLD", Applies: AppliesStaged},
			live: fieldView{Display: "stored-secret", Masked: true}, present: true, changed: true, remove: true,
		})
		require.Equal(t, OpRemove, c.Op)
		require.True(t, c.Masked)
		require.Equal(t, Mask, c.From)
	})
}

// ---------------------------------------------------------------------------
// planned.finish: Summary, scopes, guard

func unitPlanned(production bool) *planned {
	return &planned{
		env:     services.EnvScope{Env: db.Environment{ID: unitUUID(900), Slug: "env", IsProduction: production}},
		scopes:  scopeSet{},
		fileSHA: "filesha",
		public:  Plan{Guard: Guard{ProductionApproval: ApprovalNotRequired}},
	}
}

func unitChange(field, op, applies string) PlanChange {
	return PlanChange{Path: "services.x." + field, Field: field, Op: op, Applies: applies}
}

func unitUpdate(slug string, changes ...PlanChange) *svcPlan {
	return &svcPlan{
		slug: slug, ls: &liveService{Source: &db.ConfigSource{}},
		pl: PlanService{Slug: slug, Kind: "image", Action: ActionUpdate, Changes: changes},
	}
}

func TestUnitFinishSummaryAccounting(t *testing.T) {
	p := unitPlanned(false)
	p.public.Services = []PlanService{
		{Slug: "legacy", Kind: "image", Action: ActionNotInFile, Managed: true, Changes: []PlanChange{}},
		{Slug: "stray", Kind: "git", Action: ActionNotInFile, Changes: []PlanChange{}},
	}
	p.svcs = []*svcPlan{
		{
			slug: "api", create: true, rs: &projectconfig.ResolvedService{Slug: "api", Kind: projectconfig.KindImage},
			pl: PlanService{Slug: "api", Kind: "image", Action: ActionCreate, Changes: []PlanChange{
				unitChange("config.image", OpAdd, AppliesStaged), unitChange("config.port", OpAdd, AppliesStaged),
			}},
		},
		unitUpdate("web",
			unitChange("config.port", OpSet, AppliesStaged),
			unitChange("scale.replicas", OpSet, AppliesImmediate),
			unitChange("backups.nightly", OpAdd, AppliesDeferred),
			unitChange("variables.OLD", OpRemove, AppliesStaged),
		),
		{slug: "worker", ls: &liveService{}, pl: PlanService{Slug: "worker", Kind: "image", Action: ActionUnchanged, Changes: []PlanChange{}}},
	}
	p.warnf("secret_missing_value", "services.web.secrets.keys[0]", projectconfig.Pos{Line: 31, Column: 9}, "STRIPE_SECRET_KEY has no value yet")
	p.infof("deferred", "services.web.backups.nightly", projectconfig.Pos{Line: 40, Column: 1}, "runs after the first deploy")

	p.finish()
	pub := p.public

	require.Equal(t, PlanSummary{
		Create: 1, Update: 1, Unchanged: 1, NotInFile: 2, Adopted: 0,
		Staged: 4, Immediate: 1, Deferred: 1, Pruned: 1,
		Errors: 0, Warnings: 1, RequiresDeploy: []string{"api", "web"},
	}, pub.Summary)
	require.True(t, pub.Valid)
	require.Equal(t, []string{policy.ScopeProjectsWrite, policy.ScopeDeploymentsWrite}, pub.RequiredScopes)
	require.Equal(t, Guard{ProductionApproval: ApprovalNotRequired}, pub.Guard, "a non-production environment never needs approval")
	require.True(t, p.hasWork)
	require.False(t, p.needsRole)
	require.Regexp(t, `^pl_[0-9a-f]{32}$`, pub.PlanID)
	require.Equal(t, p.planID(), pub.PlanID)

	var slugs []string
	for _, s := range pub.Services {
		slugs = append(slugs, s.Slug)
	}
	require.Equal(t, []string{"api", "web", "worker", "legacy", "stray"}, slugs, "file services in order, then the live ones the file does not mention")
	require.Len(t, pub.Diagnostics, 2)
}

func TestUnitFinishRequiresDeployOnlyForStagedChanges(t *testing.T) {
	p := unitPlanned(false)
	p.svcs = []*svcPlan{
		unitUpdate("immediate-only", unitChange("scale.replicas", OpSet, AppliesImmediate)),
		unitUpdate("staged", unitChange("config.port", OpSet, AppliesStaged)),
		unitUpdate("deferred-only", unitChange("backups.nightly", OpAdd, AppliesDeferred)),
	}
	p.finish()
	require.Equal(t, []string{"staged"}, p.public.Summary.RequiresDeploy)

	none := unitPlanned(false)
	none.svcs = []*svcPlan{unitUpdate("web", unitChange("scale.replicas", OpSet, AppliesImmediate))}
	none.finish()
	require.NotNil(t, none.public.Summary.RequiresDeploy, "no deploy needed is an empty list, not null")
	require.Empty(t, none.public.Summary.RequiresDeploy)

	empty := unitPlanned(false)
	empty.finish()
	require.NotNil(t, empty.public.Services)
	require.Empty(t, empty.public.Services)
	require.Equal(t, []string{policy.ScopeProjectsWrite}, empty.public.RequiredScopes, "projects:write is always required")
	require.True(t, empty.public.Valid)
	require.False(t, empty.hasWork)
}

func TestUnitFinishCreateNeedsADeployEvenWithoutChanges(t *testing.T) {
	p := unitPlanned(false)
	p.svcs = []*svcPlan{{
		slug: "api", create: true, rs: &projectconfig.ResolvedService{Slug: "api", Kind: projectconfig.KindImage},
		pl: PlanService{Slug: "api", Action: ActionCreate, Changes: []PlanChange{}},
	}}
	p.finish()
	require.Equal(t, []string{"api"}, p.public.Summary.RequiresDeploy)
	require.Equal(t, 1, p.public.Summary.Create)
	require.Equal(t, 0, p.public.Summary.Adopted)
}

func TestUnitFinishProductionGuard(t *testing.T) {
	for _, tc := range []struct {
		name       string
		production bool
		changes    []PlanChange
		want       string
	}{
		{"production with an immediate change", true, []PlanChange{unitChange("config.port", OpSet, AppliesStaged), unitChange("scale.replicas", OpSet, AppliesImmediate)}, ApprovalRequired},
		{"production with a deferred change", true, []PlanChange{unitChange("backups.nightly", OpAdd, AppliesDeferred)}, ApprovalRequired},
		{"production with staged changes only", true, []PlanChange{unitChange("config.port", OpSet, AppliesStaged)}, ApprovalNotRequired},
		{"production without changes", true, nil, ApprovalNotRequired},
		{"another environment with an immediate change", false, []PlanChange{unitChange("scale.replicas", OpSet, AppliesImmediate)}, ApprovalNotRequired},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := unitPlanned(tc.production)
			p.svcs = []*svcPlan{unitUpdate("web", tc.changes...)}
			p.finish()
			require.Equal(t, tc.want, p.public.Guard.ProductionApproval)
			if tc.want == ApprovalRequired {
				require.NotEmpty(t, p.public.Guard.Reason)
			} else {
				require.Empty(t, p.public.Guard.Reason)
			}
		})
	}

	t.Run("a blocked service contributes nothing", func(t *testing.T) {
		p := unitPlanned(true)
		p.svcs = []*svcPlan{unitUpdate("web", unitChange("scale.replicas", OpSet, AppliesImmediate))}
		p.svcs[0].pl.Action = ActionBlocked
		p.finish()
		require.Equal(t, ApprovalNotRequired, p.public.Guard.ProductionApproval)
	})
}

func TestUnitFinishBlockedAndErroneousPlansAreInvalid(t *testing.T) {
	t.Run("blocked service", func(t *testing.T) {
		p := unitPlanned(false)
		p.svcs = []*svcPlan{
			{slug: "bad", ls: &liveService{}, pl: PlanService{Slug: "bad", Action: ActionBlocked, Blockers: []string{"unknown_server"},
				Changes: []PlanChange{unitChange("scale.replicas", OpSet, AppliesImmediate)}}},
			unitUpdate("good", unitChange("config.port", OpSet, AppliesStaged)),
		}
		p.finish()
		pub := p.public
		require.False(t, pub.Valid, "one blocker anywhere makes the plan invalid")
		require.Equal(t, 0, pub.Summary.Errors)
		require.Equal(t, 1, pub.Summary.Staged, "the blocked service's changes are not counted")
		require.Equal(t, 0, pub.Summary.Immediate)
		require.Equal(t, 0, pub.Summary.Adopted, "a blocked service is not adopted")
		require.Equal(t, []string{"good"}, pub.Summary.RequiresDeploy)
		require.Equal(t, []string{policy.ScopeProjectsWrite}, pub.RequiredScopes, "scopes of a blocked service are not demanded")
	})

	t.Run("errors", func(t *testing.T) {
		p := unitPlanned(false)
		pos := projectconfig.Pos{Line: 2, Column: 1}
		p.errorf("invalid_value", "services.web.port", pos, "boom")
		p.errorf("invalid_value", "services.web.port", pos, "boom")
		p.warnf("w", "services.web", pos, "careful")
		p.finish()
		require.False(t, p.public.Valid)
		require.Equal(t, 1, p.public.Summary.Errors, "identical diagnostics count once")
		require.Equal(t, 1, p.public.Summary.Warnings)
		require.Len(t, p.public.Diagnostics, 2)
	})

	t.Run("warnings alone do not invalidate", func(t *testing.T) {
		p := unitPlanned(false)
		p.warnf("w", "services.web", projectconfig.Pos{}, "careful")
		p.finish()
		require.True(t, p.public.Valid)
	})
}

func unitDomainField(rule projectconfig.DomainRule, changed bool) *fieldPlan {
	return &fieldPlan{
		fv:      projectconfig.FieldValue{ID: "domains.shop.acme.com", Path: `services.web.domains."shop.acme.com"`, Value: rule, Applies: AppliesStaged},
		changed: changed,
	}
}

func TestUnitFinishRequiredScopes(t *testing.T) {
	port := 8080
	opts := &agentprotocol.RouteOptions{Compress: true}
	domainChange := unitChange("domains.shop.acme.com", OpAdd, AppliesStaged)

	for _, tc := range []struct {
		name  string
		build func() *svcPlan
		want  []string
	}{
		{
			"staged config only",
			func() *svcPlan { return unitUpdate("web", unitChange("config.port", OpSet, AppliesStaged)) },
			[]string{"projects:write"},
		},
		{
			"scale and backups need deployments:write",
			func() *svcPlan {
				return unitUpdate("web", unitChange("scale.replicas", OpSet, AppliesImmediate), unitChange("backups.nightly", OpAdd, AppliesImmediate))
			},
			[]string{"projects:write", "deployments:write"},
		},
		{
			"networks need deployments:write",
			func() *svcPlan { return unitUpdate("web", unitChange("networks.shared", OpAdd, AppliesStaged)) },
			[]string{"projects:write", "deployments:write"},
		},
		{
			"uptime needs admin",
			func() *svcPlan { return unitUpdate("web", unitChange("uptime.enabled", OpSet, AppliesImmediate)) },
			[]string{"projects:write", "admin"},
		},
		{
			"a domain rule with options needs proxy:write",
			func() *svcPlan {
				sp := unitUpdate("web", domainChange)
				sp.fields = []*fieldPlan{unitDomainField(projectconfig.DomainRule{Host: "shop.acme.com", Options: opts}, true)}
				return sp
			},
			[]string{"projects:write", "proxy:write"},
		},
		{
			"a domain rule with only a target port does not",
			func() *svcPlan {
				sp := unitUpdate("web", domainChange)
				sp.fields = []*fieldPlan{unitDomainField(projectconfig.DomainRule{Host: "shop.acme.com", TargetPort: &port}, true)}
				return sp
			},
			[]string{"projects:write"},
		},
		{
			"an unchanged rule with options does not",
			func() *svcPlan {
				sp := unitUpdate("web")
				sp.fields = []*fieldPlan{unitDomainField(projectconfig.DomainRule{Host: "shop.acme.com", Options: opts}, false)}
				return sp
			},
			[]string{"projects:write"},
		},
		{
			"a rule that could not be computed does not",
			func() *svcPlan {
				sp := unitUpdate("web")
				f := unitDomainField(projectconfig.DomainRule{Host: "shop.acme.com", Options: opts}, true)
				f.skipped = true
				sp.fields = []*fieldPlan{f}
				return sp
			},
			[]string{"projects:write"},
		},
		{
			"a pruned wildcard domain needs proxy:write",
			func() *svcPlan {
				sp := unitUpdate("web", unitChange("domains.*.acme.com", OpRemove, AppliesStaged))
				sp.removes = []*fieldPlan{{
					fv:     projectconfig.FieldValue{ID: "domains.*.acme.com", Path: "services.web.domains", Applies: AppliesStaged},
					remove: true,
				}}
				return sp
			},
			[]string{"projects:write", "proxy:write"},
		},
		{
			"route config fields need proxy:write",
			func() *svcPlan { return unitUpdate("gw", unitChange("config.route.upstreams", OpSet, AppliesStaged)) },
			[]string{"projects:write", "proxy:write"},
		},
		{
			"creating a route service needs proxy:write even without changes",
			func() *svcPlan {
				return &svcPlan{
					slug: "gw", create: true, rs: &projectconfig.ResolvedService{Slug: "gw", Kind: projectconfig.KindRoute},
					pl: PlanService{Slug: "gw", Action: ActionCreate, Changes: []PlanChange{}},
				}
			},
			[]string{"projects:write", "proxy:write"},
		},
		{
			"creating an image service does not",
			func() *svcPlan {
				return &svcPlan{
					slug: "api", create: true, rs: &projectconfig.ResolvedService{Slug: "api", Kind: projectconfig.KindImage},
					pl: PlanService{Slug: "api", Action: ActionCreate, Changes: []PlanChange{}},
				}
			},
			[]string{"projects:write"},
		},
		{
			"a process scale needs deployments:write",
			func() *svcPlan {
				return unitUpdate("web", unitChange("processes.worker.scale.replicas", OpSet, AppliesImmediate), unitChange("config.processes.worker", OpAdd, AppliesStaged))
			},
			[]string{"projects:write", "deployments:write"},
		},
		{
			"a staged process and its name need projects:write only",
			func() *svcPlan {
				return unitUpdate("web", unitChange("config.processes.worker", OpAdd, AppliesStaged), unitChange("config.processName", OpSet, AppliesStaged), unitChange("config.singleton", OpSet, AppliesStaged))
			},
			[]string{"projects:write"},
		},
		{
			"everything, in policy order",
			func() *svcPlan {
				sp := unitUpdate("web",
					unitChange("uptime.enabled", OpSet, AppliesImmediate),
					unitChange("domains.shop.acme.com", OpAdd, AppliesStaged),
					unitChange("scale.replicas", OpSet, AppliesImmediate),
					unitChange("config.port", OpSet, AppliesStaged),
				)
				sp.fields = []*fieldPlan{unitDomainField(projectconfig.DomainRule{Host: "shop.acme.com", Options: opts}, true)}
				return sp
			},
			[]string{"projects:write", "deployments:write", "proxy:write", "admin"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := unitPlanned(false)
			p.svcs = []*svcPlan{tc.build()}
			p.finish()
			require.Equal(t, tc.want, p.public.RequiredScopes)
		})
	}
}

func TestUnitFinishSharedLayers(t *testing.T) {
	shared := func(id string, state string, present, changed, remove, skipped bool) *fieldPlan {
		return &fieldPlan{
			fv:      projectconfig.FieldValue{ID: id, Path: "variables." + strings.TrimPrefix(strings.TrimPrefix(id, "project."), "environment."), Applies: AppliesStaged},
			desired: fieldView{FP: "sha256:d", Display: "new"}, live: fieldView{FP: "sha256:l", Display: "old"},
			present: present, state: state, changed: changed, remove: remove, skipped: skipped,
		}
	}

	p := unitPlanned(false)
	p.shared = []*sharedPlan{
		{
			layer: layerProject,
			fields: []*fieldPlan{
				shared("project.APP_NAME", StateFileChanged, true, true, false, false),
				shared("project.SAME", StateInSync, true, false, false, false),
				shared("project.BROKEN", StateNew, false, true, false, true),
			},
			removes: []*fieldPlan{shared("project.OLD", StateInSync, true, true, true, false)},
		},
		{
			layer:  layerEnvironment,
			fields: []*fieldPlan{shared("environment.STAGE", StateNew, false, true, false, false)},
		},
	}
	p.finish()
	pub := p.public

	require.Len(t, pub.Shared.Project, 2)
	require.Equal(t, "project.APP_NAME", pub.Shared.Project[0].Field)
	require.Equal(t, OpSet, pub.Shared.Project[0].Op)
	require.Equal(t, ReasonFileChanged, pub.Shared.Project[0].Reason)
	require.Equal(t, "old", pub.Shared.Project[0].From)
	require.Equal(t, "new", pub.Shared.Project[0].To)
	require.Equal(t, "project.OLD", pub.Shared.Project[1].Field)
	require.Equal(t, OpRemove, pub.Shared.Project[1].Op, "removals follow the writes")

	require.Len(t, pub.Shared.Environment, 1)
	require.Equal(t, OpAdd, pub.Shared.Environment[0].Op)
	require.Equal(t, ReasonNew, pub.Shared.Environment[0].Reason)

	require.Equal(t, 3, pub.Summary.Staged)
	require.Equal(t, 1, pub.Summary.Pruned)
	require.True(t, p.hasWork)
	require.True(t, p.needsRole, "a project-layer write needs an owner or admin")
	require.Empty(t, pub.Summary.RequiresDeploy, "shared variables restage their consumers, they are not services")

	envOnly := unitPlanned(false)
	envOnly.shared = []*sharedPlan{{layer: layerEnvironment, fields: []*fieldPlan{shared("environment.STAGE", StateNew, false, true, false, false)}}}
	envOnly.finish()
	require.False(t, envOnly.needsRole)
	require.Empty(t, envOnly.public.Shared.Project)
	require.Len(t, envOnly.public.Shared.Environment, 1)
}

// ---------------------------------------------------------------------------
// planID

func unitPlanIDFixture() *planned {
	svcID := unitUUID(701)
	p := unitPlanned(false)
	p.req = PlanRequest{
		Only:  []string{"services.web", "services.api"},
		Input: Input{File: "schema = 1", Attachments: map[string]string{"a.txt": "1", "b.txt": "2"}},
	}
	p.svcs = []*svcPlan{{
		slug: "web",
		pl: PlanService{
			Slug: "web", ServiceID: &svcID, Generation: 7,
			Changes: []PlanChange{{Path: "services.web.port", Field: "config.port", Op: OpSet, Applies: AppliesStaged, baseFP: "sha256:b", liveFP: "sha256:l", desiredFP: "sha256:d"}},
		},
		fields: []*fieldPlan{
			{fv: projectconfig.FieldValue{ID: "scale.replicas", Applies: AppliesImmediate}, live: fieldView{FP: "sha256:live-scale"}},
			{fv: projectconfig.FieldValue{ID: "config.port", Applies: AppliesStaged}, live: fieldView{FP: "sha256:live-port"}},
			{fv: projectconfig.FieldValue{ID: "uptime.enabled", Applies: AppliesImmediate}, live: fieldView{FP: "sha256:skipped-1"}, skipped: true},
		},
	}}
	p.shared = []*sharedPlan{{layer: layerProject, fields: []*fieldPlan{
		{fv: projectconfig.FieldValue{ID: "project.A"}, live: fieldView{FP: "sha256:sl"}, desired: fieldView{FP: "sha256:sd"}},
	}}}
	return p
}

func TestUnitPlanIDStability(t *testing.T) {
	base := unitPlanIDFixture().planID()
	require.Regexp(t, `^pl_[0-9a-f]{32}$`, base)
	require.Equal(t, base, unitPlanIDFixture().planID(), "the same inputs give the same id")

	changes := map[string]func(p *planned){
		"file sha":           func(p *planned) { p.fileSHA = "another" },
		"environment":        func(p *planned) { p.env.Env.ID = unitUUID(901) },
		"only: one more":     func(p *planned) { p.req.Only = append(p.req.Only, "services.worker") },
		"only: another":      func(p *planned) { p.req.Only = []string{"services.web"} },
		"only: dropped":      func(p *planned) { p.req.Only = nil },
		"prune":              func(p *planned) { p.req.Prune = true },
		"attachment content": func(p *planned) { p.req.Input.Attachments["a.txt"] = "changed" },
		"attachment added":   func(p *planned) { p.req.Input.Attachments["c.txt"] = "3" },
		"attachment removed": func(p *planned) { delete(p.req.Input.Attachments, "b.txt") },
		"attachment renamed": func(p *planned) { p.req.Input.Attachments = map[string]string{"a.txt": "1", "c.txt": "2"} },
		"generation":         func(p *planned) { p.svcs[0].pl.Generation++ },
		"service id":         func(p *planned) { id := unitUUID(702); p.svcs[0].pl.ServiceID = &id },
		"service id dropped": func(p *planned) { p.svcs[0].pl.ServiceID = nil },
		"slug":               func(p *planned) { p.svcs[0].slug = "web2" },
		"service added":      func(p *planned) { p.svcs = append(p.svcs, &svcPlan{slug: "worker", pl: PlanService{Slug: "worker"}}) },
		"service removed":    func(p *planned) { p.svcs = nil },
		"live immediate fp":  func(p *planned) { p.svcs[0].fields[0].live.FP = "sha256:other" },
		"change base fp":     func(p *planned) { p.svcs[0].pl.Changes[0].baseFP = "sha256:other" },
		"change live fp":     func(p *planned) { p.svcs[0].pl.Changes[0].liveFP = "sha256:other" },
		"change desired fp":  func(p *planned) { p.svcs[0].pl.Changes[0].desiredFP = "sha256:other" },
		"change op":          func(p *planned) { p.svcs[0].pl.Changes[0].Op = OpAdd },
		"change field":       func(p *planned) { p.svcs[0].pl.Changes[0].Field = "config.memoryMb" },
		"change added": func(p *planned) {
			p.svcs[0].pl.Changes = append(p.svcs[0].pl.Changes, unitChange("config.image", OpSet, AppliesStaged))
		},
		"change removed": func(p *planned) { p.svcs[0].pl.Changes = nil },
		"immediate field added": func(p *planned) {
			p.svcs[0].fields = append(p.svcs[0].fields, &fieldPlan{fv: projectconfig.FieldValue{ID: "scale.autoscaling.max", Applies: AppliesImmediate}, live: fieldView{FP: "sha256:x"}})
		},
		"shared desired fp": func(p *planned) { p.shared[0].fields[0].desired.FP = "sha256:other" },
		"shared live fp":    func(p *planned) { p.shared[0].fields[0].live.FP = "sha256:other" },
		"shared layer":      func(p *planned) { p.shared[0].layer = layerEnvironment },
		"shared field added": func(p *planned) {
			p.shared[0].fields = append(p.shared[0].fields, &fieldPlan{fv: projectconfig.FieldValue{ID: "project.B"}})
		},
	}
	seen := map[string]string{base: "base"}
	for name, mutate := range changes {
		p := unitPlanIDFixture()
		mutate(p)
		id := p.planID()
		require.NotEqual(t, base, id, "changing %s must change the plan id", name)
		if prev, dup := seen[id]; dup {
			t.Errorf("%s and %s produced the same plan id", name, prev)
		}
		seen[id] = name
	}

	same := map[string]func(p *planned){
		"order of only":                       func(p *planned) { p.req.Only = []string{"services.api", "services.web"} },
		"insertion order of attachments":      func(p *planned) { p.req.Input.Attachments = map[string]string{"b.txt": "2", "a.txt": "1"} },
		"the file text (only its sha counts)": func(p *planned) { p.req.Input.File = "schema = 1\n# comment" },
		"live fp of a staged field":           func(p *planned) { p.svcs[0].fields[1].live.FP = "sha256:other" },
		"live fp of a skipped field":          func(p *planned) { p.svcs[0].fields[2].live.FP = "sha256:other" },
		"summary, diagnostics and guard": func(p *planned) {
			p.public.Summary.Staged = 9
			p.public.Guard.Reason = "x"
			p.warnf("w", "p", projectconfig.Pos{}, "m")
		},
		"change display text": func(p *planned) { p.svcs[0].pl.Changes[0].To = "something else" },
		"scopes":              func(p *planned) { p.scopes.add(policy.ScopeAdmin) },
		"project":             func(p *planned) { p.project.Slug = "other" },
	}
	for name, mutate := range same {
		p := unitPlanIDFixture()
		mutate(p)
		require.Equal(t, base, p.planID(), "%s must not change the plan id", name)
	}
}

// ---------------------------------------------------------------------------
// processes: one service, several processes

const unitProcessesFile = `
schema = 1
server = "fra-1"

[services.web]
image = "nginx"
port = 3000
process_name = "app"
singleton = true

[services.web.domains."metrics.example.com"]
process = "metrics"

[services.web.processes.worker]
command = ["bundle", "exec", "sidekiq"]
stop_grace_seconds = 120

[services.web.processes.worker.scale]
replicas = 2

[services.web.processes.worker.scale.autoscaling]
enabled = true
min = 2
max = 6
target_cpu_percent = 65

[services.web.processes.metrics]
command = ["bin/metrics"]
port = 9100

[services.web.processes.metrics.health_check]
type = "tcp"
`

// The file's processes become catalog fields: the process as a staged unit, its scale as immediate keys.
func TestUnitProcessesResolveToCatalogFields(t *testing.T) {
	pr := parseFor(Input{File: unitProcessesFile}, "")
	require.NotNil(t, pr.res, "diagnostics: %v", pr.diags)
	require.False(t, pr.diags.HasErrors(), "%v", pr.diags)
	rs := pr.res.Services["web"]
	require.NotNil(t, rs)
	field := func(id string) projectconfig.FieldValue {
		t.Helper()
		fv, ok := rs.Fields[id]
		require.True(t, ok, "field %s is missing from %v", id, rs.FieldIDs())
		return fv
	}

	require.Equal(t, "app", field("config.processName").Value)
	require.Equal(t, "services.web.process_name", field("config.processName").Path)
	require.Equal(t, true, field("config.singleton").Value)

	wf := field("config.processes.worker")
	require.Equal(t, AppliesStaged, wf.Applies)
	require.Equal(t, "services.web.processes.worker", wf.Path)
	worker, ok := wf.Value.(projectconfig.ProcessValue)
	require.True(t, ok, "%T", wf.Value)
	require.Equal(t, "worker", worker.Name)
	require.Equal(t, []string{"bundle", "exec", "sidekiq"}, worker.Command)
	require.Equal(t, 120, worker.StopGraceSeconds)
	require.Zero(t, worker.Port)

	metrics, ok := field("config.processes.metrics").Value.(projectconfig.ProcessValue)
	require.True(t, ok)
	require.Equal(t, 9100, metrics.Port)
	require.Equal(t, "tcp", metrics.HealthCheck.Type)

	for id, want := range map[string]any{
		"processes.worker.scale.replicas":                     2,
		"processes.worker.scale.autoscaling.enabled":          true,
		"processes.worker.scale.autoscaling.min":              2,
		"processes.worker.scale.autoscaling.max":              6,
		"processes.worker.scale.autoscaling.targetCpuPercent": 65,
	} {
		fv := field(id)
		require.Equal(t, want, fv.Value, id)
		require.Equal(t, AppliesImmediate, fv.Applies, id)
	}
	require.Equal(t, "services.web.processes.worker.scale.autoscaling.target_cpu_percent", field("processes.worker.scale.autoscaling.targetCpuPercent").Path)
	for id := range rs.Fields {
		require.False(t, strings.HasPrefix(id, "processes.metrics."), "a process without a scale table owns no scale key: %s", id)
	}

	dom, ok := field("domains.metrics.example.com").Value.(projectconfig.DomainRule)
	require.True(t, ok)
	require.NotNil(t, dom.Process)
	require.Equal(t, "metrics", *dom.Process)

	// Every id the file produces is a catalog field with a handler, of the apply kind the catalog names.
	for id, fv := range rs.Fields {
		spec, ok := projectconfig.LookupField(id)
		require.True(t, ok, id)
		require.Equal(t, spec.Applies, fv.Applies, id)
		require.NotNil(t, opsFor(id), id)
	}
}

func TestUnitProcessValueOfIsTheDefaultedValue(t *testing.T) {
	cfg := services.Config{Port: 80, Processes: []services.ProcessConfig{
		{Name: "worker", StartCommand: []string{"bundle", "exec", "sidekiq"}, StopGraceSeconds: 120},
		{Name: "metrics", StartCommand: []string{"bin/metrics"}, Port: 9100, Singleton: true, MemoryMB: 64, CPUs: 0.5},
	}}.WithDefaults()

	require.Equal(t, projectconfig.ProcessValue{
		Name: "worker", Command: []string{"bundle", "exec", "sidekiq"}, StopGraceSeconds: 120,
		HealthCheck: projectconfig.ProcessHealth{Type: "none", IntervalSeconds: 2, TimeoutSeconds: 3, DeadlineSeconds: 120, SuccessThreshold: 2},
	}, processValueOf(cfg, "worker"), "a process without a port has no health check; stop grace, memory and cpus stay as declared")
	require.Equal(t, projectconfig.ProcessValue{
		Name: "metrics", Command: []string{"bin/metrics"}, Port: 9100, Singleton: true, MemoryMB: 64, CPUs: 0.5,
		HealthCheck: projectconfig.ProcessHealth{Type: "http", Path: "/", Port: 9100, IntervalSeconds: 2, TimeoutSeconds: 3, DeadlineSeconds: 120, SuccessThreshold: 2},
	}, processValueOf(cfg, "metrics"), "the health check follows the process' own port")

	require.Equal(t, projectconfig.ProcessValue{Name: "ghost", Command: []string{}}, processValueOf(cfg, "ghost"))
	require.True(t, hasProcess(cfg, "worker"))
	require.False(t, hasProcess(cfg, "ghost"))
	require.False(t, hasProcess(services.Config{}, "worker"))
}

func TestUnitProcessFieldAddsReplacesAndKeepsTheRest(t *testing.T) {
	pc := unitPlanCtx()
	set := func(cfg *services.Config, name string, v projectconfig.ProcessValue) {
		t.Helper()
		f, ok := lookupCfgField("config.processes." + name)
		require.True(t, ok)
		require.NoError(t, f.set(pc, cfg, v))
	}

	cfg := services.Config{Image: "nginx", Port: 80}.WithDefaults()
	set(&cfg, "worker", projectconfig.ProcessValue{Command: []string{"bundle", "exec", "sidekiq"}})
	set(&cfg, "clock", projectconfig.ProcessValue{Command: []string{"clockwork"}, Singleton: true})
	names := func(c services.Config) []string {
		var out []string
		for _, p := range c.WithDefaults().Processes {
			out = append(out, p.Name)
		}
		return out
	}
	require.Equal(t, []string{"clock", "worker"}, names(cfg), "the name comes from the id, and the platform sorts the processes")
	require.Equal(t, 80, cfg.Port, "the service's own process is untouched")

	before := append([]services.ProcessConfig(nil), cfg.Processes...)
	snapshot := cfg
	set(&cfg, "worker", projectconfig.ProcessValue{Command: []string{"bundle", "exec", "sidekiq", "-c", "5"}, StopGraceSeconds: 90})
	require.Equal(t, []string{"clock", "worker"}, names(cfg), "setting an existing process replaces it")
	require.Equal(t, []string{"bundle", "exec", "sidekiq", "-c", "5"}, processValueOf(cfg.WithDefaults(), "worker").Command)
	require.Equal(t, 90, processValueOf(cfg.WithDefaults(), "worker").StopGraceSeconds)
	require.True(t, processValueOf(cfg.WithDefaults(), "clock").Singleton, "the other process stays")
	require.Equal(t, before, snapshot.Processes, "a setter never mutates the slice of the config it was handed")

	dropProcess(&cfg, "clock")
	require.Equal(t, []string{"worker"}, names(cfg))
	require.Equal(t, []string{"clock", "worker"}, names(snapshot), "dropping builds a new slice")
	dropProcess(&cfg, "ghost")
	require.Equal(t, []string{"worker"}, names(cfg))
	dropProcess(&cfg, "worker")
	require.Empty(t, cfg.Processes)
}

func TestUnitProcessNameFieldTreatsTheDefaultAsUnset(t *testing.T) {
	pc := unitPlanCtx()
	f, ok := lookupCfgField("config.processName")
	require.True(t, ok)

	cfg := services.Config{}.WithDefaults()
	require.Equal(t, "web", f.get(pc, cfg), "an unnamed default process is called web")
	require.NoError(t, f.set(pc, &cfg, "jobs"))
	require.Equal(t, "jobs", cfg.ProcessName)
	require.Equal(t, "jobs", f.get(pc, cfg))
	require.NoError(t, f.set(pc, &cfg, "web"))
	require.Empty(t, cfg.ProcessName, "the default name is stored as empty so the staged config does not change")

	// A file that spells the default out plans to zero; a real rename does not.
	ls := &liveService{Cfg: services.Config{}.WithDefaults()}
	live, present := configOps{}.live(pc, ls, "config.processName", "")
	require.True(t, present)
	same, err := configOps{}.desired(pc, ls, projectconfig.FieldValue{ID: "config.processName", Value: "web"})
	require.NoError(t, err)
	require.Equal(t, live.FP, same.FP)
	renamed, err := configOps{}.desired(pc, ls, projectconfig.FieldValue{ID: "config.processName", Value: "app"})
	require.NoError(t, err)
	require.NotEqual(t, live.FP, renamed.FP)
	require.Equal(t, "app", renamed.Display)
}

func TestUnitProcessIsAbsentUntilTheServiceHasIt(t *testing.T) {
	pc := unitPlanCtx()
	plain := &liveService{Cfg: services.Config{Image: "nginx", Port: 80}.WithDefaults()}
	_, present := configOps{}.live(pc, plain, "config.processes.worker", "")
	require.False(t, present, "a service without the process reports it absent: the plan adds it")

	withIt := &liveService{Cfg: services.Config{Image: "nginx", Port: 80, Processes: []services.ProcessConfig{{Name: "worker", StartCommand: []string{"w"}}}}.WithDefaults()}
	lv, present := configOps{}.live(pc, withIt, "config.processes.worker", "")
	require.True(t, present)
	require.Equal(t, `["w"]`, lv.Display)
	_, present = configOps{}.live(pc, withIt, "config.processes.other", "")
	require.False(t, present)

	require.True(t, absentOnKind("config.processes.worker", services.Config{}))
	require.True(t, absentOnKind("config.processes.worker", services.Config{Processes: []services.ProcessConfig{{Name: "other"}}}))
	require.False(t, absentOnKind("config.processes.worker", services.Config{Processes: []services.ProcessConfig{{Name: "worker"}}}))
}

func TestUnitProcessDesiredEqualsLiveWhenTheFileWritesTheDefaults(t *testing.T) {
	pc := unitPlanCtx()
	ls := &liveService{Cfg: services.Config{Image: "nginx", Port: 80, Processes: []services.ProcessConfig{
		{Name: "worker", StartCommand: []string{"bundle", "exec", "sidekiq"}},
		{Name: "metrics", StartCommand: []string{"bin/metrics"}, Port: 9100},
	}}.WithDefaults()}

	fp := func(id string, v projectconfig.ProcessValue) (live, desired string) {
		t.Helper()
		lv, present := configOps{}.live(pc, ls, id, "")
		require.True(t, present, id)
		dv, err := configOps{}.desired(pc, ls, projectconfig.FieldValue{ID: id, Value: v})
		require.NoError(t, err, id)
		return lv.FP, dv.FP
	}

	// Leaving everything but the command out, or writing the platform's defaults, is the live process.
	l, d := fp("config.processes.worker", projectconfig.ProcessValue{Name: "worker", Command: []string{"bundle", "exec", "sidekiq"}})
	require.Equal(t, l, d)
	l, d = fp("config.processes.worker", projectconfig.ProcessValue{
		Name: "worker", Command: []string{"bundle", "exec", "sidekiq"},
		HealthCheck: projectconfig.ProcessHealth{Type: "none", IntervalSeconds: 2, TimeoutSeconds: 3, DeadlineSeconds: 120, SuccessThreshold: 2},
	})
	require.Equal(t, l, d)
	l, d = fp("config.processes.metrics", projectconfig.ProcessValue{Name: "metrics", Command: []string{"bin/metrics"}, Port: 9100, HealthCheck: projectconfig.ProcessHealth{Type: "http", Path: "/"}})
	require.Equal(t, l, d, "the health check port defaults to the process' own port")

	// A real difference is one.
	for name, v := range map[string]projectconfig.ProcessValue{
		"command":    {Name: "worker", Command: []string{"bundle", "exec", "sidekiq", "-c", "5"}},
		"port":       {Name: "worker", Command: []string{"bundle", "exec", "sidekiq"}, Port: 4000},
		"singleton":  {Name: "worker", Command: []string{"bundle", "exec", "sidekiq"}, Singleton: true},
		"stop grace": {Name: "worker", Command: []string{"bundle", "exec", "sidekiq"}, StopGraceSeconds: 120},
		"memory":     {Name: "worker", Command: []string{"bundle", "exec", "sidekiq"}, MemoryMB: 128},
		"health":     {Name: "worker", Command: []string{"bundle", "exec", "sidekiq"}, HealthCheck: projectconfig.ProcessHealth{IntervalSeconds: 9}},
	} {
		l, d := fp("config.processes.worker", v)
		require.NotEqual(t, l, d, name)
	}

	// The same command in a service that lacks the process is still a different (absent) value.
	other := &liveService{Cfg: services.Config{Image: "nginx", Port: 80}.WithDefaults()}
	dv, err := configOps{}.desired(pc, other, projectconfig.FieldValue{ID: "config.processes.worker", Value: projectconfig.ProcessValue{Name: "worker", Command: []string{"w"}}})
	require.NoError(t, err)
	require.Equal(t, `["w"]`, dv.Display)
}

func TestUnitProcessDisplay(t *testing.T) {
	for _, tc := range []struct {
		name string
		v    projectconfig.ProcessValue
		want string
	}{
		{"command only", projectconfig.ProcessValue{Command: []string{"bundle", "exec", "sidekiq"}}, `["bundle","exec","sidekiq"]`},
		{"no command", projectconfig.ProcessValue{}, `[]`},
		{"port", projectconfig.ProcessValue{Command: []string{"m"}, Port: 9100}, `["m"] port 9100`},
		{"singleton", projectconfig.ProcessValue{Command: []string{"c"}, Singleton: true}, `["c"] singleton`},
		{"grace", projectconfig.ProcessValue{Command: []string{"w"}, StopGraceSeconds: 120}, `["w"] grace 120s`},
		{"everything in order", projectconfig.ProcessValue{Command: []string{"w"}, Port: 3001, Singleton: true, StopGraceSeconds: 90}, `["w"] port 3001 singleton grace 90s`},
	} {
		require.Equal(t, tc.want, processDisplay(tc.v), tc.name)
		require.Equal(t, tc.want, display(tc.v), tc.name+" through display")
	}
}

func TestUnitSplitProcessScaleID(t *testing.T) {
	for _, tc := range []struct {
		id, process, key string
		ok               bool
	}{
		{"processes.worker.scale.replicas", "worker", "replicas", true},
		{"processes.mail-worker.scale.autoscaling.max", "mail-worker", "autoscaling.max", true},
		{"processes.w1.scale.autoscaling.targetCpuPercent", "w1", "autoscaling.targetCpuPercent", true},
		{"processes.worker", "", "", false},
		{"config.processes.worker", "", "", false},
		{"scale.replicas", "", "", false},
		{"", "", "", false},
	} {
		process, key, ok := splitProcessScaleID(tc.id)
		require.Equal(t, tc.ok, ok, tc.id)
		if tc.ok {
			require.Equal(t, tc.process, process, tc.id)
			require.Equal(t, tc.key, key, tc.id)
		}
	}

	// The scale ids of a process never reach the handler of the service's own scale, and the
	// staged process id never reaches the scale handler.
	require.IsType(t, processScaleOps{}, opsFor("processes.worker.scale.replicas"))
	require.IsType(t, scaleOps{}, opsFor("scale.replicas"))
	require.IsType(t, configOps{}, opsFor("config.processes.worker"))
}

func TestUnitProcessScaleOps(t *testing.T) {
	pc := unitPlanCtx()
	scaled := json.RawMessage(`{"enabled":true,"min":2,"max":6,"targetCpuPercent":65}`)
	ls := &liveService{ProcScales: []db.ServiceProcessScale{{Process: "worker", Replicas: 3, Autoscaling: scaled}}}
	ops := processScaleOps{}

	for id, want := range map[string]any{
		"processes.worker.scale.replicas":                     3,
		"processes.worker.scale.autoscaling.enabled":          true,
		"processes.worker.scale.autoscaling.min":              2,
		"processes.worker.scale.autoscaling.max":              6,
		"processes.worker.scale.autoscaling.targetCpuPercent": 65,
	} {
		v, present := ops.live(pc, ls, id, "")
		require.True(t, present, id)
		require.Equal(t, projectconfig.Fingerprint(want), v.FP, id)
		require.Equal(t, display(want), v.Display, id)
	}

	// A process without a row is a process that was never scaled: one replica, the default autoscaling.
	def := services.DefaultAutoscaling()
	for id, want := range map[string]any{
		"processes.clock.scale.replicas":                     1,
		"processes.clock.scale.autoscaling.enabled":          def.Enabled,
		"processes.clock.scale.autoscaling.min":              def.Min,
		"processes.clock.scale.autoscaling.max":              def.Max,
		"processes.clock.scale.autoscaling.targetCpuPercent": def.TargetCPUPercent,
	} {
		v, present := ops.live(pc, ls, id, "")
		require.True(t, present, id)
		require.Equal(t, projectconfig.Fingerprint(want), v.FP, id)
	}
	_, present := ops.live(pc, &liveService{}, "processes.worker.scale.replicas", "")
	require.True(t, present, "a service without any row has the defaults too")

	for _, id := range []string{"processes.worker.scale.bogus", "processes.worker.replicas", "scale.replicas", "config.processes.worker"} {
		_, present := ops.live(pc, ls, id, "")
		require.False(t, present, id)
	}

	// The file's value is compared with the live one by fingerprint, as for the service's own scale.
	dv, err := ops.desired(pc, ls, projectconfig.FieldValue{ID: "processes.worker.scale.replicas", Value: 3})
	require.NoError(t, err)
	lv, _ := ops.live(pc, ls, "processes.worker.scale.replicas", "")
	require.Equal(t, lv.FP, dv.FP)
	dv, err = ops.desired(pc, ls, projectconfig.FieldValue{ID: "processes.worker.scale.replicas", Value: 4})
	require.NoError(t, err)
	require.NotEqual(t, lv.FP, dv.FP)
	require.Equal(t, "4", dv.Display)
	dv, err = ops.desired(pc, ls, projectconfig.FieldValue{ID: "processes.worker.scale.autoscaling.enabled", Value: false})
	require.NoError(t, err)
	require.Equal(t, "false", dv.Display)
	_, err = ops.desired(pc, ls, projectconfig.FieldValue{ID: "processes.worker.scale.replicas", Value: "two"})
	var re *refError
	require.True(t, errors.As(err, &re), "%v", err)
	require.Equal(t, "wrong_type", re.Code)
}

func TestUnitDefaultProcessNameOf(t *testing.T) {
	named := &liveService{Cfg: services.Config{ProcessName: "app"}}
	withFile := func(name any) *planCtx {
		return &planCtx{rs: &projectconfig.ResolvedService{Fields: map[string]projectconfig.FieldValue{
			"config.processName": {ID: "config.processName", Value: name},
		}}}
	}
	for _, tc := range []struct {
		name string
		pc   *planCtx
		ls   *liveService
		want string
	}{
		{"nothing known", &planCtx{}, nil, "web"},
		{"a live service without a name", &planCtx{}, &liveService{}, "web"},
		{"the live name", &planCtx{}, named, "app"},
		{"the file wins over the live name", withFile("jobs"), named, "jobs"},
		{"the file's default spelled out", withFile("web"), named, "web"},
		{"an empty name in the file is the default", withFile(""), named, "web"},
		{"a file that does not mention it keeps the live name", &planCtx{rs: &projectconfig.ResolvedService{Fields: map[string]projectconfig.FieldValue{}}}, named, "app"},
	} {
		require.Equal(t, tc.want, defaultProcessNameOf(tc.pc, tc.ls), tc.name)
	}
}

// A domain of a service with processes names its process; the service's own process is stored as "".
func TestUnitDomainProcessPart(t *testing.T) {
	pc := unitPlanCtx()
	pc.rs = &projectconfig.ResolvedService{Fields: map[string]projectconfig.FieldValue{
		"config.processName": {ID: "config.processName", Value: "app"},
	}}
	ls := &liveService{
		Cfg: services.Config{ProcessName: "app", Port: 8080, Processes: []services.ProcessConfig{{Name: "metrics", StartCommand: []string{"m"}, Port: 9100}}}.WithDefaults(),
		Domains: []db.Domain{
			{ID: unitUUID(1), Hostname: "app.example.com", TargetPort: 8080},
			{ID: unitUUID(2), Hostname: "metrics.example.com", TargetPort: 9100, Component: "metrics"},
		},
	}
	rule := func(host, process string) projectconfig.FieldValue {
		return projectconfig.FieldValue{ID: "domains." + host, Value: projectconfig.DomainRule{Host: host, Process: &process}}
	}
	check := func(host, process string, same bool) {
		t.Helper()
		dv, err := domainOps{}.desired(pc, ls, rule(host, process))
		require.NoError(t, err)
		require.Equal(t, "process", dv.Proj)
		lv, present := domainOps{}.live(pc, ls, "domains."+host, dv.Proj)
		require.True(t, present)
		if same {
			require.Equal(t, lv.FP, dv.FP, "%s routes to %q", host, process)
		} else {
			require.NotEqual(t, lv.FP, dv.FP, "%s does not route to %q", host, process)
		}
	}
	check("metrics.example.com", "metrics", true)
	check("metrics.example.com", "app", false)
	check("app.example.com", "app", true) // the service's own process, whatever its name, is stored as ""
	check("app.example.com", "metrics", false)

	// Without the file's name the live one counts, and "web" is only the default of an unnamed service.
	pc.rs = nil
	check("app.example.com", "app", true)
	plain := &liveService{Cfg: services.Config{Port: 8080}.WithDefaults(), Domains: []db.Domain{{ID: unitUUID(3), Hostname: "a.example.com", TargetPort: 8080}}}
	dv, err := domainOps{}.desired(pc, plain, rule("a.example.com", "web"))
	require.NoError(t, err)
	lv, _ := domainOps{}.live(pc, plain, "domains.a.example.com", dv.Proj)
	require.Equal(t, lv.FP, dv.FP)

	require.Equal(t, []string{"process"}, ruleParts(projectconfig.DomainRule{Host: "a.example.com", Process: new("metrics")}))
	require.Equal(t, []string{"targetPort", "process"}, ruleParts(projectconfig.DomainRule{Host: "a.example.com", TargetPort: new(9100), Process: new("metrics")}))
}

func TestUnitUnmanagedProcessesAreCounted(t *testing.T) {
	ls := &liveService{Cfg: services.Config{Image: "nginx", Processes: []services.ProcessConfig{
		{Name: "worker", StartCommand: []string{"w"}}, {Name: "clock", StartCommand: []string{"c"}},
	}}.WithDefaults()}
	rs := &projectconfig.ResolvedService{Fields: map[string]projectconfig.FieldValue{"config.processes.worker": {}}}
	require.Equal(t, map[string]int{"processes": 1}, unmanagedCounts(ls, rs), "the process the file does not name is counted, never listed")

	rs.Fields["config.processes.clock"] = projectconfig.FieldValue{}
	require.Nil(t, unmanagedCounts(ls, rs))

	// The scale of a process is not a member: it follows its process.
	delete(rs.Fields, "config.processes.clock")
	rs.Fields["processes.clock.scale.replicas"] = projectconfig.FieldValue{}
	require.Equal(t, map[string]int{"processes": 1}, unmanagedCounts(ls, rs))

	require.Nil(t, unmanagedCounts(&liveService{Cfg: services.Config{Image: "nginx"}.WithDefaults()}, &projectconfig.ResolvedService{}), "a service without processes counts none")
}
