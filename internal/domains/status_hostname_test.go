package domains

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/nebulactrl/nebulactrl/internal/apierr"
	"github.com/nebulactrl/nebulactrl/internal/db"
	"github.com/nebulactrl/nebulactrl/internal/services"
	"github.com/nebulactrl/nebulactrl/internal/testutil"
)

// A hostname a status page serves is reserved: a service domain cannot take it
// (the status page side is tested in internal/statuspages).
func TestServiceDomainCannotTakeAStatusPageHostname(t *testing.T) {
	pool := testutil.NewPool(t)
	q := db.New(pool)
	bg := context.Background()
	ws, err := q.CreateWorkspace(bg, "team")
	require.NoError(t, err)
	host := "status.example.com"
	_, err = q.InsertStatusPage(bg, db.InsertStatusPageParams{WorkspaceID: ws.ID, Title: "Status", Slug: "status", Hostname: &host, ShowUptime: true, Theme: "auto"})
	require.NoError(t, err)
	// A page without a hostname reserves nothing.
	_, err = q.InsertStatusPage(bg, db.InsertStatusPageParams{WorkspaceID: ws.ID, Title: "Draft", Slug: "draft", ShowUptime: true, Theme: "auto"})
	require.NoError(t, err)

	s := &Service{pool: pool, q: q}
	other, err := q.CreateWorkspace(bg, "other team")
	require.NoError(t, err)
	for _, h := range []string{"status.example.com", "STATUS.Example.com"} {
		// The team that owns the page is told which page holds the name.
		err := s.validate(bg, q, services.Scope{WorkspaceID: ws.ID}, nil, defaultSpec(h), uuid.Nil)
		var ae *apierr.Error
		require.ErrorAs(t, err, &ae, h)
		require.Equal(t, 409, ae.Status)
		require.Equal(t, "domain_conflict", ae.Code)
		require.Equal(t, "the hostname "+h+" is used by the status page Status", ae.Message)

		// Another team learns only that the name is not available.
		err = s.validate(bg, q, services.Scope{WorkspaceID: other.ID}, nil, defaultSpec(h), uuid.Nil)
		require.ErrorAs(t, err, &ae, h)
		require.Equal(t, 409, ae.Status)
		require.Equal(t, "domain_conflict", ae.Code)
		require.Equal(t, "the hostname "+h+" is not available", ae.Message)
	}

	require.NoError(t, s.validate(bg, q, services.Scope{}, nil, defaultSpec("app.example.com"), uuid.Nil), "other hostnames are free")
	require.NoError(t, s.validate(bg, q, services.Scope{}, nil, defaultSpec("status.example.org"), uuid.Nil))
}
