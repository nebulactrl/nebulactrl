// Pure helpers of the sign-in flow: what to tell the person for each reason a
// sign-in ended, and how codes are shown and typed.

const LOGIN_ERRORS: Readonly<Record<string, string>> = {
	oidc_denied:
		"The identity provider did not grant access, so nobody was signed in.",
	invalid_oidc_state:
		"That sign-in attempt expired or did not start here. Start it again.",
	oidc_failed:
		"Single sign-on did not complete. Try again, or ask an administrator to check the provider's settings.",
	oidc_exchange_failed:
		"The identity provider rejected the sign-in. Try again, or ask an administrator to check the provider's client ID and secret.",
	oidc_invalid_token:
		"The identity provider sent a token NebulaCtrl could not verify. Ask an administrator to check the provider's settings.",
	email_not_verified:
		"Your identity provider has no verified email for this account, so it cannot be matched to a NebulaCtrl account.",
	domain_not_allowed:
		"Your email domain is not allowed to sign in with this provider.",
	signup_disabled:
		"No account exists for that email, and sign-up is by invitation only. Ask a team owner for an invite.",
	signup_unavailable: "Sign-up through this provider is not available.",
	invite_invalid:
		"That invite is invalid, has expired, or was sent to a different email.",
	identity_conflict:
		"Your email already has a different account at that provider linked to it. Sign in with the linked account.",
	account_exists:
		"An account with that email already exists. Sign in with your password, then link your single sign-on account in Account → Security.",
	rate_limited: "Too many attempts. Wait a minute, then try again.",
	login_failed: "Signing in did not complete. Try again.",
};

/** The sentence for a `?error=<code>` the OIDC callback redirected with. */
export function loginErrorMessage(
	code: string | undefined,
): string | undefined {
	if (!code) return undefined;
	// Own properties only: `?error=__proto__` must not return Object.prototype
	// (React cannot render it as a child and the sign-in page would crash).
	return Object.hasOwn(LOGIN_ERRORS, code)
		? LOGIN_ERRORS[code]
		: "Sign-in did not complete. Try again, or use your password.";
}

/** Authenticator codes are typed with or without spaces. */
export function normalizeCode(input: string): string {
	return input.replace(/\s+/g, "");
}

/** Six decimal digits: an authenticator code rather than a recovery code. */
export function isTotpShape(input: string): boolean {
	return /^\d{6}$/.test(normalizeCode(input));
}

/** Whether the field holds something submittable as a code or a recovery code. */
export function isPlausibleCode(input: string, recovery: boolean): boolean {
	const code = normalizeCode(input);
	if (recovery) return code.replace(/-/g, "").length === 16;
	return /^\d{6}$/.test(code);
}

/** `JBSWY3DPEHPK3PXP` -> `JBSW Y3DP EHPK 3PXP`, for reading off a screen. */
export function formatSecret(secret: string): string {
	return secret.replace(/\s+/g, "").replace(/(.{4})(?=.)/g, "$1 ");
}

/** The plain-text file offered for download next to the codes. */
export function recoveryCodesText(
	codes: readonly string[],
	account: string,
): string {
	return [
		"NebulaCtrl recovery codes",
		account ? `Account: ${account}` : "",
		"",
		"Each code works once. Keep them somewhere safe and private.",
		"",
		...codes,
		"",
	]
		.filter((line, index, all) => line !== "" || all[index - 1] !== "")
		.join("\n");
}

/** A warning level for how many recovery codes are left. */
export function recoveryCodesLevel(left: number): "ok" | "low" | "none" {
	if (left <= 0) return "none";
	if (left <= 3) return "low";
	return "ok";
}

/** The server route that redirects to the identity provider. A full page load, not an API call. */
export function providerStartPath(providerId: string, invite?: string): string {
	const base = `/api/v1/auth/oidc/${encodeURIComponent(providerId)}/start`;
	return invite ? `${base}?invite=${encodeURIComponent(invite)}` : base;
}

/**
 * What to tell the person when a passkey step failed. `null` means the person
 * closed the prompt themselves: nothing to report.
 */
export function passkeyFailureText(
	error: unknown,
	status: number | undefined,
	code: string | undefined,
	message: string,
	ceremonyKind: string | undefined,
): string | null {
	if (ceremonyKind === "cancelled") return null;
	if (ceremonyKind) return message;
	if (code === "passkey_failed") {
		return "That passkey was not accepted. Try another one, or use your password.";
	}
	if (code === "no_pending_passkey") {
		return "The passkey prompt expired. Start again.";
	}
	if (code === "no_pending_login") {
		return "That sign-in expired. Start again with your password.";
	}
	if (code === "two_factor_locked") {
		return "Too many wrong attempts. Start again with your password.";
	}
	if (code === "passkeys_unavailable") return message;
	if (status === 429)
		return "Too many attempts. Wait a minute, then try again.";
	return error instanceof Error ? message : "The passkey could not be used.";
}

const LINK_ERRORS: Readonly<Record<string, string>> = {
	oidc_denied:
		"The identity provider did not grant access, so nothing was linked.",
	invalid_oidc_state:
		"That attempt expired or did not start here. Start linking again.",
	email_not_verified:
		"Your identity provider has no verified email for this account, so it cannot be linked.",
	domain_not_allowed:
		"Your email domain is not allowed to sign in with this provider.",
	identity_conflict:
		"That account at the provider is already linked to another NebulaCtrl account.",
	oidc_failed:
		"Linking did not complete. Try again, or ask an administrator to check the provider's settings.",
};

/** What the link callback left in the address of Account → Security. */
export type SsoLinkOutcome =
	| { kind: "linked" }
	| { kind: "error"; message: string };

/**
 * Reads the result of a link started from Account → Security: the callback
 * redirects to `/account/security#sso=linked` or `#sso-error=<code>`.
 * `undefined` when the fragment says nothing about a link.
 */
export function ssoLinkOutcome(hash: string): SsoLinkOutcome | undefined {
	const params = new URLSearchParams(
		hash.startsWith("#") ? hash.slice(1) : hash,
	);
	if (params.get("sso") === "linked") return { kind: "linked" };
	const code = params.get("sso-error");
	if (code === null) return undefined;
	return {
		kind: "error",
		message: Object.hasOwn(LINK_ERRORS, code)
			? (LINK_ERRORS[code] as string)
			: "Linking did not complete. Try again.",
	};
}
