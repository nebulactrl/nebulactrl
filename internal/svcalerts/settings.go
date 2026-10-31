package svcalerts

import (
	"context"
	"errors"
	"strconv"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/nebulactrl/nebulactrl/internal/apierr"
)

// Settings are the thresholds of the detectors, per workspace. They decide when
// a condition becomes an incident; which channel hears about it is the
// channel's own event selection (service.crashed, service.unhealthy,
// service.restart_loop, service.recovered, backup.missed).
type Settings struct {
	// CrashGrace: how long a container must stay down (or restarting) before
	// service.crashed / service.restart_loop is sent. A restart that Docker
	// performed after a crash is announced at once.
	CrashGrace time.Duration
	// UnhealthyGrace: how long the health check must keep failing.
	UnhealthyGrace time.Duration
	// LoopRestarts within LoopWindow make a restart loop.
	LoopRestarts int
	LoopWindow   time.Duration
	// RecoverAfter: how long the service must look healthy before the incident
	// is closed and service.recovered is sent.
	RecoverAfter time.Duration
	// FlapIncidents within FlapWindow make a flapping service: it is announced
	// once as a restart loop and then stays silent until it was stable for
	// FlapWindow.
	FlapIncidents int
	FlapWindow    time.Duration
	// BackupGrace is the extra time a scheduled backup gets after its due time
	// before backup.missed is sent; 0 = automatic.
	BackupGrace time.Duration
	// LogLines runtime log lines are attached to a crash or restart-loop message.
	LogLines int
}

// DefaultSettings returns the thresholds used when a workspace set none.
func DefaultSettings() Settings {
	return Settings{
		CrashGrace:     30 * time.Second,
		UnhealthyGrace: 2 * time.Minute,
		LoopRestarts:   3,
		LoopWindow:     10 * time.Minute,
		RecoverAfter:   2 * time.Minute,
		FlapIncidents:  3,
		FlapWindow:     time.Hour,
		LogLines:       5,
	}
}

// SettingsView is the API shape of Settings.
type SettingsView struct {
	CrashGraceSeconds     int `json:"crashGraceSeconds" minimum:"0" maximum:"3600" doc:"A container that stays down (or restarting) this long is announced as crashed. A restart Docker performed after a crash is announced at once."`
	UnhealthyGraceSeconds int `json:"unhealthyGraceSeconds" minimum:"0" maximum:"3600" doc:"The health check must keep failing this long before service.unhealthy is sent."`
	LoopRestarts          int `json:"loopRestarts" minimum:"2" maximum:"50" doc:"Docker restarts within the loop window that make a restart loop."`
	LoopWindowMinutes     int `json:"loopWindowMinutes" minimum:"1" maximum:"1440" doc:"The window the restarts are counted in."`
	RecoverAfterSeconds   int `json:"recoverAfterSeconds" minimum:"10" maximum:"3600" doc:"How long a service must look healthy before the incident is closed and service.recovered is sent."`
	FlapIncidents         int `json:"flapIncidents" minimum:"2" maximum:"20" doc:"Incidents within the flap window that make a flapping service: announced once, then silent until it has been stable for a whole window."`
	FlapWindowMinutes     int `json:"flapWindowMinutes" minimum:"5" maximum:"1440" doc:"The window incidents are counted in, and how long a flapping service must be stable before its recovery is sent."`
	BackupGraceMinutes    int `json:"backupGraceMinutes" minimum:"0" maximum:"10080" doc:"Extra time a scheduled backup gets after its due time before backup.missed is sent. 0 = automatic: a quarter of the schedule's interval, between 15 minutes and 6 hours."`
	LogLines              int `json:"logLines" minimum:"0" maximum:"20" doc:"Runtime log lines attached to a crash or restart-loop message (redacted for the service's secrets). 0 attaches none."`
}

// View converts to the API shape.
func (s Settings) View() SettingsView {
	return SettingsView{
		CrashGraceSeconds:     int(s.CrashGrace / time.Second),
		UnhealthyGraceSeconds: int(s.UnhealthyGrace / time.Second),
		LoopRestarts:          s.LoopRestarts,
		LoopWindowMinutes:     int(s.LoopWindow / time.Minute),
		RecoverAfterSeconds:   int(s.RecoverAfter / time.Second),
		FlapIncidents:         s.FlapIncidents,
		FlapWindowMinutes:     int(s.FlapWindow / time.Minute),
		BackupGraceMinutes:    int(s.BackupGrace / time.Minute),
		LogLines:              s.LogLines,
	}
}

func between(name string, v, lo, hi int) error {
	if v < lo || v > hi {
		return apierr.Unprocessable("validation_failed", name+" must be between "+itoa(lo)+" and "+itoa(hi))
	}
	return nil
}

func itoa(n int) string { return strconv.Itoa(n) }

// Validate checks the ranges the database also enforces.
func (v SettingsView) Validate() error {
	for _, c := range []struct {
		name        string
		val, lo, hi int
	}{
		{"crashGraceSeconds", v.CrashGraceSeconds, 0, 3600},
		{"unhealthyGraceSeconds", v.UnhealthyGraceSeconds, 0, 3600},
		{"loopRestarts", v.LoopRestarts, 2, 50},
		{"loopWindowMinutes", v.LoopWindowMinutes, 1, 1440},
		{"recoverAfterSeconds", v.RecoverAfterSeconds, 10, 3600},
		{"flapIncidents", v.FlapIncidents, 2, 20},
		{"flapWindowMinutes", v.FlapWindowMinutes, 5, 1440},
		{"backupGraceMinutes", v.BackupGraceMinutes, 0, 10080},
		{"logLines", v.LogLines, 0, 20},
	} {
		if err := between(c.name, c.val, c.lo, c.hi); err != nil {
			return err
		}
	}
	return nil
}

// Settings converts the API shape.
func (v SettingsView) Settings() Settings {
	return Settings{
		CrashGrace:     time.Duration(v.CrashGraceSeconds) * time.Second,
		UnhealthyGrace: time.Duration(v.UnhealthyGraceSeconds) * time.Second,
		LoopRestarts:   v.LoopRestarts,
		LoopWindow:     time.Duration(v.LoopWindowMinutes) * time.Minute,
		RecoverAfter:   time.Duration(v.RecoverAfterSeconds) * time.Second,
		FlapIncidents:  v.FlapIncidents,
		FlapWindow:     time.Duration(v.FlapWindowMinutes) * time.Minute,
		BackupGrace:    time.Duration(v.BackupGraceMinutes) * time.Minute,
		LogLines:       v.LogLines,
	}
}

// Querier is what settings are read through (a pool or a transaction).
type Querier interface {
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

// LoadSettings returns the workspace's thresholds, or the defaults when none
// were saved.
func LoadSettings(ctx context.Context, q Querier, workspaceID uuid.UUID) (Settings, error) {
	var v SettingsView
	err := q.QueryRow(ctx, `SELECT crash_grace_seconds, unhealthy_grace_seconds, loop_restarts, loop_window_minutes,
		recover_after_seconds, flap_incidents, flap_window_minutes, backup_grace_minutes, log_lines
		FROM service_alert_settings WHERE workspace_id = $1`, workspaceID).Scan(
		&v.CrashGraceSeconds, &v.UnhealthyGraceSeconds, &v.LoopRestarts, &v.LoopWindowMinutes,
		&v.RecoverAfterSeconds, &v.FlapIncidents, &v.FlapWindowMinutes, &v.BackupGraceMinutes, &v.LogLines)
	if errors.Is(err, pgx.ErrNoRows) {
		return DefaultSettings(), nil
	}
	if err != nil {
		return Settings{}, err
	}
	return v.Settings(), nil
}
