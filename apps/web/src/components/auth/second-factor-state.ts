// What the second step of sign-in says when a call fails, as pure functions.

export const EXPIRED_TEXT =
	"That sign-in expired. Go back and enter your password again.";

/** A failed second-factor call, reduced to what the screen needs. */
export interface SecondFactorFailure {
	text: string;
	/** The password step is over (expired or locked): only Back is left. */
	expired: boolean;
}

export function secondFactorFailure(
	status: number | undefined,
	code: string | undefined,
	message: string,
): SecondFactorFailure {
	if (code === "no_pending_login") return { text: EXPIRED_TEXT, expired: true };
	if (code === "two_factor_locked") {
		return {
			text: "Too many wrong codes. Wait a few minutes, then sign in again.",
			expired: true,
		};
	}
	if (status === 429) {
		return {
			text: "Too many attempts. Wait a minute, then try again.",
			expired: false,
		};
	}
	if (status === 401) {
		return {
			text: "That code is not valid. Check the code and try again.",
			expired: false,
		};
	}
	return { text: message, expired: false };
}

/**
 * Whether the answer to "how can this sign-in finish" says no password was
 * accepted: someone opened `/login?step=2fa` with nothing pending, or the
 * pending sign-in timed out.
 */
export function isNoPendingLogin(
	status: number | undefined,
	code: string | undefined,
): boolean {
	return code === "no_pending_login" || status === 401;
}
