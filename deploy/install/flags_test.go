package installdeploy

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// canary is a stand-in DNS API token: it must never show up in output, in
// install.conf or in the settings the control plane does not need it in.
const canary = "cfTok_canary-0123456789abcdefghij"

// stubs replace everything that talks to the machine. The functions under test
// (option checks, the plan, the files written) run for real.
const stubs = `
docker() { return 1; }
port_busy() { return 1; }
port_busy_udp() { return 1; }
getent() { return 1; }
have() { return 1; }
install() { :; }
detect_ips() { PUBLIC_IP=${PUBLIC_IP:-203.0.113.9}; LAN_IP=10.0.0.5; }
MEM_MB=2048
FRESH=0
`

// runInstaller sources install.sh with args as its command line, in dir as the
// installation directory, then runs body. It returns the combined output.
func runInstaller(t *testing.T, dir string, args []string, body string) (string, error) {
	t.Helper()
	bash, err := exec.LookPath("bash")
	if err != nil {
		t.Skip("bash not available")
	}
	quoted := make([]string, len(args))
	for i, a := range args {
		quoted[i] = "'" + strings.ReplaceAll(a, "'", `'\''`) + "'"
	}
	script := "set -Eeuo pipefail\n" +
		"export NC_INSTALL_SOURCE_ONLY=1 NEBULACTRL_INSTALL_DIR='" + dir + "' BACKUP_DIR=x\n" +
		"set -- --dir '" + dir + "' " + strings.Join(quoted, " ") + "\n" +
		". ./install.sh\n" + stubs + body
	cmd := exec.Command(bash, "-c", script)
	cmd.Dir = "."
	out, err := cmd.CombinedOutput()
	return string(out), err
}

func tokenFile(t *testing.T, content string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "cf-token")
	if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func wantLine(t *testing.T, out, want string) {
	t.Helper()
	for _, l := range strings.Split(out, "\n") {
		if l == want {
			return
		}
	}
	t.Errorf("output lacks the line %q:\n%s", want, out)
}

const show = `
echo "mode=$MODE"; echo "url=$PUBLIC_URL"; echo "bind=$CP_BIND"; echo "port=$CP_PORT"; echo "local=$CP_URL_LOCAL"
echo "loop=$NEED_LOOPBACK"; echo "allow=$DASHBOARD_ALLOW"; echo "dns01=$DASHBOARD_DNS01"
echo "http=$PROXY_HTTP_PORT"; echo "https=$PROXY_HTTPS_PORT"; echo "admin=$PROXY_ADMIN_PORT"; echo "moving=$PROXY_MOVING"
`

// The Coolify side-by-side layout: domain mode, the proxy on 8080/8443, the
// dashboard allow list and DNS-01.
func TestSideBySideFlagsParseAndPersist(t *testing.T) {
	dir := t.TempDir()
	tok := tokenFile(t, canary+"\n")
	out, err := runInstaller(t, dir, []string{
		"--domain", "Nebula.Example.com", "--email", "me@example.com", "--no-firewall", "--no-fail2ban", "--no-hardening",
		"--port", "8100", "--proxy-http-port", "8080", "--proxy-https-port", "8443",
		"--dashboard-allow", "100.64.0.0/10, FD7A:115C:A1E0::/48", "--dashboard-dns01", "cloudflare", "--dns-token-file", tok,
	}, "step_plan\nstep_files\n"+show)
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	for _, l := range []string{
		"mode=domain", "url=https://nebula.example.com:8443", "bind=127.0.0.1", "port=8100", "local=http://127.0.0.1:8100",
		"allow=100.64.0.0/10,fd7a:115c:a1e0::/48", "dns01=cloudflare", "http=8080", "https=8443", "admin=9000",
	} {
		wantLine(t, out, l)
	}
	if strings.Contains(out, canary) {
		t.Fatal("the token was printed")
	}
	conf, err := os.ReadFile(filepath.Join(dir, "install.conf"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(conf), canary) {
		t.Fatal("the token was written to install.conf")
	}

	// A plain re-run (no flags) keeps every setting.
	out, err = runInstaller(t, dir, nil, "step_plan\n"+show)
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	for _, l := range []string{
		"url=https://nebula.example.com:8443", "port=8100", "allow=100.64.0.0/10,fd7a:115c:a1e0::/48", "dns01=cloudflare", "http=8080", "https=8443",
	} {
		wantLine(t, out, l)
	}

	// Moving the proxy back: only the ports change.
	out, err = runInstaller(t, dir, []string{"--proxy-http-port", "80", "--proxy-https-port", "443"}, "step_plan\n"+show)
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	wantLine(t, out, "url=https://nebula.example.com")
	wantLine(t, out, "http=80")
	wantLine(t, out, "port=8100")
}

// The version is remembered only when it was pinned, so a plain re-run
// upgrades. A 0.1.0 install.conf saved the resolved VERSION and IMAGE (on
// Ubuntu even the OS version, "26.04.1 LTS (…)", taken from /etc/os-release):
// those lines must not pin anything.
func TestVersionIsRememberedOnlyWhenPinned(t *testing.T) {
	const showVersion = `echo "version=$VERSION"; echo "image=$IMAGE"` + "\n"
	dir := t.TempDir()
	legacy := "DOMAIN=nebula.example.com\nVERSION=26.04.1\\ LTS\\ \\(Resolute\\ Raccoon\\)\nIMAGE=ghcr.io/nebulactrl/nebulactrl:0.1.0\n"
	if err := os.WriteFile(filepath.Join(dir, "install.conf"), []byte(legacy), 0o600); err != nil {
		t.Fatal(err)
	}
	out, err := runInstaller(t, dir, nil, showVersion+"save_conf\n")
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	wantLine(t, out, "version=latest")
	wantLine(t, out, "image=ghcr.io/nebulactrl/nebulactrl:latest")
	conf, err := os.ReadFile(filepath.Join(dir, "install.conf"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(conf), "Resolute") || strings.HasPrefix(string(conf), "VERSION=") || strings.Contains(string(conf), "\nVERSION=") || strings.Contains(string(conf), "\nIMAGE=") {
		t.Fatalf("the legacy version survived in install.conf:\n%s", conf)
	}

	// --version pins, and the pin survives a plain re-run.
	if out, err = runInstaller(t, dir, []string{"--version", "0.1.1"}, "save_conf\n"); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	out, err = runInstaller(t, dir, nil, showVersion)
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	wantLine(t, out, "version=0.1.1")
	wantLine(t, out, "image=ghcr.io/nebulactrl/nebulactrl:0.1.1")

	// --version latest drops the pin again.
	if out, err = runInstaller(t, dir, []string{"--version", "latest"}, "save_conf\n"); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	out, err = runInstaller(t, dir, nil, showVersion)
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	wantLine(t, out, "version=latest")
}

// With the proxy on 8080 and no --port the control plane's loopback port must
// not collide with it.
func TestLoopbackPortAvoidsTheProxyPorts(t *testing.T) {
	out, err := runInstaller(t, t.TempDir(), []string{
		"--domain", "nebula.example.com", "--email", "me@example.com", "--proxy-http-port", "8080", "--proxy-https-port", "8443",
	}, "step_plan\n"+show)
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	wantLine(t, out, "port=8081")
	wantLine(t, out, "local=http://127.0.0.1:8081")

	out, err = runInstaller(t, t.TempDir(), []string{
		"--domain", "nebula.example.com", "--email", "me@example.com", "--proxy-http-port", "8080", "--proxy-https-port", "8443", "--port", "8080",
	}, "step_plan\n"+show)
	if err == nil || !strings.Contains(out, "also a port of the proxy") {
		t.Fatalf("an explicit --port on a proxy port must be refused:\n%s", out)
	}
}

// --bind persists across re-runs, and .env is not rewritten without it.
func TestBindPersistsAndNeedsLoopbackPublish(t *testing.T) {
	dir := t.TempDir()
	body := "step_plan\nstep_files\n" + show
	out, err := runInstaller(t, dir, []string{"--bind", "100.64.0.7", "--port", "8100", "--no-local-server"}, body)
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	for _, l := range []string{"mode=ip", "url=http://100.64.0.7:8100", "bind=100.64.0.7", "loop=1", "local=http://127.0.0.1:8100"} {
		wantLine(t, out, l)
	}
	env := readFile(t, filepath.Join(dir, ".env"))
	for _, l := range []string{"NEBULACTRL_BIND=100.64.0.7", "NEBULACTRL_PORT=8100", "COMPOSE_FILE=docker-compose.yml:docker-compose.loopback.yml"} {
		if !strings.Contains(env, l+"\n") {
			t.Errorf(".env lacks %q:\n%s", l, env)
		}
	}
	if !strings.Contains(readFile(t, filepath.Join(dir, "docker-compose.loopback.yml")), `"127.0.0.1:${NEBULACTRL_PORT}:8080"`) {
		t.Error("the loopback override does not publish 127.0.0.1")
	}

	// Re-run without the flag: still bound, still the same .env.
	out, err = runInstaller(t, dir, []string{"--no-local-server"}, body)
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	wantLine(t, out, "bind=100.64.0.7")
	if again := readFile(t, filepath.Join(dir, ".env")); !strings.Contains(again, "NEBULACTRL_BIND=100.64.0.7\n") {
		t.Errorf("the bind address was dropped from .env:\n%s", again)
	}

	// 0.0.0.0 goes back to every address and removes the override.
	out, err = runInstaller(t, dir, []string{"--bind", "0.0.0.0", "--no-local-server"}, body)
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	wantLine(t, out, "bind=0.0.0.0")
	if _, err := os.Stat(filepath.Join(dir, "docker-compose.loopback.yml")); err == nil {
		t.Error("the loopback override stayed")
	}
}

// A bind address put into .env by hand survives a re-run.
func TestHandEditedBindInEnvIsKept(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, ".env"), []byte("NEBULACTRL_BIND=100.64.0.7\nNEBULACTRL_SECRET_KEY=k\nPOSTGRES_PASSWORD=p\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	out, err := runInstaller(t, dir, []string{"--port", "8100", "--no-local-server"}, "step_plan\nstep_files\n"+show)
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	wantLine(t, out, "bind=100.64.0.7")
	if env := readFile(t, filepath.Join(dir, ".env")); !strings.Contains(env, "NEBULACTRL_BIND=100.64.0.7\n") {
		t.Errorf(".env lost the hand-edited bind:\n%s", env)
	}
}

// The dashboard settings reach .env (the control plane's seed), the token only there.
func TestDashboardSettingsAreWrittenToEnvAndKept(t *testing.T) {
	dir := t.TempDir()
	tok := tokenFile(t, canary)
	args := []string{
		"--domain", "nebula.example.com", "--email", "me@example.com",
		"--dashboard-allow", "100.64.0.0/10", "--dashboard-dns01", "cloudflare", "--dns-token-file", tok,
	}
	out, err := runInstaller(t, dir, args, "step_plan\nstep_files\n")
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	if strings.Contains(out, canary) {
		t.Fatalf("the token was printed:\n%s", out)
	}
	env := readFile(t, filepath.Join(dir, ".env"))
	for _, l := range []string{
		"NEBULACTRL_DASHBOARD_ALLOW_CIDRS=100.64.0.0/10", "NEBULACTRL_DASHBOARD_DNS_RESOLVER=cloudflare",
		"NEBULACTRL_DASHBOARD_DNS_TOKEN=" + canary, "NEBULACTRL_PUBLIC_URL=https://nebula.example.com",
	} {
		if !strings.Contains(env, l+"\n") {
			t.Errorf(".env lacks %q", l)
		}
	}
	if st, _ := os.Stat(filepath.Join(dir, ".env")); st == nil || st.Mode().Perm() != 0o600 {
		t.Errorf(".env mode = %v, want 0600", st)
	}
	// The saved token is reused without the file.
	out, err = runInstaller(t, dir, nil, "step_plan\nstep_files\n")
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	if env := readFile(t, filepath.Join(dir, ".env")); !strings.Contains(env, "NEBULACTRL_DASHBOARD_DNS_TOKEN="+canary+"\n") {
		t.Error("a re-run dropped the stored token")
	}
	// Switching DNS-01 off clears the stored token.
	out, err = runInstaller(t, dir, []string{"--dashboard-dns01", "off"}, "step_plan\nstep_files\n")
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	if env := readFile(t, filepath.Join(dir, ".env")); strings.Contains(env, canary) {
		t.Error("the token stayed in .env after --dashboard-dns01 off")
	}
}

func TestOptionValidation(t *testing.T) {
	good := tokenFile(t, canary)
	spaced := tokenFile(t, "has space in it 0123456789")
	multi := tokenFile(t, canary+"\nsecond-line-0123456789\n")
	dom := []string{"--domain", "nebula.example.com", "--email", "me@example.com"}
	cases := []struct {
		name string
		args []string
		want string
	}{
		{"cidr", append([]string{"--dashboard-allow", "100.64.0.0/33"}, dom...), "is not an IP address or CIDR"},
		{"cidr words", append([]string{"--dashboard-allow", "tailnet"}, dom...), "is not an IP address or CIDR"},
		{"allow without domain", []string{"--dashboard-allow", "100.64.0.0/10"}, "need --domain"},
		{"allow without local server", append([]string{"--dashboard-allow", "100.64.0.0/10", "--no-local-server"}, dom...), "drop --no-local-server"},
		{"dns01 without domain", []string{"--dashboard-dns01", "cloudflare", "--dns-token-file", good}, "need --domain"},
		{"dns01 without token", append([]string{"--dashboard-dns01", "cloudflare"}, dom...), "needs --dns-token-file"},
		{"dns01 provider", append([]string{"--dashboard-dns01", "route53"}, dom...), "supports cloudflare"},
		{"token file without dns01", append([]string{"--dns-token-file", good}, dom...), "goes with --dashboard-dns01"},
		{"token file missing", append([]string{"--dashboard-dns01", "cloudflare", "--dns-token-file", "/nonexistent/x"}, dom...), "cannot read --dns-token-file"},
		{"token with spaces", append([]string{"--dashboard-dns01", "cloudflare", "--dns-token-file", spaced}, dom...), "does not hold an API token"},
		{"token on two lines", append([]string{"--dashboard-dns01", "cloudflare", "--dns-token-file", multi}, dom...), "single line"},
		{"ports equal", []string{"--proxy-http-port", "8443", "--proxy-https-port", "8443"}, "must differ"},
		{"port range", []string{"--proxy-http-port", "70000"}, "between 1 and 65535"},
		{"admin clash", []string{"--proxy-admin-port", "443"}, "must differ"},
		{"bind", []string{"--bind", "tailscale0"}, "--bind must be an IPv4 address"},
		{"bind octet", []string{"--bind", "100.64.0.300"}, "--bind must be an IPv4 address"},
		{"port is proxy port (ip mode)", []string{"--port", "8080", "--proxy-http-port", "8080"}, "also a port of the proxy"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			out, err := runInstaller(t, t.TempDir(), tc.args, "step_plan\n")
			if err == nil {
				t.Fatalf("accepted %v:\n%s", tc.args, out)
			}
			if !strings.Contains(out, tc.want) {
				t.Errorf("message lacks %q:\n%s", tc.want, out)
			}
			if strings.Contains(out, canary) || strings.Contains(out, "second-line") || strings.Contains(out, "has space in it") {
				t.Errorf("the token leaked into the message:\n%s", out)
			}
		})
	}
}

// off removes the dashboard allow list, and IPv6 plus bare addresses pass.
func TestAllowListNormalization(t *testing.T) {
	out, err := runInstaller(t, t.TempDir(), []string{
		"--domain", "nebula.example.com", "--email", "me@example.com", "--dashboard-allow", " 203.0.113.7 ,fd7a:115c:a1e0::/48,,::1 ",
	}, "step_plan\n"+show)
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	wantLine(t, out, "allow=203.0.113.7,fd7a:115c:a1e0::/48,::1")

	dir := t.TempDir()
	if out, err = runInstaller(t, dir, []string{"--domain", "nebula.example.com", "--email", "me@example.com", "--dashboard-allow", "10.0.0.0/8"}, "step_plan\nsave_conf\n"); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	out, err = runInstaller(t, dir, []string{"--dashboard-allow", "off"}, "step_plan\n"+show)
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	wantLine(t, out, "allow=")
}

// HTTP-01 cannot work with the proxy off :80, and the installer says so.
func TestHTTP01WarningInSideBySideMode(t *testing.T) {
	args := []string{"--domain", "nebula.example.com", "--email", "me@example.com", "--proxy-http-port", "8080", "--proxy-https-port", "8443"}
	out, err := runInstaller(t, t.TempDir(), args, "step_plan\n")
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	if !strings.Contains(out, "cannot validate nebula.example.com over HTTP-01") {
		t.Errorf("no HTTP-01 warning:\n%s", out)
	}
	tok := tokenFile(t, canary)
	out, err = runInstaller(t, t.TempDir(), append(args, "--dashboard-dns01", "cloudflare", "--dns-token-file", tok), "step_plan\n")
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	if strings.Contains(out, "HTTP-01") {
		t.Errorf("a DNS-01 dashboard still warns about HTTP-01:\n%s", out)
	}
}

func readFile(t *testing.T, p string) string {
	t.Helper()
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// --update-to is what the dashboard's update runs: it installs the release for this
// run without remembering it, so a later plain re-run still upgrades. A pinned
// installation moves its pin (it asked for a fixed version), and one pinned to an
// image refuses (an update to a release cannot change it).
func TestUpdateToInstallsWithoutPinning(t *testing.T) {
	const showVersion = `echo "version=$VERSION"; echo "image=$IMAGE"` + "\n"
	dir := t.TempDir()
	out, err := runInstaller(t, dir, []string{"--update-to", "0.1.2"}, showVersion+"save_conf\n")
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	wantLine(t, out, "version=0.1.2")
	wantLine(t, out, "image=ghcr.io/nebulactrl/nebulactrl:0.1.2")
	if conf := readFile(t, filepath.Join(dir, "install.conf")); strings.Contains(conf, "0.1.2") {
		t.Errorf("--update-to was remembered:\n%s", conf)
	}
	out, err = runInstaller(t, dir, nil, showVersion)
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	wantLine(t, out, "version=latest")

	// A pinned installation moves its pin along.
	pinned := t.TempDir()
	if out, err = runInstaller(t, pinned, []string{"--version", "0.1.0"}, "save_conf\n"); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	out, err = runInstaller(t, pinned, []string{"--update-to", "0.1.2"}, showVersion+"save_conf\n")
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	wantLine(t, out, "version=0.1.2")
	if conf := readFile(t, filepath.Join(pinned, "install.conf")); !strings.Contains(conf, "PIN_VERSION=0.1.2") {
		t.Errorf("the pin did not move to 0.1.2:\n%s", conf)
	}

	// An image pin cannot follow a release, and a malformed version is refused.
	imagePinned := t.TempDir()
	if out, err = runInstaller(t, imagePinned, []string{"--image", "nebulactrl:local"}, "save_conf\n"); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	if out, err = runInstaller(t, imagePinned, []string{"--update-to", "0.1.2"}, ""); err == nil || !strings.Contains(out, "pinned image") {
		t.Errorf("an update over an image pin was not refused (%v):\n%s", err, out)
	}
	if out, err = runInstaller(t, t.TempDir(), []string{"--update-to", "0.1.2; reboot"}, ""); err == nil || !strings.Contains(out, "needs a release version") {
		t.Errorf("a malformed version was not refused (%v):\n%s", err, out)
	}
}

func exitCode(err error) int {
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		return ee.ExitCode()
	}
	return -1
}

// rollbackRig writes an installation whose upgrade already rewrote .env and the
// compose file, with the copies step_remember keeps, and replaces docker, compose
// and the readiness wait so the calls are recorded in the returned log path.
func rollbackRig(t *testing.T) (dir, log, stubsBody string) {
	t.Helper()
	dir = t.TempDir()
	log = filepath.Join(dir, "calls.log")
	for name, content := range map[string]string{
		".env":                        "NEBULACTRL_IMAGE=ghcr.io/nebulactrl/nebulactrl:0.1.2\n",
		".env.previous":               "NEBULACTRL_IMAGE=ghcr.io/nebulactrl/nebulactrl:latest\n",
		"docker-compose.yml":          "new compose\n",
		"docker-compose.yml.previous": "old compose\n",
	} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	stubsBody = `
docker() { echo "docker $*" >>'` + log + `'; }
dc() { echo "dc $*" >>'` + log + `'; }
UPGRADING=1; PREV_IMAGE=ghcr.io/nebulactrl/nebulactrl:latest; PREV_IMAGE_ID=sha256:old
`
	return dir, log, stubsBody
}

// The new control plane is not healthy: the previous image, compose file and
// settings come back, the previous version runs again, the installer exits 4 and
// the copies (they hold secrets) are gone.
func TestUnhealthyUpgradeRollsBack(t *testing.T) {
	dir, log, body := rollbackRig(t)
	out, err := runInstaller(t, dir, nil, body+"wait_ready() { return 0; }\nstart_failed 'the control plane did not become healthy'\n")
	if exitCode(err) != 4 {
		t.Fatalf("exit %d, want 4 (%v):\n%s", exitCode(err), err, out)
	}
	if !strings.Contains(out, "The installer rolled back") {
		t.Errorf("the rollback is not announced:\n%s", out)
	}
	if got := readFile(t, filepath.Join(dir, ".env")); got != "NEBULACTRL_IMAGE=ghcr.io/nebulactrl/nebulactrl:latest\n" {
		t.Errorf(".env was not restored: %q", got)
	}
	if got := readFile(t, filepath.Join(dir, "docker-compose.yml")); got != "old compose\n" {
		t.Errorf("the compose file was not restored: %q", got)
	}
	calls := readFile(t, log)
	if !strings.Contains(calls, "docker tag sha256:old ghcr.io/nebulactrl/nebulactrl:latest") {
		t.Errorf("the previous image was not tagged back (a moved latest would run the new image):\n%s", calls)
	}
	if !strings.Contains(calls, "dc up -d --remove-orphans --wait") {
		t.Errorf("the previous version was not started:\n%s", calls)
	}
	for _, leftover := range []string{".env.previous", "docker-compose.yml.previous"} {
		if _, err := os.Stat(filepath.Join(dir, leftover)); err == nil {
			t.Errorf("%s was left behind", leftover)
		}
	}
}

// A rollback that does not come up either is a plain failure (exit 1) that says the control plane is down.
func TestFailedRollbackSaysTheControlPlaneIsDown(t *testing.T) {
	dir, _, body := rollbackRig(t)
	out, err := runInstaller(t, dir, nil, body+"wait_ready() { return 1; }\nstart_failed 'the control plane did not become healthy'\n")
	if exitCode(err) != 1 {
		t.Fatalf("exit %d, want 1 (%v):\n%s", exitCode(err), err, out)
	}
	if !strings.Contains(out, "did not work either: the control plane is down") {
		t.Errorf("the failure is not explained:\n%s", out)
	}
}

// A fresh install has nothing to roll back to: it stops, and touches no image.
func TestUnhealthyFreshInstallStops(t *testing.T) {
	dir, log, body := rollbackRig(t)
	out, err := runInstaller(t, dir, nil, body+"UPGRADING=0\nstart_failed 'the control plane did not become healthy'\n")
	if exitCode(err) != 1 {
		t.Fatalf("exit %d, want 1 (%v):\n%s", exitCode(err), err, out)
	}
	if _, err := os.Stat(log); err == nil {
		t.Errorf("a fresh install touched docker:\n%s", readFile(t, log))
	}
	if got := readFile(t, filepath.Join(dir, ".env")); !strings.Contains(got, ":0.1.2") {
		t.Errorf(".env was rewritten: %q", got)
	}
}

// step_remember keeps the running image by ID, the settings and the compose file;
// a new installation keeps nothing.
func TestRememberKeepsWhatARollbackNeeds(t *testing.T) {
	dir, _, _ := rollbackRig(t)
	for _, f := range []string{".env.previous", "docker-compose.yml.previous"} {
		if err := os.Remove(filepath.Join(dir, f)); err != nil {
			t.Fatal(err)
		}
	}
	body := `
have() { return 0; }
dc() { echo cid123; }
docker() { echo sha256:running; }
step_remember
echo "upgrading=$UPGRADING"; echo "prev=$PREV_IMAGE"; echo "id=$PREV_IMAGE_ID"
trap - EXIT
`
	out, err := runInstaller(t, dir, nil, body)
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	wantLine(t, out, "upgrading=1")
	wantLine(t, out, "prev=ghcr.io/nebulactrl/nebulactrl:0.1.2")
	wantLine(t, out, "id=sha256:running")
	if got := readFile(t, filepath.Join(dir, ".env.previous")); got != readFile(t, filepath.Join(dir, ".env")) {
		t.Errorf(".env.previous differs from .env: %q", got)
	}
	if got := readFile(t, filepath.Join(dir, "docker-compose.yml.previous")); got != "new compose\n" {
		t.Errorf("compose copy: %q", got)
	}

	fresh := t.TempDir()
	out, err = runInstaller(t, fresh, nil, body)
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	wantLine(t, out, "upgrading=0")
	if _, err := os.Stat(filepath.Join(fresh, ".env.previous")); err == nil {
		t.Error("a new installation kept a copy of .env")
	}
}

// The built-in services: .env gets a generated admin identity (once), the registry's secret and
// auth directory, and --instance-dir points the data at host directories and is remembered.
func TestInstanceServicesEnvAndInstanceDir(t *testing.T) {
	dir := t.TempDir()
	data := filepath.Join(t.TempDir(), "data")
	body := "step_plan\nstep_files\n"
	out, err := runInstaller(t, dir, []string{"--port", "8100", "--no-local-server", "--instance-dir", data}, body)
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	env := readFile(t, filepath.Join(dir, ".env"))
	for _, want := range []string{
		"NEBULACTRL_REGISTRY_AUTH_DIR=" + dir + "/registry-auth",
		"NEBULACTRL_S3_DATA=" + data + "/s3",
		"NEBULACTRL_REGISTRY_DATA=" + data + "/registry",
		"NEBULACTRL_LOKI_DATA=" + data + "/loki",
	} {
		if !strings.Contains(env, want+"\n") {
			t.Errorf(".env lacks %q:\n%s", want, env)
		}
	}
	key, secret, regsec := envValue(env, "NEBULACTRL_S3_ADMIN_KEY"), envValue(env, "NEBULACTRL_S3_ADMIN_SECRET"), envValue(env, "NEBULACTRL_REGISTRY_HTTP_SECRET")
	if len(key) != 20 || key != strings.ToUpper(key) || len(secret) < 32 || len(regsec) < 32 {
		t.Errorf("generated keys have the wrong shape: key=%q secret=%q registry=%q", key, secret, regsec)
	}
	for _, name := range []string{"master.toml", "registry.yaml", "loki.yaml"} {
		b, err := os.ReadFile(filepath.Join(dir, "instance", name))
		if err != nil || len(b) == 0 {
			t.Errorf("instance/%s was not written: %v", name, err)
		}
	}
	if !strings.Contains(readFile(t, filepath.Join(dir, "install.conf")), data) {
		t.Error("--instance-dir was not remembered in install.conf")
	}

	// A plain re-run keeps the identity (the S3 holds a key pair that must not change) and the directory.
	out, err = runInstaller(t, dir, []string{"--port", "8100", "--no-local-server"}, body)
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	again := readFile(t, filepath.Join(dir, ".env"))
	for _, k := range []string{"NEBULACTRL_S3_ADMIN_KEY", "NEBULACTRL_S3_ADMIN_SECRET", "NEBULACTRL_REGISTRY_HTTP_SECRET", "NEBULACTRL_S3_DATA"} {
		if envValue(again, k) != envValue(env, k) || envValue(again, k) == "" {
			t.Errorf("%s changed or vanished on a re-run: %q -> %q", k, envValue(env, k), envValue(again, k))
		}
	}

	// Without --instance-dir the compose file's named volumes are used: no *_DATA settings.
	plain := t.TempDir()
	if out, err = runInstaller(t, plain, []string{"--port", "8100", "--no-local-server"}, body); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	if env := readFile(t, filepath.Join(plain, ".env")); strings.Contains(env, "_DATA=") {
		t.Errorf("a default install must use named volumes:\n%s", env)
	}
}

func envValue(env, key string) string {
	for _, l := range strings.Split(env, "\n") {
		if v, ok := strings.CutPrefix(l, key+"="); ok {
			return v
		}
	}
	return ""
}

// The disk warning counts the disk of --instance-dir (the nearest existing parent), not the install directory.
func TestInstanceDiskWarning(t *testing.T) {
	out, err := runInstaller(t, t.TempDir(), []string{"--instance-dir", "/nonexistent/deeper/dir"}, `
df() { printf 'Filesystem 1024-blocks Used Available Capacity Mounted\n/dev/x 1 1 41943040 1%% /\n'; }
NC_WARNINGS=()
check_instance_disk
printf 'warnings=%s\n' "${#NC_WARNINGS[@]}"
printf '%s\n' "${NC_WARNINGS[@]}"
`)
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	wantLine(t, out, "warnings=1")
	if !strings.Contains(out, "less than 60 GB free") || !strings.Contains(out, "40 GB free") || !strings.Contains(out, "--instance-dir") {
		t.Errorf("the warning does not say what is wrong and what to do:\n%s", out)
	}

	out, err = runInstaller(t, t.TempDir(), []string{"--instance-dir", "/"}, `
df() { printf 'Filesystem 1024-blocks Used Available Capacity Mounted\n/dev/x 1 1 83886080 1%% /\n'; }
NC_WARNINGS=()
check_instance_disk
printf 'warnings=%s\n' "${#NC_WARNINGS[@]}"
`)
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	wantLine(t, out, "warnings=0")
}
