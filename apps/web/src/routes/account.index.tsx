import { createFileRoute, redirect } from "@tanstack/react-router";
import { firstPage } from "#/features/settings/nav-model";

// `/account` opens the first page.
export const Route = createFileRoute("/account/")({
	beforeLoad: () => {
		throw redirect({
			to: "/account/$section",
			params: { section: firstPage("account").section },
			replace: true,
		});
	},
});
