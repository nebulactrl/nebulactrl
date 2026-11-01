// The last ten queries a person ran, newest first, kept in localStorage.

const KEY = "nebulactrl.logs.recent-queries";
const MAX = 10;

/** Puts `query` first, drops an earlier copy and keeps the newest ten. Blank queries are ignored. */
export function pushRecent(list: readonly string[], query: string): string[] {
	const trimmed = query.trim();
	if (trimmed === "") return [...list];
	return [trimmed, ...list.filter((item) => item !== trimmed)].slice(0, MAX);
}

export function loadRecent(): string[] {
	try {
		const raw = localStorage.getItem(KEY);
		const value: unknown = raw ? JSON.parse(raw) : [];
		if (!Array.isArray(value)) return [];
		return value
			.filter((item): item is string => typeof item === "string")
			.slice(0, MAX);
	} catch {
		return [];
	}
}

export function saveRecent(list: readonly string[]) {
	try {
		localStorage.setItem(KEY, JSON.stringify(list));
	} catch {
		// Private mode or a full quota: recents are a convenience.
	}
}
