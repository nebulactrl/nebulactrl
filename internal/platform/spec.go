package platform

import (
	"fmt"

	"github.com/nebulactrl/nebulactrl/internal/services"
)

// nameHeadscale is the name of the service of the system project.
const nameHeadscale = "Headscale"

// portHeadscale is the port Headscale listens on.
const portHeadscale = 8080

const (
	// headscaleUser owns the pre-auth keys NebulaCtrl mints.
	headscaleUser = "nebulactrl"
	// headscaleTag is the tag the integration's default tags use.
	headscaleTag = "tag:nebula-svc"
)

// varSpec is a variable of a platform service.
type varSpec struct {
	Key, Value string
	Secret     bool
}

// volSpec is a named volume.
type volSpec struct{ Name, Mount string }

// serviceSpec is what Provision creates for a kind: one service with its
// volume, variables and the HTTP domain that makes it reachable.
type serviceSpec struct {
	Name    string
	Config  services.Config
	Vars    []varSpec
	Volumes []volSpec
	// DomainPort is the port the hostname routes to.
	DomainPort int
}

// specInput carries everything a spec needs.
type specInput struct {
	Hostname  string
	PublicURL string
}

// build returns the service of a kind.
func build(kind string, in specInput) (serviceSpec, error) {
	switch kind {
	case KindHeadscale:
		return headscaleSpec(in), nil
	}
	return serviceSpec{}, fmt.Errorf("unknown platform service kind %q", kind)
}

func httpHealth(path string, port, deadline int) services.Healthcheck {
	return services.Healthcheck{Type: services.HealthHTTP, Path: path, Port: port, DeadlineSeconds: deadline}
}

func headscaleSpec(in specInput) serviceSpec {
	return serviceSpec{
		Name: nameHeadscale,
		Config: services.Config{
			Image: ImageHeadscale, Port: portHeadscale, StartCommand: []string{"serve"},
			Healthcheck: httpHealth("/health", portHeadscale, 120),
			Files: []services.FileMount{
				{Path: "/etc/headscale/config.yaml", Mode: "0644", Content: headscaleConfig(in.PublicURL)},
			},
			// The DERP map is fetched from Tailscale's servers.
			BlockEgress: false,
		},
		Volumes:    []volSpec{{Name: "data", Mount: "/var/lib/headscale"}},
		DomainPort: portHeadscale,
	}
}
