package registries

import (
	"context"
	"net/http"
	"strings"
	"sync"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/stretchr/testify/require"

	"github.com/nebulactrl/nebulactrl/internal/db"
)

// sweepRegistry is a fake registry that verifies the delete token like the real one.
type sweepRegistry struct {
	t  *testing.T
	ms ManagedSecrets

	mu      sync.Mutex
	deletes []string
	status  map[string]int // by digest; default 202
}

func (r *sweepRegistry) round(req *http.Request) (*http.Response, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	tok, ok := strings.CutPrefix(req.Header.Get("Authorization"), "Bearer ")
	require.True(r.t, ok, "the sweep authenticates with a bearer token")
	_, acc, _ := verifyToken(r.t, tok, r.ms, BuiltinService)
	// /v2/<repo>/manifests/<digest>
	path := strings.TrimPrefix(req.URL.Path, "/v2/")
	i := strings.LastIndex(path, "/manifests/")
	require.Positive(r.t, i, path)
	repo, digest := path[:i], path[i+len("/manifests/"):]
	require.Equal(r.t, http.MethodDelete, req.Method)
	require.Equal(r.t, []tokenAccess{{Type: "repository", Name: repo, Actions: []string{"delete"}}}, acc, "a delete token for exactly this repository")
	r.deletes = append(r.deletes, repo+"@"+digest)
	if st, ok := r.status[digest]; ok {
		return response(st, nil), nil
	}
	return response(http.StatusAccepted, nil), nil
}

func (f *fixture) image(registryID uuid.UUID, repo, digest, state string) db.RegistryImage {
	f.t.Helper()
	var img db.RegistryImage
	err := f.pool.QueryRow(context.Background(),
		`INSERT INTO registry_images (registry_id, repository, digest, platform, build_hash, size_bytes, state)
		 VALUES ($1, $2, $3, 'linux/amd64', 'h', 1000, $4)
		 RETURNING id, registry_id, service_id, release_id, repository, digest, platform, build_hash, size_bytes, state, pushed_at`,
		registryID, repo, digest, state).Scan(&img.ID, &img.RegistryID, &img.ServiceID, &img.ReleaseID, &img.Repository, &img.Digest, &img.Platform, &img.BuildHash, &img.SizeBytes, &img.State, &img.PushedAt)
	require.NoError(f.t, err)
	return img
}

func (f *fixture) imageState(id uuid.UUID) string {
	f.t.Helper()
	var s string
	require.NoError(f.t, f.pool.QueryRow(context.Background(), `SELECT state FROM registry_images WHERE id = $1`, id).Scan(&s))
	return s
}

func digest(n int) string { return "sha256:" + strings.Repeat(string(rune('a'+n)), 64) }

func TestSweepDeletesOnlyExpiredDigestsAndNeverLiveOnes(t *testing.T) {
	f := newFixture(t)
	reg, ms := f.builtinRegistry("NebulaCtrl registry", "registry.example.test")
	fr := &sweepRegistry{t: t, ms: ms}
	f.httpFn = fr.round

	// The repository is recorded with its host (host/ns/repo); the sweep strips it.
	live1 := f.image(reg.ID, "registry.example.test/team/app", digest(0), ImageLive)
	live2 := f.image(reg.ID, "registry.example.test/team/app", digest(1), ImageLive)
	exp1 := f.image(reg.ID, "registry.example.test/team/app", digest(2), ImageExpired)
	exp2 := f.image(reg.ID, "registry.example.test/team/web", digest(3), ImageExpired)
	gone := f.image(reg.ID, "registry.example.test/team/web", digest(4), ImageDeleted)
	noHost := f.image(reg.ID, "team/api", digest(5), ImageExpired)

	res, err := f.svc.SweepRegistry(context.Background(), reg.ID)
	require.NoError(t, err)
	require.Equal(t, SweepResult{Deleted: 3}, res)

	require.ElementsMatch(t, []string{
		"team/app@" + digest(2), "team/web@" + digest(3), "team/api@" + digest(5),
	}, fr.deletes, "only expired digests, host stripped")
	for _, img := range []db.RegistryImage{live1, live2} {
		require.Equal(t, ImageLive, f.imageState(img.ID), "a live image is never touched")
	}
	for _, img := range []db.RegistryImage{exp1, exp2, noHost} {
		require.Equal(t, ImageDeleted, f.imageState(img.ID))
	}
	require.Equal(t, ImageDeleted, f.imageState(gone.ID))

	// Run again: nothing left to do, no request.
	fr.deletes = nil
	res, err = f.svc.SweepRegistry(context.Background(), reg.ID)
	require.NoError(t, err)
	require.Zero(t, res.Deleted)
	require.Empty(t, fr.deletes)

	// The state and an audit row record it; the live count and size are untouched.
	view, err := f.svc.Get(f.ctx, reg.ID)
	require.NoError(t, err)
	require.Equal(t, 2, view.ImageCount)
	require.EqualValues(t, 2000, view.SizeBytes)
	require.NotNil(t, view.Builtin.LastSweepAt)
	require.Zero(t, view.Builtin.LastSweepDeleted, "the last run deleted nothing")
	var n int
	require.NoError(t, f.pool.QueryRow(context.Background(), `SELECT count(*) FROM audit_events WHERE action = $1`, AuditSweep).Scan(&n))
	require.Equal(t, 1, n, "only the run that deleted something is audited")
}

func TestSweepNeverClaimsAnImageThatIsLiveAgain(t *testing.T) {
	f := newFixture(t)
	reg, _ := f.builtinRegistry("NebulaCtrl registry", "registry.example.test")
	img := f.image(reg.ID, "registry.example.test/team/app", digest(0), ImageExpired)

	// The deployment flow revives the digest (a new push of the same digest) between
	// the sweep's list and its claim: the claim must not match.
	_, err := f.pool.Exec(context.Background(), `UPDATE registry_images SET state = 'live' WHERE id = $1`, img.ID)
	require.NoError(t, err)
	_, err = f.q.ClaimExpiredRegistryImage(context.Background(), img.ID)
	require.ErrorIs(t, err, pgx.ErrNoRows)
	require.Equal(t, ImageLive, f.imageState(img.ID))

	// An expired one is claimed exactly once.
	_, err = f.pool.Exec(context.Background(), `UPDATE registry_images SET state = 'expired' WHERE id = $1`, img.ID)
	require.NoError(t, err)
	c, err := f.q.ClaimExpiredRegistryImage(context.Background(), img.ID)
	require.NoError(t, err)
	require.Equal(t, ImageDeleted, c.State)
	_, err = f.q.ClaimExpiredRegistryImage(context.Background(), img.ID)
	require.ErrorIs(t, err, pgx.ErrNoRows)
}

func TestSweepRetriesFailedDeletesAndTreatsMissingAsDeleted(t *testing.T) {
	f := newFixture(t)
	reg, ms := f.builtinRegistry("NebulaCtrl registry", "registry.example.test")
	fr := &sweepRegistry{t: t, ms: ms, status: map[string]int{digest(1): 500, digest(2): 404}}
	f.httpFn = fr.round
	ok := f.image(reg.ID, "registry.example.test/team/app", digest(0), ImageExpired)
	broken := f.image(reg.ID, "registry.example.test/team/app", digest(1), ImageExpired)
	missing := f.image(reg.ID, "registry.example.test/team/app", digest(2), ImageExpired)

	res, err := f.svc.SweepRegistry(context.Background(), reg.ID)
	require.Error(t, err)
	require.Equal(t, 2, res.Deleted)
	require.Equal(t, 1, res.Failed)
	require.Equal(t, ImageDeleted, f.imageState(ok.ID))
	require.Equal(t, ImageDeleted, f.imageState(missing.ID), "404: already gone")
	require.Equal(t, ImageExpired, f.imageState(broken.ID), "a failed delete is retried at the next run")

	fr.status = nil
	fr.deletes = nil
	res, err = f.svc.SweepRegistry(context.Background(), reg.ID)
	require.NoError(t, err)
	require.Equal(t, 1, res.Deleted)
	require.Equal(t, []string{"team/app@" + digest(1)}, fr.deletes)
	require.Equal(t, ImageDeleted, f.imageState(broken.ID))
}

func TestSweepStopsWhenTheRegistryRefusesDeletes(t *testing.T) {
	f := newFixture(t)
	reg, ms := f.builtinRegistry("NebulaCtrl registry", "registry.example.test")
	fr := &sweepRegistry{t: t, ms: ms, status: map[string]int{digest(0): 405, digest(1): 405}}
	f.httpFn = fr.round
	a := f.image(reg.ID, "registry.example.test/team/app", digest(0), ImageExpired)
	b := f.image(reg.ID, "registry.example.test/team/app", digest(1), ImageExpired)

	res, err := f.svc.SweepRegistry(context.Background(), reg.ID)
	require.ErrorContains(t, err, "storage deletes are not enabled")
	require.Zero(t, res.Deleted)
	require.Len(t, fr.deletes, 1, "no point asking again for every digest")
	require.Equal(t, ImageExpired, f.imageState(a.ID))
	require.Equal(t, ImageExpired, f.imageState(b.ID))
}

func TestSweepSkipsWhatItMust(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	reg, ms := f.builtinRegistry("NebulaCtrl registry", "registry.example.test")
	fr := &sweepRegistry{t: t, ms: ms}
	f.httpFn = fr.round
	img := f.image(reg.ID, "registry.example.test/team/app", digest(0), ImageExpired)
	untouched := func(why string) {
		t.Helper()
		require.Empty(t, fr.deletes, why)
		require.Equal(t, ImageExpired, f.imageState(img.ID), why)
	}

	// Retention switched off.
	_, err := f.pool.Exec(ctx, `UPDATE registries SET manage_retention = false WHERE id = $1`, reg.ID)
	require.NoError(t, err)
	res, err := f.svc.SweepRegistry(ctx, reg.ID)
	require.NoError(t, err)
	require.NotEmpty(t, res.Skipped)
	untouched("retention is not managed")
	n, err := f.svc.Sweep(ctx)
	require.NoError(t, err)
	require.Zero(t, n, "the periodic sweep only visits registries that manage retention")
	_, err = f.pool.Exec(ctx, `UPDATE registries SET manage_retention = true WHERE id = $1`, reg.ID)
	require.NoError(t, err)

	// The garbage-collection window (read-only): deletes would fail and the window must not be disturbed.
	require.NoError(t, f.svc.PatchManagedState(ctx, map[string]any{"phase": "drain", "readOnly": true}))
	res, err = f.svc.SweepRegistry(ctx, reg.ID)
	require.NoError(t, err)
	require.Contains(t, res.Skipped, "garbage-collection window")
	untouched("read-only")
	require.NoError(t, f.svc.PatchManagedState(ctx, map[string]any{"phase": nil, "readOnly": nil}))

	// The registry's service is not ready.
	f.setBuiltinStatus("failed")
	res, err = f.svc.SweepRegistry(ctx, reg.ID)
	require.NoError(t, err)
	require.Contains(t, res.Skipped, "failed")
	untouched("not ready")

	// An external registry is never swept.
	ext := f.createExternal("ghcr")
	_, err = f.svc.SweepRegistry(ctx, ext.ID)
	require.NoError(t, err)
	untouched("external")
}

func TestSweepSelectionOnlyListsBuiltinRetentionRegistries(t *testing.T) {
	f := newFixture(t)
	builtin, _ := f.builtinRegistry("builtin", "127.0.0.1:18500")
	f.createExternal("external")
	list, err := f.q.ListBuiltinRetentionRegistries(context.Background())
	require.NoError(t, err)
	require.Len(t, list, 1)
	require.Equal(t, builtin.ID, list[0].ID)
}

func TestBuiltinStatePatchKeepsOtherOptionKeys(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	f.builtinRegistry("builtin", "127.0.0.1:18500")
	_, err := f.pool.Exec(ctx, `UPDATE instance_services SET options = '{"unrelated":"x"}' WHERE kind = 'registry'`)
	require.NoError(t, err)

	require.NoError(t, f.svc.PatchManagedState(ctx, map[string]any{"phase": "collect", "readOnly": true, "lastGcMessage": "a"}))
	require.NoError(t, f.svc.PatchManagedState(ctx, map[string]any{"phase": nil, "lastGcMessage": "b"}))
	st, err := f.svc.ManagedState(ctx)
	require.NoError(t, err)
	require.Empty(t, st.Phase, "a null clears a field")
	require.True(t, st.ReadOnly, "fields not in the patch stay")
	require.Equal(t, "b", st.LastGCMessage)
	var opts string
	require.NoError(t, f.pool.QueryRow(ctx, `SELECT options::text FROM instance_services WHERE kind = 'registry'`).Scan(&opts))
	require.Contains(t, opts, `"unrelated"`)
}
