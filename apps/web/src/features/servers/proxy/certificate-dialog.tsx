import { useMutation, useQueryClient } from "@tanstack/react-query";
import { FileUpIcon, KeyRoundIcon } from "lucide-react";
import { type ChangeEvent, type FormEvent, useRef, useState } from "react";
import { errorMessage } from "#/api/client-config";
import { uploadCertificate } from "#/api/gen/sdk.gen";
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
import { Textarea } from "#/components/ui/textarea";
import { toast } from "#/components/ui/toast";
import { pemProblem } from "#/features/proxy/lib/cert";
import { isAgentTooOld } from "#/features/proxy/lib/errors";
import { invalidateProxy } from "#/features/proxy/queries";
import { AgentTooOldBanner } from "#/features/proxy/ui/agent-too-old";

/** A PEM from a file picker, into the field it belongs to. */
function FilePicker({
	label,
	onText,
}: {
	label: string;
	onText(text: string): void;
}) {
	const input = useRef<HTMLInputElement>(null);
	const read = async (event: ChangeEvent<HTMLInputElement>) => {
		const file = event.target.files?.[0];
		event.target.value = "";
		if (file) onText(await file.text());
	};
	return (
		<>
			<input
				ref={input}
				type="file"
				accept=".pem,.crt,.cer,.key,text/plain"
				className="sr-only"
				tabIndex={-1}
				aria-label={label}
				onChange={(event) => void read(event)}
			/>
			<Button
				type="button"
				variant="secondary"
				size="xs"
				onClick={() => input.current?.click()}
			>
				<FileUpIcon aria-hidden />
				Choose file
			</Button>
		</>
	);
}

/**
 * Upload a certificate and its private key for a server. The key is
 * write-only: once it is added the field shows a lock chip, never the text,
 * and the control plane never returns it.
 */
export function CertificateDialog({
	serverId,
	serverName,
	open,
	onOpenChange,
}: {
	serverId: string;
	serverName: string;
	open: boolean;
	onOpenChange(open: boolean): void;
}) {
	return (
		<Dialog open={open} onOpenChange={onOpenChange}>
			<DialogContent size="md">
				{open ? (
					<CertificateForm
						serverId={serverId}
						serverName={serverName}
						onDone={() => onOpenChange(false)}
					/>
				) : null}
			</DialogContent>
		</Dialog>
	);
}

function CertificateForm({
	serverId,
	serverName,
	onDone,
}: {
	serverId: string;
	serverName: string;
	onDone(): void;
}) {
	const queryClient = useQueryClient();
	const [name, setName] = useState("");
	const [certPem, setCertPem] = useState("");
	const [keyPem, setKeyPem] = useState("");
	const [keyLocked, setKeyLocked] = useState(false);
	const [touched, setTouched] = useState(false);
	const [idempotencyKey] = useState(() => crypto.randomUUID());

	const certProblem =
		certPem === "" ? null : pemProblem(certPem, "certificate");
	const keyProblem = keyPem === "" ? null : pemProblem(keyPem, "key");
	const valid =
		name.trim() !== "" &&
		certPem.trim() !== "" &&
		keyPem.trim() !== "" &&
		certProblem === null &&
		keyProblem === null;

	const upload = useMutation({
		mutationFn: async () => {
			const { data } = await uploadCertificate({
				path: { serverId },
				body: {
					name: name.trim(),
					certPem: certPem.trim(),
					keyPem: keyPem.trim(),
				},
				headers: { "Idempotency-Key": idempotencyKey },
			});
			return data;
		},
		onSuccess: async (cert) => {
			await invalidateProxy(queryClient);
			toast.success(
				`Certificate ${cert.name} staged`,
				`Deploy the proxy on ${serverName} to install it.`,
			);
			onDone();
		},
	});

	const submit = (event: FormEvent) => {
		event.preventDefault();
		setTouched(true);
		if (valid && !upload.isPending) upload.mutate();
	};

	const setKey = (text: string) => {
		setKeyPem(text);
		setKeyLocked(text.trim() !== "" && pemProblem(text, "key") === null);
	};

	return (
		<form onSubmit={submit} className="contents">
			<DialogHeader>
				<DialogTitle>Upload a certificate</DialogTitle>
				<DialogDescription>
					For hostnames the proxy cannot get a certificate for itself: an
					internal CA, a wildcard, an origin certificate. It is installed on{" "}
					{serverName} the next time you deploy the proxy.
				</DialogDescription>
			</DialogHeader>
			<DialogBody>
				{isAgentTooOld(upload.error) ? (
					<AgentTooOldBanner serverName={serverName} />
				) : null}
				<Field>
					<FieldLabel htmlFor="cert-name">Name</FieldLabel>
					<Input
						id="cert-name"
						scale="lg"
						autoFocus
						autoComplete="off"
						placeholder="wildcard-example-com"
						value={name}
						onChange={(event) => setName(event.target.value)}
					/>
					<FieldDescription>Unique on this server.</FieldDescription>
				</Field>
				<Field>
					<div className="flex items-center justify-between gap-2">
						<FieldLabel htmlFor="cert-pem">Certificate (PEM)</FieldLabel>
						<FilePicker label="Certificate file" onText={setCertPem} />
					</div>
					<Textarea
						id="cert-pem"
						rows={5}
						spellCheck={false}
						className="font-mono text-2xs"
						placeholder={
							"-----BEGIN CERTIFICATE-----\n…\n-----END CERTIFICATE-----"
						}
						aria-invalid={certProblem !== null}
						value={certPem}
						onChange={(event) => setCertPem(event.target.value)}
					/>
					<FieldDescription>
						The leaf first, then any intermediates.
					</FieldDescription>
					{certProblem ? <FieldError>{certProblem}</FieldError> : null}
				</Field>
				<Field>
					<div className="flex items-center justify-between gap-2">
						<FieldLabel htmlFor="key-pem">Private key (PEM)</FieldLabel>
						{keyLocked ? null : (
							<FilePicker label="Private key file" onText={setKey} />
						)}
					</div>
					{keyLocked ? (
						<div className="surface-well flex items-center gap-2 rounded-ctl px-3 py-2 text-sm">
							<KeyRoundIcon aria-hidden className="size-4 text-ok" />
							<span className="flex-1 text-ink-1">
								Private key added
								<span className="ml-2 text-ink-3 text-xs">
									write-only, never shown again
								</span>
							</span>
							<Button
								type="button"
								variant="ghost"
								size="xs"
								onClick={() => {
									setKeyPem("");
									setKeyLocked(false);
								}}
							>
								Replace
							</Button>
						</div>
					) : (
						<Textarea
							id="key-pem"
							rows={4}
							spellCheck={false}
							autoComplete="off"
							className="font-mono text-2xs"
							placeholder={
								"-----BEGIN PRIVATE KEY-----\n…\n-----END PRIVATE KEY-----"
							}
							aria-invalid={keyProblem !== null}
							value={keyPem}
							onChange={(event) => setKey(event.target.value)}
						/>
					)}
					<FieldDescription>
						Without a passphrase. Sealed at rest and sent to the agent only when
						the proxy is deployed.
					</FieldDescription>
					{keyProblem ? <FieldError>{keyProblem}</FieldError> : null}
				</Field>
				{upload.isError && !isAgentTooOld(upload.error) ? (
					<FieldError>{errorMessage(upload.error)}</FieldError>
				) : null}
				{touched && !valid && !upload.isError ? (
					<FieldError>Add a name, the certificate and its key.</FieldError>
				) : null}
			</DialogBody>
			<DialogFooter>
				<Button type="button" variant="ghost" onClick={onDone}>
					Cancel
				</Button>
				<Button type="submit" disabled={upload.isPending}>
					{upload.isPending ? <Spinner /> : null}
					Upload certificate
				</Button>
			</DialogFooter>
		</form>
	);
}
