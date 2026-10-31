import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { Link } from "@tanstack/react-router";
import type { ReactNode } from "react";
import { errorCode, errorMessage } from "#/api/client-config";
import {
	getMeshOptions,
	reprovisionMeshLinkMutation,
	updateMeshSettingsMutation,
} from "#/api/gen/@tanstack/react-query.gen";
import type { MeshLinkView } from "#/api/gen/types.gen";
import { useConfirm } from "#/components/common/confirm-dialog";
import { EmptyState } from "#/components/common/empty-state";
import { ErrorState } from "#/components/common/error-state";
import { Relative } from "#/components/common/relative";
import { Alert, AlertDescription, AlertTitle } from "#/components/ui/alert";
import { Button } from "#/components/ui/button";
import { KvRow } from "#/components/ui/kv-row";
import { Skeleton } from "#/components/ui/skeleton";
import { Switch } from "#/components/ui/switch";
import { toast } from "#/components/ui/toast";
import { AclCard } from "./acl-card";
import { LinksTable, NodesTable, RulesTable } from "./mesh-tables";
import {
	coordinatorChip,
	derpDisclosure,
	deviceBudgetText,
	linkLabel,
	type MeshView,
	meshErrorText,
	PRICING_URL,
	plural,
	providerLabel,
} from "./model";
import { invalidateMesh } from "./queries";
import { StateLabel } from "./state-label";
import { useMeshAccess } from "./use-mesh-access";

function Block({
	id,
	title,
	description,
	children,
}: {
	id: string;
	title: string;
	description?: ReactNode;
	children: ReactNode;
}) {
	return (
		<section
			aria-labelledby={`${id}-title`}
			className="flex min-w-0 flex-col gap-3"
		>
			<div className="flex flex-col gap-2">
				<h3 id={`${id}-title`} className="section-head">
					{title}
				</h3>
				{description ? <p className="type-secondary">{description}</p> : null}
			</div>
			{children}
		</section>
	);
}

/** No tailnet to join: the mesh needs your Tailscale or a Headscale NebulaCtrl runs. */
function NotConfigured() {
	return (
		<EmptyState
			illustration="network"
			title="Connect a tailnet first"
			body="The mesh joins your servers to a tailnet so services and database replicas can reach each other across servers. Use your own Tailscale, or let NebulaCtrl run a Headscale for you."
			action={
				<Button
					variant="primary"
					nativeButton={false}
					render={
						<Link
							to="/settings/$section"
							params={{ section: "integrations" }}
						/>
					}
				>
					Connect Tailscale
				</Button>
			}
			secondaryAction={
				<Button
					variant="secondary"
					nativeButton={false}
					render={
						<Link to="/settings/$section" params={{ section: "headscale" }} />
					}
				>
					Set up Headscale
				</Button>
			}
		/>
	);
}

function Banners({ mesh }: { mesh: MeshView }) {
	const warnings = mesh.warnings ?? [];
	if (!mesh.paused && mesh.coordinator.ok !== false && warnings.length === 0) {
		return null;
	}
	const reason = (
		mesh.pausedReason || "A change to the coordinator is in progress"
	).replace(/\.?$/, ".");
	return (
		<div className="flex flex-col gap-3">
			{mesh.paused ? (
				<Alert variant="warning">
					<AlertTitle>The mesh is paused</AlertTitle>
					<AlertDescription>
						{reason} Connections that are up keep working. New keys, new links
						and access-policy changes wait until it resumes, and a deploy that
						needs a new link fails with "Not deployed: mesh paused. No changes
						were made."
					</AlertDescription>
				</Alert>
			) : null}
			{mesh.coordinator.ok === false ? (
				<Alert variant="warning">
					<AlertTitle>The coordinator is unreachable</AlertTitle>
					<AlertDescription>
						Existing connections keep working. New nodes, new links and policy
						changes wait until it answers again.
					</AlertDescription>
				</Alert>
			) : null}
			{warnings.map((warning) => (
				<Alert key={warning} variant="warning">
					<AlertDescription>{warning}</AlertDescription>
				</Alert>
			))}
		</div>
	);
}

function StatusRows({ mesh, canWrite }: { mesh: MeshView; canWrite: boolean }) {
	const queryClient = useQueryClient();
	const coordinator = coordinatorChip(mesh.coordinator);
	const nodes = mesh.nodes ?? [];
	const autoJoin = useMutation({
		...updateMeshSettingsMutation(),
		onSuccess: () => invalidateMesh(queryClient),
		onError: (error) =>
			toast.error(
				"Could not change this setting",
				meshErrorText(errorCode(error), errorMessage(error)),
			),
	});
	return (
		<div className="rule-rows">
			<KvRow label="Provider" description="The tailnet the servers join.">
				<div className="flex flex-col gap-1">
					<span className="font-medium text-ink-1 text-sm">
						{providerLabel(mesh.provider)}
					</span>
					{mesh.loginServer ? (
						<code className="truncate font-mono text-ink-3 text-xs">
							{mesh.loginServer}
						</code>
					) : null}
				</div>
			</KvRow>
			<KvRow label="Coordinator" description="Hands out keys and policy.">
				<div className="flex flex-col gap-1">
					<StateLabel chip={coordinator} />
					{mesh.coordinator.checkedAt ? (
						<span className="text-ink-4 text-2xs">
							Checked <Relative iso={mesh.coordinator.checkedAt} />
						</span>
					) : null}
				</div>
			</KvRow>
			<KvRow label="Mesh" description="Servers that have joined.">
				<span className="text-ink-2 text-sm">
					{nodes.length === 0
						? "No server has joined yet. Enable the mesh from a server's page."
						: `${plural(nodes.length, "server")} in the mesh`}
				</span>
			</KvRow>
			<KvRow
				label="Join servers automatically"
				htmlFor="mesh-auto-join"
				description="Online servers that can host a node join without being enabled one by one. Each adds one device."
			>
				<div className="flex items-center gap-3">
					<Switch
						id="mesh-auto-join"
						checked={mesh.autoJoin}
						disabled={!canWrite || autoJoin.isPending}
						onCheckedChange={(value) =>
							autoJoin.mutate({ body: { autoJoin: value } })
						}
					/>
					<span className="text-ink-2 text-sm">
						{mesh.autoJoin ? "On" : "Off"}
					</span>
				</div>
			</KvRow>
		</div>
	);
}

function MeshBody({ mesh }: { mesh: MeshView }) {
	const queryClient = useQueryClient();
	const confirm = useConfirm();
	const { canWrite, isAdmin } = useMeshAccess();
	const nodes = mesh.nodes ?? [];
	const links = mesh.links ?? [];
	const rules = mesh.rules ?? [];

	const reprovision = useMutation({
		...reprovisionMeshLinkMutation(),
		onSuccess: async () => {
			await invalidateMesh(queryClient);
			toast.success(
				"Re-provisioning",
				"A fresh node is being registered; the state updates here.",
			);
		},
		onError: (error) =>
			toast.error(
				"Could not re-provision the link",
				meshErrorText(errorCode(error), errorMessage(error)),
			),
	});

	const askReprovision = (link: MeshLinkView) =>
		void confirm({
			title: "Re-provision this link?",
			body: `${linkLabel(link)} gets a new key and a new mesh address; the old device is deleted from your tailnet. The access rules are regenerated. What it carries drops for a few seconds.`,
			cta: "Re-provision",
			danger: true,
			hold: false,
			onConfirm: async () => {
				await reprovision.mutateAsync({
					path: { linkId: link.id },
					headers: { "Idempotency-Key": crypto.randomUUID() },
				});
			},
		});

	return (
		<div className="flex flex-col gap-8">
			<Banners mesh={mesh} />
			<Block
				id="mesh-status"
				title="Status"
				description="The mesh is a tailnet NebulaCtrl joins your servers to. Each server runs one node; a link between servers adds one more."
			>
				<StatusRows mesh={mesh} canWrite={canWrite} />
			</Block>
			<Block
				id="mesh-acl"
				title="Access policy"
				description="The rules that let one server's link reach one port on another. Everything else stays denied."
			>
				<AclCard mesh={mesh} isAdmin={isAdmin} />
			</Block>
			<Block id="mesh-servers" title="Servers">
				{nodes.length === 0 ? (
					<p className="type-secondary">
						No server has a node. Open a server and enable the mesh on it; the
						plan shows what that adds.
					</p>
				) : (
					<NodesTable nodes={nodes} />
				)}
			</Block>
			<Block
				id="mesh-links"
				title="Links"
				description="A link carries one environment's traffic between two servers, for services or for database replication. It exists only while something needs it."
			>
				{links.length === 0 ? (
					<p className="type-secondary">
						No links yet. One appears when a service reaches another on a
						different server, or a replica is added across servers.
					</p>
				) : (
					<LinksTable
						links={links}
						canWrite={canWrite}
						reprovisioning={
							reprovision.isPending
								? (reprovision.variables?.path.linkId ?? null)
								: null
						}
						onReprovision={askReprovision}
					/>
				)}
			</Block>
			<Block
				id="mesh-rules"
				title="Access rules"
				description="Derived from the links above; you cannot edit them here."
			>
				{rules.length === 0 ? (
					<p className="type-secondary">No rules: nothing needs one yet.</p>
				) : (
					<RulesTable rules={rules} links={links} />
				)}
			</Block>
			<Block id="mesh-devices" title="Devices">
				<p className="type-secondary">
					{deviceBudgetText(mesh.devices.used)}{" "}
					<a
						href={PRICING_URL}
						target="_blank"
						rel="noopener noreferrer"
						className="text-accent-ink underline"
					>
						Tailscale pricing
					</a>
				</p>
			</Block>
			<Block id="mesh-relays" title="Relays">
				<Alert variant="info">
					<AlertDescription>{derpDisclosure(mesh.provider)}</AlertDescription>
				</Alert>
			</Block>
		</div>
	);
}

/**
 * Team settings → Server mesh: the tailnet the servers share. Provider and coordinator,
 * how the access policy is handled, every server's node, the links between
 * servers with what each may reach, the rules derived from them, and what it
 * costs in devices. Each state is a status icon and words.
 */
export function MeshSection() {
	const mesh = useQuery({ ...getMeshOptions(), retry: false });
	if (mesh.isPending) return <Skeleton className="h-60 rounded-card" />;
	if (mesh.isError && !mesh.data) {
		if (errorCode(mesh.error) === "mesh_not_configured") {
			return <NotConfigured />;
		}
		return (
			<ErrorState
				frame="raised"
				title="The mesh did not load"
				body="The control plane did not answer, so nothing was changed. Try again in a moment."
				retry={() => void mesh.refetch()}
			/>
		);
	}
	if (mesh.data.provider === "none") return <NotConfigured />;
	return <MeshBody mesh={mesh.data} />;
}
