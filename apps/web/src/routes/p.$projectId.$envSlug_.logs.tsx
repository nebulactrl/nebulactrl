import { createFileRoute } from "@tanstack/react-router";
import { z } from "zod";
import { logHistorySearch } from "#/features/logs/history/url";
import { logSourcesSearch } from "#/features/logs-page/explorer/sources";
import { LogsPage } from "#/features/logs-page/logs-page";

// `$envSlug_` keeps this route a sibling of the canvas route rather than a
// child, so the canvas route owns its whole body and needs no <Outlet />.
// `?q=<query>` is the log's search; a malformed value is dropped.
export const Route = createFileRoute("/p/$projectId/$envSlug_/logs")({
	validateSearch: z.object({
		q: z.string().optional().catch(undefined),
		// `?logs=history`, `?range=` and `?from=&to=` are its stored history.
		...logHistorySearch,
		// `?sources=runtime,requests` narrows the log to one source.
		...logSourcesSearch,
	}),
	component: LogsRoute,
});

function LogsRoute() {
	const { projectId, envSlug } = Route.useParams();
	return <LogsPage projectId={projectId} envSlug={envSlug} />;
}
