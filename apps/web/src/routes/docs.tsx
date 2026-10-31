import { createFileRoute } from "@tanstack/react-router";
import { DocsLayout } from "#/features/docs/docs-layout";

// Public: the root's session gate lets /docs through signed out.
export const Route = createFileRoute("/docs")({
	component: DocsLayout,
});
