// The last ten queries a person ran in a team, newest first, kept in localStorage.
// A query can name a customer or a token, so the list belongs to one user in
// one team and is not shown to the next person on the same browser.

/** The one global key older builds wrote; it belonged to nobody. */
const LEGACY_KEY = "nebulactrl.logs.recent-queries";
const STORAGE_PREFIX = "nebulactrl.logs.recent-queries:";
const MAX = 10;

function storageKey(teamId: string, userId: string): string {
	return `${STORAGE_PREFIX}${teamId}:${userId}`;
}

/** Puts `query` first, drops an earlier copy and keeps the newest ten. Blank queries are ignored. */
export function pushRecent(list: readonly string[], query: string): string[] {
	const trimmed = query.trim();
	if (trimmed === "") return [...list];
	return [trimmed, ...list.filter((item) => item !== trimmed)].slice(0, MAX);
}

/** What `userId` ran in `teamId`. The old global list is dropped, not carried over. */
export function loadRecent(teamId: string, userId: string): string[] {
	try {
		localStorage.removeItem(LEGACY_KEY);
		const raw = localStorage.getItem(storageKey(teamId, userId));
		const value: unknown = raw ? JSON.parse(raw) : [];
		if (!Array.isArray(value)) return [];
		return value
			.filter((item): item is string => typeof item === "string")
			.slice(0, MAX);
	} catch {
		return [];
	}
}

export function saveRecent(
	teamId: string,
	userId: string,
	list: readonly string[],
) {
	try {
		localStorage.setItem(storageKey(teamId, userId), JSON.stringify(list));
	} catch {
		// Private mode or a full quota: recents are a convenience.
	}
}
