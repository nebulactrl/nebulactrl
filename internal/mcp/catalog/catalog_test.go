package catalog

import (
	"encoding/json"
	"os"
	"regexp"
	"sort"
	"strings"
	"testing"
)

type specOp struct {
	ID        string
	Method    string
	Path      string
	Tag       string
	Public    bool
	HasBearer bool
	Scopes    []string
	SSE       bool
}

// loadSpec reads every operation of openapi/openapi.json. It is the source of
// truth: the file is regenerated from the huma registrations.
func loadSpec(t *testing.T) []specOp {
	t.Helper()
	raw, err := os.ReadFile("../../../openapi/openapi.json")
	if err != nil {
		t.Fatalf("read spec: %v", err)
	}
	var doc struct {
		Paths map[string]map[string]struct {
			OperationID string                                                  `json:"operationId"`
			Tags        []string                                                `json:"tags"`
			Security    []map[string][]string                                   `json:"security"`
			Responses   map[string]struct{ Content map[string]json.RawMessage } `json:"responses"`
		} `json:"paths"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("parse spec: %v", err)
	}
	var ops []specOp
	for path, item := range doc.Paths {
		for method, o := range item {
			switch method {
			case "get", "post", "put", "patch", "delete":
			default:
				continue
			}
			op := specOp{ID: o.OperationID, Method: strings.ToUpper(method), Path: path}
			if len(o.Tags) > 0 {
				op.Tag = o.Tags[0]
			}
			if len(o.Security) == 1 && len(o.Security[0]) == 0 {
				op.Public = true
			}
			for _, s := range o.Security {
				if sc, ok := s["bearer"]; ok {
					op.HasBearer = true
					op.Scopes = sc
				}
			}
			for code, r := range o.Responses {
				if strings.HasPrefix(code, "2") {
					if _, ok := r.Content["text/event-stream"]; ok {
						op.SSE = true
					}
				}
			}
			ops = append(ops, op)
		}
	}
	sort.Slice(ops, func(i, j int) bool { return ops[i].ID < ops[j].ID })
	return ops
}

func TestManifestCoversSpec(t *testing.T) {
	ops := loadSpec(t)
	seen := map[string]int{}
	for _, o := range ops {
		if o.ID == "" {
			t.Errorf("%s %s has no operationId", o.Method, o.Path)
			continue
		}
		seen[o.ID]++
	}
	for id, n := range seen {
		if n != 1 {
			t.Errorf("operationId %q appears %d times in the spec", id, n)
		}
		if _, ok := Manifest[id]; !ok {
			t.Errorf("operation %q is in the spec but not decided in the catalog", id)
		}
	}
	for id := range Manifest {
		if seen[id] == 0 {
			t.Errorf("stale catalog entry %q: not in the spec", id)
		}
	}
	for id, e := range Manifest {
		if e.OperationID() != id {
			t.Errorf("entry under key %q reports operation %q", id, e.OperationID())
		}
	}
	if got, want := len(Manifest), len(seen); got != want {
		t.Errorf("manifest has %d entries, spec has %d operations", got, want)
	}
	if got := len(All()); got != len(Manifest) {
		t.Errorf("All() = %d entries, want %d", got, len(Manifest))
	}
}

func TestLookup(t *testing.T) {
	e, ok := Lookup("listProjects")
	if !ok {
		t.Fatal("listProjects not found")
	}
	if c, isCur := e.(Curated); !isCur || c.Name != "project_list" {
		t.Fatalf("listProjects = %#v", e)
	}
	if _, ok := Lookup("nope"); ok {
		t.Fatal("unknown operation found")
	}
	if Allowed("login") || !Allowed("listProjects") || !Allowed("getBackup") || Allowed("nope") {
		t.Fatal("Allowed disagrees with the manifest")
	}
}

// TestTeamSliceIsAReadOnlyGeneric pins that a team's own slice view is readable
// over MCP (it holds no key, bucket or address), as a read-only generic operation.
func TestTeamSliceIsAReadOnlyGeneric(t *testing.T) {
	e, ok := Lookup("getTeamSlice")
	if !ok {
		t.Fatal("getTeamSlice is not classified")
	}
	g, isGeneric := e.(Generic)
	if !isGeneric {
		t.Fatalf("getTeamSlice = %#v, want a generic entry", e)
	}
	if g.Hints != hRead {
		t.Errorf("getTeamSlice hints = %#v, want read-only", g.Hints)
	}
	if !Allowed("getTeamSlice") {
		t.Error("getTeamSlice must be allowed")
	}
}

var snakeCase = regexp.MustCompile(`^[a-z][a-z0-9]*(_[a-z0-9]+)*$`)

// reservedTools are composites and meta-tools owned by internal/mcp; a curated
// name must never collide with one.
var reservedTools = []string{
	"get_overview", "find_issues", "diagnose_service", "explain_failed_deployment", "diagnose_server",
	"tail_logs", "search_resources", "review_changes", "stage_changes", "apply_changes",
	"rollback_service", "add_database", "setup_domain", "verify_service", "get_recent_changes",
	"sync_config",
	"find_operation", "call_operation",
}

// nativeTools have hand-written handlers and verb_noun names.
var nativeTools = []string{
	"run_command", "get_task_run", "list_templates", "plan_template", "install_template",
	"list_backup_destinations", "list_backups", "create_backup", "restore_backup",
}

func TestCuratedNamesAndDescriptions(t *testing.T) {
	names := map[string]string{}
	native := map[string]bool{}
	reserved := map[string]bool{}
	for _, n := range reservedTools {
		reserved[n] = true
	}
	for _, c := range CuratedTools() {
		if !snakeCase.MatchString(c.Name) {
			t.Errorf("%s: tool name %q is not snake_case", c.Op, c.Name)
		}
		if prev, dup := names[c.Name]; dup {
			t.Errorf("tool name %q is used by %s and %s", c.Name, prev, c.Op)
		}
		names[c.Name] = c.Op
		if reserved[c.Name] {
			t.Errorf("%s: tool name %q collides with a composite or meta-tool", c.Op, c.Name)
		}
		if !c.Toolset.Valid() {
			t.Errorf("%s: invalid toolset %q", c.Op, c.Toolset)
		}
		if len(c.Description) < 80 {
			t.Errorf("%s: description of %s is too short to guide a model (%d chars)", c.Op, c.Name, len(c.Description))
		}
		if strings.Contains(c.Description, "TODO") {
			t.Errorf("%s: description has a TODO", c.Op)
		}
		if c.ProdGuard && !strings.Contains(c.Description, "Production guard") {
			t.Errorf("%s: ProdGuard tool %s does not describe the guard", c.Op, c.Name)
		}
		if !c.ProdGuard && strings.Contains(c.Description, "Production guard") {
			t.Errorf("%s: tool %s describes a guard its entry does not declare", c.Op, c.Name)
		}
		if c.Confirm != nil && !strings.Contains(c.Description, c.Confirm.Field) {
			t.Errorf("%s: description of %s does not mention its confirm argument %q", c.Op, c.Name, c.Confirm.Field)
		}
		if c.Native {
			native[c.Name] = true
		}
	}
	if len(native) != len(nativeTools) {
		t.Errorf("native tools = %v, want %v", native, nativeTools)
	}
	for _, n := range nativeTools {
		if !native[n] {
			t.Errorf("native tool %q is missing or not marked Native", n)
		}
	}
}

func TestToolBudget(t *testing.T) {
	c := Count()
	if c.Curated < 45 || c.Curated > 65 {
		t.Errorf("curated tools = %d, want about 50 (45..65)", c.Curated)
	}
	if listed := c.Curated + len(reservedTools); listed > 80 {
		t.Errorf("a full token would list %d tools, want at most 80", listed)
	}
	for _, ts := range Toolsets() {
		if ts == ToolsetCore {
			continue
		}
		if c.CuratedToolsets[ts] == 0 {
			t.Errorf("toolset %s has no curated tool", ts)
		}
	}
}

func TestEveryDenyHasReason(t *testing.T) {
	for id, e := range Manifest {
		d, ok := e.(Deny)
		if !ok {
			continue
		}
		if !d.Reason.Valid() {
			t.Errorf("%s: deny reason %q is not in the closed set", id, d.Reason)
		}
		if strings.TrimSpace(d.Why) == "" {
			t.Errorf("%s: deny has no Why", id)
		}
	}
	// Every reason is used at least once, so a reason does not rot.
	got := Count().DenyReasons
	for _, r := range DenyReasons() {
		if got[r] == 0 {
			t.Errorf("deny reason %q is unused", r)
		}
	}
}

func TestEveryAllowedEntryHasToolset(t *testing.T) {
	for id, e := range Manifest {
		if g, ok := e.(Generic); ok && !g.Toolset.Valid() {
			t.Errorf("%s: invalid toolset %q", id, g.Toolset)
		}
	}
}

// TestDenyListMinimum pins the minimum deny list by operation and
// by class, so loosening it takes a deliberate test edit.
func TestDenyListMinimum(t *testing.T) {
	mustDeny := map[string]DenyReason{
		"login":                          ReasonAuthPlumbing,
		"verifySecondFactor":             ReasonAuthPlumbing,
		"passkeyLogin":                   ReasonAuthPlumbing,
		"requestPasswordReset":           ReasonAuthPlumbing,
		"startProviderLogin":             ReasonAuthPlumbing,
		"createToken":                    ReasonMintsCredential,
		"revokeToken":                    ReasonAuthPlumbing,
		"createEnrollmentToken":          ReasonMintsCredential,
		"createMemberPasswordResetLink":  ReasonMintsCredential,
		"rotateDeployHook":               ReasonMintsCredential,
		"setDeployHook":                  ReasonMintsCredential,
		"revealDatabaseExport":           ReasonRevealsSecret,
		"revealDatabasePublicConnection": ReasonRevealsSecret,
		"revealVolumeBackupRecoveryKey":  ReasonRevealsSecret,
		"updateInstanceBackup":           ReasonInstanceAdmin,
		"getInstanceServices":            ReasonInstanceAdmin,
		"updateInstanceServices":         ReasonInstanceAdmin,
		"checkInstanceServices":          ReasonInstanceAdmin,
		"updateTeamSliceQuota":           ReasonInstanceAdmin,
		"purgeTeamSlice":                 ReasonInstanceAdmin,
		"updateInstance":                 ReasonInstanceAdmin,
		"createAuthProvider":             ReasonInstanceAdmin,
		"updateAuthSettings":             ReasonInstanceAdmin,
		"setServerUnrestricted":          ReasonHostRoot,
		"setServerHostShell":             ReasonHostRoot,
		"applyServerHardening":           ReasonHostRoot,
		"applyServerPatches":             ReasonHostRoot,
		"rebootServer":                   ReasonHostRoot,
		"upgradeServerAgent":             ReasonHostRoot,
		"upgradeServerAgents":            ReasonHostRoot,
		"updateInstanceUpgrade":          ReasonInstanceAdmin,
		"putCloudflareIntegration":       ReasonInstanceAdmin,
		"putTailscaleIntegration":        ReasonInstanceAdmin,
		"createDnsCredential":            ReasonInstanceAdmin,
		"createRegistry":                 ReasonInstanceAdmin,
		"createGitConnection":            ReasonInstanceAdmin,
		"updateGitConnection":            ReasonInstanceAdmin,
		"createSecretManager":            ReasonInstanceAdmin,
		"updateSecretManager":            ReasonInstanceAdmin,
		"updateTeamMember":               ReasonInstanceAdmin,
		"removeTeamMember":               ReasonInstanceAdmin,
		"getServiceLogs":                 ReasonStreaming,
		"getEnvironmentLogs":             ReasonStreaming,
		"getDeploymentLogs":              ReasonStreaming,
		"streamEvents":                   ReasonStreaming,
		"deployService":                  ReasonSuperseded,
		"deployStaged":                   ReasonSuperseded,
		"rollbackDeployment":             ReasonSuperseded,
		"getServiceCentralLogs":          ReasonSuperseded,
		"combineServices":                ReasonPersonOnly,
		"abortCombine":                   ReasonPersonOnly,
		"retryCombine":                   ReasonPersonOnly,
	}
	for id, want := range mustDeny {
		e, ok := Manifest[id]
		if !ok {
			t.Errorf("%s: not in the manifest", id)
			continue
		}
		d, ok := e.(Deny)
		if !ok {
			t.Errorf("%s must be denied (%s), is %s", id, want, e.Kind())
			continue
		}
		if d.Reason != want {
			t.Errorf("%s denied as %s, want %s", id, d.Reason, want)
		}
	}

	for _, o := range loadSpec(t) {
		e := Manifest[o.ID]
		if e == nil {
			continue
		}
		// Whole classes.
		switch {
		case o.Tag == "auth", o.Tag == "setup", o.Tag == "instance":
			if e.Kind() != KindDeny {
				t.Errorf("%s (tag %s) must be denied", o.ID, o.Tag)
			}
		case o.Tag == "me" && o.ID != "getMe":
			if e.Kind() != KindDeny {
				t.Errorf("%s (own account) must be denied", o.ID)
			}
		}
		if strings.HasPrefix(o.ID, "reveal") && e.Kind() != KindDeny {
			t.Errorf("%s reveals a secret and must be denied", o.ID)
		}
		if o.SSE && e.Kind() != KindDeny {
			t.Errorf("%s streams server-sent events and must be denied", o.ID)
		}
		if o.Public && e.Kind() != KindDeny {
			t.Errorf("%s is a public operation and must be denied", o.ID)
		}
	}
}

// TestScopesPublished checks the spec carries the scopes the registry filters
// on (api.WithScopes publishes them as the operation's bearer security).
func TestScopesPublished(t *testing.T) {
	for _, o := range loadSpec(t) {
		if Allowed(o.ID) && !o.HasBearer {
			t.Errorf("allowed operation %s declares no bearer scopes in the spec", o.ID)
		}
	}
}

// readOnlyPosts are the POST operations that change nothing.
var readOnlyPosts = map[string]bool{
	"planDatabaseUpgrade": true, "planDeployment": true, "planServerProxy": true, "planServerDrain": true,
	"planComposeImport": true, "planTemplate": true, "previewBuildPlan": true, "previewEnvTemplates": true,
	"validateVariable": true, "checkStack": true, "previewTaskSchedule": true, "planCombineServices": true,
}

// deleteNotDestructive are DELETE operations whose effect is trivially
// reversible or removes no data.
var deleteNotDestructive = map[string]bool{
	"deleteLink": true, "detachServiceNetwork": true, "disableDatabasePublicPort": true,
}

func hintsOf(e Entry) (Hints, bool) {
	switch t := e.(type) {
	case Curated:
		return t.Hints, true
	case Generic:
		return t.Hints, true
	}
	return Hints{}, false
}

// TestHintsMatchOperation is the annotation table test: all hints are checked
// against the HTTP method and the known read-only and destructive sets.
func TestHintsMatchOperation(t *testing.T) {
	for _, o := range loadSpec(t) {
		e, ok := Manifest[o.ID]
		if !ok {
			continue
		}
		h, ok := hintsOf(e)
		if !ok {
			continue
		}
		if h.ReadOnly && h.Destructive {
			t.Errorf("%s: readOnly and destructive are exclusive", o.ID)
		}
		if h.ReadOnly && !h.Idempotent {
			t.Errorf("%s: a read is idempotent", o.ID)
		}
		switch o.Method {
		case "GET":
			if !h.ReadOnly {
				t.Errorf("%s: GET must be readOnly", o.ID)
			}
		case "DELETE":
			if h.ReadOnly || !h.Idempotent {
				t.Errorf("%s: DELETE must be a non-readOnly idempotent call (%+v)", o.ID, h)
			}
			if h.Destructive == deleteNotDestructive[o.ID] {
				t.Errorf("%s: DELETE destructive=%v, want %v", o.ID, h.Destructive, !deleteNotDestructive[o.ID])
			}
		case "PUT", "PATCH":
			if h.ReadOnly {
				t.Errorf("%s: %s must not be readOnly", o.ID, o.Method)
			}
		case "POST":
			if h.ReadOnly != readOnlyPosts[o.ID] {
				t.Errorf("%s: POST readOnly=%v, want %v", o.ID, h.ReadOnly, readOnlyPosts[o.ID])
			}
		}
	}
}

// prodGuarded lists the operations the domain services guard
// (deployments.Deploy, DeployStaged and Rollback are denied above and reached
// through composites). The operations the MCP dispatch guards itself are the
// keys of central (prod.go).
var prodGuarded = map[string]bool{
	"stopService": true, "restartService": true, "deleteService": true, "restoreBackup": true,
	"runServiceTask": true, "runServiceCommand": true, "deleteVolume": true, "deleteDomain": true,
	"deleteEnvironment": true, "updateEnvironment": true, "deleteProject": true,
	"restoreVolumeBackup": true, "upgradeDatabase": true, "rotateDatabaseCredentials": true,
	"setServiceMaintenance": true, "setServiceScaling": true, "deleteTemplateInstance": true,
	"importCompose": true, "cancelDeployment": true, "startServerDrain": true, "moveService": true, "setProcessScaling": true,
}

func TestProdGuardMatchesGuardedList(t *testing.T) {
	for id, e := range Manifest {
		var guard bool
		switch t := e.(type) {
		case Curated:
			guard = t.ProdGuard
		case Generic:
			guard = t.ProdGuard
		default:
			continue
		}
		_, isCentral := CentralGuard(id)
		if isCentral && prodGuarded[id] {
			t.Errorf("%s: guarded both in its domain service and centrally (the approval would be asked twice)", id)
		}
		if want := prodGuarded[id] || isCentral; guard != want {
			t.Errorf("%s: ProdGuard=%v, prodGuarded and central say %v", id, guard, want)
		}
	}
	for id := range prodGuarded {
		if _, ok := Manifest[id]; !ok {
			t.Errorf("guarded operation %s is not in the manifest", id)
		}
	}
	for id := range central {
		if _, ok := Manifest[id]; !ok {
			t.Errorf("centrally guarded operation %s is not in the manifest", id)
		}
	}
	// A guarded tool never relies on a model-made confirm: Confirm is only an
	// extra echo.
	for _, c := range CuratedTools() {
		if c.ProdGuard && c.Confirm != nil && c.Confirm.Field == "confirmProduction" {
			t.Errorf("%s: confirmProduction is not accepted for production", c.Name)
		}
	}
}

// prodScopedParam matches the path parameters that identify a resource of an
// environment (a service, its domain, its backups, …).
var prodScopedParam = regexp.MustCompile(`\{(serviceId|environmentId|projectId|domainId|volumeId|backupId|scheduleId|taskId|retiredId|routeId|linkId|previewId|instanceId|deploymentId|restoreId)\}`)

func guardOf(e Entry) bool {
	switch t := e.(type) {
	case Curated:
		return t.ProdGuard
	case Generic:
		return t.ProdGuard
	}
	return false
}

// TestEveryProductionMutationIsGuardedDeniedOrExempt fails for an allowed,
// mutating operation on a production-scoped id that is neither guarded (in its
// domain service, or centrally by the MCP dispatch) nor denied nor listed with
// the reason it needs no approval. It keeps gaps like task, git-link,
// clone, DNS and backup deletion from opening unnoticed.
func TestEveryProductionMutationIsGuardedDeniedOrExempt(t *testing.T) {
	for _, o := range loadSpec(t) {
		if o.Method == "GET" {
			continue
		}
		e, ok := Manifest[o.ID]
		if !ok || e.Kind() == KindDeny {
			continue
		}
		if h, _ := hintsOf(e); h.ReadOnly {
			continue
		}
		if !prodScopedParam.MatchString(o.Path) {
			continue
		}
		_, exempt := ProdExempt(o.ID)
		switch guard := guardOf(e); {
		case guard && exempt:
			t.Errorf("%s: guarded and also listed as exempt", o.ID)
		case !guard && !exempt:
			t.Errorf("%s %s mutates a production-scoped resource but is neither guarded, denied nor exempt: add it to central (prod.go), deny it, or list it in prodExempt with the reason", o.Method, o.ID)
		}
	}
	for id := range prodExempt {
		e, ok := Manifest[id]
		if !ok {
			t.Errorf("exempt operation %s is not in the manifest", id)
			continue
		}
		if e.Kind() == KindDeny {
			t.Errorf("exempt operation %s is denied", id)
		}
	}
}

// TestCentralGuardsMatchTheirOperations checks each centrally guarded
// operation: its target id is a parameter of the operation, its Show and When
// arguments exist in the operation's input, and it carries a summary.
func TestCentralGuardsMatchTheirOperations(t *testing.T) {
	byID := map[string]specOp{}
	for _, o := range loadSpec(t) {
		byID[o.ID] = o
	}
	for id, c := range central {
		o, ok := byID[id]
		if !ok {
			t.Errorf("%s: not in the spec", id)
			continue
		}
		if o.Method == "GET" {
			t.Errorf("%s: a read cannot be guarded", id)
		}
		if p := c.Target.Param(); p == "" || !strings.Contains(o.Path, "{"+p+"}") {
			t.Errorf("%s: target %q needs the path parameter %q, the path is %s", id, c.Target, c.Target.Param(), o.Path)
		}
		if !strings.Contains(c.Summary, "{name}") || len(c.Effects) == 0 {
			t.Errorf("%s: the approver needs a summary with {name} and at least one effect", id)
		}
		for _, w := range c.When {
			if w.Arg == "" {
				t.Errorf("%s: a When condition names no argument", id)
			}
		}
	}
	// Servers are not environment-scoped ids, but these two cut production off.
	for _, id := range []string{"revokeServer", "disableServerTunnel"} {
		if _, ok := CentralGuard(id); !ok {
			t.Errorf("%s must be guarded when the server hosts production services", id)
		}
	}
}

func TestCountsAddUp(t *testing.T) {
	c := Count()
	if c.Curated+c.Generic+c.Denied != c.Total || c.Total != len(Manifest) {
		t.Fatalf("counts do not add up: %+v", c)
	}
	var byToolset int
	for _, n := range c.Toolsets {
		byToolset += n
	}
	if byToolset != c.Curated+c.Generic {
		t.Fatalf("toolset counts %d != allowed %d", byToolset, c.Curated+c.Generic)
	}
	var byReason int
	for _, n := range c.DenyReasons {
		byReason += n
	}
	if byReason != c.Denied {
		t.Fatalf("deny reason counts %d != denied %d", byReason, c.Denied)
	}
	t.Logf("total=%d curated=%d (native=%d) generic=%d denied=%d", c.Total, c.Curated, c.Native, c.Generic, c.Denied)
	t.Logf("toolsets (allowed): %v; curated: %v", c.Toolsets, c.CuratedToolsets)
	t.Logf("deny reasons: %v", c.DenyReasons)
}

// ── projections ──

func decode(t *testing.T, s string) any {
	t.Helper()
	var v any
	if err := json.Unmarshal([]byte(s), &v); err != nil {
		t.Fatal(err)
	}
	return v
}

func encode(t *testing.T, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

const variablesJSON = `{
 "items":[
  {"key":"API_KEY","isSecret":true,"set":true,"scope":"runtime","value":"sekret-canary-12345"},
  {"key":"LOG_LEVEL","isSecret":false,"set":true,"scope":"both","value":"debug","comment":"verbose"},
  {"key":"DB","isSecret":false,"computedSecret":true,"set":true,"value":"postgres://u:sekret-canary-12345@h/db"}
 ],
 "platform":[{"key":"PORT","value":"8080","category":"runtime"}],
 "exports":[{"key":"DATABASE_URL","secret":true}],
 "warnings":[{"key":"X"}],"restaged":[]}`

func TestVariableProjection(t *testing.T) {
	c, ok := LookupTool("variable_list")
	if !ok || c.Projection == nil {
		t.Fatal("variable_list has no projection")
	}

	concise := encode(t, c.Projection.Apply(DetailConcise, decode(t, variablesJSON)))
	for _, bad := range []string{"sekret-canary-12345", "debug", `"value"`, "exports", "DATABASE_URL", "verbose"} {
		if strings.Contains(concise, bad) {
			t.Errorf("concise variable list leaks %q: %s", bad, concise)
		}
	}
	for _, want := range []string{`"key":"API_KEY"`, `"isSecret":true`, `"set":true`, `"key":"LOG_LEVEL"`, `"platformKeys":["PORT"]`, "warnings"} {
		if !strings.Contains(concise, want) {
			t.Errorf("concise variable list lacks %s: %s", want, concise)
		}
	}

	full := encode(t, c.Projection.Apply(DetailFull, decode(t, variablesJSON)))
	if strings.Contains(full, "sekret-canary-12345") {
		t.Errorf("full variable list leaks a secret value: %s", full)
	}
	if !strings.Contains(full, `"value":"debug"`) {
		t.Errorf("full variable list lacks the non-secret value: %s", full)
	}
}

func TestServiceProjection(t *testing.T) {
	c, ok := LookupTool("service_get")
	if !ok {
		t.Fatal("service_get missing")
	}
	in := `{"id":"s1","name":"web","slug":"web","generation":3,"appliedGeneration":2,
	 "config":{"image":"nginx","port":80,"files":[{"path":"/etc/big","content":"xxxxxxxx"}],"stack":{"compose":"huge"}},
	 "domains":[{"hostname":"a.example.com","status":"active","authUsers":[{"username":"u"}],"options":{"x":1}}],
	 "observed":{"state":"running","containerId":"abc","replicas":[{"id":1}]}}`
	out := encode(t, c.Projection.Apply(DetailConcise, decode(t, in)))
	for _, bad := range []string{"xxxxxxxx", "huge", "authUsers", "containerId", `"options"`} {
		if strings.Contains(out, bad) {
			t.Errorf("concise service leaks bulk field %q: %s", bad, out)
		}
	}
	for _, want := range []string{`"hostname":"a.example.com"`, `"generation":3`, `"image":"nginx"`, `"state":"running"`} {
		if !strings.Contains(out, want) {
			t.Errorf("concise service lacks %s: %s", want, out)
		}
	}
	full := encode(t, c.Projection.Apply(DetailFull, decode(t, in)))
	if !strings.Contains(full, "xxxxxxxx") || !strings.Contains(full, "containerId") {
		t.Errorf("full service dropped fields: %s", full)
	}
}

func TestListProjectionKeepsPaging(t *testing.T) {
	c, _ := LookupTool("deployment_list")
	in := `{"items":[{"id":"d1","status":"succeeded","build":{"steps":["a","b"]},"commit":{"sha":"abc","author":"me"}}],"nextCursor":"zzz"}`
	out := encode(t, c.Projection.Apply(DetailConcise, decode(t, in)))
	if !strings.Contains(out, `"nextCursor":"zzz"`) || strings.Contains(out, "steps") || strings.Contains(out, "author") {
		t.Errorf("list projection: %s", out)
	}
	if !strings.Contains(out, `"sha":"abc"`) {
		t.Errorf("list projection dropped commit.sha: %s", out)
	}
}

func TestMetricsDownsample(t *testing.T) {
	pts := make([]any, 300)
	for i := range pts {
		pts[i] = map[string]any{"t": float64(i)}
	}
	c, _ := LookupTool("service_metrics")
	out := c.Projection.Apply(DetailConcise, map[string]any{"points": pts, "stepSeconds": float64(10)}).(map[string]any)
	got := out["points"].([]any)
	if len(got) != 60 {
		t.Fatalf("points = %d, want 60", len(got))
	}
	if first := got[0].(map[string]any)["t"].(float64); first != 0 {
		t.Errorf("first point = %v, want 0", first)
	}
	if last := got[59].(map[string]any)["t"].(float64); last != 299 {
		t.Errorf("last point = %v, want 299 (newest kept)", last)
	}
	if out["downsampledFrom"] != 300 {
		t.Errorf("downsampledFrom = %v", out["downsampledFrom"])
	}
	small := c.Projection.Apply(DetailConcise, map[string]any{"points": pts[:10]}).(map[string]any)
	if len(small["points"].([]any)) != 10 {
		t.Error("short series must be kept whole")
	}
}

func TestNilProjectionIsIdentity(t *testing.T) {
	var p *Projection
	in := map[string]any{"a": 1}
	if got := p.Apply(DetailConcise, in).(map[string]any); got["a"] != 1 {
		t.Fatal("nil projection changed the value")
	}
}

func TestParseDetail(t *testing.T) {
	if ParseDetail("") != DetailConcise || ParseDetail("full") != DetailFull || ParseDetail("verbose") != DetailConcise {
		t.Fatal("ParseDetail")
	}
}
