package canvas

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/nebulactrl/nebulactrl/internal/apierr"
	"github.com/nebulactrl/nebulactrl/internal/db"
)

// Layout constants of AddGroupTx (canvas units).
const (
	groupColWidth  = 320
	groupRowHeight = 170
	groupPadX      = 40
	groupPadTop    = 70
	groupPadBottom = 40
	groupGap       = 80
)

// AddGroupTx adds a titled group holding serviceIDs to the environment's saved
// layout inside tx, placing the services on a small grid below everything
// already on the canvas. Group id and name are the caller's. The layout
// version advances; a concurrent layout save makes the transaction fail with
// layout_conflict (the caller retries or reports it).
func AddGroupTx(ctx context.Context, tx pgx.Tx, envID uuid.UUID, groupID, name string, serviceIDs []uuid.UUID) error {
	q := db.New(tx)
	layout := emptyLayout()
	var version int64
	row, err := q.GetCanvasLayout(ctx, envID)
	switch {
	case errors.Is(err, pgx.ErrNoRows):
	case err != nil:
		return fmt.Errorf("get canvas: %w", err)
	default:
		c, err := decode(row)
		if err != nil {
			return err
		}
		layout, version = c.Layout, c.Version
	}
	layout = normalize(layout)

	// Everything below the lowest existing node or group.
	bottom := 0.0
	for _, p := range layout.Nodes {
		bottom = math.Max(bottom, p.Y+groupRowHeight)
	}
	for _, g := range layout.Groups {
		bottom = math.Max(bottom, g.Y+g.H)
	}
	top := 0.0
	if bottom > 0 {
		top = bottom + groupGap
	}

	cols := 2
	if len(serviceIDs) > 4 {
		cols = 3
	}
	if len(serviceIDs) < cols {
		cols = max(len(serviceIDs), 1)
	}
	rows := (len(serviceIDs) + cols - 1) / cols
	ids := make([]string, len(serviceIDs))
	for i, id := range serviceIDs {
		ids[i] = id.String()
		layout.Nodes[ids[i]] = Pos{X: groupPadX + float64(i%cols)*groupColWidth, Y: top + groupPadTop + float64(i/cols)*groupRowHeight}
	}
	layout.Groups = append(layout.Groups, Group{
		ID: groupID, Name: name, X: 0, Y: top,
		W: float64(cols)*groupColWidth + groupPadX, H: float64(rows)*groupRowHeight + groupPadTop + groupPadBottom,
		ServiceIDs: ids,
	})
	if err := validate(layout); err != nil {
		return err
	}
	raw, err := json.Marshal(layout)
	if err != nil {
		return err
	}
	if version == 0 {
		if _, err := q.InsertCanvasLayout(ctx, db.InsertCanvasLayoutParams{EnvironmentID: envID, Layout: raw}); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return apierr.PreconditionFailed("layout_conflict", "the canvas layout changed; retry")
			}
			return fmt.Errorf("save canvas: %w", err)
		}
		return nil
	}
	if _, err := q.UpdateCanvasLayoutCAS(ctx, db.UpdateCanvasLayoutCASParams{EnvironmentID: envID, Layout: raw, ExpectedVersion: version}); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return apierr.PreconditionFailed("layout_conflict", "the canvas layout changed; retry")
		}
		return fmt.Errorf("save canvas: %w", err)
	}
	return nil
}

// RemoveGroupTx drops the group (and the saved positions of its services).
func RemoveGroupTx(ctx context.Context, tx pgx.Tx, envID uuid.UUID, groupID string) error {
	q := db.New(tx)
	row, err := q.GetCanvasLayout(ctx, envID)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("get canvas: %w", err)
	}
	c, err := decode(row)
	if err != nil {
		return err
	}
	l := normalize(c.Layout)
	kept := make([]Group, 0, len(l.Groups))
	for _, g := range l.Groups {
		if g.ID == groupID {
			for _, sid := range g.ServiceIDs {
				delete(l.Nodes, sid)
			}
			continue
		}
		kept = append(kept, g)
	}
	l.Groups = kept
	raw, err := json.Marshal(l)
	if err != nil {
		return err
	}
	if _, err := q.UpdateCanvasLayoutCAS(ctx, db.UpdateCanvasLayoutCASParams{EnvironmentID: envID, Layout: raw, ExpectedVersion: c.Version}); err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return fmt.Errorf("save canvas: %w", err)
	}
	return nil
}
