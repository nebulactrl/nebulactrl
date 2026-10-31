package auth

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/nebulactrl/nebulactrl/internal/db"
)

// BootstrapTeam creates the first team before the first user exists, so the
// installer's local server (which belongs to a team) can enroll while the
// dashboard is still waiting for its admin. Setup adopts the team: the first
// user becomes its owner (see adoptOrCreateFirstTeam). It returns the first
// team, created or not, and is serialized with Setup. Once any team exists it
// creates nothing.
func BootstrapTeam(ctx context.Context, pool *pgxpool.Pool, name string) (db.Workspace, error) {
	var ws db.Workspace
	err := db.InTx(ctx, pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, "SELECT pg_advisory_xact_lock($1)", setupLockID); err != nil {
			return err
		}
		q := db.New(tx)
		first, err := q.FirstWorkspace(ctx)
		if err == nil {
			ws = first
			return nil
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		ws, err = NewTeam(ctx, q, name, nil)
		return err
	})
	return ws, err
}

// adoptOrCreateFirstTeam returns the team the first user will own: the team
// BootstrapTeam made (the first team, with no members yet), renamed when the
// user chose another name; otherwise a new team.
func adoptOrCreateFirstTeam(ctx context.Context, q *db.Queries, name string, createdBy *uuid.UUID) (db.Workspace, error) {
	first, err := q.FirstWorkspace(ctx)
	if errors.Is(err, pgx.ErrNoRows) {
		return NewTeam(ctx, q, name, createdBy)
	}
	if err != nil {
		return db.Workspace{}, fmt.Errorf("first team: %w", err)
	}
	members, err := q.CountWorkspaceMemberships(ctx, first.ID)
	if err != nil {
		return db.Workspace{}, fmt.Errorf("count team members: %w", err)
	}
	if members > 0 {
		return NewTeam(ctx, q, name, createdBy)
	}
	name, err = NormalizeTeamName(name)
	if err != nil {
		return db.Workspace{}, err
	}
	if first.Name == name {
		return first, nil
	}
	return q.UpdateTeamName(ctx, db.UpdateTeamNameParams{ID: first.ID, Name: name})
}
