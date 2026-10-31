import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { ExternalLinkIcon } from "lucide-react";
import { useState } from "react";
import { errorMessage } from "#/api/client-config";
import { getServiceSshKeyOptions } from "#/api/gen/@tanstack/react-query.gen";
import {
	acceptServiceSshHostKey,
	deleteServiceSshKey,
	setServiceSshKey,
	testServiceSshConnection,
} from "#/api/gen/sdk.gen";
import type { HostView, TestResult } from "#/api/gen/types.gen";
import { invalidateOperations } from "#/api/invalidate";
import type { Service } from "#/api/models";
import { CodeBlock } from "#/components/common/code-block";
import { useConfirm } from "#/components/common/confirm-dialog";
import { Relative } from "#/components/common/relative";
import { Alert, AlertDescription, AlertTitle } from "#/components/ui/alert";
import { Button } from "#/components/ui/button";
import { Skeleton } from "#/components/ui/skeleton";
import { Spinner } from "#/components/ui/spinner";
import { Textarea } from "#/components/ui/textarea";
import { toast } from "#/components/ui/toast";
import { SettingRow } from "./fields";
import { keyTypeLabel, shortFingerprint } from "./ssh-model";

const SSH_OPERATIONS = ["getServiceSshKey"] as const;

/**
 * The deploy key rows of a git service whose repository is an SSH URL: the
 * generated or uploaded key (public half only, the private key is write-only),
 * the provider's "add deploy key" link, a connection test and the pinned host
 * key with its "Accept new host key" action. Like the git link, these are
 * operational settings: they apply at once and are never staged.
 */
export function SshKeyRows({ service }: { service: Service }) {
	const queryClient = useQueryClient();
	const confirm = useConfirm();
	const query = useQuery(
		getServiceSshKeyOptions({ path: { serviceId: service.id } }),
	);
	const [uploading, setUploading] = useState(false);
	const [privateKey, setPrivateKey] = useState("");
	const [test, setTest] = useState<TestResult | null>(null);

	const refresh = () => invalidateOperations(queryClient, SSH_OPERATIONS);
	const path = { serviceId: service.id };

	const setKey = useMutation({
		mutationFn: (input: { privateKey?: string; rotate?: boolean }) =>
			setServiceSshKey({ path, body: input }),
		onSuccess: async (_data, input) => {
			setUploading(false);
			setPrivateKey("");
			setTest(null);
			toast.success(
				input.rotate ? "Deploy key replaced" : "Deploy key added",
				"Add the public key to the repository, then test the connection.",
			);
			await refresh();
		},
		onError: (e) =>
			toast.error("Could not set the deploy key", errorMessage(e)),
	});

	const remove = useMutation({
		mutationFn: () => deleteServiceSshKey({ path }),
		onSuccess: async () => {
			setTest(null);
			toast.success("Deploy key removed");
			await refresh();
		},
		onError: (e) =>
			toast.error("Could not remove the deploy key", errorMessage(e)),
	});

	const check = useMutation({
		mutationFn: async () => (await testServiceSshConnection({ path })).data,
		onSuccess: async (result) => {
			setTest(result ?? null);
			await refresh();
		},
		onError: (e) =>
			toast.error("Could not test the connection", errorMessage(e)),
	});

	const accept = useMutation({
		mutationFn: (fingerprint: string) =>
			acceptServiceSshHostKey({ path, body: { fingerprint } }),
		onSuccess: async () => {
			setTest(null);
			toast.success("Host key accepted", "Deploys trust the new key.");
			await refresh();
		},
		onError: (e) =>
			toast.error("Could not accept the host key", errorMessage(e)),
	});

	if (query.isPending) {
		return (
			<SettingRow label="Deploy key">
				<Skeleton className="h-9 max-w-[360px] rounded-ctl" />
			</SettingRow>
		);
	}
	if (query.isError || !query.data) {
		return (
			<SettingRow label="Deploy key">
				<span className="text-destructive-text text-xs">
					Could not load the deploy key: {errorMessage(query.error)}
				</span>
			</SettingRow>
		);
	}
	const status = query.data;
	const key = status.key;
	const busy =
		setKey.isPending || remove.isPending || check.isPending || accept.isPending;

	const replace = async (own: boolean) => {
		if (own) {
			setUploading(true);
			return;
		}
		const ok = await confirm({
			title: "Generate a new deploy key?",
			body: "The current key stops working for this service as soon as it is replaced. Add the new public key to the repository before the next deploy.",
			cta: "Generate new key",
		});
		if (ok) setKey.mutate({ rotate: true });
	};

	const removeKey = async () => {
		const ok = await confirm({
			title: "Remove the deploy key?",
			body: "Deploys of this service fail while its repository is an SSH URL and it has no key. The pinned host keys are removed too.",
			cta: "Remove key",
			danger: true,
		});
		if (ok) remove.mutate();
	};

	const acceptHost = async (host: HostView) => {
		const offered = host.mismatch;
		if (!offered) return;
		const ok = await confirm({
			title: `Accept the new host key of ${host.host}?`,
			body: (
				<span className="flex flex-col gap-2">
					<span>
						Only accept it if the key was changed on purpose (a re-installed
						server, a provider key rotation). Otherwise someone may be
						intercepting the connection.
					</span>
					<span className="flex flex-col gap-1 font-mono text-xs">
						<span>Pinned: {host.fingerprint}</span>
						<span>Offered: {offered.fingerprint}</span>
					</span>
				</span>
			),
			cta: "Accept new host key",
		});
		if (ok) accept.mutate(offered.fingerprint);
	};

	const submitUpload = () =>
		setKey.mutate({ privateKey, rotate: key !== undefined });

	const uploadForm = (
		<div className="flex w-full flex-col gap-2">
			<Textarea
				id="ssh-private-key"
				aria-label="Private key"
				rows={6}
				spellCheck={false}
				autoComplete="off"
				className="font-mono text-xs"
				placeholder={"-----BEGIN OPENSSH PRIVATE KEY-----\n…"}
				value={privateKey}
				onChange={(event) => setPrivateKey(event.target.value)}
			/>
			<span className="type-secondary">
				An unencrypted OpenSSH or PEM private key (Ed25519, ECDSA or RSA of at
				least 2048 bits). Write-only: stored encrypted, never shown again.
			</span>
			<span className="flex gap-2">
				<Button
					variant="primary"
					size="sm"
					disabled={privateKey.trim() === "" || busy}
					onClick={submitUpload}
				>
					{setKey.isPending ? <Spinner /> : null}
					Save key
				</Button>
				<Button
					variant="secondary"
					size="sm"
					onClick={() => {
						setUploading(false);
						setPrivateKey("");
					}}
				>
					Cancel
				</Button>
			</span>
		</div>
	);

	return (
		<>
			<SettingRow
				anchor="ssh-deploy-key"
				label="Deploy key"
				description={
					key
						? "Add this public key to the repository as a deploy key. Read-only access is enough."
						: "An SSH URL clones with a key pair. Generate one here and add its public key to the repository, or upload your own."
				}
			>
				<div className="flex w-full flex-col gap-2">
					{key ? (
						<>
							<CodeBlock copyValue={key.publicKey} breakAll>
								{key.publicKey}
							</CodeBlock>
							<span className="type-secondary">
								{keyTypeLabel(key.type)} · {shortFingerprint(key.fingerprint)} ·{" "}
								{key.source === "uploaded" ? "uploaded" : "generated"}{" "}
								<Relative iso={key.rotatedAt ?? key.createdAt} />
							</span>
							<span className="flex flex-wrap items-center gap-2">
								{status.deployKeyLink ? (
									<Button
										variant="secondary"
										size="sm"
										nativeButton={false}
										render={
											<a
												href={status.deployKeyLink.url}
												target="_blank"
												rel="noreferrer"
											/>
										}
									>
										{status.deployKeyLink.label}
										<ExternalLinkIcon aria-hidden className="size-3" />
									</Button>
								) : null}
								<Button
									variant="secondary"
									size="sm"
									disabled={busy || !status.usesSshUrl}
									onClick={() => check.mutate()}
								>
									{check.isPending ? <Spinner /> : null}
									Test connection
								</Button>
								<Button
									variant="ghost"
									size="sm"
									disabled={busy}
									onClick={() => void replace(false)}
								>
									Generate new key
								</Button>
								<Button
									variant="ghost"
									size="sm"
									disabled={busy}
									onClick={() => void replace(true)}
								>
									Upload a key
								</Button>
								<Button
									variant="danger"
									size="sm"
									disabled={busy}
									onClick={() => void removeKey()}
								>
									Remove key
								</Button>
							</span>
							{status.usesSshUrl ? null : (
								<span className="type-secondary">
									Save the repository URL to test the connection: it uses the
									saved URL.
								</span>
							)}
							{uploading ? uploadForm : null}
						</>
					) : uploading ? (
						uploadForm
					) : (
						<span className="flex flex-wrap items-center gap-2">
							<Button
								variant="primary"
								size="sm"
								disabled={busy}
								onClick={() => setKey.mutate({})}
							>
								{setKey.isPending ? <Spinner /> : null}
								Use SSH: generate a key
							</Button>
							<Button
								variant="secondary"
								size="sm"
								disabled={busy}
								onClick={() => setUploading(true)}
							>
								Upload your own key
							</Button>
						</span>
					)}
					{test ? <TestOutcome result={test} /> : null}
				</div>
			</SettingRow>
			{status.hostKeys && status.hostKeys.length > 0 ? (
				<SettingRow
					anchor="ssh-host-key"
					label="Host key"
					description="Recorded on the first connection. A different key later fails the deploy until you accept it."
				>
					<div className="flex w-full flex-col gap-2">
						{status.hostKeys.map((host) => (
							<HostKeyLine
								key={host.host}
								host={host}
								busy={busy}
								onAccept={() => void acceptHost(host)}
							/>
						))}
					</div>
				</SettingRow>
			) : null}
		</>
	);
}

function TestOutcome({ result }: { result: TestResult }) {
	if (result.ok) {
		return (
			<Alert variant="success">
				<AlertTitle>Connected</AlertTitle>
				<AlertDescription>
					The deploy key can read the repository
					{result.commitSha
						? ` (branch head ${result.commitSha.slice(0, 7)})`
						: ""}
					.
				</AlertDescription>
			</Alert>
		);
	}
	return (
		<Alert variant="destructive">
			<AlertTitle>
				{result.code === "host_key_changed"
					? "Host key changed"
					: "Could not connect"}
			</AlertTitle>
			<AlertDescription>{result.message}</AlertDescription>
		</Alert>
	);
}

function HostKeyLine({
	host,
	busy,
	onAccept,
}: {
	host: HostView;
	busy: boolean;
	onAccept(): void;
}) {
	return (
		<div className="flex flex-col gap-2">
			<span className="flex flex-col gap-0.5">
				<span className="font-mono text-ink-1 text-xs">{host.host}</span>
				<span className="type-secondary">
					{keyTypeLabel(host.keyType)} · {host.fingerprint} · pinned{" "}
					<Relative iso={host.firstSeenAt} />
				</span>
			</span>
			{host.mismatch ? (
				<Alert variant="destructive">
					<AlertTitle>Host key changed</AlertTitle>
					<AlertDescription>
						<span className="flex flex-col gap-2">
							<span>
								The server presented {host.mismatch.fingerprint}{" "}
								<Relative iso={host.mismatch.seenAt} /> instead of the pinned
								key. Deploys of this service fail until you accept the new key.
							</span>
							<span>
								<Button
									variant="secondary"
									size="sm"
									disabled={busy}
									onClick={onAccept}
								>
									Accept new host key
								</Button>
							</span>
						</span>
					</AlertDescription>
				</Alert>
			) : null}
		</div>
	);
}
