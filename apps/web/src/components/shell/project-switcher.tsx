import { useQuery } from "@tanstack/react-query";
import { useNavigate, useRouterState } from "@tanstack/react-router";
import { CheckIcon, PlusIcon } from "lucide-react";
import { projectsQuery } from "#/api/hooks";
import { envSection } from "#/components/project/section";
import { useOpenProject } from "#/components/project/use-open-project";
import {
	DropdownMenu,
	DropdownMenuContent,
	DropdownMenuGroup,
	DropdownMenuItem,
	DropdownMenuLabel,
	DropdownMenuSeparator,
	DropdownMenuTrigger,
} from "#/components/ui/dropdown-menu";
import { MenuCaret } from "./menu-caret";
import { useShellActions } from "./shell-actions";
import type { ProjectContext } from "./use-project-context";

const TRIGGER =
	"surface-raised pressable flex h-[30px] min-w-0 shrink cursor-pointer items-center gap-2 whitespace-nowrap rounded-ctl px-2.5 font-medium text-[13px] text-ink-1 leading-none outline-none";

/** The project crumb in the top bar: a menu of every project, plus "New project". */
export function ProjectSwitcher({ context }: { context: ProjectContext }) {
	const navigate = useNavigate();
	const projects = useQuery(projectsQuery());
	const { openNewProject } = useShellActions();

	const openProject = useOpenProject();
	// Stay on the page being read (Observability, Deployments, …).
	const section = useRouterState({
		select: (state) => envSection(state.location.pathname),
	});
	const open = (projectId: string) => {
		if (projectId !== context.projectId) void openProject(projectId, section);
	};

	return (
		<DropdownMenu>
			<DropdownMenuTrigger
				data-nomorph
				aria-label={
					context.project
						? `Project: ${context.project.name}. Switch project`
						: "Switch project"
				}
				className={`${TRIGGER} @max-[759px]:gap-0 @max-[759px]:px-[5px]`}
			>
				<span
					aria-hidden
					className="grid size-[18px] shrink-0 place-items-center rounded-[5px] bg-linear-to-b from-[#7b79ff] to-[#4a47d8] font-semibold text-[10px] text-white uppercase leading-none shadow-[inset_0_1px_0_rgb(255_255_255/.38),0_0_0_1px_rgb(2_4_10/.6)]"
				>
					{(context.project?.name ?? "·").slice(0, 1)}
				</span>
				{/* Under 640px the tile alone stands for the project. */}
				<span className="max-w-[180px] truncate @max-[759px]:hidden">
					{context.project?.name ?? "…"}
				</span>
				<MenuCaret className="@max-[759px]:hidden" />
			</DropdownMenuTrigger>
			<DropdownMenuContent align="start" sideOffset={6} className="w-[260px]">
				<DropdownMenuGroup>
					<DropdownMenuLabel>Projects</DropdownMenuLabel>
					{(projects.data ?? []).map((project) => (
						<DropdownMenuItem
							render={<button type="button" />}
							key={project.id}
							aria-current={
								project.id === context.projectId ? "true" : undefined
							}
							className="gap-2.5"
							onClick={() => open(project.id)}
						>
							<span className="min-w-0 flex-1 truncate">{project.name}</span>
							{project.id === context.projectId ? (
								<CheckIcon aria-hidden className="size-3 text-primary" />
							) : (
								<span className="font-mono text-2xs text-subtle leading-none">
									{project.slug}
								</span>
							)}
						</DropdownMenuItem>
					))}
				</DropdownMenuGroup>
				<DropdownMenuSeparator />
				<DropdownMenuGroup>
					<DropdownMenuItem
						render={<button type="button" />}
						className="gap-2.5"
						onClick={() => navigate({ to: "/" })}
					>
						All projects
					</DropdownMenuItem>
					<DropdownMenuItem
						render={<button type="button" />}
						className="gap-2.5"
						onClick={openNewProject}
					>
						<PlusIcon />
						New project
					</DropdownMenuItem>
				</DropdownMenuGroup>
			</DropdownMenuContent>
		</DropdownMenu>
	);
}
