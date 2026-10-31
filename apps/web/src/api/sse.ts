// Server-sent events, through the generated client's SSE runtime
// (`client.sse.get`, which reuses the configured base URL, credentials and
// request interceptors). Two consumers:
//
// - the invalidation feed (`GET /events`): every event names a resource that
//   changed, and the matching queries refetch. Its connection state drives
//   the "Live updates paused — retrying" pill.
// - log streams (`useLogStream`): service, environment and deployment logs.
//
// Neither opens a connection itself. Browsers allow six HTTP/1.1 connections
// per host and a stream holds one, so every stream goes through the tab's
// `streamHub` (shared by key, at most `MAX_STREAMS_PER_TAB` at once, none
// while the tab is hidden) and the feed is shared across tabs by the
// `feed-relay` leader election.

import type { QueryClient } from "@tanstack/react-query";
import {
	useCallback,
	useEffect,
	useRef,
	useState,
	useSyncExternalStore,
} from "react";
import { ApiError, reportFailure } from "./client-config";
import {
	type FeedChannel,
	type FeedLocks,
	type FeedMessage,
	startFeedRelay,
} from "./feed-relay";
import { client } from "./gen/client.gen";
import { invalidateOperations } from "./invalidate";
import { mergeByTime } from "./log-order";
import { StreamHub, type StreamStatus } from "./stream-hub";

export type SseStatus = StreamStatus;

/** Hard cap on streams one tab holds open: the feed plus one log tail. */
export const MAX_STREAMS_PER_TAB = 2;

export const streamHub = new StreamHub(MAX_STREAMS_PER_TAB);

if (typeof document !== "undefined") {
	streamHub.setHidden(document.hidden);
	document.addEventListener("visibilitychange", () => {
		streamHub.setHidden(document.hidden);
	});
}

export interface OpenSseOptions<T> {
	/** Path template relative to the API base, e.g. `/services/{serviceId}/logs`. */
	url: string;
	path?: Record<string, string>;
	query?: Record<string, string | number | boolean | undefined>;
	signal: AbortSignal;
	onEvent(data: T, event: string | undefined): void;
	onStatus(status: Exclude<SseStatus, "waiting">): void;
	/**
	 * Keep reconnecting after the server closes the stream cleanly (the
	 * invalidation feed never ends). Log streams end with `event: end`.
	 */
	reconnect?: boolean;
}

const RETRY_BASE_MS = 1000;
const RETRY_MAX_MS = 30_000;

function failureStatus(error: unknown): number | undefined {
	const match = /SSE failed: (\d{3})/.exec(
		error instanceof Error ? error.message : "",
	);
	return match?.[1] ? Number(match[1]) : undefined;
}
function sleep(ms: number, signal: AbortSignal): Promise<void> {
	const { promise, resolve } = Promise.withResolvers<void>();
	const timer = setTimeout(resolve, ms);
	signal.addEventListener(
		"abort",
		() => {
			clearTimeout(timer);
			resolve();
		},
		{ once: true },
	);
	return promise;
}

/**
 * Opens one SSE stream and keeps it open (with exponential backoff) until
 * `signal` aborts, the server sends `event: end`, or — without `reconnect` —
 * the server closes it. Resolves when finished; never rejects.
 */
export async function openSse<T>(options: OpenSseOptions<T>): Promise<void> {
	const { signal } = options;
	let failures = 0;
	let ended = false;
	while (!signal.aborted && !ended) {
		options.onStatus("connecting");
		let failed = false;
		try {
			const { stream } = await client.sse.get({
				url: options.url,
				...(options.path ? { path: options.path } : {}),
				...(options.query ? { query: options.query } : {}),
				signal,
				headers: { Accept: "text/event-stream" },
				// One attempt per loop turn: this wrapper owns backoff so it can
				// report status and give up on a 401.
				sseMaxRetryAttempts: 1,
				onSseEvent: (event: { event?: string }) => {
					failures = 0;
					if (event.event === "end") {
						ended = true;
						options.onStatus("ended");
					} else {
						options.onStatus("live");
					}
				},
				onSseError: (error: unknown) => {
					failed = true;
					const status = failureStatus(error);
					if (status === 401 || status === 403) {
						reportFailure(
							new ApiError(status, "unauthorized", "Session ended."),
						);
						ended = true;
					}
				},
			});
			for await (const data of stream) {
				options.onEvent(data as T, undefined);
			}
		} catch {
			failed = true;
		}
		if (signal.aborted || ended) break;
		if (!failed && !options.reconnect) {
			options.onStatus("ended");
			break;
		}
		options.onStatus("paused");
		failures++;
		await sleep(
			Math.min(RETRY_BASE_MS * 2 ** (failures - 1), RETRY_MAX_MS),
			signal,
		);
	}
}

// --- The invalidation feed ---------------------------------------------------

export interface ResourceEvent {
	type: string;
	projectId?: string;
	environmentId?: string;
	resourceId?: string;
}

/** Generated query ids (operation ids) each event type makes stale. */
const INVALIDATES: Record<string, readonly string[]> = {
	"service.updated": [
		"listServices",
		"getServiceConfigSource",
		"getEnvironmentConfigSource",
		"getService",
		"listPendingChanges",
		"listDeployments",
		"listServers",
		"listVariables",
		"listLinks",
		"getFleet",
	],
	"service.deleted": [
		"listServices",
		"listLinks",
		"listServers",
		"getFleet",
		"listTemplateInstances",
	],
	// A service health incident opened, escalated or closed.
	"service.incident.updated": ["listServiceIncidents"],
	// A service's uptime state changed (down, recovered, maintenance, paused):
	// its inspector card and the environment's Uptime section follow.
	"uptime.state.changed": ["getServiceUptime", "getEnvironmentUptime"],
	"deployment.updated": [
		"listServerProxyRoutes",
		"listServerPorts",
		"getServerProxy",
		"getDatabase",
		"listServices",
		"getService",
		"listDeployments",
		"listEnvironmentDeployments",
		"getEnvironmentDeploymentsDaily",
		// A deploy applies the staged references: a stitched cable turns solid.
		"listLinks",
		"getDeployment",
		"listDeploymentEvents",
		"listDeploymentCommands",
		"getDeploymentRelease",
		"listPendingChanges",
		"listServers",
		"getFleet",
		"getServerDrain",
		"getEnvironmentMesh",
		"getServiceMesh",
	],
	"server.removed": ["listServers", "getFleet"],
	"server.updated": [
		"getInstanceUpgrade",
		"getServerProxy",
		"getServerTunnel",
		"getServerCleanup",
		"getServerHardening",
		"getServerPatches",
		"planServerPatches",
		"getServerPatchRun",
		"listCertificates",
		"listServerProxyRoutes",
		"listServerPorts",
		"listServers",
		"getServer",
		"listServices",
		"getFleet",
		"getServerDrain",
		"getMesh",
		"getServerMesh",
		"getEnvironmentMesh",
		"getServiceMesh",
	],
	"domain.updated": [
		"listServerProxyRoutes",
		"listServerPorts",
		"listDomains",
		"listServices",
		"getService",
		"listPendingChanges",
		"getServiceConfigSource",
	],
	"variable.updated": [
		"listVariables",
		"getVariableHistory",
		"listSuggestedVariables",
		"listProjectSharedVariables",
		"listEnvironmentSharedVariables",
		"getVariableUsage",
		"listServices",
		"getService",
		"listPendingChanges",
		// An edit in the dashboard is how an owned field turns into "edited here".
		"getServiceConfigSource",
		// A reference in a value draws a cable; deleting it takes the cable away.
		"listLinks",
	],
	"link.updated": ["listLinks", "getEnvironmentMesh", "getServiceMesh"],
	// Mesh nodes, links, grants or the access policy changed.
	"mesh.updated": [
		"getMesh",
		"getServerMesh",
		"getEnvironmentMesh",
		"getServiceMesh",
	],
	// A shared network was created, deleted, attached or detached.
	"network.updated": [
		"listNetworks",
		"getNetwork",
		"listServices",
		"getService",
		"listPendingChanges",
	],
	// A template install or stack delete rewrites the saved layout; the canvas
	// adopts it itself (see canvas-landing.ts), so only the stack list follows.
	"canvas.updated": ["listTemplateInstances"],
	"database.updated": [
		"getDatabaseReplication",
		"getDatabase",
		"listServices",
		"getService",
		"listPendingChanges",
		"listVariables",
	],
	"backup.updated": [
		"listBackups",
		"getBackup",
		"listBackupSchedules",
		"listBackupRestores",
		"getBackupRestore",
		// Volume backups share the event: their rows, config, restores and the
		// retired volumes a finished restore leaves behind.
		"getVolumeBackupConfig",
		"listVolumeBackups",
		"getVolumeBackup",
		"listVolumeRestores",
		"listRetiredVolumes",
		"listVolumes",
		"listPendingChanges",
		"listServices",
		"getService",
	],
	"backup.destination.updated": ["listBackupDestinations"],
	"instance.backup.updated": ["getInstanceBackup", "listInstanceBackupRuns"],
	// Agent versions, the automatic-upgrade setting and the backup taken before an upgrade.
	"instance.upgrade.updated": ["getInstanceUpgrade"],
	// A log drain's settings and its per-server delivery health.
	"log_drain.updated": ["listLogDrains", "getLogDrain"],
	// A platform service's progress and wiring; the integration it feeds
	// (destination, drain, tailnet) changes with it.
	"platform.updated": [
		"listPlatformServices",
		"getPlatformService",
		"listBackupDestinations",
		"listLogDrains",
		"getLogDrain",
		"getTailscaleIntegration",
	],
	// Capture, retention and caps of one server's stored logs.
	"server.log_settings.updated": ["getServerLogSettings"],
	// Channels of the notification settings and their delivery log.
	"notification.channel.updated": [
		"listNotificationChannels",
		"getNotificationChannel",
		"listNotificationDeliveries",
	],
	"notification.delivery.updated": [
		"listNotificationChannels",
		"listNotificationDeliveries",
	],
	// Cron tasks of a service and the runs (history, output) of tasks and one-off commands.
	"task.updated": ["listServiceTasks", "listServiceTaskRuns"],
	"task.run.updated": ["listServiceTasks", "listServiceTaskRuns", "getTaskRun"],
	// A preview's status, its environment (switcher grouping) and its services.
	"preview.updated": [
		"listPreviews",
		"getPreviewSettings",
		"listEnvironments",
		"listServices",
	],
	// A git connection's state and its webhook deliveries; links show on services.
	"git.connection.updated": [
		"listGitConnections",
		"getGitConnection",
		"getServiceGit",
		"listGitDeliveries",
	],
	"git.delivery.created": ["listGitDeliveries", "listGitConnections"],
	"project.created": ["listProjects"],
	"project.updated": ["listProjects", "listEnvironments"],
	"project.deleted": ["listProjects", "listEnvironments"],
	"environment.created": ["listEnvironments"],
	"environment.updated": ["listEnvironments"],
	"environment.deleted": ["listEnvironments"],
	// nebula.toml: a push was checked, a file was applied or a binding changed.
	// The banner, the chips and the field marks follow; an apply also moved
	// the staged changes and what the services show.
	"config.updated": [
		"getEnvironmentConfigSource",
		"getServiceConfigSource",
		"listPendingChanges",
		"listServices",
		"getService",
	],
	// Teams: the switcher, the member table and the invite list.
	"team.created": ["listTeams"],
	"team.updated": ["listTeams"],
	"team.deleted": ["listTeams"],
	"member.updated": ["listTeams", "listTeamMembers"],
	"member.removed": ["listTeams", "listTeamMembers"],
	"member.joined": ["listTeams", "listTeamMembers", "listTeamInvites"],
	"invite.created": ["listTeamInvites"],
	"invite.revoked": ["listTeamInvites"],
	"instance.updated": ["getInstance"],
	// Status pages, their components, incidents and maintenance windows.
	"status_page.updated": [
		"listStatusPages",
		"getStatusPage",
		"checkStatusPageDns",
		"listStatusIncidents",
		"getStatusIncident",
		"listMaintenanceWindows",
		"getMaintenanceWindow",
	],
};

const COALESCE_MS = 150;

let feedStatus: SseStatus = "connecting";
const feedListeners = new Set<() => void>();

function setFeedStatus(next: SseStatus) {
	if (feedStatus === next) return;
	feedStatus = next;
	for (const listener of feedListeners) listener();
}

/** The broadcast channel the tabs share the feed on; absent where unsupported. */
function openFeedChannel(): FeedChannel<ResourceEvent> | undefined {
	if (typeof BroadcastChannel === "undefined") return undefined;
	const channel = new BroadcastChannel("nebulactrl-events");
	return {
		post: (message) => channel.postMessage(message),
		listen: (handler) => {
			channel.onmessage = (event: MessageEvent<FeedMessage<ResourceEvent>>) =>
				handler(event.data);
		},
		close: () => channel.close(),
	};
}

/**
 * Starts this tab's share of the `/events` feed and invalidates the queries
 * each event names. One tab per browser holds the stream (see `feed-relay`);
 * this tab hears the others' events too. Events arrive in bursts (a deploy
 * emits many), so ids are coalesced into one invalidation pass per 150 ms.
 * Returns the stop function.
 */
export function startEventsFeed(queryClient: QueryClient): () => void {
	const pending = new Set<string>();
	let timer: ReturnType<typeof setTimeout> | undefined;

	const flush = () => {
		timer = undefined;
		const ids = [...pending];
		pending.clear();
		void invalidateOperations(queryClient, ids);
	};

	const stopRelay = startFeedRelay<ResourceEvent>({
		locks:
			typeof navigator === "undefined"
				? undefined
				: (navigator.locks as FeedLocks | undefined),
		channel: openFeedChannel(),
		connect: ({ onEvent, onStatus }, shared) =>
			streamHub.subscribe<ResourceEvent>(
				{
					key: "feed:events",
					pinned: true,
					// The leader's stream serves every tab, so a hidden leader must
					// keep it; a tab with its own stream releases it while hidden.
					background: shared,
					open: ({ signal, onEvent: emit, onStatus: report }) =>
						openSse<ResourceEvent>({
							url: "/events",
							signal,
							reconnect: true,
							onStatus: report,
							onEvent: emit,
						}),
				},
				{ onEvent, onStatus },
			),
		onStatus: setFeedStatus,
		onEvent: (event) => {
			for (const id of INVALIDATES[event.type] ?? []) pending.add(id);
			timer ??= setTimeout(flush, COALESCE_MS);
		},
		onResync: () => void queryClient.invalidateQueries(),
	});

	// After an outage, or a stretch in a hidden tab, the feed missed events, so
	// refetch once it is back.
	let missed = false;
	const unsubscribe = subscribeFeed(() => {
		if (feedStatus === "paused" || feedStatus === "waiting") missed = true;
		else if (feedStatus === "live" && missed) {
			missed = false;
			void queryClient.invalidateQueries();
		}
	});

	return () => {
		stopRelay();
		clearTimeout(timer);
		unsubscribe();
		setFeedStatus("connecting");
	};
}

function subscribeFeed(listener: () => void): () => void {
	feedListeners.add(listener);
	return () => {
		feedListeners.delete(listener);
	};
}

/** The invalidation feed's connection state. */
export function useEventsStatus(): SseStatus {
	return useSyncExternalStore(
		subscribeFeed,
		() => feedStatus,
		() => feedStatus,
	);
}

// --- Log streams -------------------------------------------------------------

export interface LogLine {
	/** Monotonic per stream; a stable React key. */
	id: number;
	ts?: string;
	line: string;
	/** `build` | `deploy` | `runtime` (deployment streams only). */
	stream?: string;
	serviceId?: string;
	serviceName?: string;
	/** Stored history lines carry them so the `replica:` and `deployment:` facets work. */
	replica?: number | string;
	deploymentId?: string;
}

interface LogFrame {
	ts?: string;
	line: string;
	stream?: string;
	serviceId?: string;
	serviceName?: string;
}

export type LogStreamKind =
	| "service"
	| "environment"
	| "deployment"
	| "taskRun";

export interface UseLogStreamOptions {
	kind: LogStreamKind;
	/** Service, environment or deployment id, per `kind`. */
	id: string | undefined;
	/** Lines of history to start from (service/environment streams). */
	tail?: number;
	/** Set false to stop streaming (e.g. a paused tail or a hidden tab). */
	enabled?: boolean;
	/** Oldest lines are dropped past this many. */
	maxLines?: number;
	/** Compose stacks only: show one component's lines (service streams). */
	component?: string | undefined;
	/**
	 * Services with processes only: show one process' lines (service
	 * streams). Without it every process streams, each line prefixed
	 * `<process> | `.
	 */
	process?: string | undefined;
	/**
	 * Deployment streams only: `all` adds the debug stream, which the ordinary
	 * log leaves out; or a comma-separated list (`debug`, `build,deploy`).
	 */
	streams?: string | undefined;
}

export interface LogStream {
	lines: LogLine[];
	/**
	 * Changes when a line was placed before ones already in `lines` (an
	 * environment tail merges its services by time). Anything that keeps the
	 * lines by position starts over then.
	 */
	epoch: number;
	status: SseStatus;
	clear(): void;
}

const FLUSH_MS = 100;
/** Frames a shared log stream keeps for a view that joins it late. */
const LOG_REPLAY = 10_100;

/**
 * Streams logs into React state. Frames are buffered and flushed every
 * 100 ms so a chatty container costs one render per tick, not per line.
 * Changing `kind`, `id`, `tail`, `component`, `process` or `streams` starts a
 * fresh stream.
 */
export function useLogStream({
	kind,
	id,
	tail = 200,
	enabled = true,
	maxLines = 5000,
	component,
	process,
	streams,
}: UseLogStreamOptions): LogStream {
	const [shown, setShown] = useState<{ lines: LogLine[]; epoch: number }>({
		lines: [],
		epoch: 0,
	});
	const [status, setStatus] = useState<SseStatus>("connecting");
	const counter = useRef(0);

	useEffect(() => {
		setShown((current) => ({ lines: [], epoch: current.epoch }));
		counter.current = 0;
		if (!id || !enabled) {
			setStatus("ended");
			return;
		}
		let buffer: LogLine[] = [];
		const timer = setInterval(() => {
			if (buffer.length === 0) return;
			const batch = buffer;
			buffer = [];
			setShown((current) => {
				// Services each tail their own history, so an environment's lines
				// arrive service by service; the others are already in order.
				if (kind === "environment") {
					const merged = mergeByTime(current.lines, batch);
					return {
						lines:
							merged.lines.length > maxLines
								? merged.lines.slice(-maxLines)
								: merged.lines,
						epoch: merged.reordered ? current.epoch + 1 : current.epoch,
					};
				}
				const lines =
					current.lines.length === 0 ? batch : current.lines.concat(batch);
				return {
					lines: lines.length > maxLines ? lines.slice(-maxLines) : lines,
					epoch: current.epoch,
				};
			});
		}, FLUSH_MS);

		const route = {
			service: { url: "/services/{serviceId}/logs", path: { serviceId: id } },
			environment: {
				url: "/environments/{environmentId}/logs",
				path: { environmentId: id },
			},
			deployment: {
				url: "/deployments/{deploymentId}/logs",
				path: { deploymentId: id },
			},
			taskRun: {
				url: "/task-runs/{runId}/logs",
				path: { runId: id },
			},
		}[kind];
		const base =
			kind === "deployment" || kind === "taskRun"
				? { follow: true }
				: { follow: true, tail: Math.max(0, tail) };
		// A Compose stack's lines carry a "<component> | " prefix; the server filters on it.
		const withComponent =
			kind === "service" && component ? { ...base, component } : base;
		// A service with processes: the server filters by the process label.
		const withProcess =
			kind === "service" && process
				? { ...withComponent, process }
				: withComponent;
		const query =
			kind === "deployment" && streams
				? { ...withProcess, streams }
				: withProcess;

		// Same URL and query, same stream: two views of one tail share a connection.
		const key = `${route.url}|${JSON.stringify(route.path)}|${JSON.stringify(query)}`;
		const unsubscribe = streamHub.subscribe<LogFrame>(
			{
				key,
				replay: LOG_REPLAY,
				open: ({ signal, onEvent, onStatus }) =>
					openSse<LogFrame>({ ...route, query, signal, onStatus, onEvent }),
			},
			{
				onStatus: setStatus,
				// The stream starts over (it waited for a free connection, or the
				// tab was hidden) and replays its tail: drop what is on screen.
				onRestart: () => {
					buffer = [];
					counter.current = 0;
					setShown((current) => ({ lines: [], epoch: current.epoch }));
				},
				onEvent: (frame) => {
					if (typeof frame?.line !== "string") return;
					counter.current += 1;
					const entry: LogLine = { id: counter.current, line: frame.line };
					if (frame.ts) entry.ts = frame.ts;
					if (frame.stream) entry.stream = frame.stream;
					if (frame.serviceId) entry.serviceId = frame.serviceId;
					if (frame.serviceName) entry.serviceName = frame.serviceName;
					buffer.push(entry);
				},
			},
		);

		return () => {
			unsubscribe();
			clearInterval(timer);
		};
	}, [kind, id, tail, enabled, maxLines, component, process, streams]);

	const clear = useCallback(
		() => setShown((current) => ({ lines: [], epoch: current.epoch })),
		[],
	);
	return { lines: shown.lines, epoch: shown.epoch, status, clear };
}
