import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { CloudIcon, RefreshCwIcon } from "lucide-react";
import { errorMessage } from "#/api/client-config";
import { fixDomainDns, updateDomainDns } from "#/api/gen/sdk.gen";
import type { DnsStatus } from "#/api/gen/types.gen";
import { invalidateOperations } from "#/api/invalidate";
import type { ServiceDomain } from "#/api/models";
import { useConfirm } from "#/components/common/confirm-dialog";
import { Alert, AlertDescription } from "#/components/ui/alert";
import { Badge } from "#/components/ui/badge";
import { Button } from "#/components/ui/button";
import { FieldError } from "#/components/ui/field";
import { statusKindOfTone } from "#/components/ui/lamp";
import { Segmented } from "#/components/ui/segmented";
import { Spinner } from "#/components/ui/spinner";
import { StatusIcon } from "#/components/ui/status-icon";
import { toast } from "#/components/ui/toast";
import {
	dnsStateInfo,
	fixAction,
	proxyLabel,
} from "#/features/cloudflare/model";
import {
	CLOUDFLARE_OPERATIONS,
	cloudflareQuery,
	domainDnsQuery,
} from "#/features/cloudflare/queries";
import { useIsAdmin } from "#/features/proxy/use-admin";
import { APPLIES_NOW } from "./domain-copy";
import { OriginLockSection } from "./origin-lock-section";

function RecordLine({
	type,
	content,
	proxied,
	owned,
	tag,
}: {
	type: string;
	content: string;
	proxied: boolean;
	owned: boolean;
	tag: string;
}) {
	return (
		<li className="flex flex-wrap items-center gap-2 font-mono text-[12px] text-ink-2">
			<span className="min-w-24 text-ink-3">{tag}</span>
			<span>{type}</span>
			<span className="text-ink-1">{content}</span>
			<Badge>{proxyLabel(proxied)}</Badge>
			{owned ? <Badge variant="accent">managed by NebulaCtrl</Badge> : null}
		</li>
	);
}

/**
 * The Cloudflare DNS of one domain: its live status (present, conflicting,
 * wrong target, missing, not in a zone), the proxy choice (DNS only or
 * proxied), the origin lock and the one-click fix. A hostname in a zone of the
 * connected account is always managed by NebulaCtrl. Shown only for a team
 * that connected Cloudflare; the status is read from the zone when opened.
 */
export function CloudflareDnsSection({ domain }: { domain: ServiceDomain }) {
	const queryClient = useQueryClient();
	const confirm = useConfirm();
	const connection = useQuery(cloudflareQuery());
	const isAdmin = useIsAdmin();
	const configured = connection.data?.configured === true;
	const live = useQuery({ ...domainDnsQuery(domain.id), enabled: configured });

	const refresh = () =>
		invalidateOperations(queryClient, CLOUDFLARE_OPERATIONS);

	const update = useMutation({
		mutationFn: async (body: { proxied?: boolean; originLock?: boolean }) => {
			const { data } = await updateDomainDns({
				path: { domainId: domain.id },
				body,
			});
			return data;
		},
		onSuccess: async (result) => {
			await refresh();
			if (result.error)
				toast.error("Saved, but the DNS sync failed", result.error);
		},
	});

	const fix = useMutation({
		mutationFn: async (adopt: boolean) => {
			const { data } = await fixDomainDns({
				path: { domainId: domain.id },
				body: { adopt },
			});
			return data;
		},
		onSuccess: async () => {
			await refresh();
			toast.success(
				"DNS fixed",
				`${domain.hostname} now points at the server.`,
			);
		},
	});

	if (!configured) return null;
	const status: DnsStatus | undefined = live.data;
	const cached = domain.dns ?? null;
	const info = dnsStateInfo(status?.status ?? cached?.status);
	const inZone = (status?.status ?? cached?.status) !== "not_in_zone";
	const action = status ? fixAction(status) : null;
	const busy = update.isPending || fix.isPending;
	const proxied = status?.proxied ?? cached?.proxied ?? false;
	const tunnel = status?.tunnel === true;
	const records = status?.records ?? [];
	const warnings = status?.warnings ?? [];
	const error = status?.error || cached?.error || "";
	const zoneName = status?.zoneName || cached?.zoneName || "";
	const conflict = (status?.status ?? cached?.status) === "conflict";

	const runFix = () => {
		if (!action) return;
		if (action.kind === "fix") fix.mutate(false);
		if (action.kind === "adopt")
			void confirm({
				title: conflict
					? `Replace the record for ${domain.hostname}?`
					: `Adopt the record for ${domain.hostname}?`,
				body: action.confirm,
				cta: action.label,
				danger: conflict,
				onConfirm: async () => {
					await fix.mutateAsync(true);
				},
			});
	};

	return (
		<div className="flex flex-col gap-2.5" data-section="cloudflare-dns">
			<div className="flex flex-wrap items-center gap-2">
				<CloudIcon aria-hidden className="size-4 text-ink-3" />
				<span className="font-medium text-[13px] text-ink-1">
					Cloudflare DNS
				</span>
				{info ? (
					<Badge tone={info.tone}>
						<StatusIcon kind={statusKindOfTone(info.tone)} label="" />
						{info.label}
					</Badge>
				) : live.isPending ? (
					<Spinner />
				) : null}
				{zoneName ? (
					<span className="text-ink-3 text-xs">zone {zoneName}</span>
				) : null}
				<Button
					variant="ghost"
					size="icon-sm"
					aria-label="Check DNS again"
					disabled={live.isFetching}
					onClick={() => void live.refetch()}
				>
					{live.isFetching ? <Spinner /> : <RefreshCwIcon aria-hidden />}
				</Button>
			</div>
			{live.isError ? (
				<FieldError>{errorMessage(live.error)}</FieldError>
			) : conflict && status?.message ? (
				<Alert variant="warning">
					<AlertDescription>{status.message}</AlertDescription>
				</Alert>
			) : (
				<p className="text-ink-3 text-xs leading-[1.5]">
					{status?.message || info?.hint || "Reading the zone…"}
				</p>
			)}
			{inZone && !conflict && zoneName ? (
				<p className="text-ink-2 text-xs leading-[1.5]">
					NebulaCtrl manages this record in your Cloudflare zone {zoneName}.
				</p>
			) : null}
			{inZone && (status || cached) ? (
				<div className="flex flex-col gap-3">
					<div className="flex flex-wrap items-center justify-between gap-3">
						<div className="flex flex-col gap-0.5">
							<span className="text-[13px] text-ink-1">Proxy</span>
							<span className="text-ink-3 text-xs">
								{tunnel
									? "Tunnel records are always proxied."
									: `DNS only, or through Cloudflare (orange cloud). ${APPLIES_NOW}, in your zone.`}
							</span>
						</div>
						<Segmented
							aria-label="Cloudflare proxy"
							font="sans"
							density="regular"
							value={tunnel || proxied ? "proxied" : "dns"}
							onValueChange={(value) => {
								if (!tunnel) update.mutate({ proxied: value === "proxied" });
							}}
							options={[
								{ value: "dns", label: "DNS only", disabled: busy || tunnel },
								{ value: "proxied", label: "Proxied", disabled: busy },
							]}
						/>
					</div>
					{status?.originLock ? (
						<OriginLockSection
							hostname={domain.hostname}
							domainId={domain.id}
							lock={status.originLock}
							proxied={tunnel || proxied}
							busy={busy}
							onOptIn={(optIn) => update.mutate({ originLock: optIn })}
						/>
					) : null}
					{records.length > 0 || status?.desired?.content ? (
						<ul className="flex flex-col gap-1">
							{records.map((r) => (
								<RecordLine
									key={`${r.type}:${r.content}`}
									type={r.type}
									content={r.content}
									proxied={r.proxied}
									owned={r.owned}
									tag={r.owned ? "in zone" : "not NebulaCtrl"}
								/>
							))}
							{status?.desired?.content && status.status !== "managed" ? (
								<RecordLine
									type={status.desired.type}
									content={status.desired.content}
									proxied={status.effectiveProxied}
									owned
									tag="wanted"
								/>
							) : null}
						</ul>
					) : null}
				</div>
			) : null}
			{warnings.map((warning) => (
				<Alert key={warning} variant="warning">
					<AlertDescription>{warning}</AlertDescription>
				</Alert>
			))}
			{error ? (
				<Alert variant="destructive">
					<AlertDescription>
						The last sync failed: {error}. It is retried in the background.
					</AlertDescription>
				</Alert>
			) : null}
			{action && (action.kind === "fix" || action.kind === "adopt") ? (
				<div className="flex items-center gap-2">
					{/* Adopting or replacing a record NebulaCtrl did not create needs
					    the admin scope: the API answers 403 to anyone else. */}
					{action.kind === "fix" || isAdmin ? (
						<Button
							variant="secondary"
							size="sm"
							disabled={busy}
							onClick={runFix}
						>
							{fix.isPending ? <Spinner /> : null}
							{action.label}
						</Button>
					) : null}
					{action.kind === "adopt" ? (
						<span className="text-ink-3 text-xs">
							{conflict
								? "NebulaCtrl leaves these records as they are until they are replaced."
								: "The existing record is not NebulaCtrl's: it is left alone unless it is adopted."}
							{isAdmin ? "" : " An admin can do this."}
						</span>
					) : null}
				</div>
			) : null}
			{update.isError ? (
				<FieldError>{errorMessage(update.error)}</FieldError>
			) : null}
			{fix.isError ? <FieldError>{errorMessage(fix.error)}</FieldError> : null}
		</div>
	);
}
