/** One ranked line of an origins list. */
export interface OriginRow {
	key: string;
	label: string;
	requests: number;
	/** 0..1 of the located requests. */
	share: number;
	/** The map places this row stands for, lit on the map while the row is hovered or focused. */
	placeKeys: readonly string[];
}

/** Rows a list shows before its disclosure. */
export const LIST_ROWS = 8;

/** A row under this share (1%) is not worth a line of its own while the list is folded. */
export const OTHER_BELOW = 0.01;

export interface FoldedRows {
	rows: OriginRow[];
	/** The rows folded away, summed; null when nothing is folded. */
	other: (OriginRow & { count: number }) | null;
}

/**
 * The rows a list shows, busiest first. Expanded, all of them. Folded, the
 * first `top` rows of at least `OTHER_BELOW`; every other row is summed into
 * one "other" line, so the shares still add up (a single leftover row keeps
 * its own line: one row is not worth folding).
 */
export function foldRows(
	rows: readonly OriginRow[],
	otherLabel: string,
	expanded: boolean,
	top = LIST_ROWS,
): FoldedRows {
	if (expanded) return { rows: [...rows], other: null };
	let shown = rows.slice(0, top).filter((r) => r.share >= OTHER_BELOW);
	// Every row tiny (a long tail of a hundred countries): still name the busiest.
	if (shown.length === 0) shown = rows.slice(0, top);
	let rest = rows.filter((r) => !shown.includes(r));
	if (rest.length === 1) {
		shown.push(...rest);
		rest = [];
	}
	if (rest.length === 0) return { rows: shown, other: null };
	return {
		rows: shown,
		other: {
			key: "other",
			label: otherLabel,
			count: rest.length,
			requests: rest.reduce((n, r) => n + r.requests, 0),
			share: rest.reduce((n, r) => n + r.share, 0),
			placeKeys: rest.flatMap((r) => r.placeKeys),
		},
	};
}
