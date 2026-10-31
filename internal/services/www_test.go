package services

import (
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/nebulactrl/nebulactrl/internal/db"
	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

func TestWWWPartner(t *testing.T) {
	for host, want := range map[string]string{
		"example.com":        "www.example.com",
		"www.example.com":    "example.com",
		"Example.COM":        "www.example.com",
		"app.example.co.uk":  "www.app.example.co.uk",
		"www.app.example.io": "app.example.io",
	} {
		got, ok := WWWPartner(host)
		require.True(t, ok, host)
		require.Equal(t, want, got, host)
	}
	for _, host := range []string{"", "*.example.com", "www.localhost", "www.com"} {
		_, ok := WWWPartner(host)
		require.False(t, ok, host)
	}
}

func TestWWWCompanionDetection(t *testing.T) {
	svc := uuid.New()
	to := func(s string) *string { return &s }
	serve := db.Domain{ID: uuid.New(), ServiceID: svc, Hostname: "example.com", Kind: agentprotocol.DomainServe}
	redirect := db.Domain{ID: uuid.New(), ServiceID: svc, Hostname: "www.example.com", Kind: agentprotocol.DomainRedirect, RedirectTo: to("example.com")}
	require.True(t, IsWWWCompanion(serve, redirect))

	other := redirect
	other.RedirectTo = to("elsewhere.com")
	require.False(t, IsWWWCompanion(serve, other), "points somewhere else")
	prefixed := redirect
	prefixed.PathPrefix = "/blog"
	require.False(t, IsWWWCompanion(serve, prefixed), "not a whole-host redirect")
	wrongHost := redirect
	wrongHost.Hostname = "blog.example.com"
	require.False(t, IsWWWCompanion(serve, wrongHost))
	servePrefixed := serve
	servePrefixed.PathPrefix = "/api"
	require.False(t, IsWWWCompanion(servePrefixed, redirect))

	got, ok := FindWWWCompanion([]db.Domain{serve, redirect}, serve)
	require.True(t, ok)
	require.Equal(t, redirect.ID, got.ID)
	foreign := redirect
	foreign.ServiceID = uuid.New()
	_, ok = FindWWWCompanion([]db.Domain{serve, foreign}, serve)
	require.False(t, ok, "a redirect on another service is not this domain's")

	views := []DomainView{{}, {}}
	annotateWWW(views, []db.Domain{serve, redirect}, []db.Domain{serve, redirect})
	require.Equal(t, "www.example.com", views[0].WWWPartner)
	require.True(t, views[0].WWWRedirect)
	require.Equal(t, "example.com", views[1].WWWRedirectFor)
	require.False(t, views[1].WWWRedirect)
}
