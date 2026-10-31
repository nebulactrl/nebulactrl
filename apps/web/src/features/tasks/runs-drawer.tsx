import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { cn } from "cn";
import { RotateCcwIcon } from "lucide-react";
import { useState } from "react";
import {
	listServiceTaskRunsOptions,
	listServiceTasksOptions,
} from "#/api/gen/@tanstack/react-query.gen";
import { runServiceCommand, runServiceTask } from "#/api/gen/sdk.gen";
import type { RunView } from "#/api/gen/types.gen";
import { serviceQuery } from "#/api/hooks";
import { CodeBlock } from "#/components/common/code-block";
import { ErrorState } from "#/components/common/error-state";
import { Relative } from "#/components/common/relative";
import { Alert, AlertDescription } from "#/components/ui/alert";
import { Button } from "#/components/ui/button";
import { StatusPill } from "#/components/ui/lamp";
import {
	Sheet,
	SheetBody,
	SheetContent,
	SheetDescription,
	SheetHeader,
	SheetTitle,
} from "#/components/ui/sheet";
import { Skeleton } from "#/components/ui/skeleton";
import { toast } from "#/components/ui/toast";
import { absoluteTime } from "#/lib/format";
import {
	commandText,
	formatRunDuration,
	isActive,
	rerunKind,
	runPresentation,
	sourceLabel,
} from "./model";
import { invalidateTasks, startErrorMessage } from "./queries";
import { RunOutput } from "./run-output";
import {
	closeRunHistory,
	type RunHistoryTarget,
	useRunHistoryTarget,
} from "./store";

const RUN_LIMIT = 50;
/** Refetch cadence while a run is active (the event feed is the primary signal). */
const ACTIVE_POLL_MS = 4000;

function RunRow({
	run,
	selected,
	onSelect,
}: {
	run: RunView;
	selected: boolean;
	onSelect: () => void;
}) {
	const p = runPresentation(run.status);
	return (
		<li>
			<button
				type="button"
				aria-current={selected ? "true" : undefined}
				onClick={onSelect}
				className={cn(
					"flex w-full flex-col gap-1 border-line-faint border-b px-3 py-2 text-left outline-none hover:bg-white/5 focus-visible:focus-ring",
					selected && "bg-white/[0.07]",
				)}
			>
				<span className="flex flex-wrap items-center gap-x-3 gap-y-1">
					<StatusPill tone={p.tone} pulse={p.pulse}>
						{p.label}
					</StatusPill>
					<span className="min-w-0 truncate font-medium text-ink-1 text-sm">
						{run.name}
					</span>
					<span className="text-ink-3 text-xs">{sourceLabel(run.source)}</span>
				</span>
				<span className="flex flex-wrap items-center gap-x-3 text-ink-3 text-xs tabular-nums">
					<span>
						{run.startedAt ? (
							<>
								started <Relative iso={run.startedAt} />
							</>
						) : (
							<>
								created <Relative iso={run.createdAt} />
							</>
						)}
					</span>
					<span>{formatRunDuration(run)}</span>
					<span className="font-mono">
						{run.exitCode === undefined ? "exit –" : `exit ${run.exitCode}`}
					</span>
				</span>
				{run.error ? (
					<span
						className="line-clamp-2 text-destructive-text text-xs"
						title={run.error}
					>
						{run.status === "skipped" ? `Skipped: ${run.error}` : run.error}
					</span>
				) : null}
			</button>
		</li>
	);
}

function RunDetail({
	run,
	serviceId,
	onStarted,
}: {
	run: RunView;
	serviceId: string;
	/** A "Run again" started this run: show it. */
	onStarted: (runId: string) => void;
}) {
	const queryClient = useQueryClient();
	const p = runPresentation(run.status);
	const active = isActive(run.status);
	const rerun = rerunKind(run);

	const again = useMutation({
		mutationFn: async () => {
			if (rerun === "task" && run.taskId) {
				const { data } = await runServiceTask({ path: { taskId: run.taskId } });
				return data;
			}
			const { data } = await runServiceCommand({
				path: { serviceId },
				body: { command: run.command, timeoutSeconds: run.timeoutSeconds },
			});
			return data;
		},
		onSuccess: async (started) => {
			toast.success("Started", `${started.name} is running again.`);
			await invalidateTasks(queryClient);
			onStarted(started.id);
		},
		onError: (e) =>
			toast.error("Could not start the run", startErrorMessage(e)),
	});

	return (
		<div className="flex min-h-0 flex-1 flex-col gap-3 p-3">
			<div className="flex flex-wrap items-center gap-x-3 gap-y-1.5">
				<StatusPill tone={p.tone} pulse={p.pulse}>
					{p.label}
				</StatusPill>
				<span className="min-w-0 truncate font-medium text-ink-1 text-sm">
					{run.name}
				</span>
				{rerun ? (
					<Button
						variant="secondary"
						size="sm"
						className="ml-auto"
						disabled={active || again.isPending}
						title={active ? "Wait for this run to finish" : undefined}
						onClick={() => again.mutate()}
					>
						<RotateCcwIcon aria-hidden />
						Run again
					</Button>
				) : null}
			</div>
			<dl className="grid grid-cols-[auto_1fr] gap-x-4 gap-y-0.5 text-xs">
				<dt className="text-ink-3">Source</dt>
				<dd className="text-ink-2">
					{sourceLabel(run.source)}
					{run.actorLabel ? ` · ${run.actorLabel}` : ""}
				</dd>
				<dt className="text-ink-3">Started</dt>
				<dd className="text-ink-2">
					{run.startedAt ? absoluteTime(run.startedAt) : "not started"}
				</dd>
				<dt className="text-ink-3">Duration</dt>
				<dd className="text-ink-2 tabular-nums">{formatRunDuration(run)}</dd>
				<dt className="text-ink-3">Exit code</dt>
				<dd className="font-mono text-ink-2">{run.exitCode ?? "–"}</dd>
				<dt className="text-ink-3">Timeout</dt>
				<dd className="text-ink-2 tabular-nums">{run.timeoutSeconds}s</dd>
			</dl>
			<CodeBlock copyValue={commandText(run.command)}>
				{commandText(run.command)}
			</CodeBlock>
			{run.error ? (
				<Alert variant={run.status === "skipped" ? "warning" : "destructive"}>
					<AlertDescription>
						{run.status === "skipped" ? "Skipped: " : null}
						{run.error}
					</AlertDescription>
				</Alert>
			) : null}
			{run.status === "skipped" ? (
				<p className="text-ink-3 text-xs">
					A skipped run never started, so it has no output.
				</p>
			) : (
				<RunOutput
					key={run.id}
					runId={run.id}
					active={active}
					label={`Output of ${run.name}`}
					className="flex-1"
				/>
			)}
		</div>
	);
}

function RunsList({ target }: { target: RunHistoryTarget }) {
	const { serviceId, taskId } = target;
	const runs = useQuery({
		...listServiceTaskRunsOptions({
			path: { serviceId },
			query: taskId ? { taskId, limit: RUN_LIMIT } : { limit: RUN_LIMIT },
		}),
		refetchInterval: (q) =>
			(q.state.data?.items ?? []).some((r) => isActive(r.status))
				? ACTIVE_POLL_MS
				: false,
	});
	const [selectedId, setSelectedId] = useState<string | null>(null);

	if (runs.isPending) {
		return (
			<div className="flex flex-col gap-2 p-4" role="status">
				<Skeleton className="h-14 rounded-lg" />
				<Skeleton className="h-14 rounded-lg" />
				<Skeleton className="h-14 rounded-lg" />
			</div>
		);
	}
	if (runs.isError && !runs.data) {
		return (
			<div className="p-4">
				<ErrorState
					frame="raised"
					title="Run history did not load"
					body="The control plane did not answer, so nothing was changed."
					retry={() => void runs.refetch()}
				/>
			</div>
		);
	}

	const items = runs.data?.items ?? [];
	if (items.length === 0) {
		return (
			<p className="p-4 text-ink-3 text-sm">
				{taskId
					? "This task has not run yet. Use Run now on the task, or wait for its schedule."
					: "Nothing has run yet. Scheduled tasks, Run now and one-off commands appear here."}
			</p>
		);
	}
	const selected = items.find((r) => r.id === selectedId) ?? items[0];

	return (
		<div className="flex min-h-0 flex-1 flex-col">
			<ul
				aria-label="Runs, newest first"
				className="max-h-[38%] shrink-0 overflow-y-auto overscroll-contain border-line border-b"
			>
				{items.map((run) => (
					<RunRow
						key={run.id}
						run={run}
						selected={run.id === selected?.id}
						onSelect={() => setSelectedId(run.id)}
					/>
				))}
			</ul>
			{selected ? (
				<RunDetail
					run={selected}
					serviceId={serviceId}
					onStarted={setSelectedId}
				/>
			) : null}
		</div>
	);
}

/**
 * The run history of a service (or of one task) as a drawer: runs newest
 * first, and the output of the one selected. Mounted once by `TasksHost`.
 */
export function RunsDrawer() {
	const target = useRunHistoryTarget();
	const service = useQuery({
		...serviceQuery(target?.serviceId ?? ""),
		enabled: target !== null,
	});
	const tasks = useQuery({
		...listServiceTasksOptions({
			path: { serviceId: target?.serviceId ?? "" },
		}),
		enabled: target?.taskId !== undefined,
	});
	const taskName = tasks.data?.items?.find(
		(t) => t.id === target?.taskId,
	)?.name;

	return (
		<Sheet
			open={target !== null}
			onOpenChange={(open) => {
				if (!open) closeRunHistory();
			}}
		>
			<SheetContent className="sm:max-w-[640px]">
				<SheetHeader className="pb-3">
					<SheetTitle className="pr-10">
						{taskName ? `Runs of ${taskName}` : "Run history"}
					</SheetTitle>
					<SheetDescription>
						{service.data ? `${service.data.name}: ` : ""}
						scheduled runs, Run now and one-off commands. Times are UTC.
					</SheetDescription>
				</SheetHeader>
				<SheetBody className="flex flex-col overscroll-contain p-0">
					{target ? (
						<RunsList
							key={`${target.serviceId}:${target.taskId ?? ""}`}
							target={target}
						/>
					) : null}
				</SheetBody>
			</SheetContent>
		</Sheet>
	);
}
