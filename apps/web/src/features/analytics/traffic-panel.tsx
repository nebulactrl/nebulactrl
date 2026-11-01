import { useQuery } from "@tanstack/react-query";
import { Link } from "@tanstack/react-router";
import { useState } from "react";
import { serviceQuery } from "#/api/hooks";
import { CopyButton } from "#/components/common/copy-button";
import { EmptyState } from "#/components/common/empty-state";
import { ErrorState } from "#/components/common/error-state";
import { Relative } from "#/components/common/relative";
import { Alert, AlertDescription } from "#/components/ui/alert";
import { Button } from "#/components/ui/button";
import { CheckLabel } from "#/components/ui/check-label";
import { Skeleton, SkeletonReveal } from "#/components/ui/skeleton";
import { isDelayed } from "../metrics/model";
import {
	isLiveRange,
	isTrafficRange,
	TRAFFIC_RANGES,
	type TrafficRange,
} from "../metrics/ranges";
import { OptionSelect } from "../metrics/ui/option-select";
import { RangeControl } from "../metrics/ui/range-control";
import { usePersisted } from "../metrics/use-persisted";
import { serverViewQuery } from "../servers/queries";
import { type TrafficScope, useTrafficReport } from "./queries";
import { TrafficView } from "./traffic-view";

export type { TrafficScope } from "./queries";

export interface TrafficPanelProps {
	scope: TrafficScope;
	/** Called by the "Add a domain" button when a service has no hostname. */
	onAddDomain?: () => void;
	/** `summary` shows the KPIs and status chart only (environment overview). */
	variant?: "full" | "summary";
}

const ALL_HOSTS = "__all__";

function Notice({
	tone = "info",
	children,
}: {
	tone?: "info" | "warning" | "error";
	children: React.ReactNode;
}) {
	return (
		<Alert
			variant={
				tone === "error"
					? "destructive"
					: tone === "warning"
						? "warning"
						: "info"
			}
			role={tone === "info" ? "status" : "alert"}
		>
			<AlertDescription>{children}</AlertDescription>
		</Alert>
	);
}

/**
 * Traffic analytics for a service, a server (all hostnames, plus unrouted
 * ones) or a whole environment. Handles the states in order: no domain,
 * analytics off for the server, proxy error, server offline, zero requests;
 * otherwise the full dashboard with range, compare and host controls.
 */
export function TrafficPanel({
	scope,
	onAddDomain,
	variant = "full",
}: TrafficPanelProps) {
	const kind =
		"serviceId" in scope
			? "service"
			: "serverId" in scope
				? "server"
				: "environment";
	const [range, setRange] = usePersisted<TrafficRange>(
		`traffic-range-${kind}`,
		"24h",
		isTrafficRange,
	);
	const [compare, setCompare] = useState(true);
	const [pagesOnly, setPagesOnly] = useState(true);
	const [host, setHost] = useState<string | null>(null);

	const service = useQuery({
		...serviceQuery("serviceId" in scope ? scope.serviceId : ""),
		enabled: "serviceId" in scope,
	});
	const serverId =
		"serverId" in scope
			? scope.serverId
			: (service.data?.serverId ?? undefined);
	const server = useQuery({
		...serverViewQuery(serverId ?? ""),
		enabled: serverId !== undefined,
	});

	const domains = service.data?.domains ?? [];
	const noDomain =
		"serviceId" in scope && service.isSuccess && domains.length === 0;
	const disabled = server.data?.settings.trafficAnalyticsEnabled === false;
	const blocked = noDomain || disabled;

	const traffic = useTrafficReport(
		scope,
		{ range, compare, host, pagesOnly },
		!blocked && ("serviceId" in scope ? service.isSuccess : true),
	);
	const report = traffic.data;
	const live = isLiveRange("traffic", range);
	const offline = server.data?.status === "offline";
	const stepMs = (report?.range.stepSeconds ?? 60) * 1000;
	const newest = report?.series?.at(-1);
	const delayed = isDelayed(
		newest ? Date.parse(newest.ts) : undefined,
		stepMs,
		traffic.dataUpdatedAt || Date.now(),
		2,
	);

	const header = (
		<div className="flex flex-wrap items-center justify-between gap-x-4 gap-y-2.5">
			<RangeControl
				ranges={TRAFFIC_RANGES}
				value={range}
				onChange={setRange}
				live={live && !blocked && !offline}
				delayed={delayed}
			/>
			<div className="flex flex-wrap items-center gap-3">
				{domains.length > 1 ? (
					<OptionSelect
						aria-label="Hostname"
						value={host ?? ALL_HOSTS}
						onChange={(next) => setHost(next === ALL_HOSTS ? null : next)}
						options={[
							{ value: ALL_HOSTS, label: "All hosts" },
							...domains.map((d) => ({ value: d.hostname, label: d.hostname })),
						]}
					/>
				) : null}
				<CheckLabel
					checked={compare}
					onCheckedChange={setCompare}
					className="text-ink-3 text-xs"
				>
					Compare to previous period
				</CheckLabel>
			</div>
		</div>
	);

	let body: React.ReactNode;
	if (noDomain) {
		body = (
			<EmptyState
				illustration="planet"
				frame="none"
				className="surface-well rounded-card py-10"
				title="Add a domain to collect traffic analytics"
				body="Traffic is counted at the server's proxy by hostname. A service with no domain has no public requests to count."
				action={
					onAddDomain ? (
						<Button size="sm" onClick={onAddDomain}>
							Add a domain
						</Button>
					) : undefined
				}
			/>
		);
	} else if (disabled && server.data) {
		body = (
			<EmptyState
				illustration="planet"
				frame="none"
				className="surface-well rounded-card py-10"
				title={`Traffic analytics is off for ${server.data.name}`}
				body="Turn it on in the server's settings. It needs no proxy restart, and only aggregate counts are kept."
				action={
					<Button
						variant="secondary"
						size="sm"
						nativeButton={false}
						render={
							<Link
								to="/servers/$serverId/settings/$section"
								params={{ serverId: server.data.id, section: "monitoring" }}
							/>
						}
					>
						Open server settings
					</Button>
				}
			/>
		);
	} else if (traffic.isError && !report) {
		body = (
			<ErrorState
				title="Traffic did not load"
				body="The control plane did not answer. Try again in a moment."
				retry={() => void traffic.refetch()}
			/>
		);
	} else if (
		report &&
		report.summary.requests === 0 &&
		!report.unrouted?.requests
	) {
		const sample = host ?? domains[0]?.hostname;
		body = (
			<EmptyState
				illustration="planet"
				frame="none"
				className="surface-well rounded-card py-10"
				title="No requests yet"
				body={
					<>
						Nothing reached this {kind} in the last {range}.
						{sample ? (
							<span className="mt-2 flex items-center justify-center gap-1.5">
								<code className="surface-well rounded-ctl px-2 py-1 font-mono text-ink-1 text-xs">
									curl https://{sample}
								</code>
								<CopyButton
									value={`curl https://${sample}`}
									label="Copy curl command"
									variant="ghost"
									size="icon-sm"
								/>
							</span>
						) : null}
					</>
				}
			/>
		);
	} else if (report) {
		body = (
			<div
				className={
					offline ? "opacity-70 transition-opacity duration-quick" : undefined
				}
			>
				<TrafficView
					report={report}
					range={range}
					updatedAt={traffic.dataUpdatedAt}
					pagesOnly={pagesOnly}
					onPagesOnlyChange={setPagesOnly}
					geoServerId={server.data?.id}
					variant={variant}
					onHostSelect={domains.length > 1 ? setHost : undefined}
				/>
			</div>
		);
	}

	return (
		<div className="@container flex flex-col gap-4">
			{header}
			{server.data?.proxy.status === "error" ? (
				<Notice tone="error">
					The proxy on {server.data.name} reports an error
					{server.data.proxy.error ? `: ${server.data.proxy.error}` : ""}.
					Traffic cannot be collected until it is fixed.
				</Notice>
			) : null}
			{offline && server.data ? (
				<Notice tone="warning">
					{server.data.name} is offline
					{server.data.lastSeenAt ? (
						<>
							{" "}
							(last seen <Relative iso={server.data.lastSeenAt} />)
						</>
					) : null}
					. The figures below are the last that arrived.
				</Notice>
			) : null}
			<SkeletonReveal
				loading={!blocked && traffic.isPending}
				skeleton={
					<div
						className="flex flex-col gap-4"
						role="status"
						aria-label="Loading traffic"
					>
						<div className="grid grid-cols-2 gap-2.5 @3xl:grid-cols-4">
							{["requests", "visitors", "bandwidth", "latency"].map((id) => (
								<Skeleton key={id} className="h-[150px] rounded-card" />
							))}
						</div>
						<Skeleton className="h-[260px] rounded-card" />
						<Skeleton className="h-[240px] rounded-card" />
					</div>
				}
			>
				{() => body}
			</SkeletonReveal>
		</div>
	);
}
