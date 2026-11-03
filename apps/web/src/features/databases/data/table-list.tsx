import { cn } from "cn";
import { SearchIcon, SquareTerminalIcon, TableIcon } from "lucide-react";
import { useState } from "react";
import { Input } from "#/components/ui/input";
import { estimateLabel, filterTables, type TableRef, tableKey } from "./model";

/** What the right pane shows: the SQL console, or one table's rows. */
export type DataSelection =
	| { kind: "query" }
	| { kind: "table"; table: TableRef };

function isSelected(selection: DataSelection, table: TableRef): boolean {
	return (
		selection.kind === "table" && tableKey(selection.table) === tableKey(table)
	);
}

const ROW =
	"flex w-full min-w-0 cursor-pointer items-center gap-2 rounded-ctl px-2 py-1.5 text-left text-[13px] text-ink-2 outline-none transition-colors hover:bg-white/4 focus-visible:focus-ring aria-pressed:bg-accent-wash aria-pressed:text-ink-1";

/**
 * The left column: the SQL console, then the database's tables and views with
 * a filter. The pressed row is what the right pane shows.
 */
export function TableList({
	tables,
	selection,
	onSelect,
}: {
	tables: readonly TableRef[];
	selection: DataSelection;
	onSelect(selection: DataSelection): void;
}) {
	const [filter, setFilter] = useState("");
	const shown = filterTables(tables, filter);
	return (
		<nav aria-label="Tables" className="flex min-h-0 min-w-0 flex-col gap-2">
			<button
				type="button"
				aria-pressed={selection.kind === "query"}
				className={ROW}
				onClick={() => onSelect({ kind: "query" })}
			>
				<SquareTerminalIcon
					aria-hidden
					className="size-3.5 shrink-0 text-ink-3"
				/>
				<span className="truncate">SQL console</span>
			</button>
			<div className="relative">
				<SearchIcon
					aria-hidden
					className="pointer-events-none absolute top-1/2 left-2.5 size-3.5 -translate-y-1/2 text-ink-3"
				/>
				<Input
					type="search"
					aria-label="Filter tables"
					placeholder="Filter tables"
					value={filter}
					autoComplete="off"
					spellCheck={false}
					className="pl-8"
					onChange={(event) => setFilter(event.target.value)}
				/>
			</div>
			<ul className="flex max-h-[420px] min-h-0 flex-col gap-0.5 overflow-y-auto">
				{shown.map((table) => {
					const estimate = estimateLabel(table.rowEstimate);
					return (
						<li key={tableKey(table)}>
							<button
								type="button"
								aria-pressed={isSelected(selection, table)}
								className={ROW}
								onClick={() => onSelect({ kind: "table", table })}
							>
								<TableIcon
									aria-hidden
									className={cn(
										"size-3.5 shrink-0",
										table.kind === "view" ? "text-info" : "text-ink-3",
									)}
								/>
								<span className="truncate font-mono text-xs">
									{tableKey(table)}
								</span>
								{table.kind === "view" ? (
									<span className="shrink-0 text-2xs text-ink-4">view</span>
								) : null}
								{estimate === "" ? null : (
									<span className="ml-auto shrink-0 text-2xs text-ink-4">
										{estimate}
									</span>
								)}
							</button>
						</li>
					);
				})}
			</ul>
			{tables.length === 0 ? (
				<p className="px-2 text-ink-3 text-xs">
					This database has no tables yet.
				</p>
			) : shown.length === 0 ? (
				<p className="px-2 text-ink-3 text-xs">
					No table matches “{filter.trim()}”.
				</p>
			) : null}
		</nav>
	);
}
