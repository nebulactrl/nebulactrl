import { createFileRoute, redirect } from "@tanstack/react-router";
import {
	findPage,
	firstPage,
	pickSettingsSearch,
} from "#/features/settings/nav-model";
import { AccountSettings } from "#/features/settings/settings-areas";

export const Route = createFileRoute("/account/$section")({
	validateSearch: pickSettingsSearch,
	beforeLoad: ({ params }) => {
		if (!findPage("account", params.section)) {
			throw redirect({
				to: "/account/$section",
				params: { section: firstPage("account").section },
				replace: true,
			});
		}
	},
	component: AccountRoute,
});

function AccountRoute() {
	const { section } = Route.useParams();
	return <AccountSettings section={section} />;
}
