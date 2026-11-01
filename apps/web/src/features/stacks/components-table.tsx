import { useQuery } from "@tanstack/react-query";
import { cn } from "cn";
import {
	FolderOpenIcon,
	ScrollTextIcon,
	TerminalSquareIcon,
} from "lucide-react";
import { useEffect, useMemo, useRef } from "react";
import type { Service } from "#/api/models";
import { LiveValue } from "#/components/motion/live-value";
import { Button } from "#/components/ui/button";
import { StatusIcon } from "#/components/ui/status-icon";
import {
	Table,
	TableBody,
	TableCell,
	TableHead,
	TableHeader,
	TableRow,
} from "#/components/ui/table";
import { serviceMetricsQuery } from "#/features/metrics/queries";
import { openFiles, openShell } from "#/features/terminal";
import { formatBytes } from "#/lib/format";
import {
	type ComponentRow,
	type ComponentUsage,
	componentRows,
	stackComponents,
} from "./model";
import { useComponentParam, useStackNavigation } from "./url";

const NO_USAGE: ComponentUsage[] = [];

/** "12.5%" of one core, or an em dash with no sample. */
function cpuText(row: ComponentRow): string {
	return row.cpuPercent === null ? "—" : `${row.cpuPercent.toFixed(1)}%`;
}

function memText(row: ComponentRow): string {
	return row.memBytes === null ? "—" : formatBytes(row.memBytes);
}

/**
 * The Overview's Components table: one row per compose service with its state
 * and health in words and a status icon, the image and the digest the release
 * runs, restarts, CPU and memory from the newest metrics sample and shortcuts
 * to its logs, a shell and its files. The row `?component=` names is
 * highlighted and scrolled into view.
 */
export function ComponentsTable({ service }: { service: Service }) {
	const highlighted = useComponentParam();
	const { show } = useStackNavigation();
	const metrics = useQuery({
		...serviceMetricsQuery(service.id, "1h"),
		enabled: service.currentDeploymentId !== null,
	});
	const points = metrics.data?.points ?? [];
	const newest = points[points.length - 1];
	const usage = useMemo<ComponentUsage[]>(
		() =>
			newest?.replicas
				? newest.replicas.map((replica) => ({
						index: replica.index,
						cpuPercent: replica.cpuPercent,
						memBytes: replica.memBytes,
					}))
				: NO_USAGE,
		[newest],
	);
	const rows = useMemo(
		() =>
			componentRows({
				components: stackComponents(service),
				replicas: service.observed?.replicas ?? [],
				usage,
				digests: service.currentRelease?.source.components,
			}),
		[service, usage],
	);

	const marked = useRef<HTMLTableRowElement | null>(null);
	useEffect(() => {
		if (highlighted === undefined) return;
		marked.current?.scrollIntoView({ block: "nearest" });
	}, [highlighted]);

	if (rows.length === 0) {
		return (
			<p className="m-0 text-ink-3 text-xs leading-[1.5]">
				No components yet. The Compose file's services appear here once it is
				saved.
			</p>
		);
	}

	return (
		<Table aria-label="Components" className="text-[13px]">
			<TableHeader>
				<TableRow>
					<TableHead className="px-2">Component</TableHead>
					<TableHead className="px-2">State</TableHead>
					<TableHead className="px-2 text-right">CPU · memory</TableHead>
					<TableHead className="w-0 px-1">
						<span className="sr-only">Actions</span>
					</TableHead>
				</TableRow>
			</TableHeader>
			<TableBody>
				{rows.map((row) => {
					const isMarked = row.name === highlighted;
					return (
						<TableRow
							key={row.name}
							ref={isMarked ? marked : undefined}
							aria-selected={isMarked || undefined}
							data-component={row.name}
						>
							<TableCell className="min-w-0 px-2 py-2.5 align-top">
								<div className="font-medium font-mono text-ink-1 text-xs">
									{row.name}
								</div>
								<div
									title={[row.image, row.digest].filter(Boolean).join(" · ")}
									className="mt-0.5 max-w-[150px] truncate font-mono text-[10.5px] text-ink-3 leading-[1.4]"
								>
									{row.image || "no image"}
									{row.digest ? ` · ${row.digest}` : ""}
								</div>
							</TableCell>
							<TableCell className="px-2 py-2.5 align-top">
								<span className="flex items-center gap-1.5 whitespace-nowrap text-ink-1 text-xs">
									<StatusIcon kind={row.state.kind} label="" />
									{row.state.label}
								</span>
								<span
									className={cn(
										"mt-0.5 block font-mono text-[10.5px] leading-[1.4]",
										row.restarts > 0 ? "text-warning-text" : "text-ink-3",
									)}
								>
									{row.restarts === 1
										? "1 restart"
										: `${row.restarts} restarts`}
								</span>
							</TableCell>
							<TableCell className="px-2 py-2.5 text-right align-top font-mono text-ink-2 text-xs tabular-nums">
								<div>
									<LiveValue>{cpuText(row)}</LiveValue>
								</div>
								<div className="mt-0.5 text-[10.5px] text-ink-3">
									<LiveValue>{memText(row)}</LiveValue>
								</div>
							</TableCell>
							<TableCell className="px-1 py-2 align-top">
								<div className="flex items-center justify-end">
									<Button
										variant="ghost"
										size="icon-sm"
										aria-label={`Logs of ${row.name}`}
										title={`Logs of ${row.name}`}
										onClick={() => show({ tab: "logs", component: row.name })}
									>
										<ScrollTextIcon aria-hidden />
									</Button>
									<Button
										variant="ghost"
										size="icon-sm"
										aria-label={`Open a shell in ${row.name}`}
										title={
											row.running
												? `Open a shell in ${row.name}`
												: `${row.name} is not running`
										}
										disabled={!row.running}
										onClick={() =>
											openShell(service.id, { replica: row.index })
										}
									>
										<TerminalSquareIcon aria-hidden />
									</Button>
									<Button
										variant="ghost"
										size="icon-sm"
										aria-label={`Browse files of ${row.name}`}
										title={
											row.running
												? `Browse files of ${row.name}`
												: `${row.name} is not running`
										}
										disabled={!row.running}
										onClick={() =>
											openFiles(service.id, { replica: row.index })
										}
									>
										<FolderOpenIcon aria-hidden />
									</Button>
								</div>
							</TableCell>
						</TableRow>
					);
				})}
			</TableBody>
		</Table>
	);
}
