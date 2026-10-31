import { useMutation, useQuery } from "@tanstack/react-query";
import { ArrowLeftIcon, FingerprintIcon } from "lucide-react";
import { type FormEvent, useEffect, useState } from "react";
import { errorCode, errorMessage, errorStatus } from "#/api/client-config";
import { getSecondFactorMethodsOptions } from "#/api/gen/@tanstack/react-query.gen";
import {
	passkeySecondFactor,
	passkeySecondFactorOptions,
	verifySecondFactor,
} from "#/api/gen/sdk.gen";
import { Button } from "#/components/ui/button";
import { Field, FieldDescription, FieldLabel } from "#/components/ui/field";
import { Input } from "#/components/ui/input";
import { Spinner } from "#/components/ui/spinner";
import {
	getPasskey,
	PasskeyError,
	passkeysSupported,
	type RequestOptionsJSON,
} from "#/lib/webauthn";
import {
	isPlausibleCode,
	normalizeCode,
	passkeyFailureText,
} from "./auth-messages";
import { AuthNotice } from "./auth-notice";
import {
	EXPIRED_TEXT,
	isNoPendingLogin,
	secondFactorFailure,
} from "./second-factor-state";

/**
 * The second step of sign-in: the password (or the identity provider) was
 * accepted and NebulaCtrl now asks for a passkey, an authenticator code, or a
 * recovery code if the phone is gone. Nothing is signed in until this succeeds.
 */
export function SecondFactorStep({
	onVerified,
	onBack,
	onExpired,
	notice,
}: {
	onVerified: () => void | Promise<void>;
	onBack: () => void;
	/** No sign-in is pending: show the password step again with this reason. */
	onExpired: (reason: string) => void;
	/** Why the person is here, when it was not their own password entry. */
	notice?: string;
}) {
	const methods = useQuery({
		...getSecondFactorMethodsOptions(),
		retry: false,
		refetchOnWindowFocus: false,
	});
	// Nothing is pending (the page was opened directly, or the sign-in timed
	// out): hand back to the password step with the reason.
	const noPending =
		methods.isError &&
		isNoPendingLogin(errorStatus(methods.error), errorCode(methods.error));
	useEffect(() => {
		if (noPending) onExpired(EXPIRED_TEXT);
	}, [noPending, onExpired]);
	const [useCode, setUseCode] = useState(false);
	const [recoveryChoice, setRecoveryChoice] = useState(false);
	const [code, setCode] = useState("");

	const canPasskey = Boolean(methods.data?.passkey) && passkeysSupported();
	// An account with passkeys and no authenticator app has recovery codes only.
	const recoveryOnly = methods.data ? !methods.data.totp : false;
	const recovery = recoveryOnly || recoveryChoice;

	const verify = useMutation({
		mutationFn: () =>
			verifySecondFactor({ body: { code: normalizeCode(code) } }),
		onSuccess: () => onVerified(),
	});

	const usePasskey = useMutation({
		mutationFn: async () => {
			const options = (await passkeySecondFactorOptions())
				.data as RequestOptionsJSON;
			const assertion = await getPasskey(options);
			await passkeySecondFactor({ body: assertion });
		},
		onSuccess: () => onVerified(),
	});

	const submit = (event: FormEvent) => {
		event.preventDefault();
		if (verify.isPending || !isPlausibleCode(code, recovery)) return;
		verify.mutate();
	};

	const header = (text: string) => (
		<div className="mt-1.5 mb-1 flex flex-col gap-2">
			<h1 className="type-page-title text-[24px] text-ink-1">
				Two-step verification
			</h1>
			<span className="type-body text-pretty text-ink-3">{text}</span>
		</div>
	);

	if (methods.isPending || noPending) {
		return (
			<div className="flex flex-col gap-4">
				{header("Checking how you can finish signing in.")}
				<div className="flex justify-center py-3">
					<Spinner />
				</div>
			</div>
		);
	}

	if (canPasskey && !useCode) {
		const error = usePasskey.error;
		const text = error
			? passkeyFailureText(
					error,
					errorStatus(error),
					errorCode(error),
					error instanceof PasskeyError ? error.message : errorMessage(error),
					error instanceof PasskeyError ? error.kind : undefined,
				)
			: null;
		const expired =
			errorCode(error) === "no_pending_login" ||
			errorCode(error) === "two_factor_locked";
		return (
			<div className="flex flex-col gap-4">
				{header("Confirm it is you with a passkey registered to your account.")}
				{notice && !text ? (
					<AuthNotice tone="warning">{notice}</AuthNotice>
				) : null}
				{text ? (
					<AuthNotice
						tone="destructive"
						action={
							expired ? (
								<Button
									type="button"
									variant="ghost"
									size="sm"
									onClick={onBack}
								>
									Back
								</Button>
							) : undefined
						}
					>
						{text}
					</AuthNotice>
				) : null}
				<Button
					type="button"
					size="xl"
					className="w-full"
					autoFocus
					disabled={usePasskey.isPending || expired}
					onClick={() => usePasskey.mutate()}
				>
					{usePasskey.isPending ? <Spinner /> : <FingerprintIcon />}
					Use a passkey
				</Button>
				{expired ? null : (
					<div className="flex flex-wrap items-center justify-between gap-x-3 gap-y-1">
						<Button type="button" variant="ghost" size="sm" onClick={onBack}>
							<ArrowLeftIcon />
							Back
						</Button>
						<Button
							type="button"
							variant="ghost"
							size="sm"
							onClick={() => {
								setUseCode(true);
								usePasskey.reset();
							}}
						>
							{recoveryOnly ? "Use a recovery code" : "Use a code instead"}
						</Button>
					</div>
				)}
			</div>
		);
	}

	const failure = verify.error
		? secondFactorFailure(
				errorStatus(verify.error),
				errorCode(verify.error),
				errorMessage(verify.error),
			)
		: undefined;
	// The sign-in is dead: the code field and Verify stay, disabled, and Back is
	// the notice's own.
	const locked = failure?.expired === true;
	const passkeyUnusable =
		Boolean(methods.data?.passkey) && !passkeysSupported();

	return (
		<form onSubmit={submit} className="flex flex-col gap-4">
			{header(
				recovery
					? "Enter one of your recovery codes. Each works once."
					: "Enter the 6-digit code from your authenticator app.",
			)}
			{notice && !failure ? (
				<AuthNotice tone="warning">{notice}</AuthNotice>
			) : null}
			{passkeyUnusable && !failure ? (
				<AuthNotice tone="warning">
					Your passkeys cannot be used on this page. Passkeys need HTTPS or
					localhost.
				</AuthNotice>
			) : null}
			{failure ? (
				<AuthNotice
					tone="destructive"
					action={
						failure.expired ? (
							<Button type="button" variant="ghost" size="sm" onClick={onBack}>
								Back
							</Button>
						) : undefined
					}
				>
					{failure.text}
				</AuthNotice>
			) : null}
			<Field>
				<FieldLabel htmlFor="second-factor-code">
					{recovery ? "Recovery code" : "Authenticator code"}
				</FieldLabel>
				{recovery ? (
					<Input
						key="recovery"
						id="second-factor-code"
						scale="xl"
						autoComplete="off"
						autoCapitalize="none"
						spellCheck={false}
						autoFocus
						disabled={locked}
						placeholder="xxxx-xxxx-xxxx-xxxx"
						className="font-mono"
						value={code}
						onChange={(event) => setCode(event.target.value)}
					/>
				) : (
					<Input
						key="totp"
						id="second-factor-code"
						scale="xl"
						inputMode="numeric"
						autoComplete="one-time-code"
						pattern="[0-9 ]*"
						maxLength={7}
						autoFocus
						disabled={locked}
						placeholder="123 456"
						className="font-mono tracking-[0.2em]"
						value={code}
						onChange={(event) => setCode(event.target.value)}
					/>
				)}
				{recovery ? (
					<FieldDescription>
						Spaces, dashes and capitals do not matter.
					</FieldDescription>
				) : null}
			</Field>
			<Button
				type="submit"
				size="xl"
				className="w-full"
				disabled={
					verify.isPending || locked || !isPlausibleCode(code, recovery)
				}
			>
				{verify.isPending ? <Spinner /> : null}
				Verify
			</Button>
			{failure?.expired ? null : (
				<div className="flex flex-wrap items-center justify-between gap-x-3 gap-y-1">
					<Button type="button" variant="ghost" size="sm" onClick={onBack}>
						<ArrowLeftIcon />
						Back
					</Button>
					<div className="flex flex-wrap items-center gap-1">
						{canPasskey ? (
							<Button
								type="button"
								variant="ghost"
								size="sm"
								onClick={() => {
									setUseCode(false);
									verify.reset();
								}}
							>
								Use a passkey
							</Button>
						) : null}
						{recoveryOnly ? null : (
							<Button
								type="button"
								variant="ghost"
								size="sm"
								onClick={() => {
									setRecoveryChoice((value) => !value);
									setCode("");
									verify.reset();
								}}
							>
								{recovery ? "Use an authenticator code" : "Use a recovery code"}
							</Button>
						)}
					</div>
				</div>
			)}
		</form>
	);
}
