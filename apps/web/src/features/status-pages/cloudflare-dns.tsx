import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { CloudIcon, RefreshCwIcon, ShieldCheckIcon } from "lucide-react";
import { errorCode, errorMessage } from "#/api/client-config";
import {
	fixStatusPageCloudflare,
	updateStatusPageCloudflare,
} from "#/api/gen/sdk.gen";
import type { GetStatusPageCloudflareResponse } from "#/api/gen/types.gen";
import { invalidateOperations } from "#/api/invalidate";
import { useConfirm } from "#/components/common/confirm-dialog";
import { CredentialHelp } from "#/components/common/credential-help";
import { Alert, AlertDescription } from "#/components/ui/alert";
import { Badge } from "#/components/ui/badge";
import { Button } from "#/components/ui/button";
import { FieldError } from "#/components/ui/field";
import { statusKindOfTone } from "#/components/ui/lamp";
import { Segmented } from "#/components/ui/segmented";
import { Skeleton } from "#/components/ui/skeleton";
import { Spinner } from "#/components/ui/spinner";
import { StatusIcon } from "#/components/ui/status-icon";
import { Switch } from "#/components/ui/switch";
import { toast } from "#/components/ui/toast";
import {
	dnsStateInfo,
	needsAopPermission,
	ORIGIN_LOCK_OPT_OUT_WARNING,
	proxyLabel,
} from "#/features/cloudflare/model";
import { useIsAdmin } from "#/features/proxy/use-admin";
import { cloudflareAopGuide } from "#/lib/credential-links";
import { DnsCheck } from "./dns-check";
import { dnsPanelMode, fixProblem, pageFixAction, pageLockInfo } from "./model";
import { statusPageCloudflareQuery } from "./queries";

/** Everything a Cloudflare change makes stale on this page's DNS panel. */
const CLOUDFLARE_PAGE_OPERATIONS = [
	"getStatusPageCloudflare",
	"checkStatusPageDns",
] as const;

type PageCloudflare = GetStatusPageCloudflareResponse;

/**
 * The page's DNS panel. A hostname in a connected Cloudflare zone gets the
 * Cloudflare controls; any other hostname (or no connection, or a refused
 * read) gets the records to create by hand.
 */
export function DnsPanel({
	pageId,
	hostname,
}: {
	pageId: string;
	hostname: string;
}) {
	const cloudflare = useQuery(statusPageCloudflareQuery(pageId));
	if (cloudflare.isPending) {
		return (
			<Skeleton
				className="h-28 rounded-card"
				role="status"
				aria-label="Reading Cloudflare"
			/>
		);
	}
	if (cloudflare.data && dnsPanelMode(cloudflare.data) === "cloudflare") {
		return (
			<CloudflareDns
				pageId={pageId}
				hostname={hostname}
				status={cloudflare.data}
				fetching={cloudflare.isFetching}
				onRefresh={() => void cloudflare.refetch()}
			/>
		);
	}
	return <DnsCheck pageId={pageId} />;
}

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
		<li className="flex flex-wrap items-center gap-2 font-mono text-xs text-ink-2">
			<span className="min-w-24 text-ink-3">{tag}</span>
			<span>{type}</span>
			<span className="text-ink-1">{content}</span>
			<Badge>{proxyLabel(proxied)}</Badge>
			{owned ? <Badge variant="accent">managed by NebulaCtrl</Badge> : null}
		</li>
	);
}

function CloudflareDns({
	pageId,
	hostname,
	status,
	fetching,
	onRefresh,
}: {
	pageId: string;
	hostname: string;
	status: PageCloudflare;
	fetching: boolean;
	onRefresh: () => void;
}) {
	const queryClient = useQueryClient();
	const confirm = useConfirm();
	const refresh = () =>
		invalidateOperations(queryClient, CLOUDFLARE_PAGE_OPERATIONS);

	const update = useMutation({
		mutationFn: async (body: { proxied?: boolean; originLock?: boolean }) => {
			const { data } = await updateStatusPageCloudflare({
				path: { pageId },
				body,
			});
			return data;
		},
		onSuccess: async (result) => {
			await refresh();
			if (result.error)
				toast.error("Saved, but the DNS sync failed", result.error);
		},
		onError: () => void refresh(),
	});

	const fix = useMutation({
		mutationFn: async (adopt: boolean) => {
			const { data } = await fixStatusPageCloudflare({
				path: { pageId },
				body: { adopt },
			});
			return data;
		},
		onSuccess: async () => {
			await refresh();
			toast.success("DNS fixed", `${hostname} now points at the server.`);
		},
		onError: () => void refresh(),
	});

	const info = dnsStateInfo(status.status);
	const action = pageFixAction(status);
	const lock = pageLockInfo(status.originLock);
	const isAdmin = useIsAdmin();
	const busy = update.isPending || fix.isPending;
	const desired = status.desiredRecords ?? [];
	const records = status.records ?? [];
	const warnings = status.warnings ?? [];
	const conflict = status.status === "conflict";

	const runFix = () => {
		if (action.kind === "fix") fix.mutate(false);
		if (action.kind === "adopt")
			void confirm({
				title: conflict
					? `Replace the record for ${hostname}?`
					: `Adopt the record for ${hostname}?`,
				body: action.confirm,
				cta: action.label,
				danger: conflict,
				onConfirm: async () => {
					await fix.mutateAsync(true);
				},
			});
	};

	const toggleLock = (next: boolean) => {
		if (next || !status.proxied) {
			update.mutate({ originLock: next });
			return;
		}
		void confirm({
			title: `Open ${hostname} to the internet?`,
			body: ORIGIN_LOCK_OPT_OUT_WARNING,
			cta: "Turn the lock off",
			dismiss: "Keep it locked",
			danger: true,
			onConfirm: async () => {
				update.mutate({ originLock: false });
			},
		});
	};

	return (
		<div className="flex flex-col gap-3" data-section="cloudflare-dns">
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
				) : null}
				{status.zoneName ? (
					<span className="text-ink-3 text-xs">zone {status.zoneName}</span>
				) : null}
				<Button
					variant="ghost"
					size="icon-sm"
					aria-label="Read the zone again"
					disabled={fetching}
					onClick={onRefresh}
				>
					{fetching ? <Spinner /> : <RefreshCwIcon aria-hidden />}
				</Button>
			</div>
			{conflict && status.message ? (
				<Alert variant="warning">
					<AlertDescription>{status.message}</AlertDescription>
				</Alert>
			) : (
				<p className="m-0 text-ink-3 text-xs leading-[1.5]">
					{status.message || info?.hint}
				</p>
			)}
			{!conflict && status.zoneName ? (
				<p className="m-0 text-ink-2 text-xs leading-[1.5]">
					NebulaCtrl manages this record in your Cloudflare zone{" "}
					{status.zoneName}.
				</p>
			) : null}

			<div className="flex flex-wrap items-center justify-between gap-3">
				<div className="flex flex-col gap-0.5">
					<span className="text-[13px] text-ink-1">Proxy</span>
					<span className="text-ink-3 text-xs">
						DNS only, or through Cloudflare (orange cloud).
					</span>
				</div>
				<Segmented
					aria-label="Cloudflare proxy"
					font="sans"
					density="regular"
					value={status.proxied ? "proxied" : "dns"}
					onValueChange={(value) =>
						update.mutate({ proxied: value === "proxied" })
					}
					options={[
						{ value: "dns", label: "DNS only", disabled: busy },
						{ value: "proxied", label: "Proxied", disabled: busy },
					]}
				/>
			</div>

			<div
				className="flex flex-col gap-2.5 rounded-card border border-line p-3"
				data-section="origin-lock"
			>
				<div className="flex flex-wrap items-center gap-2">
					<ShieldCheckIcon aria-hidden className="size-4 text-ink-3" />
					<span className="font-medium text-[13px] text-ink-1">
						Origin locked to Cloudflare
					</span>
					<Badge>
						<StatusIcon kind={lock.kind} label="" />
						{lock.label}
					</Badge>
				</div>
				{lock.reason ? (
					<p className="m-0 text-ink-3 text-xs leading-[1.5]">{lock.reason}</p>
				) : null}
				{lock.aop ? (
					<p className="m-0 text-ink-3 text-xs leading-[1.5]">{lock.aop}</p>
				) : null}
				{needsAopPermission(status.originLock) ? (
					<CredentialHelp guide={cloudflareAopGuide()} fieldIds={[]} />
				) : null}
				<div className="flex flex-wrap items-center justify-between gap-3">
					<div className="flex flex-col gap-0.5">
						<label
							htmlFor={`page-cf-lock-${pageId}`}
							className="text-[13px] text-ink-1"
						>
							Lock the origin to Cloudflare
						</label>
						<span className="text-ink-3 text-xs">
							On by default for a proxied, published page.
						</span>
					</div>
					<Switch
						id={`page-cf-lock-${pageId}`}
						checked={status.originLock.optIn}
						disabled={busy}
						onCheckedChange={toggleLock}
					/>
				</div>
				{!status.originLock.optIn && status.proxied ? (
					<Alert variant="warning">
						<AlertDescription>{ORIGIN_LOCK_OPT_OUT_WARNING}</AlertDescription>
					</Alert>
				) : null}
			</div>

			{desired.length > 0 || records.length > 0 ? (
				<ul className="m-0 flex list-none flex-col gap-1 p-0">
					{records.map((record) => (
						<RecordLine
							key={`in-zone:${record.type}:${record.content}`}
							type={record.type}
							content={record.content}
							proxied={record.proxied}
							owned={record.owned}
							tag={record.owned ? "in zone" : "not NebulaCtrl"}
						/>
					))}
					{status.status !== "managed"
						? desired.map((record) => (
								<RecordLine
									key={`wanted:${record.type}:${record.content}`}
									type={record.type}
									content={record.content}
									proxied={status.effectiveProxied}
									owned
									tag="wanted"
								/>
							))
						: null}
				</ul>
			) : null}

			{warnings.map((warning) => (
				<Alert key={warning} variant="warning">
					<AlertDescription>{warning}</AlertDescription>
				</Alert>
			))}
			{status.error ? (
				<Alert variant="destructive">
					<AlertDescription>
						The last sync failed: {status.error}. It is retried in the
						background.
					</AlertDescription>
				</Alert>
			) : null}
			{action.kind === "fix" || action.kind === "adopt" ? (
				<div className="flex flex-wrap items-center gap-2">
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
			{fix.isError ? (
				<FieldError>
					{fixProblem(errorCode(fix.error)) ?? errorMessage(fix.error)}
				</FieldError>
			) : null}

			<DnsCheck pageId={pageId} managed />
		</div>
	);
}
