import type { ApprovalRequester, ApprovalView } from "#/api/gen/types.gen";

/** The label on the status chip and the sentence that says what it means for the waiting call. */
export function approvalStatus(a: Pick<ApprovalView, "status">): {
	label: string;
	detail: string;
} {
	switch (a.status) {
		case "pending":
			return {
				label: "Waiting for approval",
				detail:
					"Nothing has run. The call waits until you approve it, and stays refused if you decline.",
			};
		case "approved":
			return {
				label: "Approved",
				detail:
					"The agent can repeat the call now. It runs once, exactly as shown, and only while this request has not expired.",
			};
		case "declined":
			return {
				label: "Declined",
				detail: "The change will not run. The agent was told it was refused.",
			};
		case "used":
			return {
				label: "Approved and used",
				detail: "The approved call ran once. A new call needs a new approval.",
			};
		case "expired":
			return {
				label: "Expired",
				detail:
					"The request ran out of time. Ask the agent to try again to get a new one.",
			};
		case "stale":
			return {
				label: "Out of date",
				detail:
					"The service changed after this was requested, so the plan above no longer matches. The agent's next call opens a new request.",
			};
	}
}

/** "token ci-agent through Claude Code": who asked, in words. */
export function requesterText(r: ApprovalRequester): string {
	const who =
		r.kind === "mcp" ? `MCP token ${r.tokenName}` : `API token ${r.tokenName}`;
	return r.clientName ? `${who}, through ${r.clientName}` : who;
}
