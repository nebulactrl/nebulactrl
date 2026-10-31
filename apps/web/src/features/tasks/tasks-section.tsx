import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { HistoryIcon, PlayIcon, PlusIcon, TerminalIcon } from "lucide-react";
import { useState } from "react";
import { errorMessage } from "#/api/client-config";
import {
	listServiceTasksOptions,
	listServiceTasksQueryKey,
} from "#/api/gen/@tanstack/react-query.gen";
import {
	deleteServiceTask,
	runServiceTask,
	updateServiceTask,
} from "#/api/gen/sdk.gen";
import type { TaskListOutputBody, TaskView } from "#/api/gen/types.gen";
import type { Service } from "#/api/models";
import { useConfirm } from "#/components/common/confirm-dialog";
import { ErrorState } from "#/components/common/error-state";
import { Relative } from "#/components/common/relative";
import { Badge } from "#/components/ui/badge";
import { Button } from "#/components/ui/button";
import { StatusPill } from "#/components/ui/lamp";
import { Skeleton } from "#/components/ui/skeleton";
import { Switch } from "#/components/ui/switch";
import {
	Table,
	TableBody,
	TableCell,
	TableHead,
	TableHeader,
	TableRow,
} from "#/components/ui/table";
import { toast } from "#/components/ui/toast";
import { describeCron } from "#/features/backups/model";
import { FieldMark } from "#/features/project-config";
import { Section } from "#/features/service-settings/fields";
import { commandText, formatRunTimes, runPresentation } from "./model";
import { invalidateTasks, startErrorMessage } from "./queries";
import { openRunCommand, openRunHistory } from "./store";
import { TaskDialog } from "./task-dialog";

function TaskRow({
	task,
	deployed,
	running,
	onToggle,
	onRun,
	onEdit,
	onDelete,
}: {
	task: TaskView;
	deployed: boolean;
	running: boolean;
	onToggle: (task: TaskView, enabled: boolean) => void;
	onRun: (task: TaskView) => void;
	onEdit: (task: TaskView) => void;
	onDelete: (task: TaskView) => void;
}) {
	const command = commandText(task.command);
	const described = describeCron(task.schedule);
	const last = task.lastRun;
	const lastPresentation = last ? runPresentation(last.status) : null;
	return (
		<TableRow>
			<TableCell pin="start">
				<span className="flex min-w-0 flex-col gap-0.5">
					<span className="flex min-w-0 items-center gap-1.5">
						<span className="truncate font-medium text-ink-1 text-sm">
							{task.name}
						</span>
						<FieldMark field={`tasks.${task.name}`} />
					</span>
					<code
						className="type-mono max-w-[280px] truncate text-ink-3 text-xs"
						title={command}
					>
						{command || "–"}
					</code>
				</span>
			</TableCell>
			<TableCell label="Schedule">
				<span className="flex flex-col gap-0.5">
					<code className="type-mono text-ink-2 text-xs">{task.schedule}</code>
					<span className="text-ink-3 text-xs">
						{described !== task.schedule ? `${described}, ` : ""}
						{task.timezone}
					</span>
				</span>
			</TableCell>
			<TableCell label="Enabled">
				<span className="flex items-center gap-2 text-ink-3 text-xs">
					<Switch
						size="sm"
						aria-label={
							task.enabled ? `Pause ${task.name}` : `Resume ${task.name}`
						}
						checked={task.enabled}
						onCheckedChange={(next) => onToggle(task, next)}
					/>
					{task.enabled ? "On" : "Paused"}
				</span>
			</TableCell>
			<TableCell label="Next run" className="text-ink-2 text-xs">
				{task.enabled && task.nextRunAt ? (
					<span className="flex flex-col gap-0.5">
						<Relative iso={task.nextRunAt} />
						<span className="text-ink-3">
							{formatRunTimes(task.nextRunAt, task.timezone).zoned}{" "}
							{task.timezone}
						</span>
					</span>
				) : (
					<span className="text-ink-4">{task.enabled ? "–" : "Paused"}</span>
				)}
			</TableCell>
			<TableCell label="Last run">
				{last && lastPresentation ? (
					<span className="flex flex-col items-start gap-1">
						<button
							type="button"
							title="Open the run history of this task"
							aria-label={`${lastPresentation.label}: open the run history of ${task.name}`}
							className="rounded-full outline-none focus-visible:focus-ring"
							onClick={() => openRunHistory(task.serviceId, task.id)}
						>
							<StatusPill
								tone={lastPresentation.tone}
								pulse={lastPresentation.pulse}
							>
								{lastPresentation.label}
							</StatusPill>
						</button>
						<span className="text-ink-3 text-xs">
							<Relative iso={task.lastRunAt ?? last.createdAt} />
						</span>
					</span>
				) : (
					<span className="text-ink-4 text-xs">Never run</span>
				)}
			</TableCell>
			<TableCell pin="end">
				<span className="flex justify-end gap-1">
					<Button
						variant="ghost"
						size="sm"
						disabled={!deployed || running}
						title={deployed ? undefined : "Deploy the service first"}
						onClick={() => onRun(task)}
					>
						<PlayIcon aria-hidden />
						Run now
					</Button>
					<Button variant="ghost" size="sm" onClick={() => onEdit(task)}>
						Edit
					</Button>
					<Button
						variant="ghost"
						size="sm"
						className="text-destructive-text"
						onClick={() => onDelete(task)}
					>
						Delete
					</Button>
				</span>
			</TableCell>
		</TableRow>
	);
}

/**
 * The Tasks section of a service's settings: scheduled commands that run in
 * a fresh container of the service's image, plus one-off commands and the
 * run history. Each schedule has an IANA time zone (UTC by default). Each change applies at once; nothing is
 * staged or deployed.
 */
export function TasksSection({ service }: { service: Service }) {
	const serviceId = service.id;
	const deployed = service.currentDeploymentId !== null;
	const queryClient = useQueryClient();
	const confirm = useConfirm();
	const tasks = useQuery(listServiceTasksOptions({ path: { serviceId } }));
	const [dialogOpen, setDialogOpen] = useState(false);
	const [editing, setEditing] = useState<TaskView | null>(null);

	const toggle = useMutation({
		mutationFn: ({ task, enabled }: { task: TaskView; enabled: boolean }) =>
			updateServiceTask({ path: { taskId: task.id }, body: { enabled } }),
		onMutate: async ({ task, enabled }) => {
			const key = listServiceTasksQueryKey({ path: { serviceId } });
			await queryClient.cancelQueries({ queryKey: key });
			const before = queryClient.getQueryData<TaskListOutputBody>(key);
			queryClient.setQueryData<TaskListOutputBody>(key, (current) =>
				current
					? {
							...current,
							items: (current.items ?? []).map((t) =>
								t.id === task.id ? { ...t, enabled } : t,
							),
						}
					: current,
			);
			return { key, before };
		},
		onError: (e, _vars, context) => {
			if (context?.before)
				queryClient.setQueryData(context.key, context.before);
			toast.error("Could not change the task", errorMessage(e));
		},
		onSettled: () => invalidateTasks(queryClient),
	});

	const run = useMutation({
		mutationFn: async (task: TaskView) => {
			await runServiceTask({ path: { taskId: task.id } });
		},
		onSuccess: async (_data, task) => {
			toast.success(
				`Started ${task.name}`,
				"Its output is in the run history.",
			);
			openRunHistory(serviceId, task.id);
			await invalidateTasks(queryClient);
		},
		onError: (e) =>
			toast.error("Could not start the task", startErrorMessage(e)),
	});

	const remove = useMutation({
		mutationFn: (task: TaskView) =>
			deleteServiceTask({ path: { taskId: task.id } }),
		onSuccess: async () => {
			toast.success("Task deleted", "Its past runs stay in the run history.");
			await invalidateTasks(queryClient);
		},
		onError: (e) => toast.error("Could not delete the task", errorMessage(e)),
	});

	const askDelete = (task: TaskView) =>
		void confirm({
			title: `Delete ${task.name}?`,
			body: "The schedule stops. A run that is in progress finishes; past runs stay in the run history.",
			cta: "Delete task",
			danger: true,
			hold: false,
			onConfirm: async () => {
				await remove.mutateAsync(task);
			},
		});

	const items = tasks.data?.items ?? [];

	return (
		<Section
			title="Tasks"
			description="Commands that run on a schedule, in a fresh container of this service's image with its variables and volumes. Each schedule is read in its own time zone (UTC by default). Changes apply at once; nothing is redeployed."
			aside={
				<span className="flex flex-wrap items-center gap-1.5">
					<Button
						variant="ghost"
						size="sm"
						disabled={!deployed}
						title={deployed ? undefined : "Deploy the service first"}
						onClick={() => openRunCommand(serviceId)}
					>
						<TerminalIcon aria-hidden />
						Run command…
					</Button>
					<Button
						variant="ghost"
						size="sm"
						onClick={() => openRunHistory(serviceId)}
					>
						<HistoryIcon aria-hidden />
						Run history
					</Button>
					<Button
						variant="secondary"
						size="sm"
						onClick={() => {
							setEditing(null);
							setDialogOpen(true);
						}}
					>
						<PlusIcon aria-hidden />
						Add task
					</Button>
				</span>
			}
		>
			{tasks.isPending ? (
				<Skeleton className="h-24 rounded-card" />
			) : tasks.isError && !tasks.data ? (
				<ErrorState
					frame="raised"
					title="Tasks did not load"
					body="The control plane did not answer, so nothing was changed."
					retry={() => void tasks.refetch()}
				/>
			) : items.length === 0 ? (
				<p className="py-2 text-ink-3 text-sm">
					No tasks yet. Add one to run a command on a schedule, such as a
					nightly cleanup or a report.
					{deployed ? null : (
						<>
							{" "}
							<Badge tone="warn" shape="capsule">
								not deployed
							</Badge>{" "}
							Tasks run in the image of the current release, so deploy the
							service first.
						</>
					)}
				</p>
			) : (
				<div className="overflow-hidden border-line border-y">
					<Table stack>
						<TableHeader>
							<TableRow>
								<TableHead>Task</TableHead>
								<TableHead>Schedule</TableHead>
								<TableHead>Enabled</TableHead>
								<TableHead>Next run</TableHead>
								<TableHead>Last run</TableHead>
								<TableHead className="w-px" />
							</TableRow>
						</TableHeader>
						<TableBody>
							{items.map((task) => (
								<TaskRow
									key={task.id}
									task={task}
									deployed={deployed}
									running={run.isPending && run.variables?.id === task.id}
									onToggle={(t, enabled) => toggle.mutate({ task: t, enabled })}
									onRun={(t) => run.mutate(t)}
									onEdit={(t) => {
										setEditing(t);
										setDialogOpen(true);
									}}
									onDelete={askDelete}
								/>
							))}
						</TableBody>
					</Table>
				</div>
			)}
			<TaskDialog
				open={dialogOpen}
				onOpenChange={(open) => {
					setDialogOpen(open);
					if (!open) setEditing(null);
				}}
				serviceId={serviceId}
				task={editing}
			/>
		</Section>
	);
}
