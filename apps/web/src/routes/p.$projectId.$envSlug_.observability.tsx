import { createFileRoute, redirect } from "@tanstack/react-router";
import { z } from "zod";
import { logHistorySearch } from "#/features/logs/history/url";
import { ObservabilityPage } from "#/features/observability/observability-page";

// `$envSlug_` keeps this route a sibling of the canvas route rather than a
// child, so the canvas route owns its whole body and needs no <Outlet />.
// The environment log moved to the Logs page: a link that still carries its
// `?q=`, `?logs=`, `?range=` or `?from=&to=` lands there with them.
export const Route = createFileRoute("/p/$projectId/$envSlug_/observability")({
	validateSearch: z.object({
		q: z.string().optional().catch(undefined),
		...logHistorySearch,
	}),
	beforeLoad: ({ params, search }) => {
		if (Object.values(search).some((value) => value !== undefined)) {
			throw redirect({
				to: "/p/$projectId/$envSlug/logs",
				params,
				search,
				replace: true,
				statusCode: 301,
			});
		}
	},
	component: ObservabilityRoute,
});

function ObservabilityRoute() {
	const { projectId, envSlug } = Route.useParams();
	return <ObservabilityPage projectId={projectId} envSlug={envSlug} />;
}
