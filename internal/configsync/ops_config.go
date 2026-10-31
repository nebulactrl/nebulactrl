package configsync

import (
	"sort"
	"strings"

	"github.com/google/uuid"

	"github.com/nebulactrl/nebulactrl/internal/projectconfig"
	"github.com/nebulactrl/nebulactrl/internal/services"
)

// cfgField reads and writes one atom of services.Config by field id. get returns the
// value in the form the file's value is compared in; set writes the file's value.
// Both run on defaulted configs: the desired value of a field is the live config with
// only that field set, defaulted again, then read back.
type cfgField struct {
	get func(pc *planCtx, c services.Config) any
	set func(pc *planCtx, c *services.Config, v any) error
}

// convert turns the Go value of a catalog field into the type of a Config field.
func convert[T any](v any) (T, bool) {
	var zero T
	switch any(zero).(type) {
	case int:
		n, ok := asInt(v)
		if !ok {
			return zero, false
		}
		return any(n).(T), true
	case string:
		s, ok := v.(string)
		if !ok {
			return zero, false
		}
		return any(s).(T), true
	case bool:
		b, ok := asBool(v)
		if !ok {
			return zero, false
		}
		return any(b).(T), true
	case float64:
		f, ok := asFloat(v)
		if !ok {
			return zero, false
		}
		return any(f).(T), true
	case []string:
		s, ok := asStrings(v)
		if !ok {
			return zero, false
		}
		return any(append([]string(nil), s...)).(T), true
	}
	return zero, false
}

// scalar builds a cfgField for a plain Config field.
func scalar[T any](ptr func(c *services.Config) *T) cfgField {
	return cfgField{
		get: func(_ *planCtx, c services.Config) any {
			v := *ptr(&c)
			if s, ok := any(v).([]string); ok {
				return nonNil(s)
			}
			return v
		},
		set: func(_ *planCtx, c *services.Config, v any) error {
			t, ok := convert[T](v)
			if !ok {
				return refErr("wrong_type", "unexpected value %v", v)
			}
			*ptr(c) = t
			return nil
		},
	}
}

// serverNameOf resolves a server id of a Config field to its lower-cased name ("" when unset).
func serverNameOf(pc *planCtx, id *uuid.UUID) string {
	if id == nil {
		return ""
	}
	if s, ok := pc.refs().serverByID(*id); ok {
		return strings.ToLower(s.Name)
	}
	return id.String()
}

var cfgFields = map[string]cfgField{
	"config.image":            scalar(func(c *services.Config) *string { return &c.Image }),
	"config.gitUrl":           scalar(func(c *services.Config) *string { return &c.GitURL }),
	"config.gitBranch":        scalar(func(c *services.Config) *string { return &c.GitBranch }),
	"config.submodules":       scalar(func(c *services.Config) *string { return &c.Submodules }),
	"config.dockerfilePath":   scalar(func(c *services.Config) *string { return &c.DockerfilePath }),
	"config.buildContext":     scalar(func(c *services.Config) *string { return &c.BuildContext }),
	"config.buildType":        scalar(func(c *services.Config) *string { return &c.BuildType }),
	"config.buildLocation":    scalar(func(c *services.Config) *string { return &c.BuildLocation }),
	"config.port":             scalar(func(c *services.Config) *int { return &c.Port }),
	"config.startCommand":     scalar(func(c *services.Config) *[]string { return &c.StartCommand }),
	"config.oneshot":          scalar(func(c *services.Config) *bool { return &c.OneShot }),
	"config.processName":      processNameField,
	"config.singleton":        scalar(func(c *services.Config) *bool { return &c.Singleton }),
	"config.stopGraceSeconds": scalar(func(c *services.Config) *int { return &c.StopGraceSeconds }),
	"config.memoryMb":         scalar(func(c *services.Config) *int { return &c.MemoryMB }),
	"config.cpus":             scalar(func(c *services.Config) *float64 { return &c.CPUs }),
	"config.imageRetention":   scalar(func(c *services.Config) *int { return &c.ImageRetention }),
	"config.blockEgress":      scalar(func(c *services.Config) *bool { return &c.BlockEgress }),

	"config.healthcheck.type":               scalar(func(c *services.Config) *string { return &c.Healthcheck.Type }),
	"config.healthcheck.path":               scalar(func(c *services.Config) *string { return &c.Healthcheck.Path }),
	"config.healthcheck.port":               scalar(func(c *services.Config) *int { return &c.Healthcheck.Port }),
	"config.healthcheck.intervalSeconds":    scalar(func(c *services.Config) *int { return &c.Healthcheck.IntervalSeconds }),
	"config.healthcheck.timeoutSeconds":     scalar(func(c *services.Config) *int { return &c.Healthcheck.TimeoutSeconds }),
	"config.healthcheck.startPeriodSeconds": scalar(func(c *services.Config) *int { return &c.Healthcheck.StartPeriodSeconds }),
	"config.healthcheck.deadlineSeconds":    scalar(func(c *services.Config) *int { return &c.Healthcheck.DeadlineSeconds }),
	"config.healthcheck.successThreshold":   scalar(func(c *services.Config) *int { return &c.Healthcheck.SuccessThreshold }),
	"config.healthcheck.command":            scalar(func(c *services.Config) *[]string { return &c.Healthcheck.Command }),

	"config.preDeployCommand":         scalar(func(c *services.Config) *[]string { return &c.PreDeployCommand }),
	"config.preDeployTimeoutSeconds":  scalar(func(c *services.Config) *int { return &c.PreDeployTimeoutSeconds }),
	"config.postDeployCommand":        scalar(func(c *services.Config) *[]string { return &c.PostDeployCommand }),
	"config.postDeployTimeoutSeconds": scalar(func(c *services.Config) *int { return &c.PostDeployTimeoutSeconds }),

	"config.buildServerId": {
		get: func(pc *planCtx, c services.Config) any { return serverNameOf(pc, c.BuildServerID) },
		set: func(pc *planCtx, c *services.Config, v any) error {
			name := asString(v)
			if name == "" {
				c.BuildServerID = nil
				return nil
			}
			srv, res := pc.refs().server(name)
			switch res {
			case refNone:
				return refErr("unknown_server", "no server named %q", name)
			case refAmbiguous:
				return refErr("ambiguous_reference", "several servers are named %q", name)
			}
			c.BuildServerID = &srv.ID
			return nil
		},
	},
	"config.registryId": {
		get: func(pc *planCtx, c services.Config) any {
			if c.RegistryID == nil {
				return ""
			}
			if r, ok := pc.refs().registryByID(*c.RegistryID); ok {
				return strings.ToLower(r.Name)
			}
			return c.RegistryID.String()
		},
		set: func(pc *planCtx, c *services.Config, v any) error {
			name := asString(v)
			if name == "" {
				c.RegistryID = nil
				return nil
			}
			r, res := pc.refs().registry(name)
			switch res {
			case refNone:
				return refErr("unknown_registry", "no registry named %q", name)
			case refAmbiguous:
				return refErr("ambiguous_reference", "several registries are named %q", name)
			}
			c.RegistryID = &r.ID
			return nil
		},
	},
	"config.files": {
		get: func(_ *planCtx, c services.Config) any { return filesValue(c.Files) },
		set: func(_ *planCtx, c *services.Config, v any) error {
			files, ok := v.([]projectconfig.FileMount)
			if !ok && v != nil {
				return refErr("wrong_type", "files have an unexpected value")
			}
			c.Files = nil
			for _, f := range files {
				c.Files = append(c.Files, services.FileMount{Path: f.Path, Mode: f.Mode, Content: f.Content})
			}
			return nil
		},
	},
}

// filesValue is the comparable form of a file set: sorted by path, the default mode written out.
func filesValue(files []services.FileMount) []projectconfig.FileMount {
	out := make([]projectconfig.FileMount, 0, len(files))
	for _, f := range files {
		mode := f.Mode
		if mode == "" {
			mode = "0644"
		}
		out = append(out, projectconfig.FileMount{Path: f.Path, Mode: mode, Content: f.Content})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Path < out[j].Path })
	return out
}

// configOps handles every config.* field through cfgFields and the kind-specific tables of ops_config_kinds.go.
type configOps struct{}

func lookupCfgField(id string) (cfgField, bool) {
	if f, ok := cfgFields[id]; ok {
		return f, true
	}
	return kindCfgField(id)
}

// baseConfig is the config a field is applied to: the live config, or the defaults of a new service.
func baseConfig(ls *liveService) services.Config {
	if ls != nil {
		return ls.Cfg
	}
	return services.Config{}.WithDefaults()
}

func (configOps) live(pc *planCtx, ls *liveService, id, _ string) (fieldView, bool) {
	f, ok := lookupCfgField(id)
	if !ok {
		return fieldView{}, false
	}
	if absentOnKind(id, ls.Cfg) {
		return fieldView{}, false
	}
	v := f.get(pc, ls.Cfg)
	return view(v, display(v)), true
}

func (configOps) desired(pc *planCtx, ls *liveService, fv projectconfig.FieldValue) (fieldView, error) {
	f, ok := lookupCfgField(fv.ID)
	if !ok {
		return fieldView{}, refErr("invalid_value", "field %s is not supported by this version", fv.ID)
	}
	cfg := baseConfig(ls)
	if err := f.set(pc, &cfg, fv.Value); err != nil {
		return fieldView{}, err
	}
	v := f.get(pc, cfg.WithDefaults())
	return view(v, display(v)), nil
}

// absentOnKind reports a kind-specific field the live config does not have at all
// (a route field of an image service).
func absentOnKind(id string, c services.Config) bool {
	switch {
	case strings.HasPrefix(id, "config.route."):
		return c.Route == nil
	case strings.HasPrefix(id, "config.stack."):
		return c.Stack == nil
	case strings.HasPrefix(id, "config.database."):
		return c.Database == nil
	case strings.HasPrefix(id, "config.processes."):
		return !hasProcess(c, strings.TrimPrefix(id, "config.processes."))
	}
	return false
}
