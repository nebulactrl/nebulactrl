import { useQuery } from "@tanstack/react-query";
import { Link, useLocation } from "@tanstack/react-router";
import { type FormEvent, useEffect, useState } from "react";
import { errorMessage } from "#/api/client-config";
import { environmentsQuery, meQuery, projectsQuery } from "#/api/hooks";
import { type Project, productionFirst } from "#/api/models";
import { CopyButton } from "#/components/common/copy-button";
import { ErrorState } from "#/components/common/error-state";
import { PageFrame } from "#/components/shell/page-frame";
import { Badge } from "#/components/ui/badge";
import { Button } from "#/components/ui/button";
import { Card, CardFooter } from "#/components/ui/card";
import { Field, FieldError, FieldLabel } from "#/components/ui/field";
import { Input } from "#/components/ui/input";
import { Skeleton, SkeletonReveal } from "#/components/ui/skeleton";
import { Spinner } from "#/components/ui/spinner";
import { Textarea } from "#/components/ui/textarea";
import { toast } from "#/components/ui/toast";
import { ProjectBuildLocation } from "#/features/build-placement/project-build-location";
import { pagesFor, type SettingsPage } from "#/features/settings/nav-model";
import { SettingsShell } from "#/features/settings/settings-shell";
import { isRole } from "#/features/teams/roles";
import { useDocumentTitle } from "#/lib/hooks/use-document-title";
import { useDeleteProject } from "./delete-project-dialog";
import { useUpdateProject } from "./use-update-project";

/** A read-only fact with a copy button: the slug and the ID. */
function CopiedFact({
	label,
	value,
	copyLabel,
}: {
	label: string;
	value: string;
	copyLabel: string;
}) {
	return (
		<span className="inline-flex items-center gap-1.5">
			{label}
			<code className="select-all font-mono text-ink-2">{value}</code>
			<CopyButton
				value={value}
				label={copyLabel}
				variant="ghost"
				size="icon-xs"
			/>
		</span>
	);
}

function GeneralSection({ project }: { project: Project }) {
	const [name, setName] = useState(project.name);
	const [description, setDescription] = useState(project.description);
	const save = useUpdateProject(project, () => toast.success("Project saved"));

	// Someone else's save (or ours, once refetched) replaces the form.
	useEffect(() => {
		setName(project.name);
		setDescription(project.description);
	}, [project.name, project.description]);

	const trimmed = name.trim();
	const dirty =
		trimmed !== "" &&
		(trimmed !== project.name || description !== project.description);
	const submit = (event: FormEvent) => {
		event.preventDefault();
		if (dirty && !save.isPending) save.mutate({ name: trimmed, description });
	};

	return (
		<div className="flex max-w-[560px] flex-col gap-5">
			<Card>
				<form onSubmit={submit} className="contents">
					<div className="flex flex-col gap-4 px-5 py-5">
						<Field>
							<FieldLabel htmlFor="project-name">Name</FieldLabel>
							<Input
								id="project-name"
								scale="lg"
								autoComplete="off"
								value={name}
								onChange={(event) => setName(event.target.value)}
							/>
						</Field>
						<Field>
							<FieldLabel htmlFor="project-description">Description</FieldLabel>
							<Textarea
								id="project-description"
								rows={3}
								value={description}
								onChange={(event) => setDescription(event.target.value)}
							/>
						</Field>
						{save.isError ? (
							<FieldError>{errorMessage(save.error)}</FieldError>
						) : null}
					</div>
					<CardFooter>
						<Button
							type="submit"
							variant={dirty ? "primary" : "secondary"}
							className="disabled:bg-transparent disabled:text-ink-3 disabled:opacity-100 disabled:shadow-none"
							disabled={!dirty || save.isPending}
						>
							{save.isPending ? <Spinner /> : null}
							Save changes
						</Button>
					</CardFooter>
				</form>
			</Card>
			<p className="flex flex-wrap items-center gap-x-5 gap-y-1 text-ink-3 text-xs">
				<CopiedFact label="Slug" value={project.slug} copyLabel="Copy slug" />
				<CopiedFact
					label="Project ID"
					value={project.id}
					copyLabel="Copy project ID"
				/>
			</p>
		</div>
	);
}

function EnvironmentsSection({ project }: { project: Project }) {
	const environments = useQuery(environmentsQuery(project.id));
	if (environments.isPending) {
		return (
			<div role="status" aria-label="Loading environments">
				<Skeleton className="h-32 max-w-[560px] rounded-card" />
			</div>
		);
	}
	if (environments.isError && environments.data === undefined) {
		return (
			<ErrorState
				frame="raised"
				title="Environments did not load"
				body={errorMessage(environments.error)}
				retry={() => void environments.refetch()}
			/>
		);
	}
	// Previews belong to Previews; this lists the project's own environments.
	const own = productionFirst(
		(environments.data ?? []).filter(
			(candidate) => candidate.kind !== "preview",
		),
	);
	return (
		<Card className="max-w-[560px]">
			<ul className="divide-y divide-line">
				{own.map((environment) => (
					<li key={environment.id}>
						<Link
							to="/p/$projectId/$envSlug"
							params={{ projectId: project.id, envSlug: environment.slug }}
							className="flex min-h-12 items-center gap-3 px-5 py-3 text-ink-1 outline-none hover:bg-white/4 hover:text-ink-1 focus-visible:focus-ring"
						>
							<span className="min-w-0 truncate font-medium text-sm">
								{environment.name}
							</span>
							{environment.isProduction ? (
								<Badge variant="accent">Production</Badge>
							) : null}
							<span className="ml-auto truncate font-mono text-ink-4 text-xs">
								{environment.slug}
							</span>
						</Link>
					</li>
				))}
			</ul>
		</Card>
	);
}

/**
 * Deleting the project: a row of its own below everything else, with its
 * own words, so it is never read as part of editing.
 */
function DangerSection({ project }: { project: Project }) {
	const { ask, dialog } = useDeleteProject();
	return (
		<>
			<section
				aria-labelledby="delete-project-heading"
				className="flex max-w-[560px] flex-wrap items-center justify-between gap-4 rounded-card border border-destructive-border p-4"
			>
				<div className="flex min-w-0 flex-1 flex-col gap-1">
					<h3
						id="delete-project-heading"
						className="font-medium text-destructive-text text-sm"
					>
						Delete this project
					</h3>
					<p className="text-pretty text-ink-3 text-xs leading-relaxed">
						Deletes {project.name} with its environments, services, databases,
						volumes, domains and previews, and stops their containers. Backups
						already written to your destinations are kept. You will see what
						goes and be asked to type the project's name.
					</p>
				</div>
				<Button variant="danger" onClick={() => ask(project)}>
					Delete project
				</Button>
			</section>
			{dialog}
		</>
	);
}

/** The section the address names (`#environments`), else the first one. */
function activeSection(hash: string, pages: readonly SettingsPage[]): string {
	return pages.find((page) => page.section === hash)?.section ?? "general";
}

function SettingsBody({ project }: { project: Project }) {
	const me = useQuery(meQuery());
	const hash = useLocation({ select: (location) => location.hash });
	const role = me.data?.principal.role;
	const pages = pagesFor(
		"project",
		{ instanceAdmin: me.data?.instanceAdmin === true },
		isRole(role) ? role : undefined,
	);
	const active = activeSection(hash, pages);

	return (
		<SettingsShell
			area="project"
			title="Project settings"
			scopeLine={`Project · ${project.name} · applies to all its environments`}
			pages={pages}
			active={active}
			projectId={project.id}
		>
			{active === "general" ? <GeneralSection project={project} /> : null}
			{active === "environments" ? (
				<EnvironmentsSection project={project} />
			) : null}
			{active === "build-location" ? (
				<ProjectBuildLocation projectId={project.id} />
			) : null}
			{active === "danger" ? <DangerSection project={project} /> : null}
		</SettingsShell>
	);
}

/**
 * A project's settings: its name and description, its environments, where it
 * builds, and deleting it. The page lives outside any one environment, so it
 * reads the project from the list the rest of the console already holds.
 */
export function ProjectSettingsPage({ projectId }: { projectId: string }) {
	const projects = useQuery(projectsQuery());
	const project = projects.data?.find(
		(candidate) => candidate.id === projectId,
	);
	useDocumentTitle(["Project settings", project?.name]);

	if (project !== undefined) return <SettingsBody project={project} />;
	return (
		<PageFrame page="project-settings" title="Project settings">
			<SkeletonReveal
				loading={projects.data === undefined && !projects.isError}
				skeleton={<Skeleton className="h-64 rounded-card" />}
			>
				{() =>
					projects.data === undefined ? (
						<ErrorState
							frame="raised"
							title="Couldn't load the project"
							body={errorMessage(projects.error)}
							retry={() => void projects.refetch()}
						/>
					) : (
						<ErrorState
							frame="raised"
							title="Project not found"
							body="This project does not exist, or you cannot see it. It may have been deleted."
						/>
					)
				}
			</SkeletonReveal>
		</PageFrame>
	);
}
