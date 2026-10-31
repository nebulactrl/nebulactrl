// Basic-auth users of a domain. The control plane only ever returns user
// names; a password is typed once (a new user, or a change) and never read back.

import type { AuthUserBody } from "#/api/gen/types.gen";
import { MAX_BASIC_USERS, validateUsername } from "./validate";

export interface AuthRow {
	username: string;
	/** Write-only: set for a new user or a password change, empty to keep the stored one. */
	password: string;
	/** The user is already stored on the control plane. */
	existing: boolean;
}

export const toAuthRows = (usernames: readonly string[]): AuthRow[] =>
	usernames.map((username) => ({ username, password: "", existing: true }));

/** Anything the PUT would change: users added or removed, or a password typed. */
export function authUsersChanged(
	saved: readonly string[],
	rows: readonly AuthRow[],
): boolean {
	if (rows.some((row) => row.password !== "" || !row.existing)) return true;
	if (saved.length !== rows.length) return true;
	const names = new Set(rows.map((row) => row.username));
	return saved.some((name) => !names.has(name));
}

/** The `PUT /auth-users` body: every user that should exist; a missing password keeps the old one. */
export function buildAuthBody(rows: readonly AuthRow[]): AuthUserBody[] {
	return rows.map((row) => ({
		username: row.username.trim(),
		...(row.password !== "" ? { password: row.password } : {}),
	}));
}

/** Errors keyed `users` (the list) or `users.<index>.username|password`. */
export function validateAuthRows(
	rows: readonly AuthRow[],
): Record<string, string> {
	const errors: Record<string, string> = {};
	if (rows.length > MAX_BASIC_USERS) {
		errors.users = `At most ${MAX_BASIC_USERS} users.`;
	}
	const seen: string[] = [];
	rows.forEach((row, index) => {
		const name = row.username.trim();
		const problem = validateUsername(name);
		if (problem) errors[`users.${index}.username`] = problem;
		else if (seen.includes(name)) {
			errors[`users.${index}.username`] = "Already listed.";
		}
		seen.push(name);
		if (!row.existing && row.password === "") {
			errors[`users.${index}.password`] = "Set a password for a new user.";
		}
	});
	return errors;
}
