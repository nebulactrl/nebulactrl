import type { StatusKind } from "#/components/ui/status-icon";
import { quoteArg } from "#/features/service-settings/command-line";
import { NODE_COMPONENT_LIMIT } from "#/features/stacks/model";

// The pure side of a service that runs several processes: one build, one
// image and one release, started as separate containers. The control plane
// sends the processes in rollout order, the default one first; the default
// process is the service's own port and start command, so a service without
// `processes` has none of this.

/** The default process' name when the service does not set `processName`. */
export const DEFAULT_PROCESS_NAME = "web";

/** The most processes a service may have, the default one included. */
export const MAX_PROCESSES = 10;

/** Same pattern the control plane applies: a DNS label of 1 to 31 characters. */
export const PROCESS_NAME_PATTERN = /^[a-z0-9]([a-z0-9-]{0,29}[a-z0-9])?$/;

/** What the agent reports of one process. */
export interface ObservedProcessLike {
	state: string;
	health: string;
	replicasReady: number;
	replicasDesired: number;
}

/** One process as the service view carries it. */
export interface ProcessLike {
	name: string;
	default: boolean;
	port: number;
	command: readonly string[];
	singleton: boolean;
	scaling: {
		replicas: number;
		ready: number;
		autoscaling?: { enabled: boolean } | undefined;
	};
	observed: ObservedProcessLike | null;
	/** In the staged config, not in the running release. */
	pending: boolean;
	/** In the running release, dropped by the staged config. */
	removed: boolean;
}

export interface ProcessSource {
	processes: readonly ProcessLike[];
}

/** True when the service runs more than its default process. */
export function hasProcesses(service: ProcessSource): boolean {
	return service.processes.length > 0;
}

const NO_PROCESSES: readonly ProcessLike[] = [];

/** The processes of a service in rollout order; none for a single-process service. */
export function serviceProcesses(
	service: Partial<ProcessSource>,
): readonly ProcessLike[] {
	return service.processes ?? NO_PROCESSES;
}

/** The name of the service's own process: `processName`, else "web". */
export function defaultProcessName(config: {
	processName?: string | undefined;
}): string {
	return config.processName || DEFAULT_PROCESS_NAME;
}

// --- states -----------------------------------------------------------------

export interface ProcessState {
	kind: StatusKind;
	label: string;
}

/**
 * One process' state in words and a status icon. Mirrors a stack component's
 * (`componentState`): the agent aggregates the containers of the process, so
 * `health` is `healthy` only when every desired replica is. A process the
 * agent has not reported is "Not started".
 */
export function processState(
	observed: Pick<ObservedProcessLike, "state" | "health"> | null | undefined,
): ProcessState {
	if (!observed) return { kind: "pending", label: "Not started" };
	switch (observed.state) {
		case "running":
			switch (observed.health) {
				case "healthy":
					return { kind: "healthy", label: "Healthy" };
				case "unhealthy":
					return { kind: "failed", label: "Unhealthy" };
				case "degraded":
					return { kind: "degraded", label: "Degraded" };
				case "starting":
					return { kind: "building", label: "Starting" };
				default:
					return { kind: "live", label: "Running" };
			}
		case "restarting":
			return { kind: "degraded", label: "Restarting" };
		case "exited":
			return { kind: "failed", label: "Exited" };
		case "created":
			return { kind: "building", label: "Starting" };
		case "missing":
			return { kind: "failed", label: "Missing" };
		default:
			return { kind: "unknown", label: observed.state || "Unknown" };
	}
}

// --- rows -------------------------------------------------------------------

/** The longest command summary a row shows. */
const COMMAND_SUMMARY_LIMIT = 40;

/** What a process with no start command runs: the image's own. */
const IMAGE_COMMAND = "image command";

/**
 * The start command on one line: the first three arguments, quoted like the
 * settings field shows them, cut with an ellipsis at 40 characters. An empty
 * argv is the image's own command.
 */
export function commandSummary(argv: readonly string[]): string {
	if (argv.length === 0) return IMAGE_COMMAND;
	const head = argv.slice(0, 3).map(quoteArg).join(" ");
	const text = argv.length > 3 ? `${head} …` : head;
	if (text.length <= COMMAND_SUMMARY_LIMIT) return text;
	return `${text.slice(0, COMMAND_SUMMARY_LIMIT - 1).trimEnd()}…`;
}

/**
 * Ready over desired: "2/2", "0/1 · singleton" for a process that never runs
 * two instances, "2/3 · auto" while autoscaling picks the count.
 */
export function replicasText(process: ProcessLike): string {
	const ready = process.observed?.replicasReady ?? process.scaling.ready;
	const desired = process.observed?.replicasDesired ?? process.scaling.replicas;
	let text = `${ready}/${desired}`;
	if (process.singleton) text += " · singleton";
	if (process.scaling.autoscaling?.enabled) text += " · auto";
	return text;
}

/**
 * The replicas that belong to one row. A replica without a process label
 * belongs to the default process, and so does one whose label names no row:
 * the agent still labels it with the default process' name from before a
 * staged rename.
 */
export function replicasOfProcess<T extends { process: string }>(
	rows: readonly Pick<ProcessRow, "name" | "isDefault">[],
	replicas: readonly T[],
	name: string,
): T[] {
	const isDefault = rows.find((row) => row.name === name)?.isDefault ?? false;
	return replicas.filter((replica) => {
		if (replica.process === name) return true;
		if (!isDefault) return false;
		return (
			replica.process === "" ||
			!rows.some((row) => row.name === replica.process)
		);
	});
}

export interface ProcessRow {
	name: string;
	isDefault: boolean;
	/** The quiet "default" tag: only a default process without a port needs it (otherwise the port says it). */
	showDefaultTag: boolean;
	commandSummary: string;
	/** 0 when the process serves no HTTP. */
	port: number;
	replicasText: string;
	/** True while autoscaling picks the replica count. */
	autoscaling: boolean;
	kind: StatusKind;
	stateLabel: string;
	/** The word beside a process the running release does not match; null otherwise. */
	badge: "staged" | "removed" | null;
	pending: boolean;
	removed: boolean;
}

export function processRow(process: ProcessLike): ProcessRow {
	const state = processState(process.observed);
	const badge = process.removed ? "removed" : process.pending ? "staged" : null;
	let kind = state.kind;
	let stateLabel = state.label;
	if (process.removed) {
		kind = process.observed ? state.kind : "stopped";
		stateLabel = "Removed";
	} else if (process.pending && !process.observed) {
		kind = "pending";
		stateLabel = "Staged";
	}
	return {
		name: process.name,
		isDefault: process.default,
		showDefaultTag: process.default && process.port === 0,
		commandSummary: commandSummary(process.command),
		port: process.port,
		replicasText: replicasText(process),
		autoscaling: process.scaling.autoscaling?.enabled === true,
		kind,
		stateLabel,
		badge,
		pending: process.pending,
		removed: process.removed,
	};
}

/** One row per process, in the order the control plane sends them. */
export function processRows(service: Partial<ProcessSource>): ProcessRow[] {
	return serviceProcesses(service).map(processRow);
}

// --- the canvas card --------------------------------------------------------

/** The most process rows the card lists before "+N more". */
export const NODE_PROCESS_LIMIT = NODE_COMPONENT_LIMIT;

export interface NodeProcessSummary {
	shown: ProcessRow[];
	/** Processes beyond the limit: the card says "+N more". */
	more: number;
	total: number;
}

const isTrouble = (row: ProcessRow): boolean =>
	row.kind === "failed" || row.kind === "degraded";

/**
 * The rows of a service's card. Up to `limit` processes it lists them all in
 * rollout order; past it, failed and degraded processes lead (rollout order
 * within each group), so a problem is never behind "+N more".
 */
export function nodeProcessSummary(
	service: Partial<ProcessSource>,
	limit = NODE_PROCESS_LIMIT,
): NodeProcessSummary {
	const all = processRows(service);
	const ordered =
		all.length > limit
			? [...all.filter(isTrouble), ...all.filter((row) => !isTrouble(row))]
			: all;
	return {
		shown: ordered.slice(0, limit),
		more: Math.max(0, ordered.length - limit),
		total: ordered.length,
	};
}

/** The card is as wide as a stack's so names, commands and ports fit. */
export const PROCESS_NODE_WIDTH = 336;
const PROCESS_HEADER_HEIGHT = 52;
const PROCESS_ROW_HEIGHT = 28;
const PROCESS_FOOTER_HEIGHT = 44;

/**
 * The height the card takes: header, one row per shown process (at least one,
 * for the empty line), the "+N more" row when there is one and the footer.
 * The canvas layout uses it so arranging never overlaps the card.
 */
export function processCardSize(service: Partial<ProcessSource>): {
	w: number;
	h: number;
} {
	const summary = nodeProcessSummary(service);
	const rows = Math.max(1, summary.shown.length) + (summary.more > 0 ? 1 : 0);
	return {
		w: PROCESS_NODE_WIDTH,
		h:
			PROCESS_HEADER_HEIGHT + rows * PROCESS_ROW_HEIGHT + PROCESS_FOOTER_HEIGHT,
	};
}

/** "3 processes", "1 process". */
export function processCountText(count: number): string {
	return `${count} ${count === 1 ? "process" : "processes"}`;
}

// --- pickers ----------------------------------------------------------------

export interface ProcessOption {
	name: string;
	isDefault: boolean;
}

/**
 * The processes a log or metrics view can be narrowed to: the ones that exist
 * now, default first. A staged or removed process has no logs of its own to
 * pick (a removed one keeps its past lines under "All processes").
 */
export function processFilterOptions(
	service: Partial<ProcessSource>,
): ProcessOption[] {
	return serviceProcesses(service)
		.filter((process) => !process.removed && !process.pending)
		.map((process) => ({ name: process.name, isDefault: process.default }));
}

/** A stale name (renamed or removed since it was chosen) falls back to all processes. */
export function resolveProcessFilter(
	options: readonly ProcessOption[],
	value: string | undefined,
): string | undefined {
	if (value === undefined) return undefined;
	return options.some((option) => option.name === value) ? value : undefined;
}

export interface DomainProcessOption {
	name: string;
	port: number;
	isDefault: boolean;
}

/** The processes a domain can route to: the ones with a port. */
export function domainProcessOptions(
	service: Partial<ProcessSource>,
): DomainProcessOption[] {
	return serviceProcesses(service)
		.filter((process) => process.port > 0 && !process.removed)
		.map((process) => ({
			name: process.name,
			port: process.port,
			isDefault: process.default,
		}));
}

/**
 * The process a domain row names, as the picker shows it: the stored "" means
 * the default process, so the picker shows that process' own name.
 */
export function domainProcessName(
	service: Partial<ProcessSource>,
	stored: string,
): string {
	if (stored !== "") return stored;
	return (
		serviceProcesses(service).find((process) => process.default)?.name ?? ""
	);
}

/** What a domain stores for a picked process: "" for the default process. */
export function storedDomainProcess(
	service: Partial<ProcessSource>,
	picked: string,
): string {
	const option = serviceProcesses(service).find(
		(process) => process.name === picked,
	);
	return option?.default ? "" : picked;
}

/** "worker · replica 1": how a replica reads when the service has several processes. */
export function processReplicaLabel(process: string, index: number): string {
	return process === "" ? `Replica ${index}` : `${process} · replica ${index}`;
}
