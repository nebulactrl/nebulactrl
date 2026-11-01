package deployments

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/nebulactrl/nebulactrl/internal/netguard"
)

// A tenant sets the git URL of a service, and the plan shows what happened:
// the control plane must not reach its own network through it.
func TestLsRemoteRefusesInternalAddresses(t *testing.T) {
	t.Setenv(netguard.AllowEnv, "")
	hits := 0
	internal := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits++
		_, _ = w.Write([]byte("INTERNAL-ONLY-BODY"))
	}))
	defer internal.Close()

	_, err := LsRemote(context.Background(), internal.URL+"/x.git", "main", "", "")
	require.Error(t, err)
	require.Contains(t, err.Error(), "refused")
	require.NotContains(t, err.Error(), "INTERNAL-ONLY-BODY")
	require.Zero(t, hits, "the loopback server was never contacted")

	// go-git dials git and SSH remotes itself, so the guard checks them first.
	for _, u := range []string{"git://127.0.0.1/x.git", "git://169.254.169.254/x.git", "ssh://git@10.0.0.5/x.git", "git@192.168.1.10:acme/x.git"} {
		require.ErrorIs(t, guardRemote(context.Background(), u), netguard.ErrBlocked, u)
	}
	_, err = LsRemote(context.Background(), "git://127.0.0.1:9/x.git", "main", "", "")
	require.ErrorContains(t, err, "refused")
}

// go-git copies the whole body of an error answer into its error; the plan and
// build-plan messages must carry the status only.
func TestLsRemoteErrorsDoNotEchoTheRemoteAnswer(t *testing.T) {
	t.Setenv(netguard.AllowEnv, "127.0.0.1")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte("INTERNAL-ONLY-BODY"))
	}))
	defer srv.Close()
	_, err := LsRemote(context.Background(), srv.URL+"/x.git", "main", "", "")
	require.Error(t, err)
	require.NotContains(t, err.Error(), "INTERNAL-ONLY-BODY")
	require.Contains(t, err.Error(), "500")

	notFound := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte("INTERNAL-ONLY-BODY"))
	}))
	defer notFound.Close()
	_, err = LsRemote(context.Background(), notFound.URL+"/x.git", "main", "", "")
	require.Error(t, err)
	require.NotContains(t, err.Error(), "INTERNAL-ONLY-BODY")
	require.Contains(t, err.Error(), "repository not found")
}

func TestLsRemoteNeverSendsATokenOverPlainHTTPAndHidesURLCredentials(t *testing.T) {
	t.Setenv(netguard.AllowEnv, "127.0.0.1")
	seen := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = true
		w.WriteHeader(http.StatusForbidden)
	}))
	defer srv.Close()
	_, err := LsRemote(context.Background(), srv.URL+"/x.git", "main", "", "tok-secret-123456")
	require.ErrorContains(t, err, "plain http")
	require.NotContains(t, err.Error(), "tok-secret-123456")
	require.False(t, seen, "the request was not even made")

	// Legacy URLs with credentials never reach an error message.
	_, err = LsRemote(context.Background(), "https://user:pa55word@127.0.0.1:1/x.git", "main", "", "")
	require.Error(t, err)
	require.NotContains(t, err.Error(), "pa55word")
}
