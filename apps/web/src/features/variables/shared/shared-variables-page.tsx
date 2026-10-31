import { useQuery } from "@tanstack/react-query";
import { cn } from "cn";
import {
	EllipsisVerticalIcon,
	LockIcon,
	PencilIcon,
	PlusIcon,
	RefreshCwIcon,
	Share2Icon,
	Trash2Icon,
} from "lucide-react";
import { useState } from "react";
import { errorMessage } from "#/api/client-config";
import {
	rotateEnvironmentSharedVariable,
	rotateProjectSharedVariable,
	rotateTeamSharedVariable,
} from "#/api/gen/sdk.gen";
import type { SharedVariableView } from "#/api/gen/types.gen";
import {
	environmentSharedVariablesQuery,
	projectSharedVariablesQuery,
	teamSharedVariablesQuery,
} from "#/api/hooks";
import type { Environment } from "#/api/models";
import { useConfirm } from "#/components/common/confirm-dialog";
import { EmptyState } from "#/components/common/empty-state";
import { ErrorState } from "#/components/common/error-state";
import { EnvironmentPage } from "#/components/shell/page-frame";
import { Badge } from "#/components/ui/badge";
import { Button } from "#/components/ui/button";
import {
	DropdownMenu,
	DropdownMenuContent,
	DropdownMenuItem,
	DropdownMenuSeparator,
	DropdownMenuTrigger,
} from "#/components/ui/dropdown-menu";
import { Segmented } from "#/components/ui/segmented";
import { Skeleton } from "#/components/ui/skeleton";
import { toast } from "#/components/ui/toast";
import { useServices } from "#/features/canvas/data";
import { RotatePlanDialog, type RotationSource } from "../rotate-dialog";
import { ShareDialog } from "./share-dialog";
import { SharedVariableDialog } from "./shared-variable-dialog";
import { type SharedOwner, useSharedMutations } from "./use-shared-mutations";

const SECRET_MASK = "••••••";

type Layer = "environment" | "project" | "team";

/**
 * Shared variables: values kept once for a whole project (or one of its
 * environments) and referenced from services as `${{shared.KEY}}`; the team's
 * values apply to every project and are referenced as `${{team.KEY}}`. An
 * environment value overrides the project value for that environment. Nothing
 * reaches a service implicitly: "Share to services" stages the reference.
 */
export function SharedVariablesPage({
	projectId,
	envSlug,
}: {
	projectId: string;
	envSlug: string;
}) {
	return (
		<EnvironmentPage
			projectId={projectId}
			envSlug={envSlug}
			page="variables"
			title="Shared variables"
			description={(environment) =>
				`Values shared across ${environment.name}, the whole project or the whole team. A service uses one through a reference such as KEY=\${{shared.KEY}} (team values: \${{team.KEY}}).`
			}
		>
			{(environment) => <SharedBody environment={environment} />}
		</EnvironmentPage>
	);
}

function SharedBody({ environment }: { environment: Environment }) {
	const [layer, setLayer] = useState<Layer>("environment");
	const owner: SharedOwner =
		layer === "team"
			? { layer }
			: layer === "project"
				? { layer, id: environment.projectId }
				: { layer, id: environment.id };
	return (
		<div className="flex min-w-0 flex-col gap-4">
			<Segmented
				aria-label="Which shared variables"
				font="sans"
				density="regular"
				value={layer}
				onValueChange={setLayer}
				options={[
					{ value: "environment", label: environment.name },
					{ value: "project", label: "Whole project" },
					{ value: "team", label: "Whole team" },
				]}
			/>
			<SharedLayer
				key={`${owner.layer}:${owner.id ?? ""}`}
				owner={owner}
				environment={environment}
			/>
		</div>
	);
}

function SharedLayer({
	owner,
	environment,
}: {
	owner: SharedOwner;
	environment: Environment;
}) {
	const project = owner.layer === "project";
	const team = owner.layer === "team";
	const projectList = useQuery({
		...projectSharedVariablesQuery(owner.id ?? ""),
		enabled: project,
	});
	const environmentList = useQuery({
		...environmentSharedVariablesQuery(owner.id ?? ""),
		enabled: owner.layer === "environment",
	});
	const teamList = useQuery({
		...teamSharedVariablesQuery(),
		enabled: team,
		retry: false,
	});
	const list = team ? teamList : project ? projectList : environmentList;
	const services = useServices(environment.id);
	const mutations = useSharedMutations(owner);
	const confirm = useConfirm();
	const [editing, setEditing] = useState<SharedVariableView | "new" | null>(
		null,
	);
	const [sharing, setSharing] = useState<string | null>(null);
	const [rotating, setRotating] = useState<string | null>(null);
	const ownerName = team
		? "every project of this team"
		: project
			? "every environment of this project"
			: environment.name;

	const rotation: RotationSource = {
		queryKey: ["shared-rotation-plan", owner.layer, owner.id ?? "", rotating],
		fetchPlan: async () => {
			const key = rotating ?? "";
			const { data } =
				owner.layer === "team"
					? await rotateTeamSharedVariable({
							path: { key },
							body: { dryRun: true },
						})
					: owner.layer === "project"
						? await rotateProjectSharedVariable({
								path: { projectId: owner.id, key },
								body: { dryRun: true },
							})
						: await rotateEnvironmentSharedVariable({
								path: { environmentId: owner.id, key },
								body: { dryRun: true },
							});
			return data;
		},
		stage: {
			mutate: (key, options) =>
				mutations.rotate.mutate({ key, dryRun: false }, options),
			isPending: mutations.rotate.isPending,
			isError: mutations.rotate.isError,
			error: mutations.rotate.error,
		},
	};

	if (list.isPending) {
		return (
			<div
				className="flex flex-col gap-2"
				role="status"
				aria-label="Loading shared variables"
			>
				<Skeleton className="h-10 w-full" />
				<Skeleton className="h-10 w-full" />
			</div>
		);
	}
	if (list.isError) {
		return (
			<ErrorState
				frame="raised"
				title="Shared variables did not load"
				body={errorMessage(list.error)}
				retry={() => void list.refetch()}
			/>
		);
	}
	const items = list.data;

	const remove = async (variable: SharedVariableView) => {
		const ok = await confirm({
			title: `Delete ${variable.key}?`,
			body:
				variable.usedBy > 0
					? `${variable.usedBy} variable${variable.usedBy === 1 ? "" : "s"} reference it. Services that resolved to it are restaged and will fail to deploy until the references are fixed${variable.layer === "environment" ? ", unless a project value of the same name exists" : ""}.`
					: "Nothing references it.",
			cta: "Delete variable",
			glyph: "trash",
			danger: true,
			hold: false,
			onConfirm: async () => {
				await mutations.remove.mutateAsync(variable.key);
			},
		});
		if (ok) toast.success(`Deleted ${variable.key}`);
	};

	return (
		<section aria-label="Shared variables" className="flex flex-col gap-3">
			{items.length > 0 ? (
				<div className="flex flex-wrap items-center justify-between gap-2">
					<p className="type-secondary">
						{items.filter((v) => !v.inherited).length} variable
						{items.filter((v) => !v.inherited).length === 1 ? "" : "s"} shared
						with {ownerName}.
					</p>
					<Button size="md" onClick={() => setEditing("new")}>
						<PlusIcon aria-hidden />
						New shared variable
					</Button>
				</div>
			) : null}
			{items.length === 0 ? (
				<EmptyState
					illustration="folder"
					frame="none"
					size="lg"
					title="No shared variables yet"
					body="Keep a value in one place, such as a session secret or an API host, and reference it from several services."
					action={
						<Button size="md" onClick={() => setEditing("new")}>
							<PlusIcon aria-hidden />
							New shared variable
						</Button>
					}
				/>
			) : (
				<ul
					aria-label="Shared variables"
					className="surface-raised divide-y divide-line-faint rounded-card"
				>
					{items.map((variable) => (
						<li
							key={`${variable.layer}:${variable.key}`}
							className={cn(
								"grid grid-cols-[minmax(0,12rem)_minmax(0,1fr)_auto] items-center gap-x-3 px-3 py-2 font-medium font-mono text-[11.5px]",
							)}
						>
							<span title={variable.key} className="truncate text-ink-2">
								{variable.key}
							</span>
							<span className="flex min-w-0 flex-col gap-1 text-ink-3">
								<span className="flex min-w-0 items-center gap-2">
									{variable.isSecret ? (
										<>
											<span aria-hidden>{SECRET_MASK}</span>
											<span className="sr-only">Secret value, hidden</span>
										</>
									) : (
										<span className="truncate" title={variable.value ?? ""}>
											{variable.value || (
												<span className="text-ink-4">empty</span>
											)}
										</span>
									)}
								</span>
								<span className="flex flex-wrap items-center gap-1 font-sans font-normal">
									{variable.isSecret ? (
										<Badge>
											<LockIcon aria-hidden />
											secret
										</Badge>
									) : variable.computedSecret ? (
										<Badge title="Contains a secret through a reference">
											contains a secret
										</Badge>
									) : null}
									{variable.source === "generated" ? (
										<Badge title="Generated once on the server">
											generated
											{(variable.generated ?? []).length > 0
												? ` · ${(variable.generated ?? []).map((g) => `${g.fn}(${(g.args ?? []).join(", ")})`).join(" + ")}`
												: ""}
										</Badge>
									) : null}
									{variable.inherited ? <Badge>from project</Badge> : null}
									{variable.overrides ? (
										<Badge
											tone="warn"
											title="Replaces the project value in this environment"
										>
											overrides project
										</Badge>
									) : null}
									<Badge title="Variables that reference it in this environment">
										used by {variable.usedBy}
									</Badge>
									{variable.comment ? (
										<span className="text-[11px] text-ink-4">
											{variable.comment}
										</span>
									) : null}
								</span>
							</span>
							<DropdownMenu>
								<DropdownMenuTrigger
									render={
										<Button
											variant="ghost"
											size="icon-sm"
											aria-label={`Actions for ${variable.key}`}
										/>
									}
								>
									<EllipsisVerticalIcon aria-hidden />
								</DropdownMenuTrigger>
								<DropdownMenuContent align="end" className="w-[210px]">
									<DropdownMenuItem
										render={<button type="button" />}
										disabled={variable.inherited}
										onClick={() => setEditing(variable)}
									>
										<PencilIcon aria-hidden />
										Edit
									</DropdownMenuItem>
									<DropdownMenuItem
										render={<button type="button" />}
										onClick={() => setSharing(variable.key)}
									>
										<Share2Icon aria-hidden />
										Share to services…
									</DropdownMenuItem>
									{variable.source === "generated" && !variable.inherited ? (
										<DropdownMenuItem
											render={<button type="button" />}
											onClick={() => setRotating(variable.key)}
										>
											<RefreshCwIcon aria-hidden />
											Rotate…
										</DropdownMenuItem>
									) : null}
									<DropdownMenuSeparator />
									<DropdownMenuItem
										render={<button type="button" />}
										variant="destructive"
										disabled={variable.inherited}
										onClick={() => void remove(variable)}
									>
										<Trash2Icon aria-hidden />
										Delete
									</DropdownMenuItem>
								</DropdownMenuContent>
							</DropdownMenu>
						</li>
					))}
				</ul>
			)}
			<SharedVariableDialog
				owner={owner}
				ownerName={ownerName}
				target={editing}
				siblings={items}
				onClose={() => setEditing(null)}
			/>
			<ShareDialog
				owner={owner}
				variableKey={sharing}
				services={services.data ?? []}
				onClose={() => setSharing(null)}
			/>
			<RotatePlanDialog
				source={rotation}
				variableKey={rotating}
				onClose={() => setRotating(null)}
			/>
		</section>
	);
}
