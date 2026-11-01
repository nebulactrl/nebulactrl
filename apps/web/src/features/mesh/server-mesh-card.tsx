import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { Link } from "@tanstack/react-router";
import { NetworkIcon, RefreshCwIcon, ZapIcon } from "lucide-react";
import { type ReactNode, useState } from "react";
import { errorCode, errorMessage } from "#/api/client-config";
import {
	checkServerMeshMutation,
	getServerMeshOptions,
	reprovisionServerMeshMutation,
} from "#/api/gen/@tanstack/react-query.gen";
import { setServerMesh } from "#/api/gen/sdk.gen";
import type { NodeView, ServerCheckResult } from "#/api/gen/types.gen";
import { useConfirm } from "#/components/common/confirm-dialog";
import { CopyButton } from "#/components/common/copy-button";
import { DocsLink } from "#/components/docs/docs-link";
import { Alert, AlertDescription } from "#/components/ui/alert";
import { Button } from "#/components/ui/button";
import { Card, CardContent, CardHeader, CardTitle } from "#/components/ui/card";
import { KvRow } from "#/components/ui/kv-row";
import { Skeleton } from "#/components/ui/skeleton";
import { Spinner } from "#/components/ui/spinner";
import { toast } from "#/components/ui/toast";
import { useServers } from "#/features/canvas/data";
import { AgentTooOldBanner } from "#/features/proxy/ui/agent-too-old";
import type { ServerView } from "#/features/servers/model";
import { EnableMeshDialog } from "./enable-mesh-dialog";
import {
	checkChip,
	meshErrorText,
	peerSummary,
	plural,
	stateChip,
} from "./model";
import { invalidateMesh } from "./queries";
import { StateLabel } from "./state-label";
import { useMeshAccess } from "./use-mesh-access";

function NodeRows({ node }: { node: NodeView }) {
	const fqdn = node.fqdn.replace(/\.$/, "");
	const ips = node.ips ?? [];
	const exposures = node.exposures ?? [];
	return (
		<>
			<KvRow
				label="Mesh address"
				description="This server's node on the tailnet."
			>
				{ips.length === 0 && !fqdn ? (
					<span className="text-ink-4 text-xs">Not assigned yet</span>
				) : (
					<span className="flex min-w-0 items-center gap-1.5">
						<span className="flex min-w-0 flex-col gap-0.5">
							{ips.length > 0 ? (
								<code className="truncate font-mono text-ink-1 text-xs">
									{ips.join("  ")}
								</code>
							) : null}
							{fqdn ? (
								<code className="truncate font-mono text-2xs text-ink-4">
									{fqdn}
								</code>
							) : null}
						</span>
						<CopyButton
							value={ips[0] ?? fqdn}
							label="Copy mesh address"
							variant="ghost"
							size="icon-xs"
						/>
					</span>
				)}
			</KvRow>
			<KvRow
				label="Peers"
				description="Other servers in the mesh, and how traffic reaches them."
			>
				<span className="text-ink-2 text-sm">
					{peerSummary(node.peers ?? [])}
				</span>
			</KvRow>
			<KvRow
				label="Shares"
				description="Services of this server that others reach over the mesh."
			>
				{exposures.length === 0 ? (
					<span className="text-ink-3 text-sm">Nothing yet</span>
				) : (
					<ul className="flex flex-col gap-0.5">
						{exposures.map((exposure) => (
							<li
								key={`${exposure.serviceId}:${exposure.purpose}`}
								className="font-mono text-ink-2 text-xs"
							>
								{exposure.serviceName}:{exposure.meshPort}
							</li>
						))}
					</ul>
				)}
			</KvRow>
			<KvRow
				label="Devices"
				description="The node, plus the links this server hosts."
			>
				<span className="font-mono text-ink-2 text-sm tabular-nums">
					{node.devices}
				</span>
			</KvRow>
		</>
	);
}

function CheckResults({ results }: { results: readonly ServerCheckResult[] }) {
	const servers = useServers();
	const names = new Map((servers.data ?? []).map((s) => [s.id, s.name]));
	if (results.length === 0) {
		return (
			<p className="text-ink-3 text-xs">
				No peer to test yet. Enable the mesh on another server first.
			</p>
		);
	}
	return (
		<ul className="surface-well flex flex-col gap-1.5 rounded-card p-3">
			{results.map((result) => (
				<li
					key={result.peerServerId ?? "self"}
					className="flex flex-wrap items-center gap-x-3 gap-y-0.5 text-xs"
				>
					<span className="font-mono text-ink-2">
						{result.peerServerId
							? (names.get(result.peerServerId) ?? result.peerServerId)
							: "This server"}
					</span>
					<StateLabel chip={checkChip(result.outcome)} />
					{result.latencyMs != null ? (
						<span className="font-mono text-ink-3">
							{Math.round(result.latencyMs)} ms
						</span>
					) : null}
					{result.message ? (
						<span className="text-ink-3">{result.message}</span>
					) : null}
				</li>
			))}
		</ul>
	);
}

/**
 * This server's place in the mesh: its state, address and peers, and the
 * actions that fit: enable (with the plan first), test connectivity,
 * re-provision and disable. Needs servers:write to change anything.
 */
export function ServerMeshCard({ server }: { server: ServerView }) {
	const queryClient = useQueryClient();
	const confirm = useConfirm();
	const { canWrite } = useMeshAccess();
	const query = useQuery({
		...getServerMeshOptions({ path: { serverId: server.id } }),
		retry: false,
	});
	const [enabling, setEnabling] = useState(false);
	const [results, setResults] = useState<ServerCheckResult[] | null>(null);

	const test = useMutation({
		...checkServerMeshMutation(),
		onSuccess: (answer) => setResults(answer.results ?? []),
		onError: (error) =>
			toast.error(
				"Could not test the mesh",
				meshErrorText(errorCode(error), errorMessage(error)),
			),
	});
	const reprovision = useMutation({
		...reprovisionServerMeshMutation(),
		onSuccess: async () => {
			await invalidateMesh(queryClient);
			toast.success(
				"Re-provisioning",
				"A fresh node is being registered; the state updates here.",
			);
		},
		onError: (error) =>
			toast.error(
				"Could not re-provision",
				meshErrorText(errorCode(error), errorMessage(error)),
			),
	});

	const askReprovision = () =>
		void confirm({
			title: "Re-provision the mesh node?",
			body: `${server.name}'s node is deleted from your tailnet and registered again with a new key and a new mesh address. The access rules are regenerated. Links through this server drop for a few seconds.`,
			cta: "Re-provision",
			danger: true,
			hold: false,
			onConfirm: async () => {
				await reprovision.mutateAsync({
					path: { serverId: server.id },
					headers: { "Idempotency-Key": crypto.randomUUID() },
				});
			},
		});

	const askDisable = () =>
		void confirm({
			title: `Take ${server.name} out of the mesh?`,
			body: `${server.name}'s node is deleted from your tailnet, and the links that start or end on it are removed. Services and replicas on other servers lose their path to it. Containers on ${server.name} keep running.`,
			cta: "Disable mesh",
			danger: true,
			hold: false,
			onConfirm: async () => {
				try {
					await setServerMesh({
						path: { serverId: server.id },
						body: { enabled: false },
						headers: { "Idempotency-Key": crypto.randomUUID() },
					});
				} catch (error) {
					throw new Error(meshErrorText(errorCode(error), errorMessage(error)));
				}
				await invalidateMesh(queryClient);
				setResults(null);
				toast.success(`${server.name} left the mesh`);
			},
		});

	const data = query.data;
	const node = data?.node ?? null;

	let body: ReactNode;
	if (query.isPending) {
		body = <Skeleton className="m-4 h-28 rounded-card" role="status" />;
	} else if (query.isError || !data) {
		body =
			errorCode(query.error) === "mesh_not_configured" ? (
				<div className="px-5 py-4 text-ink-3 text-sm">
					No tailnet is connected yet.{" "}
					<Link
						to="/settings/$section"
						params={{ section: "server-mesh" }}
						className="text-accent-ink underline"
					>
						Open Team settings → Server mesh
					</Link>{" "}
					to see what the mesh needs.
				</div>
			) : (
				<div className="flex flex-col items-start gap-2 px-5 py-4">
					<p className="text-ink-3 text-sm">
						The mesh status did not load. Nothing was changed.
					</p>
					<Button variant="secondary" onClick={() => void query.refetch()}>
						Try again
					</Button>
				</div>
			);
	} else {
		body = (
			<CardContent className="py-1">
				{!data.agentCapable ? (
					<div className="py-3.5">
						<AgentTooOldBanner serverName={server.name} />
					</div>
				) : null}
				{data.paused ? (
					<div className="py-3.5">
						<Alert variant="warning">
							<AlertDescription>
								The mesh is paused, so changes here wait until it resumes.
								Connections that are up keep working.
							</AlertDescription>
						</Alert>
					</div>
				) : null}
				<KvRow
					label="State"
					description={
						data.enabled
							? "As the agent last reported."
							: "Not in the mesh. The plan shows what enabling adds."
					}
				>
					<div className="flex flex-col gap-1">
						{data.enabled && node ? (
							<StateLabel chip={stateChip(node.state, node.lastSeenAt)} />
						) : (
							<StateLabel chip={{ kind: "stopped", label: "Not enabled" }} />
						)}
						{node?.needsReprovision ? (
							<span className="text-status-failed-on text-2xs">
								The node was removed from the tailnet. Re-provision it.
							</span>
						) : null}
						{node?.message ? (
							<span className="text-ink-3 text-xs">{node.message}</span>
						) : null}
					</div>
				</KvRow>
				{data.enabled && node ? <NodeRows node={node} /> : null}
				{!data.tunDevice ? (
					<KvRow
						label="Links to other servers"
						description="Reaching a service on another server needs a link on this one."
					>
						<span className="text-ink-3 text-xs leading-[1.5]">
							This server has no <code className="font-mono">/dev/net/tun</code>
							. It can join the mesh and be reached, but it cannot host a link.
						</span>
					</KvRow>
				) : null}
				{results ? (
					<div className="py-3.5">
						<CheckResults results={results} />
					</div>
				) : null}
				<div className="flex flex-wrap items-center gap-2 py-3.5">
					{data.enabled ? (
						<>
							<Button
								variant="secondary"
								disabled={test.isPending}
								onClick={() =>
									test.mutate({ path: { serverId: server.id }, body: {} })
								}
							>
								{test.isPending ? <Spinner /> : <ZapIcon aria-hidden />}
								Test
							</Button>
							{canWrite ? (
								<>
									<Button
										variant="secondary"
										disabled={reprovision.isPending || !data.agentCapable}
										onClick={askReprovision}
									>
										{reprovision.isPending ? (
											<Spinner />
										) : (
											<RefreshCwIcon aria-hidden />
										)}
										Re-provision
									</Button>
									<Button
										variant="ghost"
										className="ml-auto text-destructive-text"
										onClick={askDisable}
									>
										Disable mesh
									</Button>
								</>
							) : null}
						</>
					) : canWrite ? (
						<Button
							variant="primary"
							disabled={!data.agentCapable}
							onClick={() => setEnabling(true)}
						>
							<NetworkIcon aria-hidden />
							Enable mesh…
						</Button>
					) : (
						<span className="text-ink-3 text-xs">
							Enabling the mesh needs the servers:write scope.
						</span>
					)}
				</div>
			</CardContent>
		);
	}

	return (
		<Card>
			<CardHeader>
				<CardTitle className="flex items-center gap-3">
					This server's node
					{data?.enabled && node ? (
						<span className="font-normal text-ink-3 text-xs">
							{plural(node.devices, "device")}
						</span>
					) : null}
					<DocsLink to="operations/mesh" />
				</CardTitle>
			</CardHeader>
			{body}
			<EnableMeshDialog
				serverId={server.id}
				serverName={server.name}
				open={enabling}
				onOpenChange={setEnabling}
			/>
		</Card>
	);
}
