import { createFileRoute, Outlet, useRouter } from "@tanstack/react-router";
import { ServerLayout } from "#/features/servers/server-layout";

// The server page's frame. Every tab is a route under it; `?drain=1` opens the
// drain sheet on whichever tab is open.
export const Route = createFileRoute("/servers/$serverId")({
	validateSearch: (search: Record<string, unknown>): { drain?: true } =>
		search.drain === 1 || search.drain === "1" || search.drain === true
			? { drain: true }
			: {},
	component: ServerRoute,
});

function ServerRoute() {
	const { serverId } = Route.useParams();
	const { drain } = Route.useSearch();
	const router = useRouter();
	return (
		<ServerLayout
			serverId={serverId}
			drainOpen={drain === true}
			onDrainOpenChange={(open) =>
				router.history.replace(
					`${router.state.location.pathname}${open ? "?drain=1" : ""}`,
				)
			}
		>
			<Outlet />
		</ServerLayout>
	);
}
