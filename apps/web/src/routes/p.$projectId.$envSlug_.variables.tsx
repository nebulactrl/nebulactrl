import { createFileRoute } from "@tanstack/react-router";
import { SharedVariablesPage } from "#/features/variables/shared/shared-variables-page";

// `$envSlug_` keeps this a sibling of the canvas route, as observability is.
export const Route = createFileRoute("/p/$projectId/$envSlug_/variables")({
	component: VariablesRoute,
});

function VariablesRoute() {
	const { projectId, envSlug } = Route.useParams();
	return <SharedVariablesPage projectId={projectId} envSlug={envSlug} />;
}
