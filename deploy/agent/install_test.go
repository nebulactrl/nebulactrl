package agentdeploy

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"text/template"
)

func render(t *testing.T, version string) string {
	t.Helper()
	tmpl, err := template.New("install-agent.sh").Parse(InstallScriptTemplate)
	if err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	err = tmpl.Execute(&buf, struct{ DefaultURL, Version, ReleasePubKey string }{
		DefaultURL: "https://nebula.example.com", Version: version, ReleasePubKey: "RWSfYAoo0hKcaKeFYcOsofTSgfZdb2GVo/jjE4pfxaDYSi4SSsC6inQP",
	})
	if err != nil {
		t.Fatal(err)
	}
	return buf.String()
}

func TestRenderedScriptIsValidShell(t *testing.T) {
	sh, err := exec.LookPath("sh")
	if err != nil {
		t.Skip("sh not available")
	}
	p := filepath.Join(t.TempDir(), "install-agent.sh")
	if err := os.WriteFile(p, []byte(render(t, "0.1.2")), 0o600); err != nil {
		t.Fatal(err)
	}
	if out, err := exec.Command(sh, "-n", p).CombinedOutput(); err != nil {
		t.Fatalf("sh -n: %v\n%s", err, out)
	}
}

// The agent runs as root: the binary is installed only when its release signature verifies.
func TestAgentBinaryIsVerifiedBeforeItIsInstalled(t *testing.T) {
	s := render(t, "0.1.2")
	for _, want := range []string{
		"RELEASE_VERSION='0.1.2'",
		"RELEASE_PUBKEY='RWSfYAoo0hKcaKeFYcOsofTSgfZdb2GVo/jjE4pfxaDYSi4SSsC6inQP'",
		`nc_verify_minisig "$TMP" "$SIGFILE" "$RELEASE_PUBKEY"`,
		"https://nebulactrl.com/releases/$RELEASE_VERSION",
		"--proto '=https'",
		"--no-verify",
	} {
		if !strings.Contains(s, want) {
			t.Errorf("the script lacks %q", want)
		}
	}
	// Installing without a signature check is not the default and not a fallback.
	if strings.Contains(s, "could not verify the download") {
		t.Error("an agent must not be installed after a failed check")
	}
	verify := strings.Index(s, `nc_verify_minisig "$TMP"`)
	install := strings.Index(s, `mv "$TMP" "$BIN"`)
	if verify < 0 || install < 0 || verify > install {
		t.Error("the signature must be checked before the binary is moved into place")
	}
	// A development control plane has no release version: the script says so (and --no-verify is the way out).
	if !strings.Contains(render(t, ""), "RELEASE_VERSION=''") {
		t.Error("a development build renders an empty release version")
	}
}

// The enrollment token can come from a file or the environment: argv is readable by every local user.
func TestEnrollmentTokenNeedNotBeOnTheCommandLine(t *testing.T) {
	s := render(t, "0.1.2")
	for _, want := range []string{"--token-file", "NEBULACTRL_ENROLL_TOKEN"} {
		if !strings.Contains(s, want) {
			t.Errorf("the script lacks %q", want)
		}
	}
}

// The verifier is the same text as the installer's (a fix in one must reach the other).
func TestVerifierMatchesTheInstallers(t *testing.T) {
	installer, err := os.ReadFile(filepath.Join("..", "install", "install.sh"))
	if err != nil {
		t.Fatal(err)
	}
	block := func(name, s string) string {
		start := strings.Index(s, "# >>> nc_verify_minisig\n")
		end := strings.Index(s, "# <<< nc_verify_minisig\n")
		if start < 0 || end < start {
			t.Fatalf("%s has no nc_verify_minisig block", name)
		}
		return s[start:end]
	}
	if a, b := block("install.sh", string(installer)), block("install.sh.tmpl", InstallScriptTemplate); a != b {
		t.Errorf("nc_verify_minisig differs between deploy/install/install.sh and deploy/agent/install.sh.tmpl")
	}
}
