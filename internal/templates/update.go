package templates

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/nebulactrl/nebulactrl/internal/apierr"
	"github.com/nebulactrl/nebulactrl/internal/audit"
	"github.com/nebulactrl/nebulactrl/internal/auth"
	"github.com/nebulactrl/nebulactrl/internal/db"
	"github.com/nebulactrl/nebulactrl/internal/policy"
	"github.com/nebulactrl/nebulactrl/internal/services"
)

// VariableReader reads a service's stored variables (the variables package).
// Secret variables come back without a value.
type VariableReader interface {
	Values(ctx context.Context, serviceID uuid.UUID) (map[string]LiveVariable, error)
}

// Update-blocked reasons shown in place of a diff.
const (
	blockedNoSnapshot = "This stack was installed before template updates existed, so there is no record of what it was installed from. Install the template again to get the new revision."
	blockedRemoved    = "This template is no longer in the catalog."
)

// TemplateUpdate is the diff between an installed stack and the catalog's
// current revision of its template.
type TemplateUpdate struct {
	InstanceID        uuid.UUID `json:"instanceId"`
	TemplateID        string    `json:"templateId"`
	Name              string    `json:"name"`
	InstalledRevision int       `json:"installedRevision"`
	InstalledVersion  string    `json:"installedVersion"`
	CurrentRevision   int       `json:"currentRevision"`
	CurrentVersion    string    `json:"currentVersion"`
	// Available is true when the revisions differ in something the update
	// applies or reports.
	Available bool `json:"available"`
	// Blocked is set when the stack cannot be updated; Changes is empty then.
	Blocked string                 `json:"blocked,omitempty"`
	Changes []TemplateUpdateChange `json:"changes"`
	// Inputs are the inputs the new revision added, to be filled in when staging.
	Inputs []TemplatePlanInput `json:"inputs"`
	// Counts by status.
	Apply   int `json:"apply"`
	Kept    int `json:"kept"`
	Manual  int `json:"manual"`
	Applied int `json:"applied"`
	// Notes of the new revision (first steps, default logins).
	Notes []string `json:"notes"`
}

// TemplateUpdateRequest is the body of applying an update.
type TemplateUpdateRequest struct {
	// Revision is the catalog revision the user reviewed; a newer one in the
	// catalog makes the request fail with 409 so nothing unreviewed is staged.
	Revision int `json:"revision" minimum:"1"`
	// Inputs are values for the inputs the new revision added.
	Inputs map[string]string `json:"inputs,omitempty"`
}

// TemplateUpdateResult is the outcome of staging an update.
type TemplateUpdateResult struct {
	Instance TemplateInstance       `json:"instance"`
	Changes  []TemplateUpdateChange `json:"changes"`
	// Staged is the number of changes written (all of them staged, none deployed).
	Staged int `json:"staged"`
	// Services lists the services that now have staged changes.
	Services []string `json:"services"`
	Warnings []string `json:"warnings"`
}

// updateCtx is the loaded state of one update.
type updateCtx struct {
	inst   db.TemplateInstance
	env    services.EnvScope
	old    *CatalogTemplate
	cur    *CatalogTemplate
	stored map[string]string
	rows   map[string]db.Service // template service key → service
	slugs  map[string]string
}

// loadUpdate loads the instance and both revisions. blocked is non-empty when
// the stack cannot be updated (old and cur may then be nil).
func (s *Service) loadUpdate(ctx context.Context, instanceID uuid.UUID, scope string) (*updateCtx, string, error) {
	if err := policy.Require(ctx, scope, nil); err != nil {
		return nil, "", err
	}
	inst, err := s.q.GetTemplateInstance(ctx, instanceID)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, "", apierr.NotFound("template instance")
	}
	if err != nil {
		return nil, "", fmt.Errorf("get instance: %w", err)
	}
	env, err := services.LoadEnvironment(ctx, s.q, scope, inst.EnvironmentID)
	if err != nil {
		return nil, "", apierr.NotFound("template instance")
	}
	uc := &updateCtx{inst: inst, env: env, stored: map[string]string{}, rows: map[string]db.Service{}, slugs: map[string]string{}}
	_ = json.Unmarshal(inst.Inputs, &uc.stored)
	cur, ok := s.Catalog.Get(inst.TemplateID)
	if !ok {
		return uc, blockedRemoved, nil
	}
	uc.cur = cur
	if len(inst.TemplateSnapshot) == 0 {
		return uc, blockedNoSnapshot, nil
	}
	if uc.old, err = decodeSnapshot(inst.TemplateSnapshot); err != nil {
		return nil, "", err
	}
	svcs, err := s.q.ListTemplateInstanceServices(ctx, &inst.ID)
	if err != nil {
		return nil, "", fmt.Errorf("list instance services: %w", err)
	}
	for _, sv := range svcs {
		if sv.TemplateKey == nil || strings.HasPrefix(*sv.TemplateKey, "port:") {
			continue
		}
		uc.rows[*sv.TemplateKey] = sv
		uc.slugs[*sv.TemplateKey] = sv.Slug
	}
	return uc, "", nil
}

func (s *Service) header(uc *updateCtx) TemplateUpdate {
	u := TemplateUpdate{
		InstanceID: uc.inst.ID, TemplateID: uc.inst.TemplateID, Name: uc.inst.Name, InstalledRevision: int(uc.inst.TemplateRevision),
		InstalledVersion: uc.inst.TemplateVersion, Changes: []TemplateUpdateChange{}, Inputs: []TemplatePlanInput{}, Notes: []string{},
	}
	if uc.cur != nil {
		u.CurrentRevision, u.CurrentVersion, u.Notes = uc.cur.Revision, AppVersion(uc.cur), nonNil(uc.cur.Notes)
	}
	return u
}

// evaluateUpdate diffs the revisions and classifies every change against the
// live services.
func (s *Service) evaluateUpdate(ctx context.Context, uc *updateCtx) ([]TemplateUpdateChange, templateKnown, error) {
	k := knownInputs(uc.old, uc.cur, uc.stored)
	changes := diffTemplates(uc.old, uc.cur, k)
	live := map[string]*liveService{}
	for key, row := range uc.rows {
		ls := &liveService{Slug: row.Slug, Cfg: services.DecodeConfig(row.Config), Volumes: map[string]string{}, Vars: map[string]LiveVariable{}}
		vols, err := s.q.ListServiceVolumes(ctx, row.ID)
		if err != nil {
			return nil, k, fmt.Errorf("list volumes: %w", err)
		}
		for _, v := range vols {
			ls.Volumes[v.Name] = v.MountPath
		}
		if rd, ok := s.Variables.(VariableReader); ok {
			if ls.Vars, err = rd.Values(ctx, row.ID); err != nil {
				return nil, k, err
			}
		}
		live[key] = ls
	}
	changes = classifyChanges(classifyInput{Old: uc.old, Cur: uc.cur, Known: k, Slugs: uc.slugs, Live: live}, changes)
	return changes, k, nil
}

// newInputs lists the inputs the current revision added.
func newInputs(old, cur *CatalogTemplate) []TemplatePlanInput {
	have := map[string]bool{}
	for _, in := range old.Inputs {
		have[in.Key] = true
	}
	out := []TemplatePlanInput{}
	for _, in := range cur.Inputs {
		if have[in.Key] {
			continue
		}
		out = append(out, TemplatePlanInput{
			Key: in.Key, Label: in.Label, Description: in.Description, Required: in.Required, Secret: in.Secret,
			Options: in.Options, Format: in.Format, Generated: hasGenerator(in.Default), Value: nonSecretDefault(in),
		})
	}
	return out
}

func nonSecretDefault(in TemplateInput) string {
	if in.Secret || hasGenerator(in.Default) {
		return ""
	}
	return in.Default
}

// GetUpdate returns the diff between an installed stack and the catalog's
// current revision of its template. It writes nothing.
func (s *Service) GetUpdate(ctx context.Context, instanceID uuid.UUID) (TemplateUpdate, error) {
	uc, blocked, err := s.loadUpdate(ctx, instanceID, policy.ScopeProjectsRead)
	if err != nil {
		return TemplateUpdate{}, err
	}
	u := s.header(uc)
	if blocked != "" {
		if uc.cur == nil || uc.cur.Revision > int(uc.inst.TemplateRevision) {
			u.Blocked = blocked
		}
		return u, nil
	}
	changes, _, err := s.evaluateUpdate(ctx, uc)
	if err != nil {
		return TemplateUpdate{}, err
	}
	u.Changes = changes
	u.Available = len(changes) > 0
	u.Apply, u.Kept = countStatus(changes, ChangeApply), countStatus(changes, ChangeKept)
	u.Manual, u.Applied = countStatus(changes, ChangeManual), countStatus(changes, ChangeApplied)
	u.Inputs = newInputs(uc.old, uc.cur)
	return u, nil
}

// ApplyUpdate stages the update: configuration, variables, files and volumes of
// the stack's services change as staged edits (nothing deploys; the staged bar
// and Deploy do that), the instance records the new revision and snapshot, and
// everything the update does not do is reported. Fields the user changed are
// kept. A retry after a failure is safe: what is already applied is skipped.
func (s *Service) ApplyUpdate(ctx context.Context, instanceID uuid.UUID, req TemplateUpdateRequest) (TemplateUpdateResult, error) {
	uc, blocked, err := s.loadUpdate(ctx, instanceID, policy.ScopeProjectsWrite)
	if err != nil {
		return TemplateUpdateResult{}, err
	}
	if blocked != "" {
		return TemplateUpdateResult{}, apierr.Conflict("update_unavailable", blocked)
	}
	if req.Revision != uc.cur.Revision {
		return TemplateUpdateResult{}, apierr.Conflict("template_changed", fmt.Sprintf("the template is at revision %d now, not %d: review the update again", uc.cur.Revision, req.Revision))
	}
	changes, k, err := s.evaluateUpdate(ctx, uc)
	if err != nil {
		return TemplateUpdateResult{}, err
	}

	// Inputs: values for the added inputs, defaults and generators for the rest.
	added := newInputs(uc.old, uc.cur)
	addedKeys := map[string]bool{}
	for _, in := range added {
		addedKeys[in.Key] = true
	}
	given := map[string]string{}
	for key, v := range req.Inputs {
		if !addedKeys[key] {
			return TemplateUpdateResult{}, apierr.Unprocessable("unknown_input", "input "+key+" is not new in this revision")
		}
		given[key] = v
	}
	probe := &CatalogTemplate{Inputs: slices.DeleteFunc(slices.Clone(uc.cur.Inputs), func(in TemplateInput) bool { return !addedKeys[in.Key] })}
	resolved, err := ResolveInputs(probe, given, s.Materialize)
	if err != nil {
		return TemplateUpdateResult{}, err
	}
	for key, v := range resolved.Values {
		k.Vals[key] = v
	}
	secretInputs := map[string]bool{}
	for key := range k.Secret {
		secretInputs[key] = true
	}
	for key := range resolved.Secret {
		secretInputs[key] = true
	}
	// The values that are not secret join the instance's recorded inputs.
	merged := map[string]string{}
	for key, v := range uc.stored {
		merged[key] = v
	}
	for key, v := range resolved.Values {
		if !resolved.Secret[key] && !resolved.Generated[key] {
			merged[key] = v
		}
	}

	byService := map[string][]TemplateUpdateChange{}
	for _, c := range changes {
		if c.Status == ChangeApply && c.Service != "" {
			byService[c.Service] = append(byService[c.Service], c)
		}
	}
	curSvc := serviceByKey(uc.cur)
	staged := []string{}
	stagedN := 0
	for _, sv := range uc.cur.Services {
		cs := byService[sv.Key]
		if len(cs) == 0 {
			continue
		}
		if err := s.applyServiceUpdate(ctx, uc, curSvc[sv.Key], cs, k.Vals, secretInputs); err != nil {
			return TemplateUpdateResult{}, fmt.Errorf("update %s: %w", sv.Name, err)
		}
		staged = append(staged, uc.rows[sv.Key].Slug)
		stagedN += len(cs)
	}

	snapshot, err := encodeSnapshot(uc.cur)
	if err != nil {
		return TemplateUpdateResult{}, err
	}
	inputsJSON, err := json.Marshal(merged)
	if err != nil {
		return TemplateUpdateResult{}, err
	}
	actorType, actorID := audit.ActorSystem, ""
	if p, ok := auth.PrincipalFrom(ctx); ok {
		actorType, actorID = p.Type, p.ID().String()
	}
	var row db.TemplateInstance
	err = db.InTx(ctx, s.Pool, func(tx pgx.Tx) error {
		row, err = db.New(tx).SetTemplateInstanceVersion(ctx, db.SetTemplateInstanceVersionParams{
			ID: uc.inst.ID, TemplateRevision: int32(uc.cur.Revision), TemplateVersion: AppVersion(uc.cur), TemplateSnapshot: snapshot,
			Inputs: inputsJSON, UpdatedByType: actorType, UpdatedBy: actorID,
		})
		if err != nil {
			return err
		}
		fields := make([]string, 0, stagedN)
		for _, c := range changes {
			if c.Status == ChangeApply {
				fields = append(fields, c.Service+":"+c.Field)
			}
		}
		return audit.Record(ctx, tx, audit.Entry{
			Action: "template.update", ResourceType: "template_instance", ResourceID: uc.inst.ID.String(), ProjectID: &uc.env.Env.ProjectID,
			Metadata: map[string]any{
				"template": uc.inst.TemplateID, "fromRevision": uc.inst.TemplateRevision, "toRevision": uc.cur.Revision,
				"fromVersion": uc.inst.TemplateVersion, "toVersion": AppVersion(uc.cur), "staged": fields,
				"kept": countStatus(changes, ChangeKept), "manual": countStatus(changes, ChangeManual),
			},
		})
	})
	if err != nil {
		return TemplateUpdateResult{}, fmt.Errorf("record template update: %w", err)
	}

	inst, err := s.instanceView(ctx, row)
	if err != nil {
		return TemplateUpdateResult{}, err
	}
	warnings := []string{}
	if n := countStatus(changes, ChangeManual); n > 0 {
		warnings = append(warnings, fmt.Sprintf("%d change(s) are not applied by an update; see the list.", n))
	}
	return TemplateUpdateResult{Instance: inst, Changes: changes, Staged: stagedN, Services: staged, Warnings: warnings}, nil
}

// applyServiceUpdate writes one service's apply changes: variables and
// volumes in one transaction, then the configuration (image, health check,
// files...) through the service update, which validates it and stages it.
func (s *Service) applyServiceUpdate(ctx context.Context, uc *updateCtx, cs *TemplateService, changes []TemplateUpdateChange, vals map[string]string, secretInputs map[string]bool) error {
	row := uc.rows[cs.Key]
	var cfgFields []string
	hasFiles := false
	needTx := false
	for _, c := range changes {
		switch {
		case strings.HasPrefix(c.Field, "env."), strings.HasPrefix(c.Field, "volume:"):
			needTx = true
		case strings.HasPrefix(c.Field, "file:"):
			hasFiles = true
		case strings.HasPrefix(c.Field, "input:"), c.Field == "service", strings.HasPrefix(c.Field, "database."), c.Field == "ports", c.Field == "domains":
		default:
			cfgFields = append(cfgFields, c.Field)
		}
	}
	if needTx {
		if s.Variables == nil {
			return apierr.Unprocessable("variables_unavailable", "this server build cannot write variables")
		}
		err := db.InTx(ctx, s.Pool, func(tx pgx.Tx) error {
			q := db.New(tx)
			volumes := false
			for _, c := range changes {
				if key, ok := strings.CutPrefix(c.Field, "env."); ok {
					raw := cs.Env[key]
					secret := envIsSecret(cs, key, raw, secretInputs)
					if err := s.Variables.UpsertTx(ctx, tx, row.ID, key, envValue(raw, vals, uc.slugs), secret); err != nil {
						return fmt.Errorf("write variable %s: %w", key, err)
					}
				}
				if name, ok := strings.CutPrefix(c.Field, "volume:"); ok {
					if _, err := q.InsertVolume(ctx, db.InsertVolumeParams{ServiceID: row.ID, Name: name, MountPath: c.To}); err != nil {
						return fmt.Errorf("insert volume %s: %w", name, err)
					}
					volumes = true
				}
			}
			if volumes {
				if _, err := q.BumpServiceGeneration(ctx, row.ID); err != nil {
					return err
				}
			}
			return nil
		})
		if err != nil {
			return err
		}
	}
	if len(cfgFields) == 0 && !hasFiles {
		return nil
	}
	cur, err := s.Services.Get(ctx, row.ID)
	if err != nil {
		return err
	}
	next := serviceConfig(cs, vals).WithDefaults()
	patched, err := patchConfig(cur.Config, next, cfgFields)
	if err != nil {
		return err
	}
	if hasFiles {
		patched.Files = patchFiles(patched.Files, changes, next.Files)
	}
	_, err = s.Services.Update(ctx, row.ID, cur.Generation, services.UpdateInput{Config: &patched})
	return err
}

// markUpdate fills the update fields of an instance view from the catalog.
func (s *Service) markUpdate(v *TemplateInstance, r db.TemplateInstance) {
	cur, ok := s.Catalog.Get(r.TemplateID)
	if !ok {
		v.UpdateBlocked = blockedRemoved
		return
	}
	v.CurrentRevision, v.CurrentVersion = cur.Revision, AppVersion(cur)
	if len(r.TemplateSnapshot) == 0 {
		if cur.Revision > int(r.TemplateRevision) {
			v.UpdateBlocked = blockedNoSnapshot
		}
		return
	}
	old, err := decodeSnapshot(r.TemplateSnapshot)
	if err != nil {
		return
	}
	stored := map[string]string{}
	_ = json.Unmarshal(r.Inputs, &stored)
	v.UpdateAvailable = len(diffTemplates(old, cur, knownInputs(old, cur, stored))) > 0
}
