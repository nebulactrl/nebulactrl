package hostpatch

import (
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

// sections is a parsed "@@NAME value" output: the one-line values and the
// lines under each section header.
type sections struct {
	values map[string]string
	lines  map[string][]string
	order  []string
}

func parseSections(out string) sections {
	s := sections{values: map[string]string{}, lines: map[string][]string{}}
	cur := ""
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimRight(line, "\r")
		if strings.HasPrefix(line, "@@") {
			name, val, _ := strings.Cut(line[2:], " ")
			cur = name
			s.values[name] = strings.TrimSpace(val)
			s.order = append(s.order, name)
			continue
		}
		if cur != "" {
			s.lines[cur] = append(s.lines[cur], line)
		}
	}
	return s
}

func (s sections) has(name string) bool { _, ok := s.values[name]; return ok }

func (s sections) int(name string) int {
	n, _ := strconv.Atoi(s.values[name])
	return n
}

// ParseCheck turns the output of CheckScript into a PatchState. An answer that
// was cut short (no "@@END") is an error: it must not read as "no updates".
func ParseCheck(out string) (agentprotocol.PatchState, error) {
	s := parseSections(out)
	if !s.has("END") {
		return agentprotocol.PatchState{}, errors.New("the host's answer was cut short")
	}
	st := agentprotocol.PatchState{BootID: s.values["BOOTID"], Manager: s.values["MANAGER"]}
	switch st.Manager {
	case agentprotocol.PatchManagerAPT:
		st.Supported = true
		st.Packages = ParseAptSimulation(strings.Join(s.lines["UPDATES"], "\n"))
		if s.int("REFRESH") != 0 {
			st.Message = "The package lists could not be refreshed; these are the updates as of the last successful refresh."
		}
	case agentprotocol.PatchManagerDNF:
		st.Supported = true
		if rc := s.int("UPDATESRC"); rc != 0 && rc != 100 {
			return agentprotocol.PatchState{}, fmt.Errorf("dnf check-update failed (exit %d)", rc)
		}
		st.Packages = ParseDnfCheckUpdate(strings.Join(s.lines["UPDATES"], "\n"), strings.Join(s.lines["SECURITY"], "\n"))
	default:
		st.Manager = ""
		st.Message = "No supported package manager (apt or dnf) was found on this host."
		return st, nil
	}
	st.Updates = len(st.Packages)
	for _, p := range st.Packages {
		if p.Security {
			st.Security++
		}
	}
	sortPackages(st.Packages)
	if len(st.Packages) > agentprotocol.MaxPatchPackages {
		st.Packages = st.Packages[:agentprotocol.MaxPatchPackages]
	}
	st.RebootRequired = s.values["REBOOT"] == "1"
	if st.RebootRequired {
		seen := map[string]bool{}
		for _, l := range s.lines["REBOOTPKGS"] {
			if l = strings.TrimSpace(l); l != "" && !seen[l] {
				seen[l] = true
				st.RebootPackages = append(st.RebootPackages, l)
			}
		}
		sort.Strings(st.RebootPackages)
	}
	return st, nil
}

// sortPackages puts security updates first, then orders by name.
func sortPackages(p []agentprotocol.PatchPackage) {
	sort.SliceStable(p, func(i, j int) bool {
		if p[i].Security != p[j].Security {
			return p[i].Security
		}
		return p[i].Name < p[j].Name
	})
}

// "Inst libssl3 [3.0.2-0ubuntu1.10] (3.0.2-0ubuntu1.12 Ubuntu:22.04/jammy-updates, Ubuntu:22.04/jammy-security [amd64])"
var aptInst = regexp.MustCompile(`^Inst (\S+)(?: \[([^\]]*)\])? \((\S+)(?: ([^)]*))?\)`)

// ParseAptSimulation reads the "Inst" lines of `apt-get -s upgrade`. An update
// is a security update when one of its origins is a security suite
// (jammy-security, stable-security, ...).
func ParseAptSimulation(out string) []agentprotocol.PatchPackage {
	var pkgs []agentprotocol.PatchPackage
	for _, line := range strings.Split(out, "\n") {
		m := aptInst.FindStringSubmatch(strings.TrimSpace(line))
		if m == nil {
			continue
		}
		origins := strings.ToLower(m[4])
		pkgs = append(pkgs, agentprotocol.PatchPackage{
			Name: m[1], From: m[2], To: m[3], Security: strings.Contains(origins, "-security"),
		})
	}
	return pkgs
}

var dnfArches = map[string]bool{
	"x86_64": true, "aarch64": true, "noarch": true, "i686": true, "i386": true,
	"ppc64le": true, "s390x": true, "armv7hl": true, "src": true,
}

// ParseDnfCheckUpdate reads `dnf -q check-update` (every update) and the same
// command with --security (the security subset). Lines are
// "name.arch  version  repository"; the "Obsoleting Packages" section is ignored.
func ParseDnfCheckUpdate(all, security string) []agentprotocol.PatchPackage {
	sec := map[string]bool{}
	for _, p := range parseDnfLines(security) {
		sec[p.Name] = true
	}
	pkgs := parseDnfLines(all)
	for i := range pkgs {
		pkgs[i].Security = sec[pkgs[i].Name]
	}
	return pkgs
}

func parseDnfLines(out string) []agentprotocol.PatchPackage {
	var pkgs []agentprotocol.PatchPackage
	seen := map[string]bool{}
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "Obsoleting Packages") {
			break
		}
		f := strings.Fields(line)
		if len(f) != 3 {
			continue
		}
		i := strings.LastIndexByte(f[0], '.')
		if i <= 0 || !dnfArches[f[0][i+1:]] {
			continue
		}
		name := f[0][:i]
		if seen[name] {
			continue
		}
		seen[name] = true
		pkgs = append(pkgs, agentprotocol.PatchPackage{Name: name, To: f[1]})
	}
	return pkgs
}

// ParseReboot reads the output of RebootScript: the boot id it will replace and
// whether the reboot was scheduled.
func ParseReboot(out string) (bootID, how string, err error) {
	s := parseSections(out)
	if s.has("FAILED") {
		return s.values["BOOTID"], "", errors.New(s.values["FAILED"])
	}
	if !s.has("SCHEDULED") {
		return s.values["BOOTID"], "", errors.New("the host did not confirm the reboot")
	}
	return s.values["BOOTID"], s.values["SCHEDULED"], nil
}

// StartState is what StartScript reported.
type StartState string

// Start states.
const (
	StartStarted StartState = "started"
	StartRunning StartState = "running"
	StartDone    StartState = "done"
)

// ParseStart reads the output of StartScript.
func ParseStart(out string) (StartState, error) {
	s := parseSections(out)
	switch {
	case s.has("FAILED"):
		return "", errors.New(s.values["FAILED"])
	case s.has("STARTED"):
		return StartStarted, nil
	case s.has("RUNNING"):
		return StartRunning, nil
	case s.has("DONE"):
		return StartDone, nil
	}
	return "", errors.New("the host did not start the upgrade")
}

// UpgradeStatus is what StatusScript reported.
type UpgradeStatus struct {
	// State is "running", "finished", "lost" or "none".
	State    string
	ExitCode int
	Log      string
}

// Upgrade states.
const (
	UpgradeRunning  = "running"
	UpgradeFinished = "finished"
	UpgradeLost     = "lost"
	UpgradeNone     = "none"
)

// ParseStatus reads the output of StatusScript.
func ParseStatus(out string) (UpgradeStatus, error) {
	// Everything after the @@LOG line is the upgrade's own output: it is never
	// parsed for markers, so a line that looks like one cannot change the verdict.
	head, log := out, ""
	if i := strings.Index("\n"+out, "\n@@LOG\n"); i >= 0 {
		head, log = out[:max(i-1, 0)], strings.TrimRight(out[i+len("@@LOG\n"):], "\n")
	}
	s := parseSections(head)
	switch {
	case s.has("RC"):
		rc, err := strconv.Atoi(s.values["RC"])
		if err != nil {
			return UpgradeStatus{}, fmt.Errorf("unreadable exit code %q", s.values["RC"])
		}
		return UpgradeStatus{State: UpgradeFinished, ExitCode: rc, Log: log}, nil
	case s.has("RUNNING"):
		return UpgradeStatus{State: UpgradeRunning, Log: log}, nil
	case s.has("LOST"):
		return UpgradeStatus{State: UpgradeLost, Log: log}, nil
	case s.has("NONE"):
		return UpgradeStatus{State: UpgradeNone}, nil
	}
	return UpgradeStatus{}, errors.New("the host did not report the upgrade's state")
}
