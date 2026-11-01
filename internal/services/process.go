package services

import (
	"context"
	"fmt"
	"maps"
	"regexp"
	"slices"
	"strings"

	"github.com/google/uuid"

	"github.com/nebulactrl/nebulactrl/internal/apierr"
	"github.com/nebulactrl/nebulactrl/internal/db"
	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

// One service, several processes. The service's own runtime fields (Port,
// StartCommand, Healthcheck, StopGraceSeconds, MemoryMB, CPUs, Singleton) are
// the DEFAULT process; Config.Processes holds the additional named ones. A
// service without Processes is a plain single-process service.

const (
	// MaxProcesses bounds the processes of one service, the default included.
	MaxProcesses = agentprotocol.MaxProcesses
	// DefaultProcessName names the default process unless Config.ProcessName says otherwise.
	DefaultProcessName = agentprotocol.DefaultProcessName
	// DefaultStopGraceSeconds is Config.StopGraceSeconds when it is not set.
	DefaultStopGraceSeconds = 30
)

// ProcessNamePattern is the shape of a process name: DNS-label-safe, 1..31
// characters (it becomes part of container names and of a DNS alias). It is the
// same expression as agentprotocol.ProcessNamePattern.
var ProcessNamePattern = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]{0,29}[a-z0-9])?$`)

// ProcessConfig is one additional process (JSON = API shape and stored shape).
type ProcessConfig struct {
	Name string `json:"name"`
	// StartCommand is the argv (no shell); required for a named process.
	StartCommand []string `json:"startCommand"`
	// Port is the HTTP port of the process; 0 = no HTTP.
	Port int `json:"port,omitempty"`
	// Healthcheck has the shape and defaulting of Config.Healthcheck, keyed on THIS process' port.
	Healthcheck Healthcheck `json:"healthcheck"`
	// StopGraceSeconds: 0 = inherit Config.StopGraceSeconds; else 1..3600.
	StopGraceSeconds int  `json:"stopGraceSeconds,omitempty"`
	Singleton        bool `json:"singleton,omitempty"`
	// MemoryMB: 0 = inherit Config.MemoryMB.
	MemoryMB int `json:"memoryMb,omitempty"`
	// CPUs: 0 = inherit Config.CPUs.
	CPUs float64 `json:"cpus,omitempty"`
}

// EffectiveProcess is a resolved process: inheritance applied, defaults filled.
type EffectiveProcess struct {
	Name             string
	Default          bool
	Command          []string
	Port             int
	Healthcheck      Healthcheck
	StopGraceSeconds int
	Singleton        bool
	MemoryMB         int
	CPUs             float64
}

// HasProcesses reports whether the service runs additional named processes.
func (c Config) HasProcesses() bool { return len(c.Processes) > 0 }

// DefaultProcessName is the name of the default process: ProcessName, or "web".
func (c Config) DefaultProcessName() string {
	if c.ProcessName != "" {
		return c.ProcessName
	}
	return DefaultProcessName
}

// EffectiveProcesses lists the resolved processes: the default process FIRST,
// then the named ones by name. A single-process service has exactly one.
func (c Config) EffectiveProcesses() []EffectiveProcess {
	stopGrace := c.StopGraceSeconds
	if stopGrace == 0 {
		stopGrace = DefaultStopGraceSeconds
	}
	out := slices.Grow([]EffectiveProcess{{
		Name: c.DefaultProcessName(), Default: true, Command: c.StartCommand, Port: c.Port,
		Healthcheck: c.Healthcheck.withDefaults(c.Port), StopGraceSeconds: stopGrace, Singleton: c.Singleton,
		MemoryMB: c.MemoryMB, CPUs: c.CPUs,
	}}, len(c.Processes))
	named := slices.Clone(c.Processes)
	slices.SortFunc(named, func(a, b ProcessConfig) int { return strings.Compare(a.Name, b.Name) })
	for _, p := range named {
		e := EffectiveProcess{
			Name: p.Name, Command: p.StartCommand, Port: p.Port, Healthcheck: p.Healthcheck.withDefaults(p.Port),
			StopGraceSeconds: p.StopGraceSeconds, Singleton: p.Singleton, MemoryMB: p.MemoryMB, CPUs: p.CPUs,
		}
		if e.StopGraceSeconds == 0 {
			e.StopGraceSeconds = stopGrace
		}
		if e.MemoryMB == 0 {
			e.MemoryMB = c.MemoryMB
		}
		if e.CPUs == 0 {
			e.CPUs = c.CPUs
		}
		out = append(out, e)
	}
	return out
}

// Process returns the named process; "" and the default process' own name
// return the default process.
func (c Config) Process(name string) (EffectiveProcess, bool) {
	all := c.EffectiveProcesses()
	if name == "" || name == all[0].Name {
		return all[0], true
	}
	for _, p := range all[1:] {
		if p.Name == name {
			return p, true
		}
	}
	return EffectiveProcess{}, false
}

// ProcessNames lists the process names, the default process' first.
func (c Config) ProcessNames() []string {
	all := c.EffectiveProcesses()
	names := make([]string, len(all))
	for i, p := range all {
		names[i] = p.Name
	}
	return names
}

// RolloutOrder orders processes for a deploy: processes with a port first, then
// the rest; each group keeps the order it came in (default first, then by name).
func RolloutOrder(ps []EffectiveProcess) []EffectiveProcess {
	out := make([]EffectiveProcess, 0, len(ps))
	for _, p := range ps {
		if p.Port > 0 {
			out = append(out, p)
		}
	}
	for _, p := range ps {
		if p.Port <= 0 {
			out = append(out, p)
		}
	}
	return out
}

// RoutedProcess is the unique process with an HTTP port; ok is false when no
// process or more than one has a port.
func (c Config) RoutedProcess() (EffectiveProcess, bool) {
	var found EffectiveProcess
	n := 0
	for _, p := range c.EffectiveProcesses() {
		if p.Port > 0 {
			found = p
			n++
		}
	}
	return found, n == 1
}

// portProcessNames names the processes that have a port.
func portProcessNames(ps []EffectiveProcess) []string {
	names := []string{}
	for _, p := range ps {
		if p.Port > 0 {
			names = append(names, p.Name)
		}
	}
	return names
}

// ResolveDomainProcess resolves which process a domain of an app routes to, from
// the `process` the caller named ("" = not named) and the target port it gave (0 =
// not given). It returns the process, the value to store in domains.component
// ("" for the default process, else the process name) and, on failure, a 422:
//
//   - invalid_process: a process was named on a service without processes
//   - process_required: several processes have a port and none was named (details.processes)
//   - no_http_port: no process has a port
//   - unknown_process: the name is none of the service's processes (details.processes)
//   - process_has_no_port: the named process has no port
//   - invalid_target_port: the target port is not the process' port
func (c Config) ResolveDomainProcess(process string, targetPort int) (p EffectiveProcess, storedProcess string, err error) {
	all := c.EffectiveProcesses()
	if !c.HasProcesses() {
		if process != "" {
			return EffectiveProcess{}, "", apierr.Unprocessable("invalid_process", "process only applies to services with several processes")
		}
		p = all[0]
		if p.Port <= 0 {
			return EffectiveProcess{}, "", apierr.Unprocessable("no_http_port", "the service has no HTTP port; set a port before adding a domain")
		}
		if targetPort != 0 && targetPort != p.Port {
			return EffectiveProcess{}, "", apierr.Unprocessable("invalid_target_port", fmt.Sprintf("targetPort must equal the service port (%d)", p.Port))
		}
		return p, "", nil
	}
	if process == "" {
		switch withPort := portProcessNames(all); {
		case all[0].Port > 0:
			p = all[0]
		case len(withPort) == 0:
			return EffectiveProcess{}, "", apierr.Unprocessable("no_http_port", "no process of the service has an HTTP port; set a port before adding a domain")
		case len(withPort) > 1:
			return EffectiveProcess{}, "", apierr.Unprocessable("process_required",
				fmt.Sprintf("several processes have an HTTP port (%s): name the process the domain routes to", strings.Join(withPort, ", "))).
				WithDetails(map[string]any{"processes": withPort})
		default:
			p, _ = c.Process(withPort[0])
		}
		// The default process wins when it has a port, even next to others that have one (rule 2).
	} else {
		var ok bool
		if p, ok = c.Process(process); !ok {
			return EffectiveProcess{}, "", apierr.Unprocessable("unknown_process",
				fmt.Sprintf("the service has no process %q (processes: %s)", process, strings.Join(c.ProcessNames(), ", "))).
				WithDetails(map[string]any{"processes": c.ProcessNames()})
		}
		if p.Port <= 0 {
			return EffectiveProcess{}, "", apierr.Unprocessable("process_has_no_port", fmt.Sprintf("the process %q has no HTTP port; set a port before adding a domain", p.Name))
		}
	}
	if targetPort != 0 && targetPort != p.Port {
		return EffectiveProcess{}, "", apierr.Unprocessable("invalid_target_port", fmt.Sprintf("targetPort must equal the port of the process %q (%d)", p.Name, p.Port))
	}
	if p.Default {
		return p, "", nil
	}
	return p, p.Name, nil
}

// validateProcesses checks ProcessName, Singleton and Processes of a defaulted
// config. The source type is checked by Validate.
func (c Config) validateProcesses() error {
	if c.ProcessName != "" && !ProcessNamePattern.MatchString(c.ProcessName) {
		return invalid("processName must be lowercase letters, digits and '-', start and end with a letter or digit, and have at most 31 characters")
	}
	if len(c.Processes) == 0 {
		return nil
	}
	if c.Database != nil || c.Stack != nil || c.Route != nil || c.OneShot {
		return invalid("processes do not apply to databases, stacks, route services or one-shot services")
	}
	if 1+len(c.Processes) > MaxProcesses {
		return invalid("a service runs at most %d processes, the default one included", MaxProcesses)
	}
	def := c.DefaultProcessName()
	portOwner := map[int]string{}
	if c.Port > 0 {
		portOwner[c.Port] = def
	}
	seen := map[string]bool{}
	for _, p := range c.Processes {
		if !ProcessNamePattern.MatchString(p.Name) {
			return invalid("process name %q must be lowercase letters, digits and '-', start and end with a letter or digit, and have at most 31 characters", p.Name)
		}
		if p.Name == def {
			return invalid("the process name %q is taken by the default process (processName)", p.Name)
		}
		if seen[p.Name] {
			return invalid("process %q is defined twice", p.Name)
		}
		seen[p.Name] = true
		if len(p.StartCommand) == 0 {
			return invalid("process %q: startCommand is required", p.Name)
		}
		if err := agentprotocol.ValidTaskCommand(p.StartCommand); err != nil {
			return invalid("process %q: startCommand: %s", p.Name, err)
		}
		for i, a := range p.StartCommand {
			if a == "" {
				return invalid("process %q: startCommand element %d is empty", p.Name, i)
			}
		}
		if p.Port < 0 || p.Port > 65535 {
			return invalid("process %q: port must be between 0 and 65535", p.Name)
		}
		if p.StopGraceSeconds != 0 && (p.StopGraceSeconds < 1 || p.StopGraceSeconds > 3600) {
			return invalid("process %q: stopGraceSeconds must be between 1 and 3600", p.Name)
		}
		if p.MemoryMB < 0 {
			return invalid("process %q: memoryMb must not be negative", p.Name)
		}
		if p.MemoryMB > 0 && p.MemoryMB < 6 {
			return invalid("process %q: memoryMb must be at least 6", p.Name)
		}
		if p.CPUs < 0 {
			return invalid("process %q: cpus must not be negative", p.Name)
		}
		if err := validateHealthcheck(p.Healthcheck, p.Port, fmt.Sprintf("process %q: ", p.Name)); err != nil {
			return err
		}
		if p.Port > 0 {
			if other, taken := portOwner[p.Port]; taken {
				return invalid("processes %q and %q both use port %d", p.Name, other, p.Port)
			}
			portOwner[p.Port] = p.Name
		}
	}
	return nil
}

// CheckProcessesInUse refuses a config that drops a process a domain still
// routes to, leaves it without a port, or turns it into the default process
// (422 `process_in_use`). A domain naming the old name of a staged rename counts
// for the new name. Domains of the default process (empty component) follow the
// service port and are not checked; those of a named process follow its port
// (FollowProcessPorts). Stacks use domains.component for compose components: see
// CheckComponentsInUse.
func CheckProcessesInUse(ctx context.Context, q *db.Queries, serviceID uuid.UUID, cfg Config) error {
	if cfg.Stack != nil {
		return nil
	}
	doms, renames, err := processDomainsOf(ctx, q, serviceID)
	if err != nil {
		return err
	}
	for _, d := range doms {
		if d.Component == "" {
			continue
		}
		name := renamedProcess(cfg, renames, d.Component)
		p, ok := cfg.Process(name)
		if !ok {
			return processInUse(d.Component, d.Hostname,
				fmt.Sprintf("process %q still has the domain %s: remove the domain first", d.Component, d.Hostname))
		}
		if p.Default {
			return processInUse(d.Component, d.Hostname,
				fmt.Sprintf("the domain %s routes to process %q, which is the default process now: remove or change the domain first", d.Hostname, d.Component))
		}
		if p.Port <= 0 {
			return processInUse(d.Component, d.Hostname,
				fmt.Sprintf("the domain %s routes to process %q, which has no port: remove or change the domain first", d.Hostname, d.Component))
		}
	}
	return nil
}

// FollowProcessPorts points the domains of each named process at the port the
// process has in cfg, after CheckProcessesInUse accepted cfg (every such domain
// has a process with a port).
func FollowProcessPorts(ctx context.Context, q *db.Queries, serviceID uuid.UUID, cfg Config) error {
	if cfg.Stack != nil {
		return nil
	}
	doms, renames, err := processDomainsOf(ctx, q, serviceID)
	if err != nil {
		return err
	}
	ports := map[string]int32{} // component -> port
	for _, d := range doms {
		if d.Component == "" {
			continue
		}
		if p, ok := cfg.Process(renamedProcess(cfg, renames, d.Component)); ok && !p.Default && p.Port > 0 && int32(p.Port) != d.TargetPort {
			ports[d.Component] = int32(p.Port)
		}
	}
	for _, component := range slices.Sorted(maps.Keys(ports)) {
		if err := q.SetProcessDomainsPort(ctx, db.SetProcessDomainsPortParams{ServiceID: serviceID, Component: component, TargetPort: ports[component]}); err != nil {
			return fmt.Errorf("retarget domains of process %s: %w", component, err)
		}
	}
	return nil
}

// processDomainsOf loads the domains of a service and its staged process renames.
func processDomainsOf(ctx context.Context, q *db.Queries, serviceID uuid.UUID) ([]db.Domain, map[string]string, error) {
	doms, err := q.ListServiceDomains(ctx, serviceID)
	if err != nil {
		return nil, nil, fmt.Errorf("list domains: %w", err)
	}
	renames, err := PendingProcessRenames(ctx, q, serviceID)
	if err != nil {
		return nil, nil, err
	}
	return doms, renames, nil
}

// renamedProcess is the name cfg knows a domain's component by: the new name of
// a staged rename while cfg has no process of the old one.
func renamedProcess(cfg Config, renames map[string]string, component string) string {
	if _, ok := cfg.Process(component); !ok {
		if to, ok := renames[component]; ok {
			return to
		}
	}
	return component
}

func processInUse(process, hostname, msg string) error {
	return apierr.Unprocessable("process_in_use", msg).WithDetails(map[string]any{"process": process, "hostname": hostname})
}

// CheckLogProcess validates the `process` filter of the log endpoints: ""
// is every process; a name must belong to a service with processes and be one
// of them.
func CheckLogProcess(sc Scope, process string) error {
	if process == "" {
		return nil
	}
	cfg := sc.Config()
	if !cfg.HasProcesses() {
		return apierr.Unprocessable("validation_failed", "process only applies to services with several processes")
	}
	if _, ok := cfg.Process(process); !ok {
		return apierr.Unprocessable("unknown_process", fmt.Sprintf("the service has no process %q", process)).
			WithDetails(map[string]any{"processes": cfg.ProcessNames()})
	}
	return nil
}

// ProcessInput is ProcessConfig as the API takes it: the health check fields are
// optional and omitted ones take their defaults.
type ProcessInput struct {
	Name             string           `json:"name" minLength:"1" maxLength:"31" pattern:"^[a-z0-9]([a-z0-9-]{0,29}[a-z0-9])?$" doc:"The process name: lowercase letters, digits and '-', at most 31 characters. Part of container names and of a DNS alias (<name>.<service slug>)"`
	StartCommand     []string         `json:"startCommand" doc:"The command (argv, no shell) the process runs in the service's image"`
	Port             int              `json:"port,omitempty" doc:"The HTTP port the process listens on; 0 = no HTTP. Unique across the service's processes"`
	Healthcheck      HealthcheckInput `json:"healthcheck,omitempty"`
	StopGraceSeconds int              `json:"stopGraceSeconds,omitempty" doc:"Seconds to wait after the stop signal before killing; 0 = the service's stopGraceSeconds"`
	Singleton        bool             `json:"singleton,omitempty" doc:"Never two instances at once: the old one stops before the new one starts; scaling is off"`
	MemoryMB         int              `json:"memoryMb,omitempty" doc:"Memory limit in MB; 0 = the service's limit"`
	CPUs             float64          `json:"cpus,omitempty" doc:"CPU limit; 0 = the service's limit"`
}

// ToProcess converts to a (not yet defaulted) ProcessConfig.
func (p ProcessInput) ToProcess() ProcessConfig {
	return ProcessConfig{
		Name: p.Name, StartCommand: p.StartCommand, Port: p.Port, StopGraceSeconds: p.StopGraceSeconds,
		Singleton: p.Singleton, MemoryMB: p.MemoryMB, CPUs: p.CPUs,
		Healthcheck: Healthcheck{
			Type: p.Healthcheck.Type, Path: p.Healthcheck.Path, Port: p.Healthcheck.Port, IntervalSeconds: p.Healthcheck.IntervalSeconds,
			TimeoutSeconds: p.Healthcheck.TimeoutSeconds, StartPeriodSeconds: p.Healthcheck.StartPeriodSeconds,
			DeadlineSeconds: p.Healthcheck.DeadlineSeconds, SuccessThreshold: p.Healthcheck.SuccessThreshold, Command: p.Healthcheck.Command,
		},
	}
}
