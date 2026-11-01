import { useQuery } from "@tanstack/react-query";
import { createFileRoute, useNavigate } from "@tanstack/react-router";
import { PlusIcon, ServerIcon } from "lucide-react";
import { projectsQuery } from "#/api/hooks";
import { DocsButton } from "#/components/common/docs-button";
import { EmptyState } from "#/components/common/empty-state";
import { ErrorState } from "#/components/common/error-state";
import { ProjectCard } from "#/components/project/project-card";
import { PageFrame } from "#/components/shell/page-frame";
import { useShellActions } from "#/components/shell/shell-actions";
import { Button } from "#/components/ui/button";
import { Kbd } from "#/components/ui/kbd";
import { Skeleton } from "#/components/ui/skeleton";
import { useServers } from "#/features/canvas/data";
import { useDocumentTitle } from "#/lib/hooks/use-document-title";

export const Route = createFileRoute("/")({
	component: ProjectsPage,
});

const GRID =
	"grid grid-cols-[repeat(auto-fill,minmax(min(100%,320px),1fr))] gap-3.5";

function ProjectsPage() {
	const { openNewProject } = useShellActions();
	const projects = useQuery(projectsQuery());
	useDocumentTitle(["Projects"]);
	const navigate = useNavigate();
	const servers = useServers();
	const noServers = servers.isSuccess && servers.data.length === 0;
	const items = projects.data;

	let body: React.ReactNode;
	if (projects.isPending) {
		body = (
			<div className={GRID} role="status" aria-label="Loading projects">
				{[0, 1, 2].map((index) => (
					<Skeleton key={index} className="h-[176px] rounded-card" />
				))}
			</div>
		);
	} else if (projects.isError && items === undefined) {
		body = (
			<ErrorState
				frame="raised"
				title="Projects did not load"
				body="The control plane did not answer. Nothing was changed; check that it is running, then try again."
				retry={() => void projects.refetch()}
			/>
		);
	} else if (items && items.length === 0) {
		body = (
			<EmptyState
				illustration="planet"
				frame="none"
				size="lg"
				title="Create your first project"
				body={
					noServers
						? "A project holds the services of one product, split into environments such as production and staging. Its services run on a server, and this team has none yet: add one before you deploy."
						: "A project holds the services of one product, split into environments such as production and staging."
				}
				action={
					<Button onClick={openNewProject}>
						<PlusIcon />
						New project
					</Button>
				}
				secondaryAction={
					noServers ? (
						<Button
							variant="secondary"
							onClick={() =>
								void navigate({ to: "/servers", search: { enroll: true } })
							}
						>
							<ServerIcon />
							Add a server
						</Button>
					) : (
						<DocsButton path="operations/projects.md">
							Read about projects
						</DocsButton>
					)
				}
				hint={
					<>
						<Kbd>⌘K</Kbd> opens the command palette
					</>
				}
			/>
		);
	} else {
		body = (
			<div className={GRID}>
				{(items ?? []).map((project) => (
					<ProjectCard key={project.id} project={project} />
				))}
			</div>
		);
	}

	return (
		<PageFrame
			page="projects"
			title="Projects"
			description="Each project groups the services of one product into environments."
			actions={
				items && items.length > 0 ? (
					<Button onClick={openNewProject}>
						<PlusIcon />
						New project
					</Button>
				) : undefined
			}
		>
			{body}
		</PageFrame>
	);
}
