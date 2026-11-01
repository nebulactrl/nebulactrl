import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { Link } from "@tanstack/react-router";
import { type FormEvent, useState } from "react";
import { errorCode, errorMessage, errorStatus } from "#/api/client-config";
import {
	checkPasswordResetToken,
	confirmPasswordReset,
} from "#/api/gen/sdk.gen";
import { AuthNotice } from "#/components/auth/auth-notice";
import { AuthCard } from "#/components/auth/auth-screen";
import { Button } from "#/components/ui/button";
import { CheckLabel } from "#/components/ui/check-label";
import { Field, FieldDescription, FieldLabel } from "#/components/ui/field";
import { Input } from "#/components/ui/input";
import { Spinner } from "#/components/ui/spinner";
import { relativeTime } from "#/lib/format";
import { useDocumentTitle } from "#/lib/hooks/use-document-title";
import {
	DEAD_LINK_TEXT,
	MIN_PASSWORD,
	passwordProblem,
	resetFailure,
} from "./messages";
import { resetLinkState, tokenOutcome } from "./reset-state";

/**
 * The page a reset link opens: choose a new password. The link works once; the
 * server ends every session of the account when it is used. Its API tokens
 * keep working unless the person ticks the box, and are listed afterwards.
 * The link is checked as the page opens, so a dead one never shows a form.
 */
export function ResetPasswordPage({ token }: { token: string | undefined }) {
	useDocumentTitle(["Choose a new password"]);
	const queryClient = useQueryClient();
	const [password, setPassword] = useState("");
	const [confirm, setConfirm] = useState("");
	const [revokeApiTokens, setRevokeApiTokens] = useState(false);

	const check = useQuery({
		queryKey: ["password-reset-check", token],
		queryFn: async () => {
			await checkPasswordResetToken({ body: { token: token ?? "" } });
			return true;
		},
		enabled: Boolean(token),
		retry: false,
		staleTime: Number.POSITIVE_INFINITY,
		refetchOnWindowFocus: false,
	});

	const reset = useMutation({
		mutationFn: async () => {
			const { data } = await confirmPasswordReset({
				body: { token: token ?? "", password, revokeApiTokens },
			});
			// Go nil slices arrive as null.
			return { ...data, keptTokens: data.keptTokens ?? [] };
		},
		// Every session ended on the server: forget the cached user so the
		// sign-in page is not skipped.
		onSuccess: () => queryClient.clear(),
	});

	const problem = passwordProblem(password, confirm);
	const submit = (event: FormEvent) => {
		event.preventDefault();
		if (reset.isPending || problem || confirm === "") return;
		reset.mutate();
	};

	if (!token) {
		return (
			<AuthCard>
				<h1 className="type-page-title text-[24px] text-ink-1">
					Reset link missing
				</h1>
				<AuthNotice tone="destructive">
					This page needs the link from your reset email. Open it again, or ask
					for a new one.
				</AuthNotice>
				<Button
					render={<Link to="/forgot-password" />}
					nativeButton={false}
					variant="secondary"
				>
					Ask for a new link
				</Button>
			</AuthCard>
		);
	}

	if (reset.isSuccess) {
		const { revoked, kept } = tokenOutcome(reset.data);
		return (
			<AuthCard>
				<h1 className="type-page-title text-[24px] text-ink-1">
					Password changed
				</h1>
				<AuthNotice tone="success">
					You were signed out on every device. Sign in with your new password.
					Two-factor authentication still applies.
				</AuthNotice>
				{revoked ? <AuthNotice tone="success">{revoked}</AuthNotice> : null}
				{kept ? (
					<div className="flex flex-col gap-2">
						<AuthNotice tone="warning">{kept}</AuthNotice>
						<ul className="m-0 flex list-none flex-col gap-1.5 p-0">
							{reset.data.keptTokens.map((entry) => (
								<li
									key={entry.id}
									className="surface-well flex flex-col gap-0.5 rounded-ctl px-3 py-2 text-xs"
								>
									<span className="font-medium text-ink-1">{entry.name}</span>
									<span className="text-ink-3">
										<span className="font-mono">{entry.prefix}</span>
										{" · "}
										{entry.teamName}
										{" · "}
										{entry.lastUsedAt
											? `used ${relativeTime(entry.lastUsedAt)}`
											: "never used"}
									</span>
								</li>
							))}
						</ul>
					</div>
				) : null}
				<Button render={<Link to="/login" />} nativeButton={false} size="xl">
					Sign in
				</Button>
			</AuthCard>
		);
	}

	const state = resetLinkState({
		pending: check.isPending,
		failed: check.isError,
		code: check.isError ? errorCode(check.error) : undefined,
	});

	if (state === "checking") {
		return (
			<AuthCard>
				<h1 className="type-page-title text-[24px] text-ink-1">
					Checking your reset link
				</h1>
				<div className="flex justify-center py-3">
					<Spinner />
				</div>
			</AuthCard>
		);
	}

	if (state === "dead") {
		return (
			<AuthCard>
				<h1 className="type-page-title text-[24px] text-ink-1">
					This reset link does not work
				</h1>
				<AuthNotice tone="destructive">{DEAD_LINK_TEXT}</AuthNotice>
				<Button
					render={<Link to="/forgot-password" />}
					nativeButton={false}
					variant="secondary"
				>
					Ask for a new link
				</Button>
			</AuthCard>
		);
	}

	const failure = reset.error
		? resetFailure(
				errorCode(reset.error),
				errorStatus(reset.error),
				errorMessage(reset.error),
			)
		: undefined;
	const deadLink =
		reset.error && errorCode(reset.error) === "invalid_reset_token";

	return (
		<AuthCard>
			<form onSubmit={submit} className="flex flex-col gap-4">
				<div className="mt-1.5 mb-1 flex flex-col gap-2">
					<h1 className="type-page-title text-[24px] text-ink-1">
						Choose a new password
					</h1>
					<span className="type-body text-pretty text-ink-3">
						This signs you out everywhere else.
					</span>
				</div>
				{failure ? (
					<AuthNotice
						tone="destructive"
						action={
							deadLink ? (
								<Link
									to="/forgot-password"
									className="shrink-0 text-accent-ink underline"
								>
									New link
								</Link>
							) : undefined
						}
					>
						{failure}
					</AuthNotice>
				) : null}
				<Field>
					<FieldLabel htmlFor="reset-password">New password</FieldLabel>
					<Input
						id="reset-password"
						scale="xl"
						type="password"
						autoComplete="new-password"
						autoFocus
						required
						minLength={MIN_PASSWORD}
						value={password}
						onChange={(event) => setPassword(event.target.value)}
					/>
					<FieldDescription>
						At least {MIN_PASSWORD} characters.
					</FieldDescription>
				</Field>
				<Field>
					<FieldLabel htmlFor="reset-confirm">Repeat the password</FieldLabel>
					<Input
						id="reset-confirm"
						scale="xl"
						type="password"
						autoComplete="new-password"
						required
						value={confirm}
						onChange={(event) => setConfirm(event.target.value)}
					/>
					{confirm !== "" && confirm !== password ? (
						<FieldDescription>The two passwords do not match.</FieldDescription>
					) : null}
				</Field>
				<CheckLabel
					checked={revokeApiTokens}
					onCheckedChange={setRevokeApiTokens}
					className="items-start"
					checkboxClassName="mt-0.5"
					labelClassName="text-ink-2 text-xs leading-snug"
				>
					Also revoke my API tokens. Leave this off to keep scripts and the CLI
					working; they are listed after the change.
				</CheckLabel>
				<Button
					type="submit"
					size="xl"
					className="w-full"
					disabled={reset.isPending || problem !== undefined || confirm === ""}
				>
					{reset.isPending ? <Spinner /> : null}
					Change password
				</Button>
			</form>
		</AuthCard>
	);
}
