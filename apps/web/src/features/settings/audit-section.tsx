import { keepPreviousData, useInfiniteQuery } from "@tanstack/react-query";
import { cn } from "cn";
import { useState } from "react";
import { listAuditEventsInfiniteOptions } from "#/api/gen/@tanstack/react-query.gen";
import { CopyButton } from "#/components/common/copy-button";
import { EmptyState } from "#/components/common/empty-state";
import { ErrorState } from "#/components/common/error-state";
import { Relative } from "#/components/common/relative";
import { type CalloutTone, ToneIcon } from "#/components/common/tone-icon";
import { Badge } from "#/components/ui/badge";
import { Button } from "#/components/ui/button";
import { Skeleton, SkeletonReveal } from "#/components/ui/skeleton";
import { Spinner } from "#/components/ui/spinner";
import {
	Table,
	TableBody,
	TableCell,
	TableHead,
	TableHeader,
	TableRow,
} from "#/components/ui/table";
import { Toggle } from "#/components/ui/toggle";
import { absoluteTime } from "#/lib/format";
import {
	auditActionLabel,
	auditDetails,
	auditResourceLabel,
	metadataJson,
} from "./audit-describe";
import {
	AUDIT_FILTERS,
	type AuditActionKind,
	type AuditFilterId,
	auditActionKind,
	auditFilterById,
	auditQuery,
} from "./audit-filters";

const PAGE_SIZE = 50;

const KIND_TONE: Record<AuditActionKind, CalloutTone> = {
	create: "success",
	remove: "danger",
	change: "info",
};

const KIND_LABEL: Record<AuditActionKind, string> = {
	create: "Created",
	remove: "Removed or ended",
	change: "Changed",
};

function ActorBadge({ type }: { type: string }) {
	return (
		<Badge variant={type === "user" ? "default" : "accent"} shape="capsule">
			{type}
		</Badge>
	);
}

/** The audit log: filterable by action family, newest first, cursor pagination through "Load more". */
export function AuditSection() {
	const [filterId, setFilterId] = useState<AuditFilterId>("all");
	const filter = auditFilterById(filterId);
	const events = useInfiniteQuery({
		// The filter is part of the options, so it is part of the query key:
		// each chip has its own cursor chain and cache.
		...listAuditEventsInfiniteOptions({
			query: auditQuery(filterId, PAGE_SIZE),
		}),
		initialPageParam: { query: { limit: PAGE_SIZE } },
		getNextPageParam: (last) => last.nextCursor || undefined,
		placeholderData: keepPreviousData,
	});
	const rows = (events.data?.pages ?? []).flatMap((page) => page.items ?? []);

	const chips = (
		<fieldset
			aria-label="Filter by event type"
			className="m-0 flex min-w-0 flex-wrap items-center gap-1.5 border-0 p-0"
		>
			{AUDIT_FILTERS.map((option) => (
				<Toggle
					key={option.id}
					variant="outline"
					size="sm"
					className="rounded-full"
					pressed={option.id === filterId}
					onPressedChange={() => setFilterId(option.id)}
				>
					{option.label}
				</Toggle>
			))}
		</fieldset>
	);

	let body: React.ReactNode;
	if (events.isError && rows.length === 0) {
		body = (
			<ErrorState
				frame="raised"
				title="The audit log did not load"
				body="Reading it needs the admin scope. Check your session and try again; nothing was changed."
				retry={() => void events.refetch()}
			/>
		);
	} else if (rows.length === 0) {
		body = (
			<EmptyState
				illustration="folder"
				title={filter.empty.title}
				body={filter.empty.body}
			/>
		);
	} else {
		body = (
			<>
				<div className="overflow-hidden border-line border-y">
					<Table stack>
						<TableHeader>
							<TableRow>
								<TableHead>When</TableHead>
								<TableHead>Who</TableHead>
								<TableHead>Action</TableHead>
								<TableHead>Resource</TableHead>
								<TableHead>Details</TableHead>
							</TableRow>
						</TableHeader>
						<TableBody>
							{rows.map((event) => {
								const kind = auditActionKind(event.action);
								return (
									<TableRow key={event.id}>
										<TableCell
											pin="start"
											className="whitespace-nowrap font-mono text-ink-3 text-xs"
										>
											<span title={absoluteTime(event.createdAt)}>
												<Relative iso={event.createdAt} />
											</span>
										</TableCell>
										<TableCell label="Who">
											<span className="flex items-center gap-2">
												<ActorBadge type={event.actorType} />
												<span className="truncate text-ink-1 text-xs">
													{event.actorLabel}
												</span>
											</span>
										</TableCell>
										<TableCell label="Action">
											<span
												title={`${KIND_LABEL[kind]} (${event.action})`}
												className="inline-flex items-center gap-1.5 text-ink-1 text-xs"
											>
												<ToneIcon tone={KIND_TONE[kind]} className="size-3.5" />
												<span className="sr-only">{KIND_LABEL[kind]}: </span>
												{auditActionLabel(event.action)}
											</span>
										</TableCell>
										<TableCell label="Resource" className="text-ink-3 text-xs">
											{auditResourceLabel(event.resourceType)}
											{event.resourceId ? (
												<span className="font-mono">
													{" "}
													{event.resourceId.slice(0, 8)}
												</span>
											) : null}
										</TableCell>
										<TableCell label="Details">
											<span className="flex max-w-[360px] items-center gap-1.5">
												<span
													title={metadataJson(event.metadata)}
													className="min-w-0 truncate text-ink-3 text-xs"
												>
													{auditDetails(event.action, event.metadata)}
												</span>
												{Object.keys(event.metadata ?? {}).length > 0 ? (
													<CopyButton
														variant="ghost"
														size="icon-xs"
														label="Copy event details"
														value={metadataJson(event.metadata)}
													/>
												) : null}
											</span>
										</TableCell>
									</TableRow>
								);
							})}
						</TableBody>
					</Table>
				</div>
				<div className="flex items-center justify-between gap-3">
					<span className="text-ink-3 text-xs">
						{rows.length} {rows.length === 1 ? "event" : "events"} loaded
						{events.hasNextPage ? "" : ", that is all of them"}.
					</span>
					{events.hasNextPage ? (
						<Button
							variant="secondary"
							size="sm"
							disabled={events.isFetchingNextPage}
							onClick={() => void events.fetchNextPage()}
						>
							{events.isFetchingNextPage ? <Spinner /> : null}
							Load more
						</Button>
					) : null}
				</div>
			</>
		);
	}

	return (
		<div className="flex flex-col gap-4">
			{chips}
			<div
				className={cn(
					"flex flex-col gap-3 transition-opacity duration-quick motion-reduce:transition-none",
					events.isPlaceholderData && "opacity-60",
				)}
				aria-busy={events.isPlaceholderData}
			>
				<SkeletonReveal
					loading={events.isPending}
					className="flex flex-col gap-3"
					skeleton={<Skeleton className="h-48 rounded-card" />}
				>
					{() => body}
				</SkeletonReveal>
			</div>
		</div>
	);
}
