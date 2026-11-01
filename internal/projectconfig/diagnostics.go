package projectconfig

import (
	"fmt"
	"sort"
	"strings"
)

// Severities of a Diagnostic.
const (
	SeverityError   = "error"
	SeverityWarning = "warning"
	SeverityInfo    = "info"
)

// Diagnostic codes owned by the format core. Codes the control plane
// raises against live state (unknown_server, hostname_in_use, ...) are defined
// by internal/configsync with the same snake_case spelling.
const (
	CodeTOMLSyntax              = "toml_syntax"
	CodeFileTooLarge            = "file_too_large"
	CodeInvalidEncoding         = "invalid_encoding"
	CodeLimitExceeded           = "limit_exceeded"
	CodeSchemaUnsupported       = "schema_unsupported"
	CodeMissingRequired         = "missing_required"
	CodeUnknownKey              = "unknown_key"
	CodeWrongType               = "wrong_type"
	CodeInvalidValue            = "invalid_value"
	CodeInvalidSlug             = "invalid_slug"
	CodeReservedSlug            = "reserved_slug"
	CodeMissingSource           = "missing_source"
	CodeConflictingSource       = "conflicting_source"
	CodeNotAllowedForKind       = "not_allowed_for_kind"
	CodeDerivedField            = "derived_field"
	CodeUnsetInBase             = "unset_in_base"
	CodeInvalidUnsetPath        = "invalid_unset_path"
	CodeCredentialKey           = "credential_key"
	CodeCredentialInURL         = "credential_in_url"
	CodeLiteralSecret           = "literal_secret"
	CodeSecretInFile            = "secret_in_file"
	CodeSecretHasValue          = "secret_has_value"
	CodeInvalidReference        = "invalid_reference"
	CodeUnknownServiceReference = "unknown_service_reference"
	CodeUnresolvedReference     = "unresolved_reference"
	CodeReferenceCycle          = "reference_cycle"
	CodeReservedVariable        = "reserved_variable"
	CodeInvalidTemplate         = "invalid_template"
	CodePathEscape              = "path_escape"
	CodeAttachmentMissing       = "attachment_missing"
	CodeUnknownEnvironment      = "unknown_environment"
	CodeLicenseNotAccepted      = "license_not_accepted"
	CodeDockerUnrestricted      = "docker_unrestricted"
	CodeReplicasRequireStateles = "replicas_require_stateless"
	CodeEgressBlocked           = "egress_blocked"
	CodeInvalidUpstream         = "invalid_upstream"
	CodeHostnameInUse           = "hostname_in_use"
	CodeProcessCommandRequired  = "process_command_required"
	CodeProcessNameReserved     = "process_name_reserved"
	CodeProcessPortConflict     = "process_port_conflict"
	CodeProcessSingletonScale   = "process_singleton_scale"
)

// Pos is a 1-based source range. End is exclusive in column terms (EndColumn
// is the column after the last character). The zero Pos means "no position".
type Pos struct {
	Line, Column, EndLine, EndColumn int
}

// IsZero reports whether p carries no position.
func (p Pos) IsZero() bool { return p.Line == 0 }

// Diagnostic is the one problem object of validate, plan and apply.
type Diagnostic struct {
	Severity    string   `json:"severity" doc:"error, warning or info" enum:"error,warning,info" example:"error"`
	Code        string   `json:"code" doc:"Stable snake_case code" example:"unknown_key"`
	Message     string   `json:"message" doc:"Human-readable description. Never contains a secret value." example:"unknown key \"healthcheck\""`
	Path        string   `json:"path,omitempty" doc:"File key path of the problem" example:"services.web.healthcheck"`
	Line        int      `json:"line,omitempty" doc:"1-based line; absent for problems that are not in the file" example:"19"`
	Column      int      `json:"column,omitempty" doc:"1-based column" example:"1"`
	EndLine     int      `json:"endLine,omitempty" doc:"1-based end line" example:"19"`
	EndColumn   int      `json:"endColumn,omitempty" doc:"1-based end column (exclusive)" example:"12"`
	Hint        string   `json:"hint,omitempty" doc:"What to write instead" example:"write health_check"`
	Suggestions []string `json:"suggestions,omitempty" doc:"Did-you-mean candidates"`
	Environment string   `json:"environment,omitempty" doc:"Present when the problem exists only after resolving this environment" example:"staging"`
}

// Diagnostics is an ordered list of problems.
type Diagnostics []Diagnostic

// HasErrors reports whether any diagnostic has severity error.
func (d Diagnostics) HasErrors() bool {
	for _, x := range d {
		if x.Severity == SeverityError {
			return true
		}
	}
	return false
}

// Count returns the number of diagnostics of a severity.
func (d Diagnostics) Count(severity string) int {
	n := 0
	for _, x := range d {
		if x.Severity == severity {
			n++
		}
	}
	return n
}

// HasCode reports whether any diagnostic carries code.
func (d Diagnostics) HasCode(code string) bool {
	for _, x := range d {
		if x.Code == code {
			return true
		}
	}
	return false
}

// WithPos sets the position fields from p.
func (d Diagnostic) WithPos(p Pos) Diagnostic {
	d.Line, d.Column, d.EndLine, d.EndColumn = p.Line, p.Column, p.EndLine, p.EndColumn
	return d
}

// Pos returns the position of the diagnostic.
func (d Diagnostic) Pos() Pos {
	return Pos{Line: d.Line, Column: d.Column, EndLine: d.EndLine, EndColumn: d.EndColumn}
}

// String renders one diagnostic as the CLI text line:
//
//	nebula.toml:19:1: error[unknown_key] services.web.healthcheck: unknown key "healthcheck"; did you mean "health_check"?
//
// file is the name shown before the position ("nebula.toml" when empty).
func (d Diagnostic) Render(file string) string {
	if file == "" {
		file = "nebula.toml"
	}
	var b strings.Builder
	b.WriteString(file)
	if d.Line > 0 {
		fmt.Fprintf(&b, ":%d:%d", d.Line, max(d.Column, 1))
	}
	fmt.Fprintf(&b, ": %s[%s]", d.Severity, d.Code)
	if d.Path != "" {
		b.WriteString(" " + d.Path + ":")
	}
	b.WriteString(" " + d.Message)
	switch {
	case len(d.Suggestions) == 1:
		fmt.Fprintf(&b, "; did you mean %q?", d.Suggestions[0])
	case len(d.Suggestions) > 1:
		q := make([]string, len(d.Suggestions))
		for i, s := range d.Suggestions {
			q[i] = fmt.Sprintf("%q", s)
		}
		fmt.Fprintf(&b, "; did you mean %s?", strings.Join(q, " or "))
	}
	if d.Hint != "" {
		b.WriteString(" (" + d.Hint + ")")
	}
	if d.Environment != "" {
		fmt.Fprintf(&b, " [environment %s]", d.Environment)
	}
	return b.String()
}

// RenderText renders every diagnostic, one per line, sorted by position.
func (d Diagnostics) RenderText(file string) string {
	var b strings.Builder
	for _, x := range d.Sorted() {
		b.WriteString(x.Render(file))
		b.WriteByte('\n')
	}
	return b.String()
}

// Sorted returns a copy ordered by line, column, then code and path
// (diagnostics without a position last).
func (d Diagnostics) Sorted() Diagnostics {
	out := append(Diagnostics(nil), d...)
	sort.SliceStable(out, func(i, j int) bool {
		a, b := out[i], out[j]
		if (a.Line == 0) != (b.Line == 0) {
			return a.Line != 0
		}
		if a.Line != b.Line {
			return a.Line < b.Line
		}
		if a.Column != b.Column {
			return a.Column < b.Column
		}
		if a.Code != b.Code {
			return a.Code < b.Code
		}
		return a.Path < b.Path
	})
	return out
}

// Dedup removes diagnostics identical in code, path, message and environment.
func (d Diagnostics) Dedup() Diagnostics {
	seen := make(map[string]bool, len(d))
	out := make(Diagnostics, 0, len(d))
	for _, x := range d {
		k := x.Code + "\x00" + x.Path + "\x00" + x.Message + "\x00" + x.Environment
		if seen[k] {
			continue
		}
		seen[k] = true
		out = append(out, x)
	}
	return out
}

// diagKey identifies a diagnostic independent of the environment tag.
func diagKey(x Diagnostic) string { return x.Code + "\x00" + x.Path + "\x00" + x.Message }

// sink collects diagnostics while a pass runs.
type sink struct {
	list Diagnostics
	pos  func(path string, value bool) Pos
}

func (s *sink) add(sev, code, path, msg string) *Diagnostic {
	d := Diagnostic{Severity: sev, Code: code, Message: msg, Path: path}
	if s.pos != nil && path != "" {
		d = d.WithPos(s.pos(path, valueCodes[code]))
	}
	s.list = append(s.list, d)
	return &s.list[len(s.list)-1]
}

func (s *sink) errorf(code, path, format string, a ...any) *Diagnostic {
	return s.add(SeverityError, code, path, fmt.Sprintf(format, a...))
}

func (s *sink) warnf(code, path, format string, a ...any) *Diagnostic {
	return s.add(SeverityWarning, code, path, fmt.Sprintf(format, a...))
}

// valueCodes lists the codes whose position is the value (not the key) of the path.
var valueCodes = map[string]bool{
	CodeWrongType: true, CodeInvalidValue: true, CodeSchemaUnsupported: true, CodeLiteralSecret: true,
	CodeSecretInFile: true, CodeCredentialInURL: true, CodeInvalidReference: true, CodeInvalidTemplate: true,
	CodeReservedVariable: false, CodePathEscape: true, CodeAttachmentMissing: true, CodeInvalidUnsetPath: true,
	CodeUnknownServiceReference: true, CodeUnresolvedReference: true, CodeDerivedField: false, CodeInvalidEncoding: false,
}
