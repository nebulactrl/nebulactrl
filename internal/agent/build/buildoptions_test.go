package build

import (
	"context"
	"io"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/go-git/go-git/v5/plumbing/transport"
	githttp "github.com/go-git/go-git/v5/plumbing/transport/http"
	"github.com/stretchr/testify/require"
	"golang.org/x/crypto/ssh"

	"github.com/nebulactrl/nebulactrl/internal/gitssh"
	"github.com/nebulactrl/nebulactrl/internal/gitssh/gitsshtest"
	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

// countArg counts the occurrences of want in args.
func countArg(args []string, want string) int {
	n := 0
	for _, a := range args {
		if a == want {
			n++
		}
	}
	return n
}

func TestNoCacheFlagsReachEveryBuildArgumentVector(t *testing.T) {
	plain := goodRequest()
	cached := goodRequest()
	cached.NoCache = true
	pushPlain, pushCached := goodPush("reg.example.com"), goodPush("reg.example.com")
	pushCached.NoCache = true

	vectors := map[string]struct{ off, on []string }{
		"dockerfile": {Args(plain, "/w/Dockerfile", "/w/iid", "/w/src"), Args(cached, "/w/Dockerfile", "/w/iid", "/w/src")},
		"railpack docker": {
			railpackBuildArgs(plain, "/w/plan.json", "/w/iid", "/w/src", "docker"),
			railpackBuildArgs(cached, "/w/plan.json", "/w/iid", "/w/src", "docker"),
		},
		"railpack buildx": {
			railpackBuildArgs(plain, "/w/plan.json", "/w/iid", "/w/src", "buildx"),
			railpackBuildArgs(cached, "/w/plan.json", "/w/iid", "/w/src", "buildx"),
		},
		"push dockerfile": {pushArgs(pushPlain, "/w/Dockerfile", "/w/meta", "/w/src", false), pushArgs(pushCached, "/w/Dockerfile", "/w/meta", "/w/src", false)},
		"push railpack":   {pushArgs(pushPlain, "/w/plan.json", "/w/meta", "/w/src", true), pushArgs(pushCached, "/w/plan.json", "/w/meta", "/w/src", true)},
	}
	for name, v := range vectors {
		require.Zero(t, countArg(v.off, "--no-cache"), name)
		require.Zero(t, countArg(v.off, "--pull"), name)
		require.Equal(t, 1, countArg(v.on, "--no-cache"), name)
		require.Equal(t, 1, countArg(v.on, "--pull"), name)
		require.Equal(t, "/w/src", v.on[len(v.on)-1], name+": the context stays last")
		require.Len(t, v.on, len(v.off)+2, name+": nothing else changes")
	}
}

func TestNoCacheBuildOnDockerfileLogsAndPassesTheFlags(t *testing.T) {
	var got []string
	var lines []string
	b := &Builder{
		StateDir: t.TempDir(),
		Clone: func(_ context.Context, _ Request, dir string) error {
			return putFiles(dir, map[string]string{"Dockerfile": "FROM scratch\n"})
		},
		Run: func(_ context.Context, args []string, _ []string, _ io.Writer) error {
			got = args
			for i, a := range args {
				if a == "--iidfile" {
					return os.WriteFile(args[i+1], []byte("sha256:cafe\n"), 0o644)
				}
			}
			return nil
		},
	}
	req := goodRequest()
	req.NoCache = true
	_, err := b.Build(context.Background(), req, func(l string) { lines = append(lines, l) })
	require.NoError(t, err)
	require.Contains(t, got, "--no-cache")
	require.Contains(t, got, "--pull")
	require.Contains(t, strings.Join(lines, "\n"), "Deploying without cache")
	require.Contains(t, strings.Join(lines, "\n"), "$ docker build --progress=plain")

	// The next build of the same service is a normal one: the flag lives on the request.
	lines = nil
	req.NoCache = false
	_, err = b.Build(context.Background(), req, func(l string) { lines = append(lines, l) })
	require.NoError(t, err)
	require.NotContains(t, got, "--no-cache")
	require.NotContains(t, strings.Join(lines, "\n"), "without cache")
}

func TestNoCacheRailpackBuildPassesTheFlagsAndKeepsThemOnTheBuildxRetry(t *testing.T) {
	f := &fakeDocker{t: t, fixture: "node", containerd: true, nativeBuildErr: "failed: mergeop is not supported\n"}
	req := goodRequest()
	req.BuildType = ""
	req.NoCache = true
	_, err := railpackBuilder(t, f, nodeRepo()).Build(context.Background(), req, func(string) {})
	require.NoError(t, err)

	var builds [][]string
	for _, c := range f.calls {
		if c[0] == "build" || (c[0] == "buildx" && c[1] == "build") {
			builds = append(builds, c)
		}
	}
	require.Len(t, builds, 2, "the native build, then the retry on the managed BuildKit builder")
	require.Equal(t, "buildx", builds[1][0])
	for _, b := range builds {
		require.Contains(t, b, "--no-cache")
		require.Contains(t, b, "--pull")
	}
}

func TestNoCachePushedBuildPassesTheFlags(t *testing.T) {
	host, _ := registry(t)
	d := newPushDocker(t)
	req := goodPush(host)
	req.NoCache = true
	var lines []string
	_, err := pushBuilder(t, d).BuildAndPush(context.Background(), req, func(l string) { lines = append(lines, l) })
	require.NoError(t, err)
	require.Len(t, d.buildArgs, 1)
	require.Contains(t, d.buildArgs[0], "--no-cache")
	require.Contains(t, d.buildArgs[0], "--pull")
	require.Contains(t, strings.Join(lines, "\n"), "Deploying without cache")
}

func TestRequestRejectsAnUnknownSubmodulesMode(t *testing.T) {
	for _, ok := range []string{"", "auto", "on", "off"} {
		r := goodRequest()
		r.Submodules = ok
		require.NoError(t, r.Validate(), ok)
	}
	r := goodRequest()
	r.Submodules = "recursive"
	require.ErrorContains(t, r.Validate(), "submodules mode")
}

func putFiles(dir string, files map[string]string) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	for name, content := range files {
		p := filepath.Join(dir, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			return err
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			return err
		}
	}
	return nil
}

// ---- submodule credentials ----

func httpSubmoduleAuth(parent, token string) *submoduleAuth {
	u, _ := url.Parse(parent)
	return &submoduleAuth{
		req: Request{GitURL: parent, Token: token}, parentHTTP: u, logf: func(string) {},
		parentAuth:    &githttp.BasicAuth{Username: agentprotocol.DefaultGitUsername, Password: token},
		parentExplain: func(err error) error { return err },
	}
}

func basicToken(t *testing.T, a transport.AuthMethod) string {
	t.Helper()
	b, ok := a.(*githttp.BasicAuth)
	require.True(t, ok, "%T", a)
	return b.Password
}

func TestSubmoduleCredentialsNeverLeaveTheParentsHost(t *testing.T) {
	a := httpSubmoduleAuth("https://github.com/acme/app.git", "tok-123456")

	// Same host over https: the parent's token.
	tg, err := a.target("https://github.com/acme/lib.git")
	require.NoError(t, err)
	require.Equal(t, "tok-123456", basicToken(t, tg.auth))

	// Another host, a look-alike host and a different port get nothing.
	for _, other := range []string{"https://gitlab.com/acme/lib.git", "https://github.com.evil.example/acme/lib.git", "https://github.com:8443/acme/lib.git", "http://github.com/acme/lib.git"} {
		tg, err = a.target(other)
		require.NoError(t, err, other)
		require.Nil(t, tg.auth, other)
		require.Equal(t, other, tg.url)
	}

	// A relative URL resolves against the parent's remote: same credentials.
	tg, err = a.target("../lib.git")
	require.NoError(t, err)
	require.Equal(t, "../lib.git", tg.url)
	require.Equal(t, "tok-123456", basicToken(t, tg.auth))

	// ssh on the parent's host is fetched over https with the token (git@github.com:org/lib.git).
	for _, sshURL := range []string{"git@github.com:acme/lib.git", "ssh://git@github.com/acme/lib.git"} {
		tg, err = a.target(sshURL)
		require.NoError(t, err, sshURL)
		require.Equal(t, "https://github.com/acme/lib.git", tg.url, sshURL)
		require.Equal(t, "tok-123456", basicToken(t, tg.auth), sshURL)
	}
	// ssh to another host cannot be authenticated.
	_, err = a.target("git@gitlab.com:acme/lib.git")
	require.ErrorContains(t, err, "no credentials")

	// A repository without a token fetches submodules anonymously.
	anon := httpSubmoduleAuth("https://github.com/acme/app.git", "")
	anon.parentAuth = nil
	tg, err = anon.target("https://github.com/acme/lib.git")
	require.NoError(t, err)
	require.Nil(t, tg.auth)
	_, err = anon.target("git@github.com:acme/lib.git")
	require.ErrorContains(t, err, "no credentials")
}

func TestSubmoduleURLsFromAnUntrustedGitmodulesAreRefused(t *testing.T) {
	a := httpSubmoduleAuth("https://github.com/acme/app.git", "tok-123456")
	for _, bad := range []string{"", "file:///etc", "/var/lib/nebulactrl-agent", "ext::sh -c id", "ftp://example.com/x.git", "https://user:pw@github.com/acme/lib.git"} {
		_, err := a.target(bad)
		require.Error(t, err, "%q", bad)
	}
	tg, err := a.target("git://example.com/lib.git")
	require.NoError(t, err)
	require.Nil(t, tg.auth, "the git protocol carries no credentials")

	require.NoError(t, checkSubmodulePath("vendor/lib", "vendor/lib"))
	for _, bad := range [][2]string{{"x", "../escape"}, {"x", "/abs"}, {"x", "a/.git/b"}, {"../x", "ok"}, {"x", ""}, {"x", "a\\b"}} {
		require.Error(t, checkSubmodulePath(bad[0], bad[1]), "%q", bad)
	}
}

// ---- cloning submodules (real go-git against a pinned-host-key SSH git server) ----

type submoduleRepo struct {
	url, sha string
	req      Request
	srv      *gitsshtest.Server
}

// newSubmoduleRepo serves app.git over SSH. app has vendor/lib (lib.git) and lib
// has deps/nested (nested.git), all on the same server, so one deploy key and
// one pinned host key cover the whole tree. libURL overrides the submodule URL
// written to app's .gitmodules.
func newSubmoduleRepo(t *testing.T, libURL string) submoduleRepo {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git binary not available")
	}
	root := t.TempDir()
	key, err := gitssh.Generate("t")
	require.NoError(t, err)
	authorized, _, _, _, err := ssh.ParseAuthorizedKey([]byte(key.PublicKey))
	require.NoError(t, err)
	host := gitsshtest.NewHostSigner(t)
	srv := gitsshtest.Start(t, root, host, authorized)
	base := "ssh://git@" + srv.Addr
	ep, err := gitssh.ParseURL(base + "/x.git")
	require.NoError(t, err)
	pin := gitssh.NewHostKey(ep.Addr(), host.PublicKey()).Line()

	git := func(dir string, args ...string) string {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-c", "user.name=t", "-c", "user.email=t@example.com", "-c", "init.defaultBranch=main", "-c", "commit.gpgsign=false", "-c", "protocol.file.allow=always"}, args...)...)
		cmd.Dir = dir
		out, err := cmd.CombinedOutput()
		require.NoError(t, err, string(out))
		return strings.TrimSpace(string(out))
	}
	// repo makes a bare repository named name in root from files, plus one gitlink.
	repo := func(name string, files map[string]string, linkPath, linkURL, linkSHA string) string {
		work := t.TempDir()
		git(work, "init", "-q")
		require.NoError(t, putFiles(work, files))
		if linkPath != "" {
			require.NoError(t, os.WriteFile(filepath.Join(work, ".gitmodules"), []byte("[submodule \""+linkPath+"\"]\n\tpath = "+linkPath+"\n\turl = "+linkURL+"\n"), 0o644))
		}
		git(work, "add", "-A")
		if linkPath != "" { // after add -A, which would drop a gitlink without a worktree directory
			git(work, "update-index", "--add", "--cacheinfo", "160000,"+linkSHA+","+linkPath)
		}
		git(work, "commit", "-q", "-m", "init")
		git(root, "clone", "-q", "--bare", work, name+".git")
		return git(work, "rev-parse", "HEAD")
	}
	nestedSHA := repo("nested", map[string]string{"nested.txt": "n"}, "", "", "")
	libSHA := repo("lib", map[string]string{"lib.txt": "l"}, "deps/nested", base+"/nested.git", nestedSHA)
	if libURL == "" {
		libURL = base + "/lib.git"
	}
	appSHA := repo("app", map[string]string{"Dockerfile": "FROM scratch\n"}, "vendor/lib", libURL, libSHA)
	return submoduleRepo{
		url: base + "/app.git", sha: appSHA, srv: srv,
		req: Request{DeploymentID: "d", ServiceSlug: "web", GitURL: base + "/app.git", CommitSHA: appSHA, SSHKey: string(key.PrivatePEM), KnownHosts: []string{pin}},
	}
}

func cloneSubmodules(t *testing.T, r submoduleRepo, mode string) (dir string, logs []string, err error) {
	t.Helper()
	req := r.req
	req.Submodules = mode
	req.Logf = func(l string) { logs = append(logs, l) }
	require.NoError(t, req.Validate())
	dir = filepath.Join(t.TempDir(), "src")
	err = gitClone(context.Background(), req, dir)
	return dir, logs, err
}

func TestCloneChecksSubmodulesOutRecursivelyWithTheDeployKey(t *testing.T) {
	r := newSubmoduleRepo(t, "")
	for _, mode := range []string{"", "auto", "on"} {
		dir, logs, err := cloneSubmodules(t, r, mode)
		require.NoError(t, err, "mode %q", mode)
		require.FileExists(t, filepath.Join(dir, "Dockerfile"))
		require.FileExists(t, filepath.Join(dir, "vendor", "lib", "lib.txt"), "mode %q", mode)
		require.FileExists(t, filepath.Join(dir, "vendor", "lib", "deps", "nested", "nested.txt"), "nested submodules are followed")
		require.Contains(t, strings.Join(logs, "\n"), "Fetching submodule vendor/lib from ssh://git@")
	}
}

func TestCloneWithSubmodulesOffLeavesThemEmpty(t *testing.T) {
	r := newSubmoduleRepo(t, "")
	dir, logs, err := cloneSubmodules(t, r, "off")
	require.NoError(t, err)
	require.FileExists(t, filepath.Join(dir, "Dockerfile"))
	require.NoFileExists(t, filepath.Join(dir, "vendor", "lib", "lib.txt"))
	require.Empty(t, logs)
}

func TestCloneSubmoduleOnAnotherSSHHostIsAWarningInAutoAndAnErrorInOn(t *testing.T) {
	// Another port is another host: the deploy key and its pinned host key do not apply.
	r := newSubmoduleRepo(t, "ssh://git@127.0.0.1:1/lib.git")

	dir, logs, err := cloneSubmodules(t, r, "auto")
	require.NoError(t, err, "auto keeps building the way it did before submodule support")
	require.FileExists(t, filepath.Join(dir, "Dockerfile"))
	require.NoFileExists(t, filepath.Join(dir, "vendor", "lib", "lib.txt"))
	require.Contains(t, strings.Join(logs, "\n"), "Warning: submodule vendor/lib skipped")
	require.Contains(t, strings.Join(logs, "\n"), "no credentials for")

	// Compare with a clone that skips submodules: the parent clone is the only
	// connection that authenticates, so the failing submodule adds none.
	base := r.srv.AuthAttempts()
	_, _, err = cloneSubmodules(t, r, "off")
	require.NoError(t, err)
	parentOnly := r.srv.AuthAttempts() - base
	base = r.srv.AuthAttempts()
	_, _, err = cloneSubmodules(t, r, "on")
	require.ErrorContains(t, err, "submodule vendor/lib")
	require.ErrorContains(t, err, "no credentials")
	require.Equal(t, parentOnly, r.srv.AuthAttempts()-base, "the key was never offered to the other host")
}

func TestCloneSubmoduleWithAFileURLIsRefused(t *testing.T) {
	r := newSubmoduleRepo(t, "file:///etc")
	_, _, err := cloneSubmodules(t, r, "on")
	require.ErrorContains(t, err, "unsupported url scheme")
	dir, logs, err := cloneSubmodules(t, r, "auto")
	require.NoError(t, err)
	require.NoDirExists(t, filepath.Join(dir, "vendor", "lib", "etc"))
	require.Contains(t, strings.Join(logs, "\n"), "unsupported url scheme")
}

func TestCloneResolvesARelativeSubmoduleURLAgainstTheParentWithItsCredentials(t *testing.T) {
	r := newSubmoduleRepo(t, "../lib.git")
	dir, _, err := cloneSubmodules(t, r, "on")
	require.NoError(t, err)
	require.FileExists(t, filepath.Join(dir, "vendor", "lib", "lib.txt"))
}
