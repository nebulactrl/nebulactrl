import { createFileRoute } from "@tanstack/react-router";
import { IncidentDetail } from "#/features/status-pages/incident-detail";

export const Route = createFileRoute("/status-pages/incidents/$incidentId")({
	component: IncidentRoute,
});

function IncidentRoute() {
	const { incidentId } = Route.useParams();
	return <IncidentDetail incidentId={incidentId} />;
}
