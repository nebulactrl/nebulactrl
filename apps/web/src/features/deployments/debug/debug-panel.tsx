import { Dialog as DialogPrimitive } from "@base-ui/react/dialog";
import { exact } from "@nebulactrl/tokens";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { BugIcon, ChevronLeftIcon, XIcon } from "lucide-react";
import { motion, useReducedMotion } from "motion/react";
import { useEffect, useRef, useState } from "react";
import { errorMessage } from "#/api/client-config";
import {
	deploymentEventsQuery,
	deploymentQuery,
	serversQuery,
	serviceQuery,
} from "#/api/hooks";
import { useLogStream } from "#/api/sse";
import {
	COPIED_MS,
	CopyButton,
	writeClipboard,
} from "#/components/common/copy-button";
import { ErrorState } from "#/components/common/error-state";
import { Relative } from "#/components/common/relative";
import { CopyCheckIcon } from "#/components/icons/animated/copy-check-icon";
import { TextMorph } from "#/components/motion/text-morph";
import { Button } from "#/components/ui/button";
import {
	SheetContent,
	SheetDescription,
	SheetTitle,
} from "#/components/ui/sheet";
import { OPEN_LAYER_ABOVE_OVER_SHEET_SELECTOR } from "#/components/ui/sheet-dismiss";
import { Skeleton } from "#/components/ui/skeleton";
import { StatusIcon } from "#/components/ui/status-icon";
import { Tabs, TabsContent, TabsList, TabsTrigger } from "#/components/ui/tabs";
import { toast } from "#/components/ui/toast";
import { EASE_OUT } from "#/lib/ease";
import { absoluteTime, formatDuration } from "#/lib/format";
import { lastInputWasKeyboard } from "#/lib/input-modality";
import {
	deploymentMessage,
	durationMs,
	isInFlight,
	STATUS_KIND,
	STATUS_LABEL,
	TRIGGER_LABEL,
} from "../labels";
import { CommandsTab } from "./commands-tab";
import { LogTab } from "./log-tab";
import {
	deploymentCommandsQuery,
	deploymentReleaseQuery,
	toDebugCommands,
	toDebugRelease,
} from "./queries";
import { ReleaseTab } from "./release-tab";
import { buildDebugReport } from "./report";
import { TimelineTab } from "./timeline-tab";

const DEBUG_TABS = ["timeline", "log", "release", "commands"] as const;
type DebugTab = (typeof DEBUG_TABS)[number];

const TAB_LABEL: Record<DebugTab, string> = {
	timeline: "Timeline",
	log: "Debug log",
	release: "Release",
	commands: "Agent commands",
};

/** The panel's width: wider than the inspector it covers, full screen under 760px. */
const DEBUG_PANEL_WIDTH = "clamp(720px, 70vw, 1200px)";

/** What the panel keeps showing while it slides out. */
export interface DebugPanelProps {
	/** The deployment to debug; kept by the caller while the panel closes. */
	deploymentId: string;
	open: boolean;
	onClose(): void;
}

/**
 * The deployment debug panel: a sheet stacked above the inspector (above it in
 * z-order, wider than it) with four tabs: the raw event Timeline, the Debug log
 * (every stream, including the debug one the ordinary log hides), the Release
 * and the Agent commands. Escape or the close button returns to the inspector.
 * It is a layer above another sheet (`data-layer="over"`): Escape belongs to it
 * first, and the inspector's chords stay quiet while it is open.
 */
export function DeploymentDebugPanel({
	deploymentId,
	open,
	onClose,
}: DebugPanelProps) {
	return (
		<DialogPrimitive.Root
			open={open}
			modal={false}
			disablePointerDismissal
			onOpenChange={(next, details) => {
				if (next) return;
				// Only Escape and the close button close it. Escape belongs to a
				// menu or dialog opened inside the panel first.
				if (
					details.reason === "focus-out" ||
					(details.reason === "escape-key" &&
						document.querySelector(OPEN_LAYER_ABOVE_OVER_SHEET_SELECTOR) !==
							null)
				) {
					details.cancel();
					return;
				}
				onClose();
			}}
		>
			<SheetContent
				variant="docked"
				motion="slide"
				showCloseButton={false}
				width={DEBUG_PANEL_WIDTH}
				data-layer="over"
				data-screen-label="deployment-debug"
				className="top-[52px] z-[45] border-line border-l max-[759px]:inset-0 max-[759px]:w-auto! max-[759px]:max-w-none max-[759px]:border-l-0"
				initialFocus={() =>
					document.querySelector<HTMLElement>(
						'[data-screen-label="deployment-debug"]',
					)
				}
			>
				<DebugBody deploymentId={deploymentId} open={open} onClose={onClose} />
			</SheetContent>
		</DialogPrimitive.Root>
	);
}

function DebugBody({ deploymentId, open, onClose }: DebugPanelProps) {
	const queryClient = useQueryClient();
	const [tab, setTab] = useState<DebugTab>("timeline");
	// Opened with `d`: everything is there at once, nothing moves (a keyboard
	// action never waits on an animation).
	const [still] = useState(lastInputWasKeyboard);
	const deployment = useQuery({
		...deploymentQuery(deploymentId),
		enabled: open,
	});
	const d = deployment.data;
	const running = d ? isInFlight(d.status) : false;
	const service = useQuery({
		...serviceQuery(d?.serviceId ?? ""),
		enabled: open && d !== undefined,
	});
	const servers = useQuery({ ...serversQuery(), enabled: open });
	const events = useQuery({
		...deploymentEventsQuery(deploymentId),
		enabled: open,
	});
	const release = useQuery({
		...deploymentReleaseQuery(deploymentId, running),
		select: toDebugRelease,
		enabled: open && tab === "release",
	});
	const commands = useQuery({
		...deploymentCommandsQuery(deploymentId, running),
		select: toDebugCommands,
		enabled: open && tab === "commands",
	});
	// The panel's own live stream: every stream including debug. The inspector
	// behind lets go of its log stream while this is open (two streams per tab).
	const stream = useLogStream({
		kind: "deployment",
		id: deploymentId,
		streams: "all",
		maxLines: 10_600,
		enabled: open,
	});

	const serverName = (id: string | null | undefined) =>
		id ? (servers.data?.find((s) => s.id === id)?.name ?? null) : null;

	const [copied, setCopied] = useState(false);
	const [copying, setCopying] = useState(false);
	const copiedTimer = useRef<number | undefined>(undefined);
	useEffect(() => () => window.clearTimeout(copiedTimer.current), []);

	// Escape returns to the inspector wherever focus sits (it may still be on
	// the page behind when the panel opens from a reload); a menu or dialog
	// opened inside the panel takes the key first.
	const onCloseRef = useRef(onClose);
	onCloseRef.current = onClose;
	useEffect(() => {
		if (!open) return;
		const onKeyDown = (event: KeyboardEvent) => {
			if (event.key !== "Escape" || event.defaultPrevented) return;
			if (document.querySelector(OPEN_LAYER_ABOVE_OVER_SHEET_SELECTOR) !== null)
				return;
			onCloseRef.current();
		};
		document.addEventListener("keydown", onKeyDown);
		return () => document.removeEventListener("keydown", onKeyDown);
	}, [open]);

	const copyReport = async () => {
		if (!d || copying) return;
		setCopying(true);
		try {
			// Fresh reads: the report is what the control plane says now.
			const [freshEvents, freshRelease, freshCommands] = await Promise.all([
				queryClient.fetchQuery(deploymentEventsQuery(deploymentId)),
				queryClient
					.fetchQuery(deploymentReleaseQuery(deploymentId, false))
					.then(toDebugRelease)
					.catch(() => null),
				queryClient
					.fetchQuery(deploymentCommandsQuery(deploymentId, false))
					.then(toDebugCommands)
					.catch(() => null),
			]);
			const text = buildDebugReport({
				deployment: d,
				serviceName: service.data?.name ?? d.serviceId,
				serverName: serverName(d.serverId),
				buildServerName: serverName(d.buildServerId),
				events: freshEvents,
				release: freshRelease,
				commands: freshCommands,
				logLines: stream.lines,
				generatedAt: new Date().toISOString(),
				url: window.location.href,
			});
			if (await writeClipboard(text)) {
				setCopied(true);
				window.clearTimeout(copiedTimer.current);
				copiedTimer.current = window.setTimeout(
					() => setCopied(false),
					COPIED_MS,
				);
				toast.success(
					"Debug report copied",
					"Secrets are masked. Paste it into a bug report.",
				);
			} else {
				toast.error(
					"Could not copy the report",
					"The browser blocked clipboard access.",
				);
			}
		} catch (error) {
			toast.error("Could not build the report", errorMessage(error));
		} finally {
			setCopying(false);
		}
	};

	const message = d ? deploymentMessage(d).text : null;
	const serviceName = service.data?.name;
	const eventCount = events.data?.length;
	const lineCount = stream.lines.length;

	return (
		<>
			<header className="flex shrink-0 flex-col gap-3 border-line border-b px-5 pt-3 pb-3.5">
				<Rise index={0} still={still} className="flex items-center gap-2">
					<Button
						variant="ghost"
						size="sm"
						onClick={onClose}
						aria-label={
							serviceName ? `Back to ${serviceName}` : "Back to the service"
						}
						className="-ml-2 min-w-0 gap-1 pr-2.5 pl-1.5 text-ink-3 hover:text-ink-1"
					>
						<ChevronLeftIcon aria-hidden />
						<span className="truncate">{serviceName ?? "Back"}</span>
						<span aria-hidden className="text-ink-4">
							/ Deployments
						</span>
					</Button>
					<span className="flex-1" />
					<Button
						variant="secondary"
						size="sm"
						onClick={() => void copyReport()}
						disabled={!d || copying}
						aria-busy={copying}
						className={copied ? "text-status-live-on" : undefined}
					>
						<CopyCheckIcon
							copied={copied}
							checkClassName="text-status-live-on"
						/>
						<TextMorph text={copied ? "Copied" : "Copy debug report"} />
					</Button>
					<DialogPrimitive.Close
						render={<Button variant="ghost" size="icon" />}
						aria-label="Close the debug panel"
						title="Close (Esc)"
						onClick={onClose}
					>
						<XIcon aria-hidden />
					</DialogPrimitive.Close>
				</Rise>

				<Rise index={0} still={still} className="flex items-start gap-3">
					<span
						aria-hidden
						className="surface-well mt-0.5 grid size-9 shrink-0 place-items-center rounded-ctl text-ink-2"
					>
						<BugIcon className="size-4" />
					</span>
					<div className="flex min-w-0 flex-1 flex-col gap-1">
						<SheetTitle className="flex min-w-0 items-center gap-2 text-[17px]">
							Debug deployment
							{d ? (
								<span className="flex shrink-0 items-center gap-1.5 font-medium text-[12px] text-ink-2">
									<StatusIcon kind={STATUS_KIND[d.status]} label="" />
									{STATUS_LABEL[d.status]}
								</span>
							) : null}
						</SheetTitle>
						<SheetDescription className="flex min-w-0 items-center gap-1.5">
							{message ? (
								<span className="min-w-0 truncate" title={message}>
									{message}
								</span>
							) : null}
							<span className="shrink-0 font-mono text-[11.5px] text-ink-3">
								<span aria-hidden>{message ? "· " : ""}</span>
								<span title={deploymentId}>{deploymentId.slice(0, 8)}</span>
							</span>
							<CopyButton
								value={deploymentId}
								label="Copy the deployment ID"
								variant="ghost"
								size="icon-xs"
							/>
						</SheetDescription>
					</div>
				</Rise>

				{deployment.isPending ? (
					<Skeleton className="h-9 w-full" />
				) : deployment.isError || d === undefined ? (
					<ErrorState
						title="The deployment did not load"
						body="It may have been deleted, or you may not have access. Nothing was changed."
						retry={() => void deployment.refetch()}
					/>
				) : (
					<Rise
						index={1}
						still={still}
						as="dl"
						className="surface-well grid grid-cols-2 gap-x-5 gap-y-2.5 rounded-ctl px-3.5 py-2.5 text-xs sm:grid-cols-3 lg:grid-cols-5"
					>
						<Meta label="Trigger">{TRIGGER_LABEL[d.trigger]}</Meta>
						<Meta label="Actor">{d.actorLabel}</Meta>
						<Meta label="Started">
							<span title={absoluteTime(d.startedAt ?? d.createdAt)}>
								<Relative iso={d.startedAt ?? d.createdAt} />
							</span>
						</Meta>
						<Meta label={running ? "Running for" : "Duration"}>
							<span className="font-mono tabular-nums">
								{formatDuration(durationMs(d) ?? 0)}
							</span>
						</Meta>
						<Meta label={d.buildServerId ? "Server · built on" : "Server"}>
							<span className="min-w-0 truncate">
								{serverName(d.serverId) ?? "—"}
								{d.buildServerId
									? ` · ${serverName(d.buildServerId) ?? "build server"}`
									: ""}
							</span>
						</Meta>
					</Rise>
				)}
			</header>

			<Rise index={2} still={still} className="flex min-h-0 flex-1 flex-col">
				<Tabs
					value={tab}
					onValueChange={(next) => setTab(next as DebugTab)}
					className="min-h-0 flex-1"
				>
					<TabsList
						variant="sheet"
						aria-label="Debug sections"
						className="shrink-0 gap-6 border-line border-b px-5"
					>
						{DEBUG_TABS.map((id) => {
							const count =
								id === "timeline"
									? eventCount
									: id === "log" && lineCount > 0
										? lineCount
										: undefined;
							return (
								<TabsTrigger
									key={id}
									value={id}
									className="h-10 shrink-0 gap-1.5 font-medium text-[13px]"
								>
									{TAB_LABEL[id]}
									{count !== undefined ? (
										<span
											aria-hidden
											className="font-mono text-[11px] text-ink-3 tabular-nums"
										>
											{count.toLocaleString()}
										</span>
									) : null}
								</TabsTrigger>
							);
						})}
					</TabsList>
					<TabsContent
						value="timeline"
						className="min-h-0 flex-1 overflow-y-auto overscroll-contain"
					>
						<TimelineTab
							events={events.data}
							pending={events.isPending}
							error={events.isError}
							onRetry={() => void events.refetch()}
							originIso={d?.createdAt ?? ""}
							live={running}
						/>
					</TabsContent>
					<TabsContent value="log" className="min-h-0 flex-1">
						<LogTab stream={stream} running={running} />
					</TabsContent>
					<TabsContent
						value="release"
						className="min-h-0 flex-1 overflow-y-auto overscroll-contain"
					>
						<ReleaseTab
							release={release.data}
							pending={release.isPending}
							error={release.error}
							onRetry={() => void release.refetch()}
						/>
					</TabsContent>
					<TabsContent
						value="commands"
						className="min-h-0 flex-1 overflow-y-auto overscroll-contain"
					>
						<CommandsTab
							commands={commands.data}
							pending={commands.isPending}
							error={commands.isError}
							onRetry={() => void commands.refetch()}
						/>
					</TabsContent>
				</Tabs>
			</Rise>
		</>
	);
}

/**
 * One chunk of the panel's staged entrance (better-ui, exact): opacity, a
 * 4px blur and a 12px rise over 300ms, chunks 100ms apart, starting while
 * the panel is still sliding in so the two read as one motion. Runs once, on
 * open. `still` (opened from the keyboard) shows it at once; under reduced
 * motion it only fades. The settled chunk keeps no transform or filter, so it
 * does not become a containing block for what it holds.
 */
function Rise({
	index,
	still,
	as = "div",
	className,
	children,
}: {
	index: number;
	still: boolean;
	as?: "div" | "dl";
	className?: string;
	children: React.ReactNode;
}) {
	const reduced = useReducedMotion() ?? false;
	const Tag = as === "dl" ? motion.dl : motion.div;
	const { enter } = exact;
	const from = reduced
		? { opacity: 0 }
		: {
				opacity: 0,
				transform: `translateY(${enter.rise}px)`,
				filter: `blur(${enter.blur}px)`,
			};
	return (
		<Tag
			className={className}
			initial={still ? false : from}
			animate={{
				opacity: 1,
				transform: "translateY(0px)",
				filter: "blur(0px)",
				transitionEnd: { transform: "none", filter: "none" },
			}}
			transition={{
				duration: enter.duration / 1000,
				ease: EASE_OUT,
				delay: (RISE_DELAY_MS + index * enter.stagger) / 1000,
			}}
		>
			{children}
		</Tag>
	);
}

/** The entrance starts once the panel has moved far enough to carry it. */
const RISE_DELAY_MS = 80;

function Meta({
	label,
	children,
}: {
	label: string;
	children: React.ReactNode;
}) {
	return (
		<div className="flex min-w-0 flex-col gap-0.5">
			<dt className="type-micro text-ink-3">{label}</dt>
			<dd className="min-w-0 text-ink-1">{children}</dd>
		</div>
	);
}
