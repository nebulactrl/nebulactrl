import { useNavigate, useRouterState } from "@tanstack/react-router";
import {
	CheckIcon,
	CopyPlusIcon,
	FileCogIcon,
	FileDiffIcon,
	FileDownIcon,
	GitPullRequestIcon,
	PlusIcon,
} from "lucide-react";
import { Fragment, useMemo, useState } from "react";
import type { Environment } from "#/api/models";
import { NewEnvironmentDialog } from "#/components/project/new-environment-dialog";
import { envSection, envSectionRoute } from "#/components/project/section";
import { Badge } from "#/components/ui/badge";
import {
	DropdownMenu,
	DropdownMenuContent,
	DropdownMenuGroup,
	DropdownMenuItem,
	DropdownMenuLabel,
	DropdownMenuSeparator,
	DropdownMenuTrigger,
} from "#/components/ui/dropdown-menu";
import { StatusIcon, type StatusKind } from "#/components/ui/status-icon";
import { CloneEnvironmentDialog } from "#/features/clone";
import {
	groupEnvironments,
	type PreviewStatus,
	previewLabel,
	STATUS_LABEL,
} from "#/features/previews/model";
import { usePreviews } from "#/features/previews/use-previews";
import {
	openProjectConfig,
	ProjectConfigHost,
} from "#/features/project-config";
import { MenuCaret } from "./menu-caret";
import type { ProjectContext } from "./use-project-context";

/** The icon kind of each preview status; the name beside it says it in words. */
const PREVIEW_KIND: Record<PreviewStatus, StatusKind> = {
	creating: "building",
	deploying: "deploying",
	seeding: "deploying",
	ready: "live",
	failed: "failed",
	paused: "stopped",
	deleting: "offline",
};

/** A preview's status icon: an environment is only as useful as it is running. */
function PreviewStatusIcon({ status }: { status: PreviewStatus | undefined }) {
	if (status === undefined)
		return (
			<GitPullRequestIcon aria-hidden className="size-3 shrink-0 text-ink-3" />
		);
	return (
		<StatusIcon kind={PREVIEW_KIND[status]} label={STATUS_LABEL[status]} />
	);
}

/**
 * The environment crumb in the top bar; a production environment carries a
 * "Production" text badge. Pull-request previews are listed under the
 * environment they were cloned from, labelled `PR #12 · title`, with their
 * status.
 */
export function EnvironmentSwitcher({ context }: { context: ProjectContext }) {
	const navigate = useNavigate();
	const current = context.environment;
	const section = useRouterState({
		select: (state) => envSection(state.location.pathname),
	});
	const previews = usePreviews(context.projectId);
	const [creating, setCreating] = useState(false);
	const [cloning, setCloning] = useState(false);
	const statusByEnvironment = useMemo(
		() =>
			new Map((previews.data ?? []).map((p) => [p.environmentId, p.status])),
		[previews.data],
	);
	const { groups, detached } = useMemo(
		() => groupEnvironments(context.environments),
		[context.environments],
	);

	const go = (environment: Environment) => {
		if (environment.id === current?.id) return;
		// Stay on the page being read: /p/<id>/<env>/<section>.
		void navigate({
			to: envSectionRoute(section),
			params: { projectId: context.projectId, envSlug: environment.slug },
		});
	};

	const previewItem = (environment: Environment) => (
		<DropdownMenuItem
			render={<button type="button" />}
			key={environment.id}
			aria-current={environment.id === current?.id ? "true" : undefined}
			className="gap-2.5 pl-6"
			onClick={() => go(environment)}
		>
			<PreviewStatusIcon status={statusByEnvironment.get(environment.id)} />
			<span className="min-w-0 flex-1 truncate">
				{previewLabel(environment)}
			</span>
			{environment.id === current?.id ? (
				<CheckIcon aria-hidden className="size-3 text-accent-ink" />
			) : statusByEnvironment.get(environment.id) === "paused" ? (
				<span className="font-mono text-2xs text-ink-4 leading-none">
					paused
				</span>
			) : null}
		</DropdownMenuItem>
	);

	const nameSaysProduction = /prod/i.test(current?.name ?? "");

	return (
		<>
			<DropdownMenu>
				<DropdownMenuTrigger
					data-nomorph
					className="surface-raised pressable flex h-[30px] min-w-0 shrink cursor-pointer items-center gap-2 whitespace-nowrap rounded-ctl px-2.5 font-medium text-[13px] text-ink-1 leading-none outline-none @max-[759px]:gap-1.5 @max-[759px]:px-2"
				>
					{current?.kind === "preview" ? (
						<PreviewStatusIcon status={statusByEnvironment.get(current.id)} />
					) : null}
					<span className="min-w-0 max-w-[160px] truncate @max-[759px]:max-w-[88px]">
						{current?.name ?? context.envSlug ?? "…"}
					</span>
					{current?.isProduction && current.kind !== "preview" ? (
						// A text marker, not a colour: production is the guarded environment.
						// Under 640px a name that already says so needs no second word.
						<Badge
							variant="accent"
							className={nameSaysProduction ? "@max-[759px]:hidden" : undefined}
						>
							Production
						</Badge>
					) : null}
					<MenuCaret />
				</DropdownMenuTrigger>
				<DropdownMenuContent align="start" sideOffset={6} className="w-[260px]">
					<DropdownMenuGroup>
						<DropdownMenuLabel>Environments</DropdownMenuLabel>
						{groups.map(({ base, previews: nested }) => (
							<Fragment key={base.id}>
								<DropdownMenuItem
									render={<button type="button" />}
									aria-current={base.id === current?.id ? "true" : undefined}
									className="gap-2.5"
									onClick={() => go(base)}
								>
									<span className="min-w-0 flex-1 truncate">{base.name}</span>
									{base.isProduction ? (
										<Badge variant="accent">Production</Badge>
									) : null}
									{base.id === current?.id ? (
										<CheckIcon aria-hidden className="size-3 text-accent-ink" />
									) : null}
								</DropdownMenuItem>
								{nested.map(previewItem)}
							</Fragment>
						))}
						{detached.length > 0 ? (
							<>
								<DropdownMenuLabel>Previews</DropdownMenuLabel>
								{detached.map(previewItem)}
							</>
						) : null}
					</DropdownMenuGroup>
					<DropdownMenuSeparator />
					<DropdownMenuGroup>
						<DropdownMenuItem
							render={<button type="button" />}
							className="gap-2.5"
							onClick={() => setCreating(true)}
						>
							<PlusIcon />
							New environment
						</DropdownMenuItem>
						{current && current.kind !== "preview" ? (
							<DropdownMenuItem
								render={<button type="button" />}
								className="gap-2.5"
								onClick={() => setCloning(true)}
							>
								<CopyPlusIcon />
								Clone environment…
							</DropdownMenuItem>
						) : null}
						{current && current.kind !== "preview" ? (
							<>
								<DropdownMenuSeparator />
								<DropdownMenuLabel>nebula.toml</DropdownMenuLabel>
								<DropdownMenuItem
									render={<button type="button" />}
									className="gap-2.5"
									onClick={() => openProjectConfig("review")}
								>
									<FileDiffIcon />
									Review nebula.toml…
								</DropdownMenuItem>
								<DropdownMenuItem
									render={<button type="button" />}
									className="gap-2.5"
									onClick={() => openProjectConfig("export")}
								>
									<FileDownIcon />
									Export nebula.toml…
								</DropdownMenuItem>
								<DropdownMenuItem
									render={<button type="button" />}
									className="gap-2.5"
									onClick={() => openProjectConfig("source")}
								>
									<FileCogIcon />
									Config from git…
								</DropdownMenuItem>
							</>
						) : null}
					</DropdownMenuGroup>
				</DropdownMenuContent>
			</DropdownMenu>
			<NewEnvironmentDialog
				projectId={context.projectId}
				open={creating}
				onOpenChange={setCreating}
			/>
			{current && current.kind !== "preview" ? (
				<CloneEnvironmentDialog
					environment={current}
					open={cloning}
					onOpenChange={setCloning}
				/>
			) : null}
			{current ? <ProjectConfigHost environment={current} /> : null}
		</>
	);
}
