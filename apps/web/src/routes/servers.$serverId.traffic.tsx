import { createFileRoute } from "@tanstack/react-router";
import { TrafficPanel } from "#/features/analytics";

export const Route = createFileRoute("/servers/$serverId/traffic")({
	component: TrafficRoute,
});

function TrafficRoute() {
	const { serverId } = Route.useParams();
	return <TrafficPanel scope={{ serverId }} />;
}
