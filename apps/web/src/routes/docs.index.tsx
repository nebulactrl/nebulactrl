import { createFileRoute } from "@tanstack/react-router";
import { DocsLanding } from "#/features/docs/docs-landing";

export const Route = createFileRoute("/docs/")({
	component: DocsLanding,
});
