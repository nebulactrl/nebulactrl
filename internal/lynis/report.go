// Package lynis is NebulaCtrl's side of Lynis (CISOfy's open-source host
// auditing tool): the parser of its report file, the curated catalog of safe
// remediations with the shell that checks, applies and undoes each of them, and
// the guidance shown for the findings NebulaCtrl does not fix by itself.
//
// Both the agent (which runs everything on the host) and the control plane
// (which explains the result) import it.
package lynis

import (
	"bufio"
	"fmt"
	"io"
	"strconv"
	"strings"

	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

// ReportPath is where Lynis writes its machine-readable report by default
// (https://cisofy.com/documentation/lynis/get-started/#report-files).
const ReportPath = "/var/log/lynis-report.dat"

// Limits that keep a hostile or oversized report from growing the result.
const (
	maxFindings   = 500
	maxFields     = 200
	maxTextLength = 600
)

// ParseReport reads a Lynis report file. It keeps the keys NebulaCtrl shows
// (the version, the hardening index, the warnings, the suggestions and the
// per-key details) and ignores the rest, which on a real host includes
// package lists of several hundred kilobytes.
//
// Lines are `key=value`; keys ending in `[]` repeat. A warning or suggestion
// is `ID|text|details|solution|`, with `-` for an empty part. A detail is
// `ID|section|k:v;k:v;|`.
func ParseReport(r io.Reader) (*agentprotocol.HardeningReport, error) {
	rep := &agentprotocol.HardeningReport{
		Warnings: []agentprotocol.HardeningFinding{}, Suggestions: []agentprotocol.HardeningFinding{},
	}
	details := map[string][]agentprotocol.HardeningDetail{}
	var seenIndex bool
	br := bufio.NewReaderSize(r, 1<<16)
	for {
		line, err := br.ReadString('\n')
		if len(line) > 0 {
			key, val, ok := strings.Cut(strings.TrimRight(line, "\r\n"), "=")
			if ok && !strings.HasPrefix(key, "#") {
				switch key {
				case "lynis_version":
					rep.LynisVersion = clip(val)
				case "hostname":
					rep.Hostname = clip(val)
				case "os_name":
					rep.OS = strings.TrimSpace(clip(val) + " " + rep.OS)
				case "os_version":
					rep.OS = strings.TrimSpace(rep.OS + " " + clip(val))
				case "report_datetime_end":
					rep.FinishedAt = clip(val)
				case "hardening_index":
					n, convErr := strconv.Atoi(strings.TrimSpace(val))
					if convErr != nil || n < 0 || n > 100 {
						return nil, fmt.Errorf("lynis report: hardening_index %q is not a score from 0 to 100", val)
					}
					rep.HardeningIndex, seenIndex = n, true
				case "lynis_tests_done":
					rep.TestsDone, _ = strconv.Atoi(strings.TrimSpace(val))
				case "vulnerable_packages_found":
					rep.VulnerablePackages, _ = strconv.Atoi(strings.TrimSpace(val))
				case "warning[]":
					if f, ok := parseFinding(val); ok && len(rep.Warnings) < maxFindings {
						rep.Warnings = append(rep.Warnings, f)
					}
				case "suggestion[]":
					if f, ok := parseFinding(val); ok && len(rep.Suggestions) < maxFindings {
						rep.Suggestions = append(rep.Suggestions, f)
					}
				case "details[]":
					if id, d, ok := parseDetail(val); ok && len(details[id]) < maxFields {
						details[id] = append(details[id], d)
					}
				}
			}
		}
		if err != nil {
			if err != io.EOF {
				return nil, fmt.Errorf("read lynis report: %w", err)
			}
			break
		}
	}
	if !seenIndex {
		return nil, fmt.Errorf("lynis report: no hardening_index (the audit did not finish)")
	}
	attach := func(fs []agentprotocol.HardeningFinding) {
		used := map[string]bool{}
		for i := range fs {
			// Details belong to a test, not to one of its findings: attach them to
			// the first finding of the ID so they are not listed twice.
			if d := details[fs[i].ID]; len(d) > 0 && !used[fs[i].ID] {
				fs[i].Fields = d
				used[fs[i].ID] = true
			}
		}
	}
	attach(rep.Suggestions)
	attach(rep.Warnings)
	return rep, nil
}

func clip(s string) string {
	s = strings.TrimSpace(s)
	if len(s) > maxTextLength {
		s = s[:maxTextLength]
	}
	return s
}

func dash(s string) string {
	s = strings.TrimSpace(s)
	if s == "-" {
		return ""
	}
	return clip(s)
}

// parseFinding reads `ID|text|details|solution|`.
func parseFinding(v string) (agentprotocol.HardeningFinding, bool) {
	parts := strings.Split(v, "|")
	if len(parts) < 2 || !validTestID(parts[0]) {
		return agentprotocol.HardeningFinding{}, false
	}
	f := agentprotocol.HardeningFinding{ID: parts[0], Text: dash(parts[1])}
	if len(parts) > 2 {
		f.Details = dash(parts[2])
	}
	if len(parts) > 3 {
		f.Solution = strings.TrimPrefix(dash(parts[3]), "text:")
	}
	return f, true
}

// parseDetail reads `ID|section|desc:...;field:...;prefval:...;value:...;|`.
func parseDetail(v string) (string, agentprotocol.HardeningDetail, bool) {
	parts := strings.SplitN(v, "|", 4)
	if len(parts) < 3 || !validTestID(parts[0]) {
		return "", agentprotocol.HardeningDetail{}, false
	}
	var d agentprotocol.HardeningDetail
	for _, kv := range strings.Split(parts[2], ";") {
		k, val, ok := strings.Cut(kv, ":")
		if !ok {
			continue
		}
		switch k {
		case "desc":
			d.Description = clip(val)
		case "field":
			d.Field = clip(val)
		case "prefval":
			d.Preferred = clip(val)
		case "value":
			d.Value = clip(val)
		}
	}
	if d.Field == "" {
		return "", agentprotocol.HardeningDetail{}, false
	}
	return parts[0], d, true
}

// validTestID accepts Lynis test IDs: three to five capital letters, a dash and
// four digits (SSH-7408, USB-1000, KRNL-6000), optionally a ":NN" suffix on
// manual checks.
func validTestID(id string) bool {
	base, _, _ := strings.Cut(id, ":")
	letters, digits, ok := strings.Cut(base, "-")
	if !ok || len(letters) < 3 || len(letters) > 5 || len(digits) != 4 {
		return false
	}
	for _, c := range letters {
		if c < 'A' || c > 'Z' {
			return false
		}
	}
	for _, c := range digits {
		if c < '0' || c > '9' {
			return false
		}
	}
	return true
}
