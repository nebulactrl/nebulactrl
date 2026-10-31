import { useQueries, useQuery } from "@tanstack/react-query";
import { Link, useNavigate } from "@tanstack/react-router";
import { ArrowUpRightIcon, EllipsisVerticalIcon } from "lucide-react";
import { memo } from "react";
import { environmentsQuery, servicesQuery } from "#/api/hooks";
import { type Project, productionFirst } from "#/api/models";
import { copyWithToast } from "#/components/common/copy-button";
import { Relative } from "#/components/common/relative";
import {
	ContextMenu,
	ContextMenuContent,
	ContextMenuTrigger,
} from "#/components/motion/context-menu";
import { Button } from "#/components/ui/button";
import {
	DropdownMenu,
	DropdownMenuContent,
	DropdownMenuTrigger,
} from "#/components/ui/dropdown-menu";
import { statusKindOfTone } from "#/components/ui/lamp";
import { Skeleton } from "#/components/ui/skeleton";
import { StatusIcon } from "#/components/ui/status-icon";
import {
	type CardAction,
	CardContextRows,
	CardDropdownRows,
} from "#/features/projects/card-menu";
import { useDeleteProject } from "#/features/projects/delete-project-dialog";
import { vt } from "#/lib/transitions";
import { projectDestination } from "./destination";
import { lastDeployAt, serviceNoun, summarizeEnvironment } from "./summary";
import { useOpenProject } from "./use-open-project";

/** How many environments a card lists before folding the rest into "+N more". */
const MAX_ENVIRONMENTS = 3;

/** The card's face: a link to the project's canvas. */
function ProjectCardLink({ project }: { project: Project }) {
	const environments = useQuery(environmentsQuery(project.id));
	const ordered = productionFirst(environments.data ?? []);
	const serviceLists = useQueries({
		queries: ordered.map((environment) => servicesQuery(environment.id)),
	});
	// Every project is created with a production environment, so the slug is
	// a safe landing before the list has answered.
	const destination = projectDestination(
		project.id,
		ordered[0]?.slug ?? "production",
	);

	const loading =
		environments.isPending || serviceLists.some((list) => list.isPending);
	const summaries = ordered.map((environment, index) =>
		summarizeEnvironment(environment, serviceLists[index]?.data ?? []),
	);
	const allServices = serviceLists.flatMap((list) => list.data ?? []);
	const total = summaries.reduce((sum, item) => sum + item.serviceCount, 0);
	const lastDeploy = lastDeployAt(allServices);
	const shown = summaries.slice(0, MAX_ENVIRONMENTS);
	const hidden = summaries.length - shown.length;

	return (
		<Link
			{...destination}
			{...vt("project", project.id)}
			className="group/project surface-raised flex min-h-[176px] w-full min-w-0 flex-1 flex-col gap-3.5 overflow-hidden rounded-card p-[18px] text-ink-1 transition-[border-color] duration-quick ease-out hover:border-line-strong hover:text-ink-1 focus-visible:focus-ring"
		>
			<span className="flex items-start justify-between gap-3">
				<span className="flex min-w-0 flex-col gap-1.5">
					<span
						title={project.name}
						className="truncate font-display font-semibold text-[17px] leading-[1.15] tracking-[-0.02em]"
					>
						{project.name}
					</span>
					<span className="truncate font-mono text-ink-4 text-xs leading-none">
						{project.slug}
					</span>
				</span>
				<ArrowUpRightIcon
					aria-hidden
					className="mt-0.5 mr-7 size-4 shrink-0 text-ink-4 transition-[color] duration-quick ease-out group-hover/project:text-ink-2"
				/>
			</span>
			{project.description ? (
				<span className="line-clamp-2 text-pretty text-ink-3 text-[13px] leading-[1.4]">
					{project.description}
				</span>
			) : null}
			<span className="surface-well mt-auto flex flex-col gap-2 rounded-ctl px-3 py-2.5">
				{loading ? (
					<>
						<Skeleton className="h-3.5 w-3/4" />
						<Skeleton className="h-3.5 w-1/2" />
					</>
				) : (
					shown.map((summary) => (
						<span
							key={summary.id}
							className="flex min-w-0 items-center gap-2 text-xs leading-none"
						>
							<StatusIcon kind={statusKindOfTone(summary.tone)} label="" />
							<span className="truncate font-medium font-mono text-ink-1">
								{summary.slug}
							</span>
							<span className="ml-auto shrink-0 text-ink-3">
								{summary.label}
							</span>
						</span>
					))
				)}
				{hidden > 0 ? (
					<span className="text-ink-4 text-xs leading-none">
						+{hidden} more {hidden === 1 ? "environment" : "environments"}
					</span>
				) : null}
			</span>
			<span className="flex items-end justify-between gap-3">
				<span className="flex flex-col gap-1.5">
					<span className="type-metric text-ink-1 leading-none">
						{loading ? "\u00a0" : total}
					</span>
					<span className="type-micro">
						{loading ? "\u00a0" : serviceNoun(total)}
					</span>
				</span>
				<span className="flex flex-col items-end gap-1.5">
					<span className="font-mono text-ink-2 text-sm leading-none">
						{loading ? (
							"\u00a0"
						) : lastDeploy ? (
							<Relative iso={lastDeploy} />
						) : (
							"never"
						)}
					</span>
					<span className="type-micro">last deploy</span>
				</span>
			</span>
		</Link>
	);
}

/**
 * One project in the home grid: name, slug, description, and each
 * environment as a status icon with its state in words. The footer says how many
 * services the project runs and when anything last deployed. The whole card
 * leads to the project's canvas on its first environment (production first)
 * and is the shared element of the card-to-canvas view transition (and the
 * way back: the canvas shrinks into this card). Its `⋯` button and a
 * right-click (or the context-menu key, or Shift+F10, on the focused card)
 * open the same actions.
 */
export const ProjectCard = memo(function ProjectCard({
	project,
}: {
	project: Project;
}) {
	const navigate = useNavigate();
	const openProject = useOpenProject();
	const { ask, dialog } = useDeleteProject();

	const run = (action: CardAction) => {
		switch (action) {
			case "open":
				void openProject(project.id);
				break;
			case "settings":
				void navigate({
					to: "/p/$projectId/settings",
					params: { projectId: project.id },
				});
				break;
			case "copy-id":
				void copyWithToast(project.id);
				break;
			case "delete":
				ask(project);
				break;
		}
	};

	return (
		<ContextMenu>
			{/* A right-click or key inside a menu's own popup belongs to that menu. */}
			<ContextMenuTrigger
				ignore={(target) => target.closest('[role="menu"]') !== null}
			>
				<div className="group/card relative flex">
					<ProjectCardLink project={project} />
					<DropdownMenu>
						<DropdownMenuTrigger
							render={
								<Button
									variant="ghost"
									size="icon-sm"
									aria-label={`Actions for ${project.name}`}
									// Quiet until the card is looked at; always there on touch.
									className="absolute top-3 right-3 opacity-0 transition-opacity duration-quick ease-out focus-visible:opacity-100 group-focus-within/card:opacity-100 group-hover/card:opacity-100 data-popup-open:opacity-100 pointer-coarse:opacity-100"
								/>
							}
						>
							<EllipsisVerticalIcon aria-hidden />
						</DropdownMenuTrigger>
						<DropdownMenuContent align="end" className="w-[210px]">
							<CardDropdownRows run={run} />
						</DropdownMenuContent>
					</DropdownMenu>
				</div>
			</ContextMenuTrigger>
			<ContextMenuContent ariaLabel={`Actions for ${project.name}`}>
				<CardContextRows run={run} />
			</ContextMenuContent>
			{dialog}
		</ContextMenu>
	);
});
