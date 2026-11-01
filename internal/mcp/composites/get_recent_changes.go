package composites

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/nebulactrl/nebulactrl/internal/auth"
	"github.com/nebulactrl/nebulactrl/internal/deployments"
	"github.com/nebulactrl/nebulactrl/internal/mcp/mcpkit"
	"github.com/nebulactrl/nebulactrl/internal/policy"
	"github.com/nebulactrl/nebulactrl/internal/services"
)

const (
	recentDefaultLimit = 30
	recentMaxLimit     = 100
	// recentSlack is how many rows beyond the page each source reads, so rows
	// that tie on the cursor timestamp cannot starve a page.
	recentSlack = 10
	// recentMaxIDs bounds the resource ids an environment-wide audit read filters on.
	recentMaxIDs = 5000
)

type getRecentChangesInput struct {
	Service     string `json:"service,omitempty" jsonschema:"Service selector: id, project/env/service or hostname. Give exactly one of service, environment or project (environment may be combined with project)."`
	Environment string `json:"environment,omitempty" jsonschema:"Environment selector: id, project/env, or a slug together with project."`
	Project     string `json:"project,omitempty" jsonschema:"Project selector: id, slug or name."`
	Since       string `json:"since,omitempty" jsonschema:"Start of the window: an RFC3339 timestamp or a duration such as 90m, 24h or 7d before now. Default 24h."`
	Limit       int    `json:"limit,omitempty" jsonschema:"Maximum events per page (default 30, max 100)."`
	Cursor      string `json:"cursor,omitempty" jsonschema:"nextCursor of the previous page."`
	Detail      string `json:"detail,omitempty" jsonschema:"concise (default): action, resource, actor, time; full: also the (value-free) audit metadata and deployment status messages."`
}

type changeEvent struct {
	At       string `json:"at" jsonschema:"RFC3339 UTC time"`
	Kind     string `json:"kind" jsonschema:"audit or deployment"`
	Category string `json:"category" jsonschema:"deployment, service, variable, domain, volume, link, environment, project or other"`
	Action   string `json:"action" jsonschema:"audit action such as variable.set, or deployment.<status>"`
	Resource string `json:"resource" jsonschema:"what it was done to: project/env/service, domain hostname, ..."`
	// ResourceType and ResourceID identify the resource for other tools.
	ResourceType string `json:"resourceType"`
	ResourceID   string `json:"resourceId"`
	ActorType    string `json:"actorType" jsonschema:"user, token, mcp or system"`
	Actor        string `json:"actor" jsonschema:"display label: user name, token name or system"`
	Client       string `json:"client,omitempty" jsonschema:"the MCP client's name when recorded"`
	// StagedEdit marks edits that only stage a change; the running service
	// changes when apply_changes (or a deployment) applies them.
	StagedEdit bool `json:"stagedEdit,omitempty"`
	// What is a short value-free description: variable keys, changed fields, hostname, volume name.
	What string `json:"what,omitempty"`

	// Deployment events.
	DeploymentID string `json:"deploymentId,omitempty"`
	Status       string `json:"status,omitempty"`
	Trigger      string `json:"trigger,omitempty"`
	Strategy     string `json:"strategy,omitempty"`
	Commit       string `json:"commit,omitempty"`
	Image        string `json:"image,omitempty"`
	// StatusMessage is the message of the platform or agent (detail:full); it
	// can echo build or registry output, so it is an untrusted block.
	StatusMessage any `json:"statusMessage,omitempty" jsonschema:"message of the platform or agent, as an untrusted block"`

	Metadata map[string]any `json:"metadata,omitempty"`
}

type getRecentChangesOutput struct {
	Envelope
	// Scope is what the timeline covers: service, environment or project, with its selector.
	Scope      string        `json:"scope"`
	Selector   string        `json:"selector"`
	Since      string        `json:"since" jsonschema:"start of the window, RFC3339 UTC"`
	Events     []changeEvent `json:"events" jsonschema:"newest first"`
	NextCursor string        `json:"nextCursor,omitempty"`
	Truncated  bool          `json:"truncated,omitempty"`
}

func (c *composites) getRecentChanges() mcpkit.Tool {
	return mcpkit.Tool{
		Name:  "get_recent_changes",
		Title: "Recent changes",
		Description: "\"What changed before it broke?\" A newest-first timeline for one service, environment or project over a time window (default 24h): " +
			"deployments (status, trigger, strategy, commit or image, who started them), and audit events with the actor type (user, token, mcp) and label: " +
			"staged edits to variables (keys only, never values), domains, service config, volumes and links, plus environment and project changes. " +
			"Give one selector: `service`, `environment` or `project`. Edits marked stagedEdit are only staged: they take effect when applied (apply_changes). " +
			"Paginate with `cursor`. It does not return logs or diffs of values: use diagnose_service for the current state, explain_failed_deployment for a failure, review_changes for pending changes.",
		Toolset: mcpkit.ToolsetCore,
		Scopes:  []string{policy.ScopeProjectsRead},
		Hints:   mcpkit.Hints{ReadOnly: true, Destructive: false, Idempotent: true, OpenWorld: false},
		Input:   getRecentChangesInput{},
		Output:  getRecentChangesOutput{},
		Call: func(ctx context.Context, p *auth.Principal, raw json.RawMessage) (any, error) {
			in, err := mcpkit.Decode[getRecentChangesInput](raw)
			if err != nil {
				return nil, err
			}
			detail, err := mcpkit.ParseDetail(in.Detail)
			if err != nil {
				return nil, err
			}
			svc, env, prj := strings.TrimSpace(in.Service), strings.TrimSpace(in.Environment), strings.TrimSpace(in.Project)
			switch {
			case svc == "" && env == "" && prj == "":
				return nil, invalid("give one of service, environment or project")
			case svc != "" && (env != "" || prj != ""):
				return nil, invalid("give a service on its own, or an environment, or a project")
			}
			now := c.Now()
			since, err := recentParseSince(in.Since, now)
			if err != nil {
				return nil, err
			}
			if in.Limit < 0 {
				return nil, invalid("limit must be positive")
			}
			limit := in.Limit
			if limit == 0 {
				limit = recentDefaultLimit
			}
			limit = min(limit, recentMaxLimit)
			cur, err := decodeChangeCursor(in.Cursor)
			if err != nil {
				return nil, err
			}
			return c.recentChanges(ctx, p, recentQuery{service: svc, environment: env, project: prj, since: since, limit: limit, cursor: cur, detail: detail})
		},
	}
}

// recentParseSince reads an RFC3339 timestamp or a duration ("90m", "24h", "7d").
func recentParseSince(s string, now time.Time) (time.Time, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return now.Add(-24 * time.Hour), nil
	}
	if t, err := time.Parse(time.RFC3339, s); err == nil {
		if t.After(now) {
			return time.Time{}, invalid("since is in the future")
		}
		return t.UTC(), nil
	}
	d, err := time.ParseDuration(s)
	if err != nil {
		if days, ok := strings.CutSuffix(s, "d"); ok {
			if n, perr := strconv.Atoi(days); perr == nil {
				d, err = time.Duration(n)*24*time.Hour, nil
			}
		}
	}
	if err != nil || d <= 0 {
		return time.Time{}, invalid("since must be an RFC3339 timestamp or a positive duration such as 90m, 24h or 7d")
	}
	return now.Add(-d), nil
}

// ---- cursor ----

// changeCursor is the position after the last returned event.
type changeCursor struct {
	at  time.Time
	key string
}

func (c changeCursor) encode() string {
	return base64.RawURLEncoding.EncodeToString([]byte(strconv.FormatInt(c.at.UnixNano(), 10) + "|" + c.key))
}

func decodeChangeCursor(s string) (*changeCursor, error) {
	if s == "" {
		return nil, nil
	}
	raw, err := base64.RawURLEncoding.DecodeString(s)
	if err != nil {
		return nil, invalid("invalid cursor")
	}
	ns, key, ok := strings.Cut(string(raw), "|")
	n, perr := strconv.ParseInt(ns, 10, 64)
	if !ok || perr != nil || key == "" {
		return nil, invalid("invalid cursor")
	}
	return &changeCursor{at: time.Unix(0, n).UTC(), key: key}, nil
}

// admits reports whether an item at (at, key) comes strictly after the cursor
// in newest-first order.
func (c *changeCursor) admits(at time.Time, key string) bool {
	if c == nil {
		return true
	}
	return at.Before(c.at) || (at.Equal(c.at) && key < c.key)
}

// ---- the timeline ----

type recentQuery struct {
	service, environment, project string
	since                         time.Time
	limit                         int
	cursor                        *changeCursor
	detail                        mcpkit.Detail
}

// timelineItem is an event with its sort key.
type timelineItem struct {
	at  time.Time
	key string
	ev  changeEvent
}

// changeScope is the resolved target of the timeline.
type changeScope struct {
	kind, selector string
	projectID      uuid.UUID
	// resourceIDs filters the audit read; nil reads every row of the project.
	resourceIDs []string
	// slugs are the service slugs of the scope, for removal rows of resources
	// that no longer exist (matched through metadata.service).
	slugs  []string
	labels map[string]string
	nodes  []*envNode
}

func (c *composites) recentChanges(ctx context.Context, p *auth.Principal, q recentQuery) (*getRecentChangesOutput, error) {
	sc, err := c.resolveChangeScope(ctx, p, q)
	if err != nil {
		return nil, err
	}
	if err := policy.Require(ctx, policy.ScopeProjectsRead, &sc.projectID); err != nil {
		return nil, err
	}

	fetch := q.limit + recentSlack
	run := c.newPartial(ctx, "get_recent_changes", 6)
	var (
		mu    sync.Mutex
		items []timelineItem
	)
	addDeps := func(ds []deployments.DeploymentView, label func(uuid.UUID) string) {
		mu.Lock()
		defer mu.Unlock()
		for _, d := range ds {
			if d.CreatedAt.Before(q.since) {
				continue
			}
			if sc.resourceIDs != nil { // deployment.cancel audit rows are filed under the deployment id
				sc.resourceIDs = append(sc.resourceIDs, d.ID.String())
			}
			items = append(items, timelineItem{at: d.CreatedAt, key: "d:" + d.ID.String(), ev: deploymentEvent(d, label(d.ServiceID), q.detail)})
		}
	}
	svcLabel := func(id uuid.UUID) string { return sc.labels[id.String()] }
	switch sc.kind {
	case "service":
		run.run("deployments", func(ctx context.Context) error {
			sid, _ := uuid.Parse(sc.resourceIDs[0])
			ds, _, err := c.Deployments.List(ctx, sid, "", fetch)
			if err != nil {
				return err
			}
			addDeps(ds, svcLabel)
			return nil
		})
	default:
		for _, n := range sc.nodes {
			run.run("deployments "+n.path(), func(ctx context.Context) error {
				ds, _, err := c.Deployments.ListEnvironment(ctx, n.Env.ID, "", nil, "", fetch)
				if err != nil {
					return err
				}
				views := make([]deployments.DeploymentView, len(ds))
				for i, d := range ds {
					views[i] = d.DeploymentView
				}
				addDeps(views, svcLabel)
				return nil
			})
		}
	}
	errs := run.wait()
	// Hold back the audit read until the deployment ids are known so
	// deployment.cancel rows of this scope are included.
	rows, aerr := c.readAudit(ctx, p, sc, q, fetch)
	if aerr != nil {
		errs = append(errs, sectionError("audit", aerr))
	}
	for _, r := range rows {
		items = append(items, r.item(sc, q.detail))
	}

	sort.SliceStable(items, func(i, j int) bool {
		if !items[i].at.Equal(items[j].at) {
			return items[i].at.After(items[j].at)
		}
		return items[i].key > items[j].key
	})
	kept := items[:0]
	for _, it := range items {
		if q.cursor.admits(it.at, it.key) {
			kept = append(kept, it)
		}
	}
	more := len(kept) > q.limit
	if more {
		kept = kept[:q.limit]
	}

	out := &getRecentChangesOutput{
		Envelope: Envelope{Errors: errs, NextTools: []NextTool{}},
		Scope:    sc.kind, Selector: sc.selector, Since: utc(q.since), Events: make([]changeEvent, 0, len(kept)),
	}
	for _, it := range kept {
		out.Events = append(out.Events, it.ev)
	}
	// Stay under the 64 KB cap: drop the oldest events and resume from the last kept one.
	for mcpkit.JSONSize(out) > mcpkit.MaxResultBytes-1024 && len(kept) > 1 {
		kept = kept[:len(kept)*3/4]
		out.Events = out.Events[:len(kept)]
		out.Truncated, more = true, true
	}
	if more && len(kept) > 0 {
		last := kept[len(kept)-1]
		out.NextCursor = changeCursor{at: last.at, key: last.key}.encode()
	}
	out.Summary, out.NextTools = recentSummary(out, sc, q)
	return out, nil
}

func (c *composites) resolveChangeScope(ctx context.Context, p *auth.Principal, q recentQuery) (*changeScope, error) {
	switch {
	case q.service != "":
		v, err := c.Resolver.Service(ctx, q.service)
		if err != nil {
			return nil, err
		}
		scp, err := c.scope(ctx, v.ID)
		if err != nil {
			return nil, err
		}
		sel := selectorOf(scp)
		sc := &changeScope{kind: "service", selector: sel, projectID: v.ProjectID, slugs: []string{v.Slug}, labels: map[string]string{}}
		sc.resourceIDs = []string{v.ID.String(), v.EnvironmentID.String(), v.ProjectID.String()}
		sc.labels[v.ID.String()] = sel
		sc.labels[v.EnvironmentID.String()] = "environment " + path(scp.ProjectSlug, scp.EnvSlug)
		sc.labels[v.ProjectID.String()] = "project " + scp.ProjectSlug
		labelService(sc, sel, v)
		if links, err := c.Services.ListLinks(ctx, v.EnvironmentID); err == nil {
			for _, l := range links {
				if l.FromServiceID == v.ID || l.ToServiceID == v.ID {
					sc.resourceIDs = append(sc.resourceIDs, l.ID)
				}
			}
		}
		return sc, nil
	case q.environment != "":
		sel := q.environment
		if q.project != "" && !strings.Contains(sel, "/") {
			if _, err := uuid.Parse(sel); err != nil {
				sel = q.project + "/" + sel
			}
		}
		env, err := c.Resolver.Environment(ctx, sel)
		if err != nil {
			return nil, err
		}
		prj, err := c.Projects.Get(ctx, env.ProjectID)
		if err != nil {
			return nil, err
		}
		n := &envNode{Project: prj, Env: env}
		svcs, truncated, err := c.listAllServices(ctx, env.ID)
		if err != nil {
			return nil, err
		}
		n.Services, n.ServicesTruncated = svcs, truncated
		sc := &changeScope{kind: "environment", selector: n.path(), projectID: prj.ID, labels: map[string]string{}, nodes: []*envNode{n}}
		sc.resourceIDs = []string{env.ID.String(), prj.ID.String()}
		sc.labels[env.ID.String()] = "environment " + n.path()
		sc.labels[prj.ID.String()] = "project " + prj.Slug
		for _, v := range svcs {
			sc.slugs = append(sc.slugs, v.Slug)
			labelService(sc, n.svcPath(v), v)
		}
		if links, err := c.Services.ListLinks(ctx, env.ID); err == nil {
			for _, l := range links {
				sc.resourceIDs = append(sc.resourceIDs, l.ID)
			}
		}
		return sc, nil
	}
	prj, err := c.Resolver.Project(ctx, q.project)
	if err != nil {
		return nil, err
	}
	envs, err := c.Environments.List(ctx, prj.ID)
	if err != nil {
		return nil, err
	}
	sc := &changeScope{kind: "project", selector: prj.Slug, projectID: prj.ID, labels: map[string]string{prj.ID.String(): "project " + prj.Slug}}
	for _, e := range envs {
		n := &envNode{Project: prj, Env: e}
		sc.labels[e.ID.String()] = "environment " + n.path()
		svcs, truncated, err := c.listAllServices(ctx, e.ID)
		if err != nil {
			return nil, err
		}
		n.Services, n.ServicesTruncated = svcs, truncated
		for _, v := range svcs {
			labelService(sc, n.svcPath(v), v)
		}
		sc.nodes = append(sc.nodes, n)
	}
	return sc, nil
}

// labelService records the display names of a service and of the domains
// and volumes attached to it, and (for a filtered read) their ids.
func labelService(sc *changeScope, sel string, v services.ServiceView) {
	sc.labels[v.ID.String()] = sel
	if sc.resourceIDs != nil && !containsStr(sc.resourceIDs, v.ID.String()) {
		sc.resourceIDs = append(sc.resourceIDs, v.ID.String())
	}
	for _, d := range v.Domains {
		sc.labels[d.ID.String()] = "domain " + d.Hostname + " (" + sel + ")"
		if sc.resourceIDs != nil && len(sc.resourceIDs) < recentMaxIDs {
			sc.resourceIDs = append(sc.resourceIDs, d.ID.String())
		}
	}
	for _, vol := range v.Volumes {
		sc.labels[vol.ID.String()] = "volume " + vol.Name + " (" + sel + ")"
		if sc.resourceIDs != nil && len(sc.resourceIDs) < recentMaxIDs {
			sc.resourceIDs = append(sc.resourceIDs, vol.ID.String())
		}
	}
}

// ---- audit read ----

// auditRow is one audit_events row.
type auditRow struct {
	id           int64
	actorType    string
	actorLabel   string
	action       string
	resourceType string
	resourceID   string
	metadata     map[string]any
	at           time.Time
}

// readAudit reads the project's audit rows for the scope with a parameterized
// query. audit.Service.List needs the admin scope, so the projects:read
// variant lives here: the caller must be able to see the project (checked by
// the caller with policy.Require), rows are restricted to the caller's
// workspace and to project_id = the scope's project (rows without a project
// are never returned), and optionally to the scope's resource ids.
func (c *composites) readAudit(ctx context.Context, p *auth.Principal, sc *changeScope, q recentQuery, limit int) ([]auditRow, error) {
	args := []any{p.WorkspaceID, sc.projectID, q.since}
	sql := `SELECT id, actor_type, actor_label, action, resource_type, resource_id, metadata, created_at
FROM audit_events
WHERE workspace_id = $1 AND project_id = $2 AND created_at >= $3
  AND action NOT IN ('deployment.create', 'deployment.rollback')`
	if q.cursor != nil {
		args = append(args, q.cursor.at)
		sql += fmt.Sprintf(" AND created_at <= $%d", len(args))
	}
	if sc.resourceIDs != nil {
		ids := dedupe(sc.resourceIDs)
		args = append(args, ids)
		sql += fmt.Sprintf(" AND (resource_id = ANY($%d::text[])", len(args))
		if len(sc.slugs) > 0 {
			args = append(args, sc.slugs)
			sql += fmt.Sprintf(" OR (action IN ('domain.remove', 'volume.delete') AND metadata->>'service' = ANY($%d::text[]))", len(args))
		}
		sql += ")"
	}
	args = append(args, limit)
	sql += fmt.Sprintf(" ORDER BY created_at DESC, id DESC LIMIT $%d", len(args))

	rows, err := c.Pool.Query(ctx, sql, args...)
	if err != nil {
		return nil, fmt.Errorf("read audit events: %w", err)
	}
	defer rows.Close()
	var out []auditRow
	for rows.Next() {
		var r auditRow
		var meta []byte
		if err := rows.Scan(&r.id, &r.actorType, &r.actorLabel, &r.action, &r.resourceType, &r.resourceID, &meta, &r.at); err != nil {
			return nil, fmt.Errorf("scan audit event: %w", err)
		}
		if len(meta) > 0 {
			_ = json.Unmarshal(meta, &r.metadata)
		}
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read audit events: %w", err)
	}
	return out, nil
}

func dedupe(in []string) []string {
	seen := make(map[string]bool, len(in))
	out := make([]string, 0, len(in))
	for _, s := range in {
		if !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	return out
}

// stagedEdit reports whether an audit action only stages a change.
func stagedEdit(action string) bool {
	switch {
	case strings.HasPrefix(action, "variable."):
		return action != "variable.rotation_due"
	case strings.HasPrefix(action, "volume."), strings.HasPrefix(action, "link."), strings.HasPrefix(action, "domain."):
		return true
	}
	return action == "service.update"
}

func category(action string) string {
	prefix, _, _ := strings.Cut(action, ".")
	switch prefix {
	case "deployment", "service", "variable", "domain", "volume", "link", "environment", "project":
		return prefix
	}
	return "other"
}

func (r auditRow) item(sc *changeScope, detail mcpkit.Detail) timelineItem {
	meta := scrubMetadata(r.action, r.metadata)
	label := sc.labels[r.resourceID]
	if label == "" {
		label = r.resourceType + " " + clip(r.resourceID, 36)
		if r.resourceType == "link" {
			if from, to := metaString(meta, "from"), metaString(meta, "to"); from != "" || to != "" {
				label = "link " + from + " → " + to
			}
		}
		if s := metaString(meta, "service"); s != "" && (r.resourceType == "domain" || r.resourceType == "volume") {
			label = r.resourceType + " " + chgFirst(metaString(meta, "hostname"), metaString(meta, "name")) + " (service " + s + ")"
		}
		if r.resourceType == "service" {
			label = "service " + chgFirst(metaString(meta, "slug"), metaString(meta, "name"), clip(r.resourceID, 36))
		}
	}
	actor := r.actorType
	ev := changeEvent{
		At: utc(r.at), Kind: "audit", Category: category(r.action), Action: r.action, Resource: label,
		ResourceType: r.resourceType, ResourceID: r.resourceID, ActorType: actor, Actor: r.actorLabel,
		Client:     chgFirst(metaString(meta, "client"), metaString(meta, "clientName"), metaString(meta, "mcpClient")),
		StagedEdit: stagedEdit(r.action), What: whatChanged(r.action, meta),
	}
	if detail.Full() && len(meta) > 0 {
		ev.Metadata = meta
	}
	return timelineItem{at: r.at, key: fmt.Sprintf("a:%020d", r.id), ev: ev}
}

func deploymentEvent(d deployments.DeploymentView, svcLabel string, detail mcpkit.Detail) changeEvent {
	ev := changeEvent{
		At: utc(d.CreatedAt), Kind: "deployment", Category: "deployment", Action: "deployment." + d.Status,
		Resource: chgFirst(svcLabel, "service "+d.ServiceID.String()), ResourceType: "deployment", ResourceID: d.ID.String(),
		ActorType: d.ActorType, Actor: d.ActorLabel, DeploymentID: d.ID.String(),
		Status: d.Status, Trigger: d.Trigger, Strategy: d.Strategy,
	}
	if d.Commit != nil && d.Commit.SHA != "" {
		// Only the sha: the branch name comes from outside the team and could
		// carry instructions for the model.
		ev.Commit = clip(d.Commit.SHA, 12)
	}
	if d.Release != nil {
		ev.Image = clip(d.Release.ImageRef, 120)
	}
	if detail.Full() {
		ev.StatusMessage = untrustedText("deployment.statusMessage", clip(d.StatusMessage, 200))
	}
	return ev
}

// ---- metadata ----

// forbiddenMetaWords drop a metadata key when its lowercase name contains one.
var forbiddenMetaWords = []string{"value", "secret", "token", "password", "passwd", "credential", "authorization", "apikey", "private"}

// variableOnlyKeys are dropped from variable.* rows: the audit trail records
// the previous and new values of non-secret variables and fingerprints of
// secrets, none of which may leave the control plane.
var variableOnlyKeys = []string{"from", "to", "fingerprint"}

// scrubMetadata returns a copy of m without anything that could carry a value.
func scrubMetadata(action string, m map[string]any) map[string]any {
	if len(m) == 0 {
		return nil
	}
	strict := strings.HasPrefix(action, "variable.")
	out := scrubMap(m, strict)
	if len(out) == 0 {
		return nil
	}
	return out
}

func dropKey(k string, strict bool) bool {
	l := strings.ToLower(k)
	for _, w := range forbiddenMetaWords {
		if strings.Contains(l, w) {
			return true
		}
	}
	if strict {
		for _, w := range variableOnlyKeys {
			if strings.HasPrefix(l, w) {
				return true
			}
		}
	}
	return false
}

func scrubMap(m map[string]any, strict bool) map[string]any {
	out := make(map[string]any, len(m))
	for k, v := range m {
		if dropKey(k, strict) {
			continue
		}
		out[k] = scrubValue(v, strict)
	}
	return out
}

func scrubValue(v any, strict bool) any {
	switch x := v.(type) {
	case map[string]any:
		return scrubMap(x, strict)
	case []any:
		out := make([]any, len(x))
		for i, e := range x {
			out[i] = scrubValue(e, strict)
		}
		return out
	case string:
		return clip(x, 300)
	}
	return v
}

func metaString(m map[string]any, key string) string {
	s, _ := m[key].(string)
	return s
}

func metaStrings(m map[string]any, key string) []string {
	raw, _ := m[key].([]any)
	out := make([]string, 0, len(raw))
	for _, e := range raw {
		if s, ok := e.(string); ok {
			out = append(out, s)
		}
	}
	return out
}

func chgFirst(ss ...string) string {
	for _, s := range ss {
		if s != "" {
			return s
		}
	}
	return ""
}

// whatChanged is a short, value-free description built from an allowlist of
// scrubbed metadata fields.
func whatChanged(action string, m map[string]any) string {
	var parts []string
	list := func(label, key string) {
		if l := metaStrings(m, key); len(l) > 0 {
			if len(l) > 8 {
				l = append(l[:8:8], fmt.Sprintf("+%d more", len(l)-8))
			}
			parts = append(parts, label+": "+strings.Join(l, ", "))
		}
	}
	list("keys", "keys")
	list("fields", "fields")
	if h := metaString(m, "hostname"); h != "" {
		parts = append(parts, h)
	}
	if n := metaString(m, "name"); n != "" && !strings.HasPrefix(action, "service.") {
		parts = append(parts, n)
	}
	if s := metaString(m, "service"); s != "" && (strings.HasPrefix(action, "domain.") || strings.HasPrefix(action, "volume.")) {
		parts = append(parts, "service "+s)
	}
	if strings.HasPrefix(action, "link.") {
		if from, to := metaString(m, "from"), metaString(m, "to"); from != "" || to != "" {
			parts = append(parts, from+" → "+to)
		}
	}
	return clip(strings.Join(parts, "; "), 240)
}

// ---- summary ----

func recentSummary(o *getRecentChangesOutput, sc *changeScope, q recentQuery) (string, []NextTool) {
	next := []NextTool{}
	var deps, audits, staged int
	byActor := map[string]int{}
	var failed *changeEvent
	for i := range o.Events {
		e := o.Events[i]
		if e.Kind == "deployment" {
			deps++
			if failed == nil && deploymentFailed(e.Status) {
				failed = &o.Events[i]
			}
		} else {
			audits++
		}
		if e.StagedEdit {
			staged++
		}
		byActor[e.ActorType]++
	}
	scopeName := sc.kind + " " + sc.selector
	var s string
	if len(o.Events) == 0 {
		s = fmt.Sprintf("No changes to %s since %s.", scopeName, utc(q.since))
		if q.cursor == nil {
			s += " Widen the window with `since` (e.g. 7d)."
		}
	} else {
		actors := make([]string, 0, 4)
		for _, t := range []string{"user", "token", "mcp", "system"} {
			if byActor[t] > 0 {
				actors = append(actors, fmt.Sprintf("%d %s", byActor[t], t))
			}
		}
		s = fmt.Sprintf("%s on %s since %s: %s, %s (%s). Newest: %s %s by %s.", plural(len(o.Events), "change", "changes"), scopeName, utc(q.since),
			plural(deps, "deployment", "deployments"), plural(audits, "audit event", "audit events"), strings.Join(actors, ", "),
			o.Events[0].Action, o.Events[0].Resource, chgFirst(o.Events[0].Actor, o.Events[0].ActorType))
		if staged > 0 {
			s += " " + plural(staged, "edit is", "edits are") + " only staged until applied."
		}
	}
	if o.NextCursor != "" {
		s += " More events: pass nextCursor as `cursor`."
		args := map[string]any{sc.kind: sc.selector, "cursor": o.NextCursor}
		args["since"] = utc(q.since)
		next = append(next, NextTool{Tool: "get_recent_changes", Args: args, Why: "Older changes in the window."})
	}
	if failed != nil {
		next = append(next, NextTool{Tool: "explain_failed_deployment", Args: map[string]any{"deployment": failed.DeploymentID}, Why: "The deployment " + failed.Status + " at " + failed.At + "."})
	}
	if staged > 0 {
		args := map[string]any{}
		if sc.kind == "service" {
			args["service"] = sc.selector
		} else if sc.kind == "environment" {
			args["environment"] = sc.selector
		}
		if len(args) > 0 {
			next = append(next, NextTool{Tool: "review_changes", Args: args, Why: "See which staged changes are still pending."})
		}
	}
	if sc.kind == "service" {
		next = append(next, NextTool{Tool: "diagnose_service", Args: map[string]any{"service": sc.selector}, Why: "Current state, logs and findings."})
	}
	return s, next
}
