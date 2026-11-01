import { createFileRoute, useNavigate } from "@tanstack/react-router";
import { ProxyTab } from "#/features/servers/proxy/proxy-tab";
import { useServer } from "#/features/servers/server-context";

export const Route = createFileRoute("/servers/$serverId/proxy")({
	component: ProxyRoute,
});

function ProxyRoute() {
	const server = useServer();
	const navigate = useNavigate();
	return (
		<ProxyTab
			server={server}
			onOpenSettings={() =>
				void navigate({
					to: "/servers/$serverId/settings/$section",
					params: { serverId: server.id, section: "general" },
				})
			}
		/>
	);
}
