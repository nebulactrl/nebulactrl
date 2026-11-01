import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { createFileRoute, Link, useRouter } from "@tanstack/react-router";
import { type FormEvent, useState } from "react";
import { z } from "zod";
import { errorCode, errorMessage, errorStatus } from "#/api/client-config";
import {
	getPasswordResetStatusOptions,
	listLoginProvidersOptions,
} from "#/api/gen/@tanstack/react-query.gen";
import { login } from "#/api/gen/sdk.gen";
import { meQuery } from "#/api/hooks";
import { loginErrorMessage } from "#/components/auth/auth-messages";
import { AuthNotice } from "#/components/auth/auth-notice";
import { AuthCard } from "#/components/auth/auth-screen";
import { PasskeyLoginButton } from "#/components/auth/passkey-button";
import { SecondFactorStep } from "#/components/auth/second-factor-step";
import { SsoButtons } from "#/components/auth/sso-buttons";
import { Button } from "#/components/ui/button";
import { Field, FieldLabel } from "#/components/ui/field";
import { Input } from "#/components/ui/input";
import { Spinner } from "#/components/ui/spinner";
import { ASK_ADMIN_NOTE } from "#/features/password-reset/messages";
import { ResetByHostHelp } from "#/features/password-reset/reset-help";
import { useDocumentTitle } from "#/lib/hooks/use-document-title";
import { safeReturnTo } from "#/lib/return-to";
import { passkeysSupported } from "#/lib/webauthn";

export const Route = createFileRoute("/login")({
	validateSearch: z.object({
		returnTo: z.string().optional().catch(undefined),
		// `2fa`: a provider sign-in finished and a code is still needed.
		step: z.literal("2fa").optional().catch(undefined),
		// Why a provider sign-in ended (see components/auth/auth-messages.ts).
		error: z.string().optional().catch(undefined),
		// `already-set-up`: someone opened /setup after the first admin exists.
		notice: z.literal("already-set-up").optional().catch(undefined),
	}),
	component: LoginPage,
});

function LoginPage() {
	const {
		returnTo,
		step: initialStep,
		error: redirectError,
		notice: setupNotice,
	} = Route.useSearch();
	const router = useRouter();
	const queryClient = useQueryClient();
	const [email, setEmail] = useState("");
	const [password, setPassword] = useState("");
	const [step, setStep] = useState<"password" | "2fa">(
		initialStep === "2fa" ? "2fa" : "password",
	);
	const [notice, setNotice] = useState<string | undefined>(
		setupNotice === "already-set-up"
			? "This installation is already set up. Sign in instead."
			: undefined,
	);
	useDocumentTitle(["Sign in"]);

	const providers = useQuery(listLoginProvidersOptions());
	const reset = useQuery(getPasswordResetStatusOptions());

	const finish = async () => {
		// The sign-in response is not trusted for the profile shape: read /me.
		await queryClient.invalidateQueries({ queryKey: meQuery().queryKey });
		await router.invalidate();
		await router.navigate({ href: safeReturnTo(returnTo) });
	};

	const signIn = useMutation({
		mutationFn: () => login({ body: { email: email.trim(), password } }),
		onSuccess: finish,
		onError: (error) => {
			// The password was right and the account has 2FA: ask for the code.
			if (errorCode(error) === "two_factor_required") setStep("2fa");
		},
	});

	const submit = (event: FormEvent) => {
		event.preventDefault();
		if (signIn.isPending) return;
		signIn.mutate();
	};

	if (step === "2fa") {
		return (
			<AuthCard>
				<SecondFactorStep
					onVerified={finish}
					onBack={() => {
						signIn.reset();
						setPassword("");
						setStep("password");
					}}
					onExpired={(reason) => {
						setNotice(reason);
						setPassword("");
						setStep("password");
					}}
				/>
			</AuthCard>
		);
	}

	const failure =
		signIn.error && errorCode(signIn.error) !== "two_factor_required"
			? errorStatus(signIn.error) === 401
				? "That email and password do not match."
				: errorStatus(signIn.error) === 429
					? "Too many sign-in attempts. Wait a minute, then try again."
					: errorMessage(signIn.error)
			: undefined;
	const redirectFailure = loginErrorMessage(redirectError);

	return (
		<AuthCard>
			<form onSubmit={submit} className="flex flex-col gap-4">
				<div className="mt-1.5 mb-1 flex flex-col gap-2">
					<h1 className="type-page-title text-[24px] text-ink-1">Sign in</h1>
				</div>
				{failure ? <AuthNotice tone="destructive">{failure}</AuthNotice> : null}
				{!failure && redirectFailure ? (
					<AuthNotice tone="destructive">{redirectFailure}</AuthNotice>
				) : null}
				{!failure && !redirectFailure && notice ? (
					<AuthNotice tone="warning">{notice}</AuthNotice>
				) : null}
				<Field>
					<FieldLabel htmlFor="login-email">Email</FieldLabel>
					<Input
						id="login-email"
						scale="xl"
						type="email"
						autoComplete="username"
						autoFocus
						required
						value={email}
						onChange={(event) => setEmail(event.target.value)}
					/>
				</Field>
				<Field>
					<FieldLabel htmlFor="login-password">Password</FieldLabel>
					<Input
						id="login-password"
						scale="xl"
						type="password"
						autoComplete="current-password"
						required
						value={password}
						onChange={(event) => setPassword(event.target.value)}
					/>
				</Field>
				<Button
					type="submit"
					size="xl"
					className="w-full"
					disabled={signIn.isPending}
				>
					{signIn.isPending ? <Spinner /> : null}
					Sign in
				</Button>
				{reset.data ? (
					<p className="text-center text-ink-3 text-xs text-pretty">
						{reset.data.available ? (
							<Link to="/forgot-password" className="text-accent-ink underline">
								Forgot your password?
							</Link>
						) : (
							<ResetByHostHelp
								lead={`Forgot your password? ${ASK_ADMIN_NOTE}`}
							/>
						)}
					</p>
				) : null}
				<SsoButtons providers={providers.data?.providers ?? []}>
					{passkeysSupported() ? (
						<PasskeyLoginButton onSignedIn={finish} />
					) : null}
				</SsoButtons>
			</form>
		</AuthCard>
	);
}
