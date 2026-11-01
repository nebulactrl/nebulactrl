import { createFileRoute } from "@tanstack/react-router";
import { SecurityTab } from "#/features/servers/security/security-tab";
import { useServer } from "#/features/servers/server-context";

export const Route = createFileRoute("/servers/$serverId/security")({
	component: SecurityRoute,
});

function SecurityRoute() {
	const server = useServer();
	return <SecurityTab key={`security-${server.id}`} server={server} />;
}
