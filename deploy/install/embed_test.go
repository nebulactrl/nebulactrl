package installdeploy

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"go.yaml.in/yaml/v3"

	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

func TestScriptInlinesAssetsAndLibraries(t *testing.T) {
	s := Script()
	if !strings.HasPrefix(s, "#!/usr/bin/env bash") {
		t.Fatalf("missing shebang: %.40q", s)
	}
	if strings.Contains(s, assetsMarker+"\n") {
		t.Fatal("assets marker was not replaced")
	}
	for _, want := range []string{"image: ${NEBULACTRL_IMAGE", "name: nebulactrl", "--purge", "NC_ASSET_EOF_0", "NC_ASSET_EOF_1", "NC_ASSET_EOF_4", "auth_enabled: true", "s3.bucket.quota.enforce -apply", "service: nebulactrl-registry"} {
		if !strings.Contains(s, want) {
			t.Errorf("assembled script lacks %q", want)
		}
	}
	for _, lib := range Libraries() {
		if strings.Contains(s, includePrefix+lib+"\n") {
			t.Errorf("include marker for %s was not replaced", lib)
		}
	}
}

func TestAssembledScriptIsValidBash(t *testing.T) {
	bash, err := exec.LookPath("bash")
	if err != nil {
		t.Skip("bash not available")
	}
	for name, body := range map[string]string{"install.sh": Script(), "uninstall.sh": Uninstall()} {
		p := filepath.Join(t.TempDir(), name)
		if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
		if out, err := exec.Command(bash, "-n", p).CombinedOutput(); err != nil {
			t.Errorf("%s: bash -n: %v\n%s", name, err, out)
		}
	}
}

func TestAssetFunctionServesTheComposeFile(t *testing.T) {
	bash, err := exec.LookPath("bash")
	if err != nil {
		t.Skip("bash not available")
	}
	fn := assetFunction()
	cases := []struct{ name, want string }{{"docker-compose.yml", Compose()}, {"uninstall.sh", Uninstall()}}
	for _, name := range InstanceConfigs() {
		cases = append(cases, struct{ name, want string }{name, mustRead(name)})
	}
	for _, tc := range cases {
		out, err := exec.Command(bash, "-c", fn+"\nnc_asset "+tc.name).Output()
		if err != nil {
			t.Fatalf("%s: %v", tc.name, err)
		}
		if string(out) != tc.want {
			t.Errorf("%s: inlined copy differs from the file (%d vs %d bytes)", tc.name, len(out), len(tc.want))
		}
	}
	if err := exec.Command(bash, "-c", fn+"\nnc_asset nope").Run(); err == nil {
		t.Error("unknown asset should fail")
	}
}

// composeFile is the part of docker-compose.yml these tests look at.
type composeFile struct {
	Services map[string]struct {
		Image         string            `yaml:"image"`
		ContainerName string            `yaml:"container_name"`
		Ports         []any             `yaml:"ports"`
		Networks      yaml.Node         `yaml:"networks"`
		Environment   map[string]string `yaml:"environment"`
		Command       []string          `yaml:"command"`
		Volumes       []string          `yaml:"volumes"`
		Entrypoint    []string          `yaml:"entrypoint"`
		CapAdd        []string          `yaml:"cap_add"`
	} `yaml:"services"`
	Networks map[string]struct {
		Internal bool `yaml:"internal"`
		External bool `yaml:"external"`
	} `yaml:"networks"`
	Volumes map[string]any `yaml:"volumes"`
}

func parseCompose(t *testing.T) composeFile {
	t.Helper()
	var c composeFile
	if err := yaml.Unmarshal([]byte(Compose()), &c); err != nil {
		t.Fatal(err)
	}
	return c
}

func networksOf(n yaml.Node) []string {
	var out []string
	switch n.Kind {
	case yaml.SequenceNode:
		for _, c := range n.Content {
			out = append(out, c.Value)
		}
	case yaml.MappingNode:
		for i := 0; i < len(n.Content); i += 2 {
			out = append(out, n.Content[i].Value)
		}
	}
	return out
}

// The built-in services are private: no published port, each on an internal network of its own
// (shared only with the control plane, so a compromised service cannot reach the others), pinned
// images; the control plane is on those networks and gets their addresses.
func TestInstanceServicesArePrivateAndPinned(t *testing.T) {
	c := parseCompose(t)
	own := map[string]string{"seaweedfs": "svc-s3", "registry": "svc-registry", "loki": "svc-loki"}
	for _, network := range own {
		if !c.Networks[network].Internal {
			t.Fatalf("the %s network must be internal (no route out)", network)
		}
	}
	if _, ok := c.Networks["services"]; ok {
		t.Error("the shared `services` network is gone: each instance service has its own")
	}
	for _, name := range []string{"seaweedfs", "registry", "loki"} {
		svc, ok := c.Services[name]
		if !ok {
			t.Fatalf("service %s is missing", name)
		}
		if len(svc.Ports) != 0 {
			t.Errorf("%s publishes ports %v; nothing may be published", name, svc.Ports)
		}
		if got := networksOf(svc.Networks); len(got) != 1 || got[0] != own[name] {
			t.Errorf("%s must be on the %s network only, got %v", name, own[name], got)
		}
		if !strings.Contains(svc.Image, "@sha256:") {
			t.Errorf("%s image is not pinned by digest: %s", name, svc.Image)
		}
	}
	cpNets := strings.Join(networksOf(c.Services["nebulactrl"].Networks), " ")
	for _, network := range own {
		if !strings.Contains(cpNets, network) {
			t.Errorf("the control plane must join %s", network)
		}
		if strings.Contains(strings.Join(networksOf(c.Services["postgres"].Networks), " "), network) {
			t.Errorf("postgres must not join %s", network)
		}
	}
	// SeaweedFS's master and volume servers take writes only with a token the master signed.
	if !strings.Contains(strings.Join(c.Services["seaweedfs"].Command, " "), "[jwt.signing]") {
		t.Error("SeaweedFS must start with a jwt.signing key (security.toml)")
	}
	cp := c.Services["nebulactrl"]
	for k, want := range map[string]string{
		"NEBULACTRL_INSTANCE_S3_URL":       "http://seaweedfs:8333",
		"NEBULACTRL_INSTANCE_REGISTRY_URL": "http://registry:5000",
		"NEBULACTRL_INSTANCE_LOKI_URL":     "http://loki:3100",
	} {
		if cp.Environment[k] != want {
			t.Errorf("control plane %s = %q, want %q", k, cp.Environment[k], want)
		}
	}
	for _, k := range []string{"NEBULACTRL_S3_ADMIN_KEY", "NEBULACTRL_S3_ADMIN_SECRET", "NEBULACTRL_REGISTRY_JWKS_PATH"} {
		if cp.Environment[k] == "" {
			t.Errorf("control plane lacks %s", k)
		}
	}
	// Both sides of the JWKS hand-over use the same directory variable.
	if !strings.HasSuffix(cp.Environment["NEBULACTRL_REGISTRY_JWKS_PATH"], "/registry-auth/jwks.json") {
		t.Errorf("unexpected JWKS path %q", cp.Environment["NEBULACTRL_REGISTRY_JWKS_PATH"])
	}
	for _, v := range c.Services["registry"].Volumes {
		if strings.HasPrefix(v, "${NEBULACTRL_REGISTRY_AUTH_DIR") && !strings.HasSuffix(v, ":/etc/distribution/auth:ro") {
			t.Errorf("the registry must mount the key directory read-only: %s", v)
		}
	}
	for _, v := range []string{"s3-data", "registry-data", "loki-data"} {
		if _, ok := c.Volumes[v]; !ok {
			t.Errorf("named volume %s is not declared (the default of --instance-dir)", v)
		}
	}
}

// The registry's container name is a contract with the agent (the garbage collector runs in it), and the
// S3 must be able to write IAM; the registry config and the control plane's tokens must agree.
func TestInstanceServicesMatchTheControlPlane(t *testing.T) {
	c := parseCompose(t)
	if got := c.Services["registry"].ContainerName; got != agentprotocol.RegistryContainerName {
		t.Fatalf("registry container_name = %q, but the agent runs the garbage collector in %q", got, agentprotocol.RegistryContainerName)
	}
	if !strings.Contains(strings.Join(c.Services["seaweedfs"].Entrypoint, " ")+strings.Join(c.Services["seaweedfs"].Command, " "), "-s3.iam.readOnly=false") {
		t.Error("SeaweedFS must start with -s3.iam.readOnly=false: the control plane creates a key per team through IAM")
	}

	var reg struct {
		Storage struct {
			Delete struct {
				Enabled bool `yaml:"enabled"`
			} `yaml:"delete"`
		} `yaml:"storage"`
		HTTP struct {
			RelativeURLs bool `yaml:"relativeurls"`
		} `yaml:"http"`
		Auth struct {
			Token struct {
				Service           string   `yaml:"service"`
				Issuer            string   `yaml:"issuer"`
				JWKS              string   `yaml:"jwks"`
				SigningAlgorithms []string `yaml:"signingalgorithms"`
			} `yaml:"token"`
		} `yaml:"auth"`
	}
	if err := yaml.Unmarshal([]byte(mustRead("instance/registry.yaml")), &reg); err != nil {
		t.Fatal(err)
	}
	// Service, issuer and algorithm are what the control plane signs with; the contract test in
	// internal/instsvc/registryadmin compares them with internal/registries.
	tok := reg.Auth.Token
	if tok.Service != "nebulactrl-registry" || tok.Issuer != "nebulactrl" || len(tok.SigningAlgorithms) != 1 || tok.SigningAlgorithms[0] != "ES256" {
		t.Errorf("registry token settings %+v", tok)
	}
	if tok.JWKS != "/etc/distribution/auth/jwks.json" {
		t.Errorf("jwks path %q is not the mounted key directory", tok.JWKS)
	}
	if !reg.Storage.Delete.Enabled || !reg.HTTP.RelativeURLs {
		t.Error("the registry needs storage.delete.enabled (retention) and http.relativeurls (Location stays on 127.0.0.1:18500)")
	}

	var loki struct {
		AuthEnabled bool `yaml:"auth_enabled"`
		Limits      struct {
			Retention string `yaml:"retention_period"`
		} `yaml:"limits_config"`
		Compactor struct {
			Retention bool   `yaml:"retention_enabled"`
			Store     string `yaml:"delete_request_store"`
		} `yaml:"compactor"`
	}
	if err := yaml.Unmarshal([]byte(mustRead("instance/loki.yaml")), &loki); err != nil {
		t.Fatal(err)
	}
	if !loki.AuthEnabled {
		t.Error("Loki needs auth_enabled: the tenant is the team")
	}
	if loki.Limits.Retention != "168h" || !loki.Compactor.Retention || loki.Compactor.Store == "" {
		t.Errorf("Loki must keep 7 days with the compactor deleting: %+v", loki)
	}
}
