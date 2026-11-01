package registries

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestListImagesPagesNewestFirstAndFiltersByState(t *testing.T) {
	f := newFixture(t)
	reg, _ := f.builtinRegistry("NebulaCtrl registry", "registry.example.test")
	_, ows := f.otherTeam()
	other, _ := f.builtinRegistryIn(ows, "Other", "other.example.test")
	base := time.Now().Add(-time.Hour)
	var ids []string
	for i := range 7 {
		state := ImageLive
		switch i {
		case 1, 4:
			state = ImageExpired
		case 2:
			state = ImageDeleted
		}
		img := f.image(reg.ID, "registry.example.test/team/app", digest(i), state)
		_, err := f.pool.Exec(context.Background(), `UPDATE registry_images SET pushed_at = $2 WHERE id = $1`, img.ID, base.Add(time.Duration(i)*time.Minute))
		require.NoError(t, err)
		ids = append(ids, img.ID.String())
	}
	f.image(other.ID, "other.example.test/x/y", digest(9), ImageLive) // another registry: never listed here

	page, err := f.svc.ListImages(f.ctx, reg.ID, "", "", 3)
	require.NoError(t, err)
	require.Len(t, page.Items, 3)
	require.NotEmpty(t, page.NextCursor)
	require.Equal(t, ids[6], page.Items[0].ID.String(), "newest first")
	require.Equal(t, "registry.example.test/team/app@"+digest(6), page.Items[0].Reference)
	require.Equal(t, "registry.example.test/team/app", page.Items[0].Repository)
	require.Equal(t, "linux/amd64", page.Items[0].Platform)
	require.EqualValues(t, 1000, *page.Items[0].SizeBytes)

	var got []string
	for _, it := range page.Items {
		got = append(got, it.ID.String())
	}
	cursor := page.NextCursor
	for cursor != "" {
		page, err = f.svc.ListImages(f.ctx, reg.ID, "", cursor, 3)
		require.NoError(t, err)
		for _, it := range page.Items {
			got = append(got, it.ID.String())
		}
		cursor = page.NextCursor
	}
	require.Equal(t, []string{ids[6], ids[5], ids[4], ids[3], ids[2], ids[1], ids[0]}, got, "every image once, in order")

	live, err := f.svc.ListImages(f.ctx, reg.ID, ImageLive, "", 0)
	require.NoError(t, err)
	require.Len(t, live.Items, 4)
	for _, it := range live.Items {
		require.Equal(t, ImageLive, it.State)
	}
	expired, err := f.svc.ListImages(f.ctx, reg.ID, ImageExpired, "", 0)
	require.NoError(t, err)
	require.Len(t, expired.Items, 2)
	require.Empty(t, expired.NextCursor)

	_, err = f.svc.ListImages(f.ctx, reg.ID, "weird", "", 0)
	require.Equal(t, "validation_failed", code(err))
	_, err = f.svc.ListImages(f.ctx, reg.ID, "", "not-a-cursor", 0)
	require.Equal(t, "invalid_cursor", code(err))

	// The limit is capped.
	for i := range 3 {
		f.image(reg.ID, "registry.example.test/team/bulk", fmt.Sprintf("sha256:%064d", i), ImageLive)
	}
	all, err := f.svc.ListImages(f.ctx, reg.ID, "", "", 100000)
	require.NoError(t, err)
	require.LessOrEqual(t, len(all.Items), MaxImageLimit)
	require.Len(t, all.Items, 10)

	// The view counts live images and their size only.
	v, err := f.svc.Get(f.ctx, reg.ID)
	require.NoError(t, err)
	require.Equal(t, 7, v.ImageCount)
	require.EqualValues(t, 7000, v.SizeBytes)
}

func TestImageReferenceDoesNotDuplicateTheHost(t *testing.T) {
	require.Equal(t, "ghcr.io/acme/app@sha256:1", imageReference("ghcr.io", "acme/app", "sha256:1"))
	require.Equal(t, "ghcr.io/acme/app@sha256:1", imageReference("ghcr.io", "ghcr.io/acme/app", "sha256:1"))
}
