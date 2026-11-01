package backups

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/nebulactrl/nebulactrl/internal/auth"
	"github.com/nebulactrl/nebulactrl/internal/db"
	"github.com/nebulactrl/nebulactrl/internal/policy"
	"github.com/nebulactrl/nebulactrl/internal/prodguard"
	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

// restToken is a REST token as authentication builds it: guarded unless exempt.
func (f *fixture) restToken() context.Context {
	p, _ := auth.PrincipalFrom(f.ctx)
	tok := uuid.New()
	return auth.WithPrincipal(context.Background(), &auth.Principal{
		Type: auth.TypeToken, UserID: p.UserID, TokenID: &tok, WorkspaceID: f.ws.ID, Role: auth.RoleOwner,
		Label: "ci", Scopes: policy.AllScopes, RequireProductionApproval: true,
	})
}

// mcpClient is an MCP principal; MCP is guarded by default.
func (f *fixture) mcpClient() context.Context {
	p, _ := auth.PrincipalFrom(f.ctx)
	tok := uuid.New()
	return auth.WithPrincipal(context.Background(), &auth.Principal{
		Type: auth.TypeMCP, UserID: p.UserID, TokenID: &tok, WorkspaceID: f.ws.ID, Role: auth.RoleOwner,
		Label: "claude-code", Scopes: policy.AllScopes,
	})
}

// person is the human who approves.
func (f *fixture) person() context.Context {
	p, _ := auth.PrincipalFrom(f.ctx)
	return auth.WithPrincipal(context.Background(), &auth.Principal{
		Type: auth.TypeUser, UserID: p.UserID, WorkspaceID: f.ws.ID, Role: auth.RoleOwner, Label: "Ada", Scopes: policy.AllScopes,
	})
}

func (f *fixture) installGuard() *prodguard.Guard {
	f.t.Helper()
	g := prodguard.New(prodguard.Deps{Pool: f.pool, Box: f.box})
	g.Install()
	f.t.Cleanup(prodguard.Uninstall)
	return g
}

func (f *fixture) approve(g *prodguard.Guard, err error) {
	f.t.Helper()
	ar, ok := prodguard.AsRequired(err)
	require.True(f.t, ok, "want approval_required, got %v", err)
	_, aerr := g.Approve(f.person(), ar.ApprovalID)
	require.NoError(f.t, aerr)
}

func (f *fixture) leaveProduction() {
	f.t.Helper()
	_, err := f.pool.Exec(context.Background(), "UPDATE environments SET is_production = false WHERE id = $1", f.env.ID)
	require.NoError(f.t, err)
}

// guardedDatabase seeds a database with a destination.
func (f *fixture) guardedDatabase() (db.Service, db.BackupDestination) {
	f.t.Helper()
	sv := f.database("pg")
	return sv, mustDest(f.t, f, f.s3Destination().ID)
}

// guardOutcome is one prepared guarded call: the call itself, a check that
// nothing changed yet and a check that the call took effect.
type guardOutcome struct {
	call      func(ctx context.Context) error
	untouched func(t *testing.T)
	done      func(t *testing.T)
}

type guardCase struct {
	name         string
	op           string // the catalog operation id of the approval
	summary      string // a distinctive part of the plan's summary
	irreversible bool
	setup        func(f *fixture) guardOutcome
}

func guardCases() []guardCase {
	return []guardCase{
		{
			name: "DeleteBackup", op: "deleteBackup", summary: "Delete a database backup", irreversible: true,
			setup: func(f *fixture) guardOutcome {
				sv, dest := f.guardedDatabase()
				b := f.succeededBackup(sv, dest, nil, time.Hour, "k/prod")
				return guardOutcome{
					call: func(ctx context.Context) error { return f.svc.DeleteBackup(ctx, b.ID) },
					untouched: func(t *testing.T) {
						v, err := f.svc.GetBackup(f.ctx, b.ID)
						require.NoError(t, err)
						require.False(t, v.ObjectDeleted, "the dump is still stored")
						require.Zero(t, f.countCmds(agentprotocol.KindBackupDelete), "no deletion was enqueued")
					},
					done: func(t *testing.T) { require.Equal(t, 1, f.countCmds(agentprotocol.KindBackupDelete)) },
				}
			},
		},
		{
			name: "SetLocked(false)", op: "lockBackup", summary: "Unlock a database backup",
			setup: func(f *fixture) guardOutcome {
				sv, dest := f.guardedDatabase()
				b := f.succeededBackup(sv, dest, nil, time.Hour, "k/prod")
				_, err := f.svc.SetLocked(f.ctx, b.ID, true)
				require.NoError(f.t, err)
				locked := func(t *testing.T, want bool) {
					v, err := f.svc.GetBackup(f.ctx, b.ID)
					require.NoError(t, err)
					require.Equal(t, want, v.Locked)
				}
				return guardOutcome{
					call: func(ctx context.Context) error {
						_, err := f.svc.SetLocked(ctx, b.ID, false)
						return err
					},
					untouched: func(t *testing.T) { locked(t, true) },
					done:      func(t *testing.T) { locked(t, false) },
				}
			},
		},
		{
			name: "CreateSchedule", op: "createBackupSchedule", summary: "Add a backup schedule",
			setup: func(f *fixture) guardOutcome {
				sv, dest := f.guardedDatabase()
				count := func(t *testing.T, want int) {
					list, err := f.svc.ListSchedules(f.ctx, sv.ID)
					require.NoError(t, err)
					require.Len(t, list, want)
				}
				return guardOutcome{
					call: func(ctx context.Context) error {
						_, err := f.svc.CreateSchedule(ctx, sv.ID, ScheduleInput{DestinationID: dest.ID, Cron: "@daily"})
						return err
					},
					untouched: func(t *testing.T) { count(t, 0) },
					done:      func(t *testing.T) { count(t, 1) },
				}
			},
		},
		{
			name: "UpdateSchedule", op: "updateBackupSchedule", summary: "Change the backup schedule",
			setup: func(f *fixture) guardOutcome {
				sv, dest := f.guardedDatabase()
				sch, err := f.svc.CreateSchedule(f.ctx, sv.ID, ScheduleInput{DestinationID: dest.ID, Cron: "@daily"})
				require.NoError(f.t, err)
				retention := func(t *testing.T, want int) {
					list, err := f.svc.ListSchedules(f.ctx, sv.ID)
					require.NoError(t, err)
					require.Len(t, list, 1)
					require.Equal(t, want, list[0].RetentionCount)
				}
				return guardOutcome{
					call: func(ctx context.Context) error {
						_, err := f.svc.UpdateSchedule(ctx, sch.ID, SchedulePatch{RetentionCount: new(3)})
						return err
					},
					untouched: func(t *testing.T) { retention(t, sch.RetentionCount) },
					done:      func(t *testing.T) { retention(t, 3) },
				}
			},
		},
		{
			name: "DeleteSchedule", op: "deleteBackupSchedule", summary: "Delete the backup schedule", irreversible: true,
			setup: func(f *fixture) guardOutcome {
				sv, dest := f.guardedDatabase()
				sch, err := f.svc.CreateSchedule(f.ctx, sv.ID, ScheduleInput{DestinationID: dest.ID, Cron: "@daily"})
				require.NoError(f.t, err)
				count := func(t *testing.T, want int) {
					list, err := f.svc.ListSchedules(f.ctx, sv.ID)
					require.NoError(t, err)
					require.Len(t, list, want)
				}
				return guardOutcome{
					call:      func(ctx context.Context) error { return f.svc.DeleteSchedule(ctx, sch.ID) },
					untouched: func(t *testing.T) { count(t, 1) },
					done:      func(t *testing.T) { count(t, 0) },
				}
			},
		},
		{
			name: "PutVolumeBackupConfig", op: "putVolumeBackupConfig", summary: "Change the volume backup settings",
			setup: func(f *fixture) guardOutcome {
				dest := f.s3Destination()
				app := f.app("web", true)
				configured := func(t *testing.T, want bool) {
					v, err := f.svc.GetVolumeBackupConfig(f.ctx, app.ID)
					require.NoError(t, err)
					require.Equal(t, want, v.Configured)
				}
				return guardOutcome{
					call: func(ctx context.Context) error {
						_, err := f.svc.PutVolumeBackupConfig(ctx, app.ID, VolumeBackupConfigInput{DestinationID: dest.ID})
						return err
					},
					untouched: func(t *testing.T) { configured(t, false) },
					done:      func(t *testing.T) { configured(t, true) },
				}
			},
		},
		{
			name: "DeleteVolumeBackupConfig", op: "deleteVolumeBackupConfig", summary: "Delete the volume backup settings", irreversible: true,
			setup: func(f *fixture) guardOutcome {
				dest := f.s3Destination()
				app := f.app("web", true)
				f.volConfig(app, dest, nil)
				configured := func(t *testing.T, want bool) {
					v, err := f.svc.GetVolumeBackupConfig(f.ctx, app.ID)
					require.NoError(t, err)
					require.Equal(t, want, v.Configured)
				}
				return guardOutcome{
					call:      func(ctx context.Context) error { return f.svc.DeleteVolumeBackupConfig(ctx, app.ID) },
					untouched: func(t *testing.T) { configured(t, true) },
					done:      func(t *testing.T) { configured(t, false) },
				}
			},
		},
		{
			name: "DeleteVolumeBackup", op: "deleteVolumeBackup", summary: "Delete a volume backup", irreversible: true,
			setup: func(f *fixture) guardOutcome {
				_, b := f.succeededVolumeBackup()
				base := f.countCmds(agentprotocol.KindVolumeForget)
				return guardOutcome{
					call: func(ctx context.Context) error { return f.svc.DeleteVolumeBackup(ctx, b.ID) },
					untouched: func(t *testing.T) {
						v, err := f.svc.GetVolumeBackup(f.ctx, b.ID)
						require.NoError(t, err)
						require.False(t, v.ObjectDeleted, "the snapshot is still stored")
						require.Equal(t, base, f.countCmds(agentprotocol.KindVolumeForget), "no deletion was enqueued")
					},
					done: func(t *testing.T) { require.Equal(t, base+1, f.countCmds(agentprotocol.KindVolumeForget)) },
				}
			},
		},
		{
			name: "SetVolumeBackupLocked(false)", op: "lockVolumeBackup", summary: "Unlock a volume backup",
			setup: func(f *fixture) guardOutcome {
				_, b := f.succeededVolumeBackup()
				_, err := f.svc.SetVolumeBackupLocked(f.ctx, b.ID, true)
				require.NoError(f.t, err)
				locked := func(t *testing.T, want bool) {
					v, err := f.svc.GetVolumeBackup(f.ctx, b.ID)
					require.NoError(t, err)
					require.Equal(t, want, v.Locked)
				}
				return guardOutcome{
					call: func(ctx context.Context) error {
						_, err := f.svc.SetVolumeBackupLocked(ctx, b.ID, false)
						return err
					},
					untouched: func(t *testing.T) { locked(t, true) },
					done:      func(t *testing.T) { locked(t, false) },
				}
			},
		},
		{
			name: "DeleteRetiredVolume", op: "deleteRetiredVolume", summary: "Delete a retired volume", irreversible: true,
			setup: func(f *fixture) guardOutcome {
				app := f.app("web", true)
				require.NoError(f.t, f.q.InsertRetiredVolume(context.Background(), db.InsertRetiredVolumeParams{
					ServiceID: app.ID, VolumeName: "data", DockerName: "nc-vol-old-data", Reason: "test",
				}))
				retired, err := f.svc.ListRetiredVolumes(f.ctx, app.ID)
				require.NoError(f.t, err)
				require.Len(f.t, retired, 1)
				return guardOutcome{
					call: func(ctx context.Context) error { return f.svc.DeleteRetiredVolume(ctx, retired[0].ID) },
					untouched: func(t *testing.T) {
						require.Zero(t, f.countCmds(agentprotocol.KindVolumeRemove), "no removal was enqueued")
					},
					done: func(t *testing.T) {
						cmd, _ := f.lastCmd(agentprotocol.KindVolumeRemove)
						require.Contains(t, string(cmd.Payload), "nc-vol-old-data")
					},
				}
			},
		},
	}
}

// A REST token or an MCP client cannot make these changes in production
// without a person's approval; the approved call passes.
func TestProductionBackupChangesNeedApprovalForGuardedPrincipals(t *testing.T) {
	principals := []struct {
		name string
		ctx  func(f *fixture) context.Context
	}{
		{"REST token", (*fixture).restToken},
		{"MCP", (*fixture).mcpClient},
	}
	for _, gc := range guardCases() {
		t.Run(gc.name, func(t *testing.T) {
			for _, who := range principals {
				t.Run(who.name, func(t *testing.T) {
					f := newFixture(t) // the fixture environment is production
					g := f.installGuard()
					o := gc.setup(f)
					ctx := who.ctx(f)

					err := o.call(ctx)
					ar, ok := prodguard.AsRequired(err)
					require.True(t, ok, "got %v", err)
					require.Equal(t, gc.op, ar.Plan.Op)
					require.Contains(t, ar.Plan.Summary, gc.summary)
					require.Equal(t, gc.irreversible, ar.Plan.Irreversible)
					o.untouched(t)

					// Asking again before the approval changes nothing either.
					_, ok = prodguard.AsRequired(o.call(ctx))
					require.True(t, ok)
					o.untouched(t)

					f.approve(g, err)
					require.NoError(t, o.call(ctx))
					o.done(t)
				})
			}
		})
	}
}

// A person in the browser is never asked.
func TestProductionBackupChangesDoNotAskBrowserSessions(t *testing.T) {
	for _, gc := range guardCases() {
		t.Run(gc.name, func(t *testing.T) {
			f := newFixture(t)
			f.installGuard()
			o := gc.setup(f)
			require.NoError(t, o.call(f.ctx))
			o.done(t)
		})
	}
}

// Outside production nothing is guarded, not even for a REST token.
func TestBackupChangesOutsideProductionAreNotGuarded(t *testing.T) {
	for _, gc := range guardCases() {
		t.Run(gc.name, func(t *testing.T) {
			f := newFixture(t)
			f.installGuard()
			o := gc.setup(f)
			f.leaveProduction()
			require.NoError(t, o.call(f.restToken()))
			o.done(t)
		})
	}
}

// Locking a backup protects it, so it needs no approval; only unlocking does.
func TestLockingABackupIsNotGuarded(t *testing.T) {
	t.Run("database backup", func(t *testing.T) {
		f := newFixture(t)
		f.installGuard()
		sv, dest := f.guardedDatabase()
		b := f.succeededBackup(sv, dest, nil, time.Hour, "k/prod")
		v, err := f.svc.SetLocked(f.restToken(), b.ID, true)
		require.NoError(t, err)
		require.True(t, v.Locked)
	})
	t.Run("volume backup", func(t *testing.T) {
		f := newFixture(t)
		f.installGuard()
		_, vb := f.succeededVolumeBackup()
		vv, err := f.svc.SetVolumeBackupLocked(f.restToken(), vb.ID, true)
		require.NoError(t, err)
		require.True(t, vv.Locked)
	})
}

// An approval covers one call with exactly these arguments, once.
func TestBackupApprovalIsBoundToTheCallAndUsedOnce(t *testing.T) {
	f := newFixture(t)
	g := f.installGuard()
	rest := f.restToken()
	sv, dest := f.guardedDatabase()

	daily := ScheduleInput{DestinationID: dest.ID, Cron: "@daily"}
	_, firstErr := f.svc.CreateSchedule(rest, sv.ID, daily)
	_, ok := prodguard.AsRequired(firstErr)
	require.True(t, ok, "got %v", firstErr)
	f.approve(g, firstErr)

	// Other arguments are another call: the approval does not carry over.
	hourly := ScheduleInput{DestinationID: dest.ID, Cron: "@hourly"}
	_, otherErr := f.svc.CreateSchedule(rest, sv.ID, hourly)
	_, ok = prodguard.AsRequired(otherErr)
	require.True(t, ok, "got %v", otherErr)
	list, err := f.svc.ListSchedules(f.ctx, sv.ID)
	require.NoError(t, err)
	require.Empty(t, list)

	_, err = f.svc.CreateSchedule(rest, sv.ID, daily)
	require.NoError(t, err)

	// The approval was used up.
	_, againErr := f.svc.CreateSchedule(rest, sv.ID, daily)
	_, ok = prodguard.AsRequired(againErr)
	require.True(t, ok, "got %v", againErr)
	list, err = f.svc.ListSchedules(f.ctx, sv.ID)
	require.NoError(t, err)
	require.Len(t, list, 1)
}
