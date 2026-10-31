import { createFileRoute } from "@tanstack/react-router";
import { ProjectSettingsPage } from "#/features/projects/project-settings-page";

// A project's own page, outside any environment: a static `settings` segment
// outranks the `$envSlug` canvas route.
export const Route = createFileRoute("/p/$projectId/settings")({
	component: ProjectSettingsRoute,
});

function ProjectSettingsRoute() {
	const { projectId } = Route.useParams();
	return <ProjectSettingsPage projectId={projectId} />;
}
