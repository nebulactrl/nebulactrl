import { useQuery, useQueryClient } from "@tanstack/react-query";
import { useNavigate } from "@tanstack/react-router";
import { type ReactNode, useCallback, useState } from "react";
import { errorMessage } from "#/api/client-config";
import { deleteProject } from "#/api/gen/sdk.gen";
import { environmentsQuery, projectDeletionPlanQuery } from "#/api/hooks";
import { invalidateOperations, SERVICE_OPERATIONS } from "#/api/invalidate";
import type { Project } from "#/api/models";
import { ConfirmDialog } from "#/components/common/confirm-dialog";
import { Skeleton } from "#/components/ui/skeleton";
import { toast } from "#/components/ui/toast";
import { describeDeletionPlan } from "./deletion-plan";

type Target = Pick<Project, "id" | "name">;

/** What the server would delete now; read only while the dialog is open. */
function useDeletionPlan(projectId: string, enabled: boolean) {
	return useQuery({ ...projectDeletionPlanQuery(projectId), enabled });
}

function PlanList({ plan }: { plan: ReturnType<typeof useDeletionPlan> }) {
	if (plan.isPending) {
		return (
			<div
				role="status"
				aria-label="Counting what this project contains"
				className="flex flex-col gap-2"
			>
				<Skeleton className="h-3.5 w-2/3" />
				<Skeleton className="h-3.5 w-1/2" />
				<Skeleton className="h-3.5 w-3/5" />
			</div>
		);
	}
	if (plan.isError) {
		return (
			<p className="text-destructive-text">
				The control plane could not list what this project contains, so nothing
				can be deleted yet. {errorMessage(plan.error)}
			</p>
		);
	}

	const lines = [
		...describeDeletionPlan(plan.data),
		"Shared variables of the project and its environments",
	];
	return (
		<>
			<ul className="flex list-disc flex-col gap-1 pl-5 marker:text-ink-4">
				{lines.map((line) => (
					<li key={line} className="text-ink-2">
						{line}
					</li>
				))}
			</ul>
			{plan.data.backupDestinationsKept &&
			(plan.data.databases > 0 || plan.data.volumes > 0) ? (
				<p>Any backups already written to your destinations are kept.</p>
			) : null}
		</>
	);
}

/**
 * Deleting a project and everything in it. The list is what the server
 * counts right now, and the action stays locked until it has arrived and
 * the project's name is typed, so nobody confirms what they were not shown.
 */
function DeleteProjectDialog({
	project,
	open,
	onOpenChange,
}: {
	project: Target;
	open: boolean;
	onOpenChange: (open: boolean) => void;
}) {
	const queryClient = useQueryClient();
	const navigate = useNavigate();
	const plan = useDeletionPlan(project.id, open);

	const remove = async () => {
		await deleteProject({
			path: { projectId: project.id },
			query: { cascade: true, confirm: project.name },
			throwOnError: true,
		});
		toast.success(`Deleted ${project.name}`);
		// Leave the project's own pages before anything refetches them.
		await navigate({ to: "/" });
		queryClient.removeQueries({
			queryKey: environmentsQuery(project.id).queryKey,
		});
		queryClient.removeQueries({
			queryKey: projectDeletionPlanQuery(project.id).queryKey,
		});
		void invalidateOperations(queryClient, [
			"listProjects",
			"listEnvironments",
			...SERVICE_OPERATIONS,
		]);
	};

	return (
		<ConfirmDialog
			open={open}
			onOpenChange={onOpenChange}
			title={`Delete ${project.name}?`}
			glyph="trash"
			danger
			cta="Delete project"
			typeToConfirm={project.name}
			blocked={!plan.isSuccess}
			onConfirm={remove}
			body={
				<div className="flex flex-col gap-3">
					<p>
						This deletes <b className="text-ink-1">{project.name}</b> and
						everything in it, and stops its containers. It cannot be undone.
					</p>
					<PlanList plan={plan} />
				</div>
			}
		/>
	);
}

/**
 * The delete dialog's open state. `ask(project)` opens it for that project;
 * render `dialog` next to the control that asks. The dialog mounts on the
 * first ask, so a screen of project cards carries one only where it was used.
 */
export function useDeleteProject(): {
	ask: (project: Target) => void;
	dialog: ReactNode;
} {
	const [target, setTarget] = useState<Target | null>(null);
	const [open, setOpen] = useState(false);
	const ask = useCallback((project: Target) => {
		setTarget(project);
		setOpen(true);
	}, []);
	return {
		ask,
		dialog: target ? (
			<DeleteProjectDialog
				key={target.id}
				project={target}
				open={open}
				onOpenChange={setOpen}
			/>
		) : null,
	};
}
