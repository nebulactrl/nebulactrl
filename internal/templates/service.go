package templates

import (
	"context"
	"log/slog"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/nebulactrl/nebulactrl/internal/db"
	"github.com/nebulactrl/nebulactrl/internal/deployments"
	"github.com/nebulactrl/nebulactrl/internal/events"
	"github.com/nebulactrl/nebulactrl/internal/jobs"
	"github.com/nebulactrl/nebulactrl/internal/services"
)

// VariableWriter writes one variable of a service inside the caller's
// transaction (the variables package: materialises generators, forces secrets
// for generated values, bumps the generation, audits the key).
type VariableWriter interface {
	UpsertTx(ctx context.Context, tx pgx.Tx, serviceID uuid.UUID, key, value string, isSecret bool) error
}

// DatabaseSpec asks the databases package for a managed database.
type DatabaseSpec struct {
	EnvironmentID uuid.UUID
	Name          string
	Engine        string
	Version       string
	DatabaseName  string
	ServerID      *uuid.UUID
}

// DatabaseCreator creates a managed database service inside the caller's
// transaction (no audit, no events).
type DatabaseCreator interface {
	CreateTx(ctx context.Context, tx pgx.Tx, in DatabaseSpec) (db.Service, error)
}

// PortRoute is a published TCP/UDP port of a template service.
type PortRoute struct {
	EnvironmentID   uuid.UUID
	TargetServiceID uuid.UUID
	Name            string
	Protocol        string
	ListenPort      int
	TargetPort      int
}

// PortRouter creates TCP/UDP proxy routes (the proxyroutes package).
type PortRouter interface {
	// AddPortRouteTx stages a route and returns the id of the route service.
	AddPortRouteTx(ctx context.Context, tx pgx.Tx, in PortRoute) (uuid.UUID, error)
	// PortInUse reports whether another route on the server owns the port.
	PortInUse(ctx context.Context, q *db.Queries, serverID uuid.UUID, protocol string, port int) (bool, error)
}

// Deployer deploys staged services (the deployments package).
type Deployer interface {
	DeployStaged(ctx context.Context, envID uuid.UUID, serviceIDs []uuid.UUID, message string) (deployments.StagedResult, error)
}

// Deps are the collaborators of the template service.
type Deps struct {
	Pool      *pgxpool.Pool
	Bus       *events.Bus
	Jobs      *jobs.Manager
	Catalog   *Catalog
	Services  *services.Service
	Variables VariableWriter
	Databases DatabaseCreator
	Ports     PortRouter
	Deploy    Deployer
	// Materialize evaluates input generators (variables.Materialize).
	Materialize Materializer
	Logger      *slog.Logger
}

// Service implements the template use cases.
type Service struct {
	Deps
	q *db.Queries
}

// NewService returns a Service (Pool may be nil for OpenAPI generation).
func NewService(d Deps) *Service {
	if d.Logger == nil {
		d.Logger = slog.Default()
	}
	s := &Service{Deps: d}
	if d.Pool != nil {
		s.q = db.New(d.Pool)
	}
	return s
}

// ---- views ----

// TemplateCategory is a gallery category.
type TemplateCategory struct {
	ID    string `json:"id"`
	Label string `json:"label"`
	Count int    `json:"count"`
}

// categoryLabels are the display names of Categories.
var categoryLabels = map[string]string{
	CategoryHomelab: "Homelab & networking", CategoryMedia: "Media & entertainment", CategoryStorage: "Files & storage",
	CategoryGames: "Game servers", CategoryDevtools: "Developer tools", CategoryAI: "AI & machine learning",
	CategoryData: "Databases & backend", CategoryWeb: "Websites & CMS", CategoryAnalytics: "Analytics & monitoring",
	CategoryAutomation: "Automation & workflows", CategoryProductivity: "Productivity & communication",
	CategorySecurity: "Security & identity",
}

// TemplateSummary is a catalog card.
type TemplateSummary struct {
	ID       string `json:"id"`
	Revision int    `json:"revision"`
	// Version is the app version the template pins (the tag of its main image).
	Version    string   `json:"version,omitempty"`
	Name       string   `json:"name"`
	Summary    string   `json:"summary"`
	Category   string   `json:"category"`
	Tags       []string `json:"tags"`
	Icon       string   `json:"icon,omitempty"`
	License    string   `json:"license"`
	LicenseURL string   `json:"licenseUrl,omitempty"`
	// LicenseClass is permissive | copyleft | source-available | proprietary.
	LicenseClass string `json:"licenseClass"`
	// Services is the number of services an install creates (databases included).
	Services int `json:"services"`
	// Databases lists the managed database engines of the template.
	Databases []string `json:"databases"`
	MemoryMB  int      `json:"memoryMb"`
	// PublishesPorts is true when the install exposes TCP/UDP ports (game servers).
	PublishesPorts bool `json:"publishesPorts"`
	// Heavy is true for installs that download several GB or need a lot of RAM.
	Heavy bool `json:"heavy,omitempty"`
	// RequiresUnrestricted is true when the install needs a server an instance admin made unrestricted (devices, GPUs, host-level options).
	RequiresUnrestricted bool `json:"requiresUnrestricted,omitempty"`
}

// summaryOf builds the card of t.
func summaryOf(t *CatalogTemplate) TemplateSummary {
	s := TemplateSummary{
		ID: t.ID, Revision: t.Revision, Version: AppVersion(t), Name: t.Name, Summary: t.Summary, Category: t.Category, Tags: nonNil(t.Tags), Icon: t.Icon,
		License: t.Upstream.License, LicenseURL: t.Upstream.LicenseURL, LicenseClass: t.Upstream.Class,
		Services: len(t.Services), Databases: []string{}, MemoryMB: t.Requires.MemoryMB, Heavy: t.Heavy, RequiresUnrestricted: t.RequiresUnrestricted,
	}
	mem := 0
	for _, sv := range t.Services {
		if sv.Database != nil {
			s.Databases = append(s.Databases, sv.Database.Engine)
		}
		if sv.Resources != nil {
			mem += sv.Resources.MemoryMB
		}
		for _, p := range sv.Ports {
			s.PublishesPorts = s.PublishesPorts || p.Publish
		}
	}
	if s.MemoryMB == 0 {
		s.MemoryMB = mem
	}
	return s
}

func nonNil(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}

// TemplateInstanceService is one service of an installed stack.
type TemplateInstanceService struct {
	ID   uuid.UUID `json:"id"`
	Key  string    `json:"key"`
	Slug string    `json:"slug"`
	Name string    `json:"name"`
}

// TemplateInstance is an installed template stack.
type TemplateInstance struct {
	ID               uuid.UUID `json:"id"`
	EnvironmentID    uuid.UUID `json:"environmentId"`
	TemplateID       string    `json:"templateId"`
	TemplateRevision int       `json:"templateRevision"`
	// TemplateVersion is the app version the install pinned (the tag of the
	// template's main image), "" for installs from before versions were recorded.
	TemplateVersion string `json:"templateVersion"`
	// CurrentRevision and CurrentVersion describe the template as the catalog
	// has it now; both are 0/"" when the template left the catalog.
	CurrentRevision int    `json:"currentRevision"`
	CurrentVersion  string `json:"currentVersion"`
	// UpdateAvailable is true when the catalog's template differs from the
	// installed one in something an update can apply or must report.
	UpdateAvailable bool `json:"updateAvailable"`
	// UpdateBlocked says why an available revision cannot be applied
	// (an install from before updates existed, or a template that left the catalog).
	UpdateBlocked string                    `json:"updateBlocked,omitempty"`
	UpdatedAt     *time.Time                `json:"updatedAt,omitempty"`
	Name          string                    `json:"name"`
	GroupID       string                    `json:"groupId"`
	Inputs        map[string]string         `json:"inputs"`
	Services      []TemplateInstanceService `json:"services"`
	CreatedAt     time.Time                 `json:"createdAt"`
}
