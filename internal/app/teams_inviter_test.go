package app_test

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// The invite page shows who invited you, by display name only: the preview of
// a link is readable by whoever holds it, so the inviter's email stays out.
func TestInvitePreviewNamesTheInviterWithoutTheirEmail(t *testing.T) {
	h := newHarness(t)
	h.setup()
	team := h.teamID()

	tok := h.inviteLink(team, "developer", nil)
	prev := h.otherBrowser().do("GET", "/invites/"+tok, "", nil)
	require.Equal(t, 200, prev.Status, string(prev.Raw))
	require.Equal(t, "Admin", prev.JSON()["invitedBy"])
	require.NotContains(t, string(prev.Raw), "admin@example.com")
}
