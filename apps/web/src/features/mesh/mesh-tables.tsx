import { Link } from "@tanstack/react-router";
import { RefreshCwIcon } from "lucide-react";
import type { MeshLinkView, NodeView, RuleView } from "#/api/gen/types.gen";
import { CopyButton } from "#/components/common/copy-button";
import { Button } from "#/components/ui/button";
import {
	Table,
	TableBody,
	TableCell,
	TableHead,
	TableHeader,
	TableRow,
} from "#/components/ui/table";
import {
	grantLabel,
	peerSummary,
	purposeLabel,
	ruleRows,
	stateChip,
} from "./model";
import { StateLabel } from "./state-label";

const FRAME = "overflow-hidden border-line border-y";

/** The mesh address of a node or link, with copy: tailnet name above, addresses below. */
function Address({
	fqdn,
	ips,
	label,
}: {
	fqdn: string | undefined;
	ips: readonly string[];
	label: string;
}) {
	const name = fqdn?.replace(/\.$/, "");
	if (!name && ips.length === 0) {
		return <span className="text-ink-4 text-xs">Not assigned yet</span>;
	}
	const copy = ips[0] ?? name ?? "";
	return (
		<span className="flex min-w-0 items-center gap-1.5">
			<span className="flex min-w-0 flex-col gap-0.5">
				{ips.length > 0 ? (
					<code className="truncate font-mono text-ink-1 text-xs">
						{ips.join("  ")}
					</code>
				) : null}
				{name ? (
					<code className="truncate font-mono text-2xs text-ink-4">{name}</code>
				) : null}
			</span>
			<CopyButton
				value={copy}
				label={`Copy ${label}`}
				variant="ghost"
				size="icon-xs"
			/>
		</span>
	);
}

/** One row per mesh-enabled server: its node, address, peers and what it shares. */
export function NodesTable({ nodes }: { nodes: readonly NodeView[] }) {
	return (
		<div className={FRAME}>
			<Table stack>
				<TableHeader>
					<TableRow>
						<TableHead>Server</TableHead>
						<TableHead>State</TableHead>
						<TableHead>Mesh address</TableHead>
						<TableHead>Peers</TableHead>
						<TableHead>Shares</TableHead>
						<TableHead className="text-right">Devices</TableHead>
					</TableRow>
				</TableHeader>
				<TableBody>
					{nodes.map((node) => (
						<TableRow key={node.serverId}>
							<TableCell pin="start">
								<Link
									to="/servers/$serverId/settings/$section"
									params={{ serverId: node.serverId, section: "mesh" }}
									className="font-medium text-ink-1 hover:underline"
								>
									{node.serverName}
								</Link>
							</TableCell>
							<TableCell label="State" pin="end">
								<span className="flex flex-col gap-0.5">
									<StateLabel chip={stateChip(node.state, node.lastSeenAt)} />
									{node.needsReprovision ? (
										<span className="text-status-failed-on text-2xs">
											Needs re-provision
										</span>
									) : null}
								</span>
							</TableCell>
							<TableCell label="Mesh address">
								<Address
									fqdn={node.fqdn}
									ips={node.ips ?? []}
									label={`${node.serverName} mesh address`}
								/>
							</TableCell>
							<TableCell label="Peers" className="text-ink-3 text-xs">
								{peerSummary(node.peers ?? [])}
							</TableCell>
							<TableCell label="Shares" className="text-ink-3 text-xs">
								{(node.exposures ?? []).length === 0 ? (
									"Nothing"
								) : (
									<span className="flex flex-col gap-0.5">
										{(node.exposures ?? []).map((exposure) => (
											<span
												key={`${exposure.serviceId}:${exposure.purpose}`}
												className="font-mono"
											>
												{exposure.serviceName}:{exposure.meshPort}
											</span>
										))}
									</span>
								)}
							</TableCell>
							<TableCell
								label="Devices"
								className="text-right font-mono text-ink-2 text-xs tabular-nums"
							>
								{node.devices}
							</TableCell>
						</TableRow>
					))}
				</TableBody>
			</Table>
		</div>
	);
}

/** One row per link node: where it lives, which way it goes, what it may reach. */
export function LinksTable({
	links,
	canWrite,
	reprovisioning,
	onReprovision,
}: {
	links: readonly MeshLinkView[];
	canWrite: boolean;
	reprovisioning: string | null;
	onReprovision: (link: MeshLinkView) => void;
}) {
	return (
		<div className={FRAME}>
			<Table stack>
				<TableHeader>
					<TableRow>
						<TableHead>Environment</TableHead>
						<TableHead>Path</TableHead>
						<TableHead>Carries</TableHead>
						<TableHead>State</TableHead>
						<TableHead>May reach</TableHead>
						<TableHead className="w-px" />
					</TableRow>
				</TableHeader>
				<TableBody>
					{links.map((link) => (
						<TableRow key={link.id}>
							<TableCell pin="start">
								<span className="flex flex-col gap-0.5">
									<span className="font-medium text-ink-1">
										{link.environmentName}
									</span>
									{link.projectName ? (
										<span className="text-2xs text-ink-4">
											{link.projectName}
										</span>
									) : null}
								</span>
							</TableCell>
							<TableCell label="Path" className="font-mono text-ink-2 text-xs">
								{link.serverName} → {link.remoteServerName}
							</TableCell>
							<TableCell label="Carries" className="text-ink-3 text-xs">
								{purposeLabel(link.purpose)}
							</TableCell>
							<TableCell label="State" pin="end">
								<StateLabel chip={stateChip(link.state)} />
							</TableCell>
							<TableCell label="May reach" className="text-ink-3 text-xs">
								{(link.grants ?? []).length === 0 ? (
									"Nothing yet"
								) : (
									<ul className="flex flex-col gap-0.5">
										{(link.grants ?? []).map((grant) => (
											<li
												key={`${grant.exposureId}:${grant.refId}:${grant.source}`}
												className="font-mono"
											>
												{grantLabel(grant)}
											</li>
										))}
									</ul>
								)}
							</TableCell>
							<TableCell className="w-px">
								{canWrite ? (
									<Button
										variant="ghost"
										size="sm"
										disabled={reprovisioning === link.id}
										onClick={() => onReprovision(link)}
									>
										<RefreshCwIcon aria-hidden />
										Re-provision
									</Button>
								) : null}
							</TableCell>
						</TableRow>
					))}
				</TableBody>
			</Table>
		</div>
	);
}

/** The derived access rules, read-only: which link may reach which address, and why. */
export function RulesTable({
	rules,
	links,
}: {
	rules: readonly RuleView[];
	links: readonly MeshLinkView[];
}) {
	const rows = ruleRows(rules, links);
	return (
		<div className={FRAME}>
			<Table stack>
				<TableHeader>
					<TableRow>
						<TableHead>From</TableHead>
						<TableHead>May reach</TableHead>
						<TableHead>Because</TableHead>
					</TableRow>
				</TableHeader>
				<TableBody>
					{rows.map((row) => (
						<TableRow key={row.id}>
							<TableCell pin="start" className="text-ink-1 text-xs">
								{row.from}
							</TableCell>
							<TableCell
								label="May reach"
								className="font-mono text-ink-2 text-xs"
							>
								{row.target}
							</TableCell>
							<TableCell label="Because" className="text-ink-3 text-xs">
								{row.because || "A grant of this link"}
							</TableCell>
						</TableRow>
					))}
				</TableBody>
			</Table>
		</div>
	);
}
