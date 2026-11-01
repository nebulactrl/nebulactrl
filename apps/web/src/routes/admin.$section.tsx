import { createFileRoute, redirect } from "@tanstack/react-router";
import {
	findPage,
	firstPage,
	pickSettingsSearch,
} from "#/features/settings/nav-model";
import { AdminSettings } from "#/features/settings/settings-areas";

export const Route = createFileRoute("/admin/$section")({
	validateSearch: pickSettingsSearch,
	beforeLoad: ({ params }) => {
		if (!findPage("admin", params.section)) {
			throw redirect({
				to: "/admin/$section",
				params: { section: firstPage("admin").section },
				replace: true,
			});
		}
	},
	component: AdminRoute,
});

function AdminRoute() {
	const { section } = Route.useParams();
	return <AdminSettings section={section} />;
}
