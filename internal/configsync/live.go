package configsync

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/nebulactrl/nebulactrl/internal/db"
	"github.com/nebulactrl/nebulactrl/internal/secretbox"
	"github.com/nebulactrl/nebulactrl/internal/services"
	"github.com/nebulactrl/nebulactrl/internal/varexpr"
)

// liveVar is a stored service variable as far as the file can see it: the
// value of a secret is never read, only the template text a file could have
// written (a generator template, or a reference-only text).
type liveVar struct {
	Key      string
	Secret   bool
	Scope    string
	Comment  string
	Rotate   *int
	Source   string
	Template string // template text; "" when the row is a secret with literal content
	Opaque   bool   // secret whose text is not a template or reference-only text
}

// liveService is everything the file can own on one live service of the environment.
type liveService struct {
	Row      db.Service
	Cfg      services.Config
	Vars     map[string]liveVar
	Domains  []db.Domain
	Volumes  []db.Volume
	Networks []db.ListServicesNetworksRow
	Links    []string // slugs of the services this one depends on
	Git      *db.ServiceGit
	GitConn  string // name of the git connection
	Tasks    map[string]db.ServiceTask
	Backups  []db.BackupSchedule
	Monitor  *db.UptimeMonitor
	Scale    services.Autoscaling

	// ProcScales are the scale rows of the named processes (a process without a row has the defaults).
	ProcScales []db.ServiceProcessScale

	Source  *db.ConfigSource
	Owned   map[string]string
	Pending map[string]json.RawMessage
}

// liveEnv is the live state of one environment plus the name lookups of its workspace.
type liveEnv struct {
	Env      services.EnvScope
	Project  db.Project
	Services map[string]*liveService // by slug
	Order    []string                // slugs in creation order

	SharedProject map[string]liveVar
	SharedEnv     map[string]liveVar
	Settings      *db.EnvironmentConfigSetting
	OwnedShared   map[string]string

	// Refs are the workspace's named things the file points at.
	Refs *refs
}

// refs are the workspace's named things a file points at.
type refs struct {
	Servers      []db.Server
	Connections  []db.GitConnection
	Registries   []db.Registry
	Destinations []db.BackupDestination
	Networks     []db.SharedNetwork
	Certs        []db.Certificate
	Cloudflare   bool
}

// lookup resolution outcomes.
const (
	refOK = iota
	refNone
	refAmbiguous
)

func pick[T any](items []T, name string, nameOf func(T) string) (T, int) {
	var zero, found T
	n := 0
	for _, it := range items {
		if strings.EqualFold(nameOf(it), name) {
			found = it
			n++
		}
	}
	switch n {
	case 0:
		return zero, refNone
	case 1:
		return found, refOK
	}
	return zero, refAmbiguous
}

func (r *refs) server(name string) (db.Server, int) {
	return pick(r.Servers, name, func(s db.Server) string { return s.Name })
}

func (r *refs) serverByID(id uuid.UUID) (db.Server, bool) {
	for _, s := range r.Servers {
		if s.ID == id {
			return s, true
		}
	}
	return db.Server{}, false
}

func (r *refs) connection(name string) (db.GitConnection, int) {
	return pick(r.Connections, name, func(c db.GitConnection) string { return c.Name })
}

func (r *refs) connectionByID(id uuid.UUID) (db.GitConnection, bool) {
	for _, c := range r.Connections {
		if c.ID == id {
			return c, true
		}
	}
	return db.GitConnection{}, false
}

func (r *refs) registry(name string) (db.Registry, int) {
	return pick(r.Registries, name, func(x db.Registry) string { return x.Name })
}

func (r *refs) registryByID(id uuid.UUID) (db.Registry, bool) {
	for _, x := range r.Registries {
		if x.ID == id {
			return x, true
		}
	}
	return db.Registry{}, false
}

func (r *refs) destination(name string) (db.BackupDestination, int) {
	return pick(r.Destinations, name, func(x db.BackupDestination) string { return x.Name })
}

func (r *refs) destinationByID(id uuid.UUID) (db.BackupDestination, bool) {
	for _, x := range r.Destinations {
		if x.ID == id {
			return x, true
		}
	}
	return db.BackupDestination{}, false
}

func (r *refs) network(name string) (db.SharedNetwork, int) {
	return pick(r.Networks, name, func(x db.SharedNetwork) string { return x.Name })
}

// certificate resolves an uploaded certificate by name on one server.
func (r *refs) certificate(name string, serverID uuid.UUID) (db.Certificate, int) {
	var onServer []db.Certificate
	for _, c := range r.Certs {
		if c.ServerID == serverID {
			onServer = append(onServer, c)
		}
	}
	return pick(onServer, name, func(c db.Certificate) string { return c.Name })
}

func (r *refs) certificateByID(id uuid.UUID) (db.Certificate, bool) {
	for _, c := range r.Certs {
		if c.ID == id {
			return c, true
		}
	}
	return db.Certificate{}, false
}

// loadRefs reads the workspace's named things.
func loadRefs(ctx context.Context, q *db.Queries, ws uuid.UUID) (*refs, error) {
	var (
		r   refs
		err error
	)
	if r.Servers, err = q.ListWorkspaceServers(ctx, ws); err != nil {
		return nil, fmt.Errorf("list servers: %w", err)
	}
	if r.Connections, err = q.ListGitConnections(ctx, ws); err != nil {
		return nil, fmt.Errorf("list git connections: %w", err)
	}
	if r.Registries, err = q.ListRegistries(ctx, ws); err != nil {
		return nil, fmt.Errorf("list registries: %w", err)
	}
	if r.Destinations, err = q.ListBackupDestinations(ctx, ws); err != nil {
		return nil, fmt.Errorf("list backup destinations: %w", err)
	}
	if r.Networks, err = q.ListSharedNetworks(ctx, db.ListSharedNetworksParams{WorkspaceID: ws}); err != nil {
		return nil, fmt.Errorf("list networks: %w", err)
	}
	if r.Certs, err = q.ListWorkspaceCertificates(ctx, ws); err != nil {
		return nil, fmt.Errorf("list certificates: %w", err)
	}
	if _, err := q.GetCloudflareConnection(ctx, ws); err == nil {
		r.Cloudflare = true
	} else if !errors.Is(err, pgx.ErrNoRows) {
		return nil, fmt.Errorf("get cloudflare connection: %w", err)
	}
	return &r, nil
}

// loadLive reads the live state of an environment with q (a transaction's queries when the
// caller holds the apply lock).
func loadLive(ctx context.Context, q *db.Queries, box *secretbox.Box, env services.EnvScope) (*liveEnv, error) {
	proj, err := q.GetProject(ctx, db.GetProjectParams{ID: env.Env.ProjectID, WorkspaceID: env.WorkspaceID})
	if err != nil {
		return nil, fmt.Errorf("get project: %w", err)
	}
	rf, err := loadRefs(ctx, q, env.WorkspaceID)
	if err != nil {
		return nil, err
	}
	le := &liveEnv{Env: env, Project: proj, Services: map[string]*liveService{}, Refs: rf, OwnedShared: map[string]string{}}

	rows, err := q.ListEnvironmentServicesAll(ctx, env.Env.ID)
	if err != nil {
		return nil, fmt.Errorf("list services: %w", err)
	}
	ids := make([]uuid.UUID, len(rows))
	byID := map[uuid.UUID]*liveService{}
	slugOf := map[uuid.UUID]string{}
	for i, r := range rows {
		ids[i] = r.ID
		ls := &liveService{Row: r, Cfg: services.DecodeConfig(r.Config), Vars: map[string]liveVar{}, Tasks: map[string]db.ServiceTask{},
			Scale: services.DecodeAutoscaling(r.Autoscaling), Owned: map[string]string{}, Pending: map[string]json.RawMessage{}}
		le.Services[r.Slug] = ls
		le.Order = append(le.Order, r.Slug)
		byID[r.ID] = ls
		slugOf[r.ID] = r.Slug
	}

	for _, r := range rows {
		ls := byID[r.ID]
		vars, err := q.ListServiceVariables(ctx, r.ID)
		if err != nil {
			return nil, fmt.Errorf("list variables: %w", err)
		}
		for _, v := range vars {
			lv, err := decodeVar(box, secretbox.RowID(v.ServiceID.String(), v.Key), v.Key, v.ValueEnc, v.TemplateEnc, v.IsSecret, v.Scope, v.Comment, v.RotateEveryDays, v.Source, secretbox.ServiceVariablesValue, secretbox.ServiceVariablesTemplate)
			if err != nil {
				return nil, err
			}
			ls.Vars[v.Key] = lv
		}
		if ls.Domains, err = q.ListServiceDomains(ctx, r.ID); err != nil {
			return nil, fmt.Errorf("list domains: %w", err)
		}
		if ls.Volumes, err = q.ListServiceVolumes(ctx, r.ID); err != nil {
			return nil, fmt.Errorf("list volumes: %w", err)
		}
	}
	if len(ids) > 0 {
		nets, err := q.ListServicesNetworks(ctx, ids)
		if err != nil {
			return nil, fmt.Errorf("list networks: %w", err)
		}
		for _, n := range nets {
			byID[n.ServiceID].Networks = append(byID[n.ServiceID].Networks, n)
		}
		gits, err := q.ListServiceGitForServices(ctx, ids)
		if err != nil {
			return nil, fmt.Errorf("list git links: %w", err)
		}
		for _, g := range gits {
			g := g
			ls := byID[g.ServiceID]
			ls.Git = &g
			if c, ok := rf.connectionByID(g.ConnectionID); ok {
				ls.GitConn = c.Name
			}
		}
		tasks, err := q.ListServiceTasksForServices(ctx, ids)
		if err != nil {
			return nil, fmt.Errorf("list tasks: %w", err)
		}
		for _, t := range tasks {
			byID[t.ServiceID].Tasks[t.Name] = t
		}
		bks, err := q.ListBackupSchedulesForServices(ctx, ids)
		if err != nil {
			return nil, fmt.Errorf("list backup schedules: %w", err)
		}
		for _, b := range bks {
			byID[b.ServiceID].Backups = append(byID[b.ServiceID].Backups, b)
		}
		mons, err := q.ListUptimeMonitorsForServices(ctx, ids)
		if err != nil {
			return nil, fmt.Errorf("list monitors: %w", err)
		}
		for _, m := range mons {
			m := m
			byID[m.ServiceID].Monitor = &m
		}
		procScales, err := q.ListProcessScalesForServices(ctx, ids)
		if err != nil {
			return nil, fmt.Errorf("list process scales: %w", err)
		}
		for _, r := range procScales {
			byID[r.ServiceID].ProcScales = append(byID[r.ServiceID].ProcScales, r)
		}
	}
	links, err := q.ListEnvironmentLinks(ctx, env.Env.ID)
	if err != nil {
		return nil, fmt.Errorf("list links: %w", err)
	}
	for _, l := range links {
		if ls := byID[l.FromServiceID]; ls != nil {
			if to, ok := slugOf[l.ToServiceID]; ok {
				ls.Links = append(ls.Links, to)
			}
		}
	}
	for _, ls := range byID {
		sort.Strings(ls.Links)
	}

	srcs, err := q.ListConfigSourcesByEnvironment(ctx, env.Env.ID)
	if err != nil {
		return nil, fmt.Errorf("list config sources: %w", err)
	}
	for _, s := range srcs {
		ls := byID[s.ServiceID]
		if ls == nil {
			continue
		}
		s := s
		ls.Source = &s
		_ = json.Unmarshal(s.Owned, &ls.Owned)
		_ = json.Unmarshal(s.Pending, &ls.Pending)
		if ls.Owned == nil {
			ls.Owned = map[string]string{}
		}
		if ls.Pending == nil {
			ls.Pending = map[string]json.RawMessage{}
		}
	}

	if le.SharedProject, err = loadShared(box, func() ([]db.SharedVariable, error) {
		return q.ListSharedVariablesProject(ctx, env.Env.ProjectID)
	}); err != nil {
		return nil, err
	}
	if le.SharedEnv, err = loadShared(box, func() ([]db.SharedVariable, error) {
		return q.ListSharedVariablesEnv(ctx, &env.Env.ID)
	}); err != nil {
		return nil, err
	}
	set, err := q.GetEnvironmentConfigSettings(ctx, env.Env.ID)
	switch {
	case err == nil:
		le.Settings = &set
		_ = json.Unmarshal(set.OwnedShared, &le.OwnedShared)
		if le.OwnedShared == nil {
			le.OwnedShared = map[string]string{}
		}
	case !errors.Is(err, pgx.ErrNoRows):
		return nil, fmt.Errorf("get environment settings: %w", err)
	}
	return le, nil
}

func loadShared(box *secretbox.Box, list func() ([]db.SharedVariable, error)) (map[string]liveVar, error) {
	rows, err := list()
	if err != nil {
		return nil, fmt.Errorf("list shared variables: %w", err)
	}
	out := make(map[string]liveVar, len(rows))
	for _, v := range rows {
		lv, err := decodeVar(box, v.ID.String(), v.Key, v.ValueEnc, v.TemplateEnc, v.IsSecret, "runtime", v.Comment, nil, v.Source, secretbox.SharedVariablesValue, secretbox.SharedVariablesTemplate)
		if err != nil {
			return nil, err
		}
		out[v.Key] = lv
	}
	return out, nil
}

// decodeVar reads the parts of a stored variable a file can compare with. The
// value of a secret is opened only to learn whether it consists of references
// alone (then it is its own template); it is never kept.
func decodeVar(box *secretbox.Box, rid, key string, valueEnc, templateEnc []byte, secret bool, scope, comment string, rotate *int32, source string, valueCtx, tplCtx secretbox.Column) (liveVar, error) {
	lv := liveVar{Key: key, Secret: secret, Scope: scope, Comment: comment, Source: source}
	if rotate != nil {
		n := int(*rotate)
		lv.Rotate = &n
	}
	if len(templateEnc) > 0 {
		t, err := box.OpenString(tplCtx, rid, templateEnc)
		if err != nil {
			return lv, fmt.Errorf("decrypt template of %s: %w", key, err)
		}
		lv.Template = t
		return lv, nil
	}
	val, err := box.OpenString(valueCtx, rid, valueEnc)
	if err != nil {
		return lv, fmt.Errorf("decrypt variable %s: %w", key, err)
	}
	if !secret {
		lv.Template = val
		return lv, nil
	}
	if referenceOnly(val) {
		lv.Template = val
	} else {
		lv.Opaque = true
	}
	return lv, nil
}

// referenceOnly reports whether text is a template made of references and
// transforms only (no literal characters, no generators): such a text is
// not a secret itself and a file can compare against it.
func referenceOnly(text string) bool {
	if !strings.Contains(text, "${{") {
		return false
	}
	t, err := varexpr.Parse(text)
	if err != nil || t.HasGenerators() {
		return false
	}
	for _, p := range t.Parts {
		if p.Expr == nil {
			if p.Text != "" {
				return false
			}
		}
	}
	return true
}

// Aliases for the stored rows of tasks and backup schedules.
type (
	liveTask   = db.ServiceTask
	liveBackup = db.BackupSchedule
)

// backupKeys maps the key of each backup schedule to its row: explicit config keys first, then the
// schedules created elsewhere under the synthesised keys schedule-1, schedule-2, ... (creation
// order, skipping keys already taken). A file that writes one of those keys adopts the schedule.
func (ls *liveService) backupKeys() map[string]liveBackup {
	out := make(map[string]liveBackup, len(ls.Backups))
	for _, b := range ls.Backups {
		if b.ConfigKey != nil {
			out[*b.ConfigKey] = b
		}
	}
	n := 0
	for _, b := range ls.Backups {
		if b.ConfigKey != nil {
			continue
		}
		for {
			n++
			k := fmt.Sprintf("schedule-%d", n)
			if _, taken := out[k]; !taken {
				out[k] = b
				break
			}
		}
	}
	return out
}
