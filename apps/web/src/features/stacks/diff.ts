// A small line diff for compose files: the staged change `config.stack.compose`
// carries both full texts, and the review shows what changed line by line.

type DiffKind = "same" | "add" | "remove";

export interface DiffLine {
	kind: DiffKind;
	text: string;
	/** 1-based line number in the old text (not for added lines). */
	oldLine: number | null;
	/** 1-based line number in the new text (not for removed lines). */
	newLine: number | null;
}

/** The field the pending-changes API names the compose file by. */
export const COMPOSE_FIELD = "config.stack.compose";

/** Past this many cells the middle of a diff is shown as one replacement instead of an LCS. */
const MAX_CELLS = 4_000_000;

function lines(text: string): string[] {
	if (text === "") return [];
	const parts = text.replace(/\r\n/g, "\n").split("\n");
	if (parts[parts.length - 1] === "") parts.pop();
	return parts;
}

/**
 * The line diff from `from` to `to`: lines kept, removed and added in
 * reading order. The common head and tail are matched first; the middle uses
 * a longest-common-subsequence table (or, for a very large middle, one block
 * of removals then one of additions).
 */
export function diffLines(from: string, to: string): DiffLine[] {
	const a = lines(from);
	const b = lines(to);
	let head = 0;
	while (head < a.length && head < b.length && a[head] === b[head]) head += 1;
	let tail = 0;
	while (
		tail < a.length - head &&
		tail < b.length - head &&
		a[a.length - 1 - tail] === b[b.length - 1 - tail]
	) {
		tail += 1;
	}
	const midA = a.slice(head, a.length - tail);
	const midB = b.slice(head, b.length - tail);

	const out: DiffLine[] = [];
	let oldLine = 1;
	let newLine = 1;
	const push = (kind: DiffKind, text: string) => {
		out.push({
			kind,
			text,
			oldLine: kind === "add" ? null : oldLine,
			newLine: kind === "remove" ? null : newLine,
		});
		if (kind !== "add") oldLine += 1;
		if (kind !== "remove") newLine += 1;
	};

	for (const text of a.slice(0, head)) push("same", text);

	const n = midA.length;
	const m = midB.length;
	if (n * m > MAX_CELLS) {
		for (const text of midA) push("remove", text);
		for (const text of midB) push("add", text);
	} else {
		// lcs[i][j]: the common subsequence length of midA[i..] and midB[j..].
		const lcs: Uint32Array[] = Array.from(
			{ length: n + 1 },
			() => new Uint32Array(m + 1),
		);
		for (let i = n - 1; i >= 0; i--) {
			const row = lcs[i] as Uint32Array;
			const below = lcs[i + 1] as Uint32Array;
			for (let j = m - 1; j >= 0; j--) {
				row[j] =
					midA[i] === midB[j]
						? (below[j + 1] as number) + 1
						: Math.max(below[j] as number, row[j + 1] as number);
			}
		}
		let i = 0;
		let j = 0;
		while (i < n && j < m) {
			if (midA[i] === midB[j]) {
				push("same", midA[i] as string);
				i += 1;
				j += 1;
			} else if (
				((lcs[i + 1] as Uint32Array)[j] as number) >=
				((lcs[i] as Uint32Array)[j + 1] as number)
			) {
				push("remove", midA[i] as string);
				i += 1;
			} else {
				push("add", midB[j] as string);
				j += 1;
			}
		}
		for (; i < n; i++) push("remove", midA[i] as string);
		for (; j < m; j++) push("add", midB[j] as string);
	}

	for (const text of a.slice(a.length - tail)) push("same", text);
	return out;
}

export interface DiffStats {
	added: number;
	removed: number;
}

export function diffStats(diff: readonly DiffLine[]): DiffStats {
	let added = 0;
	let removed = 0;
	for (const line of diff) {
		if (line.kind === "add") added += 1;
		else if (line.kind === "remove") removed += 1;
	}
	return { added, removed };
}

/**
 * Keeps changed lines with `context` unchanged lines around them; the rest
 * collapses into a `gap` entry saying how many lines were left out.
 */
export type DiffHunkItem = DiffLine | { kind: "gap"; hidden: number };

export function collapseDiff(
	diff: readonly DiffLine[],
	context = 2,
): DiffHunkItem[] {
	const keep = new Array<boolean>(diff.length).fill(false);
	diff.forEach((line, index) => {
		if (line.kind === "same") return;
		for (
			let k = Math.max(0, index - context);
			k <= Math.min(diff.length - 1, index + context);
			k++
		) {
			keep[k] = true;
		}
	});
	const out: DiffHunkItem[] = [];
	let hidden = 0;
	diff.forEach((line, index) => {
		if (keep[index]) {
			if (hidden > 0) {
				out.push({ kind: "gap", hidden });
				hidden = 0;
			}
			out.push(line);
		} else {
			hidden += 1;
		}
	});
	if (hidden > 0) out.push({ kind: "gap", hidden });
	return out;
}

/** "compose file: +3 −1 lines", the one-line account of a staged compose change. */
export function composeChangeText(from: string, to: string): string {
	const { added, removed } = diffStats(diffLines(from, to));
	if (added === 0 && removed === 0) return "compose file unchanged";
	return `compose file: +${added} −${removed} ${added + removed === 1 ? "line" : "lines"}`;
}
