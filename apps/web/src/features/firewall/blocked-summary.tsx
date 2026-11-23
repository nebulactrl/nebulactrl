import type { UseQueryResult } from "@tanstack/react-query";
import { Link, useParams } from "@tanstack/react-router";
import { cn } from "cn";
import type { ReactNode } from "react";
import { errorMessage } from "#/api/client-config";
import type { Error as ApiError, FirewallBlocks } from "#/api/gen/types.gen";
import type { Service } from "#/api/models";
import { Button } from "#/components/ui/button";
import { Segmented } from "#/components/ui/segmented";
import { Skeleton } from "#/components/ui/skeleton";
import {
	BLOCK_RANGE_PHRASE,
	BLOCK_RANGES,
	type BlockRange,
	blockedCountText,
	blockedLogsQuery,
	blockedRuleLabel,
	removedBlocks,
} from "./model";

/** The environment the reader is in: the Logs page lives under it. */
function useEnvSlug(): string | undefined {
	return useParams({ strict: false }).envSlug;
}

/**
 * A link to the Logs page filtered to the requests the firewall blocked for
 * `service`: every rule's, or just `ruleId`'s. Outside an environment (no
 * Logs page to go to) it is plain text.
 */
function BlockedLogsLink({
	service,
	ruleId,
	children,
	className,
	label,
}: {
	service: Service;
	ruleId?: string;
	children: ReactNode;
	className?: string;
	label?: string;
}) {
	const envSlug = useEnvSlug();
	if (envSlug === undefined) {
		return <span className={className}>{children}</span>;
	}
	return (
		<Link
			to="/p/$projectId/$envSlug/logs"
			params={{ projectId: service.projectId, envSlug }}
			search={{
				q: blockedLogsQuery(service.name, ruleId),
				sources: "requests",
			}}
			aria-label={label}
			className={className}
		>
			{children}
		</Link>
	);
}

/**
 * The number of requests a rule blocked in the window, in the rule's row:
 * muted, tabular, "—" when it blocked none (or nothing is counted), and a link
 * to those requests in Logs when it did.
 */
export function RuleBlockCount({
	service,
	ruleId,
	ruleName,
	count,
}: {
	service: Service;
	ruleId: string;
	ruleName: string;
	/** Absent: none, or not counted. */
	count: number | undefined;
}) {
	const base =
		"w-14 shrink-0 text-right font-mono text-ink-3 text-xs tabular-nums";
	if (!count) {
		return (
			<span
				className={cn(base, "text-ink-4")}
				title="No blocked requests in this range"
			>
				—
			</span>
		);
	}
	return (
		<BlockedLogsLink
			service={service}
			ruleId={ruleId}
			label={`${ruleName}: ${blockedCountText(count)} blocked, see them in Logs`}
			className={cn(base, "rounded-ctl hover:text-ink-1 hover:underline")}
		>
			{blockedCountText(count)}
		</BlockedLogsLink>
	);
}

/**
 * Settings → Firewall, "Blocked in the last 24 hours: N" with a range switch,
 * a link to the blocked requests in Logs, and the rules that blocked requests
 * and have since been removed. The per-rule counts of the rows read the same
 * answer.
 */
export function BlockedSummary({
	service,
	blocks,
	range,
	onRangeChange,
	serverName,
}: {
	service: Service;
	blocks: UseQueryResult<FirewallBlocks, ApiError>;
	range: BlockRange;
	onRangeChange(next: BlockRange): void;
	serverName: string;
}) {
	const data = blocks.data;
	const phrase = BLOCK_RANGE_PHRASE[range];
	const removed = removedBlocks(data);
	const counted = data?.trafficAnalytics !== false;

	let body: ReactNode = null;
	if (blocks.isPending) {
		body = (
			<div aria-busy="true">
				<Skeleton className="h-5 w-64 rounded-ctl" />
			</div>
		);
	} else if (blocks.isError && !data) {
		body = (
			<div className="flex flex-wrap items-center gap-2 text-destructive-text text-xs">
				<span role="alert">
					Blocked requests could not be read: {errorMessage(blocks.error)}
				</span>
				<Button
					type="button"
					variant="secondary"
					size="sm"
					onClick={() => void blocks.refetch()}
				>
					Retry
				</Button>
			</div>
		);
	} else if (data && !counted) {
		body = (
			<p className="m-0 text-ink-3 text-xs leading-[1.45]">
				Blocked requests are not counted: traffic analytics is off
				{serverName ? (
					<>
						{" "}
						on <span className="font-mono text-ink-2">{serverName}</span>
					</>
				) : null}
				. The firewall still blocks.{" "}
				{service.serverId ? (
					<Link
						to="/servers/$serverId/settings/$section"
						params={{ serverId: service.serverId, section: "monitoring" }}
						className="text-accent-ink underline"
					>
						Turn it on in the server's settings
					</Link>
				) : (
					"Deploy the service to a server to count them."
				)}
			</p>
		);
	} else if (data) {
		body = (
			<>
				{data.total > 0 ? (
					<BlockedLogsLink
						service={service}
						className="text-accent-ink text-xs underline"
					>
						See blocked requests in Logs
					</BlockedLogsLink>
				) : (
					<p className="m-0 text-ink-3 text-xs">
						Nothing was blocked in {phrase}. Counts start when the proxy first
						reports them.
					</p>
				)}
				{removed.length > 0 ? (
					<ul
						aria-label="Removed rules that blocked requests"
						className="m-0 flex list-none flex-col gap-1 p-0"
					>
						{removed.map((row) => (
							<li
								key={row.id}
								className="flex items-baseline justify-between gap-3 text-ink-3 text-xs"
							>
								<span className="min-w-0 truncate">
									{blockedRuleLabel(row)}
								</span>
								<RuleBlockCount
									service={service}
									ruleId={row.id}
									ruleName={blockedRuleLabel(row)}
									count={row.requests}
								/>
							</li>
						))}
					</ul>
				) : null}
			</>
		);
	}

	return (
		<fieldset
			aria-label="Blocked requests"
			className="m-0 flex min-w-0 flex-col gap-2 border-0 p-0"
		>
			<div className="flex flex-wrap items-center justify-between gap-x-4 gap-y-2">
				<p className="m-0 text-[13px] text-ink-2 leading-[1.5]">
					Blocked in {phrase}:{" "}
					<span
						className={cn(
							"font-medium font-mono text-ink-1 tabular-nums",
							blocks.isPlaceholderData && "opacity-60",
						)}
					>
						{data && counted ? data.total.toLocaleString("en-US") : "—"}
					</span>
				</p>
				<Segmented
					aria-label="Blocked requests range"
					value={range}
					onValueChange={onRangeChange}
					options={BLOCK_RANGES.map((value) => ({ value, label: value }))}
				/>
			</div>
			{body}
		</fieldset>
	);
}
