import { createFileRoute } from "@tanstack/react-router";
import { PageEditor } from "#/features/status-pages/page-editor";

export const Route = createFileRoute("/status-pages/$pageId")({
	component: StatusPageRoute,
});

function StatusPageRoute() {
	const { pageId } = Route.useParams();
	return <PageEditor pageId={pageId} />;
}
