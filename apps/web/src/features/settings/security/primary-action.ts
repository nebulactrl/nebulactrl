// Account → Security has one primary key at most (one primary per
// surface). Passkeys and two-step verification both start with a "set it up"
// action; only the first thing worth doing carries the accent.

export type SecurityPrimary = "passkey" | "totp" | "none";

/**
 * Which card owns the accent key. Once the account has a second factor nothing
 * on the page is urgent, so every action is secondary. With none, a passkey is
 * the better first step (it also signs in without a password), and the
 * authenticator app takes the key only where passkeys cannot be used.
 */
export function securityPrimary(input: {
	totpEnabled: boolean;
	passkeyCount: number;
	canAddPasskey: boolean;
}): SecurityPrimary {
	if (input.totpEnabled || input.passkeyCount > 0) return "none";
	return input.canAddPasskey ? "passkey" : "totp";
}
