/**
 * What the live tail's footer says about the lines the search covers:
 * "Searching the last 1,200 lines. Older lines are not kept." With a single
 * line there is no "last N", and with none nothing is searched yet.
 */
export function searchScopeNote(total: number): string {
	if (total === 0) return "Nothing to search yet. Older lines are not kept.";
	if (total === 1) return "Searching the only line. Older lines are not kept.";
	return `Searching the last ${total.toLocaleString("en-US")} lines. Older lines are not kept.`;
}
