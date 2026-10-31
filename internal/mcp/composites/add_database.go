package composites

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"sort"
	"strings"

	"github.com/google/uuid"

	"github.com/nebulactrl/nebulactrl/internal/apierr"
	"github.com/nebulactrl/nebulactrl/internal/auth"
	"github.com/nebulactrl/nebulactrl/internal/databases"
	"github.com/nebulactrl/nebulactrl/internal/databases/engines"
	"github.com/nebulactrl/nebulactrl/internal/mcp/mcpkit"
	"github.com/nebulactrl/nebulactrl/internal/policy"
	"github.com/nebulactrl/nebulactrl/internal/prodguard"
	"github.com/nebulactrl/nebulactrl/internal/projects"
	"github.com/nebulactrl/nebulactrl/internal/services"
	"github.com/nebulactrl/nebulactrl/internal/variables"
)

type addDatabaseInput struct {
	Environment   string `json:"environment" jsonschema:"environment selector: uuid, project/env, or a project slug (its single production environment)"`
	Engine        string `json:"engine" jsonschema:"database engine id, e.g. postgres, mysql, mariadb, redis, valkey, mongodb, clickhouse"`
	Version       string `json:"version,omitempty" jsonschema:"major version, e.g. 17 (default: the engine's default version)"`
	Name          string `json:"name,omitempty" jsonschema:"service name (default: the engine id); its slug is what ${{slug.KEY}} references use"`
	Server        string `json:"server,omitempty" jsonschema:"server id or name to place the database on (default: the app's server, else placed at first deploy)"`
	App           string `json:"app,omitempty" jsonschema:"service selector of an app IN THE SAME ENVIRONMENT that should get the connection variables (staged references)"`
	Prefix        string `json:"prefix,omitempty" jsonschema:"variable name prefix on the app, e.g. DATABASE gives DATABASE_HOST, DATABASE_PORT, DATABASE_URL (default: DATABASE, REDIS for redis/valkey)"`
	Deploy        bool   `json:"deploy,omitempty" jsonschema:"also deploy the database service (never the app). Production environments need human approval. Default false: everything stays staged"`
	AcceptLicense bool   `json:"acceptLicense,omitempty" jsonschema:"set true only after the user accepted the licence note of a source-available engine (mongodb, redis)"`
	Detail        string `json:"detail,omitempty" jsonschema:"concise (default) or full (adds the engine's export catalog)"`
}

// AddDatabaseCreated describes the database service.
type AddDatabaseCreated struct {
	Selector          string `json:"selector" jsonschema:"project/env/service selector"`
	ID                string `json:"id"`
	Slug              string `json:"slug" jsonschema:"the name other services use in ${{slug.KEY}} references"`
	Name              string `json:"name"`
	Engine            string `json:"engine"`
	Version           string `json:"version"`
	Image             string `json:"image"`
	Port              int    `json:"port"`
	Volume            string `json:"volume" jsonschema:"data volume as name:mountPath"`
	Server            string `json:"server" jsonschema:"server name, empty until placed at first deploy"`
	ServerID          string `json:"serverId"`
	Generation        int64  `json:"generation"`
	AppliedGeneration int64  `json:"appliedGeneration"`
	Staged            bool   `json:"staged" jsonschema:"true while generation > appliedGeneration: nothing runs until apply_changes"`
	DeploymentID      string `json:"deploymentId,omitempty" jsonschema:"set when deploy:true started the database deployment"`
	DeploymentStatus  string `json:"deploymentStatus,omitempty"`
}

// AddDatabaseVar is one connection variable staged on the app.
type AddDatabaseVar struct {
	Key       string `json:"key"`
	Reference string `json:"reference" jsonschema:"the reference the variable holds; the credentials stay inside the database service and are never returned"`
	Status    string `json:"status" jsonschema:"staged | unchanged (already set to this reference) | skipped (the key holds another value; not overwritten)"`
}

// AddDatabaseApp describes the app wiring.
type AddDatabaseApp struct {
	Selector          string           `json:"selector,omitempty"`
	ID                string           `json:"id,omitempty"`
	LinkID            string           `json:"linkId,omitempty" jsonschema:"id of the manual link edge database <- app"`
	Variables         []AddDatabaseVar `json:"variables"`
	Generation        int64            `json:"generation,omitempty"`
	AppliedGeneration int64            `json:"appliedGeneration,omitempty"`
}

// AddDatabaseExport is one reference a service can use to reach the database.
type AddDatabaseExport struct {
	Key         string `json:"key"`
	Reference   string `json:"reference"`
	Secret      bool   `json:"secret"`
	Primary     bool   `json:"primary"`
	Description string `json:"description"`
}

// AddDatabaseOutput is the plan add_database returns.
type AddDatabaseOutput struct {
	Envelope
	Database AddDatabaseCreated `json:"database"`
	// Resumed is true when a database of that name and engine was staged
	// earlier and never deployed: add_database continued with it (link,
	// variables, deploy) instead of creating a second one.
	Resumed  bool                `json:"resumed"`
	App      AddDatabaseApp      `json:"app"`
	Warnings []string            `json:"warnings"`
	Exports  []AddDatabaseExport `json:"exports" jsonschema:"every reference the database offers (detail:full only)"`
}

var addDatabasePrefixRE = regexp.MustCompile(`^[A-Z][A-Z0-9_]{0,63}$`)

var addDatabaseAliases = map[string]string{
	"postgresql": engines.Postgres, "pg": engines.Postgres, "pgsql": engines.Postgres,
	"mongo": engines.MongoDB, "maria": engines.MariaDB,
}

func (c *composites) addDatabase() mcpkit.Tool {
	return mcpkit.Tool{
		Name:  "add_database",
		Title: "Add a database to an app",
		Description: "Recipe: create a managed database (postgres, mysql, mariadb, redis, valkey, mongodb, clickhouse) in an environment and, when `app` is given, wire it " +
			"to that app: stage connection variables on the app that REFERENCE the database (`${{db.NEBULA_PRIVATE_HOST}}`, its port and its connection-string export; the " +
			"credentials stay in the database service and are never returned) and create the link edge. Use it instead of hand-writing credentials. " +
			"Everything is STAGED: nothing runs until `apply_changes` (the database is deployed before the app that references it). " +
			"With deploy:true the database service alone is also deployed (production environments need human approval, then call again with the same arguments: it continues with the staged database); the app stays staged. " +
			"Warns when the app and the database sit on different servers (no shared private network). " +
			"Refuses when a service of that name already exists in the environment (use another `name`). It does NOT change the app's code, import data or deploy the app. " +
			"Returns the created service (selector, id, version, port, volume), the staged variable KEYS, the link id, warnings, and next steps: review_changes, then apply_changes with the expectedGeneration of each service.",
		Toolset: mcpkit.ToolsetCore,
		Scopes:  []string{policy.ScopeProjectsWrite},
		Hints:   mcpkit.Hints{ReadOnly: false, Destructive: false, Idempotent: false, OpenWorld: false},
		Input:   addDatabaseInput{},
		Output:  AddDatabaseOutput{},
		Call: func(ctx context.Context, _ *auth.Principal, raw json.RawMessage) (any, error) {
			in, err := mcpkit.Decode[addDatabaseInput](raw)
			if err != nil {
				return nil, err
			}
			return c.runAddDatabase(ctx, in)
		},
	}
}

func (c *composites) runAddDatabase(ctx context.Context, in addDatabaseInput) (AddDatabaseOutput, error) {
	detail, err := mcpkit.ParseDetail(in.Detail)
	if err != nil {
		return AddDatabaseOutput{}, err
	}
	if strings.TrimSpace(in.Environment) == "" {
		return AddDatabaseOutput{}, invalid("environment is required (project/env, an environment id, or a project slug)")
	}
	if strings.TrimSpace(in.Engine) == "" {
		return AddDatabaseOutput{}, invalid("engine is required (one of %s)", strings.Join(engines.IDs(), ", "))
	}

	// ---- arguments and the primary resources: fail before creating anything ----
	catalog, err := c.Databases.Catalog(ctx)
	if err != nil {
		return AddDatabaseOutput{}, err
	}
	eng, err := addDatabaseEngine(catalog, in.Engine)
	if err != nil {
		return AddDatabaseOutput{}, err
	}
	version := strings.TrimSpace(in.Version)
	if version != "" && !addDatabaseHasVersion(eng, version) {
		return AddDatabaseOutput{}, invalid("%s has no version %q (one of %s)", eng.Label, version, strings.Join(addDatabaseVersions(eng), ", "))
	}
	if eng.LicenseClass == engines.LicenseSourceAvailable && !in.AcceptLicense {
		e := mcpkit.Errf("license_not_accepted", "%s is source-available: %s Ask the user, then call again with acceptLicense:true.", eng.Label, eng.LicenseNote)
		e.Data = map[string]any{"engine": eng.ID, "license": eng.License}
		return AddDatabaseOutput{}, e
	}
	prefix, err := addDatabasePrefix(in.Prefix)
	if err != nil {
		return AddDatabaseOutput{}, err
	}
	name := strings.TrimSpace(in.Name)
	if name == "" {
		name = eng.ID
	}
	if len(name) > 100 {
		return AddDatabaseOutput{}, invalid("name is longer than 100 characters")
	}

	env, err := c.Resolver.Environment(ctx, in.Environment)
	if err != nil {
		return AddDatabaseOutput{}, err
	}
	var app *services.ServiceView
	if strings.TrimSpace(in.App) != "" {
		v, err := c.Resolver.Service(ctx, in.App)
		if err != nil {
			return AddDatabaseOutput{}, err
		}
		if v.EnvironmentID != env.ID {
			e := mcpkit.Errf(mcpkit.CodeInvalidArgument,
				"app %q is not in environment %s: a database and the app that uses it must share an environment (the private network). Nothing was created. Pick an app of this environment or the environment of the app.",
				in.App, env.Slug)
			e.Data = map[string]any{"app": v.ID.String(), "appEnvironmentId": v.EnvironmentID.String(), "environmentId": env.ID.String()}
			return AddDatabaseOutput{}, e
		}
		app = &v
	}
	var serverID *uuid.UUID
	switch {
	case strings.TrimSpace(in.Server) != "":
		id, err := c.addDatabaseServer(ctx, in.Server)
		if err != nil {
			return AddDatabaseOutput{}, err
		}
		serverID = &id
	case app != nil && app.ServerID != nil:
		id := *app.ServerID
		serverID = &id
	}

	// ---- idempotency: the same name in the environment ----
	existing, err := c.addDatabaseFind(ctx, env.ID, name)
	if err != nil {
		return AddDatabaseOutput{}, err
	}
	var dbv services.ServiceView
	resumed := false
	if existing != nil {
		if !in.Deploy || !addDatabaseResumable(*existing, eng.ID, version) {
			return AddDatabaseOutput{}, c.addDatabaseConflict(ctx, *existing, name)
		}
		dbv, resumed = *existing, true
	} else {
		dbv, err = c.Databases.Create(ctx, databases.CreateInput{
			EnvironmentID: env.ID, Name: name, Engine: eng.ID, Version: version, ServerID: serverID, AcceptLicense: in.AcceptLicense,
		})
		if err != nil {
			return AddDatabaseOutput{}, err
		}
	}

	// ---- from here on the database exists: failures become errors[] ----
	out := AddDatabaseOutput{
		Resumed:  resumed,
		App:      AddDatabaseApp{Variables: []AddDatabaseVar{}},
		Warnings: []string{},
		Exports:  []AddDatabaseExport{},
	}
	out.Errors, out.NextTools = []SectionError{}, []NextTool{}
	dbSel, envSel := dbv.ID.String(), env.ID.String()
	if sc, err := c.scope(ctx, dbv.ID); err == nil {
		dbSel, envSel = selectorOf(sc), path(sc.ProjectSlug, sc.EnvSlug)
		if sc.IsProduction {
			out.Warnings = append(out.Warnings, "This is a production environment: deploying needs human approval (apply_changes asks for it).")
		}
	}
	out.Database = addDatabaseCreated(dbv, dbSel)

	p := c.newPartial(ctx, "add_database", 4)
	if app != nil {
		appSel := app.ID.String()
		if asc, err := c.scope(ctx, app.ID); err == nil {
			appSel = selectorOf(asc)
		}
		out.App.Selector, out.App.ID = appSel, app.ID.String()
		if w := addDatabasePlacement(dbv, *app, appSel, dbSel); w != "" {
			out.Warnings = append(out.Warnings, w)
		}
		var linkWarnings []string
		p.run("link", func(ctx context.Context) error {
			// The database was staged by this call and never ran, and the link
			// opens no mesh grant without acceptMeshChanges: nothing that already
			// runs changes, so the link needs no approval of its own.
			res, err := c.Services.CreateLink(prodguard.Covered(ctx), env.ID, app.ID, dbv.ID)
			if ae, ok := apierr.As(err); ok && ae.Code == "link_exists" {
				out.App.LinkID = c.addDatabaseLinkID(ctx, env.ID, app.ID, dbv.ID)
				return nil
			}
			if err != nil {
				return err
			}
			out.App.LinkID = res.Link.ID
			linkWarnings = res.Warnings
			return nil
		})
		p.run("variables", func(ctx context.Context) error {
			vars, warns, err := c.addDatabaseStageVars(ctx, *app, dbv, prefix)
			out.App.Variables = vars
			out.Warnings = append(out.Warnings, warns...)
			return err
		})
		out.Errors = p.wait()
		for _, w := range linkWarnings {
			if !slices.ContainsFunc(out.Warnings, func(have string) bool { return strings.Contains(have, w) }) {
				out.Warnings = append(out.Warnings, w)
			}
		}
	}

	// ---- optionally deploy the database only ----
	if in.Deploy {
		dv, err := c.Deployments.Deploy(ctx, dbv.ID, "", "")
		switch {
		case addDatabaseGuardError(err):
			// The production guard: the client gets the plan and the dashboard
			// page where a person approves it; the repeated call finds the
			// staged database again and deploys it. Never an errors[] entry.
			return AddDatabaseOutput{}, err
		case err != nil:
			out.Errors = append(out.Errors, sectionError("deploy", err))
			out.Warnings = append(out.Warnings, "The database stays staged (not deployed). Fix the problem named in errors[] and run apply_changes for it.")
		default:
			out.Database.DeploymentID, out.Database.DeploymentStatus = dv.ID.String(), dv.Status
		}
	}

	// ---- refresh generations and the export catalog ----
	p2 := c.newPartial(ctx, "add_database", 4)
	p2.run("database", func(ctx context.Context) error {
		v, err := c.Services.Get(ctx, dbv.ID)
		if err != nil {
			return err
		}
		dep, status := out.Database.DeploymentID, out.Database.DeploymentStatus
		out.Database = addDatabaseCreated(v, dbSel)
		out.Database.DeploymentID, out.Database.DeploymentStatus = dep, status
		return nil
	})
	if app != nil {
		p2.run("app", func(ctx context.Context) error {
			v, err := c.Services.Get(ctx, app.ID)
			if err != nil {
				return err
			}
			out.App.Generation, out.App.AppliedGeneration = v.Generation, v.AppliedGeneration
			return nil
		})
	}
	if detail.Full() {
		p2.run("exports", func(ctx context.Context) error {
			v, err := c.Databases.Get(ctx, dbv.ID)
			if err != nil {
				return err
			}
			for _, e := range v.Exports {
				out.Exports = append(out.Exports, AddDatabaseExport{Key: e.Key, Reference: e.Reference, Secret: e.Secret, Primary: e.Primary, Description: e.Description})
			}
			return nil
		})
	}
	out.Errors = append(out.Errors, p2.wait()...)
	sort.SliceStable(out.Errors, func(i, j int) bool { return out.Errors[i].Section < out.Errors[j].Section })

	out.Summary = addDatabaseSummary(out, in.Deploy)
	out.NextTools = addDatabaseNext(out, envSel)
	return out, nil
}

// addDatabaseGuardError reports an outcome of the production guard (approval
// required, declined, expired, unavailable): it is the tool's error, unchanged.
func addDatabaseGuardError(err error) bool {
	var ar *prodguard.ErrApprovalRequired
	if errors.As(err, &ar) {
		return true
	}
	ae, ok := apierr.As(err)
	return ok && strings.HasPrefix(ae.Code, "approval_")
}

// addDatabaseCreated renders the database service.
func addDatabaseCreated(v services.ServiceView, sel string) AddDatabaseCreated {
	d := AddDatabaseCreated{
		Selector: sel, ID: v.ID.String(), Slug: v.Slug, Name: v.Name,
		ServerID: idPtr(v.ServerID), Generation: v.Generation, AppliedGeneration: v.AppliedGeneration,
		Staged: v.Generation > v.AppliedGeneration,
	}
	if v.Server != nil {
		d.Server = v.Server.Name
	}
	if spec := v.Config.Database; spec != nil {
		n := spec.Normalize()
		d.Engine, d.Version, d.Image, d.Port = n.Engine, n.Version, n.Image(), n.Port()
	}
	if len(v.Volumes) > 0 {
		d.Volume = v.Volumes[0].Name + ":" + v.Volumes[0].MountPath
	}
	return d
}

// addDatabaseEngine finds the catalog engine for a user-typed id.
func addDatabaseEngine(catalog []databases.EngineView, id string) (databases.EngineView, error) {
	want := strings.ToLower(strings.TrimSpace(id))
	if a, ok := addDatabaseAliases[want]; ok {
		want = a
	}
	ids := make([]string, 0, len(catalog))
	for _, e := range catalog {
		if e.ID == want {
			return e, nil
		}
		ids = append(ids, e.ID)
	}
	return databases.EngineView{}, invalid("unknown database engine %q (one of %s)", id, strings.Join(ids, ", "))
}

func addDatabaseHasVersion(e databases.EngineView, v string) bool {
	for _, x := range e.Versions {
		if x.ID == v {
			return true
		}
	}
	return false
}

func addDatabaseVersions(e databases.EngineView) []string {
	out := make([]string, 0, len(e.Versions))
	for _, x := range e.Versions {
		out = append(out, x.ID)
	}
	return out
}

// addDatabasePrefix normalizes the variable prefix; "" keeps the web UI's
// defaults (DATABASE_ / REDIS_ and the engine's own URL name).
func addDatabasePrefix(raw string) (string, error) {
	p := strings.ToUpper(strings.Trim(strings.TrimSpace(raw), "_"))
	if p == "" {
		return "", nil
	}
	if !addDatabasePrefixRE.MatchString(p) {
		return "", invalid("prefix %q must be letters, digits and underscores, starting with a letter (e.g. DATABASE)", raw)
	}
	return p, nil
}

// addDatabaseServer resolves a server id or name.
func (c *composites) addDatabaseServer(ctx context.Context, sel string) (uuid.UUID, error) {
	sel = strings.TrimSpace(sel)
	if id, err := uuid.Parse(sel); err == nil {
		return id, nil
	}
	list, _, err := c.Servers.List(ctx, "", 200)
	if err != nil {
		return uuid.Nil, err
	}
	var hits []uuid.UUID
	var choices []mcpkit.Choice
	for _, s := range list {
		if strings.EqualFold(s.Name, sel) {
			hits = append(hits, s.ID)
			choices = append(choices, mcpkit.Choice{ID: s.ID.String(), Kind: "server", Name: s.Name})
		}
	}
	switch len(hits) {
	case 1:
		return hits[0], nil
	case 0:
		return uuid.Nil, invalid("no server named %q; pass a server id or an exact name", sel)
	}
	e := mcpkit.Errf(mcpkit.CodeAmbiguous, "several servers are named %q; pass the server id", sel)
	e.Data = map[string]any{"choices": choices}
	return uuid.Nil, e
}

// addDatabaseFind returns the service of the environment whose slug or name is
// name, nil for none.
func (c *composites) addDatabaseFind(ctx context.Context, envID uuid.UUID, name string) (*services.ServiceView, error) {
	slug := projects.Slugify(name, "service")
	cursor := ""
	for range 20 {
		page, next, err := c.Services.List(ctx, envID, cursor, 200)
		if err != nil {
			return nil, err
		}
		for i := range page {
			if page[i].Slug == slug || strings.EqualFold(page[i].Name, name) {
				return &page[i], nil
			}
		}
		if next == "" {
			return nil, nil
		}
		cursor = next
	}
	return nil, nil
}

// addDatabaseResumable reports whether an existing service is a database of the
// requested engine that was staged but never deployed.
func addDatabaseResumable(v services.ServiceView, engine, version string) bool {
	spec := v.Config.Database
	if v.Kind != services.KindDatabase || spec == nil || spec.Engine != engine {
		return false
	}
	if version != "" && spec.Normalize().Version != version {
		return false
	}
	return v.CurrentDeploymentID == nil && v.LatestDeployment == nil
}

func (c *composites) addDatabaseConflict(ctx context.Context, v services.ServiceView, name string) error {
	sel := v.ID.String()
	if sc, err := c.scope(ctx, v.ID); err == nil {
		sel = selectorOf(sc)
	}
	e := mcpkit.Errf("conflict", "a %s named %q already exists in this environment (%s). Use another `name`, or reference the existing one; to re-run after a failed attempt, pass deploy:true with the same engine.", v.Kind, v.Name, sel)
	e.Data = map[string]any{"selector": sel, "id": v.ID.String(), "kind": v.Kind, "requestedName": name}
	return e
}

// addDatabasePlacement explains a cross-server placement (the web UI's wording).
func addDatabasePlacement(dbv, app services.ServiceView, appSel, dbSel string) string {
	if dbv.ServerID == nil || app.ServerID == nil || *dbv.ServerID == *app.ServerID {
		return ""
	}
	dbSrv, appSrv := idPtr(dbv.ServerID), idPtr(app.ServerID)
	if dbv.Server != nil {
		dbSrv = dbv.Server.Name
	}
	if app.Server != nil {
		appSrv = app.Server.Name
	}
	return fmt.Sprintf("%s is on server %s but %s is on server %s. %s", dbSel, dbSrv, appSel, appSrv, services.CrossServerWarning)
}

// addDatabaseLinkID finds the manual link app -> database.
func (c *composites) addDatabaseLinkID(ctx context.Context, envID, appID, dbID uuid.UUID) string {
	links, err := c.Services.ListLinks(ctx, envID)
	if err != nil {
		return ""
	}
	for _, l := range links {
		if l.FromServiceID == appID && l.ToServiceID == dbID {
			return l.ID
		}
	}
	return ""
}

// addDatabaseRefs are the references staged on the app: key -> reference.
// Order is stable.
type addDatabaseRef struct{ key, ref string }

func addDatabaseRefs(dbv services.ServiceView, prefix string) []addDatabaseRef {
	cfg := dbv.Config
	if cfg.Database == nil {
		cfg.Database = &engines.Spec{Engine: engines.Postgres}
	}
	// The same recommended set the web's connect popover preselects.
	rec := services.RecommendedLinkRefs(dbv.Slug, services.KindDatabase, cfg, prefix)
	out := make([]addDatabaseRef, 0, len(rec))
	for _, p := range rec {
		out = append(out, addDatabaseRef{p.Key, p.Value})
	}
	return out
}

// addDatabaseStageVars stages the reference variables on the app. Keys the app
// already holds with another value are left alone and reported.
func (c *composites) addDatabaseStageVars(ctx context.Context, app, dbv services.ServiceView, prefix string) ([]AddDatabaseVar, []string, error) {
	refs := addDatabaseRefs(dbv, prefix)
	out := make([]AddDatabaseVar, 0, len(refs))
	var warns []string
	cur, err := c.Variables.List(ctx, app.ID)
	if err != nil {
		return out, warns, err
	}
	have := map[string]*string{}
	for _, v := range cur.Items {
		have[v.Key] = v.Value
	}
	no := false
	var put []variables.VariableInput
	for _, r := range refs {
		val, ok := have[r.key]
		switch {
		case ok && val != nil && *val == r.ref:
			out = append(out, AddDatabaseVar{Key: r.key, Reference: r.ref, Status: "unchanged"})
		case ok:
			out = append(out, AddDatabaseVar{Key: r.key, Reference: r.ref, Status: "skipped"})
			warns = append(warns, fmt.Sprintf("%s is already set on the app to another value and was not overwritten. Replace it with variable_set (value %s) or pick another `prefix`.", r.key, r.ref))
		default:
			v := r.ref
			put = append(put, variables.VariableInput{Key: r.key, Value: &v, IsSecret: &no})
			out = append(out, AddDatabaseVar{Key: r.key, Reference: r.ref, Status: "staged"})
		}
	}
	if len(put) == 0 {
		return out, warns, nil
	}
	res, err := c.Variables.Put(ctx, app.ID, put)
	if err != nil {
		for i := range out {
			if out[i].Status == "staged" {
				out[i].Status = "failed"
			}
		}
		return out, warns, err
	}
	for _, w := range res.Warnings {
		msg := w.Message
		if w.Key != "" {
			msg = w.Key + ": " + msg
		}
		warns = append(warns, clip(msg, 300))
	}
	return out, warns, nil
}

func addDatabaseSummary(o AddDatabaseOutput, deploy bool) string {
	d := o.Database
	var b strings.Builder
	verb := "Staged"
	if o.Resumed {
		verb = "Continued with the staged"
	}
	fmt.Fprintf(&b, "%s %s %s (%s) as %s", verb, d.Engine, d.Version, d.Slug, d.Selector)
	if d.Server != "" {
		fmt.Fprintf(&b, " on %s", d.Server)
	}
	b.WriteString(".")
	if o.App.Selector != "" {
		staged := 0
		for _, v := range o.App.Variables {
			if v.Status == "staged" {
				staged++
			}
		}
		fmt.Fprintf(&b, " Staged %s on %s", plural(staged, "reference variable", "reference variables"), o.App.Selector)
		if o.App.LinkID != "" {
			b.WriteString(" and linked it")
		}
		b.WriteString(".")
	}
	switch {
	case d.DeploymentID != "":
		fmt.Fprintf(&b, " Database deployment %s started (%s); the app is still staged.", d.DeploymentID, d.DeploymentStatus)
	case deploy:
		b.WriteString(" The database was NOT deployed (see errors).")
	default:
		b.WriteString(" Nothing runs until apply_changes.")
	}
	if len(o.Errors) > 0 {
		fmt.Fprintf(&b, " %s failed: the database stays staged; see errors[].", plural(len(o.Errors), "step", "steps"))
	}
	return b.String()
}

func addDatabaseNext(o AddDatabaseOutput, envSel string) []NextTool {
	d := o.Database
	entry := func(sel string, gen int64) map[string]any {
		return map[string]any{"service": sel, "expectedGeneration": gen}
	}
	var next []NextTool
	switch {
	case d.DeploymentID != "" && o.App.Selector != "":
		next = append(next,
			NextTool{Tool: "verify_service", Args: map[string]any{"service": d.Selector}, Why: "confirm the database is up before the app starts"},
			NextTool{Tool: "apply_changes", Args: map[string]any{"services": []any{entry(o.App.Selector, o.App.Generation)}},
				Why: "deploy the app with its staged connection variables (it waits for the database deployment)"})
	case d.DeploymentID != "":
		next = append(next, NextTool{Tool: "verify_service", Args: map[string]any{"service": d.Selector}, Why: "confirm the database is up"})
	default:
		next = append(next, NextTool{Tool: "review_changes", Args: map[string]any{"environment": envSel}, Why: "review the staged database and app changes before applying"})
		svcs := []any{entry(d.Selector, d.Generation)}
		if o.App.Selector != "" {
			svcs = append(svcs, entry(o.App.Selector, o.App.Generation))
		}
		next = append(next, NextTool{Tool: "apply_changes", Args: map[string]any{"services": svcs},
			Why: "deploy the database first, then the app (apply_changes orders deployments by references)"})
	}
	for _, v := range o.App.Variables {
		if v.Status == "skipped" {
			next = append(next, NextTool{Tool: "variable_set", Args: map[string]any{"service": o.App.Selector, "key": v.Key, "value": v.Reference},
				Why: v.Key + " already holds another value on the app; replace it only if that is intended"})
		}
	}
	if o.App.Selector == "" {
		next = append(next, NextTool{Tool: "variable_set", Why: "give an app access: set a variable on it to " + "${{" + d.Slug + "." + engines.PrimaryExport(d.Engine) + "}}"})
	}
	return next
}
