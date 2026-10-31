import { createFileRoute } from "@tanstack/react-router";
import { z } from "zod";
import { DeploymentsPage } from "#/features/env-deployments/deployments-page";
import { prefetchEnvDeployments } from "#/features/env-deployments/queries";

// The filters live in the address, so a filtered list can be linked to:
// `?status=failed&service=<id>&type=builds`. A malformed value is dropped.
// `type=builds` keeps only the deployments that built an image.
export const Route = createFileRoute("/p/$projectId/$envSlug_/deployments")({
	validateSearch: z.object({
		status: z.string().optional().catch(undefined),
		service: z.string().optional().catch(undefined),
		type: z.literal("builds").optional().catch(undefined),
	}),
	loaderDeps: ({ search }) => ({
		status: search.status,
		service: search.service,
	}),
	// Starts the page's requests and returns at once: the navigation is never
	// held back, and the sidebar link's intent preload (a 50 ms hover) has the
	// list in the cache by the time the click renders the page.
	loader: ({ context, params, deps }) => {
		void prefetchEnvDeployments(
			context.queryClient,
			params.projectId,
			params.envSlug,
			deps.status,
			deps.service,
		);
	},
	component: DeploymentsRoute,
});

function DeploymentsRoute() {
	const { projectId, envSlug } = Route.useParams();
	const { status, service, type } = Route.useSearch();
	const navigate = Route.useNavigate();
	return (
		<DeploymentsPage
			projectId={projectId}
			envSlug={envSlug}
			status={status}
			serviceId={service}
			buildsOnly={type === "builds"}
			onFilterChange={(next) =>
				void navigate({
					search: (previous) => ({ ...previous, ...next }),
					replace: true,
				})
			}
		/>
	);
}
