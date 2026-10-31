import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { GitBranchIcon, PlusIcon } from "lucide-react";
import { type FormEvent, useEffect, useState } from "react";
import { errorMessage } from "#/api/client-config";
import { listGitConnectionsOptions } from "#/api/gen/@tanstack/react-query.gen";
import {
	deleteGitConnection,
	refreshGitHubInstallation,
	rotateGitWebhookSecret,
	startGitConnectionOAuth,
	testGitConnection,
	updateGitConnection,
} from "#/api/gen/sdk.gen";
import type { ConnectionView } from "#/api/gen/types.gen";
import { useConfirm } from "#/components/common/confirm-dialog";
import { CredentialHelp } from "#/components/common/credential-help";
import { EmptyState } from "#/components/common/empty-state";
import { ErrorState } from "#/components/common/error-state";
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
import { Field, FieldError, FieldLabel } from "#/components/ui/field";
import { Input } from "#/components/ui/input";
import { Skeleton } from "#/components/ui/skeleton";
import { Spinner } from "#/components/ui/spinner";
import { toast } from "#/components/ui/toast";
import { gitProviderGuide } from "#/lib/credential-links";
import { ConnectDialog } from "./connect-dialog";
import { ConnectionCard } from "./connection-card";
import { DeliveriesDialog } from "./deliveries-dialog";
import { readFlowResult } from "./model";
import { invalidateGit } from "./queries";
import { WebhookPanel } from "./webhook-panel";

/** Reports the outcome a connection flow redirected back with, then cleans the URL. */
function useFlowResult(onDone: () => void) {
	useEffect(() => {
		const flow = readFlowResult(window.location.search);
		if (!flow.connected && !flow.notice && !flow.error) return;
		if (flow.error) toast.error("The connection did not finish", flow.error);
		else if (flow.notice) toast.warning("Almost there", flow.notice);
		else toast.success("Connected", "The provider is ready to use.");
		window.history.replaceState(
			null,
			"",
			`${window.location.pathname}${flow.rest}${window.location.hash}`,
		);
		onDone();
	}, [onDone]);
}

function ReplaceTokenDialog({
	connection,
	onClose,
}: {
	connection: ConnectionView | null;
	onClose(): void;
}) {
	const queryClient = useQueryClient();
	const guide = connection
		? gitProviderGuide(connection.provider, connection.baseUrl)
		: null;
	const [token, setToken] = useState("");
	const [error, setError] = useState<string | null>(null);
	const save = useMutation({
		mutationFn: async () => {
			if (!connection) return;
			await updateGitConnection({
				path: { connectionId: connection.id },
				body: { token: token.trim() },
			});
		},
		onSuccess: async () => {
			toast.success("Token replaced", "The connection was checked with it.");
			await invalidateGit(queryClient);
			setToken("");
			onClose();
		},
		onError: (e) => setError(errorMessage(e)),
	});
	const submit = (event: FormEvent) => {
		event.preventDefault();
		if (token.trim() === "" || save.isPending) return;
		setError(null);
		save.mutate();
	};
	return (
		<Dialog
			open={connection !== null}
			onOpenChange={(open) => !open && onClose()}
		>
			<DialogContent size="sm">
				<form onSubmit={submit} className="contents">
					<DialogHeader>
						<DialogTitle>Replace the token of {connection?.name}</DialogTitle>
						<DialogDescription>
							The new token is checked against the provider before it is stored.
							It is encrypted and never shown again.
						</DialogDescription>
					</DialogHeader>
					<DialogBody>
						{guide ? (
							<CredentialHelp guide={guide} fieldIds={["replace-token"]} />
						) : null}
						<Field>
							<FieldLabel htmlFor="replace-token">New access token</FieldLabel>
							<Input
								id="replace-token"
								type="password"
								autoComplete="new-password"
								autoFocus
								value={token}
								onChange={(e) => setToken(e.target.value)}
							/>
							<FieldError>{error ?? undefined}</FieldError>
						</Field>
					</DialogBody>
					<DialogFooter>
						<Button type="button" variant="secondary" onClick={onClose}>
							Cancel
						</Button>
						<Button
							type="submit"
							variant="primary"
							disabled={token.trim() === "" || save.isPending}
						>
							{save.isPending ? <Spinner /> : null}
							Replace token
						</Button>
					</DialogFooter>
				</form>
			</DialogContent>
		</Dialog>
	);
}

function WebhookDialog({
	connection,
	onClose,
}: {
	connection: ConnectionView | null;
	onClose(): void;
}) {
	const [secret, setSecret] = useState<string | undefined>(undefined);
	const rotate = useMutation({
		mutationFn: async () => {
			if (!connection) return "";
			const { data } = await rotateGitWebhookSecret({
				path: { connectionId: connection.id },
			});
			return data.webhookSecret;
		},
		onSuccess: setSecret,
		onError: (e) => toast.error("Could not rotate the secret", errorMessage(e)),
	});
	const close = () => {
		setSecret(undefined);
		onClose();
	};
	return (
		<Dialog
			open={connection !== null}
			onOpenChange={(open) => !open && close()}
		>
			<DialogContent size="lg">
				<DialogHeader>
					<DialogTitle>Webhook setup · {connection?.name}</DialogTitle>
					<DialogDescription>
						Bitbucket has no API NebulaCtrl can register webhooks through, so
						each repository gets one by hand.
					</DialogDescription>
				</DialogHeader>
				<DialogBody>
					<WebhookPanel url={connection?.webhookUrl ?? ""} secret={secret} />
				</DialogBody>
				<DialogFooter>
					<Button
						variant="secondary"
						onClick={() => rotate.mutate()}
						disabled={rotate.isPending}
					>
						{rotate.isPending ? <Spinner /> : null}
						Rotate the secret
					</Button>
					<Button variant="primary" onClick={close}>
						Done
					</Button>
				</DialogFooter>
			</DialogContent>
		</Dialog>
	);
}

/**
 * Team settings → Integrations → Git providers: connect GitHub, GitLab, Gitea,
 * Forgejo and Bitbucket, check them, read their webhook delivery log.
 */
export function GitConnectionsSection() {
	const queryClient = useQueryClient();
	const confirm = useConfirm();
	const connections = useQuery(listGitConnectionsOptions());
	const [connecting, setConnecting] = useState(false);
	const [logOf, setLogOf] = useState<ConnectionView | null>(null);
	const [hookOf, setHookOf] = useState<ConnectionView | null>(null);
	const [tokenOf, setTokenOf] = useState<ConnectionView | null>(null);
	const [testingId, setTestingId] = useState<string | null>(null);

	useFlowResult(() => void invalidateGit(queryClient));

	const test = useMutation({
		mutationFn: async (c: ConnectionView) => {
			setTestingId(c.id);
			const { data } = await testGitConnection({
				path: { connectionId: c.id },
			});
			return data;
		},
		onSuccess: async (c) => {
			if (c.status === "active")
				toast.success(
					`${c.name} works`,
					"The provider accepted the credentials.",
				);
			else toast.error(`${c.name} needs attention`, c.statusMessage);
			await invalidateGit(queryClient);
		},
		onError: (e) =>
			toast.error("Could not test the connection", errorMessage(e)),
		onSettled: () => setTestingId(null),
	});

	const authorize = useMutation({
		mutationFn: async (c: ConnectionView) => {
			const { data } = await startGitConnectionOAuth({
				path: { connectionId: c.id },
			});
			return data.authorizeUrl;
		},
		onSuccess: (url) => window.location.assign(url),
		onError: (e) =>
			toast.error("Could not start the authorization", errorMessage(e)),
	});

	const refresh = useMutation({
		mutationFn: async (c: ConnectionView) => {
			await refreshGitHubInstallation({ path: { connectionId: c.id } });
		},
		onSuccess: async () => {
			toast.success("GitHub App installed", "Repositories are available now.");
			await invalidateGit(queryClient);
		},
		onError: (e) =>
			toast.error("The App is not installed yet", errorMessage(e)),
	});

	const remove = useMutation({
		mutationFn: async (c: ConnectionView) => {
			await deleteGitConnection({
				path: { connectionId: c.id },
				query: { force: true },
			});
			return c;
		},
		onSuccess: async (c) => {
			toast.success(
				`Removed ${c.name}`,
				"Its services keep their repository URL.",
			);
			await invalidateGit(queryClient);
		},
		onError: (e) =>
			toast.error("Could not remove the connection", errorMessage(e)),
	});

	if (connections.isPending) return <Skeleton className="h-40 rounded-card" />;
	if (connections.isError && !connections.data) {
		return (
			<ErrorState
				frame="raised"
				title="Git connections did not load"
				body="The control plane did not answer. Nothing was changed."
				retry={() => void connections.refetch()}
			/>
		);
	}
	const items = connections.data?.items ?? [];
	const add = (
		<Button variant="primary" onClick={() => setConnecting(true)}>
			<PlusIcon />
			Connect a provider
		</Button>
	);

	return (
		<div className="flex flex-col gap-4">
			{items.length === 0 ? (
				<EmptyState
					illustration="network"
					title="Deploy on every push"
					body="Connect GitHub, GitLab, Gitea, Forgejo or Bitbucket to pick repositories instead of pasting URLs, deploy when you push (with watch paths for monorepos), see a status on each commit and get a preview for every pull request."
					action={add}
				/>
			) : (
				<>
					<div className="flex flex-wrap items-center justify-between gap-3">
						<p className="flex items-center gap-2 text-ink-3 text-xs">
							<GitBranchIcon aria-hidden="true" className="size-4" />
							{items.length} {items.length === 1 ? "connection" : "connections"}
							. Webhooks are verified; a replayed delivery is rejected.
						</p>
						{add}
					</div>
					<div className="grid gap-3 xl:grid-cols-2">
						{items.map((c) => (
							<ConnectionCard
								key={c.id}
								connection={c}
								testing={testingId === c.id}
								onTest={() => test.mutate(c)}
								onDeliveries={() => setLogOf(c)}
								onAuthorize={() => authorize.mutate(c)}
								onRefresh={() => refresh.mutate(c)}
								onWebhook={() => setHookOf(c)}
								onToken={() => setTokenOf(c)}
								onRemove={() =>
									void confirm({
										title: `Remove ${c.name}?`,
										body:
											c.serviceCount > 0
												? `${c.serviceCount} service${c.serviceCount === 1 ? "" : "s"} stop deploying on push and lose their clone credentials; their repository URL stays. Webhooks NebulaCtrl created are deleted.${c.authType === "github_app" ? " The GitHub App itself stays on GitHub: delete it there." : ""}`
												: "Webhooks NebulaCtrl created are deleted.",
										cta: "Remove connection",
										danger: true,
										hold: false,
										onConfirm: async () => {
											await remove.mutateAsync(c);
										},
									})
								}
							/>
						))}
					</div>
				</>
			)}
			<ConnectDialog open={connecting} onOpenChange={setConnecting} />
			<DeliveriesDialog connection={logOf} onClose={() => setLogOf(null)} />
			<WebhookDialog connection={hookOf} onClose={() => setHookOf(null)} />
			<ReplaceTokenDialog
				connection={tokenOf}
				onClose={() => setTokenOf(null)}
			/>
		</div>
	);
}
