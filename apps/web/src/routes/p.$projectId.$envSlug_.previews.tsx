import { createFileRoute } from "@tanstack/react-router";
import { PreviewsPage } from "#/features/previews/previews-page";

// `$envSlug_` keeps this a sibling of the canvas route, as observability is.
export const Route = createFileRoute("/p/$projectId/$envSlug_/previews")({
	component: PreviewsRoute,
});

function PreviewsRoute() {
	const { projectId, envSlug } = Route.useParams();
	return <PreviewsPage projectId={projectId} envSlug={envSlug} />;
}
