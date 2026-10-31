package platform

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"

	"github.com/nebulactrl/nebulactrl/internal/db"
	"github.com/nebulactrl/nebulactrl/internal/services"
)

// PlatformServiceRef is a service of a platform service.
type PlatformServiceRef struct {
	ID   uuid.UUID `json:"id"`
	Name string    `json:"name"`
	Slug string    `json:"slug"`
	// State is running, starting, stopped, staged (changes wait for a deploy), missing (deleted outside this page) or not deployed.
	State  string `json:"state" enum:"running,starting,stopped,staged,not_deployed,missing"`
	Health string `json:"health,omitempty"`
}

// PlatformWiringView says what a platform service is connected to.
type PlatformWiringView struct {
	Type string `json:"type" enum:"tailscale_integration"`
	ID   string `json:"id"`
	Name string `json:"name"`
}

// PlatformBackupView is the volume backup of a data-holding platform service.
type PlatformBackupView struct {
	Configured      bool       `json:"configured"`
	DestinationID   *uuid.UUID `json:"destinationId,omitempty"`
	DestinationName string     `json:"destinationName,omitempty"`
	Hint            string     `json:"hint,omitempty"`
}

// PlatformServiceView is a platform service as the API returns it. Credentials never appear.
type PlatformServiceView struct {
	ID      uuid.UUID `json:"id"`
	Kind    string    `json:"kind" enum:"headscale"`
	Label   string    `json:"label"`
	Status  string    `json:"status" enum:"provisioning,ready,failed,removing"`
	Step    string    `json:"step" enum:"configure,deploy,await,wire,done"`
	Message string    `json:"message" doc:"What is happening or why it failed."`

	Hostname  string `json:"hostname"`
	PublicURL string `json:"publicUrl"`

	ServerID   *uuid.UUID `json:"serverId,omitempty"`
	ServerName string     `json:"serverName,omitempty"`
	// Project, environment and service links to the canvas of the system project.
	ProjectID       *uuid.UUID           `json:"projectId,omitempty"`
	EnvironmentSlug string               `json:"environmentSlug,omitempty"`
	Services        []PlatformServiceRef `json:"services"`

	Wiring *PlatformWiringView `json:"wiring,omitempty"`
	Backup *PlatformBackupView `json:"backup,omitempty"`

	// Details are non-secret facts to show (bucket, retention, key expiry, ...).
	Details  map[string]string `json:"details"`
	Warnings []string          `json:"warnings"`

	CreatedAt time.Time `json:"createdAt"`
	UpdatedAt time.Time `json:"updatedAt"`
}

func (s *Service) view(ctx context.Context, row db.PlatformService) (PlatformServiceView, error) {
	info, _ := InfoFor(row.Kind)
	opts := decodeJSON[options](row.Options)
	w := decodeJSON[wiring](row.Wiring)
	v := PlatformServiceView{
		ID: row.ID, Kind: row.Kind, Label: info.Label, Status: row.Status, Step: row.Step, Message: row.StatusMessage,
		Hostname: row.Hostname, PublicURL: row.PublicUrl, Services: []PlatformServiceRef{}, Details: map[string]string{}, Warnings: []string{},
		CreatedAt: row.CreatedAt, UpdatedAt: row.UpdatedAt,
	}
	rows, err := s.q.ListServicesIncludingDeleted(ctx, row.ServiceIds)
	if err != nil {
		return v, err
	}
	byID := map[uuid.UUID]db.Service{}
	for _, r := range rows {
		byID[r.ID] = r
	}
	for _, id := range row.ServiceIds {
		r, ok := byID[id]
		if !ok || r.DeletedAt != nil {
			v.Services = append(v.Services, PlatformServiceRef{ID: id, Name: "(removed)", State: "missing"})
			if row.Status == StatusReady {
				v.Warnings = append(v.Warnings, "A service of this platform service was removed outside this page. Remove it here and set it up again.")
			}
			continue
		}
		ref := PlatformServiceRef{ID: r.ID, Name: r.Name, Slug: r.Slug, State: serviceState(r)}
		if o := services.DecodeObserved(r.Observed); o != nil {
			ref.Health = o.Health
		}
		v.Services = append(v.Services, ref)
		if v.ServerID == nil && r.ServerID != nil {
			v.ServerID = r.ServerID
		}
	}
	if v.ServerID != nil {
		if srv, err := s.q.LoadServerRow(ctx, db.LoadServerRowParams{ID: *v.ServerID, WorkspaceID: &row.WorkspaceID}); err == nil {
			v.ServerName = srv.Name
		}
	}
	if row.EnvironmentID != nil {
		if env, err := s.q.GetEnvironmentScoped(ctx, db.GetEnvironmentScopedParams{ID: *row.EnvironmentID, WorkspaceID: row.WorkspaceID}); err == nil {
			v.ProjectID, v.EnvironmentSlug = &env.ProjectID, env.Slug
		}
	}
	if w.Type != "" && w.ID != "" {
		v.Wiring = &PlatformWiringView{Type: w.Type, ID: w.ID, Name: w.Name}
	}
	if opts.KeyExpiresAt != nil {
		v.Details["apiKeyExpires"] = opts.KeyExpiresAt.UTC().Format("2006-01-02")
	}
	if info.HoldsData && row.Status == StatusReady {
		v.Backup = s.backupView(ctx, w)
	}
	return v, nil
}

func (s *Service) backupView(ctx context.Context, w wiring) *PlatformBackupView {
	b := &PlatformBackupView{Hint: "Not backed up. Add an external backup destination, then schedule a backup of its volume."}
	id, err := uuid.Parse(w.BackupDestinationID)
	if err != nil {
		return b
	}
	b.Configured, b.DestinationID, b.Hint = true, &id, "Backed up daily at 03:17 UTC."
	if dests, err := s.Backups.ListDestinations(ctx); err == nil {
		for _, d := range dests {
			if d.ID == id {
				b.DestinationName = d.Name
			}
		}
	}
	return b
}

// serviceState summarizes a service row for the page.
func serviceState(r db.Service) string {
	o := services.DecodeObserved(r.Observed)
	switch {
	case o != nil && o.State == "running" && o.Health == "healthy":
		if r.Generation > r.AppliedGeneration {
			return "staged"
		}
		return "running"
	case o != nil && o.State == "running":
		return "starting"
	case o != nil && (o.State == "exited" || o.State == "stopped"):
		return "stopped"
	case r.CurrentDeploymentID == nil:
		return "not_deployed"
	}
	return "starting"
}

// PlatformKindView is one entry of the catalog with the team's instance of it.
type PlatformKindView struct {
	PlatformKindInfo
	// Instance is the team's platform service of this kind, if any.
	Instance *PlatformServiceView `json:"instance,omitempty"`
}

// PlatformListing is the Team settings → Private network (Headscale) page: the catalog and what is set up.
type PlatformListing struct {
	Kinds []PlatformKindView `json:"kinds"`
	// Project is the system project (nil until the first service).
	ProjectID *uuid.UUID `json:"projectId,omitempty"`
}

// List returns the catalog with the team's platform services.
func (s *Service) List(ctx context.Context) (PlatformListing, error) {
	ws, err := s.admin(ctx)
	if err != nil {
		return PlatformListing{}, err
	}
	rows, err := s.q.ListPlatformServices(ctx, ws)
	if err != nil {
		return PlatformListing{}, fmt.Errorf("list platform services: %w", err)
	}
	out := PlatformListing{Kinds: []PlatformKindView{}}
	byKind := map[string]db.PlatformService{}
	for _, r := range rows {
		byKind[r.Kind] = r
	}
	if p, err := s.q.GetSystemProject(ctx, ws); err == nil {
		out.ProjectID = &p.ID
	}
	for _, info := range Catalog() {
		kv := PlatformKindView{PlatformKindInfo: info}
		if r, ok := byKind[info.Kind]; ok {
			v, err := s.view(ctx, r)
			if err != nil {
				return PlatformListing{}, err
			}
			kv.Instance = &v
		}
		out.Kinds = append(out.Kinds, kv)
	}
	return out, nil
}

// Get returns one platform service.
func (s *Service) Get(ctx context.Context, id uuid.UUID) (PlatformServiceView, error) {
	row, err := s.load(ctx, id)
	if err != nil {
		return PlatformServiceView{}, err
	}
	return s.view(ctx, row)
}
