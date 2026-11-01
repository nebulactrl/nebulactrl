import { useMutation, useQueryClient } from "@tanstack/react-query";
import { type FormEvent, useEffect, useState } from "react";
import { errorMessage } from "#/api/client-config";
import { getMySecurityQueryKey } from "#/api/gen/@tanstack/react-query.gen";
import {
	deletePasskey,
	finishPasskeyRegistration,
	renamePasskey,
	startPasskeyRegistration,
} from "#/api/gen/sdk.gen";
import type { PasskeyBody } from "#/api/gen/types.gen";
import { RecoveryCodesPanel } from "#/components/auth/recovery-codes";
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
import {
	type CreationOptionsJSON,
	createPasskey,
	PasskeyError,
} from "#/lib/webauthn";
import { suggestPasskeyName } from "./passkey-name";

const refreshSecurity = (queryClient: ReturnType<typeof useQueryClient>) =>
	queryClient.invalidateQueries({ queryKey: getMySecurityQueryKey() });

function PasswordField({
	id,
	value,
	onChange,
	description,
}: {
	id: string;
	value: string;
	onChange: (value: string) => void;
	description: string;
}) {
	return (
		<Field>
			<FieldLabel htmlFor={id}>Password</FieldLabel>
			<Input
				id={id}
				type="password"
				scale="lg"
				autoComplete="current-password"
				value={value}
				onChange={(event) => onChange(event.target.value)}
			/>
			<FieldDescription>{description}</FieldDescription>
		</Field>
	);
}

function failureOf(error: unknown): string {
	return error instanceof PasskeyError ? error.message : errorMessage(error);
}

/**
 * Add a passkey: a name, the password again (when the account has one, so a
 * stolen session cannot plant a passkey), then the browser's own prompt. When
 * the account had no recovery code left the server issues a set, shown once.
 */
export function AddPasskeyDialog({
	open,
	onOpenChange,
	hasPassword,
	account,
}: {
	open: boolean;
	onOpenChange: (open: boolean) => void;
	hasPassword: boolean;
	account: string;
}) {
	const queryClient = useQueryClient();
	const [name, setName] = useState("");
	const [password, setPassword] = useState("");
	const [codes, setCodes] = useState<readonly string[] | null>(null);
	useEffect(() => {
		if (open) {
			setName(suggestPasskeyName(navigator.userAgent));
			setPassword("");
			setCodes(null);
		}
	}, [open]);

	const add = useMutation({
		mutationFn: async () => {
			const options = (
				await startPasskeyRegistration({
					body: hasPassword ? { password } : {},
				})
			).data as CreationOptionsJSON;
			const credential = await createPasskey(options);
			return (
				await finishPasskeyRegistration({
					body: { name: name.trim(), credential },
				})
			).data;
		},
		onSuccess: async (data) => {
			await refreshSecurity(queryClient);
			if (data.recoveryCodes && data.recoveryCodes.length > 0) {
				setCodes(data.recoveryCodes);
				return;
			}
			toast.success("Passkey added", data.passkey.name);
			onOpenChange(false);
		},
	});

	const ready = name.trim() !== "" && (!hasPassword || password !== "");
	const submit = (event: FormEvent) => {
		event.preventDefault();
		if (!ready || add.isPending) return;
		add.mutate();
	};

	const failure =
		add.error instanceof PasskeyError && add.error.kind === "cancelled"
			? "The passkey prompt was closed, so nothing was added."
			: add.error
				? failureOf(add.error)
				: null;

	return (
		<Dialog
			open={open}
			onOpenChange={(next) => {
				// The recovery codes are shown once: closing by accident would lose them.
				if (!next && codes) return;
				onOpenChange(next);
			}}
		>
			<DialogContent size="md">
				{codes ? (
					<>
						<DialogHeader>
							<DialogTitle>Passkey added</DialogTitle>
							<DialogDescription>
								Save these recovery codes. If you lose every passkey, each code
								signs you in once.
							</DialogDescription>
						</DialogHeader>
						<DialogBody>
							<RecoveryCodesPanel
								codes={codes}
								account={account}
								onDone={() => onOpenChange(false)}
							/>
						</DialogBody>
					</>
				) : (
					<form onSubmit={submit} className="contents">
						<DialogHeader>
							<DialogTitle>Add a passkey</DialogTitle>
							<DialogDescription>
								Your browser or security key will ask you to confirm with a
								fingerprint, face, PIN or touch. The passkey can sign you in
								without a password, and it also counts as your second step.
							</DialogDescription>
						</DialogHeader>
						<DialogBody>
							<Field>
								<FieldLabel htmlFor="passkey-name">Name</FieldLabel>
								<Input
									id="passkey-name"
									scale="lg"
									autoComplete="off"
									autoFocus
									maxLength={60}
									value={name}
									onChange={(event) => setName(event.target.value)}
								/>
								<FieldDescription>
									Something to tell your passkeys apart, like “MacBook” or
									“YubiKey”.
								</FieldDescription>
							</Field>
							{hasPassword ? (
								<PasswordField
									id="passkey-add-password"
									value={password}
									onChange={setPassword}
									description="Confirm it is you before a new way to sign in is added."
								/>
							) : null}
							{failure ? <FieldError>{failure}</FieldError> : null}
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
								disabled={!ready || add.isPending}
							>
								{add.isPending ? <Spinner /> : null}
								Continue
							</Button>
						</DialogFooter>
					</form>
				)}
			</DialogContent>
		</Dialog>
	);
}

/** Rename a passkey. */
export function RenamePasskeyDialog({
	passkey,
	onOpenChange,
}: {
	passkey: PasskeyBody | null;
	onOpenChange: (open: boolean) => void;
}) {
	const queryClient = useQueryClient();
	const [name, setName] = useState("");
	useEffect(() => {
		if (passkey) setName(passkey.name);
	}, [passkey]);

	const rename = useMutation({
		mutationFn: () =>
			renamePasskey({
				path: { id: passkey?.id ?? "" },
				body: { name: name.trim() },
			}),
		onSuccess: async () => {
			toast.success("Passkey renamed");
			onOpenChange(false);
			await refreshSecurity(queryClient);
		},
	});

	const ready = name.trim() !== "" && name.trim() !== passkey?.name;
	const submit = (event: FormEvent) => {
		event.preventDefault();
		if (!ready || rename.isPending) return;
		rename.mutate();
	};

	return (
		<Dialog open={passkey !== null} onOpenChange={onOpenChange}>
			<DialogContent size="sm">
				<form onSubmit={submit} className="contents">
					<DialogHeader>
						<DialogTitle>Rename passkey</DialogTitle>
					</DialogHeader>
					<DialogBody>
						<Field>
							<FieldLabel htmlFor="passkey-rename">Name</FieldLabel>
							<Input
								id="passkey-rename"
								scale="lg"
								autoComplete="off"
								autoFocus
								maxLength={60}
								value={name}
								onChange={(event) => setName(event.target.value)}
							/>
						</Field>
						{rename.isError ? (
							<FieldError>{errorMessage(rename.error)}</FieldError>
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
							disabled={!ready || rename.isPending}
						>
							{rename.isPending ? <Spinner /> : null}
							Save
						</Button>
					</DialogFooter>
				</form>
			</DialogContent>
		</Dialog>
	);
}

/** Remove a passkey. It needs the password when the account has one. */
export function RemovePasskeyDialog({
	passkey,
	onOpenChange,
	hasPassword,
	lastSecondFactor,
	requiredByTeams,
}: {
	passkey: PasskeyBody | null;
	onOpenChange: (open: boolean) => void;
	hasPassword: boolean;
	/** Removing it leaves the account with no second factor. */
	lastSecondFactor: boolean;
	requiredByTeams: readonly string[];
}) {
	const queryClient = useQueryClient();
	const [password, setPassword] = useState("");
	useEffect(() => {
		if (passkey) setPassword("");
	}, [passkey]);

	const remove = useMutation({
		mutationFn: () =>
			deletePasskey({
				path: { id: passkey?.id ?? "" },
				body: hasPassword ? { password } : {},
			}),
		onSuccess: async () => {
			toast.success("Passkey removed", passkey?.name);
			onOpenChange(false);
			await refreshSecurity(queryClient);
		},
	});

	const ready = !hasPassword || password !== "";
	const submit = (event: FormEvent) => {
		event.preventDefault();
		if (!ready || remove.isPending) return;
		remove.mutate();
	};

	return (
		<Dialog open={passkey !== null} onOpenChange={onOpenChange}>
			<DialogContent size="sm">
				<form onSubmit={submit} className="contents">
					<DialogHeader>
						<DialogTitle>Remove “{passkey?.name}”?</DialogTitle>
						<DialogDescription>
							It can no longer sign in to NebulaCtrl. The passkey stays on your
							device until you delete it there.
							{lastSecondFactor
								? " This is your last second step, so its recovery codes stop working too."
								: ""}
						</DialogDescription>
					</DialogHeader>
					<DialogBody>
						{lastSecondFactor && requiredByTeams.length > 0 ? (
							<FieldDescription>
								{requiredByTeams.join(", ")}{" "}
								{requiredByTeams.length === 1 ? "requires" : "require"} two-step
								verification. You will be asked to set it up again before you
								can act there.
							</FieldDescription>
						) : null}
						{hasPassword ? (
							<PasswordField
								id="passkey-remove-password"
								value={password}
								onChange={setPassword}
								description="Confirm it is you before a way to sign in is removed."
							/>
						) : null}
						{remove.isError ? (
							<FieldError>{errorMessage(remove.error)}</FieldError>
						) : null}
					</DialogBody>
					<DialogFooter>
						<Button
							type="button"
							variant="ghost"
							onClick={() => onOpenChange(false)}
						>
							Keep it
						</Button>
						<Button
							type="submit"
							variant="danger-solid"
							disabled={!ready || remove.isPending}
						>
							{remove.isPending ? <Spinner /> : null}
							Remove
						</Button>
					</DialogFooter>
				</form>
			</DialogContent>
		</Dialog>
	);
}
