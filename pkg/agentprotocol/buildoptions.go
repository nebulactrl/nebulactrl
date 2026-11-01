package agentprotocol

// Build options of a git source: a one-deployment "no cache" build and git
// submodules. Both travel in GitSource (and BuildPlanPayload for submodules).

// CapBuildOptions is advertised in Facts.Capabilities by agents that honour
// GitSource.NoCache and GitSource.Submodules. An older agent would silently drop
// the fields (building with cache, checking out no submodules), so the control
// plane refuses (422 agent_too_old) a no-cache deployment or a submodule
// checkout on a server without it. A server that has never connected has no
// facts yet and is assumed to enroll with a current agent.
const CapBuildOptions = "build.options"

// Submodule modes (service config `submodules`, GitSource.Submodules).
const (
	// SubmodulesAuto checks submodules out recursively when the commit has a
	// .gitmodules file. A submodule that cannot be fetched is logged as a warning
	// and the build continues without it.
	SubmodulesAuto = "auto"
	// SubmodulesOn always checks them out recursively; a failure fails the build.
	SubmodulesOn = "on"
	// SubmodulesOff never does.
	SubmodulesOff = "off"
)

// ValidSubmodules reports whether m is a submodule mode. The empty string is
// SubmodulesAuto.
func ValidSubmodules(m string) bool {
	switch m {
	case "", SubmodulesAuto, SubmodulesOn, SubmodulesOff:
		return true
	}
	return false
}

// SubmodulesEnabled reports whether a mode may check submodules out at all.
func SubmodulesEnabled(m string) bool { return m != SubmodulesOff }
