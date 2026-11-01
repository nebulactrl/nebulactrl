import { useMutation, useQueryClient } from "@tanstack/react-query";
import { Link } from "@tanstack/react-router";
import {
	PlugZapIcon,
	RotateCwIcon,
	WandSparklesIcon,
	WaypointsIcon,
} from "lucide-react";
import { useState } from "react";
import { errorMessage } from "#/api/client-config";
import {
	reconnectPlatformService,
	retryPlatformService,
} from "#/api/gen/sdk.gen";
import type {
	PlatformKindView,
	PlatformServiceView,
} from "#/api/gen/types.gen";
import { CopyButton } from "#/components/common/copy-button";
import { Alert, AlertDescription, AlertTitle } from "#/components/ui/alert";
import { Button } from "#/components/ui/button";
import { Spinner } from "#/components/ui/spinner";
import { StatusIcon } from "#/components/ui/status-icon";
import { toast } from "#/components/ui/toast";
import {
	availability,
	canvasTarget,
	containerState,
	detailEntries,
	detailLabel,
	stepRows,
	wiringTarget,
} from "./model";
import { StatusLine, StepList, WarningNote } from "./parts";
import { invalidatePlatform } from "./queries";
import { RemoveDialog } from "./remove-dialog";
import { platformSetup } from "./store";

function KindIcon() {
	return (
		<span
			aria-hidden
			className="surface-well grid size-9 shrink-0 place-items-center rounded-ctl text-accent-ink"
		>
			<WaypointsIcon className="size-4" />
		</span>
	);
}

/** A label above a value, for the facts of a ready service. */
function Fact({
	label,
	children,
}: {
	label: string;
	children: React.ReactNode;
}) {
	return (
		<div className="flex min-w-0 flex-col gap-0.5">
			<dt className="type-label">{label}</dt>
			<dd className="m-0 min-w-0 text-ink-1 text-xs">{children}</dd>
		</div>
	);
}

/** Where a ready service answers, what it runs on and what it was wired into. */
function Facts({ view }: { view: PlatformServiceView }) {
	const address = view.publicUrl || view.hostname;
	const wiring = view.wiring;
	const target = wiring ? wiringTarget(wiring.type) : null;
	return (
		<dl className="m-0 grid gap-x-6 gap-y-2.5 sm:grid-cols-2">
			{address ? (
				<Fact label="Address">
					<span className="flex min-w-0 items-center gap-1.5">
						<span className="truncate font-mono" title={address}>
							{address}
						</span>
						<CopyButton
							value={address}
							label="Copy address"
							variant="ghost"
							size="icon-xs"
						/>
					</span>
				</Fact>
			) : null}
			{view.serverName ? <Fact label="Server">{view.serverName}</Fact> : null}
			<Fact label="Access">Reachable from your servers and tailnet only</Fact>
			{detailEntries(view.details).map(([key, value]) => (
				<Fact key={key} label={detailLabel(key)}>
					<span className="font-mono">{value}</span>
				</Fact>
			))}
			{wiring && target ? (
				<Fact label="Wired into">
					<Link
						to="/settings/$section"
						params={{ section: target.slug }}
						className="text-accent-ink underline underline-offset-2"
					>
						{target.section}
					</Link>
					<span className="text-ink-3">: {wiring.name}</span>
				</Fact>
			) : null}
		</dl>
	);
}

/** The containers the service runs as, each with its state in words. */
function Containers({ view }: { view: PlatformServiceView }) {
	const refs = view.services ?? [];
	if (refs.length === 0) return null;
	return (
		<ul
			aria-label="Services"
			className="m-0 flex list-none flex-wrap gap-x-4 gap-y-1 p-0 text-ink-3 text-xs"
		>
			{refs.map((ref) => {
				const state = containerState(ref.state);
				return (
					<li key={ref.id} className="flex items-center gap-1.5">
						<StatusIcon kind={state.kind} label="" />
						<span className="text-ink-2">{ref.name}</span>
						<span>{state.label}</span>
					</li>
				);
			})}
		</ul>
	);
}

/** One platform service of this team: what it is, how it is doing, what it is wired into, and the actions. */
function InstanceBody({
	catalog,
	view,
}: {
	catalog: PlatformKindView;
	view: PlatformServiceView;
}) {
	const queryClient = useQueryClient();
	const [removing, setRemoving] = useState(false);
	const canvas = canvasTarget(view);
	const ready = view.status === "ready";

	const retry = useMutation({
		mutationFn: async () => {
			await retryPlatformService({ path: { id: view.id }, throwOnError: true });
		},
		onSuccess: () => invalidatePlatform(queryClient),
		onError: (error) => toast.error("Could not retry", errorMessage(error)),
	});
	const reconnect = useMutation({
		mutationFn: async () => {
			await reconnectPlatformService({
				path: { id: view.id },
				throwOnError: true,
			});
		},
		onSuccess: async () => {
			toast.success(
				`Reconnecting ${view.label}`,
				"The wiring runs again and is tested.",
			);
			await invalidatePlatform(queryClient);
		},
		onError: (error) => toast.error("Could not reconnect", errorMessage(error)),
	});

	return (
		<>
			{view.status === "failed" ? (
				<Alert variant="destructive">
					<AlertTitle>Set-up failed</AlertTitle>
					<AlertDescription>
						{view.message || "It stopped without saying why."}
					</AlertDescription>
				</Alert>
			) : null}
			{view.status === "provisioning" ? (
				<div className="flex flex-col gap-2">
					<StepList rows={stepRows(view)} />
					{view.message ? (
						<p className="m-0 text-ink-3 text-xs">{view.message}</p>
					) : null}
				</div>
			) : null}
			{view.status === "removing" ? (
				<p className="m-0 text-ink-3 text-xs">
					{view.message || "Removing it and what it was wired into."}
				</p>
			) : null}
			{ready ? <Facts view={view} /> : null}
			<Containers view={view} />
			{(view.warnings ?? []).map((warning) => (
				<WarningNote key={warning}>{warning}</WarningNote>
			))}
			{ready && view.backup && !view.backup.configured ? (
				<p className="m-0 text-ink-3 text-xs leading-relaxed">
					{view.backup.hint}
				</p>
			) : null}
			<footer className="-mb-1 flex flex-wrap items-center gap-1 border-line-faint border-t pt-2.5">
				{canvas ? (
					<Button
						variant="ghost"
						size="sm"
						nativeButton={false}
						render={
							<Link
								to="/p/$projectId/$envSlug"
								params={{
									projectId: canvas.projectId,
									envSlug: canvas.envSlug,
								}}
							/>
						}
					>
						Open in canvas
					</Button>
				) : null}
				{view.status === "failed" ? (
					<Button
						variant="secondary"
						size="sm"
						disabled={retry.isPending}
						onClick={() => retry.mutate()}
					>
						{retry.isPending ? <Spinner /> : <RotateCwIcon aria-hidden />}
						Retry
					</Button>
				) : null}
				{ready ? (
					<Button
						variant="ghost"
						size="sm"
						disabled={reconnect.isPending}
						title="Run the wiring again. Creates a new API key."
						onClick={() => reconnect.mutate()}
					>
						{reconnect.isPending ? <Spinner /> : <PlugZapIcon aria-hidden />}
						Reconnect
					</Button>
				) : null}
				{view.status === "removing" ? null : (
					<Button
						variant="ghost"
						size="sm"
						className="ml-auto text-destructive-text"
						onClick={() => setRemoving(true)}
					>
						Remove
					</Button>
				)}
			</footer>
			<RemoveDialog
				view={view}
				label={catalog.label}
				open={removing}
				onOpenChange={setRemoving}
			/>
		</>
	);
}

/** Headscale is not set up yet: what it is for, what it costs, and the button (or why not). */
function Invitation({
	catalog,
	usableServers,
}: {
	catalog: PlatformKindView;
	usableServers: number;
}) {
	const state = availability(catalog, { usableServers });
	const reason =
		state.state === "available" || state.state === "exists"
			? null
			: state.reason;
	return (
		<>
			<p className="m-0 text-ink-2 text-xs leading-relaxed">
				{catalog.summary}
			</p>
			<p className="m-0 text-ink-3 text-xs">{catalog.footprint}.</p>
			{catalog.notice ? (
				<p className="m-0 text-ink-3 text-xs leading-relaxed">
					{catalog.notice}
				</p>
			) : null}
			<div className="flex flex-wrap items-center gap-3">
				<Button
					type="button"
					variant="secondary"
					disabled={state.state !== "available"}
					onClick={() => platformSetup.open()}
				>
					<WandSparklesIcon aria-hidden />
					Set up for me
				</Button>
				{reason ? (
					<span className="text-ink-3 text-xs" data-testid="platform-reason">
						{reason}
					</span>
				) : null}
			</div>
		</>
	);
}

/**
 * The team's Headscale: its catalog entry and, when set up, the service with
 * its status, wiring and actions.
 */
export function PlatformServiceCard({
	catalog,
	usableServers,
}: {
	catalog: PlatformKindView;
	usableServers: number;
}) {
	const view = catalog.instance;
	return (
		<article
			data-testid={`platform-${catalog.kind}`}
			aria-label={catalog.label}
			className="surface-raised flex min-w-0 flex-col gap-3.5 rounded-card p-4"
		>
			<header className="flex items-start gap-3">
				<KindIcon />
				<div className="flex min-w-0 flex-1 flex-col gap-0.5">
					<span className="flex flex-wrap items-center gap-2">
						<h3 className="m-0 font-medium text-ink-1 text-sm">
							{catalog.label}
						</h3>
					</span>
					<span className="truncate font-mono text-2xs text-ink-3">
						{catalog.product} · {catalog.license}
					</span>
				</div>
				{view ? (
					<StatusLine view={view} />
				) : (
					<span className="text-ink-3 text-xs">Not set up</span>
				)}
			</header>
			{view ? (
				<InstanceBody catalog={catalog} view={view} />
			) : (
				<Invitation catalog={catalog} usableServers={usableServers} />
			)}
		</article>
	);
}
