package composeimport

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestInterpolation(t *testing.T) {
	env := map[string]string{"A": "alpha", "EMPTY": "", "N": "5"}
	cases := []struct{ in, want string }{
		{"plain", "plain"},
		{"$$HOME costs $$5", "$HOME costs $5"},
		{"$A and ${A}", "alpha and alpha"},
		{"${UNSET:-def}", "def"},
		{"${EMPTY:-def}", "def"},
		{"${EMPTY-def}", ""},
		{"${A:-def}", "alpha"},
		{"${UNSET-def}", "def"},
		{"${A:+yes}", "yes"},
		{"${EMPTY:+yes}", ""},
		{"${EMPTY+yes}", "yes"},
		{"${UNSET:-${A}-x}", "alpha-x"},
		{"port ${N}0", "port 50"},
		{"cost $ 5 and $", "cost $ 5 and $"},
		{"${bad syntax}", "${bad syntax}"},
	}
	for _, c := range cases {
		require.Equal(t, c.want, newInterpolator(env).Expand(c.in), c.in)
	}

	ip := newInterpolator(env)
	require.Equal(t, "x", ip.Expand("${MISSING}x"))
	ip.Expand("${NEED:?set NEED}")
	ip.Expand("${OTHER?other}")
	require.Equal(t, []string{"MISSING"}, ip.Missing())
	require.Equal(t, []string{"NEED: set NEED", "OTHER: other"}, ip.Required())
}

func TestParseDotenv(t *testing.T) {
	src := "# comment\n" +
		"A=1\n" +
		"export B=two words # trailing\n" +
		"C=\"quoted # not a comment\"\n" +
		"D='single $A'\n" +
		"E=${A}${B}\n" +
		"F=\"line1\nline2\"\n" +
		"G=\"esc\\ttab\\n\"\n" +
		"BARE\n" +
		"=novalue\n" +
		"bad key=1\n" +
		"H=\n"
	got, notes := ParseDotenv(src, map[string]string{"BASE": "b"})
	require.Equal(t, "1", got["A"])
	require.Equal(t, "two words", got["B"])
	require.Equal(t, "quoted # not a comment", got["C"])
	require.Equal(t, "single $A", got["D"])
	require.Equal(t, "1two words", got["E"])
	require.Equal(t, "line1\nline2", got["F"])
	require.Equal(t, "esc\ttab\n", got["G"])
	require.Equal(t, "", got["H"])
	require.NotContains(t, got, "BARE")
	require.Len(t, notes, 2)
}

func TestShellSplit(t *testing.T) {
	got, err := shellSplit(`sh -c "echo 'a b' && run \"x\"" plain\ word 'single quoted'`)
	require.NoError(t, err)
	require.Equal(t, []string{"sh", "-c", `echo 'a b' && run "x"`, "plain word", "single quoted"}, got)
	_, err = shellSplit(`a "unterminated`)
	require.Error(t, err)
}

func TestUnits(t *testing.T) {
	for in, want := range map[any]int{"512m": 512, "1g": 1024, "1.5G": 1536, "256MB": 256, "1GiB": 1024, 268435456: 256, "1048576": 1, "100k": 1} {
		got, ok := parseMemoryMB(in)
		require.True(t, ok, in)
		require.Equal(t, want, got, in)
	}
	_, ok := parseMemoryMB("lots")
	require.False(t, ok)

	d, ok := parseDuration("1m30s")
	require.True(t, ok)
	require.Equal(t, 90*time.Second, d)
	d, _ = parseDuration(10)
	require.Equal(t, 10*time.Second, d)
}

func TestParsePortString(t *testing.T) {
	good := map[string]cPort{
		"80":                {Target: 80, Protocol: "tcp"},
		"8080:80":           {Target: 80, Published: 8080, Protocol: "tcp"},
		"127.0.0.1:8080:80": {Target: 80, Published: 8080, Protocol: "tcp", HostIP: "127.0.0.1"},
		"53:53/udp":         {Target: 53, Published: 53, Protocol: "udp"},
		"[::1]:8080:80":     {Target: 80, Published: 8080, Protocol: "tcp", HostIP: "::1"},
	}
	for in, want := range good {
		got, err := parsePortString(in)
		require.NoError(t, err, in)
		require.Equal(t, []cPort{want}, got, in)
	}
	for _, in := range []string{"80-90:80-90", "x", "0", "80/icmp", "1:2:3:4"} {
		_, err := parsePortString(in)
		require.Error(t, err, in)
	}
}

func TestParseMountString(t *testing.T) {
	m, err := parseMountString("pgdata:/var/lib/postgresql/data")
	require.NoError(t, err)
	require.Equal(t, cMount{Type: "volume", Source: "pgdata", Target: "/var/lib/postgresql/data"}, m)
	m, _ = parseMountString("./conf/app.yml:/etc/app.yml:ro")
	require.Equal(t, cMount{Type: "bind", Source: "./conf/app.yml", Target: "/etc/app.yml", ReadOnly: true}, m)
	m, _ = parseMountString("/data")
	require.Equal(t, cMount{Type: "volume", Target: "/data"}, m)
	m, _ = parseMountString("/srv/x:/x")
	require.Equal(t, "bind", m.Type)
	_, err = parseMountString("a:relative")
	require.Error(t, err)
}

func TestParseProjectErrors(t *testing.T) {
	for name, src := range map[string]string{
		"not yaml":       "services: [",
		"not a mapping":  "- a\n- b\n",
		"no services":    "name: x\n",
		"no image":       "services:\n  a:\n    command: x\n",
		"extends cycle":  "services:\n  a:\n    image: x\n    extends: b\n  b:\n    image: x\n    extends: a\n",
		"extends absent": "services:\n  a:\n    image: x\n    extends: nope\n",
	} {
		_, err := parseProject(src, nil, nil)
		require.Error(t, err, name)
	}
}

func TestParseProjectShapes(t *testing.T) {
	src := `
x-shared: &shared
  restart: always
services:
  a:
    <<: *shared
    image: nginx
    environment:
      - K1=v1
      - K2
      - K3=a=b
    ports:
      - target: 80
        published: "8080"
        protocol: tcp
    volumes:
      - type: bind
        source: ./x
        target: /x
        read_only: true
    depends_on: [b]
    healthcheck:
      test: curl -f http://localhost/
      interval: 10s
      retries: 5
  b:
    image: busybox
    command: sleep 10
    labels:
      traefik.enable: "false"
`
	p, err := parseProject(src, map[string]string{"K2": "from-dotenv"}, nil)
	require.NoError(t, err)
	require.Len(t, p.Services, 2)
	a := p.Services[0]
	require.Equal(t, "a", a.Key)
	require.Equal(t, "always", a.Restart)
	require.Equal(t, []envPair{{"K1", "v1", "environment"}, {"K2", "from-dotenv", "dotenv"}, {"K3", "a=b", "environment"}}, a.Env)
	require.Equal(t, []cPort{{Target: 80, Published: 8080, Protocol: "tcp"}}, a.Ports)
	require.Equal(t, []cMount{{Type: "bind", Source: "./x", Target: "/x", ReadOnly: true}}, a.Mounts)
	require.Equal(t, []cDep{{Name: "b", Condition: "service_started"}}, a.DependsOn)
	require.Equal(t, []string{"CMD-SHELL", "curl -f http://localhost/"}, a.Healthcheck.Test)
	require.Equal(t, 10*time.Second, a.Healthcheck.Interval)
	require.Equal(t, 5, a.Healthcheck.Retries)
	require.Equal(t, []string{"sleep", "10"}, p.Services[1].Command)
}

func TestTraefikLabels(t *testing.T) {
	tr := parseTraefikLabels(map[string]string{
		"traefik.enable":                                   "true",
		"traefik.http.routers.a.rule":                      "Host(`a.example.com`, `b.example.com`) && PathPrefix(`/x`) || Host(`c.example.org`)",
		"traefik.http.services.a.loadbalancer.server.port": "3000",
		"traefik.http.routers.a.entrypoints":               "websecure",
	})
	require.True(t, tr.Enabled)
	require.Equal(t, []string{"a.example.com", "b.example.com", "c.example.org"}, tr.Hosts)
	require.Equal(t, 3000, tr.Port)
	require.False(t, parseTraefikLabels(map[string]string{"traefik.enable": "false"}).Enabled)
	require.Empty(t, parseTraefikLabels(map[string]string{"traefik.http.routers.a.rule": "Host(`not a host`)"}).Hosts)
}
