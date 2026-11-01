import { Link } from "@tanstack/react-router";
import { cn } from "cn";
import { ArrowRightIcon } from "lucide-react";
import { KpiNumber } from "#/components/common/kpi-number";
import { Skeleton } from "#/components/ui/skeleton";
import { latencyText } from "#/features/analytics/latency-text";
import { useTrafficReport } from "#/features/analytics/queries";
import {
	compactNumber,
	formatByteCount,
} from "#/features/metrics/chart-format";
import { EYEBROW } from "#/features/metrics/ui/eyebrow";
import type { ServerView } from "../model";

/**
 * The last 24 hours of traffic on this server in four numbers (requests,
 * visitors, bandwidth and p95 latency), as one link to the Traffic tab. It
 * asks for the same report the tab opens with, so the tab is warm when it is
 * reached. With analytics off it says so and links to where it is turned on.
 */
export function TrafficSummary({ server }: { server: ServerView }) {
	const enabled = server.settings.trafficAnalyticsEnabled;
	const reported = server.lastSeenAt !== null && server.status !== "pending";
	const traffic = useTrafficReport(
		{ serverId: server.id },
		{ range: "24h", compare: true, host: null, pagesOnly: true },
		enabled && reported,
	);
	const params = { serverId: server.id };

	if (!enabled) {
		return (
			<p className="surface-raised flex flex-wrap items-center justify-between gap-x-4 gap-y-2 rounded-card px-4 py-3 text-ink-3 text-sm">
				Traffic analytics is off for this server.
				<Link
					to="/servers/$serverId/settings/$section"
					params={{ ...params, section: "monitoring" }}
					className="rounded-sm text-accent-ink underline underline-offset-2 outline-none focus-visible:focus-ring"
				>
					Turn it on in settings
				</Link>
			</p>
		);
	}
	if (reported && traffic.isPending) {
		return (
			<Skeleton
				className="h-[74px] rounded-card"
				role="status"
				aria-label="Loading traffic"
			/>
		);
	}

	// A host that never reported has no traffic to count: zeros there would
	// read as measurements.
	const summary = reported ? traffic.data?.summary : undefined;
	const items: [string, string][] = [
		["Requests", summary ? compactNumber(summary.requests) : "–"],
		["Visitors", summary ? compactNumber(summary.visitors) : "–"],
		["Bandwidth", summary ? formatByteCount(summary.bytesOut) : "–"],
		[
			"p95 latency",
			summary ? latencyText(summary.requests, summary.p95Ms) : "–",
		],
	];
	return (
		<Link
			to="/servers/$serverId/traffic"
			params={params}
			className="surface-raised group flex items-center gap-4 rounded-card px-4 py-3 text-ink-1 outline-none transition-[border-color] duration-quick ease-out hover:border-line-strong focus-visible:focus-ring"
		>
			<dl className="grid min-w-0 flex-1 grid-cols-2 gap-x-4 gap-y-3 sm:grid-cols-4">
				{items.map(([label, value]) => (
					<div key={label} className="flex min-w-0 flex-col gap-2">
						<dt className={cn(EYEBROW, "truncate")}>{label}</dt>
						<dd className="type-metric truncate">
							<KpiNumber text={value} />
						</dd>
					</div>
				))}
			</dl>
			<span className="flex shrink-0 items-center gap-1.5 text-ink-3 text-xs transition-colors duration-quick ease-out group-hover:text-ink-1 max-sm:sr-only">
				Last 24 hours
				<ArrowRightIcon aria-hidden className="size-3.5" />
			</span>
		</Link>
	);
}
