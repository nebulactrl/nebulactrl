import { createFileRoute } from "@tanstack/react-router";
import { DocsPageRoute } from "#/features/docs/docs-page";

export const Route = createFileRoute("/docs/$")({
	component: DocsPageRoute,
});
