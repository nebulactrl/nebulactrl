import type { ConfigInput } from "#/api/gen/types.gen";
import type { HealthcheckType, ProcessConfig } from "#/api/models";
import {
	quoteArg,
	splitCommand,
} from "#/features/service-settings/command-line";
import {
	DEFAULT_PROCESS_NAME,
	MAX_PROCESSES,
	PROCESS_NAME_PATTERN,
} from "./model";

// The settings form's working copy of the additional processes. Numbers are
// kept as the text the reader typed, like the rest of the settings draft; a
// blank stop grace, memory or CPU limit means "inherit the service's".

type ProcessInputWire = NonNullable<ConfigInput["processes"]>[number];

/** How many additional processes a service may have beside its default one. */
export const MAX_NAMED_PROCESSES = MAX_PROCESSES - 1;

export interface ProcessHealthDraft {
	type: HealthcheckType;
	/** argv of an exec health check as text; CMD or CMD-SHELL comes first. */
	command: string;
	path: string;
	port: string;
	intervalSeconds: string;
	timeoutSeconds: string;
	startPeriodSeconds: string;
	deadlineSeconds: string;
	successThreshold: string;
	/**
	 * The reader set the health check themselves. While false, the check is
	 * left to the control plane's default for the process (HTTP on its port,
	 * none without one) and the fields below only show that default.
	 */
	custom: boolean;
}

export interface ProcessDraft {
	/** Stable across renames, so the row keeps its identity (and focus) while the name is typed. */
	key: string;
	/** The name in the saved config; null for a row added in this session. */
	originalName: string | null;
	name: string;
	/** argv as text, quoted like the start command field. */
	command: string;
	port: string;
	/** Blank inherits the service's stop grace period. */
	stopGraceSeconds: string;
	singleton: boolean;
	/** Blank inherits the service's memory limit. */
	memoryMb: string;
	/** Blank inherits the service's CPU limit. */
	cpus: string;
	health: ProcessHealthDraft;
}

/** What the controls of a process row can be told is wrong. */
export type ProcessField =
	| "name"
	| "command"
	| "port"
	| "stopGraceSeconds"
	| "memoryMb"
	| "cpus"
	| "health";

export type ProcessErrors = Partial<Record<ProcessField, string>>;

const text = (value: number | undefined): string =>
	value === undefined || value === 0 ? "" : String(value);

/** The health timings the control plane fills in when they are left out. */
const DEFAULT_TIMINGS = {
	intervalSeconds: "2",
	timeoutSeconds: "3",
	startPeriodSeconds: "0",
	deadlineSeconds: "120",
	successThreshold: "2",
};

/** The check the control plane picks for a process that sets none: HTTP on its port, none without one. */
const defaultHealthType = (port: string): HealthcheckType =>
	port.trim() === "" ? "none" : "http";

function toHealthDraft(
	config: ProcessConfig["healthcheck"],
	processPort: number | undefined,
): ProcessHealthDraft {
	// The stored port is always filled in; one equal to the process port keeps
	// following it, so it is shown blank.
	const followsPort = config.port === undefined || config.port === processPort;
	const draft = {
		type: config.type,
		command: (config.command ?? []).map(quoteArg).join(" "),
		path: config.path ?? "",
		port: followsPort ? "" : text(config.port),
		intervalSeconds: String(config.intervalSeconds),
		timeoutSeconds: String(config.timeoutSeconds),
		startPeriodSeconds: String(config.startPeriodSeconds),
		deadlineSeconds: String(config.deadlineSeconds),
		successThreshold: String(config.successThreshold),
	};
	const isDefault =
		config.type === defaultHealthType(text(processPort)) &&
		draft.port === "" &&
		draft.command === "" &&
		draft.path === (config.type === "http" ? "/" : "") &&
		draft.intervalSeconds === DEFAULT_TIMINGS.intervalSeconds &&
		draft.timeoutSeconds === DEFAULT_TIMINGS.timeoutSeconds &&
		draft.startPeriodSeconds === DEFAULT_TIMINGS.startPeriodSeconds &&
		draft.deadlineSeconds === DEFAULT_TIMINGS.deadlineSeconds &&
		draft.successThreshold === DEFAULT_TIMINGS.successThreshold;
	return { ...draft, custom: !isDefault };
}

/**
 * The health check the form shows and validates: the stored fields, except
 * that a check nobody customized shows the default for the process as it is
 * now, so adding or clearing its port moves the check with it.
 */
export function effectiveHealth(draft: ProcessDraft): ProcessHealthDraft {
	if (draft.health.custom) return draft.health;
	return {
		...draft.health,
		type: defaultHealthType(draft.port),
		command: "",
		path: "",
		port: "",
		...DEFAULT_TIMINGS,
	};
}

export function toProcessDraft(process: ProcessConfig): ProcessDraft {
	return {
		key: process.name,
		originalName: process.name,
		name: process.name,
		command: process.startCommand.map(quoteArg).join(" "),
		port: text(process.port),
		stopGraceSeconds: text(process.stopGraceSeconds),
		singleton: process.singleton === true,
		memoryMb: text(process.memoryMb),
		cpus: text(process.cpus),
		health: toHealthDraft(process.healthcheck, process.port),
	};
}

export function toProcessDrafts(
	processes: readonly ProcessConfig[],
): ProcessDraft[] {
	return processes.map(toProcessDraft);
}

let added = 0;

/** A blank process row: no port yet, so no health check; one is defaulted to the port once it has one. */
export function newProcessDraft(name: string): ProcessDraft {
	added += 1;
	return {
		key: `new-${added}`,
		originalName: null,
		name,
		command: "",
		port: "",
		stopGraceSeconds: "",
		singleton: false,
		memoryMb: "",
		cpus: "",
		health: {
			type: "none",
			command: "",
			path: "",
			port: "",
			...DEFAULT_TIMINGS,
			custom: false,
		},
	};
}

/**
 * The name a new process is prefilled with: "worker", then "worker-2",
 * "worker-3", skipping names the service already uses (the default
 * process' included).
 */
export function nextProcessName(
	taken: readonly string[],
	base = "worker",
): string {
	const used = new Set(taken);
	if (!used.has(base)) return base;
	for (let n = 2; ; n++) {
		const candidate = `${base}-${n}`;
		if (!used.has(candidate)) return candidate;
	}
}

// --- validation -------------------------------------------------------------

function integerIn(
	value: string,
	min: number,
	max: number,
	label: string,
	optional: boolean,
): string | undefined {
	if (value.trim() === "")
		return optional ? undefined : `${label} is required.`;
	const parsed = Number(value);
	if (!Number.isInteger(parsed)) return `${label} must be a whole number.`;
	if (parsed < min || parsed > max)
		return `${label} must be between ${min} and ${max}.`;
	return undefined;
}

/** What is wrong with a process name on its own, or null. */
export function processNameError(name: string): string | null {
	if (name === "") return "Name is required.";
	if (!PROCESS_NAME_PATTERN.test(name)) {
		return "Use 1 to 31 lowercase letters, digits or hyphens, starting and ending with a letter or digit.";
	}
	return null;
}

/** What the rest of the draft tells validation about the process rows. */
export interface ProcessContext {
	/** The default process' name as typed; blank is "web". */
	defaultName: string;
	/** The default process' port as typed. */
	defaultPort: string;
	/** The service's domains: a saved process one routes to cannot lose its port. */
	domains?: readonly { hostname: string; process: string }[];
}

function validateHealth(draft: ProcessDraft): string | undefined {
	const health = effectiveHealth(draft);
	const checks = [
		integerIn(health.port, 1, 65535, "Health check port", true),
		integerIn(health.intervalSeconds, 1, 3600, "Interval", false),
		integerIn(health.timeoutSeconds, 1, 3600, "Timeout", false),
		integerIn(health.startPeriodSeconds, 0, 3600, "Start period", false),
		integerIn(health.deadlineSeconds, 1, 3600, "Deadline", false),
		integerIn(health.successThreshold, 1, 100, "Success threshold", false),
	];
	const numeric = checks.find((message) => message !== undefined);
	if (numeric) return numeric;
	if (health.type === "exec") {
		const argv = splitCommand(health.command);
		if (argv.length < 2 || (argv[0] !== "CMD" && argv[0] !== "CMD-SHELL")) {
			return 'Start with CMD or CMD-SHELL, then the command: CMD-SHELL "pg_isready -U app".';
		}
	}
	if (
		(health.type === "http" || health.type === "tcp") &&
		draft.port.trim() === "" &&
		health.port.trim() === ""
	) {
		return "This health check needs the process to have a port, or its own health check port.";
	}
	return undefined;
}

/**
 * What is wrong with each process row, by row key and field. Rows with no
 * problem are absent. Checks what the control plane checks, so a save that
 * passes here is not refused for these reasons: a valid unique name that is
 * not the default process' name, a start command, ports that are unique
 * across every process of the service, and the limits' ranges.
 */
export function processErrors(
	processes: readonly ProcessDraft[],
	context: ProcessContext,
): Record<string, ProcessErrors> {
	const result: Record<string, ProcessErrors> = {};
	const fail = (key: string, field: ProcessField, message: string) => {
		const row = result[key] ?? {};
		row[field] ??= message;
		result[key] = row;
	};
	const defaultName = context.defaultName.trim() || DEFAULT_PROCESS_NAME;
	const names = new Map<string, number>();
	for (const process of processes) {
		names.set(process.name, (names.get(process.name) ?? 0) + 1);
	}
	const ports = new Map<string, string[]>();
	const claimPort = (port: string, owner: string) => {
		const value = port.trim();
		if (value === "") return;
		ports.set(value, [...(ports.get(value) ?? []), owner]);
	};
	claimPort(context.defaultPort, defaultName);
	for (const process of processes) claimPort(process.port, process.name);

	const renamedFrom = new Set(
		processes
			.filter((p) => p.originalName !== null && p.originalName !== p.name)
			.map((p) => p.originalName as string),
	);
	for (const process of processes) {
		const { key } = process;
		const nameProblem = processNameError(process.name);
		if (nameProblem) fail(key, "name", nameProblem);
		else if (process.name === defaultName) {
			fail(
				key,
				"name",
				`${defaultName} is the name of the default process. Pick another name.`,
			);
		} else if ((names.get(process.name) ?? 0) > 1) {
			fail(key, "name", `Another process is already called ${process.name}.`);
		} else if (
			process.name !== process.originalName &&
			renamedFrom.has(process.name)
		) {
			fail(
				key,
				"name",
				`${process.name} is being renamed in this save. Save that rename first, then use the name.`,
			);
		}
		if (splitCommand(process.command).length === 0) {
			fail(
				key,
				"command",
				"A start command is required: a process has no image command to fall back on.",
			);
		}
		const portProblem = integerIn(process.port, 1, 65535, "Port", true);
		if (portProblem) fail(key, "port", portProblem);
		else if (process.port.trim() !== "") {
			const owners = ports.get(process.port.trim()) ?? [];
			if (owners.length > 1) {
				const other =
					owners.find((owner) => owner !== process.name) ?? "another process";
				fail(
					key,
					"port",
					`Port ${process.port.trim()} is also used by ${other}. Give each process its own port.`,
				);
			}
		} else if (process.originalName !== null) {
			const routed = (context.domains ?? [])
				.filter((domain) => domain.process === process.originalName)
				.map((domain) => domain.hostname);
			if (routed.length > 0) {
				const many = routed.length > 1;
				fail(
					key,
					"port",
					`${routed.join(", ")} ${many ? "route" : "routes"} to this process; remove the ${many ? "domains" : "domain"} first.`,
				);
			}
		}
		const grace = integerIn(
			process.stopGraceSeconds,
			1,
			3600,
			"Stop grace period",
			true,
		);
		if (grace) fail(key, "stopGraceSeconds", grace);
		const memory = integerIn(process.memoryMb, 6, 1_048_576, "Memory", true);
		if (memory) fail(key, "memoryMb", memory);
		if (process.cpus.trim() !== "") {
			const cpus = Number(process.cpus);
			if (!Number.isFinite(cpus) || cpus <= 0 || cpus > 256) {
				fail(key, "cpus", "CPUs must be a number between 0.01 and 256.");
			}
		}
		const health = validateHealth(process);
		if (health) fail(key, "health", health);
	}
	return result;
}

/** The first thing that stops the processes from being saved, or null. */
export function processBlocker(
	processes: readonly ProcessDraft[],
	context: ProcessContext,
): string | null {
	if (processes.length > MAX_NAMED_PROCESSES) {
		return `A service has at most ${MAX_PROCESSES} processes, the default one included. Remove a process to add another.`;
	}
	const errors = processErrors(processes, context);
	for (const process of processes) {
		const row = errors[process.key];
		if (!row) continue;
		const message =
			row.name ??
			row.command ??
			row.port ??
			row.stopGraceSeconds ??
			row.memoryMb ??
			row.cpus ??
			row.health;
		if (message) {
			return `${process.name || "A process"}: ${message}`;
		}
	}
	return null;
}

// --- building the PATCH -----------------------------------------------------

const int = (value: string): number => Number.parseInt(value, 10);

/**
 * The request shape of one process. A health check the reader never set is
 * left out, so the control plane defaults it to the process: HTTP on its
 * port, none without one.
 */
export function buildProcessInput(draft: ProcessDraft): ProcessInputWire {
	const input: ProcessInputWire = {
		name: draft.name,
		startCommand: splitCommand(draft.command),
	};
	if (draft.health.custom) {
		const { health: source } = draft;
		const health: NonNullable<ProcessInputWire["healthcheck"]> = {
			type: source.type,
			intervalSeconds: int(source.intervalSeconds),
			timeoutSeconds: int(source.timeoutSeconds),
			startPeriodSeconds: int(source.startPeriodSeconds),
			deadlineSeconds: int(source.deadlineSeconds),
			successThreshold: int(source.successThreshold),
		};
		if (source.path.trim() !== "") health.path = source.path.trim();
		if (source.port.trim() !== "") health.port = int(source.port);
		if (source.type === "exec") health.command = splitCommand(source.command);
		input.healthcheck = health;
	}
	if (draft.port.trim() !== "") input.port = int(draft.port);
	if (draft.stopGraceSeconds.trim() !== "")
		input.stopGraceSeconds = int(draft.stopGraceSeconds);
	if (draft.singleton) input.singleton = true;
	if (draft.memoryMb.trim() !== "") input.memoryMb = int(draft.memoryMb);
	if (draft.cpus.trim() !== "") input.cpus = Number(draft.cpus);
	return input;
}

/** The `processes` array the PATCH replaces the list with; undefined when the service has none. */
export function buildProcessesInput(
	drafts: readonly ProcessDraft[],
): ProcessInputWire[] | undefined {
	if (drafts.length === 0) return undefined;
	return drafts.map(buildProcessInput);
}

/**
 * Old name to new name for every saved process the draft renamed, so the
 * control plane moves its scale and domains instead of treating the rename as
 * a remove plus an add. Undefined when nothing was renamed.
 */
export function renameProcesses(
	drafts: readonly ProcessDraft[],
): Record<string, string> | undefined {
	const renames: Record<string, string> = {};
	for (const draft of drafts) {
		if (draft.originalName === null || draft.originalName === draft.name)
			continue;
		renames[draft.originalName] = draft.name;
	}
	return Object.keys(renames).length === 0 ? undefined : renames;
}
