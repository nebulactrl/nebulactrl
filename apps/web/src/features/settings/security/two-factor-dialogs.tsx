import { useMutation, useQueryClient } from "@tanstack/react-query";
import { type FormEvent, useEffect, useState } from "react";
import { errorMessage } from "#/api/client-config";
import { getMySecurityQueryKey } from "#/api/gen/@tanstack/react-query.gen";
import { disableTotp, regenerateRecoveryCodes } from "#/api/gen/sdk.gen";
import {
	isPlausibleCode,
	isTotpShape,
	normalizeCode,
} from "#/components/auth/auth-messages";
import { RecoveryCodesPanel } from "#/components/auth/recovery-codes";
import { Alert, AlertDescription } from "#/components/ui/alert";
import { Button } from "#/components/ui/button";
import {
	Dialog,
	DialogBody,
	DialogContent,
	DialogDescription,
	DialogFooter,
	DialogHeader,
	DialogTitle,
} from "#/components/ui/dialog";
import {
	Field,
	FieldDescription,
	FieldError,
	FieldLabel,
} from "#/components/ui/field";
import { Input } from "#/components/ui/input";
import { Spinner } from "#/components/ui/spinner";
import { toast } from "#/components/ui/toast";

/** A current authenticator code, or a recovery code when the phone is gone. */
function acceptableCode(input: string): boolean {
	return isTotpShape(input) || isPlausibleCode(input, true);
}

function CodeField({
	id,
	value,
	onChange,
}: {
	id: string;
	value: string;
	onChange: (value: string) => void;
}) {
	return (
		<Field>
			<FieldLabel htmlFor={id}>Authenticator or recovery code</FieldLabel>
			<Input
				id={id}
				scale="lg"
				autoComplete="one-time-code"
				autoCapitalize="none"
				spellCheck={false}
				autoFocus
				placeholder="123 456"
				className="font-mono"
				value={value}
				onChange={(event) => onChange(event.target.value)}
			/>
			<FieldDescription>
				The 6-digit code from your app, or one of your recovery codes.
			</FieldDescription>
		</Field>
	);
}

/** Turning 2FA off asks for a code, and for the password when the account has one. */
export function DisableTwoFactorDialog({
	open,
	onOpenChange,
	hasPassword,
	requiredByTeams,
}: {
	open: boolean;
	onOpenChange: (open: boolean) => void;
	hasPassword: boolean;
	requiredByTeams: readonly string[];
}) {
	const queryClient = useQueryClient();
	const [code, setCode] = useState("");
	const [password, setPassword] = useState("");
	useEffect(() => {
		if (open) {
			setCode("");
			setPassword("");
		}
	}, [open]);

	const disable = useMutation({
		mutationFn: () =>
			disableTotp({
				body: {
					code: normalizeCode(code),
					...(hasPassword ? { password } : {}),
				},
			}),
		onSuccess: async () => {
			toast.success("Two-step verification is off");
			onOpenChange(false);
			await queryClient.invalidateQueries({
				queryKey: getMySecurityQueryKey(),
			});
		},
	});

	const ready = acceptableCode(code) && (!hasPassword || password !== "");
	const submit = (event: FormEvent) => {
		event.preventDefault();
		if (!ready || disable.isPending) return;
		disable.mutate();
	};

	return (
		<Dialog open={open} onOpenChange={onOpenChange}>
			<DialogContent size="sm">
				<form onSubmit={submit} className="contents">
					<DialogHeader>
						<DialogTitle>Turn off two-step verification?</DialogTitle>
						<DialogDescription>
							Your recovery codes stop working too. Signing in will need only
							your password.
						</DialogDescription>
					</DialogHeader>
					<DialogBody>
						{requiredByTeams.length > 0 ? (
							<Alert variant="warning">
								<AlertDescription>
									{requiredByTeams.join(", ")}{" "}
									{requiredByTeams.length === 1 ? "requires" : "require"} it.
									You will be asked to set it up again before you can act there.
								</AlertDescription>
							</Alert>
						) : null}
						<CodeField id="disable-2fa-code" value={code} onChange={setCode} />
						{hasPassword ? (
							<Field>
								<FieldLabel htmlFor="disable-2fa-password">Password</FieldLabel>
								<Input
									id="disable-2fa-password"
									type="password"
									scale="lg"
									autoComplete="current-password"
									value={password}
									onChange={(event) => setPassword(event.target.value)}
								/>
							</Field>
						) : null}
						{disable.isError ? (
							<FieldError>{errorMessage(disable.error)}</FieldError>
						) : null}
					</DialogBody>
					<DialogFooter>
						<Button
							type="button"
							variant="ghost"
							onClick={() => onOpenChange(false)}
						>
							Keep it on
						</Button>
						<Button
							type="submit"
							variant="danger-solid"
							disabled={!ready || disable.isPending}
						>
							{disable.isPending ? <Spinner /> : null}
							Turn off
						</Button>
					</DialogFooter>
				</form>
			</DialogContent>
		</Dialog>
	);
}

/** New recovery codes replace every old one; a current code proves it is you. */
export function RegenerateCodesDialog({
	open,
	onOpenChange,
	account,
	byPassword = false,
}: {
	open: boolean;
	onOpenChange: (open: boolean) => void;
	account: string;
	/** A passkey-only account has no authenticator app: the password proves it is you. */
	byPassword?: boolean;
}) {
	const queryClient = useQueryClient();
	const [code, setCode] = useState("");
	const [password, setPassword] = useState("");
	const [codes, setCodes] = useState<readonly string[] | null>(null);
	useEffect(() => {
		if (open) {
			setCode("");
			setPassword("");
			setCodes(null);
		}
	}, [open]);

	const regenerate = useMutation({
		mutationFn: async () =>
			(
				await regenerateRecoveryCodes({
					body: byPassword ? { password } : { code: normalizeCode(code) },
				})
			).data,
		onSuccess: (data) => setCodes(data.recoveryCodes ?? []),
	});

	const close = async () => {
		onOpenChange(false);
		await queryClient.invalidateQueries({ queryKey: getMySecurityQueryKey() });
	};

	const ready = byPassword ? password !== "" : acceptableCode(code);
	const submit = (event: FormEvent) => {
		event.preventDefault();
		if (!ready || regenerate.isPending) return;
		regenerate.mutate();
	};

	return (
		<Dialog
			open={open}
			onOpenChange={(next) => {
				// The new codes are shown once: closing by accident would lose them.
				if (!next && codes) return;
				onOpenChange(next);
			}}
		>
			<DialogContent size="md">
				{codes ? (
					<>
						<DialogHeader>
							<DialogTitle>Your new recovery codes</DialogTitle>
							<DialogDescription>
								The previous codes no longer work.
							</DialogDescription>
						</DialogHeader>
						<DialogBody>
							<RecoveryCodesPanel
								codes={codes}
								account={account}
								onDone={() => void close()}
							/>
						</DialogBody>
					</>
				) : (
					<form onSubmit={submit} className="contents">
						<DialogHeader>
							<DialogTitle>Replace recovery codes</DialogTitle>
							<DialogDescription>
								This invalidates all of your current codes and issues ten new
								ones.
							</DialogDescription>
						</DialogHeader>
						<DialogBody>
							{byPassword ? (
								<Field>
									<FieldLabel htmlFor="regen-password">Password</FieldLabel>
									<Input
										id="regen-password"
										type="password"
										scale="lg"
										autoComplete="current-password"
										autoFocus
										value={password}
										onChange={(event) => setPassword(event.target.value)}
									/>
								</Field>
							) : (
								<CodeField id="regen-code" value={code} onChange={setCode} />
							)}
							{regenerate.isError ? (
								<FieldError>{errorMessage(regenerate.error)}</FieldError>
							) : null}
						</DialogBody>
						<DialogFooter>
							<Button
								type="button"
								variant="ghost"
								onClick={() => onOpenChange(false)}
							>
								Cancel
							</Button>
							<Button
								type="submit"
								variant="primary"
								disabled={!ready || regenerate.isPending}
							>
								{regenerate.isPending ? <Spinner /> : null}
								Create new codes
							</Button>
						</DialogFooter>
					</form>
				)}
			</DialogContent>
		</Dialog>
	);
}
