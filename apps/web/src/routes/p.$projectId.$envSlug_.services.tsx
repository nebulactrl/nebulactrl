import { createFileRoute, redirect } from "@tanstack/react-router";

// The Services list page is gone: the canvas is the list. Old links land there.
export const Route = createFileRoute("/p/$projectId/$envSlug_/services")({
	beforeLoad: ({ params }) => {
		throw redirect({
			to: "/p/$projectId/$envSlug",
			params,
			replace: true,
			statusCode: 301,
		});
	},
});
