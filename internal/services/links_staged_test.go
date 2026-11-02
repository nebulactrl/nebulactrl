package services_test

import (
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/nebulactrl/nebulactrl/internal/services"
)

func TestMarkStagedLinksFlagsReferencesTheRunningReleaseLacks(t *testing.T) {
	api, worker, cron, db, cache := uuid.New(), uuid.New(), uuid.New(), uuid.New(), uuid.New()
	slugOf := map[uuid.UUID]string{api: "api", worker: "worker", cron: "cron", db: "db", cache: "cache"}
	links := []services.LinkView{
		// api holds DB_URL in its release but DB_HOST is new: the link stays live (not every reference is staged).
		{ID: "1", FromServiceID: api, ToServiceID: db, References: []services.LinkReferenceKey{{Key: "DB_URL"}, {Key: "DB_HOST"}}},
		// worker has nothing staged at all (clean): its references are all applied.
		{ID: "2", FromServiceID: worker, ToServiceID: db, References: []services.LinkReferenceKey{{Key: "DB_URL"}}},
		// cron was never deployed: nothing in the release, so everything is staged.
		{ID: "3", FromServiceID: cron, ToServiceID: db, References: []services.LinkReferenceKey{{Key: "DB_URL"}}},
		// A hand-drawn link without references is never staged.
		{ID: "4", FromServiceID: api, ToServiceID: cache},
		// A release holding the key but pointing at another service does not count.
		{ID: "5", FromServiceID: api, ToServiceID: cache, References: []services.LinkReferenceKey{{Key: "URL"}}},
	}
	clean := map[uuid.UUID]bool{worker: true}
	release := map[uuid.UUID]map[string]services.RawVar{
		api: {
			"DB_URL": {Value: "${{db.DATABASE_URL}}"},
			"URL":    {Value: "${{db.DATABASE_URL}}"},
		},
	}

	services.MarkStagedLinks(links, slugOf, clean, release)

	require.False(t, links[0].References[0].Staged, "DB_URL is already in the release")
	require.True(t, links[0].References[1].Staged, "DB_HOST is new")
	require.False(t, links[0].Staged, "one live reference keeps the edge solid")
	require.False(t, links[1].References[0].Staged)
	require.False(t, links[1].Staged)
	require.True(t, links[2].References[0].Staged)
	require.True(t, links[2].Staged)
	require.False(t, links[3].Staged)
	require.True(t, links[4].References[0].Staged, "the release's URL points at db, not cache")
	require.True(t, links[4].Staged)
}
