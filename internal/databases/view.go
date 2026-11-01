package databases

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/nebulactrl/nebulactrl/internal/agents"
	"github.com/nebulactrl/nebulactrl/internal/apierr"
	"github.com/nebulactrl/nebulactrl/internal/databases/engines"
	"github.com/nebulactrl/nebulactrl/internal/db"
	"github.com/nebulactrl/nebulactrl/internal/policy"
	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

// VersionView, FlavorView and EngineView describe the catalog to the UI.
type VersionView struct {
	ID      string `json:"id"`
	Label   string `json:"label"`
	Default bool   `json:"default"`
	Notes   string `json:"notes,omitempty"`
}

// FlavorView is an image variant of an engine.
type FlavorView struct {
	ID       string   `json:"id"`
	Label    string   `json:"label"`
	Note     string   `json:"note,omitempty"`
	Versions []string `json:"versions"`
}

// EngineView is one engine of the catalog.
type EngineView struct {
	ID           string        `json:"id"`
	Label        string        `json:"label"`
	Description  string        `json:"description"`
	Category     string        `json:"category" enum:"relational,keyvalue,document,analytics"`
	Port         int           `json:"port"`
	Default      bool          `json:"default" doc:"The preselected engine of its category"`
	OptIn        bool          `json:"optIn" doc:"Creation needs acceptLicense=true"`
	License      string        `json:"license"`
	LicenseClass string        `json:"licenseClass" enum:"permissive,copyleft,source-available"`
	LicenseNote  string        `json:"licenseNote,omitempty"`
	Rotation     string        `json:"rotation" enum:"exec,redeploy"`
	Versions     []VersionView `json:"versions"`
	Flavors      []FlavorView  `json:"flavors"`
}

// ToEngineView converts a catalog engine.
func ToEngineView(e engines.Engine) EngineView {
	v := EngineView{
		ID: e.ID, Label: e.Label, Description: e.Description, Category: e.Category, Port: e.Port, Default: e.Default, OptIn: e.OptIn,
		License: e.License, LicenseClass: e.LicenseClass, LicenseNote: e.LicenseNote, Rotation: string(e.Rotation),
		Versions: []VersionView{}, Flavors: []FlavorView{},
	}
	for _, ver := range e.Versions {
		v.Versions = append(v.Versions, VersionView{ID: ver.ID, Label: ver.Label, Default: ver.Default, Notes: ver.Notes})
	}
	for _, f := range e.Flavors {
		fv := FlavorView{ID: f.ID, Label: f.Label, Note: f.Note, Versions: []string{}}
		for _, id := range e.SortedVersionIDs() {
			if _, ok := f.Tags[id]; ok {
				fv.Versions = append(fv.Versions, id)
			}
		}
		v.Flavors = append(v.Flavors, fv)
	}
	return v
}

// Catalog lists the engines that can be created.
func (s *Service) Catalog(ctx context.Context) ([]EngineView, error) {
	if err := policy.Require(ctx, policy.ScopeProjectsRead, nil); err != nil {
		return nil, err
	}
	out := make([]EngineView, 0, len(engines.Catalog()))
	for _, e := range engines.Catalog() {
		out = append(out, ToEngineView(e))
	}
	return out, nil
}

// ExportView names a derived connection variable. Values are never listed: use
// the audited reveal action.
type ExportView struct {
	Key         string `json:"key"`
	Description string `json:"description"`
	Secret      bool   `json:"secret"`
	Primary     bool   `json:"primary"`
	Reference   string `json:"reference" doc:"How other services reference it"`
}

// InfoView is the last snapshot the agent reported.
type InfoView struct {
	Version     string    `json:"version,omitempty"`
	SizeBytes   int64     `json:"sizeBytes"`
	Connections int       `json:"connections"`
	UpdatedAt   time.Time `json:"updatedAt"`
	Error       string    `json:"error,omitempty"`
}

// RotationView is a past or running credential rotation.
type RotationView struct {
	ID         uuid.UUID  `json:"id"`
	Mode       string     `json:"mode"`
	Status     string     `json:"status" enum:"pending,applied,failed"`
	Error      string     `json:"error,omitempty"`
	CreatedAt  time.Time  `json:"createdAt"`
	FinishedAt *time.Time `json:"finishedAt,omitempty"`
}

// UpgradeView is a past or running pg_upgrade.
type UpgradeView struct {
	ID          uuid.UUID  `json:"id"`
	Method      string     `json:"method"`
	FromVersion string     `json:"fromVersion"`
	ToVersion   string     `json:"toVersion"`
	OldVolume   string     `json:"oldVolume"`
	NewVolume   string     `json:"newVolume"`
	Status      string     `json:"status" enum:"pending,ready,failed"`
	Error       string     `json:"error,omitempty"`
	CreatedAt   time.Time  `json:"createdAt"`
	FinishedAt  *time.Time `json:"finishedAt,omitempty"`
}

// DatabaseView is the managed-database panel of a service.
type DatabaseView struct {
	ServiceID      uuid.UUID     `json:"serviceId"`
	Engine         EngineView    `json:"engine"`
	Spec           engines.Spec  `json:"spec"`
	Image          string        `json:"image"`
	Deployed       bool          `json:"deployed"`
	CredentialKeys []string      `json:"credentialKeys" doc:"Variables that hold credentials; rotate them with the rotate action"`
	Exports        []ExportView  `json:"exports"`
	Info           *InfoView     `json:"info,omitempty"`
	Rotation       *RotationView `json:"rotation,omitempty" doc:"The latest rotation"`
	Upgrade        *UpgradeView  `json:"upgrade,omitempty" doc:"The latest pg_upgrade"`
	// Replication is the read replica group of a PostgreSQL database (role
	// "standalone" when it has none). Replication is manual: nothing is detected or switched automatically.
	Replication *ReplicationView `json:"replication,omitempty"`
}

// Get returns the managed-database panel of a service.
func (s *Service) Get(ctx context.Context, serviceID uuid.UUID) (DatabaseView, error) {
	sc, spec, eng, err := s.managed(ctx, policy.ScopeProjectsRead, serviceID)
	if err != nil {
		return DatabaseView{}, err
	}
	v := DatabaseView{
		ServiceID: serviceID, Engine: ToEngineView(*eng), Spec: spec, Image: spec.Image(), Deployed: sc.Service.CurrentDeploymentID != nil,
		CredentialKeys: eng.CredentialKeys(), Exports: []ExportView{},
	}
	primary := engines.PrimaryExport(eng.ID)
	for _, e := range exportCatalog(spec) {
		v.Exports = append(v.Exports, ExportView{
			Key: e.Key, Description: e.Description, Secret: e.Secret, Primary: e.Key == primary,
			Reference: "${{" + sc.Service.Slug + "." + e.Key + "}}",
		})
	}
	if info, err := s.q.GetDatabaseInfo(ctx, serviceID); err == nil {
		iv := &InfoView{UpdatedAt: info.UpdatedAt, Error: info.Error}
		if len(info.Info) > 0 {
			var d agentprotocol.DBInfo
			if json.Unmarshal(info.Info, &d) == nil {
				iv.Version, iv.SizeBytes, iv.Connections = d.Version, d.SizeBytes, d.Connections
			}
		}
		v.Info = iv
	} else if !isNoRows(err) {
		return DatabaseView{}, err
	}
	if rots, err := s.q.ListDatabaseRotations(ctx, serviceID); err != nil {
		return DatabaseView{}, err
	} else if len(rots) > 0 {
		r := rots[0]
		v.Rotation = &RotationView{ID: r.ID, Mode: r.Mode, Status: r.Status, Error: r.Error, CreatedAt: r.CreatedAt, FinishedAt: r.FinishedAt}
	}
	if up, err := s.q.GetLatestDatabaseUpgrade(ctx, serviceID); err == nil {
		v.Upgrade = &UpgradeView{
			ID: up.ID, Method: up.Method, FromVersion: up.FromVersion, ToVersion: up.ToVersion, OldVolume: up.OldVolume, NewVolume: up.NewVolume,
			Status: up.Status, Error: up.Error, CreatedAt: up.CreatedAt, FinishedAt: up.FinishedAt,
		}
	} else if !isNoRows(err) {
		return DatabaseView{}, err
	}
	if spec.Engine == engines.Postgres {
		rv, err := s.replicationView(ctx, s.q, sc)
		if err != nil {
			return DatabaseView{}, err
		}
		v.Replication = &rv
	}
	return v, nil
}

// RefreshResult is returned when an info refresh was queued.
type RefreshResult struct {
	CommandID uuid.UUID `json:"commandId"`
}

// Refresh asks the agent for the database's version, size and open
// connections; the answer lands in Get's `info` when the agent replies.
func (s *Service) Refresh(ctx context.Context, serviceID uuid.UUID) (RefreshResult, error) {
	sc, spec, _, err := s.managed(ctx, policy.ScopeProjectsRead, serviceID)
	if err != nil {
		return RefreshResult{}, err
	}
	if err := policy.Require(ctx, policy.ScopeDeploymentsWrite, &sc.ProjectID); err != nil {
		return RefreshResult{}, err
	}
	if sc.Service.DesiredState == "stopped" {
		return RefreshResult{}, apierr.Conflict("service_stopped", "the database is stopped")
	}
	serverID, err := s.currentServer(ctx, s.q, sc)
	if err != nil {
		return RefreshResult{}, err
	}
	raw := mustJSON(agentprotocol.DBAdminPayload{
		ServiceID: serviceID.String(), Action: agentprotocol.DBActionInfo,
		Engine: spec.Engine, Version: spec.Version, Flavor: spec.Flavor, Database: spec.DatabaseName, User: spec.Username,
	})
	var id uuid.UUID
	err = db.InTx(ctx, s.pool, func(tx pgx.Tx) error {
		var err error
		id, err = s.disp.Enqueue(ctx, tx, agents.NewCommand{ServerID: serverID, Kind: agentprotocol.KindDBAdmin, Payload: raw})
		return err
	})
	if err != nil {
		return RefreshResult{}, fmt.Errorf("enqueue db_admin: %w", err)
	}
	return RefreshResult{CommandID: id}, nil
}
