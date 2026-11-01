package configsync

import (
	"crypto/sha1" //nolint:gosec // UUIDv5 is defined over SHA-1
	"reflect"
	"strings"

	"github.com/google/uuid"

	"github.com/nebulactrl/nebulactrl/internal/databases/engines"
	"github.com/nebulactrl/nebulactrl/internal/projectconfig"
	"github.com/nebulactrl/nebulactrl/internal/services"
	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

// kindCfgField resolves the dynamic config ids: docker options, route, stack and database keys,
// and the processes of a service.
func kindCfgField(id string) (cfgField, bool) {
	switch {
	case strings.HasPrefix(id, "config.processes."):
		return processField(strings.TrimPrefix(id, "config.processes.")), true
	case strings.HasPrefix(id, "config.docker."):
		return dockerField(strings.TrimPrefix(id, "config.docker."))
	case strings.HasPrefix(id, "config.route."):
		return routeField(strings.TrimPrefix(id, "config.route."))
	case strings.HasPrefix(id, "config.stack.resources."):
		return stackResourceField(strings.TrimPrefix(id, "config.stack.resources."))
	case id == "config.stack.compose":
		return cfgField{
			get: func(_ *planCtx, c services.Config) any {
				if c.Stack == nil {
					return ""
				}
				return c.Stack.Compose
			},
			set: func(_ *planCtx, c *services.Config, v any) error {
				s, ok := v.(string)
				if !ok {
					return refErr("wrong_type", "compose must be text")
				}
				st := stackOf(c)
				st.Compose = s
				return nil
			},
		}, true
	case id == "config.stack.waitSeconds":
		return cfgField{
			get: func(_ *planCtx, c services.Config) any {
				if c.Stack == nil {
					return 0
				}
				return c.Stack.WaitSeconds
			},
			set: func(_ *planCtx, c *services.Config, v any) error {
				n, ok := asInt(v)
				if !ok {
					return refErr("wrong_type", "wait_seconds must be a number")
				}
				stackOf(c).WaitSeconds = n
				return nil
			},
		}, true
	case strings.HasPrefix(id, "config.database."):
		return databaseField(strings.TrimPrefix(id, "config.database."))
	}
	return cfgField{}, false
}

// stackOf returns the (created on demand) stack config of c.
func stackOf(c *services.Config) *services.StackConfig {
	if c.Stack == nil {
		c.Stack = &services.StackConfig{}
	} else {
		cp := *c.Stack
		c.Stack = &cp
	}
	return c.Stack
}

func stackResourceField(component string) (cfgField, bool) {
	return cfgField{
		get: func(_ *planCtx, c services.Config) any {
			if c.Stack == nil {
				return projectconfig.StackResourceValue{Component: component}
			}
			r := c.Stack.Resources[component]
			return projectconfig.StackResourceValue{Component: component, MemoryMB: r.MemoryMB, CPUs: r.CPUs}
		},
		set: func(_ *planCtx, c *services.Config, v any) error {
			r, ok := v.(projectconfig.StackResourceValue)
			if !ok {
				return refErr("wrong_type", "resources of %s have an unexpected value", component)
			}
			st := stackOf(c)
			res := make(map[string]agentprotocol.StackResources, len(st.Resources)+1)
			for k, x := range st.Resources {
				res[k] = x
			}
			res[component] = agentprotocol.StackResources{MemoryMB: r.MemoryMB, CPUs: r.CPUs}
			st.Resources = res
			return nil
		},
	}, true
}

func databaseField(name string) (cfgField, bool) {
	ptr := func(s *engines.Spec) *string {
		switch name {
		case "engine":
			return &s.Engine
		case "version":
			return &s.Version
		case "flavor":
			return &s.Flavor
		case "databaseName":
			return &s.DatabaseName
		case "username":
			return &s.Username
		}
		return nil
	}
	if ptr(&engines.Spec{}) == nil {
		return cfgField{}, false
	}
	return cfgField{
		get: func(_ *planCtx, c services.Config) any {
			if c.Database == nil {
				return ""
			}
			s := *c.Database
			return *ptr(&s)
		},
		set: func(_ *planCtx, c *services.Config, v any) error {
			str, ok := v.(string)
			if !ok {
				return refErr("wrong_type", "%s must be text", name)
			}
			var s engines.Spec
			if c.Database != nil {
				s = *c.Database
			}
			*ptr(&s) = str
			c.Database = &s
			return nil
		},
	}, true
}

// dockerField reads and writes one DockerOptions field by its JSON name through reflection: the
// value types of the catalog are the platform's own, so the assignment is direct.
func dockerField(jsonName string) (cfgField, bool) {
	t := reflect.TypeOf(agentprotocol.DockerOptions{})
	idx := -1
	for i := range t.NumField() {
		if strings.Split(t.Field(i).Tag.Get("json"), ",")[0] == jsonName {
			idx = i
		}
	}
	if idx < 0 {
		return cfgField{}, false
	}
	return cfgField{
		get: func(_ *planCtx, c services.Config) any {
			opts := c.Docker.Normalized()
			if opts == nil {
				opts = &agentprotocol.DockerOptions{}
			}
			f := reflect.ValueOf(*opts).Field(idx)
			if f.Kind() == reflect.Pointer {
				if f.IsNil() {
					return agentprotocol.GPURequest{}
				}
				return f.Elem().Interface()
			}
			return f.Interface()
		},
		set: func(_ *planCtx, c *services.Config, v any) error {
			var opts agentprotocol.DockerOptions
			if c.Docker != nil {
				opts = *c.Docker
			}
			dst := reflect.ValueOf(&opts).Elem().Field(idx)
			src := reflect.ValueOf(v)
			if !src.IsValid() {
				dst.Set(reflect.Zero(dst.Type()))
			} else if dst.Kind() == reflect.Pointer {
				if src.IsZero() {
					dst.Set(reflect.Zero(dst.Type()))
				} else {
					p := reflect.New(dst.Type().Elem())
					p.Elem().Set(src)
					dst.Set(p)
				}
			} else if src.Type().AssignableTo(dst.Type()) {
				dst.Set(src)
			} else if isIntKind(src.Kind()) && isIntKind(dst.Kind()) {
				dst.Set(src.Convert(dst.Type()))
			} else {
				return refErr("wrong_type", "docker.%s has an unexpected value", jsonName)
			}
			c.Docker = &opts
			return nil
		},
	}, true
}

// slugNamespace derives the placeholder id of a service that does not exist yet.
var slugNamespace = uuid.MustParse("6ba7b810-9dad-11d1-80b4-00c04fd430c8")

// placeholderID is the id a not-yet-created service gets while a plan compares route upstreams.
func placeholderID(slug string) uuid.UUID {
	return uuid.NewHash(sha1.New(), slugNamespace, []byte("nebula.toml/"+slug), 5) //nolint:gosec // UUIDv5 is defined over SHA-1
}

// slugID resolves a service slug to its id (a placeholder while the service is only planned).
func (pc *planCtx) slugID(slug string) uuid.UUID {
	if pc.createdID != nil {
		if id, ok := pc.createdID(slug); ok {
			return id
		}
	}
	if ls, ok := pc.live.Services[slug]; ok {
		return ls.Row.ID
	}
	return placeholderID(slug)
}

// slugOf is the inverse of slugID.
func (pc *planCtx) slugOf(id uuid.UUID) string {
	for slug, ls := range pc.live.Services {
		if ls.Row.ID == id {
			return slug
		}
	}
	if pc.res != nil {
		for slug := range pc.res.Services {
			if placeholderID(slug) == id {
				return slug
			}
			if pc.createdID != nil {
				if cid, ok := pc.createdID(slug); ok && cid == id {
					return slug
				}
			}
		}
	}
	return id.String()
}

func routeField(name string) (cfgField, bool) {
	route := func(c *services.Config) *services.RouteConfig {
		if c.Route == nil {
			c.Route = &services.RouteConfig{}
		} else {
			cp := *c.Route
			c.Route = &cp
		}
		return c.Route
	}
	get := func(f func(r services.RouteConfig, pc *planCtx) any) func(*planCtx, services.Config) any {
		return func(pc *planCtx, c services.Config) any {
			if c.Route == nil {
				return f(services.RouteConfig{}, pc)
			}
			return f(*c.Route, pc)
		}
	}
	str := func(p func(r *services.RouteConfig) *string) cfgField {
		return cfgField{
			get: get(func(r services.RouteConfig, _ *planCtx) any { return *p(&r) }),
			set: func(_ *planCtx, c *services.Config, v any) error {
				s, ok := v.(string)
				if !ok {
					return refErr("wrong_type", "route.%s must be text", name)
				}
				*p(route(c)) = s
				return nil
			},
		}
	}
	boolean := func(p func(r *services.RouteConfig) *bool) cfgField {
		return cfgField{
			get: get(func(r services.RouteConfig, _ *planCtx) any { return *p(&r) }),
			set: func(_ *planCtx, c *services.Config, v any) error {
				b, ok := asBool(v)
				if !ok {
					return refErr("wrong_type", "route.%s must be true or false", name)
				}
				*p(route(c)) = b
				return nil
			},
		}
	}
	switch name {
	case "protocol":
		return str(func(r *services.RouteConfig) *string { return &r.Protocol }), true
	case "scheme":
		f := str(func(r *services.RouteConfig) *string { return &r.Scheme })
		// An empty scheme is the default http: both write the same route.
		f.get = get(func(r services.RouteConfig, _ *planCtx) any {
			if r.Scheme == "" {
				return "http"
			}
			return r.Scheme
		})
		return f, true
	case "serverName":
		return str(func(r *services.RouteConfig) *string { return &r.ServerName }), true
	case "healthPath":
		return str(func(r *services.RouteConfig) *string { return &r.HealthPath }), true
	case "insecureSkipVerify":
		return boolean(func(r *services.RouteConfig) *bool { return &r.InsecureSkipVerify }), true
	case "verifySkip":
		return boolean(func(r *services.RouteConfig) *bool { return &r.VerifySkip }), true
	case "openToInternet":
		return boolean(func(r *services.RouteConfig) *bool { return &r.OpenToInternet }), true
	case "passHost":
		return cfgField{
			get: get(func(r services.RouteConfig, _ *planCtx) any { return r.PassHost == nil || *r.PassHost }),
			set: func(_ *planCtx, c *services.Config, v any) error {
				b, ok := asBool(v)
				if !ok {
					return refErr("wrong_type", "route.pass_host must be true or false")
				}
				route(c).PassHost = &b
				return nil
			},
		}, true
	case "listenPort", "proxyProtocol":
		return cfgField{
			get: get(func(r services.RouteConfig, _ *planCtx) any {
				if name == "listenPort" {
					return r.ListenPort
				}
				return r.ProxyProtocol
			}),
			set: func(_ *planCtx, c *services.Config, v any) error {
				n, ok := asInt(v)
				if !ok {
					return refErr("wrong_type", "route.%s must be a number", name)
				}
				if name == "listenPort" {
					route(c).ListenPort = n
				} else {
					route(c).ProxyProtocol = n
				}
				return nil
			},
		}, true
	case "allowCidrs", "denyCidrs":
		return cfgField{
			get: get(func(r services.RouteConfig, _ *planCtx) any {
				if name == "allowCidrs" {
					return nonNil(r.AllowCIDRs)
				}
				return nonNil(r.DenyCIDRs)
			}),
			set: func(_ *planCtx, c *services.Config, v any) error {
				l, ok := asStrings(v)
				if !ok {
					return refErr("wrong_type", "route.%s must be a list of text", name)
				}
				if name == "allowCidrs" {
					route(c).AllowCIDRs = append([]string(nil), l...)
				} else {
					route(c).DenyCIDRs = append([]string(nil), l...)
				}
				return nil
			},
		}, true
	case "upstreams":
		return cfgField{
			get: get(func(r services.RouteConfig, pc *planCtx) any {
				out := make([]projectconfig.UpstreamValue, 0, len(r.Upstreams))
				for _, u := range r.Upstreams {
					if u.ServiceID != nil {
						out = append(out, projectconfig.UpstreamValue{Service: pc.slugOf(*u.ServiceID), Port: u.Port})
					} else {
						out = append(out, projectconfig.UpstreamValue{Address: strings.TrimSpace(u.Address)})
					}
				}
				return out
			}),
			set: func(pc *planCtx, c *services.Config, v any) error {
				ups, ok := v.([]projectconfig.UpstreamValue)
				if !ok {
					return refErr("wrong_type", "route.upstreams have an unexpected value")
				}
				r := route(c)
				var next []services.RouteUpstream
				for _, u := range ups {
					if u.Service != "" {
						if _, live := pc.live.Services[u.Service]; !live {
							if _, inFile := pc.res.Services[u.Service]; !inFile {
								return refErr("invalid_upstream", "upstream service %q is not a service of this environment", u.Service)
							}
						}
						id := pc.slugID(u.Service)
						next = append(next, services.RouteUpstream{ServiceID: &id, Port: u.Port})
					} else {
						next = append(next, services.RouteUpstream{Address: strings.TrimSpace(u.Address)})
					}
				}
				r.Upstreams = next
				return nil
			},
		}, true
	}
	return cfgField{}, false
}

func isIntKind(k reflect.Kind) bool {
	switch k {
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		return true
	}
	return false
}
