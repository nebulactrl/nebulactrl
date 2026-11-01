import type { FieldChange } from "#/api/gen/types.gen";
import type { Service } from "#/api/models";
import { isInFlight } from "#/features/deployments/labels";
import {
	commandSummary,
	DEFAULT_PROCESS_NAME,
} from "#/features/processes/model";
import { quoteArg } from "#/features/service-settings/command-line";
import {
	COMPOSE_FIELD,
	type DiffLine,
	diffLines,
} from "#/features/stacks/diff";

/** How the control plane masks secret values in `pending-changes`. */
export const SECRET_MASK = "••••••";

/** A never-deployed service reports this single change. */
export const FIRST_DEPLOYMENT = "deployment";

/** A staged switch between a Docker image and a Git repository. */
const SOURCE_FIELD = "source";

export type DiffSign = "+" | "−" | "~";

/** One staged change as a diff line in the review sheet. */
export interface ReviewLine {
	id: string;
	/** The API field the line is about: `config.port`, `variables.KEY`. */
	field: string;
	sign: DiffSign;
	text: string;
	/** What a "label  value" line sets or deploys, shown apart from its label. */
	value?: string;
	/** A short note after the line: "secret", "not set". */
	note?: string;
	/** A staged text file (a stack's compose file) as a line diff, shown under the line. */
	diff?: DiffLine[];
}

/** The staged lines of one service. */
export interface ReviewGroup {
	serviceId: string;
	/** The service's name. */
	name: string;
	lines: ReviewLine[];
}

export interface ServiceChanges {
	service: Pick<Service, "id" | "name" | "slug">;
	changes: readonly FieldChange[];
}

/** The staged changes of one service: variables plus everything else. */
export function changesOf(
	service: Pick<Service, "pendingChangeCounts">,
): number {
	return (
		service.pendingChangeCounts.variables + service.pendingChangeCounts.settings
	);
}

/**
 * The services the control plane flags as staged although no change is left
 * (the flag can outlive the last discarded change). They count for nothing:
 * the Deploy key's N is the number of lines the review sheet lists.
 */
export function staleStaged<
	T extends Pick<Service, "hasStagedChanges" | "pendingChangeCounts">,
>(services: readonly T[]): T[] {
	return services.filter(
		(service) => service.hasStagedChanges && changesOf(service) === 0,
	);
}

const PROCESS_FIELD = "config.processes.";

/** What the control plane's `DiffConfig` calls the fields of a process, in words. */
const PROCESS_FIELD_LABEL: Readonly<Record<string, string>> = {
	startCommand: "Start command",
	port: "Port",
	stopGraceSeconds: "Stop grace period",
	singleton: "Singleton",
	memoryMb: "Memory (MB)",
	cpus: "CPUs",
	"healthcheck.type": "Health check type",
	"healthcheck.path": "Health check path",
	"healthcheck.port": "Health check port",
	"healthcheck.command": "Health check command",
	"healthcheck.intervalSeconds": "Health check interval",
	"healthcheck.timeoutSeconds": "Health check timeout",
	"healthcheck.startPeriodSeconds": "Health check start period",
	"healthcheck.deadlineSeconds": "Health check deadline",
	"healthcheck.successThreshold": "Health check success threshold",
};

/** A command the control plane sends as a JSON array, shown the way the settings field types it. */
function readable(value: string): string {
	if (!value.startsWith("[")) return value;
	try {
		const argv: unknown = JSON.parse(value);
		if (Array.isArray(argv) && argv.every((arg) => typeof arg === "string")) {
			return argv.map(quoteArg).join(" ");
		}
	} catch {
		// Not JSON after all: show it as it came.
	}
	return value;
}

/** What a whole process says about itself, for the line that adds or removes it. */
function processSummary(json: string): string | undefined {
	try {
		const process: unknown = JSON.parse(json);
		if (typeof process !== "object" || process === null) return undefined;
		const { startCommand, port } = process as {
			startCommand?: unknown;
			port?: unknown;
		};
		const parts: string[] = [];
		if (Array.isArray(startCommand) && startCommand.length > 0) {
			parts.push(commandSummary(startCommand.map(String)));
		}
		if (typeof port === "number" && port > 0) parts.push(`port ${port}`);
		return parts.length > 0 ? parts.join(" · ") : undefined;
	} catch {
		return undefined;
	}
}

/**
 * The review line of a process change: `config.processes.<name>` is a whole
 * process added or removed, `config.processes.<name>.<field>` one of its
 * settings, and the default process has two fields of its own. Null for any
 * other field.
 */
function processLine(
	change: FieldChange,
): Pick<ReviewLine, "sign" | "text" | "value"> | null {
	if (change.field === "config.processName") {
		return {
			sign: "~",
			text: "Default process name",
			value: `${change.from || DEFAULT_PROCESS_NAME} → ${change.to || DEFAULT_PROCESS_NAME}`,
		};
	}
	if (change.field === "config.singleton") {
		return {
			sign: "~",
			text: "Default process singleton",
			value: `${change.from || "false"} → ${change.to || "false"}`,
		};
	}
	if (!change.field.startsWith(PROCESS_FIELD)) return null;
	const path = change.field.slice(PROCESS_FIELD.length);
	const dot = path.indexOf(".");
	if (dot === -1) {
		const added = change.from === "";
		const summary = processSummary(added ? change.to : change.from);
		return {
			sign: added ? "+" : "−",
			text: `Process ${path} ${added ? "added" : "removed"}`,
			...(summary ? { value: summary } : {}),
		};
	}
	const name = path.slice(0, dot);
	const field = path.slice(dot + 1);
	const label = PROCESS_FIELD_LABEL[field] ?? field;
	const from = readable(change.from);
	const to = readable(change.to);
	return {
		sign: from === "" ? "+" : to === "" ? "−" : "~",
		text: `${label} of ${name}`,
		value: from === "" ? to : to === "" ? from : `${from} → ${to}`,
	};
}

function lineFor(serviceId: string, change: FieldChange): ReviewLine {
	const id = `${serviceId}:${change.field}`;
	if (change.field === FIRST_DEPLOYMENT) {
		return { id, field: change.field, sign: "+", text: "First deployment" };
	}
	const secret = change.from === SECRET_MASK || change.to === SECRET_MASK;
	const base = {
		id,
		field: change.field,
		...(secret ? { note: "secret" } : {}),
	};
	// The compose file travels as two full texts: show what changed, line by line.
	if (change.field === COMPOSE_FIELD) {
		return {
			...base,
			sign: change.from === "" ? "+" : change.to === "" ? "−" : "~",
			text: change.field,
			diff: diffLines(change.from, change.to),
		};
	}
	// Process settings read as sentences, not as `config.processes.<name>.<field>`.
	const process = processLine(change);
	if (process) return { ...base, ...process };
	// A source type switch is one change: the control plane words both sides
	// ("image", "Git (github.com/acme/shop, main, Railpack)"), so the line only
	// needs a plain label instead of the field id.
	if (change.field === SOURCE_FIELD) {
		return {
			...base,
			sign: "~",
			text: "Source",
			value: `${change.from} → ${change.to}`,
		};
	}
	// A restore swaps the Docker volume behind a mount: the control plane puts
	// the restore label after the mount path ("/data · restored from Jan 12,
	// 03:30"). That is one fact about the mount, not an arrow between two.
	if (
		change.field.startsWith("volumes.") &&
		change.from !== "" &&
		change.to.startsWith(`${change.from} · `)
	) {
		return { ...base, sign: "~", text: change.field, value: change.to };
	}
	if (change.from === "" && change.to !== "") {
		return { ...base, sign: "+", text: change.field, value: change.to };
	}
	if (change.to === "" && change.from !== "") {
		return { ...base, sign: "−", text: change.field, value: change.from };
	}
	return {
		...base,
		sign: "~",
		text: change.field,
		value: `${change.from} → ${change.to}`,
	};
}

/** The staged lines per service, in the shape the review sheet draws. */
export function reviewGroups(
	entries: readonly ServiceChanges[],
): ReviewGroup[] {
	return entries
		.filter((entry) => entry.changes.length > 0)
		.map((entry) => ({
			serviceId: entry.service.id,
			name: entry.service.name,
			lines: entry.changes.map((change) => lineFor(entry.service.id, change)),
		}));
}

/** The service's latest deployment has not reached a terminal status. */
export function hasInFlightDeployment(
	service: Pick<Service, "latestDeployment">,
): boolean {
	const status = service.latestDeployment?.status;
	return status !== undefined && isInFlight(status);
}
