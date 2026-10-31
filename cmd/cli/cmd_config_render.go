package main

import (
	"fmt"
	"io"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/nebulactrl/nebulactrl/internal/projectconfig"
)

// The CLI reads the config endpoints into these structs (the JSON bodies of
// the API, camelCase) and renders them as text. `--json` never goes through
// them: it prints the response body as received.

type cfgValidateService struct {
	Slug string `json:"slug"`
	Kind string `json:"kind"`
}

type cfgValidateResult struct {
	Valid        bool                      `json:"valid"`
	Schema       int                       `json:"schema"`
	Diagnostics  projectconfig.Diagnostics `json:"diagnostics"`
	Environments []string                  `json:"environments"`
	Services     []cfgValidateService      `json:"services"`
}

type cfgRef struct {
	ID   string `json:"id"`
	Slug string `json:"slug"`
}

type cfgEnvRef struct {
	ID           string `json:"id"`
	Slug         string `json:"slug"`
	IsProduction bool   `json:"isProduction"`
}

type cfgPlanSource struct {
	Kind       string `json:"kind"`
	Repo       string `json:"repo"`
	Branch     string `json:"branch"`
	Path       string `json:"path"`
	Commit     string `json:"commit"`
	FileSHA256 string `json:"fileSha256"`
	Bytes      int    `json:"bytes"`
}

type cfgPlanSummary struct {
	Create         int      `json:"create"`
	Update         int      `json:"update"`
	Unchanged      int      `json:"unchanged"`
	NotInFile      int      `json:"notInFile"`
	Adopted        int      `json:"adopted"`
	Staged         int      `json:"staged"`
	Immediate      int      `json:"immediate"`
	Deferred       int      `json:"deferred"`
	Pruned         int      `json:"pruned"`
	Errors         int      `json:"errors"`
	Warnings       int      `json:"warnings"`
	RequiresDeploy []string `json:"requiresDeploy"`
}

type cfgChange struct {
	Path    string `json:"path"`
	Line    int    `json:"line"`
	Field   string `json:"field"`
	Op      string `json:"op"`
	Applies string `json:"applies"`
	Reason  string `json:"reason"`
	From    string `json:"from"`
	To      string `json:"to"`
	Masked  bool   `json:"masked"`
}

type cfgPlanService struct {
	Slug      string         `json:"slug"`
	Kind      string         `json:"kind"`
	Action    string         `json:"action"`
	ServiceID string         `json:"serviceId"`
	Managed   bool           `json:"managed"`
	State     string         `json:"state"`
	Note      string         `json:"note"`
	Changes   []cfgChange    `json:"changes"`
	Released  []string       `json:"released"`
	Unmanaged map[string]int `json:"unmanaged"`
	Blockers  []string       `json:"blockers"`
}

type cfgShared struct {
	Project     []cfgChange `json:"project"`
	Environment []cfgChange `json:"environment"`
}

type cfgGuard struct {
	ProductionApproval string `json:"productionApproval"`
	Reason             string `json:"reason"`
}

type cfgPlan struct {
	PlanID         string                    `json:"planId"`
	Schema         int                       `json:"schema"`
	Valid          bool                      `json:"valid"`
	Project        cfgRef                    `json:"project"`
	Environment    cfgEnvRef                 `json:"environment"`
	Source         cfgPlanSource             `json:"source"`
	Summary        cfgPlanSummary            `json:"summary"`
	Diagnostics    projectconfig.Diagnostics `json:"diagnostics"`
	Services       []cfgPlanService          `json:"services"`
	Shared         cfgShared                 `json:"shared"`
	RequiredScopes []string                  `json:"requiredScopes"`
	Guard          cfgGuard                  `json:"guard"`
}

type cfgApplyStep struct {
	Step    string `json:"step"`
	Status  string `json:"status"`
	Message string `json:"message"`
}

type cfgAppliedService struct {
	Slug       string `json:"slug"`
	ServiceID  string `json:"serviceId"`
	Action     string `json:"action"`
	Generation int64  `json:"generation"`
}

type cfgApplyResult struct {
	PlanID      string              `json:"planId"`
	Status      string              `json:"status"`
	Steps       []cfgApplyStep      `json:"steps"`
	Services    []cfgAppliedService `json:"services"`
	Deployments []string            `json:"deployments"`
	Plan        cfgPlan             `json:"plan"`
}

type cfgExportResult struct {
	File     string                    `json:"file"`
	Warnings projectconfig.Diagnostics `json:"warnings"`
}

type cfgEnvServiceState struct {
	Slug    string `json:"slug"`
	State   string `json:"state"`
	Changes int    `json:"changes"`
}

type cfgEnvSource struct {
	Mode         string               `json:"mode"`
	ConnectionID string               `json:"connectionId"`
	Repo         string               `json:"repo"`
	Branch       string               `json:"branch"`
	Path         string               `json:"path"`
	ResolvedPath string               `json:"resolvedPath"`
	AutoApply    bool                 `json:"autoApply"`
	Status       string               `json:"status"`
	StatusDetail string               `json:"statusDetail"`
	Commit       string               `json:"commit"`
	FileSHA256   string               `json:"fileSha256"`
	CheckedAt    string               `json:"checkedAt"`
	Services     []cfgEnvServiceState `json:"services"`
}

// hasChanges reports whether applying the plan would write anything: a new or
// updated service, a change to a shared layer, a release, a prune or an
// adoption. Kept and unchanged entries do not count.
func (p *cfgPlan) hasChanges() bool {
	s := p.Summary
	if s.Create > 0 || s.Update > 0 || s.Pruned > 0 || s.Adopted > 0 {
		return true
	}
	live := func(cs []cfgChange) bool {
		for _, c := range cs {
			if c.Op != "keep" {
				return true
			}
		}
		return false
	}
	if live(p.Shared.Project) || live(p.Shared.Environment) {
		return true
	}
	for _, sv := range p.Services {
		switch sv.Action {
		case "create":
			return true
		case "update":
			if live(sv.Changes) || len(sv.Released) > 0 {
				return true
			}
		}
	}
	return false
}

// ok reports whether the plan can be applied: it says valid and carries no
// error diagnostic.
func (p *cfgPlan) ok() bool { return p.Valid && !p.Diagnostics.HasErrors() }

// cleanText drops terminal control characters from text that came from the
// API or a file, so a value cannot rewrite the terminal.
func cleanText(s string) string {
	if !strings.ContainsFunc(s, func(r rune) bool { return r < 0x20 && r != '\t' || r == 0x7f }) {
		return s
	}
	return strings.Map(func(r rune) rune {
		if r < 0x20 && r != '\t' || r == 0x7f {
			return -1
		}
		return r
	}, s)
}

func plural(n int, noun string) string {
	if n == 1 {
		return fmt.Sprintf("%d %s", n, noun)
	}
	return fmt.Sprintf("%d %ss", n, noun)
}

func runeLen(s string) int { return utf8.RuneCountInString(s) }

func padRight(s string, width int) string {
	if n := runeLen(s); n < width {
		return s + strings.Repeat(" ", width-n)
	}
	return s
}

func shortSHA(s string) string {
	s = strings.TrimSpace(s)
	if len(s) > 7 {
		return s[:7]
	}
	return s
}

func fmtBytes(n int) string {
	switch {
	case n < 1024:
		return fmt.Sprintf("%d B", n)
	case n < 1<<20:
		return fmt.Sprintf("%.1f KB", float64(n)/1024)
	default:
		return fmt.Sprintf("%.1f MB", float64(n)/(1<<20))
	}
}

// quoteOr joins quoted words: "a", "b" or "c".
func quoteOr(words []string) string {
	q := make([]string, len(words))
	for i, w := range words {
		q[i] = strconv.Quote(w)
	}
	if len(q) <= 1 {
		return strings.Join(q, "")
	}
	return strings.Join(q[:len(q)-1], ", ") + " or " + q[len(q)-1]
}

// formatDiagnostic renders one diagnostic on a single line:
//
//	nebula.toml:19:1: error[unknown_key] services.web.healthcheck: unknown key "healthcheck"; did you mean "health_check"?
func formatDiagnostic(file string, d projectconfig.Diagnostic) string {
	var b strings.Builder
	b.WriteString(cleanText(file))
	if d.Line > 0 {
		fmt.Fprintf(&b, ":%d", d.Line)
		if d.Column > 0 {
			fmt.Fprintf(&b, ":%d", d.Column)
		}
	}
	fmt.Fprintf(&b, ": %s[%s] ", d.Severity, d.Code)
	if d.Path != "" {
		b.WriteString(cleanText(d.Path) + ": ")
	}
	b.WriteString(cleanText(d.Message))
	if len(d.Suggestions) > 0 {
		s := make([]string, len(d.Suggestions))
		for i, x := range d.Suggestions {
			s[i] = cleanText(x)
		}
		b.WriteString("; did you mean " + quoteOr(s) + "?")
	}
	if d.Environment != "" {
		b.WriteString(" [environment " + cleanText(d.Environment) + "]")
	}
	return b.String()
}

// splitBySeverity returns the errors, warnings and everything else, each in
// the order the API gave them.
func splitBySeverity(ds projectconfig.Diagnostics) (errs, warns, infos projectconfig.Diagnostics) {
	for _, d := range ds {
		switch d.Severity {
		case "error":
			errs = append(errs, d)
		case "warning":
			warns = append(warns, d)
		default:
			infos = append(infos, d)
		}
	}
	return errs, warns, infos
}

// writeDiagnostics prints diagnostics errors first, one per line plus an
// indented hint line when there is one.
func writeDiagnostics(w io.Writer, file, indent string, ds projectconfig.Diagnostics) {
	errs, warns, infos := splitBySeverity(ds)
	for _, group := range []projectconfig.Diagnostics{errs, warns, infos} {
		for _, d := range group {
			fmt.Fprintln(w, indent+formatDiagnostic(file, d))
			if d.Hint != "" {
				fmt.Fprintln(w, indent+"  hint: "+cleanText(d.Hint))
			}
		}
	}
}

// countsLine is "2 errors, 1 warning" (empty when there is neither).
func countsLine(ds projectconfig.Diagnostics) string {
	errs, warns, _ := splitBySeverity(ds)
	var parts []string
	if len(errs) > 0 {
		parts = append(parts, plural(len(errs), "error"))
	}
	if len(warns) > 0 {
		parts = append(parts, plural(len(warns), "warning"))
	}
	return strings.Join(parts, ", ")
}

// writeValidate prints the diagnostics of a validate run and one summary line.
func writeValidate(w io.Writer, file string, r *cfgValidateResult) {
	writeDiagnostics(w, file, "", r.Diagnostics)
	counts := countsLine(r.Diagnostics)
	if !r.Valid || r.Diagnostics.HasErrors() {
		if counts == "" {
			counts = "see above"
		}
		fmt.Fprintf(w, "%s: invalid. %s.\n", cleanText(file), counts)
		return
	}
	parts := []string{fmt.Sprintf("schema %d", r.Schema)}
	if n := len(r.Services); n > 0 {
		list := make([]string, n)
		for i, s := range r.Services {
			list[i] = cleanText(s.Slug) + " " + cleanText(s.Kind)
		}
		parts = append(parts, plural(n, "service")+" ("+strings.Join(list, ", ")+")")
	} else {
		parts = append(parts, "no services")
	}
	if n := len(r.Environments); n > 0 {
		envs := make([]string, n)
		for i, e := range r.Environments {
			envs[i] = cleanText(e)
		}
		parts = append(parts, plural(n, "environment")+" ("+strings.Join(envs, ", ")+")")
	}
	if counts != "" {
		parts = append(parts, counts)
	}
	fmt.Fprintf(w, "%s: valid. %s.\n", cleanText(file), strings.Join(parts, ", "))
}

// planRow is one line of the plan listing before column layout.
type planRow struct {
	indent  int // spaces before the marker
	marker  string
	name    string
	desc    string
	applies string
	note    string
}

const (
	planDescCol     = 32 // default column of the description
	planAppliesCol  = 74 // default column of the staged/immediate label
	planNoteWidth   = 10 // width of the label column before a note
	planTopIndent   = 2  // spaces before the marker of a service row
	planChildIndent = 6  // spaces before the marker of a field row
)

// descStart is where the row's description begins: the default column, or two
// spaces after a name that is longer than the room before it.
func (r planRow) descStart() int {
	return max(planDescCol, r.indent+2+runeLen(r.name)+2)
}

// layoutRows aligns the three columns: descriptions start at column 32 (or
// right after a long name), labels at column 74 (further right only when a
// description needs the room), notes after the label.
func layoutRows(rows []planRow) []string {
	ac := planAppliesCol
	for _, r := range rows {
		if r.desc != "" {
			ac = max(ac, r.descStart()+runeLen(r.desc)+2)
		}
	}
	lines := make([]string, 0, len(rows))
	for _, r := range rows {
		var b strings.Builder
		b.WriteString(strings.Repeat(" ", r.indent))
		b.WriteString(r.marker + " " + r.name)
		pos := r.indent + 2 + runeLen(r.name)
		pad := func(to int) {
			to = max(to, pos+2)
			b.WriteString(strings.Repeat(" ", to-pos))
			pos = to
		}
		if r.desc != "" {
			pad(r.descStart())
			b.WriteString(r.desc)
			pos += runeLen(r.desc)
		}
		if r.applies != "" {
			pad(ac)
			b.WriteString(r.applies)
			pos += runeLen(r.applies)
			if r.note != "" {
				pad(pos - runeLen(r.applies) + max(planNoteWidth, runeLen(r.applies)+2))
				b.WriteString(r.note)
			}
		} else if r.note != "" {
			pad(ac)
			b.WriteString(r.note)
		}
		lines = append(lines, strings.TrimRight(b.String(), " "))
	}
	return lines
}

func hasControl(s string) bool {
	return strings.ContainsFunc(s, func(r rune) bool { return r < 0x20 || r == 0x7f })
}

// planValue renders one side of a change: masked values as the API sent them,
// variable values quoted, anything else as is (control characters escaped).
func planValue(field, v string, masked bool) string {
	switch {
	case masked:
		return cleanText(v)
	case strings.Contains(field, "variables.") || hasControl(v):
		return strconv.Quote(v)
	case v == "":
		return "(none)"
	}
	return v
}

func appliesLabel(a string) string {
	switch a {
	case "staged":
		return "staged"
	case "immediate":
		return "applies at once"
	case "deferred":
		return "deferred"
	}
	return cleanText(a)
}

func reasonNote(r string) string {
	switch r {
	case "drift":
		return "edited in dashboard since last apply"
	case "conflict":
		return "edited in dashboard and in the file"
	case "adopt":
		return "already live; the file takes it over"
	}
	return ""
}

// changeRow turns one change into a row; name is the path below its parent.
func changeRow(indent int, name string, ch cfgChange) planRow {
	r := planRow{indent: indent, name: cleanText(name), note: reasonNote(ch.Reason)}
	to, from := planValue(ch.Field, ch.To, ch.Masked), planValue(ch.Field, ch.From, ch.Masked)
	switch ch.Op {
	case "add":
		r.marker, r.desc, r.applies = "+", to, appliesLabel(ch.Applies)
	case "remove":
		r.marker, r.desc, r.applies = "-", from+" → (removed)", appliesLabel(ch.Applies)
	case "keep":
		r.marker, r.desc, r.note = "=", "kept (no longer in the file)", ""
	case "release":
		r.marker, r.desc, r.note = "?", "released (stays as it is)", ""
	default:
		r.marker, r.desc, r.applies = "~", from+" → "+to, appliesLabel(ch.Applies)
	}
	return r
}

func changeRows(indent int, prefix string, cs []cfgChange) []planRow {
	rows := make([]planRow, 0, len(cs))
	for _, ch := range cs {
		rows = append(rows, changeRow(indent, strings.TrimPrefix(ch.Path, prefix), ch))
	}
	return rows
}

func unmanagedDesc(m map[string]int) string {
	keys := make([]string, 0, len(m))
	for k, n := range m {
		if n > 0 {
			keys = append(keys, k)
		}
	}
	sort.Strings(keys)
	parts := make([]string, len(keys))
	for i, k := range keys {
		parts[i] = fmt.Sprintf("%d %s", m[k], cleanText(k))
	}
	return strings.Join(parts, ", ")
}

func serviceRows(s cfgPlanService, verbose bool) []planRow {
	name := "services." + cleanText(s.Slug)
	prefix := "services." + s.Slug + "."
	var rows []planRow
	switch s.Action {
	case "create":
		desc := "new service"
		if s.Kind != "" {
			desc = "new " + cleanText(s.Kind) + " service"
		}
		rows = append(rows, planRow{marker: "+", name: name, desc: desc, applies: "staged"})
		if verbose {
			rows = append(rows, changeRows(planChildIndent, prefix, s.Changes)...)
		}
	case "update":
		rows = append(rows, planRow{marker: "~", name: name})
		rows = append(rows, changeRows(planChildIndent, prefix, s.Changes)...)
		for _, p := range s.Released {
			rows = append(rows, planRow{indent: planChildIndent, marker: "?", name: cleanText(strings.TrimPrefix(p, prefix)), desc: "released (stays as it is)"})
		}
	case "unchanged":
		rows = append(rows, planRow{marker: "=", name: name, desc: "no changes"})
	case "not_in_file":
		desc := "not in file (kept)"
		if s.Managed {
			desc = "not in file (kept; it was managed by the file)"
		}
		rows = append(rows, planRow{marker: "?", name: name, desc: desc})
	case "blocked":
		rows = append(rows, planRow{marker: "!", name: name, desc: "blocked: " + cleanText(strings.Join(s.Blockers, ", "))})
	default:
		rows = append(rows, planRow{marker: " ", name: name, desc: cleanText(s.Action)})
	}
	if verbose && (s.Action == "update" || s.Action == "unchanged") {
		if d := unmanagedDesc(s.Unmanaged); d != "" {
			rows = append(rows, planRow{indent: planChildIndent, marker: ".", name: "unmanaged", desc: d + " not in the file (untouched)"})
		}
	}
	return rows
}

func sharedRows(layer string, cs []cfgChange) []planRow {
	if len(cs) == 0 {
		return nil
	}
	rows := []planRow{{marker: "~", name: "shared variables (" + layer + ")"}}
	return append(rows, changeRows(planChildIndent, "", cs)...)
}

// summaryLine is "1 create, 1 update (3 staged, 1 immediate), 1 unchanged, 1 not in file."
func summaryLine(p *cfgPlan) string {
	s := p.Summary
	var head []string
	if s.Create > 0 {
		head = append(head, fmt.Sprintf("%d create", s.Create)+pluralS(s.Create))
	}
	if s.Update > 0 {
		head = append(head, fmt.Sprintf("%d update", s.Update)+pluralS(s.Update))
	}
	var when []string
	if s.Staged > 0 {
		when = append(when, fmt.Sprintf("%d staged", s.Staged))
	}
	if s.Immediate > 0 {
		when = append(when, fmt.Sprintf("%d immediate", s.Immediate))
	}
	if s.Deferred > 0 {
		when = append(when, fmt.Sprintf("%d deferred", s.Deferred))
	}
	paren := ""
	if len(when) > 0 {
		paren = " (" + strings.Join(when, ", ") + ")"
	}
	parts := []string{}
	switch {
	case len(head) > 0:
		head[len(head)-1] += paren
		parts = append(parts, head...)
	case paren != "":
		parts = append(parts, "shared changes"+paren)
	}
	if s.Adopted > 0 {
		parts = append(parts, fmt.Sprintf("%d adopted", s.Adopted))
	}
	if s.Pruned > 0 {
		parts = append(parts, fmt.Sprintf("%d pruned", s.Pruned))
	}
	if len(parts) == 0 {
		parts = append(parts, "no changes")
	}
	if s.Unchanged > 0 {
		parts = append(parts, fmt.Sprintf("%d unchanged", s.Unchanged))
	}
	if s.NotInFile > 0 {
		parts = append(parts, fmt.Sprintf("%d not in file", s.NotInFile))
	}
	return strings.Join(parts, ", ") + "."
}

func pluralS(n int) string {
	if n == 1 {
		return ""
	}
	return "s"
}

// writePlan renders a plan: header, the change listing, the diagnostics
// grouped by severity, a summary line and the plan id hint. label names the
// local file in diagnostics when the plan does not know the path.
func writePlan(w io.Writer, p *cfgPlan, label string, verbose bool) {
	file := label
	if p.Source.Kind == "git" && p.Source.Path != "" { // an inline file keeps its local name: line numbers refer to it
		file = p.Source.Path
	}
	if file == "" {
		file = "nebula.toml"
	}
	left := "Plan for " + cleanText(p.Project.Slug) + " / " + cleanText(p.Environment.Slug)
	src := cleanText(file)
	if p.Source.Commit != "" {
		src += " @ " + cleanText(shortSHA(p.Source.Commit))
	}
	if p.Source.Bytes > 0 {
		src += " (" + fmtBytes(p.Source.Bytes) + ")"
	}
	fmt.Fprintf(w, "%s\n\n", padRight(left, max(39, runeLen(left)+2))+src)

	var rows []planRow
	for _, s := range p.Services {
		rows = append(rows, serviceRows(s, verbose)...)
	}
	rows = append(rows, sharedRows("project", p.Shared.Project)...)
	rows = append(rows, sharedRows("environment", p.Shared.Environment)...)
	if len(rows) > 0 {
		for i := range rows {
			if rows[i].indent == 0 {
				rows[i].indent = planTopIndent // service rows; their fields sit at planChildIndent
			}
		}
		for _, l := range layoutRows(rows) {
			fmt.Fprintln(w, l)
		}
		fmt.Fprintln(w)
	}

	errs, warns, infos := splitBySeverity(p.Diagnostics)
	for _, g := range []struct {
		noun string
		ds   projectconfig.Diagnostics
	}{{"error", errs}, {"warning", warns}, {"note", infos}} {
		if len(g.ds) == 0 {
			continue
		}
		fmt.Fprintf(w, "  %s:\n", plural(len(g.ds), g.noun))
		writeDiagnostics(w, file, "    ", g.ds)
		fmt.Fprintln(w)
	}

	fmt.Fprintln(w, summaryLine(p))
	switch {
	case !p.ok():
		fmt.Fprintln(w, "The plan is not valid: fix the errors above; nothing can be applied.")
	case p.hasChanges() && p.PlanID != "":
		id := p.PlanID
		if len(id) > 11 {
			id = id[:11]
		}
		fmt.Fprintf(w, "Plan id %s… — pass --plan-id to apply to fail if anything changed meanwhile.\n", cleanText(id))
	}
	if p.Guard.ProductionApproval == "required" {
		msg := "A human approval is required before this plan can be applied"
		if p.Guard.Reason != "" {
			msg += " (" + cleanText(p.Guard.Reason) + ")"
		}
		fmt.Fprintln(w, msg+".")
	}
}

func resultWord(status string) string {
	switch status {
	case "staged":
		return "staged"
	case "partial":
		return "partial: some steps did not run; fix the cause and run apply again, it converges"
	case "failed":
		return "failed: nothing was applied"
	case "noop":
		return "nothing to apply"
	}
	return cleanText(status)
}

// writeApplyResult prints the plan an apply ran against and then its outcome.
func writeApplyResult(w io.Writer, r *cfgApplyResult, label string, verbose bool) {
	writePlan(w, &r.Plan, label, verbose)
	fmt.Fprintln(w)
	fmt.Fprintf(w, "Result: %s.\n", resultWord(r.Status))
	for _, s := range r.Steps {
		line := "  " + padRight(cleanText(s.Status), 14) + cleanText(s.Step)
		if s.Message != "" {
			line += "  " + cleanText(s.Message)
		}
		fmt.Fprintln(w, line)
	}
	if len(r.Services) > 0 {
		list := make([]string, len(r.Services))
		for i, s := range r.Services {
			list[i] = fmt.Sprintf("%s (%s, generation %d)", cleanText(s.Slug), cleanText(s.Action), s.Generation)
		}
		fmt.Fprintf(w, "Services: %s\n", strings.Join(list, ", "))
	}
	if len(r.Deployments) > 0 {
		ids := make([]string, len(r.Deployments))
		for i, d := range r.Deployments {
			ids[i] = cleanText(d)
		}
		fmt.Fprintf(w, "Deployments: %s\n", strings.Join(ids, ", "))
	}
}

// stepsFailed counts the steps that failed.
func (r *cfgApplyResult) stepsFailed() int {
	n := 0
	for _, s := range r.Steps {
		if s.Status == "failed" {
			n++
		}
	}
	return n
}
