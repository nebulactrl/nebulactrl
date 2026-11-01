package configsync

import (
	"slices"
	"strconv"
	"strings"

	"github.com/nebulactrl/nebulactrl/internal/projectconfig"
	"github.com/nebulactrl/nebulactrl/internal/services"
)

// processValueOf is the comparable form of the named process of a defaulted config: the
// shape the file's value is normalised to. A process the config does not have reads as
// the empty value; configOps.live reports it absent.
func processValueOf(c services.Config, name string) projectconfig.ProcessValue {
	for _, p := range c.Processes {
		if p.Name != name {
			continue
		}
		h := p.Healthcheck
		return projectconfig.ProcessValue{
			Name: p.Name, Command: nonNil(p.StartCommand), Port: p.Port, StopGraceSeconds: p.StopGraceSeconds,
			Singleton: p.Singleton, MemoryMB: p.MemoryMB, CPUs: p.CPUs,
			HealthCheck: projectconfig.ProcessHealth{
				Type: h.Type, Path: h.Path, Port: h.Port, IntervalSeconds: h.IntervalSeconds, TimeoutSeconds: h.TimeoutSeconds,
				StartPeriodSeconds: h.StartPeriodSeconds, DeadlineSeconds: h.DeadlineSeconds, SuccessThreshold: h.SuccessThreshold,
				Command: slices.Clone(h.Command),
			},
		}
	}
	return projectconfig.ProcessValue{Name: name, Command: []string{}}
}

// hasProcess reports whether the config has the named process.
func hasProcess(c services.Config, name string) bool {
	return slices.ContainsFunc(c.Processes, func(p services.ProcessConfig) bool { return p.Name == name })
}

// processField is the cfgField of config.processes.<name>: the whole process is one unit,
// set adds or replaces it and the config's defaults then fill what the file leaves out.
func processField(name string) cfgField {
	return cfgField{
		get: func(_ *planCtx, c services.Config) any { return processValueOf(c, name) },
		set: func(_ *planCtx, c *services.Config, v any) error {
			pv, ok := v.(projectconfig.ProcessValue)
			if !ok {
				return refErr("wrong_type", "process %s has an unexpected value", name)
			}
			p := pv.Config()
			p.Name = name
			procs := make([]services.ProcessConfig, 0, len(c.Processes)+1)
			replaced := false
			for _, cur := range c.Processes {
				if cur.Name == name {
					procs, replaced = append(procs, p), true
					continue
				}
				procs = append(procs, cur)
			}
			if !replaced {
				procs = append(procs, p)
			}
			c.Processes = procs
			return nil
		},
	}
}

// dropProcess removes the named process from c (a pruned process).
func dropProcess(c *services.Config, name string) {
	c.Processes = slices.DeleteFunc(slices.Clone(c.Processes), func(p services.ProcessConfig) bool { return p.Name == name })
}

// processNameField is config.processName. The default name is "web": a file that writes it
// leaves the stored name empty, so the staged config does not change for a spelled-out default.
var processNameField = cfgField{
	get: func(_ *planCtx, c services.Config) any { return c.DefaultProcessName() },
	set: func(_ *planCtx, c *services.Config, v any) error {
		s, ok := v.(string)
		if !ok {
			return refErr("wrong_type", "process_name must be text")
		}
		if s == services.DefaultProcessName {
			s = ""
		}
		c.ProcessName = s
		return nil
	},
}

// processDisplay renders a process for a plan line: its command, then what differs from a plain worker.
func processDisplay(v projectconfig.ProcessValue) string {
	var b strings.Builder
	b.WriteString(display(nonNil(v.Command)))
	if v.Port > 0 {
		b.WriteString(" port " + strconv.Itoa(v.Port))
	}
	if v.Singleton {
		b.WriteString(" singleton")
	}
	if v.StopGraceSeconds > 0 {
		b.WriteString(" grace " + strconv.Itoa(v.StopGraceSeconds) + "s")
	}
	return b.String()
}

// defaultProcessNameOf is the name of the service's own process once the file is applied: the
// file's process_name, else the live one, else "web". Domains name their process through it.
func defaultProcessNameOf(pc *planCtx, ls *liveService) string {
	if pc.rs != nil {
		if f, ok := pc.rs.Fields["config.processName"]; ok {
			if s, ok := f.Value.(string); ok && s != "" {
				return s
			}
			return services.DefaultProcessName
		}
	}
	if ls != nil {
		return ls.Cfg.DefaultProcessName()
	}
	return services.DefaultProcessName
}

// splitProcessScaleID splits processes.<name>.scale.<key> into the process name and the key
// below scale ("replicas", "autoscaling.max", ...).
func splitProcessScaleID(id string) (process, key string, ok bool) {
	rest, ok := strings.CutPrefix(id, "processes.")
	if !ok {
		return "", "", false
	}
	return strings.Cut(rest, ".scale.")
}

// processScaleOps handles processes.<name>.scale.* (immediate). A process without a scale row has
// one replica and the default autoscaling, like a service that was never scaled.
type processScaleOps struct{}

func (processScaleOps) live(_ *planCtx, ls *liveService, id, _ string) (fieldView, bool) {
	name, key, ok := splitProcessScaleID(id)
	if !ok {
		return fieldView{}, false
	}
	sc := services.ProcessScalingOf(ls.ProcScales, name)
	var v any
	switch key {
	case "replicas":
		v = sc.Replicas
	case "autoscaling.enabled":
		v = sc.Autoscaling.Enabled
	case "autoscaling.min":
		v = sc.Autoscaling.Min
	case "autoscaling.max":
		v = sc.Autoscaling.Max
	case "autoscaling.targetCpuPercent":
		v = sc.Autoscaling.TargetCPUPercent
	default:
		return fieldView{}, false
	}
	return view(v, display(v)), true
}

func (processScaleOps) desired(pc *planCtx, ls *liveService, fv projectconfig.FieldValue) (fieldView, error) {
	return scaleOps{}.desired(pc, ls, fv)
}
