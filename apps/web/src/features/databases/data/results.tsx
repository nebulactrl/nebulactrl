import { type QueryResult, resultFooter } from "./model";
import { CommandResult, ResultGrid, truncationNote } from "./result-grid";

/**
 * What a run or a page returned: one section per statement (a grid for rows,
 * the completion tag for a statement without), a note under each set the
 * control plane cut, and a footer with the row count and the time.
 */
export function Results({
	result,
	label,
}: {
	result: QueryResult;
	/** Names the grids for assistive tech: "Result", "Rows of users". */
	label: string;
}) {
	const many = result.sets.length > 1;
	return (
		<div data-slot="data-results" className="flex min-w-0 flex-col gap-3">
			{result.sets.length === 0 ? (
				<p className="text-ink-3 text-sm">The statement returned no result.</p>
			) : null}
			{result.sets.map((set, index) => (
				<section
					// A result has no id of its own: its position in the run is it.
					// biome-ignore lint/suspicious/noArrayIndexKey: see above
					key={index}
					className="flex min-w-0 flex-col gap-1.5"
				>
					{many ? (
						<h4 className="font-medium text-ink-3 text-xs">
							Result {index + 1} of {result.sets.length}
						</h4>
					) : null}
					{set.columns.length === 0 ? (
						<CommandResult set={set} />
					) : (
						<ResultGrid
							set={set}
							label={many ? `${label} ${index + 1}` : label}
						/>
					)}
					{set.truncated ? (
						<p className="text-ink-3 text-xs">{truncationNote(set)}</p>
					) : null}
				</section>
			))}
			<p className="text-ink-3 text-xs">
				{resultFooter(result)}
				{result.truncatedCells ? " · long values are clipped" : ""}
			</p>
		</div>
	);
}
