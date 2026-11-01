package statuspages

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"net/url"
	"regexp"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/nebulactrl/nebulactrl/internal/apierr"
	"github.com/nebulactrl/nebulactrl/internal/audit"
	"github.com/nebulactrl/nebulactrl/internal/auth"
	"github.com/nebulactrl/nebulactrl/internal/db"
	"github.com/nebulactrl/nebulactrl/internal/domains"
	"github.com/nebulactrl/nebulactrl/internal/hostclaim"
	"github.com/nebulactrl/nebulactrl/internal/integrations/cloudflare"
	"github.com/nebulactrl/nebulactrl/internal/systemroute"
)

// Limits of the text a page holds.
const (
	maxTitleLen       = 100
	maxDescriptionLen = 500
	maxSlugLen        = 50
	maxDisplayNameLen = 60
	maxGroupNameLen   = 60
	maxFooterLen      = 500
	maxURLLen         = 300
	// MaxComponents is how many components one page can show.
	MaxComponents = 100

	dnsLookupTimeout = 10 * time.Second // the authoritative servers, then the public resolvers, then the host's
)

// Themes of a public page.
var themes = []string{"auto", "light", "dark"}

var slugPattern = regexp.MustCompile(`^[a-z0-9]+(-[a-z0-9]+)*$`)

// StatusPageView is a status page as the API shows it.
type StatusPageView struct {
	ID          uuid.UUID                 `json:"id"`
	Title       string                    `json:"title"`
	Slug        string                    `json:"slug"`
	Description string                    `json:"description"`
	Footer      string                    `json:"footer" doc:"Plain text shown under the page"`
	Hostname    *string                   `json:"hostname" doc:"The public hostname; null until one is set"`
	Published   bool                      `json:"published" doc:"Served on the hostname (needs a hostname and at least one component)"`
	ShowUptime  bool                      `json:"showUptime" doc:"Show availability percentages next to the day bars"`
	Theme       string                    `json:"theme" enum:"auto,light,dark"`
	Components  []StatusPageComponentView `json:"components" doc:"In page order"`
	CreatedAt   time.Time                 `json:"createdAt"`
	UpdatedAt   time.Time                 `json:"updatedAt"`
}

// StatusPageComponentView is one service shown on a page. The service, environment and
// project names are internal: they are empty for callers restricted to other
// projects, and never reach the public page.
type StatusPageComponentView struct {
	ServiceID       uuid.UUID `json:"serviceId"`
	DisplayName     string    `json:"displayName" doc:"The public name"`
	GroupName       string    `json:"groupName" doc:"Components with the same group name are shown together; empty = ungrouped"`
	URL             string    `json:"url" doc:"Optional https link the component's name points to; empty for none"`
	ServiceName     string    `json:"serviceName"`
	EnvironmentName string    `json:"environmentName"`
	ProjectName     string    `json:"projectName"`
}

// PageCreate creates a page (unpublished: publishing needs components).
type PageCreate struct {
	Title       string
	Slug        string // default: derived from the title
	Description string
	Footer      string
	Hostname    string
	ShowUptime  *bool // default true
	Theme       string
	// FromProjectID starts the page with the components of a project: its
	// production-environment services that have a public domain.
	FromProjectID *uuid.UUID
}

// PagePatch changes a page; nil fields stay as they are. An empty Hostname
// removes it.
type PagePatch struct {
	Title       *string
	Slug        *string
	Description *string
	Footer      *string
	Hostname    *string
	Published   *bool
	ShowUptime  *bool
	Theme       *string
}

// ComponentInput is one entry of a page's ordered component list.
type ComponentInput struct {
	ServiceID   uuid.UUID
	DisplayName string // default: the service's name
	GroupName   string
	URL         string // optional absolute https link for the name
}

func (s *Service) componentViews(rows []db.ListStatusPageComponentsRow, p *auth.Principal) map[uuid.UUID][]StatusPageComponentView {
	out := make(map[uuid.UUID][]StatusPageComponentView)
	for _, r := range rows {
		v := StatusPageComponentView{ServiceID: r.ServiceID, DisplayName: r.DisplayName, GroupName: r.GroupName, URL: r.Url}
		if p == nil || p.CanSeeProject(r.ProjectID) {
			v.ServiceName, v.EnvironmentName, v.ProjectName = r.ServiceName, r.EnvironmentName, r.ProjectName
		}
		out[r.PageID] = append(out[r.PageID], v)
	}
	return out
}

func toPageView(row db.StatusPage, comps []StatusPageComponentView) StatusPageView {
	if comps == nil {
		comps = []StatusPageComponentView{}
	}
	return StatusPageView{
		ID: row.ID, Title: row.Title, Slug: row.Slug, Description: row.Description, Hostname: row.Hostname,
		Published: row.Published, ShowUptime: row.ShowUptime, Theme: row.Theme, Components: comps,
		CreatedAt: row.CreatedAt, UpdatedAt: row.UpdatedAt, Footer: row.Footer,
	}
}

func (s *Service) pageView(ctx context.Context, q *db.Queries, p *auth.Principal, row db.StatusPage) (StatusPageView, error) {
	comps, err := q.ListStatusPageComponents(ctx, db.ListStatusPageComponentsParams{WorkspaceID: row.WorkspaceID, PageID: &row.ID})
	if err != nil {
		return StatusPageView{}, fmt.Errorf("list components: %w", err)
	}
	return toPageView(row, s.componentViews(comps, p)[row.ID]), nil
}

// List returns the workspace's status pages with their components.
func (s *Service) List(ctx context.Context) ([]StatusPageView, error) {
	ws, p, err := s.reader(ctx)
	if err != nil {
		return nil, err
	}
	rows, err := s.q.ListStatusPages(ctx, ws)
	if err != nil {
		return nil, fmt.Errorf("list status pages: %w", err)
	}
	comps, err := s.q.ListStatusPageComponents(ctx, db.ListStatusPageComponentsParams{WorkspaceID: ws})
	if err != nil {
		return nil, fmt.Errorf("list components: %w", err)
	}
	byPage := s.componentViews(comps, p)
	out := make([]StatusPageView, 0, len(rows))
	for _, r := range rows {
		out = append(out, toPageView(r, byPage[r.ID]))
	}
	return out, nil
}

// Get returns one status page.
func (s *Service) Get(ctx context.Context, id uuid.UUID) (StatusPageView, error) {
	ws, p, err := s.reader(ctx)
	if err != nil {
		return StatusPageView{}, err
	}
	row, err := s.q.GetStatusPage(ctx, db.GetStatusPageParams{ID: id, WorkspaceID: ws})
	if errors.Is(err, pgx.ErrNoRows) {
		return StatusPageView{}, apierr.NotFound("status page")
	}
	if err != nil {
		return StatusPageView{}, fmt.Errorf("get status page: %w", err)
	}
	return s.pageView(ctx, s.q, p, row)
}

// ValidateHostname normalizes (lowercase, no trailing dot) and validates the
// hostname of a status page, and refuses the dashboard's own host.
func ValidateHostname(raw string) (string, error) {
	h, err := domains.ValidateHostname(raw)
	if err != nil {
		return "", err
	}
	if systemroute.Reserved(h) {
		return "", apierr.Conflict("hostname_reserved", "the hostname "+h+" is the dashboard's own address")
	}
	return h, nil
}

// checkHostnameFree refuses a hostname a service domain already serves or that
// lies in a Cloudflare zone another team connected. The answer is the same
// "not available" whatever the reason: it never says whether, or which team,
// holds the name.
func checkHostnameFree(ctx context.Context, q *db.Queries, ws uuid.UUID, host string) error {
	taken, err := q.DomainHostnameTaken(ctx, host)
	if err != nil {
		return fmt.Errorf("check service domains: %w", err)
	}
	if taken {
		return hostclaim.Unavailable("hostname_conflict", host)
	}
	return hostclaim.Check(ctx, q, ws, "hostname_conflict", host)
}

// checkPublishable enforces what publishing needs: a hostname to serve on and
// at least one component to show.
func checkPublishable(hostname *string, liveComponents int) error {
	if hostname == nil || *hostname == "" {
		return apierr.Unprocessable("publish_needs_hostname", "set a hostname before publishing the page")
	}
	if liveComponents == 0 {
		return apierr.Unprocessable("publish_needs_components", "add at least one component before publishing the page")
	}
	return nil
}

// servedHost is the hostname the page is served on, "" while it is not served.
func servedHost(r db.StatusPage) string {
	if r.Published && r.Hostname != nil {
		return *r.Hostname
	}
	return ""
}

func checkSlug(slug string) (string, error) {
	slug = strings.TrimSpace(slug)
	if len(slug) > maxSlugLen || !slugPattern.MatchString(slug) {
		return "", apierr.Unprocessable("invalid_slug", "slug may only contain lowercase letters, digits and single hyphens, at most 50 characters")
	}
	return slug, nil
}

func checkTheme(theme string) error {
	if !slices.Contains(themes, theme) {
		return apierr.Unprocessable("invalid_theme", "theme must be auto, light or dark")
	}
	return nil
}

// checkComponentURL validates the optional link of a component: an absolute
// https URL with a host and no credentials, at most 300 characters. Empty
// means no link.
func checkComponentURL(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", nil
	}
	bad := func(msg string) error { return apierr.Unprocessable("invalid_url", msg) }
	if len(raw) > maxURLLen {
		return "", bad(fmt.Sprintf("url must be at most %d characters", maxURLLen))
	}
	if strings.ContainsFunc(raw, func(r rune) bool { return r <= ' ' || r == 0x7f }) {
		return "", bad("url must not contain spaces or control characters")
	}
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || u.Hostname() == "" {
		return "", bad("url must be an absolute https URL, e.g. https://example.com/docs")
	}
	if u.User != nil {
		return "", bad("url must not contain credentials")
	}
	return raw, nil
}

// slugify turns a title into a slug ("API status" becomes "api-status").
func slugify(title string) string {
	var b strings.Builder
	dash := false
	for _, r := range strings.ToLower(title) {
		switch {
		case r >= 'a' && r <= 'z' || r >= '0' && r <= '9':
			if dash && b.Len() > 0 {
				b.WriteByte('-')
			}
			dash = false
			b.WriteRune(r)
		default:
			dash = true
		}
	}
	s := b.String()
	if len(s) > maxSlugLen {
		s = strings.TrimRight(s[:maxSlugLen], "-")
	}
	if s == "" {
		return "status"
	}
	return s
}

// freeSlug returns base, or base-2, base-3, ... when taken.
func freeSlug(base string, taken map[string]bool) string {
	if !taken[base] {
		return base
	}
	for i := 2; ; i++ {
		suffix := fmt.Sprintf("-%d", i)
		cand := base
		if len(cand)+len(suffix) > maxSlugLen {
			cand = strings.TrimRight(cand[:maxSlugLen-len(suffix)], "-")
		}
		if cand += suffix; !taken[cand] {
			return cand
		}
	}
}

// Create adds an unpublished page.
func (s *Service) Create(ctx context.Context, in PageCreate) (StatusPageView, error) {
	ws, p, err := s.writer(ctx)
	if err != nil {
		return StatusPageView{}, err
	}
	title, err := cleanLine("title", in.Title, 1, maxTitleLen)
	if err != nil {
		return StatusPageView{}, err
	}
	desc, err := cleanText("description", in.Description, 0, maxDescriptionLen)
	if err != nil {
		return StatusPageView{}, err
	}
	footer, err := cleanText("footer", in.Footer, 0, maxFooterLen)
	if err != nil {
		return StatusPageView{}, err
	}
	theme := in.Theme
	if theme == "" {
		theme = "auto"
	}
	if err := checkTheme(theme); err != nil {
		return StatusPageView{}, err
	}
	showUptime := in.ShowUptime == nil || *in.ShowUptime
	var host *string
	if strings.TrimSpace(in.Hostname) != "" {
		h, err := ValidateHostname(in.Hostname)
		if err != nil {
			return StatusPageView{}, err
		}
		host = &h
	}
	explicitSlug := strings.TrimSpace(in.Slug) != ""
	slug := ""
	if explicitSlug {
		if slug, err = checkSlug(in.Slug); err != nil {
			return StatusPageView{}, err
		}
	}

	var row db.StatusPage
	err = db.InTx(ctx, s.pool, func(tx pgx.Tx) error {
		q := db.New(tx)
		if host != nil {
			if err := checkHostnameFree(ctx, q, ws, *host); err != nil {
				return err
			}
		}
		if !explicitSlug {
			existing, err := q.ListStatusPages(ctx, ws)
			if err != nil {
				return fmt.Errorf("list status pages: %w", err)
			}
			taken := make(map[string]bool, len(existing))
			for _, e := range existing {
				taken[e.Slug] = true
			}
			slug = freeSlug(slugify(title), taken)
		}
		var err error
		row, err = q.InsertStatusPage(ctx, db.InsertStatusPageParams{
			WorkspaceID: ws, Title: title, Slug: slug, Description: desc, Footer: footer, Hostname: host,
			Published: false, ShowUptime: showUptime, Theme: theme,
		})
		if err != nil {
			return err
		}
		md := map[string]any{"title": row.Title, "slug": row.Slug, "hostname": row.Hostname}
		if in.FromProjectID != nil {
			n, err := prefillComponents(ctx, q, ws, row.ID, *in.FromProjectID)
			if err != nil {
				return err
			}
			md["fromProject"], md["components"] = in.FromProjectID.String(), n
		}
		if host != nil && s.dns != nil {
			if err := s.dns.StatusPageHostChanged(ctx, tx, row.ID, true); err != nil {
				return err
			}
		}
		return audit.Record(ctx, tx, audit.Entry{
			Action: ActionPageCreate, ResourceType: resourcePage, ResourceID: row.ID.String(), WorkspaceID: ws,
			Metadata: md,
		})
	})
	if err != nil {
		return StatusPageView{}, fail("create status page", err)
	}
	s.publish(ws, row.ID)
	return s.pageView(ctx, s.q, p, row)
}

// Update changes a page. Publishing needs a hostname and a live component; a
// change of the served hostname re-sends the control-plane server's routes.
func (s *Service) Update(ctx context.Context, id uuid.UUID, in PagePatch) (StatusPageView, error) {
	ws, p, err := s.writer(ctx)
	if err != nil {
		return StatusPageView{}, err
	}
	var after db.StatusPage
	err = db.InTx(ctx, s.pool, func(tx pgx.Tx) error {
		q := db.New(tx)
		before, err := q.GetStatusPageForUpdate(ctx, db.GetStatusPageForUpdateParams{ID: id, WorkspaceID: ws})
		if errors.Is(err, pgx.ErrNoRows) {
			return apierr.NotFound("status page")
		}
		if err != nil {
			return fmt.Errorf("get status page: %w", err)
		}
		next := before
		var changed []string
		set := func(name string) { changed = append(changed, name) }
		if in.Title != nil {
			if next.Title, err = cleanLine("title", *in.Title, 1, maxTitleLen); err != nil {
				return err
			}
			set("title")
		}
		if in.Slug != nil {
			if next.Slug, err = checkSlug(*in.Slug); err != nil {
				return err
			}
			set("slug")
		}
		if in.Description != nil {
			if next.Description, err = cleanText("description", *in.Description, 0, maxDescriptionLen); err != nil {
				return err
			}
			set("description")
		}
		if in.Footer != nil {
			if next.Footer, err = cleanText("footer", *in.Footer, 0, maxFooterLen); err != nil {
				return err
			}
			set("footer")
		}
		hostChanged := false
		if in.Hostname != nil {
			if strings.TrimSpace(*in.Hostname) == "" {
				next.Hostname = nil
			} else {
				h, err := ValidateHostname(*in.Hostname)
				if err != nil {
					return err
				}
				next.Hostname = &h
			}
			hostChanged = (before.Hostname == nil) != (next.Hostname == nil) ||
				(before.Hostname != nil && next.Hostname != nil && *before.Hostname != *next.Hostname)
			if hostChanged {
				set("hostname")
				if next.Hostname != nil {
					if err := checkHostnameFree(ctx, q, ws, *next.Hostname); err != nil {
						return err
					}
				}
			}
		}
		if in.Published != nil {
			next.Published = *in.Published
			if next.Published != before.Published {
				set("published")
			}
		}
		if in.ShowUptime != nil {
			next.ShowUptime = *in.ShowUptime
			set("showUptime")
		}
		if in.Theme != nil {
			if err := checkTheme(*in.Theme); err != nil {
				return err
			}
			next.Theme = *in.Theme
			set("theme")
		}
		if next.Published && (!before.Published || hostChanged) {
			n, err := q.CountLiveStatusPageComponents(ctx, id)
			if err != nil {
				return fmt.Errorf("count components: %w", err)
			}
			if err := checkPublishable(next.Hostname, int(n)); err != nil {
				return err
			}
		}
		after, err = q.UpdateStatusPage(ctx, db.UpdateStatusPageParams{
			ID: id, WorkspaceID: ws, Title: next.Title, Slug: next.Slug, Description: next.Description, Footer: next.Footer, Hostname: next.Hostname,
			Published: next.Published, ShowUptime: next.ShowUptime, Theme: next.Theme,
		})
		if err != nil {
			return err
		}
		if err := audit.Record(ctx, tx, audit.Entry{
			Action: ActionPageUpdate, ResourceType: resourcePage, ResourceID: id.String(), WorkspaceID: ws,
			Metadata: map[string]any{"title": after.Title, "hostname": after.Hostname, "published": after.Published, "changed": changed},
		}); err != nil {
			return err
		}
		if hostChanged && s.dns != nil {
			if err := s.dns.StatusPageHostChanged(ctx, tx, id, before.Hostname == nil); err != nil {
				return err
			}
		}
		if servedHost(before) != servedHost(after) {
			return s.pushRoutes(ctx, tx)
		}
		return nil
	})
	if err != nil {
		return StatusPageView{}, fail("update status page", err)
	}
	s.publish(ws, id)
	return s.pageView(ctx, s.q, p, after)
}

// Delete removes a page. Incidents and maintenance windows stay (they lose the
// page); a served page's route is withdrawn.
func (s *Service) Delete(ctx context.Context, id uuid.UUID) error {
	ws, _, err := s.writer(ctx)
	if err != nil {
		return err
	}
	err = db.InTx(ctx, s.pool, func(tx pgx.Tx) error {
		q := db.New(tx)
		row, err := q.GetStatusPageForUpdate(ctx, db.GetStatusPageForUpdateParams{ID: id, WorkspaceID: ws})
		if errors.Is(err, pgx.ErrNoRows) {
			return apierr.NotFound("status page")
		}
		if err != nil {
			return fmt.Errorf("get status page: %w", err)
		}
		if _, err := q.DeleteStatusPage(ctx, db.DeleteStatusPageParams{ID: id, WorkspaceID: ws}); err != nil {
			return err
		}
		if row.Hostname != nil && s.dns != nil {
			if err := s.dns.StatusPageRemoved(ctx, tx, ws); err != nil {
				return err
			}
		}
		if err := audit.Record(ctx, tx, audit.Entry{
			Action: ActionPageDelete, ResourceType: resourcePage, ResourceID: id.String(), WorkspaceID: ws,
			Metadata: map[string]any{"title": row.Title, "hostname": row.Hostname, "published": row.Published},
		}); err != nil {
			return err
		}
		if servedHost(row) != "" {
			return s.pushRoutes(ctx, tx)
		}
		return nil
	})
	if err != nil {
		return fail("delete status page", err)
	}
	s.publish(ws, id)
	return nil
}

// SetComponents replaces the page's components with the ordered list. Every
// service must be a live service of the workspace; the display name defaults
// to the service's name and is at most 60 characters.
func (s *Service) SetComponents(ctx context.Context, id uuid.UUID, in []ComponentInput) (StatusPageView, error) {
	ws, p, err := s.writer(ctx)
	if err != nil {
		return StatusPageView{}, err
	}
	if len(in) > MaxComponents {
		return StatusPageView{}, apierr.Unprocessable("too_many_components", fmt.Sprintf("a page shows at most %d components", MaxComponents))
	}
	ids := make([]uuid.UUID, 0, len(in))
	for _, c := range in {
		ids = append(ids, c.ServiceID)
	}
	if len(dedupe(ids)) != len(ids) {
		return StatusPageView{}, apierr.Unprocessable("duplicate_component", "a service can be a component of a page only once")
	}
	var row db.StatusPage
	err = db.InTx(ctx, s.pool, func(tx pgx.Tx) error {
		q := db.New(tx)
		var err error
		row, err = q.GetStatusPageForUpdate(ctx, db.GetStatusPageForUpdateParams{ID: id, WorkspaceID: ws})
		if errors.Is(err, pgx.ErrNoRows) {
			return apierr.NotFound("status page")
		}
		if err != nil {
			return fmt.Errorf("get status page: %w", err)
		}
		svcs, err := workspaceServices(ctx, q, ws, ids)
		if err != nil {
			return err
		}
		type entry struct {
			service     uuid.UUID
			name, group string
			url         string
		}
		entries := make([]entry, 0, len(in))
		for _, c := range in {
			name := strings.TrimSpace(c.DisplayName)
			if name == "" {
				name = truncateRunes(svcs[c.ServiceID].Name, maxDisplayNameLen)
			}
			if name, err = cleanLine("display_name", name, 1, maxDisplayNameLen); err != nil {
				return err
			}
			group, err := cleanLine("group_name", c.GroupName, 0, maxGroupNameLen)
			if err != nil {
				return err
			}
			link, err := checkComponentURL(c.URL)
			if err != nil {
				return err
			}
			entries = append(entries, entry{c.ServiceID, name, group, link})
		}
		if row.Published && len(entries) == 0 {
			return apierr.Unprocessable("publish_needs_components", "a published page needs at least one component: unpublish it first")
		}
		if err := q.DeleteStatusPageComponents(ctx, id); err != nil {
			return fmt.Errorf("clear components: %w", err)
		}
		for i, e := range entries {
			if err := q.InsertStatusPageComponent(ctx, db.InsertStatusPageComponentParams{
				PageID: id, ServiceID: e.service, DisplayName: e.name, GroupName: e.group, Url: e.url, Position: int32(i),
			}); err != nil {
				return fmt.Errorf("insert component: %w", err)
			}
		}
		return audit.Record(ctx, tx, audit.Entry{
			Action: ActionPageComponents, ResourceType: resourcePage, ResourceID: id.String(), WorkspaceID: ws,
			Metadata: map[string]any{"title": row.Title, "components": len(entries)},
		})
	})
	if err != nil {
		return StatusPageView{}, fail("set components", err)
	}
	s.publish(ws, id)
	return s.pageView(ctx, s.q, p, row)
}

// prefillComponents gives a new page the components of a project: the
// services of its production environment that have at least one public domain
// (see ListPrefillServices), under their service names, ungrouped, in name
// order. It returns how many there are. The project must belong to the workspace.
func prefillComponents(ctx context.Context, q *db.Queries, ws, pageID, projectID uuid.UUID) (int, error) {
	ok, err := q.ProjectInWorkspace(ctx, db.ProjectInWorkspaceParams{ProjectID: projectID, WorkspaceID: ws})
	if err != nil {
		return 0, fmt.Errorf("check project: %w", err)
	}
	if !ok {
		return 0, apierr.Unprocessable("unknown_project", "the project does not exist in this workspace")
	}
	rows, err := q.ListPrefillServices(ctx, db.ListPrefillServicesParams{ProjectID: projectID, WorkspaceID: ws, RowLimit: MaxComponents})
	if err != nil {
		return 0, fmt.Errorf("list project services: %w", err)
	}
	for i, r := range rows {
		if err := q.InsertStatusPageComponent(ctx, db.InsertStatusPageComponentParams{
			PageID: pageID, ServiceID: r.ID, DisplayName: truncateRunes(strings.TrimSpace(r.Name), maxDisplayNameLen), GroupName: "", Url: "", Position: int32(i),
		}); err != nil {
			return 0, fmt.Errorf("insert component: %w", err)
		}
	}
	return len(rows), nil
}

func truncateRunes(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return strings.TrimSpace(string(r[:n]))
}

// DNS check ---------------------------------------------------------------

// DNS check results.
const (
	DNSOK       = "ok"       // the hostname resolves to the control-plane server and nowhere else
	DNSMismatch = "mismatch" // it resolves, but not (only) to the server
	DNSMissing  = "missing"  // no record yet
	DNSUnknown  = "unknown"  // the server has no public IP, or the lookup failed
)

// StatusPageDNSRecord is a record the hostname needs.
type StatusPageDNSRecord struct {
	Type  string `json:"type" enum:"A,AAAA"`
	Name  string `json:"name"`
	Value string `json:"value"`
}

// StatusPageDNSCheck reports the records a page's hostname needs and whether it
// resolves to the control-plane server now.
type StatusPageDNSCheck struct {
	Hostname string                `json:"hostname"`
	Records  []StatusPageDNSRecord `json:"records" doc:"The records to create: the control-plane server's public addresses"`
	Resolved []string              `json:"resolved" doc:"What the hostname resolves to right now"`
	Status   string                `json:"status" enum:"ok,mismatch,missing,unknown"`
	Message  string                `json:"message"`
	Proxied  bool                  `json:"proxied" doc:"The page's Cloudflare record is proxied and the hostname resolves to Cloudflare's addresses: status is ok then"`
	// CloudflareRecord is true when NebulaCtrl's last sync found the page's record in the connected Cloudflare zone.
	// NameChangedAt is the newer of the page's last edit (the hostname is set or changed there) and NebulaCtrl's last change to its Cloudflare record.
	NameChangedAt    time.Time `json:"nameChangedAt" doc:"When the hostname was set or its record last created or changed. The dashboard tells the user a new name can take up to 30 minutes to reach every DNS resolver for that long"`
	CloudflareRecord bool      `json:"cloudflareRecord" doc:"Cloudflare's API confirmed the page's record exists in the connected zone. The records to create by hand are not needed then, and a hostname that does not resolve yet is waiting on DNS caches"`
}

// cloudflareLagMessage is said when Cloudflare has the record and the lookup
// still does not show it.
const cloudflareLagMessage = "Cloudflare has the record. Some DNS caches can take up to 30 minutes to see a new name."

// parseIPs reads the addresses of a server's public_ip column (one address, or
// several separated by commas or spaces). Anything unparseable is skipped.
func parseIPs(s string) []net.IP {
	var out []net.IP
	for _, f := range strings.FieldsFunc(s, func(r rune) bool { return r == ',' || r == ' ' || r == ';' }) {
		if ip := net.ParseIP(strings.TrimSpace(f)); ip != nil {
			out = append(out, ip)
		}
	}
	return out
}

func ipStrings(ips []net.IP) []string {
	out := make([]string, 0, len(ips))
	for _, ip := range ips {
		out = append(out, ip.String())
	}
	sort.Strings(out)
	return slices.Compact(out)
}

// evaluateDNS compares what host resolves to (got, or lookupErr) with the
// addresses of the server (want).
func evaluateDNS(host string, want, got []net.IP, lookupErr error) (status, message string) {
	if len(want) == 0 {
		return DNSUnknown, "The control-plane server has no public IP set, so DNS cannot be checked. Point the hostname at the server's public IP."
	}
	wantS := ipStrings(want)
	var dnsErr *net.DNSError
	if lookupErr != nil {
		if errors.As(lookupErr, &dnsErr) && dnsErr.IsNotFound {
			return DNSMissing, "No A or AAAA record found for " + host + " yet."
		}
		return DNSUnknown, "The lookup of " + host + " failed; try again in a moment."
	}
	gotS := ipStrings(got)
	if len(gotS) == 0 {
		return DNSMissing, "No A or AAAA record found for " + host + " yet."
	}
	var missing, extra []string
	for _, w := range wantS {
		if !slices.Contains(gotS, w) {
			missing = append(missing, w)
		}
	}
	for _, g := range gotS {
		if !slices.Contains(wantS, g) {
			extra = append(extra, g)
		}
	}
	switch {
	case len(missing) == 0 && len(extra) == 0:
		return DNSOK, host + " points to this server."
	case len(missing) == 0:
		return DNSMismatch, host + " also resolves to " + strings.Join(extra, ", ") + ", which is not this server. Remove those records (a record proxied through a CDN shows its addresses here)."
	default:
		return DNSMismatch, host + " resolves to " + strings.Join(gotS, ", ") + ", not to " + strings.Join(wantS, ", ") + "."
	}
}

// evaluateProxied judges a hostname whose Cloudflare record is proxied: it
// should resolve to Cloudflare's addresses only, not to the server's (Cloudflare
// reaches the server from there). ok is false when it does not, and the plain
// comparison with the server's addresses stands.
func evaluateProxied(host string, got []net.IP, list cloudflare.IPList) (message string, ok bool) {
	if len(got) == 0 {
		return "", false
	}
	for _, ip := range got {
		a, valid := netip.AddrFromSlice(ip)
		if !valid || !list.Contains(a) {
			return "", false
		}
	}
	return host + " resolves through Cloudflare, which reaches this server.", true
}

// DNS reports the records the page's hostname needs (the A and AAAA records of
// the control-plane server's public address, as the dashboard's domain DNS
// guidance names them) and whether the hostname resolves to them now.
func (s *Service) DNS(ctx context.Context, id uuid.UUID) (StatusPageDNSCheck, error) {
	ws, _, err := s.reader(ctx)
	if err != nil {
		return StatusPageDNSCheck{}, err
	}
	row, err := s.q.GetStatusPage(ctx, db.GetStatusPageParams{ID: id, WorkspaceID: ws})
	if errors.Is(err, pgx.ErrNoRows) {
		return StatusPageDNSCheck{}, apierr.NotFound("status page")
	}
	if err != nil {
		return StatusPageDNSCheck{}, fmt.Errorf("get status page: %w", err)
	}
	if row.Hostname == nil {
		return StatusPageDNSCheck{}, apierr.Unprocessable("no_hostname", "the page has no hostname yet")
	}
	host := *row.Hostname
	var want []net.IP
	srv, err := s.q.GetControlPlaneServer(ctx)
	switch {
	case err == nil && srv.PublicIp != nil:
		want = parseIPs(*srv.PublicIp)
	case err != nil && !errors.Is(err, pgx.ErrNoRows):
		return StatusPageDNSCheck{}, fmt.Errorf("load control-plane server: %w", err)
	}
	out := StatusPageDNSCheck{Hostname: host, Records: []StatusPageDNSRecord{}, Resolved: []string{}}
	for _, ip := range want {
		typ := "AAAA"
		if ip.To4() != nil {
			typ = "A"
		}
		out.Records = append(out.Records, StatusPageDNSRecord{Type: typ, Name: host, Value: ip.String()})
	}
	lctx, cancel := context.WithTimeout(ctx, dnsLookupTimeout)
	defer cancel()
	addrs, lerr := s.resolver.LookupIPAddr(lctx, host)
	got := make([]net.IP, 0, len(addrs))
	for _, a := range addrs {
		got = append(got, a.IP)
	}
	out.Resolved = ipStrings(got)
	out.Status, out.Message = evaluateDNS(host, want, got, lerr)
	out.NameChangedAt = row.UpdatedAt
	changed, err := s.q.LatestCloudflareRecordChange(ctx, db.LatestCloudflareRecordChangeParams{OwnerKind: "status_page", OwnerID: id})
	if err != nil {
		return StatusPageDNSCheck{}, fmt.Errorf("load record change time: %w", err)
	}
	if len(changed) > 0 && changed[0].After(out.NameChangedAt) {
		out.NameChangedAt = changed[0]
	}
	out.CloudflareRecord = row.CfZoneID != "" && (row.CfStatus == "managed" || row.CfStatus == "present")
	if row.CfProxied && row.CfZoneID != "" && lerr == nil && len(want) > 0 {
		list, err := cloudflare.CurrentIPList(ctx, s.q)
		if err != nil {
			return StatusPageDNSCheck{}, fmt.Errorf("load cloudflare ip list: %w", err)
		}
		if msg, ok := evaluateProxied(host, got, list); ok {
			out.Status, out.Message, out.Proxied = DNSOK, msg, true
		}
	}
	if out.CloudflareRecord && (out.Status == DNSMissing || out.Status == DNSMismatch) {
		out.Message = cloudflareLagMessage
	}
	return out, nil
}
