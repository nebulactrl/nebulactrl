// What the reset page decides before and after the new password is sent.

import { count } from "#/lib/format";

export type ResetLinkState = "checking" | "ok" | "dead";

/**
 * Whether the link is worth showing the form for. Only the server saying the
 * link is invalid makes it dead: a network failure or a rate limit leaves the
 * form up, because the link may well work on submit.
 */
export function resetLinkState(check: {
	pending: boolean;
	failed: boolean;
	code: string | undefined;
}): ResetLinkState {
	if (check.pending) return "checking";
	return check.failed && check.code === "invalid_reset_token" ? "dead" : "ok";
}

/** What a reset did to the account's API tokens, in sentences. */
export function tokenOutcome(result: {
	revokedTokens: number;
	keptTokens: readonly unknown[];
}): { revoked: string | null; kept: string | null } {
	const revoked =
		result.revokedTokens > 0
			? `${count(result.revokedTokens, "API token")} revoked.`
			: null;
	const kept =
		result.keptTokens.length > 0
			? `${count(result.keptTokens.length, "API token")} still ${
					result.keptTokens.length === 1 ? "works" : "work"
				}. If you did not expect that, sign in and revoke ${
					result.keptTokens.length === 1 ? "it" : "them"
				} in Account settings → API tokens.`
			: null;
	return { revoked, kept };
}
