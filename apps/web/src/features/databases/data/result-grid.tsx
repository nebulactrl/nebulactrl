import { observeElementRect, useVirtualizer } from "@tanstack/react-virtual";
import { useRef } from "react";
import {
	TableBody,
	TableCell,
	TableHead,
	TableHeader,
	TableRow,
} from "#/components/ui/table";
import { type ResultSet, rowsLabel } from "./model";

const ROW_HEIGHT = 32;
const COLUMN_MIN_WIDTH = 160;
/** What the grid assumes before it has measured itself (the first paint, and a DOM without layout). */
const INITIAL_VIEWPORT = { width: 800, height: 360 };

/** A cell: NULL is its own muted word, so it never reads as the text "NULL". */
function Cell({ value }: { value: string | null }) {
	if (value === null) {
		return (
			<span data-null className="text-ink-4 italic">
				NULL
			</span>
		);
	}
	return <span title={value}>{value}</span>;
}

/**
 * The rows of one result set as a table that renders only the rows in view,
 * so a thousand-row result scrolls as lightly as ten. The header stays at the
 * top of the scroll area.
 */
export function ResultGrid({
	set,
	label,
}: {
	set: ResultSet;
	/** Names the grid for assistive tech: "Result of the query", "Rows of users". */
	label: string;
}) {
	const scrollRef = useRef<HTMLDivElement>(null);
	const virtualizer = useVirtualizer({
		count: set.rows.length,
		getScrollElement: () => scrollRef.current,
		estimateSize: () => ROW_HEIGHT,
		overscan: 12,
		initialRect: INITIAL_VIEWPORT,
		// An element with no layout (a DOM without one, or a hidden pane) measures 0 high: keep the assumed viewport then.
		observeElementRect: (instance, cb) =>
			observeElementRect(instance, (rect) =>
				cb(rect.height === 0 ? INITIAL_VIEWPORT : rect),
			),
	});
	const items = virtualizer.getVirtualItems();
	const first = items[0];
	const last = items[items.length - 1];
	const padTop = first?.start ?? 0;
	const padBottom = last ? virtualizer.getTotalSize() - last.end : 0;
	const span = set.columns.length;

	return (
		<section
			ref={scrollRef}
			aria-label={label}
			// biome-ignore lint/a11y/noNoninteractiveTabindex: a scroll area the keyboard must be able to scroll
			tabIndex={0}
			className="max-h-[360px] overflow-auto rounded-card border border-line bg-surface-well/40 focus-visible:focus-ring"
		>
			<table
				data-slot="table"
				aria-rowcount={set.rows.length + 1}
				className="w-full table-fixed caption-bottom border-collapse text-sm"
				style={{ minWidth: span * COLUMN_MIN_WIDTH }}
			>
				<TableHeader className="sticky top-0 z-10 bg-surface-float">
					<TableRow>
						{set.columns.map((column, index) => (
							<TableHead
								// Columns can repeat a name ("count", "?column?"): the position is the identity.
								// biome-ignore lint/suspicious/noArrayIndexKey: see above
								key={index}
								className="h-8 truncate"
								title={`${column.name} (${column.type})`}
							>
								<span className="text-ink-1">{column.name}</span>{" "}
								<span className="font-normal text-ink-4">{column.type}</span>
							</TableHead>
						))}
					</TableRow>
				</TableHeader>
				<TableBody>
					{set.rows.length === 0 ? (
						<TableRow>
							<TableCell colSpan={span} className="h-10 text-ink-3">
								No rows
							</TableCell>
						</TableRow>
					) : null}
					{padTop > 0 ? (
						<tr aria-hidden style={{ height: padTop }}>
							<td colSpan={span} />
						</tr>
					) : null}
					{items.map((item) => {
						const row = set.rows[item.index] ?? [];
						return (
							<TableRow
								key={item.key}
								data-index={item.index}
								aria-rowindex={item.index + 2}
								style={{ height: ROW_HEIGHT }}
							>
								{set.columns.map((_, col) => (
									<TableCell
										// biome-ignore lint/suspicious/noArrayIndexKey: columns can repeat a name
										key={col}
										className="h-8 truncate py-1 font-mono text-xs"
									>
										<Cell value={row[col] ?? null} />
									</TableCell>
								))}
							</TableRow>
						);
					})}
					{padBottom > 0 ? (
						<tr aria-hidden style={{ height: padBottom }}>
							<td colSpan={span} />
						</tr>
					) : null}
				</TableBody>
			</table>
		</section>
	);
}

/** A statement that returns no rows: its completion tag ("INSERT 0 1"), or "OK". */
export function CommandResult({ set }: { set: ResultSet }) {
	return (
		<p
			role="status"
			className="rounded-card border border-line bg-surface-well/40 px-3 py-2 font-mono text-ink-2 text-xs"
		>
			{set.commandTag === "" ? "OK" : set.commandTag}
		</p>
	);
}

/** "Showing the first 1,000 rows": a set that the control plane cut. */
export function truncationNote(set: ResultSet): string {
	return `Showing the first ${rowsLabel(set.rowCount)}`;
}
