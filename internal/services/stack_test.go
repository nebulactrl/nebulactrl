package services

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

const stackCompose = `
services:
  web:
    image: traefik/whoami:v1.10
    ports: ["80:80"]
  sidecar:
    image: busybox:1.36
    command: ["sleep", "3600"]
    depends_on: [web]
`

func stackConfig(compose string) Config {
	return Config{Stack: &StackConfig{Compose: compose}}.WithDefaults()
}

func TestStackConfigDerivesComponentsAndDefaults(t *testing.T) {
	c := stackConfig(stackCompose)
	require.NoError(t, c.Validate(SourceCompose))
	require.Equal(t, agentprotocol.DefaultStackWaitSeconds, c.Stack.WaitSeconds)
	require.Len(t, c.Stack.Components, 2)
	// Sorted by name; the index is the position.
	require.Equal(t, "sidecar", c.Stack.Components[0].Name)
	require.Equal(t, 0, c.Stack.Components[0].Index)
	require.Equal(t, "web", c.Stack.Components[1].Name)
	require.Equal(t, 1, c.Stack.Components[1].Index)
	require.Equal(t, []int{80}, c.Stack.Components[1].Ports)
	require.Equal(t, []string{"sidecar", "web"}, c.Stack.ComponentNames())
	comp, ok := c.Stack.Component("web")
	require.True(t, ok)
	require.Equal(t, "traefik/whoami:v1.10", comp.Image)
	_, ok = c.Stack.Component("nope")
	require.False(t, ok)
}

func TestStackWithDefaultsDoesNotShareTheStackPointer(t *testing.T) {
	in := Config{Stack: &StackConfig{Compose: stackCompose}}
	out := in.WithDefaults()
	require.Zero(t, in.Stack.WaitSeconds, "the input is left alone")
	require.NotSame(t, in.Stack, out.Stack)
}

func TestStackConfigValidation(t *testing.T) {
	require.NoError(t, stackConfig(stackCompose).Validate(SourceCompose))

	for name, c := range map[string]Config{
		"no stack":         Config{}.WithDefaults(),
		"empty file":       stackConfig("  "),
		"not yaml":         stackConfig("services: [oops"),
		"no services":      stackConfig("version: '3'\n"),
		"wait too big":     Config{Stack: &StackConfig{Compose: stackCompose, WaitSeconds: 4000}}.WithDefaults(),
		"unknown resource": Config{Stack: &StackConfig{Compose: stackCompose, Resources: map[string]agentprotocol.StackResources{"db": {MemoryMB: 64}}}}.WithDefaults(),
		"tiny memory":      Config{Stack: &StackConfig{Compose: stackCompose, Resources: map[string]agentprotocol.StackResources{"web": {MemoryMB: 2}}}}.WithDefaults(),
		"negative cpu":     Config{Stack: &StackConfig{Compose: stackCompose, Resources: map[string]agentprotocol.StackResources{"web": {CPUs: -1}}}}.WithDefaults(),
	} {
		require.ErrorIs(t, c.Validate(SourceCompose), ErrInvalidConfig, name)
	}

	// Fields that do not apply to a stack are refused, not ignored.
	for name, mut := range map[string]func(*Config){
		"image":       func(c *Config) { c.Image = "nginx:1" },
		"git":         func(c *Config) { c.GitURL = "https://github.com/a/b.git" },
		"port":        func(c *Config) { c.Port = 80 },
		"start":       func(c *Config) { c.StartCommand = []string{"x"} },
		"files":       func(c *Config) { c.Files = []FileMount{{Path: "/a", Content: "x"}} },
		"oneshot":     func(c *Config) { c.OneShot = true },
		"predeploy":   func(c *Config) { c.PreDeployCommand = []string{"migrate"} },
		"postdeploy":  func(c *Config) { c.PostDeployCommand = []string{"warm"} },
		"docker":      func(c *Config) { c.Docker = &agentprotocol.DockerOptions{Init: true} },
		"memory":      func(c *Config) { c.MemoryMB = 128 },
		"cpus":        func(c *Config) { c.CPUs = 1 },
		"healthcheck": func(c *Config) { c.Healthcheck.Type = HealthHTTP },
	} {
		c := Config{Stack: &StackConfig{Compose: stackCompose}}
		mut(&c)
		require.ErrorIs(t, c.WithDefaults().Validate(SourceCompose), ErrInvalidConfig, name)
	}

	// A stack config only fits a compose source, and a compose source needs one.
	require.ErrorIs(t, stackConfig(stackCompose).Validate(SourceImage), ErrInvalidConfig)
}

func TestResolveKindSource(t *testing.T) {
	kind, src, err := resolveKindSource("", SourceCompose)
	require.NoError(t, err)
	require.Equal(t, [2]string{KindStack, SourceCompose}, [2]string{kind, src})
	kind, src, err = resolveKindSource(KindStack, "")
	require.NoError(t, err)
	require.Equal(t, [2]string{KindStack, SourceCompose}, [2]string{kind, src})
	kind, _, err = resolveKindSource("", SourceImage)
	require.NoError(t, err)
	require.Equal(t, KindApp, kind)

	for _, c := range [][2]string{{KindStack, SourceImage}, {KindApp, SourceCompose}, {"nonsense", SourceImage}} {
		_, _, err := resolveKindSource(c[0], c[1])
		require.Error(t, err, c)
	}
}

func TestStackDiffShowsComposeAsOneTextField(t *testing.T) {
	a := stackConfig(stackCompose)
	changed := stackCompose + "\n# a comment\n"
	b := Config{Stack: &StackConfig{Compose: changed, WaitSeconds: 600, Resources: map[string]agentprotocol.StackResources{"web": {MemoryMB: 256}}}}.WithDefaults()
	got := map[string]FieldChange{}
	for _, d := range DiffConfig("config", a, b) {
		got[d.Field] = d
	}
	require.Contains(t, got, "config.stack.compose")
	require.Equal(t, stackCompose, got["config.stack.compose"].From)
	require.Equal(t, changed, got["config.stack.compose"].To)
	require.Contains(t, got, "config.stack.waitSeconds")
	require.Contains(t, got, "config.stack.resources.web.memoryMb")
	require.NotContains(t, got, "config.stack.components", "the derived components are not a staged change")

	// A new service in the file changes the components but only the compose text is listed.
	more := stackCompose + "  extra:\n    image: busybox:1\n"
	d := DiffConfig("config", a, stackConfig(more))
	require.Len(t, d, 1)
	require.Equal(t, "config.stack.compose", d[0].Field)
}

func TestStackDomainSnapshotAndDiffCarryTheComponent(t *testing.T) {
	a := SnapshotDomain{Hostname: "app.example.com", TargetPort: 80, Component: "web"}
	require.Equal(t, "app.example.com", a.Key())
	require.Equal(t, "web", a.normalized().Component)
}

func TestCheckLogComponent(t *testing.T) {
	stack := Scope{}
	stack.Service.Kind = KindStack
	stack.Service.Config = mustJSONConfig(t, stackConfig(stackCompose))
	require.NoError(t, CheckLogComponent(stack, ""))
	require.NoError(t, CheckLogComponent(stack, "web"))
	require.Error(t, CheckLogComponent(stack, "db"))

	app := Scope{}
	app.Service.Kind = KindApp
	require.NoError(t, CheckLogComponent(app, ""))
	require.Error(t, CheckLogComponent(app, "web"))
}

func TestNotForStacks(t *testing.T) {
	stack := Scope{}
	stack.Service.Kind = KindStack
	require.Error(t, RequireNotStack(stack))
	require.EqualError(t, ErrNotForStacks(), "Not available for Compose stacks")
	app := Scope{}
	app.Service.Kind = KindApp
	require.NoError(t, RequireNotStack(app))

	ok, why := Eligibility(KindStack, stackConfig(stackCompose), 0)
	require.False(t, ok)
	require.Contains(t, why, "Compose stacks")
	require.Equal(t, 1, EffectiveReplicas(KindStack, stackConfig(stackCompose), 0, 4))
}

func TestDeriveStackOverwritesStaleComponents(t *testing.T) {
	c := Config{Stack: &StackConfig{Compose: stackCompose, Components: []agentprotocol.StackComponent{{Name: "ghost", Index: 0}}}}
	got := DeriveStack(c)
	require.Equal(t, []string{"sidecar", "web"}, got.Stack.ComponentNames())
	require.Equal(t, "ghost", c.Stack.Components[0].Name, "the input is not modified")
}

func mustJSONConfig(t *testing.T, c Config) json.RawMessage {
	t.Helper()
	raw, err := json.Marshal(c)
	require.NoError(t, err)
	return raw
}
