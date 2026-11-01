import { useMutation, useQueryClient } from "@tanstack/react-query";
import { SmartphoneIcon } from "lucide-react";
import { type FormEvent, useEffect, useRef, useState } from "react";
import { errorMessage } from "#/api/client-config";
import { getMySecurityQueryKey } from "#/api/gen/@tanstack/react-query.gen";
import { enableTotp, startTotpEnrolment } from "#/api/gen/sdk.gen";
import { CopyButton } from "#/components/common/copy-button";
import { Button } from "#/components/ui/button";
import {
	Dialog,
	DialogBody,
	DialogContent,
	DialogDescription,
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
import { Skeleton } from "#/components/ui/skeleton";
import { Spinner } from "#/components/ui/spinner";
import { formatSecret, isPlausibleCode, normalizeCode } from "./auth-messages";
import { QrCode } from "./qr-code";
import { RecoveryCodesPanel } from "./recovery-codes";

/**
 * Turning two-factor authentication on: add the key to an authenticator app,
 * prove it works with a code, then keep the recovery codes. The secret is
 * generated on the server when this mounts and only becomes active once the
 * first code verifies, so abandoning the flow changes nothing. The security
 * status refreshes only when the person leaves the recovery codes, so a
 * screen that depends on it cannot disappear while they are being copied.
 */
export function TotpEnrol({
	account,
	onFinished,
	doneLabel = "Done",
	onCancel,
	onCodesShown,
}: {
	/** The account email, for the authenticator's label and the codes file. */
	account: string;
	onFinished: () => void;
	doneLabel?: string;
	onCancel?: () => void;
	/** Called once 2FA is on and the recovery codes are on screen. */
	onCodesShown?: () => void;
}) {
	const queryClient = useQueryClient();
	const [code, setCode] = useState("");
	const [recovery, setRecovery] = useState<readonly string[] | null>(null);
	const started = useRef(false);

	const start = useMutation({
		mutationFn: async () => (await startTotpEnrolment()).data,
	});
	const { mutate: begin } = start;
	useEffect(() => {
		if (started.current) return;
		started.current = true;
		begin();
	}, [begin]);

	const enable = useMutation({
		mutationFn: async () =>
			(await enableTotp({ body: { code: normalizeCode(code) } })).data,
		onSuccess: (data) => {
			setRecovery(data.recoveryCodes ?? []);
			onCodesShown?.();
		},
	});

	const finish = async () => {
		await queryClient.invalidateQueries({ queryKey: getMySecurityQueryKey() });
		onFinished();
	};

	const submit = (event: FormEvent) => {
		event.preventDefault();
		if (!isPlausibleCode(code, false) || enable.isPending) return;
		enable.mutate();
	};

	if (recovery) {
		return (
			<RecoveryCodesPanel
				codes={recovery}
				account={account}
				doneLabel={doneLabel}
				onDone={() => void finish()}
			/>
		);
	}

	if (start.isError) {
		return (
			<div className="flex flex-col gap-3">
				<FieldError>{errorMessage(start.error)}</FieldError>
				<div>
					<Button type="button" variant="secondary" onClick={() => begin()}>
						Try again
					</Button>
				</div>
			</div>
		);
	}

	const secret = start.data?.secret;
	const ready = Boolean(secret) && isPlausibleCode(code, false);
	return (
		<form onSubmit={submit} className="flex flex-col gap-4">
			<ol className="m-0 flex list-none flex-col gap-4 p-0">
				<li className="flex flex-col gap-2">
					<span className="font-medium text-ink-1 text-sm">
						1. Add the key to an authenticator app
					</span>
					<span className="text-pretty text-ink-3 text-xs leading-relaxed">
						Use Google Authenticator, 1Password, Authy or any app that supports
						time-based codes. Scan the code, or choose “Enter a setup key” and
						paste the key.
					</span>
					{start.data?.uri ? (
						<QrCode
							value={start.data.uri}
							label="QR code for your authenticator app"
						/>
					) : null}
					{secret ? (
						<div className="surface-well flex items-center gap-3 rounded-ctl py-2.5 pr-2.5 pl-3.5">
							<code
								data-testid="totp-secret"
								className="min-w-0 flex-1 select-all break-all font-mono text-ink-1 text-sm leading-[1.6] tracking-wide"
							>
								{formatSecret(secret)}
							</code>
							<CopyButton variant="chip" value={secret} className="shrink-0" />
						</div>
					) : (
						<Skeleton className="h-12 rounded-ctl" />
					)}
					{start.data?.uri ? (
						<a
							href={start.data.uri}
							className="inline-flex w-fit items-center gap-1.5 text-accent-ink text-xs underline-offset-2 hover:underline focus-visible:focus-ring"
						>
							<SmartphoneIcon aria-hidden="true" className="size-3.5" />
							On this phone? Open in your authenticator app
						</a>
					) : null}
				</li>
				<li className="flex flex-col gap-2">
					<Field>
						<FieldLabel htmlFor="totp-enrol-code">
							2. Enter the 6-digit code it shows
						</FieldLabel>
						<Input
							id="totp-enrol-code"
							scale="lg"
							inputMode="numeric"
							autoComplete="one-time-code"
							pattern="[0-9 ]*"
							maxLength={7}
							placeholder="123 456"
							className="w-40 font-mono tracking-[0.2em]"
							value={code}
							onChange={(event) => setCode(event.target.value)}
						/>
						<FieldDescription>
							Codes change every 30 seconds. If it is rejected, check that your
							phone’s clock is set automatically.
						</FieldDescription>
					</Field>
					{enable.isError ? (
						<FieldError>{errorMessage(enable.error)}</FieldError>
					) : null}
				</li>
			</ol>
			<div className="flex justify-end gap-2">
				{onCancel ? (
					<Button type="button" variant="ghost" onClick={onCancel}>
						Cancel
					</Button>
				) : null}
				<Button
					type="submit"
					variant="primary"
					disabled={!ready || enable.isPending}
				>
					{enable.isPending ? <Spinner /> : null}
					Verify and turn on
				</Button>
			</div>
		</form>
	);
}

/** `TotpEnrol` in a dialog, for Account settings → Security. */
export function TotpEnrolDialog({
	open,
	onOpenChange,
	account,
}: {
	open: boolean;
	onOpenChange: (open: boolean) => void;
	account: string;
}) {
	// While the recovery codes are on screen the dialog cannot be dismissed by
	// accident: they are shown once.
	const [holding, setHolding] = useState(false);
	const close = () => {
		setHolding(false);
		onOpenChange(false);
	};
	return (
		<Dialog
			open={open}
			onOpenChange={(next) => {
				if (!next && holding) return;
				if (!next) setHolding(false);
				onOpenChange(next);
			}}
		>
			<DialogContent size="md">
				<DialogHeader>
					<DialogTitle>Turn on two-factor authentication</DialogTitle>
					<DialogDescription>
						Signing in will ask for a code from your authenticator app after
						your password.
					</DialogDescription>
				</DialogHeader>
				<DialogBody>
					{open ? (
						<TotpEnrol
							account={account}
							onFinished={close}
							onCancel={close}
							onCodesShown={() => setHolding(true)}
						/>
					) : null}
				</DialogBody>
			</DialogContent>
		</Dialog>
	);
}
