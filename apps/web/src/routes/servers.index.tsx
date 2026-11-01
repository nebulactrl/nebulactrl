import { createFileRoute, useNavigate } from "@tanstack/react-router";
import { serverViewsQuery } from "#/features/servers/queries";
import { ServersPage } from "#/features/servers/servers-page";

type ServersSearch = { enroll?: true; view?: "fleet" };

export const Route = createFileRoute("/servers/")({
	validateSearch: (search: Record<string, unknown>): ServersSearch => {
		const out: ServersSearch = {};
		if (search.enroll === 1 || search.enroll === "1" || search.enroll === true)
			out.enroll = true;
		if (search.view === "fleet") out.view = "fleet";
		return out;
	},
	// Starts the list request and returns at once; the intent preload of the
	// sidebar link then has the cards in the cache when the click renders.
	loader: ({ context }) => {
		void context.queryClient.prefetchQuery(serverViewsQuery());
	},
	component: ServersRoute,
});

function ServersRoute() {
	const { enroll, view } = Route.useSearch();
	const navigate = useNavigate({ from: "/servers/" });
	return (
		<ServersPage
			view={view === "fleet" ? "fleet" : "servers"}
			onViewChange={(next) =>
				void navigate({
					search: (prev) => {
						const { view: _drop, ...rest } = prev;
						return next === "fleet" ? { ...rest, view: "fleet" } : rest;
					},
					replace: true,
				})
			}
			enrollOpen={enroll === true}
			onEnrollOpenChange={(open) =>
				void navigate({
					search: (prev) => {
						const { enroll: _drop, ...rest } = prev;
						return open ? { ...rest, enroll: true } : rest;
					},
					replace: true,
				})
			}
		/>
	);
}
