package notifications

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/nebulactrl/nebulactrl/internal/db"
)

func TestServiceAndBackupAlertEventsAreInTheCatalog(t *testing.T) {
	want := map[string]struct {
		group, severity string
		defaultOn       bool
	}{
		TypeServiceCrashed:     {GroupServices, SeverityError, true},
		TypeServiceUnhealthy:   {GroupServices, SeverityWarning, true},
		TypeServiceRestartLoop: {GroupServices, SeverityError, true},
		TypeServiceRecovered:   {GroupServices, SeverityInfo, true},
		TypeUptimeDown:         {GroupServices, SeverityError, true},
		TypeUptimeUp:           {GroupServices, SeverityInfo, true},
		TypeBackupMissed:       {GroupBackups, SeverityError, true},
	}
	for typ, w := range want {
		e, ok := lookupType(typ)
		require.True(t, ok, typ)
		require.Equal(t, w.group, e.Group, typ)
		require.Equal(t, w.severity, e.Severity, typ)
		require.Equal(t, w.defaultOn, e.DefaultOn, typ)
		require.NotEmpty(t, e.Description, typ)
		require.Contains(t, DefaultEventTypes(), typ, "new channels subscribe by default")
	}
}

func TestRecoveryIsGreenButKeepsTheSeverityOfItsAlert(t *testing.T) {
	for _, sev := range []string{SeverityError, SeverityWarning, SeverityInfo} {
		require.Equal(t, toneOK, toneOf(TypeServiceRecovered, sev), sev)
		require.Equal(t, toneOK, toneOf(TypeUptimeUp, sev), sev)
	}
	require.Equal(t, toneBad, toneOf(TypeServiceCrashed, SeverityError))
	require.Equal(t, toneBad, toneOf(TypeUptimeDown, SeverityError))
	require.Equal(t, toneWarn, toneOf(TypeServiceUnhealthy, SeverityWarning))

	// A channel that only wants errors still gets the recovery of an error alert,
	// so alert and recovery are never separated by the severity filter.
	ch := db.NotificationChannel{
		EventTypes:  []string{TypeServiceCrashed, TypeServiceRecovered},
		MinSeverity: SeverityError, AllProjects: true,
	}
	require.True(t, matches(ch, Payload{Type: TypeServiceCrashed, Severity: SeverityError}, nil))
	require.True(t, matches(ch, Payload{Type: TypeServiceRecovered, Severity: SeverityError}, nil))
	require.False(t, matches(ch, Payload{Type: TypeServiceRecovered, Severity: SeverityWarning}, nil), "the recovery of a warning is a warning")
	require.False(t, matches(ch, Payload{Type: TypeServiceUnhealthy, Severity: SeverityWarning}, nil), "not subscribed")
}
