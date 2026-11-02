package canvas_test

import (
	"context"
	"fmt"
	"net/http"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/stretchr/testify/require"

	"github.com/nebulactrl/nebulactrl/internal/canvas"
)

// inTx runs fn in a transaction that commits when fn returns nil.
func (r *rig) inTx(fn func(tx pgx.Tx) error) error {
	r.t.Helper()
	ctx := context.Background()
	tx, err := r.pool.Begin(ctx)
	require.NoError(r.t, err)
	if err := fn(tx); err != nil {
		_ = tx.Rollback(ctx)
		return err
	}
	require.NoError(r.t, tx.Commit(ctx))
	return nil
}

func (r *rig) saved(envID uuid.UUID) canvas.Canvas {
	r.t.Helper()
	c, err := r.svc.Get(r.owner, r.project.ID, envID)
	require.NoError(r.t, err)
	return c
}

func ids(n int) []uuid.UUID {
	out := make([]uuid.UUID, n)
	for i := range out {
		out[i] = uuid.New()
	}
	return out
}

func TestAddGroupPlacesServicesOnAGridBelowEverythingElse(t *testing.T) {
	r := newRig(t)
	ctx := context.Background()

	// On an environment without a saved layout the group starts at the top.
	first := ids(3)
	require.NoError(t, r.inTx(func(tx pgx.Tx) error { return canvas.AddGroupTx(ctx, tx, r.env.ID, "g1", "Backend", first) }))
	c := r.saved(r.env.ID)
	require.Equal(t, int64(1), c.Version)
	require.Equal(t, []canvas.Group{{
		ID: "g1", Name: "Backend", X: 0, Y: 0, W: 680, H: 450, ServiceIDs: []string{first[0].String(), first[1].String(), first[2].String()},
	}}, c.Layout.Groups)
	require.Equal(t, map[string]canvas.Pos{
		first[0].String(): {X: 40, Y: 70},
		first[1].String(): {X: 360, Y: 70},
		first[2].String(): {X: 40, Y: 240},
	}, c.Layout.Nodes)

	// The next group goes 80 below the lowest group or node (the first group ends at y=450).
	second := ids(1)
	require.NoError(t, r.inTx(func(tx pgx.Tx) error { return canvas.AddGroupTx(ctx, tx, r.env.ID, "g2", "Cache", second) }))
	c = r.saved(r.env.ID)
	require.Equal(t, int64(2), c.Version)
	require.Len(t, c.Layout.Groups, 2)
	require.Equal(t, canvas.Group{ID: "g2", Name: "Cache", X: 0, Y: 530, W: 360, H: 280, ServiceIDs: []string{second[0].String()}}, c.Layout.Groups[1])
	require.Equal(t, canvas.Pos{X: 40, Y: 600}, c.Layout.Nodes[second[0].String()])
	require.Len(t, c.Layout.Nodes, 4, "earlier positions are kept")

	// More than four services use three columns.
	third := ids(5)
	require.NoError(t, r.inTx(func(tx pgx.Tx) error { return canvas.AddGroupTx(ctx, tx, r.env.ID, "g3", "Workers", third) }))
	c = r.saved(r.env.ID)
	g3 := c.Layout.Groups[2]
	require.Equal(t, 3.0*320+40, g3.W)
	require.Equal(t, 2.0*170+70+40, g3.H)
	require.Equal(t, 890.0, g3.Y, "80 below the cache group, which ends at 530+280=810")
}

func TestAddGroupBelowNodesThatAreNotInAnyGroup(t *testing.T) {
	r := newRig(t)
	ctx := context.Background()
	lone := uuid.NewString()
	_, err := r.svc.Put(r.owner, r.project.ID, r.env.ID, 0, canvas.Layout{Nodes: map[string]canvas.Pos{lone: {X: 5, Y: 300}}})
	require.NoError(t, err)

	svc := ids(2)
	require.NoError(t, r.inTx(func(tx pgx.Tx) error { return canvas.AddGroupTx(ctx, tx, r.env.ID, "g", "G", svc) }))
	c := r.saved(r.env.ID)
	require.Equal(t, 550.0, c.Layout.Groups[0].Y, "300 + a 170 row + an 80 gap")
	require.Equal(t, canvas.Pos{X: 5, Y: 300}, c.Layout.Nodes[lone])
}

func TestAddGroupRefusesToGrowPastTheGroupLimit(t *testing.T) {
	r := newRig(t)
	groups := make([]canvas.Group, 200)
	for i := range groups {
		groups[i] = canvas.Group{ID: fmt.Sprint("g", i)}
	}
	_, err := r.svc.Put(r.owner, r.project.ID, r.env.ID, 0, canvas.Layout{Groups: groups})
	require.NoError(t, err)

	err = r.inTx(func(tx pgx.Tx) error {
		return canvas.AddGroupTx(context.Background(), tx, r.env.ID, "one-too-many", "X", ids(1))
	})
	requireAPIError(t, err, http.StatusUnprocessableEntity, "layout_too_large")
	require.Len(t, r.saved(r.env.ID).Layout.Groups, 200)
}

func TestAddGroupWithoutServicesStillMakesAGroup(t *testing.T) {
	r := newRig(t)
	require.NoError(t, r.inTx(func(tx pgx.Tx) error {
		return canvas.AddGroupTx(context.Background(), tx, r.env.ID, "empty", "Empty", nil)
	}))
	g := r.saved(r.env.ID).Layout.Groups[0]
	require.Equal(t, []string{}, g.ServiceIDs)
	require.Equal(t, 320.0+40, g.W)
	require.Equal(t, 110.0, g.H)
}

func TestRemoveGroupDropsItsServicesPositionsOnly(t *testing.T) {
	r := newRig(t)
	ctx := context.Background()
	a, b, c := uuid.NewString(), uuid.NewString(), uuid.NewString()
	_, err := r.svc.Put(r.owner, r.project.ID, r.env.ID, 0, canvas.Layout{
		Nodes: map[string]canvas.Pos{a: {X: 1}, b: {X: 2}, c: {X: 3}},
		Groups: []canvas.Group{
			{ID: "front", Name: "Front", ServiceIDs: []string{a, b}},
			{ID: "back", Name: "Back", ServiceIDs: []string{c}},
		},
	})
	require.NoError(t, err)

	require.NoError(t, r.inTx(func(tx pgx.Tx) error { return canvas.RemoveGroupTx(ctx, tx, r.env.ID, "front") }))
	got := r.saved(r.env.ID)
	require.Equal(t, int64(2), got.Version)
	require.Equal(t, map[string]canvas.Pos{c: {X: 3}}, got.Layout.Nodes)
	require.Len(t, got.Layout.Groups, 1)
	require.Equal(t, "back", got.Layout.Groups[0].ID)

	// An unknown group changes nothing about the content.
	require.NoError(t, r.inTx(func(tx pgx.Tx) error { return canvas.RemoveGroupTx(ctx, tx, r.env.ID, "nope") }))
	got = r.saved(r.env.ID)
	require.Equal(t, map[string]canvas.Pos{c: {X: 3}}, got.Layout.Nodes)
	require.Len(t, got.Layout.Groups, 1)

	// Nothing saved, nothing to do - and nothing gets created.
	other := r.newEnv("staging")
	require.NoError(t, r.inTx(func(tx pgx.Tx) error { return canvas.RemoveGroupTx(ctx, tx, other.ID, "front") }))
	require.Zero(t, r.saved(other.ID).Version)
}

func TestDropServiceRemovesItsPositionAndMembershipAndEmptiedGroups(t *testing.T) {
	r := newRig(t)
	ctx := context.Background()
	a, b, c := uuid.New(), uuid.New(), uuid.New()
	_, err := r.svc.Put(r.owner, r.project.ID, r.env.ID, 0, canvas.Layout{
		Nodes: map[string]canvas.Pos{a.String(): {X: 1}, b.String(): {X: 2}, c.String(): {X: 3}},
		Groups: []canvas.Group{
			{ID: "pair", Name: "Pair", ServiceIDs: []string{a.String(), b.String()}},
			{ID: "solo", Name: "Solo", ServiceIDs: []string{c.String()}},
			{ID: "blank", Name: "Blank"},
		},
	})
	require.NoError(t, err)

	require.NoError(t, r.inTx(func(tx pgx.Tx) error { return canvas.DropServiceTx(ctx, tx, r.env.ID, a) }))
	got := r.saved(r.env.ID)
	require.Equal(t, int64(2), got.Version)
	require.NotContains(t, got.Layout.Nodes, a.String())
	require.Len(t, got.Layout.Nodes, 2)
	require.Equal(t, []string{b.String()}, got.Layout.Groups[0].ServiceIDs)

	// The last member leaves: the group goes with it. A group that was
	// already empty is somebody's deliberate frame and stays.
	require.NoError(t, r.inTx(func(tx pgx.Tx) error { return canvas.DropServiceTx(ctx, tx, r.env.ID, c) }))
	got = r.saved(r.env.ID)
	var groupIDs []string
	for _, g := range got.Layout.Groups {
		groupIDs = append(groupIDs, g.ID)
	}
	require.Equal(t, []string{"pair", "blank"}, groupIDs)

	// A service the layout never heard of is harmless.
	require.NoError(t, r.inTx(func(tx pgx.Tx) error { return canvas.DropServiceTx(ctx, tx, r.env.ID, uuid.New()) }))
	require.Len(t, r.saved(r.env.ID).Layout.Nodes, 1)

	// No layout saved: nothing happens.
	other := r.newEnv("staging")
	require.NoError(t, r.inTx(func(tx pgx.Tx) error { return canvas.DropServiceTx(ctx, tx, other.ID, a) }))
	require.Zero(t, r.saved(other.ID).Version)
}

func TestGroupHelpersFailOnAStoredLayoutThatCannotBeRead(t *testing.T) {
	r := newRig(t)
	ctx := context.Background()
	r.rawLayout(r.env.ID, `[1, 2]`)

	err := r.inTx(func(tx pgx.Tx) error { return canvas.AddGroupTx(ctx, tx, r.env.ID, "g", "G", ids(1)) })
	require.ErrorContains(t, err, "decode layout")
	err = r.inTx(func(tx pgx.Tx) error { return canvas.RemoveGroupTx(ctx, tx, r.env.ID, "g") })
	require.ErrorContains(t, err, "decode layout")
	err = r.inTx(func(tx pgx.Tx) error { return canvas.DropServiceTx(ctx, tx, r.env.ID, uuid.New()) })
	require.ErrorContains(t, err, "decode layout")
}

func TestCloneLayoutTranslatesIDsAndDropsWhatIsNotCloned(t *testing.T) {
	r := newRig(t)
	ctx := context.Background()
	target := r.newEnv("staging")
	a, b, c := uuid.New(), uuid.New(), uuid.New()
	na, nb := uuid.New(), uuid.New()
	_, err := r.svc.Put(r.owner, r.project.ID, r.env.ID, 0, canvas.Layout{
		Nodes: map[string]canvas.Pos{a.String(): {X: 1, Y: 2}, b.String(): {X: 3, Y: 4}, c.String(): {X: 5, Y: 6}},
		Groups: []canvas.Group{
			{ID: "kept", Name: "Kept", X: 10, Y: 20, W: 30, H: 40, Collapsed: true, ServiceIDs: []string{a.String(), c.String()}},
			{ID: "gone", Name: "Gone", ServiceIDs: []string{c.String()}},
			{ID: "blank", Name: "Blank"},
		},
		Viewport: canvas.Viewport{X: 7, Y: 8, Zoom: 2},
	})
	require.NoError(t, err)
	// A key that is not a service id is ignored rather than failing the clone.
	_, err = r.pool.Exec(ctx, `UPDATE canvas_layouts SET layout = jsonb_set(layout, '{nodes,web}', '{"x":9,"y":9}') WHERE environment_id = $1`, r.env.ID)
	require.NoError(t, err)

	require.NoError(t, canvas.CloneLayout(ctx, r.q, r.env.ID, target.ID, map[uuid.UUID]uuid.UUID{a: na, b: nb}))
	got := r.saved(target.ID)
	require.Equal(t, int64(1), got.Version)
	require.Equal(t, map[string]canvas.Pos{na.String(): {X: 1, Y: 2}, nb.String(): {X: 3, Y: 4}}, got.Layout.Nodes)
	require.Equal(t, []canvas.Group{{ID: "kept", Name: "Kept", X: 10, Y: 20, W: 30, H: 40, Collapsed: true, ServiceIDs: []string{na.String()}}}, got.Layout.Groups,
		"a group without cloned services disappears, and so does an already empty one")
	require.Equal(t, canvas.Viewport{X: 7, Y: 8, Zoom: 2}, got.Layout.Viewport)

	// The source is untouched.
	require.Len(t, r.saved(r.env.ID).Layout.Nodes, 4)
}

func TestCloneLayoutDoesNotOverwriteAnExistingTargetLayout(t *testing.T) {
	r := newRig(t)
	ctx := context.Background()
	target := r.newEnv("staging")
	a, na := uuid.New(), uuid.New()
	_, err := r.svc.Put(r.owner, r.project.ID, r.env.ID, 0, canvas.Layout{Nodes: map[string]canvas.Pos{a.String(): {X: 1}}})
	require.NoError(t, err)
	keep := uuid.NewString()
	_, err = r.svc.Put(r.owner, r.project.ID, target.ID, 0, canvas.Layout{Nodes: map[string]canvas.Pos{keep: {X: 42}}})
	require.NoError(t, err)

	require.NoError(t, canvas.CloneLayout(ctx, r.q, r.env.ID, target.ID, map[uuid.UUID]uuid.UUID{a: na}))
	require.Equal(t, map[string]canvas.Pos{keep: {X: 42}}, r.saved(target.ID).Layout.Nodes)
}

func TestCloneLayoutCopiesNothingWhenThereIsNothingUsable(t *testing.T) {
	r := newRig(t)
	ctx := context.Background()
	target := r.newEnv("staging")
	m := map[uuid.UUID]uuid.UUID{uuid.New(): uuid.New()}

	require.NoError(t, canvas.CloneLayout(ctx, r.q, r.env.ID, target.ID, m), "no saved layout")
	require.Zero(t, r.saved(target.ID).Version)

	r.rawLayout(r.env.ID, `"garbage"`)
	require.NoError(t, canvas.CloneLayout(ctx, r.q, r.env.ID, target.ID, m), "an unreadable layout is not an error")
	require.Zero(t, r.saved(target.ID).Version, "the clone gets the automatic layout")
}
