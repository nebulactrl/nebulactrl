import { createFileRoute } from "@tanstack/react-router";
import { ForgotPasswordPage } from "#/features/password-reset/forgot-password-page";

export const Route = createFileRoute("/forgot-password")({
	component: ForgotPasswordPage,
});
