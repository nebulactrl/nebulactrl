import { keepPreviousData, useInfiniteQuery } from "@tanstack/react-query";
import { type ReactNode, useState } from "react";
import { listGitDeliveriesInfiniteOptions } from "#/api/gen/@tanstack/react-query.gen";
import type { ConnectionView, WebhookDeliveryView } from "#/api/gen/types.gen";
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
import { DisclosureChevron, DisclosurePanel } from "#/components/ui/disclosure";
import { Skeleton } from "#/components/ui/skeleton";
import { Spinner } from "#/components/ui/spinner";
import { absoluteTime } from "#/lib/format";
import { DELIVERY_OUTCOME, shortSha } from "./model";

const PAGE_SIZE = 30;

function DeliveryRow({ d }: { d: WebhookDeliveryView }) {
	const [open, setOpen] = useState(false);
	const outcome = DELIVERY_OUTCOME[d.outcome] ?? {
		label: d.outcome,
		tone: "off" as const,
	};
	const headers = Object.entries(d.headers ?? {}).sort(([a], [b]) =>
		a.localeCompare(b),
	);
	return (
		<li className="flex flex-col gap-1.5 py-3">
			<div className="flex flex-wrap items-center gap-x-3 gap-y-1">
				<Badge tone={outcome.tone} lamp shape="capsule">
					{outcome.label}
				</Badge>
				<span className="font-mono text-ink-1 text-xs">{d.event || "—"}</span>
				{d.repo ? (
					<span className="min-w-0 truncate font-mono text-ink-2 text-xs">
						{d.repo}
						{d.branch ? `@${d.branch}` : ""}
						{d.commitSha ? ` · ${shortSha(d.commitSha)}` : ""}
					</span>
				) : null}
				<span
					className="ml-auto shrink-0 font-mono text-2xs text-ink-4"
					title={absoluteTime(d.receivedAt)}
				>
					<Relative iso={d.receivedAt} />
				</span>
			</div>
			<p className="m-0 break-words text-ink-2 text-xs leading-relaxed">
				{d.summary || "No details."}
				{(d.deployments ?? []).length > 0
					? ` Started ${(d.deployments ?? []).length} deployment${(d.deployments ?? []).length === 1 ? "" : "s"}.`
					: ""}
			</p>
			<div className="flex flex-col">
				<button
					type="button"
					aria-expanded={open}
					onClick={() => setOpen((v) => !v)}
					className="inline-flex w-fit cursor-pointer items-center gap-1 rounded-ctl px-1 text-ink-3 text-xs outline-none hover:text-ink-1 focus-visible:focus-ring"
				>
					<DisclosureChevron open={open} className="size-3.5" />
					Request · HTTP {d.httpStatus} · {d.payloadBytes} bytes
				</button>
				<DisclosurePanel open={open}>
					<dl className="surface-well m-0 mt-1.5 grid grid-cols-[auto_minmax(0,1fr)] gap-x-4 gap-y-1 rounded-ctl p-3 font-mono text-2xs">
						{d.deliveryId ? (
							<>
								<dt className="text-ink-3">delivery</dt>
								<dd className="m-0 break-all text-ink-1">{d.deliveryId}</dd>
							</>
						) : null}
						{headers.map(([k, v]) => (
							<div key={k} className="contents">
								<dt className="text-ink-3">{k}</dt>
								<dd className="m-0 break-all text-ink-2">{v}</dd>
							</div>
						))}
					</dl>
				</DisclosurePanel>
			</div>
		</li>
	);
}

/**
 * The webhook delivery log of a connection, newest first: what arrived, whether
 * it verified, what NebulaCtrl did with it. Headers are redacted.
 */
export function DeliveriesDialog({
	connection,
	onClose,
}: {
	connection: ConnectionView | null;
	onClose: () => void;
}) {
	const connectionId = connection?.id ?? "";
	const deliveries = useInfiniteQuery({
		...listGitDeliveriesInfiniteOptions({
			path: { connectionId },
			query: { limit: PAGE_SIZE },
		}),
		enabled: connection !== null,
		initialPageParam: { path: { connectionId }, query: { limit: PAGE_SIZE } },
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
				body="Reading it needs the admin scope. Nothing was changed."
				retry={() => void deliveries.refetch()}
			/>
		);
	} else if (rows.length === 0) {
		body = (
			<EmptyState
				frame="dashed"
				illustration="folder"
				title="No deliveries yet"
				body="Pushes and pull requests from this provider appear here, with what NebulaCtrl did about each. Nothing arriving? Check that the webhook exists and that its secret matches."
			/>
		);
	} else {
		body = (
			<ul className="m-0 flex list-none flex-col divide-y divide-line-faint p-0">
				{rows.map((d) => (
					<DeliveryRow key={d.id} d={d} />
				))}
			</ul>
		);
	}

	return (
		<Dialog
			open={connection !== null}
			onOpenChange={(open) => !open && onClose()}
		>
			<DialogContent size="lg">
				<DialogHeader>
					<DialogTitle>Webhook deliveries · {connection?.name}</DialogTitle>
					<DialogDescription>
						The last 200 deliveries, kept for 14 days. A delivery whose
						signature fails never triggers anything; a repeated delivery id is
						rejected as a replay.
					</DialogDescription>
				</DialogHeader>
				<DialogBody>
					<div className="max-h-[56vh] overflow-y-auto overscroll-contain">
						{body}
						{deliveries.hasNextPage ? (
							<div className="flex justify-center pt-3">
								<Button
									variant="secondary"
									size="sm"
									disabled={deliveries.isFetchingNextPage}
									onClick={() => void deliveries.fetchNextPage()}
								>
									{deliveries.isFetchingNextPage ? <Spinner /> : null}
									Load older
								</Button>
							</div>
						) : null}
					</div>
				</DialogBody>
				<DialogFooter>
					<Button variant="secondary" onClick={onClose}>
						Close
					</Button>
				</DialogFooter>
			</DialogContent>
		</Dialog>
	);
}
