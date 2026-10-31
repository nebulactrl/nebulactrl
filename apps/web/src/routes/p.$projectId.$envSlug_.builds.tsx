import { createFileRoute, redirect } from "@tanstack/react-router";

// Builds is a filter of Deployments now. Old links land on that filter.
export const Route = createFileRoute("/p/$projectId/$envSlug_/builds")({
	beforeLoad: ({ params }) => {
		throw redirect({
			to: "/p/$projectId/$envSlug/deployments",
			params,
			search: { type: "builds" },
			replace: true,
			statusCode: 301,
		});
	},
});
