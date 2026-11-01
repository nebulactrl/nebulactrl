import { createFileRoute, redirect } from "@tanstack/react-router";
import {
	findPage,
	firstPage,
	pickSettingsSearch,
	teamSectionAlias,
} from "#/features/settings/nav-model";
import { TeamSettings } from "#/features/settings/settings-areas";

export const Route = createFileRoute("/settings/$section")({
	validateSearch: pickSettingsSearch,
	beforeLoad: ({ params }) => {
		const alias = teamSectionAlias(params.section);
		if (alias !== params.section) {
			throw redirect({
				to: "/settings/$section",
				params: { section: alias },
				replace: true,
			});
		}
		if (!findPage("team", params.section)) {
			throw redirect({
				to: "/settings/$section",
				params: { section: firstPage("team").section },
				replace: true,
			});
		}
	},
	component: TeamRoute,
});

function TeamRoute() {
	const { section } = Route.useParams();
	return <TeamSettings section={section} />;
}
