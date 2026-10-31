package hardening

import (
	"context"
	_ "embed"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"strings"

	"github.com/nebulactrl/nebulactrl/internal/lynis"
	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

//go:embed install_lynis.sh
var installLynisScript string

// auditScript runs a Lynis audit and prints the parts of its report NebulaCtrl
// keeps. It runs from / because Lynis treats a directory with a db/ or include/
// folder as its own installation. A report whose audit did not finish has no
// hardening_index and is rejected by the parser.
const auditScript = `
cd / || exit 1
LC_ALL=C
export LC_ALL
PATH="$PATH:/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin:/usr/local/lynis"
export PATH
if ! command -v lynis >/dev/null 2>&1; then echo "@@NOLYNIS"; exit 0; fi
echo "@@VERSION $(lynis show version 2>/dev/null | head -n1)"
if command -v timeout >/dev/null 2>&1; then _t="timeout 1200"; else _t=""; fi
REPORT="${NC_LYNIS_REPORT:-` + lynis.ReportPath + `}"
rm -f "$REPORT"
$_t lynis audit system --quick --no-colors --cronjob >/dev/null 2>&1
echo "@@RC $?"
echo "@@REPORT"
grep -E '^(lynis_version|hostname|os_name|os_version|report_datetime_end|hardening_index|lynis_tests_done|vulnerable_packages_found|warning\[\]|suggestion\[\]|details\[\])=' "$REPORT" 2>/dev/null
exit 0
`

// Service performs the host_hardening actions through a Runner.
type Service struct {
	runner Runner
	log    *slog.Logger
}

// New returns a Service.
func New(r Runner, log *slog.Logger) *Service {
	if log == nil {
		log = slog.Default()
	}
	return &Service{runner: r, log: log.With("component", "hardening")}
}

// ErrNoLynis means Lynis is not installed on the host.
var ErrNoLynis = errors.New("lynis is not installed")

// Handle runs one host_hardening command.
func (s *Service) Handle(ctx context.Context, p agentprotocol.HardeningPayload) (agentprotocol.HardeningResult, error) {
	res := agentprotocol.HardeningResult{Action: p.Action, Mode: s.runner.Mode()}
	switch p.Action {
	case agentprotocol.HardeningActionAudit:
		if err := s.audit(ctx, &res, p.InstallLynis); err != nil && !errors.Is(err, ErrNoLynis) {
			return res, err
		}
	case agentprotocol.HardeningActionApply:
		ids := p.FixIDs
		if len(ids) == 0 {
			ids = lynis.Defaults()
		}
		fixes, unknown := lynis.Select(ids)
		if len(unknown) > 0 {
			return res, fmt.Errorf("unknown fix %s", strings.Join(unknown, ", "))
		}
		if p.InstallLynis {
			if err := s.ensureLynis(ctx, &res); err != nil {
				return res, err
			}
		}
		if err := s.change(ctx, &res, lynis.ScriptApply, fixes); err != nil {
			return res, err
		}
		s.reaudit(ctx, &res)
	case agentprotocol.HardeningActionRollback:
		fixes, unknown := lynis.Select(p.FixIDs)
		if len(unknown) > 0 {
			return res, fmt.Errorf("unknown fix %s", strings.Join(unknown, ", "))
		}
		if len(fixes) == 0 {
			return res, errors.New("rollback needs at least one fix")
		}
		if err := s.change(ctx, &res, lynis.ScriptRollback, fixes); err != nil {
			return res, err
		}
		s.reaudit(ctx, &res)
	default:
		return res, fmt.Errorf("unknown hardening action %q", p.Action)
	}
	// The state of every fix after whatever happened.
	if err := s.fillStates(ctx, &res); err != nil {
		return res, err
	}
	return res, nil
}

// reaudit audits again after a change so the new index is known. It never
// fails the command: the fixes were applied, and that is what must be reported.
func (s *Service) reaudit(ctx context.Context, res *agentprotocol.HardeningResult) {
	err := s.audit(ctx, res, false)
	if err != nil && !errors.Is(err, ErrNoLynis) {
		res.Message = "The fixes were applied, but the follow-up audit failed: " + err.Error()
		s.log.Warn("follow-up lynis audit failed", "err", err)
	}
}

// ensureLynis installs Lynis when it is missing.
func (s *Service) ensureLynis(ctx context.Context, res *agentprotocol.HardeningResult) error {
	out, err := s.runner.Run(ctx, installLynisScript)
	if err != nil {
		return fmt.Errorf("install Lynis: %w", err)
	}
	if out.ExitCode != 0 {
		return fmt.Errorf("install Lynis: %s", tailLine(out))
	}
	if !strings.Contains(out.Stdout, "already installed") {
		res.LynisInstalledNow = true
		s.log.Info("lynis installed")
	}
	return nil
}

// audit runs Lynis (installing it first when asked) and stores the report.
// Without Lynis it leaves LynisInstalled false and returns ErrNoLynis.
func (s *Service) audit(ctx context.Context, res *agentprotocol.HardeningResult, install bool) error {
	if install {
		if err := s.ensureLynis(ctx, res); err != nil {
			return err
		}
	}
	out, err := s.runner.Run(ctx, auditScript)
	if err != nil {
		return fmt.Errorf("run Lynis: %w", err)
	}
	if out.ExitCode != 0 {
		return fmt.Errorf("run Lynis: %s", tailLine(out))
	}
	if strings.Contains(out.Stdout, "@@NOLYNIS") {
		res.LynisInstalled = false
		res.Message = "Lynis is not installed on this server."
		return ErrNoLynis
	}
	res.LynisInstalled = true
	version, rc, report := splitAudit(out.Stdout)
	res.LynisVersion = version
	if rc != 0 {
		return fmt.Errorf("lynis exited with status %d (another audit may be running)", rc)
	}
	rep, err := lynis.ParseReport(strings.NewReader(report))
	if err != nil {
		return err
	}
	if res.LynisVersion == "" {
		res.LynisVersion = rep.LynisVersion
	}
	res.Report = rep
	return nil
}

// splitAudit separates the @@VERSION and @@RC lines from the report lines.
func splitAudit(out string) (version string, rc int, report string) {
	var b strings.Builder
	inReport := false
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimRight(line, "\r")
		switch {
		case inReport:
			b.WriteString(line)
			b.WriteByte('\n')
		case line == "@@REPORT":
			inReport = true
		case strings.HasPrefix(line, "@@VERSION "):
			version = strings.TrimSpace(strings.TrimPrefix(line, "@@VERSION "))
		case strings.HasPrefix(line, "@@RC "):
			rc, _ = strconv.Atoi(strings.TrimSpace(strings.TrimPrefix(line, "@@RC ")))
		}
	}
	return version, rc, b.String()
}

// change applies or rolls back fixes and records the outcomes.
func (s *Service) change(ctx context.Context, res *agentprotocol.HardeningResult, action string, fixes []lynis.Fix) error {
	out, err := s.runner.Run(ctx, lynis.Script(action, fixes))
	if err != nil {
		return fmt.Errorf("%s fixes: %w", action, err)
	}
	_, outcomes, reboot := lynis.ParseDriverOutput(out.Stdout)
	if len(outcomes) == 0 {
		return fmt.Errorf("%s fixes: the host returned no result (exit %d): %s", action, out.ExitCode, tailLine(out))
	}
	res.Outcomes, res.RebootRequired = outcomes, reboot
	for _, o := range outcomes {
		s.log.Info("hardening fix", "action", action, "fix", o.ID, "outcome", o.Outcome, "message", o.Message)
	}
	return nil
}

// fillStates asks the host for the state of every catalog fix.
func (s *Service) fillStates(ctx context.Context, res *agentprotocol.HardeningResult) error {
	fixes := lynis.Catalog()
	out, err := s.runner.Run(ctx, lynis.Script(lynis.ScriptStatus, fixes))
	if err != nil {
		return fmt.Errorf("check fixes: %w", err)
	}
	states, _, _ := lynis.ParseDriverOutput(out.Stdout)
	if len(states) == 0 {
		return fmt.Errorf("check fixes: the host returned no result (exit %d): %s", out.ExitCode, tailLine(out))
	}
	res.Fixes = states
	return nil
}

// tailLine is the last line the script printed, for error messages.
func tailLine(o Output) string {
	text := strings.TrimSpace(o.Stderr)
	if text == "" {
		text = strings.TrimSpace(o.Stdout)
	}
	if i := strings.LastIndex(text, "\n"); i >= 0 {
		text = text[i+1:]
	}
	if text == "" {
		text = "no output, exit " + strconv.Itoa(o.ExitCode)
	}
	if len(text) > 300 {
		text = text[:300]
	}
	return text
}
