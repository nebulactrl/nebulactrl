import type { QueryClient } from "@tanstack/react-query";
import type { EnrollmentToken } from "#/api/gen/types.gen";

/**
 * Where the install command of a server that has not connected yet is kept
 * for this tab. The control plane stores only the token's hash, so a command
 * can be read once: when it is issued. Holding it in the query cache (memory
 * only, never persisted) lets the dialog be closed and the server page still
 * show the command until it expires; a reload loses it and "New install
 * command" issues another.
 */
export const enrollmentKey = (serverId: string) =>
	["enrollment-command", serverId] as const;

/** Tokens live for an hour; the cache entry is dropped with them. */
export const ENROLLMENT_CACHE_MS = 60 * 60 * 1000;

/** Remembers a freshly issued command for its server. */
export function cacheEnrollment(
	queryClient: QueryClient,
	enrollment: EnrollmentToken,
): void {
	queryClient.setQueryData(enrollmentKey(enrollment.server.id), enrollment);
}

/** Whether the token can no longer be redeemed. */
export function enrollmentExpired(
	enrollment: Pick<EnrollmentToken, "expiresAt">,
	now: number = Date.now(),
): boolean {
	return Date.parse(enrollment.expiresAt) <= now;
}

/**
 * The install one-liner split for reading, one flag per line:
 *
 *     curl -fsSL https://host/install-agent.sh
 *     | sudo sh -s --
 *     --url https://host
 *     --token nct_enroll_…
 *
 * Only the display is split; the copy key copies the original string. The
 * pieces joined with a space give the command back.
 */
export function commandLines(command: string): string[] {
	return command
		.split(/\s(?=\|)|\s(?=--[a-z])/)
		.map((part) => part.trim())
		.filter((part) => part !== "");
}
