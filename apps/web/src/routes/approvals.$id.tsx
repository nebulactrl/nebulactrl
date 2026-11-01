import { createFileRoute } from "@tanstack/react-router";
import { ApprovalPage } from "#/features/approvals/approval-page";

export const Route = createFileRoute("/approvals/$id")({
	component: ApprovalRoute,
});

function ApprovalRoute() {
	const { id } = Route.useParams();
	return <ApprovalPage approvalId={id} />;
}
