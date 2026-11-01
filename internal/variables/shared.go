package variables

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/nebulactrl/nebulactrl/internal/apierr"
	"github.com/nebulactrl/nebulactrl/internal/audit"
	"github.com/nebulactrl/nebulactrl/internal/auth"
	"github.com/nebulactrl/nebulactrl/internal/db"
	"github.com/nebulactrl/nebulactrl/internal/policy"
	"github.com/nebulactrl/nebulactrl/internal/secretbox"
	"github.com/nebulactrl/nebulactrl/internal/services"
	"github.com/nebulactrl/nebulactrl/internal/varexpr"
)

// Shared layers.
const (
	LayerProject     = "project"
	LayerEnvironment = "environment"
	LayerTeam        = "team"
)

// SharedVariableView is the API view of a shared variable. Secret values are never
// returned.
type SharedVariableView struct {
	Key            string                  `json:"key"`
	Value          *string                 `json:"value,omitempty" doc:"Omitted for secrets"`
	IsSecret       bool                    `json:"isSecret"`
	ComputedSecret bool                    `json:"computedSecret"`
	Set            bool                    `json:"set"`
	Layer          string                  `json:"layer" enum:"project,environment,team"`
	Overrides      bool                    `json:"overrides" doc:"An environment value that overrides a project value"`
	Inherited      bool                    `json:"inherited" doc:"A project value shown in an environment listing; edit it on the project"`
	Source         string                  `json:"source"`
	Version        int                     `json:"version"`
	Comment        string                  `json:"comment"`
	Generated      []varexpr.GeneratorCall `json:"generated" nullable:"true"`
	UsedBy         int                     `json:"usedBy" doc:"Number of service variables that reference it"`
	UpdatedAt      time.Time               `json:"updatedAt"`
}

// SharedVariableList is the response of listing or writing shared variables.
type SharedVariableList struct {
	Items    []SharedVariableView `json:"items"`
	Warnings []VariableWarning    `json:"warnings"`
	Restaged []RestagedServiceRef `json:"restaged"`
}

// SharedVariableInput is one shared variable of a bulk upsert.
type SharedVariableInput struct {
	Key      string  `json:"key" minLength:"1" maxLength:"255"`
	Value    *string `json:"value,omitempty" doc:"Omit to keep the stored value. Generators run once; the same text again keeps the value."`
	IsSecret *bool   `json:"isSecret,omitempty"`
	Comment  *string `json:"comment,omitempty" maxLength:"1000"`
}

// VariableUsage is one variable that references another.
type VariableUsage struct {
	ServiceID uuid.UUID `json:"serviceId"`
	Slug      string    `json:"slug"`
	Key       string    `json:"key"`
}

// target is the project or environment a shared variable lives in.
type target struct {
	kind      string // project | environment | team
	ownerID   uuid.UUID
	projectID uuid.UUID // uuid.Nil for the team layer
	workspace uuid.UUID
	envID     *uuid.UUID
	infos     []services.EnvInfo // environments whose resolution can change
	resource  string
}

func (t target) ownerKind() string {
	switch t.kind {
	case LayerProject:
		return services.OwnerProject
	case LayerTeam:
		return services.OwnerTeam
	}
	return services.OwnerEnvironment
}

// projectRef is the project the layer belongs to; nil for the team layer.
func (t target) projectRef() *uuid.UUID {
	if t.kind == LayerTeam {
		return nil
	}
	return &t.projectID
}

// loadTeamTarget authorizes access to the team's shared variables. They apply
// to every project, so a principal restricted to some projects has no access,
// and only owners and admins may change them.
func (s *Service) loadTeamTarget(ctx context.Context, q *db.Queries, scope string) (target, error) {
	if err := policy.Require(ctx, scope, nil); err != nil {
		return target{}, err
	}
	p, err := policy.Authenticated(ctx)
	if err != nil {
		return target{}, err
	}
	if p.Restricted() {
		return target{}, apierr.Forbidden()
	}
	if scope == policy.ScopeProjectsWrite {
		if err := policy.RequireRole(ctx, nil, auth.RoleOwner, auth.RoleAdmin); err != nil {
			return target{}, err
		}
	}
	ws := policy.WorkspaceID(ctx)
	rows, err := q.ListWorkspaceEnvInfos(ctx, ws)
	if err != nil {
		return target{}, err
	}
	t := target{kind: LayerTeam, ownerID: ws, workspace: ws, resource: "team"}
	for _, r := range rows {
		t.infos = append(t.infos, services.EnvInfo{WorkspaceID: ws, ID: r.ID, ProjectID: r.ProjectID, ProjectName: r.ProjectName, Slug: r.Slug, Name: r.Name})
	}
	return t, nil
}

func (s *Service) loadProjectTarget(ctx context.Context, q *db.Queries, scope string, id uuid.UUID) (target, error) {
	if err := policy.Require(ctx, scope, nil); err != nil {
		return target{}, err
	}
	ws := policy.WorkspaceID(ctx)
	proj, err := q.GetProject(ctx, db.GetProjectParams{ID: id, WorkspaceID: ws})
	if errors.Is(err, pgx.ErrNoRows) {
		return target{}, apierr.NotFound("project")
	}
	if err != nil {
		return target{}, err
	}
	if err := policy.Require(ctx, scope, &proj.ID); err != nil {
		return target{}, err
	}
	envs, err := q.ListEnvironments(ctx, proj.ID)
	if err != nil {
		return target{}, err
	}
	t := target{kind: LayerProject, ownerID: proj.ID, projectID: proj.ID, workspace: ws, resource: "project"}
	for _, e := range envs {
		t.infos = append(t.infos, services.EnvInfo{ID: e.ID, ProjectID: proj.ID, ProjectName: proj.Name, Slug: e.Slug, Name: e.Name})
	}
	return t, nil
}

func (s *Service) loadEnvTarget(ctx context.Context, q *db.Queries, scope string, id uuid.UUID) (target, error) {
	env, err := services.LoadEnvironment(ctx, q, scope, id)
	if err != nil {
		return target{}, err
	}
	info, err := services.LoadEnvInfo(ctx, q, id)
	if err != nil {
		return target{}, err
	}
	eid := env.Env.ID
	return target{kind: LayerEnvironment, ownerID: eid, projectID: env.Env.ProjectID, workspace: env.WorkspaceID, envID: &eid, infos: []services.EnvInfo{info}, resource: "environment"}, nil
}

func (s *Service) getShared(ctx context.Context, q *db.Queries, t target, key string) (db.SharedVariable, bool, error) {
	var (
		row db.SharedVariable
		err error
	)
	switch t.kind {
	case LayerProject:
		row, err = q.GetSharedVariableProject(ctx, db.GetSharedVariableProjectParams{ProjectID: t.projectID, Key: key})
	case LayerTeam:
		row, err = q.GetSharedVariableTeam(ctx, db.GetSharedVariableTeamParams{WorkspaceID: t.workspace, Key: key})
	default:
		row, err = q.GetSharedVariableEnv(ctx, db.GetSharedVariableEnvParams{EnvironmentID: &t.ownerID, Key: key})
	}
	if errors.Is(err, pgx.ErrNoRows) {
		return db.SharedVariable{}, false, nil
	}
	return row, err == nil, err
}

// checkSharedTemplate allows only shared.* and team.* references (team values:
// team.* only), external secret references, generators and transforms: shared
// values are evaluated in every environment they reach.
func checkSharedTemplate(kind, key string, tpl *varexpr.Template) error {
	for _, r := range tpl.Refs() {
		switch {
		case r.NS == varexpr.NSTeam:
		case r.NS == varexpr.NSShared && kind != LayerTeam:
		default:
			allowed := "other shared variables (shared.KEY), team variables (team.KEY)"
			if kind == LayerTeam {
				allowed = "other team variables (team.KEY)"
			}
			return apierr.Unprocessable("invalid_shared_reference",
				fmt.Sprintf("shared variable %s references ${{%s}}: shared values may only reference %s, external secrets, generators and transforms", key, r.String(), allowed))
		}
	}
	return nil
}

type sharedWrite struct {
	value    string
	secret   bool
	source   string
	template string
	calls    []varexpr.GeneratorCall
	warnings []VariableWarning
}

func (s *Service) prepareShared(kind string, cur *db.SharedVariable, in SharedVariableInput) (sharedWrite, error) {
	var w sharedWrite
	var oldValue, oldTemplate string
	var err error
	if cur != nil {
		if oldValue, err = s.box.OpenString(secretbox.SharedVariablesValue, cur.ID.String(), cur.ValueEnc); err != nil {
			return w, err
		}
		if len(cur.TemplateEnc) > 0 {
			if oldTemplate, err = s.box.OpenString(secretbox.SharedVariablesTemplate, cur.ID.String(), cur.TemplateEnc); err != nil {
				return w, err
			}
		}
	}
	secret := cur != nil && cur.IsSecret
	if in.IsSecret != nil {
		secret = *in.IsSecret
	}
	if in.Value == nil {
		if cur == nil {
			return w, apierr.Unprocessable("value_required", "shared variable "+in.Key+" needs a value")
		}
		if cur.IsSecret && in.IsSecret != nil && !*in.IsSecret {
			return w, apierr.Unprocessable("value_required", "provide a value to make secret "+in.Key+" non-secret")
		}
		return sharedWrite{value: oldValue, secret: secret, source: cur.Source, template: oldTemplate, calls: decodeCalls(cur.GenCalls)}, nil
	}
	val := *in.Value
	tpl, err := varexpr.Parse(val)
	if err != nil {
		return w, templateError(in.Key, err)
	}
	if err := checkSharedTemplate(kind, in.Key, tpl); err != nil {
		return w, err
	}
	if !tpl.HasGenerators() {
		if _, err := varexpr.Validate(tpl); err != nil {
			return w, templateError(in.Key, err)
		}
		return sharedWrite{value: val, secret: secret, source: services.SourceManual, warnings: hygiene(in.Key, val)}, nil
	}
	if cur != nil && cur.Source == services.SourceGenerated && oldTemplate == val {
		return sharedWrite{value: oldValue, secret: secret || cur.IsSecret, source: services.SourceGenerated, template: oldTemplate, calls: decodeCalls(cur.GenCalls)}, nil
	}
	m, err := varexpr.MaterializeTemplate(tpl)
	if err != nil {
		return w, templateError(in.Key, err)
	}
	if m.Secret && in.IsSecret != nil && !*in.IsSecret {
		return w, apierr.Unprocessable("secret_required", "shared variable "+in.Key+" uses secret()/randomBytes(): generated secrets are always secret")
	}
	w = sharedWrite{value: m.Stored, secret: secret || m.Secret, source: services.SourceGenerated, template: val, calls: m.Calls}
	for _, mw := range m.Warnings {
		w.warnings = append(w.warnings, VariableWarning{Key: in.Key, Code: mw.Code, Message: mw.Message})
	}
	return w, nil
}

// writeShared stores one shared variable; it returns the change (nil for a
// no-op).
func (s *Service) writeShared(ctx context.Context, q *db.Queries, t target, key string, w sharedWrite, comment *string, op string) (*changeEntry, error) {
	cur, exists, err := s.getShared(ctx, q, t, key)
	if err != nil {
		return nil, err
	}
	var oldValue, oldTemplate string
	if exists {
		if oldValue, err = s.box.OpenString(secretbox.SharedVariablesValue, cur.ID.String(), cur.ValueEnc); err != nil {
			return nil, err
		}
		if len(cur.TemplateEnc) > 0 {
			if oldTemplate, err = s.box.OpenString(secretbox.SharedVariablesTemplate, cur.ID.String(), cur.TemplateEnc); err != nil {
				return nil, err
			}
		}
	}
	cm := ""
	if exists {
		cm = cur.Comment
	}
	if comment != nil {
		cm = *comment
	}
	if exists && oldValue == w.value && cur.IsSecret == w.secret && cur.Source == w.source && oldTemplate == w.template && cur.Comment == cm {
		return nil, nil
	}
	// The row id is part of the sealed values' context: an existing row keeps
	// its id, a new row gets one before anything is sealed.
	rowID := uuid.New()
	if exists {
		rowID = cur.ID
	}
	enc, err := s.box.SealString(secretbox.SharedVariablesValue, rowID.String(), w.value)
	if err != nil {
		return nil, err
	}
	var tmplEnc []byte
	if w.template != "" {
		if tmplEnc, err = s.box.SealString(secretbox.SharedVariablesTemplate, rowID.String(), w.template); err != nil {
			return nil, err
		}
	}
	next, err := q.NextVariableVersion(ctx, db.NextVariableVersionParams{OwnerKind: t.ownerKind(), OwnerID: t.ownerID, Key: key})
	if err != nil {
		return nil, err
	}
	var genAt *time.Time
	if w.source == services.SourceGenerated {
		now := time.Now()
		genAt = &now
	}
	calls := marshalCalls(w.calls)
	if exists {
		_, err = q.UpdateSharedVariable(ctx, db.UpdateSharedVariableParams{
			ID: cur.ID, ValueEnc: enc, IsSecret: w.secret, Source: w.source, TemplateEnc: tmplEnc, GenCalls: calls, Version: next, GeneratedAt: genAt, Comment: cm,
		})
	} else {
		_, err = q.InsertSharedVariable(ctx, db.InsertSharedVariableParams{
			ID: rowID, WorkspaceID: t.workspace, ProjectID: t.projectRef(), EnvironmentID: t.envID, Key: key, ValueEnc: enc, IsSecret: w.secret, Source: w.source,
			TemplateEnc: tmplEnc, GenCalls: calls, Version: next, GeneratedAt: genAt, Comment: cm,
		})
	}
	if err != nil {
		return nil, err
	}
	fp := s.box.Fingerprint(w.value)
	if err := services.RecordSharedVersion(ctx, q, s.box, t.workspace, t.projectID, t.ownerKind(), t.ownerID, key, next, op, w.secret, w.source, w.value, calls); err != nil {
		return nil, err
	}
	c := &changeEntry{Key: key, Op: op, Version: next, FingerprintTo: fp}
	if exists {
		c.FingerprintFr = s.box.Fingerprint(oldValue)
	}
	if !w.secret && !(exists && cur.IsSecret) {
		c.To = truncate(w.value)
		if exists {
			c.From = truncate(oldValue)
		}
	} else if w.source == services.SourceGenerated {
		c.To = "generated " + services.GenDescriptor(calls)
	}
	return c, nil
}

func marshalCalls(calls []varexpr.GeneratorCall) []byte {
	if len(calls) == 0 {
		return nil
	}
	b, _ := jsonMarshal(calls)
	return b
}

func (s *Service) auditShared(ctx context.Context, tx pgx.Tx, t target, action string, changes []changeEntry, restaged []RestagedServiceRef) error {
	keys := make([]string, len(changes))
	for i, c := range changes {
		keys[i] = c.Key
	}
	affected := make([]string, len(restaged))
	for i, r := range restaged {
		affected[i] = r.Slug
	}
	return audit.Record(ctx, tx, audit.Entry{
		Action: action, ResourceType: t.resource, ResourceID: t.ownerID.String(), ProjectID: t.projectRef(), WorkspaceID: t.workspace,
		Metadata: map[string]any{"layer": t.kind, "keys": keys, "changes": changes, "affected": affected},
	})
}

func (s *Service) publishShared(t target, restaged []RestagedServiceRef) {
	env := uuid.Nil
	if t.envID != nil {
		env = *t.envID
	}
	services.Publish(s.bus, services.EventVariableUpdated, t.projectID, env, t.ownerID)
	for _, r := range restaged {
		for _, info := range t.infos {
			services.Publish(s.bus, services.EventServiceUpdated, info.ProjectID, info.ID, r.ServiceID)
		}
	}
}

// ListShared lists the shared variables of a project (layer project) or an
// environment (its own layer plus the project values it inherits).
func (s *Service) ListShared(ctx context.Context, kind string, id uuid.UUID) (SharedVariableList, error) {
	t, err := s.loadTarget(ctx, s.q, policy.ScopeProjectsRead, kind, id)
	if err != nil {
		return SharedVariableList{}, err
	}
	return s.listShared(ctx, s.q, t)
}

func (s *Service) loadTarget(ctx context.Context, q *db.Queries, scope, kind string, id uuid.UUID) (target, error) {
	switch kind {
	case LayerProject:
		return s.loadProjectTarget(ctx, q, scope, id)
	case LayerTeam:
		return s.loadTeamTarget(ctx, q, scope)
	}
	return s.loadEnvTarget(ctx, q, scope, id)
}

func (s *Service) listShared(ctx context.Context, q *db.Queries, t target) (SharedVariableList, error) {
	var rows []db.SharedVariable
	var err error
	own := map[string]bool{}
	switch t.kind {
	case LayerProject:
		rows, err = q.ListSharedVariablesProject(ctx, t.projectID)
	case LayerTeam:
		rows, err = q.ListTeamSharedVariables(ctx, t.workspace)
	default:
		rows, err = q.ListSharedVariablesEnv(ctx, &t.ownerID)
	}
	if err != nil {
		return SharedVariableList{}, err
	}
	for _, r := range rows {
		own[r.Key] = true
	}
	var inherited []db.SharedVariable
	projectKeys := map[string]bool{}
	if t.kind == LayerEnvironment {
		pr, err := q.ListSharedVariablesProject(ctx, t.projectID)
		if err != nil {
			return SharedVariableList{}, err
		}
		for _, r := range pr {
			projectKeys[r.Key] = true
			if !own[r.Key] {
				inherited = append(inherited, r)
			}
		}
	}
	usage, err := s.usageCounts(ctx, q, t.kind, t.infos)
	if err != nil {
		return SharedVariableList{}, err
	}
	out := SharedVariableList{Items: []SharedVariableView{}, Warnings: []VariableWarning{}, Restaged: []RestagedServiceRef{}}
	computed := map[string]bool{}
	if len(t.infos) == 1 || (t.kind == LayerTeam && len(t.infos) > 0) {
		if res, err := services.LoadResolver(ctx, q, s.box, t.infos[0]); err == nil {
			for k := range sharedSecretness(res, t.kind) {
				computed[k] = true
			}
		}
	}
	add := func(r db.SharedVariable, inh bool) error {
		plain, err := s.box.OpenString(secretbox.SharedVariablesValue, r.ID.String(), r.ValueEnc)
		if err != nil {
			return fmt.Errorf("decrypt shared variable %s: %w", r.Key, err)
		}
		layer := LayerProject
		switch {
		case r.EnvironmentID != nil:
			layer = LayerEnvironment
		case r.ProjectID == nil:
			layer = LayerTeam
		}
		v := SharedVariableView{
			Key: r.Key, IsSecret: r.IsSecret, ComputedSecret: r.IsSecret || computed[r.Key], Set: true, Layer: layer,
			Overrides: layer == LayerEnvironment && projectKeys[r.Key], Inherited: inh, Source: r.Source, Version: int(r.Version), Comment: r.Comment,
			UsedBy: usage[r.Key], UpdatedAt: r.UpdatedAt,
		}
		if !r.IsSecret {
			v.Value = &plain
		}
		if r.Source == services.SourceGenerated {
			v.Generated = decodeCalls(r.GenCalls)
		}
		out.Items = append(out.Items, v)
		return nil
	}
	for _, r := range rows {
		if err := add(r, false); err != nil {
			return SharedVariableList{}, err
		}
	}
	for _, r := range inherited {
		if err := add(r, true); err != nil {
			return SharedVariableList{}, err
		}
	}
	sort.Slice(out.Items, func(i, j int) bool { return out.Items[i].Key < out.Items[j].Key })
	return out, nil
}

// sharedSecretness returns the shared (or, for kind team, team) keys that are
// secret by derivation.
func sharedSecretness(res *services.Resolver, kind string) map[string]bool {
	out := map[string]bool{}
	if kind == LayerTeam {
		for _, k := range res.TeamKeys() {
			if res.TeamIsSecret(k) {
				out[k] = true
			}
		}
		return out
	}
	for _, k := range res.SharedKeys() {
		if res.SharedIsSecret(k) {
			out[k] = true
		}
	}
	return out
}

// PutShared upserts shared variables of a project or environment and restages
// every service whose resolved environment changed.
func (s *Service) PutShared(ctx context.Context, kind string, id uuid.UUID, in []SharedVariableInput) (SharedVariableList, error) {
	t, err := s.loadTarget(ctx, s.q, policy.ScopeProjectsWrite, kind, id)
	if err != nil {
		return SharedVariableList{}, err
	}
	if err := checkSharedInputs(in); err != nil {
		return SharedVariableList{}, err
	}
	var (
		changes  []changeEntry
		warnings []VariableWarning
		restaged []RestagedServiceRef
	)
	err = db.InTx(ctx, s.pool, func(tx pgx.Tx) error {
		var err error
		changes, warnings, restaged, err = s.putSharedCore(ctx, tx, t, in)
		return err
	})
	if err != nil {
		if _, ok := apierr.As(err); ok {
			return SharedVariableList{}, err
		}
		return SharedVariableList{}, fmt.Errorf("put shared variables: %w", err)
	}
	if len(changes) > 0 {
		s.publishShared(t, restaged)
	}
	out, err := s.listShared(ctx, s.q, t)
	if err != nil {
		return SharedVariableList{}, err
	}
	out.Warnings, out.Restaged = warnings, restaged
	return out, nil
}

// PutSharedTx is PutShared inside the caller's transaction: the project or
// environment layer (kind, id) is authorized and loaded through tx. Services
// whose resolved environment changed are restaged (generation bump) in the same
// transaction. The returned closure publishes the change events and must run
// after commit (never nil).
func (s *Service) PutSharedTx(ctx context.Context, tx pgx.Tx, kind string, id uuid.UUID, in []SharedVariableInput) ([]RestagedServiceRef, func(context.Context), error) {
	noop := func(context.Context) {}
	t, err := s.loadTarget(ctx, db.New(tx), policy.ScopeProjectsWrite, kind, id)
	if err != nil {
		return nil, noop, err
	}
	if err := checkSharedInputs(in); err != nil {
		return nil, noop, err
	}
	changes, _, restaged, err := s.putSharedCore(ctx, tx, t, in)
	if err != nil {
		return nil, noop, err
	}
	if len(changes) == 0 {
		return restaged, noop, nil
	}
	return restaged, func(context.Context) { s.publishShared(t, restaged) }, nil
}

// checkSharedInputs validates a bulk shared write before any query runs.
func checkSharedInputs(in []SharedVariableInput) error {
	if len(in) > maxPerPut {
		return apierr.Unprocessable("too_many_variables", fmt.Sprintf("at most %d variables per request", maxPerPut))
	}
	seen := map[string]bool{}
	for _, v := range in {
		if len(v.Key) > maxKeyLen || !varexpr.IsKey(v.Key) {
			return apierr.Unprocessable("invalid_variable_key", "variable names match ^[A-Za-z_][A-Za-z0-9_]*$ (max 255 characters)")
		}
		if seen[v.Key] {
			return apierr.Unprocessable("duplicate_variable", "variable "+v.Key+" appears twice")
		}
		seen[v.Key] = true
		if v.Value != nil && len(*v.Value) > maxValueLen {
			return apierr.Unprocessable("value_too_large", "variable "+v.Key+" exceeds 64 KiB")
		}
	}
	return nil
}

// putSharedCore is the body of PutShared on tx.
func (s *Service) putSharedCore(ctx context.Context, tx pgx.Tx, t target, in []SharedVariableInput) ([]changeEntry, []VariableWarning, []RestagedServiceRef, error) {
	q := db.New(tx)
	before, err := s.snapshotEnvs(ctx, q, t.infos)
	if err != nil {
		return nil, nil, nil, err
	}
	var (
		changes  []changeEntry
		warnings []VariableWarning
	)
	for _, v := range in {
		cur, exists, err := s.getShared(ctx, q, t, v.Key)
		if err != nil {
			return nil, nil, nil, err
		}
		var curp *db.SharedVariable
		if exists {
			curp = &cur
		}
		w, err := s.prepareShared(t.kind, curp, v)
		if err != nil {
			return nil, nil, nil, err
		}
		warnings = append(warnings, w.warnings...)
		c, err := s.writeShared(ctx, q, t, v.Key, w, v.Comment, services.OpSet)
		if err != nil {
			return nil, nil, nil, err
		}
		if c != nil {
			changes = append(changes, *c)
		}
	}
	if len(changes) == 0 {
		return nil, warnings, nil, nil
	}
	sort.Slice(changes, func(i, j int) bool { return changes[i].Key < changes[j].Key })
	for _, info := range t.infos {
		if _, err := s.checkCycles(ctx, q, info, nil, ""); err != nil {
			return nil, nil, nil, err
		}
	}
	restaged, err := s.restage(ctx, q, before, uuid.Nil)
	if err != nil {
		return nil, nil, nil, err
	}
	if err := s.auditShared(ctx, tx, t, "shared_variable.set", changes, restaged); err != nil {
		return nil, nil, nil, err
	}
	return changes, warnings, restaged, nil
}

func (s *Service) snapshotEnvs(ctx context.Context, q *db.Queries, infos []services.EnvInfo) ([]*envSnapshot, error) {
	out := make([]*envSnapshot, 0, len(infos))
	for _, info := range infos {
		sn, err := s.snapshotEnv(ctx, q, info)
		if err != nil {
			return nil, err
		}
		out = append(out, sn)
	}
	return out, nil
}

// DeleteShared removes a shared variable. Deleting an environment value
// restages the services that resolved to it (they fall back to the project
// value, or stop resolving).
func (s *Service) DeleteShared(ctx context.Context, kind string, id uuid.UUID, key string) (SharedVariableList, error) {
	t, err := s.loadTarget(ctx, s.q, policy.ScopeProjectsWrite, kind, id)
	if err != nil {
		return SharedVariableList{}, err
	}
	var restaged []RestagedServiceRef
	err = db.InTx(ctx, s.pool, func(tx pgx.Tx) error {
		found, r, err := s.deleteSharedCore(ctx, tx, t, key)
		if err != nil {
			return err
		}
		if !found {
			return apierr.NotFound("variable")
		}
		restaged = r
		return nil
	})
	if err != nil {
		if _, ok := apierr.As(err); ok {
			return SharedVariableList{}, err
		}
		return SharedVariableList{}, fmt.Errorf("delete shared variable: %w", err)
	}
	s.publishShared(t, restaged)
	out, err := s.listShared(ctx, s.q, t)
	if err != nil {
		return SharedVariableList{}, err
	}
	out.Restaged = restaged
	return out, nil
}

// DeleteSharedTx is DeleteShared inside the caller's transaction. A missing
// variable yields found=false and no changes instead of a not-found error. The
// returned closure publishes the change events and must run after commit
// (never nil).
func (s *Service) DeleteSharedTx(ctx context.Context, tx pgx.Tx, kind string, id uuid.UUID, key string) (bool, []RestagedServiceRef, func(context.Context), error) {
	noop := func(context.Context) {}
	t, err := s.loadTarget(ctx, db.New(tx), policy.ScopeProjectsWrite, kind, id)
	if err != nil {
		return false, nil, noop, err
	}
	found, restaged, err := s.deleteSharedCore(ctx, tx, t, key)
	if err != nil || !found {
		return false, nil, noop, err
	}
	return true, restaged, func(context.Context) { s.publishShared(t, restaged) }, nil
}

// deleteSharedCore is the body of DeleteShared on tx. found=false means nothing
// existed and nothing was written.
func (s *Service) deleteSharedCore(ctx context.Context, tx pgx.Tx, t target, key string) (bool, []RestagedServiceRef, error) {
	q := db.New(tx)
	cur, exists, err := s.getShared(ctx, q, t, key)
	if err != nil {
		return false, nil, err
	}
	if !exists {
		return false, nil, nil
	}
	before, err := s.snapshotEnvs(ctx, q, t.infos)
	if err != nil {
		return false, nil, err
	}
	val, err := s.box.OpenString(secretbox.SharedVariablesValue, cur.ID.String(), cur.ValueEnc)
	if err != nil {
		return false, nil, err
	}
	if _, err := q.DeleteSharedVariable(ctx, cur.ID); err != nil {
		return false, nil, err
	}
	next, err := q.NextVariableVersion(ctx, db.NextVariableVersionParams{OwnerKind: t.ownerKind(), OwnerID: t.ownerID, Key: key})
	if err != nil {
		return false, nil, err
	}
	if err := services.RecordSharedVersion(ctx, q, s.box, t.workspace, t.projectID, t.ownerKind(), t.ownerID, key, next, services.OpDelete, cur.IsSecret, cur.Source, val, nil); err != nil {
		return false, nil, err
	}
	restaged, err := s.restage(ctx, q, before, uuid.Nil)
	if err != nil {
		return false, nil, err
	}
	if err := s.auditShared(ctx, tx, t, "shared_variable.delete", []changeEntry{{Key: key, Op: services.OpDelete, Version: next}}, restaged); err != nil {
		return false, nil, err
	}
	return true, restaged, nil
}

// RotateShared re-materialises a generated shared variable (plan or stage).
func (s *Service) RotateShared(ctx context.Context, kind string, id uuid.UUID, key string, dryRun bool) (VariableRotationPlan, error) {
	t, err := s.loadTarget(ctx, s.q, policy.ScopeProjectsWrite, kind, id)
	if err != nil {
		return VariableRotationPlan{}, err
	}
	plan := VariableRotationPlan{Key: key, Kind: KindAppSecret, Affected: []RotationAffected{}, DeployOrder: []string{}, Warnings: []string{}, Blockers: []RotationBlocker{}}
	var changes []changeEntry
	err = db.InTx(ctx, s.pool, func(tx pgx.Tx) error {
		q := db.New(tx)
		cur, exists, err := s.getShared(ctx, q, t, key)
		if err != nil {
			return err
		}
		if !exists {
			return apierr.NotFound("variable")
		}
		if cur.Source != services.SourceGenerated || len(cur.TemplateEnc) == 0 {
			return apierr.Unprocessable("not_rotatable", "only generated variables can be rotated; set a new value instead")
		}
		tmpl, err := s.box.OpenString(secretbox.SharedVariablesTemplate, cur.ID.String(), cur.TemplateEnc)
		if err != nil {
			return err
		}
		affected, err := s.applyAndDiff(ctx, q, t.infos, uuid.Nil, func() error {
			m, err := varexpr.Materialize(tmpl)
			if err != nil {
				return templateError(key, err)
			}
			c, err := s.writeShared(ctx, q, t, key, sharedWrite{value: m.Stored, secret: cur.IsSecret || m.Secret, source: services.SourceGenerated, template: tmpl, calls: m.Calls}, nil, services.OpRotate)
			if c != nil {
				changes = append(changes, *c)
			}
			return err
		})
		if err != nil {
			return err
		}
		if plan.Affected, plan.DeployOrder, err = s.finishPlan(ctx, q, t.infos, affected); err != nil {
			return err
		}
		plan.Warnings = append(plan.Warnings, "Existing sessions, tokens or signatures made with the old value become invalid once the change is deployed.")
		if dryRun {
			return errDryRun
		}
		plan.Staged = true
		restaged := make([]RestagedServiceRef, len(plan.Affected))
		for i, a := range plan.Affected {
			restaged[i] = RestagedServiceRef{ServiceID: a.ServiceID, Slug: a.Slug}
		}
		return s.auditShared(ctx, tx, t, "shared_variable.rotate", changes, restaged)
	})
	if err != nil && !errors.Is(err, errDryRun) {
		if _, ok := apierr.As(err); ok {
			return VariableRotationPlan{}, err
		}
		return VariableRotationPlan{}, fmt.Errorf("rotate shared variable: %w", err)
	}
	if plan.Staged {
		restaged := make([]RestagedServiceRef, len(plan.Affected))
		for i, a := range plan.Affected {
			restaged[i] = RestagedServiceRef{ServiceID: a.ServiceID, Slug: a.Slug}
		}
		s.publishShared(t, restaged)
	}
	return plan, nil
}

// ShareSharedInput selects the services that get a `KEY=${{shared.KEY}}` row.
type ShareSharedInput struct {
	ServiceIDs []uuid.UUID `json:"serviceIds" minItems:"1" maxItems:"200"`
}

// ShareShared stages `KEY=${{shared.KEY}}` on each service (nothing is injected
// implicitly: a shared variable reaches a service only through such a row).
func (s *Service) ShareShared(ctx context.Context, kind string, id uuid.UUID, key string, in ShareSharedInput) (SharedVariableList, error) {
	t, err := s.loadTarget(ctx, s.q, policy.ScopeProjectsWrite, kind, id)
	if err != nil {
		return SharedVariableList{}, err
	}
	var warnings []VariableWarning
	var scopes []services.Scope
	err = db.InTx(ctx, s.pool, func(tx pgx.Tx) error {
		q := db.New(tx)
		var exists bool
		if t.kind == LayerEnvironment {
			_, ok, err := s.getShared(ctx, q, t, key)
			if err != nil {
				return err
			}
			exists = ok
			if !exists {
				pt := target{kind: LayerProject, projectID: t.projectID, ownerID: t.projectID}
				_, exists, err = s.getShared(ctx, q, pt, key)
				if err != nil {
					return err
				}
			}
		} else {
			_, ok, err := s.getShared(ctx, q, t, key)
			if err != nil {
				return err
			}
			exists = ok
		}
		if !exists {
			return apierr.NotFound("variable")
		}
		ref := "${{shared." + key + "}}"
		if t.kind == LayerTeam {
			ref = "${{team." + key + "}}"
		}
		for _, sid := range in.ServiceIDs {
			sc, err := services.LoadScope(ctx, q, policy.ScopeProjectsWrite, sid)
			if err != nil {
				return err
			}
			if t.kind == LayerTeam {
				if sc.WorkspaceID != t.workspace {
					return apierr.NotFound("service")
				}
			} else if sc.ProjectID != t.projectID || (t.envID != nil && sc.EnvironmentID != *t.envID) {
				return apierr.NotFound("service")
			}
			if _, err := q.LockServiceRow(ctx, sid); err != nil {
				return apierr.NotFound("service")
			}
			if _, err := q.GetServiceVariable(ctx, db.GetServiceVariableParams{ServiceID: sid, Key: key}); err == nil {
				warnings = append(warnings, VariableWarning{Key: key, Code: "already_set", Message: sc.Service.Slug + " already defines " + key + "; left unchanged."})
				continue
			}
			res, err := services.WriteServiceVariable(ctx, q, s.box, sc, services.VarWrite{Key: key, Value: ref, Source: services.SourceManual})
			if err != nil {
				return err
			}
			if _, err := q.BumpServiceGeneration(ctx, sid); err != nil {
				return err
			}
			if err := s.auditSet(ctx, tx, sc, "variable.share", []changeEntry{s.changeOf(res, services.VarWrite{Key: key, Value: ref})}, nil); err != nil {
				return err
			}
			scopes = append(scopes, sc)
		}
		return nil
	})
	if err != nil {
		if _, ok := apierr.As(err); ok {
			return SharedVariableList{}, err
		}
		return SharedVariableList{}, fmt.Errorf("share variable: %w", err)
	}
	for _, sc := range scopes {
		s.publishChange(sc, nil)
	}
	out, err := s.listShared(ctx, s.q, t)
	if err != nil {
		return SharedVariableList{}, err
	}
	out.Warnings = warnings
	return out, nil
}

// usageCounts counts, per shared (or team) key, the service variables that
// reference it.
func (s *Service) usageCounts(ctx context.Context, q *db.Queries, kind string, infos []services.EnvInfo) (map[string]int, error) {
	out := map[string]int{}
	for _, info := range infos {
		vars, err := q.ListEnvironmentVariables(ctx, info.ID)
		if err != nil {
			return nil, err
		}
		for _, v := range vars {
			val, err := s.box.OpenString(secretbox.ServiceVariablesValue, secretbox.RowID(v.ServiceID.String(), v.Key), v.ValueEnc)
			if err != nil {
				return nil, err
			}
			refs := services.SharedReferences(val)
			if kind == LayerTeam {
				refs = services.TeamReferences(val)
			}
			for _, k := range refs {
				out[k]++
			}
		}
	}
	return out, nil
}

// VariableUsage lists the variables of an environment that reference ns.key: a shared
// variable (ns "shared") or a variable of another service (ns = its slug; the
// service's own bare references are included).
func (s *Service) VariableUsage(ctx context.Context, envID uuid.UUID, ns, key string) ([]VariableUsage, error) {
	env, err := services.LoadEnvironment(ctx, s.q, policy.ScopeProjectsRead, envID)
	if err != nil {
		return nil, err
	}
	if ns == "" {
		ns = varexpr.NSShared
	}
	svcs, err := s.q.ListAllEnvironmentServices(ctx, env.Env.ID)
	if err != nil {
		return nil, err
	}
	slugOf := map[uuid.UUID]string{}
	for _, sv := range svcs {
		slugOf[sv.ID] = sv.Slug
	}
	vars, err := s.q.ListEnvironmentVariables(ctx, env.Env.ID)
	if err != nil {
		return nil, err
	}
	out := []VariableUsage{}
	for _, v := range vars {
		val, err := s.box.OpenString(secretbox.ServiceVariablesValue, secretbox.RowID(v.ServiceID.String(), v.Key), v.ValueEnc)
		if err != nil {
			return nil, err
		}
		tpl, err := varexpr.Parse(val)
		if err != nil {
			continue
		}
		for _, r := range tpl.Refs() {
			hit := r.NS == ns && r.Key == key
			if !hit && r.NS == "" && r.Key == key && slugOf[v.ServiceID] == ns {
				hit = true
			}
			if hit {
				out = append(out, VariableUsage{ServiceID: v.ServiceID, Slug: slugOf[v.ServiceID], Key: v.Key})
				break
			}
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Slug != out[j].Slug {
			return out[i].Slug < out[j].Slug
		}
		return out[i].Key < out[j].Key
	})
	return out, nil
}
