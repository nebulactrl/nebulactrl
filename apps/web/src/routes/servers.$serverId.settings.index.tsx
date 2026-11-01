import { createFileRoute, redirect } from "@tanstack/react-router";
import { firstPage } from "#/features/settings/nav-model";

// The Settings tab opens on its first page.
export const Route = createFileRoute("/servers/$serverId/settings/")({
	beforeLoad: ({ params }) => {
		throw redirect({
			to: "/servers/$serverId/settings/$section",
			params: {
				serverId: params.serverId,
				section: firstPage("server").section,
			},
			replace: true,
		});
	},
});
