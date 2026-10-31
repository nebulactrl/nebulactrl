import { createFileRoute, redirect } from "@tanstack/react-router";
import {
	legacySettingsRedirect,
	pickSettingsSearch,
} from "#/features/settings/nav-model";

// `/settings` on its own, and every old `/settings#id` or `/settings?section=id`
// address (mail and webhooks the server already sent still carry them): read
// the hash, keep the git flow's query parameters, and move to the page that
// holds the section now.
export const Route = createFileRoute("/settings/")({
	beforeLoad: ({ location }) => {
		const { to, search } = legacySettingsRedirect(
			location.hash,
			new URLSearchParams(location.searchStr),
		);
		const [, area = "", section = ""] = to.split("/");
		const kept = pickSettingsSearch(search);
		if (area === "account") {
			throw redirect({
				to: "/account/$section",
				params: { section },
				search: kept,
				replace: true,
			});
		}
		if (area === "admin") {
			throw redirect({
				to: "/admin/$section",
				params: { section },
				search: kept,
				replace: true,
			});
		}
		throw redirect({
			to: "/settings/$section",
			params: { section },
			search: kept,
			replace: true,
		});
	},
});
