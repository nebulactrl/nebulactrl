// Package platform runs the per-team "platform service": Headscale, the tailnet
// control server a team can set up for the Tailscale integration.
//
// It is an ordinary service in a per-team system project ("NebulaCtrl
// platform"), created staged in one transaction, deployed by a River job and then
// wired into the Tailscale integration with generated credentials. Removing it
// unwires the integration first.
//
// Object storage, the image registry and central logs are not here: they are
// instance services with an isolated slice per team (internal/instsvc).
package platform

// KindHeadscale is the only kind of platform service.
const KindHeadscale = "headscale"

// Kinds lists every kind in display order.
var Kinds = []string{KindHeadscale}

// Statuses of a platform service.
const (
	StatusProvisioning = "provisioning"
	StatusReady        = "ready"
	StatusFailed       = "failed"
	StatusRemoving     = "removing"
)

// Steps of the provisioning job (platform_services.step).
const (
	StepConfigure = "configure"
	StepDeploy    = "deploy"
	StepAwait     = "await"
	StepWire      = "wire"
	StepDone      = "done"
)

// SystemProjectName is the display name of the per-team system project.
const (
	SystemProjectName = "NebulaCtrl platform"
	SystemProjectDesc = "Services NebulaCtrl runs for its own use. Managed from Team settings → Private network (Headscale)."
	SystemEnvName     = "Platform"
	SystemEnvSlug     = "platform"
)

// Images are pinned by tag and digest (the digest of the multi-platform index).
// docs/operations/platform-services.md says how to bump them; the catalog test
// refuses an unpinned image.
const (
	ImageHeadscale = "headscale/headscale:v0.29.6@sha256:cabe84f0699913dbca9320f0883dca7565dd08df3c5be29edfabbebeff763966"
)

// PlatformKindInfo describes a kind for the catalog the API returns.
type PlatformKindInfo struct {
	Kind        string `json:"kind" enum:"headscale"`
	Label       string `json:"label"`
	Product     string `json:"product" doc:"The software that runs."`
	Summary     string `json:"summary" doc:"What it is for and what Set up for me wires."`
	Image       string `json:"image" doc:"The pinned image."`
	License     string `json:"license" doc:"SPDX identifier of the upstream licence."`
	AGPL        bool   `json:"agpl" doc:"True when the upstream licence is the AGPL (shown in the UI)."`
	LicenseNote string `json:"licenseNote,omitempty"`
	Footprint   string `json:"footprint" doc:"Rough resource use."`
	HoldsData   bool   `json:"holdsData" doc:"Has a data volume worth backing up."`
	Egress      bool   `json:"egress" doc:"Needs outbound internet. Everything else starts with outbound access blocked."`
	Notice      string `json:"notice,omitempty" doc:"Caveat shown before setting it up."`
}

// Catalog returns the description of every kind, in display order.
func Catalog() []PlatformKindInfo {
	return []PlatformKindInfo{
		{
			Kind: KindHeadscale, Label: "Tailnet control server (Headscale)", Product: "Headscale", Image: ImageHeadscale, License: "BSD-3-Clause",
			Summary:   "A self-hosted Tailscale control server. Set up for me creates a user and an API key and connects the Tailscale integration to it.",
			Footprint: "32 MB image, about 12 MB of memory", HoldsData: true, Egress: true,
			Notice: "Needs outbound internet to fetch the DERP relay map, so outbound access is not blocked for this service.",
		},
	}
}

// InfoFor returns the catalog entry of kind.
func InfoFor(kind string) (PlatformKindInfo, bool) {
	for _, i := range Catalog() {
		if i.Kind == kind {
			return i, true
		}
	}
	return PlatformKindInfo{}, false
}
