import { useMutation, useQueryClient } from "@tanstack/react-query";
import { Link, useParams } from "@tanstack/react-router";
import {
	ArrowUpFromLineIcon,
	HardDriveIcon,
	PlusIcon,
	RefreshCwIcon,
	Trash2Icon,
} from "lucide-react";
import { type ReactNode, useState } from "react";
import { errorCode, errorMessage } from "#/api/client-config";
import {
	rebuildDatabaseReplicaMutation,
	removeDatabaseReplicaMutation,
} from "#/api/gen/@tanstack/react-query.gen";
import type {
	ReplicaMemberView,
	ReplicaStateView,
	ReplicationView,
} from "#/api/gen/types.gen";
import type { Service } from "#/api/models";
import { ConfirmDialog } from "#/components/common/confirm-dialog";
import { Relative } from "#/components/common/relative";
import { Alert, AlertDescription, AlertTitle } from "#/components/ui/alert";
import { Badge } from "#/components/ui/badge";
import { Button } from "#/components/ui/button";
import { Skeleton } from "#/components/ui/skeleton";
import { Spinner } from "#/components/ui/spinner";
import { toast } from "#/components/ui/toast";
import { useNow } from "#/features/canvas/inspector/overview/use-now";
import { AddReplicaDialog } from "./add-replica-dialog";
import { GroupUrls } from "./group-urls";
import { LagBar } from "./lag-bar";
import {
	isBusyState,
	isPromotable,
	isRebuildable,
	MAX_REPLICAS,
	PANEL_CAVEAT,
	PANEL_PURPOSE,
	PANEL_TITLE,
	promotionLine,
	STALE_AFTER_MS,
	shownState,
	stateInfo,
	walInfo,
} from "./model";
import { MANUAL_NOTE, PromoteDialog } from "./promote-dialog";
import { invalidateReplication, useReplication } from "./queries";

function StateBadge({ state }: { state: string }) {
	const info = stateInfo(state);
	return (
		<Badge tone={info.tone} lamp title={info.text}>
			{info.label}
		</Badge>
	);
}

function WalBadge({ status }: { status: string | null | undefined }) {
	const info = walInfo(status);
	if (info === null) return null;
	return (
		<Badge tone={info.tone} title={info.text}>
			{info.label}
		</Badge>
	);
}

/** A replica's figures: state, lag bar, WAL status, the saga's message. */
function ReplicaFigures({
	figures,
	now,
}: {
	figures: Pick<
		ReplicaStateView,
		| "state"
		| "lagBytes"
		| "replayLagMs"
		| "walStatus"
		| "message"
		| "observedAt"
	>;
	now: number;
}) {
	const stale =
		figures.observedAt != null &&
		now - Date.parse(figures.observedAt) > STALE_AFTER_MS;
	const state = shownState(figures.state, figures.observedAt, now);
	const reading = !isBusyState(state) && state !== "failed";
	return (
		<div className="flex min-w-0 flex-col gap-1.5">
			<div className="flex flex-wrap items-center gap-2">
				<StateBadge state={state} />
				{reading && state !== "unknown" ? (
					<WalBadge status={figures.walStatus} />
				) : null}
				{figures.observedAt && reading ? (
					<span className="text-2xs text-ink-3">
						Measured <Relative iso={figures.observedAt} />
					</span>
				) : null}
			</div>
			{reading && state !== "unknown" ? (
				<LagBar lag={figures} stale={stale} />
			) : null}
			{figures.message ? (
				<p className="text-2xs text-ink-3">{figures.message}</p>
			) : (
				<p className="text-2xs text-ink-3">{stateInfo(state).text}</p>
			)}
		</div>
	);
}

type Dialogs =
	| { kind: "add" }
	| { kind: "promote"; service: Pick<Service, "id" | "name" | "slug"> }
	| { kind: "rebuild"; id: string; slug: string; follow?: string }
	| { kind: "remove"; id: string; slug: string }
	| null;

/**
 * The Replicas panel of a managed Postgres database. A standalone database
 * offers "Add read replica"; a primary lists its replicas with state, lag and
 * WAL status; a replica shows how it follows its primary and offers Promote,
 * Rebuild and Remove; a fenced old primary explains itself and offers Rebuild.
 * Everything is manual: nothing here promotes by itself.
 */
export function ReplicasPanel({
	service,
	deployed,
}: {
	service: Service;
	deployed: boolean;
}) {
	const queryClient = useQueryClient();
	const now = useNow(5_000);
	const view = useReplication(service.id);
	const [dialog, setDialog] = useState<Dialogs>(null);
	const params = useParams({ strict: false });

	const rebuild = useMutation(rebuildDatabaseReplicaMutation());
	const remove = useMutation(removeDatabaseReplicaMutation());

	if (view.isPending) {
		return (
			<section aria-label="Replicas">
				<Skeleton className="h-9 w-2/3" />
			</section>
		);
	}
	if (view.isError) {
		// An older control plane has no replica API: nothing to offer.
		if (errorCode(view.error) === "not_found") return null;
		return (
			<section aria-label="Replicas">
				<Alert variant="destructive">
					<AlertTitle>The replicas did not load</AlertTitle>
					<AlertDescription>
						{errorMessage(view.error)}{" "}
						<button
							type="button"
							className="text-accent-ink underline"
							onClick={() => void view.refetch()}
						>
							Retry
						</button>
					</AlertDescription>
				</Alert>
			</section>
		);
	}

	const data = view.data;
	const rows = data.replicas ?? [];
	const promotion = promotionLine(data.promotion);
	const followed = data.primary ?? null;

	const openService = (id: string, name: string) =>
		params.projectId && params.envSlug ? (
			<Link
				to="/p/$projectId/$envSlug"
				params={{ projectId: params.projectId, envSlug: params.envSlug }}
				search={{ service: id }}
				className="font-medium text-ink-1 hover:underline"
			>
				{name}
			</Link>
		) : (
			<span className="font-medium text-ink-1">{name}</span>
		);

	const confirmRemove = async (id: string) => {
		await remove.mutateAsync({
			path: { serviceId: id },
			headers: { "Idempotency-Key": crypto.randomUUID() },
		});
		toast.add({
			type: "success",
			title: "Removing the replica",
			description:
				"Its slot and role are dropped on the primary, then the service is deleted.",
			timeout: 7000,
		});
		await invalidateReplication(queryClient);
	};

	const confirmRebuild = async (id: string, follow?: string) => {
		await rebuild.mutateAsync({
			path: { serviceId: id },
			body: follow ? { followServiceId: follow } : {},
			headers: { "Idempotency-Key": crypto.randomUUID() },
		});
		toast.add({
			type: "success",
			title: "Rebuilding the replica",
			description: "It copies the data again; follow the state in this panel.",
			timeout: 7000,
		});
		await invalidateReplication(queryClient);
	};

	return (
		<section aria-label={PANEL_TITLE} className="flex flex-col gap-2.5">
			<div className="flex items-center gap-2">
				<h3 className="type-label">{PANEL_TITLE}</h3>
				{data.role === "replica" ? (
					<Badge variant="accent">replica</Badge>
				) : data.role === "fenced" ? (
					<Badge tone="bad">fenced</Badge>
				) : null}
				{data.role === "standalone" || data.role === "primary" ? (
					<Button
						variant="secondary"
						size="md"
						className="ml-auto"
						disabled={!deployed || rows.length >= MAX_REPLICAS}
						title={
							!deployed
								? "Deploy the database first: a replica copies the running primary."
								: rows.length >= MAX_REPLICAS
									? `A primary has at most ${MAX_REPLICAS} replicas.`
									: undefined
						}
						onClick={() => setDialog({ kind: "add" })}
					>
						<PlusIcon aria-hidden />
						Add standby replica
					</Button>
				) : null}
			</div>

			<div className="flex flex-col gap-1">
				{data.role === "standalone" || data.role === "primary" ? (
					<p className="text-[13px] text-ink-2">{PANEL_PURPOSE}</p>
				) : null}
				<p className="text-2xs text-ink-3">{PANEL_CAVEAT}</p>
			</div>

			{promotion ? (
				promotion.tone === "running" ? (
					<div
						className="flex items-center gap-2.5 text-[13px] text-ink-2"
						role="status"
					>
						<Spinner />
						{promotion.text}
					</div>
				) : promotion.tone === "failed" ? (
					<Alert variant="destructive">
						<AlertTitle>The last promote failed</AlertTitle>
						<AlertDescription>{promotion.text}</AlertDescription>
					</Alert>
				) : (
					<p className="text-2xs text-ink-3">{promotion.text}</p>
				)
			) : null}

			{data.role === "standalone" ? (
				<p className="text-2xs text-ink-3">
					None yet. It adds the connection variables{" "}
					<span className="font-mono">DATABASE_WRITE_*</span> and{" "}
					<span className="font-mono">DATABASE_READ_*</span>.
				</p>
			) : null}

			{data.role === "primary" ? (
				rows.length === 0 ? (
					<p className="text-2xs text-ink-3">No replicas.</p>
				) : (
					<ul
						aria-label="Replicas"
						className="surface-raised divide-y divide-line-faint rounded-card"
					>
						{rows.map((row) => (
							<ReplicaListRow
								key={row.id}
								row={row}
								now={now}
								name={openService(row.id, row.slug)}
								rebuildable={isRebuildable(row.state)}
								// `canPromote` describes the viewed service as a replica; on a
								// primary each member qualifies by its own state.
								promotable={
									isPromotable(row.state) &&
									data.promotion?.status !== "running"
								}
								onPromote={() =>
									setDialog({
										kind: "promote",
										service: { id: row.id, name: row.slug, slug: row.slug },
									})
								}
								onRebuild={() =>
									setDialog({ kind: "rebuild", id: row.id, slug: row.slug })
								}
								onRemove={() =>
									setDialog({ kind: "remove", id: row.id, slug: row.slug })
								}
							/>
						))}
					</ul>
				)
			) : null}
			{data.role === "primary" && rows.length > 0 ? (
				<>
					<GroupUrls serviceId={service.id} />
					<p className="text-2xs text-ink-3">{MANUAL_NOTE}</p>
				</>
			) : null}

			{data.role === "replica" && data.self ? (
				<div className="flex flex-col gap-2.5">
					<p className="text-[13px] text-ink-2">
						Follows{" "}
						{data.primary
							? openService(data.primary.id, data.primary.slug)
							: "its primary"}
						. It is read-only: writes go to the primary, and its credentials are
						the primary's.
					</p>
					<ReplicaFigures figures={data.self} now={now} />
					<Alert variant="info">
						<AlertDescription>{MANUAL_NOTE}</AlertDescription>
					</Alert>
					<ReplicaActions
						state={shownState(data.self.state, data.self.observedAt, now)}
						canPromote={data.canPromote}
						busy={rebuild.isPending || remove.isPending}
						onPromote={() => setDialog({ kind: "promote", service })}
						onRebuild={() =>
							setDialog({ kind: "rebuild", id: service.id, slug: service.slug })
						}
						onRemove={() =>
							setDialog({ kind: "remove", id: service.id, slug: service.slug })
						}
					/>
					<GroupUrls serviceId={service.id} />
				</div>
			) : null}

			{data.role === "fenced" ? <FencedNote view={data} /> : null}
			{data.role === "fenced" && data.rebuildable && followed ? (
				<div>
					<Button
						variant="secondary"
						size="md"
						disabled={rebuild.isPending}
						onClick={() =>
							setDialog({
								kind: "rebuild",
								id: service.id,
								slug: service.slug,
								follow: followed.id,
							})
						}
					>
						<RefreshCwIcon aria-hidden />
						Rebuild as a replica of {followed.slug}
					</Button>
				</div>
			) : null}

			<AddReplicaDialog
				service={service}
				taken={rows.map((row) => row.slug)}
				open={dialog?.kind === "add"}
				onOpenChange={(open) => !open && setDialog(null)}
			/>
			{dialog?.kind === "promote" ? (
				<PromoteDialog
					service={dialog.service}
					open
					onOpenChange={(open) => !open && setDialog(null)}
				/>
			) : null}
			<ConfirmDialog
				open={dialog?.kind === "rebuild"}
				onOpenChange={(open) => !open && setDialog(null)}
				title={
					dialog?.kind === "rebuild"
						? `Rebuild ${dialog.slug}?`
						: "Rebuild the replica?"
				}
				body={
					dialog?.kind === "rebuild" && dialog.follow
						? "The data is copied from the new primary onto a new volume and this database follows it as a read replica. The old volume stays until you delete it."
						: "The data is copied again from the primary. The replica does not answer reads until the copy has finished."
				}
				cta="Rebuild"
				onConfirm={() =>
					dialog?.kind === "rebuild"
						? confirmRebuild(dialog.id, dialog.follow)
						: undefined
				}
			/>
			<ConfirmDialog
				open={dialog?.kind === "remove"}
				onOpenChange={(open) => !open && setDialog(null)}
				title={
					dialog?.kind === "remove"
						? `Remove ${dialog.slug}?`
						: "Remove the replica?"
				}
				body="The replica stops following the primary, its replication slot and role are dropped on the primary, and the service and its data are deleted. The primary is not touched."
				cta="Remove replica"
				danger
				{...(dialog?.kind === "remove" ? { typeToConfirm: dialog.slug } : {})}
				onConfirm={() =>
					dialog?.kind === "remove" ? confirmRemove(dialog.id) : undefined
				}
			/>
		</section>
	);
}

function FencedNote({ view }: { view: ReplicationView }) {
	return (
		<Alert variant="warning">
			<AlertTitle>Fenced</AlertTitle>
			<AlertDescription>
				{view.primary
					? `A replica (${view.primary.slug}) was promoted in place of this database. `
					: ""}
				It stays stopped and never accepts writes again; its data is kept. There
				is no way to unfence it, because that is how two primaries happen.
				Rebuild it as a replica, or delete it.
			</AlertDescription>
		</Alert>
	);
}

function ReplicaActions({
	state,
	canPromote,
	busy,
	onPromote,
	onRebuild,
	onRemove,
}: {
	state: string;
	canPromote: boolean;
	busy: boolean;
	onPromote(): void;
	onRebuild(): void;
	onRemove(): void;
}) {
	return (
		<div className="flex flex-wrap items-center gap-2">
			<Button
				variant="secondary"
				size="md"
				disabled={!(isPromotable(state) && canPromote) || busy}
				title={
					isPromotable(state)
						? undefined
						: "Only a replica that is receiving changes, or a broken one whose primary is gone, can be promoted."
				}
				onClick={onPromote}
			>
				<ArrowUpFromLineIcon aria-hidden />
				Promote to primary
			</Button>
			<Button
				variant="secondary"
				size="md"
				disabled={busy || !isRebuildable(state)}
				title={
					isRebuildable(state)
						? undefined
						: "A replica that is streaming does not need a rebuild."
				}
				onClick={onRebuild}
			>
				<RefreshCwIcon aria-hidden />
				Rebuild
			</Button>
			<Button
				variant="ghost"
				size="md"
				disabled={busy}
				className="ml-auto"
				onClick={onRemove}
			>
				<Trash2Icon aria-hidden />
				Remove replica
			</Button>
		</div>
	);
}

function ReplicaListRow({
	row,
	now,
	name,
	promotable,
	rebuildable,
	onPromote,
	onRebuild,
	onRemove,
}: {
	row: ReplicaMemberView;
	now: number;
	name: ReactNode;
	promotable: boolean;
	rebuildable: boolean;
	onPromote(): void;
	onRebuild(): void;
	onRemove(): void;
}) {
	const state = shownState(row.state, row.observedAt, now);
	return (
		<li className="flex flex-col gap-2 px-3 py-2.5">
			<div className="flex min-w-0 items-center gap-2 text-[13px]">
				{name}
				{row.serverName ? (
					<span
						title={`Runs on ${row.serverName}`}
						className="inline-flex h-5 min-w-0 max-w-[150px] items-center gap-[5px] rounded-md border border-line-faint bg-white/[0.045] px-[7px] font-mono text-[11px] text-ink-3 leading-none"
					>
						<HardDriveIcon aria-hidden className="size-3 shrink-0" />
						<span className="truncate">{row.serverName}</span>
					</span>
				) : null}
				<span className="ml-auto flex items-center gap-1">
					<Button
						variant="ghost"
						size="sm"
						disabled={!promotable || isBusyState(state)}
						title={
							promotable
								? "Make this replica the primary. Manual: nothing promotes by itself."
								: "Only a replica that is receiving changes, or a broken one whose primary is gone, can be promoted."
						}
						onClick={onPromote}
					>
						Promote
					</Button>
					{rebuildable ? (
						<Button variant="ghost" size="sm" onClick={onRebuild}>
							Rebuild
						</Button>
					) : null}
					<Button
						variant="ghost"
						size="icon-sm"
						aria-label={`Remove ${row.slug}`}
						title="Remove replica"
						disabled={state === "removing"}
						onClick={onRemove}
					>
						<Trash2Icon aria-hidden />
					</Button>
				</span>
			</div>
			<ReplicaFigures figures={row} now={now} />
		</li>
	);
}
