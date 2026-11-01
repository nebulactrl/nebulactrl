import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { cn } from "cn";
import {
	ArrowRightIcon,
	ArrowUpCircleIcon,
	KeyRoundIcon,
	LockIcon,
	RefreshCwIcon,
} from "lucide-react";
import { useRef, useState } from "react";
import { errorCode, errorMessage } from "#/api/client-config";
import { refreshDatabaseMutation } from "#/api/gen/@tanstack/react-query.gen";
import type { DatabaseView } from "#/api/gen/types.gen";
import type { Service } from "#/api/models";
import { CopyButton } from "#/components/common/copy-button";
import { Relative } from "#/components/common/relative";
import { Alert, AlertDescription, AlertTitle } from "#/components/ui/alert";
import { Badge } from "#/components/ui/badge";
import { Button } from "#/components/ui/button";
import { Skeleton } from "#/components/ui/skeleton";
import { Spinner } from "#/components/ui/spinner";
import { toast } from "#/components/ui/toast";
import { useNow } from "#/features/canvas/inspector/overview/use-now";
import { ServiceIcon } from "#/features/canvas/service-icon";
import { formatBytes } from "#/lib/format";
import { ConnectionString } from "./connection-string";
import { specLabel, upgradeTargets } from "./engines";
import {
	databasePollInterval,
	primaryExport,
	type RefreshRequest,
	refreshBlocker,
	refreshStatus,
	rotationLine,
	upgradeBanner,
} from "./model";
import { DATABASE_POLL_MS, databaseQuery, invalidateDatabase } from "./queries";
import { AdvancedReplicas } from "./replicas/advanced-replicas";
import { supportsReplicas } from "./replicas/model";
import { useReplication } from "./replicas/queries";
import { RotateDialog } from "./rotate-dialog";
import { UpgradeDialog } from "./upgrade-dialog";

function Figure({
	label,
	value,
	className,
}: {
	label: string;
	value: string;
	className?: string;
}) {
	return (
		<div className={cn("flex min-w-0 flex-col gap-1.5", className)}>
			<dt className="type-micro">{label}</dt>
			<dd className="type-metric truncate text-ink-1">{value}</dd>
		</div>
	);
}

function StateNotes({
	database,
	service,
}: {
	database: DatabaseView;
	service: Pick<Service, "hasStagedChanges">;
}) {
	const upgrade = upgradeBanner(
		database.upgrade,
		database.engine.label,
		service.hasStagedChanges,
	);
	const rotation = rotationLine(database.rotation);
	return (
		<>
			{upgrade?.tone === "pending" ? (
				<div
					className="flex items-center gap-2.5 py-2 text-[13px] text-ink-2"
					role="status"
				>
					<Spinner />
					{upgrade.text}
				</div>
			) : null}
			{upgrade?.tone === "ready" ? (
				<Alert variant="warning" className="my-2">
					<AlertTitle>Upgrade ready</AlertTitle>
					<AlertDescription>{upgrade.text}</AlertDescription>
				</Alert>
			) : null}
			{upgrade?.tone === "failed" ? (
				<Alert variant="destructive" className="my-2">
					<AlertTitle>Upgrade failed</AlertTitle>
					<AlertDescription>{upgrade.text}</AlertDescription>
				</Alert>
			) : null}
			{upgrade?.tone === "done" ? (
				<p className="py-2 text-2xs text-ink-3">{upgrade.text}</p>
			) : null}
			{rotation?.tone === "pending" ? (
				<div
					className="flex items-center gap-2.5 py-2 text-[13px] text-ink-2"
					role="status"
				>
					<Spinner />
					{rotation.text}
				</div>
			) : null}
			{rotation?.tone === "failed" ? (
				<Alert variant="destructive" className="my-2">
					<AlertTitle>Rotation failed</AlertTitle>
					<AlertDescription>{rotation.text}</AlertDescription>
				</Alert>
			) : null}
			{rotation?.tone === "applied" && database.rotation?.finishedAt ? (
				<p className="py-2 text-2xs text-ink-3">
					Credentials last rotated{" "}
					<Relative iso={database.rotation.finishedAt} />.
				</p>
			) : null}
		</>
	);
}

/**
 * The head of a managed database's Overview: a compact row with the engine,
 * its version and image, and the size, connections and running version the
 * agent last measured (with a Refresh); then the primary export as one line
 * with its copy-reference action, Copy connection string / Show, Rotate
 * credentials and Upgrade version. The rest of the exports live in the
 * Variables tab, one click away. It renders nothing for a service without a
 * managed spec. Backups sit in their own tab beside it.
 */
export function DatabaseSection({
	service,
	onOpenConnectionVariables,
}: {
	service: Service;
	/** Opens the Variables tab on its Connection variables section. */
	onOpenConnectionVariables(): void;
}) {
	const queryClient = useQueryClient();
	const now = useNow(2_000);
	const [refresh, setRefresh] = useState<RefreshRequest | null>(null);
	const refreshRef = useRef<RefreshRequest | null>(null);
	const [rotateOpen, setRotateOpen] = useState(false);
	const [upgradeOpen, setUpgradeOpen] = useState(false);

	const query = useQuery({
		...databaseQuery(service.id),
		refetchInterval: (state) =>
			databasePollInterval(
				state.state.data,
				refreshStatus(
					refreshRef.current,
					state.state.data?.info?.updatedAt,
					Date.now(),
				) === "pending",
				DATABASE_POLL_MS,
			),
	});
	const requestRefresh = useMutation({
		...refreshDatabaseMutation(),
		onError: (failure) => {
			refreshRef.current = null;
			setRefresh(null);
			toast.error("Could not refresh", errorMessage(failure));
		},
		onSuccess: () => void invalidateDatabase(queryClient),
	});
	const replication = useReplication(
		service.id,
		query.data !== undefined && supportsReplicas(query.data.engine.id),
	);

	if (query.isPending) {
		return (
			<section aria-label="Database">
				<div
					className="flex flex-col gap-2"
					role="status"
					aria-label="Loading the database"
				>
					<Skeleton className="h-9 w-2/3" />
					<Skeleton className="h-8 w-full" />
				</div>
			</section>
		);
	}
	if (query.isError) {
		// A custom database image has no managed spec: nothing to show here.
		if (errorCode(query.error) === "not_a_managed_database") return null;
		return (
			<section aria-label="Database">
				<Alert variant="destructive">
					<AlertTitle>The database details did not load</AlertTitle>
					<AlertDescription>
						{errorMessage(query.error)}{" "}
						<button
							type="button"
							className="text-accent-ink underline"
							onClick={() => void query.refetch()}
						>
							Retry
						</button>
					</AlertDescription>
				</Alert>
			</section>
		);
	}

	const database = query.data;
	const info = database.info;
	const primary = primaryExport(database);
	const blocker = refreshBlocker(service, database);
	const status = refreshStatus(refresh, info?.updatedAt, now);
	const busy =
		database.rotation?.status === "pending" ||
		database.upgrade?.status === "pending";
	const role = replication.data?.role;
	// A replica's credentials are the primary's; a group's version cannot be upgraded.
	const isReplica = role === "replica";
	const inGroup = role === "primary" || role === "replica";
	const newest =
		upgradeTargets(database.engine, database.spec.version, database.spec.flavor)
			.length === 0;

	const onRefresh = () => {
		const request = { at: Date.now(), baseline: info?.updatedAt };
		refreshRef.current = request;
		setRefresh(request);
		requestRefresh.mutate({ path: { serviceId: service.id } });
	};

	const sampled = info && !info.error;
	// The sentence under the figures: only when there is something to say
	// besides "Updated 2 m ago", which the Refresh button's tooltip carries.
	const note =
		status === "pending"
			? "Asking the agent…"
			: status === "unanswered"
				? "No answer yet: the numbers above may be stale."
				: info
					? null
					: "No sample yet.";

	return (
		<section aria-label="Database" className="flex flex-col">
			<div className="rule-rows">
				<div className="flex flex-wrap items-center gap-x-5 gap-y-3">
					<div className="flex min-w-0 flex-1 basis-44 items-center gap-2.5">
						<ServiceIcon service={service} size={26} />
						<div className="flex min-w-0 flex-col gap-1">
							<span className="truncate font-semibold text-ink-1 text-sm leading-none">
								{specLabel(database)}
							</span>
							<span
								title={database.image}
								className="truncate font-mono text-2xs text-ink-3"
							>
								{database.image}
							</span>
						</div>
					</div>
					<dl className="flex min-w-0 items-end gap-5">
						<Figure
							label="Size"
							value={sampled ? formatBytes(info.sizeBytes) : "—"}
						/>
						<Figure
							label="Connections"
							value={sampled ? String(info.connections) : "—"}
						/>
						<Figure
							label="Running"
							value={info?.version ? info.version : "—"}
							className="max-w-32"
						/>
					</dl>
					<Button
						variant="ghost"
						size="icon-sm"
						aria-label={
							info?.updatedAt
								? `Refresh the measurements (last updated ${new Date(info.updatedAt).toLocaleTimeString()})`
								: "Refresh the measurements"
						}
						title="Refresh the measurements"
						disabled={
							blocker !== null ||
							status === "pending" ||
							requestRefresh.isPending
						}
						onClick={onRefresh}
					>
						{status === "pending" ? <Spinner /> : <RefreshCwIcon aria-hidden />}
					</Button>
				</div>

				{note || blocker || info?.error ? (
					<div className="flex flex-col gap-1">
						{note ? (
							<p className="text-2xs text-ink-3" aria-live="polite">
								{note}
							</p>
						) : null}
						{blocker ? <p className="text-2xs text-ink-3">{blocker}</p> : null}
						{info?.error ? (
							<p className="text-2xs text-warn">
								The last measurement failed: {info.error}
							</p>
						) : null}
					</div>
				) : null}

				<StateNotes database={database} service={service} />

				{primary ? (
					<div className="flex min-w-0 items-center gap-2">
						<span className="shrink-0 font-medium font-mono text-[11.5px] text-ink-1">
							{primary.key}
						</span>
						{primary.secret ? (
							<Badge title="Secret: its value is only shown through Copy connection string">
								<LockIcon aria-hidden />
								secret
							</Badge>
						) : null}
						<code
							title="Reference this export from another service's variables"
							className="min-w-0 flex-1 truncate font-mono text-2xs text-ink-2"
						>
							{primary.reference}
						</code>
						<CopyButton
							value={primary.reference}
							label={`Copy reference ${primary.key}`}
							variant="ghost"
							size="icon-sm"
						/>
					</div>
				) : null}

				<div className="flex flex-col">
					{primary ? (
						<ConnectionString serviceId={service.id} exportKey={primary.key}>
							<Button
								variant="secondary"
								size="md"
								disabled={busy || isReplica}
								title={
									isReplica
										? "A read replica uses its primary's credentials: rotate them on the primary."
										: undefined
								}
								onClick={() => setRotateOpen(true)}
							>
								<KeyRoundIcon aria-hidden />
								Rotate credentials
							</Button>
							<Button
								variant="secondary"
								size="md"
								disabled={busy || inGroup || newest}
								title={
									inGroup
										? "Remove the read replicas first: a major upgrade does not carry them over."
										: newest
											? `${database.engine.label} ${database.spec.version} is the newest version offered.`
											: undefined
								}
								onClick={() => setUpgradeOpen(true)}
							>
								<ArrowUpCircleIcon aria-hidden />
								Upgrade version
							</Button>
							<Button
								variant="link"
								size="default"
								className="ml-auto font-sans text-xs"
								onClick={onOpenConnectionVariables}
							>
								All connection variables
								<ArrowRightIcon aria-hidden className="size-3" />
							</Button>
						</ConnectionString>
					) : null}
				</div>

				{supportsReplicas(database.engine.id) ? (
					<AdvancedReplicas service={service} deployed={database.deployed} />
				) : null}
			</div>

			<RotateDialog
				service={service}
				open={rotateOpen}
				onOpenChange={setRotateOpen}
			/>
			<UpgradeDialog
				service={service}
				database={database}
				open={upgradeOpen}
				onOpenChange={setUpgradeOpen}
			/>
		</section>
	);
}
