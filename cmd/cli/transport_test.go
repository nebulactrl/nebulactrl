package main

import (
	"io"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// The API token is a bearer credential: it must not cross the network in the
// clear unless the person said so.
func TestCheckTransport(t *testing.T) {
	for _, u := range []string{
		"https://nebula.example.com",
		"https://203.0.113.9:8443",
		"http://localhost:8080",
		"http://127.0.0.1:8080",
		"http://127.1.2.3",
		"http://[::1]:8080",
		"http://dev.localhost",
	} {
		require.NoError(t, checkTransport(u, false), u)
	}
	for _, u := range []string{
		"http://nebula.example.com",
		"http://203.0.113.9:8080",
		"http://10.0.0.5",
		"http://localhost.evil.example",
	} {
		err := checkTransport(u, false)
		require.Error(t, err, u)
		require.Contains(t, err.Error(), "--insecure", u)
		require.NoError(t, checkTransport(u, true), "--insecure opts in: "+u)
	}
	for _, u := range []string{"ftp://nebula.example.com", "ws://nebula.example.com", "nebula.example.com"} {
		require.Error(t, checkTransport(u, true), u)
	}
}

func TestDialRefusesPlainHTTPToARemoteHost(t *testing.T) {
	c := &cli{in: strings.NewReader(""), out: io.Discard, errOut: io.Discard}
	err := c.dial(config{URL: "http://nebula.example.com", Token: "nct_x"})
	require.Error(t, err)
	require.Nil(t, c.api, "no client was built, so no request can carry the token")

	c.insecure = true
	require.NoError(t, c.dial(config{URL: "http://nebula.example.com", Token: "nct_x"}))
	require.NotNil(t, c.api)
}

// A token is guarded on production: the CLI says where a person approves.
func TestProblemNamesTheApprovalPage(t *testing.T) {
	e := problem(409, []byte(`{"status":409,"code":"approval_required","detail":"A human must approve this production change first: Deploy api","details":{"approvalId":"a1","approvalUrl":"https://nebula.example.com/approvals/a1"}}`))
	require.Equal(t, "approval_required", e.Code)
	require.Contains(t, e.Error(), "https://nebula.example.com/approvals/a1")
	require.Contains(t, e.Error(), "same command again")
}
