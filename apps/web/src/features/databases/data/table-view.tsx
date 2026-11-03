import { useQuery, useQueryClient } from "@tanstack/react-query";
import { ChevronLeftIcon, ChevronRightIcon, RefreshCwIcon } from "lucide-react";
import { useState } from "react";
import { errorMessage } from "#/api/client-config";
import { Alert, AlertDescription, AlertTitle } from "#/components/ui/alert";
import { Button } from "#/components/ui/button";
import { Skeleton } from "#/components/ui/skeleton";
import { BlockedState } from "./blocked-state";
import {
	blockedMessage,
	estimateLabel,
	hasNextPage,
	PAGE_SIZE,
	rowsLabel,
	type TableRef,
	tableKey,
} from "./model";
import { browseQuery, invalidateData } from "./queries";
import { ResultGrid } from "./result-grid";

/**
 * One table, 100 rows at a time. Previous and Next move by offset; the page
 * stays on screen while the next one loads. Every page read is audited by the
 * control plane, so a page is fetched once and Refresh asks again on purpose.
 */
export function TableView({
	serviceId,
	table,
}: {
	serviceId: string;
	table: TableRef;
}) {
	const queryClient = useQueryClient();
	const [page, setPage] = useState(0);
	const offset = page * PAGE_SIZE;
	const rows = useQuery(browseQuery(serviceId, table, offset));
	const set = rows.data?.sets[0];
	const estimate = estimateLabel(table.rowEstimate);

	const blocked = rows.isError ? blockedMessage(rows.error) : undefined;
	if (blocked !== undefined) return <BlockedState message={blocked} />;

	return (
		<section
			aria-label={`Table ${tableKey(table)}`}
			className="flex min-w-0 flex-col gap-3"
		>
			<header className="flex flex-wrap items-center gap-x-3 gap-y-1">
				<h3 className="min-w-0 truncate font-medium font-mono text-ink-1 text-sm">
					{tableKey(table)}
				</h3>
				{estimate === "" ? null : (
					<span className="text-ink-3 text-xs">{estimate}</span>
				)}
				<Button
					variant="ghost"
					size="sm"
					className="ml-auto"
					disabled={rows.isFetching}
					onClick={() => void invalidateData(queryClient)}
				>
					<RefreshCwIcon aria-hidden />
					Refresh
				</Button>
			</header>

			{rows.isError ? (
				<Alert variant="destructive">
					<AlertTitle>The rows could not be read</AlertTitle>
					<AlertDescription>{errorMessage(rows.error)}</AlertDescription>
				</Alert>
			) : set === undefined ? (
				<div aria-hidden className="flex flex-col gap-2">
					<Skeleton className="h-8 w-full rounded-card" />
					<Skeleton className="h-40 w-full rounded-card" />
				</div>
			) : (
				<>
					<ResultGrid set={set} label={`Rows of ${tableKey(table)}`} />
					<footer className="flex flex-wrap items-center gap-2 text-ink-3 text-xs">
						<span>
							{set.rowCount === 0
								? "No rows on this page"
								: `Rows ${offset + 1}–${offset + set.rowCount}`}
							{set.rowCount > 0 ? ` · ${rowsLabel(set.rowCount)}` : ""}
						</span>
						{rows.data?.truncatedCells ? (
							<span>· long values are clipped</span>
						) : null}
						<span className="ml-auto flex items-center gap-1">
							<Button
								variant="secondary"
								size="sm"
								disabled={page === 0 || rows.isFetching}
								onClick={() => setPage((current) => Math.max(0, current - 1))}
							>
								<ChevronLeftIcon aria-hidden />
								Previous
							</Button>
							<Button
								variant="secondary"
								size="sm"
								disabled={!hasNextPage(set) || rows.isFetching}
								onClick={() => setPage((current) => current + 1)}
							>
								Next
								<ChevronRightIcon aria-hidden />
							</Button>
						</span>
					</footer>
				</>
			)}
		</section>
	);
}
