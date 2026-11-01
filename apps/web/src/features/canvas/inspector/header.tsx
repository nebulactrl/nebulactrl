import { cn } from "cn";
import {
	ArrowRightLeftIcon,
	BoxIcon,
	CopyPlusIcon,
	EllipsisIcon,
	FolderOpenIcon,
	GaugeIcon,
	GitBranchIcon,
	HammerIcon,
	PlayIcon,
	SettingsIcon,
	SquareIcon,
	TerminalSquareIcon,
	XIcon,
} from "lucide-react";
import { useEffect, useRef, useState } from "react";
import type { Service } from "#/api/models";
import { useConfirm } from "#/components/common/confirm-dialog";
import { COPIED_MS } from "#/components/common/copy-button";
import { CopyCheckIcon } from "#/components/icons/animated/copy-check-icon";
import { PendingRefreshIcon } from "#/components/icons/animated/refresh-cw-icon";
import { useProjectContext } from "#/components/shell/use-project-context";
import { Button } from "#/components/ui/button";
import {
	DropdownMenu,
	DropdownMenuContent,
	DropdownMenuItem,
	DropdownMenuSeparator,
	DropdownMenuTrigger,
} from "#/components/ui/dropdown-menu";
import { StatusPill } from "#/components/ui/lamp";
import { SheetTitle } from "#/components/ui/sheet";
import { Spinner } from "#/components/ui/spinner";
import { toast } from "#/components/ui/toast";
import { CloneServiceDialog, MoveServiceDialog } from "#/features/clone";
import { isInFlight } from "#/features/deployments/labels";
import { useDeploymentActions } from "#/features/deployments/use-deployment-actions";
import { ServiceConfigChip } from "#/features/project-config";
import { ScaleDialog } from "#/features/scaling";
import { serviceCanScale } from "#/features/scaling/model";
import { openFiles, openShell } from "#/features/terminal";
import { lastInputWasKeyboard } from "#/lib/input-modality";
import { openTargets } from "#/lib/service-urls";
import { vt } from "#/lib/transitions";
import { deployLabel } from "../lib/deploy-label";
import { ServiceIcon } from "../service-icon";
import { OpenDomainLink, OpenSiteButton } from "./open-site";
import { servicePill, sourceLine } from "./service-pill";
import type { InspectorTab } from "./tab-ids";

export interface InspectorHeaderProps {
	service: Service;
	onClose(): void;
	onTab(tab: InspectorTab): void;
}

/**
 * The panel's header: the service's tile, name and state pill, a
 * mono source line, and the quick actions. Redeploy and Restart are raised
 * keys, Stop is quiet, and the ⋯ menu holds the rest.
 */
export function InspectorHeader({
	service,
	onClose,
	onTab,
}: InspectorHeaderProps) {
	const actions = useDeploymentActions(service);
	const environment = useProjectContext()?.environments.find(
		(candidate) => candidate.id === service.environmentId,
	);
	const confirm = useConfirm();
	const [scaleOpen, setScaleOpen] = useState(false);
	const [cloneOpen, setCloneOpen] = useState(false);
	const [moveOpen, setMoveOpen] = useState(false);
	const [menuOpen, setMenuOpen] = useState(false);
	// Which copy item is showing its check, while the menu is held open.
	const [copied, setCopied] = useState<"host" | "id" | null>(null);
	const copiedTimer = useRef<number | undefined>(undefined);
	useEffect(() => () => window.clearTimeout(copiedTimer.current), []);
	const pill = servicePill(service);
	const source = sourceLine(service);
	const inFlight = service.latestDeployment
		? isInFlight(service.latestDeployment.status)
		: false;
	const deployed = service.currentDeploymentId !== null;
	const stopped = service.desiredState === "stopped";
	const isRoute = service.kind === "route";
	// A stack has no replicas to scale and no private host: its components answer by name.
	const isStack = service.kind === "stack";
	const targets = openTargets(service);
	// The link is the domain the line names; with "Tailnet only" on, that
	// domain is not routed, so it stays plain text.
	const domainTarget = source.domain
		? targets.find(
				(target) =>
					!target.tailnetOnly && target.host === source.domain?.hostname,
			)
		: undefined;

	const stop = async () => {
		await confirm({
			title: `Stop ${service.name}?`,
			body: "The container stops and its routes are removed, so it stops serving traffic. Data in volumes stays. Restart or deploy to start it again.",
			cta: "Stop service",
			danger: true,
			hold: false,
			onConfirm: () => actions.stop.mutateAsync(),
		});
	};

	// A pointer copy keeps the menu open for 1.5s with the item's glyph
	// morphed to a check, so the menu itself says it worked. A keyboard copy
	// closes the menu at once, as before, with no motion; the toast confirms
	// either way.
	const copy = async (which: "host" | "id", label: string, value: string) => {
		const keyboard = lastInputWasKeyboard();
		try {
			await navigator.clipboard.writeText(value);
			toast.success(`Copied ${label}`, value);
		} catch {
			toast.error(
				`Could not copy ${label}`,
				"The browser blocked clipboard access.",
			);
			setMenuOpen(false);
			return;
		}
		if (keyboard) {
			setMenuOpen(false);
			return;
		}
		setCopied(which);
		window.clearTimeout(copiedTimer.current);
		copiedTimer.current = window.setTimeout(() => {
			setCopied(null);
			setMenuOpen(false);
		}, COPIED_MS);
	};

	return (
		<header
			{...vt("service", service.id)}
			className={cn(
				"relative flex shrink-0 flex-col gap-3 px-4 pt-3.5 pb-3",
				environment?.isProduction && "env-rule",
			)}
		>
			{environment?.isProduction ? (
				<span className="env-caption">Production</span>
			) : null}
			<div className="flex min-w-0 items-center gap-3">
				<ServiceIcon service={service} size={34} />
				<div className="flex min-w-0 flex-1 flex-col gap-1.5">
					<div className="flex min-w-0 items-center gap-2.5">
						<SheetTitle
							title={service.name}
							className="truncate font-semibold text-base text-ink-1 leading-none tracking-[-0.01em]"
						>
							{service.name}
						</SheetTitle>
						<StatusPill kind={pill.kind}>{pill.label}</StatusPill>
						<ServiceConfigChip serviceId={service.id} className="shrink-0" />
					</div>
					{source.parts.length > 0 || source.domain ? (
						<p className="flex min-w-0 items-center gap-1.5 font-mono text-[11.5px] text-ink-3 leading-none">
							{source.kind === "git" ? (
								<GitBranchIcon aria-hidden className="size-3 shrink-0" />
							) : (
								<BoxIcon aria-hidden className="size-3 shrink-0" />
							)}
							{source.parts.length > 0 ? (
								<span
									title={source.parts.join(" · ")}
									className="min-w-0 truncate"
								>
									{source.parts.join(" · ")}
								</span>
							) : null}
							{source.domain ? (
								<>
									{source.parts.length > 0 ? <span aria-hidden>·</span> : null}
									{domainTarget ? (
										<OpenDomainLink target={domainTarget} />
									) : (
										<span className="min-w-0 truncate">
											{source.domain.hostname}
										</span>
									)}
									{source.domain.more > 0 ? (
										<span className="shrink-0">+{source.domain.more}</span>
									) : null}
								</>
							) : null}
						</p>
					) : (
						<p className="text-[11.5px] text-ink-3 leading-none">
							No source set yet
						</p>
					)}
				</div>
				<Button
					variant="ghost"
					size="icon-sm"
					className="max-[759px]:size-10"
					aria-label="Close inspector"
					title="Close (Esc)"
					onClick={onClose}
				>
					<XIcon aria-hidden />
				</Button>
			</div>
			<div className="flex items-center gap-1.5">
				<Button
					variant="secondary"
					size="sm"
					disabled={actions.deploy.isPending || inFlight}
					onClick={() => actions.deploy.mutate(undefined)}
					title={
						inFlight
							? "A deployment is already running"
							: service.hasStagedChanges
								? "Deploy the current configuration, with its staged changes"
								: "Deploy the current configuration again"
					}
				>
					<PendingRefreshIcon pending={actions.deploy.isPending} />
					{deployLabel(service)}
				</Button>
				{isRoute ? null : (
					<>
						<Button
							variant="secondary"
							size="sm"
							disabled={actions.restart.isPending || !deployed}
							onClick={() => actions.restart.mutate()}
							title={
								stopped
									? "Start the stopped container again"
									: "Restart the running container without a new deployment"
							}
						>
							{actions.restart.isPending ? (
								<Spinner />
							) : stopped ? (
								<PlayIcon aria-hidden />
							) : null}
							{stopped ? "Start" : "Restart"}
						</Button>
						<Button
							variant="ghost"
							size="sm"
							disabled={actions.stop.isPending || !deployed || stopped}
							onClick={() => void stop()}
						>
							<SquareIcon aria-hidden className="fill-current" />
							Stop
						</Button>
					</>
				)}
				<OpenSiteButton targets={targets} />
				<DropdownMenu open={menuOpen} onOpenChange={setMenuOpen}>
					<DropdownMenuTrigger
						render={
							<Button
								variant="ghost"
								size="icon-sm"
								className="ml-auto max-[759px]:size-10"
								aria-label={`More actions for ${service.name}`}
							/>
						}
					>
						<EllipsisIcon aria-hidden />
					</DropdownMenuTrigger>
					<DropdownMenuContent align="end">
						{isRoute || isStack ? null : (
							<DropdownMenuItem
								disabled={!serviceCanScale(service)}
								onClick={() => setScaleOpen(true)}
							>
								<GaugeIcon aria-hidden />
								Scale…
							</DropdownMenuItem>
						)}
						<DropdownMenuItem onClick={() => onTab("settings")}>
							<SettingsIcon aria-hidden />
							Open settings
						</DropdownMenuItem>
						{service.sourceType === "git" ? (
							<DropdownMenuItem
								disabled={
									actions.deploy.isPending ||
									actions.deployNoCache.isPending ||
									inFlight
								}
								onClick={() => void actions.confirmDeployNoCache()}
							>
								<HammerIcon aria-hidden />
								Redeploy without cache…
							</DropdownMenuItem>
						) : null}
						{isRoute || !deployed ? null : (
							<>
								<DropdownMenuItem onClick={() => openShell(service.id)}>
									<TerminalSquareIcon aria-hidden />
									Open shell…
								</DropdownMenuItem>
								<DropdownMenuItem onClick={() => openFiles(service.id)}>
									<FolderOpenIcon aria-hidden />
									Browse files…
								</DropdownMenuItem>
							</>
						)}
						{isRoute || isStack ? null : (
							<>
								<DropdownMenuItem onClick={() => setCloneOpen(true)}>
									<CopyPlusIcon aria-hidden />
									Clone…
								</DropdownMenuItem>
								<DropdownMenuItem onClick={() => setMoveOpen(true)}>
									<ArrowRightLeftIcon aria-hidden />
									Move to environment…
								</DropdownMenuItem>
							</>
						)}
						<DropdownMenuSeparator />
						{isRoute || isStack ? null : (
							<DropdownMenuItem
								closeOnClick={false}
								onClick={() =>
									void copy("host", "private host", service.privateHost)
								}
							>
								<CopyCheckIcon
									copied={copied === "host"}
									checkClassName="text-status-live-on!"
								/>
								Copy private host
							</DropdownMenuItem>
						)}
						<DropdownMenuItem
							closeOnClick={false}
							onClick={() => void copy("id", "service ID", service.id)}
						>
							<CopyCheckIcon
								copied={copied === "id"}
								checkClassName="text-status-live-on!"
							/>
							Copy service ID
						</DropdownMenuItem>
					</DropdownMenuContent>
				</DropdownMenu>
			</div>
			<ScaleDialog
				serviceId={service.id}
				open={scaleOpen}
				onOpenChange={setScaleOpen}
			/>
			<CloneServiceDialog
				service={service}
				open={cloneOpen}
				onOpenChange={setCloneOpen}
			/>
			<MoveServiceDialog
				service={service}
				open={moveOpen}
				onOpenChange={setMoveOpen}
			/>
		</header>
	);
}
