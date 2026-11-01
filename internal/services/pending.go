package services

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/google/uuid"

	"github.com/nebulactrl/nebulactrl/internal/db"
	"github.com/nebulactrl/nebulactrl/internal/secretbox"
	"github.com/nebulactrl/nebulactrl/internal/varexpr"
)

// CurrentState is the staged configuration of a service: what a deployment
// started now would ship. It is compared with a release snapshot to list the
// pending changes.
type CurrentState struct {
	Config Config
	// SourceType is the service's staged source type (image | git | …). Empty
	// skips the source comparison (callers that only read a part of the state).
	SourceType string
	ServerID   *uuid.UUID
	// EnvironmentID is the service's environment (uuid.Nil when unknown).
	EnvironmentID uuid.UUID
	// Raw holds the user variables as written (references intact).
	Raw map[string]RawVar
	// Resolved is the fully resolved environment of the service (nil when the
	// references do not resolve); it powers the "inherited" change rows.
	Resolved *Resolved
	Domains  []db.Domain
	Volumes  []db.Volume
	Networks []NetworkRef
}

// ResolverCache memoizes one resolver per environment for a batch of reads.
type ResolverCache map[uuid.UUID]*Resolver

// LoadCurrentState reads the staged state of a service.
func LoadCurrentState(ctx context.Context, q *db.Queries, box *secretbox.Box, svc db.Service) (CurrentState, error) {
	return LoadCurrentStateCached(ctx, q, box, svc, nil)
}

// LoadCurrentStateCached is LoadCurrentState reusing resolvers across calls.
func LoadCurrentStateCached(ctx context.Context, q *db.Queries, box *secretbox.Box, svc db.Service, cache ResolverCache) (CurrentState, error) {
	st := CurrentState{Config: DecodeConfig(svc.Config), SourceType: svc.SourceType, ServerID: svc.ServerID, EnvironmentID: svc.EnvironmentID}
	var err error
	if st.Volumes, err = q.ListServiceVolumes(ctx, svc.ID); err != nil {
		return CurrentState{}, fmt.Errorf("list volumes: %w", err)
	}
	nets, err := ListNetworks(ctx, q, []uuid.UUID{svc.ID})
	if err != nil {
		return CurrentState{}, err
	}
	st.Networks = nets[svc.ID]
	if st.Domains, err = q.ListServiceDomains(ctx, svc.ID); err != nil {
		return CurrentState{}, fmt.Errorf("list domains: %w", err)
	}
	if st.Raw, err = LoadRawVars(ctx, q, box, svc.ID); err != nil {
		return CurrentState{}, err
	}
	res, ok := cache[svc.EnvironmentID]
	if !ok {
		info, err := LoadEnvInfo(ctx, q, svc.EnvironmentID)
		if err != nil {
			return CurrentState{}, err
		}
		if res, err = LoadResolver(ctx, q, box, info); err != nil {
			return CurrentState{}, err
		}
		if cache != nil {
			cache[svc.EnvironmentID] = res
		}
	}
	if r, rerr := res.ResolveService(svc.Slug); rerr == nil {
		st.Resolved = &r
	}
	return st, nil
}

// LoadRawVars returns a service's variables, decrypted.
func LoadRawVars(ctx context.Context, q *db.Queries, box *secretbox.Box, serviceID uuid.UUID) (map[string]RawVar, error) {
	vars, err := q.ListServiceVariables(ctx, serviceID)
	if err != nil {
		return nil, fmt.Errorf("list variables: %w", err)
	}
	out := make(map[string]RawVar, len(vars))
	for _, v := range vars {
		val, err := box.OpenString(secretbox.ServiceVariablesValue, secretbox.RowID(serviceID.String(), v.Key), v.ValueEnc)
		if err != nil {
			return nil, fmt.Errorf("decrypt variable %s: %w", v.Key, err)
		}
		rv := RawVar{Value: val, Secret: v.IsSecret}
		if v.Scope != ScopeRuntime {
			rv.Scope = v.Scope
		}
		if v.Source == SourceGenerated {
			rv.Gen = GenDescriptor(v.GenCalls)
		}
		out[v.Key] = rv
	}
	return out, nil
}

// ReleaseEnv returns the environment frozen into a release (nil when the
// release carries none).
func ReleaseEnv(box *secretbox.Box, rel db.Release) (*EnvSnapshot, error) {
	if len(rel.EnvEnc) == 0 {
		return nil, nil
	}
	plain, err := box.Open(secretbox.ReleasesEnv, rel.ID.String(), rel.EnvEnc)
	defer secretbox.Wipe(plain)
	if err != nil {
		return nil, fmt.Errorf("decrypt release env: %w", err)
	}
	var env EnvSnapshot
	if err := json.Unmarshal(plain, &env); err != nil {
		return nil, fmt.Errorf("decode release env: %w", err)
	}
	return &env, nil
}

// ReleaseRaw returns the user variables frozen into a release (nil when the
// release carries none).
func ReleaseRaw(box *secretbox.Box, rel db.Release) (map[string]RawVar, error) {
	env, err := ReleaseEnv(box, rel)
	if err != nil || env == nil {
		return nil, err
	}
	return env.Raw, nil
}

func scopeLabel(s string) string {
	if s == "" {
		return ScopeRuntime
	}
	return s
}

// inheritedChange reports a variable whose text is unchanged but whose resolved
// value differs from the release's because something it references changed.
func inheritedChange(key string, raw RawVar, rel *EnvSnapshot, res *Resolved) (FieldChange, bool) {
	if rel == nil || res == nil {
		return FieldChange{}, false
	}
	oldV, okOld := rel.Vars[key]
	newV, okNew := res.Vars[key]
	if !okOld || !okNew || oldV == newV {
		return FieldChange{}, false
	}
	shown := raw.Value
	if raw.Secret {
		shown = Mask
	}
	var via []string
	if tpl, err := varexpr.Parse(raw.Value); err == nil {
		seen := map[string]bool{}
		for _, r := range tpl.Refs() {
			if s := r.String(); !seen[s] {
				seen[s] = true
				via = append(via, s)
			}
		}
	}
	return FieldChange{
		Field: "variables." + key, From: shown, To: shown, Origin: OriginInherited,
		Via: strings.Join(via, ", "), ResolvedChanged: true,
	}, true
}

// DiffRelease lists the field-level differences between a release snapshot and
// the staged state. Fields are `config.<path>`, `server`, `variables.<KEY>`,
// `domains.<host>` and `volumes.<name>`; secret values are masked.
func DiffRelease(ctx context.Context, q *db.Queries, box *secretbox.Box, rel db.Release, cur CurrentState) ([]FieldChange, error) {
	snap := DecodeSnapshot(rel.Config)
	out := DiffConfig("config", snap.Config, cur.Config)
	if relType := DecodeSource(rel.Source).Type; SourceSwitched(relType, cur.SourceType) {
		// One change instead of the image and repository fields it carries.
		out = append([]FieldChange{{
			Field: FieldSource, From: SourceText(relType, snap.Config, false), To: SourceText(cur.SourceType, cur.Config, true),
		}}, withoutSourceFields(out)...)
	}

	names := map[uuid.UUID]string{}
	var ids []uuid.UUID
	if snap.ServerID != nil {
		ids = append(ids, *snap.ServerID)
	}
	if cur.ServerID != nil {
		ids = append(ids, *cur.ServerID)
	}
	if len(ids) > 0 {
		rows, err := q.LoadServerRows(ctx, ids)
		if err != nil {
			return nil, fmt.Errorf("load servers: %w", err)
		}
		for _, r := range rows {
			names[r.ID] = r.Name
		}
	}
	nameOf := func(id *uuid.UUID) string {
		if id == nil {
			return ""
		}
		if n, ok := names[*id]; ok {
			return n
		}
		return id.String()
	}
	if !sameServer(snap.ServerID, cur.ServerID) {
		out = append(out, FieldChange{Field: "server", From: nameOf(snap.ServerID), To: nameOf(cur.ServerID)})
	}
	if snap.EnvironmentID != nil && cur.EnvironmentID != uuid.Nil && *snap.EnvironmentID != cur.EnvironmentID {
		out = append(out, FieldChange{Field: "environment", From: envLabel(ctx, q, *snap.EnvironmentID), To: envLabel(ctx, q, cur.EnvironmentID)})
	}

	// Variables, secrets masked.
	relEnv, err := ReleaseEnv(box, rel)
	if err != nil {
		return nil, err
	}
	var relRaw map[string]RawVar
	if relEnv != nil {
		relRaw = relEnv.Raw
	}
	if relRaw != nil || len(cur.Raw) > 0 {
		keys := map[string]struct{}{}
		for k := range relRaw {
			keys[k] = struct{}{}
		}
		for k := range cur.Raw {
			keys[k] = struct{}{}
		}
		sorted := make([]string, 0, len(keys))
		for k := range keys {
			sorted = append(sorted, k)
		}
		sort.Strings(sorted)
		show := func(v RawVar) string {
			if v.Secret {
				return Mask
			}
			return v.Value
		}
		for _, k := range sorted {
			a, hadA := relRaw[k]
			b, hasB := cur.Raw[k]
			sameText := hadA && hasB && a.Value == b.Value && a.Secret == b.Secret
			if sameText {
				if a.Scope != b.Scope {
					out = append(out, FieldChange{Field: "variables." + k + ".scope", From: scopeLabel(a.Scope), To: scopeLabel(b.Scope)})
				}
				// Same text: it may still resolve differently (a provider changed).
				if ch, ok := inheritedChange(k, b, relEnv, cur.Resolved); ok {
					out = append(out, ch)
				}
				continue
			}
			c := FieldChange{Field: "variables." + k, Origin: OriginDirect}
			if hadA {
				c.From = show(a)
			}
			if hasB {
				c.To = show(b)
				if b.Secret && b.Gen != "" {
					c.To = "generated · " + b.Gen
				}
			}
			out = append(out, c)
		}
	}

	// Domains (host, path, rule fields, auth user names) and volumes.
	domIDs := make([]uuid.UUID, len(cur.Domains))
	for i, d := range cur.Domains {
		domIDs[i] = d.ID
	}
	domUsers, err := DomainAuthUsernames(ctx, q, domIDs)
	if err != nil {
		return nil, err
	}
	out = append(out, diffDomains(snap.Domains, cur.Domains, domUsers)...)
	oldVol := map[string]SnapshotVolume{}
	for _, v := range snap.Volumes {
		oldVol[v.Name] = v
	}
	curVol := map[string]bool{}
	for _, v := range cur.Volumes {
		curVol[v.Name] = true
		old, ok := oldVol[v.Name]
		if !ok {
			out = append(out, FieldChange{Field: "volumes." + v.Name, From: "", To: v.MountPath})
			continue
		}
		to := v.MountPath
		swapped := old.EffectiveDockerName(v.ServiceID) != DockerVolumeName(v)
		if swapped {
			label := v.RestoreLabel
			if label == "" {
				label = "another Docker volume"
			}
			to += " · " + label
		}
		if old.MountPath != v.MountPath || swapped {
			out = append(out, FieldChange{Field: "volumes." + v.Name, From: old.MountPath, To: to})
		}
	}
	for _, v := range snap.Volumes {
		if !curVol[v.Name] {
			out = append(out, FieldChange{Field: "volumes." + v.Name, From: v.MountPath, To: ""})
		}
	}
	oldNets := map[uuid.UUID]SnapshotNetwork{}
	for _, n := range snap.Networks {
		oldNets[n.ID] = n
	}
	curNets := map[uuid.UUID]bool{}
	for _, n := range cur.Networks {
		curNets[n.ID] = true
		if old, ok := oldNets[n.ID]; !ok {
			out = append(out, FieldChange{Field: "networks." + n.Name, From: "", To: n.Alias})
		} else if old.Alias != n.Alias {
			out = append(out, FieldChange{Field: "networks." + n.Name, From: old.Alias, To: n.Alias})
		}
	}
	for _, n := range snap.Networks {
		if !curNets[n.ID] {
			out = append(out, FieldChange{Field: "networks." + n.Name, From: n.Alias, To: ""})
		}
	}
	if out == nil {
		out = []FieldChange{}
	}
	return out, nil
}

func sameServer(a, b *uuid.UUID) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return *a == *b
}

// envLabel names an environment as "<project> / <environment>" for staged-change
// text; the id when it cannot be read (a deleted environment).
func envLabel(ctx context.Context, q *db.Queries, id uuid.UUID) string {
	label, err := q.GetEnvironmentLabel(ctx, id)
	if err != nil {
		return id.String()
	}
	return label
}

// CountChanges splits changes into variable and setting counts.
func CountChanges(changes []FieldChange) PendingChangeCounts {
	var c PendingChangeCounts
	for _, ch := range changes {
		if strings.HasPrefix(ch.Field, "variables.") {
			c.Variables++
		} else {
			c.Settings++
		}
	}
	return c
}
