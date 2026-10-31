import { createFileRoute, notFound, Outlet } from "@tanstack/react-router";

// The instance admin's area. Anyone else gets the app's not-found page, as for
// a project they cannot see: the area is not offered, so it is not "forbidden".
export const Route = createFileRoute("/admin")({
	beforeLoad: ({ context }) => {
		if (context.user?.instanceAdmin !== true) throw notFound();
	},
	component: Outlet,
});
