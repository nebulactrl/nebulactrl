import {
	FolderOpenIcon,
	ScrollTextIcon,
	TerminalSquareIcon,
} from "lucide-react";
import { useEffect, useMemo, useRef } from "react";
import type { Service } from "#/api/models";
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
import { useStackNavigation } from "#/features/stacks/url";
import { openFiles, openShell } from "#/features/terminal";
import {
	processReplicaLabel,
	processRows,
	processState,
	serviceProcesses,
} from "./model";
import { PROCESS_STATUS_ICON } from "./process-card";
import { useProcessParam } from "./url";

/**
 * The Overview's Processes table: one row per process in rollout order, with
 * its state in words and a status icon, port, replicas and start command. The
 * row `?process=` names is highlighted, scrolled into view and lists its
 * replicas beneath it; clicking a row names that process in the address,
 * clicking it again clears it. Rows appear and reorder instantly.
 */
export function ProcessesTable({ service }: { service: Service }) {
	const highlighted = useProcessParam();
	const { show } = useStackNavigation();
	const rows = useMemo(() => processRows(service), [service]);
	const argv = useMemo(
		() =>
			new Map(
				serviceProcesses(service).map((process) => [
					process.name,
					process.command.join(" "),
				]),
			),
		[service],
	);

	const marked = useRef<HTMLTableRowElement | null>(null);
	useEffect(() => {
		if (highlighted === undefined) return;
		marked.current?.scrollIntoView({ block: "nearest" });
	}, [highlighted]);

	if (rows.length === 0) return null;

	const defaultName = rows.find((row) => row.isDefault)?.name;
	// A replica of a service without the label belongs to the default process.
	const replicasOf = (name: string) =>
		(service.observed?.replicas ?? []).filter((replica) =>
			replica.process === "" ? name === defaultName : replica.process === name,
		);

	return (
		<Table aria-label="Processes" className="text-[13px]">
			<TableHeader>
				<TableRow>
					<TableHead className="px-2">Process</TableHead>
					<TableHead className="px-2">State</TableHead>
					<TableHead className="px-2 text-right">Port</TableHead>
					<TableHead className="px-2 text-right">Replicas</TableHead>
					<TableHead className="w-0 px-1">
						<span className="sr-only">Actions</span>
					</TableHead>
				</TableRow>
			</TableHeader>
			<TableBody>
				{rows.flatMap((row) => {
					const isMarked = row.name === highlighted;
					const replicas = isMarked ? replicasOf(row.name) : [];
					const line = (
						<TableRow
							key={row.name}
							ref={isMarked ? marked : undefined}
							aria-selected={isMarked || undefined}
							data-process={row.name}
						>
							<TableCell className="min-w-0 px-2 py-2.5 align-top">
								<div className="flex items-center gap-1.5">
									<button
										type="button"
										aria-pressed={isMarked}
										title={isMarked ? "Show every process" : `Show ${row.name}`}
										onClick={() =>
											show({ process: isMarked ? null : row.name })
										}
										className="cursor-pointer rounded-sm font-medium font-mono text-ink-1 text-xs outline-none hover:underline focus-visible:focus-ring"
									>
										{row.name}
									</button>
									{row.showDefaultTag ? (
										<span className="text-[10.5px] text-ink-3">default</span>
									) : null}
									{row.badge ? (
										<span
											className={
												row.badge === "staged"
													? "text-[10.5px] text-status-staged-on"
													: "text-[10.5px] text-ink-3"
											}
										>
											{row.badge}
										</span>
									) : null}
								</div>
								<div
									title={argv.get(row.name) || row.commandSummary}
									className="mt-0.5 max-w-[190px] truncate font-mono text-[10.5px] text-ink-3 leading-[1.4]"
								>
									{row.commandSummary}
								</div>
							</TableCell>
							<TableCell className="px-2 py-2.5 align-top">
								<span className="flex items-center gap-1.5 whitespace-nowrap text-ink-1 text-xs">
									<span className={PROCESS_STATUS_ICON}>
										<StatusIcon kind={row.kind} label="" />
									</span>
									{row.stateLabel}
								</span>
							</TableCell>
							<TableCell className="px-2 py-2.5 text-right align-top font-mono text-ink-2 text-xs tabular-nums">
								{row.port > 0 ? `:${row.port}` : "—"}
							</TableCell>
							<TableCell className="px-2 py-2.5 text-right align-top font-mono text-ink-2 text-xs tabular-nums">
								{row.replicasText}
							</TableCell>
							<TableCell className="px-1 py-2 align-top">
								<div className="flex items-center justify-end">
									<Button
										variant="ghost"
										size="icon-sm"
										aria-label={`Logs of ${row.name}`}
										title={`Logs of ${row.name}`}
										onClick={(event) => {
											event.stopPropagation();
											show({ tab: "logs", process: row.name });
										}}
									>
										<ScrollTextIcon aria-hidden />
									</Button>
								</div>
							</TableCell>
						</TableRow>
					);
					if (replicas.length === 0) return [line];
					return [
						line,
						<TableRow key={`${row.name}:replicas`}>
							<TableCell colSpan={5} className="px-2 pt-0 pb-2.5">
								<ul
									aria-label={`Replicas of ${row.name}`}
									className="m-0 flex list-none flex-col gap-1 p-0"
								>
									{replicas.map((replica) => {
										const state = processState(replica);
										return (
											<li
												key={`${replica.process}:${replica.index}`}
												className="flex items-center gap-1.5 font-mono text-[11px] text-ink-2"
											>
												<span className={PROCESS_STATUS_ICON}>
													<StatusIcon kind={state.kind} label="" />
												</span>
												{processReplicaLabel(row.name, replica.index)}
												<span className="text-ink-3">{state.label}</span>
												<span className="ml-auto flex items-center">
													<Button
														variant="ghost"
														size="icon-sm"
														aria-label={`Open a shell in ${processReplicaLabel(row.name, replica.index)}`}
														title={
															replica.state === "running"
																? "Open a shell…"
																: "Not running"
														}
														disabled={replica.state !== "running"}
														onClick={() =>
															openShell(service.id, {
																process: row.name,
																replica: replica.index,
															})
														}
													>
														<TerminalSquareIcon aria-hidden />
													</Button>
													<Button
														variant="ghost"
														size="icon-sm"
														aria-label={`Browse files of ${processReplicaLabel(row.name, replica.index)}`}
														title={
															replica.state === "running"
																? "Browse files…"
																: "Not running"
														}
														disabled={replica.state !== "running"}
														onClick={() =>
															openFiles(service.id, {
																process: row.name,
																replica: replica.index,
															})
														}
													>
														<FolderOpenIcon aria-hidden />
													</Button>
												</span>
											</li>
										);
									})}
								</ul>
							</TableCell>
						</TableRow>,
					];
				})}
			</TableBody>
		</Table>
	);
}
