// What to tell the person at each step of the password-reset flow.

/** What to do when reset by email is off: an admin can generate a link. */
export const ASK_ADMIN_NOTE = "Ask an admin to send you a reset link.";

/** The host command that prints a reset link for the only admin of an installation. */
export const RESET_COMMAND = "nebulactrl-server reset-password <email>";

/** Shown on the request page when no email can be sent. */
export const UNAVAILABLE_NOTE =
	"Password reset by email is not set up on this instance.";

/** The sentence for a failed "send reset link" request. */
export function forgotFailure(
	status: number | undefined,
	message: string,
): string {
	if (status === 429)
		return "Too many reset requests. Wait a while, then try again.";
	return message;
}

const MIN_PASSWORD = 10;

export { MIN_PASSWORD };

/** A reset link that is unknown, expired or already used. */
export const DEAD_LINK_TEXT =
	"This reset link is invalid, has expired or was already used. Ask for a new one.";

/** Why a reset-link submit failed, in a sentence. */
export function resetFailure(
	code: string | undefined,
	status: number | undefined,
	message: string,
): string {
	if (code === "invalid_reset_token") return DEAD_LINK_TEXT;
	if (code === "weak_password")
		return `Choose a password of at least ${MIN_PASSWORD} characters.`;
	if (status === 429)
		return "Too many attempts. Wait a minute, then try again.";
	return message;
}

/** The two passwords typed on the reset page, checked before sending. */
export function passwordProblem(
	password: string,
	confirm: string,
): string | undefined {
	if (password.length < MIN_PASSWORD)
		return `At least ${MIN_PASSWORD} characters.`;
	if (confirm !== "" && confirm !== password)
		return "The two passwords do not match.";
	return undefined;
}
