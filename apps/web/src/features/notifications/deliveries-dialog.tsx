import { keepPreviousData, useInfiniteQuery } from "@tanstack/react-query";
import { cn } from "cn";
import { type ReactNode, useState } from "react";
import { listNotificationDeliveriesInfiniteOptions } from "#/api/gen/@tanstack/react-query.gen";
import type { ChannelDelivery, ChannelView } from "#/api/gen/types.gen";
import { EmptyState } from "#/components/common/empty-state";
import { ErrorState } from "#/components/common/error-state";
import { Relative } from "#/components/common/relative";
import { Badge } from "#/components/ui/badge";
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
import { Segmented } from "#/components/ui/segmented";
import { Skeleton } from "#/components/ui/skeleton";
import { Spinner } from "#/components/ui/spinner";
import { StatusIcon } from "#/components/ui/status-icon";
import { absoluteTime } from "#/lib/format";
import {
	attemptsLabel,
	DELIVERY_FILTERS,
	DELIVERY_STATUS,
	type DeliveryFilter,
} from "./model";

const PAGE_SIZE = 30;

function DeliveryRow({ d }: { d: ChannelDelivery }) {
	const status = DELIVERY_STATUS[d.status];
	return (
		<li className="flex flex-col gap-1.5 py-3">
			<div className="flex flex-wrap items-center gap-x-3 gap-y-1">
				<span className="inline-flex items-center gap-2 font-medium text-ink-1 text-xs">
					<StatusIcon kind={status.kind} label="" />
					{status.label}
				</span>
				<span
					className="min-w-0 flex-1 truncate text-ink-1 text-sm"
					title={d.title}
				>
					{d.title}
				</span>
				{d.isTest ? (
					<Badge variant="accent" shape="capsule">
						test
					</Badge>
				) : null}
				<span
					className="shrink-0 font-mono text-2xs text-ink-4"
					title={absoluteTime(d.createdAt)}
				>
					<Relative iso={d.createdAt} />
				</span>
			</div>
			<div className="flex flex-wrap items-center gap-x-3 gap-y-0.5 font-mono text-2xs text-ink-3">
				<span>{d.eventType}</span>
				<span>{attemptsLabel(d)}</span>
				{d.responseCode ? <span>response {d.responseCode}</span> : null}
				{d.status === "retrying" && d.nextAttemptAt ? (
					<span title={absoluteTime(d.nextAttemptAt)}>
						next try <Relative iso={d.nextAttemptAt} />
					</span>
				) : null}
				{d.status === "delivered" && d.deliveredAt ? (
					<span title={absoluteTime(d.deliveredAt)}>
						sent <Relative iso={d.deliveredAt} />
					</span>
				) : null}
			</div>
			{d.lastError ? (
				<p className="m-0 break-words rounded-ctl bg-destructive-surface px-2.5 py-1.5 font-mono text-destructive-text text-xs leading-relaxed">
					{d.lastError}
				</p>
			) : null}
		</li>
	);
}

/**
 * The delivery log of one channel, newest first: status, attempts, response
 * code and the last error (which never contains a credential).
 */
export function DeliveriesDialog({
	channel,
	onClose,
}: {
	channel: ChannelView | null;
	onClose: () => void;
}) {
	const [filter, setFilter] = useState<DeliveryFilter>("all");
	const channelId = channel?.id ?? "";
	const deliveries = useInfiniteQuery({
		...listNotificationDeliveriesInfiniteOptions({
			path: { channelId },
			query: {
				limit: PAGE_SIZE,
				...(filter === "all" ? {} : { status: filter }),
			},
		}),
		enabled: channel !== null,
		initialPageParam: { path: { channelId }, query: { limit: PAGE_SIZE } },
		getNextPageParam: (last) => last.nextCursor || undefined,
		placeholderData: keepPreviousData,
	});
	const rows = (deliveries.data?.pages ?? []).flatMap((p) => p.items ?? []);

	let body: ReactNode;
	if (deliveries.isPending) {
		body = <Skeleton className="h-40 rounded-card" />;
	} else if (deliveries.isError && rows.length === 0) {
		body = (
			<ErrorState
				frame="raised"
				title="The delivery log did not load"
				body="The control plane did not answer. Nothing was changed."
				retry={() => void deliveries.refetch()}
			/>
		);
	} else if (rows.length === 0) {
		body = (
			<EmptyState
				frame="dashed"
				illustration="folder"
				title={filter === "all" ? "Nothing sent yet" : "Nothing in this state"}
				body={
					filter === "all"
						? "Deliveries appear here as soon as an event matches this channel's rules, or when you send a test."
						: "No delivery of this channel is in that state right now."
				}
			/>
		);
	} else {
		body = (
			<ul
				className={cn(
					"m-0 flex list-none flex-col divide-y divide-line-faint p-0 transition-opacity duration-quick motion-reduce:transition-none",
					deliveries.isPlaceholderData && "opacity-60",
				)}
				aria-busy={deliveries.isPlaceholderData}
			>
				{rows.map((d) => (
					<DeliveryRow key={d.id} d={d} />
				))}
			</ul>
		);
	}

	return (
		<Dialog
			open={channel !== null}
			onOpenChange={(open) => {
				if (!open) {
					setFilter("all");
					onClose();
				}
			}}
		>
			<DialogContent size="lg">
				<DialogHeader>
					<DialogTitle>Delivery log · {channel?.name}</DialogTitle>
					<DialogDescription>
						Every message sent to this channel, kept for 30 days. A failed send
						is retried up to six times over about an hour.
					</DialogDescription>
				</DialogHeader>
				<DialogBody>
					<div>
						<Segmented<DeliveryFilter>
							aria-label="Filter by status"
							font="sans"
							value={filter}
							onValueChange={setFilter}
							options={DELIVERY_FILTERS.map((f) => ({
								value: f.value,
								label: f.label,
							}))}
						/>
					</div>
					<div className="max-h-[52vh] overflow-y-auto overscroll-contain">
						{body}
					</div>
				</DialogBody>
				<DialogFooter>
					{deliveries.hasNextPage ? (
						<Button
							variant="secondary"
							disabled={deliveries.isFetchingNextPage}
							onClick={() => void deliveries.fetchNextPage()}
						>
							{deliveries.isFetchingNextPage ? <Spinner /> : null}
							Load older
						</Button>
					) : null}
					<Button variant="primary" onClick={onClose}>
						Done
					</Button>
				</DialogFooter>
			</DialogContent>
		</Dialog>
	);
}
