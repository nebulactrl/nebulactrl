import { createFileRoute } from "@tanstack/react-router";
import { InvitePage } from "#/features/teams/invite-page";

export const Route = createFileRoute("/invite/$token")({
	component: InviteRoute,
});

function InviteRoute() {
	const { token } = Route.useParams();
	const { user } = Route.useRouteContext();
	return <InvitePage token={token} signedIn={Boolean(user)} />;
}
