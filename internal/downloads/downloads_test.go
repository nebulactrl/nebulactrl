package downloads

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/stretchr/testify/require"
)

func hexSum(b []byte) string {
	s := sha256.Sum256(b)
	return hex.EncodeToString(s[:])
}

// fixture writes an image-shaped CLI directory: a different body per platform
// and a SHA256SUMS as `sha256sum */nebulactrl*` prints it. The bodies are
// returned by "<os>-<arch>".
func fixture(t *testing.T) (dir string, bodies map[string][]byte) {
	t.Helper()
	dir = t.TempDir()
	bodies = map[string][]byte{}
	var sums strings.Builder
	for _, goos := range oses {
		for _, goarch := range arches {
			p := goos + "-" + goarch
			body := []byte("#!/bin/sh\necho nebulactrl " + p + "\n")
			name := binaryName(goos)
			require.NoError(t, os.MkdirAll(filepath.Join(dir, p), 0o755))
			require.NoError(t, os.WriteFile(filepath.Join(dir, p, name), body, 0o755))
			bodies[p] = body
			fmt.Fprintf(&sums, "%s  %s/%s\n", hexSum(body), p, name)
		}
	}
	require.NoError(t, os.WriteFile(filepath.Join(dir, "SHA256SUMS"), []byte(sums.String()), 0o644))
	return dir, bodies
}

func serve(t *testing.T, dir, publicURL string) *httptest.Server {
	t.Helper()
	r := chi.NewRouter()
	New(dir, publicURL, slog.New(slog.NewTextHandler(io.Discard, nil))).Mount(r)
	srv := httptest.NewServer(r)
	t.Cleanup(srv.Close)
	return srv
}

func get(t *testing.T, url string) (*http.Response, string) {
	t.Helper()
	res, err := http.Get(url)
	require.NoError(t, err)
	defer res.Body.Close()
	b, err := io.ReadAll(res.Body)
	require.NoError(t, err)
	return res, string(b)
}

func TestBinaryServedWithChecksumHeader(t *testing.T) {
	dir, bodies := fixture(t)
	srv := serve(t, dir, "")

	for p, want := range bodies {
		goos, goarch, _ := strings.Cut(p, "-")
		res, body := get(t, srv.URL+"/download/cli/"+goos+"/"+goarch)
		require.Equal(t, http.StatusOK, res.StatusCode, p)
		require.Equal(t, string(want), body, p)
		require.Equal(t, hexSum(want), res.Header.Get("X-Checksum-Sha256"), p)
		require.Equal(t, `"`+hexSum(want)+`"`, res.Header.Get("ETag"), p)
		require.Equal(t, "application/octet-stream", res.Header.Get("Content-Type"))
		require.Equal(t, "nosniff", res.Header.Get("X-Content-Type-Options"))
		wantName := "nebulactrl"
		if goos == "windows" {
			wantName = "nebulactrl.exe"
		}
		require.Equal(t, `attachment; filename="`+wantName+`"`, res.Header.Get("Content-Disposition"), p)
	}

	// The checksum header is the one SHA256SUMS lists for that platform.
	res, sums := get(t, srv.URL+"/download/cli/SHA256SUMS")
	require.Equal(t, http.StatusOK, res.StatusCode)
	require.Contains(t, res.Header.Get("Content-Type"), "text/plain")
	res, _ = get(t, srv.URL+"/download/cli/linux/amd64")
	require.Contains(t, sums, res.Header.Get("X-Checksum-Sha256")+"  linux-amd64/nebulactrl\n")
	require.Contains(t, sums, "  windows-arm64/nebulactrl.exe\n")
}

func TestBinaryConditionalAndHead(t *testing.T) {
	dir, bodies := fixture(t)
	srv := serve(t, dir, "")

	req, _ := http.NewRequest(http.MethodGet, srv.URL+"/download/cli/darwin/arm64", nil)
	req.Header.Set("If-None-Match", `"`+hexSum(bodies["darwin-arm64"])+`"`)
	res, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	res.Body.Close()
	require.Equal(t, http.StatusNotModified, res.StatusCode)

	res, err = http.Head(srv.URL + "/download/cli/darwin/arm64")
	require.NoError(t, err)
	res.Body.Close()
	require.Equal(t, http.StatusOK, res.StatusCode)
	require.Equal(t, hexSum(bodies["darwin-arm64"]), res.Header.Get("X-Checksum-Sha256"))
}

// Nothing the client sends reaches the file system: only the six known
// platforms resolve, whatever the path says.
func TestPlatformValidationAndNoTraversal(t *testing.T) {
	dir, _ := fixture(t)
	// Bait next to the CLI directory and inside it.
	require.NoError(t, os.WriteFile(filepath.Join(filepath.Dir(dir), "secret"), []byte("TOP-SECRET"), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "secret"), []byte("TOP-SECRET"), 0o600))
	srv := serve(t, dir, "")

	for _, p := range []string{
		"/download/cli/plan9/amd64",
		"/download/cli/linux/386",
		"/download/cli/Linux/amd64",
		"/download/cli/linux/AMD64",
		"/download/cli/linux/arm",
		"/download/cli/../amd64",
		"/download/cli/linux/..",
		"/download/cli/%2e%2e/%2e%2e",
		"/download/cli/..%2fsecret/amd64",
		"/download/cli/linux%2f..%2f../amd64",
		"/download/cli/linux/amd64%2f..%2f..%2fsecret",
		"/download/cli/linux/amd64/",
		"/download/cli/linux-amd64/nebulactrl",
		"/download/cli/%00/amd64",
		"/download/cli/ /amd64",
		"/download/cli//",
		"/download/cli/SHA256SUMS/amd64",
	} {
		res, body := get(t, srv.URL+p)
		require.Equal(t, http.StatusNotFound, res.StatusCode, p)
		require.NotContains(t, body, "TOP-SECRET", p)
		require.NotEqual(t, "application/octet-stream", res.Header.Get("Content-Type"), p)
	}

	res, body := get(t, srv.URL+"/download/cli/plan9/amd64")
	require.Equal(t, http.StatusNotFound, res.StatusCode)
	require.Equal(t, "application/problem+json", res.Header.Get("Content-Type"))
	require.Contains(t, body, "cli_platform_unknown")
	require.Contains(t, body, "linux, darwin, windows")
}

func TestSumsParsing(t *testing.T) {
	good := hexSum([]byte("x"))
	got := parseSums([]byte(strings.Join([]string{
		good + "  linux-amd64/nebulactrl",
		good + " *darwin-arm64/nebulactrl", // binary-mode marker
		strings.ToUpper(good) + "  windows-amd64/nebulactrl.exe",
		good + "  linux-amd64/other",          // wrong file name
		good + "  windows-amd64/nebulactrl",   // wrong file name for the platform
		good + "  plan9-amd64/nebulactrl",     // unknown OS
		good + "  ../linux-arm64/nebulactrl",  // not a platform directory
		good + "  linux-arm64/sub/nebulactrl", // nested
		"abc  linux-arm64/nebulactrl",         // short digest
		"garbage",
	}, "\n")))
	require.Equal(t, map[string]string{
		"linux-amd64":   good,
		"darwin-arm64":  good,
		"windows-amd64": good,
	}, got)
}

func TestBinaryMissingFromImage(t *testing.T) {
	dir, _ := fixture(t)
	// Listed in SHA256SUMS but the file is gone, and a platform that is not listed.
	require.NoError(t, os.Remove(filepath.Join(dir, "linux-arm64", "nebulactrl")))
	sums, err := os.ReadFile(filepath.Join(dir, "SHA256SUMS"))
	require.NoError(t, err)
	var kept []string
	for _, l := range strings.Split(string(sums), "\n") {
		if l != "" && !strings.Contains(l, "darwin-amd64") {
			kept = append(kept, l)
		}
	}
	require.NoError(t, os.WriteFile(filepath.Join(dir, "SHA256SUMS"), []byte(strings.Join(kept, "\n")+"\n"), 0o644))
	srv := serve(t, dir, "")

	for _, p := range []string{"/download/cli/linux/arm64", "/download/cli/darwin/amd64"} {
		res, body := get(t, srv.URL+p)
		require.Equal(t, http.StatusNotFound, res.StatusCode, p)
		require.Contains(t, body, "cli_platform_missing", p)
	}
	res, _ := get(t, srv.URL+"/download/cli/linux/amd64")
	require.Equal(t, http.StatusOK, res.StatusCode)
}

// A development build has no binaries: a clear 404 on every route.
func TestUnavailableInDev(t *testing.T) {
	empty := t.TempDir() // a directory without SHA256SUMS
	for name, dir := range map[string]string{"no dir configured": "", "dir without SHA256SUMS": empty, "missing dir": filepath.Join(empty, "nope")} {
		t.Run(name, func(t *testing.T) {
			srv := serve(t, dir, "")
			require.False(t, New(dir, "", nil).Available())

			for _, p := range []string{"/download/cli/linux/amd64", "/download/cli/SHA256SUMS"} {
				res, body := get(t, srv.URL+p)
				require.Equal(t, http.StatusNotFound, res.StatusCode, p)
				require.Equal(t, "application/problem+json", res.Header.Get("Content-Type"), p)
				require.Contains(t, body, "cli_unavailable", p)
				require.Contains(t, body, "development build", p)
				require.Contains(t, body, "go build", p)
			}
			// An unknown platform is still "unknown", not "unavailable".
			res, body := get(t, srv.URL+"/download/cli/plan9/amd64")
			require.Equal(t, http.StatusNotFound, res.StatusCode)
			require.Contains(t, body, "cli_platform_unknown")

			// cli.sh: a 404 whose body is a script that prints the reason.
			res, body = get(t, srv.URL+"/cli.sh")
			require.Equal(t, http.StatusNotFound, res.StatusCode)
			require.Contains(t, body, "development build")
			shParses(t, body)
			out, err := runScript(t, body, nil)
			require.Error(t, err)
			require.Contains(t, out, "no CLI binaries")
		})
	}
}

// shParses checks that script is valid shell without executing it (`sh -n`).
func shParses(t *testing.T, script string) {
	t.Helper()
	cmd := exec.Command("sh", "-n")
	cmd.Stdin = strings.NewReader(script)
	out, err := cmd.CombinedOutput()
	require.NoError(t, err, string(out))
}

func runScript(t *testing.T, script string, env []string) (string, error) {
	t.Helper()
	cmd := exec.Command("sh")
	cmd.Stdin = strings.NewReader(script)
	cmd.Env = append([]string{"PATH=" + os.Getenv("PATH"), "HOME=" + t.TempDir()}, env...)
	out, err := cmd.CombinedOutput()
	return string(out), err
}

func TestInstallerContent(t *testing.T) {
	dir, _ := fixture(t)

	t.Run("public URL is embedded", func(t *testing.T) {
		srv := serve(t, dir, "https://nebula.example.com/")
		res, body := get(t, srv.URL+"/cli.sh")
		require.Equal(t, http.StatusOK, res.StatusCode)
		require.Equal(t, "text/x-shellscript; charset=utf-8", res.Header.Get("Content-Type"))
		require.Equal(t, "no-store", res.Header.Get("Cache-Control"))
		require.True(t, strings.HasPrefix(body, "#!/bin/sh\n"))
		require.Contains(t, body, `NC_URL=${NEBULACTRL_URL:-'https://nebula.example.com'}`)
		require.Contains(t, body, "curl -fsSL https://nebula.example.com/cli.sh | sh")
		require.NotContains(t, body, "{{")
		shParses(t, body)
	})

	t.Run("URL derived from the request, https behind a proxy", func(t *testing.T) {
		srv := serve(t, dir, "")
		req, _ := http.NewRequest(http.MethodGet, srv.URL+"/cli.sh", nil)
		req.Host = "nebula.internal:8443"
		req.Header.Set("X-Forwarded-Proto", "https")
		res, err := http.DefaultClient.Do(req)
		require.NoError(t, err)
		b, _ := io.ReadAll(res.Body)
		res.Body.Close()
		require.Contains(t, string(b), `NC_URL=${NEBULACTRL_URL:-'https://nebula.internal:8443'}`)
	})

	t.Run("an URL that is unsafe in shell text is never embedded", func(t *testing.T) {
		srv := serve(t, dir, "https://x.example/';rm -rf ~;'")
		_, body := get(t, srv.URL+"/cli.sh")
		require.Contains(t, body, `NC_URL=${NEBULACTRL_URL:-''}`)
		require.NotContains(t, body, "rm -rf ~")
		require.NotContains(t, body, "x.example")
		shParses(t, body)
		out, err := runScript(t, body, nil)
		require.Error(t, err)
		require.Contains(t, out, "control plane URL is unknown")
	})

	t.Run("sh -n and shellcheck", func(t *testing.T) {
		srv := serve(t, dir, "https://nebula.example.com")
		_, body := get(t, srv.URL+"/cli.sh")
		shParses(t, body)
		if _, err := exec.LookPath("shellcheck"); err != nil {
			t.Skip("shellcheck is not installed")
		}
		cmd := exec.Command("shellcheck", "--shell=sh", "-")
		cmd.Stdin = strings.NewReader(body)
		out, err := cmd.CombinedOutput()
		require.NoError(t, err, string(out))
	})
}

// hostPlatform maps the machine running the test to the platform the installer
// will pick; "" on a platform the installer does not support.
func hostPlatform() string {
	if (runtime.GOOS != "linux" && runtime.GOOS != "darwin") || (runtime.GOARCH != "amd64" && runtime.GOARCH != "arm64") {
		return ""
	}
	return runtime.GOOS + "-" + runtime.GOARCH
}

// The script end to end against a real server: download, verify, install.
func TestInstallerRuns(t *testing.T) {
	platform := hostPlatform()
	if platform == "" {
		t.Skip("the installer supports linux and darwin on amd64 and arm64")
	}
	for _, tool := range []string{"curl", "install", "awk", "mktemp"} {
		if _, err := exec.LookPath(tool); err != nil {
			t.Skipf("%s is not installed", tool)
		}
	}

	t.Run("installs and verifies", func(t *testing.T) {
		dir, bodies := fixture(t)
		srv := serve(t, dir, "")
		_, script := get(t, srv.URL+"/cli.sh")
		dest := filepath.Join(t.TempDir(), "bin")

		out, err := runScript(t, script, []string{"NEBULACTRL_INSTALL_DIR=" + dest})
		require.NoError(t, err, out)
		require.Contains(t, out, "Checksum verified")
		got, err := os.ReadFile(filepath.Join(dest, "nebulactrl"))
		require.NoError(t, err)
		require.Equal(t, bodies[platform], got)
		st, err := os.Stat(filepath.Join(dest, "nebulactrl"))
		require.NoError(t, err)
		require.NotZero(t, st.Mode()&0o111, "the binary is executable")
		require.Contains(t, out, "nebulactrl login --url "+srv.URL)
	})

	t.Run("a corrupted download installs nothing", func(t *testing.T) {
		dir, _ := fixture(t)
		// The binary no longer matches SHA256SUMS.
		require.NoError(t, os.WriteFile(filepath.Join(dir, platform, "nebulactrl"), []byte("tampered"), 0o755))
		srv := serve(t, dir, "")
		_, script := get(t, srv.URL+"/cli.sh")
		dest := filepath.Join(t.TempDir(), "bin")

		out, err := runScript(t, script, []string{"NEBULACTRL_INSTALL_DIR=" + dest})
		require.Error(t, err)
		require.Contains(t, out, "checksum mismatch")
		_, statErr := os.Stat(filepath.Join(dest, "nebulactrl"))
		require.True(t, os.IsNotExist(statErr), "nothing was installed")
	})

	t.Run("without write access to /usr/local/bin it installs under HOME", func(t *testing.T) {
		if os.Geteuid() == 0 {
			t.Skip("root installs into /usr/local/bin")
		}
		if st, err := os.Stat("/usr/local/bin"); err == nil && st.IsDir() {
			if f, err := os.CreateTemp("/usr/local/bin", ".probe"); err == nil {
				f.Close()
				os.Remove(f.Name())
				t.Skip("/usr/local/bin is writable here")
			}
		}
		dir, bodies := fixture(t)
		srv := serve(t, dir, "")
		_, script := get(t, srv.URL+"/cli.sh")
		home := t.TempDir()

		cmd := exec.Command("sh")
		cmd.Stdin = strings.NewReader(script)
		cmd.Env = []string{"PATH=" + os.Getenv("PATH"), "HOME=" + home}
		out, err := cmd.CombinedOutput()
		require.NoError(t, err, string(out))
		got, err := os.ReadFile(filepath.Join(home, ".local", "bin", "nebulactrl"))
		require.NoError(t, err)
		require.Equal(t, bodies[platform], got)
		require.Contains(t, string(out), "is not on your PATH")
	})
}

func TestKnownPlatformAcceptsOnlyListedNames(t *testing.T) {
	goos, goarch, ok := knownPlatform("darwin", "arm64")
	require.True(t, ok)
	require.Equal(t, "darwin", goos)
	require.Equal(t, "arm64", goarch)
	for _, bad := range [][2]string{{"", ""}, {"linux", ""}, {"", "amd64"}, {"..", "amd64"}, {"linux", "../x"}, {"linux/", "amd64"}, {"Linux", "amd64"}, {"plan9", "amd64"}, {"linux", "riscv"}} {
		_, _, ok := knownPlatform(bad[0], bad[1])
		require.False(t, ok, "%q/%q", bad[0], bad[1])
	}
}
