package services_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/stdlib"
	"github.com/pressly/goose/v3"
	"github.com/stretchr/testify/require"

	"github.com/nebulactrl/nebulactrl/db/migrations"
	"github.com/nebulactrl/nebulactrl/internal/apierr"
	"github.com/nebulactrl/nebulactrl/internal/db"
	"github.com/nebulactrl/nebulactrl/internal/services"
	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

func worker(name string, mut ...func(*services.ProcessConfig)) services.ProcessConfig {
	p := services.ProcessConfig{Name: name, StartCommand: []string{"app", name}}
	for _, m := range mut {
		m(&p)
	}
	return p
}

func processConfig(ps ...services.ProcessConfig) services.Config {
	return services.Config{Image: "app:1", Port: 3000, Processes: ps}
}

func apiErr(t *testing.T, err error, code string) *apierr.Error {
	t.Helper()
	var ae *apierr.Error
	require.ErrorAs(t, err, &ae)
	require.Equal(t, code, ae.Code)
	require.Equal(t, 422, ae.Status)
	return ae
}

// ── names, defaults ─────────────────────────────────────────────────────────

func TestProcessNamePatternMatchesTheAgentProtocol(t *testing.T) {
	require.Equal(t, agentprotocol.ProcessNamePattern.String(), services.ProcessNamePattern.String())
	require.Equal(t, agentprotocol.MaxProcesses, services.MaxProcesses)
	require.Equal(t, agentprotocol.DefaultProcessName, services.DefaultProcessName)
}

func TestProcessDefaultsSortAndFillHealthchecks(t *testing.T) {
	c := processConfig(
		worker("zeta", func(p *services.ProcessConfig) { p.Port = 4000 }),
		worker("alpha"),
		worker("mid", func(p *services.ProcessConfig) {
			p.Port = 5000
			p.Healthcheck = services.Healthcheck{Type: services.HealthTCP}
		}),
	).WithDefaults()

	require.Equal(t, []string{"alpha", "mid", "zeta"}, []string{c.Processes[0].Name, c.Processes[1].Name, c.Processes[2].Name}, "sorted by name")

	alpha, mid, zeta := c.Processes[0], c.Processes[1], c.Processes[2]
	require.Equal(t, services.HealthNone, alpha.Healthcheck.Type, "no port, no health check")
	require.Zero(t, alpha.Healthcheck.Port)
	require.Equal(t, services.HealthHTTP, zeta.Healthcheck.Type)
	require.Equal(t, "/", zeta.Healthcheck.Path)
	require.Equal(t, 4000, zeta.Healthcheck.Port, "the health check follows THIS process' port")
	require.Equal(t, services.HealthTCP, mid.Healthcheck.Type)
	require.Equal(t, 5000, mid.Healthcheck.Port)
	require.Empty(t, mid.Healthcheck.Path, "a tcp check has no path")
	for _, p := range c.Processes {
		require.Equal(t, 2, p.Healthcheck.IntervalSeconds, p.Name)
		require.Equal(t, 3, p.Healthcheck.TimeoutSeconds, p.Name)
		require.Equal(t, 120, p.Healthcheck.DeadlineSeconds, p.Name)
		require.Equal(t, 2, p.Healthcheck.SuccessThreshold, p.Name)
		require.Zero(t, p.StopGraceSeconds, "inheritance is resolved by EffectiveProcesses, so a staged diff is stable")
		require.Zero(t, p.MemoryMB)
		require.Zero(t, p.CPUs)
	}
	require.Empty(t, c.ProcessName, "the default name is not defaulted: no spurious pending change")
	require.Equal(t, c, c.WithDefaults(), "defaults are idempotent")
	require.Equal(t, 30, c.StopGraceSeconds)
}

func TestServiceWithoutProcessesIsUnchanged(t *testing.T) {
	c := services.Config{Image: "app:1", Port: 3000}.WithDefaults()
	require.False(t, c.HasProcesses())
	require.Nil(t, c.Processes)
	require.NoError(t, c.Validate(services.SourceImage))
	raw, err := json.Marshal(c)
	require.NoError(t, err)
	for _, key := range []string{"processName", "singleton", "processes"} {
		require.NotContains(t, string(raw), key, "a plain service stores none of the new keys")
	}
	require.Len(t, c.EffectiveProcesses(), 1)
	require.Equal(t, []string{"web"}, c.ProcessNames())
}

// ── validation ──────────────────────────────────────────────────────────────

func TestProcessValidationMatrix(t *testing.T) {
	many := func(n int) []services.ProcessConfig {
		var ps []services.ProcessConfig
		for i := range n {
			ps = append(ps, worker("p"+string(rune('a'+i))))
		}
		return ps
	}
	cases := []struct {
		name   string
		mutate func(*services.Config)
		want   string // "" = valid; else a fragment of the error
	}{
		{"valid worker", func(c *services.Config) {}, ""},
		{"valid ten processes", func(c *services.Config) { c.Processes = many(9) }, ""},
		{"valid singleton default", func(c *services.Config) { c.Processes = nil; c.Singleton = true }, ""},
		{"valid renamed default", func(c *services.Config) { c.ProcessName = "api"; c.Processes = []services.ProcessConfig{worker("web")} }, ""},
		{"valid longest name", func(c *services.Config) { c.Processes = []services.ProcessConfig{worker(strings.Repeat("a", 31))} }, ""},
		{"valid explicit exec health", func(c *services.Config) {
			c.Processes = []services.ProcessConfig{worker("w", func(p *services.ProcessConfig) {
				p.Healthcheck = services.Healthcheck{Type: services.HealthExec, Command: []string{"CMD", "true"}}
			})}
		}, ""},
		{"valid tcp health on the process port", func(c *services.Config) {
			c.Processes = []services.ProcessConfig{worker("w", func(p *services.ProcessConfig) {
				p.Port = 7000
				p.Healthcheck = services.Healthcheck{Type: services.HealthTCP}
			})}
		}, ""},
		{"valid grace and limits", func(c *services.Config) {
			c.Processes = []services.ProcessConfig{worker("w", func(p *services.ProcessConfig) { p.StopGraceSeconds = 3600; p.MemoryMB = 6; p.CPUs = 0.5 })}
		}, ""},

		{"upper case name", func(c *services.Config) { c.Processes = []services.ProcessConfig{worker("Worker")} }, "process name"},
		{"underscore name", func(c *services.Config) { c.Processes = []services.ProcessConfig{worker("a_b")} }, "process name"},
		{"leading dash", func(c *services.Config) { c.Processes = []services.ProcessConfig{worker("-a")} }, "process name"},
		{"trailing dash", func(c *services.Config) { c.Processes = []services.ProcessConfig{worker("a-")} }, "process name"},
		{"too long name", func(c *services.Config) { c.Processes = []services.ProcessConfig{worker(strings.Repeat("a", 32))} }, "process name"},
		{"empty name", func(c *services.Config) { c.Processes = []services.ProcessConfig{worker("")} }, "process name"},
		{"bad processName", func(c *services.Config) { c.ProcessName = "Web" }, "processName"},
		{"bad processName without processes", func(c *services.Config) { c.Processes = nil; c.ProcessName = "a b" }, "processName"},
		{"reserved default name", func(c *services.Config) { c.Processes = []services.ProcessConfig{worker("web")} }, `taken by the default process`},
		{"reserved renamed default", func(c *services.Config) {
			c.ProcessName = "main"
			c.Processes = []services.ProcessConfig{worker("main")}
		}, `taken by the default process`},
		{"duplicate names", func(c *services.Config) { c.Processes = []services.ProcessConfig{worker("w"), worker("w")} }, `defined twice`},
		{"too many processes", func(c *services.Config) { c.Processes = many(10) }, "at most 10 processes"},
		{"port taken by the default", func(c *services.Config) {
			c.Processes = []services.ProcessConfig{worker("worker", func(p *services.ProcessConfig) { p.Port = 3000 })}
		}, `processes "worker" and "web" both use port 3000`},
		{"port taken by another process", func(c *services.Config) {
			c.Processes = []services.ProcessConfig{
				worker("a", func(p *services.ProcessConfig) { p.Port = 4000 }),
				worker("b", func(p *services.ProcessConfig) { p.Port = 4000 }),
			}
		}, `both use port 4000`},
		{"no start command", func(c *services.Config) {
			c.Processes = []services.ProcessConfig{{Name: "w"}}
		}, "startCommand is required"},
		{"empty start command element", func(c *services.Config) {
			c.Processes = []services.ProcessConfig{{Name: "w", StartCommand: []string{"app", ""}}}
		}, "empty"},
		{"empty program", func(c *services.Config) {
			c.Processes = []services.ProcessConfig{{Name: "w", StartCommand: []string{" "}}}
		}, "startCommand"},
		{"port out of range", func(c *services.Config) {
			c.Processes = []services.ProcessConfig{worker("w", func(p *services.ProcessConfig) { p.Port = 70000 })}
		}, "port must be between"},
		{"negative port", func(c *services.Config) {
			c.Processes = []services.ProcessConfig{worker("w", func(p *services.ProcessConfig) { p.Port = -1 })}
		}, "port must be between"},
		{"grace too long", func(c *services.Config) {
			c.Processes = []services.ProcessConfig{worker("w", func(p *services.ProcessConfig) { p.StopGraceSeconds = 3601 })}
		}, "stopGraceSeconds"},
		{"negative grace", func(c *services.Config) {
			c.Processes = []services.ProcessConfig{worker("w", func(p *services.ProcessConfig) { p.StopGraceSeconds = -1 })}
		}, "stopGraceSeconds"},
		{"memory too small", func(c *services.Config) {
			c.Processes = []services.ProcessConfig{worker("w", func(p *services.ProcessConfig) { p.MemoryMB = 5 })}
		}, "memoryMb"},
		{"negative cpus", func(c *services.Config) {
			c.Processes = []services.ProcessConfig{worker("w", func(p *services.ProcessConfig) { p.CPUs = -1 })}
		}, "cpus"},
		{"http health check without a port", func(c *services.Config) {
			c.Processes = []services.ProcessConfig{worker("w", func(p *services.ProcessConfig) { p.Healthcheck = services.Healthcheck{Type: services.HealthHTTP} })}
		}, "http health checks need a port"},
		{"tcp health check without a port", func(c *services.Config) {
			c.Processes = []services.ProcessConfig{worker("w", func(p *services.ProcessConfig) { p.Healthcheck = services.Healthcheck{Type: services.HealthTCP} })}
		}, "tcp health checks need a port"},
		{"bad health path", func(c *services.Config) {
			c.Processes = []services.ProcessConfig{worker("w", func(p *services.ProcessConfig) {
				p.Port = 4000
				p.Healthcheck = services.Healthcheck{Type: services.HealthHTTP, Path: "health"}
			})}
		}, "healthcheck.path must start with /"},
		{"exec health check without CMD", func(c *services.Config) {
			c.Processes = []services.ProcessConfig{worker("w", func(p *services.ProcessConfig) {
				p.Healthcheck = services.Healthcheck{Type: services.HealthExec, Command: []string{"true"}}
			})}
		}, "exec health checks"},
		{"unknown health type", func(c *services.Config) {
			c.Processes = []services.ProcessConfig{worker("w", func(p *services.ProcessConfig) { p.Healthcheck = services.Healthcheck{Type: "ping"} })}
		}, "unknown healthcheck type"},
		{"health deadline too long", func(c *services.Config) {
			c.Processes = []services.ProcessConfig{worker("w", func(p *services.ProcessConfig) {
				p.Healthcheck = services.Healthcheck{Type: services.HealthNone, DeadlineSeconds: 3601}
			})}
		}, "deadlineSeconds must be at most 3600"},
		{"negative health timing", func(c *services.Config) {
			c.Processes = []services.ProcessConfig{worker("w", func(p *services.ProcessConfig) {
				p.Healthcheck = services.Healthcheck{Type: services.HealthNone, IntervalSeconds: -1}
			})}
		}, "timings must be positive"},
		{"one-shot service", func(c *services.Config) { c.OneShot = true }, "processes do not apply"},
		{"route service", func(c *services.Config) { c.Route = &services.RouteConfig{} }, "processes do not apply"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := processConfig(worker("worker"))
			tc.mutate(&c)
			err := c.WithDefaults().Validate(services.SourceImage)
			if tc.want == "" {
				require.NoError(t, err)
				return
			}
			require.ErrorIs(t, err, services.ErrInvalidConfig)
			require.Contains(t, err.Error(), tc.want)
		})
	}
}

func TestProcessesNeedAnImageOrGitSource(t *testing.T) {
	c := processConfig(worker("worker"))
	c.GitURL, c.GitBranch = "https://git.example.com/acme/shop.git", "main"
	require.NoError(t, c.WithDefaults().Validate(services.SourceGit))

	for _, source := range []string{services.SourceCompose, services.SourceExternal} {
		err := processConfig(worker("worker")).WithDefaults().Validate(source)
		require.ErrorIs(t, err, services.ErrInvalidConfig, source)
	}
	c2 := processConfig(worker("worker"))
	c2.Image = ""
	require.ErrorIs(t, c2.WithDefaults().Validate(services.SourceImage), services.ErrInvalidConfig, "the image is still required")
}

func TestServiceLevelHealthcheckValidationIsUnchanged(t *testing.T) {
	c := services.Config{Image: "app:1", Port: 3000}.WithDefaults()
	require.NoError(t, c.Validate(services.SourceImage))
	c.Healthcheck.Path = "no-slash"
	err := c.Validate(services.SourceImage)
	require.ErrorIs(t, err, services.ErrInvalidConfig)
	require.Contains(t, err.Error(), "healthcheck.path must start with /")
	require.NotContains(t, err.Error(), "process", "the service's own messages carry no process prefix")

	c = services.Config{Image: "app:1"}.WithDefaults()
	c.Healthcheck = services.Healthcheck{Type: services.HealthHTTP, IntervalSeconds: 2, TimeoutSeconds: 3, DeadlineSeconds: 120, SuccessThreshold: 2, Path: "/"}
	require.ErrorContains(t, c.Validate(services.SourceImage), "http health checks need a port")
}

// ── effective processes ─────────────────────────────────────────────────────

func TestEffectiveProcessesInheritAndOrder(t *testing.T) {
	c := services.Config{
		Image: "app:1", Port: 3000, StartCommand: []string{"app", "serve"}, StopGraceSeconds: 45, MemoryMB: 512, CPUs: 1.5,
		Singleton: true, ProcessName: "main",
		Processes: []services.ProcessConfig{
			worker("queue", func(p *services.ProcessConfig) { p.StopGraceSeconds = 120; p.MemoryMB = 256 }),
			worker("api", func(p *services.ProcessConfig) { p.Port = 4000 }),
		},
	}.WithDefaults()

	ps := c.EffectiveProcesses()
	require.Len(t, ps, 3)
	require.Equal(t, []string{"main", "api", "queue"}, c.ProcessNames(), "the default process first, then by name")

	def := ps[0]
	require.True(t, def.Default)
	require.Equal(t, "main", def.Name)
	require.Equal(t, []string{"app", "serve"}, def.Command)
	require.Equal(t, 3000, def.Port)
	require.Equal(t, 45, def.StopGraceSeconds)
	require.True(t, def.Singleton)
	require.Equal(t, 512, def.MemoryMB)
	require.InDelta(t, 1.5, def.CPUs, 0)
	require.Equal(t, services.HealthHTTP, def.Healthcheck.Type)

	api := ps[1]
	require.False(t, api.Default)
	require.Equal(t, 45, api.StopGraceSeconds, "0 inherits the service's grace")
	require.Equal(t, 512, api.MemoryMB, "0 inherits the service's memory limit")
	require.InDelta(t, 1.5, api.CPUs, 0, "0 inherits the service's cpu limit")
	require.False(t, api.Singleton, "the default's singleton is not inherited")
	require.Equal(t, 4000, api.Healthcheck.Port)

	queue := ps[2]
	require.Equal(t, 120, queue.StopGraceSeconds)
	require.Equal(t, 256, queue.MemoryMB)
	require.Equal(t, []string{"app", "queue"}, queue.Command)

	for _, name := range []string{"", "main"} {
		got, ok := c.Process(name)
		require.True(t, ok, name)
		require.True(t, got.Default, name)
	}
	got, ok := c.Process("queue")
	require.True(t, ok)
	require.Equal(t, "queue", got.Name)
	_, ok = c.Process("web")
	require.False(t, ok, "web is not a process of this service: the default is called main")
	_, ok = c.Process("nope")
	require.False(t, ok)

	require.Equal(t, "web", services.Config{Image: "x"}.DefaultProcessName())
	require.Equal(t, "main", c.DefaultProcessName())
}

func TestEffectiveProcessesOfAPlainService(t *testing.T) {
	c := services.Config{Image: "app:1", Port: 3000}.WithDefaults()
	ps := c.EffectiveProcesses()
	require.Len(t, ps, 1)
	require.Equal(t, "web", ps[0].Name)
	require.True(t, ps[0].Default)
	require.Equal(t, 30, ps[0].StopGraceSeconds)
	got, ok := c.Process("web")
	require.True(t, ok)
	require.Equal(t, ps[0], got)
	// An unset stop grace on a config that was never defaulted still resolves.
	require.Equal(t, 30, services.Config{Image: "x"}.EffectiveProcesses()[0].StopGraceSeconds)
}

func TestRolloutOrderPutsPortProcessesFirst(t *testing.T) {
	c := services.Config{
		Image: "app:1", Port: 0,
		Processes: []services.ProcessConfig{
			worker("b-port", func(p *services.ProcessConfig) { p.Port = 4000 }),
			worker("a-noport"),
			worker("c-port", func(p *services.ProcessConfig) { p.Port = 5000 }),
			worker("d-noport"),
		},
	}.WithDefaults()
	var names []string
	for _, p := range services.RolloutOrder(c.EffectiveProcesses()) {
		names = append(names, p.Name)
	}
	require.Equal(t, []string{"b-port", "c-port", "web", "a-noport", "d-noport"}, names, "port processes first, each group default first then by name")

	c.Port = 3000
	names = nil
	for _, p := range services.RolloutOrder(c.EffectiveProcesses()) {
		names = append(names, p.Name)
	}
	require.Equal(t, []string{"web", "b-port", "c-port", "a-noport", "d-noport"}, names, "the default process leads its group")

	require.Empty(t, services.RolloutOrder(nil))
}

func TestRoutedProcess(t *testing.T) {
	p, ok := services.Config{Image: "x", Port: 3000}.WithDefaults().RoutedProcess()
	require.True(t, ok)
	require.Equal(t, "web", p.Name)

	_, ok = services.Config{Image: "x"}.WithDefaults().RoutedProcess()
	require.False(t, ok, "no process has a port")

	_, ok = processConfig(worker("api", func(c *services.ProcessConfig) { c.Port = 4000 })).WithDefaults().RoutedProcess()
	require.False(t, ok, "two processes have a port")

	c := processConfig(worker("api", func(c *services.ProcessConfig) { c.Port = 4000 }))
	c.Port = 0
	p, ok = c.WithDefaults().RoutedProcess()
	require.True(t, ok)
	require.Equal(t, "api", p.Name)
}

// ── ResolveDomainProcess ────────────────────────────────────────────────────

func TestResolveDomainProcess(t *testing.T) {
	plain := services.Config{Image: "x", Port: 3000}.WithDefaults()
	noPort := services.Config{Image: "x"}.WithDefaults()

	multi := services.Config{
		Image: "x", Port: 3000,
		Processes: []services.ProcessConfig{
			worker("api", func(p *services.ProcessConfig) { p.Port = 4000 }),
			worker("queue"),
			worker("admin", func(p *services.ProcessConfig) { p.Port = 5000 }),
		},
	}.WithDefaults()

	headless := services.Config{
		Image:     "x",
		Processes: []services.ProcessConfig{worker("api", func(p *services.ProcessConfig) { p.Port = 4000 }), worker("queue")},
	}.WithDefaults()
	twoNamed := services.Config{
		Image: "x",
		Processes: []services.ProcessConfig{
			worker("api", func(p *services.ProcessConfig) { p.Port = 4000 }),
			worker("admin", func(p *services.ProcessConfig) { p.Port = 5000 }),
		},
	}.WithDefaults()
	noHTTP := services.Config{Image: "x", Processes: []services.ProcessConfig{worker("queue")}}.WithDefaults()

	ok := []struct {
		name    string
		cfg     services.Config
		process string
		port    int
		want    string // process name
		stored  string
	}{
		{"plain, nothing given", plain, "", 0, "web", ""},
		{"plain, matching port", plain, "", 3000, "web", ""},
		{"multi, default by default", multi, "", 0, "web", ""},
		{"multi, default by name", multi, "web", 0, "web", ""},
		{"multi, default by name and port", multi, "web", 3000, "web", ""},
		{"multi, named", multi, "api", 0, "api", "api"},
		{"multi, named with its port", multi, "api", 4000, "api", "api"},
		{"multi, port-only default", multi, "", 3000, "web", ""},
		{"headless default, the unique port process", headless, "", 0, "api", "api"},
		{"headless default, named", headless, "api", 4000, "api", "api"},
	}
	for _, tc := range ok {
		t.Run(tc.name, func(t *testing.T) {
			p, stored, err := tc.cfg.ResolveDomainProcess(tc.process, tc.port)
			require.NoError(t, err)
			require.Equal(t, tc.want, p.Name)
			require.Equal(t, tc.stored, stored)
		})
	}

	bad := []struct {
		name    string
		cfg     services.Config
		process string
		port    int
		code    string
	}{
		{"plain with a process", plain, "web", 0, "invalid_process"},
		{"plain with another process", plain, "worker", 0, "invalid_process"},
		{"plain, wrong port", plain, "", 3001, "invalid_target_port"},
		{"plain without a port", noPort, "", 0, "no_http_port"},
		{"multi, unknown process", multi, "nope", 0, "unknown_process"},
		{"multi, process without a port", multi, "queue", 0, "process_has_no_port"},
		{"multi, wrong port for the default", multi, "", 4000, "invalid_target_port"},
		{"multi, wrong port for a named", multi, "api", 3000, "invalid_target_port"},
		{"multi, wrong port for the default by name", multi, "web", 4000, "invalid_target_port"},
		{"two named ports, none named", twoNamed, "", 0, "process_required"},
		{"no process has a port", noHTTP, "", 0, "no_http_port"},
		{"headless default by name has no port", headless, "web", 0, "process_has_no_port"},
	}
	for _, tc := range bad {
		t.Run(tc.name, func(t *testing.T) {
			_, _, err := tc.cfg.ResolveDomainProcess(tc.process, tc.port)
			require.Error(t, err)
			apiErr(t, err, tc.code)
		})
	}

	_, _, err := twoNamed.ResolveDomainProcess("", 0)
	require.Equal(t, []string{"admin", "api"}, apiErr(t, err, "process_required").Details["processes"], "the processes that have a port")
	_, _, err = multi.ResolveDomainProcess("nope", 0)
	require.Equal(t, []string{"web", "admin", "api", "queue"}, apiErr(t, err, "unknown_process").Details["processes"])
	_, _, err = multi.ResolveDomainProcess("api", 3000)
	require.Contains(t, apiErr(t, err, "invalid_target_port").Message, "4000", "the message names the port")
}

// ── eligibility, strategy, replicas ─────────────────────────────────────────

func TestProcessEligibility(t *testing.T) {
	plain := services.Config{Image: "x", Port: 3000}.WithDefaults()
	portless := services.Config{Image: "x"}.WithDefaults()
	multi := services.Config{
		Image: "x", Port: 3000,
		Processes: []services.ProcessConfig{
			worker("queue"),
			worker("clock", func(p *services.ProcessConfig) { p.Singleton = true }),
		},
	}.WithDefaults()
	queue, _ := multi.Process("queue")
	clock, _ := multi.Process("clock")
	def, _ := multi.Process("")

	// Without processes it is Eligibility: a port is required.
	ok, why := services.ProcessEligibility(services.KindApp, plain, 0, plain.EffectiveProcesses()[0])
	require.True(t, ok, why)
	ok, why = services.ProcessEligibility(services.KindApp, portless, 0, portless.EffectiveProcesses()[0])
	require.False(t, ok)
	require.Contains(t, why, "HTTP port")

	// With processes a port is not required: queue workers scale.
	ok, why = services.ProcessEligibility(services.KindApp, multi, 0, queue)
	require.True(t, ok, why)
	ok, why = services.ProcessEligibility(services.KindWorker, multi, 0, def)
	require.True(t, ok, why)

	ok, why = services.ProcessEligibility(services.KindApp, multi, 0, clock)
	require.False(t, ok)
	require.Equal(t, "Singleton processes run exactly one instance.", why)

	ok, why = services.ProcessEligibility(services.KindApp, multi, 1, queue)
	require.False(t, ok)
	require.Contains(t, why, "volumes")
	ok, _ = services.ProcessEligibility(services.KindDatabase, multi, 0, queue)
	require.False(t, ok)
	ok, _ = services.ProcessEligibility(services.KindStack, multi, 0, queue)
	require.False(t, ok)

	withFiles := multi
	withFiles.Files = []services.FileMount{{Path: "/etc/a", Content: "x"}}
	ok, why = services.ProcessEligibility(services.KindApp, withFiles, 0, queue)
	require.False(t, ok)
	require.Contains(t, why, "config files")

	// A singleton default process never scales, with or without other processes.
	single := plain
	single.Singleton = true
	ok, why = services.Eligibility(services.KindApp, single, 0)
	require.False(t, ok)
	require.Equal(t, services.SingletonReason, why)
	ok, _ = services.ProcessEligibility(services.KindApp, single, 0, single.EffectiveProcesses()[0])
	require.False(t, ok)
}

func TestEffectiveProcessReplicas(t *testing.T) {
	multi := services.Config{
		Image: "x", Port: 3000,
		Processes: []services.ProcessConfig{worker("queue"), worker("clock", func(p *services.ProcessConfig) { p.Singleton = true })},
	}.WithDefaults()
	queue, _ := multi.Process("queue")
	clock, _ := multi.Process("clock")

	require.Equal(t, 4, services.EffectiveProcessReplicas(services.KindApp, multi, 0, queue, 4))
	require.Equal(t, 10, services.EffectiveProcessReplicas(services.KindApp, multi, 0, queue, 99), "capped at 10")
	require.Equal(t, 1, services.EffectiveProcessReplicas(services.KindApp, multi, 0, queue, 0), "at least one")
	require.Equal(t, 1, services.EffectiveProcessReplicas(services.KindApp, multi, 0, queue, -3), "never below one")
	require.Equal(t, 1, services.EffectiveProcessReplicas(services.KindApp, multi, 1, queue, 4), "volumes force one instance")
	require.Equal(t, 1, services.EffectiveProcessReplicas(services.KindApp, multi, 0, clock, 4), "a singleton runs one instance")
}

func TestProcessStrategy(t *testing.T) {
	plain := services.Config{Image: "x", Port: 3000}.WithDefaults()
	portless := services.Config{Image: "x"}.WithDefaults()
	multi := services.Config{
		Image: "x", Port: 3000,
		Processes: []services.ProcessConfig{worker("queue"), worker("clock", func(p *services.ProcessConfig) { p.Singleton = true })},
	}.WithDefaults()
	queue, _ := multi.Process("queue")
	clock, _ := multi.Process("clock")
	def, _ := multi.Process("")

	require.Equal(t, agentprotocol.StrategyRolling, services.ProcessStrategy(plain, 0, plain.EffectiveProcesses()[0]))
	require.Equal(t, agentprotocol.StrategyRecreate, services.ProcessStrategy(plain, 1, plain.EffectiveProcesses()[0]), "volumes")
	require.Equal(t, agentprotocol.StrategyRecreate, services.ProcessStrategy(portless, 0, portless.EffectiveProcesses()[0]), "a port-less single-process service recreates, as today")
	require.Equal(t, agentprotocol.StrategyRolling, services.ProcessStrategy(multi, 0, queue), "a port-less process of a multi-process service rolls")
	require.Equal(t, agentprotocol.StrategyRolling, services.ProcessStrategy(multi, 0, def))
	require.Equal(t, agentprotocol.StrategyRecreate, services.ProcessStrategy(multi, 0, clock), "a singleton never overlaps")
	require.Equal(t, agentprotocol.StrategyRecreate, services.ProcessStrategy(multi, 2, queue), "volumes")
}

func TestProcessScalingOf(t *testing.T) {
	missing := services.ProcessScalingOf(nil, "queue")
	require.Equal(t, 1, missing.Replicas)
	require.Equal(t, services.DefaultAutoscaling(), missing.Autoscaling)
	require.Nil(t, missing.LastScaleAt)
	require.Nil(t, missing.LastScaleEvent)
	require.Nil(t, missing.BlockedReason)

	at := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	reason := "no capacity"
	rows := []db.ServiceProcessScale{
		{Process: "other", Replicas: 9},
		{
			Process: "queue", Replicas: 3, LastScaleAt: &at, ScaleBlockedReason: &reason,
			Autoscaling:    json.RawMessage(`{"enabled":true,"min":2,"max":6,"targetCpuPercent":60}`),
			LastScaleEvent: services.NewScaleEvent(1, 3, "manual", "dev"),
		},
	}
	got := services.ProcessScalingOf(rows, "queue")
	require.Equal(t, 3, got.Replicas)
	require.Equal(t, services.Autoscaling{Enabled: true, Min: 2, Max: 6, TargetCPUPercent: 60}, got.Autoscaling)
	require.Equal(t, &at, got.LastScaleAt)
	require.Equal(t, &reason, got.BlockedReason)
	require.NotNil(t, got.LastScaleEvent)
	require.Equal(t, 3, got.LastScaleEvent.To)
	require.Equal(t, 1, services.ProcessScalingOf(rows, "absent").Replicas)
}

// ── views ───────────────────────────────────────────────────────────────────

func TestBuildProcessViews(t *testing.T) {
	cfg := services.Config{
		Image: "x", Port: 3000, StopGraceSeconds: 40,
		Processes: []services.ProcessConfig{
			worker("worker", func(p *services.ProcessConfig) { p.StopGraceSeconds = 90 }),
			worker("clock", func(p *services.ProcessConfig) { p.Singleton = true }),
			worker("api", func(p *services.ProcessConfig) { p.Port = 4000 }),
		},
	}.WithDefaults()
	applied := services.Config{
		Image: "x", Port: 3000,
		Processes: []services.ProcessConfig{worker("worker"), worker("clock"), worker("old")},
	}.WithDefaults()
	row := db.Service{Kind: services.KindApp, Replicas: 2, Autoscaling: json.RawMessage(`{"enabled":false,"min":1,"max":3,"targetCpuPercent":70}`)}
	rows := []db.ServiceProcessScale{{Process: "worker", Replicas: 3}}
	obs := &services.ObservedView{
		State: "running", Health: "healthy", ReplicasReady: 5, ReplicasDesired: 6,
		Processes: []services.ObservedProcessView{
			{Name: "web", State: "running", Health: "healthy", ReplicasReady: 2, ReplicasDesired: 2},
			{Name: "worker", State: "running", Health: "degraded", ReplicasReady: 2, ReplicasDesired: 3},
			{Name: "clock", State: "running", Health: "healthy", ReplicasReady: 1, ReplicasDesired: 1},
			{Name: "old", State: "running", Health: "healthy", ReplicasReady: 1, ReplicasDesired: 1},
		},
	}

	views := services.BuildProcessViews(row, cfg, &applied, rows, 0, obs)
	var names []string
	for _, v := range views {
		names = append(names, v.Name)
	}
	require.Equal(t, []string{"web", "api", "clock", "worker", "old"}, names, "default first, named by name, then the removed ones")

	web, api, clock, wk, old := views[0], views[1], views[2], views[3], views[4]

	require.True(t, web.Default)
	require.Equal(t, 3000, web.Port)
	require.Equal(t, 40, web.StopGraceSeconds)
	require.Equal(t, 2, web.Scaling.Replicas, "the default process keeps the service's own replicas")
	require.Equal(t, 2, web.Scaling.Ready)
	require.True(t, web.Scaling.Eligible)
	require.NotNil(t, web.Observed)
	require.False(t, web.Pending)
	require.False(t, web.Removed)
	require.NotNil(t, web.Command)

	require.True(t, api.Pending, "in the staged config, not in the running release")
	require.Nil(t, api.Observed, "null until observed")
	require.Equal(t, 1, api.Scaling.Replicas, "no row: one replica")
	require.True(t, api.Scaling.Eligible)

	require.True(t, clock.Singleton)
	require.False(t, clock.Scaling.Eligible)
	require.NotNil(t, clock.Scaling.IneligibleReason)
	require.Equal(t, services.SingletonReason, *clock.Scaling.IneligibleReason)
	require.False(t, clock.Pending)

	require.Equal(t, 3, wk.Scaling.Replicas)
	require.Equal(t, 2, wk.Scaling.Ready)
	require.Equal(t, 90, wk.StopGraceSeconds)
	require.Equal(t, []string{"app", "worker"}, wk.Command)
	require.Equal(t, "degraded", wk.Observed.Health)

	require.True(t, old.Removed)
	require.False(t, old.Pending)
	require.False(t, old.Scaling.Eligible)

	// With volumes nothing scales.
	for _, v := range services.BuildProcessViews(row, cfg, &applied, rows, 1, obs) {
		if !v.Removed {
			require.False(t, v.Scaling.Eligible, v.Name)
		}
	}

	// A service that was never deployed has no release to be pending against.
	for _, v := range services.BuildProcessViews(row, cfg, nil, rows, 0, nil) {
		require.False(t, v.Pending, v.Name)
		require.False(t, v.Removed, v.Name)
		require.Nil(t, v.Observed, v.Name)
	}

	// A renamed default process is still found by the name it was observed under.
	renamed := cfg
	renamed.ProcessName = "main"
	for _, v := range services.BuildProcessViews(row, renamed, &applied, rows, 0, obs) {
		if v.Default {
			require.Equal(t, "main", v.Name)
			require.NotNil(t, v.Observed)
			require.Equal(t, "web", v.Observed.Name)
		}
	}

	none := services.BuildProcessViews(row, services.Config{Image: "x", Port: 3000}.WithDefaults(), &applied, rows, 0, obs)
	require.NotNil(t, none)
	require.Empty(t, none, "[] for a service without processes")
}

func TestDecodeObservedHasNoNullProcesses(t *testing.T) {
	o := services.DecodeObserved(json.RawMessage(`{"state":"running","health":"healthy","replicas":[]}`))
	require.NotNil(t, o)
	require.NotNil(t, o.Processes)
	raw, err := json.Marshal(o)
	require.NoError(t, err)
	require.Contains(t, string(raw), `"processes":[]`)
}

func TestProcessInputToProcess(t *testing.T) {
	in := services.ProcessInput{
		Name: "worker", StartCommand: []string{"app", "w"}, Port: 4000, StopGraceSeconds: 20, Singleton: true, MemoryMB: 128, CPUs: 0.5,
		Healthcheck: services.HealthcheckInput{Type: services.HealthTCP, Port: 4001, IntervalSeconds: 5, Command: []string{"CMD", "x"}},
	}
	got := in.ToProcess()
	require.Equal(t, services.ProcessConfig{
		Name: "worker", StartCommand: []string{"app", "w"}, Port: 4000, StopGraceSeconds: 20, Singleton: true, MemoryMB: 128, CPUs: 0.5,
		Healthcheck: services.Healthcheck{Type: services.HealthTCP, Port: 4001, IntervalSeconds: 5, Command: []string{"CMD", "x"}},
	}, got)
	raw, err := json.Marshal(in)
	require.NoError(t, err)
	var shape map[string]any
	require.NoError(t, json.Unmarshal(raw, &shape))
	for _, k := range []string{"name", "startCommand", "port", "healthcheck", "stopGraceSeconds", "singleton", "memoryMb", "cpus"} {
		require.Contains(t, shape, k)
	}
}

// ── source switch ───────────────────────────────────────────────────────────

func TestProcessesSurviveASourceSwitch(t *testing.T) {
	c := services.Config{
		Image: "app:1", Port: 3000, ProcessName: "main", Singleton: true,
		Processes: []services.ProcessConfig{worker("queue")},
	}.WithDefaults()

	toGit := c.ForSource(services.SourceGit)
	require.Empty(t, toGit.Image)
	require.Equal(t, "main", toGit.ProcessName)
	require.True(t, toGit.Singleton)
	require.Equal(t, c.Processes, toGit.Processes)

	back := toGit.ForSource(services.SourceImage)
	require.Equal(t, c.Processes, back.Processes)
	require.Equal(t, "main", back.ProcessName)

	// Undoing the staged source change keeps the processes edited since.
	edited := toGit
	edited.Processes = []services.ProcessConfig{worker("queue"), worker("clock")}
	undone := edited.WithSourceOf(c)
	require.Equal(t, "app:1", undone.Image)
	require.Len(t, undone.Processes, 2)
	require.Equal(t, "main", undone.ProcessName)
	require.True(t, undone.Singleton)
}

// ── pending changes ─────────────────────────────────────────────────────────

func TestDiffConfigOfProcesses(t *testing.T) {
	base := services.Config{Image: "app:1", Port: 3000}.WithDefaults()
	withWorker := services.Config{Image: "app:1", Port: 3000, Processes: []services.ProcessConfig{worker("worker")}}.WithDefaults()

	require.Empty(t, services.DiffConfig("config", withWorker, withWorker))

	// Adding a process is one change whose value is the process as compact JSON.
	added := services.DiffConfig("config", base, withWorker)
	require.Len(t, added, 1)
	require.Equal(t, "config.processes.worker", added[0].Field)
	require.Empty(t, added[0].From)
	wantJSON, err := json.Marshal(withWorker.Processes[0])
	require.NoError(t, err)
	require.Equal(t, string(wantJSON), added[0].To)
	require.NotContains(t, added[0].To, "\n")

	// Removing it is the reverse.
	removed := services.DiffConfig("config", withWorker, base)
	require.Len(t, removed, 1)
	require.Equal(t, "config.processes.worker", removed[0].Field)
	require.Equal(t, string(wantJSON), removed[0].From)
	require.Empty(t, removed[0].To)

	// Editing one reports the field, keyed by the process name.
	edited := services.Config{Image: "app:1", Port: 3000, Processes: []services.ProcessConfig{
		worker("worker", func(p *services.ProcessConfig) { p.StartCommand = []string{"app", "other"}; p.Singleton = true }),
	}}.WithDefaults()
	got := map[string]services.FieldChange{}
	for _, c := range services.DiffConfig("config", withWorker, edited) {
		got[c.Field] = c
	}
	require.Len(t, got, 2, "%v", got)
	require.Equal(t, `["app","worker"]`, got["config.processes.worker.startCommand"].From)
	require.Equal(t, `["app","other"]`, got["config.processes.worker.startCommand"].To)
	require.Equal(t, "true", got["config.processes.worker.singleton"].To)

	// A change to one process does not touch the other.
	two := services.Config{Image: "app:1", Port: 3000, Processes: []services.ProcessConfig{worker("worker"), worker("clock")}}.WithDefaults()
	twoEdited := services.Config{Image: "app:1", Port: 3000, Processes: []services.ProcessConfig{
		worker("worker"), worker("clock", func(p *services.ProcessConfig) { p.Port = 6000 }),
	}}.WithDefaults()
	var fields []string
	for _, c := range services.DiffConfig("config", two, twoEdited) {
		fields = append(fields, c.Field)
	}
	for _, f := range fields {
		require.True(t, strings.HasPrefix(f, "config.processes.clock."), f)
	}
	require.Contains(t, fields, "config.processes.clock.port")

	// The scalars are plain fields.
	scalars := base
	scalars.ProcessName, scalars.Singleton = "main", true
	got = map[string]services.FieldChange{}
	for _, c := range services.DiffConfig("config", base, scalars) {
		got[c.Field] = c
	}
	require.Equal(t, "main", got["config.processName"].To)
	require.Equal(t, "true", got["config.singleton"].To)
	require.Len(t, got, 2)
}

// ── CheckLogProcess ─────────────────────────────────────────────────────────

func scopeOf(t *testing.T, kind string, cfg services.Config) services.Scope {
	t.Helper()
	raw, err := json.Marshal(cfg.WithDefaults())
	require.NoError(t, err)
	return services.Scope{Service: db.Service{Kind: kind, Config: raw}}
}

func TestCheckLogProcess(t *testing.T) {
	multi := scopeOf(t, services.KindApp, services.Config{Image: "x", Port: 3000, Processes: []services.ProcessConfig{worker("queue")}})
	require.NoError(t, services.CheckLogProcess(multi, ""))
	require.NoError(t, services.CheckLogProcess(multi, "queue"))
	require.NoError(t, services.CheckLogProcess(multi, "web"), "the default process is a process too")
	apiErr(t, services.CheckLogProcess(multi, "nope"), "unknown_process")

	plain := scopeOf(t, services.KindApp, services.Config{Image: "x", Port: 3000})
	require.NoError(t, services.CheckLogProcess(plain, ""))
	apiErr(t, services.CheckLogProcess(plain, "web"), "validation_failed")
	apiErr(t, services.CheckLogProcess(plain, "queue"), "validation_failed")
}

// ── database-backed: CheckProcessesInUse, scale rows, rollups, migration ────

func TestCheckProcessesInUse(t *testing.T) {
	f := newTxFixture(t)
	cfg := services.Config{Image: "x", Port: 3000, Processes: []services.ProcessConfig{
		worker("api", func(p *services.ProcessConfig) { p.Port = 4000 }), worker("queue"),
	}}.WithDefaults()
	svc := f.insert(f.prod.ID, "web", cfg)
	bg := context.Background()
	_, err := f.q.InsertDomain(bg, db.InsertDomainParams{ServiceID: svc.ID, Hostname: "app.example.com", TargetPort: 3000})
	require.NoError(t, err)
	_, err = f.q.InsertDomain(bg, db.InsertDomainParams{ServiceID: svc.ID, Hostname: "api.example.com", TargetPort: 4000, Component: "api"})
	require.NoError(t, err)

	require.NoError(t, services.CheckProcessesInUse(bg, f.q, svc.ID, cfg), "every domain still has its process")

	// The default process' domain follows the service port and is never checked.
	changedPort := cfg
	changedPort.Port = 3100
	require.NoError(t, services.CheckProcessesInUse(bg, f.q, svc.ID, changedPort))

	// Dropping a process a domain routes to.
	dropped := services.Config{Image: "x", Port: 3000, Processes: []services.ProcessConfig{worker("queue")}}.WithDefaults()
	ae := apiErr(t, services.CheckProcessesInUse(bg, f.q, svc.ID, dropped), "process_in_use")
	require.Equal(t, "api", ae.Details["process"])
	require.Equal(t, "api.example.com", ae.Details["hostname"])

	// Going back to a single-process service.
	plain := services.Config{Image: "x", Port: 3000}.WithDefaults()
	apiErr(t, services.CheckProcessesInUse(bg, f.q, svc.ID, plain), "process_in_use")

	// The process no longer listens on the domain's port.
	moved := services.Config{Image: "x", Port: 3000, Processes: []services.ProcessConfig{
		worker("api", func(p *services.ProcessConfig) { p.Port = 4001 }), worker("queue"),
	}}.WithDefaults()
	apiErr(t, services.CheckProcessesInUse(bg, f.q, svc.ID, moved), "process_in_use")
	noPort := services.Config{Image: "x", Port: 3000, Processes: []services.ProcessConfig{worker("api"), worker("queue")}}.WithDefaults()
	apiErr(t, services.CheckProcessesInUse(bg, f.q, svc.ID, noPort), "process_in_use")
}

func TestDomainViewsSplitComponentAndProcess(t *testing.T) {
	f := newTxFixture(t)
	cfg := services.Config{Image: "x", Port: 3000, Processes: []services.ProcessConfig{worker("api", func(p *services.ProcessConfig) { p.Port = 4000 })}}.WithDefaults()
	svc := f.insert(f.prod.ID, "web", cfg)
	bg := context.Background()
	d1, err := f.q.InsertDomain(bg, db.InsertDomainParams{ServiceID: svc.ID, Hostname: "app.example.com", TargetPort: 3000})
	require.NoError(t, err)
	d2, err := f.q.InsertDomain(bg, db.InsertDomainParams{ServiceID: svc.ID, Hostname: "api.example.com", TargetPort: 4000, Component: "api"})
	require.NoError(t, err)

	views, err := services.DomainViews(bg, f.q, []db.Domain{d1, d2})
	require.NoError(t, err)
	require.Equal(t, "", views[0].Process)
	require.Equal(t, "", views[0].Component)
	require.Equal(t, "api", views[1].Process, "an app's component column is its process")
	require.Equal(t, "", views[1].Component)
}

func TestProcessScaleQueries(t *testing.T) {
	f := newTxFixture(t)
	svc := f.imageService(f.prod.ID, "web")
	bg := context.Background()
	q := f.q

	require.Empty(t, mustScales(t, q, svc.ID))
	row, err := q.EnsureProcessScale(bg, db.EnsureProcessScaleParams{ServiceID: svc.ID, Process: "queue"})
	require.NoError(t, err)
	require.EqualValues(t, 1, row.Replicas)
	again, err := q.EnsureProcessScale(bg, db.EnsureProcessScaleParams{ServiceID: svc.ID, Process: "queue"})
	require.NoError(t, err, "ensure is idempotent")
	require.Equal(t, row.Process, again.Process)
	require.Len(t, mustScales(t, q, svc.ID), 1)

	// Compare-and-swap: the second swap with a stale expectation finds no row.
	applied, err := q.ApplyProcessScale(bg, db.ApplyProcessScaleParams{
		ServiceID: svc.ID, Process: "queue", Replicas: 3, ExpectedReplicas: 1, LastScaleEvent: services.NewScaleEvent(1, 3, "manual", "dev"),
	})
	require.NoError(t, err)
	require.EqualValues(t, 3, applied.Replicas)
	require.NotNil(t, applied.LastScaleAt)
	_, err = q.ApplyProcessScale(bg, db.ApplyProcessScaleParams{
		ServiceID: svc.ID, Process: "queue", Replicas: 5, ExpectedReplicas: 1, LastScaleEvent: services.NewScaleEvent(1, 5, "manual", "dev"),
	})
	require.ErrorIs(t, err, pgx.ErrNoRows)
	require.Equal(t, svc.Generation, f.row(svc.ID).Generation, "scale rows never bump the service generation")
	require.EqualValues(t, svc.Replicas, f.row(svc.ID).Replicas, "the default process' columns are untouched")

	// Autoscaling upserts: a process without a row gets one.
	as, err := q.SetProcessAutoscaling(bg, db.SetProcessAutoscalingParams{
		ServiceID: svc.ID, Process: "mailer", Autoscaling: json.RawMessage(`{"enabled":true,"min":1,"max":4,"targetCpuPercent":60}`),
	})
	require.NoError(t, err)
	require.True(t, services.DecodeAutoscaling(as.Autoscaling).Enabled)
	require.Len(t, mustScales(t, q, svc.ID), 2)

	// The blocked reason returns a row only when it changed.
	reason := "no capacity"
	_, err = q.SetProcessScaleBlocked(bg, db.SetProcessScaleBlockedParams{ServiceID: svc.ID, Process: "queue", Reason: &reason})
	require.NoError(t, err)
	_, err = q.SetProcessScaleBlocked(bg, db.SetProcessScaleBlockedParams{ServiceID: svc.ID, Process: "queue", Reason: &reason})
	require.ErrorIs(t, err, pgx.ErrNoRows)

	// Rename keeps the row, prune drops the rest.
	require.NoError(t, q.RenameProcessScale(bg, db.RenameProcessScaleParams{ServiceID: svc.ID, FromProcess: "queue", ToProcess: "jobs"}))
	got := mustScales(t, q, svc.ID)
	require.Equal(t, []string{"jobs", "mailer"}, processesOf(got))
	require.EqualValues(t, 3, got[0].Replicas)
	require.NoError(t, q.PruneProcessScales(bg, db.PruneProcessScalesParams{ServiceID: svc.ID, Keep: []string{"jobs"}}))
	require.Equal(t, []string{"jobs"}, processesOf(mustScales(t, q, svc.ID)))
	require.NoError(t, q.PruneProcessScales(bg, db.PruneProcessScalesParams{ServiceID: svc.ID, Keep: []string{}}))
	require.Empty(t, mustScales(t, q, svc.ID), "an empty keep list removes every row")

	// The table constrains names and counts.
	_, err = f.pool.Exec(bg, "INSERT INTO service_process_scale (service_id, process) VALUES ($1, 'Bad_Name')", svc.ID)
	require.Error(t, err)
	_, err = f.pool.Exec(bg, "INSERT INTO service_process_scale (service_id, process, replicas) VALUES ($1, 'ok', 11)", svc.ID)
	require.Error(t, err)

	// Rows go with the service.
	_, err = q.EnsureProcessScale(bg, db.EnsureProcessScaleParams{ServiceID: svc.ID, Process: "queue"})
	require.NoError(t, err)
	_, err = f.pool.Exec(bg, "DELETE FROM services WHERE id = $1", svc.ID)
	require.NoError(t, err)
	require.Zero(t, f.count("SELECT count(*) FROM service_process_scale WHERE service_id = $1", svc.ID))
}

func mustScales(t *testing.T, q *db.Queries, id uuid.UUID) []db.ServiceProcessScale {
	t.Helper()
	rows, err := q.ListProcessScales(context.Background(), id)
	require.NoError(t, err)
	return rows
}

func processesOf(rows []db.ServiceProcessScale) []string {
	var out []string
	for _, r := range rows {
		out = append(out, r.Process)
	}
	return out
}

func TestClampProcessReplicas(t *testing.T) {
	f := newTxFixture(t)
	cfg := services.Config{Image: "x", Port: 3000, Processes: []services.ProcessConfig{
		worker("queue"), worker("clock", func(p *services.ProcessConfig) { p.Singleton = true }),
	}}.WithDefaults()
	svc := f.insert(f.prod.ID, "web", cfg)
	bg := context.Background()
	q := f.q
	for _, name := range []string{"queue", "clock", "gone"} {
		_, err := q.EnsureProcessScale(bg, db.EnsureProcessScaleParams{ServiceID: svc.ID, Process: name})
		require.NoError(t, err)
		_, err = q.ApplyProcessScale(bg, db.ApplyProcessScaleParams{
			ServiceID: svc.ID, Process: name, Replicas: 3, ExpectedReplicas: 1, LastScaleEvent: services.NewScaleEvent(1, 3, "manual", "dev"),
		})
		require.NoError(t, err)
		_, err = q.SetProcessAutoscaling(bg, db.SetProcessAutoscalingParams{
			ServiceID: svc.ID, Process: name, Autoscaling: json.RawMessage(`{"enabled":true,"min":1,"max":4,"targetCpuPercent":60}`),
		})
		require.NoError(t, err)
	}

	// No volumes: only the singleton is ineligible, and "gone" is no process of the release.
	require.NoError(t, f.inTx(true, func(tx pgx.Tx) error {
		return services.ClampProcessReplicas(f.ctx, tx, f.scope(tx, svc.ID), cfg, 0)
	}))
	byName := map[string]db.ServiceProcessScale{}
	for _, r := range mustScales(t, q, svc.ID) {
		byName[r.Process] = r
	}
	require.NotContains(t, byName, "gone")
	require.EqualValues(t, 3, byName["queue"].Replicas)
	require.True(t, services.DecodeAutoscaling(byName["queue"].Autoscaling).Enabled)
	require.EqualValues(t, 1, byName["clock"].Replicas)
	require.False(t, services.DecodeAutoscaling(byName["clock"].Autoscaling).Enabled)
	require.Equal(t, 1, f.audits("service.scale", svc.ID.String()))

	// With a volume nothing scales any more.
	require.NoError(t, f.inTx(true, func(tx pgx.Tx) error {
		return services.ClampProcessReplicas(f.ctx, tx, f.scope(tx, svc.ID), cfg, 1)
	}))
	for _, r := range mustScales(t, q, svc.ID) {
		require.EqualValues(t, 1, r.Replicas, r.Process)
		require.False(t, services.DecodeAutoscaling(r.Autoscaling).Enabled, r.Process)
	}

	// Without processes every row goes.
	plain := services.Config{Image: "x", Port: 3000}.WithDefaults()
	require.NoError(t, f.inTx(true, func(tx pgx.Tx) error {
		return services.ClampProcessReplicas(f.ctx, tx, f.scope(tx, svc.ID), plain, 0)
	}))
	require.Empty(t, mustScales(t, q, svc.ID))
}

func TestCombineQueries(t *testing.T) {
	f := newTxFixture(t)
	bg := context.Background()
	q := f.q
	target := f.imageService(f.prod.ID, "target")
	src1 := f.imageService(f.prod.ID, "src1")
	src2 := f.imageService(f.prod.ID, "src2")

	c, err := q.InsertCombine(bg, db.InsertCombineParams{
		EnvironmentID: f.prod.ID, TargetServiceID: target.ID, SourceServiceIds: []uuid.UUID{src1.ID, src2.ID},
		Plan: json.RawMessage(`{"moves":[]}`), CreatedBy: "dev@example.com",
	})
	require.NoError(t, err)
	require.Equal(t, "staged", c.State)

	for _, id := range []uuid.UUID{target.ID, src1.ID, src2.ID} {
		n, err := q.CountActiveCombinesForService(bg, id)
		require.NoError(t, err)
		require.EqualValues(t, 1, n, "target and sources both count")
	}
	n, err := q.CountActiveCombinesForService(bg, f.imageService(f.prod.ID, "other").ID)
	require.NoError(t, err)
	require.Zero(t, n)

	// One active combine per target.
	_, err = q.InsertCombine(bg, db.InsertCombineParams{
		EnvironmentID: f.prod.ID, TargetServiceID: target.ID, SourceServiceIds: []uuid.UUID{src1.ID}, Plan: json.RawMessage(`{}`),
	})
	require.Error(t, err)

	got, err := q.GetCombine(bg, c.ID)
	require.NoError(t, err)
	require.Equal(t, c.ID, got.ID)
	list, err := q.ListCombinesForEnvironment(bg, f.prod.ID)
	require.NoError(t, err)
	require.Len(t, list, 1)

	// The state moves by compare-and-swap.
	_, err = q.AdvanceCombine(bg, db.AdvanceCombineParams{ID: c.ID, FromState: "deploying", ToState: "cutover"})
	require.ErrorIs(t, err, pgx.ErrNoRows)
	adv, err := q.AdvanceCombine(bg, db.AdvanceCombineParams{ID: c.ID, FromState: "staged", ToState: "deploying"})
	require.NoError(t, err)
	require.Equal(t, "deploying", adv.State)
	require.Nil(t, adv.FinishedAt)
	done, err := q.AdvanceCombine(bg, db.AdvanceCombineParams{ID: c.ID, FromState: "deploying", ToState: "failed", Error: "boom"})
	require.NoError(t, err)
	require.Equal(t, "boom", done.Error)
	require.NotNil(t, done.FinishedAt)
	n, err = q.CountActiveCombinesForService(bg, target.ID)
	require.NoError(t, err)
	require.Zero(t, n, "a failed combine is no longer active")

	// A retry clears the finish time and the error.
	retry, err := q.AdvanceCombine(bg, db.AdvanceCombineParams{ID: c.ID, FromState: "failed", ToState: "deploying"})
	require.NoError(t, err)
	require.Nil(t, retry.FinishedAt)
	require.Empty(t, retry.Error)

	require.NoError(t, f.inTx(true, func(tx pgx.Tx) error {
		l, err := db.New(tx).LockCombine(bg, c.ID)
		require.NoError(t, err)
		require.Equal(t, "deploying", l.State)
		return nil
	}))

	// Moving domains and tasks onto the target.
	d, err := q.InsertDomain(bg, db.InsertDomainParams{ServiceID: src1.ID, Hostname: "a.example.com", TargetPort: 80})
	require.NoError(t, err)
	moved, err := q.MoveDomainsToService(bg, db.MoveDomainsToServiceParams{DomainIds: []uuid.UUID{d.ID}, ServiceID: target.ID, Component: "api"})
	require.NoError(t, err)
	require.Len(t, moved, 1)
	require.Equal(t, target.ID, moved[0].ServiceID)
	require.Equal(t, "api", moved[0].Component)

	_, err = f.pool.Exec(bg, `INSERT INTO service_tasks (service_id, name, schedule, command) VALUES ($1, 'nightly', '0 3 * * *', '["app","run"]'::jsonb)`, src2.ID)
	require.NoError(t, err)
	require.NoError(t, q.MoveServiceTasks(bg, db.MoveServiceTasksParams{FromServiceID: src2.ID, ToServiceID: target.ID}))
	require.Equal(t, 1, f.count("SELECT count(*) FROM service_tasks WHERE service_id = $1", target.ID))
	require.Zero(t, f.count("SELECT count(*) FROM service_tasks WHERE service_id = $1", src2.ID))

	// Replacing a release snapshot.
	var relID uuid.UUID
	require.NoError(t, f.pool.QueryRow(bg,
		`INSERT INTO releases (service_id, source, config) VALUES ($1, '{"type":"image"}'::jsonb, '{"config":{}}'::jsonb) RETURNING id`, target.ID).Scan(&relID))
	require.NoError(t, q.UpdateReleaseConfig(bg, db.UpdateReleaseConfigParams{ID: relID, Config: json.RawMessage(`{"config":{"image":"x"}}`)}))
	var cfg string
	require.NoError(t, f.pool.QueryRow(bg, "SELECT config::text FROM releases WHERE id = $1", relID).Scan(&cfg))
	require.JSONEq(t, `{"config":{"image":"x"}}`, cfg)
}

func TestProcessMetricRollups(t *testing.T) {
	f := newTxFixture(t)
	svc := f.imageService(f.prod.ID, "web")
	bg := context.Background()
	q := f.q
	srv, err := q.CreateServer(bg, db.CreateServerParams{WorkspaceID: f.ws.ID, Name: "agent1"})
	require.NoError(t, err)
	_, err = f.pool.Exec(bg, "UPDATE services SET server_id = $2 WHERE id = $1", svc.ID, srv.ID)
	require.NoError(t, err)

	base := time.Now().UTC().Truncate(time.Minute).Add(-5 * time.Minute)
	put := func(ts time.Time, process string, replica int, cpu float64) {
		require.NoError(t, q.InsertServiceMetricSample(bg, db.InsertServiceMetricSampleParams{
			ServerID: &srv.ID, ServiceID: svc.ID, Ts: ts, Replica: int32(replica), Process: process, CpuPercent: cpu, MemBytes: 100 << 20, MemLimitBytes: 1 << 30,
		}))
	}
	for _, ts := range []time.Time{base.Add(5 * time.Second), base.Add(15 * time.Second)} {
		put(ts, "web", 0, 10)
		put(ts, "web", 1, 20)
		put(ts, "worker", 0, 5)
	}
	// A legacy sample carries no process: it is part of the total only.
	put(base.Add(2*time.Minute), "", 0, 40)

	since := base.Add(-time.Minute)
	_, err = q.DownsampleMetrics1m(bg, since)
	require.NoError(t, err)
	_, err = q.DownsampleMetrics1m(bg, since)
	require.NoError(t, err, "idempotent: the conflict target includes the process")
	_, err = q.DownsampleMetrics15m(bg, since)
	require.NoError(t, err)

	read := func(process string) []db.ServiceMetrics1mRow {
		rows, err := q.ServiceMetrics1m(bg, db.ServiceMetrics1mParams{ServiceID: &svc.ID, Process: process, Since: since, BucketSecs: 60})
		require.NoError(t, err)
		return rows
	}
	total := read("")
	require.Len(t, total, 2, "the multi-process minute and the legacy minute")
	require.InDelta(t, 35, total[0].CpuAvg, 0.01, "10+20+5 summed per instant over every process")
	require.EqualValues(t, 3, total[0].Replicas)
	require.InDelta(t, 40, total[1].CpuAvg, 0.01)

	web := read("web")
	require.Len(t, web, 1)
	require.InDelta(t, 30, web[0].CpuAvg, 0.01)
	require.EqualValues(t, 2, web[0].Replicas)
	wk := read("worker")
	require.Len(t, wk, 1)
	require.InDelta(t, 5, wk[0].CpuAvg, 0.01)
	require.Empty(t, read("absent"))

	rows15, err := q.ServiceMetrics15m(bg, db.ServiceMetrics15mParams{ServiceID: &svc.ID, Process: "worker", Since: since.Add(-15 * time.Minute), BucketSecs: 900})
	require.NoError(t, err)
	require.Len(t, rows15, 1)
	require.InDelta(t, 5, rows15[0].CpuAvg, 0.01)
	rows15, err = q.ServiceMetrics15m(bg, db.ServiceMetrics15mParams{ServiceID: &svc.ID, Process: "", Since: since.Add(-15 * time.Minute), BucketSecs: 900})
	require.NoError(t, err)
	require.NotEmpty(t, rows15)

	raw := func(process string) []db.ServiceMetricsRawRow {
		rows, err := q.ServiceMetricsRaw(bg, db.ServiceMetricsRawParams{ServiceID: &svc.ID, Process: process, Since: since, BucketSecs: 60})
		require.NoError(t, err)
		return rows
	}
	require.InDelta(t, 35, raw("")[0].CpuAvg, 0.01, "no process filter: the service total")
	require.InDelta(t, 5, raw("worker")[0].CpuAvg, 0.01)

	// The latest-sample queries filter the same way.
	latest, err := q.LatestServiceReplicaSamples(bg, db.LatestServiceReplicaSamplesParams{ServiceID: &svc.ID, Since: since, Process: "web"})
	require.NoError(t, err)
	require.Len(t, latest, 2)
	allLatest, err := q.LatestServiceReplicaSamples(bg, db.LatestServiceReplicaSamplesParams{ServiceID: &svc.ID, Since: since})
	require.NoError(t, err)
	require.Len(t, allLatest, 2, "one newest sample per replica index across processes")
	buckets, err := q.ServiceCPUBuckets(bg, db.ServiceCPUBucketsParams{ServiceID: &svc.ID, Since: since, BucketSecs: 60, Process: "worker"})
	require.NoError(t, err)
	require.Len(t, buckets, 1)
	require.InDelta(t, 5, buckets[0].CpuAvg, 0.01)
}

func TestMigration0017DownAndUp(t *testing.T) {
	f := newTxFixture(t)
	bg := context.Background()
	sqlDB := stdlib.OpenDBFromPool(f.pool)
	defer sqlDB.Close()
	p, err := goose.NewProvider(goose.DialectPostgres, sqlDB, migrations.FS)
	require.NoError(t, err)

	exists := func(table string) bool {
		var n int
		require.NoError(t, f.pool.QueryRow(bg, "SELECT count(*) FROM information_schema.tables WHERE table_schema = current_schema() AND table_name = $1", table).Scan(&n))
		return n == 1
	}
	require.True(t, exists("service_process_scale"))
	require.True(t, exists("service_combines"))

	_, err = p.DownTo(bg, 16)
	require.NoError(t, err, "the Down block applies cleanly")
	require.False(t, exists("service_process_scale"))
	require.False(t, exists("service_combines"))
	var cols int
	require.NoError(t, f.pool.QueryRow(bg, "SELECT count(*) FROM information_schema.columns WHERE table_schema = current_schema() AND column_name = 'process' AND table_name LIKE 'metric_samples%'").Scan(&cols))
	require.Zero(t, cols)

	_, err = p.Up(bg)
	require.NoError(t, err, "and the Up block applies again")
	require.True(t, exists("service_process_scale"))
	require.True(t, exists("service_combines"))
}
