package mcp

import (
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/nebulactrl/nebulactrl/internal/mcp/mcpkit"
	"github.com/nebulactrl/nebulactrl/internal/prodguard"
)

// confirmationRequired is the result of a guarded call that no person has
// approved yet: the plan, and the page where a signed-in person approves it.
// Repeating exactly the same call afterwards finds the approval and runs it
// once.
//
// Approving happens only on that page. The client is never asked to approve
// (no elicitation form, no answer it could send back): whoever holds the token
// controls the client, so an answer from it would be the agent approving
// itself.
func confirmationRequired(e *prodguard.ErrApprovalRequired) *mcp.CallToolResult {
	te := mcpkit.Errf(mcpkit.CodeConfirmationRequired,
		"A person must approve this production change before it runs: %s. Ask the user to open %s signed in to the dashboard and approve it, then repeat exactly the same call. "+
			"Do not retry before they have, and never pass a confirm value yourself: it is ignored for production.",
		e.Plan.Summary, e.URL)
	te.Data = map[string]any{
		"approvalId":  e.ApprovalID.String(),
		"approvalUrl": e.URL,
		"expiresAt":   e.ExpiresAt.UTC().Format(time.RFC3339),
		"plan":        e.Plan,
	}
	return errorResult(te)
}
