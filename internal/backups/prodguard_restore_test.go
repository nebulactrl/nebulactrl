package backups

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/nebulactrl/nebulactrl/internal/prodguard"
)

// Restoring a backup or a dump file into production, or over it, is a change a
// REST token or an MCP client may only make with a person's approval. The
// approved call passes once; a browser session is never asked.
func TestProductionRestoresNeedApprovalForGuardedPrincipals(t *testing.T) {
	type guarded struct {
		name, op, summary string
		irreversible      bool
		setup             func(f *fixture) func(ctx context.Context) error
		restores          func(f *fixture) int
	}
	restoreRows := func(f *fixture) int { return f.count("SELECT count(*) FROM backup_restores") }
	dumpRows := func(f *fixture) int { return f.count("SELECT count(*) FROM dump_restores") }
	cases := []guarded{
		{
			name: "restore into a new database", op: "restore_backup", summary: "Restore a backup of pg into a new database",
			setup: func(f *fixture) func(ctx context.Context) error {
				sv, dest := f.guardedDatabase()
				b := f.succeededBackup(sv, dest, nil, time.Hour, "k/prod.dump")
				return func(ctx context.Context) error {
					_, err := f.svc.Restore(ctx, b.ID, RestoreInput{Name: "pg-copy"})
					return err
				}
			},
			restores: restoreRows,
		},
		{
			name: "restore over the database", op: "restore_backup", summary: "overwriting its data", irreversible: true,
			setup: func(f *fixture) func(ctx context.Context) error {
				sv, dest := f.guardedDatabase()
				b := f.succeededBackup(sv, dest, nil, time.Hour, "k/prod.dump")
				return func(ctx context.Context) error {
					_, err := f.svc.Restore(ctx, b.ID, RestoreInput{Mode: ModeInPlace, ConfirmSlug: sv.Slug})
					return err
				}
			},
			restores: restoreRows,
		},
		{
			name: "dump restore into a new database", op: "restore_dump", summary: "uploaded dump file into a new database",
			setup: func(f *fixture) func(ctx context.Context) error {
				sv, _ := f.guardedDatabase()
				return func(ctx context.Context) error {
					_, err := f.svc.CreateDumpRestore(ctx, sv.ID, DumpRestoreInput{Name: "pg-copy"})
					return err
				}
			},
			restores: dumpRows,
		},
		{
			name: "dump restore over the database", op: "restore_dump", summary: "overwriting its data", irreversible: true,
			setup: func(f *fixture) func(ctx context.Context) error {
				sv, dest := f.guardedDatabase()
				return func(ctx context.Context) error {
					_, err := f.svc.CreateDumpRestore(ctx, sv.ID, DumpRestoreInput{Mode: ModeInPlace, ConfirmSlug: sv.Slug, DestinationID: &dest.ID})
					return err
				}
			},
			restores: dumpRows,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			for name, who := range map[string]func(f *fixture) context.Context{"REST token": (*fixture).restToken, "MCP": (*fixture).mcpClient} {
				t.Run(name, func(t *testing.T) {
					f := newFixture(t) // the fixture environment is production
					g := f.installGuard()
					call := tc.setup(f)
					ctx := who(f)

					err := call(ctx)
					ar, ok := prodguard.AsRequired(err)
					require.True(t, ok, "got %v", err)
					require.Equal(t, tc.op, ar.Plan.Op)
					require.Contains(t, ar.Plan.Summary, tc.summary)
					require.Equal(t, tc.irreversible, ar.Plan.Irreversible)
					require.Zero(t, tc.restores(f), "nothing was created before the approval")

					f.approve(g, err)
					require.NoError(t, call(ctx))
					require.Equal(t, 1, tc.restores(f))
				})
			}
			t.Run("browser session", func(t *testing.T) {
				f := newFixture(t)
				f.installGuard()
				require.NoError(t, tc.setup(f)(f.ctx))
				require.Equal(t, 1, tc.restores(f))
			})
			t.Run("outside production", func(t *testing.T) {
				f := newFixture(t)
				f.installGuard()
				call := tc.setup(f)
				f.leaveProduction()
				require.NoError(t, call(f.restToken()))
				require.Equal(t, 1, tc.restores(f))
			})
		})
	}
}
