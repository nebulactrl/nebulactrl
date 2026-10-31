import { createFileRoute } from "@tanstack/react-router";
import { z } from "zod";
import { logHistorySearch } from "#/features/logs/history/url";
import { ObservabilityPage } from "#/features/observability/observability-page";

// `$envSlug_` keeps this route a sibling of the canvas route rather than a
// child, so the canvas route owns its whole body and needs no <Outlet />.
// `?q=<query>` is the merged log's search; a malformed value is dropped.
export const Route = createFileRoute("/p/$projectId/$envSlug_/observability")({
	validateSearch: z.object({
		q: z.string().optional().catch(undefined),
		// `?logs=history`, `?range=` and `?from=&to=` are its stored history.
		...logHistorySearch,
	}),
	component: ObservabilityRoute,
});

function ObservabilityRoute() {
	const { projectId, envSlug } = Route.useParams();
	return <ObservabilityPage projectId={projectId} envSlug={envSlug} />;
}
