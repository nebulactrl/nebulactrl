import { createFileRoute, redirect } from "@tanstack/react-router";
import { useServer } from "#/features/servers/server-context";
import { ServerSettings } from "#/features/servers/server-settings";
import { findPage, firstPage } from "#/features/settings/nav-model";

export const Route = createFileRoute("/servers/$serverId/settings/$section")({
	beforeLoad: ({ params }) => {
		if (!findPage("server", params.section)) {
			throw redirect({
				to: "/servers/$serverId/settings/$section",
				params: {
					serverId: params.serverId,
					section: firstPage("server").section,
				},
				replace: true,
			});
		}
	},
	component: SettingsRoute,
});

function SettingsRoute() {
	const { section } = Route.useParams();
	const server = useServer();
	return <ServerSettings server={server} section={section} />;
}
