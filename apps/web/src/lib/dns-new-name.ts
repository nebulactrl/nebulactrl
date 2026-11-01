/** How long a new name may take to reach every DNS resolver. */
const NEW_NAME_WINDOW_MS = 30 * 60 * 1000;

/** The heads-up for a name that just changed: a resolver may still remember it as missing. */
export const NEW_NAME_NOTE =
	"New names can take up to 30 minutes to reach every DNS resolver. If it doesn't open on your device yet, your network's DNS may still remember it as missing; it clears by itself.";

/**
 * Whether a hostname is still new: `changedAt` (when it was set, or its record
 * created or changed) is less than 30 minutes before `now`. No timestamp, an
 * unreadable one, or one in the future beyond a minute of clock skew is not new.
 */
export function isNewName(
	changedAt: string | null | undefined,
	now: number,
): boolean {
	if (!changedAt) return false;
	const at = Date.parse(changedAt);
	if (Number.isNaN(at)) return false;
	const age = now - at;
	return age >= -60_000 && age < NEW_NAME_WINDOW_MS;
}
