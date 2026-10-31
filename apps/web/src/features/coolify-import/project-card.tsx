import { useId } from "react";
import type {
	CoolifyPlan,
	CoolifyPlanProject,
	Project,
} from "#/api/gen/types.gen";
import { Badge } from "#/components/ui/badge";
import { CheckLabel } from "#/components/ui/check-label";
import { Input } from "#/components/ui/input";
import {
	Select,
	SelectContent,
	SelectItem,
	SelectTrigger,
	SelectValue,
} from "#/components/ui/select";
import {
	type Baseline,
	collisionHint,
	coolifyProjectName,
	type ImportDraft,
	isProjectSkipped,
	patchProject,
	rewritesFor,
} from "./lib";
import { ResourceCard } from "./resource-card";

const NEW_PROJECT = "new";

const ACTION_LABEL = {
	create: "new project",
	existing: "merged into an existing project",
	skip: "skipped",
} as const;

/**
 * One Coolify project: whether to import it, what the NebulaCtrl project is
 * called or which existing one takes it, and under it each environment with
 * its applications and databases.
 */
export function ProjectCard({
	project,
	plan,
	draft,
	baseline,
	existingProjects,
	onDraft,
}: {
	project: CoolifyPlanProject;
	plan: CoolifyPlan;
	draft: ImportDraft;
	baseline: Baseline | undefined;
	existingProjects: readonly Project[];
	onDraft(update: (current: ImportDraft) => ImportDraft): void;
}) {
	const id = useId();
	const mine = draft.projects[project.uuid];
	const skipped = isProjectSkipped(project, draft);
	const merged = mine?.existingProjectId ?? "";
	const hint = collisionHint(project, mine);
	const coolifyName = coolifyProjectName(baseline, project);
	const patch = (change: Parameters<typeof patchProject>[2]) =>
		onDraft((current) => patchProject(current, project.uuid, change));
	const action = skipped ? "skip" : project.action;

	return (
		<li
			data-slot="coolify-project-card"
			data-action={action}
			className="surface-raised flex flex-col gap-3 rounded-card border border-line px-4 py-3.5"
		>
			<div className="flex flex-wrap items-center gap-x-3 gap-y-2">
				<CheckLabel
					checked={!skipped}
					onCheckedChange={(next) => patch({ skip: next ? undefined : true })}
					className="min-w-0 flex-1 gap-2.5"
					labelClassName="flex flex-wrap items-center gap-x-2 gap-y-1"
				>
					<span className="font-medium text-[14px] text-ink-1">
						{coolifyName}
					</span>
					<span className="font-mono text-2xs text-ink-3">
						{skipped ? "not imported" : `slug ${project.slug}`}
					</span>
					<Badge variant={skipped ? "default" : "accent"}>
						{ACTION_LABEL[action]}
					</Badge>
				</CheckLabel>
			</div>

			{skipped ? (
				<p className="m-0 text-[12px] text-ink-3 leading-[1.5]">
					Nothing from this project is imported. Tick it to bring it back.
				</p>
			) : (
				<>
					<div className="flex flex-wrap items-end gap-3">
						<div className="flex w-64 max-w-full flex-col gap-1">
							<span id={`${id}-into`} className="type-label">
								Import into
							</span>
							<Select
								value={merged === "" ? NEW_PROJECT : merged}
								items={[
									{ value: NEW_PROJECT, label: "A new project" },
									...existingProjects.map((p) => ({
										value: p.id,
										label: p.name,
									})),
								]}
								onValueChange={(next) => {
									const value = String(next ?? NEW_PROJECT);
									patch(
										value === NEW_PROJECT
											? { existingProjectId: undefined }
											: { existingProjectId: value, name: undefined },
									);
								}}
							>
								<SelectTrigger
									aria-labelledby={`${id}-into`}
									size="sm"
									className="w-full"
								>
									<SelectValue />
								</SelectTrigger>
								<SelectContent alignItemWithTrigger={false}>
									<SelectItem value={NEW_PROJECT}>A new project</SelectItem>
									{existingProjects.map((p) => (
										<SelectItem key={p.id} value={p.id}>
											{p.name}
										</SelectItem>
									))}
								</SelectContent>
							</Select>
						</div>
						{merged === "" ? (
							<div className="flex min-w-48 flex-1 flex-col gap-1">
								<label htmlFor={`${id}-name`} className="type-label">
									New project name
								</label>
								<Input
									id={`${id}-name`}
									autoComplete="off"
									spellCheck={false}
									value={mine?.name ?? project.name}
									onChange={(event) => patch({ name: event.target.value })}
									className="h-7 max-w-sm"
								/>
							</div>
						) : (
							<p className="m-0 max-w-sm pb-1 text-[12px] text-ink-3 leading-[1.5]">
								Environments with the same name are reused; the others are
								created.
							</p>
						)}
					</div>
					{hint ? (
						<p role="note" className="m-0 text-[12px] text-ink-2 leading-[1.5]">
							{hint}
						</p>
					) : null}

					{(project.environments ?? []).map((environment) => (
						<section
							key={environment.slug}
							aria-label={`Environment ${environment.name}`}
							className="flex flex-col gap-2 border-line-faint border-t pt-3"
						>
							<div className="flex flex-wrap items-center gap-x-2 gap-y-1">
								<h4 className="type-label m-0">Environment</h4>
								<span className="font-medium text-[13px] text-ink-1">
									{environment.name}
								</span>
								<span className="font-mono text-2xs text-ink-3">
									{environment.slug}
								</span>
								<Badge>
									{environment.action === "existing" ? "reused" : "new"}
								</Badge>
								{environment.production ? (
									<Badge variant="accent">production</Badge>
								) : null}
							</div>
							{(environment.resources ?? []).length === 0 ? (
								<p className="m-0 text-[12px] text-ink-3">
									No application or database in this environment.
								</p>
							) : (
								<ul className="m-0 flex list-none flex-col gap-2 p-0">
									{(environment.resources ?? []).map((resource) => (
										<ResourceCard
											key={resource.uuid}
											resource={resource}
											draft={draft.resources[resource.uuid]}
											rewrites={rewritesFor(plan.rewrites, resource.name)}
											baseline={baseline}
											onDraft={onDraft}
										/>
									))}
								</ul>
							)}
						</section>
					))}
				</>
			)}
		</li>
	);
}
