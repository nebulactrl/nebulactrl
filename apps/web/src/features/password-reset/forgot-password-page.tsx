import { useMutation, useQuery } from "@tanstack/react-query";
import { Link } from "@tanstack/react-router";
import { type FormEvent, useState } from "react";
import { errorCode, errorMessage, errorStatus } from "#/api/client-config";
import { getPasswordResetStatusOptions } from "#/api/gen/@tanstack/react-query.gen";
import { requestPasswordReset } from "#/api/gen/sdk.gen";
import { AuthNotice } from "#/components/auth/auth-notice";
import { AuthCard } from "#/components/auth/auth-screen";
import { Button } from "#/components/ui/button";
import { Field, FieldLabel } from "#/components/ui/field";
import { Input } from "#/components/ui/input";
import { Spinner } from "#/components/ui/spinner";
import { useDocumentTitle } from "#/lib/hooks/use-document-title";
import { ASK_ADMIN_NOTE, forgotFailure, UNAVAILABLE_NOTE } from "./messages";
import { ResetByHostHelp } from "./reset-help";

/**
 * Asks for a reset link by email. The answer is the same for every address:
 * the page never says whether an account exists.
 */
export function ForgotPasswordPage() {
	useDocumentTitle(["Reset password"]);
	const status = useQuery(getPasswordResetStatusOptions());
	const [email, setEmail] = useState("");
	const request = useMutation({
		mutationFn: () => requestPasswordReset({ body: { email: email.trim() } }),
	});

	const submit = (event: FormEvent) => {
		event.preventDefault();
		if (request.isPending) return;
		request.mutate();
	};

	const available = status.data?.available;
	const unavailable =
		available === false ||
		(request.error &&
			errorCode(request.error) === "password_reset_unavailable");
	const failure =
		request.error && !unavailable
			? forgotFailure(errorStatus(request.error), errorMessage(request.error))
			: undefined;

	return (
		<AuthCard>
			<div className="flex flex-col gap-4">
				<div className="mt-1.5 mb-1 flex flex-col gap-2">
					<h1 className="type-page-title text-[24px] text-ink-1">
						Reset your password
					</h1>
					{request.isSuccess || unavailable ? null : (
						<span className="type-body text-pretty text-ink-3">
							Enter your email and we send you a link to choose a new password.
						</span>
					)}
				</div>
				{unavailable ? (
					<AuthNotice tone="warning">
						<ResetByHostHelp lead={`${UNAVAILABLE_NOTE} ${ASK_ADMIN_NOTE}`} />
					</AuthNotice>
				) : request.isSuccess ? (
					<AuthNotice tone="success">
						If that account can reset its password, a link is on its way. It
						works once and expires after 30 minutes.
					</AuthNotice>
				) : (
					<form onSubmit={submit} className="flex flex-col gap-4">
						{failure ? (
							<AuthNotice tone="destructive">{failure}</AuthNotice>
						) : null}
						<Field>
							<FieldLabel htmlFor="forgot-email">Email</FieldLabel>
							<Input
								id="forgot-email"
								scale="xl"
								type="email"
								autoComplete="username"
								autoFocus
								required
								value={email}
								onChange={(event) => setEmail(event.target.value)}
							/>
						</Field>
						<Button
							type="submit"
							size="xl"
							className="w-full"
							disabled={
								request.isPending || status.isPending || email.trim() === ""
							}
						>
							{request.isPending ? <Spinner /> : null}
							Send reset link
						</Button>
					</form>
				)}
				<p className="text-center text-ink-3 text-xs">
					<Link to="/login" className="text-accent-ink underline">
						Back to sign in
					</Link>
				</p>
			</div>
		</AuthCard>
	);
}
