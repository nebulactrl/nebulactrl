import { createFileRoute } from "@tanstack/react-router";
import { UpdatesPage } from "#/features/servers/updates-page";

export const Route = createFileRoute("/servers/$serverId/updates")({
	component: UpdatesPage,
});
