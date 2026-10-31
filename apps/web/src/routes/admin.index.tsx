import { createFileRoute, redirect } from "@tanstack/react-router";
import { firstPage } from "#/features/settings/nav-model";

// `/admin` opens the first page.
export const Route = createFileRoute("/admin/")({
	beforeLoad: () => {
		throw redirect({
			to: "/admin/$section",
			params: { section: firstPage("admin").section },
			replace: true,
		});
	},
});
