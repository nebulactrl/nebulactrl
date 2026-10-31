package canvas

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/nebulactrl/nebulactrl/internal/db"
)

// CloneLayout copies the canvas layout of environment fromEnvID to toEnvID,
// translating service ids through newID (old id to new id). Positions and groups
// of services without an entry in newID are dropped, and a group left without
// services disappears. An environment without a saved layout, or with an
// unreadable one, copies nothing (the new environment gets the automatic layout).
func CloneLayout(ctx context.Context, q *db.Queries, fromEnvID, toEnvID uuid.UUID, newID map[uuid.UUID]uuid.UUID) error {
	row, err := q.GetCanvasLayout(ctx, fromEnvID)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("get canvas layout: %w", err)
	}
	var in Layout
	if err := json.Unmarshal(row.Layout, &in); err != nil {
		return nil // unreadable layouts are not worth failing a clone for
	}
	out := Layout{Nodes: map[string]Pos{}, Groups: []Group{}, Viewport: in.Viewport}
	for k, p := range in.Nodes {
		if id, err := uuid.Parse(k); err == nil {
			if n, ok := newID[id]; ok {
				out.Nodes[n.String()] = p
			}
		}
	}
	for _, g := range in.Groups {
		ids := []string{}
		for _, sid := range g.ServiceIDs {
			if id, err := uuid.Parse(sid); err == nil {
				if n, ok := newID[id]; ok {
					ids = append(ids, n.String())
				}
			}
		}
		if len(ids) > 0 {
			g.ServiceIDs = ids
			out.Groups = append(out.Groups, g)
		}
	}
	raw, err := json.Marshal(out)
	if err != nil {
		return err
	}
	if _, err := q.InsertCanvasLayout(ctx, db.InsertCanvasLayoutParams{EnvironmentID: toEnvID, Layout: raw}); err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return fmt.Errorf("insert canvas layout: %w", err)
	}
	return nil
}

// DropServiceTx removes a service's saved position and group membership from
// the environment's layout inside tx (a group left empty is removed). It does
// nothing when the environment has no saved layout. The layout version
// advances.
func DropServiceTx(ctx context.Context, tx pgx.Tx, envID, serviceID uuid.UUID) error {
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
	sid := serviceID.String()
	delete(l.Nodes, sid)
	kept := make([]Group, 0, len(l.Groups))
	for _, g := range l.Groups {
		ids := make([]string, 0, len(g.ServiceIDs))
		for _, id := range g.ServiceIDs {
			if id != sid {
				ids = append(ids, id)
			}
		}
		if len(ids) == 0 && len(g.ServiceIDs) > 0 {
			continue
		}
		g.ServiceIDs = ids
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
