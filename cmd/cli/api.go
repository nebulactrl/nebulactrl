package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/nebulactrl/nebulactrl/internal/apiclient"
)

const requestTimeout = 30 * time.Second

// cli carries everything a command needs; tests construct it with their own
// streams.
type cli struct {
	in     io.Reader
	out    io.Writer
	errOut io.Writer

	asJSON  bool
	idemKey string // --idempotency-key; random per invocation when empty
	// insecure (--insecure) allows the token to travel over plain http to a
	// host that is not this machine.
	insecure bool

	pollEvery time.Duration // deployment poll interval for --wait

	api    *apiclient.ClientWithResponses // request/response calls, with a timeout
	stream *apiclient.Client              // long-lived SSE calls, no timeout
	cfg    config
}

// connect builds the generated clients from the stored config. Commands that
// talk to the API call it first.
func (c *cli) connect() error {
	if c.api != nil {
		return nil
	}
	cfg, err := loadConfig()
	if err != nil {
		return err
	}
	if cfg.URL == "" || cfg.Token == "" {
		return errors.New("not logged in: run `nebulactrl login --url <url> --token <token>`")
	}
	return c.dial(cfg)
}

func (c *cli) dial(cfg config) error {
	if err := checkTransport(cfg.URL, c.insecure); err != nil {
		return err
	}
	editor := apiclient.WithRequestEditorFn(func(_ context.Context, req *http.Request) error {
		req.Header.Set("Authorization", "Bearer "+cfg.Token)
		req.Header.Set("User-Agent", "nebulactrl-cli/"+version)
		return nil
	})
	base := apiBase(cfg.URL)
	api, err := apiclient.NewClientWithResponses(base, editor,
		apiclient.WithHTTPClient(&http.Client{Timeout: requestTimeout}))
	if err != nil {
		return err
	}
	stream, err := apiclient.NewClient(base, editor, apiclient.WithHTTPClient(&http.Client{}))
	if err != nil {
		return err
	}
	c.cfg, c.api, c.stream = cfg, api, stream
	return nil
}

// checkTransport refuses to send the API token over plain http to a host
// other than this machine: anyone on the path would read it. https is always
// allowed; --insecure opts in to http for a trusted network.
func checkTransport(rawURL string, insecure bool) error {
	u, err := url.ParseRequestURI(rawURL)
	if err != nil {
		return fmt.Errorf("invalid control plane URL %q: %w", rawURL, err)
	}
	switch u.Scheme {
	case "https":
		return nil
	case "http":
		if insecure || isLoopbackHost(u.Hostname()) {
			return nil
		}
		return fmt.Errorf("refusing to send the API token over plain http to %s: use an https:// URL, or pass --insecure if you trust the network between you and the control plane", u.Host)
	}
	return fmt.Errorf("invalid control plane URL %q: the scheme must be https (or http for this machine)", rawURL)
}

// isLoopbackHost reports whether host names this machine.
func isLoopbackHost(host string) bool {
	if host == "localhost" || strings.HasSuffix(host, ".localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// key returns the Idempotency-Key for one mutating call.
func (c *cli) key() *string {
	k := c.idemKey
	if k == "" {
		k = uuid.NewString()
	}
	return &k
}

// apiError is a decoded RFC 9457 problem response.
type apiError struct {
	Status int
	Code   string
	Title  string
	Detail string
	Fields []string // "location: message"; offending values are never included
}

func (e *apiError) Error() string {
	msg := e.Detail
	if msg == "" {
		msg = e.Title
	}
	if msg == "" {
		msg = http.StatusText(e.Status)
	}
	var b strings.Builder
	b.WriteString(msg)
	if e.Code != "" {
		fmt.Fprintf(&b, " (%s, HTTP %d)", e.Code, e.Status)
	} else {
		fmt.Fprintf(&b, " (HTTP %d)", e.Status)
	}
	for _, f := range e.Fields {
		b.WriteString("\n  - " + f)
	}
	if e.Code == "backup_replica_unsupported" {
		b.WriteString("\n  hint: backups run on the primary; `nebulactrl databases replicas ls <database>` shows which service that is")
	}
	return b.String()
}

// problem decodes an error response body. The `value` of validation details
// is dropped on purpose: it can echo a secret the user just submitted.
func problem(status int, body []byte) *apiError {
	e := &apiError{Status: status}
	var p apiclient.Error
	if json.Unmarshal(body, &p) == nil && p.Code != "" {
		e.Code = p.Code
		if p.Title != nil {
			e.Title = *p.Title
		}
		if p.Detail != nil {
			e.Detail = *p.Detail
		}
		if p.Errors != nil {
			for _, d := range *p.Errors {
				var loc, msg string
				if d.Location != nil {
					loc = *d.Location
				}
				if d.Message != nil {
					msg = *d.Message
				}
				if loc != "" || msg != "" {
					e.Fields = append(e.Fields, strings.TrimPrefix(loc+": "+msg, ": "))
				}
			}
		}
		if p.Code == "approval_required" {
			// A token needs a person's approval on production: say where.
			var raw struct {
				Details struct {
					ApprovalURL string `json:"approvalUrl"`
				} `json:"details"`
			}
			if json.Unmarshal(body, &raw) == nil && raw.Details.ApprovalURL != "" {
				e.Fields = append(e.Fields, "a person approves it at "+raw.Details.ApprovalURL+" (signed in to the dashboard); then run the same command again")
			}
		}
		return e
	}
	e.Title = http.StatusText(status)
	return e
}

// response is the common surface of every generated *XResponse.
type response interface {
	StatusCode() int
	GetBody() []byte
}

// check turns a transport error or a non-2xx response into an error. r is
// only touched when err is nil.
func check[R response](r R, err error) error {
	if err != nil {
		return fmt.Errorf("request failed: %w", err)
	}
	sc := r.StatusCode()
	if sc == 0 {
		return errors.New("request failed: no response")
	}
	if sc >= 200 && sc < 300 {
		return nil
	}
	return problem(sc, r.GetBody())
}

// checkRaw is check for raw *http.Response values (streaming calls); a failed
// response is consumed and closed.
func checkRaw(hr *http.Response, err error) error {
	if err != nil {
		return fmt.Errorf("request failed: %w", err)
	}
	if hr.StatusCode >= 200 && hr.StatusCode < 300 {
		return nil
	}
	defer hr.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(hr.Body, 1<<20))
	return problem(hr.StatusCode, body)
}

func (c *cli) ctx(parent context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(parent, requestTimeout)
}

// page is one page of a cursor-paginated list.
type page[T any] struct {
	items *[]T
	next  *string
}

// listAll follows nextCursor until the list is exhausted.
func listAll[T any](fetch func(cursor *string) (page[T], error)) ([]T, error) {
	all := []T{}
	var cursor *string
	for {
		p, err := fetch(cursor)
		if err != nil {
			return nil, err
		}
		if p.items != nil {
			all = append(all, *p.items...)
		}
		if p.next == nil || *p.next == "" {
			return all, nil
		}
		cursor = p.next
	}
}

func pageLimit() *int64 { n := int64(200); return &n }

func (c *cli) projects(ctx context.Context) ([]apiclient.Project, error) {
	return listAll(func(cur *string) (page[apiclient.Project], error) {
		r, err := c.api.ListProjectsWithResponse(ctx, &apiclient.ListProjectsParams{Cursor: cur, Limit: pageLimit()})
		if err := check(r, err); err != nil {
			return page[apiclient.Project]{}, err
		}
		return page[apiclient.Project]{r.JSON200.Items, r.JSON200.NextCursor}, nil
	})
}

func (c *cli) environments(ctx context.Context, projectID string) ([]apiclient.Environment, error) {
	r, err := c.api.ListEnvironmentsWithResponse(ctx, projectID)
	if err := check(r, err); err != nil {
		return nil, err
	}
	if r.JSON200.Items == nil {
		return []apiclient.Environment{}, nil
	}
	return *r.JSON200.Items, nil
}

func (c *cli) servicesOf(ctx context.Context, envID string) ([]apiclient.ServiceView, error) {
	return listAll(func(cur *string) (page[apiclient.ServiceView], error) {
		r, err := c.api.ListServicesWithResponse(ctx, envID, &apiclient.ListServicesParams{Cursor: cur, Limit: pageLimit()})
		if err := check(r, err); err != nil {
			return page[apiclient.ServiceView]{}, err
		}
		return page[apiclient.ServiceView]{r.JSON200.Items, r.JSON200.NextCursor}, nil
	})
}

func (c *cli) servers(ctx context.Context) ([]apiclient.Server, error) {
	return listAll(func(cur *string) (page[apiclient.Server], error) {
		r, err := c.api.ListServersWithResponse(ctx, &apiclient.ListServersParams{Cursor: cur, Limit: pageLimit()})
		if err := check(r, err); err != nil {
			return page[apiclient.Server]{}, err
		}
		return page[apiclient.Server]{r.JSON200.Items, r.JSON200.NextCursor}, nil
	})
}

func isUUID(s string) bool {
	_, err := uuid.Parse(s)
	return err == nil
}

func matchFold(a, b string) bool { return strings.EqualFold(a, b) }

// resolveProject accepts an ID, slug or name.
func (c *cli) resolveProject(ctx context.Context, ref string) (apiclient.Project, error) {
	all, err := c.projects(ctx)
	if err != nil {
		return apiclient.Project{}, err
	}
	for _, p := range all {
		if p.Id == ref {
			return p, nil
		}
	}
	for _, p := range all {
		if matchFold(p.Slug, ref) || matchFold(p.Name, ref) {
			return p, nil
		}
	}
	return apiclient.Project{}, fmt.Errorf("project %q not found", ref)
}

// resolveEnv accepts an environment ID, slug or name within a project.
func (c *cli) resolveEnv(ctx context.Context, projectID, ref string) (apiclient.Environment, error) {
	all, err := c.environments(ctx, projectID)
	if err != nil {
		return apiclient.Environment{}, err
	}
	for _, e := range all {
		if e.Id == ref || matchFold(e.Slug, ref) || matchFold(e.Name, ref) {
			return e, nil
		}
	}
	return apiclient.Environment{}, fmt.Errorf("environment %q not found in this project", ref)
}

// resolveServer accepts a server ID or name.
func (c *cli) resolveServer(ctx context.Context, ref string) (apiclient.Server, error) {
	all, err := c.servers(ctx)
	if err != nil {
		return apiclient.Server{}, err
	}
	for _, s := range all {
		if s.Id == ref {
			return s, nil
		}
	}
	for _, s := range all {
		if matchFold(s.Name, ref) {
			return s, nil
		}
	}
	return apiclient.Server{}, fmt.Errorf("server %q not found", ref)
}

// serviceID resolves `<id>` or `project/env/slug` to a service ID.
func (c *cli) serviceID(ctx context.Context, ref string) (string, error) {
	if isUUID(ref) {
		return ref, nil
	}
	parts := strings.Split(ref, "/")
	if len(parts) != 3 || parts[0] == "" || parts[1] == "" || parts[2] == "" {
		return "", fmt.Errorf("service %q: use a service ID or project/env/service-slug", ref)
	}
	p, err := c.resolveProject(ctx, parts[0])
	if err != nil {
		return "", err
	}
	e, err := c.resolveEnv(ctx, p.Id, parts[1])
	if err != nil {
		return "", err
	}
	svcs, err := c.servicesOf(ctx, e.Id)
	if err != nil {
		return "", err
	}
	for _, s := range svcs {
		if matchFold(s.Slug, parts[2]) {
			return s.Id, nil
		}
	}
	return "", fmt.Errorf("service %q not found in %s/%s", parts[2], p.Slug, e.Slug)
}
