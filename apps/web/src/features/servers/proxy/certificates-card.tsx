import { useMutation, useQueryClient } from "@tanstack/react-query";
import { cn } from "cn";
import { Trash2Icon, UploadIcon } from "lucide-react";
import { useState } from "react";
import { deleteCertificate } from "#/api/gen/sdk.gen";
import type { ServerCertificate } from "#/api/gen/types.gen";
import { useConfirm } from "#/components/common/confirm-dialog";
import { Badge } from "#/components/ui/badge";
import { Button } from "#/components/ui/button";
import { StatusIcon, type StatusKind } from "#/components/ui/status-icon";
import { toast } from "#/components/ui/toast";
import {
	type CertExpiry,
	certExpiry,
	namesSummary,
	shortFingerprint,
} from "#/features/proxy/lib/cert";
import { invalidateProxy } from "#/features/proxy/queries";
import { CertificateDialog } from "./certificate-dialog";

const FILL: Record<CertExpiry["tone"], string> = {
	ok: "bg-ok",
	warn: "bg-warn",
	bad: "bg-bad",
};

const TEXT: Record<CertExpiry["tone"], string> = {
	ok: "text-ink-2",
	warn: "text-warning-text",
	bad: "text-destructive-text",
};

/** How much of the validity window is left, as a bar and as words. */
function ExpiryBar({
	cert,
	now,
}: {
	cert: Pick<ServerCertificate, "notBefore" | "notAfter">;
	now?: number;
}) {
	const expiry = certExpiry(cert, now);
	const percent = Math.round(expiry.fraction * 100);
	return (
		<div className="flex min-w-0 flex-col gap-1">
			<meter
				className="sr-only"
				aria-label="Validity left"
				min={0}
				max={100}
				value={percent}
				title={expiry.label}
			/>
			<div
				aria-hidden
				className="surface-well h-1.5 w-full overflow-hidden rounded-full"
			>
				<div
					className={cn("h-full rounded-full", FILL[expiry.tone])}
					style={{
						width: `${Math.max(percent, expiry.fraction > 0 ? 2 : 0)}%`,
					}}
				/>
			</div>
			<span className={cn("text-xs tabular-nums", TEXT[expiry.tone])}>
				{expiry.label}
			</span>
		</div>
	);
}

const STATUS_KIND: Record<ServerCertificate["status"], StatusKind> = {
	active: "live",
	pending: "pending",
	error: "failed",
};

const STATUS_LABEL: Record<ServerCertificate["status"], string> = {
	active: "Installed",
	pending: "Not installed yet",
	error: "Problem",
};

function CertificateRow({
	cert,
	onRemove,
}: {
	cert: ServerCertificate;
	onRemove(): void;
}) {
	const removing = cert.change === "remove";
	return (
		<li className="grid gap-3 sm:grid-cols-[minmax(0,1fr)_200px_auto] sm:items-center">
			<div className="min-w-0">
				<div className="flex flex-wrap items-center gap-2">
					<span
						className={cn(
							"font-medium text-ink-1 text-sm",
							removing && "line-through opacity-70",
						)}
					>
						{cert.name}
					</span>
					<span className="inline-flex items-center gap-1.5 text-ink-2 text-xs">
						<StatusIcon kind={STATUS_KIND[cert.status]} label="" />
						{STATUS_LABEL[cert.status]}
					</span>
					{cert.change === "add" ? (
						<Badge
							tone="staged"
							title="Staged: installed on the next Deploy proxy"
						>
							Staged
						</Badge>
					) : null}
					{removing ? (
						<Badge
							tone="staged"
							title="Staged: removed on the next Deploy proxy"
						>
							Removal staged
						</Badge>
					) : null}
				</div>
				<p
					className="mt-1 truncate font-mono text-2xs text-ink-2"
					title={(cert.domains ?? []).join(", ")}
				>
					{namesSummary(cert.domains ?? [])}
				</p>
				<p className="mt-0.5 truncate text-2xs text-ink-3">
					{cert.issuer} · SHA-256 {shortFingerprint(cert.fingerprint)}
				</p>
				{cert.statusMessage ? (
					<p className="mt-1 text-destructive-text text-xs">
						{cert.statusMessage}
					</p>
				) : null}
			</div>
			<ExpiryBar cert={cert} />
			<Button
				variant="ghost"
				size="icon-sm"
				aria-label={`Delete certificate ${cert.name}`}
				disabled={removing}
				onClick={onRemove}
			>
				<Trash2Icon aria-hidden />
			</Button>
		</li>
	);
}

/** Uploaded certificates of a server, with their expiry and staged state. */
export function CertificatesCard({
	serverId,
	serverName,
	certificates,
	agentTooOld,
}: {
	serverId: string;
	serverName: string;
	certificates: readonly ServerCertificate[];
	agentTooOld: boolean;
}) {
	const queryClient = useQueryClient();
	const confirm = useConfirm();
	const [uploading, setUploading] = useState(false);

	const remove = useMutation({
		mutationFn: async (certificateId: string) => {
			await deleteCertificate({ path: { certificateId } });
		},
		onSuccess: async () => {
			await invalidateProxy(queryClient);
			toast.success(
				"Certificate removal staged",
				"It is removed from the proxy on the next deploy.",
			);
		},
	});

	const askRemove = (cert: ServerCertificate) =>
		confirm({
			title: `Delete ${cert.name}?`,
			body:
				cert.change === "add" || cert.appliedAt === null
					? "It was never installed, so it is deleted right away."
					: "Domains pinned to it must be unpinned first. The proxy stops serving it on the next deploy; hostnames covered only by it fall back to the default certificate.",
			cta: "Delete certificate",
			glyph: "trash",
			danger: true,
			hold: false,
			onConfirm: async () => {
				// certificate_in_use and the rest show under the dialog body.
				await remove.mutateAsync(cert.id);
			},
		});

	return (
		<div className="flex flex-col gap-3">
			<div className="flex items-center gap-3">
				<p className="type-secondary min-w-0 flex-1">
					Your own certificates for hostnames the proxy cannot issue for.
					Automatic certificates need nothing here.
				</p>
				<Button
					variant="secondary"
					size="md"
					disabled={agentTooOld}
					title={agentTooOld ? "Upgrade the agent first" : undefined}
					onClick={() => setUploading(true)}
				>
					<UploadIcon aria-hidden />
					Upload
				</Button>
			</div>
			{certificates.length === 0 ? (
				<p className="text-ink-3 text-sm">No uploaded certificates.</p>
			) : (
				<ul aria-label="Certificates" className="rule-rows">
					{certificates.map((cert) => (
						<CertificateRow
							key={cert.id}
							cert={cert}
							onRemove={() => void askRemove(cert)}
						/>
					))}
				</ul>
			)}
			<CertificateDialog
				serverId={serverId}
				serverName={serverName}
				open={uploading}
				onOpenChange={setUploading}
			/>
		</div>
	);
}
