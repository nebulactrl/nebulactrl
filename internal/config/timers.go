package config

import (
	"fmt"
	"strings"
	"time"
)

// Timers are advanced overrides of product timers that nothing but a test has a
// reason to shorten. Every field is a duration in Go syntax ("45s", "10m"); the
// zero value means "use the built-in default", which the package that owns the
// timer defines (the default is documented in docs/operations/configuration.md).
// The integration suite sets short values (tests/integration/harness_timers_test.go);
// a production install leaves them unset.
type Timers struct {
	// MeshLinkGrace is how long a cross-server grant whose variable reference is
	// gone stays before the repair removes it (NEBULACTRL_MESH_LINK_GRACE;
	// built-in 10m). An edge's grant is removed at once regardless.
	MeshLinkGrace time.Duration
	// ReplicaBrokenAfter is how long a database replica may be out of streaming
	// before it is marked broken (NEBULACTRL_REPLICA_BROKEN_AFTER; built-in 2m).
	// It must stay longer than the replication poll period (30s).
	ReplicaBrokenAfter time.Duration
	// UptimeInterval is how often the control plane expects an agent to probe
	// its services (NEBULACTRL_UPTIME_INTERVAL; built-in 30s). It only scales the
	// "no check for 3 intervals + 30s means unknown" rule; the agent reads the
	// same variable for its own probe period.
	UptimeInterval time.Duration
}

// timerVar describes one Timers field: its environment variable and a pointer
// to the field.
type timerVar struct {
	env   string
	field func(*Timers) *time.Duration
}

var timerVars = []timerVar{
	{"NEBULACTRL_MESH_LINK_GRACE", func(t *Timers) *time.Duration { return &t.MeshLinkGrace }},
	{"NEBULACTRL_REPLICA_BROKEN_AFTER", func(t *Timers) *time.Duration { return &t.ReplicaBrokenAfter }},
	{"NEBULACTRL_UPTIME_INTERVAL", func(t *Timers) *time.Duration { return &t.UptimeInterval }},
}

// minTimer is the shortest accepted value: anything below is a typo, not a
// tuning choice.
const minTimer = time.Second

// LoadTimers reads the timer overrides through getenv. Unset variables stay zero.
func LoadTimers(getenv func(string) string) (Timers, error) {
	var t Timers
	var errs []string
	for _, v := range timerVars {
		raw := strings.TrimSpace(getenv(v.env))
		if raw == "" {
			continue
		}
		d, err := time.ParseDuration(raw)
		if err != nil || d < minTimer {
			errs = append(errs, fmt.Sprintf("%s must be a duration of at least 1s (for example 45s or 10m)", v.env))
			continue
		}
		*v.field(&t) = d
	}
	if len(errs) > 0 {
		return t, fmt.Errorf("%s", strings.Join(errs, "; "))
	}
	return t, nil
}
