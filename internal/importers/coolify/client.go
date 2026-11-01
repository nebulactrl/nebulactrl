package coolify

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strings"
	"syscall"
	"time"

	"github.com/nebulactrl/nebulactrl/internal/apierr"
)

// Limits of one Coolify read.
const (
	requestTimeout = 30 * time.Second
	maxBody        = 16 << 20
)

// Client reads the Coolify v4 REST API (https://coolify.io/docs/api-reference).
// The token lives only in this struct: it is sent as a bearer token, never
// logged, and never part of an error message.
type Client struct {
	base  *url.URL
	token string
	hc    *http.Client
}

// StatusError is a non-2xx answer. It carries the status and the endpoint, never the body.
type StatusError struct {
	Status int
	Path   string
}

func (e *StatusError) Error() string {
	return fmt.Sprintf("Coolify answered %d for %s", e.Status, e.Path)
}

// NewClient validates the instance URL and returns a client. rawURL is the
// instance address (https://coolify.example.com); a trailing /api/v1 is accepted.
func NewClient(rawURL, token string, hc *http.Client) (*Client, error) {
	bad := func(msg string) error { return apierr.Unprocessable("invalid_coolify_url", msg) }
	rawURL = strings.TrimSpace(rawURL)
	if rawURL == "" {
		return nil, bad("the Coolify URL is required")
	}
	if !strings.Contains(rawURL, "://") {
		rawURL = "https://" + rawURL
	}
	u, err := url.Parse(rawURL)
	if err != nil || u.Host == "" {
		return nil, bad("the Coolify URL is not a valid address")
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return nil, bad("the Coolify URL must start with http:// or https://")
	}
	if u.User != nil {
		return nil, bad("put the token in the token field, not in the URL")
	}
	u.RawQuery, u.Fragment = "", ""
	p := strings.TrimRight(u.Path, "/")
	p = strings.TrimSuffix(p, "/v1")
	p = strings.TrimSuffix(p, "/api")
	u.Path = strings.TrimRight(p, "/")
	if strings.TrimSpace(token) == "" {
		return nil, apierr.Unprocessable("coolify_token_required", "a Coolify API token is required")
	}
	if hc == nil {
		hc = safeHTTPClient()
	}
	return &Client{base: u, token: strings.TrimSpace(token), hc: hc}, nil
}

// safeHTTPClient never follows redirects (the token must not leave the host it
// was given for) and refuses link-local and unspecified addresses, which cover
// cloud metadata services. Private and loopback addresses are allowed: a
// Coolify instance usually lives on a LAN or tailnet.
func safeHTTPClient() *http.Client {
	dialer := &net.Dialer{
		Timeout: 10 * time.Second,
		Control: func(_, address string, _ syscall.RawConn) error {
			host, _, err := net.SplitHostPort(address)
			if err != nil {
				return err
			}
			ip, err := netip.ParseAddr(host)
			if err != nil {
				return err
			}
			ip = ip.Unmap()
			if ip.IsUnspecified() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() || ip.IsMulticast() {
				return errors.New("address not allowed")
			}
			return nil
		},
	}
	return &http.Client{
		Timeout:       requestTimeout,
		Transport:     &http.Transport{DialContext: dialer.DialContext, TLSHandshakeTimeout: 10 * time.Second, ResponseHeaderTimeout: requestTimeout},
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
}

func (c *Client) do(ctx context.Context, path string) ([]byte, error) {
	u := *c.base
	u.Path = c.base.Path + "/api/v1" + path
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	req.Header.Set("Accept", "application/json")
	resp, err := c.hc.Do(req)
	if err != nil {
		// url.Error would repeat the URL (harmless) but keep the message short and token-free.
		return nil, fmt.Errorf("cannot reach Coolify at %s: %s", c.base.Host, cleanNetErr(err))
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxBody+1))
	if err != nil {
		return nil, fmt.Errorf("read Coolify answer for %s: %s", path, cleanNetErr(err))
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return nil, &StatusError{Status: resp.StatusCode, Path: path}
	}
	if len(body) > maxBody {
		return nil, fmt.Errorf("Coolify answer for %s is larger than %d MiB", path, maxBody>>20)
	}
	return body, nil
}

func cleanNetErr(err error) string {
	var ue *url.Error
	if errors.As(err, &ue) {
		err = ue.Err
	}
	return err.Error()
}

func (c *Client) getObj(ctx context.Context, path string) (obj, error) {
	b, err := c.do(ctx, path)
	if err != nil {
		return nil, err
	}
	var o obj
	if err := json.Unmarshal(b, &o); err != nil {
		return nil, fmt.Errorf("unexpected answer from Coolify for %s", path)
	}
	return o, nil
}

func (c *Client) getList(ctx context.Context, path string) ([]obj, error) {
	b, err := c.do(ctx, path)
	if err != nil {
		return nil, err
	}
	var arr []map[string]any
	if err := json.Unmarshal(b, &arr); err != nil {
		return nil, fmt.Errorf("unexpected answer from Coolify for %s", path)
	}
	out := make([]obj, len(arr))
	for i, m := range arr {
		out[i] = obj(m)
	}
	return out, nil
}

// authFailed reports a 401/403.
func authFailed(err error) bool {
	var se *StatusError
	return errors.As(err, &se) && (se.Status == http.StatusUnauthorized || se.Status == http.StatusForbidden)
}

// Fetch reads projects, environments, applications (with variables, storages
// and scheduled tasks), databases, one-click services and servers. Only the
// projects list is required to succeed; everything else degrades to a note.
func (c *Client) Fetch(ctx context.Context) (*Inventory, error) {
	inv := &Inventory{}
	if b, err := c.do(ctx, "/version"); err == nil {
		inv.Version = strings.Trim(strings.TrimSpace(string(b)), `"`)
	}

	projects, err := c.getList(ctx, "/projects")
	if err != nil {
		return nil, mapFetchErr(err)
	}
	for _, po := range projects {
		p := parseProject(po)
		if p.UUID == "" {
			continue
		}
		if envs, err := c.getList(ctx, "/projects/"+url.PathEscape(p.UUID)+"/environments"); err == nil {
			p.Environments = nil
			for _, e := range envs {
				p.Environments = append(p.Environments, parseEnvironment(e, p.ID))
			}
		} else if len(p.Environments) == 0 {
			inv.Notes = append(inv.Notes, fmt.Sprintf("The environments of project %q could not be read (%s).", p.Name, errShort(err)))
		}
		inv.Projects = append(inv.Projects, p)
	}

	apps, err := c.getList(ctx, "/applications")
	if err != nil {
		return nil, mapFetchErr(err)
	}
	for _, ao := range apps {
		uuid := ao.str("uuid")
		if uuid == "" {
			continue
		}
		if detail, err := c.getObj(ctx, "/applications/"+url.PathEscape(uuid)); err == nil {
			ao = detail
		}
		a := parseApplication(ao)
		if envs, err := c.getList(ctx, "/applications/"+url.PathEscape(uuid)+"/envs"); err == nil {
			for _, e := range envs {
				a.Envs = append(a.Envs, parseEnv(e))
			}
		} else {
			inv.Notes = append(inv.Notes, fmt.Sprintf("The variables of %q could not be read (%s).", a.Name, errShort(err)))
		}
		if st, err := c.getObj(ctx, "/applications/"+url.PathEscape(uuid)+"/storages"); err == nil {
			for _, v := range st.list("persistent_storages") {
				a.Volumes = append(a.Volumes, parseVolume(v))
			}
			for _, f := range st.list("file_storages") {
				a.Files = append(a.Files, FileMount{MountPath: f.str("mount_path"), IsDirectory: f.bool("is_directory")})
			}
		}
		if ts, err := c.getList(ctx, "/applications/"+url.PathEscape(uuid)+"/scheduled-tasks"); err == nil {
			for _, t := range ts {
				a.Tasks = append(a.Tasks, Task{Name: t.str("name"), Frequency: t.str("frequency"), Enabled: t.bool("enabled")})
			}
		}
		inv.Applications = append(inv.Applications, a)
	}

	dbs, err := c.getList(ctx, "/databases")
	if err != nil {
		inv.Notes = append(inv.Notes, "The databases could not be read ("+errShort(err)+").")
	}
	for _, dobj := range dbs {
		uuid := dobj.str("uuid")
		if uuid == "" {
			continue
		}
		if detail, err := c.getObj(ctx, "/databases/"+url.PathEscape(uuid)); err == nil {
			dobj = detail
		}
		d := parseDatabase(dobj)
		if st, err := c.getObj(ctx, "/databases/"+url.PathEscape(uuid)+"/storages"); err == nil {
			for _, v := range st.list("persistent_storages") {
				d.Volumes = append(d.Volumes, parseVolume(v))
			}
		}
		inv.Databases = append(inv.Databases, d)
	}

	if svcs, err := c.getList(ctx, "/services"); err == nil {
		for _, so := range svcs {
			inv.Services = append(inv.Services, OneClick{
				UUID: so.str("uuid"), Name: so.str("name"), Type: so.str("service_type"), EnvironmentID: so.int("environment_id"),
				HasCompose: strings.TrimSpace(so.str("docker_compose_raw")) != "",
			})
		}
	} else {
		inv.Notes = append(inv.Notes, "The one-click services could not be read ("+errShort(err)+").")
	}

	if srvs, err := c.getList(ctx, "/servers"); err == nil {
		for _, so := range srvs {
			s := Server{UUID: so.str("uuid"), Name: so.str("name"), IP: so.str("ip")}
			inv.Servers = append(inv.Servers, s)
			if s.UUID == "" {
				continue
			}
			if rr, err := c.getObj(ctx, "/servers/"+url.PathEscape(s.UUID)+"/registries"); err == nil {
				for _, r := range rr.list("registries") {
					if !r.bool("logged_in") {
						continue
					}
					l := RegistryLogin{Host: r.str("registry"), Username: r.str("username")}
					for _, u := range r.list("used_by") {
						if n := u.str("name"); n != "" {
							l.UsedBy = append(l.UsedBy, n)
						}
					}
					inv.Registries = append(inv.Registries, l)
				}
			}
		}
	}
	return inv, nil
}

func errShort(err error) string {
	var se *StatusError
	if errors.As(err, &se) {
		return fmt.Sprintf("HTTP %d", se.Status)
	}
	return "request failed"
}

func mapFetchErr(err error) error {
	var se *StatusError
	switch {
	case authFailed(err):
		return apierr.Unprocessable("coolify_unauthorized", "Coolify refused the token. Create an API token with the read and read:sensitive abilities (Keys & Tokens → API tokens), and make sure API access is enabled on the instance.")
	case errors.As(err, &se) && se.Status == http.StatusNotFound:
		return apierr.Unprocessable("coolify_not_found", "that address does not look like a Coolify v4 instance (/api/v1 was not found)")
	}
	return apierr.Unprocessable("coolify_unreachable", err.Error())
}
