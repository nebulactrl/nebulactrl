package patching

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/nebulactrl/nebulactrl/internal/db"
	"github.com/nebulactrl/nebulactrl/internal/notifications"
	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

func mustLoc(t *testing.T, name string) *time.Location {
	t.Helper()
	loc, err := time.LoadLocation(name)
	require.NoError(t, err)
	return loc
}

func nightly(start string, minutes int) AutoSettings {
	c := DefaultAutoSettings()
	c.Enabled, c.WindowStart, c.WindowMinutes = true, start, minutes
	return c
}

var longAgo = time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC)

func TestJitterIsStableAndBounded(t *testing.T) {
	seen := map[time.Duration]bool{}
	for range 200 {
		id := uuid.New()
		for _, tc := range []struct{ window, bound int }{{60, 15}, {120, 30}, {240, 30}, {100, 25}} {
			j := jitter(id, tc.window)
			require.GreaterOrEqual(t, j, time.Duration(0))
			require.Less(t, j, time.Duration(tc.bound)*time.Minute, "window %d", tc.window)
			require.Equal(t, j, jitter(id, tc.window), "stable per server")
		}
		seen[jitter(id, 240)] = true
	}
	require.Greater(t, len(seen), 10, "servers spread over the 30 minutes")
}

func TestDueSlotWindowJitterAndNoCatchUp(t *testing.T) {
	id := uuid.MustParse("11111111-2222-3333-4444-555555555555")
	c := nightly("03:00", 120)
	j := jitter(id, 120)
	start := time.Date(2031, 6, 15, 3, 0, 0, 0, time.UTC)
	due := func(now time.Time, changed time.Time) (time.Time, bool) {
		return dueSlot(now, id, c, time.UTC, changed)
	}

	_, ok := due(start.Add(j-time.Second), longAgo)
	require.False(t, ok, "not before the server's jitter")
	got, ok := due(start.Add(j), longAgo)
	require.True(t, ok)
	require.Equal(t, start, got, "the slot is the window's start, whatever the jitter")
	_, ok = due(start.Add(time.Hour), longAgo)
	require.True(t, ok)
	_, ok = due(start.Add(105*time.Minute), longAgo)
	require.True(t, ok, "up to 15 minutes before the end")
	_, ok = due(start.Add(105*time.Minute+time.Second), longAgo)
	require.False(t, ok, "nothing starts in the last 15 minutes")
	_, ok = due(start.Add(2*time.Hour+time.Minute), longAgo)
	require.False(t, ok, "no catch-up after the window")
	_, ok = due(start.Add(10*time.Hour), longAgo)
	require.False(t, ok)
	_, ok = due(start.Add(-time.Hour), longAgo)
	require.False(t, ok, "not before the window")

	// The settings saved while a window is under way take effect the next night.
	_, ok = due(start.Add(time.Hour), start.Add(10*time.Minute))
	require.False(t, ok)
	_, ok = due(start.Add(time.Hour), start.Add(-10*time.Minute))
	require.True(t, ok, "saved before the window began")
	next, ok := due(start.Add(24*time.Hour+time.Hour), start.Add(10*time.Minute))
	require.True(t, ok)
	require.Equal(t, start.Add(24*time.Hour), next)

	off := c
	off.Enabled = false
	_, ok = dueSlot(start.Add(time.Hour), id, off, time.UTC, longAgo)
	require.False(t, ok)
}

func TestDueSlotWindowAcrossMidnightAndTimeZone(t *testing.T) {
	id := uuid.MustParse("11111111-2222-3333-4444-555555555555")
	j := jitter(id, 180)
	c := nightly("23:30", 180)
	slot := time.Date(2031, 6, 15, 23, 30, 0, 0, time.UTC)
	got, ok := dueSlot(slot.Add(time.Hour+j), id, c, time.UTC, longAgo)
	require.True(t, ok, "after midnight, still yesterday's window")
	require.Equal(t, slot, got)

	// 03:00 in Tokyo is 18:00 UTC the day before.
	tokyo := mustLoc(t, "Asia/Tokyo")
	c = nightly("03:00", 120)
	now := time.Date(2031, 6, 15, 19, 0, 0, 0, time.UTC) // 04:00 on the 16th in Tokyo
	got, ok = dueSlot(now, id, c, tokyo, longAgo)
	require.True(t, ok)
	require.Equal(t, time.Date(2031, 6, 15, 18, 0, 0, 0, time.UTC), got.UTC())
}

// Across a daylight saving change, every local date has exactly one window and an
// upgrade can start in it at most once, whatever the clock does.
func TestDueSlotAcrossDaylightSavingTime(t *testing.T) {
	berlin := mustLoc(t, "Europe/Berlin")
	id := uuid.MustParse("99999999-2222-3333-4444-555555555555")
	for _, tc := range []struct {
		name  string
		from  time.Time // UTC, two days around the change
		start string
	}{
		{"spring forward, start in the gap", time.Date(2031, 3, 29, 0, 0, 0, 0, time.UTC), "02:30"},
		{"spring forward, start after the gap", time.Date(2031, 3, 29, 0, 0, 0, 0, time.UTC), "03:00"},
		{"fall back, start in the repeated hour", time.Date(2031, 10, 25, 0, 0, 0, 0, time.UTC), "02:30"},
		{"fall back, start after it", time.Date(2031, 10, 25, 0, 0, 0, 0, time.UTC), "03:00"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := nightly(tc.start, 120)
			slots := map[time.Time]bool{}
			dates := map[string]bool{}
			for now := tc.from; now.Before(tc.from.Add(72 * time.Hour)); now = now.Add(5 * time.Minute) {
				slot, ok := dueSlot(now, id, c, berlin, longAgo)
				if !ok {
					continue
				}
				slots[slot] = true
				dates[slot.In(berlin).Format("2006-01-02")] = true
				require.False(t, now.Before(slot.Add(jitter(id, 120))), "%s: before start+jitter", now)
				require.False(t, now.After(slot.Add(105*time.Minute)), "%s: inside the last 15 minutes", now)
			}
			require.Len(t, slots, len(dates), "one window per local date")
			require.GreaterOrEqual(t, len(slots), 2)
		})
	}
}

func TestNextSlotStart(t *testing.T) {
	berlin := mustLoc(t, "Europe/Berlin")
	now := time.Date(2031, 6, 15, 0, 30, 0, 0, time.UTC) // 02:30 CEST
	require.Equal(t, time.Date(2031, 6, 15, 1, 0, 0, 0, time.UTC), nextSlotStart(now, berlin, 3, 0).UTC())
	now = time.Date(2031, 6, 15, 1, 0, 0, 0, time.UTC) // exactly 03:00 CEST: the next one is tomorrow
	require.Equal(t, time.Date(2031, 6, 16, 1, 0, 0, 0, time.UTC), nextSlotStart(now, berlin, 3, 0).UTC())
	// Across a change the local time stays 03:00.
	now = time.Date(2031, 3, 29, 12, 0, 0, 0, time.UTC)
	next := nextSlotStart(now, berlin, 3, 0)
	require.Equal(t, "2031-03-30 03:00", next.In(berlin).Format("2006-01-02 15:04"))
}

func TestQuietestStart(t *testing.T) {
	// 14 days, busy all day except 04:00-05:59 UTC.
	var rows []hourlyRequests
	base := time.Date(2031, 6, 1, 0, 0, 0, 0, time.UTC)
	for d := range 14 {
		for h := range 24 {
			n := int64(100)
			if h == 4 || h == 5 {
				n = 1
			}
			rows = append(rows, hourlyRequests{Bucket: base.AddDate(0, 0, d).Add(time.Duration(h) * time.Hour), Requests: n})
		}
	}
	got := quietestStart(rows, time.UTC, 120)
	require.NotNil(t, got)
	require.Equal(t, "04:00", *got)
	got = quietestStart(rows, time.UTC, 180)
	require.Equal(t, "03:00", *got, "a three hour window: 03-05 and 04-06 cost the same; the one closer to 03:00 wins")

	// The same data seen from Berlin (UTC+2 in June): quiet at 06:00-07:59 local.
	got = quietestStart(rows, mustLoc(t, "Europe/Berlin"), 120)
	require.Equal(t, "06:00", *got)

	// Never a start in 01:00-02:59, even when that is the quietest time.
	rows = rows[:0]
	for d := range 5 {
		for h := range 24 {
			n := int64(50)
			if h == 1 || h == 2 {
				n = 0
			}
			rows = append(rows, hourlyRequests{Bucket: base.AddDate(0, 0, d).Add(time.Duration(h) * time.Hour), Requests: n})
		}
	}
	got = quietestStart(rows, time.UTC, 60)
	require.NotNil(t, got)
	require.NotContains(t, []string{"01:00", "02:00"}, *got)

	// No traffic at all, but three days of data: the default hour.
	rows = rows[:0]
	for d := range 3 {
		rows = append(rows, hourlyRequests{Bucket: base.AddDate(0, 0, d), Requests: 0})
	}
	require.Equal(t, "03:00", *quietestStart(rows, time.UTC, 120))

	require.Nil(t, quietestStart(rows[:2], time.UTC, 120), "fewer than 3 days of data")
	require.Nil(t, quietestStart(nil, time.UTC, 120))
}

func TestNormalizeAutoSettings(t *testing.T) {
	ok := func(mod func(*AutoSettings)) AutoSettings {
		c := nightly("03:00", 120)
		mod(&c)
		return c
	}
	for _, tc := range []struct {
		name string
		in   AutoSettings
		code string
	}{
		{"timezone", ok(func(c *AutoSettings) { c.Timezone = "Mars/Olympus" }), "invalid_timezone"},
		{"start not a time", ok(func(c *AutoSettings) { c.WindowStart = "3am" }), "invalid_window"},
		{"start hour", ok(func(c *AutoSettings) { c.WindowStart = "24:00" }), "invalid_window"},
		{"start minute", ok(func(c *AutoSettings) { c.WindowStart = "03:60" }), "invalid_window"},
		{"too short", ok(func(c *AutoSettings) { c.WindowMinutes = 59 }), "invalid_window"},
		{"too long", ok(func(c *AutoSettings) { c.WindowMinutes = 241 }), "invalid_window"},
		{"package", ok(func(c *AutoSettings) { c.HoldPackages = []string{"vim; reboot"} }), "invalid_package"},
		{"empty package", ok(func(c *AutoSettings) { c.HoldPackages = []string{""} }), "invalid_package"},
		{"too many packages", ok(func(c *AutoSettings) {
			for i := range MaxHoldPackages + 1 {
				c.HoldPackages = append(c.HoldPackages, fmt.Sprintf("pkg%d", i))
			}
		}), "invalid_package"},
		{"scope", ok(func(c *AutoSettings) { c.Scope = "everything" }), "validation_failed"},
		{"reboot", ok(func(c *AutoSettings) { c.Reboot = "always" }), "validation_failed"},
	} {
		_, err := tc.in.Normalize()
		require.Error(t, err, tc.name)
		require.Equal(t, tc.code, apiCode(t, err), tc.name)
	}

	got, err := ok(func(c *AutoSettings) {
		c.Timezone, c.Scope, c.Reboot, c.WindowStart = "", "", "", "04:15"
		c.HoldPackages = []string{" vim ", "vim", "libstdc++6"}
	}).Normalize()
	require.NoError(t, err)
	require.Equal(t, "UTC", got.Timezone)
	require.Equal(t, ScopeSecurity, got.Scope)
	require.Equal(t, RebootNever, got.Reboot)
	require.Equal(t, "04:15", got.WindowStart)
	require.Equal(t, []string{"vim", "libstdc++6"}, got.HoldPackages)

	many := make([]string, MaxHoldPackages)
	for i := range many {
		many[i] = fmt.Sprintf("pkg%d", i)
	}
	_, err = ok(func(c *AutoSettings) { c.HoldPackages = many; c.Timezone = "Europe/Berlin" }).Normalize()
	require.NoError(t, err, "20 holds and an IANA zone are fine")
}

// ---- with a database ----

// At night, 2031-06-15 03:40 UTC, inside the window of nightly("03:00", 120) for any jitter.
var nightClock = time.Date(2031, 6, 15, 3, 40, 0, 0, time.UTC)

func (f *fixture) at(t time.Time) { f.svc.now = func() time.Time { return t } }

func (f *fixture) autoInput(mod func(*AutoInput)) AutoInput {
	in := AutoInput{AutoSettings: nightly("03:00", 120)}
	if mod != nil {
		mod(&in)
	}
	return in
}

// updateAuto saves the settings as the instance admin. Saving them starts a check
// that hands the host's own automatic updates over (or back), which stays pending.
func (f *fixture) updateAuto(mod func(*AutoInput)) PatchAuto {
	f.t.Helper()
	a, err := f.svc.UpdateAuto(f.owner, f.server.ID, f.autoInput(mod))
	require.NoError(f.t, err)
	return a
}

// enableAuto is updateAuto without that pending check: the tests that are not
// about the hand-over start from a server nothing is in flight on.
func (f *fixture) enableAuto(mod func(*AutoInput)) PatchAuto {
	f.t.Helper()
	a := f.updateAuto(mod)
	f.settle(f.server.ID)
	return a
}

// settle ends the runs in flight on a server without a result.
func (f *fixture) settle(id uuid.UUID) {
	f.t.Helper()
	_, err := f.pool.Exec(bg, "UPDATE server_patch_runs SET status = 'failed', finished_at = now() WHERE server_id = $1 AND status = 'pending'", id)
	require.NoError(f.t, err)
}

// checkedAt sets when the server's last check ran (the sweeps run on a fake clock).
func (f *fixture) checkedAt(t time.Time) {
	f.t.Helper()
	_, err := f.pool.Exec(bg, "UPDATE server_patch_state SET checked_at = $1", t)
	require.NoError(f.t, err)
}

func (f *fixture) applyCommands() []agentprotocol.PatchingPayload {
	f.disp.mu.Lock()
	defer f.disp.mu.Unlock()
	var out []agentprotocol.PatchingPayload
	for _, c := range f.disp.cmds {
		var p agentprotocol.PatchingPayload
		require.NoError(f.t, json.Unmarshal(c.Payload, &p))
		if p.Action == agentprotocol.PatchingActionApply {
			out = append(out, p)
		}
	}
	return out
}

func (f *fixture) lastScheduledRun(action string) db.ServerPatchRun {
	f.t.Helper()
	var id uuid.UUID
	require.NoError(f.t, f.pool.QueryRow(bg,
		`SELECT id FROM server_patch_runs WHERE server_id = $1 AND trigger = 'scheduled' AND action = $2 ORDER BY created_at DESC, id DESC LIMIT 1`,
		f.server.ID, action).Scan(&id))
	return f.run(id)
}

func (f *fixture) autoEvents() []notifications.Event {
	f.emit.mu.Lock()
	defer f.emit.mu.Unlock()
	var out []notifications.Event
	for _, ev := range f.emit.evs {
		if ev.Type == notifications.TypeServerAutoUpdateFailed {
			out = append(out, ev)
		}
	}
	return out
}

func (f *fixture) addServer(name string) uuid.UUID {
	f.t.Helper()
	srv, err := f.q.CreateServer(bg, db.CreateServerParams{WorkspaceID: f.ws, Name: name})
	require.NoError(f.t, err)
	_, err = f.pool.Exec(bg, "UPDATE servers SET status = 'online' WHERE id = $1", srv.ID)
	require.NoError(f.t, err)
	return srv.ID
}

// sweepAt runs the automatic update sweep with the clock at t.
func (f *fixture) sweepAt(t time.Time) {
	f.t.Helper()
	f.at(t)
	require.NoError(f.t, f.svc.AutoSweep(bg))
}

// answerApply has the agent finish the newest scheduled upgrade.
func (f *fixture) answerApply(res agentprotocol.PatchingResult) {
	f.t.Helper()
	run := f.lastScheduledRun(agentprotocol.PatchingActionApply)
	res.Action = agentprotocol.PatchingActionApply
	f.answer(*run.CommandID, res)
}

func (f *fixture) failApply(msg string) {
	f.t.Helper()
	run := f.lastScheduledRun(agentprotocol.PatchingActionApply)
	require.NoError(f.t, f.svc.HandleResult(bg, f.server.ID, *run.CommandID, agentprotocol.ResultFrame{
		CommandID: run.CommandID.String(), Outcome: agentprotocol.OutcomeFailed, Error: msg,
	}))
}

func TestUpdateAutoValidationAndPermissions(t *testing.T) {
	f := newFixture(t)

	a, err := f.svc.Get(f.viewer, f.server.ID)
	require.NoError(t, err)
	require.False(t, a.Auto.Enabled, "off by default")
	require.Equal(t, ScopeSecurity, a.Auto.Scope)
	require.Equal(t, "03:00", a.Auto.WindowStart)
	require.Equal(t, 120, a.Auto.WindowMinutes)
	require.Equal(t, "UTC", a.Auto.Timezone)
	require.Equal(t, RebootNever, a.Auto.Reboot)
	require.Empty(t, a.Auto.HoldPackages)
	require.Contains(t, a.Auto.BuiltinHolds, "docker-ce")
	require.Contains(t, a.Auto.BuiltinHolds, "moby-*")
	require.Nil(t, a.Auto.NextWindowStart)
	require.Nil(t, a.Auto.LastScheduledRun)
	require.Nil(t, a.Auto.DisabledReason)
	require.Nil(t, a.Auto.SuggestedStart, "no traffic data")
	require.False(t, a.Auto.RequiresAcknowledgement)

	_, err = f.svc.UpdateAuto(f.other, f.server.ID, f.autoInput(nil))
	require.Equal(t, "forbidden", apiCode(t, err), "only the instance admin")
	_, err = f.svc.UpdateAuto(f.viewer, f.server.ID, f.autoInput(nil))
	require.Error(t, err)
	_, err = f.svc.UpdateAuto(f.owner, uuid.New(), f.autoInput(nil))
	require.Equal(t, "not_found", apiCode(t, err))

	for code, mod := range map[string]func(*AutoInput){
		"invalid_timezone": func(in *AutoInput) { in.Timezone = "Nowhere/Land" },
		"invalid_window":   func(in *AutoInput) { in.WindowMinutes = 30 },
		"invalid_package":  func(in *AutoInput) { in.HoldPackages = []string{"a b"} },
	} {
		_, err = f.svc.UpdateAuto(f.owner, f.server.ID, f.autoInput(mod))
		require.Equal(t, code, apiCode(t, err))
	}
	require.Empty(t, f.auditActions(), "refused requests leave no audit row")

	got := f.enableAuto(func(in *AutoInput) {
		in.Timezone, in.Reboot, in.Scope = "Europe/Berlin", RebootWhenRequired, ScopeAll
		in.HoldPackages = []string{"postgresql-16"}
	})
	require.True(t, got.Enabled)
	require.Equal(t, "Europe/Berlin", got.Timezone)
	require.Equal(t, ScopeAll, got.Scope)
	require.Equal(t, []string{"postgresql-16"}, got.HoldPackages)
	require.NotNil(t, got.NextWindowStart)
	require.True(t, got.NextWindowStart.After(time.Now()))
	require.Equal(t, []string{ActionAuto}, f.auditActions())

	// The saved settings come back with the view; the settings are replaced.
	v, err := f.svc.Get(f.viewer, f.server.ID)
	require.NoError(t, err)
	require.Equal(t, got.HoldPackages, v.Auto.HoldPackages)
	f.enableAuto(func(in *AutoInput) { in.Enabled = false })
	v, err = f.svc.Get(f.viewer, f.server.ID)
	require.NoError(t, err)
	require.False(t, v.Auto.Enabled)
	require.Nil(t, v.Auto.NextWindowStart)
	require.Empty(t, v.Auto.HoldPackages)
}

func TestUpdateAutoNeedsAcknowledgementOnProductionServers(t *testing.T) {
	f := newFixture(t)
	f.addProductionService("web")
	v, err := f.svc.Get(f.viewer, f.server.ID)
	require.NoError(t, err)
	require.True(t, v.Auto.RequiresAcknowledgement)

	_, err = f.svc.UpdateAuto(f.owner, f.server.ID, f.autoInput(nil))
	require.Equal(t, "production_ack_required", apiCode(t, err))
	_, err = f.svc.UpdateAuto(f.owner, f.server.ID, f.autoInput(func(in *AutoInput) { in.AcknowledgeProduction = true }))
	require.NoError(t, err)
	// Turning it off needs no acknowledgement.
	_, err = f.svc.UpdateAuto(f.owner, f.server.ID, f.autoInput(func(in *AutoInput) { in.Enabled = false }))
	require.NoError(t, err)
}

func TestUpdateAutoNeedsASupportingAgentAndPackageManager(t *testing.T) {
	f := newFixture(t)
	old, _ := json.Marshal(agentprotocol.Facts{OS: "linux", Capabilities: []string{agentprotocol.CapHostPatching}})
	_, err := f.pool.Exec(bg, "UPDATE servers SET facts = $2 WHERE id = $1", f.server.ID, old)
	require.NoError(t, err)
	_, err = f.svc.UpdateAuto(f.owner, f.server.ID, f.autoInput(nil))
	require.Equal(t, "agent_too_old", apiCode(t, err), "an agent without holds would upgrade Docker")
	_, err = f.svc.UpdateAuto(f.owner, f.server.ID, f.autoInput(func(in *AutoInput) { in.Enabled = false }))
	require.NoError(t, err, "turning it off always works")

	cur, _ := json.Marshal(agentprotocol.Facts{OS: "linux", Capabilities: []string{agentprotocol.CapHostPatching, agentprotocol.CapHostPatchingAuto}})
	_, err = f.pool.Exec(bg, "UPDATE servers SET facts = $2 WHERE id = $1", f.server.ID, cur)
	require.NoError(t, err)
	f.check(agentprotocol.PatchState{Supported: false, Message: "No supported package manager"})
	_, err = f.svc.UpdateAuto(f.owner, f.server.ID, f.autoInput(nil))
	require.Equal(t, "patching_unsupported", apiCode(t, err))
	_, err = f.svc.UpdateAuto(f.owner, f.server.ID, f.autoInput(func(in *AutoInput) { in.Enabled = false }))
	require.Equal(t, "scope_unsupported", apiCode(t, err), "security-only needs apt or dnf")
	_, err = f.svc.UpdateAuto(f.owner, f.server.ID, f.autoInput(func(in *AutoInput) { in.Enabled, in.Scope = false, ScopeAll }))
	require.NoError(t, err)
	v, err := f.svc.Get(f.viewer, f.server.ID)
	require.NoError(t, err)
	require.False(t, v.Auto.SecurityOnlySupported)
}

func TestAutoSweepStartsOneScheduledUpgradePerWindow(t *testing.T) {
	f := newFixture(t)
	f.enableAuto(func(in *AutoInput) { in.HoldPackages = []string{"postgresql-16"} })
	f.check(state(3, 1, false, "b1", opensslSec, vim)) // a fresh check with updates
	before := f.disp.count()

	f.sweepAt(nightClock.Add(-2 * time.Hour))
	require.Equal(t, before, f.disp.count(), "before the window nothing starts")

	f.sweepAt(nightClock)
	require.Equal(t, before+1, f.disp.count())
	p := f.disp.last(t)
	require.Equal(t, agentprotocol.PatchingActionApply, p.Action)
	require.Equal(t, agentprotocol.PatchingTriggerScheduled, p.Trigger)
	require.True(t, p.SecurityOnly, "the default scope is security")
	require.Equal(t, []string{"postgresql-16"}, p.HoldPackages)
	require.Equal(t, agentprotocol.PatchNativeDisable, p.Native, "the host's own automatic updates are switched off")
	require.False(t, p.CheckContainers)
	run := f.lastScheduledRun(agentprotocol.PatchingActionApply)
	require.NotNil(t, run.SlotStart)
	require.True(t, run.SecurityOnly)
	require.Nil(t, run.RequestedBy)
	require.EqualValues(t, 1, *run.UpdatesBefore)

	f.sweepAt(nightClock.Add(5 * time.Minute))
	require.Equal(t, before+1, f.disp.count(), "a run in flight is not duplicated")

	// Answered (failed or not), the window has had its upgrade: no second try tonight.
	f.answerApply(agentprotocol.PatchingResult{State: state(2, 0, false, "b1", vim)})
	f.sweepAt(nightClock.Add(20 * time.Minute))
	require.Equal(t, before+1, f.disp.count(), "one upgrade per window")
	require.Contains(t, f.auditActions(), ActionRun, "the finish of a scheduled upgrade is audited")

	// Tomorrow night there is a new window; the state says there is something to do.
	f.sweepAt(nightClock.Add(24 * time.Hour))
	require.Equal(t, before+2, f.disp.count())
}

func TestAutoSweepSkipsWhenThereIsNothingToDoOrTheServerIsNotReady(t *testing.T) {
	f := newFixture(t)
	f.enableAuto(nil)
	count := func() int { return len(f.applyCommands()) }

	f.check(state(0, 0, false, "b1"))
	f.checkedAt(nightClock.Add(-time.Hour))
	f.sweepAt(nightClock)
	require.Zero(t, count(), "a fresh check without updates: nothing recorded, nothing sent")
	var runs int
	require.NoError(t, f.pool.QueryRow(bg, "SELECT count(*) FROM server_patch_runs WHERE action = 'apply'").Scan(&runs))
	require.Zero(t, runs)

	// Only non-security updates and the default scope: nothing to do either.
	f.check(state(2, 0, false, "b1", vim))
	f.checkedAt(nightClock.Add(-time.Hour))
	f.sweepAt(nightClock)
	require.Zero(t, count())

	// A stale check is not believed: the agent looks again.
	f.checkedAt(nightClock.Add(-7 * time.Hour))
	f.sweepAt(nightClock)
	require.Equal(t, 1, count())
	f.answerApply(agentprotocol.PatchingResult{State: state(0, 0, false, "b1")})
	f.checkedAt(nightClock.Add(23 * time.Hour))

	// An offline server, and one whose agent cannot hold packages, are left alone.
	f.sweepAt(nightClock.Add(24 * time.Hour))
	require.Equal(t, 1, count(), "fresh state again says nothing is pending")
	f.checkedAt(nightClock.Add(24*time.Hour - 7*time.Hour))
	_, err := f.pool.Exec(bg, "UPDATE servers SET status = 'offline' WHERE id = $1", f.server.ID)
	require.NoError(t, err)
	f.sweepAt(nightClock.Add(24 * time.Hour))
	require.Equal(t, 1, count())
	_, err = f.pool.Exec(bg, "UPDATE servers SET status = 'online' WHERE id = $1", f.server.ID)
	require.NoError(t, err)
	old, _ := json.Marshal(agentprotocol.Facts{OS: "linux", Capabilities: []string{agentprotocol.CapHostPatching}})
	_, err = f.pool.Exec(bg, "UPDATE servers SET facts = $2 WHERE id = $1", f.server.ID, old)
	require.NoError(t, err)
	f.sweepAt(nightClock.Add(24 * time.Hour))
	require.Equal(t, 1, count())
}

func TestAutoSweepWaitsForBusyServersAndRunsOneUpgradeAtATime(t *testing.T) {
	f := newFixture(t)
	second := f.addServer("Agent 2")
	f.enableAuto(nil)
	_, err := f.svc.UpdateAuto(f.owner, second, f.autoInput(nil))
	require.NoError(t, err)
	f.settle(second)
	for _, id := range []uuid.UUID{f.server.ID, second} {
		_, err := f.pool.Exec(bg, `INSERT INTO server_patch_state (server_id, workspace_id, manager, supported, updates, security_updates, checked_at)
			VALUES ($1, $2, 'apt', true, 2, 2, now())`, id, f.ws)
		require.NoError(t, err)
	}

	f.sweepAt(nightClock)
	require.Len(t, f.applyCommands(), 1, "two servers are due, one upgrade runs")
	var runningOn uuid.UUID
	require.NoError(t, f.pool.QueryRow(bg, "SELECT server_id FROM server_patch_runs WHERE action = 'apply'").Scan(&runningOn))

	f.sweepAt(nightClock.Add(10 * time.Minute))
	require.Len(t, f.applyCommands(), 1, "still waiting for the first to finish")

	// The first finishes; the other starts at the next sweep, still inside its window.
	require.NoError(t, f.pool.QueryRow(bg, "SELECT command_id FROM server_patch_runs WHERE action = 'apply'").Scan(new(uuid.UUID)))
	_, err = f.pool.Exec(bg, "UPDATE server_patch_runs SET status = 'succeeded', finished_at = now() WHERE action = 'apply'")
	require.NoError(t, err)
	f.sweepAt(nightClock.Add(15 * time.Minute))
	require.Len(t, f.applyCommands(), 2)
	var other int
	require.NoError(t, f.pool.QueryRow(bg, "SELECT count(DISTINCT server_id) FROM server_patch_runs WHERE action = 'apply'").Scan(&other))
	require.Equal(t, 2, other)

	// A deployment on the server holds the upgrade back.
	_, err = f.pool.Exec(bg, "UPDATE server_patch_runs SET status = 'succeeded', finished_at = now() WHERE action = 'apply'")
	require.NoError(t, err)
	f.addProductionService("api")
	var svcID uuid.UUID
	require.NoError(t, f.pool.QueryRow(bg, "SELECT id FROM services WHERE server_id = $1", f.server.ID).Scan(&svcID))
	_, err = f.pool.Exec(bg, `INSERT INTO deployments (service_id, server_id, trigger, actor_type, status) VALUES ($1, $2, 'manual', 'user', 'building')`, svcID, f.server.ID)
	require.NoError(t, err)
	_, err = f.pool.Exec(bg, "UPDATE server_patch_state SET checked_at = now()")
	require.NoError(t, err)
	f.sweepAt(nightClock.Add(24 * time.Hour))
	require.Len(t, f.applyCommands(), 3, "only the server without a deployment started")
	var onBusy int
	require.NoError(t, f.pool.QueryRow(bg, "SELECT count(*) FROM server_patch_runs WHERE action = 'apply' AND server_id = $1", f.server.ID).Scan(&onBusy))
	require.Equal(t, 1, onBusy, "the server with a deployment in flight did not start tonight's upgrade")

	// A backup in flight counts too.
	_, err = f.pool.Exec(bg, "UPDATE deployments SET status = 'succeeded'")
	require.NoError(t, err)
	busy, err := f.q.HasServerBusyWork(bg, &f.server.ID)
	require.NoError(t, err)
	require.False(t, busy)
	_, err = f.pool.Exec(bg, "UPDATE deployments SET status = 'releasing'")
	require.NoError(t, err)
	busy, err = f.q.HasServerBusyWork(bg, &f.server.ID)
	require.NoError(t, err)
	require.True(t, busy)
}

func TestThreeFailedScheduledRunsSwitchAutomaticUpdatesOff(t *testing.T) {
	f := newFixture(t)
	f.enableAuto(nil)
	f.check(state(3, 1, false, "b1", opensslSec, vim))
	day := func(n int) time.Time { return nightClock.Add(time.Duration(n) * 24 * time.Hour) }
	startNight := func(n int) {
		_, err := f.pool.Exec(bg, "UPDATE server_patch_state SET checked_at = now(), security_updates = 1, updates = 3")
		require.NoError(f.t, err)
		before := len(f.applyCommands())
		f.sweepAt(day(n))
		require.Len(f.t, f.applyCommands(), before+1, "night %d", n)
	}
	auto := func() PatchAuto {
		v, err := f.svc.Get(f.viewer, f.server.ID)
		require.NoError(t, err)
		return v.Auto
	}

	startNight(0)
	f.failApply("E: dpkg was interrupted")
	require.Equal(t, 1, auto().ConsecutiveFailures)
	require.True(t, auto().Enabled)
	evs := f.autoEvents()
	require.Len(t, evs, 1)
	require.Equal(t, notifications.SeverityWarning, evs[0].Severity)
	require.Contains(t, evs[0].Title, "Agent 1")
	require.Contains(t, evs[0].Body, "dpkg was interrupted")
	require.Equal(t, "failed", auto().LastScheduledRun.Status)

	// A success starts the count over.
	startNight(1)
	f.answerApply(agentprotocol.PatchingResult{State: state(0, 0, false, "b1")})
	require.Zero(t, auto().ConsecutiveFailures)
	require.Len(t, f.autoEvents(), 1, "a success is not announced")

	startNight(2)
	f.failApply("disk full")
	startNight(3)
	f.failApply("disk full")
	require.Equal(t, 2, auto().ConsecutiveFailures)
	require.True(t, auto().Enabled)
	startNight(4)
	f.failApply("lock held")
	a := auto()
	require.False(t, a.Enabled, "the third failure in a row switches it off")
	require.Equal(t, MaxAutoFailures, a.ConsecutiveFailures)
	require.NotNil(t, a.DisabledReason)
	require.Contains(t, *a.DisabledReason, "3 failed scheduled runs in a row")
	require.Contains(t, *a.DisabledReason, "lock held")
	require.Nil(t, a.NextWindowStart)
	evs = f.autoEvents()
	require.Len(t, evs, 4)
	require.Contains(t, evs[3].Title, "switched off")
	require.Contains(t, f.auditActions(), ActionAuto)

	// Off: nothing starts any more.
	before := len(f.applyCommands())
	f.sweepAt(day(5))
	require.Len(t, f.applyCommands(), before)

	// Saving the settings again clears the reason and the count.
	f.enableAuto(nil)
	a = auto()
	require.True(t, a.Enabled)
	require.Nil(t, a.DisabledReason)
	require.Zero(t, a.ConsecutiveFailures)
}

func TestUndeliveredScheduledUpgradeCountsAsAFailure(t *testing.T) {
	f := newFixture(t)
	f.enableAuto(nil)
	f.check(state(3, 1, false, "b1", opensslSec, vim))
	f.sweepAt(nightClock)
	run := f.lastScheduledRun(agentprotocol.PatchingActionApply)
	require.NoError(t, f.svc.HandleUndelivered(bg, *run.CommandID, "Not delivered: server offline. No changes were made."))
	require.Len(t, f.autoEvents(), 1)
	row, err := f.q.GetServerPatchAutoSettings(bg, f.server.ID)
	require.NoError(t, err)
	require.EqualValues(t, 1, row.ConsecutiveFailures)

	// A failed manual upgrade does not count.
	f.check(state(3, 1, false, "b1", opensslSec, vim))
	_, err = f.svc.Apply(f.owner, f.server.ID, ApplyInput{Confirm: "Agent 1"})
	require.NoError(t, err)
	manual := f.run(mustLatestRun(t, f))
	require.Equal(t, agentprotocol.PatchingTriggerManual, manual.Trigger)
	require.NoError(t, f.svc.HandleUndelivered(bg, *manual.CommandID, "Not delivered"))
	row, err = f.q.GetServerPatchAutoSettings(bg, f.server.ID)
	require.NoError(t, err)
	require.EqualValues(t, 1, row.ConsecutiveFailures)
}

func mustLatestRun(t *testing.T, f *fixture) uuid.UUID {
	t.Helper()
	var id uuid.UUID
	require.NoError(t, f.pool.QueryRow(bg, "SELECT id FROM server_patch_runs ORDER BY created_at DESC, id DESC LIMIT 1").Scan(&id))
	return id
}

func TestAutomaticRebootOnlyWhenEveryGuardHolds(t *testing.T) {
	safe := &agentprotocol.PatchContainers{Known: true, Running: 2}
	needsReboot := func(c *agentprotocol.PatchContainers) agentprotocol.PatchingResult {
		return agentprotocol.PatchingResult{State: state(0, 0, true, "b1"), Containers: c}
	}
	rebootRuns := func(f *fixture) int {
		var n int
		require.NoError(t, f.pool.QueryRow(bg, "SELECT count(*) FROM server_patch_runs WHERE action = 'reboot'").Scan(&n))
		return n
	}
	setup := func(t *testing.T, mod func(*AutoInput)) *fixture {
		f := newFixture(t)
		f.enableAuto(func(in *AutoInput) {
			in.Reboot = RebootWhenRequired
			if mod != nil {
				mod(in)
			}
		})
		f.check(state(3, 1, false, "b1", opensslSec, vim))
		f.sweepAt(nightClock)
		return f
	}

	t.Run("all guards hold", func(t *testing.T) {
		f := setup(t, nil)
		require.True(t, f.disp.last(t).CheckContainers, "the agent is asked for the containers' restart policies")
		f.answerApply(needsReboot(safe))
		require.Equal(t, 1, rebootRuns(f))
		p := f.disp.last(t)
		require.Equal(t, agentprotocol.PatchingActionReboot, p.Action)
		require.Equal(t, agentprotocol.PatchingTriggerScheduled, p.Trigger)
		reboot := f.lastScheduledRun(agentprotocol.PatchingActionReboot)
		require.Equal(t, StatusPending, reboot.Status)
		require.NotNil(t, reboot.SlotStart)
		require.Nil(t, reboot.RequestedBy)
	})
	t.Run("never", func(t *testing.T) {
		f := setup(t, func(in *AutoInput) { in.Reboot = RebootNever })
		f.answerApply(needsReboot(nil))
		require.Zero(t, rebootRuns(f))
		require.False(t, f.disp.last(t).CheckContainers)
		require.NotContains(t, f.lastScheduledRun(agentprotocol.PatchingActionApply).Message, "reboot")
	})
	t.Run("no reboot needed", func(t *testing.T) {
		f := setup(t, nil)
		f.answerApply(agentprotocol.PatchingResult{State: state(0, 0, false, "b1"), Containers: safe})
		require.Zero(t, rebootRuns(f))
	})
	t.Run("a container that does not restart", func(t *testing.T) {
		f := setup(t, nil)
		f.answerApply(needsReboot(&agentprotocol.PatchContainers{Known: true, Running: 2, Unsafe: []string{"job"}}))
		require.Zero(t, rebootRuns(f))
		msg := f.lastScheduledRun(agentprotocol.PatchingActionApply).Message
		require.Contains(t, msg, "Automatic reboot skipped")
		require.Contains(t, msg, "job")
	})
	t.Run("restart policies unknown", func(t *testing.T) {
		f := setup(t, nil)
		f.answerApply(needsReboot(&agentprotocol.PatchContainers{Reason: "the Docker daemon did not answer"}))
		require.Zero(t, rebootRuns(f))
		require.Contains(t, f.lastScheduledRun(agentprotocol.PatchingActionApply).Message, "did not answer")
	})
	t.Run("agent did not report containers", func(t *testing.T) {
		f := setup(t, nil)
		f.answerApply(needsReboot(nil))
		require.Zero(t, rebootRuns(f))
		require.Contains(t, f.lastScheduledRun(agentprotocol.PatchingActionApply).Message, "could not be read")
	})
	t.Run("the control plane host", func(t *testing.T) {
		f := setup(t, nil)
		_, err := f.pool.Exec(bg, "UPDATE servers SET is_control_plane = true WHERE id = $1", f.server.ID)
		require.NoError(t, err)
		f.answerApply(needsReboot(safe))
		require.Zero(t, rebootRuns(f))
		require.Contains(t, f.lastScheduledRun(agentprotocol.PatchingActionApply).Message, "control plane")
	})
	t.Run("less than ten minutes left", func(t *testing.T) {
		f := setup(t, nil)
		f.at(nightClock.Truncate(24 * time.Hour).Add(4*time.Hour + 51*time.Minute)) // the window ends at 05:00
		f.answerApply(needsReboot(safe))
		require.Zero(t, rebootRuns(f))
		require.Contains(t, f.lastScheduledRun(agentprotocol.PatchingActionApply).Message, "10 minutes")
	})
	t.Run("ten minutes left is enough", func(t *testing.T) {
		f := setup(t, nil)
		f.at(nightClock.Truncate(24 * time.Hour).Add(4*time.Hour + 50*time.Minute))
		f.answerApply(needsReboot(safe))
		require.Equal(t, 1, rebootRuns(f))
	})
	t.Run("one automatic reboot a day", func(t *testing.T) {
		f := setup(t, nil)
		f.answerApply(needsReboot(safe))
		require.Equal(t, 1, rebootRuns(f))
		reboot := f.lastScheduledRun(agentprotocol.PatchingActionReboot)
		f.answer(*reboot.CommandID, agentprotocol.PatchingResult{Action: agentprotocol.PatchingActionReboot, State: agentprotocol.PatchState{BootID: "b1"}})
		_, err := f.pool.Exec(bg, "UPDATE server_patch_runs SET status = 'succeeded', finished_at = now() WHERE action = 'reboot'")
		require.NoError(t, err)

		_, err = f.pool.Exec(bg, "UPDATE server_patch_state SET checked_at = now(), security_updates = 1, updates = 3")
		require.NoError(t, err)
		f.sweepAt(nightClock.Add(24 * time.Hour))
		f.answerApply(needsReboot(safe))
		require.Equal(t, 1, rebootRuns(f), "a reboot within the last 24 hours blocks the next one")
		require.Contains(t, f.lastScheduledRun(agentprotocol.PatchingActionApply).Message, "24 hours")
	})
}

func TestHostAutomaticUpdatesAreHandedOverAndBack(t *testing.T) {
	f := newFixture(t)
	// Turning the schedule on asks the agent at once (a check that carries the directive).
	f.updateAuto(nil)
	require.Equal(t, 1, f.disp.count())
	p := f.disp.last(t)
	require.Equal(t, agentprotocol.PatchingActionCheck, p.Action)
	require.Equal(t, agentprotocol.PatchNativeDisable, p.Native)
	run := f.lastScheduledRun(agentprotocol.PatchingActionCheck)
	f.answer(*run.CommandID, agentprotocol.PatchingResult{
		Action: agentprotocol.PatchingActionCheck, NativeApplied: agentprotocol.PatchNativeDisable,
		State: agentprotocol.PatchState{Manager: "apt", Supported: true, BootID: "b1", NativeAutoUpdates: ""},
	})
	row, err := f.q.GetServerPatchAutoSettings(bg, f.server.ID)
	require.NoError(t, err)
	require.True(t, row.NativeHandoff)

	// In sync: the sweep leaves the host alone.
	f.sweepAt(nightClock.Add(-6 * time.Hour))
	require.Equal(t, 1, f.disp.count())

	// Every later check of an enabled server carries the directive too (self-healing).
	f.check(state(0, 0, false, "b1"))
	require.Equal(t, agentprotocol.PatchNativeDisable, f.disp.last(t).Native)

	// Turning it off hands the host's mechanism back.
	f.updateAuto(func(in *AutoInput) { in.Enabled = false })
	p = f.disp.last(t)
	require.Equal(t, agentprotocol.PatchingActionCheck, p.Action)
	require.Equal(t, agentprotocol.PatchNativeRestore, p.Native)
	run = f.lastScheduledRun(agentprotocol.PatchingActionCheck)
	f.answer(*run.CommandID, agentprotocol.PatchingResult{Action: agentprotocol.PatchingActionCheck, NativeApplied: agentprotocol.PatchNativeRestore, State: state(0, 0, false, "b1")})
	row, err = f.q.GetServerPatchAutoSettings(bg, f.server.ID)
	require.NoError(t, err)
	require.False(t, row.NativeHandoff)
	f.check(state(0, 0, false, "b1"))
	require.Empty(t, f.disp.last(t).Native, "nothing left to hand back")
}

func TestOfflineServerGetsItsHandOverFromTheSweep(t *testing.T) {
	f := newFixture(t)
	_, err := f.pool.Exec(bg, "UPDATE servers SET status = 'offline' WHERE id = $1", f.server.ID)
	require.NoError(t, err)
	f.updateAuto(nil)
	require.Zero(t, f.disp.count(), "offline: nothing could be sent")
	_, err = f.pool.Exec(bg, "UPDATE servers SET status = 'online' WHERE id = $1", f.server.ID)
	require.NoError(t, err)
	f.sweepAt(nightClock.Add(-6 * time.Hour))
	require.Equal(t, 1, f.disp.count())
	require.Equal(t, agentprotocol.PatchNativeDisable, f.disp.last(t).Native)
	// An answer that did not switch anything is not retried in a loop.
	run := f.lastScheduledRun(agentprotocol.PatchingActionCheck)
	f.answer(*run.CommandID, agentprotocol.PatchingResult{Action: agentprotocol.PatchingActionCheck, State: state(0, 0, false, "b1")})
	f.sweepAt(nightClock.Add(-6*time.Hour + 10*time.Minute))
	require.Equal(t, 1, f.disp.count(), "tried less than 30 minutes ago")
}

func TestNativeAutoUpdatesAreShownFromTheLastCheck(t *testing.T) {
	f := newFixture(t)
	f.check(agentprotocol.PatchState{Manager: "apt", Supported: true, BootID: "b1", NativeAutoUpdates: agentprotocol.PatchNativeUnattendedUpgrades})
	v, err := f.svc.Get(f.viewer, f.server.ID)
	require.NoError(t, err)
	require.Equal(t, "unattended-upgrades", v.Auto.NativeAutoUpdates)
	require.True(t, v.Auto.SecurityOnlySupported)
}

func TestQuietHourSuggestionFromTraffic(t *testing.T) {
	f := newFixture(t)
	base := time.Now().UTC().Truncate(24*time.Hour).AddDate(0, 0, -10)
	for d := range 10 {
		for h := range 24 {
			n := 100
			if h >= 3 && h <= 5 {
				n = 1
			}
			_, err := f.pool.Exec(bg, `INSERT INTO traffic_1h (server_id, host, bucket, requests, lat_hist) VALUES ($1, 'example.com', $2, $3, '{}')`,
				f.server.ID, base.AddDate(0, 0, d).Add(time.Duration(h)*time.Hour), n)
			require.NoError(t, err)
		}
	}
	v, err := f.svc.Get(f.viewer, f.server.ID)
	require.NoError(t, err)
	require.NotNil(t, v.Auto.SuggestedStart)
	require.Equal(t, "03:00", *v.Auto.SuggestedStart)
	require.True(t, strings.HasSuffix(*v.Auto.SuggestedStart, ":00"))
}
