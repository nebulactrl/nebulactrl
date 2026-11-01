import { createFileRoute } from "@tanstack/react-router";
import { z } from "zod";
import { ResetPasswordPage } from "#/features/password-reset/reset-password-page";

export const Route = createFileRoute("/reset-password")({
	validateSearch: z.object({
		token: z.string().optional().catch(undefined),
	}),
	component: ResetRoute,
});

function ResetRoute() {
	const { token } = Route.useSearch();
	return <ResetPasswordPage token={token} />;
}
